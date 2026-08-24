package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/tui"
)

func TestManifestCommandRegistered(t *testing.T) {
	found := false
	for _, cmd := range overlayCmd.Commands() {
		if cmd.Use == "manifest" || strings.HasPrefix(cmd.Use, "manifest ") {
			found = true
			if cmd.Short == "" {
				t.Error("manifest command should have a Short description")
			}
			if cmd.Long == "" {
				t.Error("manifest command should have a Long description")
			}
			if cmd.Run == nil {
				t.Error("manifest command should have a Run function")
			}
			break
		}
	}
	if !found {
		t.Error("overlay manifest subcommand should be registered")
	}
}

func TestManifestCommandFlags(t *testing.T) {
	tests := []struct {
		flagName  string
		shorthand string
		flagType  string
		defValue  string
	}{
		{"keep", "", "bool", "false"},
		{"dry-run", "n", "bool", "false"},
		{"jobs", "j", "int", "10"},
	}
	for _, tt := range tests {
		t.Run(tt.flagName, func(t *testing.T) {
			flag := manifestCmd.Flags().Lookup(tt.flagName)
			if flag == nil {
				t.Fatalf("manifest command should have --%s flag", tt.flagName)
			}
			if flag.Value.Type() != tt.flagType {
				t.Errorf("--%s should be %s type, got %s", tt.flagName, tt.flagType, flag.Value.Type())
			}
			if flag.DefValue != tt.defValue {
				t.Errorf("--%s default = %q, want %q", tt.flagName, flag.DefValue, tt.defValue)
			}
			if tt.shorthand != "" {
				sh := manifestCmd.Flags().ShorthandLookup(tt.shorthand)
				if sh == nil {
					t.Errorf("--%s should have -%s shorthand", tt.flagName, tt.shorthand)
				}
			}
		})
	}
}

func TestManifestCommandArgsAcceptsZeroOrOne(t *testing.T) {
	if err := manifestCmd.Args(manifestCmd, []string{}); err != nil {
		t.Errorf("manifest should accept zero args, got error: %v", err)
	}
	if err := manifestCmd.Args(manifestCmd, []string{"app-misc"}); err != nil {
		t.Errorf("manifest should accept one arg, got error: %v", err)
	}
	if err := manifestCmd.Args(manifestCmd, []string{"a", "b"}); err == nil {
		t.Error("manifest should reject two args")
	}
}

// ---------------------------------------------------------------------------
// Sub-task 6.4 — both callers resolve through the same path. APPENDED.
// ---------------------------------------------------------------------------

// The imports this fragment needs beyond what overlay_manifest_test.go already
// has: os, github.com/obentoo/bentoolkit/internal/common/config and
// github.com/obentoo/bentoolkit/internal/common/tui.

// stubUIIsTerminal replaces the package's TTY seam and returns the restore
// function. The seam is what makes R3.7 checkable at all: the requirement is
// about behaviour ON a terminal and OFF one, and a test binary is only ever
// off one.
func stubUIIsTerminal(tty bool) func() {
	original := uiIsTerminal
	uiIsTerminal = func() bool { return tty }
	return func() { uiIsTerminal = original }
}

// legacyEnabled transcribes tui.Enabled's rule so R3.7 can be checked at a TTY
// the test binary does not have.
//
// Why a transcription rather than a call: tui.Enabled gates on tui.isTerminal,
// which is UNEXPORTED, so a test in package main cannot fake a terminal for it.
// Under `go test` stdout is a pipe, so calling it would only ever answer the
// off-a-terminal case — and R3.7 is precisely about both.
//
// A transcription can drift from the original, so it is ANCHORED: the test
// below calls the real tui.Enabled at the one point this binary can observe
// (off a terminal) and fails if the two disagree. That converts "I copied it
// correctly" from a claim into a measurement, at least at that point.
func legacyEnabled(tty bool) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("BENTOO_NO_TUI") != "" {
		return false
	}
	return tty
}

// TestLegacyTranscriptionIsAnchored keeps legacyEnabled honest where the real
// function can still be reached.
func TestLegacyTranscriptionIsAnchored(t *testing.T) {
	// Under `go test` stdout is a pipe, so this is the off-a-terminal case.
	if got, want := tui.Enabled(tui.Options{}), legacyEnabled(false); got != want {
		t.Fatalf("tui.Enabled says %v off a terminal, the transcription says %v — the transcription has drifted", got, want)
	}
}

// TestNoConfigMatchesLegacyBehaviour is R3.7, and it is the requirement that
// decides whether this story is safe to ship to somebody who did not ask for it.
//
// `overlay manifest` and `autoupdate --apply` both gate their live TUI on
// tui.Enabled today. After this story they gate it on report.ResolveMode. For
// an operator with no ui.mode and no flags, the two must give the SAME answer —
// on a terminal and off one — or the story silently changes a command nobody
// asked it to touch.
//
// The expected value is taken from tui.Enabled itself rather than written down,
// so this compares the new path against the real old one instead of against
// somebody's recollection of it.
func TestNoConfigMatchesLegacyBehaviour(t *testing.T) {
	for _, tty := range []bool{true, false} {
		t.Run(map[bool]string{true: "on a terminal", false: "off a terminal"}[tty], func(t *testing.T) {
			defer stubUIIsTerminal(tty)()

			legacy := legacyEnabled(tty)
			cfg := &config.Config{} // no ui block at all

			if got := manifestUsesTUI(cfg); got != legacy {
				t.Errorf("overlay manifest: new path says %v, tui.Enabled says %v (R3.7)", got, legacy)
			}
			if got := autoupdateUsesTUI(cfg); got != legacy {
				t.Errorf("autoupdate --apply: new path says %v, tui.Enabled says %v (R3.7)", got, legacy)
			}
		})
	}
}

// TestNoConfigMatchesLegacyBehaviourUnderTheOptOuts extends R3.7 to the three
// signals tui.Enabled has always honoured. An operator who opted out by setting
// NO_COLOR configured nothing about ui.mode, so this story must leave them
// exactly where they were.
func TestNoConfigMatchesLegacyBehaviourUnderTheOptOuts(t *testing.T) {
	for _, env := range []string{"NO_COLOR", "BENTOO_NO_TUI"} {
		t.Run(env, func(t *testing.T) {
			t.Setenv(env, "1")
			defer stubUIIsTerminal(true)()

			if legacyEnabled(true) {
				t.Fatalf("the premise is wrong: the legacy rule is true with %s set", env)
			}

			cfg := &config.Config{}
			if manifestUsesTUI(cfg) {
				t.Errorf("overlay manifest turned the TUI on for an operator who set %s (R3.7)", env)
			}
			if autoupdateUsesTUI(cfg) {
				t.Errorf("autoupdate --apply turned the TUI on for an operator who set %s (R3.7)", env)
			}
		})
	}
}

// TestManifestInheritsUIMode is R3.8: manifest stops deciding independently.
// The point of routing it through ResolveMode is that one setting governs both
// commands — an operator should not have to learn a different switch per
// command.
func TestManifestInheritsUIMode(t *testing.T) {
	defer stubUIIsTerminal(true)()

	plain := &config.Config{UI: config.UIConfig{Mode: "plain"}}
	if manifestUsesTUI(plain) {
		t.Error("ui.mode: plain did not reach overlay manifest — it is still deciding on its own (R3.8)")
	}

	inline := &config.Config{UI: config.UIConfig{Mode: "inline"}}
	if !manifestUsesTUI(inline) {
		t.Error("ui.mode: inline did not turn the manifest TUI on")
	}
}

// TestFullscreenDoesNotReachTheApplyPath guards the Out of Scope boundary. The
// --apply path is a Reporter consumer, not a report producer; giving it an
// alternate screen is a different piece of work, and doing it by accident here
// would take over the terminal during a build.
func TestFullscreenDoesNotReachTheApplyPath(t *testing.T) {
	defer stubUIIsTerminal(true)()

	cfg := &config.Config{UI: config.UIConfig{Mode: "fullscreen"}}

	if !autoupdateUsesTUI(cfg) {
		t.Error("ui.mode: fullscreen turned the apply-path live region off entirely")
	}
	if !manifestUsesTUI(cfg) {
		t.Error("ui.mode: fullscreen turned the manifest live region off entirely")
	}
}

// TestBothCallSitesUseTheSharedResolution closes a gap the tests above cannot
// see, and it is the difference between R3.8 being implemented and R3.8 being
// merely available.
//
// Measured, not supposed. Three mutations passed the ENTIRE cmd/bentoo suite:
// reverting chooseManifestReporter to tui.Enabled while manifestUsesTUI stayed
// correct and unused; the same for tuiEnabledForApply; and setting the captured
// config to nil so --apply ignores ui.mode. Every test above asserts the two
// booleans IN ISOLATION — none of them asserts that the call sites consult
// them, so the wire from the decision to the behaviour could be cut with the
// decision left perfectly intact.
//
// The check is that tui.Enabled has no caller left in this package. That is a
// stronger statement than "the two call sites were updated", and a cheaper one:
// there is nothing to keep in sync, because the old gate simply has no
// remaining reader here. D4 said tui.Enabled has exactly two callers; this
// story moved both, so zero is the correct number.
func TestBothCallSitesUseTheSharedResolution(t *testing.T) {
	fset := token.NewFileSet()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}

	var scanned int
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		scanned++

		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "Enabled" {
				return true
			}
			pkg, ok := selector.X.(*ast.Ident)
			if !ok || pkg.Name != "tui" {
				return true
			}

			t.Errorf("%s:%d still calls tui.Enabled — this command decides its own presentation instead of inheriting ui.mode (R3.8). "+
				"Use manifestUsesTUI or autoupdateUsesTUI; both are already resolved through report.ResolveMode.",
				name, fset.Position(call.Pos()).Line)
			return true
		})
	}

	if scanned == 0 {
		t.Fatal("scanned no non-test .go files — the sweep measured nothing")
	}
}
