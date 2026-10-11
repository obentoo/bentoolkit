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

	"github.com/obentoo/bentoolkit/internal/autoupdate/fixer"
	"github.com/obentoo/bentoolkit/internal/autoupdate/repair"
)

// adoptBuildFixer records whether the build fixer was called.
type adoptBuildFixer struct{ calls int }

func (f *adoptBuildFixer) FixBuild(context.Context, fixer.BuildFixRequest) (fixer.BuildFixResult, error) {
	f.calls++
	return fixer.BuildFixResult{}, errors.New("must not run")
}

// adoptedFixture stages the candidate with a human edit, plants a bundle in it
// and returns an applier that adopts that tree.
func adoptedFixture(t *testing.T, seam func(ctx context.Context, name string, arg ...string) *exec.Cmd) (a *Applier, overlayDir, pkg, staged, body string, mf *stageFixer, bf *adoptBuildFixer) {
	t.Helper()
	mf = &stageFixer{res: fixer.ManifestFixResult{Summary: "must not run"}}
	bf = &adoptBuildFixer{}
	a, overlayDir, pkg, _, _ = promoteFixture(t,
		WithExecCommand(seam),
		WithApplierDistdir(t.TempDir(), ""),
		WithApplierFixer(mf),
		WithApplierBuildFixer(bf),
	)
	body = candidateBody(t, overlayDir) + "\n# repaired by the operator\n"
	staged = stageCandidateFor(t, a.StagingRoot(), overlayDir, body)
	planted := repair.Bundle{
		Schema: repair.SchemaVersion, Package: pkg, CurrentVersion: "1.28.6", Candidate: "1.29.2",
		Stage: "planted-stage", Failure: "planted failure", StagedRoot: staged,
		Created: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	if err := repair.WriteBundle(planted); err != nil {
		t.Fatalf("planting the bundle: %v", err)
	}
	WithAdoptedStagedTree(staged)(a)
	return a, overlayDir, pkg, staged, body, mf, bf
}

// TestApplyAdoptedTreePassingGatesPromotesTheEditedBytes covers R5.1, R5.2 and
// R5.4: the operator's edited bytes are what is published, no agent runs.
func TestApplyAdoptedTreePassingGatesPromotesTheEditedBytes(t *testing.T) {
	a, overlayDir, pkg, _, body, mf, bf := adoptedFixture(t, mockExecCommandSuccess)
	result, err := a.Apply(t.Context(), pkg, false)
	if err != nil || !result.Success {
		t.Fatalf("Apply on the adopted tree failed: %v", err)
	}
	published := filepath.Join(overlayDir, "media-plugins", "gst-plugins-qt6", "gst-plugins-qt6-1.29.2.ebuild")
	got, rerr := os.ReadFile(published)
	if rerr != nil {
		t.Fatalf("nothing promoted: %v", rerr)
	}
	if string(got) != body {
		t.Fatalf("the published ebuild is not the adopted tree's edited bytes; a fresh tree was staged instead")
	}
	if mf.calls != 0 || bf.calls != 0 {
		t.Fatalf("an LLM agent ran on the adopted tree: manifest fixer %d, build fixer %d", mf.calls, bf.calls)
	}
	if e, found := a.pending.Get(pkg); found && e.Status != StatusApplied {
		t.Fatalf("pending status after promotion = %q, want applied or removed", e.Status)
	}
}

// TestApplyAdoptedTreeFailingGateRewritesBundleAndKeepsTree covers R5.5: the
// tree stays where it was, the bundle is rewritten, the overlay is untouched.
func TestApplyAdoptedTreeFailingGateRewritesBundleAndKeepsTree(t *testing.T) {
	failing := func(ctx context.Context, _ string, _ ...string) *exec.Cmd { return exec.CommandContext(ctx, "false") }
	a, overlayDir, pkg, staged, body, mf, bf := adoptedFixture(t, failing)
	before := hashOverlayTree(t, overlayDir)
	result, err := a.Apply(t.Context(), pkg, false)
	if err == nil || result.Success {
		t.Fatalf("fixture: re-validation passed although every child failed")
	}
	if result.StagedPath != staged {
		t.Fatalf("StagedPath = %q, want the adopted tree %q", result.StagedPath, staged)
	}
	cand := filepath.Join(staged, "media-plugins", "gst-plugins-qt6", "gst-plugins-qt6-1.29.2.ebuild")
	got, rerr := os.ReadFile(cand)
	if rerr != nil || string(got) != body {
		t.Fatalf("the adopted tree lost the operator's edit (err %v)", rerr)
	}
	b, berr := repair.ReadBundle(filepath.Join(staged, repair.BundleFile))
	if berr != nil {
		t.Fatalf("no bundle after a failed re-validation: %v", berr)
	}
	if b.Stage == "planted-stage" || b.Failure == "planted failure" || !b.Created.After(time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("the bundle was not rewritten with the new failure: %+v", b)
	}
	if b.Stage != result.FailedStage {
		t.Fatalf("bundle stage %q, result FailedStage %q", b.Stage, result.FailedStage)
	}
	if mf.calls != 0 || bf.calls != 0 {
		t.Fatalf("an LLM agent ran on the adopted tree: manifest fixer %d, build fixer %d", mf.calls, bf.calls)
	}
	if after := hashOverlayTree(t, overlayDir); after != before {
		t.Fatalf("a failed re-validation changed the published overlay")
	}
}

// TestApplyAdoptedTreeMissingCandidateIsAnErrorNamingTheRoot: an adopted root
// without the candidate ebuild fails and names the root, staging nothing new.
func TestApplyAdoptedTreeMissingCandidateIsAnErrorNamingTheRoot(t *testing.T) {
	a, _, pkg, _, _ := promoteFixture(t, WithExecCommand(mockExecCommandSuccess), WithApplierDistdir(t.TempDir(), ""))
	empty := filepath.Join(a.StagingRoot(), "media-plugins", "gst-plugins-qt6", "1.29.2")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	WithAdoptedStagedTree(empty)(a)
	result, err := a.Apply(t.Context(), pkg, false)
	if err == nil || result.Success {
		t.Fatalf("Apply succeeded on an adopted tree with no candidate ebuild")
	}
	if !strings.Contains(err.Error(), empty) {
		t.Fatalf("error does not name the adopted root %s: %v", empty, err)
	}
	if _, serr := os.Stat(filepath.Join(empty, "media-plugins", "gst-plugins-qt6", "gst-plugins-qt6-1.29.2.ebuild")); serr == nil {
		t.Fatalf("a fresh candidate was staged into the adopted root")
	}
}
