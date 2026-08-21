package component

import (
	"strconv"
	"strings"

	"github.com/obentoo/bentoolkit/misc/design/design-system/theme"
)

// Progress is one work-in-flight line: "Checking [ 45%] 9/20 ▓▓▓▓░░░░".
//
// overlay_compare.go writes this by hand as "\r  Checking: [%3d%%] %d/%d" and
// erases it with a sixty-six-space blank line — a width that has to be at least
// as long as the longest line it might ever have drawn, maintained by hand,
// nowhere near the Printf it has to match.
//
// # This type renders text and nothing else
//
// The carriage return, the erase, and the redraw cadence belong to whatever is
// driving the terminal — tui.Reporter's plain backend already throttles
// in-place updates per task id, and its bubbletea backend redraws whole frames.
// A component that emitted "\r" itself would be unusable inside bubbletea and
// untestable everywhere.
type Progress struct {
	Label string `json:"label"`
	Done  int    `json:"done"`
	// Total of 0 means indeterminate: the percentage and the bar are dropped and
	// only the count is shown. That is the honest render for work whose size is
	// not yet known, which is most of a sweep before it has finished scanning.
	Total int `json:"total"`
	// BarWidth of 0 draws no bar.
	BarWidth int `json:"bar_width,omitempty"`
}

// Percent is Done/Total as a whole number, or -1 when indeterminate.
func (p Progress) Percent() int {
	if p.Total <= 0 {
		return -1
	}
	return p.Done * 100 / p.Total
}

// Render draws the line.
func (p Progress) Render(t theme.Theme) string {
	var b strings.Builder
	if p.Label != "" {
		b.WriteString(t.Paint(theme.Heading, p.Label))
		b.WriteString(" ")
	}

	if pct := p.Percent(); pct >= 0 {
		// %3d, so the bracket does not jog left as the number grows past 9 and
		// 99 — the one detail the hand-written version got right and the reason
		// it is preserved here.
		b.WriteString(t.Paint(theme.Info, "["+pad(strconv.Itoa(pct), 3)+"%]"))
		b.WriteString(" ")
		b.WriteString(t.Paint(theme.Default, strconv.Itoa(p.Done)+"/"+strconv.Itoa(p.Total)))
		if p.BarWidth > 0 {
			b.WriteString(" " + p.bar(t, pct))
		}
		return b.String()
	}

	b.WriteString(t.Paint(theme.Default, strconv.Itoa(p.Done)))
	b.WriteString(t.Paint(theme.Muted, " so far"))
	return b.String()
}

// bar draws the filled/empty run. Plain uses '#' and '.', which survive a pipe
// and a non-UTF-8 locale; the richer modes use block glyphs.
func (p Progress) bar(t theme.Theme, pct int) string {
	full, empty := "#", "."
	if t.Mode() != theme.Plain {
		full, empty = "█", "░"
	}
	on := p.BarWidth * pct / 100
	if on > p.BarWidth {
		on = p.BarWidth
	}
	if on < 0 {
		on = 0
	}
	return t.Paint(theme.OK, strings.Repeat(full, on)) +
		t.Paint(theme.Muted, strings.Repeat(empty, p.BarWidth-on))
}
