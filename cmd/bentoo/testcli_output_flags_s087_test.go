package main

// Story 087, sub-task 1.2 — R2.1, R2.2.
//
// Written from the contract: when a testCLI run's test ends, the harness puts
// verbose, quiet and noColor back to the values they held before the run, and
// the harness's pinned list of restored report globals names all six.
//
// The leak: newRootCmd's PersistentPreRunE publishes the tree's --verbose,
// --quiet and --no-color into those package variables, and nothing puts them
// back, so a later test in the same process — under -shuffle, any later
// test — reads a run's flags it never passed.
//
// # Both directions
//
// A restore that merely RESETS the three to false after a run would fix the
// obvious leak (a --quiet run leaving quiet = true) and break its converse: a
// test that set quiet = true itself, then ran the CLI without --quiet, would
// find it false afterwards. So each flag is checked both ways, the converse
// first: a run that clears a value the test had set, and a run that sets a
// value the test had left clear. Each row also checks the run DID change the
// variable, so no row can pass without exercising the leak.

import (
	"testing"
)

// TestS087_1_2_ReportFlagGlobalsNameTheOutputFlags is R2.2: the list the
// harness restores names verbose, quiet and noColor beside the three report
// flags it already restored.
func TestS087_1_2_ReportFlagGlobalsNameTheOutputFlags(t *testing.T) {
	want := map[any]string{
		&autoupdateUI:     "autoupdateUI",
		&autoupdateAll:    "autoupdateAll",
		&autoupdateExport: "autoupdateExport",
		&verbose:          "verbose",
		&quiet:            "quiet",
		&noColor:          "noColor",
	}
	got := map[any]bool{}
	for _, p := range reportFlagGlobals() {
		got[p] = true
	}
	for p, name := range want {
		if !got[p] {
			t.Errorf("reportFlagGlobals() does not name %s — a testCLI run leaves it set for every later test in the process", name)
		}
	}
	if n := len(reportFlagGlobals()); n != len(want) {
		t.Errorf("reportFlagGlobals() names %d globals, want the %d report and output globals", n, len(want))
	}
}

// TestS087_1_2_RunRestoresOutputFlagsAfterItsTest is R2.1, end to end through
// the harness: each row runs one `bentoo version` in a subtest, and once that
// subtest has ended the variable must hold what it held before.
func TestS087_1_2_RunRestoresOutputFlagsAfterItsTest(t *testing.T) {
	// This test changes the three variables itself; put them back for the
	// tests after it whatever the harness does.
	restoreFlagGlobals(t, &verbose, &quiet, &noColor)

	type row struct {
		name   string
		target *bool
		before bool
		args   []string
	}
	rows := []row{
		// Converse first: the run CLEARS a value the test had set.
		{name: "quiet set before, run without --quiet", target: &quiet, before: true, args: []string{"version"}},
		{name: "verbose set before, run without --verbose", target: &verbose, before: true, args: []string{"version"}},
		{name: "noColor set before, run without --no-color", target: &noColor, before: true, args: []string{"version"}},
		// Then the run SETS a value the test had left clear.
		{name: "quiet clear before, run with --quiet", target: &quiet, before: false, args: []string{"version", "--quiet"}},
		{name: "verbose clear before, run with --verbose", target: &verbose, before: false, args: []string{"version", "--verbose"}},
		{name: "noColor clear before, run with --no-color", target: &noColor, before: false, args: []string{"version", "--no-color"}},
	}

	for _, r := range rows {
		verbose, quiet, noColor = false, false, false
		*r.target = r.before

		t.Run(r.name, func(t *testing.T) {
			_, stderr, code := newTestCLI(t).Run(r.args...)
			if code != 0 {
				t.Fatalf("`bentoo %v` exited %d (stderr: %q)", r.args, code, stderr)
			}
			if *r.target == r.before {
				t.Fatalf("the run left the variable at %v; this row would pass without exercising the leak", *r.target)
			}
		})

		if *r.target != r.before {
			t.Errorf("%s: the variable was left at %v after the run's test ended; it was %v before the run", r.name, *r.target, r.before)
		}
	}
}
