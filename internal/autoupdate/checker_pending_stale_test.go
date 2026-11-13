package autoupdate

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/registry"
)

// stalePendingChecker builds a Checker over an overlay holding pkg at
// current, with pending.json seeded with one entry for pkg targeting target,
// and an upstream answering upstream (an empty upstream answers 500).
func stalePendingChecker(t *testing.T, current, target, upstream string) (*Checker, string) {
	t.Helper()
	tmp := t.TempDir()
	overlayDir := filepath.Join(tmp, "overlay")
	configDir := filepath.Join(tmp, "config")
	pkg := "llvm-core/lld"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if upstream == "" {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"version": upstream})
	}))
	t.Cleanup(server.Close)

	createTestEbuild(t, overlayDir, pkg, current)
	seed, err := NewPendingList(configDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.Add(PendingUpdate{Package: pkg, CurrentVersion: "23.1.4", NewVersion: target, Status: StatusPending}); err != nil {
		t.Fatal(err)
	}

	checker, err := NewChecker(overlayDir,
		WithConfigDir(configDir),
		WithPackagesConfig(&registry.PackagesConfig{Packages: map[string]registry.PackageConfig{
			pkg: {URL: server.URL, Parser: "json", Path: "version"},
		}}),
		WithRateLimiter(unlimitedRateLimiter()),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := checker.Pending().Get(pkg); !ok {
		t.Fatal("precondition: the seeded pending entry was not loaded")
	}
	return checker, pkg
}

// A version bumped by hand leaves its pending entry behind; the next check
// must drop it rather than validate a bump whose ebuild already exists.
func TestCheckPackage_DropsAPendingEntryTheTreeAlreadyHolds(t *testing.T) {
	checker, pkg := stalePendingChecker(t, "23.1.5", "23.1.5", "23.1.5")
	if _, err := checker.CheckPackage(t.Context(), pkg, true); err != nil {
		t.Fatalf("CheckPackage: %v", err)
	}
	if e, ok := checker.Pending().Get(pkg); ok {
		t.Errorf("pending still holds %s %s -> %s although the tree has %s", pkg, e.CurrentVersion, e.NewVersion, "23.1.5")
	}
}

// The drop does not depend on reaching upstream: a stale entry must not
// survive a run whose fetch failed.
func TestCheckPackage_DropsAStaleEntryEvenWhenUpstreamFails(t *testing.T) {
	checker, pkg := stalePendingChecker(t, "23.1.5", "23.1.5", "")
	_, _ = checker.CheckPackage(t.Context(), pkg, true)
	if _, ok := checker.Pending().Get(pkg); ok {
		t.Error("a stale pending entry survived a check whose upstream fetch failed")
	}
}

// An entry that still targets a version above the tree is real work and stays,
// whatever upstream answered this time.
func TestCheckPackage_KeepsAPendingEntryAboveTheTree(t *testing.T) {
	checker, pkg := stalePendingChecker(t, "23.1.5", "23.1.6", "")
	_, _ = checker.CheckPackage(t.Context(), pkg, true)
	if _, ok := checker.Pending().Get(pkg); !ok {
		t.Error("a pending entry targeting a newer version than the tree was dropped")
	}
}
