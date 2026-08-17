// MERGE FRAGMENT — story 034, sub-task 1.1 (`ResolveBaseline`).
//
// Target file: internal/overlay/baseline_test.go  (NEW file, package overlay)
// Sub-task 6.2 appends to the same file later. Every symbol here is prefixed
// `baseline`, so nothing it adds can collide.
// Borrowed, never re-declared: `writeVerifyEbuild` (compare_verification_test.go).
//
// PINNED CONTRACT (design.md "Components & Interfaces"):
//
//	type Baseline struct {
//	    Repo, Version, Path string
//	    Distance            int    // 0 = same version
//	    Found               bool
//	    Unexamined          string // non-empty when the baseline exists but could not be read
//	}
//	func ResolveBaseline(gentooTree, atom, version string) (Baseline, error)
//
// # WHAT THE DISTANCE ASSERTIONS DELIBERATELY DO NOT PIN
//
// design.md says the distance is "the distance from ours" without fixing a unit —
// versions apart, releases apart, or a component-wise delta are all consistent
// with it, and choosing one here would pin an implementation detail the story
// never decided. So the tests assert the two properties the report actually
// rests on: zero for a same-version baseline, and MONOTONIC otherwise — a nearer
// baseline reports a smaller distance than a farther one. That is exactly what
// D2 says the number is for ("it bounds how much the next decision is worth"),
// and any unit that gets it backwards fails.
package overlay

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/ebuild"
)

const baselineEbuildBody = "EAPI=8\ninherit gstreamer-meson\nDESCRIPTION=\"Qt6 plugin for GStreamer\"\n"

const (
	baselineCategory = "media-libs"
	baselinePkg      = "gst-plugins-qt6"
	baselineAtom     = baselineCategory + "/" + baselinePkg
)

// baselineTree writes a ::gentoo fixture carrying `versions` of the gst package.
//
// There is no .git in it, and there never will be: the real /var/db/repos/gentoo
// is a shallow clone whose history says nothing (M-A), so an implementation that
// consulted a log would be right here and wrong on the host. See
// TestResolveBaselineTouchesNoNetworkAndNoGit below, which makes that mechanical.
//
// It carries profiles/repo_name — Portage's own repository marker, and the thing
// LocateBaselineTree recognises a tree by (sub-task 6.1's recorded contract).
// Without it this is a directory of ebuilds rather than a repository, which is
// precisely the state 6.1's fixture builds on purpose (carryBaseline=false) to
// exercise "the tree cannot be located". A fixture that is meant to BE a
// ::gentoo tree has to carry it.
func baselineTree(t *testing.T, versions ...string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "profiles"), 0o750); err != nil {
		t.Fatalf("mkdir profiles: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "profiles", "repo_name"), []byte("gentoo\n"), 0o600); err != nil {
		t.Fatalf("write repo_name: %v", err)
	}
	for _, v := range versions {
		writeVerifyEbuild(t, root, baselineCategory, baselinePkg, v, baselineEbuildBody)
	}
	return root
}

// TestResolveBaseline is R1.1 to R1.4: the baseline is named, never assumed.
//
// _Requirements: R1, R1.1, R1.2, R1.3, R1.4_
func TestResolveBaseline(t *testing.T) {
	t.Run("the same version wins over a nearer-numbered neighbour", func(t *testing.T) {
		// 1.29.3 is numerically adjacent and 1.29.2 is IDENTICAL. A resolver
		// that ranked by proximity alone could pick either; R1.1 says the exact
		// version is not a candidate among others, it is the answer.
		tree := baselineTree(t, "1.29.2", "1.29.3")

		got, err := ResolveBaseline(tree, baselineAtom, "1.29.2")
		if err != nil {
			t.Fatalf("ResolveBaseline returned %v, want nil", err)
		}
		if !got.Found {
			t.Fatal("Found is false although ::gentoo carries the package at our exact version")
		}
		if got.Version != "1.29.2" {
			t.Errorf("Version is %q, want %q — the same version is the baseline when it exists (R1.1)", got.Version, "1.29.2")
		}
		if got.Distance != 0 {
			t.Errorf("Distance is %d, want 0 for a same-version baseline", got.Distance)
		}
		if got.Path == "" {
			t.Error("Path is empty — 'which ebuild was this measured against' is the question R1 exists to answer")
		}
		if got.Unexamined != "" {
			t.Errorf("Unexamined is %q, want empty for a readable baseline", got.Unexamined)
		}
	})

	t.Run("with no exact match the nearest is chosen", func(t *testing.T) {
		// 1.29.1 is one patch release below ours; 1.20.0 is nine minor series
		// away. No plausible distance metric ranks 1.20.0 closer, so the choice
		// is unambiguous whatever unit the implementation uses.
		tree := baselineTree(t, "1.20.0", "1.29.1")

		got, err := ResolveBaseline(tree, baselineAtom, "1.29.2")
		if err != nil {
			t.Fatalf("ResolveBaseline returned %v, want nil", err)
		}
		if !got.Found {
			t.Fatal("Found is false although ::gentoo carries the package at two other versions (R1.2)")
		}
		if got.Version != "1.29.1" {
			t.Errorf("Version is %q, want %q — the nearest version ::gentoo carries", got.Version, "1.29.1")
		}
		if got.Distance == 0 {
			t.Error("Distance is 0 for a version that is not ours — 0 means 'same version', and a report that cannot tell the two apart cannot tell a real divergence from an artefact of comparing against the wrong version")
		}
	})

	t.Run("the distance grows with the gap", func(t *testing.T) {
		// The property the number is FOR: a baseline one patch away and one
		// three series away are not the same kind of evidence, and the report
		// must not let them look alike (D2).
		near, err := ResolveBaseline(baselineTree(t, "1.29.1"), baselineAtom, "1.29.2")
		if err != nil {
			t.Fatalf("ResolveBaseline (near) returned %v, want nil", err)
		}
		far, err := ResolveBaseline(baselineTree(t, "1.20.0"), baselineAtom, "1.29.2")
		if err != nil {
			t.Fatalf("ResolveBaseline (far) returned %v, want nil", err)
		}
		if near.Distance >= far.Distance {
			t.Errorf("distance to 1.29.1 is %d and to 1.20.0 is %d; the nearer baseline must report the smaller distance, whatever unit is used", near.Distance, far.Distance)
		}
	})

	t.Run("a package ::gentoo does not carry has no baseline", func(t *testing.T) {
		got, err := ResolveBaseline(baselineTree(t, "1.29.2"), "app-editors/zed", "1.0.0")
		if err != nil {
			t.Fatalf("ResolveBaseline returned %v, want nil — 84 of 321 packages are in this state and it is not an error", err)
		}
		if got.Found {
			t.Error("Found is true for a package absent from ::gentoo — R1.3 requires no baseline to be reported, and no realignment is ever proposed from one")
		}
		if got.Version != "" || got.Path != "" {
			t.Errorf("a not-found baseline still names Version=%q Path=%q; there is nothing to name", got.Version, got.Path)
		}
	})
}

// TestResolveBaselineUnreadableIsAStateNotAFailure is D2's second half: a
// baseline that EXISTS but cannot be read is a per-package unexamined state, and
// the run continues. `verifyAgainstLocalContent` already reaches that state and
// exits 0 on the stated principle that absence of evidence is not evidence;
// making this one an error would regress that behaviour for the whole run.
//
// The read is broken by putting a DIRECTORY where the ebuild must be, not by
// chmod 000. `act` runs the CI job as root, and root reads a 000 file happily —
// a permission-based fixture is a false green there and a real failure locally,
// which is the worst of both. A directory is unreadable-as-a-file for everyone.
//
// _Requirements: R1, R1.4_
func TestResolveBaselineUnreadableIsAStateNotAFailure(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, baselineCategory, baselinePkg, baselinePkg+"-1.29.2.ebuild")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("planting the unreadable baseline: %v", err)
	}

	got, err := ResolveBaseline(root, baselineAtom, "1.29.2")

	if err != nil {
		t.Fatalf("ResolveBaseline returned %v; an unreadable baseline is a per-package state, not a run failure (D2)", err)
	}
	if got.Unexamined == "" {
		t.Error("Unexamined is empty although the baseline could not be read — reported as nothing-to-say, this package would be indistinguishable from one that matches ::gentoo exactly")
	}
}

// TestResolveBaselineTouchesNoNetworkAndNoGit holds R1.4 mechanically rather
// than by inspection.
//
// PATH is emptied, so `git` — and everything else the package could shell out
// to — is unreachable: an implementation that shelled out fails here instead of
// working locally and misreading the shallow clone on the host. The fixture has
// no .git and no remote, so a resolver that needed either has nothing to fall
// back on.
//
// _Requirements: R1, R1.4_
func TestResolveBaselineTouchesNoNetworkAndNoGit(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	tree := baselineTree(t, "1.29.2")

	got, err := ResolveBaseline(tree, baselineAtom, "1.29.2")
	if err != nil {
		t.Fatalf("ResolveBaseline returned %v with PATH empty; the baseline is a file on disk and reading it needs no subprocess", err)
	}
	if !got.Found || got.Version != "1.29.2" {
		t.Errorf("got %+v, want the 1.29.2 baseline found from the local tree alone", got)
	}
	if _, statErr := os.Stat(filepath.Join(tree, ".git")); statErr == nil {
		t.Fatal("the fixture grew a .git; the point of this test is that the answer is in the tree's CONTENT, which is all the real shallow clone offers (M-A)")
	}
}

// MERGE FRAGMENT — story 034, sub-task 1.2 (a missing baseline tree is a skipped run).
//
// Target file: internal/overlay/baseline_test.go  (APPEND, after 1.1's fragment)
// Reused from 1.1 and never re-declared: `baselineTree`, `baselineAtom`,
// `baselineCategory`, `baselinePkg`, `baselineEbuildBody`.
//
// Pinned contract:
//
//	var ErrNoBaselineTree = errors.New("no ::gentoo tree to compare against")
//	func LocateBaselineTree(candidate string) (root string, err error)
//
//	// on CompareReport
//	BaselineSkipped string // non-empty: the review was SKIPPED, and the text
//	                       // names what was looked for. Zero value renders nothing.
//
// Two states this sub-task must keep apart, and the whole fragment is about the
// difference:
//
//   - No TREE is a RUN-level outcome. Nothing was examined, the review could not
//     do its job, and D9 makes this the one condition the command exits non-zero
//     on. It is reported as SKIPPED naming the path, and it returns a sentinel
//     the caller can branch an exit code off.
//   - A tree that exists but whose ebuild cannot be read is a PER-PACKAGE state
//     (1.1's Unexamined), the run continues, and the exit code stays 0 —
//     verifyAgainstLocalContent already behaves that way and D2 says
//     ResolveBaseline follows it.
//
// The exit code itself belongs to cmd/bentoo and is asserted in 6.1. What this
// package owes is the distinction the caller branches on: a distinct error for
// the run-level case and a nil error for the per-package one. Asserting only
// "an error came back" would be satisfied by an implementation that returned the
// same error for both, which is exactly how a per-package non-answer would start
// failing whole runs.

// TestLocateBaselineTreeNamesWhatItLookedFor is R1.5. "Not found" without a path
// is unactionable: the operator cannot tell a mistyped configuration from a tree
// that genuinely is not installed, and those need opposite fixes.
//
// _Requirements: R1, R1.5_
func TestLocateBaselineTreeNamesWhatItLookedFor(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "var", "db", "repos", "gentoo")

	root, err := LocateBaselineTree(missing)

	if err == nil {
		t.Fatalf("LocateBaselineTree(%q) returned root %q and no error; a tree that is not there must not read as an empty clean one", missing, root)
	}
	if !errors.Is(err, ErrNoBaselineTree) {
		t.Errorf("the error is %v, want it to wrap ErrNoBaselineTree — the caller derives a non-zero exit from THIS condition and from no other (D9), and it cannot do that by matching on a message", err)
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("the error is %q, want it to name the path it looked for (%s) — 'no baseline tree' that cannot say where it looked leaves a typo and an uninstalled tree indistinguishable (R1.5)", err.Error(), missing)
	}
}

// TestLocateBaselineTreeAcceptsARealTree is the control. Without it the test
// above passes on an implementation that never finds anything.
//
// _Requirements: R1, R1.4, R1.5_
func TestLocateBaselineTreeAcceptsARealTree(t *testing.T) {
	tree := baselineTree(t, "1.29.2")

	root, err := LocateBaselineTree(tree)
	if err != nil {
		t.Fatalf("LocateBaselineTree(%q) = %v, want nil for a tree that is right there", tree, err)
	}
	if root != tree {
		t.Errorf("located %q, want %q — the review must name the tree it actually used, not a default it fell back to", root, tree)
	}
}

// TestMissingBaselineTreeIsSkippedNotClean is the failure mode the sub-task
// exists to prevent: a review that could not look reporting the same thing as a
// review that looked and found nothing wrong.
//
// The assertion is on the report, because that is where the two would become
// indistinguishable. `BaselineSkipped` carries the path, so the SKIPPED line can
// name what was looked for without the renderer having to learn about flags —
// the same zero-value discipline every field task 6.3 adds is held to.
//
// _Requirements: R1, R1.5_
func TestMissingBaselineTreeIsSkippedNotClean(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no", "such", "tree")
	report := &CompareReport{
		TotalPackages:    1,
		ComparedPackages: 1,
		Results: []CompareResult{{
			Category: baselineCategory, Package: baselinePkg,
			LocalVersion: "1.29.2", Status: StatusUpToDate,
		}},
	}

	MarkBaselineSkipped(report, missing)

	if report.BaselineSkipped == "" {
		t.Fatal("BaselineSkipped is empty after a run with no baseline tree; the report then reads exactly like a clean one, which is the whole failure R1.5 names")
	}
	if !strings.Contains(report.BaselineSkipped, missing) {
		t.Errorf("BaselineSkipped is %q, want it to name %s", report.BaselineSkipped, missing)
	}

	rendered := FormatReport(report)
	if !strings.Contains(rendered, "SKIPPED") {
		t.Errorf("the rendered report never says SKIPPED:\n%s\nstory 031's rule is that every outcome names its own reach, and silence here claims a reach the run did not have", rendered)
	}
	if !strings.Contains(rendered, missing) {
		t.Errorf("the rendered report never names the path that was looked for:\n%s", rendered)
	}

	// And the zero value still renders nothing, so a plain compare is untouched.
	clean := &CompareReport{TotalPackages: 1, ComparedPackages: 1, Results: report.Results}
	if strings.Contains(FormatReport(clean), "SKIPPED") {
		t.Error("a report with BaselineSkipped empty renders a SKIPPED line; R7.2 says a run that requested no review prints what it printed yesterday")
	}
}

// TestMissingTreeAndUnreadableEbuildAreDifferentOutcomes is the discriminating
// case, and the one that keeps 1.2 from swallowing 1.1's per-package state.
//
// Collapsing them is easy and quiet: one `if err != nil { return err }` in the
// wrong place turns every unreadable ebuild into a failed run, and 84 packages
// with no counterpart plus any permission oddity on the host would then exit
// non-zero forever.
//
// _Requirements: R1, R1.3, R1.5_
func TestMissingTreeAndUnreadableEbuildAreDifferentOutcomes(t *testing.T) {
	// Run-level: the tree is not there at all.
	_, runErr := LocateBaselineTree(filepath.Join(t.TempDir(), "absent"))
	if !errors.Is(runErr, ErrNoBaselineTree) {
		t.Fatalf("a missing tree returned %v, want ErrNoBaselineTree; the comparison below is meaningless without it", runErr)
	}

	// Per-package: the tree is there, the ebuild is not readable. A DIRECTORY
	// stands where the file must be — `act` runs as root and root reads a 000
	// file happily, so a permission fixture is a false green in CI.
	root := t.TempDir()
	blocked := filepath.Join(root, baselineCategory, baselinePkg, baselinePkg+"-1.29.2.ebuild")
	if err := os.MkdirAll(blocked, 0o750); err != nil {
		t.Fatalf("planting the unreadable baseline: %v", err)
	}
	got, pkgErr := ResolveBaseline(root, baselineAtom, "1.29.2")

	if pkgErr != nil {
		t.Errorf("an unreadable ebuild returned %v, want nil; absence of evidence is not evidence, verifyAgainstLocalContent already exits 0 on it, and making it an error regresses today's behaviour (D2)", pkgErr)
	}
	if errors.Is(pkgErr, ErrNoBaselineTree) {
		t.Error("an unreadable ebuild returned ErrNoBaselineTree; the caller would exit non-zero on a per-package non-answer, and 84 packages have no counterpart at all")
	}
	if got.Unexamined == "" {
		t.Error("Unexamined is empty for the unreadable ebuild, so the per-package state was reported as nothing at all")
	}
}

// MERGE FRAGMENT — story 034, sub-task 6.2 (packages ::gentoo does not carry).
//
// Target file: internal/overlay/baseline_test.go  (APPEND, after 1.2's fragment)
// Reused and never re-declared: `baselineTree`, `baselineAtom`,
// `baselineCategory`, `baselinePkg`, `baselineEbuildBody` (1.1's fragment),
// `writeVerifyEbuild` (compare_verification_test.go), `needsRealignVerdict`
// (5.1's fragment).
//
// Pinned contract:
//
//	type LocalRepo struct {
//	    Name, Path string
//	    Available  bool // its CONTENTS are on disk here; "registered" is not "available"
//	}
//	type OtherRepo struct {
//	    Name, Version string
//	    Checked       bool // false: not consulted. Never "does not carry it".
//	}
//	func OtherRepositories(atom string, repos []LocalRepo) []OtherRepo
//
//	// on CompareResult
//	Others []OtherRepo // renders nothing at its zero value, like every field 6.3 adds
//
// That last field is a GAP this fragment closes rather than copies. design.md's
// carrier lists Baseline, Axes, Classified, Declarations and RealignVerdict, and
// task 6.3 adds exactly those — but 6.2 produces per-package rows for the other
// repositories and there is nowhere on the result to put them. Either 6.3 grows
// a sixth field or 6.2's output reaches the report by some route the design does
// not describe; the first is the smaller change and is what is pinned here.
//
// `Checked` is the whole sub-task in one field. ~428 repositories are resolvable
// by name and almost none of them are on disk; asking about 84 packages across
// all of them would be thousands of network lookups in a stage the story
// promises is offline. So a repository that was not consulted is reported as NOT
// CHECKED, and a bool that only said "carries / does not carry" could not express
// that — which is why the assertions below test the three-way outcome and not
// just the two-way one.

const otherRepoAtom = "app-editors/zed"

// otherRepoTree writes a repository carrying app-editors/zed at one version.
func otherRepoTree(t *testing.T, version string) string {
	t.Helper()
	root := t.TempDir()
	writeVerifyEbuild(t, root, "app-editors", "zed", version, "EAPI=8\nDESCRIPTION=\"Zed editor\"\n")
	return root
}

// TestOtherRepositoriesAreInformativeOnly is R6.1 and R6.2 together. Knowing
// that GURU packages something we package alone is useful; treating GURU as a
// baseline is not, because a repository outside ::gentoo has not been through
// the same review and its quality varies per repository.
//
// The "no proposal" half is asserted through `needsRealignVerdict`, the same
// predicate task 5.1 uses to decide who the model is asked about. Asserting on
// the predicate rather than on rendered text means an informative row cannot
// become a proposal by some later change to the renderer.
//
// _Requirements: R6, R6.1, R6.2_
func TestOtherRepositoriesAreInformativeOnly(t *testing.T) {
	guru := otherRepoTree(t, "0.150.0")

	got := OtherRepositories(otherRepoAtom, []LocalRepo{{Name: "guru", Path: guru, Available: true}})

	if len(got) != 1 {
		t.Fatalf("got %d rows, want 1 — GURU carries the package and that is worth reporting: %+v", len(got), got)
	}
	if got[0].Name != "guru" || got[0].Version != "0.150.0" {
		t.Errorf("row is %+v, want guru at 0.150.0", got[0])
	}
	if !got[0].Checked {
		t.Error("Checked is false for a repository whose contents were read; the report would say 'not checked' about the one repository it did check")
	}

	// No ::gentoo baseline, so no realignment is ever proposed — R1.3 and R6.2
	// reaching the same conclusion by different routes.
	res := CompareResult{
		Category: "app-editors", Package: "zed",
		Baseline: Baseline{Found: false},
		Others:   got,
	}
	if needsRealignVerdict(res) {
		t.Error("a package carried only by another repository is queued for a realignment verdict; R6.2 makes another repository informative only, and a proposal built from one would realign towards a tree nobody reviewed")
	}
}

// TestOtherRepositoriesNameEachAndChooseBetweenNone is R6.3. The instruction is
// unusually explicit — "choose between them for no purpose" — because picking a
// winner is the natural next line of code and it would quietly create the
// second-baseline concept the story rejects.
//
// _Requirements: R6, R6.1, R6.3_
func TestOtherRepositoriesNameEachAndChooseBetweenNone(t *testing.T) {
	guru := otherRepoTree(t, "0.150.0")
	other := otherRepoTree(t, "0.151.0")

	got := OtherRepositories(otherRepoAtom, []LocalRepo{
		{Name: "guru", Path: guru, Available: true},
		{Name: "steamos", Path: other, Available: true},
	})

	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2 — both repositories carry it and both are named: %+v", len(got), got)
	}
	names := map[string]bool{}
	for _, r := range got {
		names[r.Name] = true
		if !r.Checked {
			t.Errorf("%s is reported as not checked although its contents were read", r.Name)
		}
	}
	for _, want := range []string{"guru", "steamos"} {
		if !names[want] {
			t.Errorf("no row names %s; R6.3 names each and ranks none", want)
		}
	}

	res := CompareResult{Category: "app-editors", Package: "zed", Baseline: Baseline{Found: false}, Others: got}
	if needsRealignVerdict(res) {
		t.Error("two informative rows produced a realignment candidate; more evidence from repositories that are not ::gentoo is still not a baseline (R6.2)")
	}
}

// TestOtherRepositoriesReportNotCheckedRatherThanNotCarrying is the distinction
// this sub-task exists for.
//
// "steamos does not carry it" and "steamos was never asked" are different
// answers and only one of them is true. Collapsing them would let the report
// claim, on the strength of ~428 repositories nobody queried, that a package is
// ours alone.
//
// _Requirements: R6, R6.1_
func TestOtherRepositoriesReportNotCheckedRatherThanNotCarrying(t *testing.T) {
	guru := otherRepoTree(t, "0.150.0")
	// Available locally, and genuinely does not carry the package.
	empty := t.TempDir()

	got := OtherRepositories(otherRepoAtom, []LocalRepo{
		{Name: "guru", Path: guru, Available: true},
		{Name: "empty-but-present", Path: empty, Available: true},
		{Name: "registered-only", Path: filepath.Join(t.TempDir(), "not-on-disk"), Available: false},
	})

	byName := map[string]OtherRepo{}
	for _, r := range got {
		byName[r.Name] = r
	}

	carrying, ok := byName["guru"]
	if !ok || carrying.Version == "" {
		t.Errorf("guru is missing or carries no version: %+v", byName)
	}

	// Checked and empty-handed: a real negative.
	checkedEmpty, ok := byName["empty-but-present"]
	if !ok {
		t.Fatalf("the repository that was checked and did not carry the package has no row at all: %+v", got)
	}
	if !checkedEmpty.Checked || checkedEmpty.Version != "" {
		t.Errorf("empty-but-present is %+v, want Checked=true with no version — it was read and it does not carry it", checkedEmpty)
	}

	// Not consulted: no claim either way.
	unchecked, ok := byName["registered-only"]
	if !ok {
		t.Fatalf("a registered repository whose contents are not on disk has no row; 'we did not look' is an answer the report owes (R6.1): %+v", got)
	}
	if unchecked.Checked {
		t.Error("registered-only is reported as checked although nothing on disk holds its contents; 'reachable by name' is not 'queried'")
	}
	if unchecked.Version != "" {
		t.Errorf("registered-only reports version %q without having been read", unchecked.Version)
	}
	if checkedEmpty.Checked == unchecked.Checked {
		t.Error("a repository that was read and came up empty is indistinguishable from one that was never consulted; those are different answers and only one is true (R6.1)")
	}
}

// TestOtherRepositoriesMakeNoNetworkCall holds the stage's offline promise
// mechanically: PATH is emptied so no `git`, `wget` or `curl` is reachable, and
// the repositories are directories.
//
// This is the assertion that stops the obvious "improvement" — consulting the
// registry for the other ~428 — from arriving unnoticed as thousands of lookups.
//
// _Requirements: R6, R6.1_
func TestOtherRepositoriesMakeNoNetworkCall(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	guru := otherRepoTree(t, "0.150.0")

	got := OtherRepositories(otherRepoAtom, []LocalRepo{{Name: "guru", Path: guru, Available: true}})

	if len(got) != 1 || got[0].Version != "0.150.0" {
		t.Errorf("got %+v with PATH empty, want guru at 0.150.0 read straight off the disk", got)
	}
}

// TestOtherRepositoriesDoNotReadWhatTheCallerDidNotOfferAsAvailable is ADDED BY
// SUB-TASK 6.2, beside the fragment above rather than inside it, and it closes a
// gap mutation testing found in the fragment's own not-checked case.
//
// There the unavailable repository's path does not exist either, so an
// implementation that ignored Available and went to the disk anyway still
// reports "not checked" — the stat simply fails. The guard that matters is
// therefore proved by the fixture only by accident.
//
// Here the path is a real tree that really carries the package. Available is
// false all the same, and the answer owed is still NOT CHECKED: this pass reads
// what the caller says is here and never goes looking, which is the only reason
// it can promise to be offline across ~428 registered repositories.
//
// _Requirements: R6, R6.1_
func TestOtherRepositoriesDoNotReadWhatTheCallerDidNotOfferAsAvailable(t *testing.T) {
	carried := otherRepoTree(t, "0.150.0")

	got := OtherRepositories(otherRepoAtom, []LocalRepo{{Name: "guru", Path: carried, Available: false}})

	if len(got) != 1 {
		t.Fatalf("got %d rows, want 1 — a repository the caller named still owes an answer: %+v", len(got), got)
	}
	if got[0].Checked {
		t.Error("a repository the caller did not offer as available was read anyway; Available is what makes this pass offline, and consulting a path it did not vouch for is the first step towards consulting all ~428")
	}
	if got[0].Version != "" {
		t.Errorf("Version is %q for a repository that was not checked; the tree does carry that version, which is exactly why reporting it here would be reporting something nobody was asked to look at", got[0].Version)
	}
}

// TestNoBaselineCountIsReportedWithItsDenominator is R6.4. The count on its own
// answers "how many packages ::gentoo does not carry"; with its denominator it
// answers "how much of the overlay this review could not measure", which is the
// question the number is actually for.
//
// _Requirements: R6, R6.4_
func TestNoBaselineCountIsReportedWithItsDenominator(t *testing.T) {
	report := &CompareReport{
		TotalPackages:    3,
		ComparedPackages: 3,
		NoBaselineCount:  1,
		Results: []CompareResult{
			{Category: baselineCategory, Package: baselinePkg, Baseline: Baseline{Repo: "gentoo", Version: "1.29.2", Found: true}},
			{Category: "sys-devel", Package: "binutils", Baseline: Baseline{Repo: "gentoo", Version: "2.46", Found: true}},
			{Category: "app-editors", Package: "zed", Baseline: Baseline{Found: false}},
		},
	}

	rendered := FormatReport(report)

	if !strings.Contains(rendered, "1") || !strings.Contains(rendered, "3") {
		t.Errorf("the report does not state the no-baseline count with the number of packages examined:\n%s", rendered)
	}
	// The zero value stays silent, so a plain compare is untouched (R7.2). The
	// shipped report has never used the word "baseline", so its absence is a
	// usable proxy for "this story's lines did not render".
	quiet := &CompareReport{TotalPackages: 3, ComparedPackages: 3}
	quiet.Results = append([]CompareResult(nil), report.Results...)
	for i := range quiet.Results {
		quiet.Results[i].Baseline = Baseline{}
		quiet.Results[i].Others = nil
	}
	if strings.Contains(strings.ToLower(FormatReport(quiet)), "baseline") {
		t.Errorf("a report with no baseline data renders a baseline line; a run that requested no review must print what it printed yesterday (R7.2):\n%s", FormatReport(quiet))
	}
}

// TestNoBaselineCountCountsTheAbsentBaselineAndNotTheStatus is ADDED BY SUB-TASK
// 6.2, beside the fragment above rather than inside it, and it closes a gap
// sub-task 6.3 measured and reported: countNoBaseline rewritten to count
// `Status == StatusNotInRemote` instead of `!Baseline.Found` passes the entire
// suite.
//
// The cause is that every fixture correlates the two. In annotateFixtureTrees
// ONE map decides both — a package the map marks absent gets no provider version
// (so the comparison calls it not-in-remote) AND no ::gentoo ebuild (so the
// baseline is not found) — which makes the two predicates indistinguishable in
// every tree the suite builds.
//
// They are not the same question. R6.4 asks how much of the overlay the review
// could not measure, which is a fact about ::gentoo; the status is a fact about
// what the comparison managed to do. The table below holds a row where they part
// company, and it is a state production reaches: a ::gentoo package directory
// that exists and will not list leaves Found false with Unexamined set
// (ResolveBaseline), while the comparison that hit the same failure reports
// StatusError rather than StatusNotInRemote.
//
// _Requirements: R6, R6.4, R1.3_
func TestNoBaselineCountCountsTheAbsentBaselineAndNotTheStatus(t *testing.T) {
	results := []CompareResult{
		// Carried by ::gentoo and compared: neither definition counts it.
		{
			Category: baselineCategory, Package: baselinePkg, Status: StatusUpToDate,
			Baseline: Baseline{Repo: baselineRepo, Version: "1.29.2", Found: true},
		},
		// One of the 84, and the correlated case both definitions agree on.
		{
			Category: "app-editors", Package: "zed", Status: StatusNotInRemote,
			Baseline: Baseline{Found: false},
		},
		// The divergent row: no baseline was named, and the status is not
		// not-in-remote. Counting by status misses it and reports 1.
		{
			Category: "sys-devel", Package: "binutils", Status: StatusError,
			Baseline: Baseline{Found: false, Unexamined: "the ::gentoo package directory could not be listed"},
		},
	}

	if got := countNoBaseline(results); got != 2 {
		t.Errorf("countNoBaseline is %d, want 2 — rows 2 and 3 name no ::gentoo baseline, and a count that reads the comparison's status instead of Baseline.Found answers a different question and reports 1", got)
	}
}

// MERGE FRAGMENT — story 036, sub-task 1.1 (`versionValue`, `versionDistance`).
//
// Target file: internal/overlay/baseline_test.go  (APPEND, at the very end of
// the file — after story 034's three fragments, the last of which ends with
// TestNoBaselineCountCountsTheAbsentBaselineAndNotTheStatus). Do NOT repeat the
// `package overlay` clause: it is already at the top of the target.
//
// # IMPORT MERGE — the one mechanical thing to get right
//
// This fragment needs "github.com/obentoo/bentoolkit/internal/common/ebuild"
// added to baseline_test.go's EXISTING import block (today: errors, os,
// path/filepath, strings, testing). Merge it into that block; do not open a
// second one, and do not leave it unused. Story 034 lost a Run-mode cycle to a
// dead import in a merged fragment, which is why this is stated first.
//
// # Symbols
//
// Added, all prefixed `versionMetric` so nothing can collide with the three
// story-034 fragments already in this file: `versionMetricLadders`,
// `versionMetricPool`, and the five Test functions below — named so that the
// sub-task's own validation command, `-run 'TestVersionDistance|TestVersionValue'`,
// selects every one of them.
//
// Borrowed, never re-declared: `ebuild.CompareVersions` — the repository's own
// version ordering, used ONLY to prove the fixtures' ladders really are
// ascending. The metric under test never decides which of two versions is
// greater; ordering stays with CompareVersions, and this fragment keeps that
// separation on both sides.
//
// # PINNED CONTRACT (design.md "Components & Interfaces", D1)
//
//	func versionValue(version string) int   // NEW — the version's position on the
//	                                        // existing significance ladder,
//	                                        // clamped per level
//	func versionDistance(a, b string) int   // CHANGED — |value(a) - value(b)|,
//	                                        // same name, signature and unit
//
// Both are UNEXPORTED, so this is an in-package test by necessity, not by
// choice.
//
// # What this fragment exists to close
//
// MONOTONICITY ALONG THE VERSION ORDER. Story 034 recorded, in its own deviation
// register, that the ladder's direction rested on documentation rather than on
// an assertion: inverting the weights left its suite green. So the defect this
// story fixes was never pinned by anything, and the eleven wrong baselines are
// its visible half. The triple loop below is the whole point of the file — for
// any three versions in release order, stepping further away must never report a
// smaller number.
//
// # A deliberate boundary on that claim, argued rather than hidden
//
// Monotonicity is asserted over ladders whose components all sit INSIDE the
// ladder's radix (minor, patch and finer each below 100, which is 1_000_000 /
// 10_000 and 10_000 / 100). That is not a convenience: a mixed-radix positional
// value is monotone exactly while no component carries into the level above it,
// so `1.2.150` would out-value `1.3.0` and no implementation of D1 could pass a
// ladder containing it. D1 says as much when it notes that `1.32.2` has minor 32
// and is "still well inside the radix", and the per-level clamp exists for the
// malformed rest. Every version in M-2 is inside it. Asserting more than that
// would be authoring a test the design cannot satisfy, so the ladders are chosen
// where the property genuinely holds and this comment says where that ends.

// versionMetricLadders are ascending version ladders, one per package the
// measurement (story.md M-2) names, plus runc — whose 1.4.2 / 1.4.3 / 1.5.1 is
// the shape design D1 works through line by line.
//
// They are FIXTURES and never a live query, for the reason the whole testing
// strategy gives: what ::gentoo carries tomorrow must not change what this suite
// means.
//
// Each ladder is asserted to be ascending before it is used, by the repository's
// own comparison, so a typo in a version string fails as a broken fixture rather
// than as a broken metric.
func versionMetricLadders() map[string][]string {
	return map[string][]string{
		// D1's worked example, and the plainest reading of the defect: 1.4.2 is
		// reported nearer to 1.5.1 than 1.4.3 is, although 1.4.3 is strictly
		// greater and therefore fewer releases away.
		"runc": {"1.3.6", "1.4.2", "1.4.3", "1.5.1"},
		// The mechanism at its worst: 12.3 wins on a minor digit that MATCHES
		// ours across two different major lines.
		"nvidia-cuda-toolkit": {"12.3.2", "12.9.2", "13.3.1"},
		// A minor component well above 9, which is where a digit-by-digit
		// comparison stops meaning anything at all.
		"fakeroot": {"1.32.2", "1.33", "2.1.4"},
		// A zero major line, where every difference lives in the minor and patch
		// components.
		"sentry-native": {"0.7.2", "0.7.6", "0.16.2"},
		// mesa's real trio. Ours is 26.3.0_pre20260810 in the overlay; the
		// suffix is invisible to the ladder by design, so it lands exactly where
		// 26.3.0 does and is written that way here.
		"mesa": {"26.1.4", "26.1.6", "26.3.0"},
	}
}

// versionMetricPool is a flat set of versions for the properties that are about
// PAIRS rather than about order: symmetry, non-negativity, and zero-iff-identical.
//
// It deliberately mixes the shapes the ladder truncates — a revision, a
// pre-release suffix, a version that stops short — with ordinary ones, because
// those are the pairs where a metric quietly returns 0 for two different
// ebuilds.
func versionMetricPool() []string {
	return []string{
		"1.0", "1.0.0", "1.29", "1.29.1", "1.29.2",
		"2.52.5", "2.52.5-r410", "2.52.5-r601",
		"4.7", "4.7.1", "4.8", "4.8_alpha3",
		"12.9.2", "13.3.1", "26.1.6", "26.3.0",
	}
}

// TestVersionValuePositionsAVersionOnTheLadder pins D1's arithmetic directly:
// a version read as a number in the mixed-radix positional system the existing
// significance ladder already defines — major 1_000_000, minor 10_000, patch
// 100, finer 1.
//
// The numbers are pinned rather than described because R1.5 requires the UNIT to
// survive: `versionDistance` feeds both the baseline distance and the
// reduction's span, and the width bound compares the two against each other. Two
// numbers in different units are not comparable at all, so a rescaled ladder is
// a silent regression in `ReduceDiff` that nothing in reduce_test.go would
// catch.
//
// _Requirements: R1, R1.3, R1.5_
func TestVersionValuePositionsAVersionOnTheLadder(t *testing.T) {
	cases := []struct {
		version string
		want    int
		why     string
	}{
		{"1.5.1", 1_050_100, "D1's worked example: 1*1e6 + 5*1e4 + 1*100"},
		{"1.4.2", 1_040_200, "D1's worked example"},
		{"1.4.3", 1_040_300, "D1's worked example, and the version the old metric ranked further away"},
		{"1.29.2", 1_290_200, "the version the shipped suite measures its distances from"},
		{"1.29", 1_290_000, "a version that stops short is the same point as 1.29.0 — componentAt already says so"},
		{"2.52.5-r601", 2_520_500, "a revision is invisible to the ladder by design; the caller's tie-break is what orders two of them"},
		{"4.8_alpha3", 4_080_000, "and so is a pre-release suffix — versionComponents truncates at the first non-digit-non-separator"},
	}

	for _, tc := range cases {
		t.Run(tc.version, func(t *testing.T) {
			if got := versionValue(tc.version); got != tc.want {
				t.Errorf("versionValue(%q) is %d, want %d — %s. The ladder is major 1_000_000, minor 10_000, patch 100, finer 1, and R1.5 requires it to stay in that unit because the reduction's width bound compares a span against the baseline distance",
					tc.version, got, tc.want, tc.why)
			}
		})
	}

	// The two distances D1 derives from those values, in the same breath. They
	// are the whole defect in two numbers: 1.4.3 must come out NEARER to 1.5.1
	// than 1.4.2 is, and the shipped metric reports the opposite.
	nearer, farther := versionDistance("1.5.1", "1.4.3"), versionDistance("1.5.1", "1.4.2")
	if nearer != 9_800 {
		t.Errorf("versionDistance(1.5.1, 1.4.3) is %d, want 9_800 — |1_050_100 - 1_040_300| (D1)", nearer)
	}
	if farther != 9_900 {
		t.Errorf("versionDistance(1.5.1, 1.4.2) is %d, want 9_900 — |1_050_100 - 1_040_200| (D1)", farther)
	}
	if nearer >= farther {
		t.Errorf("1.4.3 is %d release steps from 1.5.1 and 1.4.2 is %d; the greater version is fewer releases away and the metric must say so, or the baseline lands further back than necessary and every difference those releases introduced is reported as ours (R1.2)", nearer, farther)
	}

	// The two distances the sub-task's own plan predicts for the shipped suite,
	// pinned here so a rescaled ladder is caught in one place rather than in
	// whichever test happens to break first: `the distance grows with the gap`
	// (baseline_test.go) becomes 100 against 90_200, and the reduction's width
	// bound keeps refusing a third point at 190_000 against 200.
	if got := versionDistance("1.29.2", "1.29.1"); got != 100 {
		t.Errorf("versionDistance(1.29.2, 1.29.1) is %d, want 100 — one patch release, the unit the report prints as `release steps`", got)
	}
	if got := versionDistance("1.29.2", "1.20.0"); got != 90_200 {
		t.Errorf("versionDistance(1.29.2, 1.20.0) is %d, want 90_200 — nine minor series and two patch releases", got)
	}
}

// TestVersionDistanceIsMonotoneAlongTheVersionOrder is the property the shipped
// metric FAILS and which no test in story 034 pins. It is the reason eleven
// baselines are not the nearest version.
//
// The claim, stated so a reader can check it against the code: for any three
// versions in release order, stepping further away along the order must report a
// strictly larger distance — measured from below AND from above, because the
// defect is directional and a one-sided assertion would miss half of it.
//
// It asserts the ORDERING of two measurements rather than either measurement's
// value, so a change to the ladder's scale cannot fail it and an inversion of
// the ladder's direction cannot pass it. That is exactly the mutation story
// 034's suite survived.
//
// _Requirements: R1, R1.2, R1.3_
func TestVersionDistanceIsMonotoneAlongTheVersionOrder(t *testing.T) {
	for name, ladder := range versionMetricLadders() {
		t.Run(name, func(t *testing.T) {
			// The fixture's own premise first: if the ladder is not ascending by
			// the repository's own comparison, everything below is measuring a
			// typo.
			for i := 1; i < len(ladder); i++ {
				if ebuild.CompareVersions(ladder[i], ladder[i-1]) <= 0 {
					t.Fatalf("the ladder is not ascending: %q does not order above %q by ebuild.CompareVersions — the fixture is wrong, not the metric", ladder[i], ladder[i-1])
				}
			}

			for i := range ladder {
				for j := i + 1; j < len(ladder); j++ {
					for k := j + 1; k < len(ladder); k++ {
						low, mid, high := ladder[i], ladder[j], ladder[k]

						// From below: the middle version is nearer to the lowest
						// than the highest is.
						if near, far := versionDistance(low, mid), versionDistance(low, high); near >= far {
							t.Errorf("from %s: %s is %d release steps away and %s is %d, but %s comes first in release order — a version further along the line must never be reported as nearer, which is precisely how a baseline six minor releases too far back gets chosen (R1.2)",
								low, mid, near, high, far, mid)
						}

						// From above, which is the direction the baseline is
						// actually chosen in: ours is the top of the ladder and
						// the candidates are below it.
						if near, far := versionDistance(high, mid), versionDistance(high, low); near >= far {
							t.Errorf("from %s: %s is %d release steps away and %s is %d, but %s is the greater of the two and therefore fewer releases back — this is the nvidia-cuda-toolkit case, where a lower-significance digit that matches ours decides across a higher-significance difference (R1.2)",
								high, mid, near, low, far, mid)
						}
					}
				}
			}
		})
	}
}

// TestVersionDistanceIsZeroOnlyForTheSameVersionString is the identity property,
// and it is load-bearing rather than tidy: 0 is what the report renders as "the
// same version", and `ReduceDiff` reads 0 as "no version move can explain
// anything here".
//
// Identity is STRING equality and not `ebuild.CompareVersions`, on the
// constraint story.md states: that comparison pads missing components with
// zeros, so `1.0` and `1.0.0` order equal while being two different ebuilds.
// The `distance == 0 -> 1` floor is what keeps them apart, and it must survive
// D1 unchanged.
//
// _Requirements: R1, R1.3, R1.5_
func TestVersionDistanceIsZeroOnlyForTheSameVersionString(t *testing.T) {
	pool := versionMetricPool()

	for _, version := range pool {
		if got := versionDistance(version, version); got != 0 {
			t.Errorf("versionDistance(%q, %q) is %d, want 0 — a version is zero release steps from itself, and the report prints 0 as `the same version`", version, version, got)
		}
	}

	for i := range pool {
		for j := i + 1; j < len(pool); j++ {
			if got := versionDistance(pool[i], pool[j]); got == 0 {
				t.Errorf("versionDistance(%q, %q) is 0 for two DIFFERENT version strings; 0 means `the baseline is our own ebuild`, and a report that cannot tell those apart cannot tell a real divergence from an artefact of comparing against the wrong file",
					pool[i], pool[j])
			}
		}
	}
}

// TestVersionDistanceIsSymmetricAndNeverNegative pins the two properties the
// callers take for granted without ever stating them.
//
// Negative is the dangerous one. `pickBaseline` takes the MINIMUM, so a distance
// that went negative — through an overflow the per-level clamp exists to
// prevent — would read as the nearest baseline of all and win silently, and
// `reduceSpan`'s width bound would then compare against it.
//
// _Requirements: R1, R1.3, R1.5_
func TestVersionDistanceIsSymmetricAndNeverNegative(t *testing.T) {
	pool := versionMetricPool()

	for i := range pool {
		for j := range pool {
			forward, backward := versionDistance(pool[i], pool[j]), versionDistance(pool[j], pool[i])
			if forward != backward {
				t.Errorf("versionDistance(%q, %q) is %d but versionDistance(%q, %q) is %d; the gap between two versions cannot depend on which of them is named first",
					pool[i], pool[j], forward, pool[j], pool[i], backward)
			}
			if forward < 0 {
				t.Errorf("versionDistance(%q, %q) is %d — a negative distance reads as the nearest baseline of all and wins pickBaseline's minimum silently, which is what the per-level clamp exists to prevent",
					pool[i], pool[j], forward)
			}
		}
	}
}

// TestVersionDistanceLeavesRevisionsAndSuffixesEquidistant states, as an
// assertion, the design decision D1 makes in prose: the ladder truncates at the
// first character that is neither a digit nor a separator, so two versions
// differing only by a revision or a `_suffix` land at the SAME point and are
// equidistant from any third version. The caller's tie-break is what orders
// them, and `pickBaseline` still takes the newer (R1.4).
//
// This is why `2.52.5-r601` and `2.52.5-r410` are NOT among the eleven, and
// pinning it stops the obvious "improvement" — teaching the ladder about
// revisions — from arriving as an unnoticed change to the unit the width bound
// is measured in.
//
// _Requirements: R1, R1.3, R1.4_
func TestVersionDistanceLeavesRevisionsAndSuffixesEquidistant(t *testing.T) {
	cases := []struct {
		name string
		// same and other are two version strings the ladder cannot separate:
		// they differ only below the numeric components.
		same, other string
		// third is the version both are measured against.
		third string
	}{
		{"two revisions of one version", "2.52.5-r410", "2.52.5-r601", "3.0"},
		{"a revision and the unrevised version", "2.52.5", "2.52.5-r601", "3.0"},
		{"a pre-release suffix and the release", "4.8_alpha3", "4.8", "4.7.1"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fromSame, fromOther := versionDistance(tc.same, tc.third), versionDistance(tc.other, tc.third)
			if fromSame != fromOther {
				t.Errorf("%q is %d release steps from %q and %q is %d; the ladder is truncated at the first non-digit-non-separator BY DESIGN, so these two land at the same point and the tie-break — not the measurement — is what orders them (D1, R1.4)",
					tc.same, fromSame, tc.third, tc.other, fromOther)
			}
			if got := versionDistance(tc.same, tc.other); got != 1 {
				t.Errorf("versionDistance(%q, %q) is %d, want 1 — two different ebuilds whose components agree are one step apart and never zero, because zero means `the same version`", tc.same, tc.other, got)
			}
		})
	}
}

// MERGE FRAGMENT — story 036, sub-task 1.2 (the eleven measured baselines).
//
// Target file: internal/overlay/baseline_test.go  (APPEND, immediately AFTER
// sub-task 1.1's fragment, which is itself appended after story 034's three).
// Do NOT repeat the `package overlay` clause.
//
// IMPORTS: none added. This fragment uses `testing` only, which the target file
// already imports. It does NOT need the `ebuild` import 1.1 merges in — if 1.2
// is materialised first for any reason, that import must not be added here.
//
// # Symbols
//
// Added, all prefixed `pickBaseline` so they cannot collide with 1.1's
// `versionMetric*` helpers or with story 034's `baseline*` ones:
// `pickBaselineCase`, `pickBaselineCandidates`, `pickBaselineTable`, and
// `TestPickBaselineChoosesTheNearestMeasuredVersion` — named so the sub-task's
// validation command, `-run 'TestPickBaseline'`, selects it.
//
// Borrowed, never re-declared: the production types `carriedEbuild`
// (baseline.go) and the function `pickBaseline` (baseline.go:582). Nothing here
// touches the disk, so `writeVerifyEbuild` and `baselineTree` are deliberately
// NOT used.
//
// # PINNED CONTRACT
//
//	type carriedEbuild struct{ version, filename string }   // UNCHANGED
//	func pickBaseline(carried []carriedEbuild, ours string) carriedEbuild  // UNCHANGED
//
// D2 is explicit that `pickBaseline` is NOT rewritten: its inputs get better.
// So this fragment adds no production code and asserts the selection through the
// function exactly as it stands today. If making it pass requires editing
// `pickBaseline`, the fix has been put in the wrong place — the arithmetic
// belongs in `versionDistance` (sub-task 1.1), which is the one place all four
// callers read.
//
// # Why a fixture and not the live tree
//
// The suite must not change meaning when ::gentoo syncs. The eleven rows are the
// measurement (story.md M-2) reduced to `(ours, carried versions, expected
// baseline)`, taken on 2026-08-11 against /var/db/repos/gentoo; they are frozen
// here so that a package that gains a version tomorrow cannot silently rewrite
// what this test claims. That is the testing strategy's own instruction, and it
// is also the only way the table can be read as evidence of the FIX rather than
// as a snapshot of the tree.
//
// # The control rows are not padding
//
// Four rows are cases the shipped metric ALREADY gets right: the exact-version
// match, the equidistant revision pair, a nearest-below choice, and a candidate
// on each side of ours. Without them a table of eleven failures is satisfied by
// a metric that simply inverted the answer, and inverting the answer is a real
// possibility here — the defect is directional.

// pickBaselineCase is one measured selection: what we carry, what ::gentoo
// carries, and which of those is the baseline.
type pickBaselineCase struct {
	// name is the atom the row was measured on, so a failure names the package
	// a maintainer can go and look at.
	name string
	// pkg is the package directory name, used only to build the filenames the
	// chosen candidate carries back.
	pkg string
	// ours is the version the overlay carries.
	ours string
	// carried are the versions ::gentoo carries, in the order a directory
	// listing might hand them over — deliberately not sorted, because a
	// selection that depended on listing order would report differently on
	// another filesystem.
	carried []string
	// want is the version that must be chosen.
	want string
	// why says what the row is evidence of.
	why string
}

// pickBaselineCandidates turns a row's versions into the candidates production
// would have built from a directory listing.
//
// The FILENAME is carried as well as the version, and it is asserted on below:
// `ResolveBaseline` builds `Baseline.Path` from the filename the listing
// produced, so a chooser that returned the right version beside another
// candidate's filename would name an ebuild nobody compared against.
func pickBaselineCandidates(pkg string, versions []string) []carriedEbuild {
	candidates := make([]carriedEbuild, 0, len(versions))
	for _, version := range versions {
		candidates = append(candidates, carriedEbuild{
			version:  version,
			filename: pkg + "-" + version + ".ebuild",
		})
	}
	return candidates
}

// pickBaselineTable is story.md M-2 in full, plus the controls.
func pickBaselineTable() []pickBaselineCase {
	return []pickBaselineCase{
		// ---- the eleven, measured 2026-08-11 (story.md M-2) ----
		{
			name: "dev-util/nvidia-cuda-toolkit", pkg: "nvidia-cuda-toolkit",
			ours: "13.3.1", carried: []string{"12.3.2", "12.9.2"}, want: "12.9.2",
			// LOAD-BEARING ROW. This is where the shipped metric is furthest
			// wrong, and it is wrong for a reason no adjacent-patch repair
			// touches: 12.3.2 wins because its minor digit 3 MATCHES ours
			// exactly, across a major-line difference those digits do not span.
			// A fix that only straightened out the 1.4.2 / 1.4.3 shape still
			// fails here, which is precisely why this row is in the table.
			// The baseline lands six minor releases further back than
			// necessary, and every difference those six releases introduced is
			// then reported as our divergence.
			why: "the load-bearing row: a matching lower-significance digit deciding across a major-line difference (R1.2)",
		},
		{
			name: "sys-apps/fakeroot", pkg: "fakeroot",
			ours: "2.1.4", carried: []string{"1.32.2", "1.33"}, want: "1.33",
			why: "a two-digit minor component, where a digit-by-digit comparison stops meaning anything",
		},
		{
			name: "media-plugins/frei0r-plugins", pkg: "frei0r-plugins",
			ours: "3.2.3", carried: []string{"2.3.3", "2.4.1-r1"}, want: "2.4.1-r1",
			why: "the nearest version carries a revision, which the ladder ignores and the choice must not",
		},
		{
			name: "sci-biology/foldingathome", pkg: "foldingathome",
			ours: "8.5.6-r2", carried: []string{"7.6.13-r1", "7.6.21"}, want: "7.6.21",
			why: "ours is revised too, and the revision must not enter the measurement on either side",
		},
		{
			name: "media-libs/opencv", pkg: "opencv",
			ours: "5.0.0", carried: []string{"4.11.0-r1", "4.12.0-r2"}, want: "4.12.0-r2",
			why: "a zero patch on our side, where the absolute per-component difference is at its most misleading",
		},
		{
			name: "dev-libs/sentry-native", pkg: "sentry-native",
			ours: "0.16.2", carried: []string{"0.7.2", "0.7.6"}, want: "0.7.6",
			why: "a zero major line: every difference lives in the minor and patch components",
		},
		{
			name: "media-libs/mesa", pkg: "mesa",
			ours: "26.3.0_pre20260810", carried: []string{"26.1.4", "26.1.6"}, want: "26.1.6",
			why: "a pre-release suffix on our side, truncated by the ladder and irrelevant to the choice",
		},
		{
			name: "dev-util/mesa_clc", pkg: "mesa_clc",
			ours: "26.3.0_pre20260810", carried: []string{"26.1.4", "26.1.6"}, want: "26.1.6",
			why: "mesa's companion package, measured separately because it is a separate row in the report",
		},
		{
			name: "net-misc/networkmanager", pkg: "networkmanager",
			ours: "1.58.0", carried: []string{"1.56.0", "1.56.1"}, want: "1.56.1",
			why: "one patch release apart, the narrowest of the eleven",
		},
		{
			name: "app-containers/runc", pkg: "runc",
			ours: "1.5.1", carried: []string{"1.3.6", "1.4.2", "1.4.3"}, want: "1.4.3",
			why: "design D1's worked example, and R1.1: with every candidate below ours, the GREATEST of them is the baseline",
		},
		{
			name: "dev-games/godot", pkg: "godot",
			ours: "4.8_alpha3", carried: []string{"4.7", "4.7.1"}, want: "4.7.1",
			why: "a candidate that stops short (4.7) against one that does not; 4.7 and 4.7.0 are one point on the ladder",
		},

		// ---- controls: rows the shipped metric ALREADY gets right ----
		{
			name: "control: ::gentoo carries our exact version", pkg: "gst-plugins-qt6",
			ours: "1.29.2", carried: []string{"1.29.1", "1.29.2", "1.29.3"}, want: "1.29.2",
			why: "story 034's R1.1, unchanged by this story: the same version is the answer and not a candidate among others, checked before any proximity is computed",
		},
		{
			name: "control: the nearest is plainly below ours", pkg: "gst-plugins-qt6",
			ours: "1.29.2", carried: []string{"1.20.0", "1.29.1"}, want: "1.29.1",
			why: "the shipped metric agrees here; the row is what proves the fix did not simply invert the answer",
		},
		{
			name: "control: candidates on both sides of ours", pkg: "gst-plugins-qt6",
			ours: "1.29.2", carried: []string{"1.28.0", "1.30.0"}, want: "1.30.0",
			why: "R1.2's other shape — 1.30.0 is 9_800 steps away and 1.28.0 is 10_200, so the nearer one wins whichever side it sits on",
		},
		{
			name: "control: equidistant candidates take the newer", pkg: "libxml2",
			ours: "2.52.5", carried: []string{"2.52.5-r410", "2.52.5-r601"}, want: "2.52.5-r601",
			why: "R1.4: the ladder cannot separate two revisions, so the tie-break decides — and it stays as it is today, taking the version ::gentoo still maintains",
		},
	}
}

// TestPickBaselineChoosesTheNearestMeasuredVersion pins the eleven packages
// whose baseline is not the nearest version ::gentoo carries, plus four rows the
// shipped metric already answers correctly.
//
// It asserts through `pickBaseline` rather than through `versionDistance`
// because the requirement is about the CHOICE: R1.2 says the system shall choose
// the version fewest releases away, and a metric that improved without changing
// any answer would satisfy a distance-only assertion while leaving all eleven
// baselines exactly where they are.
//
// _Requirements: R1, R1.1, R1.2, R1.4_
func TestPickBaselineChoosesTheNearestMeasuredVersion(t *testing.T) {
	for _, tc := range pickBaselineTable() {
		t.Run(tc.name, func(t *testing.T) {
			candidates := pickBaselineCandidates(tc.pkg, tc.carried)

			got := pickBaseline(candidates, tc.ours)

			if got.version != tc.want {
				t.Errorf("with ours at %s and ::gentoo carrying %v, the baseline chosen is %s and the nearest version is %s — %s.\nMeasured on the real trees on 2026-08-11 (story.md M-2); the baseline is measured from a fixture precisely so this claim does not change when ::gentoo syncs",
					tc.ours, tc.carried, got.version, tc.want, tc.why)
			}
			if want := tc.pkg + "-" + tc.want + ".ebuild"; got.version == tc.want && got.filename != want {
				t.Errorf("the chosen candidate is %s but its filename is %q, want %q — Baseline.Path is built from this filename, so the report would name an ebuild nobody compared against",
					got.version, got.filename, want)
			}
		})
	}
}
