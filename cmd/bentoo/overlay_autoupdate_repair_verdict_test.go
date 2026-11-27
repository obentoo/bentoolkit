package main

import (
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

// TestVerdictReasonsFoldsACauseEveryGateRepeats: the breakpad refusal of
// 2026-11-26 is printed once, and the gates after it point at the first one.
func TestVerdictReasonsFoldsACauseEveryGateRepeats(t *testing.T) {
	cause := "adopting the staged tree /s for dev-util/breakpad-2026.07.24: files/x.patch differs from the published package directory /o; cp /s/x /o/x"
	gates := []validate.GateResult{
		{Gate: "options", Outcome: validate.OutcomeSkipped, Reason: "the staged tree of dev-util/breakpad-2026.07.24 could not be prepared, so no gate ever read this candidate: " + cause},
		{Gate: "patches", Outcome: validate.OutcomeSkipped, Reason: "the staged tree of dev-util/breakpad-2026.07.24 could not be prepared: " + cause},
		{Gate: "configure", Outcome: validate.OutcomeSkipped, Reason: "the staged tree of dev-util/breakpad-2026.07.24 could not be prepared: " + cause},
	}
	out := strings.Join(verdictReasons(gates), "\n")
	if n := strings.Count(out, "cp /s/x /o/x"); n != 1 {
		t.Errorf("the cause is printed %d times, want once:\n%s", n, out)
	}
	if n := strings.Count(out, "(the same cause as options)"); n != 2 {
		t.Errorf("%d gate(s) point at options, want 2:\n%s", n, out)
	}
}

// TestVerdictReasonsKeepsShortAndDistinctReasons: a short reason and a
// different cause are printed whole, and a PASS carries no reason.
func TestVerdictReasonsKeepsShortAndDistinctReasons(t *testing.T) {
	gates := []validate.GateResult{
		{Gate: "options", Outcome: validate.OutcomeSkipped, Reason: "no distdir: none found"},
		{Gate: "patches", Outcome: validate.OutcomeSkipped, Reason: "no distdir: none found"},
		{Gate: "configure", Outcome: validate.OutcomePass, Reason: "ignored"},
	}
	lines := verdictReasons(gates)
	if !strings.HasSuffix(lines[1], "no distdir: none found") {
		t.Errorf("a short repeated reason was folded: %q", lines[1])
	}
	if strings.Contains(lines[2], "ignored") {
		t.Errorf("a PASS printed its reason: %q", lines[2])
	}
}
