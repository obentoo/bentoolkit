package autoupdate

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

// binaryEbuild is a prebuilt record: RESTRICT=bindist is what the tier
// detection reads, exactly as on cc-switch-bin and friends in ::bentoo.
const binaryEbuild = `EAPI=8
DESCRIPTION="Prebuilt test package"
HOMEPAGE="https://example.com"
SRC_URI=""
LICENSE="MIT"
SLOT="0"
KEYWORDS="~amd64"
RESTRICT="bindist mirror strip"
`

// binaryApplier wires an applier over a one-package overlay whose record is
// binary, counting the build child's runs in builds.
func binaryApplier(t *testing.T, builds *int) (a *Applier, pkg string) {
	t.Helper()
	tmp := t.TempDir()
	overlayDir := filepath.Join(tmp, "overlay")
	configDir := filepath.Join(tmp, "config")
	pkg = "media-plugins/gst-plugins-qt6"

	createTestEbuildFileWithContent(t, overlayDir, pkg, "1.28.6", binaryEbuild)
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
		WithApplierRunCaptured(capturedFrom(func(*exec.Cmd) ([]byte, error) {
			*builds++
			return []byte(cleanCompile), nil
		})),
		WithApplierStagingRoot(filepath.Join(tmp, "staging")),
		WithApplierDistdir(t.TempDir(), ""),
		WithApplierIsolationProbe(func() (bool, string) { return true, "" }),
	)
	if err != nil {
		t.Fatalf("creating applier: %v", err)
	}
	return a, pkg
}

// TestCheckProvesABinaryRecordByItsFetchGate is the regression behind
// "Unproven: no staged tree was retained" on every -bin package: policy sends a
// binary record to depth none, and a check at depth none used to return before
// staging anything, so the apply that publishes only proven bumps refused it
// forever. The check now stages the candidate, runs the manifest step and
// reports the fetch gate, and the apply promotes that proof without a build.
func TestCheckProvesABinaryRecordByItsFetchGate(t *testing.T) {
	builds := 0
	a, pkg := binaryApplier(t, &builds)

	res := a.Validate(t.Context(), pkg, validate.DepthNone)
	if res.DepthRequested != validate.DepthNone.String() {
		t.Fatalf("DepthRequested = %q, want none for a binary record", res.DepthRequested)
	}
	var fetch *validate.GateResult
	for i := range res.Gates {
		if res.Gates[i].Gate == validate.GateFetch {
			fetch = &res.Gates[i]
		}
		if res.Gates[i].Gate == validate.GateOptions {
			t.Errorf("the option gate ran on a binary record (%s): it can only answer SKIPPED there", res.Gates[i].Reason)
		}
	}
	if fetch == nil || fetch.Outcome != validate.OutcomePass {
		t.Fatalf("want a passing fetch gate, got %+v", res.Gates)
	}
	if builds != 0 {
		t.Fatalf("%d build(s) ran for a binary record; nothing is built at depth none", builds)
	}
	if got := proofState(res.Gates, validate.DepthNone); got != "READY" {
		t.Fatalf("the check reports %s, want READY: a fetched binary is ready to apply", got)
	}

	result, err := a.Apply(t.Context(), pkg)
	if err != nil || !result.Success {
		t.Fatalf("Apply: err=%v unproven=%v reason=%s", err, result.Unproven, result.UnprovenReason)
	}
	if result.ValidationSource != ValidationSourceStaged {
		t.Fatalf("ValidationSource = %q, want %q: the check's fetch proof holds (reason: %s)",
			result.ValidationSource, ValidationSourceStaged, result.DepthReason)
	}
}

// TestCheckAtDepthNoneOnASourceRecordStillRunsNoGate keeps the other half: an
// operator's --depth=none on a source package is still a request to run no
// gate, and nothing is staged for it.
func TestCheckAtDepthNoneOnASourceRecordStillRunsNoGate(t *testing.T) {
	builds := 0
	a, pkg := scriptedBuildApplier(t, buildRun{output: cleanCompile}, &builds)
	none := validate.DepthNone
	a.flagDepth = &none

	res := a.Validate(t.Context(), pkg, validate.DepthNone)
	for _, g := range res.Gates {
		if g.Gate == validate.GateFetch {
			t.Fatalf("a source record at depth none ran the fetch gate: %+v", res.Gates)
		}
		if g.Outcome != validate.OutcomeSkipped {
			t.Fatalf("a source record at depth none ran gate %s (%s)", g.Gate, g.Outcome)
		}
	}
	if builds != 0 {
		t.Fatalf("%d build(s) ran at depth none", builds)
	}
}
