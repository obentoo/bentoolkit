package main

import (
	"github.com/obentoo/bentoolkit/internal/common/logger"
	"github.com/obentoo/bentoolkit/internal/common/output"
	"github.com/obentoo/bentoolkit/internal/snapshot"
	"github.com/spf13/cobra"
)

// snapshotApplyDryRun stubs the full --dry-run coverage (completed in story 008).
var snapshotApplyDryRun bool

var snapshotApplyCmd = &cobra.Command{
	Use:   "apply",
	Short: "Render native config and install the systemd timer",
	Long: `Load and validate snapshot.toml, render the btrbk.conf, and install +
enable the systemd service/timer. Idempotent: re-running reconciles the units.`,
	Run: runSnapshotApply,
}

func init() {
	snapshotApplyCmd.Flags().BoolVar(&snapshotApplyDryRun, "dry-run", false,
		"print the actions without applying them (stub; full coverage in story 008)")
	snapshotCmd.AddCommand(snapshotApplyCmd)
}

func runSnapshotApply(cmd *cobra.Command, _ []string) {
	cfg, path, err := loadSnapshotConfig()
	if err != nil {
		logger.Error("snapshot apply: %v", err)
		osExit(1)
		return
	}

	if snapshotApplyDryRun {
		output.PrintInfo("dry-run: would render %s and install the systemd units", snapshot.BtrbkConfPath(path))
		return
	}

	ctx, stop := signalContext(cmd.Context())
	defer stop()

	if err := snapshot.Apply(ctx, cfg, path, snapshotRunner); err != nil {
		logger.Error("snapshot apply: %v", err)
		osExit(1)
		return
	}

	output.PrintSuccess("snapshot configuration applied (%s)", path)
}
