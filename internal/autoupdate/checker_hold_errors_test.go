package autoupdate

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// heldErrChecker is holdChecker with extra options: the aux-404-on-a-fresh-fetch
// case needs the body cache off, because with it on the aux read reuses the
// version page's body and never reaches the server a second time.
func heldErrChecker(t *testing.T, overlayDir, configDir, pkg string, cfg PackageConfig, extra ...CheckerOption) *Checker {
	t.Helper()
	opts := append([]CheckerOption{
		WithConfigDir(configDir),
		WithPackagesConfig(&PackagesConfig{Packages: map[string]PackageConfig{pkg: cfg}}),
		WithRateLimiter(unlimitedRateLimiter()),
	}, extra...)
	c, err := NewChecker(overlayDir, opts...)
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}
	return c
}

// firstThenServer answers the first request with 200 and firstBody and every
// later one with 404, so one fresh check can read the version and then fail
// the aux read.
func firstThenServer(t *testing.T, firstBody string) string {
	t.Helper()
	var mu sync.Mutex
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		n++
		first := n == 1
		mu.Unlock()
		if first {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(firstBody))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("not found"))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// blockCacheWrite makes the version cache in configDir unwritable without
// touching permissions: cache.json is a directory, so the cache's
// write-then-rename cannot replace it, while pending.json beside it still works.
func blockCacheWrite(t *testing.T, configDir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(configDir, "cache.json", "blocker"), 0o755); err != nil {
		t.Fatalf("blocking the cache file: %v", err)
	}
}

// checkForHold runs one check and returns its result after asserting the
// fixture reached the block it names.
func checkForHold(t *testing.T, c *Checker, pkg string, force, wantFromCache bool) *CheckResult {
	t.Helper()
	result, _ := c.CheckPackage(pkg, force)
	if result == nil {
		t.Fatal("CheckPackage returned no result")
	}
	if result.FromCache != wantFromCache {
		t.Fatalf("fixture: FromCache = %v, want %v (wrong CheckPackage block exercised)", result.FromCache, wantFromCache)
	}
	if !result.HasUpdate {
		t.Fatalf("fixture: expected HasUpdate, got %+v", result)
	}
	if result.Error == nil {
		t.Fatalf("fixture: the check recorded no error at all")
	}
	return result
}

// TestCheckPackage_HeldBumpWrapsErrAuxUnresolved pins S050-R3.4: a held bump is
// recognisable with errors.Is(result.Error, ErrAuxUnresolved) in every way a
// bump gets held — both CheckPackage blocks, both kinds of unresolved value,
// and the fresh fetch where result.Error already carried the cache-write error
// before the hold was joined onto it.
//
// The last two subtests are the converse: a result that is NOT a hold must not
// match the sentinel, so wrapping every check error in it would not pass.
func TestCheckPackage_HeldBumpWrapsErrAuxUnresolved(t *testing.T) {
	assertHeld := func(t *testing.T, result *CheckResult) {
		t.Helper()
		if !errors.Is(result.Error, ErrAuxUnresolved) {
			t.Errorf("errors.Is(result.Error, ErrAuxUnresolved) = false for a held bump\nresult.Error: %v", result.Error)
		}
	}

	t.Run("aux 404 on a fresh fetch", func(t *testing.T) {
		url := firstThenServer(t, holdPageNoAux)
		overlayDir, configDir := t.TempDir(), t.TempDir()
		createTestEbuild(t, overlayDir, holdPkg, holdCurrent)
		c := heldErrChecker(t, overlayDir, configDir, holdPkg, holdAuxConfig(url), WithFetchCache(false))
		result := checkForHold(t, c, holdPkg, true, false)
		if !strings.Contains(result.Error.Error(), "failed to fetch aux value") {
			t.Fatalf("fixture: the aux read did not fail on its fetch: %v", result.Error)
		}
		assertHeld(t, result)
	})

	t.Run("aux no-capture on a cache hit", func(t *testing.T) {
		srv := newHoldServer(t, http.StatusOK, holdPageNoAux)
		overlayDir, configDir := t.TempDir(), t.TempDir()
		createTestEbuild(t, overlayDir, holdPkg, holdCurrent)
		// The first check fills the version cache; the second is the cache hit.
		checkForHold(t, heldErrChecker(t, overlayDir, configDir, holdPkg, holdAuxConfig(srv.srv.URL)), holdPkg, true, false)
		result := checkForHold(t, heldErrChecker(t, overlayDir, configDir, holdPkg, holdAuxConfig(srv.srv.URL)), holdPkg, false, true)
		assertHeld(t, result)
	})

	t.Run("commit_sha_path unresolved", func(t *testing.T) {
		const pkg = "app-editors/cursor"
		srv := newHoldServer(t, http.StatusOK, `{"version":"2.0.0"}`)
		overlayDir, configDir := t.TempDir(), t.TempDir()
		createTestEbuild(t, overlayDir, pkg, "1.0.0")
		cfg := PackageConfig{Parser: "json", Path: "version", CommitSHAPath: "commitSha", URL: srv.srv.URL}
		result := checkForHold(t, heldErrChecker(t, overlayDir, configDir, pkg, cfg), pkg, true, false)
		assertHeld(t, result)
	})

	t.Run("fresh fetch whose cache write also failed", func(t *testing.T) {
		srv := newHoldServer(t, http.StatusOK, holdPageNoAux)
		overlayDir, configDir := t.TempDir(), t.TempDir()
		createTestEbuild(t, overlayDir, holdPkg, holdCurrent)
		blockCacheWrite(t, configDir)
		c := heldErrChecker(t, overlayDir, configDir, holdPkg, holdAuxConfig(srv.srv.URL))
		result := checkForHold(t, c, holdPkg, true, false)
		if !strings.Contains(result.Error.Error(), "failed to update cache") {
			t.Fatalf("fixture: the cache write did not fail, so result.Error did not already hold an error: %v", result.Error)
		}
		assertHeld(t, result)
	})

	t.Run("not held: cache write failed but the aux value resolved", func(t *testing.T) {
		srv := newHoldServer(t, http.StatusOK, holdPageWithAux)
		overlayDir, configDir := t.TempDir(), t.TempDir()
		createTestEbuild(t, overlayDir, holdPkg, holdCurrent)
		blockCacheWrite(t, configDir)
		c := heldErrChecker(t, overlayDir, configDir, holdPkg, holdAuxConfig(srv.srv.URL))
		result := checkForHold(t, c, holdPkg, true, false)
		if errors.Is(result.Error, ErrAuxUnresolved) {
			t.Errorf("a bump that was NOT held matches ErrAuxUnresolved: %v", result.Error)
		}
		if got, ok := c.pending.Get(holdPkg); !ok || got.AuxValue != "esr-bb24" {
			t.Errorf("fixture: the resolved bump was not queued with its aux value: ok=%v %+v", ok, got)
		}
	})

	t.Run("not held: the version fetch itself failed", func(t *testing.T) {
		srv := newHoldServer(t, http.StatusNotFound, "not found")
		overlayDir, configDir := t.TempDir(), t.TempDir()
		createTestEbuild(t, overlayDir, holdPkg, holdCurrent)
		c := heldErrChecker(t, overlayDir, configDir, holdPkg, holdAuxConfig(srv.srv.URL))
		result, err := c.CheckPackage(holdPkg, true)
		if err == nil || result == nil || !errors.Is(result.Error, ErrFetchFailed) {
			t.Fatalf("fixture: want a failed fetch, got result=%+v err=%v", result, err)
		}
		if errors.Is(result.Error, ErrAuxUnresolved) {
			t.Errorf("a failed fetch matches ErrAuxUnresolved: %v", result.Error)
		}
	})
}
