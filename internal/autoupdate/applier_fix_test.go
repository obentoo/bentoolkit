package autoupdate

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestApply_ManifestFix_Recovers exercises the happy path: the first `pkgdev
// manifest` fails, the wired fixer "repairs" the ebuild, and bentoo's own re-run
// of the manifest then succeeds. The apply must succeed with Fixed/FixSummary set
// and the pending entry removed.
func TestApply_ManifestFix_Recovers(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pkg := "dev-games/godot"
	oldVersion := "4.7_rc3"
	newVersion := "4.7"

	createTestEbuildFile(t, overlayDir, pkg, oldVersion)

	pending, _ := NewPendingList(configDir)
	pending.Add(PendingUpdate{
		Package:        pkg,
		CurrentVersion: oldVersion,
		NewVersion:     newVersion,
		Status:         StatusPending,
	})

	fixer := &fakeFixer{summary: "rewrote SRC_URI to the 4.7-stable asset"}

	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(pkgdevFlakySeam()),
		WithApplierFixer(fixer),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}

	result, err := applier.Apply(pkg, false)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success after fix, got failure: %v", result.Error)
	}
	if !result.Fixed {
		t.Error("expected result.Fixed = true")
	}
	if result.FixSummary != fixer.summary {
		t.Errorf("FixSummary = %q, want %q", result.FixSummary, fixer.summary)
	}
	if fixer.called != 1 {
		t.Errorf("fixer called %d times, want exactly 1", fixer.called)
	}

	// The new ebuild must remain in place (not rolled back).
	if _, statErr := os.Stat(applier.EbuildPath(pkg, newVersion)); statErr != nil {
		t.Errorf("new ebuild missing after successful fix: %v", statErr)
	}
	// Pending entry removed on full success.
	if _, found := pending.Get(pkg); found {
		t.Error("expected pending entry to be removed after successful apply")
	}

	// The fixer received a well-formed request scoped to the package directory.
	wantPkgDir := filepath.Join(overlayDir, "dev-games", "godot")
	if fixer.lastReq.PkgDir != wantPkgDir {
		t.Errorf("fixer PkgDir = %q, want %q", fixer.lastReq.PkgDir, wantPkgDir)
	}
	if fixer.lastReq.Version != newVersion {
		t.Errorf("fixer Version = %q, want %q", fixer.lastReq.Version, newVersion)
	}
	if fixer.lastReq.DistDir == "" {
		t.Error("fixer DistDir should be a non-empty temp dir")
	}
}

// TestApply_ManifestFix_QAAdvisory verifies the post-fix QA gate: after a
// successful fix+manifest, pkgcheck findings are captured into QASummary as an
// advisory without flipping Success.
func TestApply_ManifestFix_QAAdvisory(t *testing.T) {
	// Make lookPath("pkgcheck") succeed regardless of host so the QA gate runs.
	stubLookPathFound(t)

	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pkg := "dev-games/godot"
	oldVersion := "4.7_rc3"
	newVersion := "4.7"
	createTestEbuildFile(t, overlayDir, pkg, oldVersion)

	pending, _ := NewPendingList(configDir)
	pending.Add(PendingUpdate{
		Package:        pkg,
		CurrentVersion: oldVersion,
		NewVersion:     newVersion,
		Status:         StatusPending,
	})

	// pkgdev: first call fails, then succeeds; pkgcheck: prints a finding.
	const finding = "WARNING: SourceUrlWarning: stale mirror"
	calls := 0
	seam := func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		switch name {
		case "pkgdev":
			calls++
			if calls == 1 {
				return exec.CommandContext(ctx, "false")
			}
			return exec.CommandContext(ctx, "true")
		case "pkgcheck":
			return exec.CommandContext(ctx, "sh", "-c", "printf '%s' '"+finding+"'; exit 1")
		default:
			return exec.CommandContext(ctx, "true")
		}
	}

	fixer := &fakeFixer{summary: "fixed SRC_URI"}
	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(seam),
		WithApplierFixer(fixer),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}

	result, err := applier.Apply(pkg, false)
	if err != nil {
		t.Fatalf("Apply returned error: %v", err)
	}
	if !result.Success || !result.Fixed {
		t.Fatalf("expected Success && Fixed, got success=%v fixed=%v err=%v", result.Success, result.Fixed, result.Error)
	}
	if !strings.Contains(result.QASummary, "SourceUrlWarning") {
		t.Errorf("QASummary = %q, want it to carry the pkgcheck finding", result.QASummary)
	}
}

// TestApply_ManifestFix_StillFails covers the case where the fixer runs but the
// authoritative manifest re-run still fails: the apply must fail, the orphan
// ebuild must be rolled back, and the pending entry marked failed.
func TestApply_ManifestFix_StillFails(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pkg := "dev-games/godot"
	oldVersion := "4.7_rc3"
	newVersion := "4.7"

	createTestEbuildFile(t, overlayDir, pkg, oldVersion)

	pending, _ := NewPendingList(configDir)
	pending.Add(PendingUpdate{
		Package:        pkg,
		CurrentVersion: oldVersion,
		NewVersion:     newVersion,
		Status:         StatusPending,
	})

	// Fixer "succeeds" but the manifest keeps failing (mockExecCommandFailure
	// fails every pkgdev call).
	fixer := &fakeFixer{summary: "tried, but no luck"}

	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(mockExecCommandFailure),
		WithApplierFixer(fixer),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}

	result, applyErr := applier.Apply(pkg, false)
	if applyErr == nil {
		t.Fatal("expected an error when the manifest still fails after a fix")
	}
	if result.Success {
		t.Error("expected result.Success = false")
	}
	if result.Fixed {
		t.Error("Fixed must stay false when the post-fix manifest still fails")
	}
	if !errors.Is(applyErr, ErrManifestFailed) {
		t.Errorf("error %v should wrap ErrManifestFailed", applyErr)
	}
	if fixer.called != 1 {
		t.Errorf("fixer called %d times, want exactly 1", fixer.called)
	}
	// Orphan ebuild rolled back.
	if _, statErr := os.Stat(applier.EbuildPath(pkg, newVersion)); !os.IsNotExist(statErr) {
		t.Errorf("expected new ebuild to be rolled back, stat err = %v", statErr)
	}
	if update, _ := pending.Get(pkg); update.Status != StatusFailed {
		t.Errorf("pending status = %q, want %q", update.Status, StatusFailed)
	}
}

// TestApply_ManifestFix_FixerErrors covers the fixer itself returning an error:
// the apply fails like the no-fixer case (the original manifest failure is
// preserved and the fixer error is appended).
func TestApply_ManifestFix_FixerErrors(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pkg := "dev-games/godot"
	oldVersion := "4.7_rc3"
	newVersion := "4.7"

	createTestEbuildFile(t, overlayDir, pkg, oldVersion)

	pending, _ := NewPendingList(configDir)
	pending.Add(PendingUpdate{
		Package:        pkg,
		CurrentVersion: oldVersion,
		NewVersion:     newVersion,
		Status:         StatusPending,
	})

	fixer := &fakeFixer{err: errors.New("claude CLI exploded")}

	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(mockExecCommandFailure),
		WithApplierFixer(fixer),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}

	result, applyErr := applier.Apply(pkg, false)
	if applyErr == nil || result.Success {
		t.Fatal("expected failure when the fixer errors")
	}
	if !errors.Is(applyErr, ErrManifestFailed) {
		t.Errorf("error %v should wrap ErrManifestFailed", applyErr)
	}
	if update, _ := pending.Get(pkg); update.Status != StatusFailed {
		t.Errorf("pending status = %q, want %q", update.Status, StatusFailed)
	}
}

// TestApply_NoFixer_Unchanged confirms that without a fixer the manifest-failure
// path is exactly the legacy behaviour (no fix attempt, Fixed stays false).
func TestApply_NoFixer_Unchanged(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pkg := "test-cat/test-pkg"
	oldVersion := "1.0.0"
	newVersion := "2.0.0"

	createTestEbuildFile(t, overlayDir, pkg, oldVersion)

	pending, _ := NewPendingList(configDir)
	pending.Add(PendingUpdate{
		Package:        pkg,
		CurrentVersion: oldVersion,
		NewVersion:     newVersion,
		Status:         StatusPending,
	})

	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(mockExecCommandFailure),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}

	result, applyErr := applier.Apply(pkg, false)
	if applyErr == nil || result.Success {
		t.Fatal("expected the legacy manifest failure")
	}
	if result.Fixed {
		t.Error("Fixed must be false when no fixer is wired")
	}
	if !errors.Is(applyErr, ErrManifestFailed) {
		t.Errorf("error %v should wrap ErrManifestFailed", applyErr)
	}
}
