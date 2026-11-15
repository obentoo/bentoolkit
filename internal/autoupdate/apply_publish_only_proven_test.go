package autoupdate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

// TestPublishOnlyProven_RefusesWithoutAProof: with no check behind it, an
// apply that publishes only proven bumps runs no gate, fetches nothing,
// writes nothing to the overlay and keeps the pending entry.
func TestPublishOnlyProven_RefusesWithoutAProof(t *testing.T) {
	builds := 0
	a, pkg := scriptedBuildApplier(t, buildRun{output: cleanCompile}, &builds)
	a.validateUnproven = false

	result, err := a.Apply(t.Context(), pkg)

	if err != nil {
		t.Fatalf("an unproven bump is a refusal, not a failure: %v", err)
	}
	if !result.Unproven || result.Success {
		t.Fatalf("result Unproven=%v Success=%v, want an unproven refusal", result.Unproven, result.Success)
	}
	if !strings.Contains(result.UnprovenReason, "--check --llm "+pkg) {
		t.Errorf("the refusal does not say how to prove the bump: %q", result.UnprovenReason)
	}
	if builds != 0 {
		t.Errorf("%d build(s) ran; an apply that publishes only proofs must not validate", builds)
	}
	if _, err := os.Stat(filepath.Join(a.overlayPath, "media-plugins", "gst-plugins-qt6", "gst-plugins-qt6-1.29.2.ebuild")); err == nil {
		t.Error("the candidate was written into the published overlay")
	}
	if entry, ok := a.pending.Get(pkg); !ok || entry.Status != StatusPending {
		t.Errorf("the pending entry was not kept as pending: %+v (found %v)", entry, ok)
	}
}

// TestPublishOnlyProven_PublishesWhatTheCheckProved: the check proves, the
// apply publishes the proven copy without running a gate.
func TestPublishOnlyProven_PublishesWhatTheCheckProved(t *testing.T) {
	builds := 0
	a, pkg := scriptedBuildApplier(t, buildRun{output: cleanCompile}, &builds)
	a.validateUnproven = false
	a.proofMaxAge = 7 * 24 * time.Hour

	a.Validate(t.Context(), pkg, validate.DepthCompile)
	before := builds
	result, err := a.Apply(t.Context(), pkg)

	if err != nil || !result.Success {
		t.Fatalf("Apply: success=%v err=%v unproven=%q", result.Success, err, result.UnprovenReason)
	}
	if builds != before {
		t.Errorf("%d build(s) ran in an apply that publishes a proof", builds-before)
	}
}

// TestProofMaxAge_AnOldOrUndatedProofDoesNotHold: a proof past its age, or
// one that records no time, is refused and must be taken again.
func TestProofMaxAge_AnOldOrUndatedProofDoesNotHold(t *testing.T) {
	for name, recordedAt := range map[string]time.Time{
		"eight days old": time.Now().Add(-8 * 24 * time.Hour),
		"undated":        {},
	} {
		t.Run(name, func(t *testing.T) {
			builds := 0
			a, pkg := scriptedBuildApplier(t, buildRun{output: cleanCompile}, &builds)
			a.validateUnproven = false
			a.validateUnproven = false
			a.proofMaxAge = 7 * 24 * time.Hour
			a.Validate(t.Context(), pkg, validate.DepthCompile)

			staged, err := validate.StagedTreePath(a.StagingRoot(), pkg, "1.29.2")
			if err != nil {
				t.Fatal(err)
			}
			rec, err := validate.ReadStageRecord(staged)
			if err != nil {
				t.Fatal(err)
			}
			rec.RecordedAt = recordedAt
			if err := validate.WriteStageRecord(staged, rec); err != nil {
				t.Fatal(err)
			}

			result, err := a.Apply(t.Context(), pkg)

			if err != nil || !result.Unproven {
				t.Fatalf("a %s proof was not refused: unproven=%v success=%v err=%v", name, result.Unproven, result.Success, err)
			}
		})
	}
}
