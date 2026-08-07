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
func mixedReport() validate.Report {
	return validate.Report{
		Overlay: "/var/db/repos/bentoo",
		Results: []validate.EbuildResult{
			{
				Package: "media-plugins/gst-plugins-qt6",
				Version: "1.29.2",
				Options: "FAILED",
				QA:      "PASS",
				Sources: []string{"gst-plugins-good-1.29.2/meson.options"},
				Findings: []validate.Finding{
					{Gate: "options", Severity: "error", Detail: "-Daalib= is passed but upstream 1.29.2 declares no such option"},
				},
			},
			{
				Package: "media-plugins/gst-plugins-qt6",
				Version: "1.28.6",
				Options: "PASS",
				QA:      "SKIPPED",
				Reason:  "pkgcheck was not found on PATH",
				Sources: []string{"gst-plugins-good-1.28.6/meson.options"},
			},
			{
				Package: "dev-libs/cmakeproj",
				Version: "1.0",
				Options: "SKIPPED",
				QA:      "PASS",
				Reason:  "build system is not Meson: cmake",
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
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the text report does not carry the skip reason %q\n--- got ---\n%s", want, out)
		}
	}
	if !strings.Contains(out, "aalib") {
		t.Errorf("the text report does not name the failing option\n--- got ---\n%s", out)
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
	for _, key := range []string{"package", "version", "options", "qa", "findings"} {
		if _, present := first[key]; !present {
			t.Errorf("result object is missing key %q; got %v", key, jsonKeysOf(first))
		}
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

	var out string
	code, exited := captureExit(t, func() {
		out = captureStdout(t, func() { runValidate(newValidateCmd(), []string{}) })
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
