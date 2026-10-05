package main

// Authored for story 060, sub-task 4.4 — R6.3.
//
// A test that substitutes a dependency without touching process-wide state
// (environment, file descriptors) can call t.Parallel() and pass under -race.
// Every test here does exactly that: it calls an autoupdate mode function
// DIRECTLY on an autoupdateRun carrying its own options and its own deps, and
// never goes through testCLI or t.Setenv (design gotcha "t.Parallel with
// t.Setenv").
//
// No test here builds a command tree. pflag writes each flag's default
// through its pointer when the flag is bound, and the flags of every command
// but `overlay autoupdate` stay package variables in this story (out of scope),
// so two trees built at once from parallel tests would race on them. That is a
// property of the remaining globals, not of deps, and it is why the validate,
// prune, compare and snapshot families are not proven parallel here: their run
// functions are reached only through those trees.
//
// The calling convention is the one design C5 allows: the mode helpers become
// methods on *autoupdateRun —
//
//	func (r *autoupdateRun) reconcileRegistryAfterCheck(overlayPath string)
//	func (r *autoupdateRun) runSweep(ctx context.Context, overlayPath string, args []string, concurrency int) error
//
// with their parameter lists otherwise unchanged from 1462803. If the
// implementation takes the run as a parameter instead, only the call lines
// below change.
//
// Red on arrival: autoupdateRun, autoupdateOptions and deps do not exist.

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
)

// s060ReconcileOverlay lays out one pinless registry entry and its ebuild: the
// reconciliation finds exactly one stale pin, app-editors/neovim -> 0.11.1.
func s060ReconcileOverlay(t *testing.T) (overlayDir string, registry []byte) {
	t.Helper()
	overlayDir = t.TempDir()
	pkgDir := filepath.Join(overlayDir, "app-editors", "neovim")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ebuild := "EAPI=8\nDESCRIPTION=\"s060\"\nSLOT=\"0\"\nKEYWORDS=\"~amd64\"\nSRC_URI=\"\"\nLICENSE=\"MIT\"\n"
	if err := os.WriteFile(filepath.Join(pkgDir, "neovim-0.11.1.ebuild"), []byte(ebuild), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgDir := filepath.Join(overlayDir, ".autoupdate")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	registry = []byte("[\"app-editors/neovim\"]\n" +
		"url = \"https://example.invalid/neovim\"\n" +
		"parser = \"json\"\n" +
		"path = \"version\"\n" +
		"comments = \"\"\"fixture record for app-editors/neovim\"\"\"\n" +
		"# END\n")
	if err := os.WriteFile(filepath.Join(cfgDir, "packages.toml"), registry, 0o644); err != nil {
		t.Fatal(err)
	}
	return overlayDir, registry
}

func s060AssertRegistryUntouched(t *testing.T, overlayDir string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(overlayDir, ".autoupdate", "packages.toml"))
	if err != nil {
		t.Fatalf("read packages.toml: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("packages.toml was rewritten; only the substituted writer may be called\nbefore: %s\nafter:  %s", want, got)
	}
}

// TestS060ParallelReconcileWritesThroughItsOwnWriter: with --yes the batch
// goes to THIS run's writer — never the production one, which would rewrite
// the file — and nothing asks whether it may prompt.
func TestS060ParallelReconcileWritesThroughItsOwnWriter(t *testing.T) {
	t.Parallel()
	overlayDir, registry := s060ReconcileOverlay(t)

	var (
		mu      sync.Mutex
		batches []map[string]string
	)
	d := defaultDeps()
	d.registryWriter = func(path string, pins map[string]string) error {
		mu.Lock()
		defer mu.Unlock()
		if path != overlayDir {
			t.Errorf("writer got overlay %q, want %q", path, overlayDir)
		}
		batches = append(batches, pins)
		return nil
	}
	d.registryPromptIsInteractive = func() bool {
		t.Error("--yes must not consult the TTY probe")
		return false
	}
	d.confirmRegistryWrite = func(string) bool {
		t.Error("--yes must not prompt")
		return false
	}
	run := &autoupdateRun{opts: &autoupdateOptions{yes: true}, deps: d}

	run.reconcileRegistryAfterCheck(overlayDir)

	mu.Lock()
	defer mu.Unlock()
	want := []map[string]string{{"app-editors/neovim": "0.11.1"}}
	if !reflect.DeepEqual(batches, want) {
		t.Errorf("writer batches = %v, want %v (R6.3)", batches, want)
	}
	s060AssertRegistryUntouched(t, overlayDir, registry)
}

// TestS060ParallelReconcileDeclineNeverWrites: an interactive run whose
// operator declines asks once and calls no writer.
func TestS060ParallelReconcileDeclineNeverWrites(t *testing.T) {
	t.Parallel()
	overlayDir, registry := s060ReconcileOverlay(t)

	asked := 0
	d := defaultDeps()
	d.registryPromptIsInteractive = func() bool { return true }
	d.confirmRegistryWrite = func(string) bool { asked++; return false }
	d.registryWriter = func(string, map[string]string) error {
		t.Error("a declined reconciliation called the writer")
		return nil
	}
	run := &autoupdateRun{opts: &autoupdateOptions{}, deps: d}

	run.reconcileRegistryAfterCheck(overlayDir)

	if asked != 1 {
		t.Errorf("the substituted confirmation was asked %d time(s), want 1 (R6.3)", asked)
	}
	s060AssertRegistryUntouched(t, overlayDir, registry)
}

// TestS060ParallelReconcileNonInteractiveAsksNothing: without --yes and
// without a terminal, nothing is asked and nothing is written.
func TestS060ParallelReconcileNonInteractiveAsksNothing(t *testing.T) {
	t.Parallel()
	overlayDir, registry := s060ReconcileOverlay(t)

	probed := 0
	d := defaultDeps()
	d.registryPromptIsInteractive = func() bool { probed++; return false }
	d.confirmRegistryWrite = func(string) bool {
		t.Error("a non-interactive run prompted")
		return true
	}
	d.registryWriter = func(string, map[string]string) error {
		t.Error("a non-interactive run without --yes called the writer")
		return nil
	}
	run := &autoupdateRun{opts: &autoupdateOptions{}, deps: d}

	run.reconcileRegistryAfterCheck(overlayDir)

	if probed == 0 {
		t.Error("the substituted TTY probe was never consulted; the run used another one (R6.3)")
	}
	s060AssertRegistryUntouched(t, overlayDir, registry)
}

// TestS060ParallelSweepUsesItsOwnPlanner: a standalone sweep plans through
// THIS run's planner, with the overlay and the target it was given.
func TestS060ParallelSweepUsesItsOwnPlanner(t *testing.T) {
	t.Parallel()
	overlayDir, _ := s060ReconcileOverlay(t)

	refusal := errors.New("s060 parallel stub planner refused")
	var gotPath, gotTarget string
	calls := 0
	d := defaultDeps()
	d.sweepPlanner = func(_ *slog.Logger, path string, cfgs map[string]autoupdate.PackageConfig, target string) (autoupdate.SweepBatch, error) {
		calls++
		gotPath, gotTarget = path, target
		if _, ok := cfgs["app-editors/neovim"]; !ok {
			t.Errorf("planner got registry %v, want the overlay's entries", cfgs)
		}
		return autoupdate.SweepBatch{}, refusal
	}
	d.sweepExecutor = func(_ context.Context, _ string, _ autoupdate.SweepBatch, _ ...autoupdate.SweepOption) autoupdate.SweepReport {
		t.Error("the executor ran after the planner refused")
		return autoupdate.SweepReport{}
	}
	run := &autoupdateRun{opts: &autoupdateOptions{yes: true}, deps: d}

	err := run.runSweep(t.Context(), overlayDir, []string{"app-editors"}, 1)

	if calls != 1 || gotPath != overlayDir || gotTarget != "app-editors" {
		t.Errorf("planner calls = %d (path %q, target %q), want 1 with %q and %q (R6.3)", calls, gotPath, gotTarget, overlayDir, "app-editors")
	}
	if code := exitCodeFor(err); code != 1 {
		t.Errorf("runSweep after a refused plan exited %d, want 1", code)
	}
}

// TestS060ParallelDefaultDepsAreIndependent: each test's deps is its own, so
// substitutions made by tests running at the same time never meet.
func TestS060ParallelDefaultDepsAreIndependent(t *testing.T) {
	t.Parallel()
	a, b := defaultDeps(), defaultDeps()
	a.sweepPlanner = func(*slog.Logger, string, map[string]autoupdate.PackageConfig, string) (autoupdate.SweepBatch, error) {
		return autoupdate.SweepBatch{}, nil
	}
	a.registryWriter = func(string, map[string]string) error { return nil }
	if reflect.ValueOf(b.sweepPlanner).Pointer() != reflect.ValueOf(autoupdate.PlanOverlaySweep).Pointer() ||
		reflect.ValueOf(b.registryWriter).Pointer() != reflect.ValueOf(autoupdate.SetPackageVersions).Pointer() {
		t.Error("a substitution in one deps reached another (R6.3)")
	}
}
