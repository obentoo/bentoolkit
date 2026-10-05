package main

// Authored for story 046, sub-task 3.2 — R1.3, R4.1.
//
// # It needs no harness, and here is the reading behind that
//
// 3.2's Tests field says Integration, and its objective is "The check path
// produces a report.Run naming its kind". But the seam it names —
// cmd/bentoo/overlay_autoupdate_report.go — is two plain functions over plain
// values: buildReport(plan, results) turns a plan and its gate results into the
// payload, and checkReport(scanned, validated) joins the scan half onto it.
// Neither reads a flag, opens a file, resolves a config or touches the network,
// and the file's existing tests already call them directly with literal plans
// and results.
//
// Running a command to reach them would add a config, an overlay, an httptest
// server and a process-wide flag state to a test whose subject is a struct
// literal in and a struct literal out — and every one of those is a way for
// this test to fail for a reason that is not R4.1. So it calls the adapter.
//
// What genuinely needs the harness is what the adapter CANNOT answer: whether
// the flags reach the command, whether the export lands, whether an interrupted
// run still renders. Those are 4.3, 4.5 and 5.3, and they use it.
//
// # The one composition it pins
//
//	checkReport(scanned, buildReport(plan, results))
//
// written as the production path composes it, so that only the RESULT type is
// pinned. Whatever buildReport comes to return, the two still fit together, and
// what this file asserts is that what comes out the far end is a report.Run —
// the envelope, naming its kind.
//
// Red on arrival: checkReport returns report.Report, which has no Kind, no
// Schema and no Payload.
//
// gate, planOf, entry and resultOf come from overlay_autoupdate_report_test.go,
// this package's own; this file is authored beside it rather than over it,
// because that file holds the four-column tally tests story 044 wrote and this
// story must not move.

import (
	"encoding/json"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
	"github.com/obentoo/bentoolkit/internal/common/report"
)

// envelopeScan is the version-check half: one package behind upstream, one
// already current.
func envelopeScan() []autoupdate.CheckResult {
	return []autoupdate.CheckResult{
		{Package: "app-misc/jq", CurrentVersion: "1.7.1", UpstreamVersion: "1.8.0", HasUpdate: true, Type: "source"},
		{Package: "dev-lang/go", CurrentVersion: "1.26.0", UpstreamVersion: "1.26.0", Type: "source"},
	}
}

// envelopePlan is two planned packages, so a run that reached only the first
// has a gap of exactly one.
func envelopePlan() validationPlan {
	return planOf(
		entry("app-misc/jq", "compile", false),
		entry("dev-lang/go", "compile", false),
	)
}

// checkRunOf composes the adapter the way the check path composes it.
func checkRunOf(plan validationPlan, results []validate.EbuildResult) report.Run {
	return checkReport(discardLog(), envelopeScan(), buildReport(plan, results))
}

// TestBuildReportEnvelopeNamesTheCheckKind pins R4.1 at the producer. The
// document a consumer reads gets its kind from here; nothing downstream can
// invent one, and nothing downstream should have to.
func TestBuildReportEnvelopeNamesTheCheckKind(t *testing.T) {
	run := checkRunOf(envelopePlan(), []validate.EbuildResult{
		resultOf("app-misc/jq", gate("build", validate.OutcomePass)),
		resultOf("dev-lang/go", gate("build", validate.OutcomePass)),
	})

	if run.Kind != report.KindAutoupdateCheck {
		t.Errorf("Kind = %q, want %q", run.Kind, report.KindAutoupdateCheck)
	}
	if run.Schema != report.SchemaVersion {
		t.Errorf("Schema = %d, want %d", run.Schema, report.SchemaVersion)
	}

	payload, ok := run.Payload.(report.AutoupdateCheck)
	if !ok {
		t.Fatalf("Payload is %T, want report.AutoupdateCheck", run.Payload)
	}
	if len(payload.Scanned) != 2 {
		t.Errorf("the payload carries %d scanned packages, want 2 — the scan half did not survive the join", len(payload.Scanned))
	}
	if !payload.Reconciles() {
		t.Errorf("a complete run does not reconcile: tally %+v over a plan of %d (R5.5)", payload.Tally, len(payload.Plan))
	}
}

// TestBuildReportEnvelopeStatesAShortResultSlice pins R1.3 and R1.4 where the
// gap is computed. A run interrupted after its first package produced one row
// for a plan of two, and the difference is not an error to be papered over with
// an invented entry — it is the number that turns a short list into a stated
// gap.
func TestBuildReportEnvelopeStatesAShortResultSlice(t *testing.T) {
	cases := map[string]struct {
		results          []validate.EbuildResult
		wantComplete     bool
		wantNotEvaluated int
	}{
		"every planned package evaluated": {
			results: []validate.EbuildResult{
				resultOf("app-misc/jq", gate("build", validate.OutcomePass)),
				resultOf("dev-lang/go", gate("build", validate.OutcomePass)),
			},
			wantComplete:     true,
			wantNotEvaluated: 0,
		},
		"interrupted after the first": {
			results:          []validate.EbuildResult{resultOf("app-misc/jq", gate("build", validate.OutcomePass))},
			wantComplete:     false,
			wantNotEvaluated: 1,
		},
		"interrupted before any": {
			results:          nil,
			wantComplete:     false,
			wantNotEvaluated: 2,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			run := checkRunOf(envelopePlan(), tc.results)

			if run.Complete != tc.wantComplete {
				t.Errorf("Complete = %v, want %v", run.Complete, tc.wantComplete)
			}
			if run.NotEvaluated != tc.wantNotEvaluated {
				t.Errorf("NotEvaluated = %d, want %d — the packages the run never reached are counted in no column, and this is what says so (R1.4)",
					run.NotEvaluated, tc.wantNotEvaluated)
			}
		})
	}
}

// TestBuildReportEnvelopeMarshalsWithThePayloadNested is R4.1 seen from the
// consumer's side, and it is the assertion that catches an envelope built
// correctly in memory and flattened on the way out.
func TestBuildReportEnvelopeMarshalsWithThePayloadNested(t *testing.T) {
	run := checkRunOf(envelopePlan(), []validate.EbuildResult{
		resultOf("app-misc/jq", gate("build", validate.OutcomePass)),
		resultOf("dev-lang/go", gate("build", validate.OutcomePass)),
	})

	data, err := json.Marshal(run)
	if err != nil {
		t.Fatalf("marshalling the run: %v", err)
	}

	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("the envelope did not produce a JSON object: %v\n%s", err, data)
	}

	if got, _ := doc["kind"].(string); got != string(report.KindAutoupdateCheck) {
		t.Errorf(`root["kind"] = %q, want %q`, got, report.KindAutoupdateCheck)
	}
	payload, ok := doc["payload"].(map[string]any)
	if !ok {
		t.Fatalf(`root["payload"] = %#v, want the check's facts nested under it (D3)`, doc["payload"])
	}
	if _, present := payload["tally"]; !present {
		t.Errorf(`the payload has no "tally" — a consumer's .payload.tally.proved has nothing to read`)
	}
	if _, leaked := doc["tally"]; leaked {
		t.Error(`"tally" is at the ROOT — the payload was flattened into the envelope, which is schema 1 with two extra keys (D3)`)
	}
}

// TestScannedFactsCrossUnchanged pins the half of R4.2 the rename could quietly
// break: every fact the scan established reaches the model, under the model's
// own names, with nothing decided on the way across.
func TestScannedFactsCrossUnchanged(t *testing.T) {
	facts := scannedFacts(envelopeScan())

	if len(facts) != 2 {
		t.Fatalf("scannedFacts returned %d rows over 2 results", len(facts))
	}
	if facts[0].Package != "app-misc/jq" || facts[0].CandidateVersion != "1.8.0" || !facts[0].HasUpdate {
		t.Errorf("the behind package crossed as %+v", facts[0])
	}
	if facts[1].HasUpdate {
		t.Errorf("the up-to-date package crossed as having an update: %+v", facts[1])
	}
	if facts[0].Type != "source" {
		t.Errorf("Type crossed as %q, want source", facts[0].Type)
	}
}
