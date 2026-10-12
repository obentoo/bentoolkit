package autoupdate

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

// scriptedBuildApplier wires an applier with no fixer whose build child
// answers with run, and counts the build child's runs in builds.
func scriptedBuildApplier(t *testing.T, run buildRun, builds *int) (a *Applier, pkg string) {
	t.Helper()
	tmp := t.TempDir()
	overlayDir := filepath.Join(tmp, "overlay")
	configDir := filepath.Join(tmp, "config")
	pkg = "media-plugins/gst-plugins-qt6"

	createTestEbuildFile(t, overlayDir, pkg, "1.28.6")
	writePublishedManifest(t, overlayDir, publishedManifestBody)

	pending, err := NewPendingList(configDir)
	if err != nil {
		t.Fatalf("creating pending list: %v", err)
	}
	pending.Add(PendingUpdate{Package: pkg, CurrentVersion: "1.28.6", NewVersion: "1.29.2", Status: StatusPending})

	a, err = NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "true")
		}),
		WithApplierRunAttached(func(*exec.Cmd) ([]byte, error) {
			*builds++
			return []byte(run.output), run.err
		}),
		WithConfirmFunc(func(string) bool { return true }),
		WithApplierStagingRoot(filepath.Join(tmp, "staging")),
		WithApplierDistdir(t.TempDir(), ""),
		WithApplierIsolationProbe(func() (bool, string) { return true, "" }),
	)
	if err != nil {
		t.Fatalf("creating applier: %v", err)
	}
	return a, pkg
}

// TestCheckRecordsValidatedDigestSoApplyReuses: a --check whose gates pass
// records the digest of the staged candidate it read, so the following apply
// promotes the tree without running a gate again.
func TestCheckRecordsValidatedDigestSoApplyReuses(t *testing.T) {
	builds := 0
	a, pkg := scriptedBuildApplier(t, buildRun{output: cleanCompile}, &builds)

	res := a.Validate(t.Context(), pkg, validate.DepthCompile)
	if ok, why := validate.PromotionDecision(res.Gates, nil); !ok || builds == 0 {
		t.Fatalf("fixture: want a check whose build ran and whose gates pass, got %d build(s): %s", builds, why)
	}
	staged, err := validate.StagedTreePath(a.StagingRoot(), pkg, "1.29.2")
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(stagedCandidatePath(staged))
	if err != nil {
		t.Fatalf("reading the staged candidate: %v", err)
	}
	if got := recordedValidatedDigest(t, staged); got != digestOf(string(body)) {
		t.Fatalf("validated_digest = %q, want %q: a passing check proves the bytes it staged", got, digestOf(string(body)))
	}

	before := builds
	result, err := a.Apply(t.Context(), pkg, false)
	if err != nil || !result.Success {
		t.Fatalf("Apply: %v", err)
	}
	if result.ValidationSource != ValidationSourceStaged {
		t.Fatalf("ValidationSource = %q, want %q: the check's proof still holds (reason: %s)",
			result.ValidationSource, ValidationSourceStaged, result.DepthReason)
	}
	if builds != before {
		t.Fatalf("%d build(s) ran although the check already proved the tree", builds-before)
	}
}

// TestCheckWithAFailingGateRecordsNoValidatedDigest: a check whose gates did
// not pass proves no bytes.
func TestCheckWithAFailingGateRecordsNoValidatedDigest(t *testing.T) {
	builds := 0
	a, pkg := scriptedBuildApplier(t, buildRun{output: preparedThenConfigureFails, err: errors.New("exit status 1")}, &builds)

	res := a.Validate(t.Context(), pkg, validate.DepthCompile)
	if res.WorstOutcome() != validate.OutcomeFailed || builds == 0 {
		t.Fatalf("fixture: want a failing build gate, got %d build(s) and %+v", builds, res.Gates)
	}
	staged, err := validate.StagedTreePath(a.StagingRoot(), pkg, "1.29.2")
	if err != nil {
		t.Fatal(err)
	}
	if got := recordedValidatedDigest(t, staged); got != "" {
		t.Fatalf("a failing check recorded validated_digest %q", got)
	}
}
