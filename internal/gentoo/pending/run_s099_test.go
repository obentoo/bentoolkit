//go:build linux

package pending_test

// Story 099, sub-task 3.1: pending.Run runs one Portage command behind its
// priority wrappers, in a process group of its own, with capped output, a
// timeout and a ctx cancel that both kill the whole group (R1.6, R1.7, R1.8,
// R2.1, R2.11, R3.1). Every test runs REAL processes: fake POSIX sh scripts in
// t.TempDir(), never the host's emerge or glsa-check (Q18).
//
// Hostile halves first: a non-zero exit must not become an error (it is
// "@world does not resolve", not a failed check), and a start failure must
// not pass for a Result; a timeout must be ErrTimeout and a quit must not; the
// kill must reach a grandchild, not only the direct child; capping stdout
// must not break or block the child that is still writing.

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/gentoo/pending"
)

const (
	s099StdoutCap = 8 << 20
	s099StderrCap = 64 << 10
	s099HardStop  = 60 * time.Second // a hung Run fails the test instead of the suite
	s099KillBound = 5 * time.Second  // generous: R1.7 asks the tray to exit within 2 s
)

// s099Script writes an executable #!/bin/sh script named name into dir.
func s099Script(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatalf("writing fake %s: %v", name, err)
	}
	return p
}

type s099Outcome struct {
	res     pending.Result
	err     error
	elapsed time.Duration
}

// s099Run calls pending.Run in a goroutine and fails the test if it hangs.
func s099Run(t *testing.T, ctx context.Context, c pending.Command) s099Outcome {
	t.Helper()
	ch := make(chan s099Outcome, 1)
	start := time.Now()
	go func() {
		res, err := pending.Run(ctx, c)
		ch <- s099Outcome{res, err, time.Since(start)}
	}()
	select {
	case o := <-ch:
		return o
	case <-time.After(s099HardStop):
		t.Fatalf("pending.Run(%+v) did not return within %v", c, s099HardStop)
		return s099Outcome{}
	}
}

func s099MustResult(t *testing.T, o s099Outcome, what string) pending.Result {
	t.Helper()
	if o.err != nil {
		t.Fatalf("%s: Run error = %v, want a Result", what, o.err)
	}
	return o.res
}

// s099Alive reports whether pid is a running, non-zombie process.
func s099Alive(pid int) bool {
	if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
		return false
	}
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return !errors.Is(err, fs.ErrNotExist)
	}
	if i := bytes.LastIndexByte(stat, ')'); i >= 0 && i+2 < len(stat) {
		return stat[i+2] != 'Z'
	}
	return true
}

// s099AssertGone polls, bounded: a killed orphan is reaped by init, not us.
func s099AssertGone(t *testing.T, pid int, what string) {
	t.Helper()
	deadline := time.Now().Add(s099KillBound)
	for s099Alive(pid) {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Errorf("%s (pid %d) still runs after Run returned: the kill did not reach the whole group", what, pid)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// s099WaitPid waits for the fake to write a pid into path.
func s099WaitPid(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(s099HardStop)
	for {
		b, err := os.ReadFile(path)
		if err == nil && bytes.HasSuffix(b, []byte("\n")) {
			pid, perr := strconv.Atoi(strings.TrimSpace(string(b)))
			if perr != nil {
				t.Fatalf("pid file %s holds %q: %v", path, b, perr)
			}
			return pid
		}
		if time.Now().After(deadline) {
			t.Fatalf("fake never wrote its grandchild pid to %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// s099Hanger is a fake that starts a grandchild, records its pid in $1, and
// waits for it: only a group kill stops both.
const s099Hanger = `sleep 60 &
echo $! > "$1"
wait`

// TestS099RunNonZeroExitIsAResultNotAnError is R2.8's input: a command that
// ran and exited 3 is a Result carrying the code and both outputs.
func TestS099RunNonZeroExitIsAResultNotAnError(t *testing.T) {
	dir := t.TempDir()
	p := s099Script(t, dir, "emerge", `echo "[ebuild     U  ] a/b-1"
echo "emerge: there are no ebuilds to satisfy" >&2
exit 3`)
	res := s099MustResult(t, s099Run(t, context.Background(), pending.Command{Path: p, Timeout: time.Minute}), "exit 3")
	if res.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3", res.ExitCode)
	}
	if got := string(res.Stdout); got != "[ebuild     U  ] a/b-1\n" {
		t.Errorf("Stdout = %q", got)
	}
	if got := string(res.Stderr); got != "emerge: there are no ebuilds to satisfy\n" {
		t.Errorf("Stderr = %q", got)
	}
}

// TestS099RunExitZeroKeepsStdoutAndStderrApart: stderr text (the renice
// warning PORTAGE_NICENESS prints) never lands in Stdout.
func TestS099RunExitZeroKeepsStdoutAndStderrApart(t *testing.T) {
	dir := t.TempDir()
	p := s099Script(t, dir, "emerge", `echo "renice: failed to set priority: Permission denied" >&2
echo "[ebuild     U  ] dev-qt/qtbase-6.10.1:6::gentoo"`)
	res := s099MustResult(t, s099Run(t, context.Background(), pending.Command{Path: p, Timeout: time.Minute}), "exit 0")
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", res.ExitCode)
	}
	if got := string(res.Stdout); got != "[ebuild     U  ] dev-qt/qtbase-6.10.1:6::gentoo\n" {
		t.Errorf("Stdout = %q, want the merge line only", got)
	}
	if got := string(res.Stderr); got != "renice: failed to set priority: Permission denied\n" {
		t.Errorf("Stderr = %q, want the renice warning", got)
	}
}

// TestS099RunArgsReachTheCommandUnsplit: no shell sits between Run and the
// command, so an argument with blanks or shell syntax arrives as one word and
// is never executed.
func TestS099RunArgsReachTheCommandUnsplit(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "injected")
	p := s099Script(t, dir, "glsa-check", `for a in "$@"; do printf '<%s>\n' "$a"; done`)
	args := []string{"--list", "a b", "x;y", "$(touch " + marker + ")"}
	res := s099MustResult(t, s099Run(t, context.Background(), pending.Command{Path: p, Args: args, Timeout: time.Minute}), "args")
	want := "<--list>\n<a b>\n<x;y>\n<$(touch " + marker + ")>\n"
	if got := string(res.Stdout); got != want {
		t.Errorf("argv seen by the command = %q, want %q", got, want)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("an argument was evaluated by a shell")
	}
}

// TestS099RunWrapIsPrependedBeforeTheCommand is R1.6: Wrap runs first, in
// order, with the command path and arguments after it.
func TestS099RunWrapIsPrependedBeforeTheCommand(t *testing.T) {
	dir := t.TempDir()
	niceLog, ioniceLog := filepath.Join(dir, "nice.argv"), filepath.Join(dir, "ionice.argv")
	nice := s099Script(t, dir, "nice", `printf '%s\n' "$@" > `+niceLog+`
shift 2
exec "$@"`)
	ionice := s099Script(t, dir, "ionice", `printf '%s\n' "$@" > `+ioniceLog+`
shift 2
exec "$@"`)
	target := s099Script(t, dir, "emerge", `echo "ran $*"`)
	c := pending.Command{
		Path:    target,
		Args:    []string{"--pretend", "@world"},
		Timeout: time.Minute,
		Wrap:    []string{nice, "-n", "19", ionice, "-c", "3"},
	}
	res := s099MustResult(t, s099Run(t, context.Background(), c), "wrapped")
	if got := string(res.Stdout); got != "ran --pretend @world\n" {
		t.Errorf("Stdout = %q, want the command's own output", got)
	}
	for _, tc := range []struct{ log, want string }{
		{niceLog, strings.Join([]string{"-n", "19", ionice, "-c", "3", target, "--pretend", "@world"}, "\n") + "\n"},
		{ioniceLog, strings.Join([]string{"-c", "3", target, "--pretend", "@world"}, "\n") + "\n"},
	} {
		b, err := os.ReadFile(tc.log)
		if err != nil {
			t.Errorf("wrapper never ran (%s): %v", filepath.Base(tc.log), err)
			continue
		}
		if string(b) != tc.want {
			t.Errorf("%s = %q, want %q", filepath.Base(tc.log), b, tc.want)
		}
	}
}

// TestS099RunEmptyWrapRunsThePathDirectly: with neither wrapper present the
// command is the test process's own child.
func TestS099RunEmptyWrapRunsThePathDirectly(t *testing.T) {
	dir := t.TempDir()
	p := s099Script(t, dir, "emerge", `echo "$PPID $*"`)
	res := s099MustResult(t, s099Run(t, context.Background(), pending.Command{Path: p, Args: []string{"x"}, Timeout: time.Minute}), "unwrapped")
	if got, want := string(res.Stdout), strconv.Itoa(os.Getpid())+" x\n"; got != want {
		t.Errorf("Stdout = %q, want %q (parent pid and args)", got, want)
	}
}

// TestS099RunLeadsItsOwnProcessGroup is R1.6's group: the command leads a new
// process group, not the tray's.
func TestS099RunLeadsItsOwnProcessGroup(t *testing.T) {
	dir := t.TempDir()
	nice := s099Script(t, dir, "nice", `shift 2
exec "$@"`)
	p := s099Script(t, dir, "emerge", `echo "$$ $(cut -d' ' -f5 /proc/$$/stat)"`)
	res := s099MustResult(t, s099Run(t, context.Background(), pending.Command{Path: p, Timeout: time.Minute, Wrap: []string{nice, "-n", "19"}}), "group")
	f := strings.Fields(string(res.Stdout))
	if len(f) != 2 {
		t.Fatalf("Stdout = %q, want \"<pid> <pgid>\"", res.Stdout)
	}
	if f[0] != f[1] {
		t.Errorf("command pid %s, pgid %s: it does not lead its own group", f[0], f[1])
	}
	if f[1] == strconv.Itoa(syscall.Getpgrp()) {
		t.Errorf("command runs in the test's process group %s", f[1])
	}
}

// TestS099RunEnvironmentAndStdin is R2.1: no colour, a dumb terminal, the
// rest of the environment kept, and standard input /dev/null, not a terminal.
func TestS099RunEnvironmentAndStdin(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NOCOLOR", "false")
	t.Setenv("S099_KEEP", "kept")
	dir := t.TempDir()
	p := s099Script(t, dir, "emerge", `echo "NOCOLOR=$NOCOLOR"
echo "TERM=$TERM"
echo "S099_KEEP=$S099_KEEP"
echo "stdin=$(readlink /proc/$$/fd/0)"
if [ -t 0 ]; then echo tty; else echo notty; fi`)
	res := s099MustResult(t, s099Run(t, context.Background(), pending.Command{Path: p, Timeout: time.Minute}), "env")
	want := "NOCOLOR=true\nTERM=dumb\nS099_KEEP=kept\nstdin=/dev/null\nnotty\n"
	if got := string(res.Stdout); got != want {
		t.Errorf("environment seen by the command:\n%s\nwant:\n%s", got, want)
	}
}

func s099Pattern(n int) []byte {
	line := []byte("0123456789abcde\n")
	return bytes.Repeat(line, n/len(line)+1)[:n]
}

// TestS099RunCapsStdoutWithoutBreakingTheChild: output past 8 MiB is cut, the
// child still finishes normally (no full pipe, no SIGPIPE), and output at the
// cap is kept whole.
func TestS099RunCapsStdoutWithoutBreakingTheChild(t *testing.T) {
	for _, tc := range []struct {
		name    string
		written int
	}{
		{"over the cap", 9 << 20},
		{"at the cap", s099StdoutCap},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			p := s099Script(t, dir, "emerge", `yes 0123456789abcde | head -c `+strconv.Itoa(tc.written)+`
echo "head=$?" >&2`)
			res := s099MustResult(t, s099Run(t, context.Background(), pending.Command{Path: p, Timeout: time.Minute}), tc.name)
			if res.ExitCode != 0 {
				t.Errorf("ExitCode = %d, want 0", res.ExitCode)
			}
			if got := string(res.Stderr); got != "head=0\n" {
				t.Errorf("Stderr = %q, want head=0: the writer was broken by the cap", got)
			}
			want := min(tc.written, s099StdoutCap)
			if tc.written > s099StdoutCap {
				if len(res.Stdout) == 0 || len(res.Stdout) > s099StdoutCap {
					t.Fatalf("len(Stdout) = %d, want 1..%d", len(res.Stdout), s099StdoutCap)
				}
				want = len(res.Stdout)
			} else if len(res.Stdout) != want {
				t.Fatalf("len(Stdout) = %d, want %d kept whole", len(res.Stdout), want)
			}
			if !bytes.Equal(res.Stdout, s099Pattern(want)) {
				t.Error("Stdout is not the start of what the command wrote")
			}
		})
	}
}

// TestS099RunCapsStderr: stderr past 64 KiB is cut and stdout is untouched.
func TestS099RunCapsStderr(t *testing.T) {
	dir := t.TempDir()
	p := s099Script(t, dir, "emerge", `yes 0123456789abcde | head -c 102400 >&2
echo out`)
	res := s099MustResult(t, s099Run(t, context.Background(), pending.Command{Path: p, Timeout: time.Minute}), "stderr")
	if len(res.Stderr) == 0 || len(res.Stderr) > s099StderrCap {
		t.Fatalf("len(Stderr) = %d, want 1..%d", len(res.Stderr), s099StderrCap)
	}
	if !bytes.Equal(res.Stderr, s099Pattern(len(res.Stderr))) {
		t.Error("Stderr is not the start of what the command wrote")
	}
	if got := string(res.Stdout); got != "out\n" {
		t.Errorf("Stdout = %q, want %q", got, "out\n")
	}
}

// TestS099RunTimeoutKillsTheWholeGroup is R1.8: past its timeout the command
// and its grandchild are killed and the error is ErrTimeout naming the
// command.
func TestS099RunTimeoutKillsTheWholeGroup(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "grandchild.pid")
	p := s099Script(t, dir, "emerge", s099Hanger)
	o := s099Run(t, context.Background(), pending.Command{Path: p, Args: []string{pidFile}, Timeout: 500 * time.Millisecond})
	if !errors.Is(o.err, pending.ErrTimeout) {
		t.Fatalf("Run past its timeout: error = %v, want ErrTimeout", o.err)
	}
	if !strings.Contains(o.err.Error(), p) {
		t.Errorf("timeout error %q does not name the command %s", o.err, p)
	}
	if o.elapsed > 500*time.Millisecond+s099KillBound {
		t.Errorf("Run returned %v after start, want within %v of the timeout", o.elapsed, s099KillBound)
	}
	s099AssertGone(t, s099WaitPid(t, pidFile), "the timed-out command's grandchild")
}

// TestS099RunCancelKillsTheWholeGroup is R1.7: cancelling the caller's ctx (a
// quit) kills the command and its grandchild at once, and is not a timeout.
func TestS099RunCancelKillsTheWholeGroup(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "grandchild.pid")
	p := s099Script(t, dir, "emerge", s099Hanger)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan s099Outcome, 1)
	go func() {
		res, err := pending.Run(ctx, pending.Command{Path: p, Args: []string{pidFile}, Timeout: 10 * time.Minute})
		ch <- s099Outcome{res: res, err: err}
	}()
	pid := s099WaitPid(t, pidFile)
	cancelled := time.Now()
	cancel()
	var o s099Outcome
	select {
	case o = <-ch:
	case <-time.After(s099HardStop):
		t.Fatal("Run did not return after its ctx was cancelled")
	}
	if took := time.Since(cancelled); took > s099KillBound {
		t.Errorf("Run returned %v after the cancel, want within %v", took, s099KillBound)
	}
	if o.err == nil {
		t.Fatal("Run after a cancel: nil error, want the kill reported")
	}
	if errors.Is(o.err, pending.ErrTimeout) {
		t.Errorf("Run after a cancel: error %v is ErrTimeout, want it told apart from a timeout", o.err)
	}
	s099AssertGone(t, pid, "the cancelled command's grandchild")
}

// TestS099RunStartFailureIsAnError is R2.11's input: a command that cannot be
// started is an error naming it, never a Result with an exit code.
func TestS099RunStartFailureIsAnError(t *testing.T) {
	dir := t.TempDir()
	notExec := filepath.Join(dir, "glsa-check")
	if err := os.WriteFile(notExec, []byte("#!/bin/sh\necho ran\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	for _, tc := range []struct {
		name, path string
		want       error
	}{
		{"not on PATH", "s099-no-such-emerge", exec.ErrNotFound},
		{"absent absolute path", filepath.Join(dir, "emerge"), fs.ErrNotExist},
		{"not executable", notExec, fs.ErrPermission},
	} {
		o := s099Run(t, context.Background(), pending.Command{Path: tc.path, Timeout: time.Minute})
		if o.err == nil {
			t.Errorf("%s: Run = %+v with nil error, want a start error", tc.name, o.res)
			continue
		}
		if !errors.Is(o.err, tc.want) {
			t.Errorf("%s: error %v is not %v", tc.name, o.err, tc.want)
		}
		if errors.Is(o.err, pending.ErrTimeout) {
			t.Errorf("%s: start error %v reads as ErrTimeout", tc.name, o.err)
		}
		if !strings.Contains(o.err.Error(), tc.path) {
			t.Errorf("%s: error %q does not name %s", tc.name, o.err, tc.path)
		}
	}
}

// TestS099RunMeasuresDuration is R5.7's input: Duration is the command's run
// time.
func TestS099RunMeasuresDuration(t *testing.T) {
	dir := t.TempDir()
	p := s099Script(t, dir, "emerge", "sleep 0.3")
	o := s099Run(t, context.Background(), pending.Command{Path: p, Timeout: time.Minute})
	res := s099MustResult(t, o, "sleep")
	if res.Duration < 300*time.Millisecond || res.Duration > o.elapsed {
		t.Errorf("Duration = %v, want at least 300ms and at most the %v the call took", res.Duration, o.elapsed)
	}
}
