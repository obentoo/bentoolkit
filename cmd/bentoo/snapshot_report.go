package main

// The seam between `snapshot run` and the report it now ends in.
//
// Story 046, sub-task 6.2 — R1.2, R1.6, R4.4.
//
// internal/common/report must not import internal/snapshot, and internal/snapshot
// must not know what a report is, so the conversion from one to the other happens
// here and nowhere else. It is overlay_manifest_report.go's shape with the
// package vocabulary taken out, and taking it out is the whole exercise: design.md
// D6 chose this command for its DISTANCE from the check path, so if the envelope
// only fitted package-shaped runs, this file is where that would have been
// discovered.
//
// # What the distance test actually found
//
// Nothing under internal/common/report/render/ needed an edit, no field was added
// to report.Run, and no word in report.SnapshotRun names a package, a version or
// an ebuild. The two envelope fields that describe how far a run got —
// Complete and NotEvaluated — carried subvolumes without being told what a
// subvolume is, because they are phrased over "planned units" and a subvolume is
// one. D1 holds.
//
// The one place it was close is worth recording for task 9. The envelope's
// incomplete label reads "the run was interrupted", which is narrower than
// Run.Complete's own documented meaning ("stopped early — by an interrupt, or by
// a failure it could not continue past"). That wording is why buildSnapshotReport
// below counts an unreached SUBVOLUME rather than an unreached STEP: a snapshot
// run that skips prune and ship because create failed has not been interrupted,
// and marking it incomplete would have printed a sentence that is not true of it.

import (
	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/logger"
	"github.com/obentoo/bentoolkit/internal/common/report"
	"github.com/obentoo/bentoolkit/internal/common/report/render"
	"github.com/obentoo/bentoolkit/internal/snapshot"
)

// snapshotEnvelope puts one snapshot run's facts inside the envelope every
// exported document carries: the schema version, the kind of run that produced
// it, the reader's label for it, and how far down its plan the run got (R4.1,
// R1.4).
//
// # It is the ONE place this command builds a report.Run
//
// The two values it stamps unconditionally are fixed per COMMAND rather than per
// run, and that is what makes R4.4 true: a consumer holding one of these
// documents tells it apart from a manifest run's BY ITS KIND ALONE, because the
// kind is derived from nothing this run establishes. A kind that varied with what
// a run found would answer one string for a run that snapshotted nothing and
// another for a full pipeline, so a consumer filtering on it would receive some
// of this command's documents and silently miss the rest.
//
// The schema is stamped from report.SchemaVersion rather than typed here, for
// the reason that constant gives: a 2 written at each producer is several places
// to find on the day it becomes 3, and nothing that fails when one is missed.
//
// # Complete and NotEvaluated are ARGUMENTS, never read off the payload
//
// The same rule the manifest envelope states, and it is not inherited by
// analogy: the two facts are established by the run — one by whether the context
// was cancelled, the other by which subvolumes the run reached — and they travel
// from there to here. Deriving them from the payload would mean asking the rows
// what the run intended, which is the question rows cannot answer.
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
// reports, whole, before anything is printed (R1.2, R1.3, R1.6).
//
// planned is the subvolume list the run was given — snapshot.Config's
// engine.subvolumes — and it is passed in rather than recovered from result
// because a result only holds the subvolumes the run REACHED. interrupted is
// whether the run's context was cancelled, which is the caller's knowledge for
// the same reason: snapshot.RunResult records a cancellation as a sentence in a
// string field it also uses for ordinary failures, so "was this run cut short"
// cannot be recovered from it without matching on prose.
//
// # Where the step names come from, and why not from the runner
//
// Story 046 set out to have snapshot.Runner accumulate one outcome per step, on
// the measurement that a snapshot run reported no outcome to its caller at all.
// That measurement was wrong, and building the payload is what showed it:
// snapshot.RunResult.Stages is the pairing ALREADY MADE, by the manager that
// sequenced the run. Each entry names the subvolume, the semantic step (create,
// prune, ship, gfs), the destination when there was one, and the outcome. That
// is R1.6's sentence in a struct.
//
// A runner-level list could not have replaced it. The runner sees SUBPROCESSES,
// named after the program — "btrbk", "snapper" — and those names cannot be
// paired with what a report needs to say. One stage may invoke no subprocess at
// all (the ssh ship moves no bytes itself, because btrbk performs the send
// during create), one may invoke several, and the engine-config render before
// the pipeline invokes its own outside every stage. Pairing them positionally
// would attribute a failure to the wrong step, silently.
//
// # A nil result is reported as an empty run, not as a crash
//
// No caller passes one today. A report is nonetheless the thing an operator gets
// INSTEAD of a crash (R1.4), so building one must not be the moment the crash
// arrives — the same reading report.Run.Sections gives a nil payload. The plan is
// still stated, and every planned subvolume is then correctly counted as one the
// run established nothing about.
func buildSnapshotReport(result *snapshot.RunResult, planned []string, interrupted bool) report.Run {
	// Copied rather than aliased: the payload outlives this call, travels into a
	// renderer and into an exported file, and the caller's slice is the live
	// configuration. A report that could change after it was assembled would not
	// be a report of one run (R1.3).
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
	// one nothing is known about, and that is the distinction R1.4 is drawn on.
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
// # It is a translation and nothing else
//
// No stage is filtered, no order is changed and no outcome is decided here. The
// order is the order the manager established, which is a fact about the run
// rather than a presentation choice — sorting here would be this file deciding
// something the producer already decided.
//
// # The status word becomes a bool at this boundary, once
//
// snapshot.StageResult carries "ok" or "failed" as a string because it
// round-trips through the persisted run-result file that `snapshot status` reads.
// The report holds the same fact as a bool, so no renderer has to know the
// producer's spelling, and the comparison is written HERE rather than in each
// place that needs the answer — a second `== StatusOK` elsewhere is a second
// place to get the polarity wrong.
//
// # Err crosses as text, and it crosses whole
//
// The model holds facts and has no behaviour, so it carries a string rather than
// an error — the same crossing the check's scannedFacts and the manifest's
// targets make. It is taken exactly as the producer wrote it and never reworded
// or shortened: the runner has already joined the failing command's stderr onto
// it, and that text is the only thing in the whole document that says what
// actually went wrong.
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
// and nothing else — and R3.1 is why it starts now: --ui, BENTOO_UI and the
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
func snapshotReportConfig() *config.Config {
	cfg, err := config.Load()
	if err != nil {
		logger.Debug("snapshot run: no bentoo configuration was read, so ui.mode is not consulted: %v", err)
		return nil
	}
	return cfg
}

// presentSnapshotReport puts the finished report in front of the operator: the
// terminal first, then the export (R1.2, R3.4, R3.5).
//
// It is presentManifestReport's three steps with nothing added: resolve the
// mode, render the sections once, export the run. That it needed nothing added
// is the evidence D6 asked for — the presentation half of a report is the same
// three calls whether the run counted packages or subvolumes.
//
// # The terminal render happens first, and that ordering is R3.5
//
// An export is a convenience; the report on the terminal is the answer. Writing
// the file first would let a bad path — a directory that does not exist, a
// read-only mount — cost the operator the report itself. Rendering first makes
// that impossible rather than merely unlikely, and exportReport is documented to
// return nothing precisely so a failed export cannot reach into this run's exit
// status either.
//
// # The mode is resolved here, not passed in
//
// reportModeOrPlain is a pure function of the flags, the configuration and the
// terminal, so asking here gives the same answer any other caller gets — and
// --ui is the root's flag now, so the answer is the CLI's rather than one
// command's (R3.1).
//
// What stood here was resolveAutoupdateUIMode and a claim that its error was
// unreachable, because the root had already rejected the value (R3.2). The root
// rejects the FLAG, deliberately and only the flag, so an unusable BENTOO_UI or
// ui.mode reaches this call and is refused at it: the render falls back to
// plain, the refusal is STATED naming the source, the value and the mode used
// instead, and the exit status does not move (R3.7). The rule and the
// measurements behind it are on reportModeOrPlain, which the manifest producer
// calls too, so a typo answered on one command cannot be swallowed on the other.
//
// The exit status not moving matters more on this command than on any other:
// `snapshot run` is what a systemd timer invokes, and a timer that began failing
// over a display key would page somebody about a backup that in fact ran.
//
// # A render that fails is reported and does not stop the export
//
// The two are independent answers to the same report. A terminal that went away
// mid-write is no reason to also withhold the file, which may be the only copy
// left — and on a timer-driven run it usually is.
func presentSnapshotReport(d *deps, cfg *config.Config, run report.Run) {
	mode := reportModeOrPlain(cfg, false, d.uiIsTerminal)

	// Two questions, kept apart. What the report should SAY — list every step
	// that succeeded, or count them — is report.SectionOptions, answered here
	// from the root's --all. What the DEVICE allows is render.Options, and its
	// Width is left at zero: "ask the device". A number typed here would be a
	// hard-coded field width in a file this story exists to keep free of them.
	content := report.SectionOptions{ShowAll: autoupdateAll}

	// The sections are built ONCE and handed to whichever renderer the mode
	// names, which is what makes "the modes differ in presentation and not in
	// content" a fact about this call rather than a promise about three
	// renderers (R2.1). renderCheckReportIn is named for the command that first
	// needed it and knows nothing about one: it takes sections, and nothing below
	// it can tell a snapshot run from a check.
	if err := renderCheckReportIn(mode, run.Sections(content), render.Options{}); err != nil {
		logger.Warn("the report could not be rendered: %v", err)
	}

	// LAST, and unconditional. exportReport is the CLI's one export path
	// (report_export.go): it does nothing when --export named no path, it warns
	// rather than fails when the path cannot be written, and it returns nothing
	// so this run's exit status cannot be altered by a copy of an answer already
	// delivered above.
	exportReport(run)
}
