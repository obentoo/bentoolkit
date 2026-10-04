package overlay

// Authored for story 046, sub-task 7.3 — R5.1, R5.2.
//
// Written from the contract: 7.3's objective — "The remaining printing sites in
// internal/overlay return their facts" — over the three files the first
// measurement missed: baseline.go, realign_reviewer.go and status.go.
//
// # Why "nothing is written to stdout" is not the assertion that matters here
//
// None of the three calls Printf. They call output.Sprintf, which BUILDS a
// coloured string and hands it back — so a test that only asked about stdout
// would pass today, over three files that are exactly as far across the
// boundary as the two that print.
//
// What is wrong is that the APPEARANCE is decided inside the library. By the
// time the caller holds the sentence the colour is chosen, the identifier is
// inside prose, and the finding can go to a terminal and nowhere else: not to
// JSON, not to Markdown, not to a count (R5.2).
//
// # Which is why these tests force colour ON
//
// fatih/color disables itself off a TTY, and `go test` is off a TTY — so under
// the test binary output.Sprintf returns bare text and the defect is invisible.
// forceColor below is what makes the library's decision observable: with colour
// on, a value the library coloured carries an escape and a value it did not
// stays clean. That is the difference between "this happens to look fine in CI"
// and "the library did not choose".
//
// The contract is 7.1's, unchanged: type Finding{Atom, Detail} reached through
// CompareReport.Findings.
//
// Red on arrival, two ways: CompareReport has no Findings, and FormatStatus
// returns escapes once colour is forced.
//
// Borrowed, never re-declared: realignFixture and realignFakeReviewer
// (realign_reviewer_test.go), captureOverlayStdout (the helper authored with
// 7.1).
//
// NOTE for the executor: if FormatStatus moves to cmd/bentoo — which is where a
// formatter belongs once the library returns facts — this test moves with it
// and keeps asserting the same thing. What may not happen is the assertion
// being dropped because the function it names went away.

import (
	"strings"
	"testing"

	"github.com/fatih/color"
)

// forceColor turns colouring on for the duration of one test, and restores it.
//
// Without it every assertion below is vacuous: fatih/color returns plain text
// off a TTY, so the library's colour decisions are invisible to exactly the
// test that exists to find them.
func forceColor(t *testing.T) {
	t.Helper()

	original := color.NoColor
	color.NoColor = false
	t.Cleanup(func() { color.NoColor = original })
}

// statusFixture is two packages with changes of three kinds, so the formatting
// has both identifiers and statuses to decorate.
func statusFixture() []PackageStatus {
	return []PackageStatus{
		{
			Category: "app-misc",
			Package:  "jq",
			Changes: []FileChange{
				{Type: FileTypeEbuild, Name: "jq-1.8.0.ebuild", Status: "Added"},
				{Type: FileTypeManifest, Name: "Manifest", Status: "Modified"},
			},
		},
		{
			Category: "dev-lang",
			Package:  "go",
			Changes: []FileChange{
				{Type: FileTypeEbuild, Name: "go-1.26.0.ebuild", Status: "Deleted"},
			},
		},
	}
}

// TestStatusFactsCarryNoAppearance pins R5.2 for status.go. The status of a
// working tree is a fact about files; which colour "Modified" is printed in is
// a decision for whoever is printing it.
func TestStatusFactsCarryNoAppearance(t *testing.T) {
	forceColor(t)

	got := FormatStatus(statusFixture())

	if strings.ContainsRune(got, 0x1b) {
		t.Errorf("the status text the library returns carries an escape sequence — the library chose the colour, so this text cannot be exported, diffed or counted (R5.2):\n%q", got)
	}
	if !strings.Contains(got, "app-misc") || !strings.Contains(got, "jq") {
		t.Errorf("the status text lost the identifiers it is about:\n%s", got)
	}
}

// TestStatusEmptyCaseCarriesNoAppearanceEither is the case that would otherwise
// be missed: "working directory clean" is built by a different branch, with a
// different colour, and a fix applied only to the loop above leaves it coloured.
func TestStatusEmptyCaseCarriesNoAppearanceEither(t *testing.T) {
	forceColor(t)

	got := FormatStatus(nil)

	if strings.ContainsRune(got, 0x1b) {
		t.Errorf("the clean-tree sentence carries an escape sequence: %q (R5.2)", got)
	}
	if strings.TrimSpace(got) == "" {
		t.Error("a clean tree produced no sentence at all — silence is not a finding")
	}
}

// TestStatusWritesNothingToStdout is the regression half, cheap and worth
// keeping: the day someone reaches for output.Success.Printf in status.go, this
// fails without anyone reading the diff.
func TestStatusWritesNothingToStdout(t *testing.T) {
	forceColor(t)

	var got string
	out := captureOverlayStdout(t, func() { got = FormatStatus(statusFixture()) })

	if strings.TrimSpace(out) != "" {
		t.Errorf("formatting a status wrote to stdout (R5.1):\n%s", out)
	}
	if strings.TrimSpace(got) == "" {
		t.Fatal("nothing was returned either — the facts went nowhere at all")
	}
}

// TestBaselineSkipReachesTheCallerAsAFinding pins baseline.go's one run-level
// fact. "Nothing was compared against ::gentoo" is the outcome that must never
// render as silence: a report missing it is indistinguishable from one where
// every package matched.
//
// It is asserted through MarkBaselineSkipped DIRECTLY, not through
// AnnotateBaseline as 7.2 does, because this is the function baseline.go owns —
// a fix that satisfied only the AnnotateBaseline path would leave every other
// caller of this one silent.
func TestBaselineSkipReachesTheCallerAsAFinding(t *testing.T) {
	forceColor(t)

	const lookedFor = "/var/db/repos/gentoo"
	report := &CompareReport{}

	MarkBaselineSkipped(report, lookedFor)

	if report.BaselineSkipped == "" {
		t.Fatal("MarkBaselineSkipped recorded nothing")
	}
	if strings.ContainsRune(report.BaselineSkipped, 0x1b) {
		t.Errorf("the skip sentence carries an escape sequence: %q (R5.2)", report.BaselineSkipped)
	}
	if !strings.Contains(report.BaselineSkipped, lookedFor) {
		t.Errorf("the skip sentence does not name the tree it looked for: %q — a finding whose identifier is missing cannot be acted on (R5.1)", report.BaselineSkipped)
	}

	if len(report.Findings) == 0 {
		t.Fatal("the skip did not reach the caller's findings — it exists only in a field a renderer has to know to look at, which is how it goes missing from the export and the count (R5.1)")
	}
	for _, finding := range report.Findings {
		if strings.ContainsRune(finding.Detail, 0x1b) || strings.ContainsRune(finding.Atom, 0x1b) {
			t.Errorf("a baseline finding carries an escape sequence: %+v (R5.2)", finding)
		}
	}
}

// TestBaselineSkipWithNothingConfiguredStillStatesItself is the hostile half:
// the branch where there is no path to name.
//
// A fix that put the LOOKED-FOR PATH in the finding and nothing else would
// return an empty finding here — and an empty finding is silence wearing the
// shape of a report, which is the exact failure the non-empty branch is guarded
// against.
func TestBaselineSkipWithNothingConfiguredStillStatesItself(t *testing.T) {
	forceColor(t)

	report := &CompareReport{}
	MarkBaselineSkipped(report, "")

	if report.BaselineSkipped == "" {
		t.Fatal("nothing configured produced no sentence — the run reports as silence that it never ran")
	}
	if len(report.Findings) == 0 {
		t.Fatal("nothing configured produced no finding — a review that could not run must say so wherever a review that ran would have spoken (R5.1)")
	}
}

// TestRealignVerdictsReachTheCallerAsFacts pins realign_reviewer.go. The
// model's reading is put on the report so the operator can weigh it; a reading
// that arrives pre-coloured is one no export can carry and no count can
// summarize.
func TestRealignVerdictsReachTheCallerAsFacts(t *testing.T) {
	forceColor(t)

	report, prov, opts := realignFixture(t)
	reviewer := &realignFakeReviewer{note: RealignNote{
		Justified: true,
		Why:       "the qt6 option list is not handled by gstreamer-meson, so the inherit divergence still earns its place",
	}}

	out := captureOverlayStdout(t, func() {
		AnnotateRealignVerdicts(t.Context(), report, reviewer, prov, opts)
	})

	if strings.TrimSpace(out) != "" {
		t.Errorf("the realignment review wrote to stdout (R5.1):\n%s", out)
	}
	if reviewer.calls == 0 {
		t.Fatal("the reviewer was never called — the fixture produced nothing to judge, so nothing below is being tested")
	}

	for _, result := range report.Results {
		if strings.ContainsRune(result.RealignVerdict, 0x1b) {
			t.Errorf("%s/%s carries an escape sequence in its realignment verdict: %q — the library chose how a model's reading looks (R5.2)",
				result.Category, result.Package, result.RealignVerdict)
		}
	}

	if len(report.Findings) == 0 {
		t.Fatal("the realignment review returned no finding — the count of unjudged divergences, and the readings themselves, exist only as sentences somebody has to print (R5.1)")
	}
	for _, finding := range report.Findings {
		if strings.ContainsRune(finding.Detail, 0x1b) || strings.ContainsRune(finding.Atom, 0x1b) {
			t.Errorf("a realignment finding carries an escape sequence: %+v (R5.2)", finding)
		}
	}
}
