package main

import (
	"github.com/obentoo/bentoolkit/internal/common/logging"
	"github.com/obentoo/bentoolkit/internal/snapshot"
	"github.com/spf13/cobra"
)

// snapshotRunDryRun is --dry-run: print the engine/ship pipeline that would
// run without executing it — no engine-config render, no subprocess, and no
// RunResult persisted.
var snapshotRunDryRun bool

// newSnapshotRunCmd builds `snapshot run`.
func newSnapshotRunCmd(d *deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:         "run",
		Annotations: map[string]string{cancellableAnnotation: "true"},
		Short:       "Run the snapshot pipeline now",
		Long: `Execute the engine → prune → ship pipeline for every configured subvolume,
persist a RunResult for 'status', and exit non-zero if any stage failed. This is
the command driven by the systemd timer.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSnapshotRun(cmd, args, d)
		},
	}
	cmd.Flags().BoolVar(&snapshotRunDryRun, "dry-run", false,
		"print the pipeline that would run, without executing it")
	return cmd
}

func runSnapshotRun(cmd *cobra.Command, _ []string, d *deps) error {
	log := logging.FromContext(commandContext(cmd))
	cfg, path, err := loadSnapshotConfig(log)
	if err != nil {
		log.Error("snapshot run: failed", "err", err)
		return exitWith(1)
	}

	if snapshotRunDryRun {
		// Preview only — print the pipeline (engine driver per subvolume, then
		// each ship target) and return BEFORE the engine-config render, the
		// pipeline execution, the RunResult persistence and the REPORT.
		//
		// The report is for a run that FINISHES, and a preview declines to start
		// one: no snapshot.RunResult exists and no step has an outcome. A report
		// here would render empty Steps beside Ok=0/Failed=0, which
		// report.SnapshotRun documents as "a run that ran no step at all", so the
		// preview would look like a real run that achieved nothing.
		//
		// `overlay manifest --dry-run` does end in a report because its preview
		// HOLDS rows: it resolves its targets first, and only the outcomes are
		// missing (report.ManifestRun.DryRun). A snapshot preview has no rows to
		// carry, and report.SnapshotRun has no such field.
		printDryRunPlan(snapshot.PlanRun(cfg))
		return nil
	}

	ctx := commandContext(cmd)

	// Ensure the engine's native config exists (btrbk.conf or the snapper
	// configs) so the run is self-contained even if 'apply' was never executed.
	if err := snapshot.WriteEngineConfig(ctx, cfg, path, d.snapshotRunner); err != nil {
		log.Error("snapshot run: render engine config: failed", "err", err)
		return exitWith(1)
	}

	mgr, err := snapshot.NewManager(*cfg, path, d.snapshotRunner, snapshot.WithManagerLogger(log))
	if err != nil {
		log.Error("snapshot run: failed", "err", err)
		return exitWith(1)
	}

	result, runErr := mgr.Run(ctx)
	if perr := result.SaveLastRun(); perr != nil {
		log.Warn("snapshot run: persist result: failed", "err", perr)
	}

	// The run ends in a report, and it ends in exactly one. There is no
	// success line and no log.Error of the pipeline's error here: two
	// statements of one run's outcome, on two streams, leave the operator no
	// way to tell which is authoritative the day they disagree. The report
	// already names, per subvolume, how each step came out. The error paths
	// ABOVE keep their log.Error calls: before a report exists, the log line is
	// the only statement there is.
	//
	// It is rendered BEFORE the exit branch below because the run that most
	// needs a report is the one that failed.
	//
	// ctx.Err() is the interruption half of the envelope: Manager.Run records a
	// cancellation as a sentence in its ordinary failure field, so the context,
	// which cannot be mistaken for anything else, is what the report is told.
	presentSnapshotReport(log, d, snapshotReportConfig(log),
		buildSnapshotReport(&result, cfg.Engine.Subvolumes, ctx.Err() != nil))

	if runErr != nil {
		return exitWith(1)
	}
	return nil
}
