// Package theme is the vocabulary every bentoo component draws with: how much
// of the terminal a render may assume, which colour a role gets, and which
// glyph set is safe to emit.
//
// Nothing here holds global mutable state. A Theme is a value; a component is
// handed one and asks it questions. That is deliberate — see NewFor for the
// failure it prevents.
package theme

import (
	"io"
	"os"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// Mode is how much of the terminal a render is allowed to assume.
//
// # Why three and not two
//
// The obvious split is "plain or pretty". It is wrong, and internal/common/tui
// already contains the evidence: Enabled() turns the live UI off for THREE
// distinct reasons — stdout is not a terminal, NO_COLOR is set, or BENTOO_NO_TUI
// is set — and only the first of those says anything about what the receiving
// end can DISPLAY.
//
// NO_COLOR is a statement about colour. It is not a statement about UTF-8. A
// terminal with NO_COLOR set still renders ✓ perfectly, and collapsing it into
// the same bucket as `| grep` throws away information the operator can use for
// free. So the axis is not one boolean but two: may I use colour, and may I
// assume the receiver renders Unicode.
//
//   - Plain assumes NOTHING. ASCII only, no escape sequences. This is the face
//     for a pipe, a script, a CI log, a file, a non-UTF-8 locale.
//   - Unicode assumes a modern terminal but no colour. NO_COLOR on a TTY.
//   - Styled assumes both.
//
// Plain is the ZERO VALUE on purpose. A Theme nobody configured, or a component
// rendered by code that forgot to pick a mode, produces the output that is safe
// everywhere rather than the one that is prettiest here.
type Mode int

const (
	// Plain is ASCII with no escape sequences: pipes, scripts, CI logs, files.
	Plain Mode = iota
	// Unicode is Unicode glyphs with no escape sequences: a TTY under NO_COLOR.
	Unicode
	// Styled is Unicode glyphs plus colour: an ordinary interactive terminal.
	Styled
)

// String names the mode, so a report can say which face produced it.
func (m Mode) String() string {
	switch m {
	case Unicode:
		return "unicode"
	case Styled:
		return "styled"
	default:
		return "plain"
	}
}

// ModeFor is the ONE place the mode decision is made, and it mirrors
// internal/common/tui.Enabled's inputs so the two cannot disagree about the
// same terminal.
//
// isTTY comes from the stdout probe; noColor is true when NO_COLOR (or any
// project opt-out that means the same thing) carries a NON-EMPTY value — the
// NO_COLOR convention reads an empty value as "not set", which is the exact
// reading Enabled already applies.
func ModeFor(isTTY, noColor bool) Mode {
	switch {
	case !isTTY:
		return Plain
	case noColor:
		return Unicode
	default:
		return Styled
	}
}

// Role is what a piece of text MEANS, never what colour it is.
//
// Naming a role "green" is how internal/common/output and internal/common/tui
// drifted apart: output.Success is fatih green, tui.styleOK is ANSI "2", and
// nothing ever made them the same decision. A role has one definition, in
// palette below, and both faces read it.
type Role int

const (
	// Default is unstyled body text.
	Default Role = iota
	// OK marks something that succeeded.
	OK
	// Fail marks something that failed and needs a human.
	Fail
	// Warn marks something degraded that recovered on its own.
	Warn
	// Info marks progress and transitions — the → lines.
	Info
	// Skip marks something deliberately not done, which is not a failure.
	Skip
	// Accent marks the subject of a line: a package atom, a path, an id.
	Accent
	// Heading marks a section title.
	Heading
	// Muted marks context that must not compete with the line it sits under.
	Muted
)

// palette maps a role to a colour that works on BOTH backgrounds.
//
// The Light/Dark pairs are the normal (1-7) and bright (9-15) halves of the
// same ANSI hue. internal/common/tui/model.go hardcodes the normal half only
// ("1", "2", "4", "8"), which is tuned for one background and washes out on the
// other; AdaptiveColor lets lipgloss pick per terminal at render time.
//
// Basic ANSI numbers rather than hex on purpose: they inherit whatever palette
// the operator configured in their terminal, so bentoo's green is THEIR green.
// A hex value would override a carefully themed terminal to impose ours.
var palette = map[Role]lipgloss.TerminalColor{
	OK:      lipgloss.AdaptiveColor{Light: "2", Dark: "10"},
	Fail:    lipgloss.AdaptiveColor{Light: "1", Dark: "9"},
	Warn:    lipgloss.AdaptiveColor{Light: "3", Dark: "11"},
	Info:    lipgloss.AdaptiveColor{Light: "6", Dark: "14"},
	Skip:    lipgloss.AdaptiveColor{Light: "8", Dark: "8"},
	Accent:  lipgloss.AdaptiveColor{Light: "4", Dark: "12"},
	Heading: lipgloss.AdaptiveColor{Light: "0", Dark: "15"},
	Muted:   lipgloss.AdaptiveColor{Light: "8", Dark: "8"},
}

// Glyphs is the symbol set for one mode.
//
// A glyph is CONTENT, not decoration: "✓" and "[OK]" carry the same fact, and a
// reader who lost the colour must still be able to tell a pass from a failure.
// That is why the status tokens survive into Plain instead of being dropped
// with the escape sequences.
type Glyphs struct {
	OK, Fail, Warn, Info, Skip string // status tokens
	Arrow                      string // a → b
	Dash                       string // the label—detail separator
	Bullet                     string // list item
	VLine, HLine               string // box drawing
	TopLeft, BottomLeft        string
}

var (
	asciiGlyphs = Glyphs{
		OK: "[OK]", Fail: "[FAIL]", Warn: "[WARN]", Info: "[INFO]", Skip: "[SKIP]",
		Arrow: "->", Dash: "--", Bullet: "*",
		VLine: "|", HLine: "-", TopLeft: "+", BottomLeft: "+",
	}
	unicodeGlyphs = Glyphs{
		OK: "✓", Fail: "✗", Warn: "⚠", Info: "→", Skip: "·",
		Arrow: "→", Dash: "—", Bullet: "•",
		VLine: "│", HLine: "─", TopLeft: "┌", BottomLeft: "└",
	}
)

// Status returns the token for a role, or "" for a role that has none.
func (g Glyphs) Status(r Role) string {
	switch r {
	case OK:
		return g.OK
	case Fail:
		return g.Fail
	case Warn:
		return g.Warn
	case Info:
		return g.Info
	case Skip:
		return g.Skip
	default:
		return ""
	}
}

// StatusWidth is the display width of the widest status token in the set, so a
// column of mixed outcomes lines up without every caller re-measuring.
func (g Glyphs) StatusWidth() int {
	w := 0
	for _, r := range []Role{OK, Fail, Warn, Info, Skip} {
		if n := lipgloss.Width(g.Status(r)); n > w {
			w = n
		}
	}
	return w
}

// Theme is the value a component renders against. Its zero value is a valid
// Plain theme, so Theme{} is always safe to use and never nil-checks.
type Theme struct {
	mode Mode
	r    *lipgloss.Renderer
}

// New returns a Theme for mode, styling against the real stdout.
//
// The renderer is only built for Styled: the other two modes emit no escape
// sequences at all, so probing the terminal for them would be work whose result
// is discarded.
func New(m Mode) Theme {
	t := Theme{mode: m}
	if m == Styled {
		t.r = lipgloss.NewRenderer(os.Stdout)
	}
	return t
}

// NewFor returns a Theme whose colour profile and background are PINNED rather
// than detected, writing to w.
//
// # This exists to stop a test passing vacuously
//
// lipgloss detects its colour profile from the writer it was built on. A test
// captures output into a buffer, a buffer is not a terminal, and lipgloss
// therefore degrades to the Ascii profile and emits NOTHING — so a golden test
// asserting that Styled output carries colour compares one empty string against
// another and passes without measuring anything. This repository has been
// bitten by that exact shape more than once.
//
// Pinning the profile makes the escape sequences real in the golden file, which
// is the only way the assertion means something. Pinning the background does
// the same for AdaptiveColor, whose Light/Dark choice would otherwise depend on
// the machine running the test.
func NewFor(m Mode, w io.Writer, profile termenv.Profile, dark bool) Theme {
	t := Theme{mode: m}
	if m == Styled {
		r := lipgloss.NewRenderer(w)
		r.SetColorProfile(profile)
		r.SetHasDarkBackground(dark)
		t.r = r
	}
	return t
}

// Mode reports which face this theme renders.
func (t Theme) Mode() Mode { return t.mode }

// Glyphs returns the symbol set for this theme's mode. Plain gets ASCII;
// everything else gets Unicode.
func (t Theme) Glyphs() Glyphs {
	if t.mode == Plain {
		return asciiGlyphs
	}
	return unicodeGlyphs
}

// Paint applies a role's colour to s, and is a no-op in every mode but Styled.
//
// The short-circuit is not an optimisation, it is the contract: Plain and
// Unicode promise output with no escape sequences in it, and the cheapest way
// to keep that promise is to never reach the styling layer at all.
func (t Theme) Paint(r Role, s string) string {
	if t.mode != Styled || t.r == nil {
		return s
	}
	style := t.r.NewStyle()
	if c, ok := palette[r]; ok {
		style = style.Foreground(c)
	}
	if r == Heading {
		style = style.Bold(true)
	}
	if r == Muted || r == Skip {
		style = style.Faint(true)
	}
	return style.Render(s)
}

// Width is the DISPLAY width of s in terminal cells.
//
// Not len(s), and not utf8.RuneCountInString(s): the first counts bytes, so "✓"
// scores 3; the second counts runes, so a CJK package description scores half
// its real width and every column below it misaligns. lipgloss.Width also
// discounts escape sequences, which is what makes it correct on already-painted
// text.
func Width(s string) int { return lipgloss.Width(s) }
