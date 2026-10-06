package main

// The seam between `overlay validate` and the one export path the rest of the
// CLI already goes through.
//
// Story 046, sub-task 8.1 — R4.3, R3.5, design D8.
//
// `overlay validate` carried the project's SECOND export format: its own
// `--json` flag, over its own report type, in a schema no other command shared.
// A consumer that had learned to read `--export`'s documents could not read this
// one, and nothing in either document said why. D8 spends one announced break to
// end that: `--json` becomes an alias for `--export` at stdout with a `.json`
// shape, and the run it writes is the same report.Run every other command hands
// to the same writer.
//
// # What this file moves, and what it deliberately does not
//
// It moves the FLAG'S MEANING and the ENVELOPE. The validate report's CONTENT —
// the sections a human reads — migrates in story 047, and nothing here designs
// it. That split is the reason validatePayload below is a wrapper rather than a
// model: the payload is what `overlay validate` already assembles, wrapped, so
// that 047 has one export path to migrate into rather than a choice to
// re-litigate.
//
// # Why the wrapper lives HERE and not in internal/common/report
//
// internal/common/report may not import internal/autoupdate — boundary_test.go's
// forbiddenImports makes that mechanical, and its remedy names this kind of file
// as the fix. So the type that puts a validate.Report in the payload position has
// to sit on the cmd/bentoo side of the boundary, which is where
// overlay_manifest_report.go and snapshot_report.go already sit for the same
// reason. This is the third instance of one register, not a new arrangement.

import (
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/report"
)

// validatePayload is one validation run's own facts, standing in the payload
// position of the envelope.
//
// # It EMBEDS rather than copies, and that is the whole of its job
//
// An embedded struct field with no json tag is inlined by encoding/json, so the
// document under "payload" carries exactly the keys validate.Report has always
// carried — `overlay`, `results`, `unmatched_selector` — with the same tags,
// which are documented in internal/autoupdate/validate/report.go as the contract
// they are. A field-by-field copy here would be a second declaration of those
// keys, wrong the first time somebody adds a field to the producer and forgets
// this file, and silent on both sides when it happened.
//
// The consequence a reader should take from that: this story renames nothing and
// drops nothing. The break it spends is the `.payload` hop and the two root keys
// above it, and one break is what the CHANGELOG announces.
//
// # It is a WRAPPER because story 047 replaces it
//
// A proper report.ValidateRun payload — primitive facts, its own vocabulary, its
// own Sections — is 047's work, and doing it here would be designing the content
// this story's Out of Scope removes from it. What this story owes 047 is a single
// export path with the kind already reserved and emitted, which is all this type
// provides.
type validatePayload struct {
	validate.Report
}

// Sections is report.Payload's one method, and this payload uses it to say the
// one thing it can honestly say today: the validate report is not in this
// document, and here is where it went.
//
// # It DECLARES an absence; it does not fill one
//
// Sections are the blocks a human-facing renderer draws — plain, Markdown,
// inline, fullscreen. Turning a validation run into them means deciding what a
// validation run SAYS: which gates get a block, how a skip states its reason,
// what the headline column is. That is content, it is story 047's, and this
// story's Out of Scope says so in as many words. So nothing below names a
// package, a gate or a finding, and there is nothing here for 047 to
// un-invent: it replaces this block with the real ones rather than inheriting a
// placeholder an operator has already learned to read.
//
// What this block states is a fact about THE SPLIT, not about the run — which
// is why it is knowable today when the content is not.
//
// # nil was the previous answer, and it was the silent kind of honest
//
// Returning nil left `overlay validate --export=report.md` and
// `--export=report.txt` writing a file of ZERO BYTES: those two renderers
// consume sections, and there were none. An operator asked for a report,
// received a file, and nothing anywhere said the two were not the same thing —
// which is the silent omission R2.3 forbids in as many words. One section is
// what it costs to stop making the reader guess.
//
// # It is UNCONDITIONAL, and that is the requirement rather than an oversight
//
// The same block is returned for a run that failed three gates and for a run
// over a clean overlay, because what it states is equally true of both. A
// declaration derived from the payload instead would fall silent for the empty
// run — the case least able to survive it, and the one where a zero-byte file
// most resembles a clean result.
//
// # The `.json` document does not move, and cannot
//
// render.JSON serializes the envelope and reaches this payload's fields through
// encoding/json rather than through this method, so `--export=<path>.json` — and
// `--json`, which is the same path at stdout — writes byte for byte what it
// wrote before this block existed. The consumer asked once, this release, to add
// a `.payload` hop finds no second change riding along with it.
//
// The parameter is unnamed because it is not read. report.SectionOptions says
// whether a report lists every unit or counts them, and a block with no unit to
// list has nothing to shorten or expand.
func (validatePayload) Sections(report.SectionOptions) []report.Section {
	return []report.Section{{
		Title: "Overlay Validation",
		Lead: []string{
			"This document does not carry the validation report itself: no package, no gate and no finding is listed below.",
		},
		Notes: []string{
			"What is missing is missing from this DOCUMENT and not from the run: 'overlay validate' still prints every finding to the terminal, through a printer of its own that this export cannot reach.",
			"Story 047 migrates that report onto these sections, and replaces this block with the real ones. Until then the run is available in full as JSON: give --export a path ending in .json, or pass --json for the same document at stdout.",
		},
	}}
}

// validateEnvelope puts one validation run's facts inside the envelope every
// exported document carries: the schema version, the kind of run that produced
// it, the reader's label for it, and whether it reached the end of its plan
// (R4.1, R4.3).
//
// It is manifestEnvelope's shape with the domain swapped, and every decision it
// makes is that function's decision, made for a reason that has not changed just
// because the command did.
//
// # The kind and the title are fixed per COMMAND, never per run
//
// report.KindOverlayValidate was reserved in sub-task 1.3 for exactly this call
// site, and this is the change that starts emitting it. It is derived from
// nothing this run establishes — not the overlay, not how many ebuilds matched,
// not whether a gate failed — which is what makes `.kind == "overlay.validate"`
// a filter rather than a guess. A kind that varied with what a run found would
// answer one string for an empty run and another for a full one, so a consumer
// filtering on it would receive some of this command's documents and silently
// miss the rest.
//
// The title is a LABEL and not a discriminator, exactly as report.Run documents
// it: two runs may share one, and a consumer that matched on it would be
// matching on prose.
//
// # Normalized() is applied HERE, on the producer's side of the boundary
//
// render/json.go states the rule it will not break: a nil slice reaches the wire
// as null, and a consumer that trips over one is looking at a producer that left
// a slice nil — fixed there, not by a quiet rewrite on the way out. This is
// "there". validate.Report.Normalized turns nil Results, Sources and Gates into
// empty slices, `overlay validate --json` has applied it since the flag existed,
// and `jq '.results[].gates[]'` works today because of it.
//
// Dropping it would be a SECOND break riding along with the announced one: the
// same consumer that adds `.payload` would also have to start guarding against
// null. This story spends one break, and this is how it stays one.
//
// # NotEvaluated is 0 even on an interrupted run, and that is a fact not a stub
//
// The envelope's gap counts planned units the run NEVER REACHED. validate.Run has
// none: its governing rule is that a package in view is never left unmentioned,
// so a cancelled sweep appends an interruptedResult for every remaining target
// (internal/autoupdate/validate/run.go, both cancellation branches) and the
// report still lists them all. Nothing is missing from the document, so the
// honest count is zero — while Complete still says the run was cut short, which
// is the fact the operator needs and the one the count cannot carry.
func validateEnvelope(rep validate.Report, complete bool) report.Run {
	return report.Run{
		Schema:       report.SchemaVersion,
		Kind:         report.KindOverlayValidate,
		Title:        "Overlay validation",
		Complete:     complete,
		NotEvaluated: 0,
		Payload:      validatePayload{Report: rep.Normalized()},
	}
}

// renderValidateJSON writes the whole run to stdout as ONE document — the
// document `--export=<path>.json` writes to a file, at stdout instead (R4.3).
//
// # It goes through renderExport, which is the entire point of sub-task 8.1
//
// Not through an encoder of its own. Two writers producing "the JSON document"
// is how the CLI ended up with two JSON schemas in the first place, and a second
// one would drift again — the indent, the HTML escaping, the trailing newline
// and the envelope are all decided in render/json.go, once, for every caller.
// exportJSON is named rather than inferred because there is no path here for
// exportFormatFor to read an extension off: stdout has no extension, and `--json`
// IS the extension the operator typed.
//
// # It still reports to diag rather than to the logger
//
// This is the one JSON surface where stdout belongs to the document alone, so
// runValidate points diag at stderr for the whole `--json` run — a diagnostic in
// the middle of the document would break the `| jq` the flag exists for. Passing
// that same writer keeps this failure in the stream every other diagnostic of
// this run went to, rather than opening a third voice for one line.
//
// A failed write is REPORTED and does not change the exit status: the caller
// below exits on what the run DECIDED, and a stdout that went away is not a
// finding about anybody's ebuild (R3.5).
func renderValidateJSON(run report.Run, diag io.Writer) {
	if err := renderExport(os.Stdout, run, exportJSON); err != nil {
		_, _ = fmt.Fprintf(diag, "  writing the JSON report: %v\n", err)
	}
}

// validateReportConfig reads the configuration this run resolves its render
// mode against, and answers nil when there is none.
//
// # Why this file reads it rather than being handed it
//
// `func runValidate` in overlay_validate.go has already loaded it,
// by calling `func loadAppContextNoValidation` in app_context.go,
// and keeps one boolean out of it. What it does not do
// is carry the *config.Config down here, and presentValidateReport is called
// from two places in that file, so growing a parameter would edit a file this
// change has no other reason to touch. Reading it here costs one extra parse of
// one small YAML file per run, and buys the SECOND ambient source: with a nil
// config configuredUIMode reads "nothing configured", which answers BENTOO_UI
// and leaves a typo in ui.mode exactly as silent as it was (R3.7).
//
// The cost that is not free: LoadFrom reports an unknown key in that file on
// every load, so a config with a genuine typo in it now draws that warning twice
// on this command — once for the run's load, once for this one. Both name the
// same key and the same file, which is a repetition rather than a
// contradiction; the alternative is a second reader of ui.mode, and a second
// reader of a key is how two answers to it get written.
//
// # A configuration that cannot be read is not an error on this path
//
// It is snapshotReportConfig's reading and runValidate's own: a condition that
// stops the gate becomes a reported outcome, never an aborted run. A nil config
// leaves --ui, the environment and the terminal deciding — exactly what this
// command did before ui.mode existed — and the miss is logged at debug because
// on a host with no bentoo config it is the normal case, not a fault.
func validateReportConfig(log *slog.Logger) *config.Config {
	cfg, err := config.Load()
	if err != nil {
		log.Debug("overlay validate: no bentoo configuration was read, so ui.mode is not consulted", "err", err)
		return nil
	}
	return cfg
}

// presentValidateReport puts the finished report in front of the operator: the
// terminal first, then the export (R3.4, R3.5, R4.3).
//
// It is presentManifestReport's three steps in this command's vocabulary — build
// the run, render it once, export it last — and the third step is what this
// sub-task adds. `overlay validate` is now reachable by `--export` like every
// other report-producing command, so an operator who learned the flag on
// `overlay autoupdate` does not have to discover that one command spells it
// differently.
//
// # The two terminal renderers are NOT the same choice as --ui
//
// asJSON picks between a machine's document and a human's text, and that is a
// choice about WHO IS READING. --ui picks between plain, inline and fullscreen,
// which is a choice about WHAT THE DEVICE ALLOWS, and this command does not make
// it yet: its human half is still renderValidateText, its own printer, until
// story 047 gives the payload the CONTENT the shared renderers would draw. The
// one section it has today declares its own absence and nothing else (Sections,
// above), so wiring --ui here would offer three ways of drawing one sentence —
// a flag that changes the frame and never the answer, which is worse than one
// that is not there.
//
// The mode is nonetheless RESOLVED in the body below, and the two facts do not
// contradict: refusing an unusable ambient value out loud (R3.7) and drawing a
// report in the mode that survives the refusal are separate obligations. This
// command owes the first today and the second in 047. The call carries the rest.
//
// # The export happens LAST, and that ordering is R3.5
//
// An export is an additional copy; the report the operator is looking at is the
// answer. Writing the file first would let a bad path — a missing directory, a
// read-only mount — cost them the report itself. Rendering first makes that
// impossible rather than merely unlikely, and exportReport returns nothing
// precisely so a failed export cannot reach this run's exit status either: the
// caller exits on validate.Report.ExitCode, computed from what the gates said
// and from nothing about a file.
//
// It is also UNCONDITIONAL, including under --json. `--json --export=out.json`
// asks for the document twice, in two places, and answering only the first would
// be this command re-deciding what --export means for itself — which is the
// arrangement D8 exists to end.
func presentValidateReport(log *slog.Logger, d *deps, rep validate.Report, complete, asJSON bool, diag io.Writer) {
	run := validateEnvelope(rep, complete)

	// R3.7, stated for BOTH branches below and drawn by neither.
	//
	// # What was measured here before this line existed
	//
	// One fixture, one overlay, the same unusable value in the same variable,
	// the refusal counted on stderr:
	//
	//	BENTOO_UI=bogus bentoo overlay manifest --dry-run   exit 0, stated once
	//	BENTOO_UI=bogus bentoo overlay validate             exit 0, stated 0 times
	//	BENTOO_UI=bogus bentoo overlay validate --json      exit 0, stated 0 times
	//
	// This producer resolved no mode at all, so there was no error to report and
	// nothing was reported — design.md's forbidden third option, "the value
	// dropped and nothing said", on the one command whose flag semantics story
	// 046 exists to change, and the one an operator is most likely to be running
	// from a script whose shell profile they did not write. The report they got
	// was identical to the one they would have got with no BENTOO_UI at all, so
	// there was nothing in front of them from which the typo could be inferred.
	//
	// # The MODE is dropped; the SENTENCE is what this call is for
	//
	// reportModeOrPlain answers "which renderer", and this producer has exactly
	// one until story 047 — the section above says why. So the mode has no
	// consumer here and is dropped where a reader can see it being dropped. The
	// refusal is not droppable: it is what R3.7 owes, in the same words, on the
	// same stream, at the same level as the other three producers state it, so
	// an operator grepping for it finds this command beside them rather than
	// missing from them.
	//
	// A call whose result is discarded reads like dead code and is not. Delete
	// it and the silence measured above returns, unannounced;
	// TestValidateAmbientModeFromTheEnvironmentKeepsTheRunAndStatesItself and
	// its two siblings in validate_ambient_mode_test.go are what say so.
	//
	// # Here, and not at the top of the run
	//
	// Before the fork because both output paths owe the sentence and neither can
	// state it for the other: placed in the human branch it reaches nobody
	// piping to jq, placed in the JSON branch it reaches nobody reading a
	// terminal. Under --json it lands on stderr, where the document is not, so
	// the `| jq` the flag exists for is untouched.
	//
	// Never in runValidate, which is the cheap way to the same three facts and
	// the way the check producer got it wrong: a mode resolved before the work
	// and exited on turned "your shell profile has a typo" into "your validation
	// did not run", and answered a run failing for a reason of its own with a
	// diagnostic about a display key that could not have caused it. Resolved
	// here, the status stays the run's — 1 for an error finding, 2 for a
	// selector that matched nothing, 130 for an interrupt — and every one of
	// them keeps meaning what this command's help says it means.
	//
	// # On stderr, which is not an exception to this file's stream rule
	//
	// reportModeOrPlain states it through the invocation logger's Warn, so it is on stderr on
	// both paths. overlay_validate.go's rule that the default mode's text goes
	// to stdout is about THE REPORT — a SKIPPED line and the reason beside it
	// have to be read together — and this is not the report. It is a fact about
	// the display, which is why the design puts it on stderr for every producer.
	_ = reportModeOrPlain(log, validateReportConfig(log), false, d.uiIsTerminal)

	if asJSON {
		renderValidateJSON(run, diag)
	} else {
		renderValidateText(rep)
	}

	// LAST, and unconditional. exportReport is the CLI's one export path
	// (report_export.go): it does nothing when --export named no path, it warns
	// rather than fails when the path cannot be written, and it returns nothing
	// so this run's exit status cannot be altered by a copy of an answer already
	// delivered above.
	exportReport(log, run)
}
