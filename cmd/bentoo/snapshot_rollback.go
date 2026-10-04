package main

import (
	"errors"

	"github.com/obentoo/bentoolkit/internal/common/logger"
	"github.com/obentoo/bentoolkit/internal/common/output"
	"github.com/obentoo/bentoolkit/internal/snapshot"
	"github.com/spf13/cobra"
)

var (
	// snapshotRollbackYes is --yes/-y: skip the destructive-rollback confirm prompt.
	snapshotRollbackYes bool
	// snapshotRollbackDryRun is --dry-run: print the destructive action without
	// performing it — no subprocess and no confirm prompt (008 R2.3).
	snapshotRollbackDryRun bool
)

// newSnapshotRollbackCmd builds `snapshot rollback`.
func newSnapshotRollbackCmd(d *deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:         "rollback <id>",
		Annotations: map[string]string{cancellableAnnotation: "true"},
		Short:       "Roll the system back to a snapshot (snapper only)",
		Long: `Roll the system back to snapshot <id> via snapper rollback.

Rollback is snapper-specific: it requires engine.driver = "snapper" and is refused
for any other engine. It is DESTRUCTIVE — snapper makes a read-write copy of
snapshot <id> the new default subvolume, so the system boots into it on the next
reboot — and prompts for confirmation unless --yes is given.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSnapshotRollback(cmd, args, d)
		},
	}
	cmd.Flags().BoolVarP(&snapshotRollbackYes, "yes", "y", false,
		"skip the destructive-rollback confirmation prompt")
	cmd.Flags().BoolVar(&snapshotRollbackDryRun, "dry-run", false,
		"print the destructive rollback action without performing it")
	return cmd
}

func runSnapshotRollback(cmd *cobra.Command, args []string, d *deps) error {
	id := args[0]

	// Rollback is destructive: load AND validate the config (drivers + deps) so an
	// unknown driver or missing binary fails fast before any subprocess (R5.1, G3).
	cfg, _, err := loadSnapshotConfig()
	if err != nil {
		logger.Error("snapshot rollback: %v", err)
		return exitWith(1)
	}

	if snapshotRollbackDryRun {
		// 008 R2.3: preview only — nothing runs: no snapshot.Rollback, no
		// subprocess, and the confirm gate is never consulted.
		output.PrintInfo("dry-run: would run snapper rollback to snapshot %s — the system boots from it on next reboot", id)
		return nil
	}

	opts := snapshot.RollbackOptions{
		Yes:     snapshotRollbackYes,
		Confirm: d.snapshotRollbackConfirm,
		Run:     d.snapshotRunner,
	}

	ctx := commandContext(cmd)

	err = snapshot.Rollback(ctx, cfg, id, opts)
	switch {
	case err == nil:
		output.PrintSuccess("rollback to snapshot %s started — reboot to complete", id)
	case errors.Is(err, snapshot.ErrRollbackDeclined):
		// Declining a destructive rollback is a clean abort, not a failure: report
		// it and return nil — no exit status — so the exit code stays 0 (R3.2).
		output.PrintInfo("rollback declined")
	default:
		// Includes ErrRollbackUnsupported (R3.3): a refused engine is a hard error.
		logger.Error("snapshot rollback: %v", err)
		return exitWith(1)
	}
	return nil
}
