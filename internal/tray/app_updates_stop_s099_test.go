package tray_test

// Story 099, sub-task 5.3 fix round 1, Executor-authored: a stop waits,
// bounded, for the update check to leave its command runs, so the process
// exits only after the check's process groups were killed (R1.7); the log
// lines the check added carry the state key (Q13); and an updates monitor
// disabled at runtime withdraws its standing notice.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/gentoo/pending"
	"github.com/obentoo/bentoolkit/internal/tray"
)

// s099SlowStopRun wraps the fake runner: once the check's ctx is cancelled,
// the run takes delay more to return, as pending.Run does while the killed
// group exits. returned is set just before it returns.
func s099SlowStopRun(r *s099Runner, delay time.Duration, returned *atomic.Bool) func(context.Context, pending.Command) (pending.Result, error) {
	return func(ctx context.Context, c pending.Command) (pending.Result, error) {
		res, err := r.Run(ctx, c)
		if ctx.Err() != nil {
			time.Sleep(delay)
		}
		returned.Store(true)
		return res, err
	}
}

func TestS099World_X_QuitWaitsForTheCheckToLeaveRun(t *testing.T) {
	s := newS099(t)
	release := s.run.hold()
	defer close(release)
	var returned atomic.Bool
	s.startWorld(func(d *tray.Deps) { d.Run = s099SlowStopRun(s.run, 300*time.Millisecond, &returned) })
	s.untilRun(s099EmergePath, 1, 14)
	began := time.Now()
	s.menu(300) // Quit
	s086Stopped(t, s.appHarness)
	if !returned.Load() {
		t.Error("Run returned before the check's command run did: the process could exit before its group is killed")
	}
	if d := time.Since(began); d > 2*time.Second {
		t.Errorf("Quit took %v while a check ran, want under 2 s", d)
	}
	if n := s.warnsFor("updates"); n != 0 {
		t.Errorf("updates WARN lines = %d, want 0: the check ended within the bound", n)
	}
}

func TestS099World_X_BusLostWaitsForTheCheckToLeaveRun(t *testing.T) {
	s := newS099(t)
	release := s.run.hold()
	defer close(release)
	var returned atomic.Bool
	s.startWorld(func(d *tray.Deps) { d.Run = s099SlowStopRun(s.run, 300*time.Millisecond, &returned) })
	s.untilRun(s099EmergePath, 1, 14)
	close(s.bus.lost)
	if err := s086Result(t, s.appHarness); !errors.Is(err, tray.ErrBusLost) {
		t.Fatalf("Run = %v, want ErrBusLost", err)
	}
	if !returned.Load() {
		t.Error("Run returned ErrBusLost before the check's command run did")
	}
}

func TestS099World_X_QuitAbandonsARunThatIgnoresItsContextAfterTheBound(t *testing.T) {
	const bound = 200 * time.Millisecond
	t.Cleanup(tray.SetCheckStopWait(bound))
	s := newS099(t)
	stuck := make(chan struct{})
	t.Cleanup(func() { close(stuck) })
	s.startWorld(func(d *tray.Deps) {
		d.Run = func(ctx context.Context, c pending.Command) (pending.Result, error) {
			_, _ = s.run.Run(ctx, c)
			<-stuck // ignores ctx
			return pending.Result{}, ctx.Err()
		}
	})
	s.untilRun(s099EmergePath, 1, 14)
	began := time.Now()
	s.menu(300) // Quit
	s086Stopped(t, s.appHarness)
	d := time.Since(began)
	if d < bound {
		t.Errorf("Quit took %v, want at least the %v bound: it did not wait for the check", d, bound)
	}
	if d > 2*time.Second {
		t.Errorf("Quit took %v with a run that ignores its context, want under 2 s", d)
	}
	if n := s.warnsFor("updates", "state=", "target="); n != 1 {
		t.Errorf("updates WARN lines naming the abandoned check = %d, want 1", n)
	}
}

// s099ProcState is the state letter of pid in /proc, "" once it is gone,
// and whether SIGKILL is already pending for it.
func s099ProcState(pid int) (state string, killPending bool) {
	stat, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return "", false
	}
	_, rest, ok := strings.Cut(string(stat), ") ")
	if !ok || rest == "" {
		return "", false
	}
	status, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "status"))
	if err != nil {
		return "", false
	}
	for line := range strings.Lines(string(status)) {
		name, val, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok || (name != "SigPnd" && name != "ShdPnd") {
			continue
		}
		mask, err := strconv.ParseUint(strings.TrimSpace(val), 16, 64)
		if err == nil && mask&(1<<(9-1)) != 0 { // SIGKILL is 9
			killPending = true
		}
	}
	return rest[:1], killPending
}

func TestS099World_X_QuitKillsTheRealCommandGroupBeforeRunReturns(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-emerge")
	pidFile := filepath.Join(dir, "sleep.pid")
	body := "#!" + sh + "\nsleep 60 &\necho $! > \"$1\"\nwait\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	s := newS099(t)
	s.startWorld(func(d *tray.Deps) {
		d.Updates = &pending.Command{Path: script, Args: []string{pidFile}, Timeout: time.Minute}
		d.Run = func(ctx context.Context, c pending.Command) (pending.Result, error) {
			_, _ = s.run.Run(ctx, c) // counts the call; no gate, returns at once
			return pending.Run(ctx, c)
		}
	})
	s.untilRun(script, 1, 14)
	var pid int
	s.waitFor("the background sleep's pid", func() bool {
		b, err := os.ReadFile(pidFile)
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(b)))
		return err == nil && pid > 0
	})
	t.Cleanup(func() {
		if st, _ := s099ProcState(pid); st != "" && st != "Z" && st != "X" {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	s.menu(300) // Quit
	s086Stopped(t, s.appHarness)
	// The instant Run returned: the kill must already have been sent, as
	// the process may exit right now.
	if st, pending := s099ProcState(pid); st != "" && st != "Z" && st != "X" && !pending {
		t.Errorf("the command's background sleep %d is %q with no SIGKILL pending when Run returned: it outlives a tray that exits now", pid, st)
	}
}

// s099WantLineKeys checks every line carrying msg and monitor=updates: it
// has target= and state= keys and exactly one level key, and there is one.
func (s *s099Harness) s099WantLineKeys(msg string) {
	s.t.Helper()
	n := 0
	for _, l := range s.logs.lines() {
		if !strings.Contains(l, msg) || !strings.Contains(l, "monitor=updates") {
			continue
		}
		n++
		if !strings.Contains(l, " target=") || !strings.Contains(l, " state=") {
			s.t.Errorf("line lacks target or state: %s", l)
		}
		if k := len(s099LevelKey.FindAllString(l, -1)); k != 1 {
			s.t.Errorf("line carries %d level keys, want 1: %s", k, l)
		}
	}
	if n == 0 {
		s.t.Errorf("no monitor=updates line %s", msg)
	}
}

func TestS099World_X_NewLinesCarryTheStateKey(t *testing.T) {
	s := newS099(t)
	s.run.set(s099EmergePath, s099Fixed(pending.Result{ExitCode: 1, Duration: time.Second, Stderr: []byte("x\n")}, nil))
	s.startWorld(func(d *tray.Deps) {
		d.Sys = nil
		d.EmergeRunning = func() (bool, error) { return false, errors.New("reading /proc failed") }
	})
	s.firstCheck(s099EmergePath, "updates")
	s.s099WantLineKeys(`msg="update check started"`)
	s.s099WantLineKeys(`msg="@world does not resolve"`)
	s.s099WantLineKeys(`msg="looking for a running emerge failed`)
	if !s.logLine(func(l string) bool {
		return strings.Contains(l, `msg="@world does not resolve"`) && strings.Contains(l, "state=notice")
	}) {
		t.Error(`the "@world does not resolve" line does not carry the notice it raised (state=notice)`)
	}
}

func TestS099World_X_DisabledAtRuntimeWithdrawsTheNotice(t *testing.T) {
	s := newS099(t)
	s.run.set(s099EmergePath, func(n int) (pending.Result, error) {
		if n == 1 {
			return s099World(s099Qt), nil
		}
		return pending.Result{}, s099StartErr(s099EmergePath, exec.ErrNotFound)
	})
	s.startWorld(nil)
	s.firstCheck(s099EmergePath, "updates")
	if v := s.viewNow(); len(v.Info) == 0 {
		t.Fatal("no updates Info line after the first check")
	}
	sent := s.updatesSent()
	if len(sent) != 1 {
		t.Fatalf("updates notifications = %d, want 1", len(sent))
	}
	s.bump()
	s.untilRun(s099EmergePath, 2, 4)
	s.waitLines("the disable WARN", 1, "level=WARN", "monitor=updates", "disabled")
	s.waitFor("the updates notice withdrawn", func() bool { return s.closedFor("updates") == 1 })
	if v := s.viewNow(); len(v.Info) != 0 {
		t.Errorf("view Info = %q after the monitor was disabled, want none", v.Info)
	}
	s.wantTransitions("updates", "", [2]string{"ok", "notice"}, [2]string{"notice", "ok"})
	if n := s.warnsFor("updates"); n != 1 {
		t.Errorf("updates WARN lines = %d, want exactly 1", n)
	}
	if n := len(s.updatesSent()); n != 1 {
		t.Errorf("updates notifications = %d after the disable, want still 1", n)
	}
}
