package autoupdate

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate/repair"
	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

// An adopted tree whose root is accepted is the bundle's home from that moment
// on: a failure before the candidate is resolved (no candidate ebuild, inputs
// that cannot be read) rewrites the bundle with stage "adopt" and the new
// failure. A root that is refused names nothing, so no bundle anywhere moves.
// None of these failures reached a gate, so none of them writes a record.

// plantedCutoff is a moment after the planted bundle's Created and long before
// any run of this test.
var plantedCutoff = time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC)

// adoptedCandidatePath is the candidate ebuild inside an adopted tree of the
// adoptedFixture package.
func adoptedCandidatePath(staged string) string {
	return filepath.Join(staged, "media-plugins", "gst-plugins-qt6", "gst-plugins-qt6-1.29.2.ebuild")
}

// removeAdoptedCandidate deletes the candidate ebuild from the adopted tree.
func removeAdoptedCandidate(t *testing.T, staged string) {
	t.Helper()
	if err := os.Remove(adoptedCandidatePath(staged)); err != nil {
		t.Fatalf("removing the adopted candidate: %v", err)
	}
}

// makePublishedManifestUnreadable replaces the published package Manifest with
// a directory, so digesting the bump's inputs fails with something other than
// "no Manifest".
func makePublishedManifestUnreadable(t *testing.T, overlayDir string) {
	t.Helper()
	manifest := filepath.Join(overlayDir, "media-plugins", "gst-plugins-qt6", "Manifest")
	if err := os.Remove(manifest); err != nil {
		t.Fatalf("removing the published Manifest: %v", err)
	}
	if err := os.Mkdir(manifest, 0o755); err != nil {
		t.Fatal(err)
	}
}

// assertBundleRewrittenAtAdopt checks the bundle in staged was replaced by one
// describing result's pre-candidate failure.
func assertBundleRewrittenAtAdopt(t *testing.T, result *ApplyResult, err error, staged string) {
	t.Helper()
	if err == nil || result.Success {
		t.Fatalf("fixture: Apply succeeded on an adopted tree that cannot be validated")
	}
	if result.FailedStage != "adopt" {
		t.Fatalf("FailedStage = %q, want adopt", result.FailedStage)
	}
	path := filepath.Join(staged, repair.BundleFile)
	b, berr := repair.ReadBundle(path)
	if berr != nil {
		t.Fatalf("no readable bundle in the adopted tree after the failure: %v", berr)
	}
	if b.Stage == "planted-stage" || b.Failure == "planted failure" || !b.Created.After(plantedCutoff) {
		t.Fatalf("the bundle still describes the planted failure, not this run's: stage %q, failure %q, created %s",
			b.Stage, b.Failure, b.Created)
	}
	if b.Stage != "adopt" {
		t.Fatalf("bundle stage = %q, want adopt", b.Stage)
	}
	if b.Failure == "" {
		t.Fatalf("the rewritten bundle carries no failure text")
	}
	if b.StagedRoot != staged {
		t.Fatalf("bundle staged root = %q, want the adopted tree %q", b.StagedRoot, staged)
	}
	if result.RepairBundle != path {
		t.Fatalf("RepairBundle = %q, want %q", result.RepairBundle, path)
	}
}

// TestAdoptedMissingCandidateRewritesTheBundle: the root is this bump's own
// staged tree and its candidate ebuild is gone.
func TestAdoptedMissingCandidateRewritesTheBundle(t *testing.T) {
	a, overlayDir, pkg, staged, _, _, _ := adoptedFixture(t, mockExecCommandSuccess)
	removeAdoptedCandidate(t, staged)
	before := hashOverlayTree(t, overlayDir)

	result, err := a.Apply(t.Context(), pkg, false)
	assertBundleRewrittenAtAdopt(t, result, err, staged)
	if _, serr := os.Stat(adoptedCandidatePath(staged)); serr == nil {
		t.Fatalf("a fresh candidate was staged into the adopted tree")
	}
	if after := hashOverlayTree(t, overlayDir); after != before {
		t.Fatalf("a failed adoption changed the published overlay")
	}
}

// TestAdoptedUnreadableInputsRewriteTheBundle: the root is accepted and the
// bump's inputs cannot be digested.
func TestAdoptedUnreadableInputsRewriteTheBundle(t *testing.T) {
	a, overlayDir, pkg, staged, body, _, _ := adoptedFixture(t, mockExecCommandSuccess)
	makePublishedManifestUnreadable(t, overlayDir)

	result, err := a.Apply(t.Context(), pkg, false)
	assertBundleRewrittenAtAdopt(t, result, err, staged)
	got, rerr := os.ReadFile(adoptedCandidatePath(staged))
	if rerr != nil || string(got) != body {
		t.Fatalf("the adopted tree lost the operator's edit (err %v)", rerr)
	}
}

// TestAdoptedRefusedRootLeavesEveryBundleAlone: a root that is not this bump's
// staged tree is refused before it is named, so neither it nor the real tree
// gains, loses or changes a byte.
func TestAdoptedRefusedRootLeavesEveryBundleAlone(t *testing.T) {
	for _, tc := range []struct {
		name  string
		other func(a *Applier, staged string) string
	}{
		// Hostile first: a sibling whose path differs from the staged tree's
		// by one trailing character is a different bump's tree.
		{name: "a near-identical sibling", other: func(_ *Applier, staged string) string { return staged + "0" }},
		{name: "a copy elsewhere", other: func(a *Applier, _ string) string {
			return filepath.Join(a.StagingRoot(), "elsewhere", "1.29.2")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, overlayDir, pkg, staged, _, _, _ := adoptedFixture(t, mockExecCommandSuccess)
			other := tc.other(a, staged)
			if err := os.CopyFS(other, os.DirFS(staged)); err != nil {
				t.Fatal(err)
			}
			// The copy's candidate is gone too, so a run that accepted the
			// root would fail before the gates and have a reason to write.
			removeAdoptedCandidate(t, other)
			WithAdoptedStagedTree(other)(a)

			stagedBefore, otherBefore, overlayBefore := hashOverlayTree(t, staged), hashOverlayTree(t, other), hashOverlayTree(t, overlayDir)
			result, err := a.Apply(t.Context(), pkg, false)
			if err == nil || result.Success {
				t.Fatalf("Apply adopted %s, which is not the bump's staged tree %s", other, staged)
			}
			if result.RepairBundle != "" || result.StagedPath != "" {
				t.Fatalf("a refused root was named on the result: bundle %q, staged path %q", result.RepairBundle, result.StagedPath)
			}
			if hashOverlayTree(t, other) != otherBefore {
				t.Fatalf("the refused root %s changed", other)
			}
			if hashOverlayTree(t, staged) != stagedBefore {
				t.Fatalf("the bump's own staged tree %s changed although a different root was refused", staged)
			}
			if hashOverlayTree(t, overlayDir) != overlayBefore {
				t.Fatalf("a refused adoption changed the published overlay")
			}
		})
	}

	t.Run("no staging root", func(t *testing.T) {
		a, _, pkg, staged, _, _, _ := adoptedFixture(t, mockExecCommandSuccess)
		removeAdoptedCandidate(t, staged)
		a.stagingRoot = ""
		before := hashOverlayTree(t, staged)
		result, err := a.Apply(t.Context(), pkg, false)
		if err == nil || result.Success {
			t.Fatalf("Apply adopted a tree with no staging root configured")
		}
		if hashOverlayTree(t, staged) != before {
			t.Fatalf("a refusal for want of a staging root changed the adopted tree")
		}
	})
}

// TestAdoptedPreCandidateFailureWritesNoRecord: a failure before any gate ran
// writes no validation record, and leaves an existing one byte-identical.
func TestAdoptedPreCandidateFailureWritesNoRecord(t *testing.T) {
	failures := []struct {
		name    string
		breakIt func(t *testing.T, overlayDir, staged string)
	}{
		{name: "missing candidate", breakIt: func(t *testing.T, _, staged string) { removeAdoptedCandidate(t, staged) }},
		{name: "unreadable inputs", breakIt: func(t *testing.T, overlayDir, _ string) { makePublishedManifestUnreadable(t, overlayDir) }},
	}
	for _, f := range failures {
		t.Run(f.name+" with no record", func(t *testing.T) {
			a, overlayDir, pkg, staged, _, _, _ := adoptedFixture(t, mockExecCommandSuccess)
			f.breakIt(t, overlayDir, staged)
			if result, err := a.Apply(t.Context(), pkg, false); err == nil || result.Success {
				t.Fatalf("fixture: Apply succeeded")
			}
			if _, serr := os.Stat(validate.StageRecordPath(staged)); serr == nil {
				t.Fatalf("a failure before any gate ran wrote a validation record")
			}
		})

		t.Run(f.name+" with a record", func(t *testing.T) {
			a, overlayDir, pkg, staged, _, _, _ := adoptedFixture(t, mockExecCommandSuccess)
			if err := validate.WriteStageRecord(staged, validate.StageRecord{
				Package: pkg, Version: "1.29.2", ProducedBy: validate.ProducedByApplier, Depth: validate.DepthConfigure,
				Gates: []validate.GateResult{{Gate: validate.GateConfigure, Outcome: validate.OutcomeFailed, Reason: "recorded by the fixture"}},
			}); err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile(validate.StageRecordPath(staged))
			if err != nil {
				t.Fatal(err)
			}
			f.breakIt(t, overlayDir, staged)
			if result, err := a.Apply(t.Context(), pkg, false); err == nil || result.Success {
				t.Fatalf("fixture: Apply succeeded")
			}
			got, err := os.ReadFile(validate.StageRecordPath(staged))
			if err != nil || string(got) != string(want) {
				t.Fatalf("a failure before any gate ran replaced the validation record (err %v)", err)
			}
		})
	}
}
