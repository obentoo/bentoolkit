package main

// What `overlay compare` does with the comparison once it has one.
//
// Story 047, sub-task 4.1 — S047-R1.5, S047-R7.2.
//
// The sub-task's own new behaviour is the WIRING: the command hands its
// finished comparison to `func presentCompareReport` in
// overlay_compare_report.go instead of printing it, and
// `func exitOnSkippedBaseline` in overlay_compare_realign.go still runs LAST,
// after the render and after the export. Everything these cases assert is about
// that seam; nothing here asserts a column, a width or a wording the renderer
// owns.

import (
	"reflect"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/report"
	"github.com/obentoo/bentoolkit/internal/overlay"
)

// comparePresentMarkers are strings only `func (r CompareRun) Sections` in
// internal/common/report/compare_run.go can produce. The old printer wrote
// neither, so finding them in a run's stdout is what proves the command reached
// the report rather than the printer.
//
// They are short and stand alone on their line: a renderer wraps a long
// sentence at the device's width, and an assertion on a wrapped sentence
// measures the terminal instead of the seam.
var comparePresentMarkers = []string{
	"scanned in the Bentoo overlay",
	"Summary",
}

// TestRunCompareEndsInTheReport is the wiring itself: a plain run reaches
// `func presentCompareReport` (S047-R1.5).
func TestRunCompareEndsInTheReport(t *testing.T) {
	realignSetup(t, true, true)
	realignFlags(t, false, false)

	got, code := realignRun(t, nil)

	if code != 0 {
		t.Errorf("exit code is %d, want 0 — nothing on this path may fail the run", code)
	}
	for _, marker := range comparePresentMarkers {
		if !strings.Contains(got, marker) {
			t.Errorf("the run's output does not contain %q, so it did not reach the report.\noutput:\n%s", marker, got)
		}
	}
}

// TestRunCompareExitsOnSkippedBaselineAfterTheReport is the ORDER S047-R7.2
// fixes: render, export, exit.
//
// A return of the exit status executes nothing further in the handler (story
// 058), so an exit taken before the presentation would leave the operator with
// the exit status and none of the comparison they waited on.
// The two assertions together are what states the order — a non-zero code AND a
// report on stdout can only both be true if the exit ran last.
func TestRunCompareExitsOnSkippedBaselineAfterTheReport(t *testing.T) {
	realignSetup(t, true, false) // no profiles/repo_name: the review refuses the tree
	realignFlags(t, true, true)  // --realign --no-review

	got, code := realignRun(t, nil)

	if code != 1 {
		t.Fatalf("exit code is %d, want 1 — a review that located no ::gentoo tree is the command's one non-zero condition.\noutput:\n%s", code, got)
	}
	for _, marker := range comparePresentMarkers {
		if !strings.Contains(got, marker) {
			t.Errorf("the skipped-baseline run exited without presenting the report: %q is missing, so the exit pre-empted the render.\noutput:\n%s", marker, got)
		}
	}
}

// TestCompareRunNotesCarryTheRunLevelFacts covers what crosses as a NOTE and,
// as importantly, what does not (S047-R1.5).
func TestCompareRunNotesCarryTheRunLevelFacts(t *testing.T) {
	const skipped = "no ::gentoo tree was found at /nowhere; nothing was examined"
	const verdict = "realignment verdict — a model's reading, not a finding of this report: justified"

	rep := &overlay.CompareReport{Findings: []overlay.Finding{
		{Kind: overlay.FindingBaselineSkipped, Detail: skipped},
		{Kind: overlay.FindingRealignVerdict, Atom: "dev-lang/go", Detail: verdict},
	}}

	t.Run("the run-scoped baseline finding becomes a note", func(t *testing.T) {
		notes := compareRunNotes(rep, false, false, false)
		if len(notes) != 1 || notes[0] != skipped {
			t.Fatalf("notes are %+v, want the baseline sentence the producer wrote, scoped to the run", notes)
		}
	})

	t.Run("a per-atom verdict is not repeated as a run note", func(t *testing.T) {
		// It already travels as that package's ComparePkg.Reason, mapped by
		// `func comparePackageFindings` in overlay_compare_report.go. Repeating it here
		// would state one package's fact about the whole run.
		for _, note := range compareRunNotes(rep, true, true, false) {
			if strings.Contains(note, "justified") {
				t.Errorf("a per-package realignment verdict reached the run notes: %q", note)
			}
		}
	})

	t.Run("a review that produced no verdict says so, with the reason", func(t *testing.T) {
		notes := compareRunNotes(&overlay.CompareReport{}, true, false, true)
		if len(notes) != 1 {
			t.Fatalf("notes are %+v, want exactly the no-verdict notice", notes)
		}
		if !strings.Contains(notes[0], "no verdict") || !strings.Contains(notes[0], "--no-review") {
			t.Errorf("the notice is %q; it must say that no verdict was produced and why, or silence reads as every divergence judged", notes[0])
		}
	})

	t.Run("the baseline coverage is a share of the packages COMPARED", func(t *testing.T) {
		// Three compared, two rows in the view: the two numbers are equal on a
		// default run, and a denominator taken from the rows would read "1 of
		// the 2" — a report claiming it examined everything it was able to.
		coverage := &overlay.CompareReport{
			ComparedPackages: 3,
			NoBaselineCount:  1,
			Results: []overlay.CompareResult{
				{Category: "dev-libs", Package: "foo"},
				{Category: "dev-libs", Package: "bar"},
			},
		}

		notes := compareRunNotes(coverage, true, true, false)
		if len(notes) != 1 {
			t.Fatalf("notes are %+v, want one run-scoped coverage note", notes)
		}
		if !strings.Contains(notes[0], "1 of the 3 packages compared") {
			t.Errorf("the coverage note reads %q; R6.4 asks for the count as a share of the packages EXAMINED, and a denominator taken from the rows on screen would shrink with every filter", notes[0])
		}

		none := compareRunNotes(&overlay.CompareReport{ComparedPackages: 3}, true, true, false)
		if len(none) != 0 {
			t.Errorf("a run whose every package has a baseline still said %+v; a review that found nothing missing states nothing", none)
		}
	})

	t.Run("a run with nothing to add says nothing", func(t *testing.T) {
		if notes := compareRunNotes(&overlay.CompareReport{}, true, true, false); len(notes) != 0 {
			t.Errorf("notes are %+v, want none", notes)
		}
		if notes := compareRunNotes(nil, true, false, false); notes != nil {
			t.Errorf("a nil report produced %+v, want no note and no crash", notes)
		}
	})
}

// TestCompareRunNotesGoOnTheLastSection pins where a run's own sentences land
// once they are carried in the payload: under the summary, which is the only
// fixed position this report has. `func (r Run) Sections` in
// internal/common/report/run.go PREPENDS an interruption block to an incomplete
// run, so a note filed under the FIRST block would explain one gap with another
// gap's heading.
func TestCompareRunNotesGoOnTheLastSection(t *testing.T) {
	run := buildCompareReport(&overlay.CompareReport{TotalPackages: 1}, "gentoo", nil, "said")

	sections := run.Sections(report.SectionOptions{})
	if len(sections) < 2 {
		t.Fatalf("the report built %d section(s), want the payload's blocks", len(sections))
	}
	for i, section := range sections[:len(sections)-1] {
		for _, note := range section.Notes {
			if note == "said" {
				t.Errorf("section %d (%q) carries the run's note; it belongs under the tally that closes the report", i, section.Title)
			}
		}
	}
	last := sections[len(sections)-1].Notes
	if len(last) != 1 || last[0] != "said" {
		t.Errorf("the last section's notes are %q, want the run's own sentence", last)
	}

	quiet := buildCompareReport(&overlay.CompareReport{TotalPackages: 1}, "gentoo", nil).Sections(report.SectionOptions{})
	if notes := quiet[len(quiet)-1].Notes; len(notes) != 0 {
		t.Errorf("a run with nothing to add said %q", notes)
	}
}

// comparePkgWithFindings is one result and the findings established about it,
// in the order `func EstablishFindings` in internal/overlay/compare.go would
// have appended them.
func comparePkgWithFindings(category, pkg string, details ...string) (overlay.CompareResult, []overlay.Finding) {
	result := overlay.CompareResult{
		Category: category, Package: pkg, LocalVersion: "1.0.0", RemoteVersion: "1.0.0",
		Status: overlay.StatusUpToDate, Verdict: overlay.VerdictKeep, Reading: overlay.ReadingDone,
	}
	findings := make([]overlay.Finding, 0, len(details))
	for i, detail := range details {
		// The comparison's own exception first and the baseline review's axis
		// finding after it, which is the order `func EstablishFindings`
		// produces and the order that decides which one the row keeps.
		kind := overlay.FindingUndeclaredDivergence
		if i > 0 {
			kind = overlay.FindingAxisDivergence
		}
		findings = append(findings, overlay.Finding{
			Kind: kind, Atom: category + "/" + pkg, Detail: detail,
		})
	}
	return result, findings
}

// TestComparePackageFindingsCarryWhatTheRowCannot is S047-R6.1 at the seam: a
// row holds ONE reason, so every further finding a package has travels on that
// package's own entry, and a package with a single finding says nothing twice.
//
// It asserts on the PAYLOAD rather than on a list the command builds for the
// screen, which is the whole of sub-task 7.1: a sentence that exists only
// beside the terminal's sections is a sentence no export can reach.
func TestComparePackageFindingsCarryWhatTheRowCannot(t *testing.T) {
	twoA, findingsA := comparePkgWithFindings("dev-libs", "foo", "undeclared divergence — ours differs", "inherit differs from ::gentoo — gstreamer-meson")
	oneB, findingsB := comparePkgWithFindings("app-editors", "zed", "undeclared divergence — ours differs")
	rep := &overlay.CompareReport{
		TotalPackages: 2, ComparedPackages: 2,
		Results: []overlay.CompareResult{oneB, twoA}, // the producer's order: category, then package
		// Interleaved on purpose: the findings arrive in pass order, and the
		// entries must still carry them in the results' order.
		Findings: []overlay.Finding{findingsB[0], findingsA[0], findingsA[1]},
	}

	t.Run("the first finding stays on the row and the rest travel beside it", func(t *testing.T) {
		payload := compareComparePayload(t, buildCompareReport(rep, "gentoo", nil))
		if len(payload.Keep) != 2 {
			t.Fatalf("the payload holds %d row(s), want 2", len(payload.Keep))
		}
		for _, pkg := range payload.Keep {
			if !strings.Contains(pkg.Reason, "undeclared divergence") {
				t.Errorf("%s carries reason %q, want the first finding established about it", pkg.Package, pkg.Reason)
			}
		}

		byAtom := map[string][]string{}
		for _, pkg := range payload.Keep {
			if pkg.FurtherFindings == nil {
				t.Errorf("%s carries a nil FurtherFindings; null and [] say different things in the exported document", pkg.Package)
			}
			byAtom[pkg.Package] = pkg.FurtherFindings
		}
		if got := byAtom["app-editors/zed"]; len(got) != 0 {
			t.Errorf("app-editors/zed carries %q; its one finding is already on its row", got)
		}
		further := byAtom["dev-libs/foo"]
		if len(further) != 1 {
			t.Fatalf("dev-libs/foo carries %q, want exactly the finding no row could hold", further)
		}
		if !strings.Contains(further[0], "gstreamer-meson") {
			t.Errorf("the finding reads %q, want the one the row had no room for", further[0])
		}
		if strings.Contains(further[0], "undeclared divergence") {
			t.Error("the entry repeats the reason already printed on its row")
		}
		if strings.Contains(further[0], "dev-libs/foo") {
			t.Errorf("the finding names its own package: %q. The block writes the name when it writes the sentence, and a copy here is free to disagree with the row beside it", further[0])
		}
	})

	t.Run("the order follows the results and is stable", func(t *testing.T) {
		extraB := overlay.Finding{Kind: overlay.FindingAxisDivergence, Atom: "app-editors/zed", Detail: "also differs on RDEPEND"}
		ordered := &overlay.CompareReport{
			TotalPackages: 2, ComparedPackages: 2,
			Results:  rep.Results,
			Findings: append(append([]overlay.Finding{}, rep.Findings...), extraB),
		}

		first := compareComparePayload(t, buildCompareReport(ordered, "gentoo", nil)).Keep
		second := compareComparePayload(t, buildCompareReport(ordered, "gentoo", nil)).Keep
		if len(first) != 2 {
			t.Fatalf("the payload holds %d row(s), want 2", len(first))
		}
		if first[0].Package != "app-editors/zed" || first[1].Package != "dev-libs/foo" {
			t.Errorf("the rows are ordered %q then %q; results are sorted by category and package, and two runs over one overlay must read identically",
				first[0].Package, first[1].Package)
		}
		for i := range first {
			if !reflect.DeepEqual(first[i].FurtherFindings, second[i].FurtherFindings) {
				t.Errorf("row %d differs between two builds over one report: %q vs %q", i, first[i].FurtherFindings, second[i].FurtherFindings)
			}
		}
	})

	t.Run("a run-scoped finding is never a package's", func(t *testing.T) {
		scoped := &overlay.CompareReport{
			TotalPackages: 2,
			Results:       rep.Results,
			Findings:      []overlay.Finding{{Kind: overlay.FindingBaselineSkipped, Detail: "no tree"}},
		}
		for _, pkg := range compareComparePayload(t, buildCompareReport(scoped, "gentoo", nil)).Keep {
			if len(pkg.FurtherFindings) != 0 {
				t.Errorf("%s carries %q; no package is named by the empty atom", pkg.Package, pkg.FurtherFindings)
			}
		}
	})
}

// TestCompareSectionsSayAPackagesFindingsUnderItsOwnBlock is placement, which
// used to be a search over every rendered row for the package's atom and is now
// a property of how the blocks are built: each is built from ONE list, so a
// finding cannot land under a table that does not show its package.
func TestCompareSectionsSayAPackagesFindingsUnderItsOwnBlock(t *testing.T) {
	redundant := overlay.CompareResult{
		Category: "dev-lang", Package: "go", LocalVersion: "1.0.0", RemoteVersion: "1.0.0",
		Status: overlay.StatusUpToDate, Verdict: overlay.VerdictRedundant, Reading: overlay.ReadingDone,
	}
	keep, keepFindings := comparePkgWithFindings("dev-libs", "foo", "undeclared divergence — ours differs", "inherit differs from ::gentoo — gstreamer-meson")
	rep := &overlay.CompareReport{
		TotalPackages: 2, ComparedPackages: 2,
		Results: []overlay.CompareResult{redundant, keep},
		Findings: append([]overlay.Finding{
			{Kind: overlay.FindingUndeclaredDivergence, Atom: "dev-lang/go", Detail: "differs, and no entry declares why"},
		}, keepFindings...),
	}

	sections := buildCompareReport(rep, "gentoo", nil, "the run itself").Sections(report.SectionOptions{ShowAll: true})

	var held []string
	for _, section := range sections {
		for _, note := range section.Notes {
			if strings.Contains(note, "gstreamer-meson") {
				held = append(held, section.Title)
			}
			if strings.Contains(note, "the run itself") && !strings.HasPrefix(section.Title, "Summary") {
				t.Errorf("the run's own sentence is under %q", section.Title)
			}
		}
	}
	if len(held) != 1 || !strings.HasPrefix(held[0], "Keep") {
		t.Fatalf("the finding about dev-libs/foo is said under %q, want the Keep block that shows its row", held)
	}

	for _, section := range sections {
		if !strings.HasPrefix(section.Title, "Keep") {
			continue
		}
		if len(section.Notes) == 0 || !strings.Contains(section.Notes[len(section.Notes)-2], "established more about these packages") {
			t.Errorf("the block's notes are %q, want a lead introducing the finding that follows it", section.Notes)
		}
		if got := section.Notes[len(section.Notes)-1]; !strings.HasPrefix(got, "dev-libs/foo: ") {
			t.Errorf("the note reads %q; it must name its package, or a reader cannot tell which row it belongs to", got)
		}
	}
}

// TestBuildCompareReportCountsTheWholeRunNotTheView is S047-R5.2 at the seam
// the filter opens: the LISTS follow what the operator asked to see, the COUNTS
// follow what the run did. A row `--only-patched` removed takes its shortfall
// with it unless the whole run's rows are handed over separately.
func TestBuildCompareReportCountsTheWholeRunNotTheView(t *testing.T) {
	kept := overlay.CompareResult{
		Category: "dev-lang", Package: "go", LocalVersion: "1.0.0", RemoteVersion: "1.0.0",
		Status: overlay.StatusUpToDate, Verdict: overlay.VerdictKeep, Reading: overlay.ReadingDone,
	}
	filteredOut := overlay.CompareResult{
		Category: "dev-libs", Package: "openssl", LocalVersion: "3.0.0", RemoteVersion: "3.0.0",
		Status: overlay.StatusUpToDate, Verdict: overlay.VerdictKeep, Reading: overlay.ReadingFailed,
	}
	whole := []overlay.CompareResult{kept, filteredOut}
	rep := &overlay.CompareReport{
		TotalPackages: 2, ComparedPackages: 2,
		Results: []overlay.CompareResult{kept}, // what the filter left
	}

	run := buildCompareReport(rep, "gentoo", whole)
	payload := compareComparePayload(t, run)

	if len(payload.Keep) != 1 {
		t.Errorf("the Keep list holds %d row(s), want the 1 the operator asked to see", len(payload.Keep))
	}
	if run.NotEvaluated != 1 || run.Complete {
		t.Errorf("NotEvaluated is %d and Complete is %v; the filtered-out row's failed reading is a gap in the RUN, and dropping it would let a filter make a holed run look whole",
			run.NotEvaluated, run.Complete)
	}
	if payload.Unread != 1 {
		t.Errorf("Unread is %d, want 1 — it sits beside counts taken over every package, so it may not shrink with the view", payload.Unread)
	}

	// A nil says the report was never narrowed, so its own rows are the run.
	unnarrowed := buildCompareReport(rep, "gentoo", nil)
	if unnarrowed.NotEvaluated != 0 || !unnarrowed.Complete {
		t.Errorf("an unnarrowed report reports NotEvaluated %d and Complete %v, want 0 and true",
			unnarrowed.NotEvaluated, unnarrowed.Complete)
	}
}

// TestRunCompareSaysWhatARowCannotHold is S047-R6.1 end to end, over the fixture
// whose second finding used to be lost: a package with more than one finding
// keeps one on its row and says the rest in a note that NAMES it.
//
// The comparison is made on whitespace-normalised output because a note is
// wrapped to the device's width, so a fixed substring may be split across lines
// and an index into the raw bytes would be measuring the terminal. Order
// survives wrapping, which is why the assertion is "the eclass is said after the
// package that carries it" rather than "on the same line as".
func TestRunCompareSaysWhatARowCannotHold(t *testing.T) {
	realignSetup(t, true, true)
	realignFlags(t, true, true) // --realign --no-review: no model, findings only

	got, code := realignRun(t, nil)
	if code != 0 {
		t.Fatalf("exit code is %d, want 0", code)
	}

	flat := strings.Join(strings.Fields(got), " ")
	// Spelled out rather than imported: `const compareFindingsLead` is
	// unexported in internal/common/report, and exporting a sentence to save a
	// line here would publish one package's prose as another's API.
	lead := strings.Index(flat, "Beside the reason on each row, the run established more about these packages:")
	if lead < 0 {
		t.Fatalf("the report introduces no package notes.\noutput:\n%s", got)
	}
	named := strings.Index(flat[lead:], "media-libs/gst-plugins-qt6")
	if named < 0 {
		t.Fatalf("no note names the package it is about.\noutput:\n%s", got)
	}
	if !strings.Contains(flat[lead+named:], "gstreamer-meson") {
		t.Errorf("the inherit axis finding is not said under the package that carries it; this is the finding that catches issue #33.\noutput:\n%s", got)
	}
}
