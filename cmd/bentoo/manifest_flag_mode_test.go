package main

import (
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/config"
)

// Sub-task 14.1 — R3.3: a manifest run resolves its render mode ONCE, from the
// full precedence chain, and its live region obeys the same answer its report
// does.
//
// # Why this file exists at all
//
// manifestUsesTUI leaves uiInputs.Flag at its zero value, on the recorded
// ground that `overlay manifest` registers no --ui. Sub-task 4.3 moved --ui to
// the ROOT's persistent flags, so half of that premise expired: a manifest run
// DOES parse --ui, and autoupdateUI then holds what that run itself was given.
// What an operator sees today on a terminal is `bentoo overlay manifest
// --ui=plain` rendering its report in plain and still starting the bubbletea
// live region — escape sequences out of the one mode whose help text promises
// "contains no escape sequence at all".
//
// The other half did NOT expire, and the third test below pins it: --no-tui
// stays on `overlay autoupdate` alone (root.go:122-124), so reading
// the --no-tui option here would be exactly the cross-command leak the comment
// describes. NoTUI staying at zero is the intended contract, not an oversight.
//
// # Every name carries the TestManifestFlagMode prefix
//
// Go's -run is a substring regex, and sub-task 13.4 MEASURED a pattern silently
// dropping tests whose names did not literally contain it. The prefix is what
// makes `go test ./cmd/bentoo/ -run TestManifestFlagMode` select this file
// whole instead of an unannounced subset of it.

// manifestFlagModeSetUI puts the run's --ui value in place and neutralises the
// two ambient sources, so each test states its own precedence chain instead of
// inheriting the developer's shell.
//
// restoreReportFlags is the package's own answer to the hazard testcli_test.go
// records: manifestUsesTUI and autoupdateUsesTUI read the flag variables
// directly, so a test that leaves one set decides the result of whichever file
// sorts after it. Restoring is not politeness here; it is what keeps every
// ordering equivalent.
func manifestFlagModeSetUI(t *testing.T, auOpts *autoupdateOptions, ui string, noTUI bool) {
	t.Helper()

	restoreReportFlags(t)
	autoupdateUI = ui
	auOpts.noTUI = noTUI

	// Empty is "not set" for all three, per resolveUIMode's own convention.
	t.Setenv("BENTOO_UI", "")
	t.Setenv("BENTOO_NO_TUI", "")
	t.Setenv("NO_COLOR", "")
}

// TestManifestFlagModePlainTurnsTheLiveRegionOff is the defect, stated as a
// requirement: on a terminal, --ui=plain must reach the manifest live region.
//
// A terminal is stubbed because that is the only place the defect is visible —
// off one, the Interactive input degrades the mode to plain anyway and the two
// answers agree by accident.
func TestManifestFlagModePlainTurnsTheLiveRegionOff(t *testing.T) {
	auOpts := testAutoupdateOptions()
	td := defaultDeps()
	stubUIIsTerminal(td, true)
	manifestFlagModeSetUI(t, auOpts, "plain", false)

	cfg := &config.Config{} // no ui block: --ui is the only source that speaks

	if manifestUsesTUI(discardLog(), cfg, td.uiIsTerminal) {
		t.Error("overlay manifest --ui=plain still started the live region on a terminal — the operator asked for the mode whose help text promises no escape sequence at all and got them anyway (R3.3)")
	}
}

// TestManifestFlagModeInlineTurnsTheLiveRegionOn is the converse half: the flag
// must be READ, not merely obeyed when it says off. A gate hard-wired to false
// would satisfy the test above and fail this one.
func TestManifestFlagModeInlineTurnsTheLiveRegionOn(t *testing.T) {
	auOpts := testAutoupdateOptions()
	td := defaultDeps()
	stubUIIsTerminal(td, true)
	manifestFlagModeSetUI(t, auOpts, "inline", false)

	cfg := &config.Config{}

	if !manifestUsesTUI(discardLog(), cfg, td.uiIsTerminal) {
		t.Error("overlay manifest --ui=inline left the live region off on a terminal — the flag is not being read, it is being ignored in one direction (R3.3)")
	}
}

// TestManifestFlagModeNoTUIDoesNotReachAManifestRun pins the half of the
// original comment that is STILL TRUE. --no-tui is declared on `overlay
// autoupdate` alone; a manifest run never parses it, so the --no-tui option holds
// another command's answer and must not be read here.
//
// The autoupdate assertion is what stops this being a vacuous pass. Both
// commands are asked in the SAME state: the flag is genuinely set and genuinely
// outranks --ui=inline where it is declared, so manifest staying on is a
// measured difference between two commands rather than a flag that did nothing
// to anybody.
func TestManifestFlagModeNoTUIDoesNotReachAManifestRun(t *testing.T) {
	auOpts := testAutoupdateOptions()
	td := defaultDeps()
	stubUIIsTerminal(td, true)
	manifestFlagModeSetUI(t, auOpts, "inline", true)

	cfg := &config.Config{}

	if (&autoupdateRun{opts: auOpts, deps: td, uiConfig: cfg}).autoupdateUsesTUI() {
		t.Fatal("the premise is wrong: --no-tui did not turn the live region off on the command that declares it, so this test cannot tell a leak from a flag that does nothing")
	}
	if !manifestUsesTUI(discardLog(), cfg, td.uiIsTerminal) {
		t.Error("--no-tui, which only `overlay autoupdate` declares, turned a manifest run's live region off — that is a cross-command leak: the manifest run never parsed the flag it just obeyed (R3.3)")
	}
}
