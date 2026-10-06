package main

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
	"github.com/obentoo/bentoolkit/internal/common/report"
)

// checkEnvelope puts one check run's facts inside the envelope every exported
// document carries: the schema version, the kind of run that produced it, the
// reader's label for it, and how far down its plan the run got.
//
// It is the ONE place this command builds a report.Run. The kind and schema
// version are fixed per COMMAND rather than per run: a kind that varied with
// what a run found would be a discriminator nobody could filter on, and a
// schema version typed at each producer leaves several places to find on the
// day it changes. One construction site makes both true by construction.
//
// Complete and NotEvaluated are ARGUMENTS, never read off the payload.
// Deriving them — len(Results) against len(Plan) — would read any report whose
// result list is shorter than its plan as interrupted, including one built by
// hand to describe a run that never validated. Completeness is established by
// the run that did the work, and it travels from there to here.
func checkEnvelope(payload report.AutoupdateCheck, complete bool, notEvaluated int) report.Run {
	return report.Run{
		Schema: report.SchemaVersion,
		Kind:   report.KindAutoupdateCheck,
		// A fixed label, exactly like the kind: it names the command that
		// produced the document in a reader's own words and is derived from
		// nothing the run established. report.Run.Title is documented as a
		// label rather than as something to match on — a title that varied
		// with the contents would be prose — and Kind is what a consumer
		// discriminates with.
		Title:        "Autoupdate check",
		Complete:     complete,
		NotEvaluated: notEvaluated,
		Payload:      payload,
	}
}

// nothingValidated is the run a `--check` that ran no gate contributes: the
// envelope, around the payload's zero value.
//
// Five paths reach it. Four are runPendingValidation's — the `--llm` gate, an
// unreadable or empty pending list, a declined confirmation, an applier that
// would not build — and the fifth is the single-package path, which validates
// nothing by design. Every one of them established the same two things: no
// facts, and no gap.
//
// It is COMPLETE because nothing was planned, so nothing was left unreached; a
// false would draw the `Run Interrupted` block over a run that finished.
//
// The payload is the ZERO value, and its nil slices are the point: a nil slice
// reaches the JSON export as null and an empty one as [], and the first says
// the producer established nothing. This run validated nothing, so the absence
// is carried rather than replaced with an invented empty collection.
func nothingValidated() report.Run {
	// Nothing was planned, so nothing was left unreached: complete, with a gap
	// of zero.
	return checkEnvelope(report.AutoupdateCheck{}, true, 0)
}

// checkPayload is this command's own facts, read back out of the run that
// carries them.
//
// The type assertion belongs HERE and nowhere below: report.Run.Payload is an
// interface so that nothing in internal/common/report or its renderers has to
// name a concrete payload, and cmd/bentoo is where this payload is BUILT, so it
// already knows the concrete type.
//
// A payload of another type answers the zero value, and SAYS SO. checkEnvelope
// is the only writer today, but checkReport takes a report.Run of ANY kind; a
// manifest or snapshot run reaching here would tell the operator "No packages
// configured for autoupdate" about a run that scanned plenty. It is logged at
// Debug, not Warn, because it is unreachable in a real run and a line an
// operator cannot act on is noise. The zero value is still returned — "nothing
// scanned, nothing planned", the silent arm of presentCheckReport — because a
// report is what an operator gets INSTEAD of a crash, so reading one must not
// be the moment the crash arrives.
func checkPayload(log *slog.Logger, run report.Run) report.AutoupdateCheck {
	payload, ok := run.Payload.(report.AutoupdateCheck)
	if !ok {
		log.Debug("check: the run carries an unexpected kind or payload — reporting it as empty",
			"kind", run.Kind, "payload_type", fmt.Sprintf("%T", run.Payload), "want_kind", report.KindAutoupdateCheck)
	}
	return payload
}

// buildReport turns one run's plan and the results its gates produced into the
// run it reports, whole, before anything is printed.
//
// This is the seam between the producer and the view: internal/common/report
// must not import internal/autoupdate, so the conversion from validate's types
// into the model's primitive facts happens here and nowhere else.
//
// results[i] is the answer for plan.Entries[i] — the caller's contract, and what
// makes the tally reconcilable: each row is classified against ITS OWN plan
// entry rather than a lookup that could miss. A short results slice is a run
// that stopped part way, reported as such (Complete, NotEvaluated).
//
// Scanned is left to the caller: this function never sees the scan, and a nil
// Scanned reaches the JSON export as null rather than as an invented empty list.
//
// The whole run comes back, envelope included, because "how far down the plan
// the run got" is established here and only here; stating it on the payload
// too would let one document disagree with itself about the run it describes.
func buildReport(plan validationPlan, results []validate.EbuildResult) report.Run {
	// planned is the tally's denominator, named once. It is the number every count
	// below is taken against — the tally reconciles against the PLAN, never
	// against the rows, because a package that produced no row still had to be
	// counted somewhere.
	planned := len(plan.Entries)

	// How far down the plan the run actually got. A result past the end of the
	// plan has nothing to be classified against — policy, the depth request and
	// the reason all live on the entry — and the contract above says it cannot
	// happen; bounding here is what keeps it from being papered over with an
	// invented entry.
	reached := min(len(results), planned)

	out := report.AutoupdateCheck{
		Plan:    make([]report.PlanEntry, 0, planned),
		Results: make([]report.ValidationRow, 0, reached),
		// Carried across rather than recomputed, because it CANNOT be
		// recomputed: it means "the entries above the shallowest depth", and
		// the model has no ladder to compare a depth string against. Dropping
		// it here would delete the number from the JSON export, which is the
		// one place a machine reader could price a run from.
		DistfilesToFetch: plan.DistfilesToFetch,
	}

	for _, entry := range plan.Entries {
		out.Plan = append(out.Plan, planEntryFacts(entry))
	}

	for i := range reached {
		//nolint:gosec // G602: both indexes are bounded by construction. reached is
		// min(len(results), planned) and planned is len(plan.Entries), so i < reached
		// implies i is in range for BOTH slices. gosec cannot see through min().
		row := validationRowFacts(plan.Entries[i], results[i])
		out.Results = append(out.Results, row)
		countInExactlyOneColumn(&out.Tally, row.Outcome)
	}

	// A complete run answered for every planned package, so its tally
	// reconciles; an interrupted one names the gap instead of closing it.
	return checkEnvelope(out, reached == planned, planned-reached)
}

// scannedFacts is what the version check found, as the model spells it — the
// half of the report buildReport is never handed.
//
// It is a translation and nothing else: no package is filtered, no order is
// changed and no condition is decided here. PackageResult's four bools are
// mutually exclusive by property of the SCAN, so copying them across preserves
// that; collapsing them into one state here would make this a second place the
// run's conditions are decided (the renderer's `condition` already reads them).
//
// UpstreamVersion becomes CandidateVersion: the producer names the field by
// where the value came from, the model by what it IS — the version this run
// would move to, as in planEntryFacts.
//
// The error crosses as its text, in full: the model holds facts and has no
// behaviour, so it carries a string. It is never shortened or reworded, which
// is what keeps it actionable, and a nil error is the empty string, never
// "<nil>".
func scannedFacts(results []autoupdate.CheckResult) []report.PackageResult {
	facts := make([]report.PackageResult, 0, len(results))

	for _, result := range results {
		fact := report.PackageResult{
			Package:          result.Package,
			Type:             result.Type,
			CurrentVersion:   result.CurrentVersion,
			CandidateVersion: result.UpstreamVersion,
			HasUpdate:        result.HasUpdate,
			NotComparable:    result.NotComparable,
			Orphaned:         result.Orphaned,
			FromCache:        result.FromCache,
		}
		if result.Error != nil {
			fact.Error = result.Error.Error()
		}
		fact.Requirements = []report.Requirement{}
		for _, req := range result.Requirements {
			fact.Requirements = append(fact.Requirements, report.Requirement{Package: req.Package, Version: req.Version, State: req.State})
		}
		facts = append(facts, fact)
	}

	return facts
}

// checkReport is the ONE report a `--check` run produces: what the scan found,
// joined with whatever the validation contributed.
//
// Both entry paths of runCheck go through it — the batch scan with the half
// runPendingValidation handed back, the single package with nothingValidated's
// run — so "one report per run" is a property of this function rather than of
// two call sites kept in agreement. The envelope's identity is stamped by
// buildReport or nothingValidated; this function joins a half onto a run.
//
// This is the single place both halves are in hand: buildReport never sees the
// scan and runPendingValidation returns a nil Scanned. Filling Scanned on both
// sides would make the join a question of which copy wins.
//
// A run that planned nothing is COMPLETE: a false would draw the `Run
// Interrupted` block over a run that had nothing to do. The condition is read
// off the PLAN rather than off --llm, because an operator can decline the
// confirmation with `--llm` set. A short results slice keeps the incomplete
// count buildReport established. Plan, Results and Tally are left as the
// validation half had them; a producer must never prune sections for a screen.
func checkReport(log *slog.Logger, scanned []autoupdate.CheckResult, validated report.Run) report.Run {
	joined := validated

	facts := checkPayload(log, validated)
	facts.Scanned = scannedFacts(scanned)
	joined.Payload = facts

	if len(facts.Plan) == 0 {
		joined.Complete = true
		joined.NotEvaluated = 0
	}

	return joined
}

// planEntryFacts is one planned bump as the model spells it.
//
// The two ends of the bump are renamed on the way across — From/Version here
// become CurrentVersion/CandidateVersion there — because the model names a
// version by what it IS rather than by where the producer's struct kept it.
// Reason crosses untouched: shortening it is a rendering decision, and doing it
// at this boundary would make it a loss of data instead.
func planEntryFacts(entry validationPlanEntry) report.PlanEntry {
	return report.PlanEntry{
		Package:          entry.Package,
		CurrentVersion:   entry.From,
		CandidateVersion: entry.Version,
		Class:            entry.Class,
		Depth:            entry.Depth,
		Reason:           entry.Reason,
		Skipped:          entry.Skipped,
	}
}

// validationRowFacts is one planned package's verdict as the model spells it.
//
// # The package and the candidate come from the PLAN
//
// Both are on the result too, and under the positional contract the two agree.
// The entry is the authority anyway: it is the denominator the row is counted
// against, and a row naming a package the plan did not plan would be a tally
// nobody can reconcile with the list above it. The depth is the exception, and
// deliberately so — reportedDepth prefers what the runner says it REACHED,
// falling back to what was asked for, because an outcome has to name its own
// reach.
//
// # SameReasonAsPlan is decided here
//
// Here is where both strings are in hand. A renderer computing it would have to
// go looking for this row's plan entry and compare the text a second time — a
// second derivation of the same fact, which would disagree with the model the
// day either side changes.
func validationRowFacts(entry validationPlanEntry, result validate.EbuildResult) report.ValidationRow {
	// entry.Depth, not reportedDepth: the question is whether the depth the
	// POLICY selected was measured. reportedDepth prefers what the runner
	// REACHED, which is the right number to print and the wrong one to grade
	// against — a run that reached less than was asked for must not be graded
	// on the shallower rung it settled for.
	selectedDepth := entry.Depth
	if selectedDepth == "" {
		selectedDepth = result.Depth
	}
	outcome := report.Classify(entry.Skipped, decidingGateFacts(result.Gates, selectedDepth))
	reason := rowReason(outcome, result, entry)

	return report.ValidationRow{
		Package:          entry.Package,
		CandidateVersion: entry.Version,
		Outcome:          outcome,
		Depth:            reportedDepth(result, entry),
		Reason:           reason,
		SameReasonAsPlan: reason == entry.Reason,
	}
}

// decidingGateFacts reduces one result's gates to the facts Classify reads, and
// drops the QA gate on the way.
//
// The QA gate MUST NOT reach Classify — the precondition Classify's doc comment
// records and cannot enforce. Classify uses len(gates) as the denominator for
// Proved, mirroring validate.EbuildResult.WorstOutcome, which skips GateQA (as
// Report.ExitCode does). Left in, the QA gate would make the denominator one too
// large on every package pkgcheck spoke about; on a host where pkgcheck crashes
// on every package, every Proved would silently become Inconclusive. Dropping
// the gate, rather than marking it non-deciding, keeps the denominator right.
//
// Deciding means "participated in the verdict". A declined gate is SKIPPED: it
// arrives Deciding false and contributes no pass, which leaves a half-measured
// package Inconclusive instead of Proved.
//
// The cause is the typed value, never a sentence: GateResult.Declined crosses
// as its plain string value. The gate's Reason says the same in prose, and a
// rewording must never move a package between columns; skipReason reads those
// sentences only to produce the human line.
func decidingGateFacts(gates []validate.GateResult, selectedDepth string) []report.GateFact {
	facts := make([]report.GateFact, 0, len(gates))

	// The depth->gate mapping is resolved HERE and travels as a bool, because
	// internal/common/report must not import internal/autoupdate. This is
	// the same crossing Cause makes as a plain string.
	//
	// A depth that does not parse, or one no gate stands for, marks nothing:
	// selectedGate stays empty and no fact matches it, so the result falls
	// through to Inconclusive instead of guessing a gate whose pass would read
	// as proof of a rung nobody climbed.
	selectedGate := ""
	if depth, err := validate.ParseDepth(selectedDepth); err == nil {
		if gate, ok := validate.GateForDepth(depth); ok {
			selectedGate = gate
		}
	}

	for _, gate := range gates {
		if gate.Gate == validate.GateQA {
			continue
		}

		facts = append(facts, report.GateFact{
			Deciding:            gate.Outcome != validate.OutcomeSkipped,
			Passed:              gate.Outcome == validate.OutcomePass,
			Failed:              gate.Outcome == validate.OutcomeFailed,
			Cause:               string(gate.Declined),
			ProvesSelectedDepth: selectedGate != "" && gate.Gate == selectedGate,
		})
	}

	return facts
}

// rowReason is why this package earned this outcome, in full.
//
// For everything but a failure it is skipReason's cascade — the gate's own
// reason, else the depth's, else the plan's, else a sentence naming the silence
// — unchanged and still the single place that sentence is composed.
//
// A FAILURE is the one case that cascade cannot answer. A failing gate is not
// required to carry a Reason (the invariant only binds SKIPPED), so skipReason
// falls through to the plan's depth reason, SameReasonAsPlan comes out true,
// and a renderer that suppresses a repeated reason prints a failed package with
// no explanation at all. The findings ARE the explanation, so they become the
// reason. See failureDetail for what that costs.
func rowReason(outcome report.Outcome, result validate.EbuildResult, entry validationPlanEntry) string {
	if outcome == report.Errored {
		if detail := failureDetail(result); detail != "" {
			return detail
		}
	}
	return skipReason(result, entry)
}

// failureDetail is every finding a failed package produced, each named by the
// gate that produced it, joined into one line.
//
// It carries the same pairs reportValidationOutcome prints today — `gate:
// detail`, every gate, every finding — so the new path loses none of them.
//
// WHAT IT DOES COST is the structure: the model has no per-finding type, so a
// machine reader gets one string where it could have had a list. Joining is not
// shortening — no finding is dropped and no detail is truncated, which is what
// must never happen at this boundary — and one line is what the model's Reason
// field is, so a Markdown table cell and a terminal detail line both take it as
// they stand. Giving findings their own type on ValidationRow is the
// alternative, and it is a change to internal/common/report that only pays for
// itself once a renderer is written to print them per finding.
func failureDetail(result validate.EbuildResult) string {
	var details []string

	for _, gate := range result.Gates {
		for _, finding := range gate.Findings {
			details = append(details, fmt.Sprintf("%s: %s", gate.Gate, finding.Detail))
		}
	}

	return strings.Join(details, "; ")
}

// countInExactlyOneColumn adds one package to the tally.
//
// Exactly one counter is incremented per call, whatever it is handed. The
// default is Inconclusive rather than a silent no-op so that an outcome this
// switch has not been taught about still lands somewhere and still reconciles —
// "the toolkit did not establish which column this belongs in" is a limitation
// of the toolkit, which is what Inconclusive means. A no-op would drop
// the package out of the tally, and Reconciles would report the loss without
// anyone being able to see which package went missing.
func countInExactlyOneColumn(tally *report.Tally, outcome report.Outcome) {
	switch outcome {
	case report.Proved:
		tally.Proved++
	case report.Errored:
		tally.Errored++
	case report.Skipped:
		tally.Skipped++
	default:
		tally.Inconclusive++
	}
}
