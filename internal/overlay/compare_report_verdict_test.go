package overlay

import (
	"strconv"
	"strings"
	"testing"
)

// verdictReportFixture is one report holding every verdict at once, with two
// packages that share a verdict but NOT a status (app-misc/ours is
// not-in-remote, dev-lang/rust is up-to-date, both Keep). That pair is what
// makes the grouping assertion meaningful: under the old status grouping they
// land in different sections, under verdict grouping they land together.
func verdictReportFixture() *CompareReport {
	return &CompareReport{
		TotalPackages:    6,
		ComparedPackages: 6,
		OutdatedCount:    1,
		NewerCount:       1,
		UpToDateCount:    3,
		NotInRemoteCount: 1,
		Results: []CompareResult{
			{
				Category: "app-editors", Package: "zed",
				LocalVersion: "1.0", RemoteVersion: "2.0", Status: StatusOutdated,
				Verdict: VerdictNeedsRebase, Patched: true,
				PatchedBy: "app-editors/zed@stable", PatchedReason: "keeps our wayland patch",
			},
			{
				Category: "www-client", Package: "firefox",
				LocalVersion: "129.0", RemoteVersion: "129.0", Status: StatusUpToDate,
				Verdict: VerdictRedundant,
			},
			{
				Category: "sys-devel", Package: "gcc",
				LocalVersion: "14.2", RemoteVersion: "14.2", Status: StatusUpToDate,
				Verdict: VerdictRedundant,
			},
			{
				Category: "app-misc", Package: "ours",
				LocalVersion: "0.3", RemoteVersion: "", Status: StatusNotInRemote,
				Verdict: VerdictKeep,
			},
			{
				Category: "dev-lang", Package: "rust",
				LocalVersion: "1.90", RemoteVersion: "1.90", Status: StatusUpToDate,
				Verdict: VerdictKeep, Patched: true,
				PatchedBy: "dev-lang/rust", PatchedReason: "extra USE flag we depend on",
			},
			{
				Category: "sys-devel", Package: "binutils",
				LocalVersion: "2.47", RemoteVersion: "2.46.1", Status: StatusNewer,
				Verdict: VerdictUnknown,
			},
		},
	}
}

// TestFormatReportVerdict pins what the operator actually reads (R3.1, R3.7):
// a Verdict per package next to the Status it does not replace, packages grouped
// by Verdict, an explicit removal recommendation for the redundant ones — and
// every existing summary line still there, still counting by Status.
//
// The two axes answer the two different questions the operator has: *how do the
// versions compare* and *what should I do about it*. Renaming `up-to-date` to
// `redundant` would have been the smaller diff and the larger break, so the
// summary counts below are asserted verbatim as they read today.
//
// _Requirements: R3.1, R3.7_
func TestFormatReportVerdict(t *testing.T) {
	report := verdictReportFixture()
	out := FormatReport(report)
	low := strings.ToLower(out)

	t.Run("the table carries a Verdict column", func(t *testing.T) {
		if !strings.Contains(out, "Verdict") {
			t.Errorf("no Verdict column header in the report.\n--- report ---\n%s", out)
		}
		// The Status column and its four values keep their meaning: the new
		// column is additive.
		if !strings.Contains(out, "Status") {
			t.Errorf("the Status column header disappeared.\n--- report ---\n%s", out)
		}
	})

	t.Run("every package's row states its verdict", func(t *testing.T) {
		wantVerdictWord := map[string]string{
			"zed":      "needs-rebase",
			"firefox":  "redundant",
			"gcc":      "redundant",
			"ours":     "keep",
			"rust":     "keep",
			"binutils": "unknown",
		}
		for pkg, word := range wantVerdictWord {
			// The package's TABLE ROW, not the first line mentioning its name.
			// The two were the same thing until the report's prose grew: a note
			// saying a copy holds "work of ours" is the first line containing
			// "ours", and this assertion then read a sentence and demanded a
			// verdict of it. tableRows matches the first column of a row, so it
			// cannot pick up prose, a finding, or a longer package name.
			rows := tableRows(low, pkg)
			if len(rows) != 1 {
				t.Errorf("the report holds %d table rows for %s, want exactly 1.\n--- report ---\n%s", len(rows), pkg, out)
				continue
			}
			if !strings.Contains(rows[0], word) {
				t.Errorf("row for %s reads %q; it must state the verdict %q", pkg, strings.TrimSpace(rows[0]), word)
			}
		}
	})

	t.Run("packages are grouped by verdict", func(t *testing.T) {
		// The order packages appear in, mapped to their verdict. Equal verdicts
		// must be contiguous — that is what "grouped" means, and it is checked
		// without pinning which group comes first.
		order := []struct {
			pkg     string
			verdict Verdict
		}{
			{"zed", VerdictNeedsRebase},
			{"firefox", VerdictRedundant},
			{"gcc", VerdictRedundant},
			{"ours", VerdictKeep},
			{"rust", VerdictKeep},
			{"binutils", VerdictUnknown},
		}
		positions := make(map[Verdict][]int)
		for _, o := range order {
			// Positioned by the package's ROW, for the reason given above: the
			// first occurrence of a package's NAME can now be a sentence in
			// another section's note, and a span measured from that would report
			// a grouping failure that the table does not have.
			rows := tableRows(low, o.pkg)
			if len(rows) != 1 {
				t.Fatalf("the report holds %d table rows for %s, want exactly 1.\n--- report ---\n%s", len(rows), o.pkg, out)
			}
			positions[o.verdict] = append(positions[o.verdict], strings.Index(low, rows[0]))
		}
		// A group is contiguous when no OTHER verdict's row sits between its
		// first and last row.
		for v, idxs := range positions {
			lo, hi := idxs[0], idxs[0]
			for _, i := range idxs {
				lo, hi = min(lo, i), max(hi, i)
			}
			for other, otherIdxs := range positions {
				if other == v {
					continue
				}
				for _, i := range otherIdxs {
					if i > lo && i < hi {
						t.Errorf("verdict %v is not grouped: a %v row sits inside its span.\n--- report ---\n%s", v, other, out)
					}
				}
			}
		}
	})

	t.Run("a redundant package is named a removal candidate", func(t *testing.T) {
		if !strings.Contains(low, "remov") {
			t.Errorf("the report never says a redundant package can be removed from the overlay.\n--- report ---\n%s", out)
		}
	})

	t.Run("no removal is suggested when nothing is redundant", func(t *testing.T) {
		clean := verdictReportFixture()
		kept := clean.Results[:0]
		for _, r := range clean.Results {
			if r.Verdict != VerdictRedundant {
				kept = append(kept, r)
			}
		}
		clean.Results = kept
		clean.UpToDateCount = 1

		cleanOut := strings.ToLower(FormatReport(clean))
		if strings.Contains(cleanOut, "remov") {
			t.Errorf("the report recommends a removal with no redundant package in it.\n--- report ---\n%s", FormatReport(clean))
		}
	})

	t.Run("every existing summary line survives", func(t *testing.T) {
		// Counted by Status, exactly as today: 1 outdated, 3 up-to-date, 2 other,
		// 6 in total. The verdict grouping must not recompute them.
		for _, want := range []string{"Outdated: 1", "Up-to-date: 3", "Other: 2", "Total: 6"} {
			if !strings.Contains(out, want) {
				t.Errorf("summary line %q missing; the existing counts must be untouched.\n--- report ---\n%s", want, out)
			}
		}
	})
}

// lineContaining is deliberately GONE. It returned the first line of the report
// containing a package's name, which stopped being that package's row the moment
// a section note contained an ordinary English word that is also a fixture's
// package name ("...whether a copy holds work of ours" and app-misc/ours). Both
// callers now use tableRows (compare_redundant_split_test.go), which matches the
// first column of a table row and therefore cannot match prose.

// MERGE FRAGMENT — story 034, sub-task 8.1 (report every count with its denominator).
//
// Target file: internal/overlay/compare_report_verdict_test.go  (APPEND, package overlay)
// That is the compare RENDERER's test file — it already owns the report-shaped
// fixtures and the assertions on what FormatReport prints, which is exactly the
// surface this sub-task changes. Every symbol added here is prefixed
// `classification`, so nothing in it collides with `verdictReportFixture` and
// friends.
//
// Pinned contract:
//
//	func classificationLines(r CompareResult) []string        // per package
//	func runClassificationLines(rep *CompareReport) []string  // per run, with the denominator
//
// Two pure line builders, following the precedent
// overlay_compare_summary_test.go states for the summary: assert on the builder
// rather than on captured log output, because logger binds its io.Writer at
// first use and exposes no setter. Splitting the decision from the emission is
// cheaper than capturing a stream and leaves the emission trivial enough to
// read. FormatReport calls them; the tests below check both the lines and the
// fact that FormatReport shows them.
//
// The numbers in the fixtures — 31, 47, 22, 100 — are chosen so that no count is
// a SUBSTRING of another or of the total. With 3, 7, 2 and 12, an assertion that
// "the ours column does not mention 2" passes on a line reading "of 12" and
// fails on nothing.

const (
	classificationVersionMove  = 31
	classificationOurs         = 47
	classificationUnclassified = 22
	classificationTotal        = classificationVersionMove + classificationOurs + classificationUnclassified
)

// classificationResult is one package's classified difference.
func classificationResult(versionMove, ours, unclassified int, reduced bool) CompareResult {
	return CompareResult{
		Category:      "media-libs",
		Package:       "gst-plugins-qt6",
		LocalVersion:  "1.29.2",
		RemoteVersion: "1.26.11",
		Baseline:      Baseline{Repo: "gentoo", Version: "1.26.11", Path: "/x.ebuild", Distance: 1, Found: true},
		Classified: Classified{
			VersionMove:  versionMove,
			Ours:         ours,
			Unclassified: unclassified,
			Reduced:      reduced,
			Span:         1,
		},
	}
}

// classificationLineWith returns the first line mentioning `keyword`, and fails
// the test when none does — a missing line and an empty line are different
// failures and only one of them is this sub-task's.
func classificationLineWith(t *testing.T, lines []string, keyword string) string {
	t.Helper()
	for _, l := range lines {
		if strings.Contains(strings.ToLower(l), keyword) {
			return l
		}
	}
	t.Fatalf("no line mentions %q; the classification is reported without one of its three classes:\n%s", keyword, strings.Join(lines, "\n"))
	return ""
}

// TestClassificationCountsSumToTheTotal is R2.4 and R2.5 read together: the
// three counts and the total are one arithmetic, and a report that shows two of
// the three lets the third be inferred wrongly.
//
// _Requirements: R2, R2.4, R2.5_
func TestClassificationCountsSumToTheTotal(t *testing.T) {
	res := classificationResult(classificationVersionMove, classificationOurs, classificationUnclassified, true)
	lines := classificationLines(res)

	if len(lines) == 0 {
		t.Fatal("classificationLines produced nothing for a classified result; the per-package counts reach no report (R2.4)")
	}
	joined := strings.Join(lines, "\n")

	for label, want := range map[string]int{
		"version move": classificationVersionMove,
		"ours":         classificationOurs,
		"unclassified": classificationUnclassified,
		"total":        classificationTotal,
	} {
		if !strings.Contains(joined, strconv.Itoa(want)) {
			t.Errorf("the per-package classification never states the %s count (%d):\n%s", label, want, joined)
		}
	}

	sum := res.Classified.VersionMove + res.Classified.Ours + res.Classified.Unclassified
	if sum != classificationTotal {
		t.Fatalf("the fixture itself does not add up (%d != %d); every assertion here would be about the wrong arithmetic", sum, classificationTotal)
	}
}

// TestClassificationUnclassifiedIsInNeitherColumn is R2.3. A difference the
// reduction and the model both declined to attribute is unclassified, and
// pushing it into either bucket is the failure this requirement exists to
// prevent: "ours" would invite a realignment of something nobody read, and
// "version move" would subtract deliberate work as noise.
//
// _Requirements: R2, R2.3, R2.5_
func TestClassificationUnclassifiedIsInNeitherColumn(t *testing.T) {
	lines := classificationLines(classificationResult(classificationVersionMove, classificationOurs, classificationUnclassified, true))

	versionLine := classificationLineWith(t, lines, "version")
	oursLine := classificationLineWith(t, lines, "ours")
	unclassifiedLine := classificationLineWith(t, lines, "unclassified")

	if !strings.Contains(unclassifiedLine, strconv.Itoa(classificationUnclassified)) {
		t.Errorf("the unclassified line is %q, want it to state %d", unclassifiedLine, classificationUnclassified)
	}
	if strings.Contains(versionLine, strconv.Itoa(classificationUnclassified)) {
		t.Errorf("the version-move line is %q and carries the unclassified count %d; a difference nobody attributed must not be counted as version noise (R2.3)", versionLine, classificationUnclassified)
	}
	if strings.Contains(oursLine, strconv.Itoa(classificationUnclassified)) {
		t.Errorf("the ours line is %q and carries the unclassified count %d; a difference nobody attributed must not be counted as ours (R2.3)", oursLine, classificationUnclassified)
	}
}

// TestClassificationWithNoModelReportsEverythingUnclassified: with no reviewer,
// the deterministic reduction is all that ran. Whatever it could not subtract is
// unclassified — not "ours by elimination", which is the tempting shortcut and
// the one that would propose realigning 492 lines of deliberate slotting work.
//
// _Requirements: R2, R2.3, R2.5, R4.4_
func TestClassificationWithNoModelReportsEverythingUnclassified(t *testing.T) {
	// Reduced=false is D3's third preference: no usable third point, so nothing
	// was subtracted and no model spoke.
	res := classificationResult(0, 0, classificationTotal, false)
	lines := classificationLines(res)

	versionLine := classificationLineWith(t, lines, "version")
	oursLine := classificationLineWith(t, lines, "ours")
	unclassifiedLine := classificationLineWith(t, lines, "unclassified")

	if !strings.Contains(unclassifiedLine, strconv.Itoa(classificationTotal)) {
		t.Errorf("the unclassified line is %q, want all %d differences reported there", unclassifiedLine, classificationTotal)
	}
	for _, line := range []string{versionLine, oursLine} {
		if strings.Contains(line, strconv.Itoa(classificationTotal)) {
			t.Errorf("%q claims %d differences although nothing was attributed; with no model and no third point, 'ours by elimination' is a guess wearing a count", line, classificationTotal)
		}
	}
}

// TestRunClassificationStatesItsDenominator is the requirement's whole point. A
// share without its denominator — "63% attributed" — is unreadable: 63% of
// twelve differences in one package and 63% of forty thousand across the overlay
// are different claims, and a classification whose reach is invisible is
// indistinguishable from a guess.
//
// _Requirements: R2, R2.5_
func TestRunClassificationStatesItsDenominator(t *testing.T) {
	report := &CompareReport{
		TotalPackages:    2,
		ComparedPackages: 2,
		Results: []CompareResult{
			classificationResult(classificationVersionMove, classificationOurs, classificationUnclassified, true),
			classificationResult(0, 0, classificationTotal, false),
		},
	}

	lines := runClassificationLines(report)
	if len(lines) == 0 {
		t.Fatal("runClassificationLines produced nothing; the run-level share is the line 8.1 exists to add")
	}
	joined := strings.Join(lines, "\n")

	// The denominator: every difference the run examined, across both packages.
	wantExamined := 2 * classificationTotal
	if !strings.Contains(joined, strconv.Itoa(wantExamined)) {
		t.Errorf("the run-level lines never state how many differences were EXAMINED (%d):\n%s\na share without its denominator is the failure R2.5 exists to prevent", wantExamined, joined)
	}
	// The numerator that matters: the second package contributed its whole diff
	// to the unclassified column, so the run's unclassified share is large and
	// must be visible rather than averaged away.
	wantUnclassified := classificationUnclassified + classificationTotal
	if !strings.Contains(joined, strconv.Itoa(wantUnclassified)) {
		t.Errorf("the run-level lines never state the unclassified count (%d of %d):\n%s", wantUnclassified, wantExamined, joined)
	}
}

// TestClassificationRendersNothingAtItsZeroValue is R7.2 held at the renderer,
// and it is the reason these lines can be added to a shipped command at all: a
// plain `compare` fills no Classified, so these builders produce nothing and
// FormatReport prints exactly what it printed yesterday.
//
// Task 6.3's TestAnnotateBaselineZeroValueRendersNothing asserts the same
// property from the other end, over the whole rendering. This one is the local
// half: the builder itself is silent, so the silence does not depend on a caller
// remembering to check.
//
// _Requirements: R7.2, R2.5_
func TestClassificationRendersNothingAtItsZeroValue(t *testing.T) {
	if lines := classificationLines(CompareResult{Category: "media-libs", Package: "gst-plugins-qt6"}); len(lines) != 0 {
		t.Errorf("classificationLines produced %q for an unclassified-and-unexamined result; a run without --realign must render exactly what it renders today", lines)
	}
	empty := &CompareReport{TotalPackages: 1, ComparedPackages: 1, Results: []CompareResult{{
		Category: "media-libs", Package: "gst-plugins-qt6",
	}}}
	if lines := runClassificationLines(empty); len(lines) != 0 {
		t.Errorf("runClassificationLines produced %q for a report with no classification; the run-level share is emitted only for a review run", lines)
	}
}

// TestClassificationReachesTheRenderedReport closes the gap the two builders
// leave: a line builder nothing calls is a line nobody sees.
//
// _Requirements: R2, R2.4, R2.5_
func TestClassificationReachesTheRenderedReport(t *testing.T) {
	report := &CompareReport{
		TotalPackages:    1,
		ComparedPackages: 1,
		Results:          []CompareResult{classificationResult(classificationVersionMove, classificationOurs, classificationUnclassified, true)},
	}

	rendered := FormatReport(report)

	for _, want := range []string{strconv.Itoa(classificationVersionMove), strconv.Itoa(classificationOurs), strconv.Itoa(classificationUnclassified)} {
		if !strings.Contains(rendered, want) {
			t.Errorf("FormatReport does not show the count %s; the builders are written and called by nobody:\n%s", want, rendered)
		}
	}
}
