package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/obentoo/bentoolkit/internal/snapshot"
)

func TestRunSnapshotApply_RendersConf(t *testing.T) {
	// btrbk + ssh present so Validate's detection passes; no schedule keeps the
	// systemd unit writes (which target /etc) out of this verb-level test — that
	// path is covered by the snapshot package's Apply test.
	stubBinariesOnPath(t, "btrbk", "ssh")
	dir, _ := writeSnapshotConfig(t, validSnapshotTOML)
	snapshotRunner = &snapshot.MockRunner{}

	code, exited := captureExit(t, func() {
		runSnapshotApply(snapshotApplyCmd, nil)
	})
	if exited {
		t.Fatalf("apply exited with code %d, want success", code)
	}

	if _, err := os.Stat(filepath.Join(dir, "btrbk.conf")); err != nil {
		t.Errorf("btrbk.conf not rendered: %v", err)
	}
}

func TestRunSnapshotApply_InvalidConfigExits1(t *testing.T) {
	// Unknown driver fails the enum check before detection, so no PATH stubs.
	writeSnapshotConfig(t, "[engine]\ndriver = \"zfs\"\nsubvolumes = [\"/home\"]\n")
	snapshotRunner = &snapshot.MockRunner{}

	code, exited := captureExit(t, func() {
		runSnapshotApply(snapshotApplyCmd, nil)
	})
	if !exited || code != 1 {
		t.Errorf("apply exit = (%d, %v), want (1, true)", code, exited)
	}
}

func TestRunSnapshotApply_DryRunNoWrite(t *testing.T) {
	stubBinariesOnPath(t, "btrbk", "ssh")
	dir, _ := writeSnapshotConfig(t, validSnapshotTOML)
	snapshotRunner = &snapshot.MockRunner{}

	origDryRun := snapshotApplyDryRun
	snapshotApplyDryRun = true
	t.Cleanup(func() { snapshotApplyDryRun = origDryRun })

	code, exited := captureStdoutExit(t, func() {
		runSnapshotApply(snapshotApplyCmd, nil)
	})
	if exited {
		t.Fatalf("dry-run exited with code %d", code)
	}
	if _, err := os.Stat(filepath.Join(dir, "btrbk.conf")); !os.IsNotExist(err) {
		t.Errorf("dry-run must not write btrbk.conf")
	}
}

// captureStdoutExit combines stdout capture and exit capture for verbs that print
// then may exit.
func captureStdoutExit(t *testing.T, fn func()) (code int, exited bool) {
	t.Helper()
	_ = captureStdout(t, func() {
		code, exited = captureExit(t, fn)
	})
	return code, exited
}
