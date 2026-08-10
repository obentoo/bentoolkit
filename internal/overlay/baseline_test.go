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
	"os"
	"path/filepath"
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
func baselineTree(t *testing.T, versions ...string) string {
	t.Helper()
	root := t.TempDir()
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
