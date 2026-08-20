package validate

// Story 039, sub-task 2.2 — R2, R2.4.
//
// StageRecord.Proves is a FOURTH reader of "may this candidate be promoted", and
// it re-implements the rule PromotionDecision holds: its own final sentence says
// "every gate reported PASS or SKIPPED", which is exactly the vacuity story 039
// exists to deny. The reuse path it gates consults neither refuseUnproved nor
// PromotionDecision, so a record that measured nothing promotes a bump one run
// later instead of not at all.
//
// The leak is reachable today: `overlay autoupdate --check` installs its record
// writer in a defer BEFORE the manifest step, so a manifest failure records the
// requested depth plus a list of deciding gates that all reported SKIPPED — and
// Proves accepts it.

import (
	"strings"
	"testing"
)

// TestStageRecord_ProvesRefusesARecordThatMeasuredNothing is the rule
// PromotionDecision gained, applied at the reader that bypasses it.
func TestStageRecord_ProvesRefusesARecordThatMeasuredNothing(t *testing.T) {
	record := StageRecord{
		Package: "media-plugins/gst-plugins-qt6",
		Version: "1.29.2",
		Depth:   DepthCompile,
		Gates:   SkippedGates(DepthCompile, "the Manifest could not be generated for the staged tree"),
	}
	if len(record.Gates) == 0 {
		t.Fatal("the fixture recorded no gate, so this would pass through the deciding==0 branch instead")
	}

	proved, why := record.Proves(DepthCompile)

	if proved {
		t.Fatalf("a record whose every deciding gate reported SKIPPED was accepted as proof (%q); the reuse "+
			"path consults neither refuseUnproved nor PromotionDecision, so this republishes an ebuild "+
			"nothing ever read", why)
	}
	if !strings.Contains(why, "SKIPPED") {
		t.Errorf("the refusal %q does not say what was wrong with the record", why)
	}
}

// TestStageRecord_ProvesStillAcceptsARealMeasurement is the other side: a record
// holding one real PASS is a proof, and hardening the reader must not turn
// every retained tree into a re-validation. That would undo R10.1 entirely —
// the reuse path exists because proving the same tree twice is the expensive
// half of an apply.
func TestStageRecord_ProvesStillAcceptsARealMeasurement(t *testing.T) {
	record := StageRecord{
		Depth: DepthCompile,
		Gates: append([]GateResult{{Gate: GateOptions, Outcome: OutcomePass}},
			SkippedGates(DepthCompile, "this host does not hold the build dependencies")...),
	}

	if proved, why := record.Proves(DepthConfigure); !proved {
		t.Errorf("a record holding a real PASS was rejected (%q); a skip beside a measurement is the case "+
			"R3.3 exists to allow, and refusing it revalidates every retained tree forever", why)
	}
}
