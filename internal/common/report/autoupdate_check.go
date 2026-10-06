package report

import (
	"fmt"
	"strings"
)

// Sections is the autoupdate check's report as structure: what it says, in
// order, with every value at full length and not one decision about how it will
// look. It is what makes AutoupdateCheck satisfy Payload.
//
// The knowledge of what a package, a validation gate or --all means lives here,
// on the payload, rather than in a renderer: a renderer that knew it would have
// to be edited for every new command, while a payload that describes itself as
// ordered blocks lets a new command be a new payload. Every syntax reuses these
// sections, so which packages are up to date or how a skipped entry is labelled
// is derived once. Nothing is shortened or decorated here: width, escapes and
// borders are a writer's, so an export with no width budget prints every reason
// in full.
//
// Only SectionOptions reaches this method, never a Width, so a screen setting
// cannot leak onto the export path. SkipPlan drops the plan section and nothing
// else; dropping it here, not in a writer, lets every screen mode inherit the
// omission so the heading never appears twice. A run that stopped early is
// labelled by Run.Sections before it delegates here, because "the run reached
// the end of its plan" is true of any batch, not of this payload.
func (r AutoupdateCheck) Sections(opts SectionOptions) []Section {
	blocks := []Section{versionCheckSection(r, opts.ShowAll)}

	// A run that planned nothing has nothing to say about a plan, its results
	// or its tally, and says so by omitting all three rather than by stating
	// each one's emptiness in a sentence of its own.
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

// versionCheckSection is what the scan found, one row per package.
//
// The rows a run produces depend on listEvery and the counts never do: a
// package left out of the list is still counted in the note below it, so the
// short list is a stated omission rather than a silent one.
//
// listEvery is the screen's --all and the export's only setting: an export
// lists every package it looked at whatever the terminal was asked for.
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
			// Counted below, not listed here.
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
			// The list is IN ADDITION TO the count, never instead of it.
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

	// The source/bin tally the check path printed as its closing line, said
	// INSIDE the section because it is a count over the very packages
	// this section is about, taken from the TYPE column already beside every
	// row. It is appended LAST for the reason the old line was printed last: the
	// notes above qualify the rows, and this one qualifies the whole scan.
	//
	// It is stated whichever way listEvery went, because it is a count and a
	// count never depends on the listing — the same rule the up-to-date
	// note above follows, and the reason every golden gains this line.
	s.Notes = append(s.Notes, tierNote(tierCounts(r.Scanned)))

	return s
}

// validationPlanSection is the cost of the run, on screen before any of it is
// spent: which packages, how far each will be taken, and the case for it.
//
// Every entry states its reason, shortened by the writer to whatever the line
// budget allows. This is the ONE place the reason of a package whose result
// merely repeats it is stated at all — validationResultsSection prints no second
// copy — so a reason dropped here would not be shortened, it would be gone.
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
// A row whose reason repeats its plan entry word for word prints no reason: the
// plan section a few lines up already said it, and printing it twice cost ~230
// characters per skipped package. A row whose reason DIFFERS is printed, because
// a changed reason (the plan asked for a manifest, the host could not produce
// one) appears nowhere else in the report.
//
// Whether the two match is ValidationRow.SameReasonAsPlan, decided by the run
// where both strings are in hand; comparing them again here would be a second
// derivation that could disagree with the model. Which reason to print is
// settled here so every syntax inherits it; nothing is removed from the report,
// so an export with no width budget still carries the full reason.
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
// plan already printed that same sentence.
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
// Inconclusive and skipped are separate counts and must never be added together
// again: folded into one "not validated" column, a package the toolkit COULD NOT
// evaluate looked like one the operator told it to leave alone, letting a
// toolkit defect hide behind the operator's own policy.
//
// The denominator is the tally, not the plan. Total() counts what actually
// landed in a column: for a complete run it equals len(Plan) (Reconciles() says
// so), and for a run stopped part way it is the honest smaller number, which
// leaves the incomplete-run label (Run.Complete, Run.NotEvaluated) to say why.
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
// package changes the list and may never change the number underneath it.
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
// somewhere that claims nothing about it.
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
// A count must not depend on which rows were listed, and taking it here
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

// tierNote is that tally as the sentence the report states: the words
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
