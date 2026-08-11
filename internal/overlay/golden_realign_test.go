// MERGE FRAGMENT — story 034, sub-task 9.1 (the two measured cases as fixtures).
//
// Target file: internal/overlay/golden_realign_test.go  (NEW file, package overlay)
// Reused and never re-declared: `localRootedFakeProvider` and
// `writeVerifyEbuild` (compare_verification_test.go).
//
// The fixtures are Go constants rather than files under testdata/. Either is
// fine and the choice is the implementer's; constants keep the case and its
// assertion on one screen, which for two fixtures this small is worth more than
// the convention. If they move to testdata/, move TestGoldenFixturesStayMinimal
// with them — it is what keeps a "let us just use the real ebuild" from landing.
//
// These are the two cases that motivated the story, so they decide whether it
// works:
//
//   - gst-plugins-qt6 (M-D). ::gentoo's 1.26.11 is `inherit gstreamer-meson` and
//     passes 2 build options; ours passes 85, and two of them stopped existing at
//     1.29. The whole of issue #33 is in that pair of lines, and the finding must
//     arrive with NO model in the loop — it is a text comparison and a count, and
//     it must survive `--no-review`.
//
//   - nodejs (M-C). 492 differing lines at the SAME version, 32 mentioning
//     eselect and 100 mentioning slotting. Every one of those lines is ours;
//     there is no version noise to subtract. The failure this story must not
//     have is proposing to revert them wholesale on the recollection that "we
//     only added eselect" — which is off by an order of magnitude.
package overlay

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Fixture 1: the gst pair, reduced to the two lines that carry the finding.
// ---------------------------------------------------------------------------

const goldenGstBaseline = `EAPI=8
inherit gstreamer-meson

DESCRIPTION="Qt6 plugin for GStreamer"
IUSE="qml"

src_configure() {
	local emesonargs=(
		-Dqt6=enabled
		-Dexamples=disabled
	)
	meson_src_configure
}
`

const goldenGstOurs = `EAPI=8
inherit meson python-any-r1 xdg-utils

DESCRIPTION="Qt6 plugin for GStreamer"
IUSE="qml wayland"

src_configure() {
	local emesonargs=(
		-Dqt6=enabled
		-Dexamples=disabled
		-Dqml=enabled
		-Dwayland=enabled
		-Dtests=disabled
	)
	meson_src_configure
}
`

// ---------------------------------------------------------------------------
// Fixture 2: nodejs-shaped. Same version, and every difference is ours —
// slotting and eselect work that is deliberate and documented in the ebuild.
//
// `goldenNodejsBaselineMarker` appears ONLY in ::gentoo's copy. It is the tripwire
// for a wholesale realignment proposal: if the report ever shows our ebuild being
// replaced by the baseline, that line comes with it.
// ---------------------------------------------------------------------------

const goldenNodejsBaselineMarker = `RESTRICT="test"`

const goldenNodejsBaseline = `EAPI=8
inherit python-any-r1

DESCRIPTION="A JavaScript runtime built on V8"
SLOT="0"
` + goldenNodejsBaselineMarker + `

src_install() {
	default
}
`

const goldenNodejsOurs = `EAPI=8
inherit python-any-r1

DESCRIPTION="A JavaScript runtime built on V8"
SLOT="26"

# Slotted so several majors coexist; eselect switches the active one.
src_install() {
	default
	dodir /usr/share/eselect/modules
	insinto /usr/share/eselect/modules
	doins "${FILESDIR}"/nodejs.eselect
}
`

// goldenPair writes one package at one version on both sides and returns the
// overlay root, a provider rooted at the ::gentoo side, and the PackageInfo.
func goldenPair(t *testing.T, category, pkg, ourVersion, theirVersion, ours, theirs string) (string, *localRootedFakeProvider, PackageInfo) {
	t.Helper()
	// No model is reachable from any golden case. Both findings below are text
	// comparisons, and a fixture that quietly needed a provider would be the
	// opposite of what these two cases are pinned for.
	t.Setenv("PATH", t.TempDir())

	overlayRoot, gentooRoot := t.TempDir(), t.TempDir()
	writeVerifyEbuild(t, overlayRoot, category, pkg, ourVersion, ours)
	writeVerifyEbuild(t, gentooRoot, category, pkg, theirVersion, theirs)

	prov := &localRootedFakeProvider{
		root:     gentooRoot,
		versions: map[string][]string{category + "/" + pkg: {theirVersion}},
	}
	return overlayRoot, prov, PackageInfo{
		Category:      category,
		Package:       pkg,
		Versions:      []string{ourVersion},
		LatestVersion: ourVersion,
	}
}

// TestGoldenGstInheritIsFoundWithNoModel is the issue #33 pin, taken at the
// integration rung rather than at CompareAxes' own: the finding has to survive
// the whole path — comparison, annotation, rendering — with nothing wired.
//
// _Requirements: R2, R2.4_
func TestGoldenGstInheritIsFoundWithNoModel(t *testing.T) {
	overlayRoot, prov, pkg := goldenPair(t, "media-libs", "gst-plugins-qt6", "1.29.2", "1.26.11", goldenGstOurs, goldenGstBaseline)
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

	var inherit *AxisFinding
	for i := range res.Axes {
		if res.Axes[i].Axis == "inherit" {
			inherit = &res.Axes[i]
		}
	}
	if inherit == nil {
		t.Fatalf("no inherit finding with no model wired; this is the one finding that would have caught issue #33, and it costs a grep: %+v", res.Axes)
	}
	if !strings.Contains(inherit.Detail, "gstreamer-meson") {
		t.Errorf("the inherit finding is %q and does not name gstreamer-meson — the eclass ::gentoo delegates the option list to is the actionable half of the finding", inherit.Detail)
	}

	rendered := FormatReport(report)
	if !strings.Contains(rendered, "gstreamer-meson") {
		t.Errorf("the finding never reaches the rendered report:\n%s", rendered)
	}
	if res.RealignVerdict != "" {
		t.Errorf("RealignVerdict is %q with no model reachable; a verdict nobody produced is worse than none (R4.4)", res.RealignVerdict)
	}
}

// TestGoldenNodejsSameVersionIsAllOurs is M-C: at the same version there is no
// version noise, so every differing hunk is ours and none of it may be
// attributed to a version move.
//
// The per-marker table is what makes this hard to weaken. A blanket "Ours ==
// total" can be satisfied by a classifier that got the right total for the wrong
// reasons; requiring the hunk carrying `SLOT=` and the hunk carrying `eselect`
// to each be classed "ours" names the specific work that must not be reported as
// noise, and deleting a row from the table is visible in a diff.
//
// _Requirements: R2, R2.1, R2.2, R2.4_
func TestGoldenNodejsSameVersionIsAllOurs(t *testing.T) {
	overlayRoot, prov, pkg := goldenPair(t, "net-libs", "nodejs", "26.7.0", "26.7.0", goldenNodejsOurs, goldenNodejsBaseline)
	ourPath := overlayRoot + "/net-libs/nodejs/nodejs-26.7.0.ebuild"
	theirPath := strings.Replace(ourPath, overlayRoot, prov.root, 1)

	got, hunks := ReduceDiff(ourPath, theirPath, nil)

	if len(hunks) == 0 {
		t.Fatalf("no hunks between two ebuilds that differ; the fixture is not exercising anything (pkg %s/%s)", pkg.Category, pkg.Package)
	}
	if got.VersionMove != 0 {
		t.Errorf("VersionMove is %d at the same version; there is no version move to attribute anything to, and every line so attributed is deliberate work reported as noise", got.VersionMove)
	}
	if got.Unclassified != 0 {
		t.Errorf("Unclassified is %d at the same version; with both sides at 26.7.0 every difference is ours by construction and needs no model to say so", got.Unclassified)
	}
	if got.Ours != len(hunks) {
		t.Errorf("Ours is %d of %d hunks, want all of them", got.Ours, len(hunks))
	}

	markers := []struct {
		name string
		text string
	}{
		{"slotting", "SLOT="},
		{"eselect", "eselect"},
	}
	for _, m := range markers {
		t.Run(m.name+" is classed as ours", func(t *testing.T) {
			found := false
			for _, h := range hunks {
				if !strings.Contains(h.Text, m.text) {
					continue
				}
				found = true
				if h.Class != "ours" {
					t.Errorf("the hunk carrying %q is classed %q, want \"ours\"; this is the 100 lines of slotting and 32 of eselect the story's cautionary anchor is about", m.text, h.Class)
				}
			}
			if !found {
				t.Errorf("no hunk carries %q; the fixture no longer represents the nodejs case and this assertion has stopped meaning anything", m.text)
			}
		})
	}
}

// TestGoldenNodejsIsNotProposedForWholesaleRealignment is the story's cautionary
// anchor turned into an assertion.
//
// The tripwire is `goldenNodejsBaselineMarker`, a line that exists ONLY in
// ::gentoo's copy. If the report ever proposes replacing our ebuild with the
// baseline, that line comes along with the replacement text and this fails. It
// cannot be weakened by rewording a message, because it does not look for one.
//
// The second half matters as much: the divergence must still be REPORTED, by
// class. "Not proposed for realignment" achieved by printing nothing would hide
// the 492 lines instead of judging them, and the report would be quiet about the
// largest divergence in the overlay.
//
// _Requirements: R2, R2.4, R4, R4.1, R4.2_
func TestGoldenNodejsIsNotProposedForWholesaleRealignment(t *testing.T) {
	overlayRoot, prov, pkg := goldenPair(t, "net-libs", "nodejs", "26.7.0", "26.7.0", goldenNodejsOurs, goldenNodejsBaseline)
	opts := CompareOptions{IncludeSynced: true, IncludeNotInRemote: true, OverlayPath: overlayRoot}

	report, err := CompareWithProvider([]PackageInfo{pkg}, prov, opts)
	if err != nil {
		t.Fatalf("CompareWithProvider returned %v, want nil", err)
	}
	AnnotateBaseline(report, prov, opts)

	rendered := FormatReport(report)

	if strings.Contains(rendered, goldenNodejsBaselineMarker) {
		t.Errorf("the report shows ::gentoo's %q, which only appears when the baseline text is offered as a replacement (R4.2) — with no model reachable nothing judged this divergence unjustified, and reverting deliberate slotting work is the failure mode this story must not have:\n%s", goldenNodejsBaselineMarker, rendered)
	}
	if report.Results[0].RealignVerdict != "" {
		t.Errorf("RealignVerdict is %q with no model reachable (R4.4)", report.Results[0].RealignVerdict)
	}

	// And it is not quiet about it: the divergence is reported, by class.
	if report.Results[0].Classified == (Classified{}) {
		t.Errorf("the largest divergence in the overlay is reported with no classification at all; 'not proposed for realignment' must not be achieved by saying nothing:\n%s", rendered)
	}
	if report.Results[0].Classified.Ours == 0 {
		t.Error("the same-version divergence is reported as zero lines of ours; every one of nodejs's 492 differing lines is ours (M-C)")
	}
}

// TestGoldenFixturesStayMinimal is the Validation line "no real ebuild larger
// than a fixture is committed", made mechanical.
//
// The pressure is obvious and reasonable-sounding: the real nodejs ebuild would
// be a more faithful fixture. It would also be 500 lines of someone else's
// copyright in the test tree, and a diff nobody reads when it changes. A fixture
// exists to make one claim legible; these four are each under forty lines
// because that is how much it takes to make theirs.
//
// _Requirements: R2, R2.4_
func TestGoldenFixturesStayMinimal(t *testing.T) {
	const maxLines = 40
	for name, body := range map[string]string{
		"goldenGstBaseline":    goldenGstBaseline,
		"goldenGstOurs":        goldenGstOurs,
		"goldenNodejsBaseline": goldenNodejsBaseline,
		"goldenNodejsOurs":     goldenNodejsOurs,
	} {
		if n := strings.Count(body, "\n"); n > maxLines {
			t.Errorf("%s is %d lines, want at most %d; a real ebuild pasted in whole is not a fixture, it is a copy nobody reads when it changes", name, n, maxLines)
		}
	}
}
