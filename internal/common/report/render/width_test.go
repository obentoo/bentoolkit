package render

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// The atoms below are REAL, and their widths were measured with lipgloss.Width
// rather than assumed. That matters here more than usual, because the story's
// own motivating example does not survive measurement:
//
//	"gst-plugins-adaptivedemux2@stable"                is 33 cells, not 46
//	"media-plugins/gst-plugins-adaptivedemux2@stable"  is 47
//	"dev-libs/libayatana-appindicator-glib"            is 37 — the LONGEST atom
//	                                                   in the 269-package registry
//
// So no package this overlay tracks today overflows the %-45s the story is
// removing. The defect is still real, and it is the structural half of the
// argument rather than the empirical one: 45 was typed once, by hand, and the
// correct width depends on the packages in THIS run — which is knowable only
// after the run. A column that happens to be wide enough for today's registry
// is not a measured column; it is a lucky one.
const (
	longestRegistryAtom = "dev-libs/libayatana-appindicator-glib" // 37 cells
	atomOverflowing45   = "media-plugins/gst-plugins-adaptivedemux2@stable"
)

// TestColumnWidthMeasuresTheWidestValue pins R6.1: a column is as wide as the
// widest value it will actually print.
func TestColumnWidthMeasuresTheWidestValue(t *testing.T) {
	values := []string{"app-misc/jq", longestRegistryAtom, "sys-apps/portage"}

	if got, want := columnWidth(values), 37; got != want {
		t.Fatalf("columnWidth(%q) = %d, want %d", values, got, want)
	}
}

// TestColumnWidthFollowsAValueWiderThan45 is the case the hard-coded %-45s
// cannot serve. 45 is not a width, it is a guess that happens to hold.
func TestColumnWidthDoesNotCapAt45(t *testing.T) {
	got := columnWidth([]string{"app-misc/jq", atomOverflowing45})

	if want := lipgloss.Width(atomOverflowing45); got != want {
		t.Fatalf("columnWidth over a %d-cell atom = %d, want %d", want, got, want)
	}
	if got <= 45 {
		t.Fatalf("columnWidth = %d: the column collapsed to the typed width the story removes", got)
	}
}

// TestColumnWidthMeasuresCellsNotBytes pins R6.2. A CJK character occupies two
// terminal cells and three bytes in UTF-8; len() answers neither.
func TestColumnWidthMeasuresCellsNotBytes(t *testing.T) {
	const cjk = "日本語" // 3 runes, 6 cells, 9 bytes

	if got, want := columnWidth([]string{cjk}), 6; got != want {
		t.Fatalf("columnWidth(%q) = %d, want %d cells (len() would answer %d bytes)", cjk, got, want, len(cjk))
	}
}

// TestColumnWidthIgnoresANSI pins the other half of R6.2. An escape sequence
// occupies no cells, so a coloured value must not push the column wider than
// what the reader sees.
func TestColumnWidthIgnoresANSI(t *testing.T) {
	const coloured = "\x1b[31mabc\x1b[0m" // renders as 3 cells

	if got, want := columnWidth([]string{coloured}), 3; got != want {
		t.Fatalf("columnWidth(%q) = %d, want %d: escape sequences occupy no cells", coloured, got, want)
	}
}

// TestColumnWidthOfNothingIsZero — an empty run has no column to align.
func TestColumnWidthOfNothingIsZero(t *testing.T) {
	if got := columnWidth(nil); got != 0 {
		t.Fatalf("columnWidth(nil) = %d, want 0", got)
	}
}

// TestShortenNeverExceedsTheCellsAsked pins R6.4. Overflowing by even one cell
// wraps the line, and a wrapped row destroys every column to its right — which
// is the failure a width limit exists to prevent.
func TestShortenNeverExceedsTheCellsAsked(t *testing.T) {
	inputs := []string{
		"app-misc/jq",
		longestRegistryAtom,
		atomOverflowing45,
		"日本語日本語日本語日本語",
		"\x1b[31m" + longestRegistryAtom + "\x1b[0m",
		strings.Repeat("x", 230),
	}

	for _, in := range inputs {
		for _, cells := range []int{1, 2, 3, 8, 20, 45, 46, 200} {
			got := shorten(in, cells)
			if w := lipgloss.Width(got); w > cells {
				t.Errorf("shorten(%q, %d) is %d cells wide — %d too many", in, cells, w, w-cells)
			}
		}
	}
}

// TestShortenLeavesAShortValueAlone — shortening what already fits would be a
// loss of data for no gain.
func TestShortenLeavesAShortValueAlone(t *testing.T) {
	const s = "app-misc/jq"

	if got := shorten(s, 45); got != s {
		t.Fatalf("shorten(%q, 45) = %q, want it returned unchanged", s, got)
	}
}

// TestShortenMarksWhatItRemoved pins the visible half of R6.4: a value that was
// cut has to say so, or the reader takes a truncated atom for a real one.
func TestShortenMarksWhatItRemoved(t *testing.T) {
	got := shorten(atomOverflowing45, 20)

	if got == atomOverflowing45[:20] {
		t.Fatalf("shorten(%q, 20) = %q — cut with no visible mark", atomOverflowing45, got)
	}
	if lipgloss.Width(got) > 20 {
		t.Fatalf("shorten(%q, 20) is %d cells wide", atomOverflowing45, lipgloss.Width(got))
	}
}

// TestShortenNeverWraps pins the rest of R6.4 — a newline defeats the purpose
// as thoroughly as overflowing does.
func TestShortenNeverWraps(t *testing.T) {
	if got := shorten(atomOverflowing45, 20); strings.ContainsAny(got, "\n\r") {
		t.Fatalf("shorten produced a line break: %q", got)
	}
}

// TestTerminalWidthFallsBackTo80 pins R6.5. Under `go test` stdout is not a
// terminal, so this exercises the real fallback rather than a simulated one:
// the prototype work found `tput cols` failing with no TERM, and a renderer
// that cannot read a width must still produce a report.
func TestTerminalWidthFallsBackTo80(t *testing.T) {
	t.Setenv("TERM", "")
	t.Setenv("COLUMNS", "")

	if got, want := terminalWidth(), 80; got != want {
		t.Fatalf("terminalWidth() = %d with no readable terminal, want the documented %d", got, want)
	}
}

// TestExportedWrappersMatchTheInternalOnes — ColumnWidth and Shorten are
// exported so the three %-45s sites this story does NOT touch have the
// replacement at hand. A wrapper that drifts from what the renderer uses would
// hand them a different answer than the one being fixed here.
func TestExportedWrappersMatchTheInternalOnes(t *testing.T) {
	values := []string{"app-misc/jq", atomOverflowing45}

	if ColumnWidth(values) != columnWidth(values) {
		t.Errorf("ColumnWidth = %d, columnWidth = %d", ColumnWidth(values), columnWidth(values))
	}
	if Shorten(atomOverflowing45, 20) != shorten(atomOverflowing45, 20) {
		t.Errorf("Shorten = %q, shorten = %q", Shorten(atomOverflowing45, 20), shorten(atomOverflowing45, 20))
	}
}
