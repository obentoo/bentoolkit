package main

import (
	"github.com/obentoo/bentoolkit/internal/common/logger"
	"github.com/obentoo/bentoolkit/internal/common/output"
	"github.com/obentoo/bentoolkit/internal/snapshot"
	"github.com/spf13/cobra"
)

// snapshotApplyDryRun is --dry-run: print the apply plan (engine configs +
// systemd units) without writing anything and without calling systemctl
// (008 R2.1).
var snapshotApplyDryRun bool

// newSnapshotApplyCmd builds `snapshot apply`.
func newSnapshotApplyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:         "apply",
		Annotations: map[string]string{cancellableAnnotation: "true"},
		Short:       "Render native config and install the systemd timer",
		Long: `Load and validate snapshot.toml, render the btrbk.conf, and install +
enable the systemd service/timer. Idempotent: re-running reconciles the units.`,
		Run: runSnapshotApply,
	}
	cmd.Flags().BoolVar(&snapshotApplyDryRun, "dry-run", false,
		"print the configs and systemd units that would be written, without writing them")
	return cmd
}

func runSnapshotApply(cmd *cobra.Command, _ []string) {
	cfg, path, err := loadSnapshotConfig()
	if err != nil {
		logger.Error("snapshot apply: %v", err)
		osExit(1)
		return
	}

	if snapshotApplyDryRun {
		// 008 R2.1: preview only — print the engine config(s) and systemd units
		// the apply would write, with zero writes and zero subprocesses.
		printDryRunPlan(snapshot.PlanApply(cfg, path))
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
