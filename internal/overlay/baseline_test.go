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
