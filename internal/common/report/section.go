package report

// Section is one titled block of a report: a heading, the sentences that frame
// it, the rows themselves, and the sentences that qualify them.
//
// # It carries structure, never presentation
//
// Every field answers "what does this block SAY" and none answers "how does it
// look": no width, colour, padding, border or terminal dimension. The strings
// are the model's own, at full length, so the same Section prints as an
// 80-column table, a pipe table or a pane inside a box without any renderer
// being able to change what the block says.
//
// # A section with no rows is a section, not an absence
//
// "Nothing to validate" is said by a Section with a Lead and an empty Table —
// the sentence is the content, and dropping it once the table came back empty
// would make an omission indistinguishable from a block that was never
// produced.
type Section struct {
	// Title names the block. It is the only string a writer may decorate.
	Title string
	// Lead is what is said before the rows — the counts that tell a reader
	// what they are about to look at.
	Lead []string
	// Rows is the block's table. A section with no rows prints its lead alone,
	// which is how "nothing to validate" is said without an empty frame.
	Rows Table
	// Notes is what is said after the rows: what was left out and why, and
	// what the run did not do. An omitted row is stated here, never silent.
	Notes []string
}

// Table is a header and its rows, in the order they will print.
type Table struct {
	// Headers names each column. It may be empty — a table of bare rows is
	// still a table — and it may be shorter than a row is wide, which is what
	// Columns exists to survive.
	Headers []string
	// Rows are the records, in the order the run established them. Nothing
	// here is sorted, shortened or padded: order is a fact about the run, and
	// the other three are decisions a writer makes.
	Rows []Row
}

// Columns is how many columns the table has, taken over the header AND every
// row rather than from the header alone, so a row carrying an extra cell is
// printed rather than silently dropped.
//
// The difference is not academic. A count read off the headers answers 2 for a
// table whose rows carry three cells, and the third cell then falls off the
// right-hand edge of every renderer at once — with no golden file to show it,
// because the value was never rendered anywhere to be compared. Taking the
// widest thing the table holds makes the extra cell visible instead.
//
// It lives on the model rather than in a renderer for the same reason: the two
// syntaxes must not be able to disagree about how many columns a table has.
func (t Table) Columns() int {
	count := len(t.Headers)
	for _, r := range t.Rows {
		count = max(count, len(r.Cells))
	}
	return count
}

// Row is one record: its cells, and the explanation that belongs to it.
type Row struct {
	// Cells are the record's values, one per column, in header order. A row
	// may hold fewer than the headers name — a value the run never
	// established — and it may hold more; Columns is what keeps the surplus
	// from being dropped.
	Cells []string
	// Detail is the row's reason, IN FULL. A writer with a width budget cuts
	// its own copy; a writer without one prints all of it.
	//
	// Empty means this row has nothing of its own to add, which is a decision
	// about content and not a sign that the report held no reason: a result
	// whose reason repeats its plan entry leaves this empty so the sentence is
	// printed once, while the report itself still carries the whole string.
	// Every writer omits an empty detail rather than printing a bare indent.
	Detail string
}
