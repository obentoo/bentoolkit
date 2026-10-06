// Package render turns the view model in internal/common/report into text for
// a terminal: it decides how wide a column is, how a value that does not fit is
// cut, and how much room the line has to begin with.
//
// # It is the presentation side of the split, and that is why it may import lipgloss
//
// The model holds facts about a run and carries no width, no padding and no
// escape sequence; a test there enforces that it imports nothing that renders.
// This package is the other half of that boundary, so measuring and
// styling belong here and nowhere upstream of here.
//
// # Nothing here declares a width
//
// Every width in this package is either measured from the values a run actually
// produced, or read from the device the report is being printed to. The one
// number that is written down is the width assumed when the device cannot be
// reached at all, and it is documented as such.
package render

import (
	"os"
	"strconv"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
)

const (
	// fallbackTerminalWidth is the width assumed when nothing can say what the
	// real one is: 80 cells.
	//
	// This is an assumption about a device that could not be reached, not a
	// field width. A field width decides
	// how a value is laid out and can be measured instead; there is nothing to
	// measure when the far end is a log file, a pipe or a CI job with no
	// terminal at all, and a renderer that refused to print without an answer
	// would fail at exactly the moment its output matters most.
	//
	// 80 because it is the width of a VT100 line and the default every terminal
	// emulator since has inherited, so a report laid out for 80 stays readable
	// both on a real terminal and in the captured output of a job that had
	// none.
	fallbackTerminalWidth = 80

	// ellipsis marks a value that shorten had to cut.
	//
	// U+2026 occupies one cell where "..." occupies three. At the narrow end of
	// what a caller can ask for that is the difference between a mark that fits
	// beside some content and a mark that is the only thing left.
	ellipsis = "…"
)

// columnWidth is the width a column needs in order to hold every one of values:
// the widest of them, in display cells.
//
// It is computed, not typed: a width typed into a format string (the %-45s this
// replaced) is too narrow the moment one value outgrows it and too wide on every
// run that never comes close, and the right width depends on the values THIS
// run produced.
//
// It counts cells, because bytes and runes answer a different question: "日本語"
// is 9 bytes and 3 runes and takes 6 columns, and an escape sequence takes
// none. lipgloss.Width measures cells. A run with nothing in it answers 0 rather
// than a minimum, so a table with no rows prints no padding.
func columnWidth(values []string) int {
	widest := 0
	for _, value := range values {
		widest = max(widest, lipgloss.Width(value))
	}
	return widest
}

// shorten returns s reduced to at most cells display columns, marking the cut
// so the reader can see one happened.
//
// The limit is hard: a row one cell too wide wraps and throws off every column
// to its right, so the ellipsis counts against the budget. What already fits
// comes back byte for byte, because once a renderer cuts, the cut is all the
// reader gets. A cut is always visible — a silently trimmed atom reads as a
// real one and gets copied into a failing command — and below one cell nothing
// is printed rather than something misleading.
//
// ansi.Truncate does the counting because lipgloss.Width uses the same
// measurement, so shorten and columnWidth cannot disagree. It never cuts inside
// a wide character or an escape sequence (a severed escape is printed
// literally), and it inserts no line break.
func shorten(s string, cells int) string {
	// Stated here rather than delegated. ansi.Truncate happens to return "" for
	// a budget too small to hold the tail, but it does not document that, and a
	// contract this package's callers depend on should not be able to change
	// under a dependency bump.
	if cells <= 0 {
		return ""
	}

	return ansi.Truncate(s, cells, ellipsis)
}

// terminalWidth is how many display cells one line may occupy before the
// terminal wraps it.
//
// It always answers a width and never an error: there is no second place to
// ask, and a report printed at the wrong size beats one that refuses to print.
// Sources, in the order of how much each knows about where the output goes:
//
//  1. COLUMNS, when it holds a positive number: the POSIX override, and the
//     only way for a CI job or a captured run to pin a width from outside.
//  2. The device behind stdout, asked directly: stdout is where the report
//     goes, and a terminal on stderr says nothing about a redirected stdout.
//  3. fallbackTerminalWidth, when neither answered.
//
// Under `go test` stdout is a pipe, so the fallback is exercised for real.
func terminalWidth() int {
	if cols, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && cols > 0 {
		return cols
	}

	// x/term is the package bubbletea already uses to ask this, so the probe
	// pulls in no module that is not linked into the binary anyway. go.mod
	// still lists it as indirect, which this import makes stale: a later
	// `go mod tidy` will move that line, and that diff is the bookkeeping
	// catching up, not a dependency being added.
	if width, _, err := term.GetSize(os.Stdout.Fd()); err == nil && width > 0 {
		return width
	}

	return fallbackTerminalWidth
}

// ColumnWidth is columnWidth, exported.
//
// Three typed widths still stand outside the check path — overlay prune, the
// autoupdate sweep, and the drift printer — each with the same defect.
// Exporting the measurement means whoever touches those files next has the
// replacement in reach instead of a reason to type a width again.
//
// It must stay a thin call. A wrapper that drifted would hand those callers a
// different answer than this package's own renderer uses, leaving two columns
// that disagree about the same package list; a test pins the two together.
func ColumnWidth(values []string) int {
	return columnWidth(values)
}

// Shorten is shorten, exported for the reason ColumnWidth is. A caller
// replacing a typed width needs the cut that goes with it: measuring a column
// without being able to bound it just moves the overflow to the first run that
// contains a longer value.
func Shorten(s string, cells int) string {
	return shorten(s, cells)
}
