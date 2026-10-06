package render

import (
	"fmt"
	"os"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/obentoo/bentoolkit/internal/common/report"
)

// dimColor is the ANSI number the secondary lines of an inline report are drawn
// in: the rule under a title, and a row's reason.
//
// It is "8" — the same number internal/common/tui/model.go dims a task's tail
// with — because the inline report must match the presentation --apply already
// uses, and a second dim would be a second answer to the same question.
//
// A basic ANSI number rather than a hex value on purpose: it resolves to
// whatever the operator configured in their terminal, so bentoo's grey is
// THEIRS. A hex value would override a themed terminal to impose ours.
const dimColor = "8"

// Inline renders blocks into the terminal's scrollback, styled.
//
// It is Plain plus escape sequences and nothing else: the same sections,
// wording, shortening and column widths come from the identical code, and the
// paint only decorates a line that is already laid out. The modes may differ in
// presentation, never in content, and here that cannot be broken without
// deleting the seam. It takes sections, not a report: the caller decides WHAT
// the report says, this decides only how it LOOKS, so a new command is a new
// file rather than an edit here.
//
// It takes no io.Writer on purpose. Plain, Markdown and JSON write a stream that
// may go anywhere; an inline renderer owns a region of the terminal, whose
// escape sequences mean nothing to a byte sink. So the destination is
// os.Stdout, and a test captures the descriptor.
//
// It is not an internal/common/tui.Reporter: that is a progress sink fed while
// work happens, this is a finished description printed once the work is over.
// Merging them would make the final report depend on the live UI having run,
// which an export must not; nothing here accepts a tea.Msg or starts a program.
func Inline(blocks []report.Section, opts Options) error {
	if err := write(os.Stdout, blocks, textStyle(opts.cells(), inlinePaint())); err != nil {
		return fmt.Errorf("rendering the inline report: %w", err)
	}
	return nil
}

// inlinePaint is which lines of a report are emphasised and which recede: a
// bold title and a bold column header, a dim rule and a dim reason.
//
// # Every style here is weight or colour, never geometry
//
// Bold, faint and a foreground colour emit an escape sequence and not one extra
// cell. lipgloss's Width, Padding, Margin and Border emit cells, and a paint
// that used one would put characters in the inline render that the plain render
// does not have — a content difference arriving as decoration. The paint seam
// is the place that constraint is stated; this is the place it is obeyed.
//
// # Tab conversion is switched off
//
// lipgloss rewrites a tab as four spaces by default. A reason string comes from
// a subprocess, which writes what it likes, so that default would let inline
// print a line plain printed differently — a content difference produced by the
// styling layer, which is precisely what must not happen.
func inlinePaint() paint {
	// A lipgloss.Style is an all-value struct, so each derivation below copies
	// the base rather than sharing it: strong is not also dim.
	base := inlineRenderer().NewStyle().TabWidth(lipgloss.NoTabConversion)

	strong := base.Bold(true)
	dim := base.Foreground(lipgloss.Color(dimColor))

	return paint{
		heading: painter(strong),
		rule:    painter(dim),
		header:  painter(strong),
		detail:  painter(dim),
	}
}

// painter adapts a lipgloss style to the paint seam.
//
// It exists because Style.Render is variadic — Render(...string) — so it cannot
// be assigned to a func(string) string field directly. Adapting once beats four
// identical closures, each of which would be a place to write the wrong style
// name.
func painter(s lipgloss.Style) func(string) string {
	return func(line string) string { return s.Render(line) }
}

// inlineRenderer is a lipgloss renderer of this package's own, with the colour
// profile PINNED rather than detected.
//
// It is not the default renderer because lipgloss's package-level one is global
// mutable state: setting a profile on it would change how internal/common/tui
// renders too.
//
// The profile is pinned because lipgloss degrades to Ascii — emitting nothing,
// bold included — when its writer is not a terminal. Under `go test` stdout is
// a pipe, so a detected profile would let a test asserting escapes compare two
// unstyled strings and pass without measuring anything. Detecting would also
// second-guess a settled decision: ResolveMode picks inline only for an
// interactive terminal, with the caller folding --no-tui, BENTOO_NO_TUI and
// NO_COLOR into ModeInputs.NoTUI, so a probe here could only contradict it.
//
// ANSI (16 colours) because it is exactly what the paint needs: bold and one
// basic colour number.
func inlineRenderer() *lipgloss.Renderer {
	r := lipgloss.NewRenderer(os.Stdout)
	r.SetColorProfile(termenv.ANSI)
	return r
}
