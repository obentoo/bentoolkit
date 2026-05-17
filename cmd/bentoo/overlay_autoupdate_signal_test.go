package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
)

// TestRunAutoupdate_SignalCancels verifies R3.1: a SIGTERM delivered while
// `bentoo overlay autoupdate --check` is doing in-flight upstream work cancels
// that work and the command returns promptly (well within 2 s of the signal).
//
// The test is intentionally IN-PROCESS — it delivers the signal to its own PID
// rather than building and running a child binary — so it stays portable
// across CI environments. runAutoupdate wires signalContext (signal.NotifyContext)
// for the duration of the run; while that handler is installed the SIGTERM is
// caught (the test process is NOT terminated) and only cancels the run context.
// runAutoupdate's deferred stop() restores default signal behaviour on return.
//
// Skipped on Windows: SIGTERM and syscall.Kill have no portable semantics there.
func TestRunAutoupdate_SignalCancels(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGTERM / syscall.Kill is not portable on Windows")
	}

	// requestStarted is closed once the first upstream check actually reaches
	// the server, guaranteeing the signal lands while work is genuinely in
	// flight (not before NewChecker, not after CheckAll returned).
	requestStarted := make(chan struct{})
	var once sync.Once

	// The handler blocks for far longer than the test's deadline. When the run
	// context is cancelled the HTTP transport aborts the in-flight request, so
	// the handler goroutine is abandoned — that is fine for an in-process test.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(requestStarted) })
		select {
		case <-r.Context().Done():
			// Client (the cancelled checker) hung up — return immediately.
		case <-time.After(30 * time.Second):
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"version": "1.0.0"})
	}))
	defer server.Close()

	overlayDir, cleanup := setupTestHome(t)
	defer cleanup()

	// Declare enough packages (with on-disk ebuilds) that the check has real,
	// long-running work to cancel.
	pkgs := []string{
		"cat-a/pkg1", "cat-a/pkg2", "cat-a/pkg3", "cat-a/pkg4",
		"cat-b/pkg5", "cat-b/pkg6", "cat-b/pkg7", "cat-b/pkg8",
	}
	writeExitTestPackagesConfig(t, overlayDir, server.URL, pkgs)
	for _, pkg := range pkgs {
		writeExitTestEbuild(t, overlayDir, pkg, "0.9.0")
	}

	// The autoupdate config dir lives under the test HOME (set by setupTestHome).
	autoupdateConfigDir := filepath.Join(os.Getenv("HOME"), ".config", "bentoo", "autoupdate")
	if err := os.MkdirAll(autoupdateConfigDir, 0o755); err != nil {
		t.Fatalf("mkdir autoupdate config dir: %v", err)
	}

	// Pin the autoupdate flag globals to a known state for this run.
	origCheck, origForce, origConc := autoupdateCheck, autoupdateForce, autoupdateConcurrency
	autoupdateCheck = true // select the --check path
	autoupdateForce = true // bypass cache so every pkg hits the server
	autoupdateConcurrency = autoupdate.DefaultConcurrency
	defer func() {
		autoupdateCheck, autoupdateForce, autoupdateConcurrency = origCheck, origForce, origConc
	}()

	// Run the command in a goroutine. withExitIntercept absorbs the osExit call
	// that runCheck makes on completion, so the goroutine returns normally.
	done := make(chan struct{})
	go func() {
		defer close(done)
		withExitIntercept(func() { runAutoupdate(autoupdateCmd, nil) })
	}()

	// Wait until in-flight work has genuinely started before signalling.
	select {
	case <-requestStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("upstream check never started; cannot exercise signal cancellation")
	}

	// Deliver SIGTERM to this process. runAutoupdate's signal.NotifyContext
	// handler catches it and cancels the run context instead of terminating.
	signalAt := time.Now()
	proc, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("FindProcess(self): %v", err)
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("sending SIGTERM to self: %v", err)
	}

	// The command must return within ~2 s of the signal (R3.1).
	select {
	case <-done:
		if elapsed := time.Since(signalAt); elapsed > 2*time.Second {
			t.Errorf("runAutoupdate returned %v after SIGTERM; want <= 2s", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runAutoupdate did not return within 5s of SIGTERM; signal cancellation is not wired")
	}
}
