package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate/repair"
)

// assertRepairMarked fails unless the fixture's bundle is still present, carries
// the repair mark, and the candidate was not published.
func assertRepairMarked(t *testing.T, f *repairFix) {
	t.Helper()
	marked, err := repair.HasRepairMark(f.staged)
	if err != nil {
		t.Fatalf("reading the repair mark: %v", err)
	}
	if !marked {
		t.Fatalf("the bundle %s carries no repair mark after a session that did not promote", f.bundlePath)
	}
	if _, err := os.Stat(filepath.Join(f.overlay, "dev-go", "repairdemo", "repairdemo-"+repairCand+".ebuild")); err == nil {
		t.Fatalf("the candidate was published although the run did not promote")
	}
}

// TestRepairMarkExitsApplierFailure: the session's exit is followed by a
// failure to build the applier for the re-validation. The session ran, so the
// bundle must carry the mark, or a later --apply restages over its work.
func TestRepairMarkExitsApplierFailure(t *testing.T) {
	auOpts := testAutoupdateOptions()
	f := repairFixture(t, auOpts, 0)
	// The session replaces the logs directory with a regular file, so the
	// applier cannot create it and newSingleApplier fails after the session.
	logs := filepath.Join(f.configDir, "logs")
	writeStub(t, f.toolDir, "claude", fmt.Sprintf("rm -rf %q && : > %q\nexit 0\n", logs, logs))

	code, out := runRepair(t, auOpts, ttyDeps(true, true), "y\n")
	if code != 1 || !strings.Contains(out, "failed to initialize applier") {
		t.Fatalf("exit %d, want 1 with the applier-construction error:\n%s", code, out)
	}
	assertRepairMarked(t, f)
}

// TestRepairMarkExitsInterruptBeforeRevalidation: the run's context is
// cancelled while the session runs, with no SIGTERM or SIGHUP received, so the
// run stops between the session and the re-validation and must mark the bundle.
func TestRepairMarkExitsInterruptBeforeRevalidation(t *testing.T) {
	auOpts := testAutoupdateOptions()
	f := repairFixture(t, auOpts, 0)
	started, release := f.record+".started", f.record+".release"
	// The loop bound only keeps a broken test from hanging: about ten seconds.
	writeStub(t, f.toolDir, "claude", fmt.Sprintf(
		": > %q\ni=0\nwhile [ ! -e %q ] && [ $i -lt 200 ]; do sleep 0.05; i=$((i+1)); done\nexit 0\n", started, release))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			if _, err := os.Stat(started); err == nil {
				break
			}
		}
		cancel()
		_ = os.WriteFile(release, nil, 0o600)
	}()
	code, out := runRepairCtx(t, ctx, auOpts, ttyDeps(true, true), "y\n")
	wg.Wait()
	if _, err := os.Stat(started); err != nil {
		t.Fatalf("the session never started (stat %v):\n%s", err, out)
	}
	if code != 1 || !strings.Contains(out, "interrupted before the re-validation") {
		t.Fatalf("exit %d, want 1 with the interrupted-before-re-validation error:\n%s", code, out)
	}
	assertRepairMarked(t, f)
}

// TestRepairMarkExitsPromotedLeavesNoBundle guards the other side: a repair
// whose re-validation promotes the tree leaves no bundle to carry a mark.
func TestRepairMarkExitsPromotedLeavesNoBundle(t *testing.T) {
	auOpts := testAutoupdateOptions()
	f := repairFixture(t, auOpts, 0)
	code, out := runRepair(t, auOpts, ttyDeps(true, true), "y\n")
	if code != 0 {
		t.Fatalf("passing re-validation exited %d, want 0:\n%s", code, out)
	}
	if _, err := os.Stat(f.bundlePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bundle still present after promotion (stat %v)", err)
	}
}
