package overlay

import (
	"fmt"
	"strings"
	"testing"
)

// This file pins the MAGNITUDE half of an undeclared-divergence finding: the
// line counts on the warning, and the once-per-section caveat beside them.
//
// The failure it exists to prevent is a reading, not a crash. "Our ebuild
// differs from ::gentoo's" is symmetric, but the sentence lands as an
// accusation — you changed something and did not declare it — and an operator
// who believes it declares `patched` on a package that carries nothing of ours,
// permanently suppressing a removal recommendation that was correct. Measured on
// the live overlay: of eight undeclared divergences, four were a single line
// (PYTHON_COMPAT, revised in place upstream with no revbump) and one was +430
// lines of our own slotting work. The counts are what separate those two at a
// glance; the caveat is what stops the operator concluding authorship from a
// number that cannot carry it.
//
// Nothing here may change a Verdict. The counts describe, exactly as Verified
// does — see TestVerifyAgainstLocalContent, which pins that impotence.

// TestDiffLineCounts covers the shapes a real ebuild diff takes, including the
// one-line substitution that is the whole reason the counts exist.
func TestDiffLineCounts(t *testing.T) {
	tests := []struct {
		name                 string
		theirs, ours         string
		wantAdded, wantRemvd int
	}{
		{
			// The live case: kde-plasma/kwin-6.7.4, PYTHON_COMPAT revised upstream.
			name:      "one-line substitution",
			theirs:    "EAPI=8\nPYTHON_COMPAT=( python3_{12..15} )\ninherit ecm\n",
			ours:      "EAPI=8\nPYTHON_COMPAT=( python3_{11..14} )\ninherit ecm\n",
			wantAdded: 1, wantRemvd: 1,
		},
		{
			// The other live shape: kde-plasma/spectacle, a PATCHES= block added
			// on top of an otherwise untouched ebuild.
			name:      "pure insertion",
			theirs:    "EAPI=8\ninherit ecm\n",
			ours:      "EAPI=8\ninherit ecm\nPATCHES=( \"${FILESDIR}/opencv5.patch\" )\n",
			wantAdded: 1, wantRemvd: 0,
		},
		{
			name:      "pure deletion",
			theirs:    "EAPI=8\nIUSE=\"X\"\ninherit ecm\n",
			ours:      "EAPI=8\ninherit ecm\n",
			wantAdded: 0, wantRemvd: 1,
		},
		{
			// Equal buffers never reach diffLineCounts in production — bytes.Equal
			// short-circuits first — but a function that reported a phantom line
			// here would report phantom lines everywhere.
			name:      "identical",
			theirs:    "EAPI=8\n",
			ours:      "EAPI=8\n",
			wantAdded: 0, wantRemvd: 0,
		},
		{
			// A final line with no newline is still a line. Counting it as zero
			// would under-report exactly the edit an operator is looking at.
			name:      "final line without a trailing newline",
			theirs:    "EAPI=8\nSLOT=\"0\"",
			ours:      "EAPI=8\nSLOT=\"26\"",
			wantAdded: 1, wantRemvd: 1,
		},
		{
			name:      "empty upstream file is all insertion",
			theirs:    "",
			ours:      "EAPI=8\ninherit ecm\n",
			wantAdded: 2, wantRemvd: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			added, removed := diffLineCounts([]byte(tt.theirs), []byte(tt.ours))
			if added != tt.wantAdded || removed != tt.wantRemvd {
				t.Errorf("diffLineCounts = +%d/-%d, want +%d/-%d",
					added, removed, tt.wantAdded, tt.wantRemvd)
			}
		})
	}
}

// TestDiffLineCountsStayBalancedOnLargeDiffs pins what survives past
// lcs.DiffLines' maxDiffs = 100 cutoff, where the edit script stops being
// minimal: not the exact totals — GNU diff legitimately reports smaller ones —
// but the invariant that makes the number usable anyway.
//
// before - removed + added == after. A count that fails this is not a larger
// edit script, it is a wrong one, and the "one line or hundreds?" reading the
// finding exists for would no longer be safe.
func TestDiffLineCountsStayBalancedOnLargeDiffs(t *testing.T) {
	var theirs, ours strings.Builder
	const lines = 400
	for i := range lines {
		fmt.Fprintf(&theirs, "upstream line %d\n", i)
		// Every line differs, so the cutoff is comfortably exceeded.
		fmt.Fprintf(&ours, "ours line %d\n", i*2)
	}

	added, removed := diffLineCounts([]byte(theirs.String()), []byte(ours.String()))
	if lines-removed+added != lines {
		t.Errorf("diffLineCounts = +%d/-%d over %d lines: %d-%d+%d != %d; the edit script does not reconcile",
			added, removed, lines, lines, removed, added, lines)
	}
	// It must still read as "large". The exact value is the algorithm's business.
	if added < lines/2 {
		t.Errorf("diffLineCounts = +%d for %d wholly different lines; a rewrite must not read as a small drift", added, lines)
	}
}

// TestUndeclaredDivergenceCarriesMagnitude runs the whole comparison and asserts
// the counts reach the line the operator actually reads, in the orientation the
// report claims: added is what OUR ebuild carries on top of ::gentoo's.
func TestUndeclaredDivergenceCarriesMagnitude(t *testing.T) {
	pkg := PackageInfo{Category: "app-editors", Package: "zed", LatestVersion: "1.0"}
	silent := map[string]Divergence{"app-editors/zed": {}}

	overlayRoot, upstreamRoot := t.TempDir(), t.TempDir()
	// One line added on our side, nothing removed — asymmetric on purpose, so a
	// swapped argument order fails instead of reading the same either way.
	writeVerifyEbuild(t, overlayRoot, "app-editors", "zed", "1.0", zedEbuildOurs)
	writeVerifyEbuild(t, upstreamRoot, "app-editors", "zed", "1.0", zedEbuildStock)
	prov := &localRootedFakeProvider{root: upstreamRoot, versions: map[string][]string{"app-editors/zed": {"1.0"}}}

	got, out := verifyRun(t, overlayRoot, prov, pkg, silent)

	if got.DiffAdded != 1 || got.DiffRemoved != 0 {
		t.Errorf("DiffAdded/DiffRemoved = +%d/-%d, want +1/-0; ours adds the PATCHES line",
			got.DiffAdded, got.DiffRemoved)
	}
	if !strings.Contains(out, "(+1/-0)") {
		t.Errorf("the finding does not carry the size of the difference, so a one-line drift reads like our work.\n--- report ---\n%s", out)
	}
	if !strings.Contains(out, undeclaredDivergenceCaveat) {
		t.Errorf("the finding claims a divergence without saying the direction is unknown.\n--- report ---\n%s", out)
	}
}

// TestUndeclaredDivergenceMagnitudeIsZeroWhenNothingDiffers pins the other half
// of the contract: the counts are meaningful only under VerifiedDiffers, so a
// package whose bytes match must not carry a magnitude, and the caveat must not
// appear where there is no finding to qualify.
func TestUndeclaredDivergenceMagnitudeIsZeroWhenNothingDiffers(t *testing.T) {
	pkg := PackageInfo{Category: "app-editors", Package: "zed", LatestVersion: "1.0"}
	silent := map[string]Divergence{"app-editors/zed": {}}

	overlayRoot, upstreamRoot := t.TempDir(), t.TempDir()
	writeVerifyEbuild(t, overlayRoot, "app-editors", "zed", "1.0", zedEbuildStock)
	writeVerifyEbuild(t, upstreamRoot, "app-editors", "zed", "1.0", zedEbuildStock)
	prov := &localRootedFakeProvider{root: upstreamRoot, versions: map[string][]string{"app-editors/zed": {"1.0"}}}

	got, out := verifyRun(t, overlayRoot, prov, pkg, silent)

	if got.DiffAdded != 0 || got.DiffRemoved != 0 {
		t.Errorf("DiffAdded/DiffRemoved = +%d/-%d on byte-identical ebuilds, want +0/-0",
			got.DiffAdded, got.DiffRemoved)
	}
	if strings.Contains(out, undeclaredDivergenceCaveat) {
		t.Errorf("the caveat prints where no divergence was found.\n--- report ---\n%s", out)
	}
}

// TestUndeclaredDivergenceCaveatPrintsOncePerSection is the reason the caveat is
// a section footer rather than a clause on every row.
//
// Eight findings meant eight copies of the same sentence in the live run this
// came from, which is the "wallpaper the operator learns to skip" the section
// note already avoids for the removal recommendation. One occurrence, however
// many findings.
func TestUndeclaredDivergenceCaveatPrintsOncePerSection(t *testing.T) {
	silent := map[string]Divergence{
		"app-editors/zed": {},
		"app-editors/vim": {},
	}

	overlayRoot, upstreamRoot := t.TempDir(), t.TempDir()
	for _, name := range []string{"zed", "vim"} {
		writeVerifyEbuild(t, overlayRoot, "app-editors", name, "1.0", zedEbuildOurs)
		writeVerifyEbuild(t, upstreamRoot, "app-editors", name, "1.0", zedEbuildStock)
	}
	prov := &localRootedFakeProvider{root: upstreamRoot, versions: map[string][]string{
		"app-editors/zed": {"1.0"},
		"app-editors/vim": {"1.0"},
	}}

	report, err := CompareWithProvider([]PackageInfo{
		{Category: "app-editors", Package: "zed", LatestVersion: "1.0"},
		{Category: "app-editors", Package: "vim", LatestVersion: "1.0"},
	}, prov, CompareOptions{IncludeSynced: true, OverlayPath: overlayRoot, Divergence: silent})
	if err != nil {
		t.Fatalf("CompareWithProvider returned %v, want nil", err)
	}

	out := FormatReport(report)
	if n := strings.Count(out, "undeclared divergence"); n != 2 {
		t.Fatalf("report holds %d undeclared-divergence findings, want 2", n)
	}
	if n := strings.Count(out, undeclaredDivergenceCaveat); n != 1 {
		t.Errorf("the caveat prints %d times for 2 findings, want 1.\n--- report ---\n%s", n, out)
	}
}
