package autoupdate

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// =============================================================================
// Helpers
// =============================================================================

// newBaseFileChecker builds a Checker whose upstream serves BOTH endpoints a
// base_from="file" entry needs: the commit list at /commits and the version
// file at /version. cfg.URL and cfg.BaseURL are rewritten to point at them.
func newBaseFileChecker(t *testing.T, pkg, currentVersion string, cfg PackageConfig,
	commits []byte, versionFile string) *Checker {
	t.Helper()
	overlayDir := t.TempDir()
	configDir := t.TempDir()

	mux := http.NewServeMux()
	mux.HandleFunc("/commits", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(commits)
	})
	mux.HandleFunc("/version", func(w http.ResponseWriter, _ *http.Request) {
		if versionFile == "" {
			http.NotFound(w, &http.Request{})
			return
		}
		_, _ = w.Write([]byte(versionFile))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	cfg.URL = server.URL + "/commits"
	if cfg.BaseFrom == "file" {
		cfg.BaseURL = server.URL + "/version"
	}

	checker, err := NewChecker(overlayDir,
		WithConfigDir(configDir),
		WithPackagesConfig(&PackagesConfig{Packages: map[string]PackageConfig{pkg: cfg}}),
		WithRateLimiter(unlimitedRateLimiter()),
	)
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}

	createTestEbuild(t, overlayDir, pkg, currentVersion)
	return checker
}

// mesaVersionFile is mesa's VERSION file: a bare development version, which is
// why the pattern has to strip the "-devel" suffix rather than post-process it.
const mesaVersionFile = "26.3.0-devel\n"

// zedCargoToml is the shape of zed's crates/zed/Cargo.toml. The version is NOT
// on the first line, which is exactly why base_pattern needs (?m).
const zedCargoToml = `[package]
name = "zed"
version = "1.15.0"
publish = false
edition = "2021"
`

// =============================================================================
// base_from = "none"
// =============================================================================

// TestBaseFromNone_KeepsTheEbuildBase covers the upstream that does not version
// itself at all: only the snapshot suffix moves, and the PV base is the constant
// the maintainer chose.
//
// The two registry entries this exists for are sci-ml/ik_llama-cpp (one tag,
// "t0002", a prerelease roughly a year behind an active HEAD) and
// sys-apps/asus-ec-sensors (one stale tag, v0.1.0, with board support landing as
// plain commits). Neither has a version in-tree, in a tag, or in a commit title
// — verified against both upstreams on 2026-08-04 — so there is no source for
// base_from to name and the base cannot freeze, because nothing is moving for it
// to fall behind.
//
// Before "none" existed these two were the ONLY records the legacy-base rule
// reported across all 411, and both were false positives: the rule could not
// tell "nobody declared the source" from "there is no source to declare".
func TestBaseFromNone_KeepsTheEbuildBase(t *testing.T) {
	pkg := "sci-ml/ik_llama-cpp"

	cfg := baseCommitCfg()
	cfg.BaseFrom = "none"
	// The three source fields validation forbids alongside "none" are absent by
	// construction here; baseCommitCfg sets none of them.
	cfg.CommitVersionPattern = ""
	cfg.CommitMessagePath = ""

	body := makeGHCommits(
		struct{ sha, date, msg string }{testSHA40, "2026-08-04T09:00:00Z", "iqk: tighten the gemm kernel"},
	)

	checker := newBaseFileChecker(t, pkg, "0_pre20260718", cfg, body, "")
	result, err := checker.CheckPackage(pkg, true)
	if err != nil {
		t.Fatalf("CheckPackage: %v", err)
	}

	// The base stays "0" and only the date advances. A resolved base would have
	// replaced it — that is the difference this case exists to pin.
	want := "0_pre20260804"
	if result.UpstreamVersion != want {
		t.Errorf("UpstreamVersion = %q, want %q", result.UpstreamVersion, want)
	}
	if !result.HasUpdate {
		t.Error("HasUpdate = false, want true: the snapshot date moved")
	}
}

// "none" must not fetch a base source. A base_url is rejected at validation, so
// the only way to prove nothing is fetched is to watch the server: the commits
// endpoint is hit, the version endpoint never is.
func TestBaseFromNone_FetchesNoBaseSource(t *testing.T) {
	pkg := "sys-apps/asus-ec-sensors"

	var versionHits int
	mux := http.NewServeMux()
	mux.HandleFunc("/commits", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(makeGHCommits(
			struct{ sha, date, msg string }{testSHA40, "2026-08-04T09:00:00Z", "add board support"},
		))
	})
	mux.HandleFunc("/version", func(w http.ResponseWriter, _ *http.Request) {
		versionHits++
		_, _ = w.Write([]byte("1.2.3"))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	cfg := baseCommitCfg()
	cfg.BaseFrom = "none"
	cfg.CommitVersionPattern = ""
	cfg.CommitMessagePath = ""
	cfg.URL = server.URL + "/commits"

	overlayDir, configDir := t.TempDir(), t.TempDir()
	checker, err := NewChecker(overlayDir,
		WithConfigDir(configDir),
		WithPackagesConfig(&PackagesConfig{Packages: map[string]PackageConfig{pkg: cfg}}),
		WithRateLimiter(unlimitedRateLimiter()),
	)
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}
	createTestEbuild(t, overlayDir, pkg, "0_p20260711")

	if _, err := checker.CheckPackage(pkg, true); err != nil {
		t.Fatalf("CheckPackage: %v", err)
	}
	if versionHits != 0 {
		t.Errorf("base source was fetched %d time(s); \"none\" must resolve nothing", versionHits)
	}
}

// A valid "none" record loads: track = "commit", no source field of any kind.
func TestBaseFromNone_ValidatesAlone(t *testing.T) {
	cfg := baseCommitCfg()
	cfg.BaseFrom = "none"
	cfg.CommitVersionPattern = ""
	cfg.CommitMessagePath = ""
	cfg.URL = "https://example.invalid/commits"

	if err := ValidatePackageConfig("sci-ml/ik_llama-cpp", &cfg); err != nil {
		t.Errorf("a bare base_from=\"none\" must validate, got: %v", err)
	}
}

// =============================================================================
// base_from = "file"
// =============================================================================

func TestBaseFromFile_RaisesBase(t *testing.T) {
	// mesa's ebuild sits at 26.2.0_pre20260730 while upstream main already
	// declares 26.3.0-devel. Measured on 2026-07-31: the real overlay was a full
	// series behind because nothing read that file.
	pkg := "media-libs/mesa"

	cfg := baseCommitCfg()
	cfg.BaseFrom = "file"
	cfg.BasePattern = `^([0-9][0-9.]*)-devel`

	body := makeGHCommits(
		struct{ sha, date, msg string }{testSHA40, "2026-07-31T10:00:00Z", "nir: unrelated fix"},
	)

	checker := newBaseFileChecker(t, pkg, "26.2.0_pre20260730", cfg, body, mesaVersionFile)
	result, err := checker.CheckPackage(pkg, true)
	if err != nil {
		t.Fatalf("CheckPackage: %v", err)
	}

	want := "26.3.0_pre20260731"
	if result.UpstreamVersion != want {
		t.Errorf("UpstreamVersion = %q, want %q", result.UpstreamVersion, want)
	}
	if !result.HasUpdate {
		t.Error("HasUpdate = false, want true")
	}
}

func TestBaseFromFile_MultilineFile(t *testing.T) {
	// The version lives mid-file, so the pattern needs (?m). Without it Go's ^
	// anchors to the start of the whole body and matches nothing — a mistake
	// that would otherwise surface only in production.
	pkg := "app-editors/zed"

	cfg := baseCommitCfg()
	cfg.BaseFrom = "file"
	cfg.BasePattern = `(?m)^version = "([0-9][0-9.]*)"`

	body := makeGHCommits(
		struct{ sha, date, msg string }{testSHA40, "2026-07-31T19:57:13Z", "editor: unrelated"},
	)

	checker := newBaseFileChecker(t, pkg, "1.14.0_pre20260729", cfg, body, zedCargoToml)
	result, err := checker.CheckPackage(pkg, true)
	if err != nil {
		t.Fatalf("CheckPackage: %v", err)
	}

	// The whole point: zed's "Bump Zed to v1.15.0" had already fallen out of the
	// 50-commit window, so only the file can still see 1.15.0.
	want := "1.15.0_pre20260731"
	if result.UpstreamVersion != want {
		t.Errorf("UpstreamVersion = %q, want %q", result.UpstreamVersion, want)
	}
}

func TestBaseFromFile_NeverDowngrades(t *testing.T) {
	// A base older than the ebuild's is ignored: a reverted bump upstream must
	// not walk the overlay backwards.
	pkg := "media-libs/mesa"

	cfg := baseCommitCfg()
	cfg.BaseFrom = "file"
	cfg.BasePattern = `^([0-9][0-9.]*)-devel`

	body := makeGHCommits(
		struct{ sha, date, msg string }{testSHA40, "2026-07-31T10:00:00Z", "revert"},
	)

	checker := newBaseFileChecker(t, pkg, "26.4.0_pre20260730", cfg, body, mesaVersionFile)
	result, err := checker.CheckPackage(pkg, true)
	if err != nil {
		t.Fatalf("CheckPackage: %v", err)
	}

	want := "26.4.0_pre20260731" // base kept, only the date moved
	if result.UpstreamVersion != want {
		t.Errorf("UpstreamVersion = %q, want %q", result.UpstreamVersion, want)
	}
}

func TestBaseFromFile_PatternMatchesNothing_IsFatal(t *testing.T) {
	// The file moved or upstream restructured it. This must fail loudly rather
	// than fall back to the ebuild's base — the silent fallback is what let six
	// Khronos packages drift up to seven releases behind.
	pkg := "media-libs/mesa"

	cfg := baseCommitCfg()
	cfg.BaseFrom = "file"
	cfg.BasePattern = `^VERSION = ([0-9.]+)$`

	body := makeGHCommits(
		struct{ sha, date, msg string }{testSHA40, "2026-07-31T10:00:00Z", "unrelated"},
	)

	checker := newBaseFileChecker(t, pkg, "26.2.0_pre20260730", cfg, body, mesaVersionFile)
	result, err := checker.CheckPackage(pkg, true)
	if err == nil {
		t.Fatal("CheckPackage: expected an error, got nil")
	}
	if !errors.Is(err, ErrBaseVersionUnresolved) {
		t.Errorf("error = %v, want ErrBaseVersionUnresolved", err)
	}
	if result.HasUpdate {
		t.Error("HasUpdate = true; an unresolved base must not queue a bump")
	}
}

func TestBaseFromFile_CapturesNonVersion_IsFatal(t *testing.T) {
	// A pattern that matches but captures junk is worse than one that misses:
	// it would build an ebuild filename out of it.
	pkg := "media-libs/mesa"

	cfg := baseCommitCfg()
	cfg.BaseFrom = "file"
	cfg.BasePattern = `-([a-z]+)` // captures "devel" out of "26.3.0-devel"

	body := makeGHCommits(
		struct{ sha, date, msg string }{testSHA40, "2026-07-31T10:00:00Z", "unrelated"},
	)

	checker := newBaseFileChecker(t, pkg, "26.2.0_pre20260730", cfg, body, mesaVersionFile)
	_, err := checker.CheckPackage(pkg, true)
	if !errors.Is(err, ErrBaseVersionUnresolved) {
		t.Fatalf("error = %v, want ErrBaseVersionUnresolved", err)
	}
	if !strings.Contains(err.Error(), "not a valid Gentoo version") {
		t.Errorf("error %q should say what was captured and why it was rejected", err)
	}
}

func TestBaseFromFile_FetchFails_IsFatal(t *testing.T) {
	pkg := "media-libs/mesa"

	cfg := baseCommitCfg()
	cfg.BaseFrom = "file"
	cfg.BasePattern = `^([0-9][0-9.]*)-devel`

	body := makeGHCommits(
		struct{ sha, date, msg string }{testSHA40, "2026-07-31T10:00:00Z", "unrelated"},
	)

	// Empty version file → the handler 404s.
	checker := newBaseFileChecker(t, pkg, "26.2.0_pre20260730", cfg, body, "")
	_, err := checker.CheckPackage(pkg, true)
	if err == nil {
		t.Fatal("CheckPackage: expected an error when the version file is unreachable")
	}
}

// =============================================================================
// F1: a declared commit_message source that resolves nothing is fatal
// =============================================================================

func TestCommitVersionPattern_MatchesNothing_IsFatal(t *testing.T) {
	// This is the six-of-seven case: the pattern was copied from Vulkan-Headers
	// to sibling Khronos repos that never write it in their commit titles. It
	// used to fall through to the ebuild's base in silence.
	pkg := "media-libs/vulkan-loader"

	cfg := baseCommitCfg()
	cfg.CommitMessagePath = "commit.message"
	cfg.CommitVersionPattern = `Update for Vulkan-Docs ([0-9]+\.[0-9]+\.[0-9]+)`

	body := makeGHCommits(
		struct{ sha, date, msg string }{testSHA40, "2026-07-31T10:00:00Z", "build(deps): bump actions/checkout from 6 to 7"},
	)

	checker := newBaseFileChecker(t, pkg, "1.4.354_p20260730", cfg, body, "")
	result, err := checker.CheckPackage(pkg, true)
	if err == nil {
		t.Fatal("CheckPackage: expected an error, got nil")
	}
	if !errors.Is(err, ErrBaseVersionUnresolved) {
		t.Errorf("error = %v, want ErrBaseVersionUnresolved", err)
	}
	if result.HasUpdate {
		t.Error("HasUpdate = true; a dead pattern must not queue a date-only bump")
	}
	// The message must point at the fix, not just report failure.
	if !strings.Contains(err.Error(), "per_page") {
		t.Errorf("error %q should mention the fetch window as a likely cause", err)
	}
}

func TestCommitVersionPattern_StillWorksWhenItMatches(t *testing.T) {
	// Vulkan-Headers really does write these titles; nothing changes for it.
	pkg := "dev-util/vulkan-headers"

	cfg := baseCommitCfg()
	cfg.CommitMessagePath = "commit.message"
	cfg.CommitVersionPattern = `Update for Vulkan-Docs ([0-9]+\.[0-9]+\.[0-9]+)`

	body := makeGHCommits(
		struct{ sha, date, msg string }{testSHA40, "2026-07-31T00:15:17Z", "Update for Vulkan-Docs 1.4.358"},
	)

	checker := newBaseFileChecker(t, pkg, "1.4.357_p20260722", cfg, body, "")
	result, err := checker.CheckPackage(pkg, true)
	if err != nil {
		t.Fatalf("CheckPackage: %v", err)
	}
	if want := "1.4.358_p20260731"; result.UpstreamVersion != want {
		t.Errorf("UpstreamVersion = %q, want %q", result.UpstreamVersion, want)
	}
}

// =============================================================================
// Validation
// =============================================================================

func TestValidateBaseFrom(t *testing.T) {
	base := func() PackageConfig {
		c := baseCommitCfg()
		c.URL = "https://example.invalid/commits"
		return c
	}

	tests := []struct {
		name    string
		mutate  func(*PackageConfig)
		wantErr string
	}{
		{
			name: "file without base_url",
			mutate: func(c *PackageConfig) {
				c.BaseFrom = "file"
				c.BasePattern = `^([0-9.]+)$`
			},
			wantErr: "requires base_url and base_pattern",
		},
		{
			name: "file without base_pattern",
			mutate: func(c *PackageConfig) {
				c.BaseFrom = "file"
				c.BaseURL = "https://example.invalid/VERSION"
			},
			wantErr: "requires base_url and base_pattern",
		},
		{
			name: "unknown base_from",
			mutate: func(c *PackageConfig) {
				c.BaseFrom = "changelog"
			},
			wantErr: "invalid base_from",
		},
		// "none" declares that the upstream has no version to read. Declaring a
		// source alongside it is a contradiction, not dead weight, so each of the
		// four source fields is rejected on its own.
		{
			name: "none with base_url",
			mutate: func(c *PackageConfig) {
				c.BaseFrom = "none"
				c.BaseURL = "https://example.invalid/VERSION"
			},
			wantErr: `base_from="none" declares there is no base source`,
		},
		{
			name: "none with base_pattern",
			mutate: func(c *PackageConfig) {
				c.BaseFrom = "none"
				c.BasePattern = `^([0-9.]+)$`
			},
			wantErr: `base_from="none" declares there is no base source`,
		},
		{
			// The "none" case is reached before the generic
			// `base_tag_pattern requires base_from="tag"` check below the switch,
			// and says the more useful of the two things: the contradiction is
			// with the declaration, not with a missing companion.
			name: "none with base_tag_pattern",
			mutate: func(c *PackageConfig) {
				c.BaseFrom = "none"
				c.BaseTagPattern = `^v([0-9.]+)$`
			},
			wantErr: `base_from="none" declares there is no base source`,
		},
		{
			// commit_message_path is set too, otherwise the earlier
			// "commit_version_pattern requires commit_message_path" check fires
			// first and this case never reaches the switch.
			name: "none with commit_version_pattern",
			mutate: func(c *PackageConfig) {
				c.BaseFrom = "none"
				c.CommitVersionPattern = `v([0-9.]+)`
				c.CommitMessagePath = "commit.message"
			},
			wantErr: `base_from="none" declares there is no base source`,
		},
		{
			name: "tag without base_tag_pattern",
			mutate: func(c *PackageConfig) {
				c.BaseFrom = "tag"
				c.BaseURL = "https://example.invalid/refs/tags"
			},
			wantErr: "requires base_url and base_tag_pattern",
		},
		{
			name: "tag pattern with two capture groups",
			mutate: func(c *PackageConfig) {
				c.BaseFrom = "tag"
				c.BaseURL = "https://example.invalid/refs/tags"
				c.BaseTagPattern = `^v([0-9]+)\.([0-9]+)$`
			},
			wantErr: "exactly one capture group",
		},
		{
			name: "base_tag_pattern without base_from=tag",
			mutate: func(c *PackageConfig) {
				c.BaseFrom = "file"
				c.BaseURL = "https://example.invalid/VERSION"
				c.BasePattern = `^([0-9.]+)$`
				c.BaseTagPattern = `^v([0-9.]+)$`
			},
			wantErr: "base_tag_pattern requires base_from=\"tag\"",
		},
		{
			name: "base_url without base_from",
			mutate: func(c *PackageConfig) {
				c.BaseURL = "https://example.invalid/VERSION"
			},
			wantErr: "require base_from",
		},
		{
			name: "base_pattern with no capture group",
			mutate: func(c *PackageConfig) {
				c.BaseFrom = "file"
				c.BaseURL = "https://example.invalid/VERSION"
				c.BasePattern = `^[0-9.]+$`
			},
			wantErr: "exactly one capture group",
		},
		{
			name: "base_pattern with two capture groups",
			mutate: func(c *PackageConfig) {
				c.BaseFrom = "file"
				c.BaseURL = "https://example.invalid/VERSION"
				c.BasePattern = `^([0-9]+)\.([0-9]+)$`
			},
			wantErr: "exactly one capture group",
		},
		{
			name: "base_pattern that does not compile",
			mutate: func(c *PackageConfig) {
				c.BaseFrom = "file"
				c.BaseURL = "https://example.invalid/VERSION"
				c.BasePattern = `^([0-9.]+$`
			},
			wantErr: "invalid base_pattern",
		},
		{
			name: "commit_message without its pattern",
			mutate: func(c *PackageConfig) {
				c.BaseFrom = "commit_message"
			},
			wantErr: "requires commit_version_pattern",
		},
		{
			name: "base_from on a version-tracked entry",
			mutate: func(c *PackageConfig) {
				c.Track = ""
				c.CommitSHAPath = ""
				c.BaseFrom = "file"
				c.BaseURL = "https://example.invalid/VERSION"
				c.BasePattern = `^([0-9.]+)$`
			},
			wantErr: "requires track=\"commit\"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := base()
			tt.mutate(&cfg)
			err := ValidatePackageConfig("cat/pkg", &cfg)
			if err == nil {
				t.Fatalf("expected an error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidateBaseFrom_ValidConfigs(t *testing.T) {
	valid := []struct {
		name string
		cfg  func() PackageConfig
	}{
		{
			name: "file, fully specified",
			cfg: func() PackageConfig {
				c := baseCommitCfg()
				c.URL = "https://example.invalid/commits"
				c.BaseFrom = "file"
				c.BaseURL = "https://example.invalid/VERSION"
				c.BasePattern = `^([0-9][0-9.]*)-devel`
				return c
			},
		},
		{
			name: "commit_message, fully specified",
			cfg: func() PackageConfig {
				c := baseCommitCfg()
				c.URL = "https://example.invalid/commits"
				c.BaseFrom = "commit_message"
				c.CommitMessagePath = "commit.message"
				c.CommitVersionPattern = `Update for Vulkan-Docs ([0-9.]+)`
				return c
			},
		},
		{
			name: "absent, legacy entry",
			cfg: func() PackageConfig {
				c := baseCommitCfg()
				c.URL = "https://example.invalid/commits"
				return c
			},
		},
	}

	for _, tt := range valid {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.cfg()
			if err := ValidatePackageConfig("cat/pkg", &cfg); err != nil {
				t.Errorf("ValidatePackageConfig: unexpected error %v", err)
			}
		})
	}
}
