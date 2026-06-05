package autoupdate

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// =============================================================================
// Helpers
// =============================================================================

const testSHA40 = "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"
const testSHA40b = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

// ghCommit is a GitHub commits-list entry.
type ghCommit struct {
	SHA    string `json:"sha"`
	Commit struct {
		Message   string `json:"message"`
		Committer struct {
			Date string `json:"date"`
		} `json:"committer"`
	} `json:"commit"`
}

// makeGHCommits serialises a slice of (sha, dateISO8601, message) triples into
// the JSON array returned by the GitHub commits list endpoint.
func makeGHCommits(entries ...struct{ sha, date, msg string }) []byte {
	commits := make([]ghCommit, len(entries))
	for i, e := range entries {
		commits[i].SHA = e.sha
		commits[i].Commit.Message = e.msg
		commits[i].Commit.Committer.Date = e.date
	}
	b, _ := json.Marshal(commits)
	return b
}

// glEntry is a GitLab repository/commits entry.
type glEntry struct {
	ID            string `json:"id"`
	CommittedDate string `json:"committed_date"`
	Title         string `json:"title"`
}

// makeGLCommits serialises a slice of (id, dateISO, title) triples into the
// JSON array returned by the GitLab repository/commits endpoint.
func makeGLCommits(entries ...struct{ id, date, title string }) []byte {
	items := make([]glEntry, len(entries))
	for i, e := range entries {
		items[i].ID = e.id
		items[i].CommittedDate = e.date
		items[i].Title = e.title
	}
	b, _ := json.Marshal(items)
	return b
}

// newCommitChecker creates a Checker wired to a static mock HTTP server that
// always returns body, and an overlay with one ebuild at currentVersion.
func newCommitChecker(t *testing.T, pkg, currentVersion string, cfg PackageConfig, body []byte) *Checker {
	t.Helper()
	overlayDir := t.TempDir()
	configDir := t.TempDir()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)

	cfg.URL = server.URL

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

// =============================================================================
// Unit: extractSnapshotBase
// =============================================================================

func TestExtractSnapshotBase(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"1.4.352_p20260515", "1.4.352"},
		{"26.2.0_pre20260529", "26.2.0"},
		{"3.13.99_p20260517", "3.13.99"},
		{"1.4.350.0_p20260522", "1.4.350.0"},
		{"1.25.1_p20260526", "1.25.1"},
		{"4.7_beta5", "4.7_beta5"}, // no _p/_pre suffix — returned unchanged
		{"1.0.0", "1.0.0"},
		{"26.2.0_p20260605", "26.2.0"},
	}
	for _, tt := range tests {
		if got := extractSnapshotBase(tt.in); got != tt.want {
			t.Errorf("extractSnapshotBase(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// =============================================================================
// Unit: scanCommitsForVersion
// =============================================================================

func TestScanCommitsForVersion(t *testing.T) {
	ghBody := makeGHCommits(
		struct{ sha, date, msg string }{testSHA40, "2026-06-05T00:23:15Z", "Update for Vulkan-Docs 1.4.353"},
		struct{ sha, date, msg string }{testSHA40b, "2026-06-04T10:00:00Z", "Update for Vulkan-Docs 1.4.352"},
		struct{ sha, date, msg string }{"cccc", "2026-06-03T08:00:00Z", "fix: unrelated change"},
	)

	t.Run("github: picks highest version", func(t *testing.T) {
		got := scanCommitsForVersion(ghBody, "commit.message", `Update for Vulkan-Docs ([0-9]+\.[0-9]+\.[0-9]+)`)
		if got != "1.4.353" {
			t.Errorf("got %q, want 1.4.353", got)
		}
	})

	t.Run("github: no match returns empty", func(t *testing.T) {
		got := scanCommitsForVersion(ghBody, "commit.message", `SomethingElse ([0-9]+\.[0-9]+)`)
		if got != "" {
			t.Errorf("got %q, want empty string", got)
		}
	})

	t.Run("vulkan-sdk 4-part pattern", func(t *testing.T) {
		body := makeGHCommits(
			struct{ sha, date, msg string }{testSHA40, "2026-06-05T00:00:00Z", "Update glslang to vulkan-sdk-1.4.351.0"},
			struct{ sha, date, msg string }{testSHA40b, "2026-06-04T00:00:00Z", "Previous update vulkan-sdk-1.4.350.0"},
		)
		got := scanCommitsForVersion(body, "commit.message", `vulkan-sdk-([0-9]+\.[0-9]+\.[0-9]+\.[0-9]+)`)
		if got != "1.4.351.0" {
			t.Errorf("got %q, want 1.4.351.0", got)
		}
	})

	t.Run("gitlab title field", func(t *testing.T) {
		body := makeGLCommits(
			struct{ id, date, title string }{"abc123", "2026-06-05T10:00:00.000+00:00", "Release 1.26.0"},
			struct{ id, date, title string }{"def456", "2026-06-04T09:00:00.000+00:00", "fix: minor"},
		)
		got := scanCommitsForVersion(body, "title", `Release ([0-9]+\.[0-9]+\.[0-9]+)`)
		if got != "1.26.0" {
			t.Errorf("got %q, want 1.26.0", got)
		}
	})

	t.Run("invalid json returns empty", func(t *testing.T) {
		got := scanCommitsForVersion([]byte("not json"), "commit.message", `([0-9]+\.[0-9]+)`)
		if got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})

	t.Run("invalid regex returns empty", func(t *testing.T) {
		got := scanCommitsForVersion(ghBody, "commit.message", `([invalid`)
		if got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})
}

// =============================================================================
// Integration: CheckPackage with track="commit"
// =============================================================================

// baseCommitCfg returns a PackageConfig set up for GitHub commit tracking.
// The caller must set cfg.URL (done by newCommitChecker).
func baseCommitCfg() PackageConfig {
	return PackageConfig{
		Track:         "commit",
		Parser:        "json",
		Path:          "[0].commit.committer.date",
		CommitSHAPath: "[0].sha",
		Transform:     [][]string{{"T.*", ""}, {"-", ""}},
		Headers:       map[string]string{"User-Agent": "bentoo-test"},
	}
}

func TestCheckPackageCommitTrack_DateBumpOnly(t *testing.T) {
	// Current ebuild is 1.4.352_p20260515; latest commit date is 20260605 with
	// no version-bump message → new version should be 1.4.352_p20260605.
	pkg := "dev-util/vulkan-headers"
	currentVer := "1.4.352_p20260515"

	body := makeGHCommits(
		struct{ sha, date, msg string }{testSHA40, "2026-06-05T00:23:15Z", "fix: minor header adjustment"},
	)

	checker := newCommitChecker(t, pkg, currentVer, baseCommitCfg(), body)
	result, err := checker.CheckPackage(pkg, true)
	if err != nil {
		t.Fatalf("CheckPackage: %v", err)
	}

	want := "1.4.352_p20260605"
	if result.UpstreamVersion != want {
		t.Errorf("UpstreamVersion = %q, want %q", result.UpstreamVersion, want)
	}
	if !result.HasUpdate {
		t.Error("HasUpdate = false, want true")
	}
	if result.NotComparable {
		t.Error("NotComparable = true, want false")
	}
}

func TestCheckPackageCommitTrack_BaseVersionBump(t *testing.T) {
	// Commit title says "Update for Vulkan-Docs 1.4.353" → new base 1.4.353.
	pkg := "dev-util/vulkan-headers"
	currentVer := "1.4.352_p20260515"

	cfg := baseCommitCfg()
	cfg.CommitMessagePath = "commit.message"
	cfg.CommitVersionPattern = `Update for Vulkan-Docs ([0-9]+\.[0-9]+\.[0-9]+)`

	body := makeGHCommits(
		struct{ sha, date, msg string }{testSHA40, "2026-06-05T00:23:15Z", "Update for Vulkan-Docs 1.4.353"},
		struct{ sha, date, msg string }{testSHA40b, "2026-06-04T08:00:00Z", "fix: unrelated"},
	)

	checker := newCommitChecker(t, pkg, currentVer, cfg, body)
	result, err := checker.CheckPackage(pkg, true)
	if err != nil {
		t.Fatalf("CheckPackage: %v", err)
	}

	want := "1.4.353_p20260605"
	if result.UpstreamVersion != want {
		t.Errorf("UpstreamVersion = %q, want %q", result.UpstreamVersion, want)
	}
	if !result.HasUpdate {
		t.Error("HasUpdate = false, want true")
	}
}

func TestCheckPackageCommitTrack_NoUpdate(t *testing.T) {
	// Ebuild is already at the latest commit date with the same base → no update.
	pkg := "dev-util/vulkan-headers"
	currentVer := "1.4.352_p20260605"

	cfg := baseCommitCfg()
	cfg.CommitMessagePath = "commit.message"
	cfg.CommitVersionPattern = `Update for Vulkan-Docs ([0-9]+\.[0-9]+\.[0-9]+)`

	body := makeGHCommits(
		struct{ sha, date, msg string }{testSHA40, "2026-06-05T00:23:15Z", "Update for Vulkan-Docs 1.4.352"},
	)

	checker := newCommitChecker(t, pkg, currentVer, cfg, body)
	result, err := checker.CheckPackage(pkg, true)
	if err != nil {
		t.Fatalf("CheckPackage: %v", err)
	}

	if result.HasUpdate {
		t.Errorf("HasUpdate = true, want false (upstream %q, current %q)",
			result.UpstreamVersion, currentVer)
	}
}

func TestCheckPackageCommitTrack_SHA_StoredInPending(t *testing.T) {
	// Verifica que o SHA do commit mais recente é armazenado no PendingUpdate.
	pkg := "dev-util/vulkan-headers"
	currentVer := "1.4.352_p20260515"

	body := makeGHCommits(
		struct{ sha, date, msg string }{testSHA40, "2026-06-05T00:23:15Z", "fix"},
	)

	checker := newCommitChecker(t, pkg, currentVer, baseCommitCfg(), body)
	result, err := checker.CheckPackage(pkg, true)
	if err != nil {
		t.Fatalf("CheckPackage: %v", err)
	}
	if !result.HasUpdate {
		t.Fatal("expected HasUpdate=true")
	}

	update, ok := checker.pending.Get(pkg)
	if !ok {
		t.Fatal("package not in pending list")
	}
	if update.CommitHash != testSHA40 {
		t.Errorf("CommitHash = %q, want %q", update.CommitHash, testSHA40)
	}
	if update.NewVersion != result.UpstreamVersion {
		t.Errorf("pending NewVersion = %q, want %q", update.NewVersion, result.UpstreamVersion)
	}
}

func TestCheckPackageCommitTrack_SkipsCache(t *testing.T) {
	// commit-tracked packages must always fetch fresh even when force=false.
	pkg := "dev-util/vulkan-headers"
	currentVer := "1.4.352_p20260515"

	var fetchCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetchCount++
		body := makeGHCommits(struct{ sha, date, msg string }{testSHA40, "2026-06-05T00:00:00Z", "fix"})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer server.Close()

	cfg := baseCommitCfg()
	cfg.URL = server.URL

	overlayDir := t.TempDir()
	configDir := t.TempDir()
	checker, err := NewChecker(overlayDir,
		WithConfigDir(configDir),
		WithPackagesConfig(&PackagesConfig{Packages: map[string]PackageConfig{pkg: cfg}}),
		WithRateLimiter(unlimitedRateLimiter()),
	)
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}
	createTestEbuild(t, overlayDir, pkg, currentVer)

	// First call — force=false. Commit-tracked packages must still fetch.
	if _, err := checker.CheckPackage(pkg, false); err != nil {
		t.Fatalf("first check: %v", err)
	}
	// Second call — force=false again. Must fetch again (no cache read).
	if _, err := checker.CheckPackage(pkg, false); err != nil {
		t.Fatalf("second check: %v", err)
	}
	if fetchCount < 2 {
		t.Errorf("expected ≥2 fetches, got %d (cache should be bypassed for track=commit)", fetchCount)
	}
}

// =============================================================================
// Per-package table-driven tests for all 9 _p packages
// =============================================================================

func TestCheckPackageCommitTrack_AllSnapshotPackages(t *testing.T) {
	// sha used for all entries (40 hex chars)
	sha := testSHA40

	// GitHub commit list entry builder
	ghEntry := func(date, msg string) struct{ sha, date, msg string } {
		return struct{ sha, date, msg string }{sha, date, msg}
	}

	// GitLab commit list entry builder
	glEntry2 := func(date, title string) struct{ id, date, title string } {
		return struct{ id, date, title string }{sha, date, title}
	}

	tests := []struct {
		name           string
		pkg            string
		currentVersion string
		cfg            PackageConfig
		body           []byte
		wantVersion    string
		wantUpdate     bool
	}{
		// ── vulkan-headers ──────────────────────────────────────────────────────
		{
			name:           "vulkan-headers: base bump 1.4.352→1.4.353",
			pkg:            "dev-util/vulkan-headers",
			currentVersion: "1.4.352_p20260515",
			cfg: PackageConfig{
				Track:                "commit",
				Parser:               "json",
				Path:                 "[0].commit.committer.date",
				CommitSHAPath:        "[0].sha",
				CommitMessagePath:    "commit.message",
				CommitVersionPattern: `Update for Vulkan-Docs ([0-9]+\.[0-9]+\.[0-9]+)`,
				Transform:            [][]string{{"T.*", ""}, {"-", ""}},
				Headers:              map[string]string{"User-Agent": "bentoo-test"},
			},
			body:        makeGHCommits(ghEntry("2026-06-05T00:23:15Z", "Update for Vulkan-Docs 1.4.353")),
			wantVersion: "1.4.353_p20260605",
			wantUpdate:  true,
		},
		{
			name:           "vulkan-headers: already up to date",
			pkg:            "dev-util/vulkan-headers",
			currentVersion: "1.4.353_p20260605",
			cfg: PackageConfig{
				Track:                "commit",
				Parser:               "json",
				Path:                 "[0].commit.committer.date",
				CommitSHAPath:        "[0].sha",
				CommitMessagePath:    "commit.message",
				CommitVersionPattern: `Update for Vulkan-Docs ([0-9]+\.[0-9]+\.[0-9]+)`,
				Transform:            [][]string{{"T.*", ""}, {"-", ""}},
				Headers:              map[string]string{"User-Agent": "bentoo-test"},
			},
			body:        makeGHCommits(ghEntry("2026-06-05T00:23:15Z", "Update for Vulkan-Docs 1.4.353")),
			wantVersion: "1.4.353_p20260605",
			wantUpdate:  false,
		},
		// ── vulkan-loader ───────────────────────────────────────────────────────
		{
			name:           "vulkan-loader: base bump 1.4.352→1.4.353",
			pkg:            "media-libs/vulkan-loader",
			currentVersion: "1.4.352_p20260526",
			cfg: PackageConfig{
				Track:                "commit",
				Parser:               "json",
				Path:                 "[0].commit.committer.date",
				CommitSHAPath:        "[0].sha",
				CommitMessagePath:    "commit.message",
				CommitVersionPattern: `Update for Vulkan-Docs ([0-9]+\.[0-9]+\.[0-9]+)`,
				Transform:            [][]string{{"T.*", ""}, {"-", ""}},
				Headers:              map[string]string{"User-Agent": "bentoo-test"},
			},
			body:        makeGHCommits(ghEntry("2026-06-05T00:23:15Z", "Update for Vulkan-Docs 1.4.353")),
			wantVersion: "1.4.353_p20260605",
			wantUpdate:  true,
		},
		// ── vulkan-tools ────────────────────────────────────────────────────────
		{
			name:           "vulkan-tools: base bump 1.4.352→1.4.353",
			pkg:            "dev-util/vulkan-tools",
			currentVersion: "1.4.352_p20260518",
			cfg: PackageConfig{
				Track:                "commit",
				Parser:               "json",
				Path:                 "[0].commit.committer.date",
				CommitSHAPath:        "[0].sha",
				CommitMessagePath:    "commit.message",
				CommitVersionPattern: `Update for Vulkan-Docs ([0-9]+\.[0-9]+\.[0-9]+)`,
				Transform:            [][]string{{"T.*", ""}, {"-", ""}},
				Headers:              map[string]string{"User-Agent": "bentoo-test"},
			},
			body:        makeGHCommits(ghEntry("2026-06-05T00:23:15Z", "Update for Vulkan-Docs 1.4.353")),
			wantVersion: "1.4.353_p20260605",
			wantUpdate:  true,
		},
		// ── vulkan-layers ───────────────────────────────────────────────────────
		{
			name:           "vulkan-layers: base bump 1.4.352→1.4.353",
			pkg:            "media-libs/vulkan-layers",
			currentVersion: "1.4.352_p20260528",
			cfg: PackageConfig{
				Track:                "commit",
				Parser:               "json",
				Path:                 "[0].commit.committer.date",
				CommitSHAPath:        "[0].sha",
				CommitMessagePath:    "commit.message",
				CommitVersionPattern: `Update for Vulkan-Docs ([0-9]+\.[0-9]+\.[0-9]+)`,
				Transform:            [][]string{{"T.*", ""}, {"-", ""}},
				Headers:              map[string]string{"User-Agent": "bentoo-test"},
			},
			body:        makeGHCommits(ghEntry("2026-06-05T00:23:15Z", "Update for Vulkan-Docs 1.4.353")),
			wantVersion: "1.4.353_p20260605",
			wantUpdate:  true,
		},
		// ── glslang ─────────────────────────────────────────────────────────────
		{
			name:           "glslang: vulkan-sdk base bump 1.4.350.0→1.4.351.0",
			pkg:            "dev-util/glslang",
			currentVersion: "1.4.350.0_p20260522",
			cfg: PackageConfig{
				Track:                "commit",
				Parser:               "json",
				Path:                 "[0].commit.committer.date",
				CommitSHAPath:        "[0].sha",
				CommitMessagePath:    "commit.message",
				CommitVersionPattern: `vulkan-sdk-([0-9]+\.[0-9]+\.[0-9]+\.[0-9]+)`,
				Transform:            [][]string{{"T.*", ""}, {"-", ""}},
				Headers:              map[string]string{"User-Agent": "bentoo-test"},
			},
			body:        makeGHCommits(ghEntry("2026-06-05T10:00:00Z", "Update glslang to vulkan-sdk-1.4.351.0")),
			wantVersion: "1.4.351.0_p20260605",
			wantUpdate:  true,
		},
		// ── spirv-headers ───────────────────────────────────────────────────────
		{
			name:           "spirv-headers: vulkan-sdk base bump 1.4.350.0→1.4.351.0",
			pkg:            "dev-util/spirv-headers",
			currentVersion: "1.4.350.0_p20260527",
			cfg: PackageConfig{
				Track:                "commit",
				Parser:               "json",
				Path:                 "[0].commit.committer.date",
				CommitSHAPath:        "[0].sha",
				CommitMessagePath:    "commit.message",
				CommitVersionPattern: `vulkan-sdk-([0-9]+\.[0-9]+\.[0-9]+\.[0-9]+)`,
				Transform:            [][]string{{"T.*", ""}, {"-", ""}},
				Headers:              map[string]string{"User-Agent": "bentoo-test"},
			},
			body:        makeGHCommits(ghEntry("2026-06-05T10:00:00Z", "Update SPIRV-Headers to vulkan-sdk-1.4.351.0")),
			wantVersion: "1.4.351.0_p20260605",
			wantUpdate:  true,
		},
		// ── spirv-tools ─────────────────────────────────────────────────────────
		{
			name:           "spirv-tools: vulkan-sdk base bump 1.4.350.0→1.4.351.0",
			pkg:            "dev-util/spirv-tools",
			currentVersion: "1.4.350.0_p20260528",
			cfg: PackageConfig{
				Track:                "commit",
				Parser:               "json",
				Path:                 "[0].commit.committer.date",
				CommitSHAPath:        "[0].sha",
				CommitMessagePath:    "commit.message",
				CommitVersionPattern: `vulkan-sdk-([0-9]+\.[0-9]+\.[0-9]+\.[0-9]+)`,
				Transform:            [][]string{{"T.*", ""}, {"-", ""}},
				Headers:              map[string]string{"User-Agent": "bentoo-test"},
			},
			body:        makeGHCommits(ghEntry("2026-06-05T10:00:00Z", "Update SPIRV-Tools to vulkan-sdk-1.4.351.0")),
			wantVersion: "1.4.351.0_p20260605",
			wantUpdate:  true,
		},
		// ── sqlitebrowser ───────────────────────────────────────────────────────
		{
			name:           "sqlitebrowser: date bump only (no version pattern)",
			pkg:            "dev-db/sqlitebrowser",
			currentVersion: "3.13.99_p20260517",
			cfg: PackageConfig{
				Track:         "commit",
				Parser:        "json",
				Path:          "[0].commit.committer.date",
				CommitSHAPath: "[0].sha",
				Transform:     [][]string{{"T.*", ""}, {"-", ""}},
				Headers:       map[string]string{"User-Agent": "bentoo-test"},
			},
			body:        makeGHCommits(ghEntry("2026-06-05T08:00:00Z", "Add feature X")),
			wantVersion: "3.13.99_p20260605",
			wantUpdate:  true,
		},
		// ── modemmanager (GitLab) ───────────────────────────────────────────────
		{
			name:           "modemmanager: date bump via GitLab API",
			pkg:            "net-misc/modemmanager",
			currentVersion: "1.25.1_p20260526",
			cfg: PackageConfig{
				Track:         "commit",
				Parser:        "json",
				Path:          "[0].committed_date",
				CommitSHAPath: "[0].id",
				Transform:     [][]string{{"T.*", ""}, {"-", ""}},
			},
			body:        makeGLCommits(glEntry2("2026-06-05T10:00:00.000+00:00", "mm: add device support")),
			wantVersion: "1.25.1_p20260605",
			wantUpdate:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checker := newCommitChecker(t, tt.pkg, tt.currentVersion, tt.cfg, tt.body)

			result, err := checker.CheckPackage(tt.pkg, true)
			if err != nil {
				t.Fatalf("CheckPackage: %v", err)
			}

			if result.UpstreamVersion != tt.wantVersion {
				t.Errorf("UpstreamVersion = %q, want %q", result.UpstreamVersion, tt.wantVersion)
			}
			if result.HasUpdate != tt.wantUpdate {
				t.Errorf("HasUpdate = %v, want %v", result.HasUpdate, tt.wantUpdate)
			}
			if result.NotComparable {
				t.Error("NotComparable = true, want false")
			}

			// When an update is expected, verify CommitHash is persisted.
			if tt.wantUpdate {
				update, ok := checker.pending.Get(tt.pkg)
				if !ok {
					t.Fatal("package not in pending list despite HasUpdate=true")
				}
				if update.CommitHash != sha {
					t.Errorf("pending CommitHash = %q, want %q", update.CommitHash, sha)
				}
				if update.NewVersion != tt.wantVersion {
					t.Errorf("pending NewVersion = %q, want %q", update.NewVersion, tt.wantVersion)
				}
			}
		})
	}
}

// =============================================================================
// Edge cases
// =============================================================================

func TestCheckPackageCommitTrack_BaseVersionNotDowngraded(t *testing.T) {
	// If the commit title mentions a version OLDER than the current base, the
	// base must not change (we never downgrade the base version).
	pkg := "dev-util/vulkan-headers"
	currentVer := "1.4.353_p20260520"

	cfg := baseCommitCfg()
	cfg.CommitMessagePath = "commit.message"
	cfg.CommitVersionPattern = `Update for Vulkan-Docs ([0-9]+\.[0-9]+\.[0-9]+)`

	// Commit says 1.4.352, which is older than current base 1.4.353.
	body := makeGHCommits(
		struct{ sha, date, msg string }{testSHA40, "2026-06-05T00:00:00Z", "Update for Vulkan-Docs 1.4.352"},
	)

	checker := newCommitChecker(t, pkg, currentVer, cfg, body)
	result, err := checker.CheckPackage(pkg, true)
	if err != nil {
		t.Fatalf("CheckPackage: %v", err)
	}

	// Base stays at 1.4.353 (not downgraded to 1.4.352).
	wantVersion := fmt.Sprintf("1.4.353_p20260605")
	if result.UpstreamVersion != wantVersion {
		t.Errorf("UpstreamVersion = %q, want %q (base must not be downgraded)", result.UpstreamVersion, wantVersion)
	}
	if !result.HasUpdate {
		t.Error("HasUpdate = false, want true (date advanced)")
	}
}

func TestCheckPackageCommitTrack_GitLabDateFormat(t *testing.T) {
	// GitLab uses "T..." with timezone offset "+00:00" instead of "Z".
	// The transform [["T.*", ""], ["-", ""]] must still produce YYYYMMDD.
	pkg := "net-misc/modemmanager"
	currentVer := "1.25.1_p20260526"

	cfg := PackageConfig{
		Track:         "commit",
		Parser:        "json",
		Path:          "[0].committed_date",
		CommitSHAPath: "[0].id",
		Transform:     [][]string{{"T.*", ""}, {"-", ""}},
	}

	body := makeGLCommits(struct{ id, date, title string }{testSHA40, "2026-06-10T14:30:00.000+00:00", "fix"})

	checker := newCommitChecker(t, pkg, currentVer, cfg, body)
	result, err := checker.CheckPackage(pkg, true)
	if err != nil {
		t.Fatalf("CheckPackage: %v", err)
	}

	want := "1.25.1_p20260610"
	if result.UpstreamVersion != want {
		t.Errorf("UpstreamVersion = %q, want %q", result.UpstreamVersion, want)
	}
	if !result.HasUpdate {
		t.Error("HasUpdate = false, want true")
	}
}
