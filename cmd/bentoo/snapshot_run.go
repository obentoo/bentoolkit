package main

import (
	"github.com/obentoo/bentoolkit/internal/common/logging"
	"github.com/obentoo/bentoolkit/internal/snapshot"
	"github.com/spf13/cobra"
)

// snapshotRunDryRun is --dry-run: print the engine/ship pipeline that would
// run without executing it — no engine-config render, no subprocess, and no
// RunResult persisted (008 R2.2).
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
		// 008 R2.2: preview only — print the pipeline (engine driver per
		// subvolume, then each ship target) and return BEFORE the engine-config
		// render, the pipeline execution, and the RunResult persistence below.
		//
		// It returns before the REPORT as well, and that is the decision rather
		// than the one path story 046 missed (S046-R1.2).
		//
		// R1.2 binds "WHEN snapshot run FINISHES", and a preview does not finish a
		// run — it declines to start one. Nothing below this line executes, so no
		// snapshot.RunResult exists to report and no step of any subvolume has an
		// outcome. All a report could describe here is PlanRun's []string: the
		// same conditional sentences printed on the next line, with no status to
		// put beside them. Reporting that as a run would render empty Steps beside
		// Ok=0/Failed=0 — which report.SnapshotRun documents as "a run that ran no
		// step at all" — so the preview would be indistinguishable from a real run
		// that achieved nothing.
		//
		// `overlay manifest --dry-run` DOES end in a report, and the difference is
		// what each preview HOLDS rather than a disagreement between two commands.
		// That one resolves its targets off the filesystem before previewing them,
		// so RegenerateManifests hands back a row per target and only the outcomes
		// are missing; report.ManifestRun.DryRun is the preview arm that says so
		// before any count is taken (overlay_manifest_report.go). A snapshot
		// preview has no rows to carry, and report.SnapshotRun has no such field.
		// Adding one would answer a new requirement about previews, not this one
		// about runs.
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

	// The run ends in a report, and it ends in exactly one (S046-R1.2).
	//
	// What stood here was output.PrintSuccess("snapshot run completed (%d
	// stages)") on the way out of a successful run, and log.Error with the
	// pipeline's own error on the way out of a failed one. Both are gone, and
	// they are gone for the reason runManifest states for its own removal: two
	// statements of one run's outcome, in two voices on two streams, is the
	// defect rather than a safety net — an operator would have no way to tell
	// which of the two was authoritative the day they disagreed.
	//
	// Neither was a loss. "3 stages" answered neither of R1.6's questions —
	// which subvolume, and how each step came out — and the failure line said
	// "snapshot run completed with failures", which is the number the report now
	// prints beside the name of every step that produced it. The error paths
	// ABOVE this point keep their log.Error calls, and the rule is the same
	// one: before a report exists, the log line is the only statement there is;
	// after it exists, a second one is a competing account of the same run.
	//
	// It is rendered BEFORE the exit branch below, not after, because the run
	// that most needs a report is the one that failed. A report reached only on
	// success would be missing exactly when it is read.
	//
	// ctx.Err() is the interruption half of the envelope: Manager.Run records a
	// cancellation as a sentence in the same string field it uses for ordinary
	// failures, so the context — which cannot be mistaken for anything else — is
	// what the report is told.
	presentSnapshotReport(log, d, snapshotReportConfig(log),
		buildSnapshotReport(&result, cfg.Engine.Subvolumes, ctx.Err() != nil))

	if runErr != nil {
		return exitWith(1)
	}
	return nil
}
