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

	got, findings := verifyRun(t, overlayRoot, prov, pkg, silent)

	if got.DiffAdded != 1 || got.DiffRemoved != 0 {
		t.Errorf("DiffAdded/DiffRemoved = +%d/-%d, want +1/-0; ours adds the PATCHES line",
			got.DiffAdded, got.DiffRemoved)
	}
	// Story 047, sub-task 5.5 (S047-R8.2): the magnitude is asked of the finding.
	// It travels twice on purpose — as the Added/Removed FIELDS a consumer sums
	// or exports, and inside the producer's own sentence — and both are asserted,
	// because the fields without the sentence reach no operator and the sentence
	// without the fields reaches no export.
	div := findingOfKind(findings, FindingUndeclaredDivergence)
	if div == nil {
		t.Fatalf("no undeclared divergence was established:\n%+v", findings)
	}
	if div.Added != 1 || div.Removed != 0 {
		t.Errorf("the finding carries +%d/-%d, want +1/-0; a one-line drift with no size beside it reads like our work", div.Added, div.Removed)
	}
	if !strings.Contains(div.Detail, "(+1/-0)") {
		t.Errorf("the finding's own sentence does not carry the size of the difference: %q", div.Detail)
	}
	// The caveat that says the DIRECTION is unknown is the section's, not the
	// finding's, and is now emitted once per section by internal/common/report —
	// pinned in testdata/TestCompareGoldenPlain.golden. What belongs to the
	// finding is that it claims no authorship it cannot prove.
	if div.Authorship != AuthorshipUnproved || div.ProvedBy != "" {
		t.Errorf("the finding claims authorship (%v, %q) it did not prove; unproved is never a finding that the change is ::gentoo's", div.Authorship, div.ProvedBy)
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

	got, findings := verifyRun(t, overlayRoot, prov, pkg, silent)

	if got.DiffAdded != 0 || got.DiffRemoved != 0 {
		t.Errorf("DiffAdded/DiffRemoved = +%d/-%d on byte-identical ebuilds, want +0/-0",
			got.DiffAdded, got.DiffRemoved)
	}
	// No divergence, so nothing for the section caveat to qualify. Asserted as
	// the absence of the FINDING (story 047, sub-task 5.5, S047-R8.2): the caveat
	// is the section's now, and it is emitted from the presence of rows, so the
	// finding's absence is what the old assertion was really about.
	if f := findingOfKind(findings, FindingUndeclaredDivergence); f != nil {
		t.Errorf("a divergence was established over byte-identical ebuilds: %q", f.Detail)
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

	report, err := CompareWithProvider(t.Context(), []PackageInfo{
		{Category: "app-editors", Package: "zed", LatestVersion: "1.0"},
		{Category: "app-editors", Package: "vim", LatestVersion: "1.0"},
	}, prov, CompareOptions{IncludeSynced: true, OverlayPath: overlayRoot, Divergence: silent})
	if err != nil {
		t.Fatalf("CompareWithProvider returned %v, want nil", err)
	}

	// Story 047, sub-task 5.5 (S047-R8.2). The FINDINGS half is asserted here,
	// against the values: two undeclared divergences, counted by Kind rather than
	// by a substring that a caveat mentioning the same words would also match.
	EstablishFindings(report)
	n := 0
	for _, f := range report.Findings {
		if f.Kind == FindingUndeclaredDivergence {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("report holds %d undeclared-divergence findings, want 2:\n%+v", n, report.Findings)
	}

	// The CAVEAT half — "once per section, however many findings it holds" — is
	// no longer this package's. The caveat is a property of the redundant SECTION
	// and is emitted by `func compareRedundantSection` in internal/common/report,
	// once, unconditionally when the section has rows. It is pinned in
	// testdata/TestCompareGoldenPlain.golden (a whole-file golden, so a second
	// copy is a diff) and its absence over an empty section by
	// TestCompareRunEmptyRedundantRecommendsNothing. What is left here is the
	// precondition that made "once for two findings" a claim at all: that the two
	// findings are in one section, which is to say one verdict.
	for _, r := range report.Results {
		if r.Verdict != VerdictRedundant {
			t.Errorf("%s/%s is %s, not redundant; the two findings are then in two sections and 'once per section' says nothing", r.Category, r.Package, r.Verdict)
		}
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

// divergenceFor is one package's undeclared-divergence finding, or a fatal.
//
// It replaces findingLine, which cut the rendered report into lines and demanded
// exactly one naming the atom (story 047, sub-task 5.5, S047-R8.2). The
// exactly-one guard is kept and is stricter here: two findings of the same kind
// for one package is a real defect, where two LINES naming an atom could also
// have been one row and one note.
func divergenceFor(t *testing.T, findings []Finding, atom string) Finding {
	t.Helper()
	var found []Finding
	for _, f := range findings {
		if f.Kind == FindingUndeclaredDivergence && f.Atom == atom {
			found = append(found, f)
		}
	}
	if len(found) != 1 {
		t.Fatalf("the report holds %d undeclared-divergence findings for %s, want exactly 1:\n%+v", len(found), atom, findings)
	}
	return found[0]
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

	report, err := CompareWithProvider(t.Context(), []PackageInfo{
		{Category: "kde-plasma", Package: "spectacle", LatestVersion: "6.7.4"},
		{Category: "kde-plasma", Package: "kwin", LatestVersion: "6.7.4"},
	}, prov, opts)
	if err != nil {
		t.Fatalf("CompareWithProvider returned %v, want nil", err)
	}
	AnnotateAuthorship(report, prov, opts)

	// Story 047, sub-task 5.5 (S047-R8.2): the same two claims, asked of the
	// findings. The proved one is now checked on its ProvedBy FIELD as well as in
	// its sentence — S046-R5.1's whole point is that a filename in a field can be
	// re-emitted and one in prose cannot — and the unproved one keeps its
	// verbatim wording, minus the "⚠ " glyph and the atom prefix, which were the
	// renderer's and are not part of what the producer said.
	EstablishFindings(report)
	proved := divergenceFor(t, report.Findings, "kde-plasma/spectacle")
	if proved.ProvedBy != provingFile {
		t.Errorf("spectacle's finding names %q as its proving file, want %q; a filename in prose can be printed and nothing else", proved.ProvedBy, provingFile)
	}
	if proved.Authorship != AuthorshipOverlay {
		t.Errorf("spectacle's authorship is %v, want it proved the overlay's — unproved is never a finding that the change is ::gentoo's", proved.Authorship)
	}
	if !strings.Contains(proved.Detail, provingFile) || !strings.Contains(proved.Detail, "proved") {
		t.Errorf("the finding's own sentence does not state what the overlay's content proves about spectacle, or does not name %q: %q", provingFile, proved.Detail)
	}
	unproved := divergenceFor(t, report.Findings, "kde-plasma/kwin")
	if want := strings.TrimPrefix(todaysUnprovedFinding, "⚠ kde-plasma/kwin: "); unproved.Detail != want {
		t.Errorf("kwin's finding no longer reads as it does today.\n got: %q\nwant: %q", unproved.Detail, want)
	}
	if unproved.ProvedBy != "" || unproved.Authorship != AuthorshipUnproved {
		t.Errorf("kwin's finding claims authorship (%v, %q) from a one-line difference that proves nothing either way", unproved.Authorship, unproved.ProvedBy)
	}
	// The caveat over a section holding one proved and one unproved finding is
	// the renderer's, and is pinned in testdata/TestCompareGoldenPlain.golden
	// (see the note above). What matters here is that one finding is still
	// unproved, which is what makes the caveat belong at all.
	if unproved.Authorship != AuthorshipUnproved {
		t.Error("no finding is left unproved, so the section-level caveat about unproved differences would be printed over nothing")
	}
}

// hostileProvingFile is a filename carrying every verb that would misrender if
// it reached a format POSITION instead of an argument slot.
const hostileProvingFile = "files/%s-%d-%%-ours.patch"

// TestProvedByIsRenderedAsAnArgument is the successor story 047 owed and did not
// leave: the deleted test of that name fed this same filename through and looked
// for `%!s(MISSING)`, and when it went, the shape went unexercised in this
// package.
//
// # The story's Auditor rated this "not worth reopening for". That was measured
// wrong, and the measurement is the reason this test exists.
//
// The rating said every interpolation passes ProvedBy as an argument and that
// go 1.26's vet printf analyzer reports a non-constant format string, so the
// mutation the deleted test caught is caught by a gate already running. Half of
// that is true. vet catches the DEGENERATE form -- the whole format
// non-constant, no arguments left -- and reports "non-constant format string in
// call to fmt.Sprintf".
//
// It does not catch the form this defect would actually take. Concatenating
// ProvedBy INTO a format that still carries its other verbs and their arguments
// -- one edit, the shape somebody reaches for when adding a clause -- leaves
// `go vet ./...` at exit 0, golangci-lint with govet enabled at 0 issues, and
// `go test`'s own printf pass silent. Measured on 2026-09-06 against exactly
// that mutation of `func compareFindings`. This test failed on it, naming the
// `%!s(MISSING)` the deleted test was written to name.
//
// So the two checks are not one gate and its restatement. vet reasons about a
// CALL and only about a format it can fold to a constant; this reasons about the
// OUTPUT and does not care how the sentence was built.
//
// # A filename is the right subject
//
// It is the one part of this sentence that comes from the filesystem rather
// than from this package: `func ebuildFilesdirRefs` reads it out of an ebuild's
// own text. Nothing else interpolated here can carry a percent sign that
// somebody else chose.
func TestProvedByIsRenderedAsAnArgument(t *testing.T) {
	report := &CompareReport{Results: []CompareResult{{
		Category:      "kde-plasma",
		Package:       "spectacle",
		LocalVersion:  "6.7.4",
		RemoteVersion: "6.7.4",
		Verified:      VerifiedDiffers,
		DiffAdded:     3,
		DiffRemoved:   1,
		Authorship:    AuthorshipOverlay,
		ProvedBy:      hostileProvingFile,
	}}}

	EstablishFindings(report)
	proved := divergenceFor(t, report.Findings, "kde-plasma/spectacle")

	if proved.ProvedBy != hostileProvingFile {
		t.Errorf("the FIELD holds %q, want %q verbatim (S046-R5.1): a filename is evidence an operator confirms with one `ls`, and one that arrives altered confirms nothing",
			proved.ProvedBy, hostileProvingFile)
	}
	if !strings.Contains(proved.Detail, hostileProvingFile) {
		t.Errorf("the finding's sentence does not name the proving file literally: %q", proved.Detail)
	}
	if strings.Contains(proved.Detail, "%!") {
		t.Errorf("the proving file reached a format POSITION — the sentence carries a formatting artefact instead of the filename: %q",
			proved.Detail)
	}
}
