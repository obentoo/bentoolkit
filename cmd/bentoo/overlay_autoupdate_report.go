package main

import (
	"fmt"
	"strings"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
	"github.com/obentoo/bentoolkit/internal/common/logger"
	"github.com/obentoo/bentoolkit/internal/common/report"
)

// checkEnvelope puts one check run's facts inside the envelope every exported
// document carries: the schema version, the kind of run that produced it, the
// reader's label for it, and how far down its plan the run got (R4.1, R1.4).
//
// # It is the ONE place this command builds a report.Run
//
// The two values it stamps unconditionally are fixed per COMMAND rather than
// per run. A kind that varied with what a run found would be a discriminator
// nobody could filter on, and a schema version typed at each producer is the
// same defect as a column width typed into a format string: several places to
// find on the day it changes, and nothing that fails when one is missed. One
// construction site makes both true by construction instead of by agreement
// between call sites.
//
// # Complete and NotEvaluated are ARGUMENTS, never read off the payload
//
// Deriving them — len(Results) against len(Plan) — is a different rule wearing
// the same answer: it reads any report whose result list is shorter than its
// plan as interrupted, including one a caller built by hand to describe a run
// that never validated. Completeness is established by the run that did the
// work, and it travels from there to here.
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
// # It is COMPLETE, and the empty plan is why
//
// Nothing was planned, so nothing was left unreached, which is precisely what
// Complete means (S045-R4.2). A false here would draw the `Run Interrupted`
// block over a run that finished, telling an operator their run was cut short
// when it was not.
//
// # The payload is the ZERO value, and the nil slices in it are the point
//
// A nil slice reaches the JSON export as null and an empty one as [], and the
// two say different things: the first is a producer that established nothing,
// the second one that established an empty list. This run validated nothing, so
// the absence is carried rather than replaced with an invented empty collection
// (S045-R4.1, S045-R4.3).
func nothingValidated() report.Run {
	// Nothing was planned, so nothing was left unreached: complete, with a gap
	// of zero.
	return checkEnvelope(report.AutoupdateCheck{}, true, 0)
}

// checkPayload is this command's own facts, read back out of the run that
// carries them.
//
// # The type assertion belongs HERE and nowhere below
//
// report.Run.Payload is an interface precisely so that nothing in
// internal/common/report, and nothing in its renderers, has to name a concrete
// payload — a type switch over payloads is the edit-per-kind that interface
// exists to prevent (R7.4). cmd/bentoo is the other side of that seam: it is
// where this command's payload is BUILT, so it is the one place that already
// knows which concrete type is in there.
//
// # A payload of another type answers the zero value, and SAYS SO
//
// checkEnvelope is the only writer of that field today, so the assertion
// cannot fail yet — but checkReport takes a report.Run of ANY kind, and tasks
// 5, 6 and 8 of this story add three more of them. A manifest or snapshot run
// reaching here would merge into a zero payload, and the operator would then
// be told "No packages configured for autoupdate" about a run that scanned
// plenty: wrong output, and nothing in the code that would have had to be
// edited for it to happen.
//
// So it is reported rather than discarded. Debug, not Warn, because it is
// unreachable in a real run and a line an operator cannot act on is noise; the
// kind that arrived is what makes it actionable when it is not.
//
// The zero value is still returned. It reads as "nothing scanned, nothing
// planned", which routes presentCheckReport to its silent arm — and a report is
// the thing an operator gets INSTEAD of a crash (R1.4), so reading one must not
// be the moment the crash arrives. report.Run.Sections tolerates a nil payload
// for the same reason.
func checkPayload(run report.Run) report.AutoupdateCheck {
	payload, ok := run.Payload.(report.AutoupdateCheck)
	if !ok {
		logger.Debug("check: the run carries kind %q with a %T payload, want %q and report.AutoupdateCheck — reporting it as empty",
			run.Kind, run.Payload, report.KindAutoupdateCheck)
	}
	return payload
}

// buildReport turns one run's plan and the results its gates produced into the
// run it reports, whole, before anything is printed (R1, R1.3, R1.4).
//
// This is the seam between the producer and the view: internal/common/report
// must not import internal/autoupdate, so the conversion from validate's types
// into the model's primitive facts happens here and nowhere else.
//
// # results is positionally aligned to plan.Entries
//
// results[i] is the answer for plan.Entries[i]. That is the caller's contract
// and it is what makes the tally reconcilable — each planned package is
// classified against ITS OWN plan entry, so `Skipped` and `Reason` are read
// from the entry the row is counted against rather than from a lookup that
// could miss. A short results slice is a run that stopped part way, and it is
// reported as such (Complete, NotEvaluated) rather than silently reconciling.
//
// # Scanned is left to the caller
//
// This function is handed the plan and its results; it never sees the scan. A
// nil Scanned therefore says "the producer did not fill this in", which the
// JSON export deliberately carries through as null rather than rewriting into
// an empty list. Whoever holds the scan results assigns them.
//
// # The whole run comes back, envelope included
//
// "How far down the plan the run got" is established right here, and this is
// the one place it is established — so this is where it is stated, on the
// envelope that carries it rather than on the payload, which would carry it a
// second time and let one document disagree with itself about the run it
// describes (D1). Handing back the payload alone would leave those two facts to
// travel beside it as a second value, and every function between here and the
// render would then thread a pair that nothing could look up from the report.
func buildReport(plan validationPlan, results []validate.EbuildResult) report.Run {
	// planned is R5.5's denominator, named once. It is the number every count
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
// half of the report buildReport is never handed (see its "Scanned is left to
// the caller" note).
//
// # It is a translation and nothing else
//
// No package is filtered, no order is changed and no condition is decided here.
// PackageResult carries four independent bools whose mutual exclusivity is a
// property of the SCAN — one condition established per package — so copying
// them across preserves that property, while collapsing them into a single
// state here would make this a second place the run's conditions are decided.
// The renderer already has that logic (render's own `condition`), reading the
// same four bools.
//
// # UpstreamVersion becomes CandidateVersion
//
// The producer names the field by where the value came from; the model names it
// by what it IS — the version this run would move to. The rename happens at
// this boundary for the same reason From/Version become
// CurrentVersion/CandidateVersion in planEntryFacts.
//
// # The error crosses as its text, in full
//
// The model holds facts and has no behaviour, so it carries a string rather
// than an error. It is never shortened or reworded: the sentence is exactly
// what the scan reported, which is what makes it actionable — and a nil error
// is the empty string, never "<nil>".
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
		facts = append(facts, fact)
	}

	return facts
}

// checkReport is the ONE report a `--check` run produces: what the scan found,
// joined with whatever the validation contributed (S045-R1.1, S045-R1.2, D1).
//
// Both entry paths of runCheck go through it — the batch scan with the half
// runPendingValidation handed back, the single package with nothingValidated's
// run, because that path validates nothing — so "one report per run" is a
// property of this function rather than of two call sites kept in agreement.
//
// Every caller hands it a run BUILT here in the adapter, by buildReport or by
// nothingValidated, which is what leaves the envelope's identity — the schema,
// the kind, the title — stamped in exactly one place. This function joins a
// half onto a run; it does not name one.
//
// # The two halves meet here and nowhere else
//
// buildReport is never handed the scan (see its "Scanned is left to the caller"
// note) and runPendingValidation deliberately returns a nil Scanned, so this is
// the single place both are in hand. Filling it on both sides would turn the
// join into a question of which copy wins; leaving it nil would export
// `"scanned": null` and draw an empty version-check section.
//
// # A run that planned nothing is COMPLETE
//
// An empty plan left nothing unevaluated, which is precisely what Complete
// means (S045-R4.2), and a false there would draw the `Run Interrupted` block
// over a run that finished — telling the operator that a run which had nothing
// to do was cut short.
//
// The condition is read off the PLAN rather than off autoupdateLLM: an operator
// can decline the confirmation with `--llm` set, and that run validated nothing
// either. It agrees with the producer instead of overriding it — both
// buildReport and nothingValidated already answer complete for an empty plan
// (reached == len(plan.Entries) is 0 == 0) — so on every path this command
// takes today the branch below changes nothing. What it still covers is a run
// assembled by hand, which is the only way one can arrive here saying it was
// interrupted over a plan it never had.
//
// # The two envelope facts pass through, and are corrected in exactly one case
//
// A short results slice is the case that matters: the plan is non-empty, the
// branch below does not fire, and the run stays incomplete with the count
// buildReport established. Only a run with NO plan is overridden, and it is
// overridden to the one answer that is true of it — nothing was planned, so
// nothing was left unreached.
//
// # It never invents a section
//
// Plan, Results and Tally are left exactly as the validation half had them, so
// a scan-only report CARRIES no plan entry, no result row and a zero tally
// (S045-R4.1) — and the JSON export carries that absence rather than an
// invented empty collection (S045-R4.3).
//
// What a renderer then does with an empty half is the renderer's decision and
// not this function's: today each of those three sections states its own
// emptiness in a single line ("No pending update to validate.") rather than
// omitting its heading. Nothing here should ever start deciding that — a
// producer that pruned sections to suit one screen would be the second place
// the run's contents are decided.
func checkReport(scanned []autoupdate.CheckResult, validated report.Run) report.Run {
	joined := validated

	facts := checkPayload(validated)
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
// at this boundary would make it a loss of data instead (R7.4).
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
// go looking for this row's plan entry and compare the text a second time,
// which is the second derivation R1.3 forbids and which would disagree with the
// model the day either side changes.
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
// drops the QA gate on the way (R5.4).
//
// # The QA gate MUST NOT reach Classify
//
// This is the precondition Classify's doc comment records and cannot enforce.
// Classify uses len(gates) as the denominator for Proved, mirroring
// validate.EbuildResult.WorstOutcome — and WorstOutcome skips GateQA before it
// counts anything (D8, the same exclusion Report.ExitCode makes). Leaving the
// QA gate in the slice would make the denominator one too large on every
// package pkgcheck spoke about. On a host where pkgcheck does not work — this
// one, where it crashes on every package of the overlay for a pre-existing
// reason nobody can disable — that is EVERY package, and every Proved would
// silently become Inconclusive across the whole overlay, breaking R5.7 without
// a single error message. Dropping the gate, rather than marking it
// non-deciding, is what keeps the denominator right.
//
// # Deciding means "participated in the verdict"
//
// A gate that declined is SKIPPED: it was planned, it was asked, and it said
// nothing. It arrives Deciding false and contributes no pass, which is what
// leaves a half-measured package Inconclusive instead of Proved. Marking a
// declined gate as deciding would make Proved unreachable, since Proved needs
// every gate in the slice to have passed.
//
// # The cause is the typed value, never a sentence (R5.4)
//
// GateResult.Declined is a validate.DeclineCause, and it crosses as its plain
// string value. The gate's Reason says the same thing in prose, and prose gets
// reworded; a rewording that moved a package from one column to another is
// exactly what R5.4 forbids. skipReason still reads those sentences, but only
// to produce the human line — never to decide a column.
func decidingGateFacts(gates []validate.GateResult, selectedDepth string) []report.GateFact {
	facts := make([]report.GateFact, 0, len(gates))

	// The depth->gate mapping is resolved HERE and travels as a bool, because
	// internal/common/report must not import internal/autoupdate (D2). This is
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
// R7.4 actually forbids — and one line is what the model's Reason field is, so
// a Markdown table cell and a terminal detail line both take it as they stand.
// Giving findings their own type on ValidationRow is the alternative, and it is
// a change to internal/common/report that only pays for itself once a renderer
// is written to print them per finding.
func failureDetail(result validate.EbuildResult) string {
	var details []string

	for _, gate := range result.Gates {
		for _, finding := range gate.Findings {
			details = append(details, fmt.Sprintf("%s: %s", gate.Gate, finding.Detail))
		}
	}

	return strings.Join(details, "; ")
}

// countInExactlyOneColumn adds one package to the tally (R5.5).
//
// Exactly one counter is incremented per call, whatever it is handed. The
// default is Inconclusive rather than a silent no-op so that an outcome this
// switch has not been taught about still lands somewhere and still reconciles —
// "the toolkit did not establish which column this belongs in" is a limitation
// of the toolkit, which is what Inconclusive means (R5.6). A no-op would drop
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
