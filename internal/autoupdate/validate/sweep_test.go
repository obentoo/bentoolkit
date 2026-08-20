package validate

// Story 039, task 6 — R6, R6.1, R6.2, R6.3, R6.4, R6.5.
//
// Stage does os.RemoveAll(stagedRoot) before recreating ("R3.7: replace, never
// accumulate"). That replaces the tree OF THAT PACKAGE. Nothing removes the
// trees of packages that have left scope, so a --depth run over the whole
// overlay leaves one tree per package under StagingRoot, permanently. A grep for
// sweep/clean/prune functions in this package returned zero before this file.
//
// # The retention policy, and why it is not "keep the last N"
//
// The reason to keep a staged tree at all is to look at it after something went
// wrong. A tree whose gates PASSED has served its purpose the moment the verdict
// was recorded; a tree whose gate FAILED is the artifact an operator still
// needs, next to the log LogDir retained. "Keep the last N" is worse on both
// counts: it keeps passes, and it can still discard the failure somebody is
// mid-investigation on.
//
// # Recognition, and why it has to be self-verifying
//
// Stage writes profiles/repo_name into every tree it makes, holding
// stagedRepoName(pkg, version). A directory is a tree this package produced when
// that file's content matches the pkg and version ITS OWN PATH implies. The
// check is self-verifying on purpose: a directory an operator parked under the
// staging root cannot accidentally satisfy it, and a tree moved to the wrong
// path stops satisfying it — which is the safe direction, because R6.3 keeps
// what it does not recognise.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sweepTree lays out one staged tree the way Stage does, and optionally records
// a gate outcome beside it.
func sweepTree(t *testing.T, stagingRoot, atom, version string, outcome Outcome) string {
	t.Helper()
	root, err := StagedTreePath(stagingRoot, atom, version)
	if err != nil {
		t.Fatalf("naming the staged tree of %s-%s: %v", atom, version, err)
	}
	pkg := atom[strings.Index(atom, "/")+1:]
	if err := os.MkdirAll(filepath.Join(root, "profiles"), 0o750); err != nil {
		t.Fatalf("laying out %s: %v", root, err)
	}
	if err := os.WriteFile(filepath.Join(root, "profiles", "repo_name"),
		[]byte(stagedRepoName(pkg, version)+"\n"), 0o600); err != nil {
		t.Fatalf("writing repo_name: %v", err)
	}
	if outcome != "" {
		if err := WriteStageRecord(root, StageRecord{
			Package: atom, Version: version, Depth: DepthCompile,
			Gates: []GateResult{{Gate: GateConfigure, Outcome: outcome, Reason: "recorded by the fixture"}},
		}); err != nil {
			t.Fatalf("writing the stage record for %s-%s: %v", atom, version, err)
		}
	}
	return root
}

func sweepExists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	return err == nil
}

// TestSweepStagedTrees_RemovesWhatLeftScopeAndKeepsTheFailures is R6.1 and the
// retention policy.
func TestSweepStagedTrees_RemovesWhatLeftScopeAndKeepsTheFailures(t *testing.T) {
	overlay := t.TempDir()
	staging := t.TempDir()

	inScope := sweepTree(t, staging, "media-plugins/gst-plugins-qt6", "1.29.2", OutcomePass)
	gone := sweepTree(t, staging, "media-libs/gst-plugins-base", "1.28.6", OutcomePass)
	failed := sweepTree(t, staging, "dev-qt/qtbase", "6.9.1", OutcomeFailed)

	report, err := SweepStagedTrees(SweepRequest{
		Overlay:     overlay,
		StagingRoot: staging,
		InScope:     []StagedCandidate{{Atom: "media-plugins/gst-plugins-qt6", Version: "1.29.2"}},
	})
	if err != nil {
		t.Fatalf("SweepStagedTrees: %v", err)
	}

	if !sweepExists(t, inScope) {
		t.Error("the sweep removed a tree the current run is still using; a sweeper that eats the run that " +
			"called it is worse than one that never runs")
	}
	if sweepExists(t, gone) {
		t.Error("a tree whose package left scope survived the sweep (R6.1); one tree per package accumulating " +
			"forever is the whole defect")
	}
	if !sweepExists(t, failed) {
		t.Error("the sweep removed the tree of a candidate whose gate FAILED; that tree is the artifact an " +
			"operator still needs, and it is the one thing the retention policy exists to keep")
	}

	// R6.4: the report names both lists, with a reason per kept entry.
	if len(report.Removed) != 1 || !strings.Contains(report.Removed[0], "gst-plugins-base") {
		t.Errorf("the report names %v as removed, want the one out-of-scope tree", report.Removed)
	}
	if len(report.Kept) == 0 {
		t.Fatal("the report names nothing as kept; a sweeper that silently keeps things reads as a sweeper " +
			"that swept (R6.4)")
	}
	for _, k := range report.Kept {
		if k.Reason == "" {
			t.Errorf("the kept entry %q carries no reason; an operator looking at a staging root that did "+
				"not shrink has to be able to read why", k.Path)
		}
	}
}

// TestSweepStagedTrees_KeepsAndReportsWhatItDoesNotRecognise is R6.3, and it is
// what keeps this from being an rm -rf of a directory the operator may also use
// for something else.
func TestSweepStagedTrees_KeepsAndReportsWhatItDoesNotRecognise(t *testing.T) {
	overlay := t.TempDir()
	staging := t.TempDir()

	// A directory in the right SHAPE but with no repo_name: not a tree Stage
	// made, whatever it looks like.
	stranger := filepath.Join(staging, "app-misc", "not-ours", "1.0")
	if err := os.MkdirAll(stranger, 0o750); err != nil {
		t.Fatalf("laying out the stranger: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stranger, "notes.txt"), []byte("mine\n"), 0o600); err != nil {
		t.Fatalf("writing the stranger's file: %v", err)
	}
	// And one whose repo_name names a DIFFERENT package from its path — a tree
	// that was moved, which the self-verifying check must also decline.
	moved := filepath.Join(staging, "app-misc", "moved", "2.0")
	if err := os.MkdirAll(filepath.Join(moved, "profiles"), 0o750); err != nil {
		t.Fatalf("laying out the moved tree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(moved, "profiles", "repo_name"),
		[]byte(stagedRepoName("somethingelse", "9.9")+"\n"), 0o600); err != nil {
		t.Fatalf("writing the moved repo_name: %v", err)
	}

	report, err := SweepStagedTrees(SweepRequest{Overlay: overlay, StagingRoot: staging})
	if err != nil {
		t.Fatalf("SweepStagedTrees: %v", err)
	}

	for _, path := range []string{stranger, moved} {
		if !sweepExists(t, path) {
			t.Errorf("the sweep removed %s, which it cannot identify as a tree it produced (R6.3)", path)
		}
	}
	if len(report.Kept) < 2 {
		t.Errorf("the report names %d kept entries, want at least the two unrecognised ones; keeping without "+
			"reporting reads as having swept them (R6.3, R6.4)", len(report.Kept))
	}
	if len(report.Removed) != 0 {
		t.Errorf("the sweep removed %v with nothing in scope and nothing recognised", report.Removed)
	}
}

// TestSweepStagedTrees_RefusesAStagingRootInsideTheOverlay is R6.2.
//
// It reuses ensureOutsideOverlay rather than checking again, for the same reason
// R1.5 forbids a second ladder: a deletion routine with its own idea of what is
// inside the overlay is how a sweeper eventually eats a published package. And
// the refusal must remove NOTHING — a sweeper that deletes half a tree and then
// discovers where it is has already done the damage.
func TestSweepStagedTrees_RefusesAStagingRootInsideTheOverlay(t *testing.T) {
	overlay := t.TempDir()
	staging := filepath.Join(overlay, "staging")
	tree := sweepTree(t, staging, "media-libs/gst-plugins-base", "1.28.6", OutcomePass)

	_, err := SweepStagedTrees(SweepRequest{Overlay: overlay, StagingRoot: staging})

	if err == nil {
		t.Fatal("the sweep accepted a staging root inside the published overlay; `overlay autoupdate --clean` " +
			"already deletes unclaimed ebuilds there, and a sweeper that also acts inside it is one bug away " +
			"from removing a published package (R6.2)")
	}
	if !sweepExists(t, tree) {
		t.Error("the refused sweep had already removed a tree; the refusal has to come before anything is " +
			"deleted, not partway through")
	}
}

// TestSweepStagedTrees_AnInterruptedSweepLeavesNoThirdState is R6.5.
//
// The invariant this repo already relies on: either the removal is complete, or
// the entry still looks exactly like a tree the next run will replace. There is
// no half-removed state a later run can mistake for a staged one — and the test
// for it is that a second sweep over the same root reaches the same answer.
func TestSweepStagedTrees_AnInterruptedSweepLeavesNoThirdState(t *testing.T) {
	overlay := t.TempDir()
	staging := t.TempDir()
	sweepTree(t, staging, "media-libs/gst-plugins-base", "1.28.6", OutcomePass)
	sweepTree(t, staging, "dev-qt/qtbase", "6.9.1", OutcomeFailed)

	first, err := SweepStagedTrees(SweepRequest{Overlay: overlay, StagingRoot: staging})
	if err != nil {
		t.Fatalf("first sweep: %v", err)
	}
	second, err := SweepStagedTrees(SweepRequest{Overlay: overlay, StagingRoot: staging})
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}

	if len(second.Removed) != 0 {
		t.Errorf("the second sweep removed %v; the first one left something a later run still treats as "+
			"sweepable, which is the third state R6.5 forbids", second.Removed)
	}
	if len(first.Kept) != len(second.Kept) {
		t.Errorf("the two sweeps kept %d and %d entries; a run that follows an interrupted one has to reach "+
			"the same answer, or the staging root's state depends on when somebody pressed Ctrl-C",
			len(first.Kept), len(second.Kept))
	}
}
