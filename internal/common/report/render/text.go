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
	// Neither is a field width, and the difference is the whole of R6.3. A field
	// width decides how much room a VALUE gets, so it depends on the values this
	// run produced and can therefore be measured — that is what columnWidth
	// does. These two are the air BETWEEN fields: nothing in a run's data can
	// make two columns need more or less of it, so there is nothing to measure.
	indent = 2
	gap    = 2

	// detailIndent is where a row's explanation sits: further in than the row it
	// belongs to, so a reader scanning the first column never mistakes a reason
	// for another package.
	detailIndent = indent + 2
)

// Options is everything a caller may decide about one rendering. It holds no
// fact about the run — those all come from the report.
type Options struct {
	// Width is the line budget in display cells: no rendered line exceeds it.
	// Zero means "ask the device", which is terminalWidth's job, so a caller
	// that has no opinion does not have to invent one.
	Width int
	// ShowAll lists the packages found up to date in the version-check section
	// instead of only counting them (R8.2). It is the --all flag.
	//
	// False is today's behaviour and prints the count alone (R8.3): the up to
	// date packages are the bulk of a 269-package overlay and listing them by
	// default is most of why a check that found four updates prints 348 lines.
	ShowAll bool
}

// Plain renders r as text with no escape sequence in it (R2.1).
//
// # It is the mode a log file gets
//
// Plain is what a pipe, a redirect, a cron mail and a CI transcript receive, so
// it contains no colour, no cursor movement and no live region: every byte it
// writes is a byte a reader without a terminal still wants. Nothing here calls
// lipgloss's renderer — lipgloss is used only to MEASURE (lipgloss.Width) — so
// the no-escape property holds by construction rather than by discipline.
//
// # It draws no box
//
// A frame around a table is decoration a log cannot use, and it costs two cells
// of every line's budget to carry. Sections are separated by a title and a rule
// instead. The rule is built from borderStyle's own horizontal segment, so plain
// is not a second place where a line character is spelled out: change the border
// once and every mode follows.
//
// # Every write is checked
//
// A report redirected to a full disk is an ordinary failure, not an exotic one,
// and a renderer that swallowed it would report success for output nobody
// received. The first failing write is returned, wrapped, and stops the rest.
func Plain(w io.Writer, r report.Report, opts Options) error {
	width := opts.Width
	if width <= 0 {
		width = terminalWidth()
	}

	out := &lineWriter{w: w}
	for i, s := range sections(r, opts) {
		if i > 0 {
			out.line("")
		}
		writePlainSection(out, s, width)
	}

	return out.err
}

// sections is the whole report as structure: what it says, in order, with every
// value at full length and not one decision about how it will look.
//
// # It is the half Markdown reuses
//
// Plain and Markdown differ by table syntax and nothing else (D8), and this is
// the line that fact is drawn on: everything that decides WHAT a report says
// lives here, everything that decides how it LOOKS lives in a writer. A second
// syntax builds the same sections and prints them with pipes; it does not
// re-derive which packages are up to date, or how a skipped plan entry is
// labelled, because a second derivation is a second thing to keep in agreement.
//
// # Nothing here is shortened
//
// Every string a section carries is the model's own, in full. Width is applied
// by the writer, which is what lets a syntax with no width budget print the
// whole reason and keeps the model's promise that shortening is a rendering
// decision rather than a loss of data (R7.4).
func sections(r report.Report, opts Options) []section {
	return []section{
		versionCheckSection(r, opts),
		validationPlanSection(r),
		validationResultsSection(r),
		validationSummarySection(r),
	}
}

// section is one titled block of a report: a heading, the sentences that frame
// it, the rows themselves, and the sentences that qualify them.
type section struct {
	// title names the block. It is the only string a writer may decorate.
	title string
	// lead is what is said before the rows — the counts that tell a reader what
	// they are about to look at.
	lead []string
	// rows is the block's table. A section with no rows prints its lead alone,
	// which is how "nothing to validate" is said without an empty frame.
	rows table
	// notes is what is said after the rows: what was left out and why, and what
	// the run did not do. R2.5 lives here — an omitted row is stated, never
	// silent.
	notes []string
}

// table is a header and its rows, in the order they will print.
type table struct {
	headers []string
	rows    []row
}

// row is one record: its cells, and the explanation that belongs to it.
type row struct {
	cells []string
	// detail is the row's reason, IN FULL. A writer with a width budget cuts its
	// own copy; a writer without one prints all of it.
	//
	// Empty means this row has nothing of its own to add, which is a decision
	// about content and not a sign that the report held no reason: a result whose
	// reason repeats its plan entry leaves this empty so the sentence is printed
	// once (R7.2), while the report itself still carries the whole string (R7.4).
	// Every writer omits an empty detail rather than printing a bare indent.
	detail string
}

// ---------------------------------------------------------------- the sections

// versionCheckSection is what the scan found, one row per package (R8.2, R8.3).
//
// The rows a run produces depend on opts.ShowAll and the counts never do: a
// package left out of the list is still counted in the note below it, so the
// short list is a stated omission rather than a silent one (R2.5).
func versionCheckSection(r report.Report, opts Options) section {
	s := section{title: "Version Check Results"}

	if len(r.Scanned) == 0 {
		s.lead = []string{"No package is configured for autoupdate."}
		return s
	}

	found := scanCounts(r.Scanned)
	s.lead = []string{fmt.Sprintf("%d package(s) checked, %d with a pending update.", len(r.Scanned), found[conditionUpdate])}

	s.rows.headers = []string{"PACKAGE", "TYPE", "CURRENT", "CANDIDATE", "STATE"}
	for _, scanned := range r.Scanned {
		if conditionOf(scanned) == conditionUpToDate && !opts.ShowAll {
			// R8.3: counted below, not listed here.
			continue
		}
		state, detail := scanState(scanned)
		s.rows.rows = append(s.rows.rows, row{
			cells:  []string{scanned.Package, scanned.Type, scanned.CurrentVersion, scanned.CandidateVersion, state},
			detail: detail,
		})
	}

	if found[conditionUpToDate] > 0 {
		if opts.ShowAll {
			// R8.2: the list is IN ADDITION TO the count, never instead of it.
			// A reader who scrolled past the rows still gets the number, and the
			// two goldens differ in both places rather than only in one.
			s.notes = append(s.notes, fmt.Sprintf("%d package(s) are up to date and are listed above.", found[conditionUpToDate]))
		} else {
			s.notes = append(s.notes, fmt.Sprintf("%d package(s) are up to date and are not listed; pass --all to list them.", found[conditionUpToDate]))
		}
	}
	for _, stated := range []struct {
		when condition
		text string
	}{
		{conditionOrphaned, "%d package(s) have no ebuild in the overlay."},
		{conditionFailed, "%d package(s) could not be checked."},
		{conditionNotComparable, "%d package(s) reported a version that could not be ordered against the current one."},
	} {
		if found[stated.when] > 0 {
			s.notes = append(s.notes, fmt.Sprintf(stated.text, found[stated.when]))
		}
	}

	return s
}

// validationPlanSection is the cost of the run, on screen before any of it is
// spent: which packages, how far each will be taken, and the case for it.
//
// Every entry states its reason, shortened by the writer to whatever the line
// budget allows (R7.1). This is the ONE place the reason of a package whose
// result merely repeats it is stated at all — validationResultsSection prints no
// second copy (R7.2) — so a reason dropped here would not be shortened, it would
// be gone.
func validationPlanSection(r report.Report) section {
	s := section{title: "Validation Plan"}

	if len(r.Plan) == 0 {
		s.lead = []string{"No pending update to validate."}
		return s
	}

	excluded := 0
	for _, entry := range r.Plan {
		if entry.Skipped {
			excluded++
		}
	}
	lead := fmt.Sprintf("%d package(s) to evaluate", len(r.Plan))
	if excluded > 0 {
		lead += fmt.Sprintf(", %d of them excluded by policy", excluded)
	}
	s.lead = []string{lead + "."}

	s.rows.headers = []string{"PACKAGE", "BUMP", "CLASS", "DEPTH"}
	for _, entry := range r.Plan {
		depth := entry.Depth
		if entry.Skipped {
			// A package no gate will run for says so on its own row. An operator
			// reads a shorter result list as progress unless the plan already
			// told them it would be shorter.
			depth += " [not validated]"
		}
		s.rows.rows = append(s.rows.rows, row{
			cells:  []string{entry.Package, bump(entry.CurrentVersion, entry.CandidateVersion), entry.Class, depth},
			detail: entry.Reason,
		})
	}

	return s
}

// validationResultsSection is what the gates answered, one row per package the
// run reached.
//
// # A reason is printed once
//
// A row whose reason repeats its plan entry word for word prints no reason at
// all: the plan section, a few lines up the same page, already said it (R7.2).
// That one rule is the largest saving in the whole report — for every skipped
// package the check path this replaces prints the same ~230 characters twice,
// once from the plan entry and once from the result, because neither of the two
// functions doing the printing knows the other ran.
//
// A row whose reason DIFFERS is printed, and that half is what keeps the saving
// from becoming a worse defect (R7.3). A changed reason is new information: the
// plan asked for a manifest, the host could not produce one, and that sentence
// appears nowhere else in the report.
//
// # The comparison is read here, never made here
//
// Whether the two strings match is ValidationRow.SameReasonAsPlan, decided by
// the run, where both strings are already in hand. Deciding it here would mean
// finding this row's plan entry and comparing the text a second time — a second
// derivation of a value the model already carries, which is exactly what R1.3
// forbids, and one that would disagree with the model the day either side of the
// comparison changes.
//
// # Suppressed while building, not while writing
//
// WHICH reason to print is a fact about what the report SAYS, so it is settled
// here and every syntax inherits it; only how much of a printed reason fits on a
// line is left to a writer. Nothing is removed from the report itself either
// way, so an export with no width budget still carries all 230 characters
// (R7.4).
func validationResultsSection(r report.Report) section {
	s := section{title: "Validation Results"}

	if len(r.Results) == 0 {
		s.lead = []string{"No package was evaluated."}
		return s
	}

	s.rows.headers = []string{"PACKAGE", "CANDIDATE", "DEPTH", "OUTCOME"}
	for _, result := range r.Results {
		s.rows.rows = append(s.rows.rows, row{
			cells:  []string{result.Package, result.CandidateVersion, result.Depth, string(result.Outcome)},
			detail: resultDetail(result),
		})
	}

	return s
}

// resultDetail is the reason a result row prints: its own, or nothing when the
// plan already printed that same sentence (R7.2, R7.3).
//
// The empty string is how a row says "no reason of my own to add" — the writers
// already omit an empty detail rather than printing a bare indent, so there is
// no second rule to keep in agreement.
func resultDetail(result report.ValidationRow) string {
	if result.SameReasonAsPlan {
		return ""
	}
	return result.Reason
}

// validationSummarySection is the last thing a reader sees: the four counts, and
// the reminder that none of it left this machine.
//
// # Four counts, and the fourth is the point
//
// The line this replaces reported three — proved, errored, and everything else
// under "not validated" — so a package the toolkit COULD NOT evaluate was
// reported in the same column as a package the operator told it to leave alone.
// That fold lets a defect in the toolkit hide behind the operator's own policy
// (R5.1): the column grows, and nothing in the output says whose fault it is.
// Inconclusive and skipped answer that question and must never be added
// together again.
//
// # The denominator is the tally, not the plan
//
// Total() counts what actually landed in a column. For a complete run that
// equals len(Plan) — Reconciles() says so — and for a run stopped part way it is
// the honest smaller number, which leaves room for the incomplete-run label
// (Report.Complete, Report.NotEvaluated) to be added without this sentence
// having to be rewritten to stop lying.
func validationSummarySection(r report.Report) section {
	counted := r.Tally

	return section{
		title: "Validation Summary",
		lead: []string{
			fmt.Sprintf("%d package(s) evaluated: %d proved, %d errored, %d inconclusive, %d skipped.",
				counted.Total(), counted.Proved, counted.Errored, counted.Inconclusive, counted.Skipped),
		},
		notes: []string{
			"Nothing was published: a check writes no ebuild and no version pin.",
		},
	}
}

// ------------------------------------------------------------- scanned facts

// condition is the ONE thing a scan established about a package.
//
// PackageResult carries four independent bools and says in prose that they are
// mutually exclusive by construction, with "all four false" meaning up to date.
// This type is that prose as a value, decided in one place — because three
// readers needed it (the row's state word, the count beside it, and the rule
// that decides whether a row is listed at all), and three copies of the same
// branch order is three chances for a package to be counted as one thing and
// labelled as another.
type condition int

const (
	// conditionOrphaned: no ebuild in the overlay. It is read FIRST because
	// when it holds the version fields say nothing, so no later branch may
	// claim them.
	conditionOrphaned condition = iota
	// conditionFailed: the scan could not answer for this package.
	conditionFailed
	// conditionNotComparable: upstream answered something that could not be
	// ordered against the current version. It is read before HasUpdate for the
	// reason the model carries it separately — a broken parser must never be
	// read as "up to date".
	conditionNotComparable
	// conditionUpdate: upstream is newer.
	conditionUpdate
	// conditionUpToDate: the package was read and there was nothing to report.
	// It is the default rather than a flag, which is exactly how the model
	// states it.
	conditionUpToDate
)

// conditionOf reads the four flags in the model's own order. It is the only
// place in this package that reads them.
func conditionOf(result report.PackageResult) condition {
	switch {
	case result.Orphaned:
		return conditionOrphaned
	case result.Error != "":
		return conditionFailed
	case result.NotComparable:
		return conditionNotComparable
	case result.HasUpdate:
		return conditionUpdate
	default:
		return conditionUpToDate
	}
}

// scanCounts is how many packages each condition applies to. The conditions are
// mutually exclusive, so the counts sum to the number of packages scanned.
//
// It is taken in its own pass rather than inside the loop that builds the rows,
// because a count must not depend on which rows were listed: ShowAll changes the
// list and may never change the number underneath it (R8.2, R8.3).
func scanCounts(scanned []report.PackageResult) map[condition]int {
	found := make(map[condition]int, len(scanned))
	for _, result := range scanned {
		found[conditionOf(result)]++
	}
	return found
}

// scanState names a package's condition for the STATE column and returns the
// sentence that explains it, where it needs one.
//
// Only the words are decided here; which condition applies was decided once, by
// conditionOf.
func scanState(result report.PackageResult) (state, detail string) {
	switch conditionOf(result) {
	case conditionOrphaned:
		return "orphaned", "no ebuild in the overlay: the version columns say nothing about this package"
	case conditionFailed:
		return "error", result.Error
	case conditionNotComparable:
		return "not comparable" + cacheTag(result),
			fmt.Sprintf("%q could not be ordered against the current %s; check the parser configuration",
				result.CandidateVersion, result.CurrentVersion)
	case conditionUpdate:
		return "update" + cacheTag(result), ""
	default:
		return "up to date" + cacheTag(result), ""
	}
}

// cacheTag marks an answer that came from cache rather than from upstream, which
// is why a run may not reflect a bump made minutes ago.
//
// It is appended to whichever condition applies instead of being a condition of
// its own, because FromCache is orthogonal to the four: it qualifies where the
// answer came from, not what the answer was.
func cacheTag(result report.PackageResult) string {
	if result.FromCache {
		return " (cached)"
	}
	return ""
}

// bump is the two ends of a version change in one cell. Both ends travel
// together because neither is checkable without the other.
func bump(from, to string) string {
	return from + " → " + to
}

// ----------------------------------------------------------- the plain writer

// writePlainSection prints one section as text, applying the width budget that
// section building deliberately knows nothing about.
func writePlainSection(out *lineWriter, s section, width int) {
	title := shorten(s.title, width)
	out.line(title)
	if line := rule(lipgloss.Width(title)); line != "" {
		out.line(line)
	}

	for _, lead := range s.lead {
		writeProse(out, lead, width)
	}

	if len(s.rows.rows) > 0 {
		if len(s.lead) > 0 {
			out.line("")
		}
		writePlainTable(out, s.rows, width)
	}

	if len(s.notes) > 0 {
		out.line("")
		for _, note := range s.notes {
			writeProse(out, note, width)
		}
	}
}

// writePlainTable prints a table whose every column was measured from the values
// this run will actually print (R6.1, R6.3).
//
// A row's detail is CUT to one line while prose is WRAPPED, and the asymmetry is
// deliberate. A sentence standing on its own loses nothing by occupying three
// lines. A reason wrapped between two rows costs the table the property that
// makes it scannable — one record, one line — and a reader looking for the next
// package has to re-find the column instead of following it down. The cut is
// marked (R6.4) and the model still holds the whole string, so a syntax with no
// width budget prints all of it (R7.4).
func writePlainTable(out *lineWriter, t table, width int) {
	widths := plainColumnWidths(t, width)

	out.line(plainRow(t.headers, widths))
	for _, r := range t.rows {
		out.line(plainRow(r.cells, widths))

		// The emptiness is tested AFTER shortening, not before: a budget too
		// narrow to hold even the ellipsis leaves nothing to print, and printing
		// the indent anyway would put a line of pure whitespace in a log and in
		// every golden file.
		if detail := shorten(r.detail, width-detailIndent); detail != "" {
			out.line(strings.Repeat(" ", detailIndent) + detail)
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
func plainColumnWidths(t table, width int) []int {
	widths := make([]int, columnCount(t))
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

// columnCount is how many columns the table has, taken over the header AND every
// row rather than from the header alone, so a row carrying an extra cell is
// printed rather than silently dropped.
func columnCount(t table) int {
	count := len(t.headers)
	for _, r := range t.rows {
		count = max(count, len(r.cells))
	}
	return count
}

// columnValues is every string one column will print, header included: the
// header is part of the column and a width that ignored it would cut "CANDIDATE"
// down to fit the versions below it.
func columnValues(t table, column int) []string {
	values := make([]string, 0, len(t.rows)+1)
	if column < len(t.headers) {
		values = append(values, t.headers[column])
	}
	for _, r := range t.rows {
		if column < len(r.cells) {
			values = append(values, r.cells[column])
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
// is the number the terminal aligns on (R6.2).
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
// character written here, so the border is decided in one place for every mode
// (D9). The segment is repeated by count and then measured back to it, which is
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
