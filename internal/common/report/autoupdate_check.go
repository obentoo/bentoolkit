package report

import (
	"fmt"
	"strings"
)

// Sections is the autoupdate check's report as structure: what it says, in
// order, with every value at full length and not one decision about how it will
// look.
//
// # It is a METHOD, and that is the whole design
//
// Everything in this file used to live in internal/common/report/render, where
// all four renderers called it. It knows what a package is, what a validation
// gate is, what an orphaned ebuild means and what --all is for — and a renderer
// that can reach that knowledge is a renderer every new command has to be
// EDITED into, rather than one a new file can simply use. That is the defect
// story 046 removes.
//
// So the knowledge moves to the thing that holds it. A payload describes itself
// as ordered blocks; a renderer lays blocks out. Neither needs to know the
// other's vocabulary, which is what makes a fourth command a new payload rather
// than a fifth branch in a renderer (D1).
//
// This method is what makes AutoupdateCheck satisfy Payload.
//
// # It is the half every syntax reuses
//
// Plain and Markdown differ by table syntax and nothing else, and this is the
// line that fact is drawn on: everything that decides WHAT a report says is
// settled once, here, and everything that decides how it LOOKS lives in a
// writer. A second syntax prints these same sections with pipes; it does not
// re-derive which packages are up to date, or how a skipped plan entry is
// labelled, because a second derivation is a second thing to keep in agreement.
//
// # Nothing here is shortened, and nothing here is decorated
//
// Every string a section carries is this report's own, in full. Width is
// applied by the writer, which is what lets a syntax with no width budget print
// the whole reason and keeps the model's promise that shortening is a rendering
// decision rather than a loss of data (R7.4). No escape sequence, no column, no
// border is decided here — the package's own import guard and field guard both
// depend on that staying true.
//
// # SectionOptions, not render.Options
//
// ShowAll and SkipPlan answer what the report should SAY; Width answers what the
// DEVICE allows. Only the first pair reaches this method, and that separation is
// what keeps a screen setting off the export path: there is no Width field here
// for an export to be handed by accident (S044-R9.3).
//
// # The plan is the one section a caller may drop
//
// SkipPlan omits it, and only it — every other section is built exactly as it
// would have been, so the option removes a block rather than reshaping a report
// (R2.3). It is dropped HERE rather than in a writer because a section a report
// does not state is a fact about WHAT it says: the three screen modes inherit
// the omission from this one line instead of each deciding it, which is what
// keeps the heading from appearing twice in one of them (R2.2).
//
// # A run that stopped early says so, and it is not this method that says it
//
// The "Run Interrupted" label used to be built here, off two fields this type
// carried. Those fields are the ENVELOPE's now — "the run reached the end of its
// plan" is true of any batch, so restating it per payload would be one wording
// per domain to keep in agreement (D1) — and Run.Sections prepends the label
// before delegating here. Every syntax still inherits it from one sentence
// (R4.3), one level up.
func (r AutoupdateCheck) Sections(opts SectionOptions) []Section {
	blocks := []Section{versionCheckSection(r, opts.ShowAll)}

	// A run that planned nothing has nothing to say about a plan, its results
	// or its tally, and says so by omitting all three rather than by stating
	// each one's emptiness in a sentence of its own (S045-R4.1).
	//
	// The trigger is the PLAN, not the results. A run whose every package was
	// excluded by policy planned them all and evaluated none, and there the
	// three sections are exactly what the operator needs — the plan names what
	// was excluded and why. Keying on len(Results) would silence that run too.
	if len(r.Plan) == 0 {
		return blocks
	}

	if !opts.SkipPlan {
		blocks = append(blocks, validationPlanSection(r))
	}

	return append(blocks,
		validationResultsSection(r),
		validationSummarySection(r),
	)
}

// versionCheckSection is what the scan found, one row per package (R8.2, R8.3).
//
// The rows a run produces depend on listEvery and the counts never do: a
// package left out of the list is still counted in the note below it, so the
// short list is a stated omission rather than a silent one (S044-R2.5).
//
// listEvery is the screen's --all (R8.2, R8.3) and the export's only setting:
// an export lists every package it looked at whatever the terminal was asked
// for (S044-R9.3).
func versionCheckSection(r AutoupdateCheck, listEvery bool) Section {
	s := Section{Title: "Version Check Results"}

	if len(r.Scanned) == 0 {
		s.Lead = []string{"No package is configured for autoupdate."}
		return s
	}

	found := scanCounts(r.Scanned)
	s.Lead = []string{fmt.Sprintf("%d package(s) checked, %d with a pending update.", len(r.Scanned), found[conditionUpdate])}

	s.Rows.Headers = []string{"PACKAGE", "TYPE", "CURRENT", "CANDIDATE", "STATE"}
	for _, scanned := range r.Scanned {
		if conditionOf(scanned) == conditionUpToDate && !listEvery {
			// R8.3: counted below, not listed here.
			continue
		}
		state, detail := scanState(scanned)
		detail = withRequirementLines(detail, scanned.Requirements)
		s.Rows.Rows = append(s.Rows.Rows, Row{
			Cells:  []string{scanned.Package, scanned.Type, scanned.CurrentVersion, scanned.CandidateVersion, state},
			Detail: detail,
		})
	}

	if found[conditionUpToDate] > 0 {
		if listEvery {
			// R8.2: the list is IN ADDITION TO the count, never instead of it.
			// A reader who scrolled past the rows still gets the number, and the
			// two goldens differ in both places rather than only in one.
			s.Notes = append(s.Notes, fmt.Sprintf("%d package(s) are up to date and are listed above.", found[conditionUpToDate]))
		} else {
			s.Notes = append(s.Notes, fmt.Sprintf("%d package(s) are up to date and are not listed; pass --all to list them.", found[conditionUpToDate]))
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
			s.Notes = append(s.Notes, fmt.Sprintf(stated.text, found[stated.when]))
		}
	}

	// R5.1: the source/bin tally the check path printed as its closing line,
	// said INSIDE the section (D5) because it is a count over the very packages
	// this section is about, taken from the TYPE column already beside every
	// row. It is appended LAST for the reason the old line was printed last: the
	// notes above qualify the rows, and this one qualifies the whole scan.
	//
	// It is stated whichever way listEvery went, because it is a count and a
	// count never depends on the listing (R8.3) — the same rule the up-to-date
	// note above follows, and the reason every golden gains this line.
	s.Notes = append(s.Notes, tierNote(tierCounts(r.Scanned)))

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
func validationPlanSection(r AutoupdateCheck) Section {
	s := Section{Title: "Validation Plan"}

	if len(r.Plan) == 0 {
		s.Lead = []string{"No pending update to validate."}
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
	s.Lead = []string{lead + "."}

	s.Rows.Headers = []string{"PACKAGE", "BUMP", "CLASS", "DEPTH"}
	for _, entry := range r.Plan {
		depth := entry.Depth
		if entry.Skipped {
			// A package no gate will run for says so on its own row. An operator
			// reads a shorter result list as progress unless the plan already
			// told them it would be shorter.
			depth += " [not validated]"
		}
		s.Rows.Rows = append(s.Rows.Rows, Row{
			Cells:  []string{entry.Package, bump(entry.CurrentVersion, entry.CandidateVersion), entry.Class, depth},
			Detail: entry.Reason,
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
func validationResultsSection(r AutoupdateCheck) Section {
	s := Section{Title: "Validation Results"}

	if len(r.Results) == 0 {
		s.Lead = []string{"No package was evaluated."}
		return s
	}

	s.Rows.Headers = []string{"PACKAGE", "CANDIDATE", "DEPTH", "OUTCOME"}
	for _, result := range r.Results {
		s.Rows.Rows = append(s.Rows.Rows, Row{
			Cells:  []string{result.Package, result.CandidateVersion, result.Depth, string(result.Outcome)},
			Detail: resultDetail(result),
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
func resultDetail(result ValidationRow) string {
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
// (Run.Complete, Run.NotEvaluated) to be added without this sentence
// having to be rewritten to stop lying.
func validationSummarySection(r AutoupdateCheck) Section {
	counted := r.Tally

	return Section{
		Title: "Validation Summary",
		Lead: []string{
			fmt.Sprintf("%d package(s) evaluated: %d proved, %d errored, %d inconclusive, %d skipped.",
				counted.Total(), counted.Proved, counted.Errored, counted.Inconclusive, counted.Skipped),
		},
		Notes: []string{
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
func conditionOf(result PackageResult) condition {
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
// because a count must not depend on which rows were listed: listing every
// package changes the list and may never change the number underneath it
// (R8.2, R8.3).
func scanCounts(scanned []PackageResult) map[condition]int {
	found := make(map[condition]int, len(scanned))
	for _, result := range scanned {
		found[conditionOf(result)]++
	}
	return found
}

// tierTally is how a scan's packages divided between the two tiers, and how
// many landed in neither.
//
// unresolved is a THIRD number rather than a remainder folded into one of the
// other two. PackageResult.Type is documented as meaningful when EMPTY —
// nobody resolved it — so a package that carries no tier has to be counted
// somewhere that claims nothing about it (R5.1).
type tierTally struct {
	// source and bin are the two spellings the producer emits.
	source int
	bin    int
	// unresolved is everything else: the empty Type of a package whose current
	// ebuild could not be read, and any spelling this renderer does not know.
	unresolved int
}

// tierCounts divides the scanned packages between "source", "bin" and neither.
//
// # Exactly two spellings are recognised, and nothing is guessed
//
// The two literals are the producer's own (Checker.resolveType answers with one
// of them, and an operator may set either in packages.toml), and this reproduces
// the check path's tally switch verbatim so the sentence survives that path's
// deletion unchanged. Anything else — the empty string, or a typo that reached
// the registry — is counted as unresolved: defaulting it to "source" would make
// an unreadable ebuild indistinguishable from a source package, which is the one
// thing the model's doc comment says may not be assumed.
//
// # It is its own pass, like scanCounts
//
// A count must not depend on which rows were listed (R8.3), and taking it here
// rather than inside the row loop is what makes that true by construction
// instead of by a branch nobody may later move.
func tierCounts(scanned []PackageResult) tierTally {
	var found tierTally
	for _, result := range scanned {
		switch result.Type {
		case "source":
			found.source++
		case "bin":
			found.bin++
		default:
			found.unresolved++
		}
	}
	return found
}

// tierNote is that tally as the sentence the report states (R5.1): the words
// the check path this report replaces ended on, "Checked N source, M bin".
//
// The wording is separated from the counting for the reason scanState is
// separated from scanCounts: what the report SAYS about a number and how the
// number was arrived at are two things, and only one of them changes when the
// sentence is reworded.
//
// # The gap is named, never closed
//
// When some package resolved to neither tier the two figures do not add up to
// the count in the lead, and the sentence says so instead of leaving the reader
// to subtract. It says it WITHOUT naming a tier, which is the whole point:
// nobody resolved those packages, so putting them in either column — or quietly
// defaulting them to source, as the producer's own fallback does — would state
// a fact the scan never established.
func tierNote(counted tierTally) string {
	note := fmt.Sprintf("Checked %d source, %d bin", counted.source, counted.bin)
	if counted.unresolved > 0 {
		note += fmt.Sprintf("; %d package(s) resolved to neither tier", counted.unresolved)
	}
	return note + "."
}

// scanState names a package's condition for the STATE column and returns the
// sentence that explains it, where it needs one.
//
// Only the words are decided here; which condition applies was decided once, by
// conditionOf.
func scanState(result PackageResult) (state, detail string) {
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

// withRequirementLines appends one line per requirement to a row's detail,
// worded by its state, so a bump waiting on another package says so under its
// own row before anyone tries to apply it.
func withRequirementLines(detail string, reqs []Requirement) string {
	lines := make([]string, 0, len(reqs)+1)
	if detail != "" {
		lines = append(lines, detail)
	}
	for _, r := range reqs {
		switch r.State {
		case "missing":
			lines = append(lines, fmt.Sprintf("waits for %s-%s (not detected)", r.Package, r.Version))
		default:
			lines = append(lines, fmt.Sprintf("requires %s-%s (%s)", r.Package, r.Version, r.State))
		}
	}
	return strings.Join(lines, "\n")
}

// cacheTag marks an answer that came from cache rather than from upstream, which
// is why a run may not reflect a bump made minutes ago.
//
// It is appended to whichever condition applies instead of being a condition of
// its own, because FromCache is orthogonal to the four: it qualifies where the
// answer came from, not what the answer was.
func cacheTag(result PackageResult) string {
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
