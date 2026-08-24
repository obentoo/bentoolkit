package main

import (
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
	"github.com/obentoo/bentoolkit/internal/common/report"
)

// gate builds a deciding gate with a given outcome.
func gate(name string, outcome validate.Outcome) validate.GateResult {
	return validate.GateResult{Gate: name, Outcome: outcome}
}

// declined builds a gate that SKIPPED and named why.
func declined(name string, cause validate.DeclineCause) validate.GateResult {
	return validate.GateResult{Gate: name, Outcome: validate.OutcomeSkipped, Declined: cause}
}

// planOf builds a plan from entries, so a test states only what it varies.
func planOf(entries ...validationPlanEntry) validationPlan {
	return validationPlan{Entries: entries}
}

func entry(pkg, depth string, skipped bool) validationPlanEntry {
	return validationPlanEntry{
		Package: pkg,
		From:    "1.0",
		Version: "1.1",
		Class:   "patch",
		Depth:   depth,
		Reason:  "reason for " + pkg,
		Skipped: skipped,
	}
}

func resultOf(pkg string, gates ...validate.GateResult) validate.EbuildResult {
	return validate.EbuildResult{Package: pkg, Version: "1.1", Depth: "compile", Gates: gates}
}

// TestAdapterCounts pins the four columns over causes the adapter can actually
// meet, one per column, and it is the first place the story's central
// distinction is measured end to end: a package the toolkit could not evaluate
// is counted apart from one the operator excluded.
func TestAdapterCounts(t *testing.T) {
	plan := planOf(
		entry("app-misc/proved", "compile", false),
		entry("app-misc/errored", "compile", false),
		entry("app-misc/inconclusive", "compile", false),
		entry("app-misc/policy", "none", true),
	)
	results := []validate.EbuildResult{
		resultOf("app-misc/proved", gate("options", validate.OutcomePass), gate("build", validate.OutcomePass)),
		resultOf("app-misc/errored", gate("options", validate.OutcomePass), gate("build", validate.OutcomeFailed)),
		resultOf("app-misc/inconclusive", declined("build", validate.DeclineHost)),
		resultOf("app-misc/policy"),
	}

	got := buildReport(plan, results).Tally
	want := report.Tally{Proved: 1, Errored: 1, Inconclusive: 1, Skipped: 1}

	if got != want {
		t.Fatalf("tally = %+v, want %+v", got, want)
	}
}

// TestAdapterReconciles pins R5.5 on the adapter rather than on the model. The
// model can only check that the counts sum; only the adapter can get a package
// into two columns or none.
func TestAdapterReconciles(t *testing.T) {
	cases := map[string]struct {
		plan    validationPlan
		results []validate.EbuildResult
	}{
		"one per column": {
			plan: planOf(
				entry("a/proved", "compile", false),
				entry("a/errored", "compile", false),
				entry("a/inconclusive", "compile", false),
				entry("a/policy", "none", true),
			),
			results: []validate.EbuildResult{
				resultOf("a/proved", gate("build", validate.OutcomePass)),
				resultOf("a/errored", gate("build", validate.OutcomeFailed)),
				resultOf("a/inconclusive", declined("build", validate.DeclineCandidate)),
				resultOf("a/policy"),
			},
		},
		"every package declined with no cause recorded": {
			plan: planOf(entry("b/one", "compile", false), entry("b/two", "compile", false)),
			results: []validate.EbuildResult{
				resultOf("b/one", declined("build", validate.DeclineUnrecorded)),
				resultOf("b/two", declined("build", validate.DeclineUnrecorded)),
			},
		},
		"every package excluded by policy": {
			plan:    planOf(entry("c/one", "none", true), entry("c/two", "none", true)),
			results: []validate.EbuildResult{resultOf("c/one"), resultOf("c/two")},
		},
		"nothing planned": {
			plan: planOf(), results: nil,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := buildReport(tc.plan, tc.results)

			if !r.Reconciles() {
				t.Errorf("the tally does not reconcile: %+v sums to %d over a plan of %d (R5.5)",
					r.Tally, r.Tally.Total(), len(r.Plan))
			}
			if len(r.Plan) != len(tc.plan.Entries) {
				t.Errorf("the report holds %d plan entries, the plan had %d", len(r.Plan), len(tc.plan.Entries))
			}
		})
	}
}

// currentImplementationTally is the switch runValidationCheck uses today,
// transcribed rather than re-derived. R5.7 says the new classification must not
// change WHICH packages are proved or errored, so the comparison has to be
// against the real thing — a re-implementation would only prove the transcript
// agrees with itself.
func currentImplementationTally(results []validate.EbuildResult) (proved, errored int) {
	for _, result := range results {
		switch result.WorstOutcome() {
		case validate.OutcomePass:
			proved++
		case validate.OutcomeFailed:
			errored++
		}
	}
	return proved, errored
}

// TestProvedAndErroredUnchanged is R5.7, and it is the requirement that decides
// whether this story is safe to ship. Only the third column is divided; the
// first two must be exactly what they are today, over every shape that reaches
// them.
func TestProvedAndErroredUnchanged(t *testing.T) {
	fixtures := map[string][]validate.GateResult{
		"every deciding gate passed":        {gate("options", validate.OutcomePass), gate("build", validate.OutcomePass)},
		"one deciding gate failed":          {gate("options", validate.OutcomePass), gate("build", validate.OutcomeFailed)},
		"every deciding gate failed":        {gate("build", validate.OutcomeFailed)},
		"declined on the host":              {declined("build", validate.DeclineHost)},
		"declined on the candidate":         {declined("build", validate.DeclineCandidate)},
		"declined with no cause":            {declined("build", validate.DeclineUnrecorded)},
		"no gates at all":                   nil,
		"one passed and one declined":       {gate("options", validate.OutcomePass), declined("build", validate.DeclineHost)},
		"one failed and one declined":       {gate("build", validate.OutcomeFailed), declined("options", validate.DeclineHost)},
		"only the QA gate, and it passed":   {gate(validate.GateQA, validate.OutcomePass)},
		"only the QA gate, and it declined": {declined(validate.GateQA, validate.DeclineHost)},
	}

	for name, gates := range fixtures {
		t.Run(name, func(t *testing.T) {
			plan := planOf(entry("x/pkg", "compile", false))
			results := []validate.EbuildResult{resultOf("x/pkg", gates...)}

			wantProved, wantErrored := currentImplementationTally(results)
			got := buildReport(plan, results).Tally

			if got.Proved != wantProved {
				t.Errorf("proved = %d, the current implementation says %d (R5.7)", got.Proved, wantProved)
			}
			if got.Errored != wantErrored {
				t.Errorf("errored = %d, the current implementation says %d (R5.7)", got.Errored, wantErrored)
			}
		})
	}
}

// TestQAGateDoesNotReachTheClassifier is the precondition sub-task 1.3 recorded
// in Classify's doc comment, asserted where it can actually be violated.
//
// Classify uses len(gates) as the denominator for Proved, mirroring
// WorstOutcome — but WorstOutcome EXCLUDES the QA gate before counting. If the
// adapter passes it through, then on a host whose pkgcheck does not work the QA
// gate declines for EVERY package, the denominator never matches, and every
// Proved silently becomes Inconclusive across the whole overlay.
//
// That host is not hypothetical: pkgcheck crashes on every package of this
// overlay, on a pre-existing crash that cannot be disabled. This is the shape
// that would ship a silent, total regression of R5.7 while every other test
// stayed green.
func TestQAGateDoesNotReachTheClassifier(t *testing.T) {
	plan := planOf(entry("x/pkg", "compile", false))
	results := []validate.EbuildResult{
		resultOf("x/pkg",
			gate("options", validate.OutcomePass),
			gate("build", validate.OutcomePass),
			// pkgcheck is broken on this host, as it is on the real one.
			declined(validate.GateQA, validate.DeclineHost),
		),
	}

	got := buildReport(plan, results).Tally

	if got.Proved != 1 {
		t.Fatalf("proved = %d, want 1: a declining QA gate demoted a package the deciding gates proved. "+
			"The adapter must drop the QA gate before calling Classify, exactly as WorstOutcome does (R5.7)", got.Proved)
	}
	if got.Inconclusive != 0 {
		t.Errorf("inconclusive = %d, want 0", got.Inconclusive)
	}
}

// TestAdapterSetsSameReasonAsPlan pins the flag the renderers read for R7.2. It
// is computed here, where both strings are in hand — a renderer that went
// looking for the matching plan entry would be recomputing what the model
// already carries (R1.3).
func TestAdapterSetsSameReasonAsPlan(t *testing.T) {
	const shared = "depth resolved to none because policy said so"

	plan := planOf(
		validationPlanEntry{Package: "a/same", From: "1.0", Version: "1.1", Depth: "none", Reason: shared, Skipped: true},
		validationPlanEntry{Package: "a/differs", From: "1.0", Version: "1.1", Depth: "manifest", Reason: "patch bump earns manifest"},
	)
	results := []validate.EbuildResult{
		{Package: "a/same", Version: "1.1", DepthReason: shared},
		{Package: "a/differs", Version: "1.1", Gates: []validate.GateResult{
			{Gate: "manifest", Outcome: validate.OutcomeSkipped, Reason: "no Manifest could be produced", Declined: validate.DeclineCandidate},
		}},
	}

	rows := buildReport(plan, results).Results
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}

	for _, row := range rows {
		switch row.Package {
		case "a/same":
			if !row.SameReasonAsPlan {
				t.Errorf("a/same repeats the plan's reason word for word but SameReasonAsPlan is false (R7.2)\n  plan: %q\n  row:  %q", shared, row.Reason)
			}
		case "a/differs":
			if row.SameReasonAsPlan {
				t.Errorf("a/differs carries new information but was marked as repeating the plan (R7.3)\n  row: %q", row.Reason)
			}
		}
	}
}

// TestAdapterKeepsReasonsWhole pins R7.4 at the boundary where a reason could
// first be lost. Shortening is a rendering decision; the adapter is not a
// renderer.
func TestAdapterKeepsReasonsWhole(t *testing.T) {
	long := ""
	for range 12 {
		long += "this reason is long enough that a renderer would shorten it. "
	}

	plan := planOf(validationPlanEntry{Package: "a/pkg", From: "1.0", Version: "1.1", Depth: "none", Reason: long, Skipped: true})
	results := []validate.EbuildResult{{Package: "a/pkg", Version: "1.1", DepthReason: long}}

	r := buildReport(plan, results)
	if r.Plan[0].Reason != long {
		t.Errorf("the plan's reason was shortened by the adapter: %d chars, want %d (R7.4)", len(r.Plan[0].Reason), len(long))
	}
}

// TestPolicyAndLimitationDoNotSwap is the story's central distinction asserted
// per package, and it exists because the rest of this file could not catch its
// violation.
//
// Measured, not supposed: replacing `entry.Skipped` with a text match on
// skipReason's sentence — precisely the R5.4 violation — passed every other
// test here. Two reasons it slipped through. TestAdapterReconciles asserts only
// that the counts SUM, never which column a package lands in; and
// TestAdapterCounts agreed by accident, because its policy fixture is named
// "app-misc/policy", so the sentence "reason for app-misc/policy" contains the
// word the text match looks for and the wrong rule reached the right answer.
//
// So the atoms and reasons below are chosen to carry NO policy-ish word, and
// the assertion is on the outcome of each package rather than on the totals.
// The typed cause is the only thing left that can decide correctly.
func TestPolicyAndLimitationDoNotSwap(t *testing.T) {
	plan := planOf(
		validationPlanEntry{
			Package: "app-misc/alpha", From: "1.0", Version: "1.1", Depth: "none",
			Reason:  "the operator lowered this bump below what its class earns",
			Skipped: true,
		},
		validationPlanEntry{
			Package: "app-misc/beta", From: "2.0", Version: "2.1", Depth: "compile",
			Reason: "a minor bump earns compile",
		},
	)
	results := []validate.EbuildResult{
		// Excluded before any gate was planned: the operator's instruction.
		{Package: "app-misc/alpha", Version: "1.1", DepthReason: "the operator lowered this bump below what its class earns"},
		// A gate was planned and the toolkit could not run it: our limitation.
		resultOf("app-misc/beta", declined("build", validate.DeclineHost)),
	}

	rows := buildReport(plan, results).Results
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}

	want := map[string]report.Outcome{
		"app-misc/alpha": report.Skipped,      // policy said no (R5.3)
		"app-misc/beta":  report.Inconclusive, // the toolkit could not answer (R5.2)
	}
	for _, row := range rows {
		if got := row.Outcome; got != want[row.Package] {
			t.Errorf("%s = %q, want %q: a defect in the toolkit must not be able to hide behind the operator's own policy, nor the reverse",
				row.Package, got, want[row.Package])
		}
	}
}
