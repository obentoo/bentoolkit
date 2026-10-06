package main

// The seam between `overlay manifest` and the report it now ends in.
//
// internal/common/report must not import internal/overlay, and internal/overlay
// must not know what a report is, so the conversion from one to the other
// happens here and nowhere else. It replicates the seam at
// overlay_autoupdate_report.go: every decision below has a counterpart there,
// made for a reason that has not changed just because the domain did.
//
// # Why this command is the one that proves the envelope
//
// `overlay manifest` was chosen for its DISTANCE from the check path. It
// runs parallel workers, streams a live tail per worker, captures a subprocess's
// output and counts two columns rather than four. An envelope that serves both
// it and the check is an envelope; one that only served the check would be the
// autoupdate model with a new name, and nothing would have said so until a third
// command tried to use it.

import (
	"log/slog"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/report"
	"github.com/obentoo/bentoolkit/internal/common/report/render"
	"github.com/obentoo/bentoolkit/internal/overlay"
)

// manifestEnvelope puts one manifest run's facts inside the envelope every
// exported document carries: the schema version, the kind of run that produced
// it, the reader's label for it, and how far down its target list the run got.
// It is the ONE place this command builds a report.Run.
//
// Kind and Title are fixed per COMMAND rather than per run, so a consumer tells
// these documents apart from a check's BY KIND ALONE; a kind that varied with
// what a run found would let a consumer filtering on it silently miss some.
// The schema is stamped from report.SchemaVersion so a version bump has one
// place to change.
//
// Complete and NotEvaluated are ARGUMENTS, never derived from the payload by
// comparing counts against len(Targets): a dry run would come out "incomplete"
// because neither count moved, and an interrupted run whose last target
// happened to finish would come out complete. Completeness is established by
// the run that did the work.
func manifestEnvelope(payload report.ManifestRun, complete bool, notEvaluated int) report.Run {
	return report.Run{
		Schema: report.SchemaVersion,
		Kind:   report.KindOverlayManifest,
		// A fixed label, exactly like the kind: it names the command that
		// produced the document in a reader's own words and is derived from
		// nothing the run established. report.Run.Title is documented as a
		// label rather than as something to match on, and Kind is what a
		// consumer discriminates with.
		Title:        "Manifest regeneration",
		Complete:     complete,
		NotEvaluated: notEvaluated,
		Payload:      payload,
	}
}

// buildManifestReport turns what a regeneration run returned into the run it
// reports, whole, before anything is printed.
//
// THE DRY-RUN TRAP: overlay.RegenerateManifests returns every previewed target
// with Success false, so Failed() equals the number of targets on a `--dry-run`
// that ran nothing — a report would tell the operator every package just failed,
// on the one invocation chosen because it changes nothing. So the preview is
// answered BEFORE a count is taken (as overlay.FormatManifestResult does): zero
// and zero, with report.ManifestRun.DryRun carrying the reason to every renderer
// and the export.
//
// A nil result is reported as an empty run, not a crash: a report is what the
// operator gets INSTEAD of a crash.
//
// Whether the run finished is READ OFF THE RUN (Interrupted, NotEvaluated), set
// by RegenerateManifests where both the input count and the answers are known.
// It is never computed here: a run cut short after its LAST target completed has
// a gap of zero, so Complete is !Interrupted and not NotEvaluated == 0.
func buildManifestReport(result *overlay.ManifestResult, dryRun bool) report.Run {
	payload := report.ManifestRun{DryRun: dryRun}
	if result == nil {
		// No run to ask, so no gap to state: the empty report of a run that
		// established nothing, rather than one that claims to have been cut
		// short.
		return manifestEnvelope(payload, true, 0)
	}

	payload.Targets = make([]report.ManifestTarget, 0, len(result.Updates))
	for _, update := range result.Updates {
		payload.Targets = append(payload.Targets, manifestTargetFacts(update))
	}

	if !dryRun {
		// Read from the result, which derives both from one slice in one place,
		// so the pair cannot disagree with the rows listed under it. On an
		// interrupted run that slice holds only the targets the run answered
		// for, so the pair counts what was learned and the envelope below states
		// the rest — nothing lands in both, and nothing lands in neither.
		payload.Ok, payload.Failed = result.Ok(), result.Failed()
	}

	return manifestEnvelope(payload, !result.Interrupted, result.NotEvaluated)
}

// manifestTargetFacts is one target as the model spells it — a translation and
// nothing else: no target is filtered, reordered or decided here. The order is
// the one RegenerateManifests preserved, a fact about the run, not a
// presentation choice.
//
// The atom is joined here, once, so no renderer picks its own separator.
//
// update.Error crosses as text, exactly as the producer wrote it, because the
// model holds facts and no behaviour. update.Err (the same failure as a wrapped
// value) does not cross: nothing downstream can unwrap it, and what the report
// needs — which target, and why — is already in the fields beside it.
//
// Output is copied whole. A renderer with a line budget cuts its own copy and
// marks the cut; the JSON export carries the exact bytes, because whoever reads
// that file does so because the terminal that streamed them is gone.
func manifestTargetFacts(update overlay.ManifestUpdate) report.ManifestTarget {
	return report.ManifestTarget{
		Package: update.Category + "/" + update.Package,
		Success: update.Success,
		Error:   update.Error,
		Output:  update.Output,
	}
}

// presentManifestReport puts the finished report in front of the operator: the
// terminal first, then the export. It is presentCheckReport's shape minus the
// check-specific half — resolve the mode, render the sections once, export the
// run — which is why the next command to grow a report copies this one.
//
// It must be called AFTER the live UI has been torn down (finishUI in
// runManifest): rendering while the live region owns the terminal would put the
// report into a frame the TUI then redraws over.
//
// The terminal render comes first because the report there is the answer and
// the export a convenience: a bad export path must not cost the operator the
// report, and exportReport returns nothing so it cannot alter the exit status.
//
// The mode is resolved here through reportModeOrPlain, a pure function of the
// flags, config and terminal. The root rejects only an invalid --ui FLAG; an
// unusable BENTOO_UI or ui.mode reaches this call, which falls back to plain,
// states the refusal (source, value, mode used) and leaves the exit status alone.
// A render that fails is reported and does not stop the export, which may be
// the only copy left.
func presentManifestReport(log *slog.Logger, d *deps, cfg *config.Config, run report.Run) {
	mode := reportModeOrPlain(log, cfg, false, d.uiIsTerminal)

	// Two questions, kept apart. What the report should SAY — list every target
	// that succeeded, or count them — is report.SectionOptions, answered here
	// from the root's --all. What the DEVICE allows is render.Options, and its
	// Width is left at zero: "ask the device". A number typed here would be a
	// hard-coded field width, which the renderers exist to keep out of producers.
	content := report.SectionOptions{ShowAll: autoupdateAll}

	// The sections are built ONCE and handed to whichever renderer the mode
	// names, which is what makes "the modes differ in presentation and not in
	// content" a fact about this call rather than a promise about three
	// renderers. renderCheckReportIn is named for the command that first
	// needed it and knows nothing about one: it takes sections, and nothing
	// below it can tell a manifest run from a check.
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
