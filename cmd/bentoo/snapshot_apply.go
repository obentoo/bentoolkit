package main

import (
	"github.com/obentoo/bentoolkit/internal/common/logging"
	"github.com/obentoo/bentoolkit/internal/common/output"
	"github.com/obentoo/bentoolkit/internal/snapshot"
	"github.com/spf13/cobra"
)

// snapshotApplyDryRun is --dry-run: print the apply plan (engine configs +
// systemd units) without writing anything and without calling systemctl
// (008 R2.1).
var snapshotApplyDryRun bool

// newSnapshotApplyCmd builds `snapshot apply`.
func newSnapshotApplyCmd(d *deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:         "apply",
		Annotations: map[string]string{cancellableAnnotation: "true"},
		Short:       "Render native config and install the systemd timer",
		Long: `Load and validate snapshot.toml, render the btrbk.conf, and install +
enable the systemd service/timer. Idempotent: re-running reconciles the units.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSnapshotApply(cmd, args, d)
		},
	}
	cmd.Flags().BoolVar(&snapshotApplyDryRun, "dry-run", false,
		"print the configs and systemd units that would be written, without writing them")
	return cmd
}

func runSnapshotApply(cmd *cobra.Command, _ []string, d *deps) error {
	log := logging.FromContext(commandContext(cmd))
	cfg, path, err := loadSnapshotConfig(log)
	if err != nil {
		log.Error("snapshot apply: failed", "err", err)
		return exitWith(1)
	}

	if snapshotApplyDryRun {
		// 008 R2.1: preview only — print the engine config(s) and systemd units
		// the apply would write, with zero writes and zero subprocesses.
		printDryRunPlan(snapshot.PlanApply(cfg, path))
		return nil
	}

	ctx := commandContext(cmd)

	if err := snapshot.Apply(ctx, cfg, path, d.snapshotRunner); err != nil {
		log.Error("snapshot apply: failed", "err", err)
		return exitWith(1)
	}

	output.PrintSuccess("snapshot configuration applied (%s)", path)
	return nil
}
