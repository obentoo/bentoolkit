package main

// The seam between `overlay compare` and the report it now ends in.
//
// Story 047, sub-task 3.1 — S047-R1.1, S047-R1.3.
//
// internal/common/report must not import internal/overlay — `var
// forbiddenImports` in internal/common/report/boundary_test.go lists it, and
// `func TestPackageImportsNoPresentation` fails the suite for a single such
// import — and internal/overlay must not know what a report is. So the
// conversion from one to the other happens here and nowhere else (S047-D1).
//
// It is the same seam story 044 established at
// `func buildReport` in overlay_autoupdate_report.go and story 046 replicated
// at `func buildManifestReport` in overlay_manifest_report.go, and
// "replicated" is the word: every decision below has a counterpart in that
// second file, made for a reason that has not changed just because the domain
// did.
//
// # This command is the widest translation of the five
//
// A manifest target crosses as three fields. A compared package crosses as
// seven, four of which are ENUMS at the producer and words here. Two of the
// four carry a String() of their own in internal/overlay/compare.go — `type
// CompareStatus`, `type Verdict` — while `type Verification`, `type
// Authorship` and `type Reading` deliberately do not. That asymmetry is a
// decision rather than an omission, and the two functions that spell those
// words below say why at the point they spell them.

import (
	"fmt"
	"slices"
	"strings"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/logger"
	"github.com/obentoo/bentoolkit/internal/common/report"
	"github.com/obentoo/bentoolkit/internal/common/report/render"
	"github.com/obentoo/bentoolkit/internal/overlay"
)

// compareEnvelope puts one comparison's facts inside the envelope every
// exported document carries: the schema version, the kind of run that produced
// it, the reader's label for it, and how much of what it set out to establish
// it never reached (S047-R1.1).
//
// # It is the ONE place this command builds a report.Run
//
// The two values it stamps unconditionally are fixed per COMMAND rather than
// per run, and that is what makes `.kind == "overlay.compare"` a filter rather
// than a guess: the kind is derived from nothing this run establishes — not the
// repository, not how many packages matched, not whether anything was found
// redundant. A kind that varied with what a run found would answer one string
// for an empty run and another for a full one, so a consumer filtering on it
// would receive some of this command's documents and silently miss the rest.
//
// The schema is stamped from report.SchemaVersion for the reason the four
// envelopes before it give: a 2 typed at each producer is the same defect as a
// column width typed into a format string — several places to find on the day
// it changes, and nothing that fails when one is missed.
//
// # Complete and NotEvaluated are ARGUMENTS, never read off the payload
//
// Deriving them here — comparing the lists against the tally, or asking whether
// any row carries a failed reading — is a different rule wearing the same
// answer, and it is wrong in both directions. A run narrowed by
// `--only-redundant` would come out "incomplete" because most of its rows are
// missing, when it evaluated every one of them and the operator merely chose
// not to look (S047-R5.3); and an interrupted run whose last package happened
// to complete would come out complete. Completeness is established by the run
// that did the work, and it travels from there to here.
//
// The doc on `CompareReport.Interrupted` in internal/overlay/compare.go states
// the same rule from the producer's side, in the same words the manifest
// envelope uses: Complete is !Interrupted, and NEVER NotEvaluated == 0.
func compareEnvelope(payload report.CompareRun, complete bool, notEvaluated int) report.Run {
	return report.Run{
		Schema: report.SchemaVersion,
		Kind:   report.KindOverlayCompare,
		// A fixed label, exactly like the kind: it names the command that
		// produced the document in a reader's own words and is derived from
		// nothing the run established. report.Run.Title is documented as a label
		// rather than as something to match on, and Kind is what a consumer
		// discriminates with.
		Title:        "Overlay comparison",
		Complete:     complete,
		NotEvaluated: notEvaluated,
		Payload:      payload,
	}
}

// buildCompareReport turns what a comparison run returned into the run it
// reports, whole, before anything is printed (S047-R1.1, S047-R1.3).
//
// # It is a TRANSLATION and nothing else
//
// No package is filtered, no order is changed and no outcome is decided here.
// The order is the order `func CompareWithProvider` in
// internal/overlay/compare.go sorted the results into — category, then package
// — and it is a fact about the run rather than a presentation choice; sorting
// here would be this file deciding something the producer already decided.
//
// # It is called AFTER the view is narrowed
//
// `func filterCompareResults` in overlay_compare.go replaces
// `CompareReport.Results` in place, with only the rows `--only-redundant` and
// `--only-patched` asked to see, and the call site runs this function after it.
// The payload's LISTS therefore follow what the operator asked to see, while
// every count stays the producer's: each one below is read from a field
// `func CompareWithProvider` in internal/overlay/compare.go maintained, and
// never from len(Results). So a narrowed document states the whole overlay's
// tally beside the rows it shows, and still reports Complete — a filter is a
// choice the operator made, not a shortfall the run suffered (S047-R5.3).
//
// Building later loses nothing a reader had: `ComparePkg.Reason` is mapped by
// atom out of `CompareReport.Findings`, and `func EstablishFindings` in
// internal/overlay/compare.go fixes that slice onto the report before the
// filter runs, so every surviving row still finds the finding that explains it.
//
// # unfiltered is the WHOLE run's rows, and it is a parameter for one reason
//
// Being called after the narrowing costs this function the only population it
// cannot reconstruct: the rows the filter removed. Every other count is a field
// the producer maintained and survives untouched, but the two answers taken by
// walking the rows — `CompareRun.Unread` and the shortfall
// `func compareNotEvaluated` counts — would be taken over the narrowed view and
// would then shrink because the operator chose to look at less. A
// `--only-patched` run whose removed rows carried a failed reading would state
// a smaller gap than the same run unfiltered, and a filter would have made a
// holed run look whole — the one direction S047-R5.2 exists to forbid.
//
// So the caller keeps the slice before `func filterCompareResults` in
// overlay_compare.go replaces it, and hands it over here: the LISTS follow
// rep.Results and show what was asked for, the COUNTS follow this slice and
// state what the run did.
//
// It is a required parameter rather than an optional one because omitting it is
// exactly the defect above, and an omission that compiles is an omission that
// ships. A nil says something different and true: this report was never
// narrowed, so its own rows are the whole run.
//
// # The counters are taken from the producer, never subtracted from each other
//
// `CompareRun.Scanned`, `InBoth` and `OnlyLocal` each come from a counter
// `CompareWithProvider` maintained. A subtraction taken here would invent an
// answer the run never gave and print it beside two the run did — the rule
// CompareRun's own field docs state at length — and the three would part
// company the first time a status is added.
//
// # A nil report is reported as an empty run, not as a crash
//
// The signature takes a pointer for the reason `func buildManifestReport` does:
// the caller is holding one, `CompareWithProvider` returns one, and copying a
// struct that carries five slices per result to pass it by value would be a
// copy nothing needs. A report is the thing an operator gets INSTEAD of a
// crash, so building one must not be the moment the crash arrives.
func buildCompareReport(rep *overlay.CompareReport, repository string, unfiltered []overlay.CompareResult, notes ...string) report.Run {
	payload := report.CompareRun{
		Repository:  repository,
		Redundant:   []report.ComparePkg{},
		NeedsRebase: []report.ComparePkg{},
		Keep:        []report.ComparePkg{},
		Unknown:     []report.ComparePkg{},
		KeepGroups:  []report.KeepGroup{},
		// Always a slice, never nil, for the reason every list above is:
		// `func JSON` in internal/common/report/render/json.go marshals what it
		// is given and normalises nothing, and null and [] say different
		// things in the document a consumer holds.
		Notes: append([]string{}, notes...),
	}
	if rep == nil {
		// No run to ask, so no gap to state: the empty report of a run that
		// established nothing, rather than one that claims to have been cut
		// short. Every slice is already non-nil above, so this document and a
		// full one differ in their rows and not in the shape of their keys.
		return compareEnvelope(payload, true, 0)
	}

	payload.Scanned = rep.TotalPackages
	// InBoth is the three statuses that REQUIRE a remote version to have been
	// read — outdated, newer, up-to-date — summed from the producer's own
	// counters. It is deliberately not TotalPackages minus NotInRemoteCount: a
	// package whose lookup errored is one the run does not know the remote
	// carries, and a subtraction would file it under "in both" on no evidence.
	payload.InBoth = rep.OutdatedCount + rep.NewerCount + rep.UpToDateCount
	payload.OnlyLocal = rep.NotInRemoteCount
	payload.Verdicts = report.VerdictTally{
		Keep:        rep.VerdictKeepCount,
		Redundant:   rep.VerdictRedundantCount,
		NeedsRebase: rep.VerdictNeedsRebaseCount,
		Unknown:     rep.VerdictUnknownCount,
	}

	// The whole run's rows, whatever the operator asked to see. A caller that
	// never narrowed anything passes nil, and the report's own rows are then
	// already the whole run.
	whole := unfiltered
	if whole == nil {
		whole = rep.Results
	}

	// Both halves of the split are carried: the first finding becomes the row's
	// Reason and every one after it travels on the entry itself. The walk is
	// over rep.Results — the VIEW — so a package a filter removed takes its
	// further findings with it, which is the same narrowing the rows follow and
	// the opposite of what the counts do: a sentence about a package is read
	// beside that package's row, and a row `--only-redundant` removed has none.
	reasons, extra := comparePackageFindings(rep.Findings)
	lookupReasons := compareLookupReasons(rep.Findings, rep.Results)
	for _, result := range rep.Results {
		pkg := comparePkgFacts(result, reasons, extra)
		if pkg.Reason == "" {
			// A failed lookup has no finding of its own, so its reason is the
			// comparison's sentence, which names the cause (S057-R4.1).
			pkg.Reason = compareOneLine(lookupReasons[pkg.Package])
		}
		switch result.Verdict {
		case overlay.VerdictRedundant:
			payload.Redundant = append(payload.Redundant, pkg)
		case overlay.VerdictNeedsRebase:
			payload.NeedsRebase = append(payload.NeedsRebase, pkg)
		case overlay.VerdictKeep:
			payload.Keep = append(payload.Keep, pkg)
		default:
			// VerdictUnknown, and any verdict added later without a case here.
			// The fail-safe direction is the one that recommends nothing: a
			// package this file has no list for must not silently join the list
			// that recommends deleting it.
			payload.Unknown = append(payload.Unknown, pkg)
		}
	}

	// Unread counts every comparison nobody read, which is every reading state
	// except ReadingDone: a review nobody requested, one the content check made
	// impossible, and one that was attempted and failed all leave an operator
	// with a recommendation and no evidence behind it (S047-R3.1).
	//
	// It is walked over the WHOLE run rather than over the rows above, because
	// it is a count and it sits in the payload beside counts the producer
	// maintained over every package. One of the two shrinking under
	// `--only-patched` while the others did not would put two populations in one
	// section under one heading (S047-R5.2).
	//
	// ReadingFailures splits the failed readings among them by cause, in the
	// same walk, so the two are one population by construction (S057-R4.5).
	failures := map[string]int{}
	for _, result := range whole {
		if result.Reading != overlay.ReadingDone {
			payload.Unread++
		}
		if result.Reading == overlay.ReadingFailed {
			failures[compareCauseWord(result)]++
		}
	}
	payload.ReadingFailures = compareCauseCounts(failures)

	// The call sub-task 2.3 left owing. GroupKeep is a package-level function
	// rather than a method that fills this field, so the assignment is written
	// where the payload is built and is visible here: a method would make a
	// correctly built payload depend on somebody having remembered to call it,
	// and an empty KeepGroups would then mean either "no version pair repeats"
	// or "nobody ran the rule" (S047-D2).
	//
	// It runs on the FULL Keep list, after it is built, because a group is a
	// fact the run established and travels as a field — a grouping performed
	// while rows are drawn would exist on a terminal and nowhere in the exported
	// file (S047-R2.1).
	payload.KeepGroups = report.GroupKeep(payload.Keep)

	// Complete and NotEvaluated, as S047-R5.1 and S047-R5.2 phrase them. A run
	// is complete when it was not cut short AND left nothing it set out to
	// establish unestablished; NotEvaluated is how many of those there were.
	//
	// The two are one answer read twice, which is why they are computed
	// together: a document stating Complete true beside a NotEvaluated of three
	// would give a machine reader two contradictory answers to one question.
	// Interruption stays a recorded FACT rather than a count — a run cut short
	// after its last package has a gap of zero and was still cut short — so it
	// is OR-ed in and never inferred (S047-R5.1).
	notEvaluated := compareNotEvaluated(rep, whole)
	return compareEnvelope(payload, !rep.Interrupted && notEvaluated == 0, notEvaluated)
}

// compareNotEvaluated counts the facts this run set out to establish and did
// not (S047-R5.2).
//
// # Two populations, and they cannot overlap
//
// A package goes unestablished in two different ways, and the run records the
// two in different places.
//
// The first is a package the run never REACHED. The doc on
// `CompareReport.Interrupted` states that gap as TotalPackages minus
// ComparedPackages exactly: ComparedPackages++ runs once per result arriving at
// the collector, above the switch that splits results by status and outside the
// include filter, so a package missing from that number is a package whose
// worker never ran. The second is a package the run did reach and could not
// finish: a review that was killed, a version pair the content check refused, a
// lookup that errored (S047-R4.3).
//
// The two cannot double-count one package, because a package in the first
// population has no ROW. Results is appended under the same lock that
// increments ComparedPackages, so a worker that never ran left nothing for the
// loop below to look at. They are therefore added, not reconciled.
//
// The subtraction is clamped at zero. A CompareReport assembled by hand — in a
// test, or by a caller building one — may carry results without carrying the
// tally, and a negative gap would subtract from the second population and
// report a holed run as whole.
//
// # The second population is counted per PACKAGE, and that is what removes the
// double count
//
// `CompareReport.ErrorCount` is a field, so reading it looks like the better
// source than a walk. It is not, because it is not disjoint from the reading
// states: `func noteContentRefusal` in internal/overlay/compare.go writes
// ReadingNotComparable onto EVERY result whose Verified is NotVerified — in the
// comparison itself, so the count below does not depend on whether a reviewer
// existed — and a package whose lookup errored was never content-verified, so
// it carries that state as well. ErrorCount plus a tally of the reading states would report one
// failed package as two unestablished facts, and the number a machine reads
// would exceed the number of packages that produced it.
//
// So the loop asks each row a single question and increments at most once: this
// package's outcome was not established, however many ways it went wrong.
//
// # ReadingNotRequested is excluded, and that exclusion IS S047-R5.3
//
// `--no-review` leaves every row at that zero value. Counting it would report a
// run the operator deliberately narrowed as a run with a hole in it, and a
// choice is not a shortfall. This is why the set here is NOT the set
// `CompareRun.Unread` counts: Unread is every state but ReadingDone, because it
// answers "did anybody read this difference", and "nobody was asked to" is a
// true answer to that question and a false one to this one.
//
// # The rows are an ARGUMENT, so the count follows the run and not the view
//
// The call site narrows `CompareReport.Results` before this runs, so reading
// rep.Results here would count only the rows the operator asked to see: a
// `--only-patched` run would lose the shortfall of every row the filter removed
// and report itself closer to complete than it was. A choice about what to LOOK
// AT must not change what the run says it ESTABLISHED (S047-R5.2, S047-R5.3),
// so the population to walk is passed in and the caller passes the whole run's
// rows.
//
// The report is still the first parameter because the other population — the
// packages never dispatched — is a pair of counters on it, and those are not
// narrowed by anything.
func compareNotEvaluated(rep *overlay.CompareReport, results []overlay.CompareResult) int {
	// Population one: dispatched never, so established nothing.
	unreached := rep.TotalPackages - rep.ComparedPackages
	if unreached < 0 {
		unreached = 0
	}

	// Population two: reached, and still short of an answer.
	unestablished := 0
	for _, result := range results {
		switch {
		case result.Status == overlay.StatusError:
			// The lookup itself did not come back, so nothing downstream of it
			// was established either.
			unestablished++
		case result.Reading == overlay.ReadingFailed,
			result.Reading == overlay.ReadingNotComparable:
			// A review that was attempted and did not return, and a pair of
			// versions the content check refused. Both leave a recommendation
			// standing on evidence nobody has (S047-R4.3).
			unestablished++
		}
	}

	return unreached + unestablished
}

// comparePkgFacts is one compared package as the model spells it.
//
// # The atom is joined here, once
//
// Category and Package are separate on the producer's struct because the
// filesystem is; they are one string on the model's because every reader prints
// them as one. Joining at this boundary is what stops each renderer from
// picking its own separator, and it is the same crossing
// `func manifestTargetFacts` makes.
//
// # Status crosses through the producer's own String(), and Reading does not
//
// `type CompareStatus` in internal/overlay/compare.go has a String() and it is
// used: "up-to-date", "outdated", "newer" are the words that command has
// printed since before this report existed, and re-spelling them here would be
// a second vocabulary free to drift from the one the library publishes.
// `type Reading` and `type Verification` have no String() BY DESIGN — a display
// word is report vocabulary, not library vocabulary — so the words for them are
// written below, in this package, where the payload that consumes them is
// documented (S047-D1).
//
// # Authorship does not cross at all
//
// It is a fact about WHO WROTE a difference, and the producer has already put
// it into the sentence this row carries as its Reason: `func compareFindings`
// spells the proved case as "proved ours — our ebuild references <file>, which
// ::gentoo does not ship". A second field here would be a second answer to one
// question, and the one that drifted would contradict the sentence beside it.
func comparePkgFacts(result overlay.CompareResult, reasons map[string]string, extra map[string][]string) report.ComparePkg {
	atom := result.Category + "/" + result.Package
	// Non-nil whatever the package has, and folded one by one: a further
	// finding is prose the block prints beside the table, and a raw newline in
	// it would corrupt the same two writers ComparePkg.Reason folds for.
	further := make([]string, 0, len(extra[atom]))
	for _, detail := range extra[atom] {
		further = append(further, compareOneLine(detail))
	}
	return report.ComparePkg{
		Package: atom,
		Local:   result.LocalVersion,
		Remote:  result.RemoteVersion,
		Status:  result.Status.String(),
		Reading: compareReadingWord(result.Reading),
		Diff:    compareDiffCell(result),
		Reason:  compareOneLine(reasons[atom]),

		FurtherFindings: further,

		Cause: compareCauseWord(result),
		Error: compareOneLine(result.FailureText),
	}
}

// compareLookupReasons returns, for every result whose upstream lookup failed,
// the comparison's own sentence about it. `func comparePackageFindings` skips
// FindingCompared, because on every other row that sentence only restates the
// version columns; on a failed lookup it is the only thing that says why
// (S057-R4.1), so it is kept here and for those rows alone.
func compareLookupReasons(findings []overlay.Finding, results []overlay.CompareResult) map[string]string {
	failed := make(map[string]bool)
	for _, result := range results {
		if result.Status == overlay.StatusError {
			failed[result.Category+"/"+result.Package] = true
		}
	}
	reasons := make(map[string]string, len(failed))
	for _, finding := range findings {
		if finding.Kind == overlay.FindingCompared && failed[finding.Atom] {
			reasons[finding.Atom] = finding.Detail
		}
	}
	return reasons
}

// compareCauseOrder is the review-cause vocabulary in its stated order, which
// breaks ties when the causes are counted (S057-R4.3). It is spelled here, as
// `func compareReadingWord` spells the reading words, and the report package
// holds the same list for the same reason.
var compareCauseOrder = []string{
	"timed out", "could not start", "exited non-zero", "empty or unusable reply",
	"cancelled", "ebuild unreadable", "other",
}

// compareCauseWord is the report's word for why this row failed (S057-R4.4):
// the lookup's cause on a row whose lookup failed, the review's cause on a row
// whose reading failed, and "" on a row with no failure. A failed reading with
// no recorded cause — a result built by hand — also reads "".
func compareCauseWord(result overlay.CompareResult) string {
	if result.Status == overlay.StatusError {
		switch result.LookupCause {
		case overlay.LookupRateLimited:
			return "rate-limited"
		case overlay.LookupAuth:
			return "auth"
		case overlay.LookupNetwork:
			return "network"
		case overlay.LookupNotFound:
			return "not found upstream"
		case overlay.LookupOther:
			return "other"
		}
		return ""
	}
	if result.Reading != overlay.ReadingFailed {
		return ""
	}
	switch result.ReviewFailure {
	case overlay.ReviewTimedOut:
		return "timed out"
	case overlay.ReviewCouldNotStart:
		return "could not start"
	case overlay.ReviewExitedNonZero:
		return "exited non-zero"
	case overlay.ReviewUnusableReply:
		return "empty or unusable reply"
	case overlay.ReviewCancelled:
		return "cancelled"
	case overlay.ReviewEbuildUnreadable:
		return "ebuild unreadable"
	case overlay.ReviewOther:
		return "other"
	}
	return ""
}

// compareCauseCounts turns a cause tally into report entries, highest count
// first and ties in compareCauseOrder. A cause outside the vocabulary sorts
// last. The result is never nil: a run with no failed review publishes [].
func compareCauseCounts(tally map[string]int) []report.CauseCount {
	rank := func(cause string) int {
		if i := slices.Index(compareCauseOrder, cause); i >= 0 {
			return i
		}
		return len(compareCauseOrder)
	}
	out := make([]report.CauseCount, 0, len(tally))
	for cause, count := range tally {
		if count > 0 {
			out = append(out, report.CauseCount{Cause: cause, Count: count})
		}
	}
	slices.SortFunc(out, func(a, b report.CauseCount) int {
		if a.Count != b.Count {
			return b.Count - a.Count
		}
		if d := rank(a.Cause) - rank(b.Cause); d != 0 {
			return d
		}
		return strings.Compare(a.Cause, b.Cause)
	})
	return out
}

// compareReadingWord is the report's word for whether anybody read this
// difference (S047-R3.1).
//
// # The words are written HERE because they are report vocabulary
//
// `type Reading` carries no String(), and that is deliberate: the four states
// are a fact the library establishes, while "not requested" is a word a reader
// meets in a document. A String() on the producer would publish this package's
// display choice as part of internal/overlay's API, where the next renderer
// would be free to use it and the one after free to change it.
//
// The four strings are `const readingNotRequested` and its three neighbours in
// internal/common/report/compare_run.go, which counts them to build the
// redundant section's lead. They are spelled in two packages and cannot be
// shared — the constants are unexported, and exporting them would publish a
// vocabulary a consumer would then be entitled to match on. A typo in either
// half shows up as a count of zero rather than as a failure, so
// `func TestBuildCompareReport` asserts the four words literally.
//
// # There is no blank cell
//
// A Reading value with no word — reachable only if a fifth state is added
// without a case here — reads as "not requested", which is the fail-safe
// direction the redundant section already applies to an unmapped word: it can
// cost a report a removal recommendation it could have made, and never earn one
// over evidence nobody has.
func compareReadingWord(reading overlay.Reading) string {
	switch reading {
	case overlay.ReadingNotComparable:
		return "not comparable"
	case overlay.ReadingFailed:
		return "failed"
	case overlay.ReadingDone:
		return "read"
	case overlay.ReadingNotRequested:
		return "not requested"
	default:
		return "not requested"
	}
}

// compareDiffCell is what the content check found, drawn from the closed
// vocabulary S047-R3.2 fixes: "+N/-M", "identical", "not compared",
// "unreadable".
//
// The vocabulary is closed so that "no difference was found" can never be read
// as "no comparison was made". Those two are the same blank cell in the output
// this payload replaces, and they are opposite answers: one says the overlay's
// copy is redundant, the other says nobody has established anything about it.
//
// # "unreadable" has no producer today, and that is a MEASUREMENT not an omission
//
// `func verifyAgainstLocalContent` in internal/overlay/compare.go returns the
// same zero contentCheck — NotVerified, no magnitude — for all four of its
// exits: the package not resolving on both sides, the two versions differing,
// our ebuild failing to read, and upstream's failing to read. The cause is
// collapsed at the producer, and `type Reading` cannot recover it either:
// ReadingNotComparable is written on Verified == NotVerified whatever the
// cause, and ReadingFailed is about a REVIEW that did not come back rather than
// about a file that would not open.
//
// So this function emits three of the four words. Printing "unreadable" for any
// state it can actually observe would state a cause the run never established,
// which is precisely what a closed vocabulary exists to prevent. The word stays
// in the payload's documented vocabulary for the producer that later splits
// NotVerified into its causes; nothing here or downstream changes when it does.
//
// # The magnitude is stated only where a comparison found one
//
// DiffAdded and DiffRemoved are meaningful ONLY at VerifiedDiffers and are zero
// otherwise, so they are read inside that arm and nowhere else. A "+0/-0" on a
// check that never ran would be indistinguishable from a byte-identical pair,
// which is the conflation the three other words exist to remove.
func compareDiffCell(result overlay.CompareResult) string {
	switch result.Verified {
	case overlay.VerifiedDiffers:
		return fmt.Sprintf("+%d/-%d", result.DiffAdded, result.DiffRemoved)
	case overlay.VerifiedIdentical:
		return "identical"
	case overlay.NotVerified:
		return "not compared"
	default:
		// A Verification value with no word — reachable only if a fourth state
		// is added without a case here. "not compared" is the fail-safe answer:
		// it withholds a recommendation rather than claiming evidence.
		return "not compared"
	}
}

// comparePackageFindings splits the per-package findings in two: the FIRST
// finding each package has of its own, which becomes its row's reason, and
// every further finding, which becomes a note naming that package
// (S047-R6.1).
//
// # One walk, so the two halves cannot disagree
//
// They are returned together rather than by two functions because they are one
// partition: the same three exclusions decide both, and a second walk applying
// them again would be free to drift, in the direction where a finding is either
// stated twice or stated nowhere.
//
// # The second half exists because a ROW can hold one reason
//
// `ComparePkg.Reason` is one line and one record per package, so a package with
// three findings had two of them dropped here — measured on this repo, 11 of the
// 12 finding kinds are per-atom, so that is the normal case rather than an edge
// one. A note has no such budget: notes are wrapped by the renderer rather than
// cut to a column, which is why S047-R6.1 puts an explanation that does not fit
// a cell there.
//
// # FindingCompared is skipped, and skipping it is the whole rule
//
// `func compareFindings` in internal/overlay/compare.go emits one
// FindingCompared per package — "::gentoo ships X and the overlay carries Y" —
// and then a second, louder entry for the packages that also carry a
// divergence, a stale declaration or a registry declaration. The row-level
// entry restates Status, which this row already carries as its own cell, so
// carrying it as a Reason would print the STATE column again under every line.
//
// It would also, measurably, empty the report of its groups: `func GroupKeep`
// refuses to absorb a package whose Reason is non-empty, because grouping
// compresses repetition and a finding is not repetition (S047-R2.2). With every
// package carrying a restated status, no version pair would ever collect two
// members and S047-R2.1 would produce nothing on any run.
//
// # FIRST, in the order the findings were established
//
// `func EstablishFindings` appends the run-level baseline finding, then the
// comparison's, then the baseline review's per-package ones. So the first
// non-FindingCompared entry for an atom is the comparison's own exception where
// there is one, and a baseline finding only where there is not — which is the
// priority a reader wants: what is wrong with this package outranks what a
// later pass measured about it.
//
// The test is on the KIND rather than on a list of the kinds that qualify. A
// list would be a hand-maintained registry of the sort story 047's own design
// counts four of, wrong the first time a producer adds a finding and forgets
// it; the complement is one entry and cannot go stale.
//
// A finding with an empty Atom is run-scoped — FindingBaselineSkipped is the
// one — and is skipped by BOTH halves rather than filed under an empty key: no
// package is named by the empty string, so an entry there could only ever be
// read by accident. `func compareRunNotes` is where those are picked up.
func comparePackageFindings(findings []overlay.Finding) (map[string]string, map[string][]string) {
	reasons := make(map[string]string, len(findings))
	extra := make(map[string][]string)
	for _, finding := range findings {
		if finding.Kind == overlay.FindingCompared || finding.Atom == "" {
			continue
		}
		// The declared reason travels as a TAIL on the producer's own sentence,
		// which is the shape it has always had: `patched — declared by <entry>` is
		// the finding, and what the entry claims the divergence does completes it.
		// Composed per finding rather than across them, so a report whose findings
		// arrive in another order still puts each reason on its own line.
		line := finding.Detail + compareDeclaredTail(finding)
		if _, seen := reasons[finding.Atom]; !seen {
			reasons[finding.Atom] = line
		} else {
			extra[finding.Atom] = append(extra[finding.Atom], line)
		}
		// A model's words follow the finding they are about, under their own leads.
		// Nil at every zero value, which is every run no review reached.
		extra[finding.Atom] = append(extra[finding.Atom], compareReviewLines(finding)...)
	}
	return reasons, extra
}

// compareEffectCap bounds a declared reason and a proposed declaration on the
// lines below.
//
// It mirrors internal/overlay's patchedReasonCap, which is unexported and stays
// that way: a width is THIS layer's business. The export carries both values at
// full length, and a value capped on its way into the report would reach the
// JSON truncated, where there is no width to respect and nothing to restore it
// from (S047-R5.2).
const compareEffectCap = 72

// compareCapped bounds s at compareEffectCap, counting RUNES and not bytes.
//
// The text is a maintainer's or a model's prose and reaches a JSON export as
// well as a terminal. A cut through the middle of a multi-byte rune would put
// invalid UTF-8 in both, which is why this does not reuse internal/overlay's
// byte-wise truncateString.
func compareCapped(s string) string {
	runes := []rune(s)
	if len(runes) <= compareEffectCap {
		return s
	}
	return string(runes[:compareEffectCap-1]) + "…"
}

// compareDeclaredTail renders the ": <reason>" tail of a declaration line, or
// "" when nobody stated one.
//
// It reads Effect and ONLY where the maintainer is the one speaking. A model's
// reading is printed under its own labelled lead by compareReviewLines,
// precisely so a guess is never mistaken for a commitment, and appending one
// here would undo that in the one place an operator is most likely to act on it.
//
// The empty case is reachable in production and is not defensive padding:
// S025-R1.3 rejects a whitespace-only reason at validation time, but
// LoadPackagesConfig never calls ValidatePackageConfig, so the compare path sees
// entries validation never judged. A dangling colon introducing nothing would
// read as a truncation bug rather than as the missing text it is.
func compareDeclaredTail(finding overlay.Finding) string {
	if finding.Effect.Source != overlay.EffectDeclared || finding.Effect.Text == "" {
		return ""
	}
	return ": " + compareCapped(finding.Effect.Text)
}

// compareOriginProse is the report's sentence for a model's classification, or
// "" for one that says nothing (S032-R5.2).
//
// It is spelled here rather than taken from ReviewOrigin.String(). Those four
// words — unknown, overlay, upstream, both — are the feature's wire and storage
// vocabulary: the review cache persists one and the CLI adapter decodes one from
// the model's reply, so changing them would change what every note already on
// disk means. "::gentoo" is what an operator calls upstream, and it appears in
// no stored file.
func compareOriginProse(o overlay.ReviewOrigin) string {
	switch o {
	case overlay.OriginOverlay:
		return "originates in the overlay"
	case overlay.OriginUpstream:
		return "originates in ::gentoo"
	case overlay.OriginBoth:
		return "originates on both sides"
	default:
		// OriginUnknown, and any value a later constant adds without a sentence
		// here. Both mean nothing was said, and a line about an origin nobody
		// defined is worse than no line at all.
		return ""
	}
}

// compareReviewLines is what a MODEL said about one divergence: which side it
// reads the difference as coming from and what it does (S032-R5.2, S032-R5.3),
// followed by the `patched` declaration it offers for a difference it read as
// ours (S032-R5.4).
//
// Every line SAYS WHOSE WORDS THESE ARE. Everything else the report prints is
// something this tool established by comparing two files; these are a guess, and
// EffectReviewed exists so a renderer can tell an operator which of the two they
// are reading. Dropping the distinction invites them to act on the guess.
//
// It writes NO FILE: R5.4 proposes and the operator applies. The overlay
// repository auto-commits within minutes, so a declaration this program wrote
// would be published before anyone could read it.
//
// Nothing here can change a verdict, a count or which table a package sits in
// (S032-R5.8): it turns one finished Finding into lines the report would
// otherwise not have printed.
func compareReviewLines(finding overlay.Finding) []string {
	// A note missing its classification or its summary has answered neither
	// S032-R5.2 nor S032-R5.3, and a finding-shaped line stating nothing is worse
	// than no line. The producer already refuses such a note; this refuses it
	// again, on the same terms, for a Finding that reached here by another route.
	if finding.Effect.Source != overlay.EffectReviewed || finding.Effect.Text == "" {
		return nil
	}
	prose := compareOriginProse(finding.Origin)
	if prose == "" {
		return nil
	}

	lines := []string{compareReviewReadingLead + prose + " — " + finding.Effect.Text}
	// R5.4 attaches a proposal to ONE classification. `both` is deliberately not
	// it: a copy that carries work of ours AND has fallen behind ::gentoo needs the
	// rebase first, and declaring `patched` on it would record the whole difference
	// as intentional, permanently suppressing the recommendation for the half that
	// is merely stale.
	//
	// The producer already refuses to carry a proposal for any other origin. This
	// refuses it AGAIN rather than trusting that, so a model that fills the field in
	// anyway cannot get it onto the line. An empty proposal is not printed either: a
	// lead introducing nothing would read as a truncation bug.
	if finding.Origin == overlay.OriginOverlay && finding.Proposal != "" {
		lines = append(lines, compareReviewProposalLead+compareCapped(finding.Proposal))
	}
	return lines
}

// The two leads a model's words travel under. They carry no glyph and no indent:
// what an operator sees in front of a further finding is decided by the renderer
// in internal/common/report, over the notes this file builds from the findings.
const (
	compareReviewReadingLead  = "model reading, not a finding of this report: "
	compareReviewProposalLead = "proposed declaration, nothing here writes it — apply it yourself: "
)

// compareOneLine collapses every run of whitespace in s to a single space.
//
// A Row.Detail may contain no newline: a raw one corrupts both the plain writer
// and the Markdown pipe table. `ComparePkg.Reason`'s own doc says the adapter
// folds it, and this is the adapter. strings.Fields splits on any whitespace
// and discards none of the text between, so the result holds every visible
// character the original did, in order — a fold of the layout rather than a
// loss of content, which is what keeps S047-R6.3's "every explanation in full"
// true of the exported document.
//
// It is spelled here rather than shared with `func foldToOneLine` in
// internal/common/report/manifest_run.go, which is unexported: exporting it
// would publish a helper as API to save one line.
func compareOneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// presentCompareReport puts the finished comparison in front of the operator:
// the terminal first, then the export (S047-R1.2, S047-R1.3).
//
// It is `func presentManifestReport` in overlay_manifest_report.go with nothing
// added and nothing taken away, and that sameness is the requirement rather than
// a coincidence: S047-R1.2 asks that this payload reach the operator through the
// same renderer the other four kinds use, with no renderer edited to accommodate
// it. Three steps — resolve the mode, build the sections once, export the run —
// and not one of them mentions a package, a verdict or a diff. If a fifth
// command ever needs a step this one does not have, that is the signal the
// envelope stopped fitting, and the place to answer it is the envelope.
//
// # It must be called AFTER the live progress region is down
//
// `overlay compare` drives a live region through CompareOptions.ProgressCallback
// while it walks the overlay, and this writes to the same stdout. Rendering
// while that region is still up would draw the report into a frame the UI then
// redraws over, so the caller's finishUI() — the one point in the compare run
// where the terminal has been handed back — is the earliest this function may be
// called. That ordering lives at the CALL SITE because only the call site holds
// the teardown, which is the same division `func presentManifestReport` states
// for its own producer. It is also the display half of S047-R7.1: the
// interactive and building work a run does finishes before any alternate screen
// is opened, and the report is drawn once the screen is closed again.
//
// # The terminal render happens first, and that ordering is S047-R1.3
//
// An export is a convenience; the report on the terminal is the answer. Writing
// the file first would let a bad path — a directory that does not exist, a
// read-only mount — cost the operator the comparison they waited on, which for
// this command is the most expensive report of the five to produce: it reads a
// remote tree and diffs ebuilds to get there. Rendering first makes that loss
// impossible rather than merely unlikely, and `func exportReport` in
// report_export.go returns nothing precisely so a failed export cannot reach
// into this run's exit status either.
//
// # The mode is resolved HERE, not at the top of the run
//
// `func reportModeOrPlain` in overlay_autoupdate_ui.go is a pure function of the
// flags, the configuration and the terminal, so asking at this point gives the
// same answer any other producer gets, and asking late costs nothing. It also
// cannot fail: an unusable BENTOO_UI or ui.mode is refused out loud, the report
// falls back to plain and the exit status does not move. Resolving at the top of
// the run instead would turn a typo in a shell profile into "your comparison did
// not run" — a comparison that already did all of its work and has an answer to
// give.
//
// # ShowAll is answered ONCE, and it is content rather than device
//
// report.SectionOptions says what the report should SAY; render.Options says
// what the device ALLOWS. --all is the first kind, so it is read here where the
// sections are built and nowhere downstream. render.Options is passed empty
// because its only field is Width and a Width of 0 means "ask the device": a
// number typed at a call site would be this command deciding how wide somebody
// else's terminal is. The export does not share that answer — `const
// unshortenedWidth` in overlay_autoupdate_ui.go renders the plain export at a
// budget no report can reach, so every explanation crosses in full whatever the
// screen was rendered at (S047-R6.3).
//
// # A render that fails is reported and does not stop the export
//
// The two are independent answers to the same report. A terminal that went away
// mid-write is no reason to also withhold the file, which may be the only copy
// of the comparison left.
//
// # What the run has to SAY is in the payload, and that is why this takes no notes
//
// It took a variadic []compareNote until sub-task 7.1, appended those to the
// sections built on the line below, and handed the bare run to the export. The
// two halves therefore disagreed by construction: `func renderExport` in
// overlay_autoupdate_ui.go re-derives its own blocks from run.Payload and never
// saw a note, so every run-level sentence and every extra per-package finding
// reached the terminal and no export, in any of the three formats — an export
// strictly LESS complete than the screen, which is the inverse of what
// S047-R1.3 and S047-R6.3 ask for.
//
// CompareRun.Notes and ComparePkg.FurtherFindings carry them now, so
// `func (r CompareRun) Sections` emits them and both paths read the same
// sentences from the same field. This function is back to the three steps
// `func presentManifestReport` takes, which is what S047-R1.2 asked of it.
func presentCompareReport(cfg *config.Config, run report.Run) {
	mode := reportModeOrPlain(cfg)
	content := report.SectionOptions{ShowAll: autoupdateAll}
	if err := renderCheckReportIn(mode, run.Sections(content), render.Options{}); err != nil {
		logger.Warn("the report could not be rendered: %v", err)
	}
	exportReport(run)
}

// compareRunNotes is what a comparison run has to say about ITSELF: the
// run-level facts the terminal output printed beside the table, which no row
// carries. They travel on CompareRun.Notes in
// internal/common/report/compare_run.go, so `func (r CompareRun) Sections`
// emits them and the terminal and every export read the same list (S047-R1.5,
// S047-R1.3).
//
// # Every one of them is here for the same structural reason
//
// A run-level summary names no package, so it cannot be a Finding — a
// package-scoped finding with a blank atom is a bug — and it therefore had no
// atom to travel on when the report moved off `func FormatReport`. That is why
// each of these is rebuilt from the report's own numbers rather than harvested:
// the numbers survived the move, the sentences did not. The three helpers below
// (`func compareClassificationNote`, `func compareRealignNote`,
// `func compareRemovalRecommended`) each state the condition they are silent
// under, because a note that fires always is as wrong as one that never fires.
//
// # The baseline fact is HARVESTED from the findings, not re-derived
//
// `func baselineRunFindings` in internal/overlay/annotate_baseline.go emits
// FindingBaselineSkipped with `CompareReport.BaselineSkipped` verbatim as its
// Detail — the producer has already turned the field into the sentence that
// names the tree it looked for and the marker it looked for in it. Reading the
// field here instead would be a second spelling of one fact, free to drift from
// the one the library publishes.
//
// It is RUN-scoped: its Atom is empty, and `func comparePackageFindings` skips exactly
// those, so no row carries it and it would be lost with nothing to say so. That
// skip and this harvest are two halves of one decision.
//
// # FindingRealignVerdict is NOT harvested here, and that is measured
//
// `func baselineResultsFindings` in internal/overlay/annotate_baseline.go writes
// it WITH an atom, so a realignment verdict is a fact about one package rather
// than about the run: `func comparePackageFindings` already files it as that package's
// Reason and the row carries it. Repeating it here would print every verdict
// twice, once against its package and once against the whole report.
//
// # The "no verdict at all" notice is derived from the flags, because nothing
// records it
//
// No finding is written when a review reaches no model — both realign counters
// simply stay zero — so the report renders silence, and silence there reads as
// "every divergence was judged and none objected". `func realignAddendum` in
// overlay_compare_realign.go derives the sentence from the same two flags this
// takes, for the reason stated there: the renderer never learns which flags were
// passed.
//
// # The baseline review's COVERAGE is rebuilt here, from the producer's counters
//
// internal/overlay wrote the same sentence until sub-task 4.2 deleted it with
// the rest of the library's formatting; it was reachable only from the renderer
// this story retires. The FACTS are read from the report; the SENTENCE is here,
// because a library that formats a report line is the boundary story 046 closed
// (S047-D1) — internal/overlay establishes what is true, this file decides how a
// reader is told.
//
// The denominator is `CompareReport.ComparedPackages` and it may never be a len:
// Results is the VIEW, narrowed after the review has already run, so a share
// taken over it could print a fraction larger than one — and, worse, a
// believable one. `--only-outdated` is what separates the two, comparing three
// packages and showing two rows, and a report that answered "1 of the 2" there
// would be claiming it examined everything it was able to. It is the rule the
// counts in `func buildCompareReport` already follow, said once more because
// this sentence states a ratio and a ratio has two ways to go wrong (R6.4).
//
// It says what was FOUND rather than asserting anything about the packages the
// review never reached, which is the producer's own wording and the reason it is
// kept: with a status filter in play the review ranges over fewer packages than
// were compared. It renders nothing at zero, which is every run that asked for
// no review.
//
// ::gentoo is spelled here rather than taken from the producer's `baselineRepo`,
// which is unexported: the report's own vocabulary already names that tree in
// the section titles a reader meets above this note, and exporting a constant to
// save a word would publish one package's spelling as another's API.
//
// # What it deliberately does NOT carry
//
// The candidate declarations a `--realign` run proposes are per-package,
// multi-line paste blocks a maintainer copies, and every note is wrapped to the
// device by the renderer. They are printed by the run itself, beside the report,
// and the call site says why.
func compareRunNotes(rep *overlay.CompareReport, realignRan, judged, noReview bool) []string {
	if rep == nil {
		return nil
	}

	var notes []string
	for _, finding := range rep.Findings {
		if finding.Kind == overlay.FindingBaselineSkipped && finding.Atom == "" {
			notes = append(notes, compareOneLine(finding.Detail))
		}
	}

	if rep.NoBaselineCount > 0 {
		notes = append(notes, fmt.Sprintf(
			"%d of the %d packages compared were found to have no ::gentoo counterpart — those are the overlay's own work rather than a divergence from anyone's, and no realignment is proposed for them.",
			rep.NoBaselineCount, rep.ComparedPackages))
	}

	if line := compareClassificationNote(rep); line != "" {
		notes = append(notes, line)
	}

	if line := compareRealignNote(rep, realignRan, judged, noReview); line != "" {
		notes = append(notes, line)
	}

	if compareRemovalRecommended(rep) {
		notes = append(notes, comparePruneAdvice)
	}

	return notes
}

// compareClassificationNote is the run-level classification share, with the
// number it is a share of (S034-R8.1).
//
// # Why it is written here and not read from the producer
//
// internal/overlay stated the same facts from a run-level line builder reachable
// only from the renderer this story retires; sub-task 4.2 deleted both, because
// the sentence they published had no consumer once the command stopped calling
// it, while its per-package half travels on the classification findings and is
// unaffected. The FACTS are re-derived from the report here; the
// SENTENCE is written here, because a library that formats a report line is the
// boundary story 046 closed (S047-D1). It is the same split
// `func compareRunNotes` already applies to the baseline coverage above.
//
// # Why it is a note and not a finding
//
// It names no package. `type Finding` permits a blank Atom for exactly one kind
// and forbids it everywhere else, so a Finding built from a run-level summary
// would be a malformed one — which is why nothing carried it across the move and
// why a run-scoped note, whose empty atom means exactly "about the run", is where
// it belongs.
//
// # The counts and their denominator
//
// The three classes are re-derived by the same walk the producer performs: the
// per-result Classified fields, skipping every result that carries none, so this
// block and the per-package lines cannot disagree. A result with no
// classification is a package with no readable baseline, or any package at all on
// a run that asked for no review; counting it as a package with zero differences
// would inflate the reach of the classification with rows nobody looked at.
//
// The denominator is the point, and it is stated twice over: the differences
// examined are given as a count across the packages they were examined in, and
// each class is given as a count of those differences. "122 differences were
// attributed to nobody" is unreadable alone, and "63% unclassified" is worse —
// 63% of twelve differences in one package and 63% of forty thousand across the
// overlay are different claims (S034-R8.1).
//
// It is ONE line because a note is wrapped to the device by the renderer, so the
// three per-class lines the producer indented under its lead cannot survive as
// lines; they are said inline instead, which loses the indent and no count.
//
// It renders nothing when no result carries a classification, which is every run
// that requested no review: silence there is the same silence the per-package
// lines keep, and a run that classified nothing has no share to state.
//
// _Requirements: S047-R7.2, S034-R8.1_
func compareClassificationNote(rep *overlay.CompareReport) string {
	if rep == nil {
		return ""
	}

	var run overlay.Classified
	packages := 0
	for _, result := range rep.Results {
		if result.Classified == (overlay.Classified{}) {
			continue
		}
		packages++
		run.VersionMove += result.Classified.VersionMove
		run.Ours += result.Classified.Ours
		run.Unclassified += result.Classified.Unclassified
	}
	if packages == 0 {
		return ""
	}

	total := run.VersionMove + run.Ours + run.Unclassified
	return fmt.Sprintf(
		"Classification: %d differences were examined across %d of the packages reviewed — of those %d, %d were attributed to the version move, %d to us, and %d to neither.",
		total, packages, total, run.VersionMove, run.Ours, run.Unclassified)
}

// compareRealignNote is what the realignment has to say about its own
// completeness: either that no verdict was produced at all, or that some
// divergence put to the model came back without one.
//
// # The two branches answer two different questions, and the flags only answer
// the first
//
// `judged` is computed by the caller as `reviewer != nil` — whether a model was
// REACHABLE, not whether every divergence came back judged. A reviewer that
// exists and then fails half its calls satisfies `judged` and leaves half the
// divergences unjudged, which the flags cannot see. `func formatRealignSummary`
// in internal/overlay/realign_reviewer.go published that second fact and is
// reached only from the renderer this story retires, so the flag-derived notice
// alone would report a partially judged run as a fully judged one.
//
// The counters ARE on the report — CompareReport.RealignNoVerdict and
// RealignAsked, maintained by the review itself — so the second branch is read
// from the run rather than inferred from the flags, and states the count with the
// number it is a share of.
//
// The two are exclusive by construction: nothing is asked when no reviewer
// exists, so RealignAsked is zero on every run the first branch fires on. They
// are written as one function so that they cannot both be emitted, which would
// tell an operator both that nothing was judged and that some of it was.
//
// It says nothing on a run that produced a verdict for everything it asked
// about, which is the outcome that needs no qualification.
//
// _Requirements: S047-R7.2_
func compareRealignNote(rep *overlay.CompareReport, realignRan, judged, noReview bool) string {
	if !realignRan {
		return ""
	}

	if !judged {
		reason := "no model was reachable"
		if noReview {
			reason = "--no-review contacted no model"
		}
		return "Realignment verdicts: none was produced — " + reason +
			", so every divergence above carries no verdict, and an unjudged divergence is not a justified one. " +
			"Everything above was established by reading files and stands without a model."
	}

	if rep == nil || rep.RealignNoVerdict <= 0 {
		return ""
	}
	// The per-cause counts follow the count they split (S057-R3.2).
	byCause := ""
	if len(rep.RealignNoVerdictBy) > 0 {
		tally := make(map[string]int, len(rep.RealignNoVerdictBy))
		for failure, n := range rep.RealignNoVerdictBy {
			tally[compareCauseWord(overlay.CompareResult{Reading: overlay.ReadingFailed, ReviewFailure: failure})] += n
		}
		parts := make([]string, 0, len(tally))
		for _, c := range compareCauseCounts(tally) {
			parts = append(parts, fmt.Sprintf("%d %s", c.Count, c.Cause))
		}
		byCause = " (" + strings.Join(parts, ", ") + ")"
	}
	return fmt.Sprintf(
		"Realignment verdicts: %d of the %d divergences put to the model came back with no verdict%s — they were not judged, and an unjudged divergence is not a justified one. "+
			"What they state was established by reading files and stands without a model.",
		rep.RealignNoVerdict, rep.RealignAsked, byCause)
}

// comparePruneAdvice is how an operator acts on the removal recommendation
// (S025-R3.3).
//
// The redundant section recommends removing packages and, without this, names no
// way to do it: a report that recommends a destructive action and withholds the
// command is asking somebody to invent one. The sentence also says what the
// command decides on, because `bentoo overlay prune` does NOT act on the verdict
// — it re-reads content — and an operator who expects the two to agree row for
// row would read any difference as a bug.
//
// It is written in this package and not in internal/common/report, where the
// recommendation itself is built, because that package must not learn a command
// name: `bentoo overlay prune` is this domain's vocabulary, and the guards on
// internal/common/report exist to keep exactly that out. A run note from the
// adapter is the nearest home that may spell it.
const comparePruneAdvice = "Nothing is deleted here: act on the recommendation to remove with 'bentoo overlay prune', which decides on content — every version the two trees share, plus the whole files/ tree — and never on the verdict alone."

// compareRemovalRecommended reports whether this run made a removal
// recommendation at all, and so whether comparePruneAdvice has anything to attach
// itself to.
//
// # The predicate is the one the recommendation itself uses
//
// `func compareRemovalAdvice` in internal/common/report/compare_run.go recommends
// removal only where a redundant package was READ: with `read == 0` it says "no
// removal advice follows" and recommends nothing. Naming a destructive command
// under that sentence would offer an action for an empty list, which is noise at
// best and an invitation at worst. So this asks the same question of the same
// population — the redundant rows, taken from the narrowed Results the section
// draws — and reuses `func compareReadingWord`, the one place this file maps a
// Reading to the word that package counts, so the two cannot fall out of step. An
// unmapped reading falls to "not requested" there, which costs a recommendation
// this run could have supported and never earns one over evidence nobody has.
//
// It walks Results and not the whole run for the reason the notes do: the advice
// is read beside rows, and a run whose every redundant package was filtered out
// of the view has no recommendation on screen to attach a command to.
func compareRemovalRecommended(rep *overlay.CompareReport) bool {
	if rep == nil {
		return false
	}
	for _, result := range rep.Results {
		if result.Verdict == overlay.VerdictRedundant && compareReadingWord(result.Reading) == "read" {
			return true
		}
	}
	return false
}
