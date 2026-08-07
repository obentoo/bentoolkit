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

// The AUTHORSHIP half of the same finding line (R2.2, R2.3).
//
// The counts above say how LARGE a difference is. They cannot say whose it is,
// and the caveat beneath the section says so once. What the overlay's own
// content can sometimes say is the subject of everything below: an ebuild that
// references a file ::gentoo does not ship carries something that cannot have
// been inherited from ::gentoo, so the finding states that and names the file —
// one `ls` confirms the claim instead of a re-derivation of what ${FILESDIR}
// expands to.
//
// Measured on the live overlay, three of the eight undeclared divergences are
// proved this way (net-libs/nodejs, kde-plasma/spectacle,
// kde-plasma/kdeplasma-addons) and five are not. The five are NOT "upstream's":
// four are +1/-1 and most likely ::gentoo revising in place, but the report
// cannot know that, and the unproved wording pinned below exists to stop it
// claiming otherwise.

// todaysUnprovedFinding is the unproved line VERBATIM. It is spelled out rather
// than described because "the unproved case keeps today's wording" is only a
// contract if a reword FAILS — and the reword that matters is the tempting one:
// filling the silence with "the change is ::gentoo's". The report cannot tell
// which side moved (R2.3), so the line says that a difference exists and nothing
// declares it, and the caveat beneath the section carries the rest.
const todaysUnprovedFinding = "⚠ kde-plasma/kwin: undeclared divergence (+1/-1) — our 6.7.4 ebuild differs from ::gentoo's, and no entry declares why"

// provingFile is what AnnotateAuthorship records for kde-plasma/spectacle,
// package-relative exactly as it reaches the renderer.
const provingFile = "files/spectacle-opencv5.patch"

// divergenceFinding is one undeclared divergence as formatVerificationFindings
// receives it: the shape comparedResult (authorship_test.go) already produces
// for a compared package, plus the two fields AnnotateAuthorship writes onto it.
//
// Assembled here rather than driven through a real comparison because the
// question is what the RENDERER does with a mixture — a section holding a proved
// and an unproved finding at once — and reaching that through the pipeline would
// need two overlay trees to prove one sentence.
// TestProvedAuthorshipReachesTheRenderedReport below keeps this shape honest by
// making the same assertions on the real pipeline's output.
func divergenceFinding(pkg string, added, removed int, authorship Authorship, provedBy string) CompareResult {
	r := comparedResult("kde-plasma", pkg, "6.7.4", VerifiedDiffers)
	r.DiffAdded, r.DiffRemoved = added, removed
	r.Authorship, r.ProvedBy = authorship, provedBy
	return r
}

// findingLine returns the one rendered line naming atom, so an assertion lands
// on the finding under test rather than anywhere in the section — a section
// holding a proved and an unproved finding would otherwise satisfy both halves
// of a Contains check from the wrong line.
func findingLine(t *testing.T, out, atom string) string {
	t.Helper()
	var found []string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, atom) {
			found = append(found, line)
		}
	}
	if len(found) != 1 {
		t.Fatalf("the section holds %d lines naming %s, want exactly 1.\n--- section ---\n%s", len(found), atom, out)
	}
	return found[0]
}

// TestUndeclaredDivergenceFindingSaysWhatIsProved pins the two renderings the
// one branch became.
//
// The asymmetry is the whole point. A proved finding ASSERTS, with a filename,
// that removing the package discards work of ours; an unproved one asserts
// nothing about authorship at all. Collapsing either into the other loses a
// different thing: the first loses the operator's ability to check the claim,
// the second invents one.
//
// _Requirements: R2.2, R2.3_
func TestUndeclaredDivergenceFindingSaysWhatIsProved(t *testing.T) {
	t.Run("a proved divergence names the file that proves it (R2.2)", func(t *testing.T) {
		out := formatVerificationFindings([]CompareResult{
			divergenceFinding("spectacle", 9, 0, AuthorshipOverlay, provingFile),
		})
		line := findingLine(t, out, "kde-plasma/spectacle")

		if !strings.Contains(line, provingFile) {
			t.Errorf("the proved finding does not name %q, so the operator is told a proof exists and not where to look:\n%s",
				provingFile, line)
		}
		// Rendered EXACTLY as recorded — package-relative, its "files/" prefix
		// neither doubled nor stripped. Both edits send the operator to a path no
		// repository has, and a claim that cannot be checked is one nobody checks.
		if n := strings.Count(line, "files/"); n != 1 {
			t.Errorf("the line spells %q %d times, want 1; the recorded name is already package-relative:\n%s",
				"files/", n, line)
		}
		// It must READ as a proof, not as a filename decorating the same
		// uncertainty: the report has established authorship here, and an operator
		// who cannot tell this line from the unproved one gains nothing from it.
		if !strings.Contains(line, "proved") {
			t.Errorf("the proved finding never says the authorship is proved, so it reads as the unproved case with a name attached:\n%s", line)
		}
		if !strings.Contains(line, "(+9/-0)") {
			t.Errorf("the proved finding lost the size of the difference; the counts belong on BOTH renderings:\n%s", line)
		}
		// Not the old sentence with a name appended. "our ebuild differs from
		// ::gentoo's, and no entry declares why" states an ambiguity that this
		// package no longer has.
		if strings.Contains(line, "differs from ::gentoo's, and no entry declares why") {
			t.Errorf("the proved finding still states today's unproved sentence:\n%s", line)
		}
	})

	t.Run("an unproved divergence keeps today's wording (R2.3)", func(t *testing.T) {
		out := formatVerificationFindings([]CompareResult{
			// The live shape of four of the five unproved packages: one line
			// changed, nothing in the ebuild referencing a file of ours.
			divergenceFinding("kwin", 1, 1, AuthorshipUnproved, ""),
		})

		if !strings.Contains(out, todaysUnprovedFinding) {
			t.Errorf("the unproved finding no longer reads as it does today.\nwant the line: %s\n--- section ---\n%s",
				todaysUnprovedFinding, out)
		}
	})

	t.Run("both renderings keep their counts in one section", func(t *testing.T) {
		out := formatVerificationFindings([]CompareResult{
			divergenceFinding("spectacle", 9, 0, AuthorshipOverlay, provingFile),
			divergenceFinding("kwin", 1, 1, AuthorshipUnproved, ""),
		})

		if !strings.Contains(findingLine(t, out, "kde-plasma/spectacle"), "(+9/-0)") {
			t.Errorf("the proved line lost its counts.\n--- section ---\n%s", out)
		}
		if !strings.Contains(findingLine(t, out, "kde-plasma/kwin"), "(+1/-1)") {
			t.Errorf("the unproved line lost its counts.\n--- section ---\n%s", out)
		}
	})

	t.Run("a proved divergence that IS declared stays a declaration", func(t *testing.T) {
		// The case order is load-bearing: the proved branch tests !Patched, so a
		// declared package keeps its declaration line and never becomes an
		// "undeclared divergence" merely because its authorship happens to be
		// provable. Every deliberately patched package in the overlay is exactly
		// this shape — it carries our patch AND says so.
		r := divergenceFinding("spectacle", 9, 0, AuthorshipOverlay, provingFile)
		r.Patched, r.PatchedBy = true, "kde-plasma/spectacle@stable"

		out := formatVerificationFindings([]CompareResult{r})

		if strings.Contains(out, "undeclared divergence") {
			t.Errorf("a declared package is reported as an undeclared divergence.\n--- section ---\n%s", out)
		}
		if !strings.Contains(out, "kde-plasma/spectacle@stable") {
			t.Errorf("the declaration line no longer names its entry.\n--- section ---\n%s", out)
		}
		if strings.Contains(out, undeclaredDivergenceCaveat) {
			t.Errorf("the caveat prints for a section whose only finding is a declaration.\n--- section ---\n%s", out)
		}
	})
}

// TestUndeclaredDivergenceCaveatSpeaksOnlyForWhatIsUnproved pins the suppression
// rule and, just as importantly, its limits.
//
// The caveat says a difference is not proof of authorship. Where the content
// PROVED authorship that sentence is simply false, and a report that qualifies
// its own established finding teaches the operator to discount both. But the
// suppression keys on the unproved findings EXISTING, never on a proved one
// existing: one proved package in a section of eight does not resolve the other
// seven, and the live section is exactly that mixture.
//
// _Requirements: R2.2, R2.3_
func TestUndeclaredDivergenceCaveatSpeaksOnlyForWhatIsUnproved(t *testing.T) {
	proved := func(pkg string) CompareResult {
		return divergenceFinding(pkg, 9, 0, AuthorshipOverlay, provingFile)
	}
	unproved := func(pkg string) CompareResult {
		return divergenceFinding(pkg, 1, 1, AuthorshipUnproved, "")
	}

	tests := []struct {
		name       string
		results    []CompareResult
		wantCaveat bool
	}{
		{
			// Nothing is left ambiguous, so there is nothing to warn about.
			name:       "every divergence proved",
			results:    []CompareResult{proved("spectacle"), proved("kdeplasma-addons")},
			wantCaveat: false,
		},
		{
			// The live section: three proved, five not. The five still need it.
			name:       "a mixed section",
			results:    []CompareResult{proved("spectacle"), unproved("kwin"), unproved("drkonqi")},
			wantCaveat: true,
		},
		{
			// Today's behaviour, unchanged.
			name:       "every divergence unproved",
			results:    []CompareResult{unproved("kwin"), unproved("drkonqi")},
			wantCaveat: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := formatVerificationFindings(tt.results)

			n := strings.Count(out, undeclaredDivergenceCaveat)
			switch {
			case tt.wantCaveat && n != 1:
				t.Errorf("the caveat prints %d times, want exactly 1: an unproved finding is still on this section, "+
					"and one sentence for the whole section is what stops it becoming wallpaper.\n--- section ---\n%s", n, out)
			case !tt.wantCaveat && n != 0:
				t.Errorf("the caveat prints %d times where the content proved every divergence; qualifying a finding "+
					"the report established teaches the operator to discount both.\n--- section ---\n%s", n, out)
			}
		})
	}
}

// TestProvedByIsRenderedAsAnArgument makes the format-string rule executable
// rather than promised.
//
// ProvedBy is derived from EBUILD TEXT — a filename an ebuild spells, expanded
// from its own variables. Nothing about that is ours to trust as a format
// string: passed as one, "%s" in a name would consume the next argument and the
// line would render as "%!s(MISSING)" or silently attribute the wrong version.
// Every other piece of text this report prints already follows the rule; this is
// where it is checked.
func TestProvedByIsRenderedAsAnArgument(t *testing.T) {
	// Not a plausible filename, deliberately: the point is that the renderer
	// never interprets one, whatever it holds.
	const hostile = "files/%s-%d-%%-ours.patch"

	out := formatVerificationFindings([]CompareResult{
		divergenceFinding("spectacle", 9, 0, AuthorshipOverlay, hostile),
	})
	line := findingLine(t, out, "kde-plasma/spectacle")

	if !strings.Contains(line, hostile) {
		t.Errorf("the name renders as %q rather than verbatim; it reached a format string:\n%s", hostile, line)
	}
	for _, artefact := range []string{"%!s(", "%!d(", "MISSING", "EXTRA"} {
		if strings.Contains(line, artefact) {
			t.Errorf("the line carries %q, which only a format-string expansion produces:\n%s", artefact, line)
		}
	}
	// The counts must still be the ones the result carries. A name interpreted as
	// a format string would consume them as its own arguments.
	if !strings.Contains(line, "(+9/-0)") {
		t.Errorf("the counts did not survive beside a name containing verbs:\n%s", line)
	}
}

// TestProvedAuthorshipReachesTheRenderedReport runs the real pipeline —
// comparison, annotation, report — over a fixture holding both kinds.
//
// The cases above hand-build their results, which is what lets one section carry
// a proved and an unproved finding without two overlay trees. A hand-built
// fixture can drift into a shape production never produces, though, and every
// assertion on it would go on passing. This one reaches the same lines the way
// runCompare reaches them, over the two live shapes: spectacle, whose ebuild
// applies a patch ::gentoo never had, and kwin, whose single changed line proves
// nothing either way.
func TestProvedAuthorshipReachesTheRenderedReport(t *testing.T) {
	overlayRoot, upstreamRoot := t.TempDir(), t.TempDir()

	writeVerifyEbuild(t, overlayRoot, "kde-plasma", "spectacle", "6.7.4",
		"EAPI=8\ninherit ecm\nPATCHES=( \"${FILESDIR}/${PN}-opencv5.patch\" )\n")
	writeVerifyEbuild(t, upstreamRoot, "kde-plasma", "spectacle", "6.7.4",
		"EAPI=8\ninherit ecm\n")
	// ::gentoo carries a files/ tree for the package, just not this patch: the
	// miss is a real absence rather than an absent directory.
	writeFilesdirFile(t, upstreamRoot, "kde-plasma", "spectacle", "spectacle-cmake.patch")

	// The four-of-eight live shape: PYTHON_COMPAT revised upstream in place, no
	// ${FILESDIR} reference anywhere in the file.
	writeVerifyEbuild(t, overlayRoot, "kde-plasma", "kwin", "6.7.4",
		"EAPI=8\nPYTHON_COMPAT=( python3_{11..14} )\ninherit ecm\n")
	writeVerifyEbuild(t, upstreamRoot, "kde-plasma", "kwin", "6.7.4",
		"EAPI=8\nPYTHON_COMPAT=( python3_{12..15} )\ninherit ecm\n")

	prov := &localRootedFakeProvider{root: upstreamRoot, versions: map[string][]string{
		"kde-plasma/spectacle": {"6.7.4"},
		"kde-plasma/kwin":      {"6.7.4"},
	}}
	// ONE options value for both calls, exactly as runCompare passes it.
	opts := CompareOptions{
		IncludeSynced: true,
		OverlayPath:   overlayRoot,
		Divergence: map[string]Divergence{
			"kde-plasma/spectacle": {},
			"kde-plasma/kwin":      {},
		},
	}

	report, err := CompareWithProvider([]PackageInfo{
		{Category: "kde-plasma", Package: "spectacle", LatestVersion: "6.7.4"},
		{Category: "kde-plasma", Package: "kwin", LatestVersion: "6.7.4"},
	}, prov, opts)
	if err != nil {
		t.Fatalf("CompareWithProvider returned %v, want nil", err)
	}
	AnnotateAuthorship(report, prov, opts)

	out := FormatReport(report)

	proved := findingLine(t, out, "kde-plasma/spectacle")
	if !strings.Contains(proved, provingFile) || !strings.Contains(proved, "proved") {
		t.Errorf("the report does not state what the overlay's content proves about spectacle, or does not name %q:\n%s",
			provingFile, proved)
	}
	if !strings.Contains(out, todaysUnprovedFinding) {
		t.Errorf("kwin's finding no longer reads as it does today.\nwant the line: %s\n--- report ---\n%s",
			todaysUnprovedFinding, out)
	}
	// One unproved finding is left in the section, so the caveat still belongs —
	// once, for the section, exactly as with two unproved findings.
	if n := strings.Count(out, undeclaredDivergenceCaveat); n != 1 {
		t.Errorf("the caveat prints %d times for a section holding one proved and one unproved finding, want 1.\n--- report ---\n%s",
			n, out)
	}
}
