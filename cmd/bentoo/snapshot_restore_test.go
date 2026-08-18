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

// restoreArchiveRemote is the remote every archive config below ships to. Named
// once so the tests that assert on the `rclone cat` source compose it instead of
// re-spelling it next to the prefix.
const restoreArchiveRemote = "gdrive:bentoo-backups"

// restoreTOMLArchiveTwoSubvolumes is the shape 038 R5 is about: ONE archive ship
// over TWO subvolumes, each of which owns its own remote prefix. "/" is FIRST on
// purpose — it is what the pre-038 code picked blindly — so a test that selects
// "/home" fails the moment the resolved value stops reaching the object key.
//
// It is a new constant rather than an edit to restoreTOMLArchive because
// restoreTOMLArchive is the single-subvolume shape R5.1 protects; editing it
// would have quietly stopped every test above from covering that case.
const restoreTOMLArchiveTwoSubvolumes = `
[engine]
driver = "btrbk"
subvolumes = ["/", "/home"]
snapshot_dir = "/.snapshots"

[[ship]]
name = "cloud"
type = "archive"
remote = "gdrive:bentoo-backups"
compress = "zstd"
`

// restoreTOMLArchiveDeployed mirrors the currently deployed
// /etc/bentoo/snapshot.toml: exactly ONE subvolume, the root "/". This is the
// configuration in real use, so "restore still works here with no new flag" is
// the highest-stakes assertion of 038 R5.1 — and "/" also exercises the prefix
// with no ordinary characters at all (ArchivePrefix("/") is the directory "-").
const restoreTOMLArchiveDeployed = `
[engine]
driver = "btrbk"
subvolumes = ["/"]
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

// setRestoreSubvolume points --subvolume at value and restores the previous one
// after the test. It is a COMPANION setter rather than a fourth parameter on
// setRestoreFlags for two reasons: the seven tests written before 038 keep their
// call sites byte-identical, so nothing about the single-subvolume path was
// edited to make the new tests pass; and --subvolume is genuinely optional
// (R5.1), so a test that omits it is modelling a real invocation, not an
// incomplete one — which an extra "" argument everywhere would blur. It mirrors
// stubRestoreConfirm, the file's existing companion setter for an optional seam.
//
// Saving and restoring is not optional: these are package globals shared by
// every test in the binary, and Go runs a package's tests in source order, so a
// leaked value surfaces as a failure in whatever test happens to come next.
func setRestoreSubvolume(t *testing.T, value string) {
	t.Helper()
	orig := snapshotRestoreSubvolume
	snapshotRestoreSubvolume = value
	t.Cleanup(func() { snapshotRestoreSubvolume = orig })
}

// restoreCatSource is the `rclone cat` argument a restore of id from subvolume
// must carry: "<remote>/<prefix>/<leaf>". It is COMPOSED from ArchivePrefix and
// ArchiveObjectLeaf rather than spelled out — the sanitize rule then lives in one
// place — and deliberately not from ArchiveObjectName, which is the very builder
// RestoreChainFor uses: re-deriving the expectation with the code under test
// would make the assertion agree with any key that function produced.
func restoreCatSource(remote, subvolume, id string) string {
	return remote + "/" + snapshot.ArchivePrefix(subvolume) + "/" + snapshot.ArchiveObjectLeaf(id)
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

// ---------------------------------------------------------------------------
// 038 T4.2 — WHICH subvolume a restore reads back from.
//
// Every subvolume owns a remote prefix (R3.1), so the restore has to name one to
// find its objects (R3.2). Before 038 the CLI took the engine's FIRST configured
// subvolume, which on a multi-subvolume config replayed the wrong prefix's
// backups over --target. The rule now lives in snapshot.ResolveRestoreSubvolume
// and runs in the handler BEFORE anything is executed.
//
// WHAT THESE TESTS ASSERT ON, AND WHY
//
// "Fails before any subprocess" (R5.2) is asserted on the RUNNER SEAM —
// len(mr.Calls) == 0 — never on the exit code alone. A resolution placed too
// late would still exit 1, but only after `rclone cat` had already run; an
// exit-code assertion cannot tell those two apart, and the whole point of the
// requirement is the difference between them.
//
// An empty Calls slice is also what a BROKEN FIXTURE produces: a config that
// does not load, a ship that is not found, a missing binary stub. So each
// refusal test is paired with TestRunSnapshotRestore_SubvolumeFlagPicksThatPrefix
// below, which drives the SAME config through the SAME helpers and must run the
// full pipeline. If the fixture were broken, that test fails — which is what
// makes the empty Calls next to it mean "the resolution refused" rather than
// "nothing got that far".
//
// Both refusal tests also pass --yes, so the confirm gate (which also produces
// zero calls, R5.4) is out of the picture as an alternative explanation.
//
// The error TEXT is not asserted: logger binds its io.Writer at first use and
// exposes no setter (logger.go:44-52), a limitation overlay_compare_test.go
// already records. Its wording is pinned instead where it is readable — the
// unit tests over ResolveRestoreSubvolume in internal/snapshot/restore_test.go,
// which assert that the message names every configured subvolume (R5.2) and the
// rejected value (R5.3).
// ---------------------------------------------------------------------------

// TestRunSnapshotRestore_TwoSubvolumesWithoutFlagRefusesBeforeAnySubprocess is
// 038 R5.2 and the story's quality gate: two subvolumes configured, no
// --subvolume, so there is no safe guess — the verb exits non-zero having run
// NOTHING. Not one `rclone cat`: the refusal is worthless if it lands after the
// pipeline has started reading another subvolume's objects.
func TestRunSnapshotRestore_TwoSubvolumesWithoutFlagRefusesBeforeAnySubprocess(t *testing.T) {
	stubBinariesOnPath(t, "btrbk", "ssh", "rclone")
	writeSnapshotConfig(t, restoreTOMLArchiveTwoSubvolumes)
	mr := &snapshot.MockRunner{}
	snapshotRunner = mr
	setRestoreFlags(t, "/mnt/r", "cloud", true) // --yes: the confirm gate cannot be the reason nothing ran
	setRestoreSubvolume(t, "")                  // the operator passed no --subvolume

	var code int
	var exited bool
	_ = captureStdout(t, func() {
		code, exited = captureExit(t, func() {
			runSnapshotRestore(snapshotRestoreCmd, []string{"home.2026"})
		})
	})

	if !exited || code != 1 {
		t.Errorf("two subvolumes and no --subvolume: exit = (%d, %v), want (1, true) — an ambiguous restore must fail, not pick one (R5.2)",
			code, exited)
	}
	if len(mr.Calls) != 0 {
		t.Errorf("two subvolumes and no --subvolume ran %d subprocess(es); want 0 — the refusal must come BEFORE anything executes (R5.2): %+v",
			len(mr.Calls), mr.Calls)
	}
}

// TestRunSnapshotRestore_SubvolumeFlagPicksThatPrefix is the positive half of the
// pair: the SAME two-subvolume config, this time with --subvolume /home. It
// carries two jobs.
//
// R3.2: the chain reads back exactly the key the ship wrote for that subvolume —
// `rclone cat <remote>/<prefix of /home>/<id>.zst`. The negative assertion names
// the specific way this breaks: "/" is the FIRST configured subvolume, so any
// path that falls back to the old guess reads <remote>/-/<id>.zst instead. Both
// are asserted, because the positive one alone would report "wrong key" without
// saying it was the first-subvolume fallback that produced it.
//
// Anti-vacuity: it proves the fixture used by the two refusal tests is sound.
// Same config, same helpers, same stubs — and here the whole pipeline runs. So
// their empty Calls slices cannot be blamed on a config that failed to load or a
// ship that was never found.
func TestRunSnapshotRestore_SubvolumeFlagPicksThatPrefix(t *testing.T) {
	stubBinariesOnPath(t, "btrbk", "ssh", "rclone")
	writeSnapshotConfig(t, restoreTOMLArchiveTwoSubvolumes)
	mr := &snapshot.MockRunner{}
	snapshotRunner = mr
	setRestoreFlags(t, "/mnt/r", "cloud", true)
	setRestoreSubvolume(t, "/home")

	const id = "home.2026"

	var code int
	var exited bool
	_ = captureStdout(t, func() {
		code, exited = captureExit(t, func() {
			runSnapshotRestore(snapshotRestoreCmd, []string{id})
		})
	})
	if exited {
		t.Fatalf("restore --subvolume /home exited with code %d, want success", code)
	}

	want := restoreCatSource(restoreArchiveRemote, "/home", id)
	if !hasCall(mr.Calls, "rclone", "cat", want) {
		t.Errorf("expected `rclone cat %s` — the resolved subvolume's prefix (038 R3.2); calls = %+v", want, mr.Calls)
	}
	firstConfigured := restoreCatSource(restoreArchiveRemote, "/", id)
	if hasCall(mr.Calls, "rclone", "cat", firstConfigured) {
		t.Errorf("restore read %s: that is engine.subvolumes[0], not the --subvolume that was asked for — the pre-038 guess is back (038 R3.2, R5)",
			firstConfigured)
	}

	// The pipeline really ran end to end, which is what makes the two refusal
	// tests' empty Calls slices meaningful rather than incidental.
	if !hasCall(mr.Calls, "btrfs", "receive", "/mnt/r") {
		t.Errorf("expected `btrfs receive /mnt/r` — the chain must actually be applied; calls = %+v", mr.Calls)
	}
}

// TestRunSnapshotRestore_UnknownSubvolumeRefusesBeforeAnySubprocess is 038 R5.3:
// --subvolume names something the config does not list. The prefix it would
// build exists nowhere on the remote, so the restore refuses — again before any
// subprocess, not after a `rclone cat` that would fail on its own several
// seconds later with a message about an object rather than about the config.
func TestRunSnapshotRestore_UnknownSubvolumeRefusesBeforeAnySubprocess(t *testing.T) {
	stubBinariesOnPath(t, "btrbk", "ssh", "rclone")
	writeSnapshotConfig(t, restoreTOMLArchiveTwoSubvolumes)
	mr := &snapshot.MockRunner{}
	snapshotRunner = mr
	setRestoreFlags(t, "/mnt/r", "cloud", true)
	setRestoreSubvolume(t, "/var")

	var code int
	var exited bool
	_ = captureStdout(t, func() {
		code, exited = captureExit(t, func() {
			runSnapshotRestore(snapshotRestoreCmd, []string{"home.2026"})
		})
	})

	if !exited || code != 1 {
		t.Errorf("--subvolume /var (not configured): exit = (%d, %v), want (1, true) (R5.3)", code, exited)
	}
	if len(mr.Calls) != 0 {
		t.Errorf("an unconfigured --subvolume ran %d subprocess(es); want 0 (R5.3): %+v", len(mr.Calls), mr.Calls)
	}
}

// TestRunSnapshotRestore_DeployedSingleSubvolumeNeedsNoFlag is 038 R5.1 over the
// configuration actually deployed — engine.subvolumes = ["/"] — and it is the
// highest-stakes assertion here: it is the only real deployment, so breaking it
// breaks the shipped command for its actual users. With one subvolume there is
// nothing to disambiguate, so `restore <id> --target <path> --ship <name>` keeps
// working with NO new flag and reads that subvolume's prefix.
func TestRunSnapshotRestore_DeployedSingleSubvolumeNeedsNoFlag(t *testing.T) {
	stubBinariesOnPath(t, "btrbk", "ssh", "rclone")
	writeSnapshotConfig(t, restoreTOMLArchiveDeployed)
	mr := &snapshot.MockRunner{}
	snapshotRunner = mr
	setRestoreFlags(t, "/mnt/r", "cloud", true)
	setRestoreSubvolume(t, "") // exactly the deployed invocation: no --subvolume

	const id = "root.2026"

	var code int
	var exited bool
	_ = captureStdout(t, func() {
		code, exited = captureExit(t, func() {
			runSnapshotRestore(snapshotRestoreCmd, []string{id})
		})
	})
	if exited {
		t.Fatalf("single-subvolume restore without --subvolume exited with code %d; the deployed configuration must keep working unedited (R5.1)", code)
	}

	want := restoreCatSource(restoreArchiveRemote, "/", id)
	if !hasCall(mr.Calls, "rclone", "cat", want) {
		t.Errorf("expected `rclone cat %s` — the single configured subvolume's prefix (R5.1); calls = %+v", want, mr.Calls)
	}
	if !hasCall(mr.Calls, "btrfs", "receive", "/mnt/r") {
		t.Errorf("expected `btrfs receive /mnt/r`; calls = %+v", mr.Calls)
	}
}

// TestRunSnapshotRestore_DryRunOnAmbiguousConfigAlsoRefuses pins the ordering
// decision: resolution runs BEFORE the --dry-run early return, so a preview of
// an ambiguous restore refuses too instead of printing "would restore ...".
//
// A dry-run exists to show what the real invocation would do. On this config the
// real invocation refuses, so a preview that succeeded would be describing
// something that cannot happen — and would send the operator to run it for real
// on the strength of a green preview. Zero subprocesses either way, so nothing
// about 008 R2.3 changes; what changes is that the preview no longer answers a
// question the real command answers differently.
func TestRunSnapshotRestore_DryRunOnAmbiguousConfigAlsoRefuses(t *testing.T) {
	stubBinariesOnPath(t, "btrbk", "ssh", "rclone")
	writeSnapshotConfig(t, restoreTOMLArchiveTwoSubvolumes)
	mr := &snapshot.MockRunner{}
	snapshotRunner = mr
	setRestoreFlags(t, "/mnt/r", "cloud", false)
	setRestoreSubvolume(t, "")

	origDryRun := snapshotRestoreDryRun
	snapshotRestoreDryRun = true
	t.Cleanup(func() { snapshotRestoreDryRun = origDryRun })

	var code int
	var exited bool
	out := captureStdout(t, func() {
		code, exited = captureExit(t, func() {
			runSnapshotRestore(snapshotRestoreCmd, []string{"home.2026"})
		})
	})

	if !exited || code != 1 {
		t.Errorf("--dry-run on an ambiguous config: exit = (%d, %v), want (1, true) — the preview must refuse what the real run refuses (R5.2)",
			code, exited)
	}
	if strings.Contains(out, "would restore") {
		t.Errorf("--dry-run previewed a restore the real invocation would refuse; output:\n%s", out)
	}
	if len(mr.Calls) != 0 {
		t.Errorf("--dry-run ran %d subprocess(es), want 0: %+v", len(mr.Calls), mr.Calls)
	}
}

// TestRunSnapshotRestore_SubvolumeFlagRegisteredAndOptional is R5.1 at the cobra
// layer, mirroring TestRunSnapshotRestore_MissingTargetErrors. --subvolume must
// exist as a flag, and must NOT be MarkFlagRequired: marked required it would be
// mandatory for everyone, which is precisely the single-subvolume deployment
// this story promises not to disturb. Whether it is needed depends on the
// config, so the enforcement belongs to ResolveRestoreSubvolume, not to cobra.
func TestRunSnapshotRestore_SubvolumeFlagRegisteredAndOptional(t *testing.T) {
	if snapshotRestoreCmd.Flags().Lookup("subvolume") == nil {
		t.Fatal("expected a --subvolume flag on `snapshot restore`")
	}

	// Parse the deployed invocation — target + ship, no --subvolume — and reset
	// the parse state afterwards so it does not leak into the tests that share
	// this global command.
	t.Cleanup(func() {
		snapshotRestoreCmd.Flags().Visit(func(f *pflag.Flag) { f.Changed = false })
		snapshotRestoreTarget, snapshotRestoreShip, snapshotRestoreSubvolume, snapshotRestoreYes = "", "", "", false
	})
	if err := snapshotRestoreCmd.ParseFlags([]string{"--target", "/mnt/r", "--ship", "cloud", "--yes"}); err != nil {
		t.Fatalf("ParseFlags(deployed invocation): %v", err)
	}
	if err := snapshotRestoreCmd.ValidateRequiredFlags(); err != nil {
		t.Errorf("required-flag validation rejected the deployed invocation: %v; --subvolume must stay optional (R5.1)", err)
	}
}

// Compile-time check: the package-level confirm seam is assignable to
// snapshot.RestoreOptions.Confirm (the unexported confirmFunc type).
var _ = snapshot.RestoreOptions{Confirm: snapshotRestoreConfirm}
