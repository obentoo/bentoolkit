package main

import (
	"github.com/obentoo/bentoolkit/internal/common/logger"
	"github.com/obentoo/bentoolkit/internal/common/output"
	"github.com/obentoo/bentoolkit/internal/snapshot"
	"github.com/spf13/cobra"
)

var snapshotRunCmd = &cobra.Command{
	Use:   "run",
	Short: "Run the snapshot pipeline now",
	Long: `Execute the engine → prune → ship pipeline for every configured subvolume,
persist a RunResult for 'status', and exit non-zero if any stage failed. This is
the command driven by the systemd timer.`,
	Run: runSnapshotRun,
}

func init() {
	snapshotCmd.AddCommand(snapshotRunCmd)
}

func runSnapshotRun(cmd *cobra.Command, _ []string) {
	cfg, path, err := loadSnapshotConfig()
	if err != nil {
		logger.Error("snapshot run: %v", err)
		osExit(1)
		return
	}

	ctx, stop := signalContext(cmd.Context())
	defer stop()

	// Ensure the engine's native config exists (btrbk.conf or the snapper
	// configs) so the run is self-contained even if 'apply' was never executed.
	if err := snapshot.WriteEngineConfig(cfg, path); err != nil {
		logger.Error("snapshot run: render engine config: %v", err)
		osExit(1)
		return
	}

	mgr, err := snapshot.NewManager(*cfg, path, snapshotRunner)
	if err != nil {
		logger.Error("snapshot run: %v", err)
		osExit(1)
		return
	}

	result, runErr := mgr.Run(ctx)
	if perr := result.SaveLastRun(); perr != nil {
		logger.Warn("snapshot run: persist result: %v", perr)
	}
	if runErr != nil {
		logger.Error("snapshot run: %v", runErr)
		osExit(1)
		return
	}

	output.PrintSuccess("snapshot run completed (%d stages)", len(result.Stages))
}
