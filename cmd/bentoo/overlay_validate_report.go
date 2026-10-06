package main

// The seam between `overlay validate` and the one export path the rest of the
// CLI already goes through.
//
// `overlay validate` used to carry the project's SECOND export format: its own
// `--json` flag, over its own report type, in a schema no other command shared.
// One announced break ended that: `--json` is now an alias for `--export` at
// stdout with a `.json` shape, and the run it writes is the same report.Run
// every other command hands to the same writer.
//
// This file moves the FLAG'S MEANING and the ENVELOPE, not the CONTENT: the
// sections a human reads are still drawn by `overlay validate`'s own printer,
// which is why validatePayload is a wrapper rather than a model — a later
// migration has one export path to move into.
//
// The wrapper lives HERE because internal/common/report may not import
// internal/autoupdate (boundary_test.go's forbiddenImports), so the type that
// puts a validate.Report in the payload position sits on the cmd/bentoo side,
// beside overlay_manifest_report.go and snapshot_report.go.

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
// It EMBEDS rather than copies: an embedded field with no json tag is inlined by
// encoding/json, so "payload" carries exactly validate.Report's keys —
// `overlay`, `results`, `unmatched_selector` — with the tags documented in
// internal/autoupdate/validate/report.go. A field-by-field copy would be a
// second declaration of those keys, silently wrong the first time the producer
// gains a field. Nothing is renamed or dropped; the one announced break is the
// `.payload` hop and the two root keys above it.
//
// It is a WRAPPER, not a report.ValidateRun with its own vocabulary and
// Sections, because designing that content is separate work; what this type
// provides is a single export path with the kind already reserved and emitted.
type validatePayload struct {
	validate.Report
}

// Sections is report.Payload's one method, and this payload uses it to say the
// one thing it can honestly say today: the validate report is not in this
// document, and here is where it went.
//
// It DECLARES an absence and does not fill one: which gates get a block, how a
// skip states its reason and what the headline column is are content, so nothing
// below names a package, a gate or a finding, and the real sections can replace
// this block without inheriting a placeholder operators learned to read. What it
// states is a fact about where the content lives, not about the run.
//
// nil was the previous answer, and it left `--export=report.md` and
// `--export=report.txt` writing a file of ZERO BYTES with nothing saying it was
// not a report. The block is UNCONDITIONAL for the same reason: one derived from
// the payload would fall silent on the empty run, where a zero-byte file most
// resembles a clean result.
//
// The `.json` document does not change: render.JSON reaches the payload through
// encoding/json, not this method. The parameter is unnamed because it is not
// read — a block with no unit to list has nothing to shorten or expand.
func (validatePayload) Sections(report.SectionOptions) []report.Section {
	return []report.Section{{
		Title: "Overlay Validation",
		Lead: []string{
			"This document does not carry the validation report itself: no package, no gate and no finding is listed below.",
		},
		Notes: []string{
			"What is missing is missing from this DOCUMENT and not from the run: 'overlay validate' still prints every finding to the terminal, through a printer of its own that this export cannot reach.",
			"A later change migrates that report onto these sections, and replaces this block with the real ones. Until then the run is available in full as JSON: give --export a path ending in .json, or pass --json for the same document at stdout.",
		},
	}}
}

// validateEnvelope puts one validation run's facts inside the envelope every
// exported document carries: the schema version, the kind of run that produced
// it, the reader's label for it, and whether it reached the end of its plan.
// It is manifestEnvelope's shape with the domain swapped.
//
// Kind and title are fixed per COMMAND, never per run, so
// `.kind == "overlay.validate"` is a filter rather than a guess; the title is a
// LABEL, not a discriminator, as report.Run documents it.
//
// Normalized() is applied HERE, on the producer's side: render/json.go writes
// a nil slice as null and leaves the fix to the producer. Normalized turns nil
// Results, Sources and Gates into empty slices, as `--json` always has, so
// `jq '.results[].gates[]'` keeps working and the `.payload` hop stays the only
// break.
//
// NotEvaluated is 0 even on an interrupted run, and that is a fact: validate.Run
// appends an interruptedResult for every target it did not reach
// (internal/autoupdate/validate/run.go), so nothing is missing from the
// document, while Complete still says the run was cut short.
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
// document `--export=<path>.json` writes to a file, at stdout instead.
//
// It goes through renderExport, not an encoder of its own: two writers of "the
// JSON document" is how the CLI ended up with two JSON schemas, and the indent,
// HTML escaping, trailing newline and envelope are decided once in
// render/json.go. exportJSON is named rather than inferred because stdout has
// no extension to read; `--json` IS the extension the operator typed.
//
// A failure goes to diag, not the logger: runValidate points diag at stderr for
// the whole `--json` run, because a diagnostic inside the document would break
// the `| jq` the flag exists for. A failed write does not change the exit
// status: the caller exits on what the run DECIDED, and a stdout that went away
// is not a finding about anybody's ebuild.
func renderValidateJSON(run report.Run, diag io.Writer) {
	if err := renderExport(os.Stdout, run, exportJSON); err != nil {
		_, _ = fmt.Fprintf(diag, "  writing the JSON report: %v\n", err)
	}
}

// validateReportConfig reads the configuration this run resolves its render
// mode against, and answers nil when there is none.
//
// runValidate has already loaded it (loadAppContextNoValidation) but keeps only
// one boolean, and presentValidateReport is called from two places there, so
// it is read again here rather than threaded down. That costs one extra parse
// of a small YAML file and buys the SECOND ambient source, ui.mode: with a nil
// config configuredUIMode would answer BENTOO_UI alone and leave a typo in
// ui.mode silent. The price: LoadFrom warns about an unknown key on every load,
// so a genuine typo is warned about twice on this command — a repetition, not
// a contradiction, and cheaper than a second reader of ui.mode.
//
// A configuration that cannot be read is not an error on this path: a nil
// config leaves --ui, the environment and the terminal deciding, and the miss
// is logged at debug because on a host with no bentoo config it is the normal
// case.
func validateReportConfig(log *slog.Logger) *config.Config {
	cfg, err := config.Load()
	if err != nil {
		log.Debug("overlay validate: no bentoo configuration was read, so ui.mode is not consulted", "err", err)
		return nil
	}
	return cfg
}

// presentValidateReport puts the finished report in front of the operator: the
// terminal first, then the export. It is presentManifestReport's three steps —
// build the run, render it once, export it last — so `--export` works here as
// on every other report-producing command.
//
// asJSON is NOT the same choice as --ui: it picks WHO IS READING (a machine's
// document or a human's text), while --ui picks WHAT THE DEVICE ALLOWS. The
// human half is still renderValidateText, this command's own printer, and the
// one section the payload has declares its own absence (Sections, above), so
// wiring --ui here would offer three ways of drawing one sentence. The mode is
// nonetheless RESOLVED below, because refusing an unusable ambient value out
// loud is owed today; drawing in that mode waits for real sections.
//
// The export happens LAST and UNCONDITIONALLY. A bad export path must not cost
// the operator the report on screen, and exportReport returns nothing so the
// exit status stays validate.Report.ExitCode. Under --json too:
// `--json --export=out.json` asks for the document twice, and answering only
// the first would be this command re-deciding what --export means.
func presentValidateReport(log *slog.Logger, d *deps, rep validate.Report, complete, asJSON bool, diag io.Writer) {
	run := validateEnvelope(rep, complete)

	// Stated for BOTH branches below and drawn by neither: an unusable ambient
	// UI mode (BENTOO_UI or ui.mode) is refused out loud. Before this call,
	// `BENTOO_UI=bogus bentoo overlay validate`, with or without --json, exited 0
	// and stated the refusal 0 times while `overlay manifest` stated it once —
	// the value dropped and nothing said, so nothing on screen let the operator
	// infer the typo.
	//
	// The MODE is dropped because this producer has one human renderer (see
	// Sections); the refusal is what the call is for, in the same words, stream
	// and level as the other producers. A discarded result reads like dead code
	// and is not: TestValidateAmbientModeFromTheEnvironmentKeepsTheRunAndStatesItself
	// and its siblings in validate_ambient_mode_test.go fail without it.
	//
	// Here, before the fork, because both output paths owe the sentence; it goes
	// through the logger's Warn to stderr, away from the --json document. Never
	// in runValidate: a mode resolved before the work and exited on would turn a
	// shell-profile typo into "your validation did not run". Resolved here, the
	// status stays the run's — 1 for an error finding, 2 for a selector that
	// matched nothing, 130 for an interrupt.
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
