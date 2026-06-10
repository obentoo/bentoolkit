package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/snapshot"
)

// TestRunSnapshotStatus_PerStageTimersAndSpace: 008 R5.1 — status reports the
// last RunResult PER STAGE (stage, subvolume, outcome, error), the timer's next
// scheduled run via `systemctl --list-timers`, and free space.
func TestRunSnapshotStatus_PerStageTimersAndSpace(t *testing.T) {
	writeSnapshotConfig(t, validSnapshotTOML)
	redirectStateDir(t)

	rr := snapshot.RunResult{
		StartedAt: time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC),
		Stages: []snapshot.StageResult{
			{Subvolume: "/home", Stage: snapshot.StageCreate, Status: snapshot.StatusOK},
			{Subvolume: "/home", Stage: snapshot.StageShip, Target: "ssh",
				Status: snapshot.StatusFailed, Err: "connection refused"},
		},
		Err: "ship ssh for /home failed: connection refused",
	}
	if err := rr.SaveLastRun(); err != nil {
		t.Fatalf("seed last run: %v", err)
	}

	mock := &snapshot.MockRunner{
		RunFunc: func(_ context.Context, name string, args []string, _ []byte) ([]byte, error) {
			if name == "systemctl" && len(args) > 0 && args[0] == "list-timers" {
				return []byte("NEXT LEFT LAST PASSED UNIT ACTIVATES\n" +
					"Thu 2026-06-11 03:00:00 UTC 8h left - - bentoo-snapshot.timer bentoo-snapshot.service\n"), nil
			}
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

	// Per-stage breakdown of the last run (008 R5.1).
	for _, want := range []string{"create", "ok", "ship", "failed", "connection refused"} {
		if !strings.Contains(out, want) {
			t.Errorf("status per-stage report missing %q (008 R5.1); output:\n%s", want, out)
		}
	}

	// Next scheduled run from `systemctl list-timers` (008 R5.1).
	var listedTimers bool
	for _, c := range mock.Calls {
		if c.Name == "systemctl" && len(c.Args) > 0 && c.Args[0] == "list-timers" {
			listedTimers = true
		}
	}
	if !listedTimers {
		t.Errorf("status did not query systemctl list-timers (008 R5.1): %+v", mock.Calls)
	}
	if !strings.Contains(out, "2026-06-11") {
		t.Errorf("status missing the next scheduled run from --list-timers (008 R5.1); output:\n%s", out)
	}

	// Free space of the snapshot location (008 R5.1).
	if !strings.Contains(out, "free space") {
		t.Errorf("status missing free space report (008 R5.1); output:\n%s", out)
	}
}

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
