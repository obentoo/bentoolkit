package overlay

// Authored for story 046, sub-task 7.1 — R5.1, R5.2.
//
// Written from the contract: R5.1 — "WHERE a package under internal/overlay or
// internal/snapshot establishes a finding for the operator, THE SYSTEM SHALL
// return that finding to its caller" — and R5.2, which says what returning it
// buys: the same finding rendered in every mode and included in the export,
// "without the library having chosen how it looks". design.md D7 counts what is
// being moved: 30 output.* calls in this file alone.
//
// # The contract this file assumes, and why it is minimal
//
// design.md fixes the ENVELOPE and leaves the finding's own shape to this task
// (Out of Scope: "overlay validate's report content ... migrates in 047"). So
// only two things are asked of a finding here, both of them stated by R5.1 and
// R5.2 rather than invented:
//
//	type Finding struct { Atom string; Detail string }
//
//   - it names WHAT it is about in a field (Atom), because an identifier
//     formatted into a sentence cannot be counted, filtered or exported; and
//   - it says what was found in text the library did not decorate (Detail).
//
// If the implementer spells them differently the rename is mechanical. What is
// specified is that the identifier is a FIELD and the text is UNDECORATED.
//
// Red on arrival: CompareReport has no Findings, and there is no Finding type.

import (
	"strings"
	"testing"
)

// comparedThree runs a comparison over three packages that land in three
// different conditions: one behind upstream, one level with it, and one
// upstream does not carry at all.
//
// Three, because a finding list that only ever holds one entry cannot be shown
// to keep an ORDER, and because the three conditions are the three sentences
// compare.go prints today.
func comparedThree(t *testing.T) *CompareReport {
	t.Helper()

	prov := &fakeProvider{
		versions: map[string][]string{
			"cat/behind":  {"2.0"},
			"cat/current": {"1.0"},
			// cat/absent is deliberately missing: the provider answers
			// ErrNotFound, which is the third condition.
		},
	}

	pkgs := []PackageInfo{
		{Category: "cat", Package: "behind", LatestVersion: "1.0"},
		{Category: "cat", Package: "current", LatestVersion: "1.0"},
		{Category: "cat", Package: "absent", LatestVersion: "1.0"},
	}

	report, err := CompareWithProvider(t.Context(), pkgs, prov, CompareOptions{IncludeSynced: true, IncludeNotInRemote: true})
	if err != nil {
		t.Fatalf("CompareWithProvider returned an error: %v", err)
	}
	return report
}

// TestCompareReturnsItsFindings pins R5.1. A comparison establishes facts about
// packages; the caller receives them as values, so the same facts can be
// rendered three ways, exported and counted.
//
// The order is asserted because it is information: findings are established as
// the results are walked, and a set-shaped answer would leave a renderer to
// invent an order of its own — which is how two modes come to disagree about
// what a run said.
func TestCompareReturnsItsFindings(t *testing.T) {
	report := comparedThree(t)

	if len(report.Findings) == 0 {
		t.Fatal("the comparison returned no finding at all — everything it established was printed and lost (R5.1)")
	}

	var atoms []string
	for _, finding := range report.Findings {
		if strings.TrimSpace(finding.Atom) == "" {
			t.Errorf("a finding carries no atom: %+v — an identifier formatted into a sentence cannot be counted, filtered or exported (R5.1)", finding)
		}
		atoms = append(atoms, finding.Atom)
	}

	// The results are sorted by atom before the report is returned, and the
	// findings follow the results rather than the order the goroutines
	// happened to finish in.
	sorted := make([]string, len(atoms))
	copy(sorted, atoms)
	for i := 1; i < len(sorted); i++ {
		if sorted[i-1] > sorted[i] {
			t.Errorf("the findings are not in the order they were established (results are sorted by atom): %q", atoms)
			break
		}
	}
}

// TestFindingsCarryTheIdentifiersThePrintedFormCarried is the other half of
// R5.1. Returning a list of sentences would satisfy "the caller receives them"
// and lose everything that made the sentence useful: which package it is about.
func TestFindingsCarryTheIdentifiersThePrintedFormCarried(t *testing.T) {
	report := comparedThree(t)

	compared := map[string]bool{}
	for _, result := range report.Results {
		compared[result.Category+"/"+result.Package] = true
	}

	for _, finding := range report.Findings {
		if !compared[finding.Atom] {
			t.Errorf("finding %+v names %q, which is not one of the packages compared: %v", finding, finding.Atom, compared)
		}
	}
}

// TestFindingsChooseNoAppearance pins R5.2, and it is the assertion that
// actually moves this codebase. compare.go does not write to stdout today — it
// builds strings with output.Sprintf — so "nothing was printed" would pass over
// the defect untouched. What is wrong is that the COLOUR is already chosen by
// the time the caller sees the text, which is the library deciding how a
// finding looks.
//
// A finding carrying an escape sequence cannot be exported to JSON, cannot be
// put in a Markdown file and cannot be rendered plain for a log — the three
// things R5.2 exists to make possible.
func TestFindingsChooseNoAppearance(t *testing.T) {
	report := comparedThree(t)

	for _, finding := range report.Findings {
		if strings.ContainsRune(finding.Detail, 0x1b) {
			t.Errorf("finding %q carries an escape sequence in its detail: %q — the library has chosen how it looks (R5.2)", finding.Atom, finding.Detail)
		}
		if strings.ContainsRune(finding.Atom, 0x1b) {
			t.Errorf("finding atom %q carries an escape sequence — the identifier is a fact, not a rendering", finding.Atom)
		}
	}
}

// TestComparisonWritesNothingToStdout is the cheap half of the same rule, kept
// because it is the one that catches a REGRESSION: the day someone reaches for
// output.Info.Printf inside this package again, this fails without anyone
// having to read the diff.
func TestComparisonWritesNothingToStdout(t *testing.T) {
	var report *CompareReport

	out := captureOverlayStdout(t, func() { report = comparedThree(t) })

	if strings.TrimSpace(out) != "" {
		t.Errorf("the comparison wrote to stdout (R5.1):\n%s", out)
	}
	if report == nil || len(report.Findings) == 0 {
		t.Fatal("the comparison established nothing — silence with no findings is not the requirement, it is the same loss by another route")
	}
}
