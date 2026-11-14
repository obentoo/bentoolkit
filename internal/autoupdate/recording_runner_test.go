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
	"syscall"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/procgroup"
)

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
	a := &Applier{runAttached: combinedOutput}

	var passed buildAttempt
	if _, err := a.recordingRunner(t.Context(), &passed)(lingeringChild(t, 0)); err != nil {
		t.Errorf("the runner returned %v for a build that exited 0", err)
	}
	if passed.err != nil {
		t.Errorf("a build that exited 0 while a helper held its pipe was recorded as %v, want a pass (R1.5)", passed.err)
	}

	// The hostile half: the same lingering helper after a non-zero exit is
	// still a compile failure.
	var failed buildAttempt
	if _, err := a.recordingRunner(t.Context(), &failed)(lingeringChild(t, 3)); err == nil {
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
	a := &Applier{runAttached: func(cmd *exec.Cmd) ([]byte, error) {
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		cancel()
		return nil, cmd.Wait()
	}}
	cmd := exec.CommandContext(ctx, "sleep", "60")
	procgroup.Group(cmd)

	var attempt buildAttempt
	if _, err := a.recordingRunner(ctx, &attempt)(cmd); err == nil {
		t.Fatal("instrument: a build stopped by its context returned no error")
	}
	if errors.Is(attempt.err, ErrCompileFailed) {
		t.Errorf("an interrupted build was recorded as a compile failure: %v (R3.5)", attempt.err)
	}
	if !errors.Is(attempt.err, context.Canceled) || !strings.Contains(fmt.Sprint(attempt.err), "interrupted") {
		t.Errorf("an interrupted build was recorded as %v, want it to wrap context.Canceled and say \"interrupted\" (R3.5)", attempt.err)
	}
}
