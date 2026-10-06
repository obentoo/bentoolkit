package render

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/obentoo/bentoolkit/internal/common/report"
)

const (
	// fullscreenChrome is how many of the terminal's rows the frame costs
	// before one line of report is drawn: the box's top rule, its bottom rule,
	// and the key hint under it.
	//
	// It is subtracted from the viewport height, so getting it wrong does not
	// merely look untidy — it makes the last line of the frame fall off the
	// bottom of the screen, and in an alternate screen there is nowhere for it
	// to fall TO.
	fullscreenChrome = 3

	// fullscreenFrame is what the box costs horizontally: one cell of rule and
	// one of padding on each side.
	//
	// The body is laid out at the width that REMAINS, and that subtraction is
	// load-bearing rather than cosmetic. lipgloss re-wraps a line wider than
	// the box it is drawn in, and a wrap turns one row into two — rows the
	// height budget did not count, in a screen with no scrollback to lose them
	// into. Change the padding below and this number changes with it.
	fullscreenFrame = 4

	// fullscreenPad is the breathing room between the rule and the text. A
	// section heading starts at column zero — the shared writer's own
	// convention, which this mode does not get to overrule — so without
	// it every title would sit flush against the border.
	fullscreenPad = 1
)

// runProgram is the package seam for the terminal takeover: it builds the
// bubbletea program and runs it.
//
// # Why the takeover is a variable
//
// Fullscreen has four exit paths — normal completion, the quit key, an interrupt
// and a panic — and not one of them can be driven through a real alternate
// screen under `go test`, where stdout is a pipe and there is no terminal to
// take over. This repository already answers that with a seam:
// internal/common/tui/enabled.go documents isTerminal as "the package seam for
// the stdout-TTY stat ... overridden by tests".
//
// # What it does NOT replace
//
// Only the takeover. Fullscreen's own behaviour — capturing the report before
// the program starts, the deferred recover, joining the errors and printing on
// the way out — runs for real whether the seam is stubbed or not, so a test
// that swaps it still measures the dump on the way out and every exit path.
var runProgram = func(m tea.Model, opts ...tea.ProgramOption) (tea.Model, error) {
	return tea.NewProgram(m, opts...).Run()
}

// Fullscreen renders blocks in an alternate screen, then prints the whole
// report to the scrollback when that screen goes away.
//
// The report is the blocks argument, never the finished model: every exit path
// prints that slice, so the dump survives a model that crashed and a program
// that never started.
//
// Bubble Tea catches a panic in Update or View by default: it leaves the
// alternate screen and Run returns ErrProgramPanic, so that case prints through
// the errors.Join at the end like any other return. The deferred recover covers
// what Bubble Tea does not catch: a panic outside Run, or in a program started
// without its panic handler. It restores the terminal itself, prints the report
// and re-panics, since swallowing the panic would turn a crash into a silent
// wrong answer.
//
// ctrl+c adds one section, interruptNotice, at the top of the dump. Output goes
// to os.Stdout: the dump belongs in the scrollback the alternate screen hid.
func Fullscreen(blocks []report.Section, opts Options) error {
	defer func() {
		rec := recover()
		if rec == nil {
			return
		}

		restoreTerminal(os.Stdout)

		// The dump error is discarded deliberately. A panic is already in
		// flight carrying the cause worth reporting, there is no return value
		// left to put a second error in, and a failure to print is not a reason
		// to stop propagating the first failure.
		//
		// The blocks go out unlabelled, because a panic is not an interrupt:
		// nothing has set the bit below.
		_ = Plain(os.Stdout, blocks, opts)

		panic(rec)
	}()

	// interrupted is the whole of what travels back from the model: one bit,
	// written on ctrl+c and owned here. It is a pointer precisely so the
	// answer does NOT ride home inside the model — bubbletea hands Update a
	// copy of a value model, and reading the returned model back is the
	// dependency this design exists to refuse.
	//
	// It is consumed below, after the program has returned. What it produces is
	// one constant section, so the deferred branch above still has a whole
	// report to print no matter when the panic lands.
	interrupted := new(atomic.Bool)

	_, err := runProgram(newInterruptibleModel(blocks, opts, interrupted), tea.WithAltScreen())

	// The label is prepended to a NEW slice, never appended into the caller's:
	// a renderer that wrote through the sections it was handed would be editing
	// a report by having displayed it, and the same slice goes to the export a
	// few lines later in cmd/bentoo.
	//
	// The screen showed the report unlabelled, and that is correct: the
	// interrupt had not happened while it was up. The dump is the artefact
	// somebody keeps, pastes and reads later, and it is the one that has to
	// admit it is partial.
	dump := blocks
	if interrupted.Load() {
		dump = append([]report.Section{interruptNotice}, blocks...)
	}

	return errors.Join(err, Plain(os.Stdout, dump, opts))
}

// interruptNotice is the section ctrl+c adds to the dump.
//
// # A renderer states this one, and only this one
//
// Every other section is the caller's, built from what the run found. This one
// is not: nobody but this function knows the alternate screen was left through
// ctrl+c rather than through q, and a report that reads as complete when it was
// cut short is a wrong answer rather than a short one.
//
// # It carries no count, and the absence is deliberate
//
// The sentence this replaces named how many planned units the run never reached.
// That number is a fact about the RUN — it is read off the report's own plan and
// results — and a renderer that computed one would have to be handed the run to
// do it — the coupling that taking sections instead of a report removed.
// Whoever builds the sections states the count, in its own vocabulary, in its
// own leading section; this states only what happened at the terminal.
var interruptNotice = report.Section{
	Title: "Run Interrupted",
	Lead: []string{
		"This report is incomplete: the run was interrupted before its report was read out.",
	},
	Notes: []string{
		"The sections below cover only what the run had reached; anything it never got to is counted in no column.",
	},
}

// restoreTerminal puts the alternate screen away and the cursor back.
//
// The sequences come from termenv's own constants rather than being typed here,
// so this file is not a second place where an escape is spelled out. The write
// error is discarded because the only caller is on its way out through a panic:
// there is nowhere to return it and nothing left to do about it.
func restoreTerminal(w io.Writer) {
	_, _ = io.WriteString(w, termenv.CSI+termenv.ExitAltScreenSeq+termenv.CSI+termenv.ShowCursorSeq)
}

// newModel builds the alternate-screen model for blocks.
//
// It is the constructor for a render that has nobody to report an interrupt TO
// — a test driving Update directly, for one. Fullscreen uses the variant below,
// which threads its own signal in.
func newModel(blocks []report.Section, opts Options) tea.Model {
	return newInterruptibleModel(blocks, opts, new(atomic.Bool))
}

// newInterruptibleModel is newModel with the caller's interrupt bit attached.
func newInterruptibleModel(blocks []report.Section, opts Options, interrupted *atomic.Bool) tea.Model {
	return fullscreenModel{
		blocks:      blocks,
		askedWidth:  opts.Width,
		paint:       inlinePaint(),
		interrupted: interrupted,
	}
}

// fullscreenModel is the report as a bubbletea model.
//
// # It holds the SECTIONS, and now so does everything above it
//
// "The report is never read from the model" is a rule someone has to keep, and
// a model holding a payload is a model somebody can read one out of. It holds
// the sections it was handed, so the rule is a fact about the type rather than a
// discipline about the code — and the same is true one level up: Fullscreen never held a report either, so there is no second copy of
// anything to reach for.
//
// # What the fields are not
//
// interrupted is not state. It is a pointer OUT, written once and never read
// here, which is what lets a value model — copied on every Update — still tell
// its owner that ctrl+c happened.
type fullscreenModel struct {
	// blocks is the report as structure: the identical []report.Section plain,
	// Markdown and inline are written from — the same slice value, handed to
	// every mode by one caller — which is what makes "the same content in every
	// mode" hold by construction here too.
	blocks []report.Section
	// askedWidth is opts.Width, kept alone rather than the whole Options.
	// Width is the only field Options has, and unpacking it here says what the
	// model actually consults: a frame is laid out at a width, and nothing else
	// about the caller's request survives into a redraw.
	askedWidth int
	// paint is built once rather than per frame: inlinePaint constructs a
	// lipgloss renderer, and a TUI redraws far too often to pay for that on
	// every View.
	paint paint

	// width and height are the viewport, as the terminal last reported it.
	// Zero means "not sized yet", and an unsized frame omits nothing: a height
	// budget invented before the terminal answered would hide rows that fit.
	width  int
	height int

	// interrupted is Fullscreen's bit, set on ctrl+c. Never nil in practice;
	// the guard in Update keeps a hand-built model from panicking.
	interrupted *atomic.Bool
}

// Init implements tea.Model. There is nothing to start: the report was finished
// before this program existed, so the model has no work, no timer and no
// spinner to kick off.
func (m fullscreenModel) Init() tea.Cmd { return nil }

// Update implements tea.Model: a resize, and the three keys that end the run.
//
// ctrl+c both records the interrupt and quits. Recording it is what lets
// Fullscreen tell an interrupted run from a completed one without reading this
// model back; quitting rather than dying is what lets bubbletea take the
// alternate screen down on its own, so restoring it needs no second answer here.
func (m fullscreenModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			if m.interrupted != nil {
				m.interrupted.Store(true)
			}
			return m, tea.Quit
		case "q", "esc":
			return m, tea.Quit
		}
	}

	return m, nil
}

// View implements tea.Model: the report inside one box, with the keys named
// under it.
//
// The body is laid out by the SAME writer plain and inline use, at the width
// left over once the box has taken its two cells, so a heading, a column width
// or a shortened reason is never decided twice. What this function adds
// is the frame and the height budget — the two things a full screen has and a
// scrollback does not.
func (m fullscreenModel) View() string {
	width := m.frameWidth()
	inner := max(width-fullscreenFrame, 1)

	body := m.layout(inner).fit(m.rowBudget(), inner)

	box := lipgloss.NewStyle().
		Border(borderStyle).
		Padding(0, fullscreenPad).
		// lipgloss rewrites a tab as four spaces by default. A reason string
		// comes from a subprocess, so that default would let this mode print a
		// line the other modes print differently — a content difference
		// produced by the frame, which the modes must never have.
		TabWidth(lipgloss.NoTabConversion).
		Render(strings.Join(body, "\n"))

	return box + "\n" + m.hint(inner)
}

// frameWidth is how wide the frame may be: what the terminal reported, else
// what the caller asked for, else what the device says.
//
// The live resize goes in front because it is the only one of the three that
// can change while the program is running. The other two are Options.cells's
// question, asked through Options.cells so that "0 means ask the device" is
// answered in one place for every mode rather than a third time here.
func (m fullscreenModel) frameWidth() int {
	if m.width > 0 {
		return m.width
	}
	return Options{Width: m.askedWidth}.cells()
}

// rowBudget is how many rows of report the frame can hold, or 0 for "as many as
// there are".
//
// Zero is the unsized case and means no omission: before the terminal has said
// how tall it is, cutting the report would be hiding rows on a guess.
//
// A terminal shorter than the chrome itself is not branched on. The floor of
// one row keeps the arithmetic from going negative, and a four-row terminal is
// not a case worth a second layout.
func (m fullscreenModel) rowBudget() int {
	if m.height <= 0 {
		return 0
	}
	return max(m.height-fullscreenChrome, 1)
}

// hint is the line under the box: the keys, and the promise that quitting costs
// nothing.
//
// Naming the dump is not decoration. An operator who does not know the report
// will be printed on the way out has every reason to sit in a screen they
// cannot scroll rather than leave it, and the dump on exit is only useful to
// someone who knows it happens.
func (m fullscreenModel) hint(cells int) string {
	line := strings.Repeat(" ", indent) + "q/esc quit · ctrl+c interrupt · the whole report prints on exit"
	return shortenTo(decorate(m.paint.detail, line), cells)
}

// pane is the report laid out for a viewport: every line it would occupy, and
// the index each section starts at.
//
// The starts are what let an omission be counted in SECTIONS and not only in
// lines. "17 more lines" tells a reader the screen is short; "17 more lines and
// 2 whole sections" tells them the run has results they have not seen at all,
// which is the difference between an untidy screen and a misleading one.
type pane struct {
	lines  []string
	starts []int
	// paint is carried so the omission below can be emphasised with the same
	// vocabulary the rest of the frame uses, instead of inventing a second one.
	paint paint
}

// layout renders every section at the given width, padded to it so the box
// draws one straight edge instead of a ragged one.
func (m fullscreenModel) layout(cells int) pane {
	st := textStyle(cells, m.paint)

	p := pane{starts: make([]int, 0, len(m.blocks)), paint: m.paint}
	for i, s := range m.blocks {
		// The blank line between two sections is write's, reproduced here for
		// the same reason it exists there: one empty line separates a section
		// from the next, in every syntax.
		if i > 0 {
			p.lines = append(p.lines, "")
		}
		p.starts = append(p.starts, len(p.lines))

		for _, line := range sectionLines(s, st) {
			p.lines = append(p.lines, padTo(line, cells))
		}
	}

	return p
}

// fit reduces the pane to budget rows, STATING what it dropped.
//
// # The last row buys the sentence that explains the others
//
// A screen that shows four rows of a forty-row report and says nothing has
// told its reader something false: they have no way to know the run produced
// forty. So when the report does not fit, one row of the budget is spent on
// the count, and the reader loses one more line of report to learn that the
// rest exists. That trade is the requirement.
//
// A budget of 0 means the viewport is unknown and nothing is dropped.
func (p pane) fit(budget, cells int) []string {
	if budget <= 0 || len(p.lines) <= budget {
		return p.lines
	}

	shown := max(budget-1, 0)

	fitted := make([]string, 0, budget)
	fitted = append(fitted, p.lines[:shown]...)
	return append(fitted, padTo(p.omission(shown), cells))
}

// omission is the sentence fit leaves in place of the rows it dropped.
//
// The counts come first so the deepest cut a narrow terminal can make still
// leaves a number and the word that admits to it standing.
//
// It is painted with the header style — the emphasis, not the dim one. Dim is
// what this package uses for a line the reader may skip past; a report saying
// it is not all here is the last line on the screen that should recede.
func (p pane) omission(shown int) string {
	lines := len(p.lines) - shown

	sentence := fmt.Sprintf("… %d more line(s) not shown", lines)
	if hidden := p.sectionsBelow(shown); hidden > 0 {
		sentence = fmt.Sprintf("… %d more line(s) and %d whole section(s) not shown", lines, hidden)
	}

	return decorate(p.paint.header, strings.Repeat(" ", indent)+sentence+" — press q for the whole report")
}

// sectionsBelow is how many sections begin at or after the cut — the ones the
// reader sees no part of, not merely the ones cut short.
func (p pane) sectionsBelow(shown int) int {
	hidden := 0
	for _, start := range p.starts {
		if start >= shown {
			hidden++
		}
	}
	return hidden
}

// sectionLines renders one section through the shared writer and returns its
// lines.
//
// Going through write rather than reimplementing the layout is what keeps this
// mode from becoming a fourth answer to how a heading, a column or a note is
// written.
func sectionLines(s report.Section, st style) []string {
	var buf bytes.Buffer
	if err := write(&buf, []report.Section{s}, st); err != nil {
		// bytes.Buffer never fails a write, so this cannot happen — and a View
		// may not return an error anyway. Putting the failure on the screen
		// beats discarding it and rendering an empty section that looks like a
		// run with nothing in it.
		return []string{"this section could not be rendered: " + err.Error()}
	}

	return strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
}

// padTo makes a line exactly cells wide: shortened if it overflows, padded if
// it falls short.
//
// Both halves protect the height budget. A line wider than the box makes
// lipgloss wrap it into two rows, and a frame measured in rows cannot afford a
// row it did not count; padding is what gives the box one straight edge instead
// of a width taken from whichever line happened to be longest.
func padTo(line string, cells int) string {
	return pad(shortenTo(line, cells), cells)
}

// shortenTo cuts a line to cells display columns, and returns it untouched when
// it already fits.
//
// The guard is not an optimisation. shorten goes through ansi.Truncate, which
// rebuilds the string it is handed; a line that fits should reach the terminal
// as the writer produced it, escape sequences and all.
func shortenTo(line string, cells int) string {
	if lipgloss.Width(line) <= cells {
		return line
	}
	return shorten(line, cells)
}
