package autoupdate

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// holdServer serves whatever status and body were last set, so one test can
// walk an upstream through several states across checks.
type holdServer struct {
	mu     sync.Mutex
	status int
	body   string
	srv    *httptest.Server
}

func newHoldServer(t *testing.T, status int, body string) *holdServer {
	t.Helper()
	h := &holdServer{status: status, body: body}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		h.mu.Lock()
		status, body := h.status, h.body
		h.mu.Unlock()
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *holdServer) set(status int, body string) {
	h.mu.Lock()
	h.status, h.body = status, body
	h.mu.Unlock()
}

// holdChecker builds a checker over overlayDir/configDir for one package. A new
// checker over the same directories is how the next `bentoo overlay autoupdate`
// run sees the world: the version cache and pending.json come from disk.
func holdChecker(t *testing.T, overlayDir, configDir, pkg string, cfg PackageConfig) *Checker {
	t.Helper()
	c, err := NewChecker(overlayDir,
		WithConfigDir(configDir),
		WithPackagesConfig(&PackagesConfig{Packages: map[string]PackageConfig{pkg: cfg}}),
		WithRateLimiter(unlimitedRateLimiter()),
	)
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}
	return c
}

const (
	holdPkg         = "mail-client/betterbird-bin"
	holdCurrent     = "128.6.0"
	holdUpstream    = "128.7.0"
	holdPageWithAux = "<p>Current version: Betterbird 128.7.0esr-bb24</p>"
	holdPageNoAux   = "<p>Current version: Betterbird 128.7.0</p>"
)

func holdAuxConfig(url string) PackageConfig {
	return PackageConfig{
		Parser:     "regex",
		Pattern:    `Current version:\s*Betterbird\s*([0-9]+\.[0-9]+\.[0-9]+)`,
		AuxVar:     "MY_BUILD",
		AuxPattern: `Current version:\s*Betterbird\s*[0-9.]+(esr-bb[0-9]+)`,
		URL:        url,
	}
}

// checkAndExpectHeld runs one check and asserts S050-R3: an update was seen,
// the check result carries the resolution error, and pending.json holds
// exactly wantPending (nil = no entry at all).
func checkAndExpectHeld(t *testing.T, c *Checker, pkg string, force bool, wantFromCache bool, wantPending *PendingUpdate) {
	t.Helper()
	result, _ := c.CheckPackage(pkg, force)
	if result == nil {
		t.Fatal("CheckPackage returned no result")
	}
	if result.FromCache != wantFromCache {
		t.Fatalf("fixture: FromCache = %v, want %v (wrong CheckPackage block exercised)", result.FromCache, wantFromCache)
	}
	if !result.HasUpdate {
		t.Fatalf("fixture: expected HasUpdate (%s -> upstream), got %+v", holdCurrent, result)
	}
	if result.Error == nil {
		t.Error("check result carries no resolution error for an unresolved configured value")
	}
	got, ok := c.pending.Get(pkg)
	switch {
	case wantPending == nil && ok:
		t.Errorf("bump was queued despite an unresolved value: %+v", *got)
	case wantPending != nil && !ok:
		t.Errorf("the earlier pending entry was removed; want %+v", *wantPending)
	case wantPending != nil && (got.NewVersion != wantPending.NewVersion ||
		got.AuxValue != wantPending.AuxValue || got.CommitHash != wantPending.CommitHash):
		t.Errorf("the earlier pending entry was replaced\n got: %+v\nwant: %+v", *got, *wantPending)
	}
}

// TestCheckPackage_HoldsBumpWhenAuxUnresolved pins S050-R3.1 in both CheckPackage
// blocks: the fresh upstream fetch and the version-cache hit (where the aux page
// is the only fetch, so a 404 surfaces there alone).
func TestCheckPackage_HoldsBumpWhenAuxUnresolved(t *testing.T) {
	t.Run("no capture on a fresh fetch", func(t *testing.T) {
		srv := newHoldServer(t, http.StatusOK, holdPageNoAux)
		overlayDir, configDir := t.TempDir(), t.TempDir()
		createTestEbuild(t, overlayDir, holdPkg, holdCurrent)
		c := holdChecker(t, overlayDir, configDir, holdPkg, holdAuxConfig(srv.srv.URL))
		checkAndExpectHeld(t, c, holdPkg, true, false, nil)
	})

	t.Run("empty capture on a fresh fetch", func(t *testing.T) {
		srv := newHoldServer(t, http.StatusOK, holdPageNoAux)
		overlayDir, configDir := t.TempDir(), t.TempDir()
		createTestEbuild(t, overlayDir, holdPkg, holdCurrent)
		cfg := holdAuxConfig(srv.srv.URL)
		cfg.AuxPattern = `Current version:\s*Betterbird\s*[0-9.]+(\S*)</p>`
		c := holdChecker(t, overlayDir, configDir, holdPkg, cfg)
		checkAndExpectHeld(t, c, holdPkg, true, false, nil)
	})

	t.Run("aux page 404 on a cache hit", func(t *testing.T) {
		srv := newHoldServer(t, http.StatusOK, holdPageNoAux)
		overlayDir, configDir := t.TempDir(), t.TempDir()
		createTestEbuild(t, overlayDir, holdPkg, holdCurrent)
		checkAndExpectHeld(t, holdChecker(t, overlayDir, configDir, holdPkg, holdAuxConfig(srv.srv.URL)),
			holdPkg, true, false, nil)

		srv.set(http.StatusNotFound, "not found")
		checkAndExpectHeld(t, holdChecker(t, overlayDir, configDir, holdPkg, holdAuxConfig(srv.srv.URL)),
			holdPkg, false, true, nil)
	})

	t.Run("an earlier valid entry is left as it is", func(t *testing.T) {
		srv := newHoldServer(t, http.StatusOK, holdPageNoAux)
		overlayDir, configDir := t.TempDir(), t.TempDir()
		createTestEbuild(t, overlayDir, holdPkg, holdCurrent)
		earlier := PendingUpdate{
			Package:        holdPkg,
			CurrentVersion: holdCurrent,
			NewVersion:     "128.6.5",
			AuxValue:       "esr-bb23",
			Status:         StatusPending,
		}
		seed, err := NewPendingList(configDir)
		if err != nil {
			t.Fatalf("NewPendingList: %v", err)
		}
		if err := seed.Add(earlier); err != nil {
			t.Fatalf("seed pending: %v", err)
		}
		c := holdChecker(t, overlayDir, configDir, holdPkg, holdAuxConfig(srv.srv.URL))
		checkAndExpectHeld(t, c, holdPkg, true, false, &earlier)
	})
}

// TestCheckPackage_HoldsBumpWhenAuxSHAUnresolved pins S050-R3.2: a version-tracked
// package with commit_sha_path (cursor's BUILD_ID) whose SHA cannot be read is
// held in both blocks, instead of shipping the previous release's SHA.
func TestCheckPackage_HoldsBumpWhenAuxSHAUnresolved(t *testing.T) {
	const pkg = "app-editors/cursor"
	srv := newHoldServer(t, http.StatusOK, `{"version":"2.0.0"}`)
	overlayDir, configDir := t.TempDir(), t.TempDir()
	createTestEbuild(t, overlayDir, pkg, "1.0.0")
	cfg := PackageConfig{
		Parser:        "json",
		Path:          "version",
		CommitSHAPath: "commitSha",
		URL:           srv.srv.URL,
	}

	// Fresh fetch: the version parses, the SHA path is absent.
	checkAndExpectHeld(t, holdChecker(t, overlayDir, configDir, pkg, cfg), pkg, true, false, nil)

	// Cache hit: the SHA fetch is the only fetch, and it 404s.
	srv.set(http.StatusNotFound, "not found")
	checkAndExpectHeld(t, holdChecker(t, overlayDir, configDir, pkg, cfg), pkg, false, true, nil)
}

// TestCheckPackage_HeldBumpResolvesOnNextCheck pins S050-R3.3: once upstream
// publishes the aux value, the next check queues the bump with it, served from
// the version cache and with no cache clearing by the operator.
func TestCheckPackage_HeldBumpResolvesOnNextCheck(t *testing.T) {
	srv := newHoldServer(t, http.StatusOK, holdPageNoAux)
	overlayDir, configDir := t.TempDir(), t.TempDir()
	createTestEbuild(t, overlayDir, holdPkg, holdCurrent)
	checkAndExpectHeld(t, holdChecker(t, overlayDir, configDir, holdPkg, holdAuxConfig(srv.srv.URL)),
		holdPkg, true, false, nil)

	srv.set(http.StatusOK, holdPageWithAux)
	c := holdChecker(t, overlayDir, configDir, holdPkg, holdAuxConfig(srv.srv.URL))
	result, _ := c.CheckPackage(holdPkg, false)
	if result == nil || !result.FromCache || !result.HasUpdate {
		t.Fatalf("fixture: want a cache-hit check that sees the update, got %+v", result)
	}
	got, ok := c.pending.Get(holdPkg)
	if !ok {
		t.Fatal("the held bump was not queued once its aux value resolved")
	}
	if got.NewVersion != holdUpstream || got.AuxValue != "esr-bb24" {
		t.Errorf("pending = {NewVersion:%q AuxValue:%q}, want {%q %q}", got.NewVersion, got.AuxValue, holdUpstream, "esr-bb24")
	}
}
