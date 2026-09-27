package main

// Authored for story 056, sub-task 8.1 (S056-R4.7): exitProcess runs the
// cleanups registered with registerExitCleanup, last registered first, and then
// ends the process with the code it was given. Only a real process exit shows
// that, so each scenario runs in a re-exec'd child and the parent reads what the
// cleanups left behind.

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	s056ExitCleanupRoleEnv = "BENTOO_TEST_S056_EXIT_CLEANUP_ROLE"
	s056ExitCleanupLogEnv  = "BENTOO_TEST_S056_EXIT_CLEANUP_LOG"
	// s056ExitCleanupReturned is the child's own code when exitProcess returned.
	s056ExitCleanupReturned = 97
)

// s056AppendCleanup returns a cleanup that appends name and a newline to log.
func s056AppendCleanup(log, name string) func() {
	return func() {
		f, err := os.OpenFile(log, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
		if err != nil {
			return
		}
		_, _ = f.WriteString(name + "\n")
		_ = f.Close()
	}
}

// TestHelperS056ExitCleanupChild is not a test: in the child role it registers
// the scenario's cleanups and ends through exitProcess.
func TestHelperS056ExitCleanupChild(t *testing.T) {
	role := os.Getenv(s056ExitCleanupRoleEnv)
	if role == "" {
		t.Skip("child role only")
	}
	log := os.Getenv(s056ExitCleanupLogEnv)
	switch role {
	case "lifo":
		registerExitCleanup(s056AppendCleanup(log, "first"))
		registerExitCleanup(s056AppendCleanup(log, "second"))
		exitProcess(3)
	case "zero":
		registerExitCleanup(s056AppendCleanup(log, "only"))
		exitProcess(0)
	case "unregistered":
		registerExitCleanup(s056AppendCleanup(log, "kept"))
		unregister := registerExitCleanup(s056AppendCleanup(log, "dropped"))
		unregister()
		exitProcess(3)
	case "identical":
		// Two registrations of cleanups that do the same thing: unregistering
		// one must not take the other with it.
		registerExitCleanup(s056AppendCleanup(log, "same"))
		unregister := registerExitCleanup(s056AppendCleanup(log, "same"))
		unregister()
		exitProcess(3)
	case "unregister-twice":
		// A second call of the same unregister must not remove a different
		// registration that now sits where the first one was.
		registerExitCleanup(s056AppendCleanup(log, "a"))
		unregister := registerExitCleanup(s056AppendCleanup(log, "b"))
		registerExitCleanup(s056AppendCleanup(log, "c"))
		unregister()
		unregister()
		exitProcess(3)
	}
	os.Exit(s056ExitCleanupReturned)
}

// s056RunExitCleanupChild runs one scenario and returns the child's exit code
// and the lines its cleanups wrote, in order.
func s056RunExitCleanupChild(t *testing.T, role string) (code int, lines []string) {
	t.Helper()
	log := filepath.Join(t.TempDir(), "cleanups.log")
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperS056ExitCleanupChild$", "-test.count=1")
	cmd.Env = append(os.Environ(), s056ExitCleanupRoleEnv+"="+role, s056ExitCleanupLogEnv+"="+log)
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		code = 0
	case errors.As(err, &exitErr):
		code = exitErr.ExitCode()
	default:
		t.Fatalf("running the child: %v", err)
	}
	if code == s056ExitCleanupReturned {
		t.Fatalf("exitProcess returned instead of ending the process:\n%s", out)
	}
	data, err := os.ReadFile(log)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if s := strings.TrimSuffix(string(data), "\n"); s != "" {
		lines = strings.Split(s, "\n")
	}
	return code, lines
}

func s056WantLines(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("cleanups ran as %q, want %q", got, want)
	}
}

// TestExitProcess_RunsCleanupsLastRegisteredFirst: both cleanups run before the
// process ends with the given code, the last registered first.
func TestExitProcess_RunsCleanupsLastRegisteredFirst(t *testing.T) {
	code, lines := s056RunExitCleanupChild(t, "lifo")
	if code != 3 {
		t.Errorf("exitProcess(3) ended the child with %d", code)
	}
	s056WantLines(t, lines, "second", "first")
}

// TestExitProcess_ExitZeroStillRunsCleanups: R4.7 says "any exit code"; a clean
// exit is the one --check takes most often.
func TestExitProcess_ExitZeroStillRunsCleanups(t *testing.T) {
	code, lines := s056RunExitCleanupChild(t, "zero")
	if code != 0 {
		t.Errorf("exitProcess(0) ended the child with %d", code)
	}
	s056WantLines(t, lines, "only")
}

// TestExitProcess_UnregisteredCleanupDoesNotRun: an unregistered cleanup is
// skipped; the other one still runs.
func TestExitProcess_UnregisteredCleanupDoesNotRun(t *testing.T) {
	code, lines := s056RunExitCleanupChild(t, "unregistered")
	if code != 3 {
		t.Errorf("exitProcess(3) ended the child with %d", code)
	}
	s056WantLines(t, lines, "kept")
}

// TestExitProcess_UnregisterRemovesOnlyItsOwnRegistration: the hostile halves
// of "this registration, not another" — two look-alike cleanups must not both
// go, and a repeated unregister must not remove a neighbour.
func TestExitProcess_UnregisterRemovesOnlyItsOwnRegistration(t *testing.T) {
	code, lines := s056RunExitCleanupChild(t, "identical")
	if code != 3 {
		t.Errorf("exitProcess(3) ended the child with %d", code)
	}
	s056WantLines(t, lines, "same")

	code, lines = s056RunExitCleanupChild(t, "unregister-twice")
	if code != 3 {
		t.Errorf("exitProcess(3) ended the child with %d", code)
	}
	s056WantLines(t, lines, "c", "a")
}
