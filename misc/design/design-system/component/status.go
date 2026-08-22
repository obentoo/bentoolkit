package component

import (
	"strings"

	"github.com/obentoo/bentoolkit/misc/design/design-system/theme"
)

// Status is one outcome line: a token, a subject, and optionally why.
//
// Replaces output.PrintSuccess / PrintError / PrintWarning / PrintInfo, which
// today hardcode "✓ ", "✗ ", "⚠ " and "→ " into four near-identical Printf
// wrappers — and which print to stdout or stderr as a side effect, so nothing
// can assert on what they produced without capturing a stream.
type Status struct {
	Role   theme.Role `json:"role"`
	Label  string     `json:"label"`
	Detail string     `json:"detail,omitempty"`
	// Align pads the token to the width of the widest one in the set, so a
	// column of mixed outcomes lines up. Off by default: a lone line does not
	// want the padding, and a caller printing a list does.
	Align bool `json:"align,omitempty"`
}

// Render draws "✓ label — detail", or "[OK] label -- detail" in Plain.
func (s Status) Render(t theme.Theme) string {
	g := t.Glyphs()
	tok := g.Status(s.Role)
	if s.Align {
		tok = pad(tok, g.StatusWidth())
	}

	var b strings.Builder
	if tok != "" {
		b.WriteString(t.Paint(s.Role, tok))
		b.WriteString(" ")
	}
	b.WriteString(t.Paint(theme.Accent, s.Label))
	if s.Detail != "" {
		b.WriteString(" " + g.Dash + " ")
		b.WriteString(t.Paint(theme.Muted, s.Detail))
	}
	return b.String()
}

// Transition is a before-and-after on one line: "1.29.1 → 1.29.2".
//
// It is the single most repeated shape in this codebase's output after the bare
// indented line — every version bump, every rename, every realignment prints
// one — and it is currently spelled with a literal "→" at each site, which is
// why a Plain consumer receives a byte sequence it may not be able to draw.
type Transition struct {
	From string     `json:"from"`
	To   string     `json:"to"`
	Role theme.Role `json:"role,omitempty"`
	// Label prefixes the pair, as in "version: 1.29.1 → 1.29.2".
	Label string `json:"label,omitempty"`
}

// Render draws "label: from → to".
func (x Transition) Render(t theme.Theme) string {
	var b strings.Builder
	if x.Label != "" {
		b.WriteString(t.Paint(theme.Muted, x.Label+":"))
		b.WriteString(" ")
	}
	b.WriteString(t.Paint(theme.Default, x.From))
	b.WriteString(" " + t.Paint(theme.Info, t.Glyphs().Arrow) + " ")
	role := x.Role
	if role == theme.Default {
		role = theme.Accent
	}
	b.WriteString(t.Paint(role, x.To))
	return b.String()
}

// Empty is the "there is nothing here" line — snapshot_list.go prints "  (none)"
// twice, and several other commands print nothing at all, which reads as a bug
// rather than as an answer.
//
// An empty result is a RESULT. Saying so is the difference between "the command
// found none" and "the command did not run".
type Empty struct {
	// Note replaces the default "(none)" when a command can be more specific,
	// e.g. "(none — every package is current)".
	Note string `json:"note,omitempty"`
}

// Render draws the note, muted.
func (e Empty) Render(t theme.Theme) string {
	note := e.Note
	if note == "" {
		note = "(none)"
	}
	return t.Paint(theme.Muted, note)
}
