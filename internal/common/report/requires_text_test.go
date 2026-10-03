package report

// Authored for story 079, sub-task 5.1 — R3.1, R3.2, R3.3 (text) and R3.5.
//
// Written from design.md "Requirement state (check report)": report.PackageResult
// gains Requirements []Requirement{Package, Version, State}, and the text report
// prints one line per requirement under the package's row:
//
//	waits for dev-lang/dart-3.14.0 (not detected)   // missing
//	requires dev-lang/dart-3.14.0 (pending)
//	requires dev-lang/dart-3.14.0 (present)
//
// The renderer is driven through AutoupdateCheck.Sections, the half every text
// syntax reuses (Plain and Markdown differ only in table syntax), so what is
// asserted here is what every writer prints. A requirement line is accepted as
// a row cell or as one line of a row's Detail, but only inside the span of the
// row it belongs to: from that package's row up to the next package's row.
//
// Hostile half (attribution): three packages carry the SAME atom and version
// and differ only in state, so a renderer that prints the wrong state, or
// attaches a line to its neighbour, cannot pass by accident.

import (
	"reflect"
	"strings"
	"testing"
)

// requiresTextSpans maps each package row of the version-check section to the
// text units inside its span: every cell and every Detail line of its own row
// and of any following row that names no package of its own.
func requiresTextSpans(t *testing.T, blocks []Section) map[string][]string {
	t.Helper()
	var version *Section
	for i := range blocks {
		if blocks[i].Title == "Version Check Results" {
			version = &blocks[i]
		}
	}
	if version == nil {
		t.Fatalf("no \"Version Check Results\" section in %+v", blocks)
	}
	spans := map[string][]string{}
	current := ""
	for _, row := range version.Rows.Rows {
		if len(row.Cells) > 0 && strings.Contains(row.Cells[0], "/") {
			current = strings.TrimSpace(row.Cells[0])
		}
		if current == "" {
			t.Fatalf("a row precedes every package row: %+v", row)
		}
		for _, cell := range row.Cells {
			spans[current] = append(spans[current], strings.TrimSpace(cell))
		}
		for _, line := range strings.Split(row.Detail, "\n") {
			spans[current] = append(spans[current], strings.TrimSpace(line))
		}
	}
	return spans
}

// requiresTextLines is every unit of a span that reads as a requirement line.
func requiresTextLines(units []string) []string {
	var got []string
	for _, u := range units {
		if strings.HasPrefix(u, "requires ") || strings.HasPrefix(u, "waits for ") {
			got = append(got, u)
		}
	}
	return got
}

// requiresTextAll is every string a section list says, for "nowhere" checks.
func requiresTextAll(blocks []Section) string {
	var b strings.Builder
	for _, s := range blocks {
		b.WriteString(s.Title + "\n")
		for _, l := range s.Lead {
			b.WriteString(l + "\n")
		}
		b.WriteString(strings.Join(s.Rows.Headers, "\t") + "\n")
		for _, r := range s.Rows.Rows {
			b.WriteString(strings.Join(r.Cells, "\t") + "\n" + r.Detail + "\n")
		}
		for _, n := range s.Notes {
			b.WriteString(n + "\n")
		}
	}
	return b.String()
}

// TestRequiresReportTextRows — one line per requirement, worded by its state,
// under its own package's row.
func TestRequiresReportTextRows(t *testing.T) {
	r := AutoupdateCheck{Scanned: []PackageResult{
		{Package: "dev-lang/flutter", Type: "bin", CurrentVersion: "3.47.0", CandidateVersion: "3.48.0", HasUpdate: true,
			Requirements: []Requirement{
				{Package: "dev-lang/dart", Version: "3.14.0", State: "missing"},
				{Package: "dev-libs/engine", Version: "1.2", State: "present"},
			}},
		{Package: "app-misc/jq", Type: "source", CurrentVersion: "1.7.1", CandidateVersion: "1.8.0", HasUpdate: true},
		{Package: "dev-util/flutter-beta", Type: "bin", CurrentVersion: "3.49.0", CandidateVersion: "3.50.0", HasUpdate: true,
			Requirements: []Requirement{{Package: "dev-lang/dart", Version: "3.14.0", State: "pending"}}},
		{Package: "app-editors/dart-pad", Type: "bin", CurrentVersion: "1.0.0", CandidateVersion: "1.1.0", HasUpdate: true,
			Requirements: []Requirement{{Package: "dev-lang/dart", Version: "3.14.0", State: "present"}}},
	}}

	for _, showAll := range []bool{false, true} {
		spans := requiresTextSpans(t, r.Sections(SectionOptions{ShowAll: showAll}))
		want := map[string][]string{
			"dev-lang/flutter": {
				"waits for dev-lang/dart-3.14.0 (not detected)",
				"requires dev-libs/engine-1.2 (present)",
			},
			"app-misc/jq":           nil,
			"dev-util/flutter-beta": {"requires dev-lang/dart-3.14.0 (pending)"},
			"app-editors/dart-pad":  {"requires dev-lang/dart-3.14.0 (present)"},
		}
		for pkg, lines := range want {
			units, listed := spans[pkg]
			if !listed {
				t.Errorf("ShowAll=%v: package %s has no row", showAll, pkg)
				continue
			}
			if got := requiresTextLines(units); !reflect.DeepEqual(got, lines) {
				t.Errorf("ShowAll=%v: requirement lines under %s = %q, want %q (in order, one per requirement)", showAll, pkg, got, lines)
			}
		}
	}
}

// TestRequiresReportTextNoRequiresUnchanged — R3.5: a package without
// requirements reports exactly what it reported before the story. The expected
// sections below were captured from today's renderer for this very input, and
// an EMPTY Requirements slice must not change a byte of them.
func TestRequiresReportTextNoRequiresUnchanged(t *testing.T) {
	want := []Section{{
		Title: "Version Check Results",
		Lead:  []string{"2 package(s) checked, 2 with a pending update."},
		Rows: Table{
			Headers: []string{"PACKAGE", "TYPE", "CURRENT", "CANDIDATE", "STATE"},
			Rows: []Row{
				{Cells: []string{"dev-lang/flutter", "bin", "3.47.0", "3.48.0", "update"}},
				{Cells: []string{"app-misc/jq", "source", "1.7.1", "1.8.0", "update"}},
			},
		},
		Notes: []string{"Checked 1 source, 1 bin."},
	}}

	for name, reqs := range map[string][]Requirement{"nil": nil, "empty": {}} {
		r := AutoupdateCheck{Scanned: []PackageResult{
			{Package: "dev-lang/flutter", Type: "bin", CurrentVersion: "3.47.0", CandidateVersion: "3.48.0", HasUpdate: true, Requirements: reqs},
			{Package: "app-misc/jq", Type: "source", CurrentVersion: "1.7.1", CandidateVersion: "1.8.0", HasUpdate: true},
		}}
		got := r.Sections(SectionOptions{ShowAll: true})
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s Requirements: sections changed\n got: %#v\nwant: %#v", name, got, want)
		}
		text := requiresTextAll(got)
		if strings.Contains(text, "requires") || strings.Contains(text, "waits for") {
			t.Errorf("%s Requirements: a package without requirements prints requirement text:\n%s", name, text)
		}
	}
}
