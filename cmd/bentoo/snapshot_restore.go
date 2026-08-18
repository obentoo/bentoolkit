package main

import (
	"errors"

	"github.com/obentoo/bentoolkit/internal/common/logger"
	"github.com/obentoo/bentoolkit/internal/common/output"
	"github.com/obentoo/bentoolkit/internal/snapshot"
	"github.com/spf13/cobra"
)

var (
	// snapshotRestoreTarget is --target: the path the snapshot is materialised
	// into (required — a restore must know where to write the subvolume).
	snapshotRestoreTarget string
	// snapshotRestoreShip is --ship: the name of the [[ship]] entry whose driver
	// and locators drive the restore (required — it selects archive vs restic and
	// where to read from).
	snapshotRestoreShip string
	// snapshotRestoreSubvolume is --subvolume: which configured subvolume the
	// restore reads its objects back from, since each subvolume has its own
	// remote prefix (038 R3.2). Deliberately NOT MarkFlagRequired: with exactly
	// one subvolume configured — the currently deployed shape — there is nothing
	// to disambiguate and the flag stays unnecessary (R5.1). With two or more it
	// becomes mandatory in practice, enforced by ResolveRestoreSubvolume rather
	// than by cobra, because "required" here depends on the config, not the
	// command line (R5.2).
	snapshotRestoreSubvolume string
	// snapshotRestoreYes is --yes/-y: skip the destructive-restore confirm prompt.
	snapshotRestoreYes bool
	// snapshotRestoreDryRun is --dry-run: print the destructive actions without
	// performing them — no subprocess and no confirm prompt (008 R2.3).
	snapshotRestoreDryRun bool
	// snapshotRestoreConfirm is the confirm seam. nil (the default) makes
	// snapshot.Restore fall back to its own stdin y/N prompt (defaultConfirmFunc);
	// tests override it to inject a yes/no decision without terminal I/O. A plain
	// func(string) bool is assignable to RestoreOptions.Confirm (the unexported
	// confirmFunc type) from package main.
	snapshotRestoreConfirm func(string) bool
)

var snapshotRestoreCmd = &cobra.Command{
	Use:   "restore <id>",
	Short: "Restore a snapshot from a ship target into a path",
	Long: `Restore snapshot <id> into --target using the named --ship entry.

The ship's type selects the path: an "archive" ship replays the object chain
(rclone cat | decompress | btrfs receive); a "restic" ship runs restic restore.
Restore is DESTRUCTIVE — it writes a subvolume into the target — so it prompts for
confirmation unless --yes is given.

Each subvolume's objects live under their own remote prefix, so a restore has to
know which subvolume it reads from. With exactly one subvolume configured that is
implied and --subvolume is unnecessary; with several, name one — the restore
refuses to guess rather than read another subvolume's backups.`,
	Args: cobra.ExactArgs(1),
	Run:  runSnapshotRestore,
}

func init() {
	snapshotRestoreCmd.Flags().StringVar(&snapshotRestoreTarget, "target", "",
		"path to restore the snapshot into (required)")
	snapshotRestoreCmd.Flags().StringVar(&snapshotRestoreShip, "ship", "",
		"name of the [[ship]] entry that drives the restore (required)")
	snapshotRestoreCmd.Flags().StringVar(&snapshotRestoreSubvolume, "subvolume", "",
		"which configured subvolume to restore from (needed only when several are configured)")
	snapshotRestoreCmd.Flags().BoolVarP(&snapshotRestoreYes, "yes", "y", false,
		"skip the destructive-restore confirmation prompt")
	snapshotRestoreCmd.Flags().BoolVar(&snapshotRestoreDryRun, "dry-run", false,
		"print the destructive restore actions without performing them")
	_ = snapshotRestoreCmd.MarkFlagRequired("target")
	_ = snapshotRestoreCmd.MarkFlagRequired("ship")
	snapshotCmd.AddCommand(snapshotRestoreCmd)
}

func runSnapshotRestore(cmd *cobra.Command, args []string) {
	id := args[0]

	// Restore is destructive: load AND validate the config (drivers + deps) so an
	// unknown driver or missing binary fails fast before any subprocess (R6.1, G3).
	cfg, _, err := loadSnapshotConfig()
	if err != nil {
		logger.Error("snapshot restore: %v", err)
		osExit(1)
		return
	}

	ship, ok := findShipByName(cfg, snapshotRestoreShip)
	if !ok {
		logger.Error("snapshot restore: no ship entry named %q", snapshotRestoreShip)
		osExit(1)
		return
	}

	// WHICH subvolume this restore reads back from is decided HERE — before the
	// dry-run preview, before RestoreOptions is built, and therefore before any
	// subprocess (038 R5.2, R5.3). Each subvolume owns a remote prefix, so on a
	// config with several of them there is no safe guess: guessing would replay
	// another subvolume's objects over --target. The rule itself lives in the
	// snapshot package (ResolveRestoreSubvolume) so it is provable as a pure
	// function over (config, flag); this handler only calls it and maps a failure
	// onto a non-zero exit. Its message already names the configured subvolumes
	// (R5.2) or the rejected value (R5.3) and reads correctly after this prefix.
	//
	// BEFORE the dry-run return on purpose: a preview of a destructive command
	// exists to show what the real run would do, and the real run refuses here.
	// A dry-run that printed "would restore ..." on a config the real invocation
	// rejects would be a preview of something that cannot happen.
	//
	// Unconditional, not archive-only: the ambiguity is in the REQUEST, and
	// making the gate depend on the named ship's type would accept or refuse the
	// same config according to which ship was named. A restic ship simply
	// discards the value (RestoreChainFor returns no chain for it).
	subvolume, err := snapshot.ResolveRestoreSubvolume(cfg, snapshotRestoreSubvolume)
	if err != nil {
		logger.Error("snapshot restore: %v", err)
		osExit(1)
		return
	}

	if snapshotRestoreDryRun {
		// 008 R2.3: preview only — the ship is resolved (an unknown --ship still
		// failed above) and so is the subvolume, but nothing runs: no
		// snapshot.Restore, no subprocess, and the confirm gate is never consulted.
		output.PrintInfo("dry-run: would restore snapshot %s into %s via ship %q (%s)",
			id, snapshotRestoreTarget, ship.Name, ship.Type)
		return
	}

	opts := snapshot.RestoreOptions{
		Driver:  ship.Type,
		Yes:     snapshotRestoreYes,
		Run:     snapshotRunner,
		Confirm: snapshotRestoreConfirm,

		// archive driver: replay the object chain for id from the rclone remote.
		// SCOPE NOTE — the multi-link full→target chain reconstruction is future
		// work (T6.1); for this verb the chain is a SINGLE full link for the
		// requested id. chainLink is unexported, which is WHY the chain is built
		// INSIDE the snapshot package by snapshot.RestoreChainFor and assigned
		// straight into the field here. The object key it derives is
		// "<ArchivePrefix(subvolume)>/<id>.zst" for the subvolume RESOLVED above —
		// not for the engine's first configured one, which is what used to send
		// every restore on a multi-subvolume config to the wrong prefix (038 R3.2).
		// A restic restore ignores Chain entirely (RestoreChainFor returns nil for
		// it, and the resolved subvolume goes unused).
		Remote:   ship.Remote,
		Compress: ship.Compress,
		Chain:    snapshot.RestoreChainFor(cfg, ship, id, subvolume),

		// restic driver: non-secret locators only (R6.1) — repo URL + password FILE.
		Repo:         ship.Repo,
		PasswordFile: ship.PasswordFile,
	}

	ctx, stop := signalContext(cmd.Context())
	defer stop()

	err = snapshot.Restore(ctx, id, snapshotRestoreTarget, opts)
	switch {
	case err == nil:
		output.PrintSuccess("snapshot restored (%s → %s)", id, snapshotRestoreTarget)
	case errors.Is(err, snapshot.ErrRestoreDeclined):
		// Declining a destructive restore is a clean abort, not a failure: report
		// it and return WITHOUT osExit(1) so the exit code stays 0 (R5.4).
		output.PrintInfo("restore declined")
	default:
		logger.Error("snapshot restore: %v", err)
		osExit(1)
	}
}

// findShipByName returns the [[ship]] entry whose Name matches name. Match is by
// Name only (the operator addresses ships by their configured name); a Type-only
// fallback is intentionally NOT used so an ambiguous multi-ship config cannot pick
// the wrong target silently.
func findShipByName(cfg *snapshot.Config, name string) (snapshot.ShipConfig, bool) {
	for _, sh := range cfg.Ship {
		if sh.Name == name {
			return sh, true
		}
	}
	return snapshot.ShipConfig{}, false
}
