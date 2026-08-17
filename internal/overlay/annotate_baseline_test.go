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
	"os"
	"path/filepath"
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

// MERGE FRAGMENT — story 036, sub-task 2.1 (`baselineVersionMove`).
//
// Target file: internal/overlay/annotate_baseline_test.go  (APPEND, at the end
// of the file — after story 034's 6.3 fragment, which ends with
// TestAnnotateBaselineNoBaselineCount). Do NOT repeat the `package overlay`
// clause.
//
// # IMPORT MERGE
//
// This fragment needs "os" AND "path/filepath" added to
// annotate_baseline_test.go's EXISTING import block (today: strings, testing,
// and github.com/obentoo/bentoolkit/internal/common/provider). Merge them in;
// do not open a second block, and do not leave either unused — `os` is used by
// the no-subprocess test alone, `path/filepath` by moveChooserPath. Nothing
// else is added: no provider double is needed here, because the chooser takes
// its two trees as paths.
//
// # Symbols
//
// Added, all prefixed `moveChooser` so nothing can collide with story 034's
// `annotate*` helpers in the same file: `moveChooserEbuildBody`,
// `moveChooserTrees`, `moveChooserPath`, `moveChooserHunkClass`, and the five
// Test functions below — named so the sub-task's validation command,
// `-run 'TestBaselineVersionMove'`, selects every one of them.
//
// Borrowed, never re-declared: `writeVerifyEbuild`
// (compare_verification_test.go), and from production `ResolveBaseline`,
// `ReduceDiff`, `VersionMove`, `Hunk`, `baselineReachProse`, and the class
// constants `hunkVersionMove` / `hunkOurs` (reduce.go). The classes are read
// through those constants rather than through the string literals reduce_test.go
// uses, so a renamed class fails to compile instead of failing silently.
//
// # PINNED CONTRACT (design.md "Components & Interfaces", D3 and D4)
//
//	func baselineVersionMove(gentooTree, overlayPath, atom, ourVersion string, baseline Baseline) *VersionMove
//
//	// Preference 1: a ::gentoo version above the baseline -> {From: baseline, To: newer}
//	// Preference 2: our own previous version              -> {From: our_prev, To: ours}
//	// Neither: nil
//
// `internal/overlay/reduce.go` is UNCHANGED, in full. Every assertion below is
// written to hold against `ReduceDiff` exactly as it ships; if one of them seems
// to need an edit there, the assertion is wrong and this fragment is where to
// say so — not reduce.go.
//
// # Why the classification is asserted through the OUTCOME
//
// A test that counted hunks would pass on a classifier that got both answers
// backwards. So the fixture separates the two causes by CONTENT and the
// assertions name them: the region our previous version still shared with the
// baseline, changed by our own bump, must come back `version-move`; the
// divergence that predates the bump must stay `ours`. That is design D3's probe,
// reproduced as a fixture rather than described.
//
// # A consequence of D4 this fragment deliberately does NOT pin
//
// Under a corrected (nearest-version) baseline, a ::gentoo version above the
// baseline is necessarily above OURS, so preference 1's span — baseline to that
// version — is always wider than the baseline is from us, and `reduceSpan`
// refuses it. TestBaselineVersionMoveWiderThanTheBaselineIsStillRefused pins
// that refusal with the overlay's single measured preference-1 instance
// (sys-kernel/linux-firmware, story.md M-3), and the fixture there carries NO
// previous version of ours — so whether an implementation should fall back to
// preference 2 after a refusal is left open, as design.md leaves it. Do not read
// the refusal as forbidding such a fallback, and do not add one to make a test
// pass: it would be a design decision, and it belongs in the deviation register.

// moveChooserEbuildBody renders the shared ebuild skeleton with one IUSE line
// and an optional standing divergence of ours.
//
// The two editable regions sit FIVE lines apart on purpose. The reduction
// matches a hunk of ours against a hunk of the move by the text of both sides
// together, so two edits the differ merged into one hunk would be a fixture that
// cannot distinguish the two causes it exists to separate.
func moveChooserEbuildBody(iuse string, standing bool) string {
	body := `EAPI=8

DESCRIPTION="Qt6 plugin for GStreamer"
HOMEPAGE="https://gstreamer.freedesktop.org/"
LICENSE="LGPL-2.1+"
SLOT="0"
IUSE="` + iuse + `"

KEYWORDS="~amd64"
RDEPEND="media-libs/gstreamer:1.0"
DEPEND="${RDEPEND}"
BDEPEND="dev-util/meson"
`
	if standing {
		// Our standing divergence: added by us at our PREVIOUS version and still
		// here. It is absent from the version move by construction, which is why
		// the reduction must leave it as ours.
		body += "BENTOO_STANDING=1\n"
	}
	return body + `
src_configure() {
	meson_src_configure
}
`
}

// moveChooserTrees writes a ::gentoo tree and an overlay tree for one package,
// each carrying the versions given, and returns the two roots.
//
// Neither tree gets a .git, and neither ever will: /var/db/repos/gentoo is a
// shallow clone whose history is absent by construction, so a chooser that
// consulted a log would be right here and wrong on the host. The ::gentoo tree
// carries no profiles/repo_name either, because `ResolveBaseline` recognises a
// package by listing its directory and never asks for the marker —
// LocateBaselineTree is the function that does, and it is not on this path.
func moveChooserTrees(t *testing.T, category, pkg string, gentoo, overlay map[string]string) (gentooRoot, overlayRoot string) {
	t.Helper()
	gentooRoot, overlayRoot = t.TempDir(), t.TempDir()
	for version, body := range gentoo {
		writeVerifyEbuild(t, gentooRoot, category, pkg, version, body)
	}
	for version, body := range overlay {
		writeVerifyEbuild(t, overlayRoot, category, pkg, version, body)
	}
	return gentooRoot, overlayRoot
}

// moveChooserPath names the ebuild a tree carries at one version, in the shape
// the chooser is expected to hand back: the file itself, because ReduceDiff
// reads the version out of the filename on all four of its paths.
func moveChooserPath(root, category, pkg, version string) string {
	return filepath.Join(root, category, pkg, pkg+"-"+version+".ebuild")
}

// moveChooserHunkClass is the class of the ONE hunk whose text carries needle.
//
// It fails the test when several hunks carry it, because the fixture's whole
// purpose is that each cause appears in exactly one difference: two matches mean
// the differ merged or split an edit and the assertions below would then be
// measuring the differ rather than the reduction.
func moveChooserHunkClass(t *testing.T, hunks []Hunk, needle string) string {
	t.Helper()
	class, matches := "", 0
	for _, hunk := range hunks {
		if strings.Contains(hunk.Text, needle) {
			matches++
			class = hunk.Class
		}
	}
	if matches != 1 {
		t.Fatalf("%d hunks carry %q, want exactly 1 — the fixture no longer separates our bump from our standing divergence, so nothing below would be measuring the classification: %+v", matches, needle, hunks)
	}
	return class
}

// TestBaselineVersionMoveAttributesOurBumpAndLeavesOurDivergence is design D3's
// probe, as a test: the third point this overlay actually has is our own
// previous version, and the shipped `ReduceDiff` already classifies it
// correctly.
//
// The fixture is the measured shape. ::gentoo carries 1.28.0. We carry 1.28.0
// too — the version at which we were still in step with it, plus one standing
// divergence of our own — and 1.29.2, our bump. So:
//
//   - the IUSE line changed in OUR bump, on text that still agreed with the
//     baseline, so the move's hunk renders as `-baseline +ours`, matches by
//     text, and is version noise;
//   - BENTOO_STANDING predates the bump, is absent from the move entirely, and
//     is the deliberate work the reduction exists to leave behind.
//
// Both answers are asserted by NAME rather than by count, because a classifier
// that returned them the other way round would satisfy any counting assertion
// and would propose realigning our own decision as noise.
//
// _Requirements: R2, R2.1, R3, R3.1_
func TestBaselineVersionMoveAttributesOurBumpAndLeavesOurDivergence(t *testing.T) {
	const category, pkg = "media-libs", "gst-plugins-qt6"
	atom := category + "/" + pkg

	gentooRoot, overlayRoot := moveChooserTrees(t, category, pkg,
		map[string]string{"1.28.0": moveChooserEbuildBody("qml", false)},
		map[string]string{
			"1.28.0": moveChooserEbuildBody("qml", true),
			"1.29.2": moveChooserEbuildBody("qml wayland", true),
		})

	baseline, err := ResolveBaseline(gentooRoot, atom, "1.29.2")
	if err != nil {
		t.Fatalf("ResolveBaseline returned %v, want nil", err)
	}
	if baseline.Version != "1.28.0" {
		t.Fatalf("the baseline is %q, want 1.28.0 — the only version ::gentoo carries here; every assertion below describes a different comparison", baseline.Version)
	}

	move := baselineVersionMove(gentooRoot, overlayRoot, atom, "1.29.2", baseline)

	if move == nil {
		t.Fatal("no third point was chosen although the overlay carries a previous version of the package; that is the third point 86 of the 158 different-version packages have, and it is the whole of what this story's reduction reaches (R3.1)")
	}
	if want := moveChooserPath(overlayRoot, category, pkg, "1.28.0"); move.From != want {
		t.Errorf("the move starts at %q, want %q — preference 2 is diff(our_prev, ours), and a move that started anywhere else would be measuring somebody else's change", move.From, want)
	}
	if want := moveChooserPath(overlayRoot, category, pkg, "1.29.2"); move.To != want {
		t.Errorf("the move ends at %q, want %q — our own bump is what the move has to contain", move.To, want)
	}

	our := moveChooserPath(overlayRoot, category, pkg, "1.29.2")
	got, hunks := ReduceDiff(our, baseline.Path, move)

	if !got.Reduced {
		t.Fatalf("Reduced is false although the third point spans %d release steps against a baseline %d away; reduce.go is unchanged by this story, so a refusal here means the pair offered is not the one D3 names", got.Span, baseline.Distance)
	}
	if want := versionDistance("1.28.0", "1.29.2"); got.Span != want {
		t.Errorf("Span is %d, want %d — the span is the version distance the third point covers, in the same unit as Baseline.Distance, because the width bound compares the two", got.Span, want)
	}
	if class := moveChooserHunkClass(t, hunks, "wayland"); class != hunkVersionMove {
		t.Errorf("the IUSE change is classed %q, want %q — our previous version still agreed with the baseline there, so this difference is the version move showing through and attributing it to us asks the maintainer to justify their own bump (R2.1)", class, hunkVersionMove)
	}
	if class := moveChooserHunkClass(t, hunks, "BENTOO_STANDING"); class != hunkOurs {
		t.Errorf("the standing divergence is classed %q, want %q — it predates the bump and is absent from the move, and subtracting it would delete a deliberate decision from the report, which is the failure the reduction exists to prevent (R2.1)", class, hunkOurs)
	}
	if got.VersionMove != 1 || got.Ours != 1 || got.Unclassified != 0 {
		t.Errorf("the counts are VersionMove=%d Ours=%d Unclassified=%d, want 1, 1 and 0 — two differences, one cause each; anything else means the differ grouped them differently than the fixture assumes", got.VersionMove, got.Ours, got.Unclassified)
	}
}

// TestBaselineVersionMoveWithNoPreviousVersionOffersNothing is R3.3: when
// neither third point exists, none is invented.
//
// The second half is the point. `nil` has to reach the report as the honest
// sentence rather than as silence, and the sentence is the one story 034 already
// prints for every different-version package today — which is exactly the state
// this story is changing the CAUSE of, not the wording of.
//
// _Requirements: R2, R2.2, R3, R3.3_
func TestBaselineVersionMoveWithNoPreviousVersionOffersNothing(t *testing.T) {
	const category, pkg = "media-libs", "gst-plugins-qt6"
	atom := category + "/" + pkg

	gentooRoot, overlayRoot := moveChooserTrees(t, category, pkg,
		map[string]string{"1.28.0": moveChooserEbuildBody("qml", false)},
		map[string]string{"1.29.2": moveChooserEbuildBody("qml wayland", true)})

	baseline, err := ResolveBaseline(gentooRoot, atom, "1.29.2")
	if err != nil {
		t.Fatalf("ResolveBaseline returned %v, want nil", err)
	}

	move := baselineVersionMove(gentooRoot, overlayRoot, atom, "1.29.2", baseline)

	if move != nil {
		t.Fatalf("a third point %+v was offered although ::gentoo carries only the baseline and the overlay carries only our version; inventing one is how a reduction comes to claim a subtraction nothing supported (R3.3)", *move)
	}

	our := moveChooserPath(overlayRoot, category, pkg, "1.29.2")
	got, hunks := ReduceDiff(our, baseline.Path, move)

	if len(hunks) == 0 {
		t.Fatal("no hunks from two ebuilds that differ; the fixture is broken, not the reduction")
	}
	if got.Reduced || got.Span != 0 {
		t.Errorf("Reduced=%v Span=%d with no third point, want false and 0 — those two are read as a pair, and false with a span is `one was offered and refused for being too wide`, which is a different fact about this package", got.Reduced, got.Span)
	}
	if prose := baselineReachProse(got); !strings.Contains(prose, "no third point") {
		t.Errorf("the reach reads %q and does not say that no third point was available; R2.2 requires an unreduced classification to say so whatever was offered", prose)
	}
}

// TestBaselineVersionMovePrefersTheGentooMoveOverOurOwn is D4: when both third
// points exist, preference 1 wins — a move written in ::gentoo's own hand rather
// than ours, which is better provenance for the same subtraction.
//
// The fixture is the shape where preference 1 is coherent AND usable: ours sits
// BELOW ::gentoo here, so the baseline is the nearest version above us (1.31.0)
// and 1.31.2 is a further ::gentoo release above that. Both versions of the
// choice are present — the overlay also carries our own 1.28.0 — so the row
// genuinely exercises the preference rather than the absence of an alternative.
//
// The fixture is deliberately chosen so that the SHIPPED metric and D1's agree
// on which candidate is the baseline. Group 2 depends on group 1, but a fixture
// that only made sense after group 1 would fail for two different reasons at
// once and neither would be legible.
//
// _Requirements: R3, R3.2_
func TestBaselineVersionMovePrefersTheGentooMoveOverOurOwn(t *testing.T) {
	const category, pkg = "media-libs", "gst-plugins-qt6"
	atom := category + "/" + pkg

	gentooRoot, overlayRoot := moveChooserTrees(t, category, pkg,
		map[string]string{
			"1.31.0": moveChooserEbuildBody("qml", false),
			"1.31.2": moveChooserEbuildBody("qml wayland", false),
		},
		map[string]string{
			"1.28.0": moveChooserEbuildBody("qml", true),
			"1.29.0": moveChooserEbuildBody("qml", true),
		})

	baseline, err := ResolveBaseline(gentooRoot, atom, "1.29.0")
	if err != nil {
		t.Fatalf("ResolveBaseline returned %v, want nil", err)
	}
	if baseline.Version != "1.31.0" {
		t.Fatalf("the baseline is %q, want 1.31.0 — the nearer of the two versions ::gentoo carries; the preference below is meaningless against another baseline", baseline.Version)
	}

	move := baselineVersionMove(gentooRoot, overlayRoot, atom, "1.29.0", baseline)

	if move == nil {
		t.Fatal("no third point was chosen although BOTH exist: ::gentoo carries 1.31.2 above the baseline (R3.2) and the overlay carries our own 1.28.0 (R3.1)")
	}
	if move.From != baseline.Path {
		t.Errorf("the move starts at %q, want the baseline itself (%q) — preference 1 is diff(baseline, a later ::gentoo version), and a hunk of ours can only match a hunk of the move when the two share their before-text", move.From, baseline.Path)
	}
	if want := moveChooserPath(gentooRoot, category, pkg, "1.31.2"); move.To != want {
		t.Errorf("the move ends at %q, want %q — D4 keeps preference 1 first because a move in ::gentoo's own hand has better provenance than one in ours", move.To, want)
	}
	if previous := moveChooserPath(overlayRoot, category, pkg, "1.28.0"); move.From == previous || move.To == previous {
		t.Errorf("the move uses our own previous version (%q) although a ::gentoo version above the baseline exists; preference 1 wins when both do (D4)", previous)
	}
}

// TestBaselineVersionMoveWiderThanTheBaselineIsStillRefused is R3.4: story 034's
// width bound survives this story untouched, and it is asserted through a third
// point the CHOOSER produced rather than one built by hand — which is what makes
// it a statement about the new caller rather than a second copy of
// reduce_test.go's own bound test.
//
// The numbers are the overlay's single measured preference-1 instance
// (story.md M-3): sys-kernel/linux-firmware, ours 20260706, baseline 20260622,
// and ::gentoo also carrying 20260810. The move spans 188 release steps while
// the baseline is 84 away, so the bound refuses it — and that refusal is the
// correct answer, because a move that wide carries enough churn to subsume real
// divergences.
//
// The overlay carries NO previous version here on purpose: this row is about the
// bound, and adding one would silently pin whether a refused preference 1 should
// fall back to preference 2, which design.md does not decide.
//
// _Requirements: R3, R3.4, R2.2_
func TestBaselineVersionMoveWiderThanTheBaselineIsStillRefused(t *testing.T) {
	const category, pkg = "sys-kernel", "linux-firmware"
	atom := category + "/" + pkg

	gentooRoot, overlayRoot := moveChooserTrees(t, category, pkg,
		map[string]string{
			"20260622": moveChooserEbuildBody("qml", false),
			"20260810": moveChooserEbuildBody("qml wayland", false),
		},
		map[string]string{"20260706": moveChooserEbuildBody("qml wayland", true)})

	baseline, err := ResolveBaseline(gentooRoot, atom, "20260706")
	if err != nil {
		t.Fatalf("ResolveBaseline returned %v, want nil", err)
	}
	if baseline.Version != "20260622" {
		t.Fatalf("the baseline is %q, want 20260622 — the nearest version ::gentoo carries to ours", baseline.Version)
	}

	move := baselineVersionMove(gentooRoot, overlayRoot, atom, "20260706", baseline)
	if move == nil {
		t.Fatal("no third point was chosen although ::gentoo carries 20260810 above the baseline; R3.2 makes preference 1 a capability the chooser must still offer, even where the bound will refuse what it offers")
	}

	our := moveChooserPath(overlayRoot, category, pkg, "20260706")
	got, hunks := ReduceDiff(our, baseline.Path, move)

	if got.Reduced {
		t.Error("Reduced is true for a third point spanning further than the baseline is from us; R3.4 keeps story 034's bound, and subtracting less while reporting a reduction would be the same failure with better manners")
	}
	if got.VersionMove != 0 {
		t.Errorf("VersionMove is %d for a refused third point, want 0 — the over-wide move subtracted anyway, which is how a real divergence disappears into `version noise` with nobody watching", got.VersionMove)
	}
	if got.Unclassified != len(hunks) {
		t.Errorf("Unclassified is %d of %d hunks, want all of them — a refused third point leaves the whole diff exactly as if none had been offered", got.Unclassified, len(hunks))
	}
	if got.Span == 0 {
		t.Error("Span is 0 although a third point was offered and measured; the width is what makes the refusal legible rather than a silent shrug (R2.5)")
	}
}

// TestBaselineVersionMoveRunsNoCommandAndReadsNoGitLog holds the constraint
// mechanically instead of by inspection.
//
// PATH is emptied, so `git` — and every other binary on this host, including the
// model CLI at /opt/bin/claude — is unreachable. An implementation that shelled
// out to list versions or to read a history fails here rather than working
// locally and misreading the real /var/db/repos/gentoo, which is a SHALLOW clone
// whose history is absent by construction. Neither fixture tree has a .git for
// it to fall back on, and that is asserted rather than assumed.
//
// _Requirements: R3, R3.1_
func TestBaselineVersionMoveRunsNoCommandAndReadsNoGitLog(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	const category, pkg = "media-libs", "gst-plugins-qt6"
	atom := category + "/" + pkg

	gentooRoot, overlayRoot := moveChooserTrees(t, category, pkg,
		map[string]string{"1.28.0": moveChooserEbuildBody("qml", false)},
		map[string]string{
			"1.28.0": moveChooserEbuildBody("qml", true),
			"1.29.2": moveChooserEbuildBody("qml wayland", true),
		})

	for _, root := range []string{gentooRoot, overlayRoot} {
		if _, err := os.Stat(filepath.Join(root, ".git")); err == nil {
			t.Fatalf("%s grew a .git; the answer must come from the trees' CONTENT, which is all the real shallow clone offers", root)
		}
	}

	baseline, err := ResolveBaseline(gentooRoot, atom, "1.29.2")
	if err != nil {
		t.Fatalf("ResolveBaseline returned %v with PATH empty, want nil", err)
	}

	move := baselineVersionMove(gentooRoot, overlayRoot, atom, "1.29.2", baseline)

	if move == nil {
		t.Fatal("no third point was chosen with PATH empty although the overlay carries our previous version; listing a package directory needs no subprocess")
	}
	if want := moveChooserPath(overlayRoot, category, pkg, "1.28.0"); move.From != want {
		t.Errorf("the move starts at %q, want %q — the same answer the chooser gives with a full PATH", move.From, want)
	}
}

// TestAnnotateBaselinePassesTheChosenMoveToTheReduction closes the one gap
// `stories validate 036` found by mutation: every other test of the third point
// calls baselineVersionMove DIRECTLY, and every pre-existing AnnotateBaseline
// fixture writes a single version per side, so the chooser returns nil in all of
// them. Restoring story 034's `ReduceDiff(ourEbuild, baseline.Path, nil)` — the
// exact defect this story exists to fix — therefore left the WHOLE SUITE green.
//
// The assertion is on the OUTCOME of the wiring, never on the call: a version
// move reaches Classified only if AnnotateBaseline chose one AND handed it to
// ReduceDiff. A test that asserted the chooser had been called would be
// satisfied by a caller that then discarded the answer, which is the shape the
// mutation actually took.
//
// _Requirements: R2, R2.1, R3, R3.1_
func TestAnnotateBaselinePassesTheChosenMoveToTheReduction(t *testing.T) {
	// No model and no subprocess is reachable here, on the same terms as every
	// other fixture in this file.
	t.Setenv("PATH", t.TempDir())

	const category, pkg = "media-libs", "gst-plugins-qt6"
	atom := category + "/" + pkg

	// The baseline and OUR PREVIOUS version agree on IUSE, and ours is the only
	// one carrying `wayland`. So diff(baseline, ours) and diff(our_prev, ours)
	// render the same hunk, the shipped matching rule pairs them, and what our own
	// bump changed comes back version-move (D3). The span is 1.29.0 -> 1.29.2 and
	// the baseline is two minor series back, so the width bound accepts it.
	gentooRoot, overlayRoot := moveChooserTrees(t, category, pkg,
		map[string]string{"1.28.0": moveChooserEbuildBody("qml", false)},
		map[string]string{
			"1.29.0": moveChooserEbuildBody("qml", false),
			"1.29.2": moveChooserEbuildBody("qml wayland", false),
		})

	prov := &localRootedFakeProvider{
		root:     gentooRoot,
		versions: map[string][]string{atom: {"1.28.0"}},
	}
	pkgs := []PackageInfo{{
		Category:      category,
		Package:       pkg,
		Versions:      []string{"1.29.0", "1.29.2"},
		LatestVersion: "1.29.2",
	}}
	opts := annotateReviewOpts(overlayRoot)

	report := annotateCompare(t, pkgs, prov, opts)
	AnnotateBaseline(report, prov, opts)

	if len(report.Results) != 1 {
		t.Fatalf("report holds %d results, want 1", len(report.Results))
	}
	got := report.Results[0]

	if !got.Baseline.Found || got.Baseline.Version != "1.28.0" {
		t.Fatalf("baseline is %+v, want 1.28.0 found — nothing below measures a reduction without one", got.Baseline)
	}

	if got.Classified.VersionMove == 0 {
		t.Errorf("Classified.VersionMove is 0 although the overlay carries 1.29.0 below ours and its IUSE agrees with the baseline: a third point was chosen and never reached ReduceDiff, which is story 034's nil third point exactly (R3.1)")
	}
	if got.Classified.Span == 0 {
		t.Errorf("Classified.Span is 0 — a move from 1.29.0 to 1.29.2 spans real release steps, and a zero span is what a NIL third point reports (R2.2)")
	}
	if !got.Classified.Reduced {
		t.Errorf("Classified.Reduced is false although the third point's span (%d) is inside the distance from the baseline to us (%d) — the bound refused a pair it should accept (R3.4)", got.Classified.Span, got.Baseline.Distance)
	}
}
