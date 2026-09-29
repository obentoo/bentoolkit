package main

// Authored for story 056, sub-task 8.1 (S056-R4.7): the overlay lock file is
// removed before the process exits, including when the mode ends through the
// PRODUCTION osExit. The in-process intercept of that time made osExit panic,
// which unwound the deferred Release; that is how the earlier tests missed the gap.
// Here a re-exec'd child runs the real tree and ends through exitProcess exactly
// as the binary does, and the parent inspects the overlay after the child is gone.
//
// Since story 058 the child ends the way func main does: the command tree runs
// through func runMain (or func execute, for the cancelled role), every
// deferred call inside the handler runs as it returns, and only then does func
// exitProcess end the process with the mapped code.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

const (
	// s056ProdExitRoleEnv selects the child role and names its scenario.
	s056ProdExitRoleEnv = "BENTOO_TEST_S056_PRODUCTION_EXIT_ROLE"
	// s056ProdExitReturned is the exit code the child used when runAutoupdate
	// returned instead of ending the process. The child now ends through func
	// exitProcess, which never picks it, so seeing it means the child did not
	// run the production exit path.
	s056ProdExitReturned = 97
)

// TestHelperS056ProductionExitChild is not a test: in the child role it runs
// `overlay autoupdate --check` on the fixture its parent built (HOME comes from
// the environment) through a fresh command tree and ends the process the way
// func main does, so the process ends only after the handler has returned.
func TestHelperS056ProductionExitChild(t *testing.T) {
	role := os.Getenv(s056ProdExitRoleEnv)
	if role == "" {
		t.Skip("child role only")
	}
	root := newRootCmd()
	root.SetArgs([]string{"overlay", "autoupdate", "--check"})
	if role == "cancelled" {
		// Already cancelled, as SIGINT/SIGTERM would leave the run's context.
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		exitProcess(execute(ctx, root, os.Stderr))
	}
	exitProcess(runMain(root))
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
			t.Fatalf("the child was killed by a signal (%v), not ended by exitProcess:\n%s", err, data)
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
		t.Fatalf("runAutoupdate returned instead of ending the process through exitProcess; the scenario did not exercise the exit path:\n%s", out)
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
		t.Errorf("%s is still on disk after the child ended through the production exitProcess with code %d (holds %q): the lock was not removed before the process exited", lockPath, code, data)
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("inspecting %s: %v", lockPath, err)
	}
}

// TestAutoupdateOverlayLock_RemovedOnProductionExit: R4.7 — a --check ends in
// exitProcess (exit 0 included); the lock file is gone once the process is.
func TestAutoupdateOverlayLock_RemovedOnProductionExit(t *testing.T) {
	code, out, overlay, witness := s056RunProductionExitChild(t, "plain")
	s056AssertLockGoneAfterProductionExit(t, code, 0, out, overlay, witness)
}

// TestAutoupdateOverlayLock_RemovedOnProductionExitAfterCancel: R4.7's signal
// clause — the run's context is already cancelled (as SIGINT/SIGTERM would
// leave it) when the run ends through exitProcess; the lock file is still removed.
func TestAutoupdateOverlayLock_RemovedOnProductionExitAfterCancel(t *testing.T) {
	code, out, overlay, witness := s056RunProductionExitChild(t, "cancelled")
	s056AssertLockGoneAfterProductionExit(t, code, 0, out, overlay, witness)
}
