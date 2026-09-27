package main

// Authored for story 056, sub-task 8.1 (S056-R4.7): the overlay lock file is
// removed before the process exits, including when the mode ends through the
// PRODUCTION osExit. The in-process withExitIntercept makes osExit panic, which
// unwinds the deferred Release; that is how the earlier tests missed the gap.
// Here a re-exec'd child runs runAutoupdate with osExit left exactly as the
// binary ships it, and the parent inspects the overlay after the child is gone.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
)

const (
	// s056ProdExitRoleEnv selects the child role and names its scenario.
	s056ProdExitRoleEnv = "BENTOO_TEST_S056_PRODUCTION_EXIT_ROLE"
	// s056ProdExitReturned is the child's own exit code when runAutoupdate
	// returned instead of ending the process: the scenario then never reached
	// osExit, and says nothing about it.
	s056ProdExitReturned = 97
)

// TestHelperS056ProductionExitChild is not a test: in the child role it runs a
// --check on the fixture its parent built (HOME comes from the environment) with
// the production osExit, so the process ends wherever the mode ends it.
func TestHelperS056ProductionExitChild(t *testing.T) {
	role := os.Getenv(s056ProdExitRoleEnv)
	if role == "" {
		t.Skip("child role only")
	}
	autoupdateLint, autoupdateFix, autoupdateList = false, false, false
	autoupdateConcurrency = autoupdate.DefaultConcurrency
	autoupdateTimeout, autoupdateOnly, autoupdateApply = 0, "", ""
	autoupdateCheck = true
	if role == "cancelled" {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		autoupdateCmd.SetContext(ctx)
	}
	runAutoupdate(autoupdateCmd, nil)
	os.Exit(s056ProdExitReturned)
}

// s056RunProductionExitChild builds the --check fixture, plants a dead
// writer's temp as a witness that the child got past the lock (the sweep runs
// only once the lock is held), runs the child and returns its exit code, its
// output and the overlay.
func s056RunProductionExitChild(t *testing.T, role string) (code int, out, overlay, witness string) {
	t.Helper()
	overlay, _ = s056AutoupdateEnv(t)
	witness = filepath.Join(overlay, ".autoupdate", ".packages.toml.bentoo-"+strconv.Itoa(s056DeadPID(t))+"-exitwitness")
	if err := os.WriteFile(witness, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperS056ProductionExitChild$", "-test.count=1")
	cmd.Env = append(os.Environ(), s056ProdExitRoleEnv+"="+role)
	cmd.Stdin = nil // /dev/null: no registry-fix prompt
	data, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		code = 0
	case errors.As(err, &exitErr):
		code = exitErr.ExitCode()
		if code < 0 {
			t.Fatalf("the child was killed by a signal (%v), not ended by osExit:\n%s", err, data)
		}
	default:
		t.Fatalf("running the child: %v", err)
	}
	return code, string(data), overlay, witness
}

// s056AssertLockGoneAfterProductionExit holds the checks both scenarios share.
func s056AssertLockGoneAfterProductionExit(t *testing.T, code, wantCode int, out, overlay, witness string) {
	t.Helper()
	if code == s056ProdExitReturned {
		t.Fatalf("runAutoupdate returned instead of ending the process through osExit; the scenario did not exercise the exit path:\n%s", out)
	}
	if code != wantCode {
		t.Errorf("the child exited %d, want %d (the code the --check chose):\n%s", code, wantCode, out)
	}
	if _, err := os.Lstat(witness); err == nil {
		t.Fatalf("the dead writer's temp %s survived: the child never took the overlay lock, so its absence proves nothing:\n%s", witness, out)
	}
	lockPath := filepath.Join(overlay, ".autoupdate.bentoo-lock")
	if _, err := os.Lstat(lockPath); err == nil {
		data, _ := os.ReadFile(lockPath)
		t.Errorf("%s is still on disk after the child ended through the production osExit with code %d (holds %q): the lock was not removed before the process exited", lockPath, code, data)
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("inspecting %s: %v", lockPath, err)
	}
}

// TestAutoupdateOverlayLock_RemovedOnProductionExit: R4.7 — a --check ends in
// osExit (exit 0 included); the lock file is gone once the process is.
func TestAutoupdateOverlayLock_RemovedOnProductionExit(t *testing.T) {
	code, out, overlay, witness := s056RunProductionExitChild(t, "plain")
	s056AssertLockGoneAfterProductionExit(t, code, 0, out, overlay, witness)
}

// TestAutoupdateOverlayLock_RemovedOnProductionExitAfterCancel: R4.7's signal
// clause — the run's context is already cancelled (as SIGINT/SIGTERM would
// leave it) when the mode ends through osExit; the lock file is still removed.
func TestAutoupdateOverlayLock_RemovedOnProductionExitAfterCancel(t *testing.T) {
	code, out, overlay, witness := s056RunProductionExitChild(t, "cancelled")
	s056AssertLockGoneAfterProductionExit(t, code, 0, out, overlay, witness)
}
