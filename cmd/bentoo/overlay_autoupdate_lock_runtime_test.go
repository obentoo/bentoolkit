package main

// Written at run time for story 056, sub-task 5.1: S056-R4.6 (the registry-fix
// loop runs under the overlay lock) and the signal half of S056-R4.7 (the lock
// is gone after a run cancelled by SIGINT). Both were written after the lock
// existed, so their non-vacuity was shown by mutation (the lock acquisition
// disabled) rather than by a Red before the implementation — see the deviation
// register.

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/filelock"
)

// lockProbingFixer is a registry fixer that, instead of editing anything, tries
// to take the overlay lock and records what happened.
type lockProbingFixer struct {
	lockPath string
	mu       sync.Mutex
	calls    int
	lockErr  error
}

func (f *lockProbingFixer) FixRegistry(_ context.Context, _ autoupdate.RegistryFixRequest) (autoupdate.RegistryFixResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	lock, err := filelock.Acquire(f.lockPath, "probe from inside the registry fixer")
	if err == nil {
		lock.Release()
	}
	f.lockErr = err
	return autoupdate.RegistryFixResult{}, errors.New("probe fixer edits nothing")
}

// TestAutoupdateOverlayLock_HeldAcrossRegistryFixer: R4.6. A --check whose
// package fails extraction offers the interactive registry fix; the fixer runs
// while the run still holds the overlay lock, so an Acquire from inside it
// times out with ErrLocked.
func TestAutoupdateOverlayLock_HeldAcrossRegistryFixer(t *testing.T) {
	// 200 OK without the "version" field: extraction fails, a real ErrFetchFailed.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"other": "1.0.0"})
	}))
	t.Cleanup(server.Close)

	overlayDir := setupTestHome(t)
	const pkg = "app-misc/probe"
	writeExitTestPackagesConfig(t, overlayDir, server.URL, []string{pkg})
	writeExitTestEbuild(t, overlayDir, pkg, "0.9.0")

	oldWait, oldPoll := filelock.Wait, filelock.Poll
	filelock.Wait, filelock.Poll = 200*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { filelock.Wait, filelock.Poll = oldWait, oldPoll })

	fixer := &lockProbingFixer{lockPath: filepath.Join(overlayDir, ".autoupdate.bentoo-lock")}
	oldFixerFn, oldInteractive := checkRegistryFixerFn, checkInteractiveFn
	checkRegistryFixerFn = func(config.LLMConfig) (autoupdate.RegistryFixer, error) { return fixer, nil }
	checkInteractiveFn = func() bool { return true }
	t.Cleanup(func() { checkRegistryFixerFn, checkInteractiveFn = oldFixerFn, oldInteractive })

	// The prompt reads os.Stdin: answer "y" to the one package offered.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString("y\n"); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	oldStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = oldStdin; _ = r.Close() })

	origCheck, origForce, origConc := autoupdateCheck, autoupdateForce, autoupdateConcurrency
	autoupdateCheck, autoupdateForce, autoupdateConcurrency = true, true, autoupdate.DefaultConcurrency
	t.Cleanup(func() { autoupdateCheck, autoupdateForce, autoupdateConcurrency = origCheck, origForce, origConc })

	withExitIntercept(func() { runAutoupdate(autoupdateCmd, nil) })

	fixer.mu.Lock()
	defer fixer.mu.Unlock()
	if fixer.calls == 0 {
		t.Fatal("the registry fixer was never offered the failing package; the test did not reach R4.6's subject")
	}
	if !errors.Is(fixer.lockErr, filelock.ErrLocked) {
		t.Errorf("an Acquire of the overlay lock from inside the registry fixer returned %v, want filelock.ErrLocked: the fix loop ran outside the run's lock", fixer.lockErr)
	}
}

// TestAutoupdateOverlayLock_ReleasedAfterSignal: R4.7's signal half. A --check
// cancelled by SIGINT mid-flight returns, and the overlay lock file is gone.
func TestAutoupdateOverlayLock_ReleasedAfterSignal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGINT / syscall.Kill is not portable on Windows")
	}
	started := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(started) })
		select {
		case <-r.Context().Done():
		case <-time.After(30 * time.Second):
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"version": "1.0.0"})
	}))
	t.Cleanup(server.Close)

	overlayDir := setupTestHome(t)
	pkgs := []string{"cat-a/pkg1", "cat-a/pkg2"}
	writeExitTestPackagesConfig(t, overlayDir, server.URL, pkgs)
	for _, pkg := range pkgs {
		writeExitTestEbuild(t, overlayDir, pkg, "0.9.0")
	}
	lockPath := filepath.Join(overlayDir, ".autoupdate.bentoo-lock")

	origCheck, origForce, origConc := autoupdateCheck, autoupdateForce, autoupdateConcurrency
	autoupdateCheck, autoupdateForce, autoupdateConcurrency = true, true, autoupdate.DefaultConcurrency
	t.Cleanup(func() { autoupdateCheck, autoupdateForce, autoupdateConcurrency = origCheck, origForce, origConc })

	done := make(chan struct{})
	go func() {
		defer close(done)
		withExitIntercept(func() { runAutoupdate(autoupdateCmd, nil) })
	}()

	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the upstream check never started; cannot cancel it")
	}
	if _, err := os.Lstat(lockPath); err != nil {
		t.Fatalf("the overlay lock is not held while the check runs: %v", err)
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatalf("sending SIGINT to self: %v", err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runAutoupdate did not return within 5s of SIGINT")
	}
	if _, err := os.Lstat(lockPath); err == nil {
		t.Errorf("%s is still on disk after the SIGINT-cancelled run returned", lockPath)
	}
}
