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
