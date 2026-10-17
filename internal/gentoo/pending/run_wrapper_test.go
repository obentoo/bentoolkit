//go:build linux

package pending_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/gentoo/pending"
)

// A wrapper that cannot execute the command (it vanished, or lost its
// execute bit) exits 126 or 127. Run must report that as exec.ErrNotFound
// naming the command, so the caller disables the monitor instead of reading
// the exit status as "@world does not resolve".
func TestS099RunWrapperCannotExecuteIsNotFound(t *testing.T) {
	dir := t.TempDir()
	nice := s099Script(t, dir, "nice", `shift 2
exec "$@"`)
	notExec := filepath.Join(dir, "glsa-check")
	if err := os.WriteFile(notExec, []byte("#!/bin/sh\necho ran\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, path string }{
		{"missing command (127)", filepath.Join(dir, "emerge")},
		{"command not executable (126)", notExec},
	} {
		o := s099Run(t, context.Background(), pending.Command{
			Path: tc.path, Timeout: time.Minute, Wrap: []string{nice, "-n", "19"},
		})
		if !errors.Is(o.err, exec.ErrNotFound) {
			t.Errorf("%s: error = %v, want exec.ErrNotFound", tc.name, o.err)
			continue
		}
		if errors.Is(o.err, pending.ErrTimeout) {
			t.Errorf("%s: error %v reads as ErrTimeout", tc.name, o.err)
		}
		if !strings.Contains(o.err.Error(), tc.path) {
			t.Errorf("%s: error %q does not name %s", tc.name, o.err, tc.path)
		}
	}
}

// Without a wrapper, 126 and 127 still say the command never ran: python-exec,
// which emerge and glsa-check run through, exits 127 when the implementation
// for the selected Python is missing. Run reports exec.ErrNotFound naming the
// command, worded so that it does not blame $PATH.
func TestS099RunUnwrappedExit127IsNotFound(t *testing.T) {
	dir := t.TempDir()
	for _, code := range []int{126, 127} {
		name := "emerge" + strconv.Itoa(code)
		p := s099Script(t, dir, name, "exit "+strconv.Itoa(code))
		o := s099Run(t, context.Background(), pending.Command{Path: p, Timeout: time.Minute})
		s099AssertNotRun(t, o.err, name, p, code)
	}
}

// A wrapped command that exits 126 or 127 itself (nice and ionice exec it, so
// their status is its own) gets the same wording.
func TestS099RunWrappedExit127MessageDoesNotBlamePath(t *testing.T) {
	dir := t.TempDir()
	nice := s099Script(t, dir, "nice", `shift 2
exec "$@"`)
	p := s099Script(t, dir, "emerge", "exit 127")
	o := s099Run(t, context.Background(), pending.Command{Path: p, Timeout: time.Minute, Wrap: []string{nice, "-n", "19"}})
	s099AssertNotRun(t, o.err, "wrapped exit 127", p, 127)
}

// GNU nice exits 125 when nice itself failed: behind a Wrap that is a command
// that never ran, an error naming the command and the wrapper.
func TestS099RunWrapperExit125IsNotFound(t *testing.T) {
	dir := t.TempDir()
	nice := s099Script(t, dir, "nice", "exit 125")
	p := s099Script(t, dir, "emerge", "echo ran")
	o := s099Run(t, context.Background(), pending.Command{Path: p, Timeout: time.Minute, Wrap: []string{nice, "-n", "19"}})
	s099AssertNotRun(t, o.err, "wrapper exit 125", p, 125)
	if o.err != nil && !strings.Contains(o.err.Error(), nice) {
		t.Errorf("error %q does not name the wrapper %s", o.err, nice)
	}
}

// Without a wrapper, 125 is the command's own exit status: a Result.
func TestS099RunUnwrappedExit125IsAResult(t *testing.T) {
	dir := t.TempDir()
	p := s099Script(t, dir, "emerge", "exit 125")
	res := s099MustResult(t, s099Run(t, context.Background(), pending.Command{Path: p, Timeout: time.Minute}), "unwrapped exit 125")
	if res.ExitCode != 125 {
		t.Errorf("ExitCode = %d, want 125", res.ExitCode)
	}
}

// s099AssertNotRun checks the error for an exit status that says the command
// never ran: exec.ErrNotFound, not ErrTimeout, naming the command and the
// status, and not blaming $PATH.
func s099AssertNotRun(t *testing.T, err error, what, path string, code int) {
	t.Helper()
	if !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("%s: error = %v, want exec.ErrNotFound", what, err)
		return
	}
	if errors.Is(err, pending.ErrTimeout) {
		t.Errorf("%s: error %v reads as ErrTimeout", what, err)
	}
	msg := err.Error()
	if !strings.Contains(msg, path) {
		t.Errorf("%s: error %q does not name %s", what, msg, path)
	}
	if want := "exit " + strconv.Itoa(code); !strings.Contains(msg, want) {
		t.Errorf("%s: error %q does not carry %q", what, msg, want)
	}
	if strings.Contains(msg, "$PATH") {
		t.Errorf("%s: error %q blames $PATH for a command that was found", what, msg)
	}
}

// A helper the command left running is killed once the command has exited,
// whether it still holds the output pipe or not, and whatever the status.
func TestS099RunSweepsTheGroupAfterANormalExit(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"holding stdout, exit 0", `sleep 60 &
echo $! > "$1"
echo hi`},
		{"holding stdout, exit 3", `sleep 60 &
echo $! > "$1"
exit 3`},
		{"detached from stdout, exit 0", `sleep 60 >/dev/null 2>&1 &
echo $! > "$1"`},
	} {
		dir := t.TempDir()
		pidFile := filepath.Join(dir, "pid")
		p := s099Script(t, dir, "emerge", tc.body)
		o := s099Run(t, context.Background(), pending.Command{Path: p, Args: []string{pidFile}, Timeout: time.Minute})
		if o.err != nil {
			t.Errorf("%s: Run error = %v, want a Result", tc.name, o.err)
		}
		s099AssertGone(t, s099WaitPid(t, pidFile), tc.name+": the helper left behind")
	}
}

// A wrapper that runs the command, which then exits with an ordinary
// non-zero status, still yields a Result.
func TestS099RunWrappedNonZeroExitIsAResult(t *testing.T) {
	dir := t.TempDir()
	nice := s099Script(t, dir, "nice", `shift 2
exec "$@"`)
	p := s099Script(t, dir, "emerge", "exit 1")
	res := s099MustResult(t, s099Run(t, context.Background(), pending.Command{
		Path: p, Timeout: time.Minute, Wrap: []string{nice, "-n", "19"},
	}), "wrapped exit 1")
	if res.ExitCode != 1 {
		t.Errorf("ExitCode = %d, want 1", res.ExitCode)
	}
}

// A ctx already done before the start is the caller's cancel, never a
// timeout, and nothing runs.
func TestS099RunCancelledBeforeStart(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	p := s099Script(t, dir, "emerge", `touch "$1"`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	o := s099Run(t, ctx, pending.Command{Path: p, Args: []string{marker}, Timeout: time.Minute})
	if !errors.Is(o.err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", o.err)
	}
	if errors.Is(o.err, pending.ErrTimeout) {
		t.Errorf("error %v reads as ErrTimeout", o.err)
	}
	if !strings.Contains(o.err.Error(), p) {
		t.Errorf("error %q does not name %s", o.err, p)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the command ran although ctx was done before the start")
	}
}

// A non-positive Timeout sets no deadline of its own: the command runs to
// its end.
func TestS099RunZeroTimeoutSetsNoDeadline(t *testing.T) {
	dir := t.TempDir()
	p := s099Script(t, dir, "emerge", "sleep 0.2\necho done")
	res := s099MustResult(t, s099Run(t, context.Background(), pending.Command{Path: p}), "zero timeout")
	if got := string(res.Stdout); got != "done\n" {
		t.Errorf("Stdout = %q, want %q", got, "done\n")
	}
}
