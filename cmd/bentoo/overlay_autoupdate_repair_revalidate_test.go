package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/autoupdate/repair"
	"github.com/obentoo/bentoolkit/internal/common/filelock"
)

// TestRepairRevalidateFailureRewritesBundleAndExits1 covers R5.5 and R5.7.
func TestRepairRevalidateFailureRewritesBundleAndExits1(t *testing.T) {
	auOpts := testAutoupdateOptions()
	f := repairFixture(t, auOpts, 1)
	code, out := runRepair(t, auOpts, ttyDeps(true, true), "y\n")
	if code != 1 {
		t.Fatalf("failed re-validation exited %d, want 1:\n%s", code, out)
	}
	b, err := repair.ReadBundle(f.bundlePath)
	if err != nil {
		t.Fatalf("bundle gone after a failed re-validation: %v", err)
	}
	if !b.Created.After(f.created) || b.Stage == "" || b.Failure == "manifest command failed: 404 Not Found" {
		t.Fatalf("bundle not rewritten with the new failure: %+v", b)
	}
	if _, err := os.Stat(filepath.Join(f.overlay, "dev-go", "repairdemo", "repairdemo-"+repairCand+".ebuild")); err == nil {
		t.Fatalf("a failed re-validation published the candidate")
	}
}

// TestRepairRevalidateSuccessPromotesAndRemovesBundle covers R5.2, R5.3, R5.4.
func TestRepairRevalidateSuccessPromotesAndRemovesBundle(t *testing.T) {
	auOpts := testAutoupdateOptions()
	f := repairFixture(t, auOpts, 0)
	code, out := runRepair(t, auOpts, ttyDeps(true, true), "y\n")
	if code != 0 {
		t.Fatalf("passing re-validation exited %d, want 0:\n%s", code, out)
	}
	pub, err := os.ReadFile(filepath.Join(f.overlay, "dev-go", "repairdemo", "repairdemo-"+repairCand+".ebuild"))
	if err != nil || !strings.Contains(string(pub), "# repaired") {
		t.Fatalf("the operator's repaired ebuild was not promoted (err %v)", err)
	}
	if _, err := os.Stat(f.bundlePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bundle still present after promotion (stat %v)", err)
	}
	pending, err := autoupdate.NewPendingList(f.configDir)
	if err != nil {
		t.Fatal(err)
	}
	if e, found := pending.Get(repairPkg); found && e.Status != autoupdate.StatusApplied {
		t.Fatalf("pending status = %q, want applied", e.Status)
	}
}

// TestRepairRevalidateLockHeldLeavesBundleUnchanged covers R5.6.
func TestRepairRevalidateLockHeldLeavesBundleUnchanged(t *testing.T) {
	auOpts := testAutoupdateOptions()
	f := repairFixture(t, auOpts, 0)
	oldWait, oldPoll := filelock.Wait, filelock.Poll
	filelock.Wait, filelock.Poll = 200*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { filelock.Wait, filelock.Poll = oldWait, oldPoll })
	lockPath := filepath.Join(f.overlay, ".autoupdate.bentoo-lock")
	held, err := filelock.Acquire(lockPath, "test holds the overlay")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(held.Release)
	before := readFileOrEmpty(f.bundlePath)
	code, out := runRepair(t, auOpts, ttyDeps(true, true), "y\n")
	if code != 1 || !strings.Contains(out, lockPath) {
		t.Fatalf("exit %d, want 1 with the --apply lock error naming %s:\n%s", code, lockPath, out)
	}
	if readFileOrEmpty(f.bundlePath) != before {
		t.Fatalf("the bundle changed although the lock was held")
	}
	if _, err := os.Stat(filepath.Join(f.overlay, "dev-go", "repairdemo", "repairdemo-"+repairCand+".ebuild")); err == nil {
		t.Fatalf("promoted while another run held the lock")
	}
}
