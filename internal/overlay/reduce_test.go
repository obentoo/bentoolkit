// MERGE FRAGMENT — story 034, sub-task 4.1 (`ReduceDiff`).
//
// Target file: internal/overlay/reduce_test.go  (NEW file, package overlay)
// Borrowed, never re-declared: `writeVerifyEbuild` (compare_verification_test.go).
//
// PINNED CONTRACT (design.md "Components & Interfaces"):
//
//	type Hunk struct{ Class string; Start, End int; Text string }   // version-move | ours | unclassified
//	type Classified struct{ VersionMove, Ours, Unclassified int; Reduced bool; Span int }
//	type VersionMove struct{ From, To string }
//	func ReduceDiff(our, baseline string, move *VersionMove) (Classified, []Hunk)
//
// All four strings are PATHS TO EBUILD FILES, matching CompareAxes. That is not
// a free choice: `Span` is "the version distance the third point covers" and the
// span bound compares it against the baseline distance, so the callee must be
// able to read all four VERSIONS. From file paths it can; from four blobs of
// content it cannot, and the signature would have needed two more parameters.
//
// # WHY THE SPAN CASE IS THE ONE THAT MATTERS
//
// D3: an unbounded third point recreates the nodejs failure mode INSIDE the
// reduction. A `::gentoo` move spanning several series carries so much churn
// that it subsumes our real divergences and classifies deliberate work as
// version noise — the exact error the story exists to prevent, arriving through
// the subtraction instead of through the model, where nobody is looking for it.
// So a span wider than the baseline distance must subtract NOTHING and say it
// reduced nothing; reporting `Reduced=true` with a smaller subtraction would be
// the same failure with better manners.
package overlay

import (
	"path/filepath"
	"testing"
)

// reduceEbuild is the shared skeleton. Each fixture below changes one or two
// lines of it, so a hunk can be traced back to the edit that produced it.
func reduceEbuild(inherit, api, extra string) string {
	return `EAPI=8
inherit ` + inherit + `

DESCRIPTION="Qt6 plugin for GStreamer"
SLOT="0"
API_VERSION="` + api + `"
` + extra + `
src_configure() {
	meson_src_configure
}
`
}

// reduceWrite writes one ebuild at its real version-bearing filename and returns
// the path. The version in the NAME is load-bearing: it is where the span and
// the baseline distance come from.
func reduceWrite(t *testing.T, version, body string) string {
	t.Helper()
	root := t.TempDir()
	writeVerifyEbuild(t, root, "media-libs", "gst-plugins-qt6", version, body)
	return filepath.Join(root, "media-libs", "gst-plugins-qt6", "gst-plugins-qt6-"+version+".ebuild")
}

// reduceCountByClass counts hunks per class, so the report's arithmetic can be
// checked against the hunks themselves rather than against a second copy of the
// same sum.
func reduceCountByClass(hunks []Hunk, class string) int {
	n := 0
	for _, h := range hunks {
		if h.Class == class {
			n++
		}
	}
	return n
}

// reduceAssertArithmetic is R2.4 and R2.5 in one place: every hunk is counted
// exactly once, the three counts agree with the hunks they describe, and their
// sum is the total diff size. A classification whose reach is invisible is
// indistinguishable from a guess.
func reduceAssertArithmetic(t *testing.T, got Classified, hunks []Hunk) {
	t.Helper()
	if sum := got.VersionMove + got.Ours + got.Unclassified; sum != len(hunks) {
		t.Errorf("the counts sum to %d but there are %d hunks (VersionMove=%d Ours=%d Unclassified=%d); every difference must land in exactly one class and the denominator must be the total (R2.4, R2.5)",
			sum, len(hunks), got.VersionMove, got.Ours, got.Unclassified)
	}
	for _, pair := range []struct {
		class string
		count int
	}{
		{"version-move", got.VersionMove},
		{"ours", got.Ours},
		{"unclassified", got.Unclassified},
	} {
		if n := reduceCountByClass(hunks, pair.class); n != pair.count {
			t.Errorf("%d hunks carry Class %q but the count says %d — the summary and the hunks disagree", n, pair.class, pair.count)
		}
	}
	for i, h := range hunks {
		switch h.Class {
		case "version-move", "ours", "unclassified":
		default:
			t.Errorf("hunk %d has Class %q, want one of version-move | ours | unclassified", i, h.Class)
		}
	}
}

// TestReduceDiffSameVersion is M-C: nodejs-26.7.0 against ::gentoo's 26.7.0 is
// 492 differing lines and every one of them is ours, because there is no version
// noise to subtract in the first place.
//
// _Requirements: R2, R2.1, R2.2, R2.4_
func TestReduceDiffSameVersion(t *testing.T) {
	our := reduceWrite(t, "1.29.2", reduceEbuild("meson python-any-r1", "1.29", "IUSE=\"qml wayland\"\n"))
	baseline := reduceWrite(t, "1.29.2", reduceEbuild("gstreamer-meson", "1.29", "IUSE=\"qml\"\n"))

	got, hunks := ReduceDiff(our, baseline, nil)

	if len(hunks) == 0 {
		t.Fatal("no hunks from two ebuilds that differ; R2.1 compares the full content regardless of version")
	}
	if got.VersionMove != 0 {
		t.Errorf("VersionMove is %d for a same-version comparison, want 0 — both files carry the same version, so no difference between them can be attributable to a version move", got.VersionMove)
	}
	if n := reduceCountByClass(hunks, "version-move"); n != 0 {
		t.Errorf("%d hunks are classed version-move at the same version; this is how deliberate work (nodejs's slotting) gets reverted as noise", n)
	}
	if got.Unclassified != 0 {
		t.Errorf("Unclassified is %d at the same version, want 0 — with no version noise possible, every difference is ours by construction and needs no model to say so", got.Unclassified)
	}
	if got.Ours != len(hunks) {
		t.Errorf("Ours is %d of %d hunks, want all of them", got.Ours, len(hunks))
	}
	if got.Span != 0 {
		t.Errorf("Span is %d with no third point, want 0", got.Span)
	}
	reduceAssertArithmetic(t, got, hunks)
}

// TestReduceDiffAttributesTheVersionMove is D3 preference 1: ::gentoo carries two
// versions spanning ours, their diff is a version move written in ::gentoo's own
// hand, and any hunk of ours that matches a hunk of that move is version noise.
//
// The fixture separates the two causes cleanly. `API_VERSION` changes in
// ::gentoo's own move AND in ours — that is the version move. The `inherit` line
// changes only in ours — that is a decision, and no reduction may absorb it.
//
// _Requirements: R2, R2.1, R2.2, R2.4_
func TestReduceDiffAttributesTheVersionMove(t *testing.T) {
	baseline := reduceWrite(t, "1.28.0", reduceEbuild("gstreamer-meson", "1.28", ""))
	third := reduceWrite(t, "1.29.0", reduceEbuild("gstreamer-meson", "1.29", ""))
	our := reduceWrite(t, "1.29.2", reduceEbuild("meson python-any-r1", "1.29", ""))

	got, hunks := ReduceDiff(our, baseline, &VersionMove{From: baseline, To: third})

	if !got.Reduced {
		t.Fatal("Reduced is false although ::gentoo carries a usable third point; every assertion below describes a reduction that did not happen")
	}
	if got.VersionMove == 0 {
		t.Error("VersionMove is 0 although API_VERSION moved in ::gentoo's own hand between 1.28.0 and 1.29.0 — nothing was subtracted and the model will be asked to judge ::gentoo's version bump")
	}
	if got.Ours == 0 {
		t.Error("Ours is 0 although the inherit line changed only on our side — a reduction that absorbs a packaging decision is the nodejs failure mode arriving through the subtraction (D3)")
	}
	if got.Span == 0 {
		t.Error("Span is 0 for a third point one release wide; the span is reported beside Reduced precisely so a wide subtraction is visible")
	}
	reduceAssertArithmetic(t, got, hunks)
}

// TestReduceDiffWithNoThirdPointClassifiesNothing is D3 preference 3 and R2.3:
// no reduction is possible, so nothing is attributed. Pushing the hunks into
// "ours" would be the more useful-looking answer and the false one — the version
// move is genuinely mixed in and no evidence separates it.
//
// _Requirements: R2, R2.2, R2.3, R2.5_
func TestReduceDiffWithNoThirdPointClassifiesNothing(t *testing.T) {
	baseline := reduceWrite(t, "1.26.11", reduceEbuild("gstreamer-meson", "1.26", ""))
	our := reduceWrite(t, "1.29.2", reduceEbuild("meson python-any-r1", "1.29", ""))

	got, hunks := ReduceDiff(our, baseline, nil)

	if len(hunks) == 0 {
		t.Fatal("no hunks from two ebuilds that differ")
	}
	if got.Reduced {
		t.Error("Reduced is true with no third point supplied; the report must say the classification was unreduced (D3)")
	}
	if got.VersionMove != 0 || got.Ours != 0 {
		t.Errorf("VersionMove=%d Ours=%d with nothing to subtract, want 0 and 0 — an unreduced diff must not be assigned to either cause (R2.3)", got.VersionMove, got.Ours)
	}
	if got.Unclassified != len(hunks) {
		t.Errorf("Unclassified is %d of %d hunks, want all of them", got.Unclassified, len(hunks))
	}
	reduceAssertArithmetic(t, got, hunks)
}

// TestReduceDiffRefusesAThirdPointWiderThanTheBaselineDistance is the bound.
//
// The third point below spans 1.10.0 to 1.29.0 — nineteen minor series — while
// the baseline sits one release from ours. A move that wide carries enough churn
// to match almost anything, so subtracting it would classify deliberate work as
// version noise while reporting a high confidence. The rule is not "subtract
// less"; it is "subtract nothing and say so".
//
// _Requirements: R2, R2.2, R2.5_
func TestReduceDiffRefusesAThirdPointWiderThanTheBaselineDistance(t *testing.T) {
	baseline := reduceWrite(t, "1.29.0", reduceEbuild("gstreamer-meson", "1.29", ""))
	wideFrom := reduceWrite(t, "1.10.0", reduceEbuild("gstreamer-meson", "1.10", ""))
	our := reduceWrite(t, "1.29.2", reduceEbuild("meson python-any-r1", "1.29", ""))

	got, hunks := ReduceDiff(our, baseline, &VersionMove{From: wideFrom, To: baseline})

	if got.Reduced {
		t.Error("Reduced is true for a third point nineteen series wide; a span wider than the baseline distance is reported as unreduced, not subtracted (D3)")
	}
	if got.VersionMove != 0 {
		t.Errorf("VersionMove is %d, want 0 — the over-wide third point subtracted %d hunks anyway, which is how a real divergence disappears into 'version noise' with nobody watching", got.VersionMove, got.VersionMove)
	}
	if got.Unclassified != len(hunks) {
		t.Errorf("Unclassified is %d of %d hunks, want all of them — refusing the third point leaves the whole diff for the model, exactly as if none had been offered", got.Unclassified, len(hunks))
	}
	if got.Span == 0 {
		t.Error("Span is 0 although a third point was offered; the span is what makes the refusal legible in the report rather than a silent shrug")
	}
	reduceAssertArithmetic(t, got, hunks)
}
