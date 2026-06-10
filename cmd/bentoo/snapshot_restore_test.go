package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/snapshot"
	"github.com/spf13/pflag"
)

// ---------------------------------------------------------------------------
// T6.2 — `snapshot restore <id> --target <path> --ship <name> [--yes]` verb.
//
// These tests mirror snapshot_apply_test.go / snapshot_run_test.go: they stub
// osExit (captureExit), inject snapshotRunner = a MockRunner, and write a temp
// snapshot.toml via the shared helpers. They drive the dispatch happy path, the
// confirm-gate seam (snapshotRestoreConfirm), an unknown --ship, and the missing
// required --target flag.
// ---------------------------------------------------------------------------

// restoreTOMLArchive is a config with a btrbk engine over /home and an archive
// ship named "cloud" — the entry the restore verb resolves via --ship cloud.
const restoreTOMLArchive = `
[engine]
driver = "btrbk"
subvolumes = ["/home"]
snapshot_dir = "/.snapshots"

[[ship]]
name = "cloud"
type = "archive"
remote = "gdrive:bentoo-backups"
compress = "zstd"
`

// restoreTOMLRestic is a config whose "cloud" ship is a restic repo, exercising
// the restic restore dispatch (no chain) through the same verb.
const restoreTOMLRestic = `
[engine]
driver = "btrbk"
subvolumes = ["/home"]
snapshot_dir = "/.snapshots"

[[ship]]
name = "cloud"
type = "restic"
repo = "rest:https://repo.example/bentoo"
password_file = "/etc/bentoo/restic.pass"
`

// setRestoreFlags points the restore command's flags at the given values and
// restores them after the test, mirroring how the dry-run test toggles
// snapshotApplyDryRun. It returns nothing; cleanup is registered on t.
func setRestoreFlags(t *testing.T, target, ship string, yes bool) {
	t.Helper()
	origTarget, origShip, origYes := snapshotRestoreTarget, snapshotRestoreShip, snapshotRestoreYes
	snapshotRestoreTarget = target
	snapshotRestoreShip = ship
	snapshotRestoreYes = yes
	t.Cleanup(func() {
		snapshotRestoreTarget, snapshotRestoreShip, snapshotRestoreYes = origTarget, origShip, origYes
	})
}

// stubRestoreConfirm installs a confirm seam returning decision and restores the
// previous value after the test.
func stubRestoreConfirm(t *testing.T, decision bool) {
	t.Helper()
	orig := snapshotRestoreConfirm
	snapshotRestoreConfirm = func(string) bool { return decision }
	t.Cleanup(func() { snapshotRestoreConfirm = orig })
}

// hasCall reports whether calls contains an invocation of name whose first args
// match prefix (e.g. {"receive", "/mnt/r"} for `btrfs receive /mnt/r`).
func hasCall(calls []snapshot.RunnerCall, name string, prefix ...string) bool {
	for _, c := range calls {
		if c.Name != name || len(c.Args) < len(prefix) {
			continue
		}
		if slices.Equal(c.Args[:len(prefix)], prefix) {
			return true
		}
	}
	return false
}

// TestRunSnapshotRestore_ArchiveHappyPath: `restore <id> --target /mnt/r --ship
// cloud --yes` resolves the archive ship, builds a single-full-link chain, and
// drives snapshot.Restore — which runs `rclone cat | zstd -d | btrfs receive
// /mnt/r`. Exit is success (osExit not called).
func TestRunSnapshotRestore_ArchiveHappyPath(t *testing.T) {
	stubBinariesOnPath(t, "btrbk", "ssh", "rclone")
	writeSnapshotConfig(t, restoreTOMLArchive)
	mr := &snapshot.MockRunner{}
	snapshotRunner = mr
	setRestoreFlags(t, "/mnt/r", "cloud", true)

	var code int
	var exited bool
	_ = captureStdout(t, func() {
		code, exited = captureExit(t, func() {
			runSnapshotRestore(snapshotRestoreCmd, []string{"home.2026"})
		})
	})
	if exited {
		t.Fatalf("restore exited with code %d, want success", code)
	}

	// The destructive `btrfs receive /mnt/r` ran (the chain was applied), preceded
	// by `rclone cat <remote>/<object>`.
	if !hasCall(mr.Calls, "btrfs", "receive", "/mnt/r") {
		t.Errorf("expected `btrfs receive /mnt/r`, calls = %v", mr.Calls)
	}
	if !hasCall(mr.Calls, "rclone", "cat") {
		t.Errorf("expected `rclone cat <object>`, calls = %v", mr.Calls)
	}
}

// TestRunSnapshotRestore_ResticHappyPath: the same verb over a restic ship runs
// `restic restore <id> --target /mnt/r ...` and exits success.
func TestRunSnapshotRestore_ResticHappyPath(t *testing.T) {
	stubBinariesOnPath(t, "btrbk", "ssh", "restic")
	writeSnapshotConfig(t, restoreTOMLRestic)
	mr := &snapshot.MockRunner{}
	snapshotRunner = mr
	setRestoreFlags(t, "/mnt/r", "cloud", true)

	var code int
	var exited bool
	_ = captureStdout(t, func() {
		code, exited = captureExit(t, func() {
			runSnapshotRestore(snapshotRestoreCmd, []string{"home.2026"})
		})
	})
	if exited {
		t.Fatalf("restore exited with code %d, want success", code)
	}
	if !hasCall(mr.Calls, "restic", "restore", "home.2026", "--target", "/mnt/r") {
		t.Errorf("expected `restic restore home.2026 --target /mnt/r ...`, calls = %v", mr.Calls)
	}
}

// TestRunSnapshotRestore_ConfirmDeniedCleanAbort is the R5.4 gate at the verb
// level: without --yes and a confirm seam that DENIES, the restore is a clean
// abort — ErrRestoreDeclined is mapped to a non-error exit (osExit NOT called)
// and NO destructive subprocess runs.
func TestRunSnapshotRestore_ConfirmDeniedCleanAbort(t *testing.T) {
	stubBinariesOnPath(t, "btrbk", "ssh", "rclone")
	writeSnapshotConfig(t, restoreTOMLArchive)
	mr := &snapshot.MockRunner{}
	snapshotRunner = mr
	setRestoreFlags(t, "/mnt/r", "cloud", false) // no --yes → confirm gate
	stubRestoreConfirm(t, false)                 // operator declines

	var code int
	var exited bool
	_ = captureStdout(t, func() {
		code, exited = captureExit(t, func() {
			runSnapshotRestore(snapshotRestoreCmd, []string{"home.2026"})
		})
	})
	if exited {
		t.Fatalf("declined restore exited with code %d; declining is a clean abort, not a failure", code)
	}
	if len(mr.Calls) != 0 {
		t.Errorf("declined restore ran %d subprocess(es); want 0 (no destructive action)", len(mr.Calls))
	}
}

// TestRunSnapshotRestore_ConfirmApprovedProceeds: without --yes, a confirm seam
// that APPROVES lets the restore proceed (btrfs receive runs), exit success.
func TestRunSnapshotRestore_ConfirmApprovedProceeds(t *testing.T) {
	stubBinariesOnPath(t, "btrbk", "ssh", "rclone")
	writeSnapshotConfig(t, restoreTOMLArchive)
	mr := &snapshot.MockRunner{}
	snapshotRunner = mr
	setRestoreFlags(t, "/mnt/r", "cloud", false)
	stubRestoreConfirm(t, true) // operator approves

	var code int
	var exited bool
	_ = captureStdout(t, func() {
		code, exited = captureExit(t, func() {
			runSnapshotRestore(snapshotRestoreCmd, []string{"home.2026"})
		})
	})
	if exited {
		t.Fatalf("approved restore exited with code %d, want success", code)
	}
	if !hasCall(mr.Calls, "btrfs", "receive", "/mnt/r") {
		t.Errorf("approved restore did not run `btrfs receive /mnt/r`; calls = %v", mr.Calls)
	}
}

// TestRunSnapshotRestore_UnknownShipExits1: a --ship that names no [[ship]] entry
// fails fast with osExit(1) before any subprocess.
func TestRunSnapshotRestore_UnknownShipExits1(t *testing.T) {
	stubBinariesOnPath(t, "btrbk", "ssh", "rclone")
	writeSnapshotConfig(t, restoreTOMLArchive)
	mr := &snapshot.MockRunner{}
	snapshotRunner = mr
	setRestoreFlags(t, "/mnt/r", "does-not-exist", true)

	var code int
	var exited bool
	_ = captureStdout(t, func() {
		code, exited = captureExit(t, func() {
			runSnapshotRestore(snapshotRestoreCmd, []string{"home.2026"})
		})
	})
	if !exited || code != 1 {
		t.Errorf("unknown --ship exit = (%d, %v), want (1, true)", code, exited)
	}
	if len(mr.Calls) != 0 {
		t.Errorf("unknown ship ran %d subprocess(es); want 0", len(mr.Calls))
	}
}

// TestRunSnapshotRestore_MissingTargetErrors: --target is MarkFlagRequired, so a
// parse that omits it must fail required-flag validation (the same gate cobra
// runs before the Run handler), naming the missing "target" flag. Driving
// ParseFlags + ValidateRequiredFlags directly tests that contract on the
// subcommand without traversing the root command.
func TestRunSnapshotRestore_MissingTargetErrors(t *testing.T) {
	// Parse a flag line that omits --target; reset the flags' Changed state after
	// so this parse does not leak into other tests sharing the global command.
	t.Cleanup(func() {
		snapshotRestoreCmd.Flags().Visit(func(f *pflag.Flag) { f.Changed = false })
		snapshotRestoreTarget, snapshotRestoreShip, snapshotRestoreYes = "", "", false
	})

	if err := snapshotRestoreCmd.ParseFlags([]string{"--ship", "cloud", "--yes"}); err != nil {
		t.Fatalf("ParseFlags(without --target): %v", err)
	}

	err := snapshotRestoreCmd.ValidateRequiredFlags()
	if err == nil {
		t.Fatal("expected a required-flag error when --target is omitted, got nil")
	}
	if !strings.Contains(err.Error(), "target") {
		t.Errorf("error = %v, want it to mention the missing required \"target\" flag", err)
	}
}

// TestRunSnapshotRestore_DryRunPrintsActionsZeroExec: 008 R2.3 — restore
// --dry-run prints the destructive actions (id, target, ship) without running
// any subprocess and without consulting the confirm gate.
func TestRunSnapshotRestore_DryRunPrintsActionsZeroExec(t *testing.T) {
	stubBinariesOnPath(t, "btrbk", "restic")
	writeSnapshotConfig(t, restoreTOMLRestic)
	mr := &snapshot.MockRunner{}
	snapshotRunner = mr

	setRestoreFlags(t, "/mnt/r", "cloud", false)
	confirmCalled := false
	origConfirm := snapshotRestoreConfirm
	snapshotRestoreConfirm = func(string) bool { confirmCalled = true; return false }
	t.Cleanup(func() { snapshotRestoreConfirm = origConfirm })

	origDryRun := snapshotRestoreDryRun
	snapshotRestoreDryRun = true
	t.Cleanup(func() { snapshotRestoreDryRun = origDryRun })

	var code int
	var exited bool
	out := captureStdout(t, func() {
		code, exited = captureExit(t, func() { runSnapshotRestore(snapshotRestoreCmd, []string{"9921"}) })
	})
	if exited {
		t.Fatalf("restore --dry-run exited with code %d, want success", code)
	}

	if len(mr.Calls) != 0 {
		t.Errorf("restore --dry-run ran %d subprocess(es), want 0 (008 R2.3): %+v", len(mr.Calls), mr.Calls)
	}
	if confirmCalled {
		t.Error("restore --dry-run consulted the confirm gate; a preview must not prompt (008 R2.3)")
	}
	for _, want := range []string{"9921", "/mnt/r", "cloud"} {
		if !strings.Contains(out, want) {
			t.Errorf("restore --dry-run actions missing %q (008 R2.3); output:\n%s", want, out)
		}
	}
}

// Compile-time check: the package-level confirm seam is a plain func(string) bool
// assignable to snapshot.RestoreOptions.Confirm (the unexported confirmFunc type).
var _ = func() {
	var f func(string) bool = snapshotRestoreConfirm
	_ = snapshot.RestoreOptions{Confirm: f}
}
