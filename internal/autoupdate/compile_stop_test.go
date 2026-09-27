//go:build unix

package autoupdate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/procgroup"
)

// Story 054, sub-task 4.2: the paths compile_interrupt_test.go does not reach.
// R3.7 needs a privilege tool owned by another uid to refuse SIGTERM for real,
// so its capture is proved with a Cancel that refuses the way the kernel would,
// against a real child run by the real os/exec. The same file pins the build
// gates' runner (S054-R1.5, R3.5) and the compile's authoritative re-run (R3.5).

// TestKeepStopRefusalKeepsARefusedStop is R3.7's capture: a stop the child could
// not be sent is still readable once Wait has returned, with the pid it was
// refused for, whatever Wait itself reported.
func TestKeepStopRefusalKeepsARefusedStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, "sleep", "60")
	procgroup.Foreground(cmd)
	// The refusal a root-owned sudo gives, without one: this Cancel signals
	// nothing, exactly as a SIGTERM the kernel refused would. os/exec's own
	// Process.Kill ends the child once WaitDelay expires.
	cmd.Cancel = func() error {
		return fmt.Errorf("sending SIGTERM to process %d: %w", cmd.Process.Pid, syscall.EPERM)
	}
	cmd.WaitDelay = 200 * time.Millisecond
	read := keepStopRefusal(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the child: %v", err)
	}
	cancel()
	waitErr := cmd.Wait()
	t.Logf("Wait reported %v (it need not carry the refusal: see keepStopRefusal)", waitErr)

	pid, refused := read()
	if !errors.Is(refused, syscall.EPERM) {
		t.Errorf("the refused stop was not kept: read() = (%d, %v), want an error wrapping EPERM (R3.7)", pid, refused)
	}
	if pid != cmd.Process.Pid {
		t.Errorf("the refusal names process %d, want the child's pid %d (R3.7)", pid, cmd.Process.Pid)
	}
}

// The hostile half: nothing is kept when there was nothing to report, so an
// interrupt that did stop the child never claims the build may still run.
func TestKeepStopRefusalKeepsNothingWhenTheStopLanded(t *testing.T) {
	t.Run("a delivered SIGTERM", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		cmd := exec.CommandContext(ctx, "sleep", "60")
		procgroup.Foreground(cmd)
		read := keepStopRefusal(cmd)
		if err := cmd.Start(); err != nil {
			t.Fatalf("starting the child: %v", err)
		}
		cancel()
		if err := cmd.Wait(); err == nil {
			t.Fatal("instrument: a child stopped by SIGTERM exited successfully")
		}
		if _, refused := read(); refused != nil {
			t.Errorf("a delivered SIGTERM was kept as a refusal: %v", refused)
		}
	})
	t.Run("a child that had already exited", func(t *testing.T) {
		cmd := exec.CommandContext(context.Background(), "true")
		cmd.Cancel = func() error { return os.ErrProcessDone }
		read := keepStopRefusal(cmd)
		if err := cmd.Cancel(); !errors.Is(err, os.ErrProcessDone) {
			t.Fatalf("the wrapped Cancel returned %v, want os.ErrProcessDone passed through", err)
		}
		if _, refused := read(); refused != nil {
			t.Errorf("os.ErrProcessDone was kept as a refusal: %v", refused)
		}
	})
	t.Run("a command with no Cancel still starts", func(t *testing.T) {
		cmd := exec.Command("true")
		read := keepStopRefusal(cmd)
		if cmd.Cancel != nil {
			t.Fatal("keepStopRefusal gave a context-less command a Cancel, which os/exec refuses to start")
		}
		if err := cmd.Run(); err != nil {
			t.Fatalf("running a context-less command: %v", err)
		}
		if _, refused := read(); refused != nil {
			t.Errorf("a command with no Cancel reported a refusal: %v", refused)
		}
	})
}

// TestInterruptedCompileErrorNamesTheProcessThatMayStillRun is R3.7's message,
// on top of R3.5's.
func TestInterruptedCompileErrorNamesTheProcessThatMayStillRun(t *testing.T) {
	refused := fmt.Errorf("sending SIGTERM to process 4242: %w", syscall.EPERM)
	err := interruptedCompileError("media-plugins/gst-plugins-qt6", "1.29.2", "sudo", context.Canceled, 4242, refused)

	if !errors.Is(err, context.Canceled) || !errors.Is(err, syscall.EPERM) {
		t.Errorf("error %q does not wrap both ctx.Err() and the refusal", err)
	}
	if errors.Is(err, ErrCompileFailed) {
		t.Errorf("error %q wraps ErrCompileFailed (R3.5)", err)
	}
	for _, want := range []string{"interrupted", "4242", "may still be running"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q (R3.5, R3.7)", err, want)
		}
	}

	// The hostile half: with nothing refused, nothing is claimed.
	plain := interruptedCompileError("media-plugins/gst-plugins-qt6", "1.29.2", "sudo", context.Canceled, 0, nil)
	if strings.Contains(plain.Error(), "may still be running") {
		t.Errorf("an interrupt whose stop landed says %q", plain)
	}
}

// The same guarantee through compileOnce's own wiring: a real interrupted
// compile, whose SIGTERM landed, reports the interrupt and nothing more.
func TestCompileInterruptWhoseStopLandedClaimsNothingStillRuns(t *testing.T) {
	h := newCompileHarness(t, interruptibleCompile)
	out := h.runInterrupted(t, nil)
	if out.err == nil || !strings.Contains(out.err.Error(), "interrupted") {
		t.Fatalf("instrument: the compile was not reported as interrupted: %v", out.err)
	}
	if strings.Contains(out.err.Error(), "may still be running") {
		t.Errorf("an interrupted compile whose SIGTERM landed says %q (R3.7 is for a refused stop only)", out.err)
	}
}

// failThenInterruptible fails genuinely on its first run, so the fixer is
// reached, and blocks interruptibly on the second, which is the re-run.
const failThenInterruptible = `if [ ! -e "$1.first" ]; then : > "$1.first"; ` + compileTranscript + `; exit 1; fi
` + interruptibleCompile

type editingBuildFixer struct{ calls atomic.Int32 }

func (f *editingBuildFixer) FixBuild(context.Context, BuildFixRequest) (BuildFixResult, error) {
	f.calls.Add(1)
	return BuildFixResult{Summary: "edited the staged ebuild"}, nil
}

// TestCompileInterruptedReRunIsNotAFailedBuild is R3.5 on the authoritative
// re-run: interrupted after a repair, it is an interrupt, not the first failure
// "still" standing.
func TestCompileInterruptedReRunIsNotAFailedBuild(t *testing.T) {
	h := newCompileHarness(t, failThenInterruptible)
	fixer := &editingBuildFixer{}
	h.applier.buildFixer = fixer
	out := h.runInterrupted(t, nil)

	if n := fixer.calls.Load(); n != 1 {
		t.Fatalf("instrument: the fixer ran %d times, want 1 — the interrupt did not hit the re-run", n)
	}
	if out.err == nil {
		t.Fatal("an interrupted re-run returned no error")
	}
	if errors.Is(out.err, ErrCompileFailed) {
		t.Errorf("interrupted re-run error wraps ErrCompileFailed (%v) (R3.5)", out.err)
	}
	if !errors.Is(out.err, context.Canceled) || !strings.Contains(out.err.Error(), "interrupted") {
		t.Errorf("interrupted re-run error = %v, want it to wrap context.Canceled and say \"interrupted\" (R3.5)", out.err)
	}
}

// lingeringChild runs a shell that exits with the given status while the sleep
// it started in the background still holds the output pipe, under a WaitDelay
// shorter than the one group mode sets.
func lingeringChild(t *testing.T, exit int) *exec.Cmd {
	t.Helper()
	pidFile := filepath.Join(t.TempDir(), "sleep.pid")
	script := fmt.Sprintf(`sleep 30 & echo $! > %q; exit %d`, pidFile, exit)
	cmd := exec.CommandContext(context.Background(), "/bin/sh", "-c", script)
	cmd.WaitDelay = 200 * time.Millisecond
	t.Cleanup(func() {
		if raw, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	return cmd
}

func combinedOutput(cmd *exec.Cmd) ([]byte, error) { return cmd.CombinedOutput() }

// TestRecordingRunnerTreatsWaitDelayAfterSuccessAsSuccess is 4.1's finding: a
// build that exited 0 while a helper still held its pipe is a PASS, and must not
// reach repairBuildGatesAndRerun as a failure (S054-R1.5).
func TestRecordingRunnerTreatsWaitDelayAfterSuccessAsSuccess(t *testing.T) {
	a := &Applier{ctx: context.Background(), runAttached: combinedOutput}

	var passed buildAttempt
	if _, err := a.recordingRunner(&passed)(lingeringChild(t, 0)); err != nil {
		t.Errorf("the runner returned %v for a build that exited 0", err)
	}
	if passed.err != nil {
		t.Errorf("a build that exited 0 while a helper held its pipe was recorded as %v, want a pass (R1.5)", passed.err)
	}

	// The hostile half: the same lingering helper after a non-zero exit is
	// still a compile failure.
	var failed buildAttempt
	if _, err := a.recordingRunner(&failed)(lingeringChild(t, 3)); err == nil {
		t.Fatal("instrument: a build that exited 3 returned no error")
	}
	if !errors.Is(failed.err, ErrCompileFailed) {
		t.Errorf("a build that exited 3 was recorded as %v, want ErrCompileFailed", failed.err)
	}
}

// TestRecordingRunnerNeverLabelsAnInterruptACompileFailure is R3.5 on the build
// gates' path.
func TestRecordingRunnerNeverLabelsAnInterruptACompileFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := &Applier{ctx: ctx, runAttached: func(cmd *exec.Cmd) ([]byte, error) {
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		cancel()
		return nil, cmd.Wait()
	}}
	cmd := exec.CommandContext(ctx, "sleep", "60")
	procgroup.Group(cmd)

	var attempt buildAttempt
	if _, err := a.recordingRunner(&attempt)(cmd); err == nil {
		t.Fatal("instrument: a build stopped by its context returned no error")
	}
	if errors.Is(attempt.err, ErrCompileFailed) {
		t.Errorf("an interrupted build was recorded as a compile failure: %v (R3.5)", attempt.err)
	}
	if !errors.Is(attempt.err, context.Canceled) || !strings.Contains(fmt.Sprint(attempt.err), "interrupted") {
		t.Errorf("an interrupted build was recorded as %v, want it to wrap context.Canceled and say \"interrupted\" (R3.5)", attempt.err)
	}
}

// TestCompileExitZeroWithALingeringHelperPasses is S054-R1.5 on the privileged
// compile: foreground mode's WaitDelay also runs after a normal exit, so a
// compile that exited 0 while a helper it left behind held the output pipe comes
// back as exec.ErrWaitDelay, and that is a PASS. It takes the full
// procgroup.GracePeriod, which nothing outside procgroup may shorten.
func TestCompileExitZeroWithALingeringHelperPasses(t *testing.T) {
	const script = `PATH=/usr/bin:/bin; sleep 30 & echo $! > "$1.sleep"; exit 0`
	h := newCompileHarness(t, script)
	t.Cleanup(func() {
		if raw, err := os.ReadFile(h.termFile + ".sleep"); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})

	logPath, err := h.applier.runCompile(h.cand, "media-plugins/gst-plugins-qt6", "1.29.2", &ApplyResult{})
	if err != nil {
		t.Errorf("a compile that exited 0 while a helper held its pipe returned %v (log %q), want a pass (R1.5)", err, logPath)
	}
	if n := h.fixer.calls.Load(); n != 0 {
		t.Errorf("a passing compile invoked the build fixer %d time(s)", n)
	}
}

// stubbornCompile is a privilege tool that ignores the relayed SIGTERM, as a
// root ebuild slower than the grace period looks from here: os/exec kills it
// with SIGKILL, which sudo cannot pass on.
const stubbornCompile = `PATH=/usr/bin:/bin
trap '' TERM
sleep 60 >/dev/null 2>&1 &
echo "$$ $!" > "$0"
wait $!`

// Story 054, R3.7 for a stop that was delivered but not obeyed in time: the
// tool had to be killed after the grace period, so the build it ran as root is
// not known to have stopped, and the error says so, naming the process.
func TestCompileKilledAfterTheGraceSaysTheBuildMayStillRun(t *testing.T) {
	h := newCompileHarness(t, stubbornCompile)
	var pid int
	out := h.runInterrupted(t, func(childPID int) { pid = childPID })
	if out.err == nil || !strings.Contains(out.err.Error(), "interrupted") {
		t.Fatalf("instrument: the compile was not reported as interrupted: %v", out.err)
	}
	for _, want := range []string{"may still be running", strconv.Itoa(pid)} {
		if !strings.Contains(out.err.Error(), want) {
			t.Errorf("a compile killed after the grace period says %q, want it to contain %q (R3.7)", out.err, want)
		}
	}
	if errors.Is(out.err, ErrCompileFailed) {
		t.Errorf("a compile killed after an interrupt wraps ErrCompileFailed: %v (R3.5)", out.err)
	}
}
