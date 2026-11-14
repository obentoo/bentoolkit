package autoupdate

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// TestManifestFailureKeepsCause pins R4.3 for the manifest step: errors.Is
// finds ErrManifestFailed and errors.As finds the process's own *exec.ExitError,
// while the printed text keeps today's "<sentinel>: <cause>" shape.
func TestManifestFailureKeepsCause(t *testing.T) {
	// The manifest step runs inside an apply only on an adopted tree (--repair);
	// every other apply publishes a proof and fetches nothing.
	exit7 := func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", "exit 7")
	}
	applier, _, pkg, _, _, _, _ := adoptedFixture(t, exit7)

	_, applyErr := applier.Apply(t.Context(), pkg)
	if !errors.Is(applyErr, ErrManifestFailed) {
		t.Errorf("Apply error = %v, want errors.Is(ErrManifestFailed)", applyErr)
	}
	var exitErr *exec.ExitError
	if !errors.As(applyErr, &exitErr) || exitErr.ExitCode() != 7 {
		t.Errorf("Apply error = %v, want errors.As to find the manifest process's *exec.ExitError (exit 7)", applyErr)
	}
	if msg := applyErr.Error(); !strings.Contains(msg, ErrManifestFailed.Error()+": ") || !strings.Contains(msg, "exit status 7") {
		t.Errorf("Apply error text = %q, want today's \"%s: <cause>\" shape carrying \"exit status 7\"", msg, ErrManifestFailed)
	}
}

// TestRecordingRunnerKeepsCause pins R4.3 for the build-gate runner: the
// recorded error is errors.Is(ErrCompileFailed), errors.As finds the process's
// *exec.ExitError, and the text is byte-identical to "<sentinel>: <cause>".
func TestRecordingRunnerKeepsCause(t *testing.T) {
	// runAttached is the Applier's injected process runner; a plain
	// CombinedOutput stands in for the attached-terminal one.
	a := &Applier{runAttached: func(cmd *exec.Cmd) ([]byte, error) { return cmd.CombinedOutput() }}
	var attempt buildAttempt
	run := a.recordingRunner(t.Context(), &attempt)

	_, runErr := run(exec.Command("sh", "-c", "exit 5"))
	if runErr == nil {
		t.Fatal("runner returned nil for a process that exited 5")
	}
	if !errors.Is(attempt.err, ErrCompileFailed) {
		t.Errorf("recorded err = %v, want errors.Is(ErrCompileFailed)", attempt.err)
	}
	var exitErr *exec.ExitError
	if !errors.As(attempt.err, &exitErr) || exitErr.ExitCode() != 5 {
		t.Errorf("recorded err = %v, want errors.As to find the *exec.ExitError (exit 5)", attempt.err)
	}
	if want := ErrCompileFailed.Error() + ": " + runErr.Error(); attempt.err == nil || attempt.err.Error() != want {
		t.Errorf("recorded err text = %q, want byte-identical %q", attempt.err, want)
	}
}
