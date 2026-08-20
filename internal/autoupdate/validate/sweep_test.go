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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
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
		InScope:     []StagedCandidate{{Key: "media-plugins/gst-plugins-qt6", Version: "1.29.2"}},
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

// ===== Story 041, task 1.1 — R1.1 =====

// sweepFingerprint is every byte under root, as one string.
//
// It exists because most of what this story guards is something NOT happening —
// planning removes nothing, a cancelled sweep removes nothing — and "the tree
// still exists" is a weak way to say that: a routine that deleted a tree's
// record and left its directory would satisfy it. The fingerprint covers every
// path, its mode and the content of every file under it, so a sweep that
// touched anything at all moves the value.
//
// It is deliberately NOT a directory listing: R3.2 is "wholly present or wholly
// absent", and a half-removed tree is exactly the state a listing of the top
// level cannot see.
func sweepFingerprint(t *testing.T, root string) string {
	t.Helper()
	var lines []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if info.IsDir() {
			lines = append(lines, "d "+rel)
			return nil
		}
		body, readErr := os.ReadFile(path) //nolint:gosec // path comes from walking a t.TempDir()
		if readErr != nil {
			return readErr
		}
		sum := sha256.Sum256(body)
		lines = append(lines, "f "+rel+" "+hex.EncodeToString(sum[:]))
		return nil
	})
	if err != nil {
		t.Fatalf("fingerprinting the staging root %s: %v", root, err)
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}

// sweepRecordExists answers whether the staged tree at dir still carries the
// record and the marker that make it a whole tree, which is how R3.2's "wholly
// present" is checked without trusting os.Stat on the directory alone.
func sweepTreeIsWhole(t *testing.T, dir string) bool {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, "profiles", "repo_name")); err != nil {
		return false
	}
	if _, err := os.Stat(StageRecordPath(dir)); err != nil {
		return false
	}
	return true
}

// =============================================================================
// Task 1.1 — R1.1: a plan is a decision, and deciding removes nothing
// =============================================================================

// TestPlanStagedSweep_DecidesEverythingAndRemovesNothing is R1.1 in both of its
// halves, and the second half is the one that needs care.
//
// "The plan is complete" is asserted against the same three-way fixture story
// 039's retention policy was written on: a tree the current run still needs, a
// tree whose package left scope and whose gates passed, and a tree whose gate
// FAILED. The first and the third are keeps with reasons; the second is the one
// removal.
//
// "Nothing was removed" is asserted POSITIVELY, by fingerprinting the whole
// staging root before and after. Asserting merely that the plan looks right
// would pass against a planner that judged and deleted in the same traversal —
// which is precisely what SweepStagedTrees does today (validate/sweep.go:186),
// and precisely what D2 splits apart. The point at which a report exists and
// nothing has been removed has to be observable, or it does not exist.
func TestPlanStagedSweep_DecidesEverythingAndRemovesNothing(t *testing.T) {
	overlay := t.TempDir()
	staging := t.TempDir()

	inScope := sweepTree(t, staging, "media-plugins/gst-plugins-qt6", "1.29.2", OutcomePass)
	removable := sweepTree(t, staging, "media-libs/gst-plugins-base", "1.28.6", OutcomePass)
	failed := sweepTree(t, staging, "dev-qt/qtbase", "6.9.1", OutcomeFailed)

	before := sweepFingerprint(t, staging)

	plan, err := PlanStagedSweep(SweepRequest{
		Overlay:     overlay,
		StagingRoot: staging,
		InScope:     []StagedCandidate{{Key: "media-plugins/gst-plugins-qt6", Version: "1.29.2"}},
	})
	if err != nil {
		t.Fatalf("PlanStagedSweep: %v", err)
	}

	// The absence, stated positively: not "the tree is still there" but "not one
	// byte under the staging root moved".
	if after := sweepFingerprint(t, staging); after != before {
		t.Errorf("planning changed the staging root: %s -> %s. A plan is what one sweep WOULD do; a planner "+
			"that removes anything leaves no point at which the operator can read a report and still say no (R1.1)",
			before, after)
	}
	// Named separately from the fingerprint so a failure says WHICH tree went:
	// the fingerprint proves something moved, this says what.
	for _, dir := range []string{inScope, removable, failed} {
		if !sweepTreeIsWhole(t, dir) {
			t.Errorf("the tree %s is no longer whole after PLANNING; nothing in producing a plan removes anything (R1.1)", dir)
		}
	}

	if plan.StagingRoot != staging {
		t.Errorf("plan.StagingRoot = %q, want %q; the executor acts on the root the plan names, so a plan that "+
			"does not carry it cannot be handed anywhere", plan.StagingRoot, staging)
	}

	// The complete list, in both directions. A plan that named only removals
	// would be a plan an operator cannot audit: R1.1 requires the keeps and
	// their reasons in the same breath.
	if len(plan.Remove) != 1 || !strings.Contains(plan.Remove[0], "gst-plugins-base") {
		t.Errorf("plan.Remove = %v, want the one out-of-scope tree whose gates passed", plan.Remove)
	}
	keptReasons := map[string]string{}
	for _, k := range plan.Kept {
		if k.Reason == "" {
			t.Errorf("the kept entry %q carries no reason; an operator reading a plan that removes little has to "+
				"be able to read why, and a keep without a reason is indistinguishable from an oversight (R1.1)", k.Path)
		}
		keptReasons[k.Path] = k.Reason
	}
	if _, ok := keptReasons[inScope]; !ok {
		t.Errorf("the plan does not mention the in-scope tree %s at all; every tree in view is either a removal "+
			"or a keep with a reason (R1.1)", inScope)
	}
	if _, ok := keptReasons[failed]; !ok {
		t.Errorf("the plan does not mention the tree whose gate FAILED (%s); that tree is the artifact an "+
			"operator still needs, and a plan that omits it says nothing about it", failed)
	}
	if reason := keptReasons[failed]; reason != "" && !strings.Contains(strings.ToLower(reason), "fail") {
		t.Errorf("the failed tree is kept for the reason %q, which does not name the failed gate; the retention "+
			"policy is unchanged by this story and its reasons must survive the split into plan and execute", reason)
	}
	if _, ok := keptReasons[removable]; ok {
		t.Errorf("%s is named as BOTH a removal and a keep; the two lists partition what the sweep saw, and an "+
			"entry in both makes the printed plan self-contradictory", removable)
	}
}

// TestPlanStagedSweep_RefusesAStagingRootInsideTheOverlayBeforeReadingIt is
// R1.1's safety half arriving at a new door.
//
// Story 039 pins this for SweepStagedTrees, whose first statement is
// ensureOutsideOverlay. The planner is now the thing that READS the staging
// root, and a reader that walks first and checks afterwards has already listed
// the published overlay by the time it refuses. The check must sit in front of
// the walk, not merely somewhere in the function.
func TestPlanStagedSweep_RefusesAStagingRootInsideTheOverlayBeforeReadingIt(t *testing.T) {
	overlay := t.TempDir()
	inside := filepath.Join(overlay, "staging")
	if err := os.MkdirAll(inside, 0o750); err != nil {
		t.Fatalf("laying out %s: %v", inside, err)
	}

	plan, err := PlanStagedSweep(SweepRequest{Overlay: overlay, StagingRoot: inside})
	if err == nil {
		t.Fatal("PlanStagedSweep accepted a staging root INSIDE the published overlay; a routine that decides " +
			"what to delete under the overlay is one caller away from deleting a published package")
	}
	if len(plan.Remove) != 0 || len(plan.Kept) != 0 {
		t.Errorf("the refused plan still names %d removal(s) and %d keep(s); a refusal that returns a populated "+
			"plan invites a caller to execute it", len(plan.Remove), len(plan.Kept))
	}
}

// errorIsNotExist keeps the "it is gone" assertions above readable and is the
// one spelling of that question in this file.
func errorIsNotExist(err error) bool {
	return err != nil && errors.Is(err, fs.ErrNotExist)
}

// ===== Story 041, task 1.2 — R1.4, R3.1, R3.2 =====

// =============================================================================
// Task 1.2 — R1.4, R3.1, R3.2: the executor acts on the plan it was handed
// =============================================================================

// TestExecuteStagedSweep_ActsOnThePlanItWasHandedAndNotOnASecondDecision is
// R1.4, and the fixture is built so that "recomputes" and "acts on the plan"
// give visibly different answers.
//
// A tree that becomes removable AFTER the plan was made is the discriminator. An
// executor that walks the staging root again would find it and delete it — and
// the operator would have approved a printed list that never mentioned it. That
// is the failure R1.4 names: the thing displayed and the thing done have to be
// the same thing.
func TestExecuteStagedSweep_ActsOnThePlanItWasHandedAndNotOnASecondDecision(t *testing.T) {
	overlay := t.TempDir()
	staging := t.TempDir()

	planned := sweepTree(t, staging, "media-libs/gst-plugins-base", "1.28.6", OutcomePass)

	plan, err := PlanStagedSweep(SweepRequest{Overlay: overlay, StagingRoot: staging})
	if err != nil {
		t.Fatalf("PlanStagedSweep: %v", err)
	}
	if len(plan.Remove) != 1 {
		t.Fatalf("the fixture planned %d removals, want exactly 1; the rest of this test asserts nothing "+
			"without it", len(plan.Remove))
	}

	// Staged between the plan and the execution: removable by every rule, and
	// absent from the list the operator approved.
	appeared := sweepTree(t, staging, "dev-qt/qtbase", "6.9.1", OutcomePass)

	report := ExecuteStagedSweep(context.Background(), plan)

	if _, err := os.Stat(planned); !errorIsNotExist(err) {
		t.Errorf("the tree the plan named (%s) survived the execution; the executor is the only remover and it "+
			"has to remove what was approved", planned)
	}
	if !sweepTreeIsWhole(t, appeared) {
		t.Errorf("the executor removed %s, which was staged AFTER the plan was made and appears nowhere in it; "+
			"executing means acting on the displayed plan, never on a second, independently computed decision (R1.4)",
			appeared)
	}
	if len(report.Removed) != 1 || report.Removed[0] != planned {
		t.Errorf("report.Removed = %v, want exactly the planned %q; the report is what the operator diffs "+
			"against the plan they approved", report.Removed, planned)
	}
}

// TestExecuteStagedSweep_ACancelledSweepRemovesNothingAndSaysWhy is R3.1 and
// R3.2 at the first iteration of the walk, which is the same check every later
// iteration makes.
//
// The context is cancelled BEFORE the executor is entered, deliberately:
// cancelling from a goroutine mid-walk is a race, and a flaky test of an
// interrupt guard is worse than none. What this pins is that cancellation is
// consulted at all, that it stops the removal rather than merely colouring the
// report, and that every tree it did not reach is REPORTED — as a keep whose
// reason names the cancellation, not as silence. A sweep that answered "removed
// nothing" and listed nothing would be indistinguishable from an empty staging
// root.
func TestExecuteStagedSweep_ACancelledSweepRemovesNothingAndSaysWhy(t *testing.T) {
	overlay := t.TempDir()
	staging := t.TempDir()

	first := sweepTree(t, staging, "dev-qt/qtbase", "6.9.1", OutcomePass)
	second := sweepTree(t, staging, "media-libs/gst-plugins-base", "1.28.6", OutcomePass)

	plan, err := PlanStagedSweep(SweepRequest{Overlay: overlay, StagingRoot: staging})
	if err != nil {
		t.Fatalf("PlanStagedSweep: %v", err)
	}
	if len(plan.Remove) != 2 {
		t.Fatalf("the fixture planned %d removals, want 2", len(plan.Remove))
	}
	before := sweepFingerprint(t, staging)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	report := ExecuteStagedSweep(ctx, plan)

	if len(report.Removed) != 0 {
		t.Errorf("a cancelled sweep removed %v; the executor stops BEFORE the next removal, and this context was "+
			"already dead when it was entered (R3.1)", report.Removed)
	}
	if after := sweepFingerprint(t, staging); after != before {
		t.Errorf("a cancelled sweep changed the staging root: %s -> %s", before, after)
	}
	// R3.2: not merely still present — still WHOLE. A removal interrupted
	// halfway through one tree is the state the single os.RemoveAll exists to
	// make impossible.
	for _, dir := range []string{first, second} {
		if !sweepTreeIsWhole(t, dir) {
			t.Errorf("the tree %s is present but no longer whole after an interrupted sweep; each individual tree "+
				"is left either wholly present or wholly absent (R3.2)", dir)
		}
	}

	// R3.1: every tree it did not reach is reported as kept, naming the
	// cancellation. Both, not one — the report covers the whole plan.
	kept := map[string]string{}
	for _, k := range report.Kept {
		kept[k.Path] = k.Reason
	}
	for _, dir := range []string{first, second} {
		reason, ok := kept[dir]
		if !ok {
			t.Errorf("the report does not mention %s; a tree the sweep never reached still has to be named, or "+
				"the operator reads a short report as a completed sweep (R3.1)", dir)
			continue
		}
		low := strings.ToLower(reason)
		if !strings.Contains(low, "cancel") && !strings.Contains(low, "interrupt") && !strings.Contains(low, "stopped") {
			t.Errorf("%s is kept for the reason %q, which does not name the cancellation; \"you stopped me\" is a "+
				"reason, and reporting it as an ordinary keep hides that the sweep is incomplete (R3.1)", dir, reason)
		}
	}
}
