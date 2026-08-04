package autoupdate

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
)

// =============================================================================
// sweeper — the extracted executor (S027 sub-task 1.1)
// =============================================================================

// TestNewSweeperNormalisesNilFields is the G5 guard, and it is deliberately NOT
// a field-inspection test.
//
// A nil ctx does not fail loudly at construction: it panics inside
// context.WithTimeout on the first Manifest run, which is a production crash for
// any sweeper built outside NewApplier. So the test builds the sweeper the way a
// forgetful caller would — supplying only the exec seam, and a nil option for
// good measure — and then drives the path that would panic.
func TestNewSweeperNormalisesNilFields(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	pkg := "test-cat/test-pkg"
	createTestEbuildFile(t, overlayDir, pkg, "1.0.0")

	var runs atomic.Int64
	// No context, no reporter, and a nil option: everything newSweeper must
	// normalise. If it does not, runManifest panics on context.WithTimeout(nil).
	s := newSweeper(overlayDir, nil, withSweeperExec(countingManifestSeam(&runs)))

	if s.ctx == nil || s.execCommand == nil || s.reporter == nil {
		t.Fatalf("newSweeper left a nil field: ctx=%v execCommand=%v reporter=%v",
			s.ctx == nil, s.execCommand == nil, s.reporter == nil)
	}

	plan := sweepPlan{Keep: map[string]string{}, Remove: []string{"1.0.0"}}
	if _, err := s.execute(pkg, plan, "1.0.0"); err != nil {
		t.Fatalf("execute on a normalised sweeper: %v", err)
	}
	if got := runs.Load(); got != 1 {
		t.Errorf("manifest runs = %d, want 1", got)
	}
}

// TestSweeperExecuteRemovesAscendingAndRunsManifestOnce covers the ordinary
// path: every condemned version goes, in ascending order, and the Manifest is
// regenerated exactly once — after the last removal, not once per file
// (S021-R4.2, carried into S027-R4.1/R4.2).
func TestSweeperExecuteRemovesAscendingAndRunsManifestOnce(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	pkg := "test-cat/test-pkg"
	for _, v := range []string{"1.0.0", "1.5.0", "2.0.0"} {
		createTestEbuildFile(t, overlayDir, pkg, v)
	}

	var runs atomic.Int64
	s := newSweeper(overlayDir, withSweeperExec(countingManifestSeam(&runs)))

	plan := sweepPlan{
		Keep:   map[string]string{"2.0.0": pkg},
		Remove: []string{"1.0.0", "1.5.0"},
	}
	got, err := s.execute(pkg, plan, "2.0.0")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	if !reflect.DeepEqual(got.Remove, []string{"1.0.0", "1.5.0"}) {
		t.Errorf("Remove = %v, want [1.0.0 1.5.0]", got.Remove)
	}
	if runs.Load() != 1 {
		t.Errorf("manifest runs = %d, want exactly 1", runs.Load())
	}
	for _, v := range []string{"1.0.0", "1.5.0"} {
		if _, err := os.Stat(s.ebuildPath(pkg, v)); !os.IsNotExist(err) {
			t.Errorf("%s still on disk", v)
		}
	}
	if _, err := os.Stat(s.ebuildPath(pkg, "2.0.0")); err != nil {
		t.Errorf("kept version 2.0.0 was removed: %v", err)
	}
}

// TestSweeperExecuteSkipsMissingFileWithoutCounting: a file already gone meets
// the sweep's goal for that file, but it is NOT a removal — so a directory whose
// candidates had all vanished must not trigger a Manifest regeneration for a
// change that never happened.
func TestSweeperExecuteSkipsMissingFileWithoutCounting(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	pkg := "test-cat/test-pkg"
	createTestEbuildFile(t, overlayDir, pkg, "2.0.0")

	var runs atomic.Int64
	s := newSweeper(overlayDir, withSweeperExec(countingManifestSeam(&runs)))

	// 1.0.0 is condemned but is not on disk.
	plan := sweepPlan{Keep: map[string]string{"2.0.0": pkg}, Remove: []string{"1.0.0"}}
	got, err := s.execute(pkg, plan, "2.0.0")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(got.Remove) != 0 {
		t.Errorf("Remove = %v, want empty — a missing file is not a removal", got.Remove)
	}
	if runs.Load() != 0 {
		t.Errorf("manifest runs = %d, want 0 — nothing was actually removed", runs.Load())
	}
}

// TestSweeperExecuteSkipsManifestWhenNothingRemoved: an empty plan removes
// nothing and must not shell out at all.
func TestSweeperExecuteSkipsManifestWhenNothingRemoved(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	pkg := "test-cat/test-pkg"
	createTestEbuildFile(t, overlayDir, pkg, "2.0.0")

	var runs atomic.Int64
	s := newSweeper(overlayDir, withSweeperExec(countingManifestSeam(&runs)))

	if _, err := s.execute(pkg, sweepPlan{Keep: map[string]string{"2.0.0": pkg}}, "2.0.0"); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if runs.Load() != 0 {
		t.Errorf("manifest runs = %d, want 0", runs.Load())
	}
}

// TestSweeperExecuteReturnsRemovedPrefixOnFailure: a sweep cut short by a failed
// removal reports what really went away, not what it intended. The report must
// never claim a file that is still on disk.
func TestSweeperExecuteReturnsRemovedPrefixOnFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: a read-only directory does not block removal")
	}
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	pkg := "test-cat/test-pkg"
	for _, v := range []string{"1.0.0", "1.5.0", "2.0.0"} {
		createTestEbuildFile(t, overlayDir, pkg, v)
	}
	pkgDir := filepath.Join(overlayDir, "test-cat", "test-pkg")

	var runs atomic.Int64
	s := newSweeper(overlayDir, withSweeperExec(countingManifestSeam(&runs)))

	// Remove the first file, then lock the directory so the second removal fails.
	if err := os.Remove(s.ebuildPath(pkg, "1.0.0")); err != nil {
		t.Fatalf("pre-removal: %v", err)
	}
	createTestEbuildFile(t, overlayDir, pkg, "1.0.0")
	if err := os.Chmod(pkgDir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(pkgDir, 0o755) })

	plan := sweepPlan{Keep: map[string]string{"2.0.0": pkg}, Remove: []string{"1.0.0", "1.5.0"}}
	got, err := s.execute(pkg, plan, "2.0.0")
	if err == nil {
		t.Fatal("execute: expected an error from the locked directory")
	}
	if len(got.Remove) != 0 {
		t.Errorf("Remove = %v, want empty — nothing could be removed", got.Remove)
	}
	if runs.Load() != 0 {
		t.Errorf("manifest runs = %d, want 0 after a failed removal", runs.Load())
	}
}

// TestSweeperEbuildPathRejectsMalformedAtom: paths are built from split
// components, never from a raw registry key (S027-G4).
func TestSweeperEbuildPathRejectsMalformedAtom(t *testing.T) {
	s := newSweeper("/overlay")
	if got := s.ebuildPath("not-an-atom", "1.0.0"); got != "" {
		t.Errorf("ebuildPath(%q) = %q, want empty", "not-an-atom", got)
	}
	want := filepath.Join("/overlay", "cat", "pkg", "pkg-1.0.0.ebuild")
	if got := s.ebuildPath("cat/pkg", "1.0.0"); got != want {
		t.Errorf("ebuildPath = %q, want %q", got, want)
	}
}

// TestSweeperExecuteHonoursCancelledContext: the parent context reaches the
// spawned command, so a SIGINT during a sweep kills the in-flight pkgdev.
func TestSweeperExecuteHonoursCancelledContext(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	pkg := "test-cat/test-pkg"
	createTestEbuildFile(t, overlayDir, pkg, "1.0.0")
	createTestEbuildFile(t, overlayDir, pkg, "2.0.0")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := newSweeper(overlayDir,
		withSweeperContext(ctx),
		withSweeperExec(mockExecCommandSuccess),
	)

	plan := sweepPlan{Keep: map[string]string{"2.0.0": pkg}, Remove: []string{"1.0.0"}}
	if _, err := s.execute(pkg, plan, "2.0.0"); err == nil {
		t.Error("execute with a cancelled context: expected the manifest step to fail")
	}
}
