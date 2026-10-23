package report

// Authored for story 047, sub-task 2.2 — S047-R1.1, S047-R3.2, S047-R3.3,
// S047-R6.1, S047-R6.2.
//
// What CompareRun.Sections SAYS, pinned. compare_run_json_test.go pins the
// other half — the document the same payload marshals into — and the two do
// not overlap: a key set is not something Sections can state, and a sentence is
// not something a JSON key can carry.
//
// # The cases here are the ones a rewrite could quietly lose
//
// Five of the six assertions below are about a report telling the truth rather
// than about it looking right, and each names the way it could go wrong:
//
//   - a section that vanishes when it has no row, which makes "nothing is
//     redundant" indistinguishable from a report that was cut short;
//   - a refusal to compare that is never named, which leaves an operator unable
//     to tell "we compared them and they match" from "we never compared them"
//     (S047-R3.3), and — the same defect one step on — a removal recommendation
//     that covers a package nobody read, which is the screen this story exists
//     to remove;
//   - a newline in a Row.Detail, which corrupts the plain writer's layout and
//     the Markdown pipe table alike (S047-R6.2);
//   - a long explanation put in a detail, where a renderer cuts it, rather than
//     in prose that wraps (S047-R6.1);
//   - a count that moves when --all changes the listing, which makes the number
//     under a table disagree with the table above it (S047-D7).
//
// The sixth is the block list itself, in order, which is the contract every
// renderer consumes.

import (
	"regexp"
	"strings"
	"testing"
)

// compareFixture is one run shaped like the measured 279-package overlay this
// story's target rendering was built from, shrunk to the smallest payload that
// still exercises every branch.
//
// The numbers are deliberately inconsistent with the list lengths — 11 packages
// counted redundant against 3 rows, 258 counted keep against 5 — because that
// is the real shape of a compare run: every scanned package carries a verdict,
// while the lists hold only the packages both trees carry. A fixture whose
// counts happened to equal its list lengths would pass just as well against an
// implementation that read the wrong one of the two.
func compareFixture() CompareRun {
	return CompareRun{
		Repository: "gentoo",
		Scanned:    279,
		InBoth:     180,
		OnlyLocal:  99,
		Redundant: []ComparePkg{
			{
				Package: "kde-plasma/kwin",
				Local:   "6.7.4", Remote: "6.7.4-r2",
				Status: "outdated", Reading: readingNotComparable, Diff: "not compared",
			},
			{
				Package: "dev-lang/go",
				Local:   "1.27.0", Remote: "1.27.0",
				Status: "up-to-date", Reading: readingFailed, Diff: "+24/-0",
				// A raw newline, because the producer's reason is a captured
				// sentence and nothing between it and this table promises it
				// is one line.
				Reason: "differs, and no entry\ndeclares why",
			},
			{
				Package: "sys-apps/fwupd",
				Local:   "2.1.7", Remote: "2.1.7",
				Status: "up-to-date", Reading: readingRead, Diff: "identical",
			},
		},
		Keep: []ComparePkg{
			{Package: "media-plugins/gst-plugins-good", Local: "1.29.2", Remote: "1.26.11", Status: "newer"},
			{Package: "media-libs/gst-plugins-base", Local: "1.29.2", Remote: "1.26.11", Status: "newer"},
			{Package: "dev-python/gst-python", Local: "1.29.2", Remote: "1.26.11", Status: "newer"},
			{Package: "app-editors/vim", Local: "9.2.1021", Remote: "9.1.1652-r2", Status: "newer"},
			{
				Package: "media-plugins/gst-plugins-mpeg2dec",
				Local:   "1.28.6", Remote: "1.26.11", Status: "newer",
				Reason: "behind the rest of its own stack, which is at 1.29.2",
			},
		},
		KeepGroups: []KeepGroup{{
			Label: "gstreamer stack",
			Local: "1.29.2", Remote: "1.26.11",
			Members: []string{
				"media-plugins/gst-plugins-good",
				"media-libs/gst-plugins-base",
				"dev-python/gst-python",
			},
			ByCategory: []CategoryCount{
				{Category: "media-plugins", Count: 1},
				{Category: "media-libs", Count: 1},
				// A newline the producer should never send, kept here because
				// the fold is what guarantees the table survives if it does.
				{Category: "dev-\npython", Count: 1},
			},
		}},
		Unknown: []ComparePkg{
			{
				Package: "virtual/dist-kernel",
				Local:   "7.2.2", Remote: "7.1.12", Status: "newer",
				Reason: "nothing on record describes this package",
			},
		},
		Verdicts: VerdictTally{Keep: 258, Redundant: 11, NeedsRebase: 0, Unknown: 10},
		Unread:   11,
	}
}

// compareTitles is the six titles, in the order Sections returned them.
func compareTitles(blocks []Section) []string {
	titles := make([]string, 0, len(blocks))
	for _, b := range blocks {
		titles = append(titles, b.Title)
	}
	return titles
}

// compareProse is every sentence a section states outside its table: its lead
// and its notes, in one string.
//
// The two are read together on purpose. Both wrap in every renderer, so
// S047-R6.1 is satisfied by either, and a test that demanded one of the two
// would be pinning a layout rather than the requirement.
func compareProse(s Section) string {
	return strings.Join(append(append([]string{}, s.Lead...), s.Notes...), "\n")
}

// compareAllRows is every row of every section, with the section it came from,
// so a failure names where the bad row is rather than only that one exists.
func compareAllRows(blocks []Section) []struct {
	section string
	row     Row
} {
	var out []struct {
		section string
		row     Row
	}
	for _, b := range blocks {
		for _, row := range b.Rows.Rows {
			out = append(out, struct {
				section string
				row     Row
			}{b.Title, row})
		}
	}
	return out
}

// compareDigits is every run of digits in a string, in order.
//
// It is how "the counts did not move" is asserted without pinning the wording
// around them: --all changes the tail of a sentence by design, and a test that
// compared the whole string would fail on the change it is meant to allow.
var compareDigitPattern = regexp.MustCompile(`[0-9]+`)

func compareDigits(text string) []string {
	found := compareDigitPattern.FindAllString(text, -1)
	if found == nil {
		return []string{}
	}
	return found
}

// TestCompareRunSectionsAreTheSixBlocksInOrder pins the block list itself: what
// a renderer receives, and in what order.
//
// It reaches Sections through the Payload interface rather than through the
// concrete type, because satisfying that interface is what sub-task 2.2 adds
// and a call on the struct would compile whether or not it does.
func TestCompareRunSectionsAreTheSixBlocksInOrder(t *testing.T) {
	var payload Payload = compareFixture()

	want := []string{
		"Overlay Comparison",
		"Redundant — ::gentoo ships the version, and nothing cleared the content check",
		"Needs Rebase — ::gentoo moved ahead of changes of ours, which must be re-applied",
		"Keep — the overlay copy is ahead",
		"Unknown — no recommendation",
		"Summary",
	}

	got := compareTitles(payload.Sections(SectionOptions{}))
	if len(got) != len(want) {
		t.Fatalf("Sections returned %d block(s) %q, want %d: %q", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("block %d is %q, want %q", i, got[i], want[i])
		}
	}
}

// TestCompareRunKeepsASectionThatHasNoRow is the zero-rows rule: an answer of
// "none" is a block with a sentence in it, never a missing heading.
//
// The second case is the one that matters most, and it is why the guard in
// every section is on the COUNT rather than on the length of a list. A payload
// that counted two packages as needing a rebase and carries no row for either
// must not print "no package needs a rebase" — that is a report lying about the
// one number it was asked to state.
func TestCompareRunKeepsASectionThatHasNoRow(t *testing.T) {
	t.Run("nothing counted and nothing listed", func(t *testing.T) {
		blocks := compareFixture().Sections(SectionOptions{})

		rebase := blocks[2]
		if len(rebase.Rows.Rows) != 0 {
			t.Fatalf("the fixture has no needs-rebase package, got %d row(s)", len(rebase.Rows.Rows))
		}
		if len(rebase.Lead) == 0 {
			t.Error("a section with no row still has to say so: its Lead is empty")
		}
		if prose := compareProse(rebase); !strings.Contains(prose, "No package needs a rebase") {
			t.Errorf("needs-rebase prose does not state the absence:\n%s", prose)
		}
	})

	t.Run("counted but not listed", func(t *testing.T) {
		run := compareFixture()
		run.Verdicts.NeedsRebase = 2

		rebase := run.Sections(SectionOptions{})[2]
		prose := compareProse(rebase)

		if strings.Contains(prose, "No package needs a rebase") {
			t.Errorf("the run counted 2 needing a rebase and the section claims none:\n%s", prose)
		}
		if !strings.Contains(prose, "2 package(s) are counted as needing a rebase.") {
			t.Errorf("the count the run established is not stated:\n%s", prose)
		}
		if !strings.Contains(prose, "without the list it was taken over") {
			t.Errorf("the missing rows are not accounted for:\n%s", prose)
		}
	})
}

// TestCompareRunEmptyRedundantRecommendsNothing is the NEGATIVE half of the
// removal recommendation, over the one payload the other tests here cannot
// reach: a run with nothing redundant in it at all.
//
// # Where it came from, and why it is here rather than where it was
//
// Story 047, sub-task 5.5 (S047-R8.2). It was
// TestFormatReportVerdict/"no removal is suggested when nothing is redundant"
// in internal/overlay, asserted over FormatReport's text, and sub-task 4.2
// deletes FormatReport. The claim is not a formatting claim — a report that
// recommends deleting packages when it found none to delete is the failure
// S047-R3.3 exists to prevent, and it is the direction an operator acts on
// destructively — so it is rewritten against the renderer that now owns the
// sentence instead of being retired with the printer.
//
// # It is asserted on the SENTENCE and not on a substring
//
// The old assertion was `!strings.Contains(out, "remov")`, and it could not have
// been carried over: the empty section's own lead reads "...so nothing here is
// recommended for removal", which contains it. A test moved by substring would
// have failed here and been dismissed as a prose collision, retiring a real
// guard. So the two things are asserted apart — the section states the absence,
// and none of the four RECOMMENDATION sentences compareRedundantLead can produce
// appears — which is what "recommends nothing" actually means.
func TestCompareRunEmptyRedundantRecommendsNothing(t *testing.T) {
	run := compareFixture()
	run.Redundant = nil
	run.Verdicts.Redundant = 0

	redundant := run.Sections(SectionOptions{})[1]
	prose := compareProse(redundant)

	if len(redundant.Rows.Rows) != 0 {
		t.Fatalf("the fixture was emptied and the section still holds %d row(s)", len(redundant.Rows.Rows))
	}
	if !strings.Contains(prose, "No package is counted as redundant") {
		t.Errorf("the section does not state that nothing is redundant:\n%s", prose)
	}
	// Every arm of the advice, none of which may be reached with no list to
	// take it over. Naming all four is what keeps this test honest when the
	// wording of one of them changes.
	for _, recommendation := range []string{
		"The recommendation to remove covers the whole list.",
		"recommendation to remove covers the",
		"No removal advice follows from either.",
		"No removal advice follows from a package nobody read.",
	} {
		if strings.Contains(prose, recommendation) {
			t.Errorf("a run with nothing redundant reaches the removal advice %q:\n%s", recommendation, prose)
		}
	}
	// The caveat belongs to the rows and must not outlive them: it exists to
	// qualify a list, and printed over no list it warns about nothing.
	if strings.Contains(prose, "A difference is not proof of authorship") {
		t.Errorf("the authorship caveat is printed over an empty section:\n%s", prose)
	}
}

// TestCompareRunRedundantLeadNamesTheRefusedVersionPair is S047-R3.3 and the
// advice that follows from it.
//
// Two properties, and the second is the one an unconditional sentence would
// have broken. A package that was not compared BECAUSE its two versions differ
// says so, rather than leaving the producer's NotVerified ambiguous between its
// four causes — and the removal recommendation then covers exactly the packages
// somebody read, because a recommendation is what must not cover a package the
// evidence does not reach. An operator deleting work of ours on the strength of
// an unread row is the screen story 047 exists to remove.
//
// The negative arms are half the test. A refusal sentence printed
// unconditionally would tell an operator, in a run where everything was read,
// that packages were refused; a recommendation printed unconditionally would
// tell them to delete eleven packages nobody looked at.
func TestCompareRunRedundantLeadNamesTheRefusedVersionPair(t *testing.T) {
	const (
		cause  = "refuses to diff two different versions"
		advice = "recommendation to remove"
	)

	unread := func() CompareRun {
		run := compareFixture()
		// Nothing read: one refused pair, and two reviews that died.
		run.Redundant[2].Reading = readingFailed
		run.Redundant[2].Diff = "+8/-29"
		return run
	}

	allRead := func() CompareRun {
		run := compareFixture()
		for i := range run.Redundant {
			run.Redundant[i].Reading = readingRead
		}
		return run
	}

	t.Run("nothing read: the refusal is named and nothing is recommended", func(t *testing.T) {
		lead := strings.Join(unread().Sections(SectionOptions{})[1].Lead, "\n")

		if !strings.Contains(lead, cause) {
			t.Errorf("the lead does not name the version-pair refusal:\n%s", lead)
		}
		if !strings.Contains(lead, "1 was never compared at all") {
			t.Errorf("the refusal is named without the count it applies to:\n%s", lead)
		}
		if !strings.Contains(lead, "the review died on the other 2") {
			t.Errorf("the other cause of an unread comparison is not named:\n%s", lead)
		}
		if !strings.Contains(lead, "not one of them carries a reading") {
			t.Errorf("the lead does not say the list is unread:\n%s", lead)
		}
		if !strings.Contains(lead, "No removal advice follows from either.") {
			t.Errorf("the lead does not withhold the advice:\n%s", lead)
		}
		if strings.Contains(lead, advice) {
			t.Errorf("nobody read a single one of these and the report recommends removing them:\n%s", lead)
		}
	})

	t.Run("everything read: the recommendation covers the whole list", func(t *testing.T) {
		lead := strings.Join(allRead().Sections(SectionOptions{})[1].Lead, "\n")

		if strings.Contains(lead, cause) {
			t.Errorf("every package was read and the lead still claims a refusal:\n%s", lead)
		}
		if !strings.Contains(lead, "every one of them carries a reading") {
			t.Errorf("the lead does not say the list was read:\n%s", lead)
		}
		if !strings.Contains(lead, "The recommendation to remove covers the whole list.") {
			t.Errorf("the evidence is complete and the advice does not follow:\n%s", lead)
		}
	})

	t.Run("partly read: the recommendation covers what was read and says so", func(t *testing.T) {
		lead := strings.Join(compareFixture().Sections(SectionOptions{})[1].Lead, "\n")

		if !strings.Contains(lead, cause) {
			t.Errorf("the lead does not name the version-pair refusal:\n%s", lead)
		}
		if !strings.Contains(lead, "of which 1 carries a reading") {
			t.Errorf("the lead does not count what was read:\n%s", lead)
		}
		if !strings.Contains(lead, "covers the 1 somebody read, and none of the rest") {
			t.Errorf("the advice covers more than the evidence reaches:\n%s", lead)
		}
	})

	t.Run("the notes keep the caveat and do not repeat the refusal", func(t *testing.T) {
		notes := strings.Join(compareFixture().Sections(SectionOptions{})[1].Notes, "\n")

		if !strings.Contains(notes, "A difference is not proof of authorship") {
			t.Errorf("the caveat against acting on a diff alone is missing:\n%s", notes)
		}
		if strings.Contains(notes, cause) {
			t.Errorf("the refusal is stated twice, in the lead and in the notes:\n%s", notes)
		}
	})
}

// TestCompareRunRowDetailsHoldNoNewline is S047-R6.2 over every row of every
// section, in both listings.
//
// A raw newline in a Row.Detail corrupts the plain writer, which prints the
// detail under its row, and the Markdown writer, which makes it a table cell.
// The fixture carries one in a reason and one in a category name, so this is a
// check on the fold rather than on well-behaved data.
func TestCompareRunRowDetailsHoldNoNewline(t *testing.T) {
	for _, showAll := range []bool{false, true} {
		blocks := compareFixture().Sections(SectionOptions{ShowAll: showAll})

		rows := compareAllRows(blocks)
		if len(rows) == 0 {
			t.Fatalf("ShowAll=%v produced no row at all, so nothing was checked", showAll)
		}

		for _, entry := range rows {
			if strings.ContainsAny(entry.row.Detail, "\n\r") {
				t.Errorf("ShowAll=%v: %q in section %q carries a newline in its detail: %q",
					showAll, entry.row.Cells, entry.section, entry.row.Detail)
			}
		}
	}
}

// TestCompareRunLongExplanationsStayInProse is S047-R6.1: an explanation that
// runs past one line is carried by a Lead or a Notes, which wrap, and never by
// a Row.Detail, which a renderer cuts at the width the device allows.
//
// The two explanations checked are the two this payload has: why some
// comparisons were never read, and why the listed refusals were refused. Both
// are longer than any detail a terminal shows, so a report that carried either
// in a row would print the first half of it and drop the rest — which is the
// failure S047-D7 turns into a placement rule.
func TestCompareRunLongExplanationsStayInProse(t *testing.T) {
	blocks := compareFixture().Sections(SectionOptions{})

	explanations := []struct {
		fragment string
		block    int
	}{
		{"comparison(s) were never read", 0},
		{"refuses to diff two different versions", 1},
	}

	for _, e := range explanations {
		prose := compareProse(blocks[e.block])
		if !strings.Contains(prose, e.fragment) {
			t.Errorf("%q is in no lead and no note of block %q:\n%s", e.fragment, blocks[e.block].Title, prose)
		}

		for _, line := range append(append([]string{}, blocks[e.block].Lead...), blocks[e.block].Notes...) {
			if strings.Contains(line, e.fragment) && len(line) <= 80 {
				t.Errorf("the fixture no longer makes %q a long explanation (%d bytes), so this test proves nothing",
					e.fragment, len(line))
			}
		}

		for _, entry := range compareAllRows(blocks) {
			if strings.Contains(entry.row.Detail, e.fragment) {
				t.Errorf("%q reached a row detail in section %q, where a renderer cuts it: %q",
					e.fragment, entry.section, entry.row.Detail)
			}
		}
	}
}

// TestCompareRunShowAllChangesRowsAndNoCount is the --all rule: the flag
// decides what is LISTED and never what is counted (S047-D7, S044-R8.3).
//
// Both halves are asserted. The listing must actually change — a flag that
// changed nothing would pass a count check trivially — and every number the
// report states must be identical in the two runs, including the numbers in the
// note whose wording the flag deliberately changes.
func TestCompareRunShowAllChangesRowsAndNoCount(t *testing.T) {
	run := compareFixture()
	collapsed := run.Sections(SectionOptions{})
	expanded := run.Sections(SectionOptions{ShowAll: true})

	keepCollapsed, keepExpanded := collapsed[3], expanded[3]

	// One group row plus the two packages no group claimed, against one row per
	// kept package.
	if got, want := len(keepCollapsed.Rows.Rows), 3; got != want {
		t.Errorf("collapsed keep table has %d row(s), want %d", got, want)
	}
	if got, want := len(keepExpanded.Rows.Rows), len(run.Keep); got != want {
		t.Errorf("expanded keep table has %d row(s), want one per kept package (%d)", got, want)
	}

	groupRow := "gstreamer stack (3 packages)"
	if keepCollapsed.Rows.Rows[0].Cells[0] != groupRow {
		t.Errorf("collapsed keep table starts with %q, want the group row %q",
			keepCollapsed.Rows.Rows[0].Cells[0], groupRow)
	}
	for _, row := range keepExpanded.Rows.Rows {
		if row.Cells[0] == groupRow {
			t.Errorf("--all still printed the group row %q instead of its members", groupRow)
		}
	}

	// Every number, in every block, in both directions.
	for i := range collapsed {
		before, after := compareDigits(compareProse(collapsed[i])), compareDigits(compareProse(expanded[i]))
		if strings.Join(before, ",") != strings.Join(after, ",") {
			t.Errorf("block %q states %v without --all and %v with it: a count moved with the listing",
				collapsed[i].Title, before, after)
		}
	}

	// The count in the keep note is the payload's, not the table's: three
	// members are grouped and five packages are kept, whichever listing ran.
	for _, block := range []Section{keepCollapsed, keepExpanded} {
		notes := strings.Join(block.Notes, "\n")
		if !strings.Contains(notes, "3 of the 5 share a version pair") {
			t.Errorf("the keep note does not count the payload's own fields:\n%s", notes)
		}
	}
}

// TestCompareRunTablesNameTheirColumns pins which sections carry a DIFF column.
//
// The difference is what the column MEASURES rather than what it costs. Under a
// removal recommendation the diff is the evidence somebody looked (S047-R3.1),
// and under a rebase it is the delta somebody has to re-apply; under keep and
// unknown it supports no decision, and a column that means nothing on most rows
// teaches a reader to stop looking at it.
//
// The cell itself is the payload's string, printed as carried: the closed
// vocabulary is what keeps "no difference was found" apart from "no comparison
// was made", and this asserts it is not re-worded on the way out (S047-R3.2).
func TestCompareRunTablesNameTheirColumns(t *testing.T) {
	blocks := compareFixture().Sections(SectionOptions{})

	cases := []struct {
		block   int
		headers []string
	}{
		{1, []string{"PACKAGE", "BENTOO", "GENTOO", "STATE", "DIFF"}},
		{3, []string{"PACKAGE", "BENTOO", "GENTOO", "STATE"}},
		{4, []string{"PACKAGE", "BENTOO", "GENTOO", "STATE"}},
	}

	for _, c := range cases {
		got := strings.Join(blocks[c.block].Rows.Headers, " ")
		if want := strings.Join(c.headers, " "); got != want {
			t.Errorf("block %q has columns %q, want %q", blocks[c.block].Title, got, want)
		}
	}

	diffCells := map[string]string{}
	for _, row := range blocks[1].Rows.Rows {
		diffCells[row.Cells[0]] = row.Cells[4]
	}
	for atom, want := range map[string]string{
		"kde-plasma/kwin": "not compared",
		"dev-lang/go":     "+24/-0",
		"sys-apps/fwupd":  "identical",
	} {
		if got := diffCells[atom]; got != want {
			t.Errorf("%s prints diff %q, want the payload's own %q", atom, got, want)
		}
	}
}

// TestCompareRunSummarySeparatesTheRunFromTheView pins all three summary lines
// and, with them, where each number comes from.
//
// It was TestCompareRunSummaryCountsTheRunAndNotTheLists until sub-task 8.3,
// and the rename is the finding. Two of the three lines do count the run and
// not the lists — Scanned, InBoth, OnlyLocal and the four Verdicts are producer
// fields, and an implementation that recomputed any of them from a table would
// print a number that moves when a filter narrows the view. The third line is
// the exception ON PURPOSE: it asks how many scanned packages have no row
// ABOVE, which is a question about the view, so it is Scanned minus the four
// lists (S047-R2.3).
//
// The fixture makes the two candidate answers differ — 99 for OnlyLocal against
// 270 for the subtraction — so this test tells them apart in both directions.
func TestCompareRunSummarySeparatesTheRunFromTheView(t *testing.T) {
	run := compareFixture()
	summary := run.Sections(SectionOptions{})[5]

	want := []string{
		"279 scanned · 180 in both · 99 only here",
		"keep 258 · redundant 11 · needs rebase 0 · unknown 10",
		"Verdicts count every package scanned; 270 of 279 have no row above.",
	}

	if len(summary.Lead) != len(want) {
		t.Fatalf("the summary states %d line(s) %q, want %d: %q", len(summary.Lead), summary.Lead, len(want), want)
	}
	for i := range want {
		if summary.Lead[i] != want[i] {
			t.Errorf("summary line %d is %q, want %q", i, summary.Lead[i], want[i])
		}
	}

	if len(summary.Rows.Rows) != 0 {
		t.Errorf("the summary is prose, not a table: it carries %d row(s)", len(summary.Rows.Rows))
	}
}

// TestCompareRunSummaryScopeIsMeasuredOnTheView is the successor to
// TestComparisonSummaryScope in package main, which sub-task 8.1 kept alive —
// with `func verdictScopeLines` under it — because the payload's summary got
// this wrong and deleting a measured fact silently is the one thing that
// sub-task refused to do (S047-R8.2, S047-R8.1).
//
// The fact it carried, restated on this payload: how many packages have no row
// is measured against what is LISTED, never against OnlyLocal. The two agree on
// an unfiltered run and part company under a filter, because a filter narrows
// the lists while every counter stays the producer's (S047-D7). The measured
// run behind the numbers below is the maintainer's own overlay: 265 scanned,
// 101 only-local, and a --only-redundant run that printed three rows.
//
// The third and fourth subtests are this sub-task's own additions. --all
// changes how many keep ROWS print and must not change this sentence, and a
// malformed payload must not print arithmetic with a minus sign in it
// (S047-R2.3).
func TestCompareRunSummaryScopeIsMeasuredOnTheView(t *testing.T) {
	scopeLine := func(t *testing.T, run CompareRun, opts SectionOptions) string {
		t.Helper()
		lead := run.Sections(opts)[5].Lead
		if len(lead) != 3 {
			t.Fatalf("the summary states %d line(s) %q, want exactly 3", len(lead), lead)
		}
		return lead[2]
	}

	t.Run("a filter narrows the rows and the sentence follows them, not OnlyLocal", func(t *testing.T) {
		// --only-redundant on the measured run: the three redundant packages keep
		// their rows and every other list is emptied, exactly as
		// filterCompareResults leaves the payload, while Scanned and OnlyLocal are
		// untouched because a filter narrows no counter.
		run := compareFixture()
		run.Scanned, run.OnlyLocal = 265, 101
		run.NeedsRebase, run.Keep, run.KeepGroups, run.Unknown = nil, nil, nil, nil

		line := scopeLine(t, run, SectionOptions{})

		if want := "Verdicts count every package scanned; 262 of 265 have no row above."; line != want {
			t.Errorf("scope line is %q, want %q", line, want)
		}
		if strings.Contains(line, "101") {
			t.Errorf("scope line %q states OnlyLocal (101); under a filter that is not how many packages have no row", line)
		}
	})

	t.Run("a report with no rows at all is entirely unlisted", func(t *testing.T) {
		// --only-outdated on an overlay that is fully up to date: not one table
		// prints, while the summary still reports the whole scan. This is the case
		// the sentence is most needed for, and the one where reading OnlyLocal was
		// furthest out — it would have claimed 101 of 265 rows were on a screen
		// holding none.
		run := compareFixture()
		run.Scanned, run.OnlyLocal = 265, 101
		run.Redundant, run.NeedsRebase, run.Keep, run.KeepGroups, run.Unknown = nil, nil, nil, nil, nil

		line := scopeLine(t, run, SectionOptions{})

		if want := "Verdicts count every package scanned; 265 of 265 have no row above."; line != want {
			t.Errorf("scope line is %q, want %q", line, want)
		}
	})

	t.Run("--all changes the keep rows and changes no number in this sentence", func(t *testing.T) {
		// A collapsed member is above, inside the group row that stands for it, so
		// it HAS a row. The proof that this is not vacuous is the row counts: the
		// flag really does change the listing, and the sentence really does not
		// move with it.
		run := compareFixture()

		collapsed := run.Sections(SectionOptions{})[3].Rows.Rows
		expanded := run.Sections(SectionOptions{ShowAll: true})[3].Rows.Rows
		if len(collapsed) == len(expanded) {
			t.Fatalf("--all lists %d keep row(s) and no --all lists %d: the fixture collapses nothing, so this proves nothing", len(expanded), len(collapsed))
		}

		if got, want := scopeLine(t, run, SectionOptions{ShowAll: true}), scopeLine(t, run, SectionOptions{}); got != want {
			t.Errorf("--all states %q and no --all states %q: the sentence counts the packages the report left out, which no listing flag changes", got, want)
		}
	})

	t.Run("lists longer than the scan floor at zero rather than print a minus", func(t *testing.T) {
		run := compareFixture()
		run.Scanned = 2

		if got, want := scopeLine(t, run, SectionOptions{}), "Verdicts count every package scanned; 0 of 2 have no row above."; got != want {
			t.Errorf("scope line is %q, want %q", got, want)
		}
	})
}

// TestCompareRunRedundantLeadRefusesToGuessTheCause is the other half of
// S047-R3.3, and the half the requirement is easiest to fail while looking
// satisfied.
//
// The producer's NotVerified collapses at least six exits — no overlay path, no
// local version, a differing version pair, a provider that resolves no package
// directory, and either ebuild failing to open — and `func noteContentRefusal`
// writes readingNotComparable on ALL of them. Naming the version pair over that
// whole count states a cause the run never established for five of the six,
// which is the ambiguity S047-R3.3 exists to REMOVE rather than to paper over with a
// confident answer.
//
// Exactly one of the six is provable from the row: `func resolvePackagePaths`
// refuses a differing pair at its second statement, before any exit that could
// stand for another cause. So that one is named and the rest are not.
func TestCompareRunRedundantLeadRefusesToGuessTheCause(t *testing.T) {
	const (
		pairCause = "because the check refuses to diff two different versions"
		unstated  = "and this run did not record why"
	)

	run := compareFixture()
	// dev-lang/go carries the SAME version on both sides, so whatever refused it,
	// it was not the version pair.
	run.Redundant[1].Reading = readingNotComparable
	run.Redundant[1].Diff = "not compared"

	lead := strings.Join(run.Sections(SectionOptions{})[1].Lead, "\n")

	if !strings.Contains(lead, "1 was never compared at all, "+pairCause) {
		t.Errorf("the one refusal the row PROVES is no longer named:\n%s", lead)
	}
	if !strings.Contains(lead, "1 was never compared at all, "+unstated) {
		t.Errorf("a refusal with no provable cause went unreported, so the counts an operator is "+
			"given no longer account for the list:\n%s", lead)
	}
	if strings.Contains(lead, "2 were never compared at all, "+pairCause) {
		t.Errorf("a package whose two versions are IDENTICAL was reported as refused for having two "+
			"different versions. That is S047-R3.3's ambiguity replaced by a wrong answer, which is worse: "+
			"an operator cannot tell it from the case the sentence is true of.\n%s", lead)
	}
}

// TestCompareRunRedundantLeadExcludesProvedOurs is the qemu row of the live run:
// read, listed as redundant on its versions, and proved ours by its own files.
// prune refuses it, so the advice above the table must not cover it.
func TestCompareRunRedundantLeadExcludesProvedOurs(t *testing.T) {
	const whole = "The recommendation to remove covers the whole list."

	run := func(proved ...int) CompareRun {
		r := compareFixture()
		for i := range r.Redundant {
			r.Redundant[i].Reading = readingRead
		}
		for _, i := range proved {
			r.Redundant[i].ProvedOurs = true
		}
		return r
	}

	t.Run("a lone proved-ours row gets no removal advice", func(t *testing.T) {
		r := run(0)
		r.Redundant = r.Redundant[:1]
		lead := strings.Join(r.Sections(SectionOptions{})[1].Lead, "\n")

		if strings.Contains(lead, whole) {
			t.Errorf("the only row is proved ours and the lead advises removing the whole list:\n%s", lead)
		}
		if !strings.Contains(lead, "1 carries changes proved ours") {
			t.Errorf("the lead does not say the row carries changes proved ours:\n%s", lead)
		}
	})

	t.Run("the advice covers the read rows that are not proved ours", func(t *testing.T) {
		lead := strings.Join(run(1).Sections(SectionOptions{})[1].Lead, "\n")

		if strings.Contains(lead, whole) {
			t.Errorf("one of three rows is proved ours and the lead covers the whole list:\n%s", lead)
		}
		if !strings.Contains(lead, "covers the 2 ") {
			t.Errorf("the advice does not cover exactly the two removable rows:\n%s", lead)
		}
	})

	t.Run("no proved-ours row leaves the wording untouched", func(t *testing.T) {
		lead := strings.Join(run().Sections(SectionOptions{})[1].Lead, "\n")

		if !strings.Contains(lead, whole) || strings.Contains(lead, "proved ours") {
			t.Errorf("a list with nothing proved ours lost its existing advice:\n%s", lead)
		}
	})
}
