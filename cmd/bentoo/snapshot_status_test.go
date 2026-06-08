package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/snapshot"
)

func TestRunSnapshotStatus_ReadsResultAndTimer(t *testing.T) {
	writeSnapshotConfig(t, validSnapshotTOML)
	redirectStateDir(t)

	// Persist a last run for status to read back.
	rr := snapshot.RunResult{
		StartedAt: time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC),
		Stages: []snapshot.StageResult{
			{Subvolume: "/home", Stage: snapshot.StageCreate, Status: snapshot.StatusOK},
		},
	}
	if err := rr.SaveLastRun(); err != nil {
		t.Fatalf("seed last run: %v", err)
	}

	mock := &snapshot.MockRunner{
		RunFunc: func(_ context.Context, _ string, _ []string, _ []byte) ([]byte, error) {
			return []byte("enabled\n"), nil
		},
	}
	snapshotRunner = mock

	var code int
	var exited bool
	out := captureStdout(t, func() {
		code, exited = captureExit(t, func() { runSnapshotStatus(snapshotStatusCmd, nil) })
	})
	if exited {
		t.Fatalf("status exited with code %d", code)
	}

	if !strings.Contains(out, "last run:") {
		t.Errorf("status missing last run line:\n%s", out)
	}
	if !strings.Contains(out, "enabled") {
		t.Errorf("status missing timer state:\n%s", out)
	}

	// The timer state came from the mock Runner via `systemctl is-enabled`.
	var queriedTimer bool
	for _, c := range mock.Calls {
		if c.Name == "systemctl" && len(c.Args) >= 1 && c.Args[0] == "is-enabled" {
			queriedTimer = true
		}
	}
	if !queriedTimer {
		t.Errorf("status did not query timer state via systemctl is-enabled: %+v", mock.Calls)
	}
}
