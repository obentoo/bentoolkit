package main

import (
	"context"
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
	"github.com/spf13/cobra"
)

// setProcessSignalContext gives cmd the context func runMain would: the
// process-wide one from func processContext, carrying the policy the root's
// PersistentPreRunE sets from the command's own annotation. runAutoupdate
// installs no signal handler of its own since story 058 — it reads this
// context through func commandContext — so a test that signals its own process
// without it would meet the default action and kill the test binary. The
// cleanup hands the command back a plain context, so a caller that reuses its
// tree after the test sees no stale signal policy.
func setProcessSignalContext(t *testing.T, cmd *cobra.Command) {
	t.Helper()
	ctx, stop, policy := processContext()
	t.Cleanup(stop)
	policy.setFrom(cmd)
	cmd.SetContext(withSignalPolicy(ctx, policy))
	t.Cleanup(func() { cmd.SetContext(context.Background()) })
}

// TestRunAutoupdate_SignalCancels verifies R3.1: a SIGTERM delivered while
// `bentoo overlay autoupdate --check` is doing in-flight upstream work cancels
// that work and the command returns promptly (well within 2 s of the signal).
//
// The test is intentionally IN-PROCESS — it delivers the signal to its own PID
// rather than building and running a child binary — so it stays portable
// across CI environments. The test installs the process-wide handler func
// runMain would (func setProcessSignalContext); overlay autoupdate is
// cancellable, so the SIGTERM is caught (the test process is NOT terminated)
// and only cancels the run context runAutoupdate reads through func
// commandContext.
//
// Skipped on Windows: SIGTERM and syscall.Kill have no portable semantics there.
func TestRunAutoupdate_SignalCancels(t *testing.T) {
	auCmd := testAutoupdateCmd()
	auOpts := testAutoupdateOptions()
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

	overlayDir := setupTestHome(t)

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
	auOpts.check = true // select the --check path
	auOpts.force = true // bypass cache so every pkg hits the server
	auOpts.concurrency = autoupdate.DefaultConcurrency

	setProcessSignalContext(t, auCmd)

	// Run the command in a goroutine; runAutoupdate returns its outcome, so the
	// goroutine returns normally.
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = runAutoupdate(auCmd, nil, auOpts, defaultDeps())
	}()

	// Wait until in-flight work has genuinely started before signalling.
	select {
	case <-requestStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("upstream check never started; cannot exercise signal cancellation")
	}

	// Deliver SIGTERM to this process. The process-wide handler catches it and
	// cancels the run context instead of terminating.
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
