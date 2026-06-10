package main

import (
	"github.com/obentoo/bentoolkit/internal/common/logger"
	"github.com/obentoo/bentoolkit/internal/common/output"
	"github.com/obentoo/bentoolkit/internal/snapshot"
	"github.com/spf13/cobra"
)

var (
	// snapshotPruneDryRun is --dry-run: print the retention actions that would
	// run — the engine prune per subvolume plus the remote GFS per archive
	// ship — with zero subprocesses and zero writes (008 R3.2, G3).
	snapshotPruneDryRun bool
	// snapshotPruneShip is --ship: scope the prune to the named [[ship]] entry
	// only — the engine-local prune is skipped and just that ship's remote is
	// pruned (008 R3.2).
	snapshotPruneShip string
)

var snapshotPruneCmd = &cobra.Command{
	Use:   "prune",
	Short: "Apply the retention policy on demand",
	Long: `Apply the [engine.retention] policy now, without taking a snapshot: the
engine-native prune for every subvolume (btrbk clean / snapper cleanup timeline)
plus the GFS retention sweep on every archive ship's rclone remote.

--ship NAME scopes the prune to that one destination: the engine-local prune is
skipped and only the named ship's remote is pruned. Recorded incremental parents
are never deleted — each is the base of its subvolume's next incremental send.`,
	Run: runSnapshotPrune,
}

func init() {
	snapshotPruneCmd.Flags().BoolVar(&snapshotPruneDryRun, "dry-run", false,
		"print the prune actions that would run, without executing them")
	snapshotPruneCmd.Flags().StringVar(&snapshotPruneShip, "ship", "",
		"scope the prune to the named [[ship]] destination")
	snapshotCmd.AddCommand(snapshotPruneCmd)
}

// runSnapshotPrune applies the [engine.retention] policy on demand (008 R3.1):
// the engine-native prune per subvolume plus the remote GFS per archive ship,
// honoring --dry-run and --ship scoping (008 R3.2).
func runSnapshotPrune(cmd *cobra.Command, _ []string) {
	// Prune is destructive: load AND validate the config (drivers + deps) so an
	// unknown driver or missing binary fails fast before any subprocess (G3).
	cfg, path, err := loadSnapshotConfig()
	if err != nil {
		logger.Error("snapshot prune: %v", err)
		osExit(1)
		return
	}

	// An unknown --ship is a hard error BEFORE any write or subprocess — in
	// dry-run too — mirroring restore's unknown-ship handling.
	if snapshotPruneShip != "" {
		if _, ok := findShipByName(cfg, snapshotPruneShip); !ok {
			logger.Error("snapshot prune: no ship entry named %q", snapshotPruneShip)
			osExit(1)
			return
		}
	}

	if snapshotPruneDryRun {
		// 008 R3.2: preview only — print the retention plan and return BEFORE
		// the engine-config render and the Manager build: zero subprocesses,
		// zero writes (G3).
		printDryRunPlan(snapshot.PlanPrune(cfg, snapshotPruneShip))
		return
	}

	ctx, stop := signalContext(cmd.Context())
	defer stop()

	// The engine-native prune reads the native config (btrbk clean takes
	// -c btrbk.conf), so ensure it exists — mirroring `run`. Skipped under
	// --ship scoping, where the engine-local prune does not run at all.
	if snapshotPruneShip == "" {
		if err := snapshot.WriteEngineConfig(cfg, path); err != nil {
			logger.Error("snapshot prune: render engine config: %v", err)
			osExit(1)
			return
		}
	}

	mgr, err := snapshot.NewManager(*cfg, path, snapshotRunner)
	if err != nil {
		logger.Error("snapshot prune: %v", err)
		osExit(1)
		return
	}

	result, pruneErr := mgr.Prune(ctx, snapshotPruneShip)
	if pruneErr != nil {
		// Covers failed stages too: Prune returns a non-nil error whenever the
		// result records any failure — a user-invoked prune must not hide them.
		logger.Error("snapshot prune: %v", pruneErr)
		osExit(1)
		return
	}

	output.PrintSuccess("snapshot prune completed (%d stages)", len(result.Stages))
}
