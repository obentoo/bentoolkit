package main

// Authored for story 060, sub-task 4.1 — R6.1, R6.2, R6.5.
//
// Written from the contract (design C6):
//
//	type deps struct { /* one field per seam, typed like the variable it replaces */ }
//	func defaultDeps() *deps                  // the values today's variables default to
//	func newRootCmd() *cobra.Command          // = newRootCmdWith(defaultDeps())
//	func newRootCmdWith(d *deps) *cobra.Command
//
// 4.1 moves the 11 autoupdate seams, so this file names only those fields:
// registryWriter, confirmRegistryWrite, registryPromptIsInteractive,
// checkRegistryFixer, checkInteractive, resolveGentooProvider,
// setVersionsForCheck, uiIsTerminal, sweepPlanner, sweepExecutor, confirmSweep.
//
// The substituted seam is the sweep planner, reached by a standalone
// `overlay autoupdate --clean`: it is the first seam that run touches after
// reading packages.toml, so a stub that refuses is observable on stderr and in
// a call counter, with nothing written anywhere.
//
// Red on arrival: deps, defaultDeps and newRootCmdWith do not exist.

import (
	"errors"
	"log/slog"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/fatih/color"
	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/autoupdate/registry"
	"github.com/obentoo/bentoolkit/internal/common/output"
	"github.com/spf13/cobra"
)

// s060RunTree is testCLI.Run for a tree the test built itself: the same
// descriptor capture, colour opt-out and runMain entry point.
func s060RunTree(t *testing.T, root *cobra.Command, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	readOut := captureStream(t, 1, &os.Stdout)
	readErr := captureStream(t, 2, &os.Stderr)
	origColorOut, origNoColor := color.Output, color.NoColor
	color.Output, color.NoColor = os.Stdout, true
	func() {
		defer func() { color.Output, color.NoColor = origColorOut, origNoColor }()
		root.SetArgs(args)
		root.SetOut(os.Stdout)
		root.SetErr(os.Stderr)
		code = runMain(root)
	}()
	return readOut(), readErr(), code
}

const s060StubPlannerRefusal = "s060 stub planner refused the sweep"

// TestS060DepsSubstituteIsUsedOnlyInItsOwnTree is R6.1 and R6.5. Both trees
// are built before either runs, and they run interleaved: stubbed, production,
// stubbed again. The stub must answer every run of its own tree and none of
// the production tree's.
func TestS060DepsSubstituteIsUsedOnlyInItsOwnTree(t *testing.T) {
	c := newTestCLI(t)
	s058WriteRegistry(t, c.Overlay(), s058CleanRegistry)

	var (
		calls   int
		gotPath string
	)
	d := defaultDeps()
	d.sweepPlanner = func(_ *slog.Logger, overlayPath string, _ map[string]registry.PackageConfig, _ string) (autoupdate.SweepBatch, error) {
		calls++
		gotPath = overlayPath
		return autoupdate.SweepBatch{}, errors.New(s060StubPlannerRefusal)
	}
	stubbed := newRootCmdWith(d)
	production := newRootCmd()

	_, stderr, code := s060RunTree(t, stubbed, "overlay", "autoupdate", "--clean")
	if calls != 1 {
		t.Fatalf("the substituted planner was called %d time(s) by its own tree, want 1 (R6.1)\nstderr: %s", calls, stderr)
	}
	if gotPath != c.Overlay() {
		t.Errorf("the substituted planner got overlay %q, want %q", gotPath, c.Overlay())
	}
	if code != 1 || !strings.Contains(stderr, s060StubPlannerRefusal) {
		t.Errorf("stubbed tree: code %d, stderr %q; want 1 and the stub's refusal", code, stderr)
	}

	_, stderr, code = s060RunTree(t, production, "overlay", "autoupdate", "--clean")
	if calls != 1 {
		t.Errorf("the production tree reached the other tree's substitute (calls = %d) (R6.5)", calls)
	}
	if strings.Contains(stderr, s060StubPlannerRefusal) {
		t.Errorf("the production tree printed the substitute's refusal (R6.5): %q", stderr)
	}
	if code != 0 {
		t.Errorf("production tree on a clean registry with nothing to sweep exited %d, want 0\nstderr: %s", code, stderr)
	}

	s060RunTree(t, stubbed, "overlay", "autoupdate", "--clean")
	if calls != 2 {
		t.Errorf("after a production run, the stubbed tree's substitute was called %d time(s) in total, want 2 (R6.1)", calls)
	}

	// The substitution changed d, not the defaults every new tree starts from.
	if reflect.ValueOf(defaultDeps().sweepPlanner).Pointer() != reflect.ValueOf(autoupdate.PlanOverlaySweep).Pointer() {
		t.Error("defaultDeps() returned the substituted planner: a test's substitution leaked into the production wiring (R6.5)")
	}
}

// TestS060DepsDefaultsAreTheProductionImplementations is R6.2 for the 11
// autoupdate seams: each default is the very function the package variable held
// at 1462803. registryPromptIsInteractive was a closure there, so only its
// presence is pinned.
func TestS060DepsDefaultsAreTheProductionImplementations(t *testing.T) {
	d := defaultDeps()
	if d == nil {
		t.Fatal("defaultDeps() returned nil")
	}
	for _, tt := range []struct {
		field     string
		got, want any
	}{
		{"registryWriter", d.registryWriter, registry.SetPackageVersions},
		{"confirmRegistryWrite", d.confirmRegistryWrite, confirmAction},
		{"checkRegistryFixer", d.checkRegistryFixer, newConfiguredRegistryFixer},
		{"checkInteractive", d.checkInteractive, stdinIsTerminal},
		{"resolveGentooProvider", d.resolveGentooProvider, resolveGentooProvider},
		{"setVersionsForCheck", d.setVersionsForCheck, registry.SetPackageVersions},
		{"uiIsTerminal", d.uiIsTerminal, output.IsTerminal},
		{"sweepPlanner", d.sweepPlanner, autoupdate.PlanOverlaySweep},
		{"sweepExecutor", d.sweepExecutor, autoupdate.ExecuteOverlaySweep},
		{"confirmSweep", d.confirmSweep, confirmAction},
	} {
		got := reflect.ValueOf(tt.got)
		if got.Kind() != reflect.Func || got.IsNil() {
			t.Errorf("defaultDeps().%s is nil; the binary would have no implementation (R6.2)", tt.field)
			continue
		}
		if got.Pointer() != reflect.ValueOf(tt.want).Pointer() {
			t.Errorf("defaultDeps().%s is not the function its package variable held at 1462803 (R6.2)", tt.field)
		}
	}
	if d.registryPromptIsInteractive == nil {
		t.Error("defaultDeps().registryPromptIsInteractive is nil (R6.2)")
	}
}

// TestS060DepsEachCallIsAFreshValue: two defaultDeps() values are independent,
// so a substitution in one test's deps can never reach another tree.
func TestS060DepsEachCallIsAFreshValue(t *testing.T) {
	a, b := defaultDeps(), defaultDeps()
	if a == b {
		t.Fatal("defaultDeps() returned the same *deps twice; one tree's substitution would reach every tree (R6.5)")
	}
	a.confirmSweep = func(string) bool { return true }
	if reflect.ValueOf(b.confirmSweep).Pointer() != reflect.ValueOf(confirmAction).Pointer() {
		t.Error("substituting a field of one deps changed another (R6.5)")
	}
}
