package render

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/obentoo/bentoolkit/internal/common/report"
)

const (
	// indent is how far a section's body sits from the left margin; gap is the
	// space between two columns.
	//
	// Neither is a field width. A field width decides how much room a VALUE
	// gets, so it depends on the values this run produced and can therefore be
	// measured — that is what columnWidth does. These two are the air BETWEEN
	// fields: nothing in a run's data can make two columns need more or less of
	// it, so there is nothing to measure.
	indent = 2
	gap    = 2

	// detailIndent is where a row's explanation sits: further in than the row it
	// belongs to, so a reader scanning the first column never mistakes a reason
	// for another package.
	detailIndent = indent + 2
)

// Options is everything a caller may decide about one rendering. It holds no
// fact about the run — those all come from the sections it is rendering.
//
// Its one field, Width, is a line budget measured off the terminal the output
// is going to, which nothing upstream of a renderer can know; that is the whole
// of what a renderer is entitled to decide.
//
// ShowAll and SkipPlan are deliberately NOT here: whether a list is stated or
// only counted is content, so they live on report.SectionOptions, what a
// payload is ASKED. SectionOptions is what the report should SAY, Options is
// what the device ALLOWS; a renderer reading ShowAll would make plain and
// fullscreen say different things. Nothing may be added here that a payload
// could have answered. Markdown and JSON take no Options at all, so an export
// path cannot ask questions an export must never ask.
type Options struct {
	// Width is the line budget in display cells: no rendered line exceeds it
	// Zero means "ask the device", which is terminalWidth's job, so a caller
	// that has no opinion does not have to invent one.
	//
	// Read it through cells() rather than directly: the zero is a question, not
	// an answer.
	Width int
}

// cells is the line budget this render actually gets: what the caller asked
// for, or what the device says when the caller had no opinion.
//
// # It is a method because the zero value means something
//
// "Width == 0 means ask the device" is a rule, and a rule implemented at each
// call site is a rule each new renderer has to be told. Plain and Inline both
// spelled it out — four identical lines, twice — and fullscreenModel.frameWidth
// spelled the tail of it a third time. Putting it on the type that declares the
// rule in its own doc comment is what makes a fourth renderer inherit it instead
// of re-deriving it, and a re-derivation that drifted would produce a report
// laid out at a width nothing asked for.
//
// A negative Width is treated as zero rather than rejected: it is the same
// absence of an opinion, arriving from a caller that computed one badly, and a
// renderer's job in that case is to lay the report out anyway.
func (o Options) cells() int {
	if o.Width > 0 {
		return o.Width
	}
	return terminalWidth()
}

// Plain renders blocks as text with no escape sequence in it.
//
// It takes sections, not a report: everything here is layout (column widths,
// cuts, underlined headings), and nothing knows what a package or a gate is, so
// a new command is a new payload rather than an edit here. The caller decides
// WHAT the report says; this decides only how it LOOKS.
//
// It is the mode a pipe, a redirect, a cron mail and a CI transcript receive:
// no colour, no cursor movement, no live region. lipgloss is used only to
// MEASURE, never to render, so the no-escape property holds by construction.
// It draws no box, which a log cannot use and which costs two cells per line;
// sections are separated by a title and a rule built from borderStyle's own
// segment, so changing the border once changes every mode.
//
// Every write is checked: the first failing write is returned, wrapped, and
// stops the rest, rather than reporting success for output nobody received.
func Plain(w io.Writer, blocks []report.Section, opts Options) error {
	return write(w, blocks, plainStyle(opts.cells()))
}

// Markdown renders blocks as a Markdown document: the same sections, in the
// syntax a pull request comment, an issue and a file in a repository all read.
//
// It takes no Options, and that absence IS the requirement: an export carries
// the complete report — every package, every reason in full, whatever the
// terminal was asked for — and with no width or ShowAll to honour, an export
// that mirrored screen truncation cannot be written. A report is kept BECAUSE
// the terminal is gone; if threading a width in here ever looks convenient,
// that is the defect arriving as a convenience.
//
// Listing every scanned package (the screen only counts the up-to-date ones)
// is the caller's promise, made when the sections were built, so a kept report
// can answer "was this one checked at all". It differs from Plain by table
// syntax and nothing else: both hand the same sections to a style. A result
// whose reason repeats its plan entry prints no second copy here either, which
// drops a DUPLICATE: the sentence is still whole on the plan row.
func Markdown(w io.Writer, blocks []report.Section) error {
	return write(w, blocks, markdownStyle())
}

// ------------------------------------------------------------------ the styles

// style is the syntax half of a renderer: how a heading is written, how a
// sentence is written, how a table is written.
//
// It is the ONLY thing separating plain from Markdown. What a report says,
// in what order, with which rows carrying a reason, was settled by whoever built
// the sections — both styles are handed the same []report.Section and neither
// may add to it or take from it.
//
// # A width is captured here, or it does not exist
//
// plainStyle takes the line budget and closes over it. markdownStyle takes no
// argument at all, and this struct has no field to hold one, so an export has
// nowhere to receive a width even from a caller offering it. An export's
// completeness rests on that absence rather than on a branch somebody has to
// remember.
type style struct {
	// heading writes the section title, however this syntax marks one.
	heading func(out *lineWriter, title string)
	// prose writes one sentence of lead or of note.
	prose func(out *lineWriter, text string)
	// table writes the section's rows, header included. Where a row's detail
	// goes is this function's problem: the two syntaxes place it differently
	// and neither placement is a fact about the run.
	table func(out *lineWriter, t report.Table)
}

// paint is the decoration half of the terminal syntax: which escape sequences a
// finished line is wrapped in, and nothing else.
//
// Every function here is handed a COMPLETE line — indent, padded cells, gaps —
// and may only return it wrapped. It never sees a cell, so it cannot change a
// width, a cut, an order or a word: the visible characters of a decorated
// render come from the same code as an undecorated one, which keeps the content
// identical in every mode by construction. A style that PADS (lipgloss's Width,
// Padding, Margin, Border) would add visible cells and break that; bold, faint
// and a foreground colour do not.
//
// A nil field is the identity: plainStyle passes paint{} and emits byte for
// byte what it did before this seam existed, so no escape sequence reaches a
// log without a branch to remember.
type paint struct {
	// heading wraps a section title.
	heading func(string) string
	// rule wraps the line under a title.
	rule func(string) string
	// header wraps a table's header row.
	header func(string) string
	// detail wraps a row's reason, the indent included.
	detail func(string) string
}

// decorate applies f to s, or returns s untouched when f is nil.
func decorate(f func(string) string, s string) string {
	if f == nil {
		return s
	}
	return f(s)
}

// textStyle is the terminal syntax at a line budget, decorated by p: a title
// over a rule, prose wrapped to the budget, and columns measured from the values
// then padded with spaces.
//
// Both terminal modes are built from this one call, differing only in the paint
// they hand it.
func textStyle(width int, p paint) style {
	return style{
		heading: func(out *lineWriter, title string) { writePlainHeading(out, title, width, p) },
		prose:   func(out *lineWriter, text string) { writeProse(out, text, width) },
		table:   func(out *lineWriter, t report.Table) { writePlainTable(out, t, width, p) },
	}
}

// plainStyle is that syntax with no decoration at all.
func plainStyle(width int) style {
	return textStyle(width, paint{})
}

// markdownStyle is the document syntax: an ATX heading, prose written whole, and
// pipe tables.
//
// Every field is a plain function and not a closure, because there is nothing
// for one to close over — which is what an export having no settings looks like
// in code.
func markdownStyle() style {
	return style{
		heading: writeMarkdownHeading,
		prose:   writeMarkdownProse,
		table:   writeMarkdownTable,
	}
}

// write prints every section of a report in one syntax and returns the first
// write that failed.
//
// The blank line between two sections is here rather than in a style because it
// is the same line in both: one empty line separates a section from the next, in
// a terminal and in a document alike.
func write(w io.Writer, blocks []report.Section, st style) error {
	out := &lineWriter{w: w}
	for i, s := range blocks {
		if i > 0 {
			out.line("")
		}
		writeSection(out, s, st)
	}

	return out.err
}

// writeSection prints one section — heading, lead, rows, notes — in that order,
// in every syntax.
//
// The ORDER is shared and the SYNTAX is not. The two
// blank lines are shared too: a table is preceded by one only when a lead was
// printed above it, and notes always are, so the shape of a section survives the
// change of syntax.
func writeSection(out *lineWriter, s report.Section, st style) {
	st.heading(out, s.Title)

	for _, lead := range s.Lead {
		st.prose(out, lead)
	}

	if len(s.Rows.Rows) > 0 {
		if len(s.Lead) > 0 {
			out.line("")
		}
		st.table(out, s.Rows)
	}

	if len(s.Notes) > 0 {
		out.line("")
		for _, note := range s.Notes {
			st.prose(out, note)
		}
	}
}

// ----------------------------------------------------------- the plain writer

// writePlainHeading prints a section title and the rule under it, both held to
// the width budget that section building deliberately knows nothing about.
func writePlainHeading(out *lineWriter, title string, width int, p paint) {
	fitted := shorten(title, width)
	out.line(decorate(p.heading, fitted))
	if line := rule(lipgloss.Width(fitted)); line != "" {
		out.line(decorate(p.rule, line))
	}
}

// writePlainTable prints a table whose every column was measured from the values
// this run will actually print.
//
// A row's detail is CUT to one line while prose is WRAPPED, and the asymmetry is
// deliberate. A sentence standing on its own loses nothing by occupying three
// lines. A reason wrapped between two rows costs the table the property that
// makes it scannable — one record, one line — and a reader looking for the next
// package has to re-find the column instead of following it down. The cut is
// marked and the model still holds the whole string, so a syntax with no width
// budget prints all of it.
func writePlainTable(out *lineWriter, t report.Table, width int, p paint) {
	widths := plainColumnWidths(t, width)

	out.line(decorate(p.header, plainRow(t.Headers, widths)))
	for _, r := range t.Rows {
		out.line(plainRow(r.Cells, widths))

		// The emptiness is tested AFTER shortening, not before: a budget too
		// narrow to hold even the ellipsis leaves nothing to print, and printing
		// the indent anyway would put a line of pure whitespace in a log and in
		// every golden file.
		if detail := shorten(r.Detail, width-detailIndent); detail != "" {
			out.line(decorate(p.detail, strings.Repeat(" ", detailIndent)+detail))
		}
	}
}

// plainColumnWidths measures every column, then shrinks until the row fits the
// budget.
//
// # It takes from the widest column, one cell at a time
//
// The widest column is the one with room to give. Shrinking every column in
// proportion would cut a 7-cell version number in half to spare a 40-cell atom
// that was never in danger, and the version is the value a reader can least
// afford to receive half of. One cell per pass is O(overflow) on a table with a
// handful of columns, and it stops the moment the row fits.
func plainColumnWidths(t report.Table, width int) []int {
	widths := make([]int, t.Columns())
	for column := range widths {
		widths[column] = columnWidth(columnValues(t, column))
	}

	for rowWidth(widths) > width {
		widest := 0
		for column := range widths {
			if widths[column] > widths[widest] {
				widest = column
			}
		}
		if widths[widest] <= 1 {
			// Every column is down to a single cell and the line still does not
			// fit, so the budget is narrower than the table has columns. Stop
			// rather than spin: a report squeezed past what the device can hold
			// is something the caller needs to see, not something this loop can
			// resolve.
			break
		}
		widths[widest]--
	}

	return widths
}

// rowWidth is what a row of these columns occupies once the indent and the gaps
// between columns are counted. Forgetting either is how a table that measures
// as fitting still wraps.
func rowWidth(widths []int) int {
	total := indent
	for column, cells := range widths {
		if column > 0 {
			total += gap
		}
		total += cells
	}
	return total
}

// columnValues is every string one column will print, header included: the
// header is part of the column and a width that ignored it would cut "CANDIDATE"
// down to fit the versions below it.
func columnValues(t report.Table, column int) []string {
	values := make([]string, 0, len(t.Rows)+1)
	if column < len(t.Headers) {
		values = append(values, t.Headers[column])
	}
	for _, r := range t.Rows {
		if column < len(r.Cells) {
			values = append(values, r.Cells[column])
		}
	}
	return values
}

// plainRow lays out one row's cells at the measured widths.
//
// The last column is never padded and the line is right-trimmed: trailing spaces
// are invisible to a reader, visible to a diff, and pure noise in a golden file.
func plainRow(cells []string, widths []int) string {
	parts := make([]string, 0, len(widths))
	for column, cellWidth := range widths {
		value := ""
		if column < len(cells) {
			value = shorten(cells[column], cellWidth)
		}
		if column == len(widths)-1 {
			parts = append(parts, value)
			break
		}
		parts = append(parts, pad(value, cellWidth))
	}

	line := strings.Repeat(" ", indent) + strings.Join(parts, strings.Repeat(" ", gap))
	return strings.TrimRight(line, " ")
}

// pad extends value to cells display columns.
//
// It measures with lipgloss.Width for the reason columnWidth does: a CJK
// character is one rune, three bytes and two cells, and only the last of those
// is the number the terminal aligns on.
func pad(value string, cells int) string {
	if missing := cells - lipgloss.Width(value); missing > 0 {
		return value + strings.Repeat(" ", missing)
	}
	return value
}

// writeProse prints a sentence at the body indent, wrapped rather than cut.
//
// Wrapped because a sentence longer than the terminal wraps either way — the
// only question is whether it wraps here, at the indent, or at the margin with
// the next line starting in column one. ansi.Wrap breaks an over-long word as
// well, so the budget is a guarantee and not a preference.
func writeProse(out *lineWriter, text string, width int) {
	budget := max(width-indent, 1)
	for _, line := range strings.Split(ansi.Wrap(text, budget, ""), "\n") {
		if line == "" {
			out.line("")
			continue
		}
		out.line(strings.Repeat(" ", indent) + line)
	}
}

// rule is the horizontal line under a section title, as wide as the title.
//
// It is assembled from borderStyle's own top segment rather than from a
// character written here, so the border is decided in one place for every
// mode. The segment is repeated by count and then measured back to it, which is
// what makes the rule exactly as wide as asked for even if a future border is
// built from a wide character.
//
// A border made of whitespace — lipgloss.HiddenBorder — would draw a line of
// trailing spaces, so it draws nothing instead.
func rule(cells int) string {
	segment := borderStyle.Top
	if cells <= 0 || strings.TrimSpace(segment) == "" {
		return ""
	}
	return ansi.Truncate(strings.Repeat(segment, cells), cells, "")
}

// -------------------------------------------------------- the Markdown writer

const (
	// markdownHeading is the level a section title is written at: two hashes,
	// so a report pasted into an issue or a pull request comment sits UNDER
	// whatever title that page already has instead of competing with it.
	markdownHeading = "## "

	// detailHeader labels the column a row's reason is written in.
	//
	// The plain writer puts a reason on a line of its own under the row it
	// belongs to. A pipe table has no such line — a row IS a line, and anything
	// between two rows ends the table — so the same value becomes a trailing
	// cell instead, and a column in this syntax must be labelled. That is the
	// only word this writer adds to a report, and the model's own name for the
	// value ("reason", in full, per row) is the word it adds.
	detailHeader = "REASON"

	// markdownSeparatorCell is the one cell of the row a pipe table needs
	// between its header and its body. Without that row the header is not a
	// header and the whole block renders as a paragraph full of pipes.
	markdownSeparatorCell = "---"
)

// markdownCellEscaper makes a value safe to put between two pipes, without
// dropping a character of it.
//
// A pipe inside a cell would end the cell, so it is escaped — `\|` renders as
// the pipe the value held, so nothing is lost. A line break inside a cell would
// end the ROW, and the syntax has no escape for that one: a row is one line by
// definition. Folding a break to a space is the single place this writer alters
// a value, and it alters only the break — every character around it still
// reaches the reader, which is what separates folding from shortening.
// CRLF is listed before CR so a Windows line ending folds to one space and not
// to two; a Replacer prefers the pattern given first.
//
// Neither case is hypothetical. PackageResult.Error is a failure reported
// exactly as it arrived, and a subprocess writes what it likes.
var markdownCellEscaper = strings.NewReplacer(
	"|", `\|`,
	"\r\n", " ",
	"\r", " ",
	"\n", " ",
)

// writeMarkdownHeading writes an ATX heading and the blank line under it.
//
// The blank line is not decoration: it closes the heading's block, and the
// pipe table that may follow has to begin one of its own.
func writeMarkdownHeading(out *lineWriter, title string) {
	out.line(markdownHeading + title)
	out.line("")
}

// writeMarkdownProse writes one sentence, whole.
//
// Nothing is wrapped and nothing is cut. A document has no line to fit, and
// hard-wrapping a paragraph that will be re-flowed by whatever renders it only
// puts breaks where the reader's window is not.
func writeMarkdownProse(out *lineWriter, text string) {
	out.line(text)
}

// writeMarkdownTable writes a table as pipe rows.
//
// Nothing is measured and nothing is padded: whatever renders a pipe table
// aligns it, and a column width here would be the first place a line budget
// could enter the export path. Every cell holds its value in full, including
// the ~230-character reason the terminal shows sixty cells of.
//
// The column count is report.Table.Columns, which reads the header AND every
// row, so a row with an extra cell is printed rather than dropped and the two
// syntaxes cannot disagree about it.
//
// Each row is COPIED into a slice of its own, never appended to in place:
// appending the reason cell onto the section's own slice could write into the
// backing array the section still holds, a renderer quietly editing the report
// it was given.
func writeMarkdownTable(out *lineWriter, t report.Table) {
	columns := t.Columns()
	withDetail := hasDetail(t)

	headers := make([]string, columns, columns+1)
	copy(headers, t.Headers)
	if withDetail {
		headers = append(headers, detailHeader)
	}

	out.line(markdownRow(headers))
	out.line(markdownSeparator(len(headers)))

	for _, r := range t.Rows {
		cells := make([]string, columns, columns+1)
		copy(cells, r.Cells)
		if withDetail {
			cells = append(cells, r.Detail)
		}
		out.line(markdownRow(cells))
	}
}

// hasDetail reports whether any row of the table has a reason to print.
//
// A table where none does gets no reason column at all — the same rule the
// plain writer follows when it omits an empty detail rather than printing a
// bare indent. A column nothing fills is an empty cell on every row, and a
// header promising an explanation that never comes.
func hasDetail(t report.Table) bool {
	for _, r := range t.Rows {
		if r.Detail != "" {
			return true
		}
	}
	return false
}

// markdownRow lays one row's cells out between pipes.
//
// The leading and trailing pipes are optional in the syntax and written anyway:
// they make a row that ends in an empty cell — a result whose reason the plan
// already stated — still show that the cell is there.
func markdownRow(cells []string) string {
	escaped := make([]string, len(cells))
	for i, cell := range cells {
		escaped[i] = markdownCellEscaper.Replace(cell)
	}

	return "| " + strings.Join(escaped, " | ") + " |"
}

// markdownSeparator is the header rule, one cell per column.
//
// It goes through markdownRow rather than assembling pipes of its own, so this
// syntax is written down in exactly one place — which is the point of a style:
// a second spelling of a row is a second thing to keep in agreement. Escaping a
// cell of dashes is a no-op, so nothing is paid for the reuse.
func markdownSeparator(columns int) string {
	cells := make([]string, columns)
	for i := range cells {
		cells[i] = markdownSeparatorCell
	}

	return markdownRow(cells)
}

// lineWriter writes whole lines and remembers the first failure.
//
// Every one of the roughly forty writes a report makes can fail, and checking
// each at its call site would quadruple the renderer to stop at exactly the same
// place. Latching instead produces byte-for-byte the same output as checking
// every call, and Plain returns what went wrong rather than reporting success
// for output nobody received.
type lineWriter struct {
	w     io.Writer
	lines int
	err   error
}

// line writes s and a newline, or does nothing if a previous write already
// failed.
func (lw *lineWriter) line(s string) {
	if lw.err != nil {
		return
	}

	lw.lines++
	if _, err := io.WriteString(lw.w, s+"\n"); err != nil {
		// The line number is the context that makes the failure reproducible: it
		// says how much of the report reached the far end before it stopped,
		// which is the difference between a full disk and a closed pipe.
		lw.err = fmt.Errorf("writing report line %d: %w", lw.lines, err)
	}
}
