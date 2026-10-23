package render

// Authored for story 046, sub-task 2.1 — R2.1, R2.2, R2.3, R6.1, R6.3.
//
// Written from the contract: design.md's Components block fixes the signature —
// "Plain(w, []Section, Options)", "Markdown", "Widths measured via
// lipgloss.Width" — and D2 fixes what a Section carries.
//
// Red on arrival: Plain and Markdown take report.Report today, so they cannot
// be called with sections at all.
//
// # It is authored beside text_test.go, not over it
//
// text_test.go holds story 044's golden suite for the check path, which
// Unchanged Behavior 1 forbids this story from moving. This file adds the
// section-level assertions; nothing here regenerates a golden.

import (
	"bytes"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/obentoo/bentoolkit/internal/common/report"
)

// wideAtom is a package name whose display width and byte length disagree by a
// factor of three: 21 bytes, 7 runes, 14 display cells.
//
// It is the fixture R6.1 is only an assertion WITH. A column sized by len() is
// correct for every ASCII atom in this repository's overlay, so a test built
// from ASCII alone passes against a renderer that never measured anything.
const wideAtom = "app-i18n/日本語入力"

// sectionFixture is one report expressed as sections: a lead, a two-column
// table whose first column holds both a wide-rune value and a plain one, a row
// carrying its own detail, and a note stating an omission.
func sectionFixture() []report.Section {
	return []report.Section{
		{
			Title: "Validation Results",
			Lead:  []string{"4 packages evaluated"},
			Rows: report.Table{
				Headers: []string{"package", "outcome"},
				Rows: []report.Row{
					{Cells: []string{wideAtom, "PROVED"}},
					{Cells: []string{"app-misc/jq", "ERRORED"}, Detail: "configure failed: missing dependency dev-libs/ayatana-ido"},
				},
			},
			Notes: []string{"2 packages found up to date are not listed; re-run with --all"},
		},
	}
}

func renderSectionsPlain(t *testing.T, blocks []report.Section, opts Options) string {
	t.Helper()

	var buf bytes.Buffer
	if err := Plain(&buf, blocks, opts); err != nil {
		t.Fatalf("Plain returned an error: %v", err)
	}
	return buf.String()
}

// cellOffsetOf answers where value begins on its line, measured in DISPLAY
// CELLS rather than bytes — the same measurement a terminal makes when it lays
// the line out.
func cellOffsetOf(t *testing.T, out, value string) int {
	t.Helper()

	for _, line := range strings.Split(ansi.Strip(out), "\n") {
		if idx := strings.Index(line, value); idx >= 0 {
			return lipgloss.Width(line[:idx])
		}
	}
	t.Fatalf("%q never appears in the output:\n%s", value, out)
	return -1
}

// TestPlainSizesAColumnToItsWidestValue pins R6.1. The second column has to
// start at the same cell on every row, and the row that decides where that is
// is the widest one — measured in cells.
//
// A renderer that padded by len() would give the wide-rune row 21 bytes of
// budget for 14 cells of content and start its second column 7 cells early,
// which is a table whose columns no longer line up for the rest of its height.
func TestPlainSizesAColumnToItsWidestValue(t *testing.T) {
	out := renderSectionsPlain(t, sectionFixture(), Options{Width: 100})

	proved := cellOffsetOf(t, out, "PROVED")
	errored := cellOffsetOf(t, out, "ERRORED")

	if proved != errored {
		t.Errorf("the second column starts at cell %d beside %q and at cell %d beside %q — a column measured in bytes stops lining up the moment a value is not ASCII (R6.1)",
			proved, wideAtom, errored, "app-misc/jq")
	}

	// And it is sized to the WIDEST value, not to some fixed number: the
	// column must be at least as wide as the wide-rune atom it holds.
	if proved < lipgloss.Width(wideAtom) {
		t.Errorf("the second column starts at cell %d, before the first column's widest value ends (%d cells) — the values overlap",
			proved, lipgloss.Width(wideAtom))
	}
}

// TestPlainRespectsTheLineBudget pins R6.3 across the narrow end, where a
// renderer that appends its cut mark AFTER truncating overflows by exactly the
// mark and wraps — and a wrapped row throws off every column to its right for
// the rest of the table.
func TestPlainRespectsTheLineBudget(t *testing.T) {
	for _, budget := range []int{40, 60, 100} {
		out := renderSectionsPlain(t, sectionFixture(), Options{Width: budget})

		for i, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
			if w := lipgloss.Width(line); w > budget {
				t.Errorf("at a budget of %d, line %d is %d cells wide (R6.3):\n%q", budget, i+1, w, line)
			}
		}
	}
}

// TestPlainStatesWhatItOmitted pins R2.3. An omission the reader cannot see is
// worse than a report that does not fit: the section's notes are what turn a
// silent drop into a stated one, so they have to reach the output.
func TestPlainStatesWhatItOmitted(t *testing.T) {
	blocks := sectionFixture()
	note := blocks[0].Notes[0]

	out := ansi.Strip(renderSectionsPlain(t, blocks, Options{Width: 100}))
	if !strings.Contains(out, "not listed") {
		t.Errorf("the section's note never reached the output — an omission was dropped silently (R2.3)\nnote: %q\n--- output ---\n%s", note, out)
	}
}

// TestPlainHasNoEscapeByte pins R2.2 for the mode a pipe, a cron mail and a CI
// transcript receive. It is asserted on the BYTES, not on a stripped string:
// stripping first would make the test pass over exactly what it is looking for.
func TestPlainHasNoEscapeByte(t *testing.T) {
	var buf bytes.Buffer
	if err := Plain(&buf, sectionFixture(), Options{Width: 100}); err != nil {
		t.Fatalf("Plain returned an error: %v", err)
	}

	if idx := bytes.IndexByte(buf.Bytes(), 0x1b); idx >= 0 {
		t.Errorf("plain output carries an escape byte at offset %d (R2.2):\n%q", idx, buf.String())
	}
}

// TestMarkdownTakesTheSameSections pins the other half of 2.1: the export path
// consumes the same vocabulary, so the two modes cannot drift into two
// different section models.
//
// The reason is asserted IN FULL. An export is kept precisely because the
// terminal is gone, and one that mirrored the screen's shortening would answer
// no question later (R3.4).
func TestMarkdownTakesTheSameSections(t *testing.T) {
	blocks := sectionFixture()
	detail := blocks[0].Rows.Rows[1].Detail

	var buf bytes.Buffer
	if err := Markdown(&buf, blocks); err != nil {
		t.Fatalf("Markdown returned an error: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, wideAtom) {
		t.Errorf("the markdown export does not name %q", wideAtom)
	}
	if !strings.Contains(out, detail) {
		t.Errorf("the markdown export shortened a row's detail — an export carries every reason in full (R3.4)\nwant: %q\n--- output ---\n%s", detail, out)
	}
	if !strings.Contains(out, "|") {
		t.Errorf("the markdown export contains no table syntax:\n%s", out)
	}
}

// TestPlainWrapsARowDetail is the live compare run's qemu row: a finding whose
// reason sat at the end of the sentence was cut at "which ::…", so the line
// said a package was ours without saying why. A detail is wrapped under its
// row, every line at the detail indent, and none of it is dropped.
func TestPlainWrapsARowDetail(t *testing.T) {
	const detail = "undeclared divergence (+836/-820), proved ours — our 11.1.2 ebuild references " +
		"files/qemu-11.0.0-optionrom-pass-Wl-no-error-rwx-segments.patch, which ::gentoo does not ship"

	blocks := []report.Section{{
		Title: "Redundant",
		Rows: report.Table{
			Headers: []string{"package", "state"},
			Rows: []report.Row{
				{Cells: []string{"app-emulation/qemu", "up-to-date"}, Detail: detail},
				{Cells: []string{"app-misc/jq", "up-to-date"}, Detail: "short reason"},
			},
		},
	}}

	out := ansi.Strip(renderSectionsPlain(t, blocks, Options{Width: 60}))

	if strings.Contains(out, "…") {
		t.Errorf("the detail was cut instead of wrapped:\n%s", out)
	}
	var detailLines []string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, strings.Repeat(" ", detailIndent)) && strings.TrimSpace(line) != "" {
			detailLines = append(detailLines, strings.TrimSpace(line))
		}
	}
	if joined := strings.Join(detailLines, " "); !strings.Contains(joined, "which ::gentoo does not ship") {
		t.Errorf("the end of the detail, which carries the reason, never reached the output:\n%s", out)
	}
	if !strings.Contains(out, "\n"+strings.Repeat(" ", detailIndent)+"short reason\n") {
		t.Errorf("a detail that fits is no longer printed on one line at the detail indent:\n%s", out)
	}
	for i, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if w := lipgloss.Width(line); w > 60 {
			t.Errorf("line %d is %d cells wide at a budget of 60:\n%q", i+1, w, line)
		}
	}
}

// TestPlainDetailAtANarrowBudgetPrintsNoBlankLine keeps the guard the cut had:
// a budget too narrow for any detail text prints nothing, not an indent alone.
func TestPlainDetailAtANarrowBudgetPrintsNoBlankLine(t *testing.T) {
	blocks := []report.Section{{
		Title: "T",
		Rows: report.Table{
			Headers: []string{"p"},
			Rows:    []report.Row{{Cells: []string{"x"}, Detail: "some reason"}},
		},
	}}

	out := renderSectionsPlain(t, blocks, Options{Width: detailIndent})
	for i, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if line != "" && strings.TrimSpace(line) == "" {
			t.Errorf("line %d is whitespace only:\n%q", i+1, out)
		}
	}
}
