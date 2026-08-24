// Package render turns the view model in internal/common/report into text for
// a terminal: it decides how wide a column is, how a value that does not fit is
// cut, and how much room the line has to begin with.
//
// # It is the presentation side of the split, and that is why it may import lipgloss
//
// The model holds facts about a run and carries no width, no padding and no
// escape sequence; a test there enforces that it imports nothing that renders
// (D1). This package is the other half of that boundary, so measuring and
// styling belong here and nowhere upstream of here.
//
// # Nothing here declares a width
//
// Every width in this package is either measured from the values a run actually
// produced, or read from the device the report is being printed to. The one
// number that is written down is the width assumed when the device cannot be
// reached at all, and it is documented as such (R6.3, R6.5).
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
	// real one is (R6.5): 80 cells.
	//
	// This is an assumption about a device that could not be reached, not a
	// field width — the distinction is the whole of R6.3. A field width decides
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
// the widest of them, in display cells (R6.1).
//
// # The answer is computed, not typed
//
// This replaces a hand-written %-45s. A width typed into a format string is
// wrong in both directions at once — too narrow the moment one value outgrows
// it, and too wide on every run that never comes close — and neither error is
// visible when the format string is written, because the width that is correct
// depends on the values THIS run produced. That is knowable only after the run
// has produced them. A column that happens to be wide enough for today's
// package list is not a measured column; it is a lucky one, and it stays lucky
// only until the list changes.
//
// # Cells, because bytes and runes both answer a different question
//
// "日本語" is 9 bytes and 3 runes, and a terminal gives it 6 columns. An escape
// sequence is the mirror case: bytes and runes, and no column at all. Only cell
// width describes what the reader will actually see, which is the only width a
// column can be aligned to (R6.2, D6). lipgloss.Width is that measurement.
//
// # Zero for an empty run
//
// A run with nothing in it has no column to align, so the width is 0 rather
// than some minimum: padding a table with no rows would print whitespace
// nobody asked for.
func columnWidth(values []string) int {
	widest := 0
	for _, value := range values {
		widest = max(widest, lipgloss.Width(value))
	}
	return widest
}

// shorten returns s reduced to at most cells display columns, marking the cut
// so the reader can see one happened (R6.4).
//
// # The limit is hard
//
// A row one cell too wide wraps, and a wrapped row does not merely look untidy:
// it throws off every column to its right for the rest of the table. Preventing
// that is the only reason a width limit exists, so the mark counts against the
// budget like everything else the reader sees — cutting at cells and then
// appending an ellipsis would overflow by exactly the mark.
//
// # What already fits comes back untouched
//
// Byte for byte, with nothing appended. Shortening a value that fits would
// discard detail for no gain, and the loss is not recoverable downstream: the
// model still holds the full string, but once a renderer has cut it the cut is
// all the reader gets (R7.4).
//
// # A cut is always visible
//
// A silently trimmed atom reads as a real one, and an operator who copies
// "media-plugins/gst-plugins-adaptivede" into a command gets an error the
// report caused. Below one cell there is no room even for the mark, so nothing
// is printed rather than something misleading.
//
// # Why ansi.Truncate does the counting
//
// It is the same measuring code lipgloss.Width uses — lipgloss.Width is
// ansi.StringWidth per line — so shorten and columnWidth cannot disagree about
// the same string. Being grapheme- and escape-aware, it never lands a cut
// inside a wide character or inside an escape sequence: a severed escape is
// printed literally by the terminal, which is a corrupted row rather than a
// shortened one. It inserts no line break, which is the other half of R6.4.
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
// It always answers a width and never an error (R6.5). There is no second place
// to ask, so an error would give the caller nothing to do but give up, and a
// report that refuses to print because it could not size a column is a worse
// outcome than one printed at the wrong size.
//
// Three sources, in the order of how much each actually knows about where the
// output is going:
//
//  1. COLUMNS, when it holds a positive number. It is the POSIX override and
//     the only way to pin a width from outside the process — a CI job or an
//     operator capturing output to a file wants a width that has nothing to do
//     with whatever device happens to be attached.
//  2. The device behind stdout, asked directly. stdout is where the report
//     goes, so it is the one whose width matters; a terminal on stderr or stdin
//     says nothing about a stdout that was redirected.
//  3. fallbackTerminalWidth, when neither answered.
//
// Under `go test` stdout is a pipe, so the second source fails and the third is
// what runs — the fallback is exercised for real rather than simulated.
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
// This story replaces the four typed widths in the check path and leaves three
// standing — overlay prune, the autoupdate sweep, and the drift printer. They
// are outside its scope, not outside its argument: each is the same number
// typed by the same hand with the same defect. Exporting the measurement means
// whoever touches those files next has the replacement in reach instead of a
// reason to type a width again.
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
