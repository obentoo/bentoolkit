package overlay

import (
	"strings"
	"testing"
)

// This file exists because a MUTATION SURVIVED sub-task 9.1's frozen fragment,
// and survived the whole package with it.
//
// Deleting the per-package classification block from the rendering —
// `sb.WriteString(joinReportLines(classificationLines(r)))` in
// baselineResultLines, computed and thrown away — left `go test ./internal/overlay/...`
// green. Every test that could have caught it looks somewhere else:
//
//   - TestGoldenNodejsIsNotProposedForWholesaleRealignment's "not quiet about it"
//     half asserts the FIELD, `report.Results[0].Classified`, and passes the
//     rendered report only into its failure message. A field that is filled and
//     never printed satisfies it exactly.
//   - 6.3's TestAnnotateBaselineZeroValueRendersNothing does assert through
//     FormatReport, but the RUN-LEVEL block (runClassificationLines, compare.go
//     :1247) renders from the same Classified fields and is a separate call site
//     from the per-package one (compare.go:1393). Setting Classified still moved
//     the run-level total, so "the rendering changed" stayed true.
//   - 8.1's TestClassificationReachReachesTheRenderedLines calls
//     classificationLines DIRECTLY. It proves the builder says the right thing,
//     never that anybody prints what it says.
//
// WHAT THE SURVIVOR WOULD COST, which is why it is worth a test rather than a
// note: the run-level line says "N differences examined across M of the packages
// reviewed" and names no package. Over an overlay of 321 packages that is a total
// with nothing to attribute it to — the operator would be told that 492
// differences exist and never which ebuild carries them. The frozen test's own
// comment is precisely this ("the divergence must still be REPORTED, by class …
// the report would be quiet about the largest divergence in the overlay"); its
// assertion is the one thing that cannot see it.
//
// It asserts through PRODUCTION'S OWN LINES rather than through copied wording,
// so re-wording the sentences moves both sides together and only DROPPING them
// fails.

// TestNodejsClassificationIsAttributedToThePackageInTheReport is the frozen
// test's stated intent taken one step further: not merely that the classification
// was computed, but that it reaches the operator BESIDE THE PACKAGE IT BELONGS
// TO.
//
// It uses the nodejs fixture deliberately. That is the case the story's
// cautionary anchor is about, and "not proposed for realignment" is only a
// virtue if the divergence is still reported — silence would hide the overlay's
// largest divergence instead of judging it.
//
// _Requirements: R2, R2.3, R2.4, R4.1_
func TestNodejsClassificationIsAttributedToThePackageInTheReport(t *testing.T) {
	overlayRoot, prov, pkg := goldenPair(t, "net-libs", "nodejs", "26.7.0", "26.7.0", goldenNodejsOurs, goldenNodejsBaseline)
	opts := CompareOptions{IncludeSynced: true, IncludeNotInRemote: true, OverlayPath: overlayRoot}

	report, err := CompareWithProvider([]PackageInfo{pkg}, prov, opts)
	if err != nil {
		t.Fatalf("CompareWithProvider returned %v, want nil", err)
	}
	AnnotateBaseline(report, prov, opts)

	if len(report.Results) != 1 {
		t.Fatalf("report holds %d results, want 1", len(report.Results))
	}
	res := report.Results[0]
	atom := res.Category + "/" + res.Package

	lines := classificationLines(res)
	if len(lines) == 0 {
		t.Fatalf("%s carries no classification lines at all; the reduction reached no report and the assertions below would pass by vacuity (R2.4)", atom)
	}

	// The lead line NAMES the package, and that is the property the run-level
	// block cannot supply: its own lead counts every package at once, so a report
	// carrying only that one states a total nobody can attribute.
	if !strings.Contains(lines[0], atom) {
		t.Errorf("the classification's lead line is %q and does not name %s; a count with no package beside it cannot be acted on across an overlay of 321 packages (R2.4)", lines[0], atom)
	}

	rendered := FormatReport(report)
	for _, line := range lines {
		if !strings.Contains(rendered, line) {
			t.Errorf("the classification line %q was built and never printed; the divergence is classified in a field the operator never sees, which is 'not proposed for realignment' achieved by saying nothing:\n--- report ---\n%s", line, rendered)
		}
	}
}
