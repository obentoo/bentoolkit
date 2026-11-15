package main

import (
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
	"github.com/obentoo/bentoolkit/internal/common/report"
)

// TestRowReason_NamesTheGateThatStoppedTheBump reproduces the 2026-11-13
// report, where every row read as the `options` gate's skip although the bump
// was stopped further on. A failed gate with no findings, and a skip at a
// deeper gate, must reach the row, each named by its gate.
func TestRowReason_NamesTheGateThatStoppedTheBump(t *testing.T) {
	entry := validationPlanEntry{Package: "app-misc/demo", Version: "2.0", Depth: "configure", Reason: "minor bump"}
	optionsSkip := validate.GateResult{Gate: validate.GateOptions, Outcome: validate.OutcomeSkipped, Reason: "no meson_options.txt to compare"}

	failed := validate.EbuildResult{Gates: []validate.GateResult{
		optionsSkip,
		{Gate: validate.GateConfigure, Outcome: validate.OutcomeFailed, Reason: "configure died: dependency 'qt6' not found"},
	}}
	if got := rowReason(report.Errored, failed, entry); !strings.Contains(got, "qt6") {
		t.Errorf("an errored row reads %q; the failed gate's reason is missing", got)
	}

	skipped := validate.EbuildResult{Gates: []validate.GateResult{
		optionsSkip,
		{Gate: validate.GateConfigure, Outcome: validate.OutcomeSkipped, Reason: "this host does not hold the build dependencies"},
	}}
	got := rowReason(report.Inconclusive, skipped, entry)
	for _, want := range []string{"meson_options", "build dependencies"} {
		if !strings.Contains(got, want) {
			t.Errorf("a skipped row reads %q; it does not carry %q", got, want)
		}
	}
}
