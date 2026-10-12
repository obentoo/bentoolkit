package main

// Authored for story 092 (autoupdate-apply-observable), sub-task 5.1: the paths
// this story must not change (R6.1-R6.3). Regression guards — green before and
// after the story.

import (
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
)

// R6.1/R6.2: every opt-out, and a non-terminal stdout, keep the plain
// reporter. The control row (a terminal, nothing set) proves the gate can say
// yes, so the plain rows are not vacuous.
func TestUnchanged092PlainUnderOptOuts(t *testing.T) {
	restoreFlagGlobals(t, &autoupdateUI)
	for _, tc := range []struct {
		name     string
		ui       string
		noTUI    bool
		env      map[string]string
		terminal bool
		wantTUI  bool
	}{
		{name: "control: terminal, nothing set", terminal: true, wantTUI: true},
		{name: "--ui plain", ui: "plain", terminal: true},
		{name: "--no-tui", noTUI: true, terminal: true},
		{name: "NO_COLOR", env: map[string]string{"NO_COLOR": "1"}, terminal: true},
		{name: "BENTOO_NO_TUI", env: map[string]string{"BENTOO_NO_TUI": "1"}, terminal: true},
		{name: "stdout not a terminal", terminal: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NO_COLOR", "")
			t.Setenv("BENTOO_NO_TUI", "")
			t.Setenv("BENTOO_UI", "")
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			autoupdateUI = tc.ui
			o := testAutoupdateOptions()
			o.noTUI = tc.noTUI
			d := defaultDeps()
			term := tc.terminal
			d.uiIsTerminal = func() bool { return term }
			if got := testAutoupdateRunWith(o, d).tuiEnabledForApply(); got != tc.wantTUI {
				t.Errorf("live region = %v, want %v", got, tc.wantTUI)
			}
		})
	}
}

// R6.3: a successful package's Apply Result block is byte-identical to today's.
func TestUnchanged092SuccessApplyResult(t *testing.T) {
	result := &autoupdate.ApplyResult{
		Package:    "dev-go/govulncheck",
		OldVersion: "1.1.3",
		NewVersion: "1.1.4",
		Success:    true,
		Fixed:      true,
		FixSummary: "rewrote SRC_URI",
	}
	got := captureStdout(t, func() { testAutoupdateRun(testAutoupdateOptions()).displayApplyResult(result) })
	want := strings.Join([]string{
		"",
		"Apply Result",
		"",
		"  dev-go/govulncheck",
		"    Version: 1.1.3 → 1.1.4",
		"    Status:  Success",
		"    Fixed:   manifest repaired by LLM — rewrote SRC_URI",
		"",
		"✓ Update applied successfully",
		"Don't forget to commit the changes with 'bentoo overlay commit'",
		"",
	}, "\n")
	if got != want {
		t.Errorf("success Apply Result changed:\n--- got ---\n%q\n--- want ---\n%q", got, want)
	}
}
