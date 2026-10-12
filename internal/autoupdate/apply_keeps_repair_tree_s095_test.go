package autoupdate

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate/repair"
	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

// repairPendingTree is a staged tree an earlier failed apply kept: an edited
// candidate, a record showing a failed gate and, when bundled, a repair bundle.
type repairPendingTree struct {
	a          *Applier
	overlayDir string
	pkg        string
	staged     string
	body       string
	watch      *promoteWatch
}

// bundleKind is what the fixture's kept tree carries at its root.
type bundleKind int

const (
	noBundle bundleKind = iota
	// unmarkedBundle is what a plain failed --apply leaves.
	unmarkedBundle
	// markedBundle is what a --repair session that did not pass leaves.
	markedBundle
)

// repairPendingFixture builds that tree for the promote fixture's package. Every
// child process succeeds, so a run that restages instead of refusing promotes.
func repairPendingFixture(t *testing.T, kind bundleKind) repairPendingTree {
	t.Helper()
	a, overlayDir, pkg, watch, _ := promoteFixture(t,
		WithExecCommand(mockExecCommandSuccess),
		WithApplierDistdir(t.TempDir(), ""),
	)
	body := candidateBody(t, overlayDir) + "\n# repaired by the operator\n"
	staged := stageCandidateFor(t, a.StagingRoot(), overlayDir, body)
	failed := provedRecord(validate.DepthOptions, digestOf(body), stagedDistfileDigest)
	failed.Gates = []validate.GateResult{{Gate: validate.GateOptions, Outcome: validate.OutcomeFailed}}
	if err := validate.WriteStageRecord(staged, failed); err != nil {
		t.Fatalf("writing the failed record: %v", err)
	}
	if kind != noBundle {
		planted := repair.Bundle{
			Schema: repair.SchemaVersion, Package: pkg, CurrentVersion: "1.28.6", Candidate: "1.29.2",
			Stage: "planted-stage", Failure: "planted failure", StagedRoot: staged,
			Created: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		}
		if kind == markedBundle {
			planted.RepairedAt = time.Date(2020, 1, 1, 1, 0, 0, 0, time.UTC)
		}
		if err := repair.WriteBundle(planted); err != nil {
			t.Fatalf("planting the bundle: %v", err)
		}
	}
	return repairPendingTree{a: a, overlayDir: overlayDir, pkg: pkg, staged: staged, body: body, watch: watch}
}

func (f repairPendingTree) candidatePath(root string) string {
	return filepath.Join(root, "media-plugins", "gst-plugins-qt6", "gst-plugins-qt6-1.29.2.ebuild")
}

// TestApplyKeepsRepairTreeSurvivesByteForByte: a non-adopting Apply refuses a
// tree that carries a repair bundle and leaves it, the overlay and the pending
// entry exactly as they were.
func TestApplyKeepsRepairTreeSurvivesByteForByte(t *testing.T) {
	f := repairPendingFixture(t, markedBundle)
	treeBefore := hashOverlayTree(t, f.staged)
	overlayBefore := hashOverlayTree(t, f.overlayDir)
	entryBefore, _ := f.a.pending.Get(f.pkg)

	result, err := f.a.Apply(t.Context(), f.pkg, false)

	if !errors.Is(err, ErrRepairPending) {
		t.Fatalf("Apply error = %v, want one wrapping ErrRepairPending", err)
	}
	if result == nil || result.Success || !errors.Is(result.Error, ErrRepairPending) {
		t.Fatalf("result does not record the refusal: %+v", result)
	}
	want := "staged tree kept for repair: bentoo overlay autoupdate --repair " + f.pkg
	if !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), f.pkg) {
		t.Fatalf("error %q does not say %q", err, want)
	}
	giveUp := "or delete " + filepath.Join(f.staged, repair.BundleFile) + " to give the repair up"
	if !strings.Contains(err.Error(), giveUp) {
		t.Fatalf("error %q does not say %q", err, giveUp)
	}
	if got := hashOverlayTree(t, f.staged); got != treeBefore {
		t.Fatalf("the repair tree changed under a non-adopting Apply")
	}
	got, rerr := os.ReadFile(f.candidatePath(f.staged))
	if rerr != nil || string(got) != f.body {
		t.Fatalf("the operator's edited candidate was lost (err %v)", rerr)
	}
	if got := hashOverlayTree(t, f.overlayDir); got != overlayBefore {
		t.Fatalf("the published overlay changed")
	}
	entryAfter, found := f.a.pending.Get(f.pkg)
	if !found || entryAfter.Status != entryBefore.Status || entryAfter.Error != entryBefore.Error ||
		entryAfter.NewVersion != entryBefore.NewVersion {
		t.Fatalf("pending entry changed: before %+v, after %+v (found %v)", entryBefore, entryAfter, found)
	}
}

// TestApplyKeepsRepairTreeSetsRepairBundle: the refusal names the tree and its
// bundle, so the --repair hint and the Apply Result point at it.
func TestApplyKeepsRepairTreeSetsRepairBundle(t *testing.T) {
	f := repairPendingFixture(t, markedBundle)
	result, _ := f.a.Apply(t.Context(), f.pkg, false)
	if result == nil {
		t.Fatal("Apply returned no result")
	}
	if want := filepath.Join(f.staged, repair.BundleFile); result.RepairBundle != want {
		t.Fatalf("RepairBundle = %q, want %q", result.RepairBundle, want)
	}
	if result.StagedPath != f.staged {
		t.Fatalf("StagedPath = %q, want %q", result.StagedPath, f.staged)
	}
	b, err := repair.ReadBundle(result.RepairBundle)
	if err != nil {
		t.Fatalf("reading the bundle: %v", err)
	}
	if b.Stage != "planted-stage" || b.Failure != "planted failure" {
		t.Fatalf("the bundle was rewritten: %+v", b)
	}
}

// TestApplyKeepsRepairTreeDoesNotRestage: no child runs and the stage record is
// the one the earlier run left, bytes and modification time.
func TestApplyKeepsRepairTreeDoesNotRestage(t *testing.T) {
	f := repairPendingFixture(t, markedBundle)
	recordPath := validate.StageRecordPath(f.staged)
	recordBefore, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	infoBefore, err := os.Stat(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	spawnsBefore := f.watch.spawns

	_, _ = f.a.Apply(t.Context(), f.pkg, false)

	if f.watch.spawns != spawnsBefore {
		t.Fatalf("%d child process(es) ran for a refused package", f.watch.spawns-spawnsBefore)
	}
	recordAfter, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatalf("the stage record is gone: %v", err)
	}
	infoAfter, err := os.Stat(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(recordAfter) != string(recordBefore) || !infoAfter.ModTime().Equal(infoBefore.ModTime()) {
		t.Fatalf("the stage record was rewritten")
	}
}

// TestApplyKeepsRepairTreeGuardUnmarkedStillRestages: without a bundle, and
// with the unmarked bundle a plain failed --apply leaves, the kept tree is
// replaced as before, so the published ebuild is a fresh candidate.
func TestApplyKeepsRepairTreeGuardUnmarkedStillRestages(t *testing.T) {
	for name, kind := range map[string]bundleKind{"no bundle": noBundle, "unmarked bundle": unmarkedBundle} {
		t.Run(name, func(t *testing.T) {
			f := repairPendingFixture(t, kind)
			result, err := f.a.Apply(t.Context(), f.pkg, false)
			if err != nil || !result.Success {
				t.Fatalf("Apply failed: %v", err)
			}
			got, rerr := os.ReadFile(f.candidatePath(f.overlayDir))
			if rerr != nil {
				t.Fatalf("nothing promoted: %v", rerr)
			}
			if string(got) != candidateBody(t, f.overlayDir) {
				t.Fatalf("the published ebuild is the old tree's edited bytes; the tree was not restaged")
			}
		})
	}
}

// TestApplyKeepsRepairTreeCheckRefuses: --check refuses a repaired tree the
// same way, reporting the package skipped and leaving the tree as it was.
func TestApplyKeepsRepairTreeCheckRefuses(t *testing.T) {
	f := repairPendingFixture(t, markedBundle)
	treeBefore := hashOverlayTree(t, f.staged)
	spawnsBefore := f.watch.spawns

	res := f.a.Validate(t.Context(), f.pkg, validate.DepthNone)

	want := "staged tree kept for repair: bentoo overlay autoupdate --repair " + f.pkg
	if len(res.Gates) == 0 {
		t.Fatalf("Validate returned no gate outcome: %+v", res)
	}
	for _, g := range res.Gates {
		if g.Outcome != validate.OutcomeSkipped || !strings.Contains(g.Reason, want) || !strings.Contains(g.Reason, "to give the repair up") {
			t.Fatalf("gate %s = %s (%q), want skipped saying %q", g.Gate, g.Outcome, g.Reason, want)
		}
	}
	if got := hashOverlayTree(t, f.staged); got != treeBefore {
		t.Fatalf("--check changed the repair tree")
	}
	if f.watch.spawns != spawnsBefore {
		t.Fatalf("%d child process(es) ran for a refused package", f.watch.spawns-spawnsBefore)
	}
}

// TestApplyKeepsRepairTreeFailedRepairWritesMark: a re-validation of an adopted
// tree that does not pass, by a failing gate or by a refused files/ change,
// rewrites the bundle with the repair mark.
func TestApplyKeepsRepairTreeFailedRepairWritesMark(t *testing.T) {
	cases := map[string]func(t *testing.T, f repairPendingTree){
		"failing gate": func(t *testing.T, f repairPendingTree) {
			f.a.execCommand = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
				return exec.CommandContext(ctx, "false")
			}
		},
		"refused files change": func(t *testing.T, f repairPendingTree) {
			dir := filepath.Join(f.staged, "media-plugins", "gst-plugins-qt6", "files")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "fix.patch"), []byte("--- a\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			f := repairPendingFixture(t, unmarkedBundle)
			setup(t, f)
			WithAdoptedStagedTree(f.staged)(f.a)
			result, err := f.a.Apply(t.Context(), f.pkg, false)
			if err == nil || result.Success {
				t.Fatalf("fixture: the re-validation passed")
			}
			b, berr := repair.ReadBundle(filepath.Join(f.staged, repair.BundleFile))
			if berr != nil {
				t.Fatalf("no bundle after a failed re-validation: %v", berr)
			}
			if b.RepairedAt.IsZero() {
				t.Fatalf("the rewritten bundle carries no repair mark: %+v", b)
			}
		})
	}
}

// TestApplyKeepsRepairTreeGuardPlainFailureLeavesNoMark: the bundle a plain
// failed --apply writes carries no repair mark.
func TestApplyKeepsRepairTreeGuardPlainFailureLeavesNoMark(t *testing.T) {
	a, _, pkg, _, _ := promoteFixture(t, WithApplierDistdir(t.TempDir(), ""))
	result, err := a.Apply(t.Context(), pkg, false)
	if err == nil || result.RepairBundle == "" {
		t.Fatalf("fixture: want a failed apply with a bundle, got err %v, bundle %q", err, result.RepairBundle)
	}
	b, berr := repair.ReadBundle(result.RepairBundle)
	if berr != nil {
		t.Fatal(berr)
	}
	if !b.RepairedAt.IsZero() {
		t.Fatalf("a plain failed --apply marked its bundle as repaired: %+v", b)
	}
}

// TestApplyKeepsRepairTreeGuardAdoptedPathStillAdopts: the --repair route
// adopts the bundled tree and publishes its edited bytes.
func TestApplyKeepsRepairTreeGuardAdoptedPathStillAdopts(t *testing.T) {
	f := repairPendingFixture(t, markedBundle)
	WithAdoptedStagedTree(f.staged)(f.a)
	result, err := f.a.Apply(t.Context(), f.pkg, false)
	if err != nil || !result.Success {
		t.Fatalf("Apply on the adopted tree failed: %v", err)
	}
	got, rerr := os.ReadFile(f.candidatePath(f.overlayDir))
	if rerr != nil || string(got) != f.body {
		t.Fatalf("the adopted tree's edited bytes were not published (err %v)", rerr)
	}
}
