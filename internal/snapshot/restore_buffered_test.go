package snapshot

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// s053RestoreOpts is a one-link archive restore through run.
func s053RestoreOpts(run Runner) RestoreOptions {
	return RestoreOptions{
		Driver: "archive",
		Yes:    true,
		Remote: "r:bkt",
		Chain:  []chainLink{{ID: "full", Object: "-home/full.zst"}},
		Run:    run,
	}
}

// TestRestoreArchive_FailedDownloadNeverReceives pins 8.1 on the production
// runner: a download that fails after writing part of the object must not
// reach `btrfs receive`, which would leave a partial, writable subvolume that
// makes the retry fail. execCommand maps the three programs onto sh.
func TestRestoreArchive_FailedDownloadNeverReceives(t *testing.T) {
	s053NeedTools(t, "sh", "cat")
	marker := filepath.Join(t.TempDir(), "receive-started")
	orig := execCommand
	t.Cleanup(func() { execCommand = orig })
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		switch name {
		case "rclone":
			return exec.CommandContext(ctx, "sh", "-c", "printf partial-stream; sleep 0.3; exit 1")
		case "btrfs":
			return exec.CommandContext(ctx, "sh", "-c", `touch "$1"; cat >/dev/null`, "sh", marker)
		}
		return exec.CommandContext(ctx, "cat")
	}

	err := Restore(t.Context(), "full", "/mnt/restore", s053RestoreOpts(execRunner{}))
	if err == nil || !strings.Contains(err.Error(), "rclone") {
		t.Errorf("Restore = %v, want the rclone stage's failure", err)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Errorf("btrfs receive started although the download failed")
	}
}

// TestRestoreArchive_DoesNotStream pins that restore never goes through the
// Piper seam: its stages run one after another through Run.
func TestRestoreArchive_DoesNotStream(t *testing.T) {
	mr := &mockRunner{}
	if err := Restore(t.Context(), "full", "/mnt/restore", s053RestoreOpts(mr)); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if len(mr.PipeCalls) != 0 {
		t.Errorf("restore streamed through the pipe seam: PipeCalls = %q", mr.PipeCalls)
	}
	if len(mr.Calls) != 3 || mr.Calls[2].Name != "btrfs" {
		t.Errorf("Calls = %+v, want rclone, decompressor, btrfs receive", mr.Calls)
	}
}

// TestRestoreArchive_RunOnlyRunner pins that restore needs only Run: a Runner
// without the Piper seam restores instead of being refused.
func TestRestoreArchive_RunOnlyRunner(t *testing.T) {
	r := &s053RunOnlyRunner{}
	if err := Restore(t.Context(), "full", "/mnt/restore", s053RestoreOpts(r)); err != nil {
		t.Fatalf("Restore through a Run-only runner = %v, want nil", err)
	}
	if r.calls != 3 {
		t.Errorf("ran %d stages, want 3", r.calls)
	}
}
