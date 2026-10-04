package autoupdate

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestManifestFailureKeepsCause pins R4.3 for the manifest step: errors.Is
// finds ErrManifestFailed and errors.As finds the process's own *exec.ExitError,
// while the printed text keeps today's "<sentinel>: <cause>" shape.
func TestManifestFailureKeepsCause(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")
	const pkg, oldVersion, newVersion = "test-cat/test-pkg", "1.0.0", "2.0.0"
	createTestEbuildFile(t, overlayDir, pkg, oldVersion)

	pending, err := NewPendingList(configDir)
	if err != nil {
		t.Fatalf("NewPendingList: %v", err)
	}
	if err := pending.Add(PendingUpdate{Package: pkg, CurrentVersion: oldVersion, NewVersion: newVersion, Status: StatusPending}); err != nil {
		t.Fatalf("pending.Add: %v", err)
	}

	exit7 := func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", "exit 7")
	}
	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(exit7),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}

	_, applyErr := applier.Apply(t.Context(), pkg, false)
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

// TestCompileFailureKeepsCause pins R4.3 for the compile step reached through
// Apply: the manifest succeeds, the `ebuild ... compile` process exits 9, and
// the returned error is errors.Is(ErrCompileFailed) while errors.As still finds
// that process's own *exec.ExitError. The text keeps today's
// "<sentinel>: <cause>" shape carrying "exit status 9".
func TestCompileFailureKeepsCause(t *testing.T) {
	if _, err := exec.LookPath("sudo"); err != nil {
		if _, err := exec.LookPath("doas"); err != nil {
			t.Skip("neither sudo nor doas on PATH; the compile step cannot be reached")
		}
	}
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")
	const pkg, oldVersion, newVersion = "test-cat/test-pkg", "1.0.0", "2.0.0"
	createTestEbuildFile(t, overlayDir, pkg, oldVersion)

	pending, err := NewPendingList(configDir)
	if err != nil {
		t.Fatalf("NewPendingList: %v", err)
	}
	if err := pending.Add(PendingUpdate{Package: pkg, CurrentVersion: oldVersion, NewVersion: newVersion, Status: StatusPending}); err != nil {
		t.Fatalf("pending.Add: %v", err)
	}

	sawCompile := false
	execFn := func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		for _, a := range arg {
			if a == "compile" {
				sawCompile = true
				return exec.CommandContext(ctx, "sh", "-c", "exit 9")
			}
		}
		return exec.CommandContext(ctx, "true") // manifest and anything else: success
	}
	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(execFn),
		WithConfirmFunc(func(string) bool { return true }),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}

	_, applyErr := applier.Apply(t.Context(), pkg, true)
	if !sawCompile {
		t.Fatalf("Apply never ran a command whose argv contains \"compile\" (err = %v)", applyErr)
	}
	if !errors.Is(applyErr, ErrCompileFailed) {
		t.Fatalf("Apply error = %v, want errors.Is(ErrCompileFailed)", applyErr)
	}
	var exitErr *exec.ExitError
	if !errors.As(applyErr, &exitErr) || exitErr.ExitCode() != 9 {
		t.Errorf("Apply error = %v, want errors.As to find the compile process's *exec.ExitError (exit 9)", applyErr)
	}
	if msg := applyErr.Error(); !strings.Contains(msg, ErrCompileFailed.Error()+": ") || !strings.Contains(msg, "exit status 9") {
		t.Errorf("Apply error text = %q, want today's \"%s: <cause>\" shape carrying \"exit status 9\"", msg, ErrCompileFailed)
	}
}
