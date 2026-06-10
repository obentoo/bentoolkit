package main

import (
	"testing"

	"github.com/obentoo/bentoolkit/internal/snapshot"
)

// ---------------------------------------------------------------------------
// Story 007 T2.1 — `snapshot rollback <id>` verb (R3, R6.1).
//
// Mirrors snapshot_restore_test.go: osExit stubbed via captureExit, a
// MockRunner injected as snapshotRunner, temp snapshot.toml via the shared
// helpers, and the confirm gate driven through the snapshotRollbackConfirm
// seam. Rollback is snapper-specific: a non-snapper engine is refused (R3.3).
// ---------------------------------------------------------------------------

// rollbackTOMLSnapper is a config with the snapper engine over the root
// subvolume — the canonical system-rollback setup.
const rollbackTOMLSnapper = `
[engine]
driver = "snapper"
subvolumes = ["/"]
`

// rollbackTOMLBtrbk is a btrbk-engine config: rollback must be refused for it.
const rollbackTOMLBtrbk = `
[engine]
driver = "btrbk"
subvolumes = ["/home"]
snapshot_dir = "/.snapshots"
`

// setRollbackFlags points the rollback command's flags at the given values and
// restores them after the test.
func setRollbackFlags(t *testing.T, yes bool) {
	t.Helper()
	origYes := snapshotRollbackYes
	snapshotRollbackYes = yes
	t.Cleanup(func() { snapshotRollbackYes = origYes })
}

// stubRollbackConfirm installs a confirm seam returning decision, recording
// whether it was consulted, and restores the previous seam after the test.
func stubRollbackConfirm(t *testing.T, decision bool) *bool {
	t.Helper()
	called := false
	orig := snapshotRollbackConfirm
	snapshotRollbackConfirm = func(string) bool { called = true; return decision }
	t.Cleanup(func() { snapshotRollbackConfirm = orig })
	return &called
}

// TestRunSnapshotRollback_YesInvokesSnapper: `rollback 42 --yes` on a snapper
// engine runs `snapper -c root rollback 42` (R3.1) and exits success.
func TestRunSnapshotRollback_YesInvokesSnapper(t *testing.T) {
	stubBinariesOnPath(t, "snapper")
	writeSnapshotConfig(t, rollbackTOMLSnapper)
	mr := &snapshot.MockRunner{}
	snapshotRunner = mr
	setRollbackFlags(t, true)

	var code int
	var exited bool
	_ = captureStdout(t, func() {
		code, exited = captureExit(t, func() {
			runSnapshotRollback(snapshotRollbackCmd, []string{"42"})
		})
	})
	if exited {
		t.Fatalf("rollback exited with code %d, want success", code)
	}
	if len(mr.Calls) != 1 {
		t.Fatalf("len(Calls) = %d, want 1: %+v", len(mr.Calls), mr.Calls)
	}
	c := mr.Calls[0]
	wantArgs := []string{"-c", "root", "rollback", "42"}
	if c.Name != "snapper" || !equalArgs(c.Args, wantArgs) {
		t.Errorf("call = %s %v, want snapper %v", c.Name, c.Args, wantArgs)
	}
}

// TestRunSnapshotRollback_ConfirmDeniedCleanAbort is the R3.2 gate: without
// --yes and a confirm seam that DENIES, the rollback is a clean abort — exit
// success (osExit NOT called) and NO subprocess runs.
func TestRunSnapshotRollback_ConfirmDeniedCleanAbort(t *testing.T) {
	stubBinariesOnPath(t, "snapper")
	writeSnapshotConfig(t, rollbackTOMLSnapper)
	mr := &snapshot.MockRunner{}
	snapshotRunner = mr
	setRollbackFlags(t, false) // no --yes → confirm gate
	stubRollbackConfirm(t, false)

	var code int
	var exited bool
	_ = captureStdout(t, func() {
		code, exited = captureExit(t, func() {
			runSnapshotRollback(snapshotRollbackCmd, []string{"42"})
		})
	})
	if exited {
		t.Fatalf("declined rollback exited with code %d; declining is a clean abort", code)
	}
	if len(mr.Calls) != 0 {
		t.Errorf("declined rollback ran %d subprocess(es), want 0: %+v", len(mr.Calls), mr.Calls)
	}
}

// TestRunSnapshotRollback_ConfirmApprovedProceeds: without --yes, an APPROVING
// confirm seam lets the rollback proceed (R3.2).
func TestRunSnapshotRollback_ConfirmApprovedProceeds(t *testing.T) {
	stubBinariesOnPath(t, "snapper")
	writeSnapshotConfig(t, rollbackTOMLSnapper)
	mr := &snapshot.MockRunner{}
	snapshotRunner = mr
	setRollbackFlags(t, false)
	called := stubRollbackConfirm(t, true)

	var code int
	var exited bool
	_ = captureStdout(t, func() {
		code, exited = captureExit(t, func() {
			runSnapshotRollback(snapshotRollbackCmd, []string{"42"})
		})
	})
	if exited {
		t.Fatalf("approved rollback exited with code %d, want success", code)
	}
	if !*called {
		t.Error("confirm seam was not consulted without --yes")
	}
	if len(mr.Calls) != 1 || mr.Calls[0].Name != "snapper" {
		t.Errorf("approved rollback calls = %+v, want one snapper invocation", mr.Calls)
	}
}

// TestRunSnapshotRollback_NonSnapperEngineRefused is R3.3: with a btrbk engine
// the rollback is refused — osExit(1), no subprocess, and the confirm gate is
// never even consulted (the engine guard fires first).
func TestRunSnapshotRollback_NonSnapperEngineRefused(t *testing.T) {
	stubBinariesOnPath(t, "btrbk", "ssh")
	writeSnapshotConfig(t, rollbackTOMLBtrbk)
	mr := &snapshot.MockRunner{}
	snapshotRunner = mr
	setRollbackFlags(t, false)
	called := stubRollbackConfirm(t, true)

	var code int
	var exited bool
	_ = captureStdout(t, func() {
		code, exited = captureExit(t, func() {
			runSnapshotRollback(snapshotRollbackCmd, []string{"42"})
		})
	})
	if !exited || code != 1 {
		t.Errorf("non-snapper rollback exit = (%d, %v), want (1, true)", code, exited)
	}
	if len(mr.Calls) != 0 {
		t.Errorf("refused rollback ran %d subprocess(es), want 0: %+v", len(mr.Calls), mr.Calls)
	}
	if *called {
		t.Error("confirm seam consulted before the engine guard; guard must fire first")
	}
}

// equalArgs compares two argv slices (local mirror of the snapshot package's
// equalStrings test helper).
func equalArgs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
