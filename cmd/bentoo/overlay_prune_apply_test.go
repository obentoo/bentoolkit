package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/overlay"
)

// This file pins the half of `overlay prune` that can destroy something:
// --apply, its two confirmations, the registry edit that follows the removals,
// and the exit codes (R4.1–R4.4, R5.3, R5.5, R6.1–R6.3).
//
// # Why two confirmations and not one with a longer prompt
//
// The identical batch and the diverging batch are different decisions, and the
// operator is entitled to make them separately. Approving "delete 60 packages
// ::gentoo already ships" says nothing about "and also throw away the patch we
// carry in nano and never wrote down". One prompt covering both would collect a
// yes for the first and spend it on the second.
//
// # What the diverging batch can actually contain
//
// UNDECLARED divergence only, and that is a property of the pipeline rather than
// a choice this file makes. A registry entry declaring `patched` is one of the
// two axes deriveVerdict reads (compare.go:193-201): with it, StatusUpToDate
// yields VerdictKeep and StatusOutdated yields VerdictNeedsRebase, and no status
// yields VerdictRedundant. PlanPrune gates on the verdict FIRST (R2.5), so a
// declared package is refused before its content is ever compared — it can never
// reach the class R2.4 describes.
//
// R2.4 and classifyPrune's `result.Patched` branch are therefore defence in
// depth: they hold the line if deriveVerdict ever stops treating a declaration
// as disqualifying. Nothing below asserts on a state the pipeline cannot
// produce, because a test that pins an unreachable case reports on the fixture
// and not on the command.
//
// # Why --yes cannot reach the second one
//
// --yes exists so a scripted run can proceed without a human. Discarding local
// work is exactly the thing a scripted run must not be able to do on its own, so
// R4.4 refuses it outright rather than letting a flag stand in for consent. The
// asymmetry is the point, and TestPruneApplyYesSkipsOnlyTheIdenticalConfirmation
// is the test that proves --yes did not quietly grow into the second prompt.
//
// # What "removed nothing" is asserted against
//
// Every refusal is checked twice: the package directory is still on disk, AND
// the executor seam records which atoms it was asked to remove. The first
// catches a removal that happened; the second catches one that was attempted and
// merely failed to matter. A command that is safe and one that is lucky about
// what the batch contained look identical from the filesystem alone.

const (
	// pruneDeclaredPkg is byte-identical to ::gentoo and DECLARES itself patched.
	// It is REFUSED, and by the verdict rather than by the content check: the
	// declaration makes deriveVerdict return VerdictKeep, and PlanPrune's verdict
	// gate refuses a keep without comparing anything (R2.5).
	//
	// It is in every fixture below precisely because no flag reaches it. A run
	// given --include-patched AND --yes must still leave it alone, and that is
	// only observable with such a package present.
	pruneDeclaredPkg  = "kate"
	pruneDeclaredAtom = prunedCat + "/" + pruneDeclaredPkg

	// pruneUndeclaredPkg carries a real content difference and no declaration, so
	// its verdict stays `redundant` and the content check is what refuses it: the
	// undeclared divergence R2.2 refuses and R4.1 lets --include-patched
	// reconsider. It is the ONLY shape the diverging batch can hold.
	pruneUndeclaredPkg  = "nano"
	pruneUndeclaredAtom = prunedCat + "/" + pruneUndeclaredPkg
)

// pruneRegistryEntry is one record in a fixture registry. `patched` writes the
// declaration as the schema actually holds it: a string carrying the reason.
type pruneRegistryEntry struct {
	key     string
	patched bool
}

// writePruneApplyRegistry writes a packages.toml holding the given records in
// order, so one fixture can mix declared and undeclared entries — which
// writePruneRegistryEntries cannot, since it applies one declaration to all of
// them.
func writePruneApplyRegistry(t *testing.T, overlayPath string, entries []pruneRegistryEntry) {
	t.Helper()

	dir := filepath.Join(overlayPath, ".autoupdate")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}

	var b strings.Builder
	b.WriteString("# test registry\n\n")
	for _, e := range entries {
		b.WriteString("[\"" + e.key + "\"]\n")
		b.WriteString("url = \"https://example.invalid/releases\"\n")
		b.WriteString("parser = \"regex\"\n")
		b.WriteString("pattern = 'v([0-9.]+)'\n")
		if e.patched {
			b.WriteString("patched = \"" + prunePatchedReason + "\"\n")
		}
		b.WriteString("# END\n\n")
	}

	path := filepath.Join(dir, "packages.toml")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// readPruneRegistry returns packages.toml verbatim, for the byte comparisons
// R6.3 and R5.3 are stated in.
func readPruneRegistry(t *testing.T, overlayPath string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(overlayPath, ".autoupdate", "packages.toml"))
	if err != nil {
		t.Fatalf("read registry: %v", err)
	}
	return string(data)
}

// pruneApplyMixedFixture builds the three-package overlay the R4 tests need, one
// package per outcome:
//
//	app-editors/zed   identical, undeclared  -> Identical, eligible by default
//	app-editors/kate  identical, DECLARED    -> Refused, unreachable by any flag
//	app-editors/nano  content differs        -> Diverging, only under --include-patched
//
// kate is Refused and not Diverging, and the difference is the pipeline's rather
// than this fixture's: its declaration makes deriveVerdict return VerdictKeep
// (compare.go:193), and PlanPrune refuses a keep at the verdict gate without
// comparing content (R2.5). See the file header. It is here to prove that
// --include-patched and --yes together still do not reach it.
//
// zed carries two registry entries so that R5.2 — every entry of an atom, not
// only the first — is exercised by whichever test reaches the registry edit.
func pruneApplyMixedFixture(t *testing.T) (overlayPath string, prov *fakePruneGentoo) {
	t.Helper()

	overlayPath = setupTestOverlay(t)
	gentooRoot := t.TempDir()

	for _, pkg := range []string{prunedPkg, pruneDeclaredPkg} {
		writePruneCmdPackage(t, overlayPath, prunedCat, pkg, prunedVer, pruneStockEbuild)
		writePruneCmdPackage(t, gentooRoot, prunedCat, pkg, prunedVer, pruneStockEbuild)
	}
	// The only package whose bytes actually differ between the two trees.
	writePruneCmdPackage(t, overlayPath, prunedCat, pruneUndeclaredPkg, prunedVer, pruneOursEbuild)
	writePruneCmdPackage(t, gentooRoot, prunedCat, pruneUndeclaredPkg, prunedVer, pruneStockEbuild)

	writePruneApplyRegistry(t, overlayPath, []pruneRegistryEntry{
		{key: prunedAtom},
		{key: prunedAtom + "@stable"},
		{key: pruneDeclaredAtom, patched: true},
		{key: pruneUndeclaredAtom},
	})

	return overlayPath, &fakePruneGentoo{
		root: gentooRoot,
		versions: map[string][]string{
			prunedAtom:          {prunedVer},
			pruneDeclaredAtom:   {prunedVer},
			pruneUndeclaredAtom: {prunedVer},
		},
	}
}

// pruneApplyRemovablePair builds two packages that are both identical to
// ::gentoo and both undeclared, so both are eligible on a plain --apply run.
//
// The registry edit and the exit code can only be told apart when more than one
// removal is in flight: with a single package, "the registry lost the atoms that
// went" and "the registry lost every atom" are the same file.
func pruneApplyRemovablePair(t *testing.T) (overlayPath string, prov *fakePruneGentoo) {
	t.Helper()

	overlayPath = setupTestOverlay(t)
	gentooRoot := t.TempDir()

	for _, pkg := range []string{prunedPkg, pruneUndeclaredPkg} {
		writePruneCmdPackage(t, overlayPath, prunedCat, pkg, prunedVer, pruneStockEbuild)
		writePruneCmdPackage(t, gentooRoot, prunedCat, pkg, prunedVer, pruneStockEbuild)
	}

	writePruneApplyRegistry(t, overlayPath, []pruneRegistryEntry{
		{key: prunedAtom},
		{key: prunedAtom + "@stable"},
		{key: pruneUndeclaredAtom},
	})

	return overlayPath, &fakePruneGentoo{
		root: gentooRoot,
		versions: map[string][]string{
			prunedAtom:          {prunedVer},
			pruneUndeclaredAtom: {prunedVer},
		},
	}
}

// setOverlayPruneInteractive pins whether the run believes it has a terminal.
// Under `go test` stdin is never a character device, so without this seam the
// interactive requirements could only ever be tested in the direction the runner
// hands out for free.
func setOverlayPruneInteractive(t *testing.T, interactive bool) {
	t.Helper()
	orig := pruneInteractiveFn
	t.Cleanup(func() { pruneInteractiveFn = orig })
	pruneInteractiveFn = func() bool { return interactive }
}

// recordPruneConfirmations replaces the confirmation seam with one that keeps
// every prompt it was shown and answers them in the order given. A prompt beyond
// the supplied answers fails the test: an unexpected extra confirmation is a bug
// in the same family as a missing one.
func recordPruneConfirmations(t *testing.T, answers ...bool) *[]string {
	t.Helper()

	prompts := make([]string, 0, len(answers))
	setOverlayPruneConfirm(t, func(prompt string) bool {
		prompts = append(prompts, prompt)
		if len(prompts) <= len(answers) {
			return answers[len(prompts)-1]
		}
		t.Errorf("confirmation #%d was not expected — %d were, and every prompt is a separate decision the operator is being asked to make. Prompt:\n%s",
			len(prompts), len(answers), prompt)
		return false
	})
	return &prompts
}

// forbidPruneConfirmation fails the test if any confirmation is requested.
func forbidPruneConfirmation(t *testing.T) {
	t.Helper()
	setOverlayPruneConfirm(t, func(prompt string) bool {
		t.Errorf("a confirmation was requested where the run must decide without one:\n%s", prompt)
		return false
	})
}

// pruneExecutorSpy records what the removal seam was asked to do.
type pruneExecutorSpy struct {
	// atoms is every atom the executor was asked to remove, across every call.
	// It is the evidence for "was NOT reached", which the filesystem cannot give:
	// a directory still standing proves no removal happened, not that none was
	// attempted.
	atoms []string
	// fail maps an atom to the error the removal reports instead of succeeding.
	fail map[string]error
}

// spyOnPruneExecutor installs the spy, reproducing ExecutePrune's contract: it
// acts only on plans carrying Eligible, and a plan it may not act on yields no
// result at all — so len(results) is not len(batch), and a caller counting one
// from the other is counting wrong.
func spyOnPruneExecutor(t *testing.T, fail map[string]error) *pruneExecutorSpy {
	t.Helper()

	spy := &pruneExecutorSpy{fail: fail}
	setOverlayPruneExecutor(t, func(batch overlay.PruneBatch, _ overlay.PruneOptions) []overlay.PruneResult {
		var results []overlay.PruneResult
		for _, plans := range [][]overlay.PrunePlan{batch.Identical, batch.Diverging, batch.Refused} {
			for _, plan := range plans {
				if !plan.Eligible {
					continue
				}
				atom := plan.Category + "/" + plan.Package
				spy.atoms = append(spy.atoms, atom)
				if err := spy.fail[atom]; err != nil {
					results = append(results, overlay.PruneResult{Plan: plan, Err: err})
					continue
				}
				results = append(results, overlay.PruneResult{Plan: plan, Removed: true})
			}
		}
		return results
	})
	return spy
}

// forbidPruneRemovalAttempt fails the test if the executor is reached at all.
func forbidPruneRemovalAttempt(t *testing.T, why string) {
	t.Helper()
	setOverlayPruneExecutor(t, func(overlay.PruneBatch, overlay.PruneOptions) []overlay.PruneResult {
		t.Errorf("the executor was reached: %s", why)
		return nil
	})
}

// runPruneApply drives one --apply invocation and returns its output and exit
// code (-1 when the run ended without calling osExit).
func runPruneApply(t *testing.T, overlayPath string) (out string, code int) {
	t.Helper()
	out = captureStdout(t, func() {
		code = withExitIntercept(func() {
			runPrune(context.Background(), overlayPath, nil, &config.Config{})
		})
	})
	return out, code
}

// assertPrunePackagesIntact fails if any of the named packages left the overlay.
func assertPrunePackagesIntact(t *testing.T, overlayPath string, pkgs ...string) {
	t.Helper()
	for _, pkg := range pkgs {
		dir := filepath.Join(overlayPath, prunedCat, pkg)
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("%s was removed: %v", dir, err)
		}
	}
}

// TestPruneApplyDeclinedRemovesNothing pins R6.1 and R6.3.
//
// The confirmation must be REQUESTED — a run that removes nothing because it
// never asked is not consent, it is a different bug — and answering no must
// leave both the overlay and packages.toml byte-identical. The executor is wired
// to fail the test on contact, because R6.3 is supposed to hold by construction:
// a declined batch has no path to the removal at all, rather than reaching it
// and finding nothing to do.
//
// _Requirements: R6.1, R6.3_
func TestPruneApplyDeclinedRemovesNothing(t *testing.T) {
	overlayPath, prov := pruneApplyMixedFixture(t)
	withFakeGentoo(t, prov)
	setOverlayPruneFlags(t, true, false, false, false)
	setOverlayPruneInteractive(t, true)
	prompts := recordPruneConfirmations(t, false)
	forbidPruneRemovalAttempt(t, "the confirmation was declined, so nothing may be handed to it")

	before := readPruneRegistry(t, overlayPath)
	_, _ = runPruneApply(t, overlayPath)

	if len(*prompts) != 1 {
		t.Errorf("confirmations requested = %d, want 1; R6.1 asks for one confirmation covering the batch, and a run that removes nothing WITHOUT asking has not obtained consent, it has merely avoided the question", len(*prompts))
	}
	assertPrunePackagesIntact(t, overlayPath, prunedPkg, pruneDeclaredPkg, pruneUndeclaredPkg)
	if after := readPruneRegistry(t, overlayPath); after != before {
		t.Errorf("packages.toml changed after a declined confirmation; R6.3 asks for it byte-identical:\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}
}

// TestPruneApplyNonInteractiveWithoutYesExitsNonZero pins R6.2.
//
// All three of the requirement's clauses are separate assertions, because a run
// can satisfy any two and still be wrong: printing the plan without exiting
// non-zero tells a script it succeeded; exiting non-zero without printing tells
// the operator nothing about what it would have done.
//
// The confirmation seam must not be reached either. A prompt written to a
// terminal nobody is watching does not become consent by going unanswered.
//
// _Requirements: R6.2_
func TestPruneApplyNonInteractiveWithoutYesExitsNonZero(t *testing.T) {
	overlayPath, prov := pruneApplyMixedFixture(t)
	withFakeGentoo(t, prov)
	setOverlayPruneFlags(t, true, false, false, false)
	setOverlayPruneInteractive(t, false)
	forbidPruneConfirmation(t)
	forbidPruneRemovalAttempt(t, "a non-interactive run without --yes may not remove anything")

	out, code := runPruneApply(t, overlayPath)

	if code <= 0 {
		t.Errorf("exit code = %d, want non-zero; a script that reads 0 here concludes the removals were carried out", code)
	}
	if !strings.Contains(out, prunedPkg) {
		t.Errorf("the plan was not printed, so the operator cannot see what the run would have done:\n%s", out)
	}
	assertPrunePackagesIntact(t, overlayPath, prunedPkg, pruneDeclaredPkg, pruneUndeclaredPkg)
}

// TestPruneApplyYesSkipsOnlyTheIdenticalConfirmation pins the boundary of --yes
// (R6.1, R4.3, R4.4).
//
// Exactly one confirmation must survive the flag, and it must be the DIVERGING
// one. The failure this catches is the natural refactor: one prompt loop over
// both batches with a single `if !yes` around it, which reads correct and hands
// --yes the power to discard local work. That the count is one is not enough on
// its own — one prompt could equally be the identical batch's, with the diverging
// batch silently approved — so the prompt's own text is checked.
//
// The declared package is checked NOT to have been handed over. It is refused at
// the verdict gate rather than by any flag on this command, and a run holding
// both --include-patched and --yes is the strongest available demonstration that
// nothing promotes it.
//
// _Requirements: R6.1, R4.3, R4.4_
func TestPruneApplyYesSkipsOnlyTheIdenticalConfirmation(t *testing.T) {
	overlayPath, prov := pruneApplyMixedFixture(t)
	withFakeGentoo(t, prov)
	setOverlayPruneFlags(t, true, true, false, true)
	setOverlayPruneInteractive(t, true)
	prompts := recordPruneConfirmations(t, true)
	spy := spyOnPruneExecutor(t, nil)

	_, _ = runPruneApply(t, overlayPath)

	if len(*prompts) != 1 {
		t.Fatalf("confirmations requested = %d, want exactly 1: --yes covers the identical batch and nothing else, so the diverging batch must still ask", len(*prompts))
	}
	only := (*prompts)[0]
	if !strings.Contains(only, pruneUndeclaredPkg) {
		t.Errorf("the surviving confirmation is not the diverging one — --yes appears to have been spent on the wrong prompt, which means the diverging batch was approved without anyone being asked:\n%s", only)
	}

	for _, want := range []string{prunedAtom, pruneUndeclaredAtom} {
		if !containsPruneAtom(spy.atoms, want) {
			t.Errorf("%q was not handed to the executor; with --yes covering the identical batch and the diverging prompt approved, both are authorised. Attempted: %v", want, spy.atoms)
		}
	}
	if containsPruneAtom(spy.atoms, pruneDeclaredAtom) {
		t.Errorf("%q was handed to the executor; its declaration makes the verdict `keep`, and no flag on this command removes a package the verdict gate refused (R2.5). Attempted: %v", pruneDeclaredAtom, spy.atoms)
	}
}

// TestPruneApplyDivergingRefusedNonInteractiveEvenWithYes pins R4.4.
//
// --yes exists so that a scripted run can proceed. Discarding local work is
// precisely what a scripted run must not decide on its own, so no flag stands in
// for the second confirmation: without a terminal, the diverging batch is refused
// outright while the identical batch — which loses nothing — still goes.
//
// That split is the whole assertion. A run that refuses everything here would
// also pass "the diverging packages survived" while being wrong about --yes, so
// the identical package is checked to have gone through.
//
// _Requirements: R4.4, R6.2_
func TestPruneApplyDivergingRefusedNonInteractiveEvenWithYes(t *testing.T) {
	overlayPath, prov := pruneApplyMixedFixture(t)
	withFakeGentoo(t, prov)
	setOverlayPruneFlags(t, true, true, false, true)
	setOverlayPruneInteractive(t, false)
	spy := spyOnPruneExecutor(t, nil)

	out, _ := runPruneApply(t, overlayPath)

	if containsPruneAtom(spy.atoms, pruneUndeclaredAtom) {
		t.Errorf("%q was handed to the executor from a non-interactive session; it is the diverging batch, and --yes must never satisfy that confirmation (R4.4). Attempted: %v", pruneUndeclaredAtom, spy.atoms)
	}
	if containsPruneAtom(spy.atoms, pruneDeclaredAtom) {
		t.Errorf("%q was handed to the executor; it is refused at the verdict gate and no flag reaches it, so this is a different bug from the R4.4 one above. Attempted: %v", pruneDeclaredAtom, spy.atoms)
	}
	if !containsPruneAtom(spy.atoms, prunedAtom) {
		t.Errorf("%q was not removed either; --yes DOES cover the identical batch, and refusing everything would be the right answer to a different question. Attempted: %v", prunedAtom, spy.atoms)
	}
	assertPrunePackagesIntact(t, overlayPath, pruneDeclaredPkg, pruneUndeclaredPkg)

	low := strings.ToLower(out)
	if !strings.Contains(low, "interactive") {
		t.Errorf("the run does not say WHY the diverging packages were left behind; the operator gave --include-patched and --yes and is entitled to know which of the two was not enough:\n%s", out)
	}
}

// TestPruneApplyDivergingPromptNamesEachPackageAndReason pins R4.2 and R4.3.
//
// The operator answering this prompt is discarding work, and cannot weigh that
// from a count. Every package is named, and each carries the reason it is in
// this batch — for an undeclared divergence that reason is what the byte
// comparison found, which is the only account anyone has of what is in the copy.
// R4.2's other half, "its declared reason where one exists", has no case to
// exercise: a declared package never reaches this batch (see the file header),
// so asserting on it here would pin the fixture rather than the command.
//
// The two prompts must also differ in CONTENT and not merely in count — an
// identical prompt shown twice satisfies "distinct" arithmetically while asking
// the same question twice.
//
// The declared package must NOT appear. It is refused at the verdict gate, and a
// prompt offering to discard it would be asking for consent to something this
// run cannot do.
//
// _Requirements: R4.2, R4.3_
func TestPruneApplyDivergingPromptNamesEachPackageAndReason(t *testing.T) {
	overlayPath, prov := pruneApplyMixedFixture(t)
	withFakeGentoo(t, prov)
	setOverlayPruneFlags(t, true, true, false, false)
	setOverlayPruneInteractive(t, true)
	prompts := recordPruneConfirmations(t, true, true)
	_ = spyOnPruneExecutor(t, nil)

	_, _ = runPruneApply(t, overlayPath)

	if len(*prompts) != 2 {
		t.Fatalf("confirmations requested = %d, want 2: R4.3 asks for a confirmation distinct from the one covering the identical packages, because approving a batch that loses nothing says nothing about a batch that does. Prompts: %q", len(*prompts), *prompts)
	}

	var diverging string
	for _, p := range *prompts {
		if strings.Contains(p, pruneUndeclaredPkg) {
			diverging = p
		}
	}
	if diverging == "" {
		t.Fatalf("no prompt names %q, so nothing asked about the diverging batch at all. Prompts: %q", pruneUndeclaredPkg, *prompts)
	}
	if (*prompts)[0] == (*prompts)[1] {
		t.Errorf("both confirmations show the same text, so the operator was asked one question twice rather than two different ones:\n%s", diverging)
	}

	if strings.Contains(diverging, pruneDeclaredPkg) {
		t.Errorf("the diverging confirmation names %q, which no flag on this command can remove; asking to discard it collects consent for something the run will not do:\n%s", pruneDeclaredPkg, diverging)
	}
	if !strings.Contains(diverging, prunedVer) {
		t.Errorf("the diverging confirmation does not name the version that differs (%q); R4.2 asks for the size of the difference, and %q alone does not say what is being thrown away:\n%s", prunedVer, pruneUndeclaredPkg, diverging)
	}
	if !strings.Contains(strings.ToLower(diverging), "discard") {
		t.Errorf("the diverging confirmation does not state that local work is being discarded (R4.3); without it the prompt reads like the identical one:\n%s", diverging)
	}
}

// TestPruneApplyEditsRegistryOnlyForRemovedDirectories pins D7 and R5.2.
//
// The registry edit follows the removals and is driven by what actually went.
// A package whose removal FAILED keeps its entries: deleting them would leave the
// registry describing an overlay that still holds the directory, and the next
// autoupdate run would then see a package nobody tracks.
//
// Both of the removed atom's entries must go, not only the first. 90 of the real
// registry's 321 atoms carry more than one, and a half-deleted atom is worse than
// an untouched one — the survivor keeps updating a package that is gone.
//
// _Requirements: R5.2, R5.5_
func TestPruneApplyEditsRegistryOnlyForRemovedDirectories(t *testing.T) {
	overlayPath, prov := pruneApplyRemovablePair(t)
	withFakeGentoo(t, prov)
	setOverlayPruneFlags(t, true, false, false, true)
	setOverlayPruneInteractive(t, false)
	forbidPruneConfirmation(t)
	spy := spyOnPruneExecutor(t, map[string]error{
		pruneUndeclaredAtom: errors.New("removing the directory: permission denied"),
	})

	before := readPruneRegistry(t, overlayPath)
	_, _ = runPruneApply(t, overlayPath)
	after := readPruneRegistry(t, overlayPath)

	if len(spy.atoms) != 2 {
		t.Fatalf("the executor was asked to remove %v, want both packages; the registry assertions below say nothing if no removal was attempted", spy.atoms)
	}
	if after == before {
		t.Fatalf("packages.toml is unchanged, so the removed package's entries were never deleted:\n%s", after)
	}

	for _, gone := range []string{`"` + prunedAtom + `"`, `"` + prunedAtom + `@stable"`} {
		if strings.Contains(after, gone) {
			t.Errorf("registry entry %s survived the removal of its package; R5.2 deletes every entry of the atom, not only the first:\n%s", gone, after)
		}
	}
	if !strings.Contains(after, `"`+pruneUndeclaredAtom+`"`) {
		t.Errorf("registry entry for %q was deleted although its removal FAILED; the directory is still there, and an untracked package is how it stops being updated:\n%s", pruneUndeclaredAtom, after)
	}
}

// TestPruneApplyKeepRegistryLeavesConfigUntouched pins R5.3.
//
// --keep-registry removes the directories and leaves packages.toml alone —
// untouched, not rewritten identically. The overlay auto-commits, so a rewrite
// that happens to produce the same bytes is still a file the next run has to
// explain.
//
// The removals are asserted to have HAPPENED first. Without that, a run that
// silently did nothing at all would pass this test perfectly.
//
// _Requirements: R5.3_
func TestPruneApplyKeepRegistryLeavesConfigUntouched(t *testing.T) {
	overlayPath, prov := pruneApplyRemovablePair(t)
	withFakeGentoo(t, prov)
	setOverlayPruneFlags(t, true, false, true, true)
	setOverlayPruneInteractive(t, false)
	forbidPruneConfirmation(t)
	spy := spyOnPruneExecutor(t, nil)

	before := readPruneRegistry(t, overlayPath)
	_, _ = runPruneApply(t, overlayPath)
	after := readPruneRegistry(t, overlayPath)

	if len(spy.atoms) == 0 {
		t.Fatal("no removal was attempted, so 'the registry was left alone' is true for the wrong reason")
	}
	if after != before {
		t.Errorf("packages.toml changed under --keep-registry:\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}
}

// TestPruneApplyOneFailureExitsNonZero pins R5.5.
//
// One failed removal does not stop the rest — the batch was approved as a whole
// and abandoning it halfway leaves the overlay in a state nobody chose — but the
// run must still exit non-zero and name what failed. A partial success reported
// as 0 is how the failure survives to the next run unnoticed, and this command's
// output is read by a script often enough that the exit code is the only part
// some callers see.
//
// _Requirements: R5.5_
func TestPruneApplyOneFailureExitsNonZero(t *testing.T) {
	overlayPath, prov := pruneApplyRemovablePair(t)
	withFakeGentoo(t, prov)
	setOverlayPruneFlags(t, true, false, false, true)
	setOverlayPruneInteractive(t, false)
	forbidPruneConfirmation(t)
	spy := spyOnPruneExecutor(t, map[string]error{
		pruneUndeclaredAtom: errors.New("removing the directory: permission denied"),
	})

	out, code := runPruneApply(t, overlayPath)

	if len(spy.atoms) != 2 {
		t.Fatalf("the executor was asked to remove %v, want both; R5.5 is about continuing PAST a failure, which needs something after it to continue to", spy.atoms)
	}
	if code <= 0 {
		t.Errorf("exit code = %d, want non-zero after a failed removal; a caller that only reads the code would record this run as a clean sweep", code)
	}
	if !strings.Contains(out, pruneUndeclaredPkg) {
		t.Errorf("the failure does not name the package that could not be removed, so the operator cannot act on it:\n%s", out)
	}
}

// containsPruneAtom reports whether the executor was asked to remove an atom.
func containsPruneAtom(atoms []string, want string) bool {
	for _, a := range atoms {
		if a == want {
			return true
		}
	}
	return false
}
