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
