package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
)

// =============================================================================
// Standalone sweep — CLI wiring (S027 task 5)
// =============================================================================

func setSweepConfirm(t *testing.T, fn func(string) bool) {
	t.Helper()
	orig := confirmSweepFn
	t.Cleanup(func() { confirmSweepFn = orig })
	confirmSweepFn = fn
}

func setSweepExecutor(t *testing.T, fn func(context.Context, string, autoupdate.SweepBatch, ...autoupdate.SweepOption) autoupdate.SweepReport) {
	t.Helper()
	orig := sweepExecutorFn
	t.Cleanup(func() { sweepExecutorFn = orig })
	sweepExecutorFn = fn
}

func setSweepClean(t *testing.T, v bool) {
	t.Helper()
	orig := autoupdateClean
	t.Cleanup(func() { autoupdateClean = orig })
	autoupdateClean = v
}

// sweepOverlayFixture writes an overlay with one directory holding residue and
// a registry that pins the surviving version.
func sweepOverlayFixture(t *testing.T) string {
	t.Helper()
	overlayDir := filepath.Join(t.TempDir(), "overlay")
	pkgDir := filepath.Join(overlayDir, "test-cat", "residue")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, v := range []string{"1.0.0", "2.0.0"} {
		body := "EAPI=8\nDESCRIPTION=\"t\"\nSLOT=\"0\"\nKEYWORDS=\"~amd64\"\n"
		if err := os.WriteFile(filepath.Join(pkgDir, "residue-"+v+".ebuild"), []byte(body), 0o644); err != nil {
			t.Fatalf("write ebuild: %v", err)
		}
	}
	registryDir := filepath.Join(overlayDir, ".autoupdate")
	if err := os.MkdirAll(registryDir, 0o755); err != nil {
		t.Fatalf("mkdir registry: %v", err)
	}
	registry := `["test-cat/residue"]
url = "https://example.invalid/releases"
parser = "json"
path = "version"
version = "2.0.0"
`
	if err := os.WriteFile(filepath.Join(registryDir, "packages.toml"), []byte(registry), 0o644); err != nil {
		t.Fatalf("write registry: %v", err)
	}
	return overlayDir
}

func residueExists(t *testing.T, overlayDir, version string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(overlayDir, "test-cat", "residue", "residue-"+version+".ebuild"))
	return err == nil
}

// TestSweepRoutingKeepsApplyClean is the G6 guard: the `--clean` case must sit
// below both `--apply` cases, or every existing `--apply … --clean` invocation
// silently becomes an overlay-wide sweep.
func TestSweepRoutingKeepsApplyClean(t *testing.T) {
	// The switch in runAutoupdate is ordered source-wise; assert the ordering
	// directly rather than driving the whole command, which would need config,
	// network seams and a pending list.
	src, err := os.ReadFile("overlay_autoupdate.go")
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	text := string(src)
	applyAll := strings.Index(text, `case autoupdateApply == "all":`)
	applyOne := strings.Index(text, `case autoupdateApply != "":`)
	clean := strings.Index(text, "case autoupdateClean:")
	if applyAll < 0 || applyOne < 0 || clean < 0 {
		t.Fatal("the routing switch no longer has the expected cases")
	}
	if clean < applyAll || clean < applyOne {
		t.Error("the --clean case is above an --apply case: `--apply … --clean` would become an overlay sweep")
	}
}

// TestRunSweepRejectsInvalidTarget: R1.4 — a typo fails loudly, before any file
// is touched, rather than reading as "nothing to clean".
func TestRunSweepRejectsInvalidTarget(t *testing.T) {
	overlayDir := sweepOverlayFixture(t)
	setSweepClean(t, true)
	setSweepConfirm(t, func(string) bool {
		t.Fatal("a confirmation was asked for an invalid target")
		return false
	})

	code, exited := captureStdoutExit(t, func() {
		runSweep(context.Background(), overlayDir, []string{"no-such-category"})
	})
	if !exited || code == 0 {
		t.Errorf("exit code = %d, exited = %v; want a non-zero exit", code, exited)
	}
	if !residueExists(t, overlayDir, "1.0.0") {
		t.Error("a file was removed despite the invalid target")
	}
}

// TestRunSweepFailsWithoutARegistry: a sweep with no registry has no claims,
// and "no entry claims this" is the state in which nothing may be removed.
// Failing beats an empty batch that reads as "nothing to clean".
func TestRunSweepFailsWithoutARegistry(t *testing.T) {
	overlayDir := filepath.Join(t.TempDir(), "overlay")
	if err := os.MkdirAll(overlayDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	setSweepClean(t, true)

	code, exited := captureStdoutExit(t, func() {
		runSweep(context.Background(), overlayDir, nil)
	})
	if !exited || code == 0 {
		t.Errorf("exit code = %d, exited = %v; want a non-zero exit", code, exited)
	}
}

// TestRunSweepDeclinedRemovesNothing: R3.3 — the executor is never entered, so
// the overlay is byte-identical by construction rather than by care.
func TestRunSweepDeclinedRemovesNothing(t *testing.T) {
	overlayDir := sweepOverlayFixture(t)
	setSweepClean(t, true)
	setReconcileYes(t, false)
	setReconcileInteractive(t, func() bool { return true })
	setSweepConfirm(t, func(string) bool { return false })
	setSweepExecutor(t, func(context.Context, string, autoupdate.SweepBatch, ...autoupdate.SweepOption) autoupdate.SweepReport {
		t.Fatal("the executor ran after the confirmation was declined")
		return autoupdate.SweepReport{}
	})

	out := captureStdout(t, func() {
		runSweep(context.Background(), overlayDir, nil)
	})

	if !residueExists(t, overlayDir, "1.0.0") || !residueExists(t, overlayDir, "2.0.0") {
		t.Error("a declined sweep removed a file")
	}
	if !strings.Contains(out, "residue-1.0.0.ebuild") {
		t.Errorf("the plan did not list the candidate before the prompt:\n%s", out)
	}
}

// TestRunSweepNonInteractiveWithoutYesRemovesNothing is the A5 guard: `--clean`
// used to print help, so a script may already pass it. Making it act must not
// make that script delete anything.
func TestRunSweepNonInteractiveWithoutYesRemovesNothing(t *testing.T) {
	overlayDir := sweepOverlayFixture(t)
	setSweepClean(t, true)
	setReconcileYes(t, false)
	setReconcileInteractive(t, func() bool { return false })
	setSweepConfirm(t, func(string) bool {
		t.Fatal("a piped run was prompted")
		return true
	})
	setSweepExecutor(t, func(context.Context, string, autoupdate.SweepBatch, ...autoupdate.SweepOption) autoupdate.SweepReport {
		t.Fatal("a piped run reached the executor")
		return autoupdate.SweepReport{}
	})

	out := captureStdout(t, func() {
		runSweep(context.Background(), overlayDir, nil)
	})

	if !residueExists(t, overlayDir, "1.0.0") {
		t.Error("a piped sweep removed a file")
	}
	if !strings.Contains(out, "--yes") {
		t.Errorf("the report does not say how to run it unattended:\n%s", out)
	}
}

// TestRunSweepWithYesProceedsUnprompted: R3.4 — an explicit, in-so-many-words
// approval works from a pipe, a cron job or a CI step.
func TestRunSweepWithYesProceedsUnprompted(t *testing.T) {
	overlayDir := sweepOverlayFixture(t)
	setSweepClean(t, true)
	setReconcileYes(t, true)
	setReconcileInteractive(t, func() bool { return false })
	setSweepConfirm(t, func(string) bool {
		t.Fatal("--yes still prompted")
		return true
	})

	var ran bool
	setSweepExecutor(t, func(_ context.Context, _ string, batch autoupdate.SweepBatch, _ ...autoupdate.SweepOption) autoupdate.SweepReport {
		ran = true
		if batch.TotalRemove != 1 {
			t.Errorf("TotalRemove = %d, want 1", batch.TotalRemove)
		}
		return autoupdate.SweepReport{
			Results: []autoupdate.SweepDirResult{{
				Atom:    "test-cat/residue",
				Removed: []string{"1.0.0"},
				Kept:    map[string]string{"2.0.0": "test-cat/residue"},
			}},
			Removed: 1, Swept: 1,
		}
	})

	out := captureStdout(t, func() {
		runSweep(context.Background(), overlayDir, nil)
	})
	if !ran {
		t.Fatal("--yes did not reach the executor")
	}
	if !strings.Contains(out, "claimed by test-cat/residue") {
		t.Errorf("the report does not name the claiming entry:\n%s", out)
	}
}

// TestRunSweepNothingToDoDoesNotPrompt: R3.5 — asking "remove 0 files?" would
// train the operator to say yes.
func TestRunSweepNothingToDoDoesNotPrompt(t *testing.T) {
	overlayDir := sweepOverlayFixture(t)
	// Remove the residue so the plan is empty.
	if err := os.Remove(filepath.Join(overlayDir, "test-cat", "residue", "residue-1.0.0.ebuild")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	setSweepClean(t, true)
	setReconcileYes(t, false)
	setReconcileInteractive(t, func() bool { return true })
	setSweepConfirm(t, func(string) bool {
		t.Fatal("an empty plan asked for confirmation")
		return false
	})

	out := captureStdout(t, func() {
		runSweep(context.Background(), overlayDir, nil)
	})
	if !strings.Contains(out, "Nothing to sweep") {
		t.Errorf("an empty plan did not say so:\n%s", out)
	}
}

// TestDisplaySweepPlanShowsBlockedDirectories: R5.1/R5.2 — a blocked directory
// names the entry to fix and states what it would have removed.
func TestDisplaySweepPlanShowsBlockedDirectories(t *testing.T) {
	batch := autoupdate.SweepBatch{
		Dirs: []autoupdate.SweepDirPlan{
			{
				Atom:        "test-cat/blocked",
				Blocked:     "test-cat/blocked",
				WouldRemove: []string{"1.0.0"},
				Keep:        map[string]string{},
			},
			{
				Atom:        "test-cat/orphan",
				WouldRemove: []string{"0.9.0"},
				Keep:        map[string]string{},
			},
		},
	}
	out := captureStdout(t, func() { displaySweepPlan(batch) })

	for _, want := range []string{
		"has no version pin",
		"no registry entry claims this directory",
		"would have removed: 1.0.0",
		"would have removed: 0.9.0",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plan output missing %q:\n%s", want, out)
		}
	}
}

// TestDisplaySweepReportCountsAndExitPolicy: R6.2/R6.3 — the summary is where a
// human sees that work remains; a blocked or failed directory does not turn the
// command into a failure.
func TestDisplaySweepReportCountsAndExitPolicy(t *testing.T) {
	report := autoupdate.SweepReport{
		Results: []autoupdate.SweepDirResult{
			{Atom: "a/one", Removed: []string{"1.0.0"}, Kept: map[string]string{"2.0.0": "a/one"}},
			{Atom: "a/two", Blocked: "a/two", WouldRemove: []string{"1.0.0"}},
			{Atom: "a/three", Err: context.Canceled},
		},
		Removed: 1, Swept: 1, BlockedN: 1, Failed: 1,
	}
	out := captureStdout(t, func() { displaySweepReport(report) })

	for _, want := range []string{
		"Removed: one-1.0.0.ebuild",
		"claimed by a/one",
		"Swept 1 director(ies), removed 1 ebuild(s).",
		"1 director(ies) blocked",
		"1 director(ies) failed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report output missing %q:\n%s", want, out)
		}
	}
}

// TestKeptLinesNamesTheClaimingEntry: R6.1 — "kept 1.28.5" invites the question
// the report exists to answer.
func TestKeptLinesNamesTheClaimingEntry(t *testing.T) {
	lines := keptLines(map[string]string{
		"1.28.5": "media-plugins/gst-vpx@stable",
		"9999":   "",
	})
	if len(lines) != 2 {
		t.Fatalf("lines = %v, want 2", lines)
	}
	if !strings.Contains(lines[0], "claimed by media-plugins/gst-vpx@stable") {
		t.Errorf("first line = %q", lines[0])
	}
	if !strings.Contains(lines[1], "kept by rule") {
		t.Errorf("second line = %q", lines[1])
	}
}

// =============================================================================
// The reconciliation points at the sweep (S027 sub-task 6.1)
// =============================================================================

// TestDivergenceReportPointsAtTheSweep: R7.1 — the report used to end at the
// finding. It now names the command that acts on it, but only when there is
// something to act on.
func TestDivergenceReportPointsAtTheSweep(t *testing.T) {
	const pointer = "bentoo overlay autoupdate --clean"

	t.Run("printed after a non-empty unclaimed group", func(t *testing.T) {
		divs := []autoupdate.Divergence{
			{Key: "cat/pkg", Kind: autoupdate.UnclaimedEbuild, Disk: "1.0.0"},
		}
		out := captureStdout(t, func() { displayDivergences(divs, 0) })
		if !strings.Contains(out, pointer) {
			t.Errorf("the unclaimed group did not name the sweep command:\n%s", out)
		}
	})

	t.Run("absent when no ebuild is unclaimed", func(t *testing.T) {
		divs := []autoupdate.Divergence{
			{Key: "cat/pkg", Kind: autoupdate.StalePin, Pin: "1.0.0", Disk: "2.0.0"},
			{Key: "cat/other", Kind: autoupdate.NoEbuild, Pin: "1.0.0"},
		}
		out := captureStdout(t, func() { displayDivergences(divs, 1) })
		if strings.Contains(out, pointer) {
			t.Errorf("the sweep command was suggested with nothing to sweep:\n%s", out)
		}
	})
}
