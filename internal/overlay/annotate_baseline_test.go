// MERGE FRAGMENT — story 034, sub-task 6.3 (`AnnotateBaseline`, the carrier).
//
// Target file: internal/overlay/annotate_baseline_test.go  (NEW file, package overlay)
// Borrowed from the package's existing test files, never re-declared:
// `localRootedFakeProvider` and `writeVerifyEbuild` (compare_verification_test.go).
//
// PINNED CONTRACT — mirrors AnnotateAuthorship (compare.go:305) exactly, which is
// the precedent design.md says this follows:
//
//	func AnnotateBaseline(report *CompareReport, prov provider.Provider, opts CompareOptions)
//
// on CompareResult:  Baseline Baseline · Axes []AxisFinding · Classified Classified ·
//
//	Declarations []DeclaredDivergence · RealignVerdict string
//
// on CompareReport:  NoBaselineCount int
//
// The ::gentoo tree is reached through `prov`, not through a path parameter: a
// review run is refused unless the provider is a PackageDirProvider on ::gentoo
// (R7.4), so the provider IS the tree and a second way to name it would be a
// second thing to disagree with the first.
//
// # WHAT THIS FILE IS ACTUALLY FOR
//
// D1 promises that `overlay compare` without `--realign` is byte-identical to the
// shipped command. FormatReport takes a *CompareReport and never learns which
// flags were passed (overlay_compare.go:408-413), so that promise has exactly one
// mechanism: every field added here renders NOTHING at its zero value, and the
// pass that fills them runs only for a review run.
//
// A one-directional test — "zero renders nothing" — is satisfied by a field no
// renderer ever reads, which would make the promise true and worthless. So each
// field is asserted in BOTH directions: set, the rendering must change; zeroed
// again, it must return to the golden bytes. Weakening this test means deleting
// one of those two halves, and the failure message says so.
package overlay

import (
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/provider"
)

const (
	// Ours carries a divergence declaration so Declarations has something to be
	// filled with, and it differs from the baseline on the inherit and options
	// axes — the measured gst shape (M-D), reduced to what the carrier needs.
	annotateOursEbuild = `EAPI=8
inherit meson python-any-r1

# BENTOO-DIVERGENCE: INHERIT: gstreamer-meson does not handle the qt6 option list
#   drop-when: gentoo-version >= 1.29

DESCRIPTION="Qt6 plugin for GStreamer"
IUSE="qml wayland"
src_configure() {
	local emesonargs=( -Dqt6=enabled -Dqml=enabled -Dwayland=enabled )
	meson_src_configure
}
`
	annotateBaselineEbuild = `EAPI=8
inherit gstreamer-meson

DESCRIPTION="Qt6 plugin for GStreamer"
IUSE="qml"
src_configure() {
	local emesonargs=( -Dqt6=enabled )
	meson_src_configure
}
`
)

// annotateFixtureTrees writes the overlay side and the ::gentoo side and returns
// both roots plus a provider rooted at the ::gentoo one.
//
// `gentooCarries` decides, per package, whether ::gentoo has a counterpart — the
// only difference between a package with a baseline and one of the 84 without.
func annotateFixtureTrees(t *testing.T, gentooCarries map[string]bool) (overlayRoot string, prov *localRootedFakeProvider, pkgs []PackageInfo) {
	t.Helper()
	overlayRoot, gentooRoot := t.TempDir(), t.TempDir()
	versions := map[string][]string{}

	for atom, carried := range gentooCarries {
		category, pkg, ok := strings.Cut(atom, "/")
		if !ok {
			t.Fatalf("fixture atom %q is not category/package", atom)
		}
		writeVerifyEbuild(t, overlayRoot, category, pkg, "1.29.2", annotateOursEbuild)
		pkgs = append(pkgs, PackageInfo{
			Category:      category,
			Package:       pkg,
			Versions:      []string{"1.29.2"},
			LatestVersion: "1.29.2",
		})
		if carried {
			writeVerifyEbuild(t, gentooRoot, category, pkg, "1.29.2", annotateBaselineEbuild)
			versions[atom] = []string{"1.29.2"}
		}
	}
	return overlayRoot, &localRootedFakeProvider{root: gentooRoot, versions: versions}, pkgs
}

// annotateReviewOpts is the options value a REVIEW run uses. IncludeNotInRemote
// is on here and only here: D1 records that switching it on unconditionally
// would give the 84 Bentoo-only packages rows they do not have today and change
// `counted - len(report.Results)` under everyone.
func annotateReviewOpts(overlayRoot string) CompareOptions {
	return CompareOptions{
		IncludeSynced:      true,
		IncludeNotInRemote: true,
		OverlayPath:        overlayRoot,
	}
}

func annotateCompare(t *testing.T, pkgs []PackageInfo, prov provider.Provider, opts CompareOptions) *CompareReport {
	t.Helper()
	report, err := CompareWithProvider(pkgs, prov, opts)
	if err != nil {
		t.Fatalf("CompareWithProvider returned %v, want nil", err)
	}
	return report
}

// TestAnnotateBaselinePopulatesTheCarrier is the "somewhere to put the answers"
// half: after the pass, every producer in groups 1-5 has a field holding its
// result instead of returning into the air.
//
// RealignVerdict is asserted as a CARRIER here, not as a value: task 5.1 writes
// it from inside the AnnotateReviews pass (one model call per package, D7), and
// no model is reachable from this test. Its zero/non-zero rendering behaviour is
// pinned in TestAnnotateBaselineZeroValueRendersNothing below, which is the
// property 6.3 actually owns.
//
// _Requirements: R1, R1.1, R2, R2.4_
func TestAnnotateBaselinePopulatesTheCarrier(t *testing.T) {
	overlayRoot, prov, pkgs := annotateFixtureTrees(t, map[string]bool{"media-libs/gst-plugins-qt6": true})
	opts := annotateReviewOpts(overlayRoot)
	report := annotateCompare(t, pkgs, prov, opts)

	AnnotateBaseline(report, prov, opts)

	if len(report.Results) != 1 {
		t.Fatalf("report holds %d results, want 1", len(report.Results))
	}
	got := report.Results[0]

	if !got.Baseline.Found {
		t.Fatal("Baseline.Found is false for a package ::gentoo carries — R1.1 requires the baseline to be named, and every field below is meaningless without it")
	}
	if got.Baseline.Version != "1.29.2" {
		t.Errorf("Baseline.Version is %q, want %q — the same version is the baseline when it exists (R1.1)", got.Baseline.Version, "1.29.2")
	}
	if got.Baseline.Distance != 0 {
		t.Errorf("Baseline.Distance is %d, want 0 for a same-version baseline", got.Baseline.Distance)
	}
	if got.Baseline.Path == "" {
		t.Error("Baseline.Path is empty — 'which ebuild did you compare against' is unanswerable without it (R1.1)")
	}
	if len(got.Axes) == 0 {
		t.Error("Axes is empty although the two ebuilds differ on inherit, options and IUSE — the deterministic findings reached no report (D4)")
	}
	if len(got.Declarations) == 0 {
		t.Error("Declarations is empty although the ebuild carries a # BENTOO-DIVERGENCE tag — a declared divergence would be re-litigated every run (R3.1)")
	}
	// A same-version baseline has no version move to subtract, so the reduction
	// has nothing to do and every differing hunk is ours. The assertion is that
	// the field was FILLED, not that a particular arithmetic holds — that is
	// task 4.1's test.
	if got.Classified == (Classified{}) {
		t.Error("Classified is the zero value although the ebuilds differ — the per-class counts and their denominator reached no report (R2.4, R2.5)")
	}
}

// TestAnnotateBaselineZeroValueRendersNothing is the mechanism behind D1's
// byte-identical promise, asserted in both directions per field.
//
// Direction 1 (set → the rendering must CHANGE) forbids a field that no renderer
// reads. Without it, the whole story could ship as dead struct fields and this
// file would still be green.
//
// Direction 2 (zeroed → the rendering must return to the golden BYTES) is R7.2:
// a run that never requested the review leaves the fields zero, and the operator
// sees today's report exactly.
//
// _Requirements: R2, R2.4, R6, R6.4, R7.2_
func TestAnnotateBaselineZeroValueRendersNothing(t *testing.T) {
	overlayRoot, prov, pkgs := annotateFixtureTrees(t, map[string]bool{"media-libs/gst-plugins-qt6": true})
	opts := annotateReviewOpts(overlayRoot)

	golden := FormatReport(annotateCompare(t, pkgs, prov, opts))

	cases := []struct {
		field string
		set   func(*CompareReport)
	}{
		{"Baseline", func(r *CompareReport) {
			r.Results[0].Baseline = Baseline{Repo: "gentoo", Version: "1.26.11", Path: "/x/y.ebuild", Distance: 1, Found: true}
		}},
		{"Axes", func(r *CompareReport) {
			r.Results[0].Axes = []AxisFinding{{Axis: "inherit", Detail: "ours drops gstreamer-meson"}}
		}},
		{"Classified", func(r *CompareReport) {
			r.Results[0].Classified = Classified{VersionMove: 3, Ours: 7, Unclassified: 2, Reduced: true, Span: 1}
		}},
		{"Declarations", func(r *CompareReport) {
			r.Results[0].Declarations = []DeclaredDivergence{{Axis: "inherit", Reason: "the qt6 option list", DropWhen: "gentoo-version >= 1.29"}}
		}},
		{"RealignVerdict", func(r *CompareReport) {
			r.Results[0].RealignVerdict = "no longer justified: ::gentoo's eclass now covers the option list"
		}},
		{"NoBaselineCount", func(r *CompareReport) { r.NoBaselineCount = 4 }},
	}

	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			report := annotateCompare(t, pkgs, prov, opts)

			if before := FormatReport(report); before != golden {
				t.Fatalf("the un-annotated rendering is not reproducible; the comparison itself is non-deterministic:\n%s", before)
			}

			tc.set(report)
			if withValue := FormatReport(report); withValue == golden {
				t.Errorf("%s changed nothing in the rendering when set — the field is written by the review pass and read by no renderer, which makes R7.2's promise true and worthless", tc.field)
			}

			// Back to zero: the exact state a run without --realign leaves.
			zeroed := annotateCompare(t, pkgs, prov, opts)
			if got := FormatReport(zeroed); got != golden {
				t.Errorf("%s at its zero value did not render the shipped bytes.\n got:\n%s\nwant:\n%s", tc.field, got, golden)
			}
		})
	}
}

// TestAnnotateBaselineIsTheOnlyThingTheReviewChanges closes the gap the test
// above cannot see: a pass that filled the new fields correctly AND quietly
// re-sorted Results, or moved a counter, would satisfy every per-field
// assertion. So the pass is run for real and then its five fields (plus the
// report counter) are set back to zero; the rendering must be the golden bytes
// again.
//
// If it is not, the review changed something it does not own, and R7.2's
// byte-identical promise is untrue for reasons no field-by-field test can find.
//
// _Requirements: R7.2_
func TestAnnotateBaselineIsTheOnlyThingTheReviewChanges(t *testing.T) {
	overlayRoot, prov, pkgs := annotateFixtureTrees(t, map[string]bool{
		"media-libs/gst-plugins-qt6": true,
		"sys-devel/binutils":         true,
	})
	opts := annotateReviewOpts(overlayRoot)
	golden := FormatReport(annotateCompare(t, pkgs, prov, opts))

	report := annotateCompare(t, pkgs, prov, opts)
	AnnotateBaseline(report, prov, opts)

	for i := range report.Results {
		report.Results[i].Baseline = Baseline{}
		report.Results[i].Axes = nil
		report.Results[i].Classified = Classified{}
		report.Results[i].Declarations = nil
		report.Results[i].RealignVerdict = ""
	}
	report.NoBaselineCount = 0

	if got := FormatReport(report); got != golden {
		t.Errorf("AnnotateBaseline changed something outside the five carrier fields.\n got:\n%s\nwant:\n%s", got, golden)
	}
}

// TestAnnotateBaselineWithoutTheReviewLeavesEveryFieldZero is R7.2 from the
// other side: no pass, no fields. It is the state EVERY shipped invocation is in
// today and will stay in tomorrow.
//
// _Requirements: R7.2_
func TestAnnotateBaselineWithoutTheReviewLeavesEveryFieldZero(t *testing.T) {
	overlayRoot, prov, pkgs := annotateFixtureTrees(t, map[string]bool{"media-libs/gst-plugins-qt6": true})
	// A NON-review run: IncludeNotInRemote stays off, exactly as the shipped
	// command builds its options today.
	opts := CompareOptions{IncludeSynced: true, OverlayPath: overlayRoot}
	report := annotateCompare(t, pkgs, prov, opts)

	if report.NoBaselineCount != 0 {
		t.Errorf("NoBaselineCount is %d with no review requested, want 0", report.NoBaselineCount)
	}
	for _, r := range report.Results {
		switch {
		case r.Baseline != (Baseline{}):
			t.Errorf("%s/%s: Baseline is %+v with no review requested, want the zero value", r.Category, r.Package, r.Baseline)
		case len(r.Axes) != 0:
			t.Errorf("%s/%s: Axes is %+v with no review requested, want nil", r.Category, r.Package, r.Axes)
		case r.Classified != (Classified{}):
			t.Errorf("%s/%s: Classified is %+v with no review requested, want the zero value", r.Category, r.Package, r.Classified)
		case len(r.Declarations) != 0:
			t.Errorf("%s/%s: Declarations is %+v with no review requested, want nil", r.Category, r.Package, r.Declarations)
		case r.RealignVerdict != "":
			t.Errorf("%s/%s: RealignVerdict is %q with no review requested, want empty", r.Category, r.Package, r.RealignVerdict)
		}
	}
}

// TestAnnotateBaselineNoBaselineCount is R6.4: the count of packages ::gentoo
// does not carry, reported as a number the report can put a denominator under.
//
// It is asserted against the results themselves rather than against the literal
// 1, so the two can never drift: the count IS "how many results have
// Found=false", and any other definition is a second answer to the same
// question.
//
// _Requirements: R6, R6.4, R1.3_
func TestAnnotateBaselineNoBaselineCount(t *testing.T) {
	overlayRoot, prov, pkgs := annotateFixtureTrees(t, map[string]bool{
		"media-libs/gst-plugins-qt6": true,
		"sys-devel/binutils":         true,
		"app-editors/zed":            false, // one of the 84
	})
	opts := annotateReviewOpts(overlayRoot)
	report := annotateCompare(t, pkgs, prov, opts)

	AnnotateBaseline(report, prov, opts)

	want := 0
	for _, r := range report.Results {
		if !r.Baseline.Found {
			want++
		}
	}
	if want == 0 {
		t.Fatal("no result has Found=false, so the count below would pass for the wrong reason — the Bentoo-only fixture package produced no row (IncludeNotInRemote is on for a review run)")
	}
	if report.NoBaselineCount != want {
		t.Errorf("NoBaselineCount is %d, want %d — it must equal the number of results with Baseline.Found=false (R6.4)", report.NoBaselineCount, want)
	}
	if report.NoBaselineCount != 1 {
		t.Errorf("NoBaselineCount is %d, want 1 for this fixture — one of the three packages has no ::gentoo counterpart", report.NoBaselineCount)
	}
}
