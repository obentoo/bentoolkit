package main

import (
	"context"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/snapshot"
)

func TestRunSnapshotList_RendersSnapshots(t *testing.T) {
	// list is lenient (no Validate), so no PATH stubs are needed.
	writeSnapshotConfig(t, validSnapshotTOML)
	sample := "/.snapshots/home.20260608T120000\n/.snapshots/home.20260607T120000\n"
	snapshotRunner = &snapshot.MockRunner{
		RunFunc: func(_ context.Context, _ string, _ []string, _ []byte) ([]byte, error) {
			return []byte(sample), nil
		},
	}

	var code int
	var exited bool
	out := captureStdout(t, func() {
		code, exited = captureExit(t, func() { runSnapshotList(snapshotListCmd, nil) })
	})
	if exited {
		t.Fatalf("list exited with code %d", code)
	}
	if !strings.Contains(out, "/.snapshots/home.20260608T120000") {
		t.Errorf("list output missing snapshot path:\n%s", out)
	}
}
