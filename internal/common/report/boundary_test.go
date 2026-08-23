package report

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// forbiddenImports carries the two rules this package must not cross. Each
// rule travels with its own remedy, because they are different rules with
// different fixes and a shared message would send half the readers the wrong
// way: presentation belongs in the render package, but a dependency on
// internal/autoupdate is not fixed by moving it there — render sits under
// internal/common too. That one is fixed by taking the primitive fact instead.
var forbiddenImports = []struct {
	prefix string
	rule   string
	remedy string
}{
	{"github.com/charmbracelet/lipgloss", "presentation (R1.2)", presentationRemedy},
	{"github.com/charmbracelet/bubbletea", "presentation (R1.2)", presentationRemedy},
	{"github.com/charmbracelet/bubbles", "presentation (R1.2)", presentationRemedy},
	{"golang.org/x/term", "presentation (R1.2)", presentationRemedy},
	{"github.com/obentoo/bentoolkit/internal/autoupdate", "dependency direction (D2)", directionRemedy},
}

const (
	presentationRemedy = "internal/common/report describes WHAT a run found; it does not know how a run looks. " +
		"Move the formatting to internal/common/report/render, which is the package that may import this."

	directionRemedy = "a package under internal/common must not depend on internal/autoupdate — that inverts the " +
		"dependency direction. Moving it to internal/common/report/render does NOT fix this; render sits under " +
		"internal/common too. Take the primitive fact instead and convert at the adapter " +
		"(cmd/bentoo/overlay_autoupdate_report.go), the way GateFact takes a plain string cause rather than a DeclineCause."
)

// TestPackageImportsNoPresentation makes R1.2 mechanical instead of
// conventional. The model describes a run; it does not know how a run looks.
//
// GREEN ON ARRIVAL by design: this test cannot fail on the day the package is
// written, because the package has no reason to import lipgloss yet. It is a
// guard, not evidence. What proves it works is mutation — add a forbidden
// import, watch it fail, revert (see .draft/red-evidence.yaml).
//
// Why a test and not a comment: internal/common/tui states the identical rule
// in a comment, and the check path grew four hard-coded widths anyway.
//
// The sweep covers _test.go files too. A test in this package that needed
// lipgloss would mean the model had acquired presentation through the back
// door, and there is no legitimate reason for one to.
func TestPackageImportsNoPresentation(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}

	fset := token.NewFileSet()
	scanned := 0

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}

		path := filepath.Join(".", entry.Name())
		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}
		scanned++

		for _, spec := range file.Imports {
			imported := strings.Trim(spec.Path.Value, `"`)
			for _, forbidden := range forbiddenImports {
				if imported == forbidden.prefix || strings.HasPrefix(imported, forbidden.prefix+"/") {
					t.Errorf("%s imports %q, which crosses the %s rule.\n%s",
						path, imported, forbidden.rule, forbidden.remedy)
				}
			}
		}
	}

	// A sweep that scanned nothing passes vacuously and would keep passing
	// after someone renamed the files out from under it.
	if scanned == 0 {
		t.Fatal("scanned no .go files — the sweep passed without inspecting anything")
	}
}
