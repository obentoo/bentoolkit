package autoupdate

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cursorBuildID is a realistic 40-hex commitSha as returned by the cursor API.
const cursorBuildID = "b887a26c4f70bd8136bfffeda812b24194ec9ce0"

// TestSubstituteCommitHash_BuildID verifies that substituteCommitHash rewrites a
// BUILD_ID="<sha>" assignment — the auxiliary SHA variable cursor embeds in its
// SRC_URI. Without this, a version bump keeps the stale BUILD_ID and the .deb URL
// 404/403s (the cursor 403 bug).
func TestSubstituteCommitHash_BuildID(t *testing.T) {
	oldSHA := "81fcf2931d7687b4ff3f3017858d0c6dee7e2a68"
	dir := t.TempDir()
	path := filepath.Join(dir, "cursor-3.7.12.ebuild")
	content := "EAPI=8\nBUILD_ID=\"" + oldSHA + "\"\n" +
		"SRC_URI=\"https://downloads.cursor.com/production/${BUILD_ID}/linux/x64/deb/amd64/deb/cursor_${PV}_amd64.deb\"\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write ebuild: %v", err)
	}

	if err := substituteCommitHash(path, cursorBuildID); err != nil {
		t.Fatalf("substituteCommitHash: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read ebuild: %v", err)
	}
	if strings.Contains(string(got), oldSHA) {
		t.Errorf("old BUILD_ID still present:\n%s", got)
	}
	if !strings.Contains(string(got), "BUILD_ID=\""+cursorBuildID+"\"") {
		t.Errorf("new BUILD_ID not written:\n%s", got)
	}
}

// TestValidate_CommitSHAPath_VersionTrack covers the relaxed validation: a
// version-tracked package (no track="commit") may set commit_sha_path to drive
// BUILD_ID substitution, but only with parser="json".
func TestValidate_CommitSHAPath_VersionTrack(t *testing.T) {
	t.Run("json ok", func(t *testing.T) {
		cfg := PackageConfig{URL: "https://x", Parser: "json", Path: "version", CommitSHAPath: "commitSha"}
		if err := ValidatePackageConfig("app-editors/cursor", &cfg); err != nil {
			t.Errorf("expected valid, got %v", err)
		}
	})
	t.Run("non-json rejected", func(t *testing.T) {
		cfg := PackageConfig{URL: "https://x", Parser: "regex", Pattern: "v(.*)", CommitSHAPath: "commitSha"}
		if err := ValidatePackageConfig("app-editors/cursor", &cfg); err == nil {
			t.Error("expected error for commit_sha_path with parser!=json")
		}
	})
}

// TestCheckPackageVersionTrack_AuxSHA_StoredInPending verifies the end-to-end
// checker behaviour for a cursor-style package: when an update is detected, the
// commitSha from the same JSON response is stored in the pending update so the
// applier can substitute BUILD_ID.
func TestCheckPackageVersionTrack_AuxSHA_StoredInPending(t *testing.T) {
	pkg := "app-editors/cursor"
	currentVer := "3.6.31"

	body := `{"version":"3.7.12","commitSha":"` + cursorBuildID + `"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	overlayDir := t.TempDir()
	configDir := t.TempDir()
	cfg := PackageConfig{
		Parser:        "json",
		Path:          "version",
		CommitSHAPath: "commitSha",
		URL:           server.URL,
	}
	checker, err := NewChecker(overlayDir,
		WithConfigDir(configDir),
		WithPackagesConfig(&PackagesConfig{Packages: map[string]PackageConfig{pkg: cfg}}),
		WithRateLimiter(unlimitedRateLimiter()),
	)
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}
	createTestEbuild(t, overlayDir, pkg, currentVer)

	result, err := checker.CheckPackage(pkg, true)
	if err != nil {
		t.Fatalf("CheckPackage: %v", err)
	}
	if !result.HasUpdate {
		t.Fatalf("expected HasUpdate=true (3.6.31 -> 3.7.12)")
	}
	if result.UpstreamVersion != "3.7.12" {
		t.Errorf("UpstreamVersion = %q, want 3.7.12", result.UpstreamVersion)
	}

	update, ok := checker.pending.Get(pkg)
	if !ok {
		t.Fatal("package not in pending list")
	}
	if update.CommitHash != cursorBuildID {
		t.Errorf("pending CommitHash = %q, want %q", update.CommitHash, cursorBuildID)
	}
}
