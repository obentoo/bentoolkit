package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/snapshot"
)

func TestRunSnapshotRun_SuccessPersistsResult(t *testing.T) {
	stubBinariesOnPath(t, "btrbk", "ssh")
	writeSnapshotConfig(t, validSnapshotTOML)
	stateDir := redirectStateDir(t)
	snapshotRunner = &snapshot.MockRunner{} // btrbk run/clean succeed (nil,nil)

	var code int
	var exited bool
	_ = captureStdout(t, func() {
		code, exited = captureExit(t, func() { runSnapshotRun(snapshotRunCmd, nil) })
	})
	if exited {
		t.Fatalf("run exited with code %d, want success", code)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "last-run.json")); err != nil {
		t.Errorf("RunResult not persisted: %v", err)
	}
}

// TestRunSnapshotRun_DryRunPrintsPlanZeroExec: 008 R2.2 — run --dry-run prints
// the engine/ship pipeline it would execute without running any subprocess,
// without rendering the engine config, and without persisting a RunResult.
func TestRunSnapshotRun_DryRunPrintsPlanZeroExec(t *testing.T) {
	stubBinariesOnPath(t, "btrbk", "ssh")
	dir, _ := writeSnapshotConfig(t, validSnapshotTOML)
	stateDir := redirectStateDir(t)
	mr := &snapshot.MockRunner{}
	snapshotRunner = mr

	origDryRun := snapshotRunDryRun
	snapshotRunDryRun = true
	t.Cleanup(func() { snapshotRunDryRun = origDryRun })

	var code int
	var exited bool
	out := captureStdout(t, func() {
		code, exited = captureExit(t, func() { runSnapshotRun(snapshotRunCmd, nil) })
	})
	if exited {
		t.Fatalf("run --dry-run exited with code %d, want success", code)
	}

	if len(mr.Calls) != 0 {
		t.Errorf("run --dry-run ran %d subprocess(es), want 0 (008 R2.2): %+v", len(mr.Calls), mr.Calls)
	}
	if _, err := os.Stat(filepath.Join(dir, "btrbk.conf")); !os.IsNotExist(err) {
		t.Errorf("run --dry-run must not render btrbk.conf (008 R2.2)")
	}
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		t.Fatalf("ReadDir(stateDir): %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("run --dry-run persisted state (%d entries), want none (008 R2.2)", len(entries))
	}
	for _, want := range []string{"btrbk", "/home", "user@host:/backup"} {
		if !strings.Contains(out, want) {
			t.Errorf("run --dry-run plan missing %q (008 R2.2); output:\n%s", want, out)
		}
	}
}

func TestRunSnapshotRun_FailureExitsNonZero(t *testing.T) {
	stubBinariesOnPath(t, "btrbk", "ssh")
	writeSnapshotConfig(t, validSnapshotTOML)
	redirectStateDir(t)
	snapshotRunner = &snapshot.MockRunner{
		RunFunc: func(_ context.Context, _ string, _ []string, _ []byte) ([]byte, error) {
			return nil, errors.New("btrfs: subvolume not found")
		},
	}

	var code int
	var exited bool
	_ = captureStdout(t, func() {
		code, exited = captureExit(t, func() { runSnapshotRun(snapshotRunCmd, nil) })
	})
	if !exited || code != 1 {
		t.Errorf("run exit = (%d, %v), want (1, true) on pipeline failure", code, exited)
	}
}
