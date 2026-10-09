package main

// The seam between `overlay compare` and the report it now ends in.
//
// internal/common/report must not import internal/overlay (boundary_test.go's
// forbiddenImports, enforced by TestPackageImportsNoPresentation) and
// internal/overlay must not know what a report is, so the conversion from one
// to the other happens here and nowhere else — the same seam as buildReport in
// overlay_autoupdate_report.go and buildManifestReport in
// overlay_manifest_report.go, with a counterpart there for every decision below.
//
// This is the widest translation of the five: a compared package crosses as
// seven fields, four of them ENUMS at the producer and words here. CompareStatus
// and Verdict carry a String() of their own in internal/overlay/compare.go;
// Verification, Authorship and Reading deliberately do not, and the functions
// that spell those words below say why.

import (
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/report"
	"github.com/obentoo/bentoolkit/internal/common/report/render"
	"github.com/obentoo/bentoolkit/internal/overlay"
)

// compareEnvelope puts one comparison's facts inside the envelope every
// exported document carries: the schema version, the kind of run that produced
// it, the reader's label for it, and how much of what it set out to establish
// it never reached. It is the ONE place this command builds a report.Run.
//
// Kind and Title are fixed per COMMAND rather than per run, so
// `.kind == "overlay.compare"` is a filter rather than a guess; a kind that
// varied with what a run found would let a consumer filtering on it silently
// miss some documents. The schema is stamped from report.SchemaVersion so a
// version bump has one place to change.
//
// Complete and NotEvaluated are ARGUMENTS, never derived from the payload: a run
// narrowed by `--only-redundant` would come out "incomplete" though it evaluated
// every row and the operator merely chose not to look, and an interrupted run
// whose last package happened to complete would come out complete. Completeness
// is established by the run that did the work; the doc on
// CompareReport.Interrupted states the same rule from the producer's side.
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
// reports, whole, before anything is printed. It is a TRANSLATION: no package
// is filtered, reordered or decided here; the order is the one
// CompareWithProvider sorted the results into (category, then package).
//
// It is called AFTER filterCompareResults narrows CompareReport.Results to what
// `--only-redundant` and `--only-patched` asked to see. The LISTS follow that
// view; every count is a counter the producer maintained, never len(Results) and
// never a subtraction of two counters, so a narrowed document states the whole
// overlay's tally and still reports Complete — a filter is a choice, not a
// shortfall. Reasons survive the narrowing because EstablishFindings fixed
// CompareReport.Findings before the filter ran.
//
// unfiltered is the WHOLE run's rows, the one population the narrowing would
// otherwise lose: Unread and compareNotEvaluated walk rows, and taken over the
// view they would shrink with the filter and make a holed run look whole. It is
// required so omitting it cannot compile; nil means the report was never
// narrowed. A nil report is reported as an empty run, not a crash.
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
		if lookup, failed := lookupReasons[pkg.Package]; failed {
			// A failed lookup's reason is the comparison's sentence, which names
			// the cause — ahead of any other finding the row carries,
			// such as a registry declaration. That finding is not dropped: it
			// leads the row's further findings instead.
			if pkg.Reason != "" {
				pkg.FurtherFindings = append([]string{pkg.Reason}, pkg.FurtherFindings...)
			}
			pkg.Reason = compareOneLine(lookup)
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
	// with a recommendation and no evidence behind it.
	//
	// It is walked over the WHOLE run rather than over the rows above, because
	// it is a count and it sits in the payload beside counts the producer
	// maintained over every package. One of the two shrinking under
	// `--only-patched` while the others did not would put two populations in one
	// section under one heading.
	//
	// ReadingFailures splits the failed readings among them by cause, in the
	// same walk, so the two are one population by construction.
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

	// GroupKeep is a package-level function rather than a method that fills
	// this field, so the assignment is written where the payload is built and is
	// visible here: a method would make a correctly built payload depend on
	// somebody having remembered to call it, and an empty KeepGroups would then
	// mean either "no version pair repeats" or "nobody ran the rule".
	//
	// It runs on the FULL Keep list, after it is built, because a group is a
	// fact the run established and travels as a field — a grouping performed
	// while rows are drawn would exist on a terminal and nowhere in the exported
	// file.
	payload.KeepGroups = report.GroupKeep(payload.Keep)

	// Complete and NotEvaluated. A run
	// is complete when it was not cut short AND left nothing it set out to
	// establish unestablished; NotEvaluated is how many of those there were.
	//
	// The two are one answer read twice, which is why they are computed
	// together: a document stating Complete true beside a NotEvaluated of three
	// would give a machine reader two contradictory answers to one question.
	// Interruption stays a recorded FACT rather than a count — a run cut short
	// after its last package has a gap of zero and was still cut short — so it
	// is OR-ed in and never inferred.
	notEvaluated := compareNotEvaluated(rep, whole)
	return compareEnvelope(payload, !rep.Interrupted && notEvaluated == 0, notEvaluated)
}

// compareNotEvaluated counts the facts this run set out to establish and did
// not, as two populations that cannot overlap:
//
//   - packages never REACHED: TotalPackages minus ComparedPackages, clamped at
//     zero so a hand-assembled report without the tally cannot subtract from
//     the second population. Such a package has no row, because Results is
//     appended under the same lock that increments ComparedPackages.
//   - packages reached and not finished: a lookup that errored, a review that
//     was killed, a version pair the content check refused. Counted once per
//     ROW, not from ErrorCount: noteContentRefusal also marks an errored
//     package ReadingNotComparable, so summing the two would count it twice.
//
// ReadingNotRequested is excluded: `--no-review` leaves every row there, and a
// choice is not a shortfall. That is why this set differs from
// CompareRun.Unread, which answers "did anybody read this difference".
//
// The rows are an ARGUMENT because the call site narrows CompareReport.Results
// first; the caller passes the whole run's rows, so what the operator chose to
// LOOK AT cannot change what the run says it ESTABLISHED.
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
			// standing on evidence nobody has.
			unestablished++
		}
	}

	return unreached + unestablished
}

// comparePkgFacts is one compared package as the model spells it.
//
// The atom is joined here, once, so no renderer picks its own separator — the
// same crossing manifestTargetFacts makes.
//
// Status crosses through CompareStatus's own String(): "up-to-date",
// "outdated", "newer" are the library's published words, and re-spelling them
// here would be a second vocabulary free to drift. Reading and Verification
// have no String() BY DESIGN — a display word is report vocabulary — so their
// words are written in this package, beside the payload that consumes them.
//
// Authorship does not cross: compareFindings already put it in the row's Reason
// ("proved ours — our ebuild references <file>, which ::gentoo does not ship"),
// and a second field would be a second answer that could contradict it.
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
// version columns; on a failed lookup it is the only thing that says why, so
// it is kept here and for those rows alone.
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
// breaks ties when the causes are counted. It is spelled here, as
// `func compareReadingWord` spells the reading words, and the report package
// holds the same list for the same reason.
var compareCauseOrder = []string{
	"timed out", "could not start", "exited non-zero", "empty or unusable reply",
	"cancelled", "ebuild unreadable", "other",
}

// compareCauseWord is the report's word for why this row failed:
// the lookup's cause on a row whose lookup failed, the review's cause on a row
// whose reading failed, and "" on a row with no failure. A failed reading with
// no recorded cause — a result built by hand — also reads "".
func compareCauseWord(result overlay.CompareResult) string {
	if result.Status == overlay.StatusError {
		return result.LookupCause.String()
	}
	if result.Reading != overlay.ReadingFailed {
		return ""
	}
	return result.ReviewFailure.String()
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
// difference.
//
// The words are written HERE because they are report vocabulary: `type
// Reading` carries no String() on purpose, so internal/overlay's API does not
// publish this package's display choice. The four strings mirror
// readingNotRequested and its three neighbours in
// internal/common/report/compare_run.go, which counts them for the redundant
// section's lead; those constants are unexported so no consumer matches on
// them, and since a typo in either half shows up as a count of zero rather
// than a failure, TestBuildCompareReport asserts the four words literally.
//
// There is no blank cell: an unmapped Reading (a fifth state added without a
// case here) reads "not requested", the fail-safe direction — it can cost a
// removal recommendation, and never earn one over evidence nobody has.
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

// compareDiffCell is what the content check found, drawn from a closed
// vocabulary: "+N/-M", "identical", "not compared", "unreadable". It is closed
// so that "no difference was found" can never be read as "no comparison was
// made" — opposite answers that used to share one blank cell.
//
// "unreadable" has no producer today, by measurement: verifyAgainstLocalContent
// returns the same zero contentCheck (NotVerified, no magnitude) for all four of
// its exits — no package on both sides, differing versions, and either ebuild
// failing to read — and Reading cannot recover the cause either. Printing
// "unreadable" would state a cause the run never established; the word stays in
// the documented vocabulary for a producer that later splits NotVerified.
//
// The magnitude is read only at VerifiedDiffers, where it is meaningful: a
// "+0/-0" on a check that never ran would look like a byte-identical pair.
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

// comparePackageFindings splits the per-package findings in two in one walk, so
// the halves cannot disagree: the FIRST finding each package has of its own,
// which becomes its row's reason, and every further finding, which becomes a
// note naming that package. A row holds one reason, and most finding kinds are
// per-atom, so without the second half a package's other findings would be
// dropped; notes are wrapped by the renderer rather than cut to a column.
//
// FindingCompared is skipped: it restates Status ("::gentoo ships X and the
// overlay carries Y"), which the row already shows, and a non-empty Reason on
// every package would stop GroupKeep from ever grouping anything, since a
// finding is not repetition. The test is on that one KIND, not on a list of
// kinds that qualify — a list goes stale the first time a producer adds one.
//
// "First" is in EstablishFindings' order — baseline run finding, comparison,
// then baseline review — so the comparison's own exception outranks what a
// later pass measured. A finding with an empty Atom is run-scoped
// (FindingBaselineSkipped) and is skipped by BOTH halves; compareRunNotes picks
// those up.
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
// from.
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
// validation rejects a whitespace-only reason, but LoadPackagesConfig never
// calls ValidatePackageConfig, so the compare path sees entries validation
// never judged. A dangling colon introducing nothing would
// read as a truncation bug rather than as the missing text it is.
func compareDeclaredTail(finding overlay.Finding) string {
	if finding.Effect.Source != overlay.EffectDeclared || finding.Effect.Text == "" {
		return ""
	}
	return ": " + compareCapped(finding.Effect.Text)
}

// compareOriginProse is the report's sentence for a model's classification, or
// "" for one that says nothing.
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
// reads the difference as coming from and what it does, followed by the
// `patched` declaration it offers for a difference it read as ours.
//
// Every line SAYS WHOSE WORDS THESE ARE. Everything else the report prints is
// something this tool established by comparing two files; these are a guess, and
// EffectReviewed exists so a renderer can tell an operator which of the two they
// are reading. Dropping the distinction invites them to act on the guess.
//
// It writes NO FILE: the model proposes and the operator applies. The overlay
// repository auto-commits within minutes, so a declaration this program wrote
// would be published before anyone could read it.
//
// Nothing here can change a verdict, a count or which table a package sits in:
// it turns one finished Finding into lines the report would
// otherwise not have printed.
func compareReviewLines(finding overlay.Finding) []string {
	// A note missing its classification or its summary has said neither which
	// side the difference comes from nor what it does, and a finding-shaped line
	// stating nothing is worse
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
	// A proposal belongs to ONE classification. `both` is deliberately not
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
// loss of content, which is what keeps every explanation in full in the
// exported document.
//
// It is spelled here rather than shared with `func foldToOneLine` in
// internal/common/report/manifest_run.go, which is unexported: exporting it
// would publish a helper as API to save one line.
func compareOneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// presentCompareReport puts the finished comparison in front of the operator:
// the terminal first, then the export. It is presentManifestReport with nothing
// added or taken away — resolve the mode, build the sections once, export the
// run — because this payload must reach the operator through the same renderer
// as every other kind, with no renderer edited for it.
//
// It must be called AFTER the live progress region is down (the caller's
// finishUI()): rendering while that region still owns stdout would draw the
// report into a frame the UI redraws over.
//
// The terminal render comes first, so a bad export path cannot cost the
// operator the most expensive report of the five; exportReport returns nothing,
// and a failed render does not stop the export. The mode is resolved HERE, not
// at the top of the run, so a typo in BENTOO_UI or ui.mode falls back to plain
// instead of reading as "your comparison did not run". --all is content
// (report.SectionOptions); Width 0 asks the device. Notes travel in the payload
// (CompareRun.Notes, ComparePkg.FurtherFindings), so the terminal and every
// export read the same sentences.
func presentCompareReport(log *slog.Logger, d *deps, cfg *config.Config, run report.Run) {
	mode := reportModeOrPlain(log, cfg, false, d.uiIsTerminal)
	content := report.SectionOptions{ShowAll: autoupdateAll}
	if err := renderCheckReportIn(mode, run.Sections(content), render.Options{}); err != nil {
		log.Warn("the report could not be rendered", "err", err)
	}
	exportReport(log, run)
}

// compareRunNotes is what a comparison run has to say about ITSELF: run-level
// facts no row carries. They travel on CompareRun.Notes, so CompareRun.Sections
// emits them and the terminal and every export read the same list. A run-level
// summary names no package, so it cannot be a Finding; each note is therefore
// rebuilt from the report's own numbers, and each helper below states the
// condition it is silent under.
//
// The baseline-skipped fact is HARVESTED from FindingBaselineSkipped, whose
// Detail is the producer's own sentence; it is run-scoped (empty Atom), which
// is exactly what comparePackageFindings skips. FindingRealignVerdict is NOT
// harvested: it carries an atom and is already that package's Reason.
//
// The share of packages with no ::gentoo counterpart uses ComparedPackages as
// the denominator, never a len: Results is the VIEW, narrowed after the review
// ran, so a share over it could print a believable fraction of the wrong whole.
// It renders nothing at zero. The candidate declarations a `--realign` run
// proposes are NOT carried: they are multi-line paste blocks and notes are
// wrapped to the device, so the run prints them beside the report.
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
// number it is a share of. The FACTS come from the report; the SENTENCE is
// written here, because internal/overlay establishes what is true and this
// file decides how a reader is told. It is a note rather than a Finding because
// it names no package.
//
// The three classes are summed by the producer's own walk over each result's
// Classified fields, skipping results that carry none (no readable baseline, or
// no review requested) so unexamined rows do not inflate the reach, and so this
// line and the per-package lines cannot disagree.
//
// The denominator is the point: "122 differences were attributed to nobody" is
// unreadable alone, and "63% unclassified" is worse — 63% of twelve differences
// in one package and of forty thousand across the overlay are different
// claims. It is ONE line because notes are wrapped by the renderer; it renders
// nothing when no result carries a classification.
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
// `judged` (the caller's `reviewer != nil`) only says a model was REACHABLE; a
// reviewer that then fails half its calls satisfies it. So the second branch
// reads CompareReport.RealignNoVerdict and RealignAsked, maintained by the
// review itself, and states the count with the number it is a share of.
//
// The branches are exclusive by construction — nothing is asked when no
// reviewer exists — and live in one function so both can never be emitted. It
// says nothing on a run that got a verdict for everything it asked about.
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
	// The per-cause counts follow the count they split.
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

// comparePruneAdvice is how an operator acts on the removal recommendation.
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
// recommendation at all, and so whether comparePruneAdvice has anything to
// attach itself to.
//
// It asks the recommendation's own question: compareRemovalAdvice in
// internal/common/report/compare_run.go recommends removal only where a
// redundant package was READ, and naming a destructive command under "no
// removal advice follows" would offer an action for an empty list. It reuses
// compareReadingWord, the one place this file maps a Reading to the word that
// package counts, so the two cannot fall out of step.
//
// It walks Results, the narrowed view, because the advice is read beside rows:
// a run whose every redundant package was filtered out has no recommendation on
// screen to attach a command to.
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
