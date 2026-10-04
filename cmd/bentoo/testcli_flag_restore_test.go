package main

// Story 058, sub-task 11.1 — R1.3.
//
// The guard for restoreReportFlags (testcli_test.go). A testCLI Run parses
// the root report flags (--ui, --all, --export) into package variables, and
// nothing resets them until the next tree is built. The other flags of
// `overlay autoupdate` used to leak the same way — that is how
// TestRunAutoupdate_SignalCancels failed under -shuffle after the
// `--lint --fix --yes` row of TestS058AutoupdateRegistryModesKeepTheirExitCodes —
// until story 060 bound them to a per-tree autoupdateOptions; the two-tree
// proof of that is autoupdate_options_s060_test.go (R5.1).
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

// TestAutoupdateFlagGlobalsRestoredAfterRun is the leak itself, end to end: a
// harness Run that sets autoupdate flags must leave every one of them as it
// found them once its test ends. The subtest also checks the Run DID change
// them, so the test cannot pass by never exercising the leak.
func TestAutoupdateFlagGlobalsRestoredAfterRun(t *testing.T) {
	restoreReportFlags(t)

	// Name each variable by the flag that binds it, for the failure message.
	flagOf := map[uintptr]string{}
	for name, f := range autoupdateFlagsOf(t, newRootCmd()) {
		if addr, ok := flagStorage(f); ok {
			flagOf[addr] = "--" + name
		}
	}

	ptrs := reportFlagGlobals()
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
		if autoupdateUI != "plain" {
			t.Fatalf("the Run did not set the flag it passed (ui=%q); this test would pass without exercising the leak", autoupdateUI)
		}
	})

	after := snapshot()
	for i, p := range ptrs {
		if !reflect.DeepEqual(before[i], after[i]) {
			t.Errorf("the variable %s binds was left at %#v after the Run's test ended; it was %#v before", flagOf[reflect.ValueOf(p).Pointer()], after[i], before[i])
		}
	}
}
