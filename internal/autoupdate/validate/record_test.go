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
	"encoding/json"
	"os"
	"path/filepath"
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

// ===== Story 041, task 2.1 — R5.2 =====

// stageRecordTreeFor gives WriteStageRecord the directory it insists on: it
// writes a record ABOUT a tree, and refuses to create one where the tree never
// was.
func stageRecordTreeFor(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "media-plugins", "gst-plugins-qt6", "1.29.2")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("laying out the staged tree %s: %v", dir, err)
	}
	return dir
}

// TestStageRecord_TheProducerSurvivesTheRoundTripThroughDisk is R5.2's premise:
// there is no point refusing a reuse on provenance the file does not carry.
//
// The wire key is asserted on the RAW bytes rather than only through
// ReadStageRecord, because a round-trip through one process proves nothing about
// what the next version of the program will read. StageRecord already spells its
// depth as a name for that exact reason (record.go, stageRecordWire).
func TestStageRecord_TheProducerSurvivesTheRoundTripThroughDisk(t *testing.T) {
	dir := stageRecordTreeFor(t)

	written := StageRecord{
		Package:    "media-plugins/gst-plugins-qt6",
		Version:    "1.29.2",
		Depth:      DepthConfigure,
		Gates:      []GateResult{{Gate: GateConfigure, Outcome: OutcomePass}},
		ProducedBy: "validate",
	}
	if err := WriteStageRecord(dir, written); err != nil {
		t.Fatalf("WriteStageRecord: %v", err)
	}

	body, err := os.ReadFile(StageRecordPath(dir))
	if err != nil {
		t.Fatalf("reading the record back: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatalf("the record on disk is not readable JSON: %v", err)
	}
	if got := wire["produced_by"]; got != "validate" {
		t.Errorf("the record on disk carries produced_by=%v, want \"validate\"; a provenance that never reaches "+
			"the file cannot be read by the run that has to refuse on it (R5.2, D6)", got)
	}

	read, err := ReadStageRecord(dir)
	if err != nil {
		t.Fatalf("ReadStageRecord: %v", err)
	}
	if read.ProducedBy != "validate" {
		t.Errorf("ReadStageRecord returned ProducedBy=%q, want \"validate\"", read.ProducedBy)
	}
}

// TestStageRecord_ARecordWithNoProducerReadsAsApplierProduced is R5.2 itself,
// and it is a compatibility guard rather than a feature.
//
// Every record already on an operator's disk was written before this field
// existed. If absence read as "not the applier", the provenance check D6 adds to
// the reuse path would refuse EVERY retained tree staged by a release before
// this one — R10.1's whole saving, silently switched off, with each bump paying
// for its gates a second time and nothing in the output saying why.
//
// The fixture is hand-written JSON, not a StageRecord with the field left empty:
// the question is what the BYTES on disk mean, and a struct with a zero field
// would still be this version of the program talking to itself.
func TestStageRecord_ARecordWithNoProducerReadsAsApplierProduced(t *testing.T) {
	dir := stageRecordTreeFor(t)

	legacy := `{
  "package": "media-plugins/gst-plugins-qt6",
  "version": "1.29.2",
  "depth": "configure",
  "gates": [{"gate": "configure", "outcome": "PASS"}],
  "ebuild_digest": "abc123"
}
`
	// Not vacuous: if this fixture ever grows a produced_by key the test below
	// stops asking the question it was written to ask.
	if strings.Contains(legacy, "produced_by") {
		t.Fatal("the fixture names a producer, so it is not the pre-story record this test is about")
	}
	if err := os.WriteFile(StageRecordPath(dir), []byte(legacy), 0o600); err != nil {
		t.Fatalf("writing the legacy record: %v", err)
	}

	read, err := ReadStageRecord(dir)
	if err != nil {
		t.Fatalf("a record written before this story's field existed no longer reads at all: %v", err)
	}
	if read.ProducedBy != "applier" {
		t.Errorf("a record carrying no producer reads as ProducedBy=%q, want \"applier\"; every record already "+
			"on disk was written by the applier, and reading absence as anything else revalidates every retained "+
			"tree an earlier release staged (R5.2)", read.ProducedBy)
	}
	// The rest of the record must be untouched by the new field: this is the
	// compatibility claim in full, not just the one key.
	if read.Depth != DepthConfigure || len(read.Gates) != 1 || read.EbuildDigest != "abc123" {
		t.Errorf("the legacy record lost content on the way in: depth=%v gates=%d digest=%q",
			read.Depth, len(read.Gates), read.EbuildDigest)
	}
}
