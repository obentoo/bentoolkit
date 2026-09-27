//go:build unix

package procgroup_test

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
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/procgroup"
)

// Story 054, R1. Every test here spawns REAL processes: the defect is about
// which processes the kernel still has after a cancel, which no mock can show.

const (
	grace        = 5 * time.Second         // R1.2 / R1.4
	waitBound    = 6 * time.Second         // R1.3: grace + 1 s slack
	hardStop     = 20 * time.Second        // a hung Wait fails the test instead of the suite
	fastReturn   = 2 * time.Second         // "SIGTERM sufficed" / "killed at once"
	earliestKill = 4500 * time.Millisecond // escalation must wait for the grace
)

// Scripts print the pid of the process that matters, AFTER it is ready (trap
// set), so a cancel can never race the child's own setup.
const (
	// A grandchild that dies on SIGTERM and holds the stdout pipe.
	scriptGrandchild = `sh -c 'echo $$; exec sleep 60' & wait`
	// A grandchild that ignores SIGTERM (ignored dispositions survive exec).
	scriptStubbornGrandchild = `sh -c 'trap "" TERM; echo $$; exec sleep 60' & wait`
)

// firstLine is an io.Writer (not an *os.File, so os/exec copies through a pipe
// that a descendant can hold) that hands over the first output line.
type firstLine struct {
	mu   sync.Mutex
	buf  []byte
	sent bool
	ch   chan string
}

func newFirstLine() *firstLine { return &firstLine{ch: make(chan string, 1)} }

func (w *firstLine) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	if !w.sent {
		if i := bytes.IndexByte(w.buf, '\n'); i >= 0 {
			w.sent = true
			w.ch <- string(w.buf[:i])
		}
	}
	return len(p), nil
}

func (w *firstLine) pid(t *testing.T) int {
	t.Helper()
	select {
	case s := <-w.ch:
		pid, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil {
			t.Fatalf("child printed %q, want a pid: %v", s, err)
		}
		return pid
	case <-time.After(hardStop):
		t.Fatal("child never printed its pid")
		return 0
	}
}

// alive reports whether pid is a running (non-zombie) process.
func alive(pid int) bool {
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

// assertGone polls (bounded) because a killed orphan is reaped by init, not by us.
func assertGone(t *testing.T, pid int, what string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for alive(pid) {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Errorf("%s (pid %d) is still running after the call returned: the cancel did not reach it", what, pid)
			return
		}
		<-tick.C
	}
}

// waitWithin runs cmd.Wait and fails (after killing the group) if it hangs.
func waitWithin(t *testing.T, cmd *exec.Cmd) (time.Duration, error) {
	t.Helper()
	start := time.Now()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return time.Since(start), err
	case <-time.After(hardStop):
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Process.Kill()
		t.Fatalf("Wait did not return %v after cancel: a descendant holding the pipe keeps the run blocked (R1.3)", hardStop)
		return 0, nil
	}
}

func killedBy(t *testing.T, cmd *exec.Cmd) syscall.Signal {
	t.Helper()
	ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() {
		return 0
	}
	return ws.Signal()
}

// startSh starts `sh -c script` in the mode chosen by configure.
func startSh(t *testing.T, ctx context.Context, script string, configure func(*exec.Cmd)) (*exec.Cmd, *firstLine) {
	t.Helper()
	cmd := exec.CommandContext(ctx, "sh", "-c", script)
	out := newFirstLine()
	cmd.Stdout = out
	configure(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %q: %v", script, err)
	}
	return cmd, out
}

// R1.1 + R1.3: SIGTERM reaches the whole group, so a well-behaved grandchild
// holding the pipe dies at once and Wait returns long before the grace.
func TestGroupTerminatesGrandchild(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd, out := startSh(t, ctx, scriptGrandchild, procgroup.Group)
	grandchild := out.pid(t)

	cancel()
	elapsed, err := waitWithin(t, cmd)

	if elapsed > fastReturn {
		t.Errorf("Wait returned after %v, want < %v: SIGTERM to the group should end a grandchild that honours it", elapsed, fastReturn)
	}
	assertGone(t, grandchild, "grandchild sleep")
	if sig := killedBy(t, cmd); sig != syscall.SIGTERM {
		t.Errorf("direct child ended by signal %v, want SIGTERM first (R1.1); SIGKILL belongs to the escalation only", sig)
	}
	if procgroup.Result(cmd, err) == nil {
		t.Error("Result reported a cancelled command as success")
	}
}

// R1.2 + R1.3: a grandchild that ignores SIGTERM and holds the pipe is killed
// by the group SIGKILL after the grace, and not before it.
func TestGroupKillsAfterGrace(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd, out := startSh(t, ctx, scriptStubbornGrandchild, procgroup.Group)
	grandchild := out.pid(t)

	cancel()
	elapsed, _ := waitWithin(t, cmd)

	if elapsed > waitBound {
		t.Errorf("Wait returned after %v, want <= %v (R1.3)", elapsed, waitBound)
	}
	if elapsed < earliestKill {
		t.Errorf("Wait returned after %v: the TERM-ignoring grandchild was killed before the %v grace (R1.2)", elapsed, grace)
	}
	assertGone(t, grandchild, "TERM-ignoring grandchild")
}

// R1.3: the pipe holder ignores SIGTERM; Wait still returns within the bound,
// and the outcome is an error, not a success.
func TestGroupReturnsWhilePipeHeld(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	cmd, out := startSh(t, ctx, scriptStubbornGrandchild, procgroup.Group)
	grandchild := out.pid(t)

	elapsed, err := waitWithin(t, cmd)
	if elapsed > waitBound+200*time.Millisecond {
		t.Errorf("Wait returned after %v, want <= %v after the deadline (R1.3)", elapsed, waitBound)
	}
	if procgroup.Result(cmd, err) == nil {
		t.Error("a command stopped by its deadline was reported as success")
	}
	assertGone(t, grandchild, "pipe-holding grandchild")
}

// R1.4: foreground mode leaves the child in the caller's process group (the
// terminal's foreground group); group mode is the converse and moves it out.
func TestForegroundStaysInCallerGroup(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fg := exec.CommandContext(ctx, "sleep", "60")
	procgroup.Foreground(fg)
	grp := exec.CommandContext(ctx, "sleep", "60")
	procgroup.Group(grp)
	for _, c := range []*exec.Cmd{fg, grp} {
		if err := c.Start(); err != nil {
			t.Fatalf("start: %v", err)
		}
	}
	defer func() { cancel(); _ = fg.Wait(); _ = grp.Wait() }()

	fgPgid, err := syscall.Getpgid(fg.Process.Pid)
	if err != nil {
		t.Fatalf("getpgid foreground: %v", err)
	}
	if want := syscall.Getpgrp(); fgPgid != want {
		t.Errorf("foreground child pgid = %d, want the caller's %d: a password prompt in a background group stops on SIGTTIN (R1.4, R3.3)", fgPgid, want)
	}
	grpPgid, err := syscall.Getpgid(grp.Process.Pid)
	if err != nil {
		t.Fatalf("getpgid group: %v", err)
	}
	if grpPgid != grp.Process.Pid {
		t.Errorf("group child pgid = %d, want its own pid %d (a new group it leads)", grpPgid, grp.Process.Pid)
	}
}

// R1.4: foreground mode sends SIGTERM to the direct child and SIGKILLs it after
// the grace. Converse: a child that honours SIGTERM dies by SIGTERM, promptly.
func TestForegroundKillsAfterGrace(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		script   string
		wantSig  syscall.Signal
		min, max time.Duration
	}{
		{"stubborn child is killed after the grace", `trap "" TERM; echo $$; exec sleep 60`, syscall.SIGKILL, earliestKill, waitBound},
		{"normal child ends on SIGTERM", `echo $$; exec sleep 60`, syscall.SIGTERM, 0, fastReturn},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cmd, out := startSh(t, ctx, tc.script, procgroup.Foreground)
			if pid := out.pid(t); pid != cmd.Process.Pid {
				t.Fatalf("script pid %d != direct child %d", pid, cmd.Process.Pid)
			}
			cancel()
			elapsed, _ := waitWithin(t, cmd)
			if elapsed < tc.min || elapsed > tc.max {
				t.Errorf("Wait returned after %v, want within [%v, %v]", elapsed, tc.min, tc.max)
			}
			if sig := killedBy(t, cmd); sig != tc.wantSig {
				t.Errorf("child ended by signal %v, want %v", sig, tc.wantSig)
			}
		})
	}
}

// R1.5: exit 0 with a lingering pipe holder is a success, not exec.ErrWaitDelay.
// Converse: a non-zero exit with the same lingering holder stays a failure.
func TestResultTreatsWaitDelayAfterSuccessAsSuccess(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		script  string
		success bool
	}{
		{"exit 0 is success", `sh -c 'echo $$; exec sleep 60' & exit 0`, true},
		{"exit 3 is still a failure", `sh -c 'echo $$; exec sleep 60' & exit 3`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cmd, out := startSh(t, context.Background(), tc.script, procgroup.Group)
			lingering := out.pid(t)
			t.Cleanup(func() { _ = syscall.Kill(lingering, syscall.SIGKILL) })

			elapsed, err := waitWithin(t, cmd)
			if elapsed > waitBound {
				t.Errorf("Wait returned after %v, want <= %v: the lingering helper must not hold a finished command", elapsed, waitBound)
			}
			got := procgroup.Result(cmd, err)
			if tc.success && got != nil {
				t.Errorf("Result = %v, want nil: the command exited 0 (R1.5)", got)
			}
			if !tc.success {
				var exitErr *exec.ExitError
				if !errors.As(got, &exitErr) || exitErr.ExitCode() != 3 {
					t.Errorf("Result = %v, want the exit-status-3 error kept", got)
				}
			}
		})
	}
}

// R1.6: cancellation that finds the group already gone answers os.ErrProcessDone.
func TestGroupGoneIsProcessDone(t *testing.T) {
	t.Parallel()
	cmd := exec.CommandContext(context.Background(), "true")
	procgroup.Group(cmd)
	if err := cmd.Run(); err != nil {
		t.Fatalf("run true: %v", err)
	}
	if cmd.Cancel == nil {
		t.Fatal("Group left cmd.Cancel nil: nothing signals the group on cancel")
	}
	if err := cmd.Cancel(); !errors.Is(err, os.ErrProcessDone) {
		t.Errorf("Cancel on a reaped group = %v, want os.ErrProcessDone (ESRCH is not a failure)", err)
	}
}

// R8.1: overlay manifest's behaviour moved unchanged — SIGKILL to the whole
// group at once, so even a TERM-ignoring grandchild is gone well inside the
// 5 s grace that Group would spend.
func TestKillGroupNowStopsGrandchild(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd, out := startSh(t, ctx, scriptStubbornGrandchild, procgroup.KillGroupNow)
	grandchild := out.pid(t)

	cancel()
	elapsed, _ := waitWithin(t, cmd)
	if elapsed > fastReturn {
		t.Errorf("Wait returned after %v, want < %v: KillGroupNow must not wait for a grace", elapsed, fastReturn)
	}
	if sig := killedBy(t, cmd); sig != syscall.SIGKILL {
		t.Errorf("direct child ended by %v, want SIGKILL (the old stopWithDescendants signal)", sig)
	}
	assertGone(t, grandchild, "TERM-ignoring grandchild")
}

// R1.8: one package owns process-group handling; no spawner keeps a private copy.
func TestNoSpawnerKeepsAPrivateGroupKill(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	self, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, top := range []string{"cmd", "internal"} { // never .claude / .epic
		err := filepath.WalkDir(filepath.Join(root, top), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			abs, _ := filepath.Abs(path)
			if filepath.Dir(abs) == self {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, marker := range []string{"Setpgid:", "Setpgid =", "Setsid:", "Setsid ="} {
				if bytes.Contains(src, []byte(marker)) {
					t.Errorf("%s sets %q: a private process-group copy outside internal/common/procgroup (R1.8)", path, strings.TrimRight(marker, ": ="))
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", top, err)
		}
	}
}
