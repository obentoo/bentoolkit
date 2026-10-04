package main

// The seam between `overlay manifest` and the report it now ends in.
//
// Story 046, sub-task 5.2 — R1.1, R1.5, R4.4.
//
// internal/common/report must not import internal/overlay, and internal/overlay
// must not know what a report is, so the conversion from one to the other
// happens here and nowhere else. It is the same seam story 044 established at
// overlay_autoupdate_report.go, replicated for the first command that never had
// one — and "replicated" is the word: every decision below has a counterpart
// there, made for a reason that has not changed just because the domain did.
//
// # Why this command is the one that proves the envelope
//
// `overlay manifest` was chosen for its DISTANCE from the check path (D6). It
// runs parallel workers, streams a live tail per worker, captures a subprocess's
// output and counts two columns rather than four. An envelope that serves both
// it and the check is an envelope; one that only served the check would be the
// autoupdate model with a new name, and nothing would have said so until a third
// command tried to use it.

import (
	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/logger"
	"github.com/obentoo/bentoolkit/internal/common/report"
	"github.com/obentoo/bentoolkit/internal/common/report/render"
	"github.com/obentoo/bentoolkit/internal/overlay"
)

// manifestEnvelope puts one manifest run's facts inside the envelope every
// exported document carries: the schema version, the kind of run that produced
// it, the reader's label for it, and how far down its target list the run got
// (R4.1, R1.4).
//
// # It is the ONE place this command builds a report.Run
//
// The two values it stamps unconditionally are fixed per COMMAND rather than
// per run, and that is what makes R4.4 true: a consumer holding one of these
// documents tells it apart from a check's BY ITS KIND ALONE, because the kind is
// derived from nothing this run establishes. A kind that varied with what a run
// found would answer one string for an empty run and another for a full one, so
// a consumer filtering on it would receive some of this command's documents and
// silently miss the rest.
//
// The schema is stamped from report.SchemaVersion for the reason the check's
// envelope gives: a 2 typed at each producer is the same defect as a column
// width typed into a format string — several places to find on the day it
// changes, and nothing that fails when one is missed.
//
// # Complete and NotEvaluated are ARGUMENTS, never read off the payload
//
// Deriving them here — comparing the counts against len(Targets) — is a
// different rule wearing the same answer, and it is wrong in both directions.
// A dry run would come out "incomplete" because neither count moved, when it
// reached the end of everything it set out to do; and an interrupted run whose
// last target happened to finish would come out complete. Completeness is
// established by the run that did the work, and it travels from there to here.
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
// reports, whole, before anything is printed (R1.1, R1.3, R1.5).
//
// # THE DRY-RUN TRAP, and why the branch comes before the counts
//
// overlay.RegenerateManifests returns every previewed target with Success
// false. Nothing ran, so nothing succeeded — a defensible reading on the
// producer's side, and one that makes ManifestResult.Failed() equal the number
// of targets and Ok() equal zero on a `--dry-run` that invoked no command at
// all. Handing those two numbers to a report would tell an operator that every
// package in their overlay had just failed, on the one invocation they chose
// precisely because it changes nothing.
//
// So the preview is answered BEFORE a count is taken, which is the same order
// overlay.FormatManifestResult uses to avoid the same trap. Zero and zero are
// the honest pair: this run established nothing about any target, and
// report.ManifestRun.DryRun is what carries the reason on to every renderer and
// into the export, so no reader has to reconstruct it from a full table sitting
// beside two zeroes.
//
// # A nil result is reported as an empty run, not as a crash
//
// No caller passes one today. A report is nonetheless the thing an operator gets
// INSTEAD of a crash (R1.4), so building one must not be the moment the crash
// arrives — the same reading overlay.FormatManifestResult gives a nil result and
// the same reading report.Run.Sections gives a nil payload.
//
// # Whether the run finished is READ OFF THE RUN, never worked out here
//
// overlay.ManifestResult carries two facts that no amount of looking at its
// rows could reconstruct: Interrupted, and NotEvaluated — how many targets the
// run never established an outcome for. They are set by RegenerateManifests, in
// the one place that knows both how many targets went in and which came back
// with something to say.
//
// This function copies them across and does not compute them. The temptation is
// obvious and the failure is quiet: a run that was cut short after its LAST
// target completed has a gap of zero, so "gap == 0 means it finished" would tell
// the operator who pressed ctrl+c that their run ended normally. Complete is
// !Interrupted for exactly that reason, and not NotEvaluated == 0.
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

// manifestTargetFacts is one target as the model spells it.
//
// # It is a translation and nothing else
//
// No target is filtered, no order is changed and no outcome is decided here.
// The order is the order RegenerateManifests preserved, which is the order the
// targets were resolved in, and it is a fact about the run rather than a
// presentation choice — sorting here would be this file deciding something the
// producer already decided.
//
// # The atom is joined here, once
//
// Category and Package are separate on the producer's struct because the
// filesystem is; they are one string on the model's because every reader prints
// them as one. Joining at this boundary is what stops each renderer from picking
// its own separator.
//
// # Error crosses as text and Err does not cross at all
//
// The model holds facts and has no behaviour, so it carries a string rather than
// an error — the same crossing the check's scannedFacts makes. update.Error is
// the sentence, taken exactly as the producer wrote it and never reworded, which
// is what keeps it actionable; update.Err is the same failure as a value, with
// this target's atom wrapped into it, and it stays on the producer's side
// because nothing downstream of here can unwrap anything. What the report needs
// from it — which target, and why — is already in the two fields beside it.
//
// # Output is copied whole
//
// Not shortened, not folded, not trimmed. A renderer with a line budget cuts its
// own copy and marks the cut; the JSON export carries these bytes exactly as the
// failing command produced them, because the operator reading that file is doing
// so precisely because the terminal that streamed them is gone (R5.2).
func manifestTargetFacts(update overlay.ManifestUpdate) report.ManifestTarget {
	return report.ManifestTarget{
		Package: update.Category + "/" + update.Package,
		Success: update.Success,
		Error:   update.Error,
		Output:  update.Output,
	}
}

// presentManifestReport puts the finished report in front of the operator: the
// terminal first, then the export (R1.1, R3.4, R3.5).
//
// It is presentCheckReport's shape with the check-specific half removed, and
// what is left after that removal is worth naming: resolve the mode, render the
// sections once, export the run. Nothing in those three steps is about packages,
// which is why the next command to grow a report copies this function rather
// than the one it came from.
//
// # It must be called AFTER the live UI has been torn down
//
// A manifest run drives a live region that owns the terminal while it is up, and
// this writes to the same stdout. Rendering before the program has stopped would
// put the report into a frame the TUI then redraws over, so the caller's
// finishUI() is the point this function may first be called at — the one place
// in runManifest where the terminal has been handed back. That ordering lives at
// the call site because only the call site holds the teardown.
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
// measurements behind it are on reportModeOrPlain, which owns both halves so
// that this producer and the snapshot one cannot come to answer differently.
//
// # A render that fails is reported and does not stop the export
//
// The two are independent answers to the same report. A terminal that went away
// mid-write is no reason to also withhold the file, which may be the only copy
// left.
func presentManifestReport(cfg *config.Config, run report.Run) {
	mode := reportModeOrPlain(cfg, false, uiIsTerminal)

	// Two questions, kept apart. What the report should SAY — list every target
	// that succeeded, or count them — is report.SectionOptions, answered here
	// from the root's --all. What the DEVICE allows is render.Options, and its
	// Width is left at zero: "ask the device". A number typed here would be a
	// hard-coded field width in a file this story exists to keep free of them.
	content := report.SectionOptions{ShowAll: autoupdateAll}

	// The sections are built ONCE and handed to whichever renderer the mode
	// names, which is what makes "the modes differ in presentation and not in
	// content" a fact about this call rather than a promise about three
	// renderers (R2.1). renderCheckReportIn is named for the command that first
	// needed it and knows nothing about one: it takes sections, and nothing
	// below it can tell a manifest run from a check.
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
