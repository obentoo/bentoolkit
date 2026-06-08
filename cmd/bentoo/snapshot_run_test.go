package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
