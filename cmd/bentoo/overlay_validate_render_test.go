package main

// Authored for story 031, sub-task 6.3 — R5, R5.5, R5.6, R5.8, R5.9, R6.1.
//
// Two surfaces render the same report, and this is the first JSON output in
// this CLI — there is no precedent to copy, so the field names are a contract
// established here rather than a by-product of Go field names. That is why the
// JSON assertions below read raw keys out of a map instead of unmarshalling
// into the Go struct: unmarshalling into the struct would pass even if every
// tag were dropped and the keys silently became Go identifiers.
//
// captureStdout comes from overlay_analyze_test.go; captureExit from
// snapshot_test.go; stubValidateRunner from overlay_validate_test.go.
//
// Red is DEFERRED to Run mode: the command does not exist yet.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

// mixedReport carries one of every outcome, so a renderer that handles only the
// happy path cannot pass.
//
// The last entry is the case story 033 added and the shipped shape could not
// express: TWO gates skipping for DIFFERENT causes. It used to be one Reason
// field that whichever gate wrote last won.
func mixedReport() validate.Report {
	return validate.Report{
		Overlay: "/var/db/repos/bentoo",
		Results: []validate.EbuildResult{
			{
				Package:        "media-plugins/gst-plugins-qt6",
				Version:        "1.29.2",
				Depth:          "options",
				DepthRequested: "options",
				Sources:        []string{"gst-plugins-good-1.29.2/meson.options"},
				Gates: []validate.GateResult{
					{Gate: validate.GateOptions, Outcome: validate.OutcomeFailed, Findings: []validate.Finding{
						{Gate: validate.GateOptions, Severity: validate.SeverityError, Detail: "-Daalib= is passed but upstream 1.29.2 declares no such option"},
					}},
					{Gate: validate.GateQA, Outcome: validate.OutcomePass},
				},
			},
			{
				Package:        "media-plugins/gst-plugins-qt6",
				Version:        "1.28.6",
				Depth:          "options",
				DepthRequested: "options",
				Sources:        []string{"gst-plugins-good-1.28.6/meson.options"},
				Gates: []validate.GateResult{
					{Gate: validate.GateOptions, Outcome: validate.OutcomePass},
					{Gate: validate.GateQA, Outcome: validate.OutcomeSkipped, Reason: "pkgcheck was not found on PATH"},
				},
			},
			{
				Package:        "dev-libs/cmakeproj",
				Version:        "1.0",
				Depth:          "options",
				DepthRequested: "configure",
				DepthReason:    "the option gate could not read the archive, so nothing deeper could run",
				Gates: []validate.GateResult{
					{Gate: validate.GateOptions, Outcome: validate.OutcomeSkipped, Reason: "build system is not Meson: cmake"},
					{Gate: validate.GateConfigure, Outcome: validate.OutcomeSkipped, Reason: "the staged tree could not be prepared: permission denied"},
				},
			},
		},
	}
}

// TestRender_TextNamesEverySkipReason is the rule the story turns on: a skip
// nobody can read is a pass.
func TestRender_TextNamesEverySkipReason(t *testing.T) {
	stubValidateRunner(t, mixedReport())

	out := captureStdout(t, func() {
		captureExit(t, func() { runValidate(newValidateCmd(), []string{}) })
	})

	for _, want := range []string{
		"pkgcheck was not found on PATH",
		"build system is not Meson: cmake",
		// Both reasons of the two-gates-skipping entry, which is the assertion
		// the shipped renderer could not satisfy: one shared Reason field meant
		// one of these two was always lost.
		"the staged tree could not be prepared: permission denied",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the text report does not carry the skip reason %q\n--- got ---\n%s", want, out)
		}
	}
	if !strings.Contains(out, "aalib") {
		t.Errorf("the text report does not name the failing option\n--- got ---\n%s", out)
	}
}

// TestRender_TextNamesTheGateBesideItsReason is R4.4 on the human surface. Two
// skips with two causes are only actionable if the operator can tell WHICH gate
// each one stopped: "permission denied" against the configure gate and "not
// Meson" against the option gate call for different work.
func TestRender_TextNamesTheGateBesideItsReason(t *testing.T) {
	stubValidateRunner(t, mixedReport())

	out := captureStdout(t, func() {
		captureExit(t, func() { runValidate(newValidateCmd(), []string{}) })
	})

	for _, want := range []string{
		"options: build system is not Meson: cmake",
		"configure: the staged tree could not be prepared: permission denied",
		"qa: pkgcheck was not found on PATH",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the text report does not name the gate beside its reason: want %q\n--- got ---\n%s", want, out)
		}
	}
}

// TestRender_TextTallyIsTheWorstGatePerEbuild pins that the headline cannot be
// taken from one favoured gate. The shipped renderer tallied res.Options alone,
// which is how a configure failure would have printed as a pass.
func TestRender_TextTallyIsTheWorstGatePerEbuild(t *testing.T) {
	stubValidateRunner(t, validate.Report{
		Overlay: "/var/db/repos/bentoo",
		Results: []validate.EbuildResult{{
			Package: "media-plugins/gst-plugins-qt6",
			Version: "1.29.2",
			Gates: []validate.GateResult{
				{Gate: validate.GateOptions, Outcome: validate.OutcomePass},
				{Gate: validate.GateConfigure, Outcome: validate.OutcomeFailed, Findings: []validate.Finding{
					{Gate: validate.GateConfigure, Severity: validate.SeverityError, Detail: `meson.build:1:0: ERROR: Unknown option: "aalib".`},
				}},
			},
		}},
	})

	// captureStdout goes OUTSIDE captureExit. osExit panics with a sentinel that
	// captureExit recovers, so with the nesting the other way the assignment of
	// the captured text never runs and every assertion about it passes on an
	// empty string.
	var code int
	var exited bool
	out := captureStdout(t, func() {
		code, exited = captureExit(t, func() { runValidate(newValidateCmd(), []string{}) })
	})

	if !strings.Contains(out, "1 ebuilds: 1 failed, 0 passed, 0 skipped") {
		t.Errorf("the summary does not count the ebuild as failed\n--- got ---\n%s", out)
	}
	if !exited || code != 1 {
		t.Errorf("exit code: got %d (exited=%v), want 1 for a configure-gate error", code, exited)
	}
}

// TestRender_JsonIsOneDocument is R5.8. One document, not a stream — a caller
// piping this into `jq` must not have to reassemble it.
func TestRender_JsonIsOneDocument(t *testing.T) {
	stubValidateRunner(t, mixedReport())

	out := captureStdout(t, func() {
		captureExit(t, func() { runValidate(newValidateCmd(), []string{"--json"}) })
	})

	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("--json output is not a single JSON document: %v\n--- got ---\n%s", err, out)
	}
}

// TestRender_JsonKeysAreTheContract pins the wire names against the Go field
// names. A rename on the Go side must not silently rename the wire key.
func TestRender_JsonKeysAreTheContract(t *testing.T) {
	stubValidateRunner(t, mixedReport())

	out := captureStdout(t, func() {
		captureExit(t, func() { runValidate(newValidateCmd(), []string{"--json"}) })
	})

	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("unmarshalling: %v", err)
	}

	results, ok := doc["results"].([]any)
	if !ok {
		t.Fatalf("top-level key \"results\" missing or not an array; got keys %v", jsonKeysOf(doc))
	}
	if len(results) != 3 {
		t.Fatalf("results: got %d entries, want 3", len(results))
	}

	first, ok := results[0].(map[string]any)
	if !ok {
		t.Fatal("results[0] is not an object")
	}
	for _, key := range []string{"package", "version", "depth", "depth_requested", "gates", "sources"} {
		if _, present := first[key]; !present {
			t.Errorf("result object is missing key %q; got %v", key, jsonKeysOf(first))
		}
	}
	// The keys story 033 replaced. They are asserted ABSENT rather than left
	// unmentioned, so a half-done revert that reintroduced one of them beside
	// `gates` would fail here instead of shipping two disagreeing shapes.
	for _, gone := range []string{"options", "qa", "reason", "findings"} {
		if _, present := first[gone]; present {
			t.Errorf("result object still carries the replaced key %q; findings and outcomes moved onto gates", gone)
		}
	}

	gates, ok := first["gates"].([]any)
	if !ok || len(gates) == 0 {
		t.Fatalf("results[0].gates is missing or empty; got %v", first["gates"])
	}
	gate, ok := gates[0].(map[string]any)
	if !ok {
		t.Fatal("results[0].gates[0] is not an object")
	}
	for _, key := range []string{"gate", "outcome", "findings"} {
		if _, present := gate[key]; !present {
			t.Errorf("gate object is missing key %q; got %v", key, jsonKeysOf(gate))
		}
	}
}

// TestRender_JsonCarriesEveryGatesOwnReason is R4.4 on the machine surface. The
// third entry of mixedReport has two gates skipping for two causes, and a
// document that carried one of them would be the shipped defect with a new key
// name.
func TestRender_JsonCarriesEveryGatesOwnReason(t *testing.T) {
	stubValidateRunner(t, mixedReport())

	out := captureStdout(t, func() {
		captureExit(t, func() { runValidate(newValidateCmd(), []string{"--json"}) })
	})

	var doc struct {
		Results []struct {
			Gates []struct {
				Gate    string `json:"gate"`
				Outcome string `json:"outcome"`
				Reason  string `json:"reason"`
			} `json:"gates"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("unmarshalling: %v\n--- got ---\n%s", err, out)
	}
	if len(doc.Results) != 3 {
		t.Fatalf("results: got %d, want 3", len(doc.Results))
	}

	reasons := map[string]string{}
	for _, gate := range doc.Results[2].Gates {
		if gate.Outcome != "SKIPPED" {
			t.Errorf("gate %q: got outcome %q, want SKIPPED", gate.Gate, gate.Outcome)
		}
		if gate.Reason == "" {
			t.Errorf("gate %q reports SKIPPED with no reason on the wire", gate.Gate)
		}
		reasons[gate.Gate] = gate.Reason
	}
	if reasons["options"] == reasons["configure"] {
		t.Errorf("both gates report the same reason (%q); one skip overwrote the other's explanation", reasons["options"])
	}
}

// TestRender_JsonCarriesTheEvidence pins Declared.Sources reaching the wire. A
// PASS whose evidence is not printed cannot be audited, which is the complaint
// this story answers — it would be a poor joke to reproduce it here.
func TestRender_JsonCarriesTheEvidence(t *testing.T) {
	stubValidateRunner(t, mixedReport())

	out := captureStdout(t, func() {
		captureExit(t, func() { runValidate(newValidateCmd(), []string{"--json"}) })
	})

	if !strings.Contains(out, "gst-plugins-good-1.29.2/meson.options") {
		t.Errorf("the JSON report does not list the archive members actually read\n--- got ---\n%s", out)
	}
}

// TestRender_ExitCodeMatchesTheRenderedOutcomes keeps the two from drifting: a
// report that prints a failure and exits 0 is worse than no report.
func TestRender_ExitCodeMatchesTheRenderedOutcomes(t *testing.T) {
	stubValidateRunner(t, mixedReport())

	// The two captures nest this way round and not the other. osExit panics with
	// a sentinel captureExit recovers, so with captureExit on the outside the
	// `out = captureStdout(…)` assignment never runs and the FAILED assertion
	// below silently tests an empty string — a vacuous green in the very test
	// that exists to stop the report and the exit code drifting apart.
	var code int
	var exited bool
	out := captureStdout(t, func() {
		code, exited = captureExit(t, func() { runValidate(newValidateCmd(), []string{}) })
	})

	if !exited {
		t.Fatal("the command did not exit")
	}
	if strings.Contains(out, "FAILED") && code == 0 {
		t.Errorf("the report prints FAILED but the command exited 0")
	}
	if code != 1 {
		t.Errorf("exit code: got %d, want 1 for a report carrying an error finding", code)
	}
}

func jsonKeysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
