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
// with — because R2.2 asks the inline report to match the presentation --apply
// already uses, and a second dim would be a second answer to the same question.
//
// A basic ANSI number rather than a hex value on purpose: it resolves to
// whatever the operator configured in their terminal, so bentoo's grey is
// THEIRS. A hex value would override a themed terminal to impose ours.
const dimColor = "8"

// Inline renders r into the terminal's scrollback, styled (R2.2).
//
// # It is Plain plus escape sequences, and nothing else
//
// The same sections, in the same order, with the same wording, the same
// shortening and the same column widths — the identical code produces every
// visible character, because inline differs from plain by a paint and a paint
// only decorates a line that is already laid out. R2.4 says the modes may
// differ in presentation and not in content; here that is not a rule to keep
// but a thing that cannot be broken without deleting the seam.
//
// # It takes no io.Writer, and that absence is the design
//
// Plain, Markdown and JSON are handed one because their output is a stream that
// may go anywhere — a pipe, a file, a pull request comment. An inline renderer
// owns a REGION OF THE TERMINAL, and a terminal is not an arbitrary writer: the
// escape sequences below mean something to a device and nothing to a byte sink.
// So the destination is os.Stdout, named here rather than accepted, and a test
// that wants to see the output captures the descriptor.
//
// # It is not a Reporter and it consumes no progress
//
// internal/common/tui.Reporter is a progress SINK — BatchStart, TaskStage,
// TaskLine, TaskDone — a stream of events emitted while work is happening. This
// is a finished description printed once when the work is over. They coexist
// and neither replaces the other (D5): during a check the Reporter drives
// whatever live region the mode has, and when the run ends the report is
// rendered.
//
// Collapsing the two would mean the final report could only exist if the live
// UI had run, which is exactly what an export must not depend on. So nothing
// here implements that interface, accepts a tea.Msg, or starts a program.
func Inline(r report.Report, opts Options) error {
	width := opts.Width
	if width <= 0 {
		width = terminalWidth()
	}

	if err := write(os.Stdout, sections(r, opts.ShowAll, opts.SkipPlan), textStyle(width, inlinePaint())); err != nil {
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
// does not have — R2.4's failure, arriving as decoration. The paint seam is the
// place that constraint is stated; this is the place it is obeyed.
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
// # Why it is not the default renderer
//
// lipgloss's package-level renderer is global mutable state shared with every
// other package in the binary. Setting a profile on it to make this mode work
// would change how internal/common/tui renders too, from a file that has no
// business deciding that.
//
// # Why the profile is pinned
//
// lipgloss detects its profile from the writer it was built on and degrades to
// Ascii — emitting NOTHING, bold included — whenever that writer is not a
// terminal. Under `go test` stdout is a pipe, so a detected profile would make
// a test asserting that inline output carries escapes compare one unstyled
// string against another and pass without measuring anything. This repository
// has been bitten by that exact shape before; misc/design/design-system/theme
// carries the same pin for the same reason.
//
// Detecting here would also be a SECOND answer to a question already settled.
// ResolveMode picks inline only for an interactive terminal, and folds NO_COLOR
// and the other opt-outs into a downgrade to plain (R3.4, R3.7) — so by the
// time this runs, "this end wants styling" is decided. A probe here could only
// contradict it.
//
// ANSI (16 colours) rather than a richer profile because it is exactly what the
// paint above needs: bold, and one basic colour number. Claiming more would
// promise a terminal capability nothing here uses.
func inlineRenderer() *lipgloss.Renderer {
	r := lipgloss.NewRenderer(os.Stdout)
	r.SetColorProfile(termenv.ANSI)
	return r
}
