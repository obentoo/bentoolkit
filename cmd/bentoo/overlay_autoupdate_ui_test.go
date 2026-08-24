package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/report"
)

// allFlagVariableName is the package variable the --all flag binds to. It is
// named here rather than discovered so that renaming the variable without
// updating this test fails loudly instead of silently measuring nothing.
const allFlagVariableName = "autoupdateAll"

// TestUIFlagRejectsUnknownValue pins R3.9, and both halves of it. Naming the
// legal set is what turns a rejection into an instruction; and the check must
// NOT run, because a run that ignores a flag it could not understand does work
// the operator did not ask for.
func TestUIFlagRejectsUnknownValue(t *testing.T) {
	mode, _, err := resolveUIMode(uiInputs{Flag: "bogus", Interactive: true})

	if err == nil {
		t.Fatal("--ui=bogus was accepted (R3.9)")
	}
	for _, legal := range []string{"auto", "plain", "inline", "fullscreen"} {
		if !strings.Contains(err.Error(), legal) {
			t.Errorf("the error %q does not name the legal value %q", err.Error(), legal)
		}
	}
	if mode != "" {
		t.Errorf("a rejected mode still returned %q — the caller could run on it", mode)
	}
}

// TestNoTUIAlias pins R3.4: --no-tui behaves exactly as --ui=plain, including
// when the config asked for something else.
func TestNoTUIAlias(t *testing.T) {
	for _, cfgMode := range []string{"", "auto", "inline", "fullscreen"} {
		mode, _, err := resolveUIMode(uiInputs{NoTUI: true, Config: cfgMode, Interactive: true})
		if err != nil {
			t.Fatalf("resolveUIMode with --no-tui and config %q: %v", cfgMode, err)
		}
		if mode != report.ModePlain {
			t.Errorf("--no-tui with ui.mode=%q resolved to %q, want %q", cfgMode, mode, report.ModePlain)
		}
	}
}

// TestNoTUIHonoursNoColor is the precondition sub-task 1.4 recorded and could
// not enforce from inside the report package.
//
// tui.Enabled opts out on THREE signals — the flag, BENTOO_NO_TUI and NO_COLOR
// — and ModeInputs has a field for only two of them. If the caller does not
// fold NO_COLOR in, a user who sets it gets plain output today and inline
// output after this story, which is exactly the change R3.7 forbids for someone
// who configured nothing about ui.mode.
func TestNoTUIHonoursNoColor(t *testing.T) {
	for _, env := range []string{"NO_COLOR", "BENTOO_NO_TUI"} {
		t.Run(env, func(t *testing.T) {
			t.Setenv(env, "1")

			mode, _, err := resolveUIMode(uiInputs{Interactive: true})
			if err != nil {
				t.Fatalf("resolveUIMode: %v", err)
			}
			if mode != report.ModePlain {
				t.Errorf("%s is set and the mode resolved to %q, want %q: an operator who opted out today must not be opted back in (R3.7)",
					env, mode, report.ModePlain)
			}
		})
	}
}

// TestExportFormatFromExtension pins R9.2.
func TestExportFormatFromExtension(t *testing.T) {
	cases := map[string]exportFormat{
		"/tmp/report.md":     exportMarkdown,
		"/tmp/report.json":   exportJSON,
		"/tmp/report.log":    exportPlain,
		"/tmp/report.txt":    exportPlain,
		"/tmp/report":        exportPlain,
		"/tmp/REPORT.MD":     exportMarkdown,
		"/tmp/a.b.c/rep.MD":  exportMarkdown,
		"/tmp/report.json/x": exportPlain,
	}

	for path, want := range cases {
		if got := exportFormatFor(path); got != want {
			t.Errorf("exportFormatFor(%q) = %v, want %v", path, got, want)
		}
	}
}

// TestExportWritesTheCompleteReport — R9.3 at the file, not at the renderer.
func TestExportWritesTheCompleteReport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.md")

	if err := writeExport(path, exportFixture()); err != nil {
		t.Fatalf("writeExport: %v", err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the export: %v", err)
	}
	if !strings.Contains(string(body), "app-misc/jq") {
		t.Errorf("the export does not name a package the report holds\n%s", body)
	}
	if strings.Contains(string(body), "…") {
		t.Error("the export carries an ellipsis — something was shortened (R9.3)")
	}
}

// TestExportFailureStillRenders pins R9.5. An export is a convenience; the
// report on the terminal is the answer. Losing the answer because a path was
// wrong would be the tail wagging the dog.
//
// Here the failure is only required to be REPORTED as an error the caller can
// print — that the terminal render still happens is asserted where the two meet
// (sub-task 6.5).
func TestExportFailureStillRenders(t *testing.T) {
	unwritable := filepath.Join(t.TempDir(), "no-such-directory", "report.md")

	err := writeExport(unwritable, exportFixture())
	if err == nil {
		t.Fatal("writing to an unwritable path returned no error")
	}
	if !strings.Contains(err.Error(), "report.md") {
		t.Errorf("the error %q does not name the path that failed — an operator cannot fix what it does not name", err.Error())
	}
}

// TestAllDoesNotChangeActions pins R8.4. --all changes what is LISTED, never
// what is scanned, validated or acted upon.
//
// It checks that MECHANICALLY, the way sub-task 1.2 checks the model's import
// boundary, because the alternative is a test that asserts a promise. Every
// reference to the --all variable in this package must sit on a line that
// builds a renderer Options — nowhere near a filter, a loop over packages, or a
// call that acts on one.
//
// The reason to spend a parser on this: --all is a display flag, and a display
// flag that quietly narrows the work is the most expensive kind of bug here.
// The overlay auto-commits and pushes, so "acted on fewer packages than the
// operator believes" reaches origin.
func TestAllDoesNotChangeActions(t *testing.T) {
	fset := token.NewFileSet()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}

	var uses int
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}

		ast.Inspect(file, func(n ast.Node) bool {
			ident, ok := n.(*ast.Ident)
			if !ok || ident.Name != allFlagVariableName {
				return true
			}
			uses++

			line := fset.Position(ident.Pos()).Line
			if !onARenderOptionsLine(t, name, line) {
				t.Errorf("%s:%d references %s outside a renderer Options — a display flag must not reach what the run acts on (R8.4)",
					name, line, allFlagVariableName)
			}
			return true
		})
	}

	// A sweep that found nothing passes vacuously, and would keep passing after
	// somebody renamed the variable out from under it.
	if uses == 0 {
		t.Fatalf("no reference to %q was found in this package — the sweep measured nothing", allFlagVariableName)
	}
}

// onARenderOptionsLine reports whether the given line of the given file
// mentions the renderer Options the --all flag is allowed to reach. Declaration
// and flag-registration lines are allowed too: they define the flag rather than
// act on it.
func onARenderOptionsLine(t *testing.T, file string, line int) bool {
	t.Helper()

	body, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("reading %s: %v", file, err)
	}

	lines := strings.Split(string(body), "\n")
	if line-1 < 0 || line-1 >= len(lines) {
		return false
	}
	text := lines[line-1]

	for _, allowed := range []string{"ShowAll", "render.Options", "Flags()", "var ", "\tallFlag"} {
		if strings.Contains(text, allowed) {
			return true
		}
	}
	return false
}

// TestUnwritableExportStillRendersToTheTerminal is R9.5 at the point where the
// two meet, which is where sub-task 6.3's own test said it belonged.
//
// The export is a convenience and the report on the terminal is the answer, so
// losing the answer because a path was wrong would be the tail wagging the dog.
// Asserting it HERE — through presentCheckReport, the function that does both —
// is what makes the ordering a property of the code rather than of the order
// somebody happened to write two statements in: writing the file first would
// let a bad path cost the operator the report itself.
//
// It also pins the other half of R9.6: an export that could not be written
// changes no count. The tally rendered to the terminal is the fixture's own.
func TestUnwritableExportStillRendersToTheTerminal(t *testing.T) {
	originalExport := autoupdateExport
	autoupdateExport = filepath.Join(t.TempDir(), "no-such-directory", "report.md")
	t.Cleanup(func() { autoupdateExport = originalExport })

	out := captureStdout(t, func() { presentCheckReport(exportFixture(), false) })

	if !strings.Contains(out, "app-misc/jq") {
		t.Errorf("the export failed and the terminal received no report (R9.5):\n%s", out)
	}
	if !strings.Contains(strings.ToLower(out), "proved") {
		t.Errorf("the rendered report does not carry the tally, so a failed export cost a count (R9.6):\n%s", out)
	}
	if _, err := os.Stat(autoupdateExport); err == nil {
		t.Fatal("the fixture path was writable after all — this case measured nothing")
	}
}

// ---- story 045, sub-task 1.2: the export cannot skip, by construction ----

// TestPlainExportDoesNotInheritSkipPlan is R2.4's implementation half, and D3
// names it as the whole of R2.4's implementation risk: exactly one line.
//
// Markdown and JSON take no Options at all, so neither CAN honour a screen
// setting — that half is structural and TestExportKeepsThePlanTheScreenSkipped
// asserts it in the render package. The plain export is the exception: it does
// build a render.Options, and it is the one place a screen setting could leak
// into a file. A record missing the plan answers no question later, because the
// plan is where a package's reason is stated at all.
//
// # Why the scope is this whole file rather than that one line
//
// The screen's Options is built in overlay_autoupdate_check.go
// (presentCheckReport), not here. This file builds exactly one render.Options
// and it is the export's, so "this file never names SkipPlan" and "the export
// never sets SkipPlan" are the same claim — and the first is the one a reader
// can check without following a call graph.
//
// # It is green on arrival, so it carries its own vacuity guard
//
// Zero today and it must stay zero, which means the assertion alone proves
// nothing: it would also pass if renderExport were deleted, renamed, or moved
// to another file. The first check is what keeps it honest — the literal it
// guards must still be here for its absence of a field to mean anything.
func TestPlainExportDoesNotInheritSkipPlan(t *testing.T) {
	const file = "overlay_autoupdate_ui.go"

	src, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("reading %s: %v", file, err)
	}
	text := string(src)

	// Vacuity guard: the thing being guarded must exist.
	if got := strings.Count(text, "render.Options{"); got != 1 {
		t.Fatalf("%s builds %d render.Options literal(s), want exactly 1 — the export's. "+
			"With none, the SkipPlan assertion below passes without measuring anything; "+
			"with more, this file gained a second Options and the claim needs restating (D3)", file, got)
	}

	if got := strings.Count(text, "SkipPlan"); got != 0 {
		t.Errorf("%s names SkipPlan %d time(s), want 0. The only render.Options this file builds "+
			"is the plain export's, and an export must never skip the plan: a record missing it "+
			"answers no question later (R2.4, D3)", file, got)
	}
}
