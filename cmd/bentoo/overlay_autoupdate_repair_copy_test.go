package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/report"
)

// stageNewPatch writes a patch the published package directory lacks into the
// fixture's staged tree, as a session that rebased one would, and returns
// where the copy would land.
func stageNewPatch(t *testing.T, f *repairFix) (published string) {
	t.Helper()
	staged := filepath.Join(f.pkgDir, "files", "repairdemo-1.1-fix.patch")
	if err := os.MkdirAll(filepath.Dir(staged), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, []byte("--- a\n+++ b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(f.overlay, "dev-go", "repairdemo", "files", "repairdemo-1.1-fix.patch")
}

// TestRepairOffersToCopyANewPatchBeforeTheRevalidation: a yes to the copy
// carries the patch into the published directory, so the re-validation adopts
// the tree and proves it in the same run (breakpad, 2026-11-26).
func TestRepairOffersToCopyANewPatchBeforeTheRevalidation(t *testing.T) {
	f, ar, asked := offerFixture(t, true)
	published := stageNewPatch(t, f)

	ar.offerRepairs(context.Background(), f.overlay, f.configDir, config.AutoupdateConfig{}, checkedRun(report.Human))

	if *asked != 2 {
		t.Fatalf("asked %d times, want twice: the session, then the copy", *asked)
	}
	if got := readFileOrEmpty(published); got != "--- a\n+++ b\n" {
		t.Fatalf("the published patch holds %q, want the staged bytes", got)
	}
	if _, err := os.Stat(f.bundlePath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the repair was not proven after the copy: the bundle is still there (stat %v)", err)
	}
}

// TestRepairCopyDeclinedLeavesTheOverlayAlone: a no copies nothing, and the
// re-validation refuses the tree, which keeps its bundle.
func TestRepairCopyDeclinedLeavesTheOverlayAlone(t *testing.T) {
	f, ar, _ := offerFixture(t, true)
	published := stageNewPatch(t, f)
	answers := []bool{true, false} // the session yes, the copy no
	ar.deps.confirmRepair = func(string) bool { a := answers[0]; answers = answers[1:]; return a }

	ar.offerRepairs(context.Background(), f.overlay, f.configDir, config.AutoupdateConfig{}, checkedRun(report.Human))

	if len(answers) != 0 {
		t.Fatalf("%d question(s) were not asked, want the session and the copy", len(answers))
	}
	if _, err := os.Stat(published); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a declined copy wrote %s (stat %v)", published, err)
	}
	if _, err := os.Stat(f.bundlePath); err != nil {
		t.Errorf("the refused tree lost its bundle: %v", err)
	}
}

// TestApplyStagedCopyRefusesASymlinkDestination: bytes are never written
// through a link the published directory holds.
func TestApplyStagedCopyRefusesASymlinkDestination(t *testing.T) {
	dir := t.TempDir()
	src, target, dst := filepath.Join(dir, "src"), filepath.Join(dir, "elsewhere"), filepath.Join(dir, "dst")
	if err := os.WriteFile(src, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, dst); err != nil {
		t.Fatal(err)
	}
	if err := applyStagedCopy(stagedCopy(src, dst)); err == nil {
		t.Fatal("a symlink destination was written through")
	}
	if got := readFileOrEmpty(target); got != "old" {
		t.Errorf("the link target holds %q, want it untouched", got)
	}
}

func stagedCopy(src, dst string) autoupdate.StagedFileCopy {
	return autoupdate.StagedFileCopy{Rel: "files/x", Src: src, Dst: dst, Action: autoupdate.CopyFile}
}
