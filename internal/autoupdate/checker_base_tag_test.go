package autoupdate

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// =============================================================================
// Helpers
// =============================================================================

// ghRef builds one entry of a GitHub /git/refs/tags listing. objType is
// "commit" for a lightweight tag and "tag" for an annotated one, whose object
// SHA is the tag object's rather than the commit's.
func ghRef(name, sha, objType string) map[string]any {
	return map[string]any{
		"ref":    "refs/tags/" + name,
		"object": map[string]any{"sha": sha, "type": objType},
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// newTagChecker serves the commit list at /commits and the tag listing at
// /tags, wiring cfg.URL and cfg.BaseURL to them. pinnedSHA, when non-empty, is
// written into the current ebuild as EGIT_COMMIT.
func newTagChecker(t *testing.T, pkg, currentVersion string, cfg PackageConfig,
	commits, tags []byte, pinnedSHA string) *Checker {
	t.Helper()
	overlayDir := t.TempDir()
	configDir := t.TempDir()

	mux := http.NewServeMux()
	mux.HandleFunc("/commits", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(commits)
	})
	mux.HandleFunc("/tags", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(tags)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	cfg.URL = server.URL + "/commits"
	cfg.BaseURL = server.URL + "/tags"

	checker, err := NewChecker(overlayDir,
		WithConfigDir(configDir),
		WithPackagesConfig(&PackagesConfig{Packages: map[string]PackageConfig{pkg: cfg}}),
		WithRateLimiter(unlimitedRateLimiter()),
	)
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}

	createTestEbuild(t, overlayDir, pkg, currentVersion)

	if pinnedSHA != "" {
		dir := filepath.Join(overlayDir, filepath.FromSlash(pkg))
		name := pkg[strings.IndexByte(pkg, '/')+1:]
		path := filepath.Join(dir, fmt.Sprintf("%s-%s.ebuild", name, currentVersion))
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read test ebuild: %v", err)
		}
		body = append(body, []byte("\nEGIT_COMMIT=\""+pinnedSHA+"\"\n")...)
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatalf("write test ebuild: %v", err)
		}
	}
	return checker
}

func tagCfg() PackageConfig {
	c := baseCommitCfg()
	c.BaseFrom = "tag"
	c.BaseTagPattern = `^v([0-9]+\.[0-9]+\.[0-9]+)$`
	return c
}

// =============================================================================
// base_from = "tag"
// =============================================================================

func TestBaseFromTag_HighestOfFamily(t *testing.T) {
	// These repos carry several tag families at once. Only the v* family is the
	// release scheme the ebuild uses; "khronos-master-20141209" contains a much
	// larger number and would win any unfiltered ranking.
	pkg := "media-libs/vulkan-loader"

	tags := mustJSON(t, []any{
		ghRef("v1.4.356", "aaaa000000000000000000000000000000000001", "commit"),
		ghRef("v1.4.358", "aaaa000000000000000000000000000000000002", "commit"),
		ghRef("v1.4.357", "aaaa000000000000000000000000000000000003", "commit"),
		ghRef("khronos-master-20141209", "aaaa000000000000000000000000000000000004", "commit"),
		ghRef("vulkan-sdk-1.4.357.0", "aaaa000000000000000000000000000000000005", "commit"),
		ghRef("windows-rt-1.0.8.1", "aaaa000000000000000000000000000000000006", "commit"),
	})
	commits := makeGHCommits(
		struct{ sha, date, msg string }{testSHA40, "2026-07-31T10:00:00Z", "build(deps): bump actions/checkout"},
	)

	checker := newTagChecker(t, pkg, "1.4.354_p20260727", tagCfg(), commits, tags, "")
	result, err := checker.CheckPackage(pkg, true)
	if err != nil {
		t.Fatalf("CheckPackage: %v", err)
	}

	want := "1.4.358_p20260731"
	if result.UpstreamVersion != want {
		t.Errorf("UpstreamVersion = %q, want %q", result.UpstreamVersion, want)
	}
}

func TestBaseFromTag_ExactTagYieldsBareVersion(t *testing.T) {
	// vulkan-headers pinned 11d6898, which IS tag v1.4.358, and shipped as
	// 1.4.358_p20260731 — a name claiming to be newer than the release it was.
	pkg := "dev-util/vulkan-headers"

	tags := mustJSON(t, []any{
		ghRef("v1.4.357", "bbbb000000000000000000000000000000000001", "commit"),
		ghRef("v1.4.358", testSHA40, "commit"),
	})
	commits := makeGHCommits(
		struct{ sha, date, msg string }{testSHA40, "2026-07-31T00:15:17Z", "Update for Vulkan-Docs 1.4.358"},
	)

	checker := newTagChecker(t, pkg, "1.4.357_p20260722", tagCfg(), commits, tags, "")
	result, err := checker.CheckPackage(pkg, true)
	if err != nil {
		t.Fatalf("CheckPackage: %v", err)
	}

	if want := "1.4.358"; result.UpstreamVersion != want {
		t.Errorf("UpstreamVersion = %q, want %q (bare release, not a snapshot)", result.UpstreamVersion, want)
	}
	if !result.HasUpdate {
		t.Error("HasUpdate = false, want true")
	}
}

func TestBaseFromTag_ExactButOlderTagStaysSnapshot(t *testing.T) {
	// The head carries v1.4.357 while v1.4.358 already exists: the package is
	// NOT the newest release, so it must stay a snapshot.
	pkg := "dev-util/vulkan-headers"

	tags := mustJSON(t, []any{
		ghRef("v1.4.357", testSHA40, "commit"),
		ghRef("v1.4.358", "cccc000000000000000000000000000000000001", "commit"),
	})
	commits := makeGHCommits(
		struct{ sha, date, msg string }{testSHA40, "2026-07-31T00:15:17Z", "Update for Vulkan-Docs 1.4.357"},
	)

	checker := newTagChecker(t, pkg, "1.4.356_p20260722", tagCfg(), commits, tags, "")
	result, err := checker.CheckPackage(pkg, true)
	if err != nil {
		t.Fatalf("CheckPackage: %v", err)
	}

	if want := "1.4.358_p20260731"; result.UpstreamVersion != want {
		t.Errorf("UpstreamVersion = %q, want %q", result.UpstreamVersion, want)
	}
}

func TestBaseFromTag_PreSuffixNeverCollapses(t *testing.T) {
	// A _pre package's version-bump commit OPENS the cycle: zed's
	// "Bump Zed to v1.15.0" precedes the release by weeks, so even when the
	// tracked commit carries the tag the snapshot form stays correct.
	pkg := "app-editors/zed"

	tags := mustJSON(t, []any{ghRef("v1.15.0", testSHA40, "commit")})
	commits := makeGHCommits(
		struct{ sha, date, msg string }{testSHA40, "2026-07-31T10:00:00Z", "Bump Zed to v1.15.0"},
	)

	checker := newTagChecker(t, pkg, "1.14.0_pre20260729", tagCfg(), commits, tags, "")
	result, err := checker.CheckPackage(pkg, true)
	if err != nil {
		t.Fatalf("CheckPackage: %v", err)
	}

	if want := "1.15.0_pre20260731"; result.UpstreamVersion != want {
		t.Errorf("UpstreamVersion = %q, want %q (_pre must not collapse)", result.UpstreamVersion, want)
	}
}

func TestBaseFromTag_AnnotatedTagIsNotAnExactMatch(t *testing.T) {
	// An annotated tag's object SHA is the tag object's, not the commit's.
	// Comparing it to the head SHA would be comparing the wrong thing; the safe
	// answer is "not exact", i.e. keep the snapshot form.
	pkg := "dev-util/vulkan-headers"

	tags := mustJSON(t, []any{ghRef("v1.4.358", testSHA40, "tag")})
	commits := makeGHCommits(
		struct{ sha, date, msg string }{testSHA40, "2026-07-31T00:15:17Z", "Update for Vulkan-Docs 1.4.358"},
	)

	checker := newTagChecker(t, pkg, "1.4.357_p20260722", tagCfg(), commits, tags, "")
	result, err := checker.CheckPackage(pkg, true)
	if err != nil {
		t.Fatalf("CheckPackage: %v", err)
	}

	if want := "1.4.358_p20260731"; result.UpstreamVersion != want {
		t.Errorf("UpstreamVersion = %q, want %q", result.UpstreamVersion, want)
	}
}

func TestBaseFromTag_GitLabListing(t *testing.T) {
	// The GitLab repository/tags shape must parse too.
	pkg := "net-libs/libqmi"

	tags := mustJSON(t, []any{
		map[string]any{"name": "v1.39.1", "commit": map[string]any{"id": "dddd000000000000000000000000000000000001"}},
		map[string]any{"name": "v1.38.0", "commit": map[string]any{"id": "dddd000000000000000000000000000000000002"}},
	})
	commits := makeGHCommits(
		struct{ sha, date, msg string }{testSHA40, "2026-07-28T10:00:00Z", "unrelated"},
	)

	checker := newTagChecker(t, pkg, "1.38.1_pre20260720", tagCfg(), commits, tags, "")
	result, err := checker.CheckPackage(pkg, true)
	if err != nil {
		t.Fatalf("CheckPackage: %v", err)
	}

	if want := "1.39.1_pre20260728"; result.UpstreamVersion != want {
		t.Errorf("UpstreamVersion = %q, want %q", result.UpstreamVersion, want)
	}
}

func TestBaseFromTag_NoMatchingTagIsFatal(t *testing.T) {
	pkg := "dev-util/spirv-tools"

	tags := mustJSON(t, []any{ghRef("v2026.3", "eeee000000000000000000000000000000000001", "commit")})
	commits := makeGHCommits(
		struct{ sha, date, msg string }{testSHA40, "2026-07-30T10:00:00Z", "unrelated"},
	)

	cfg := tagCfg()
	cfg.BaseTagPattern = `^vulkan-sdk-([0-9.]+)$`

	checker := newTagChecker(t, pkg, "1.4.350.0_p20260730", cfg, commits, tags, "")
	_, err := checker.CheckPackage(pkg, true)
	if !errors.Is(err, ErrBaseVersionUnresolved) {
		t.Fatalf("error = %v, want ErrBaseVersionUnresolved", err)
	}
}

// =============================================================================
// The SHA guard: a bare release version must not re-bump forever
// =============================================================================

func TestCommitTrack_SameSHAIsNoUpdate(t *testing.T) {
	// The overlay holds the bare 1.4.358 (built from the exact tag). Tomorrow's
	// check builds 1.4.358_p<today>, which compares NEWER — without the SHA
	// guard the same commit would be re-bumped every single day.
	pkg := "dev-util/vulkan-headers"

	tags := mustJSON(t, []any{ghRef("v1.4.358", "ffff000000000000000000000000000000000001", "commit")})
	commits := makeGHCommits(
		struct{ sha, date, msg string }{testSHA40, "2026-08-05T10:00:00Z", "docs: unrelated"},
	)

	checker := newTagChecker(t, pkg, "1.4.358", tagCfg(), commits, tags, testSHA40)
	result, err := checker.CheckPackage(pkg, true)
	if err != nil {
		t.Fatalf("CheckPackage: %v", err)
	}

	if result.HasUpdate {
		t.Errorf("HasUpdate = true for an ebuild already pinned to %s (version would be %q)",
			testSHA40[:7], result.UpstreamVersion)
	}
}

func TestCommitTrack_DifferentSHAStillUpdates(t *testing.T) {
	// The guard must only suppress the identical commit, never a real move.
	pkg := "dev-util/vulkan-headers"

	tags := mustJSON(t, []any{ghRef("v1.4.358", "ffff000000000000000000000000000000000001", "commit")})
	commits := makeGHCommits(
		struct{ sha, date, msg string }{testSHA40, "2026-08-05T10:00:00Z", "docs: unrelated"},
	)

	checker := newTagChecker(t, pkg, "1.4.358", tagCfg(), commits, tags, testSHA40b)
	result, err := checker.CheckPackage(pkg, true)
	if err != nil {
		t.Fatalf("CheckPackage: %v", err)
	}

	if !result.HasUpdate {
		t.Error("HasUpdate = false; a different upstream commit must still bump")
	}
	if want := "1.4.358_p20260805"; result.UpstreamVersion != want {
		t.Errorf("UpstreamVersion = %q, want %q", result.UpstreamVersion, want)
	}
}

func TestCommitTrack_SameSHAButBaseCorrectionStillUpdates(t *testing.T) {
	// The guard must not outrank a base correction. vulkan-tools sat at 1.4.354
	// while upstream said 1.4.357, with the pinned commit already current: a
	// SHA-only guard would have frozen the wrong version permanently.
	pkg := "dev-util/vulkan-tools"

	tags := mustJSON(t, []any{
		ghRef("v1.4.354", "1111000000000000000000000000000000000001", "commit"),
		ghRef("v1.4.357", "1111000000000000000000000000000000000002", "commit"),
	})
	commits := makeGHCommits(
		struct{ sha, date, msg string }{testSHA40, "2026-07-31T10:00:00Z", "build(deps): bump actions/checkout"},
	)

	// Ebuild pinned to the SAME commit the check resolves, but at the old base.
	checker := newTagChecker(t, pkg, "1.4.354_p20260731", tagCfg(), commits, tags, testSHA40)
	result, err := checker.CheckPackage(pkg, true)
	if err != nil {
		t.Fatalf("CheckPackage: %v", err)
	}

	if !result.HasUpdate {
		t.Error("HasUpdate = false; a base correction must bump even when the SHA is unchanged")
	}
	if want := "1.4.357_p20260731"; result.UpstreamVersion != want {
		t.Errorf("UpstreamVersion = %q, want %q", result.UpstreamVersion, want)
	}
}

func TestCurrentEbuildCommit(t *testing.T) {
	overlay := t.TempDir()
	pkg := "dev-util/demo"
	dir := filepath.Join(overlay, "dev-util", "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	tests := []struct {
		name, body, want string
	}{
		{"EGIT_COMMIT", "EAPI=8\nSLOT=\"0\"\nEGIT_COMMIT=\"" + testSHA40 + "\"\n", testSHA40},
		{"GIT_COMMIT", "EAPI=8\nSLOT=\"0\"\n\tGIT_COMMIT=\"" + testSHA40 + "\"\n", testSHA40},
		{"bare COMMIT", "EAPI=8\nSLOT=\"0\"\nCOMMIT=" + testSHA40 + "\n", testSHA40},
		{"none", "EAPI=8\nSLOT=\"0\"\nKEYWORDS=\"amd64\"\n", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(dir, "demo-1.0.ebuild")
			if err := os.WriteFile(path, []byte(tt.body), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			if got := currentEbuildCommit(overlay, pkg, ""); got != tt.want {
				t.Errorf("currentEbuildCommit = %q, want %q", got, tt.want)
			}
		})
	}

	t.Run("missing package", func(t *testing.T) {
		if got := currentEbuildCommit(overlay, "dev-util/absent", ""); got != "" {
			t.Errorf("currentEbuildCommit = %q, want \"\" for an absent package", got)
		}
	})
}
