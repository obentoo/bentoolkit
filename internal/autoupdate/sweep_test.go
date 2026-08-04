package autoupdate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"sync/atomic"
	"testing"
	"time"
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

// =============================================================================
// scopeConfigs (S027 sub-task 2.1)
// =============================================================================

// TestScopeConfigsKeepsEveryClaimantOfTheAtom is the G1 guard.
//
// scopeConfigs feeds a sweep that deletes files, and every entry it drops turns
// that entry's ebuild into an unclaimed file. A ":slot" or "@label" sibling — or
// a disabled or held entry — still HOLDS its ebuild, so dropping one would make
// the sweep delete a maintained release line.
func TestScopeConfigsKeepsEveryClaimantOfTheAtom(t *testing.T) {
	cfgs := map[string]PackageConfig{
		"media-plugins/gst-vpx@stable": regEntry("1.28.5", ""),
		"media-plugins/gst-vpx@dev":    regEntry("1.29.2", ""),
		"net-libs/webkit-gtk:4.1":      regEntry("2.52.5-r411", ""),
		"net-libs/webkit-gtk:6":        regEntry("2.52.5-r601", ""),
		"net-misc/rclone":              regEntry("1.75.0", ""),
		"kde-plasma/kwin":              regEntry("6.7.4", ""),
		"kde-plasma/breeze":            regEntry("6.7.4", ""),
	}
	disabled := regEntry("1.0.0", "")
	disabled.Enabled = boolPtr(false)
	cfgs["media-plugins/gst-vpx@legacy"] = disabled
	held := regEntry("2.0.0", "")
	held.Hold = true
	cfgs["net-libs/webkit-gtk:5"] = held

	t.Run("empty scope returns the map unchanged", func(t *testing.T) {
		got := scopeConfigs(cfgs, "", "")
		if len(got) != len(cfgs) {
			t.Errorf("len = %d, want %d", len(got), len(cfgs))
		}
	})

	t.Run("atom scope keeps @label siblings, disabled included", func(t *testing.T) {
		got := scopeConfigs(cfgs, "media-plugins/gst-vpx", "")
		want := []string{
			"media-plugins/gst-vpx@stable",
			"media-plugins/gst-vpx@dev",
			"media-plugins/gst-vpx@legacy",
		}
		if len(got) != len(want) {
			t.Fatalf("kept %d entries (%v), want %d", len(got), keysOf(got), len(want))
		}
		for _, key := range want {
			if _, ok := got[key]; !ok {
				t.Errorf("sibling %q was dropped — the sweep would delete its ebuild", key)
			}
		}
	})

	t.Run("atom scope keeps :slot siblings, held included", func(t *testing.T) {
		got := scopeConfigs(cfgs, "net-libs/webkit-gtk", "")
		for _, key := range []string{"net-libs/webkit-gtk:4.1", "net-libs/webkit-gtk:6", "net-libs/webkit-gtk:5"} {
			if _, ok := got[key]; !ok {
				t.Errorf("sibling %q was dropped", key)
			}
		}
		if _, ok := got["net-misc/rclone"]; ok {
			t.Error("an unrelated atom survived the filter")
		}
	})

	t.Run("category scope keeps every atom of that category", func(t *testing.T) {
		got := scopeConfigs(cfgs, "", "kde-plasma")
		if len(got) != 2 {
			t.Fatalf("kept %v, want the two kde-plasma entries", keysOf(got))
		}
		for _, key := range []string{"kde-plasma/kwin", "kde-plasma/breeze"} {
			if _, ok := got[key]; !ok {
				t.Errorf("%q was dropped", key)
			}
		}
	})
}

func keysOf(m map[string]PackageConfig) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// =============================================================================
// PlanOverlaySweep (S027 sub-task 2.2)
// =============================================================================

// sweepFixture builds an overlay whose directories cover every case the planner
// must distinguish, and returns the overlay path with its registry.
func sweepFixture(t *testing.T) (string, map[string]PackageConfig) {
	t.Helper()
	overlayDir := filepath.Join(t.TempDir(), "overlay")

	files := map[string][]string{
		"test-cat/residue":  {"1.0.0", "2.0.0"}, // stale ebuild left behind
		"test-cat/clean":    {"2.0.0"},          // nothing to do
		"test-cat/blocked":  {"1.0.0", "2.0.0"}, // claiming entry has no pin
		"test-cat/live":     {"1.0.0", "2.0.0", "9999"},
		"test-cat/disabled": {"1.0.0", "2.0.0"},
		"test-cat/held":     {"1.0.0", "2.0.0"},
		"other-cat/residue": {"1.0.0", "2.0.0"},
	}
	for pkg, versions := range files {
		for _, v := range versions {
			createTestEbuildFile(t, overlayDir, pkg, v)
		}
	}

	cfgs := map[string]PackageConfig{
		"test-cat/residue":  regEntry("2.0.0", ""),
		"test-cat/clean":    regEntry("2.0.0", ""),
		"test-cat/blocked":  regEntry("", ""),
		"test-cat/live":     regEntry("2.0.0", ""),
		"other-cat/residue": regEntry("2.0.0", ""),
	}
	disabled := regEntry("2.0.0", "")
	disabled.Enabled = boolPtr(false)
	cfgs["test-cat/disabled"] = disabled
	held := regEntry("2.0.0", "")
	held.Hold = true
	cfgs["test-cat/held"] = held

	return overlayDir, cfgs
}

func atomsOf(batch SweepBatch) []string {
	out := make([]string, 0, len(batch.Dirs))
	for _, d := range batch.Dirs {
		out = append(out, d.Atom)
	}
	return out
}

func dirIn(t *testing.T, batch SweepBatch, atom string) SweepDirPlan {
	t.Helper()
	for _, d := range batch.Dirs {
		if d.Atom == atom {
			return d
		}
	}
	t.Fatalf("%s not in batch %v", atom, atomsOf(batch))
	return SweepDirPlan{}
}

// TestPlanOverlaySweepMatchesReconcileUnclaimedSet is the R2.1 parity guard:
// what a sweep touches must be exactly what `--check` printed. Two
// implementations that merely agree today would drift; this pins them.
func TestPlanOverlaySweepMatchesReconcileUnclaimedSet(t *testing.T) {
	overlayDir, cfgs := sweepFixture(t)

	var want []string
	seen := map[string]bool{}
	for _, d := range Reconcile(overlayDir, cfgs) {
		if d.Kind == UnclaimedEbuild && !seen[d.Key] {
			seen[d.Key] = true
			want = append(want, d.Key)
		}
	}
	sort.Strings(want)

	batch, err := PlanOverlaySweep(overlayDir, cfgs, "")
	if err != nil {
		t.Fatalf("PlanOverlaySweep: %v", err)
	}
	if !reflect.DeepEqual(atomsOf(batch), want) {
		t.Errorf("candidates = %v, want the reconciliation's unclaimed atoms %v", atomsOf(batch), want)
	}
}

// TestPlanOverlaySweepSkipsDisabledAndHeld: R2.2 — those entries belong to the
// existing status reconciliation, and a held entry is a maintainer decision.
func TestPlanOverlaySweepSkipsDisabledAndHeld(t *testing.T) {
	overlayDir, cfgs := sweepFixture(t)
	batch, err := PlanOverlaySweep(overlayDir, cfgs, "")
	if err != nil {
		t.Fatalf("PlanOverlaySweep: %v", err)
	}
	for _, atom := range []string{"test-cat/disabled", "test-cat/held"} {
		for _, d := range batch.Dirs {
			if d.Atom == atom {
				t.Errorf("%s reached the batch; disabled and held directories are out of scope", atom)
			}
		}
	}
}

// TestPlanOverlaySweepCarriesBlockedDirectories: a directory whose entry has no
// pin removes nothing and reports its candidates instead (R5.1/R5.2). It must
// never appear as removable, and must not inflate TotalRemove.
func TestPlanOverlaySweepCarriesBlockedDirectories(t *testing.T) {
	overlayDir, cfgs := sweepFixture(t)
	batch, err := PlanOverlaySweep(overlayDir, cfgs, "")
	if err != nil {
		t.Fatalf("PlanOverlaySweep: %v", err)
	}

	blocked := dirIn(t, batch, "test-cat/blocked")
	if len(blocked.Remove) != 0 {
		t.Errorf("Remove = %v, want empty on a blocked directory", blocked.Remove)
	}
	if len(blocked.WouldRemove) == 0 {
		t.Error("WouldRemove is empty — a blocked directory must report its candidates")
	}
	if blocked.Blocked == "" {
		t.Error("Blocked is empty — this block has an entry to name")
	}
	if !blocked.IsBlocked() {
		t.Error("IsBlocked() = false on a blocked directory")
	}

	// TotalRemove counts pending work only.
	var want int
	for _, d := range batch.Dirs {
		want += len(d.Remove)
	}
	if batch.TotalRemove != want {
		t.Errorf("TotalRemove = %d, want %d", batch.TotalRemove, want)
	}
}

// TestPlanOverlaySweepNeverRemovesLiveEbuild: UB3 — a -9999 ebuild is the one
// file an overlay cannot restore by re-fetching a release.
func TestPlanOverlaySweepNeverRemovesLiveEbuild(t *testing.T) {
	overlayDir, cfgs := sweepFixture(t)
	batch, err := PlanOverlaySweep(overlayDir, cfgs, "")
	if err != nil {
		t.Fatalf("PlanOverlaySweep: %v", err)
	}
	live := dirIn(t, batch, "test-cat/live")
	for _, v := range live.Remove {
		if v == "9999" {
			t.Fatal("the live ebuild is in Remove")
		}
	}
	if _, kept := live.Keep["9999"]; !kept {
		t.Errorf("Keep = %v, want the live ebuild kept", live.Keep)
	}
}

// TestPlanOverlaySweepIsDeterministic: the operator approves one batch, so two
// runs over an unchanged overlay must produce the identical plan — a diff
// between them has to mean the overlay changed.
func TestPlanOverlaySweepIsDeterministic(t *testing.T) {
	overlayDir, cfgs := sweepFixture(t)
	first, err := PlanOverlaySweep(overlayDir, cfgs, "")
	if err != nil {
		t.Fatalf("PlanOverlaySweep: %v", err)
	}
	second, err := PlanOverlaySweep(overlayDir, cfgs, "")
	if err != nil {
		t.Fatalf("PlanOverlaySweep: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Errorf("two runs disagree:\n%+v\n%+v", first, second)
	}
}

// TestPlanOverlaySweepScopesToTarget: an atom target plans one directory, a
// category target plans that category, and both leave the rest alone.
func TestPlanOverlaySweepScopesToTarget(t *testing.T) {
	overlayDir, cfgs := sweepFixture(t)

	atomBatch, err := PlanOverlaySweep(overlayDir, cfgs, "test-cat/residue")
	if err != nil {
		t.Fatalf("atom target: %v", err)
	}
	if !reflect.DeepEqual(atomsOf(atomBatch), []string{"test-cat/residue"}) {
		t.Errorf("atom target planned %v, want [test-cat/residue]", atomsOf(atomBatch))
	}

	catBatch, err := PlanOverlaySweep(overlayDir, cfgs, "other-cat")
	if err != nil {
		t.Fatalf("category target: %v", err)
	}
	if !reflect.DeepEqual(atomsOf(catBatch), []string{"other-cat/residue"}) {
		t.Errorf("category target planned %v, want [other-cat/residue]", atomsOf(catBatch))
	}
}

// TestPlanOverlaySweepRejectsInvalidTarget: R1.4 — a typo must fail loudly and
// before any directory is read, not read as "nothing to clean".
func TestPlanOverlaySweepRejectsInvalidTarget(t *testing.T) {
	overlayDir, cfgs := sweepFixture(t)

	for _, target := range []string{"no-such-category", "test-cat/no-such-pkg", "cat/pkg/extra", "/", "kde-plasma@dev"} {
		t.Run(target, func(t *testing.T) {
			if _, err := PlanOverlaySweep(overlayDir, cfgs, target); err == nil {
				t.Errorf("target %q was accepted", target)
			} else if !errors.Is(err, ErrInvalidSweepTarget) {
				t.Errorf("error = %v, want ErrInvalidSweepTarget", err)
			}
		})
	}
}

// TestPlanOverlaySweepAcceptsARegistryKey: pasting a key from packages.toml is
// the obvious thing to do; it normalises to the atom, and the path is built
// from the atom either way (S027-G4).
func TestPlanOverlaySweepAcceptsARegistryKey(t *testing.T) {
	overlayDir, cfgs := sweepFixture(t)
	batch, err := PlanOverlaySweep(overlayDir, cfgs, "test-cat/residue:0")
	if err != nil {
		t.Fatalf("registry key as target: %v", err)
	}
	if !reflect.DeepEqual(atomsOf(batch), []string{"test-cat/residue"}) {
		t.Errorf("planned %v, want [test-cat/residue]", atomsOf(batch))
	}
}

// =============================================================================
// ExecuteOverlaySweep (S027 sub-tasks 3.1 and 3.2)
// =============================================================================

// TestExecuteOverlaySweepIsolatesFailures: one locked directory must not strand
// the rest of the batch (R4.4). The sweep exists to clear an accumulation, and
// an all-or-nothing batch would make that accumulation permanent.
func TestExecuteOverlaySweepIsolatesFailures(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: a read-only directory does not block removal")
	}
	overlayDir, cfgs := sweepFixture(t)
	batch, err := PlanOverlaySweep(overlayDir, cfgs, "")
	if err != nil {
		t.Fatalf("PlanOverlaySweep: %v", err)
	}

	locked := filepath.Join(overlayDir, "test-cat", "residue")
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	var runs atomic.Int64
	report := ExecuteOverlaySweep(context.Background(), overlayDir, batch,
		WithSweepExecCommand(countingManifestSeam(&runs)))

	var failed, swept int
	for _, r := range report.Results {
		if r.Err != nil {
			failed++
			if r.Atom != "test-cat/residue" {
				t.Errorf("unexpected failure on %s: %v", r.Atom, r.Err)
			}
		}
		if len(r.Removed) > 0 {
			swept++
		}
	}
	if failed != 1 {
		t.Errorf("failed directories = %d, want 1", failed)
	}
	if swept == 0 {
		t.Error("no directory was swept — one failure stranded the batch")
	}
	if report.Failed != failed {
		t.Errorf("report.Failed = %d, want %d", report.Failed, failed)
	}
}

// TestExecuteOverlaySweepLeavesBlockedDirectoriesAlone: R5.1 — a blocked
// directory is carried into the report untouched, and its files stay on disk.
func TestExecuteOverlaySweepLeavesBlockedDirectoriesAlone(t *testing.T) {
	overlayDir, cfgs := sweepFixture(t)
	batch, err := PlanOverlaySweep(overlayDir, cfgs, "")
	if err != nil {
		t.Fatalf("PlanOverlaySweep: %v", err)
	}

	var runs atomic.Int64
	report := ExecuteOverlaySweep(context.Background(), overlayDir, batch,
		WithSweepExecCommand(countingManifestSeam(&runs)))

	var found bool
	for _, r := range report.Results {
		if r.Atom != "test-cat/blocked" {
			continue
		}
		found = true
		if len(r.Removed) != 0 {
			t.Errorf("Removed = %v on a blocked directory", r.Removed)
		}
		if len(r.WouldRemove) == 0 {
			t.Error("WouldRemove is empty on a blocked directory")
		}
	}
	if !found {
		t.Fatal("the blocked directory is missing from the report")
	}
	if report.BlockedN != 1 {
		t.Errorf("report.BlockedN = %d, want 1", report.BlockedN)
	}
	for _, v := range []string{"1.0.0", "2.0.0"} {
		p := filepath.Join(overlayDir, "test-cat", "blocked", "blocked-"+v+".ebuild")
		if _, err := os.Stat(p); err != nil {
			t.Errorf("blocked directory lost %s: %v", v, err)
		}
	}
}

// TestExecuteOverlaySweepRespectsConcurrencyBound: the pool never runs more
// directories at once than it was told to.
func TestExecuteOverlaySweepRespectsConcurrencyBound(t *testing.T) {
	overlayDir := filepath.Join(t.TempDir(), "overlay")
	cfgs := map[string]PackageConfig{}
	for i := 0; i < 8; i++ {
		pkg := fmt.Sprintf("test-cat/pkg%d", i)
		createTestEbuildFile(t, overlayDir, pkg, "1.0.0")
		createTestEbuildFile(t, overlayDir, pkg, "2.0.0")
		cfgs[pkg] = regEntry("2.0.0", "")
	}
	batch, err := PlanOverlaySweep(overlayDir, cfgs, "")
	if err != nil {
		t.Fatalf("PlanOverlaySweep: %v", err)
	}
	if len(batch.Dirs) != 8 {
		t.Fatalf("planned %d directories, want 8", len(batch.Dirs))
	}

	const bound = 3
	var inFlight, peak atomic.Int64
	seam := func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		cur := inFlight.Add(1)
		for {
			old := peak.Load()
			if cur <= old || peak.CompareAndSwap(old, cur) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		inFlight.Add(-1)
		return exec.CommandContext(ctx, "true")
	}

	report := ExecuteOverlaySweep(context.Background(), overlayDir, batch,
		WithSweepConcurrency(bound), WithSweepExecCommand(seam))

	if peak.Load() > bound {
		t.Errorf("peak concurrency = %d, want <= %d", peak.Load(), bound)
	}
	if report.Swept != 8 {
		t.Errorf("Swept = %d, want 8", report.Swept)
	}
	if report.Removed != 8 {
		t.Errorf("Removed = %d, want 8", report.Removed)
	}
}

// TestExecuteOverlaySweepStopsOnCancelledContext: a SIGINT stops the batch
// rather than continuing to delete.
func TestExecuteOverlaySweepStopsOnCancelledContext(t *testing.T) {
	overlayDir, cfgs := sweepFixture(t)
	batch, err := PlanOverlaySweep(overlayDir, cfgs, "")
	if err != nil {
		t.Fatalf("PlanOverlaySweep: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var runs atomic.Int64
	report := ExecuteOverlaySweep(ctx, overlayDir, batch,
		WithSweepExecCommand(countingManifestSeam(&runs)))

	if report.Removed != 0 {
		t.Errorf("Removed = %d on a cancelled context, want 0", report.Removed)
	}
	if report.Failed == 0 {
		t.Error("a cancelled batch reported no failures")
	}
}

// TestSweepManifestVersionIsAKeptVersion is the G2 guard.
//
// runManifest forwards its versions to the authenticated-distfile pre-fetch, so
// handing it a version that was just deleted pre-fetches a file nobody needs and
// leaves a surviving ebuild's distfile unfetched. The versions must come from
// Keep, highest non-live first, never from Remove.
func TestSweepManifestVersionIsAKeptVersion(t *testing.T) {
	t.Run("highest non-live kept version comes first", func(t *testing.T) {
		got := survivingVersions(map[string]string{
			"1.0.0": "cat/pkg", "2.0.0": "cat/pkg", "9999": "",
		})
		want := []string{"2.0.0", "1.0.0"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("survivingVersions = %v, want %v", got, want)
		}
	})

	t.Run("live-only Keep yields nothing to pre-fetch", func(t *testing.T) {
		if got := survivingVersions(map[string]string{"9999": ""}); len(got) != 0 {
			t.Errorf("survivingVersions = %v, want empty", got)
		}
	})

	t.Run("a plan keeping nothing is refused, not executed", func(t *testing.T) {
		overlayDir := filepath.Join(t.TempDir(), "overlay")
		pkg := "test-cat/only"
		createTestEbuildFile(t, overlayDir, pkg, "1.0.0")
		var runs atomic.Int64
		s := newSweeper(overlayDir, withSweeperExec(countingManifestSeam(&runs)))

		res := sweepOneDir(s, SweepDirPlan{Atom: pkg, Remove: []string{"1.0.0"}, Keep: map[string]string{}})
		if res.Err == nil {
			t.Fatal("a plan that keeps nothing was executed")
		}
		if _, err := os.Stat(s.ebuildPath(pkg, "1.0.0")); err != nil {
			t.Errorf("the only release was removed: %v", err)
		}
		if runs.Load() != 0 {
			t.Errorf("manifest runs = %d, want 0", runs.Load())
		}
	})
}
