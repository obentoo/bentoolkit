package main

import (
	"context"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/snapshot"
)

// ---------------------------------------------------------------------------
// Story 008 T3.1 — `snapshot prune [--dry-run] [--ship NAME]` verb (R3).
//
// Mirrors the other verb tests: osExit stubbed via captureExit, a MockRunner
// injected as snapshotRunner, temp snapshot.toml via the shared helpers. The
// MockRunner answers `rclone lsjson` with an empty object list so the archive
// GFS path runs end-to-end without deletions.
// ---------------------------------------------------------------------------

// pruneTOMLTwoArchives: btrbk engine with a GFS retention policy and two archive
// ships — the second is the --ship scoping target.
const pruneTOMLTwoArchives = `
[engine]
driver = "btrbk"
subvolumes = ["/home"]
snapshot_dir = "/.snapshots"

[engine.retention]
daily = 7

[[ship]]
name = "cloud-a"
type = "archive"
remote = "gdrive:a"

[[ship]]
name = "cloud-b"
type = "archive"
remote = "gdrive:b"
`

// setPruneFlags points the prune command's flags at the given values and
// restores them after the test.
func setPruneFlags(t *testing.T, ship string, dryRun bool) {
	t.Helper()
	origShip, origDry := snapshotPruneShip, snapshotPruneDryRun
	snapshotPruneShip = ship
	snapshotPruneDryRun = dryRun
	t.Cleanup(func() { snapshotPruneShip, snapshotPruneDryRun = origShip, origDry })
}

// pruneMockRunner returns a MockRunner whose rclone lsjson calls yield an empty
// object list (valid JSON), so GFS selection runs without any deletefile.
func pruneMockRunner() *snapshot.MockRunner {
	return &snapshot.MockRunner{
		RunFunc: func(_ context.Context, name string, args []string, _ []byte) ([]byte, error) {
			if name == "rclone" && len(args) > 0 && args[0] == "lsjson" {
				return []byte("[]"), nil
			}
			return nil, nil
		},
	}
}

// callsWith reports whether calls contains an invocation of name whose args
// include every one of want (in any position).
func callsWith(calls []snapshot.RunnerCall, name string, want ...string) bool {
	for _, c := range calls {
		if c.Name != name {
			continue
		}
		all := true
		for _, w := range want {
			found := false
			for _, a := range c.Args {
				if a == w {
					found = true
					break
				}
			}
			if !found {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

// TestRunSnapshotPrune_InvokesEnginePruneAndArchiveGFS: 008 R3.1 — prune applies
// [engine.retention] on demand: engine-native prune (btrbk clean) per subvolume
// plus the archive GFS (rclone lsjson) for every archive ship.
func TestRunSnapshotPrune_InvokesEnginePruneAndArchiveGFS(t *testing.T) {
	stubBinariesOnPath(t, "btrbk", "rclone", "zstd", "btrfs")
	writeSnapshotConfig(t, pruneTOMLTwoArchives)
	redirectStateDir(t)
	mr := pruneMockRunner()
	snapshotRunner = mr

	setPruneFlags(t, "", false)

	var code int
	var exited bool
	_ = captureStdout(t, func() {
		code, exited = captureExit(t, func() { runSnapshotPrune(snapshotPruneCmd, nil) })
	})
	if exited {
		t.Fatalf("prune exited with code %d, want success", code)
	}

	if !callsWith(mr.Calls, "btrbk", "clean", "/home") {
		t.Errorf("engine prune (btrbk clean /home) not invoked (008 R3.1); calls: %+v", mr.Calls)
	}
	if !callsWith(mr.Calls, "rclone", "lsjson", "gdrive:a") {
		t.Errorf("archive GFS for cloud-a (rclone lsjson gdrive:a) not invoked (008 R3.1); calls: %+v", mr.Calls)
	}
	if !callsWith(mr.Calls, "rclone", "lsjson", "gdrive:b") {
		t.Errorf("archive GFS for cloud-b (rclone lsjson gdrive:b) not invoked (008 R3.1); calls: %+v", mr.Calls)
	}
}

// TestRunSnapshotPrune_DryRunZeroExec: 008 R3.2 — prune --dry-run prints the
// retention plan and performs nothing: zero subprocesses.
func TestRunSnapshotPrune_DryRunZeroExec(t *testing.T) {
	stubBinariesOnPath(t, "btrbk", "rclone", "zstd", "btrfs")
	writeSnapshotConfig(t, pruneTOMLTwoArchives)
	redirectStateDir(t)
	mr := pruneMockRunner()
	snapshotRunner = mr

	setPruneFlags(t, "", true)

	var code int
	var exited bool
	out := captureStdout(t, func() {
		code, exited = captureExit(t, func() { runSnapshotPrune(snapshotPruneCmd, nil) })
	})
	if exited {
		t.Fatalf("prune --dry-run exited with code %d, want success", code)
	}

	if len(mr.Calls) != 0 {
		t.Errorf("prune --dry-run ran %d subprocess(es), want 0 (008 R3.2): %+v", len(mr.Calls), mr.Calls)
	}
	for _, want := range []string{"prune", "gdrive:a", "gdrive:b"} {
		if !strings.Contains(out, want) {
			t.Errorf("prune --dry-run plan missing %q (008 R3.2); output:\n%s", want, out)
		}
	}
}

// TestRunSnapshotPrune_ShipScopesToOneDestination: 008 R3.2 — --ship narrows the
// prune to the named destination: only that ship's remote is touched and the
// engine-local prune is skipped.
func TestRunSnapshotPrune_ShipScopesToOneDestination(t *testing.T) {
	stubBinariesOnPath(t, "btrbk", "rclone", "zstd", "btrfs")
	writeSnapshotConfig(t, pruneTOMLTwoArchives)
	redirectStateDir(t)
	mr := pruneMockRunner()
	snapshotRunner = mr

	setPruneFlags(t, "cloud-b", false)

	var code int
	var exited bool
	_ = captureStdout(t, func() {
		code, exited = captureExit(t, func() { runSnapshotPrune(snapshotPruneCmd, nil) })
	})
	if exited {
		t.Fatalf("prune --ship cloud-b exited with code %d, want success", code)
	}

	if callsWith(mr.Calls, "btrbk", "clean") {
		t.Errorf("--ship scoping must skip the engine-local prune (008 R3.2); calls: %+v", mr.Calls)
	}
	if callsWith(mr.Calls, "rclone", "lsjson", "gdrive:a") {
		t.Errorf("--ship cloud-b must not touch cloud-a's remote (008 R3.2); calls: %+v", mr.Calls)
	}
	if !callsWith(mr.Calls, "rclone", "lsjson", "gdrive:b") {
		t.Errorf("--ship cloud-b did not prune its remote (008 R3.2); calls: %+v", mr.Calls)
	}
}

// TestRunSnapshotPrune_UnknownShipExits1: an unknown --ship name is a hard error,
// mirroring restore's unknown-ship handling.
func TestRunSnapshotPrune_UnknownShipExits1(t *testing.T) {
	stubBinariesOnPath(t, "btrbk", "rclone", "zstd", "btrfs")
	writeSnapshotConfig(t, pruneTOMLTwoArchives)
	redirectStateDir(t)
	mr := pruneMockRunner()
	snapshotRunner = mr

	setPruneFlags(t, "nope", false)

	var code int
	var exited bool
	_ = captureStdout(t, func() {
		code, exited = captureExit(t, func() { runSnapshotPrune(snapshotPruneCmd, nil) })
	})
	if !exited || code != 1 {
		t.Errorf("prune --ship nope exit = (%d, %v), want (1, true)", code, exited)
	}
	if len(mr.Calls) != 0 {
		t.Errorf("unknown --ship still ran %d subprocess(es), want 0: %+v", len(mr.Calls), mr.Calls)
	}
}

// TestSnapshotPruneWired: the prune verb is registered under the snapshot group
// with its --dry-run and --ship flags.
func TestSnapshotPruneWired(t *testing.T) {
	var found bool
	for _, c := range snapshotCmd.Commands() {
		if c.Name() == "prune" {
			found = true
		}
	}
	if !found {
		t.Fatal("prune verb not registered under snapshot")
	}
	if snapshotPruneCmd.Flags().Lookup("dry-run") == nil {
		t.Error("--dry-run flag missing on prune")
	}
	if snapshotPruneCmd.Flags().Lookup("ship") == nil {
		t.Error("--ship flag missing on prune")
	}
}
