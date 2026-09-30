package main

// Story 058, sub-task 11.1 — R1.3.
//
// The guards for restoreReportFlags and restoreAutoupdateFlags (testcli_test.go).
// A testCLI Run parses `overlay autoupdate` flags into package variables, and
// nothing resets them until the next tree is built; a test that calls
// runAutoupdate on the package-level autoupdateCmd reads whatever the last Run
// left. That is how TestRunAutoupdate_SignalCancels failed under -shuffle
// after the `--lint --fix --yes` row of
// TestS058AutoupdateRegistryModesKeepTheirExitCodes.
//
// # How a flag is matched to the restore
//
// By address. pflag keeps the bound pointer inside its Value: a bool, int or
// string flag's Value IS the pointer (a *boolValue converted from the *bool it
// was given), and a StringSlice flag's Value holds it in a field named value.
// flagStorage recovers it, and a Value shape it cannot read FAILS the guard by
// name rather than slipping past it.
//
// # How a package variable is told from per-tree storage
//
// By building two trees. A flag bound to a package variable points at the same
// address in both; one read off the command (--depth) or bound to a local of
// newRootCmd (--verbose, --quiet, --no-color) points at fresh storage in each,
// and cannot outlive the tree that owns it. Only the first kind can leak, so
// only the first kind must be restored — and no list of exemptions has to be
// kept by hand.
//
// The -run prefix TestAutoupdateFlagGlobals selects this file whole.

import (
	"reflect"
	"slices"
	"sort"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// flagStorage returns the address of the variable f writes to, and false when
// f's Value has a shape this guard does not know how to read.
func flagStorage(f *pflag.Flag) (uintptr, bool) {
	v := reflect.ValueOf(f.Value)
	if v.Kind() != reflect.Pointer || v.IsNil() {
		return 0, false
	}
	if v.Elem().Kind() != reflect.Struct {
		return v.Pointer(), true
	}
	field := v.Elem().FieldByName("value")
	if !field.IsValid() || field.Kind() != reflect.Pointer || field.IsNil() {
		return 0, false
	}
	return field.Pointer(), true
}

// autoupdateFlagsOf returns every flag `overlay autoupdate` accepts in root's
// tree — its own and the ones it inherits from the root — keyed by name.
func autoupdateFlagsOf(t *testing.T, root *cobra.Command) map[string]*pflag.Flag {
	t.Helper()

	cmd, _, err := root.Find([]string{"overlay", "autoupdate"})
	if err != nil || cmd == nil || cmd.Name() != "autoupdate" {
		t.Fatalf("newRootCmd() has no `overlay autoupdate` command: %v", err)
	}
	flags := map[string]*pflag.Flag{}
	add := func(f *pflag.Flag) { flags[f.Name] = f }
	cmd.LocalFlags().VisitAll(add)
	cmd.InheritedFlags().VisitAll(add)
	return flags
}

// restoredAddresses maps the address of every variable the two restore
// helpers put back to a printable name.
func restoredAddresses(t *testing.T) map[uintptr]string {
	t.Helper()

	covered := map[uintptr]string{}
	for _, p := range append(reportFlagGlobals(), autoupdateFlagGlobals()...) {
		v := reflect.ValueOf(p)
		covered[v.Pointer()] = v.Type().String()
	}
	return covered
}

// TestAutoupdateFlagGlobalsAreAllRestored fails when a flag of `overlay
// autoupdate` binds a package variable that neither reportFlagGlobals nor
// autoupdateFlagGlobals lists — the flag a later Run would leak into every test
// that reads the variable without building a tree.
//
// It also fails the other way: an entry of autoupdateFlagGlobals that no flag
// binds means the list was not derived from the source it claims to mirror.
func TestAutoupdateFlagGlobalsAreAllRestored(t *testing.T) {
	// Building a tree writes every default through its pointer; put back what
	// the tests before this one expect to find.
	restoreReportFlags(t)
	restoreAutoupdateFlags(t)

	first := autoupdateFlagsOf(t, newRootCmd())
	second := autoupdateFlagsOf(t, newRootCmd())
	covered := restoredAddresses(t)

	names := make([]string, 0, len(first))
	for name := range first {
		names = append(names, name)
	}
	sort.Strings(names)

	bound := map[uintptr]bool{}
	var shared, perTree []string
	for _, name := range names {
		a, okA := flagStorage(first[name])
		other, inBoth := second[name]
		if !inBoth {
			t.Errorf("--%s is on one fresh tree's `overlay autoupdate` and not on the other's", name)
			continue
		}
		b, okB := flagStorage(other)
		if !okA || !okB {
			t.Errorf("--%s: cannot read the variable its %T value writes to; teach flagStorage this shape so the guard still covers it", name, first[name].Value)
			continue
		}
		if a != b {
			perTree = append(perTree, name)
			continue
		}
		shared = append(shared, name)
		bound[a] = true
		if _, ok := covered[a]; !ok {
			t.Errorf("--%s binds a package variable that no restore helper puts back: add it to autoupdateFlagGlobals (testcli_test.go), or a Run that passes it leaks its value into later tests", name)
		}
	}

	for i, p := range autoupdateFlagGlobals() {
		v := reflect.ValueOf(p)
		if !bound[v.Pointer()] {
			t.Errorf("autoupdateFlagGlobals()[%d] (a %s) is bound by no `overlay autoupdate` flag: remove it, or the list no longer mirrors overlay_autoupdate.go", i, v.Type())
		}
	}

	// The classification must have seen both kinds, or the two-tree comparison
	// proved nothing: --check is a package variable, --depth is read off the
	// command.
	if !slices.Contains(shared, "check") || !slices.Contains(perTree, "depth") {
		t.Errorf("two-tree classification is off: --check must be shared (got shared=%v), --depth per-tree (got per-tree=%v)", shared, perTree)
	}
	if len(shared) < len(autoupdateFlagGlobals()) {
		t.Errorf("swept %d package-bound flags, fewer than the %d autoupdateFlagGlobals lists", len(shared), len(autoupdateFlagGlobals()))
	}
	t.Logf("swept %d flags: %d bound to package variables; %d per-tree %v", len(names), len(shared), len(perTree), perTree)
}

// TestAutoupdateFlagGlobalsRestoredAfterRun is the leak itself, end to end: a
// harness Run that sets autoupdate flags must leave every one of them as it
// found them once its test ends. The subtest also checks the Run DID change
// them, so the test cannot pass by never exercising the leak.
func TestAutoupdateFlagGlobalsRestoredAfterRun(t *testing.T) {
	restoreReportFlags(t)
	restoreAutoupdateFlags(t)

	// Name each variable by the flag that binds it, for the failure message.
	flagOf := map[uintptr]string{}
	for name, f := range autoupdateFlagsOf(t, newRootCmd()) {
		if addr, ok := flagStorage(f); ok {
			flagOf[addr] = "--" + name
		}
	}

	ptrs := append(reportFlagGlobals(), autoupdateFlagGlobals()...)
	snapshot := func() []any {
		values := make([]any, len(ptrs))
		for i, p := range ptrs {
			values[i] = reflect.ValueOf(p).Elem().Interface()
		}
		// The slice is copied so a later append cannot rewrite the snapshot.
		for i, v := range values {
			if s, ok := v.([]string); ok && s != nil {
				values[i] = append([]string(nil), s...)
			}
		}
		return values
	}
	before := snapshot()

	t.Run("run", func(t *testing.T) {
		c := newTestCLI(t)
		// The exit status is not the point: the flags are parsed into their
		// variables before RunE decides anything.
		c.Run("overlay", "autoupdate", "--lint", "--fix", "--yes", "--except", "cat/one,cat/two", "--ui=plain")
		if !autoupdateLint || !autoupdateFix || !autoupdateYes || len(autoupdateExcept) != 2 || autoupdateUI != "plain" {
			t.Fatalf("the Run did not set the flags it passed (lint=%v fix=%v yes=%v except=%v ui=%q); this test would pass without exercising the leak",
				autoupdateLint, autoupdateFix, autoupdateYes, autoupdateExcept, autoupdateUI)
		}
	})

	after := snapshot()
	for i, p := range ptrs {
		if !reflect.DeepEqual(before[i], after[i]) {
			t.Errorf("the variable %s binds was left at %#v after the Run's test ended; it was %#v before", flagOf[reflect.ValueOf(p).Pointer()], after[i], before[i])
		}
	}
}
