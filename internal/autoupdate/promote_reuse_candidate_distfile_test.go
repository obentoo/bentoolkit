package autoupdate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

// writeStagedManifest puts the Manifest the manifest step leaves in a staged
// package directory: the candidate's own archives and nothing else.
func writeStagedManifest(t *testing.T, staged, body string) {
	t.Helper()
	path := filepath.Join(staged, "media-plugins", "gst-plugins-qt6", "Manifest")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing the staged Manifest: %v", err)
	}
}

// stageProvedCandidateScoped is stageProvedCandidate with the record a current
// proof writes: the candidate-scoped distfile digest beside the legacy one.
func stageProvedCandidateScoped(t *testing.T, a *Applier, overlayDir, stagedManifest, candidateDigest string) {
	t.Helper()
	body := candidateBody(t, overlayDir)
	staged := stageCandidateFor(t, a.StagingRoot(), overlayDir, body)
	writeStagedManifest(t, staged, stagedManifest)
	rec := provedRecord(validate.DepthConfigure, digestOf(body), stagedDistfileDigest)
	rec.CandidateDistfileDigest = &candidateDigest
	if err := validate.WriteStageRecord(staged, rec); err != nil {
		t.Fatalf("writing the stage record: %v", err)
	}
}

// TestApplierPromote_PruningAnotherVersionKeepsTheProof is #338 item 1: a
// `--clean` removed the fallback version after the proof, the published
// Manifest lost that version's DIST line, and the apply refused the proved
// candidate as "re-rolled upstream". The candidate's own archive never moved.
func TestApplierPromote_PruningAnotherVersionKeepsTheProof(t *testing.T) {
	applier, overlayDir, pkg, watch, _ := promoteFixture(t, WithExecCommand(mockExecCommandSuccess))
	// Proved while the published Manifest held only the 1.28.6 archive; the
	// candidate's 1.29.2 archive is in the staged Manifest alone.
	stageProvedCandidateScoped(t, applier, overlayDir,
		"DIST gst-plugins-good-1.29.2.tar.xz 200 BLAKE2B 99 SHA512 99\n", "")

	writePublishedManifest(t, overlayDir, "") // the fallback was pruned
	spawnsBefore := watch.spawns

	result, err := applier.Apply(t.Context(), pkg)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !result.Success || result.ValidationSource != ValidationSourceStaged {
		t.Errorf("the proved tree was not promoted after another version was pruned: success=%v source=%q err=%v",
			result.Success, result.ValidationSource, result.Error)
	}
	if watch.spawns != spawnsBefore {
		t.Errorf("%d child process(es) ran; the proof still describes this candidate", watch.spawns-spawnsBefore)
	}
}

// TestApplierPromote_ACandidateArchiveRedigestedIsStillRefused keeps the guard:
// an archive the candidate itself uses, digested differently since the proof,
// is a different source tree.
func TestApplierPromote_ACandidateArchiveRedigestedIsStillRefused(t *testing.T) {
	applier, overlayDir, pkg, _, pins := promoteFixture(t, WithExecCommand(mockExecCommandSuccess))
	// A revision bump: the candidate uses the archive 1.28.6 already published.
	stageProvedCandidateScoped(t, applier, overlayDir,
		"DIST gst-plugins-good-1.28.6.tar.xz 100 BLAKE2B ab SHA512 cd\n", stagedDistfileDigest)

	writePublishedManifest(t, overlayDir,
		"DIST gst-plugins-good-1.28.6.tar.xz 100 BLAKE2B "+rerolledDistfileDigest+" SHA512 "+rerolledDistfileDigest+"\n")

	result, _ := applier.Apply(t.Context(), pkg)
	if result.Success && result.ValidationSource == ValidationSourceStaged {
		t.Fatal("a retained tree was promoted although the candidate's own archive was re-digested")
	}
	if result.ValidationSource == ValidationSourceThisRun {
		return // revalidating is an acceptable answer
	}
	report := ""
	if result.Error != nil {
		report = result.Error.Error()
	}
	for _, want := range []string{stagedDistfileDigest, rerolledDistfileDigest} {
		if !strings.Contains(report, want) {
			t.Errorf("the refusal does not name the digest %q: %q", want, report)
		}
	}
	if len(pins) != 0 {
		t.Errorf("a refused bump wrote a pin: %v", pins)
	}
}

// TestRecordStagedProof_WritesTheCandidateScopedDigest is the producing side:
// without it every new record would fall back to the whole-Manifest digest.
func TestRecordStagedProof_WritesTheCandidateScopedDigest(t *testing.T) {
	applier, overlayDir, pkg, _, _ := promoteFixture(t, WithExecCommand(mockExecCommandSuccess))
	writePublishedManifest(t, overlayDir, "DIST gst-plugins-good-1.28.6.tar.xz 100 BLAKE2B ab SHA512 cd\n"+
		"DIST gst-plugins-good-1.29.2.tar.xz 200 BLAKE2B 99 SHA512 99\n")
	staged := stageCandidateFor(t, applier.StagingRoot(), overlayDir, candidateBody(t, overlayDir))
	writeStagedManifest(t, staged, "DIST gst-plugins-good-1.29.2.tar.xz 200 BLAKE2B 99 SHA512 99\n")

	inputs, err := applier.stagedInputsFor(pkg, "1.28.6", &PendingUpdate{Package: pkg, CurrentVersion: "1.28.6", NewVersion: "1.29.2"})
	if err != nil {
		t.Fatalf("stagedInputsFor: %v", err)
	}
	gates := provedRecord(validate.DepthConfigure, "", "").Gates
	applier.recordStagedProof(t.Context(), staged, pkg, "1.29.2", inputs, gates, validate.DepthConfigure, "", "")

	rec, err := validate.ReadStageRecord(staged)
	if err != nil {
		t.Fatalf("ReadStageRecord: %v", err)
	}
	if rec.CandidateDistfileDigest == nil || *rec.CandidateDistfileDigest != "99" {
		t.Errorf("CandidateDistfileDigest = %v, want \"99\" (the candidate's archive only)", rec.CandidateDistfileDigest)
	}
	if rec.DistfileDigest != "ab,99" {
		t.Errorf("DistfileDigest = %q, want the whole-Manifest digest \"ab,99\" kept for older readers", rec.DistfileDigest)
	}
}
