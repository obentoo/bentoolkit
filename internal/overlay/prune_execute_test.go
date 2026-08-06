package overlay

import (
	"os"
	"path/filepath"
	"testing"
)

// This file pins R5.1 and R5.5, and the design's Security Considerations.
//
// It covers the only code in this repository where a path bug destroys data
// instead of returning an error. Everywhere else a wrong path opens the wrong
// file and something fails; here it calls os.RemoveAll on it.
//
// So the executor re-checks what the planner already decided. That is not
// belt-and-braces for its own sake — it is the observation that the plan reaches
// this function as a STRUCT, and a struct can be built by anything. The planner
// fills Category and Package from the overlay scan; nothing in the type system
// says a future caller will. `SplitPackageKey` accepts "../x" happily and no
// validation runs on a registry path, so the guard's job is to make the class of
// mistake unrepresentable at the point of no return rather than to distrust the
// planner specifically.
//
// Three refusals, each with its own reason for existing:
//
//   - A resolved target that is not exactly <root>/<category>/<package> is
//     refused. Two levels, no more, no fewer, no "..".
//   - A package directory that is itself a SYMLINK is refused rather than
//     followed. RemoveAll on a symlink removes the link — but the verification
//     that authorised the removal compared the tree the link points at, which
//     may be outside the overlay entirely. The authorisation and the deletion
//     would be about two different things.
//   - A plan that is not Eligible is never removed, whatever bucket it sits in.
//     ExecutePrune receives the whole batch, and the batch contains the packages
//     the run REFUSED; removing them is the single worst bug this file can have.
//
// And one continuation rule: one package's failure never stops the rest (R5.5).
// A batch that aborts halfway leaves the overlay in a state no plan described.

// prunePlanFor builds an eligible plan for <category>/<pkg>.
func prunePlanFor(category, pkg string) PrunePlan {
	return PrunePlan{
		Category: category,
		Package:  pkg,
		Class:    PruneIdentical,
		Eligible: true,
	}
}

// writePrunePackage lays down <root>/<category>/<pkg> with one ebuild and one
// file under files/, and returns the package directory.
func writePrunePackage(t *testing.T, root, category, pkg string) string {
	t.Helper()
	dir := filepath.Join(root, category, pkg)
	writePruneEbuild(t, dir, pkg, "1.0", pruneEbuildStock)
	writePruneFile(t, dir, filepath.Join("files", "a.patch"), "hunk\n")
	writePruneFile(t, dir, "Manifest", "DIST x 1 BLAKE2B aa SHA512 aa\n")
	return dir
}

func assertPruneDirGone(t *testing.T, dir, why string) {
	t.Helper()
	if _, err := os.Lstat(dir); err == nil {
		t.Errorf("%s still exists; %s", dir, why)
	}
}

func assertPruneDirAlive(t *testing.T, dir, why string) {
	t.Helper()
	if _, err := os.Lstat(dir); err != nil {
		t.Errorf("%s was removed (%v); %s", dir, err, why)
	}
}

// onlyResult fails unless exactly one result came back, and returns it.
func onlyResult(t *testing.T, got []PruneResult) PruneResult {
	t.Helper()
	if len(got) != 1 {
		t.Fatalf("ExecutePrune returned %d results, want exactly 1: %+v", len(got), got)
	}
	return got[0]
}

// TestExecutePruneRemovesTheWholeDirectory pins R5.1: the removal takes the
// package directory entire — every ebuild, Manifest, metadata.xml and files/.
//
// The second sub-test is the one that matters most in this file. ExecutePrune is
// handed the WHOLE batch, refusals included, because the caller has one batch and
// no reason to split it. An implementation that iterates every bucket and removes
// what it finds would pass every other test here and delete the packages the run
// explicitly refused. Eligible is the authorisation; the bucket is not.
//
// _Requirements: R5.1, R6.3_
func TestExecutePruneRemovesTheWholeDirectory(t *testing.T) {
	t.Run("an eligible package goes entirely", func(t *testing.T) {
		root := t.TempDir()
		dir := writePrunePackage(t, root, pruneCat, prunePkg)

		got := ExecutePrune(
			PruneBatch{Identical: []PrunePlan{prunePlanFor(pruneCat, prunePkg)}},
			PruneOptions{OverlayPath: root},
		)

		res := onlyResult(t, got)
		if !res.Removed {
			t.Errorf("Removed = false (err %v), want true", res.Err)
		}
		if res.Err != nil {
			t.Errorf("Err = %v, want nil", res.Err)
		}
		if res.Plan.Package != prunePkg {
			t.Errorf("Plan.Package = %q, want %q; the result has to say which package it is about", res.Plan.Package, prunePkg)
		}
		assertPruneDirGone(t, dir, "R5.1 removes the whole directory, not the ebuilds inside it")
		assertPruneDirAlive(t, filepath.Join(root, pruneCat), "only the package directory goes; its category may still hold other packages")
	})

	t.Run("a plan that is not eligible is never removed", func(t *testing.T) {
		root := t.TempDir()
		refusedDir := writePrunePackage(t, root, pruneCat, "refused-pkg")
		divergingDir := writePrunePackage(t, root, pruneCat, "diverging-pkg")

		refused := prunePlanFor(pruneCat, "refused-pkg")
		refused.Class, refused.Eligible, refused.Reason = PruneRefused, false, "verdict is keep"
		diverging := prunePlanFor(pruneCat, "diverging-pkg")
		diverging.Class, diverging.Eligible, diverging.Reason = PruneDiverging, false, "version 1.0 differs"

		got := ExecutePrune(
			PruneBatch{Diverging: []PrunePlan{diverging}, Refused: []PrunePlan{refused}},
			PruneOptions{OverlayPath: root},
		)

		assertPruneDirAlive(t, refusedDir, "a refused package must be left exactly as it was")
		assertPruneDirAlive(t, divergingDir, "a diverging package is not eligible on this run and carries local work")
		for _, res := range got {
			if res.Removed {
				t.Errorf("%s reports Removed = true; Eligible is the authorisation and the bucket is not", res.Plan.Package)
			}
		}
	})
}

// TestExecutePruneRefusesAPathOutsideTheOverlay pins the guard.
//
// The plan reaches this function as a struct, and Category is just a string on
// it. A traversal component turns "delete a package we no longer need" into
// "delete whatever is two levels up", and os.RemoveAll does not ask twice.
//
// The empty-root sub-test is the same bug wearing different clothes: with no
// OverlayPath, filepath.Join yields a RELATIVE path and the removal lands
// wherever the process happens to be running — which, for a tool run from inside
// a checkout, is somebody's source tree.
//
// _Requirements: R5.1, R5.5, design Security Considerations_
func TestExecutePruneRefusesAPathOutsideTheOverlay(t *testing.T) {
	t.Run("a traversal component is refused", func(t *testing.T) {
		base := t.TempDir()
		root := filepath.Join(base, "overlay")
		if err := os.MkdirAll(root, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", root, err)
		}
		victim := writePrunePackage(t, base, "outside", "victim")

		plan := prunePlanFor(filepath.Join("..", "outside"), "victim")

		got := ExecutePrune(
			PruneBatch{Identical: []PrunePlan{plan}},
			PruneOptions{OverlayPath: root},
		)

		res := onlyResult(t, got)
		if res.Removed {
			t.Error("Removed = true for a target outside the overlay")
		}
		if res.Err == nil {
			t.Error("Err is nil; a refusal the caller cannot see is a refusal that exits 0")
		}
		assertPruneDirAlive(t, victim, "the target sits outside the overlay root and must not be touched")
	})

	t.Run("an empty overlay root removes nothing", func(t *testing.T) {
		root := t.TempDir()
		dir := writePrunePackage(t, root, pruneCat, prunePkg)

		got := ExecutePrune(
			PruneBatch{Identical: []PrunePlan{prunePlanFor(pruneCat, prunePkg)}},
			PruneOptions{},
		)

		res := onlyResult(t, got)
		if res.Removed {
			t.Error("Removed = true with no OverlayPath; the joined path is relative and lands wherever the process is running")
		}
		if res.Err == nil {
			t.Error("Err is nil for an unusable overlay root")
		}
		assertPruneDirAlive(t, dir, "nothing may be removed when the root the guard checks against is unknown")
	})
}

// TestExecutePruneRefusesASymlinkedPackageDir covers the case where the
// authorisation and the deletion would be about two different things.
//
// PruneVerification compared the tree the link points at and found it identical
// to upstream. os.RemoveAll on the link removes the LINK — leaving the real tree,
// which may sit anywhere on the filesystem, and destroying the overlay's
// reference to it. Following it instead would be worse: a deletion outside the
// overlay authorised by a comparison of a directory the operator never saw.
//
// Neither is acceptable, so a symlinked package directory is refused and both
// the link and its target are left alone.
//
// _Requirements: R5.1, design Security Considerations_
func TestExecutePruneRefusesASymlinkedPackageDir(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "overlay")
	realDir := writePrunePackage(t, filepath.Join(base, "elsewhere"), pruneCat, prunePkg)

	linkDir := filepath.Join(root, pruneCat)
	if err := os.MkdirAll(linkDir, 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", linkDir, err)
	}
	link := filepath.Join(linkDir, prunePkg)
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatalf("symlink %s -> %s: %v", link, realDir, err)
	}

	got := ExecutePrune(
		PruneBatch{Identical: []PrunePlan{prunePlanFor(pruneCat, prunePkg)}},
		PruneOptions{OverlayPath: root},
	)

	res := onlyResult(t, got)
	if res.Removed {
		t.Error("Removed = true for a symlinked package directory")
	}
	if res.Err == nil {
		t.Error("Err is nil; the operator has to be told this package was skipped and why")
	}
	assertPruneDirAlive(t, realDir, "the tree the link points at was never the thing the plan named")
	if _, err := os.Lstat(link); err != nil {
		t.Errorf("the symlink itself was removed (%v); a refused package is left exactly as it was", err)
	}
}

// TestExecutePruneContinuesAfterOneFailure pins R5.5.
//
// A batch that stops at the first failure leaves the overlay in a state no plan
// described: some packages gone, some not, and nothing on screen saying where the
// line was drawn. Every plan gets its own attempt and its own result, and the
// caller decides what the exit code should be.
//
// The failing plan is placed in the MIDDLE so that a short-circuit is visible as
// a missing third result rather than as a passing test.
//
// _Requirements: R5.5_
func TestExecutePruneContinuesAfterOneFailure(t *testing.T) {
	root := t.TempDir()
	first := writePrunePackage(t, root, pruneCat, "first-pkg")
	third := writePrunePackage(t, root, pruneCat, "third-pkg")

	// The middle plan points outside the overlay, so the guard refuses it.
	outside := writePrunePackage(t, filepath.Dir(root), "outside", "victim")
	bad := prunePlanFor(filepath.Join("..", "outside"), "victim")

	got := ExecutePrune(
		PruneBatch{Identical: []PrunePlan{
			prunePlanFor(pruneCat, "first-pkg"),
			bad,
			prunePlanFor(pruneCat, "third-pkg"),
		}},
		PruneOptions{OverlayPath: root},
	)

	if len(got) != 3 {
		t.Fatalf("ExecutePrune returned %d results, want 3 — one per plan, whatever happened to it: %+v", len(got), got)
	}
	assertPruneDirGone(t, first, "the plan before the failure was authorised and must still be carried out")
	assertPruneDirGone(t, third, "the plan after the failure must not be skipped because an earlier one failed (R5.5)")
	assertPruneDirAlive(t, outside, "the refused target must not be touched")

	failures := 0
	for _, res := range got {
		if res.Err != nil {
			failures++
		}
		if (res.Err == nil) != res.Removed {
			t.Errorf("%s reports Removed = %v with Err = %v; the two must agree, or the caller cannot tell what happened", res.Plan.Package, res.Removed, res.Err)
		}
	}
	if failures != 1 {
		t.Errorf("%d results carry an error, want exactly 1; the caller exits non-zero on any failure and needs to know which", failures)
	}
}

// TestExecutePruneRemovesNothingForAnEmptyBatch is the floor: nothing planned,
// nothing removed, no results, no panic.
//
// It is the state a declined confirmation produces, and the reason the CLI can
// guarantee R6.3 by construction — the executor is simply never handed anything.
//
// _Requirements: R6.3, R1.1_
func TestExecutePruneRemovesNothingForAnEmptyBatch(t *testing.T) {
	root := t.TempDir()
	dir := writePrunePackage(t, root, pruneCat, prunePkg)

	got := ExecutePrune(PruneBatch{}, PruneOptions{OverlayPath: root})

	if len(got) != 0 {
		t.Errorf("ExecutePrune returned %d results for an empty batch, want 0: %+v", len(got), got)
	}
	assertPruneDirAlive(t, dir, "an empty batch authorises nothing")
}
