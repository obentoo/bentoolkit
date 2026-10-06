package main

// The seam between `snapshot run` and the report it ends in.
//
// internal/common/report must not import internal/snapshot, and internal/snapshot
// must not know what a report is, so the conversion happens here and nowhere
// else. It is overlay_manifest_report.go's shape without the package
// vocabulary, which proves the envelope is not package-shaped: nothing under
// render/ needed an edit, report.Run gained no field, and report.SnapshotRun
// names no package, version or ebuild. Complete and NotEvaluated carry
// subvolumes because they are phrased over "planned units".
//
// One wording constrains this file: the envelope's incomplete label reads "the
// run was interrupted", narrower than Run.Complete's documented meaning
// (stopped early by an interrupt or by a failure). So buildSnapshotReport counts
// an unreached SUBVOLUME, not an unreached STEP: a run that skips prune and ship
// because create failed was not interrupted, and marking it incomplete would
// print a sentence that is not true of it.

import (
	"log/slog"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/report"
	"github.com/obentoo/bentoolkit/internal/common/report/render"
	"github.com/obentoo/bentoolkit/internal/snapshot"
)

// snapshotEnvelope puts one snapshot run's facts inside the envelope every
// exported document carries: the schema version, the kind of run that produced
// it, the reader's label for it, and how far down its plan the run got.
//
// It is the ONE place this command builds a report.Run. Kind is fixed per
// COMMAND, derived from nothing the run establishes, so a consumer tells these
// documents apart from a manifest run's by kind alone; a kind that varied with
// what a run found would make a filter on it silently miss some documents. The
// schema comes from report.SchemaVersion, so the day it changes there is one
// place to edit, not one per producer.
//
// Complete and NotEvaluated are ARGUMENTS, never read off the payload: the run
// establishes them (whether its context was cancelled, which subvolumes it
// reached), and rows cannot say what the run intended.
func snapshotEnvelope(payload report.SnapshotRun, complete bool, notEvaluated int) report.Run {
	return report.Run{
		Schema: report.SchemaVersion,
		Kind:   report.KindSnapshotRun,
		// A fixed label, exactly like the kind: it names the command that
		// produced the document in a reader's own words and is derived from
		// nothing the run established. report.Run.Title is documented as a
		// label rather than as something to match on, and Kind is what a
		// consumer discriminates with.
		Title:        "Snapshot run",
		Complete:     complete,
		NotEvaluated: notEvaluated,
		Payload:      payload,
	}
}

// buildSnapshotReport turns what a pipeline run returned into the run it
// reports, whole, before anything is printed.
//
// planned is engine.subvolumes, passed in because a result only holds the
// subvolumes the run REACHED. interrupted is the caller's knowledge too:
// snapshot.RunResult records a cancellation as prose in its ordinary failure
// field, and prose is not matched on.
//
// The steps come from snapshot.RunResult.Stages, the pairing the manager that
// sequenced the run ALREADY MADE: subvolume, semantic step (create, prune, ship,
// gfs), destination and outcome. The runner cannot supply them: it sees
// SUBPROCESSES named after the program, and a stage may run none (the ssh ship;
// btrbk sends during create) or several, so pairing them positionally would
// blame the wrong step, silently.
//
// A nil result is reported as an empty run, not a crash — a report is what the
// operator gets INSTEAD of a crash. The plan is still stated, and every planned
// subvolume is counted as one the run established nothing about.
func buildSnapshotReport(result *snapshot.RunResult, planned []string, interrupted bool) report.Run {
	// Copied rather than aliased: the payload outlives this call, travels into a
	// renderer and into an exported file, and the caller's slice is the live
	// configuration. A report that could change after it was assembled would not
	// be a report of one run.
	payload := report.SnapshotRun{Subvolumes: append([]string(nil), planned...)}

	var stages []snapshot.StageResult
	if result != nil {
		stages = result.Stages
	}

	payload.Steps = make([]report.SnapshotStep, 0, len(stages))

	// unreached starts as the whole plan and loses a subvolume the moment a
	// stage speaks for it. What is left at the end is the gap the envelope
	// reports.
	//
	// A subvolume with a stage has an OUTCOME, even when that outcome is a
	// failure that stopped its remaining stages — the run established something
	// about it, and the row says what. A subvolume with no stage at all is the
	// one nothing is known about, and that is the distinction NotEvaluated
	// counts.
	//
	// A set rather than a counter, so a configuration that named the same
	// subvolume twice contributes one unit rather than two: naming a subvolume
	// twice does not create a second subvolume to reach.
	unreached := make(map[string]struct{}, len(planned))
	for _, subvolume := range planned {
		unreached[subvolume] = struct{}{}
	}

	for _, stage := range stages {
		step := snapshotStepFacts(stage)
		payload.Steps = append(payload.Steps, step)

		// Counted from the SAME value the row prints, in the same pass, which is
		// what keeps the number above the table and the word on the row from
		// describing different runs (the internal/overlay ManifestUpdate.fail
		// precedent). Ok+Failed therefore always equals len(Steps): there is no
		// third status a stage can carry, and if one is ever added it lands in
		// this branch rather than in neither.
		if step.Success {
			payload.Ok++
		} else {
			payload.Failed++
		}

		// A stage attributable to no subvolume — a sweep of a remote holding
		// objects from all of them — deletes nothing, because it speaks for
		// none of the plan. delete on an absent key is a no-op, which is
		// exactly the reading wanted here.
		delete(unreached, stage.Subvolume)
	}

	notEvaluated := len(unreached)

	// # Complete is NOT `notEvaluated == 0`, and the difference is the whole
	// # point of the manifest precedent
	//
	// A run cancelled once its LAST subvolume had already finished leaves a gap
	// of zero, so "no gap means it finished" would tell the operator who pressed
	// ctrl+c that their run ended normally. Whether the run was cut short is read
	// from the run — from its own context — and the gap is reported beside it,
	// including when the gap is zero: the envelope's label states the number
	// either way, because "interrupted, and nothing was lost" is an answer and
	// silence is not.
	//
	// The converse matters just as much here and is what the unit choice buys: a
	// run whose create failed skipped that subvolume's prune and ship, and it is
	// still COMPLETE. It reached the end of its plan and reported an outcome for
	// every subvolume in it. Counting those skipped stages as "not evaluated"
	// would have put "the run was interrupted" above the report of a run that
	// nobody interrupted.
	return snapshotEnvelope(payload, !interrupted && notEvaluated == 0, notEvaluated)
}

// snapshotStepFacts is one pipeline stage as the model spells it.
//
// It is a translation and nothing else: no stage is filtered and no order is
// changed, because the order is a fact the manager established.
//
// The status word becomes a bool here, once. snapshot.StageResult carries "ok"
// or "failed" as a string because it round-trips through the run-result file
// `snapshot status` reads; a second `== StatusOK` elsewhere would be a second
// place to get the polarity wrong.
//
// Err crosses as text, whole and unreworded: the model holds facts, not
// behaviour, and the runner has already joined the failing command's stderr
// onto it — the only text in the document that says what went wrong.
func snapshotStepFacts(stage snapshot.StageResult) report.SnapshotStep {
	return report.SnapshotStep{
		Subvolume: stage.Subvolume,
		Step:      stage.Stage,
		Target:    stage.Target,
		Success:   stage.Status == snapshot.StatusOK,
		Error:     stage.Err,
	}
}

// snapshotReportConfig reads the configuration the report resolves its renderer
// against, and answers nil when there is none.
//
// `snapshot run` has never read the bentoo configuration — it reads snapshot.toml
// and nothing else — and it reads it now because --ui, BENTOO_UI and the
// ui.mode key are the CLI's precedence chain, and a command that consulted only
// the first two would honour an operator's configuration everywhere except here.
//
// # A configuration that cannot be read is not an error on this path
//
// This runs under a systemd timer as root, where there may be no bentoo config at
// all, and the answer to "which renderer" must never be the reason a snapshot run
// fails. A nil config is read as "nothing configured" by configuredUIMode, which
// leaves the flag, the environment and the terminal deciding — exactly what this
// command did before the key existed. The miss is logged at debug level rather
// than warned about, because on the intended host it is the normal case.
func snapshotReportConfig(log *slog.Logger) *config.Config {
	cfg, err := config.Load()
	if err != nil {
		log.Debug("snapshot run: no bentoo configuration was read, so ui.mode is not consulted", "err", err)
		return nil
	}
	return cfg
}

// presentSnapshotReport puts the finished report in front of the operator: the
// terminal first, then the export. It is presentManifestReport's three steps
// with nothing added — resolve the mode, render the sections once, export.
//
// The terminal render comes first: the report on the terminal is the answer
// and the export a convenience, so a bad path — a missing directory, a
// read-only mount — must not cost the operator the report itself.
//
// The mode is resolved here by reportModeOrPlain, a pure function of the
// flags, the configuration and the terminal. The root rejects only an unusable
// --ui FLAG, so an unusable BENTOO_UI or ui.mode reaches this call: the render
// falls back to plain, the refusal is STATED naming the source, the value and
// the mode used, and the exit status does not move. That matters most here:
// `snapshot run` is what a systemd timer invokes, and a timer failing over a
// display key would page somebody about a backup that in fact ran.
//
// A render that fails is reported and does not stop the export: a terminal
// gone mid-write is no reason to withhold the file, often the only copy left.
func presentSnapshotReport(log *slog.Logger, d *deps, cfg *config.Config, run report.Run) {
	mode := reportModeOrPlain(log, cfg, false, d.uiIsTerminal)

	// Two questions, kept apart. What the report should SAY — list every step
	// that succeeded, or count them — is report.SectionOptions, answered here
	// from the root's --all. What the DEVICE allows is render.Options, and its
	// Width is left at zero: "ask the device". A number typed here would be a
	// hard-coded field width, which the renderers exist to keep out of here.
	content := report.SectionOptions{ShowAll: autoupdateAll}

	// The sections are built ONCE and handed to whichever renderer the mode
	// names, which is what makes "the modes differ in presentation and not in
	// content" a fact about this call rather than a promise about three
	// renderers. renderCheckReportIn is named for the command that first
	// needed it and knows nothing about one: it takes sections, and nothing below
	// it can tell a snapshot run from a check.
	if err := renderCheckReportIn(mode, run.Sections(content), render.Options{}); err != nil {
		log.Warn("the report could not be rendered", "err", err)
	}

	// LAST, and unconditional. exportReport is the CLI's one export path
	// (report_export.go): it does nothing when --export named no path, it warns
	// rather than fails when the path cannot be written, and it returns nothing
	// so this run's exit status cannot be altered by a copy of an answer already
	// delivered above.
	exportReport(log, run)
}
