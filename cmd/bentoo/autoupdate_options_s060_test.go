package main

// Authored for story 060, sub-task 3.1 — R5.1 (and the pflag gotcha of design
// C5 that R5.1 turns on).
//
// Written from the contract: every value `overlay autoupdate` binds a flag to
// belongs to one invocation — one autoupdateOptions per newAutoupdateCmd call —
// so two command trees in one process never see each other's values.
//
// The observation is the flag's own Value, read back from each tree. Today
// every flag is bound to a package variable, so the Value of tree A reads the
// same variable tree B writes; and pflag writes a flag's DEFAULT through its
// pointer when the flag is bound, so merely BUILDING tree B resets what tree A
// already parsed. Both are asserted, the second first: it is the hostile case
// (a tree that was never even parsed corrupts the other).
//
// Out of scope here and asserted nowhere: --ui, --all and --export are root
// persistent flags that stay package variables (design C5, D4); --depth is
// read off the command and was never a global.
//
// Red on arrival: tree A reads tree B's values (shared package variables).

import (
	"testing"

	"github.com/spf13/cobra"
)

// s060AutoupdateOf returns the `overlay autoupdate` command of root.
func s060AutoupdateOf(t *testing.T, root *cobra.Command) *cobra.Command {
	t.Helper()
	au, _, err := root.Find([]string{"overlay", "autoupdate"})
	if err != nil || au == nil || au.Name() != "autoupdate" {
		t.Fatalf("no `overlay autoupdate` in the tree: %v", err)
	}
	return au
}

func s060Parse(t *testing.T, au *cobra.Command, args ...string) {
	t.Helper()
	if err := au.ParseFlags(args); err != nil {
		t.Fatalf("ParseFlags(%q): %v", args, err)
	}
}

// s060FlagValues reads back each named flag's current value.
func s060FlagValues(t *testing.T, au *cobra.Command, names []string) map[string]string {
	t.Helper()
	out := make(map[string]string, len(names))
	for _, n := range names {
		f := au.Flags().Lookup(n)
		if f == nil {
			t.Fatalf("autoupdate has no --%s flag", n)
		}
		out[n] = f.Value.String()
	}
	return out
}

// s060TreeAArgs sets one value on every kind of flag autoupdate binds: int,
// string (including an explicitly empty one), bool, and a string slice.
var s060TreeAArgs = []string{
	"--check", "--force", "--compile", "--require-isolation", "--clean",
	"--concurrency", "7", "--timeout", "9", "--only", "bin",
	"--revivable", "--no-tui", "--yes", "--no-fetch-cache", "--llm",
	"--distdir", "/tmp/s060-a", "--distfiles-cache", "",
	"--except", "dev-libs/icu-compat", "--except", "media-libs/libjxl-compat",
}

var s060TreeAWant = map[string]string{
	"check": "true", "force": "true", "compile": "true", "require-isolation": "true", "clean": "true",
	"concurrency": "7", "timeout": "9", "only": "bin",
	"revivable": "true", "no-tui": "true", "yes": "true", "no-fetch-cache": "true", "llm": "true",
	"distdir": "/tmp/s060-a", "distfiles-cache": "",
	"except": "[dev-libs/icu-compat,media-libs/libjxl-compat]",
}

func s060FlagNames() []string {
	names := make([]string, 0, len(s060TreeAWant))
	for n := range s060TreeAWant {
		names = append(names, n)
	}
	return names
}

// TestS060OptionsBuildingASecondTreeLeavesTheFirstAlone is the pflag hostile
// case: tree B is only constructed, never parsed, and tree A must still hold
// every value it parsed.
func TestS060OptionsBuildingASecondTreeLeavesTheFirstAlone(t *testing.T) {
	newTestCLI(t) // isolated env; restores any flag state a shared global would leak

	a := s060AutoupdateOf(t, newRootCmd())
	s060Parse(t, a, s060TreeAArgs...)

	_ = s060AutoupdateOf(t, newRootCmd()) // build B, parse nothing

	got := s060FlagValues(t, a, s060FlagNames())
	for name, want := range s060TreeAWant {
		if got[name] != want {
			t.Errorf("tree A --%s = %q after a second tree was built, want %q — the second tree reset it (R5.1)", name, got[name], want)
		}
	}
}

// TestS060OptionsTwoTreesKeepTheirOwnFlagValues is R5.1 in both directions:
// two trees parse different values and each reads back only its own. Tree B
// passes nothing for most flags, so it must read the defaults, not tree A's
// values.
func TestS060OptionsTwoTreesKeepTheirOwnFlagValues(t *testing.T) {
	newTestCLI(t)

	a := s060AutoupdateOf(t, newRootCmd())
	b := s060AutoupdateOf(t, newRootCmd())
	s060Parse(t, a, s060TreeAArgs...)
	s060Parse(t, b, "--list", "--concurrency", "2", "--only", "source", "--except", "x11-misc/other")

	names := s060FlagNames()
	gotA := s060FlagValues(t, a, names)
	for name, want := range s060TreeAWant {
		if gotA[name] != want {
			t.Errorf("tree A --%s = %q, want its own %q (R5.1)", name, gotA[name], want)
		}
	}

	wantB := map[string]string{
		"concurrency": "2", "only": "source", "except": "[x11-misc/other]",
		"check": "false", "compile": "false", "yes": "false", "timeout": "0", "distdir": "",
	}
	// --distfiles-cache was not passed to B: it must read the flag's default,
	// not A's explicit "".
	wantB["distfiles-cache"] = b.Flags().Lookup("distfiles-cache").DefValue
	gotB := s060FlagValues(t, b, names)
	for name, want := range wantB {
		if gotB[name] != want {
			t.Errorf("tree B --%s = %q, want %q — tree A's value leaked into it (R5.1)", name, gotB[name], want)
		}
	}
	if !b.Flags().Changed("list") || a.Flags().Changed("list") {
		t.Errorf("--list Changed: A=%v B=%v, want A=false B=true", a.Flags().Changed("list"), b.Flags().Changed("list"))
	}
}

// TestS060OptionsDefaultsAreUnchanged pins U1 for the values the options
// struct now carries: a fresh tree reads every default it read at 1462803.
// It cannot pass by accident on a shared global, because tree A has just
// parsed non-default values into whatever it is bound to.
func TestS060OptionsDefaultsAreUnchanged(t *testing.T) {
	newTestCLI(t)

	a := s060AutoupdateOf(t, newRootCmd())
	s060Parse(t, a, s060TreeAArgs...)
	fresh := s060AutoupdateOf(t, newRootCmd())

	for _, name := range s060FlagNames() {
		f := fresh.Flags().Lookup(name)
		if got := f.Value.String(); got != f.DefValue {
			t.Errorf("fresh tree --%s = %q, want its default %q", name, got, f.DefValue)
		}
	}
	// And the first tree still reads what it parsed.
	if got := a.Flags().Lookup("concurrency").Value.String(); got != "7" {
		t.Errorf("tree A --concurrency = %q, want 7 (R5.1)", got)
	}
}
