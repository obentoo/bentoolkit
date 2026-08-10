package overlay

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/obentoo/bentoolkit/internal/common/logger"
	"github.com/obentoo/bentoolkit/internal/common/output"
	"github.com/obentoo/bentoolkit/internal/common/provider"
)

// noticeLogf emits the ONE line this pass prints before it starts, and nothing
// else. It defaults to logger.Info and is a package var for the reason warnLogf
// next door is one: so a test can read what the pass announced without reading
// stderr.
//
// It is a second deliberate exception to "a library returns errors, it does not
// log", and the exception is narrower than warnLogf's rather than wider. What it
// prints is not a diagnostic and not a finding: it is the BILL — how many model
// calls this pass is about to make — and a bill has to arrive before the money is
// spent. The report cannot carry it, because the report is printed when the pass
// has finished; by then the cost has been paid and the operator has agreed to
// nothing (D7).
var noticeLogf = logger.Info

// realignAllowedTools is the tool allow-list for the reviewing model, and it
// holds NOTHING THAT WRITES (R4.5).
//
// The reason is the same one story 033's D7 gives, and it is not abstract here:
// the overlay repository auto-commits and pushes within minutes, so a model that
// could write a file could publish an unreviewed ebuild before anyone read it.
// The staged tree of group 7 is the only boundary between a bad proposal and a
// published one, and a tool that writes goes around it.
//
// What is on the list, and why each earns its place:
//
//   - Read opens ONE more file than the two the request already carries: the
//     eclass ::gentoo delegates to. That is issue #33's whole finding — our
//     ebuild dropped `gstreamer-meson` and took 85 build options into our own
//     hand — and whether the divergence is still justified is a question about
//     what that eclass carries TODAY, which is a file on disk.
//   - Grep and Glob are how that file is found without the caller having to
//     name it. Both only read.
//
// What is deliberately absent:
//
//   - Bash, for exactly the reason Write is absent: `bash -c 'echo > x'` writes,
//     and an allow-list that admits a shell has narrowed nothing. registry_fixer.go
//     narrows to `Bash(curl *)` because a registry repair must confirm an upstream
//     page; this review reads two local files and needs no process at all.
//   - WebFetch and every other network tool. R1.4 puts the whole of this review
//     in the local tree, and a verdict grounded in a page fetched at review time
//     could not be reproduced by the operator checking it.
//
// It is a FUNCTION returning a fresh slice rather than a package var, so the
// list cannot be widened at a distance: a var would be one `append` in an
// unrelated file away from holding Write, and that append would be invisible to
// every reader of this comment.
//
// _Requirements: R4, R4.5_
func realignAllowedTools() []string {
	return []string{"Read", "Grep", "Glob"}
}

// RealignRequest is one undeclared divergence put to the reviewing model: which
// package, at which version of ours, and the two ebuilds that differ.
//
// It carries the two files' BYTES and no measurement of them, exactly as
// ReviewRequest does and for the same reason: a prompt assembled from the SIZE
// of a difference would be a computation on that size, which R1.3 forbids
// (compare_diff_counts_fence_test.go).
//
// The BASELINE side is ::gentoo's ebuild as ResolveBaseline named it — which
// need not be at our version, and usually is not. That is the difference between
// this and ReviewRequest.Theirs: the content check refuses two different versions
// on purpose, while R2.1 says the baseline comparison happens regardless of the
// version relationship.
//
// Version is OURS. The baseline's own version is not carried, and does not need
// to be: how far the baseline is from us is a deterministic answer this package
// already has (Baseline.Distance) and already prints beside the verdict
// (baselineDistanceProse), so the operator reads the distance from the report
// rather than from a model that was told about it.
//
// The two byte slices are also what the verdict cache keys on, so the request
// holds precisely what the answer depends on and nothing else.
//
// _Requirements: R4, R4.1, R4.2_
type RealignRequest struct {
	// Category, Package and Version identify the package for the reviewer's
	// prose. They are NOT part of the cache's index: the verdict is a reading of
	// the two files, and the same two files are the same question.
	Category, Package, Version string
	// Ours is our overlay ebuild at Version; Baseline is the ::gentoo ebuild it
	// was measured against. The ORDER is meaningful — the answer is about what
	// OURS carries on top of the baseline — so a request with the two swapped
	// would invert the judgement.
	Ours, Baseline []byte
}

// RealignNote is a model's judgement of one undeclared divergence: whether it is
// still justified and why (R4.1), and the baseline text that would replace it
// when it is not (R4.2).
//
// It is COMMENTARY and nothing else. Nothing that decides a Verdict or an exit
// code may read what this becomes on the result (R4.3), which is the fence
// realign_reviewer_test.go holds over RealignVerdict — one mechanism decides,
// the other comments, and a disagreement between them stays legible only while
// that holds.
//
// The JSON tags exist because the verdict cache persists this type
// (review_cache.go). They are the same lowercase names the CLI adapter in cmd/
// decodes from the model's reply, so one spelling serves both.
//
// _Requirements: R4, R4.1, R4.2_
type RealignNote struct {
	// Justified is the model's answer: does the divergence still earn its place?
	//
	// It is a plain bool with no third state, and Why below is what keeps that
	// honest: a note with no reason says nothing at all (realignNoteSpeaks), so
	// "false" can never arrive by default and be read as a judgement.
	Justified bool `json:"justified"`
	// Why is the model's reason, in its own words (R4.1). A verdict without one
	// is an instruction rather than an input — "not justified" with nothing
	// behind it cannot be checked, argued with, or acted on — so a note that
	// carries no Why is treated as no note.
	Why string `json:"why"`
	// BaselineText is the ::gentoo text that would replace the divergence when
	// the model judges it unjustified (R4.2). It is a PROPOSAL printed on a
	// terminal: nothing here writes it into an ebuild, because the overlay
	// auto-commits and a write would be a publish.
	//
	// It is empty for a divergence the model considers justified — there is then
	// nothing to replace — and may also be empty on an unjustified one, which
	// the report says rather than hides.
	BaselineText string `json:"baseline_text"`
}

// RealignReviewer is the seam through which a model's judgement of one
// divergence reaches this package. Like DivergenceReviewer next door it is the
// whole of what internal/overlay knows about an LLM: a function that takes two
// ebuilds and returns a bool and two strings.
//
// It is declared HERE, in the consumer, for the reason DivergenceReviewer is:
// holding an autoupdate type would create the import edge 025 R2.4 and D8b keep
// absent in both directions, and review_test.go fences it. The only production
// implementation is an adapter over the `claude` CLI, and it belongs in cmd/,
// which already imports both halves.
//
// It is a SECOND interface rather than a second method on DivergenceReviewer
// because the two questions are separately answerable and separately refusable:
// `--no-review` turns off the commentary, `--realign` turns on the judgement, and
// one interface would make an implementation of either promise both.
//
// The CONTEXT is the caller's, so a cancelled compare aborts an in-flight review.
// The per-invocation TIMEOUT belongs to the adapter, which knows what it is
// invoking — autoupdate.ClaudeCodeClient already wraps every call in one.
//
// A nil RealignReviewer is not an error anywhere: it is how a run that asked for
// no verdicts, and a machine with no `claude` on PATH, reach one no-op path
// instead of two conditions in cmd/ that could disagree.
//
// _Requirements: R4, R4.1_
type RealignReviewer interface {
	ReviewRealignment(ctx context.Context, req RealignRequest) (RealignNote, error)
}

// realignBaselineTextCap bounds the baseline text printed on the verdict line.
//
// It is wider than patchedReasonCap (72) because the two bound different things.
// That one bounds operator PROSE, whose tail can be recovered from the registry;
// this one bounds CODE the operator has to be able to read — an `inherit` line
// and the options beside it — and a snippet cut mid-token is worse than no
// snippet, because it looks like something that could be pasted.
//
// It is still a cap, because a model asked for "the text that would replace it"
// can answer with an entire ebuild, and one such answer would otherwise decide
// the width of the whole report.
const realignBaselineTextCap = 240

// realignSummaryLead opens the run-level line that says how many divergences
// went unjudged. It is a constant so a test can name it without copying the
// wording, on the same argument that made undeclaredDivergenceCaveat one.
const realignSummaryLead = "Realignment verdicts: "

// realignCandidateReadingLead introduces the model's words inside a candidate
// declaration, and SAYS WHOSE THEY ARE.
//
// Everything else in a candidate is something the tool established by comparing
// two files; this is a guess. The candidate is text a maintainer is invited to
// paste into an ebuild, where it becomes the recorded reason a divergence
// exists — so a reader who cannot tell the two apart would be committing a
// model's guess as their own decision.
const realignCandidateReadingLead = "a model's reading, check it before accepting: "

// needsRealignVerdict reports whether one result's divergence is a question for
// the model: undeclared, or declared with a reason that has run out (R3.1, R3.3,
// D7).
//
// This is the FILTER, and the filter is the whole cost control. 237 packages of
// full ebuild diff is not a prompt anyone should pay for, and what decides who
// pays is the declaration: a decision with a reason beside it is not re-litigated
// on every run, and asking anyway is what would make the first run's bill
// permanent.
//
// A package ::gentoo carries no version of is never asked about (R1.3): there is
// nothing to realign towards, and 84 of the overlay's 321 packages are in that
// state. It is the FIRST condition for that reason — it is a fact about ::gentoo,
// while everything below is a fact about our ebuild.
//
// It deliberately does NOT consult isUndeclaredDivergence. That predicate reads
// Verified, which the content check leaves at NotVerified whenever the two
// versions differ (resolvePackagePaths) — and R2.1 removes exactly that
// precondition from the baseline comparison. Reusing it would silence the review
// on every package where ::gentoo has moved on, which is the case the review
// exists for.
//
// On today's overlay this admits everything, because zero declarations exist.
// That is not the filter failing; it is the price of nothing having been written
// down, and R3.5's candidate declarations are what the first run buys with it.
//
// _Requirements: R3.1, R3.3, R4, R4.1, R1.3_
func needsRealignVerdict(r CompareResult) bool {
	if !r.Baseline.Found {
		return false
	}
	return !realignDeclarationHolds(r.Declarations)
}

// realignDeclarationHolds reports whether any declaration on the ebuild still
// stands — that is, whether one of them is unexpired.
//
// Expiry is READ, never computed here: EvaluateDeclarations decides it against
// the ::gentoo tree (R3.3), so a caller that skipped that pass hands in
// declarations with Expired false throughout and every one of them is read as
// standing — which is exactly what the parser's own output means.
func realignDeclarationHolds(declared []DeclaredDivergence) bool {
	for _, declaration := range declared {
		if !declaration.Expired {
			return true
		}
	}
	return false
}

// AnnotateRealignVerdicts attaches a model's judgement to every undeclared or
// expired divergence the report holds: whether it is still justified and why
// (R4.1), and the ::gentoo text that would replace it when it is not (R4.2).
//
// A NIL REVIEWER RETURNS IMMEDIATELY, which is the point of the parameter's type:
// a run that asked for no verdicts and a machine with no `claude` on PATH reach
// one no-op path instead of two conditions in cmd/ that could disagree.
//
// # It returns NOTHING, and that is R4.4
//
// An unreachable model is EXIT 0 (D9). The deterministic half of the report — the
// baseline, the structural axes, the declarations, the reduction — is complete
// and useful without a model; only the verdicts are missing, and the one thing
// the run owes the operator is to say so, which formatRealignSummary does. A pass
// that returned an error would hand the caller something to exit on, and an
// overlay whose model was briefly unreachable would fail a pipeline for a reason
// that has nothing to do with the overlay.
//
// Every failure here is a way of HAVING NO VERDICT — a reviewer that errored, ran
// out of time, answered with nothing usable, or an ebuild that moved underneath
// the run — and a missing verdict must never look like a verdict of "justified".
// It is left EMPTY and counted, never invented.
//
// # The cost, and the three things that bound it
//
// The measured overlay has 237 packages with a ::gentoo counterpart and zero
// declarations, so this filter admits all of them on the first run. Three
// mechanisms keep that from being absurd (D7):
//
//   - ONE MODEL CALL PER PACKAGE. The reviewer is asked once, about the pair of
//     files, and never a second time inside the run.
//   - THE CACHE, keyed on the content of those two files (review_cache.go), so a
//     pair neither side has touched is never re-asked. It is what makes the
//     second run cheap, which is the only reason the first one's price is
//     payable.
//   - THE NUMBER IS PRINTED BEFORE THE PASS STARTS. A cost that only becomes
//     visible while it is being paid is not a cost anybody agreed to.
//
// It is SEQUENTIAL, like AnnotateReviews and for the same reason: inside the
// comparison it would inherit ten-way concurrency, which here means ten
// concurrent `claude` processes and a report whose order depends on which
// finished first.
//
// # It runs after the baseline review, and runs it if nobody has
//
// The filter reads Baseline.Found and Declarations, which AnnotateBaseline
// writes. Rather than silently judging nothing when the caller forgot, this runs
// that pass — it is deterministic, local and idempotent, so the only difference
// between doing it here and having had it done is which line of the caller it
// happened on. A report that HAS been annotated is left alone, so no run pays for
// the walk twice.
//
// # It writes NO FILE (R3.6, R4.2)
//
// The baseline text it prints is a PROPOSAL for the operator to apply. The
// overlay repository auto-commits and pushes within minutes, so an ebuild
// rewritten here would be published before anyone could read it — which is the
// whole reason group 7 stages a realignment and gates it instead.
//
// # Nothing it writes decides anything (R4.3)
//
// It writes exactly one field per result, RealignVerdict, plus two run-level
// counters, and reads no Verdict, no Status and no count. Zero those three and
// the rendering is byte-identical to the one the comparison produced.
//
// _Requirements: R4, R4.1, R4.2, R4.3, R4.4, R4.5_
func AnnotateRealignVerdicts(report *CompareReport, rev RealignReviewer, prov provider.Provider, opts CompareOptions) {
	if report == nil || rev == nil {
		return
	}

	// The baseline review is this pass's INPUT: without it every result carries
	// the zero Baseline, the filter below admits nothing, and the run would judge
	// nothing while reporting no failure at all.
	if !realignBaselineIsAnnotated(report) {
		AnnotateBaseline(report, prov, opts)
	}

	// The set the model will be asked about, collected BEFORE anything is opened,
	// so the number below is the whole of the bill and not a running total.
	var pending []int
	for i := range report.Results {
		if needsRealignVerdict(report.Results[i]) {
			pending = append(pending, i)
		}
	}
	if len(pending) == 0 {
		// Nothing to judge, so nothing is announced, nothing is opened, and the
		// cache is not touched — which also keeps a run with no questions from
		// warning about a cache it never needed.
		return
	}

	// THE BILL, STATED BEFORE IT IS PAID (D7). "At most" is the honest word: the
	// cache answers some of these for free, and a pair that turns out to be
	// byte-identical is not a divergence to judge at all.
	noticeLogf("overlay: %d of the %d compared packages carry a divergence nothing declares; the realignment review will make at most that many model calls, one per package — fewer where neither ebuild has changed since the last run",
		len(pending), len(report.Results))

	// The caller's context, and NO deadline of this pass's own: cancelling the
	// compare must abort an in-flight review, while the per-invocation timeout
	// belongs to the adapter, which knows what it is invoking. A second deadline
	// here would be a second thing to tune for one round trip.
	ctx := opts.Ctx
	if ctx == nil {
		ctx = context.Background() // SAFE: CompareOptions.Ctx is an additive field; nil means "no cancellation requested", exactly as CompareWithProvider reads it
	}

	// ONE cache for the run, like AnnotateReviews': the "warn once" guard inside
	// a reviewCache is scoped to the value, so two would warn twice about the
	// same broken file.
	dir, err := reviewCacheDirFor()
	if err != nil {
		warnLogf("overlay: the realignment verdicts have nowhere to be cached (%v); this run still judges every divergence, stores nothing, and the next one will ask again", err)
		dir = ""
	}
	cache := newReviewCache(dir)

	// Each failure is announced ONCE PER RUN and counted thereafter. An
	// unreachable model fails identically for every package, and 237 identical
	// warnings would bury the report the operator actually asked for — the same
	// argument reviewCache.storeWarnOnce makes, and the same one that prints
	// undeclaredDivergenceCaveat once per section rather than once per row. The
	// count is not lost: formatRealignSummary states it with its denominator.
	var unreadablePair, callFailed, silentAnswer realignWarnOnce

	asked, unanswered := 0, 0
	for n, i := range pending {
		// Indexed rather than ranged over a copy: this pass exists to write one
		// field back onto the report the caller is holding.
		r := &report.Results[i]
		atom := r.Category + "/" + r.Package

		if err := ctx.Err(); err != nil {
			// Ctrl-C. Every remaining call would fail on this same context, so
			// carrying on would bury the report under one identical warning per
			// package. Stopping leaves the rest with an empty verdict, which reads
			// as "nothing was said" — which is true.
			warnLogf("overlay: the realignment review stopped (%v); %d of %d divergences carry no verdict, and the report is otherwise complete",
				err, len(pending)-n, len(pending))
			break
		}

		req, ok := realignRequestFor(*r, opts)
		if !ok {
			asked++
			unanswered++
			unreadablePair.say(fmt.Sprintf(
				"overlay: the two ebuilds behind %s could not be read for a realignment verdict; it carries none, and any further package in the same state is counted in the report's realignment summary rather than warned about again", atom))
			continue
		}
		if bytes.Equal(req.Ours, req.Baseline) {
			// The two files are the same file. There is no divergence here to
			// justify, and a verdict on one would be a judgement about nothing —
			// so this is not counted as unanswered either: nothing was asked.
			continue
		}
		asked++

		// A cached verdict is the same question already answered: the key is the
		// two files' content, so while neither has changed the answer cannot have.
		// An entry that says nothing is treated as a miss — a hand-edited or
		// half-written one must not suppress a question forever, and nothing here
		// expires.
		if note, hit := cache.getRealign(req); hit && realignNoteSpeaks(note) {
			r.RealignVerdict = formatRealignVerdict(note)
			continue
		}

		note, err := rev.ReviewRealignment(ctx, req)
		if err != nil {
			unanswered++
			callFailed.say(fmt.Sprintf(
				"overlay: the realignment review of %s failed (%v); it carries no verdict, the report is otherwise complete, and any further failure is counted in the report's realignment summary rather than warned about again", atom, err))
			continue
		}
		if !realignNoteSpeaks(note) {
			unanswered++
			silentAnswer.say(fmt.Sprintf(
				"overlay: the realignment review of %s came back with no reason; a verdict nobody argued for is not one, so it carries none, and any further silent answer is counted in the report's realignment summary rather than warned about again", atom))
			continue
		}

		r.RealignVerdict = formatRealignVerdict(note)
		// Stored only once it is worth storing. A cache with no expiry would keep
		// an empty answer for as long as neither ebuild changed, which is the one
		// failure that would not heal itself on the next run.
		cache.putRealign(req, note)
	}

	report.RealignAsked, report.RealignNoVerdict = asked, unanswered
}

// realignBaselineIsAnnotated reports whether the baseline review has already
// written its answers onto this report.
//
// The two signals are the two things only that pass can produce: a result
// carrying a non-zero Baseline, or the run-level count of results carrying none.
// The second is what makes the first sound — a report in which ::gentoo carries
// none of the packages has a zero Baseline on every row, and taking that for
// "nobody looked" would run the pass a second time on the one report where it
// found nothing.
//
// It errs toward RE-RUNNING rather than toward skipping. AnnotateBaseline is
// deterministic, local and idempotent, so a false negative costs one extra walk
// over files already in the page cache; a false positive would leave the filter
// reading fields nobody filled and judge nothing while reporting no failure.
func realignBaselineIsAnnotated(report *CompareReport) bool {
	if report.NoBaselineCount > 0 {
		return true
	}
	for i := range report.Results {
		if report.Results[i].Baseline != (Baseline{}) {
			return true
		}
	}
	return false
}

// realignRequestFor builds the request for one result, reading both ebuilds. ok
// is false when either cannot be had, which the caller reports as a package with
// no verdict rather than as an error.
//
// It does NOT go through resolvePackagePaths, and that is the one thing to
// notice about it. That resolver refuses two different versions on purpose — it
// exists to compare like with like — while the baseline is most interesting
// precisely when ::gentoo is at another version, and R2.1 says the content is
// compared regardless of the version relationship. So our side is named from
// what the scan found (ourBaselineEbuild, the same one AnnotateBaseline used) and
// ::gentoo's side is the path ResolveBaseline already chose and the report
// already prints.
//
// Nothing here comes from registry input: Category and Package are directory
// names the overlay scan produced, and Baseline.Path was built by ResolveBaseline
// from an atom it validated.
func realignRequestFor(result CompareResult, opts CompareOptions) (RealignRequest, bool) {
	ourPath := ourBaselineEbuild(result, opts)
	if ourPath == "" || result.Baseline.Path == "" {
		return RealignRequest{}, false
	}

	ours, err := os.ReadFile(ourPath) //nolint:gosec // path built from scanned overlay directory names, never from registry input
	if err != nil {
		return RealignRequest{}, false
	}
	baseline, err := os.ReadFile(result.Baseline.Path) //nolint:gosec // path resolved by ResolveBaseline inside the ::gentoo tree, never from registry input
	if err != nil {
		return RealignRequest{}, false
	}

	// The ORDER is the request's meaning: the judgement is about what OURS
	// carries on top of the baseline, and the cache's fingerprint does not
	// commute.
	return RealignRequest{
		Category: result.Category,
		Package:  result.Package,
		Version:  result.LocalVersion,
		Ours:     ours,
		Baseline: baseline,
	}, true
}

// realignNoteSpeaks reports whether a note says anything worth printing.
//
// It is the ONE definition of "unusable", shared by the pass that attaches a
// verdict and the cache that stores one, so a note the annotator accepted can
// never be one the cache refuses to serve back.
//
// The test is the REASON and not the answer. Justified is a bool: it has no
// "nobody said" value, so on its own an empty reply is indistinguishable from a
// considered "not justified" — and that particular default is the expensive one,
// since it is the verdict that invites a realignment. A reason is also what makes
// the verdict checkable at all: "not justified" with no why is an instruction,
// not an input.
//
// The whitespace trim matters because a model's "one line" can be a newline.
func realignNoteSpeaks(note RealignNote) bool {
	return strings.TrimSpace(note.Why) != ""
}

// formatRealignVerdict renders one note as the sentence the report prints
// (R4.1, R4.2).
//
// Both halves are the model's own text, flattened onto one line: the report's
// STRUCTURE is its lines, so prose carrying a newline would print a second line
// indistinguishable from a finding this tool stands behind.
//
// The whole judgement is composed into the ONE field the fence watches
// (RealignVerdict, R4.3) rather than spread across two. A second field holding
// the baseline text would sit outside that fence, and the guarantee "nothing
// that decides a Verdict reads the model's opinion" would then hold for half of
// the opinion.
//
// An unjustified verdict with no replacement text SAYS SO rather than printing
// nothing: R4.2 asks for the text, and silence where it should be would read as
// "the divergence can simply be dropped".
//
// The repository is named through axisBaselineLabel, this package's one spelling
// of "::gentoo" in a finding, so the label here and the one on an axis line
// cannot drift apart.
func formatRealignVerdict(note RealignNote) string {
	why := oneLine(note.Why)
	if note.Justified {
		return "still justified — " + why
	}

	baseline := truncateString(oneLine(note.BaselineText), realignBaselineTextCap)
	if baseline == "" {
		return "NOT justified — " + why + " (the model named no " + axisBaselineLabel + " text to replace it with)"
	}
	return "NOT justified — " + why + "; the " + axisBaselineLabel + " text that would replace it: " + baseline
}

// formatRealignSummary renders the run-level line that says how many divergences
// came back with NO VERDICT, with the number they are a share of (R4.4).
//
// This line is R4.4 itself. An unreachable model exits 0 (D9), so the exit code
// says nothing, and every affected package carries an empty RealignVerdict —
// which renders as silence, and silence here reads as "every divergence was
// judged and none objected". The denominator is the point for the reason
// formatBaselineSummary's is: "12 packages went unjudged" is a different claim
// out of 12 than out of 237.
//
// It renders NOTHING at zero, which is every run that produced a verdict for
// everything it asked about AND every run that asked about nothing — including
// every run that requested no review at all, which is what keeps R7.2's
// byte-identical promise mechanical.
func formatRealignSummary(report *CompareReport) string {
	if report == nil || report.RealignNoVerdict <= 0 {
		return ""
	}
	return output.Sprintf(output.Warning,
		"\n%s%d of the %d divergences put to the model came back with no verdict — they were not judged, and an unjudged divergence is not a justified one. Everything above was established by reading files and stands without a model.\n",
		realignSummaryLead, report.RealignNoVerdict, report.RealignAsked)
}

// CandidateDeclarationsWithVerdict is CandidateDeclarations enriched with the
// model's reading, which is task 3.3's proposal finished where the model already
// is (R3.5, R4.1).
//
// The split between the two is deliberate and runs the other way from how it
// looks. CandidateDeclarations builds a block from the axis and the deterministic
// finding ALONE — no model, no reviewer, no provider in its signature — because
// group 3 declares no dependency on group 5 and because under `--no-review` there
// is no reading in existence, so a candidate that needed one would be
// unsatisfiable on the run that most needs the output. This adds to that; it does
// not move it. Called on a result with no verdict it returns exactly what
// CandidateDeclarations returned.
//
// What the enrichment buys is the half a deterministic candidate cannot state.
// The block says WHAT diverges — "ours passes 85 build options, ::gentoo's passes
// 2" — and the reason that is acceptable is what a maintainer has to write. The
// model's reading is a first draft of that sentence, and it is labelled as one:
// realignCandidateReadingLead says whose words follow, because the candidate is
// text somebody is invited to paste into an ebuild, where it becomes the recorded
// reason a divergence exists.
//
// It takes the whole result rather than the three fields it reads so that a
// renderer, which is holding exactly one of these, cannot pair one package's axes
// with another's verdict.
//
// It writes NOTHING (R3.6). Like everything else in this file it returns text.
//
// _Requirements: R3, R3.5, R3.6, R4.1_
func CandidateDeclarationsWithVerdict(r CompareResult) []string {
	candidates := CandidateDeclarations(r.Axes, r.Declarations)

	reading := oneLine(r.RealignVerdict)
	if reading == "" {
		return candidates
	}
	for i, block := range candidates {
		candidates[i] = candidateWithReading(block, reading)
	}
	return candidates
}

// candidateWithReading appends the model's reading to a candidate's TAG LINE.
//
// The tag line, and not a third line of its own, because what comes out has to
// parse back through ParseDivergences as exactly one well-formed declaration —
// candidateBlock's promise — and a bare comment line between the tag and its
// `drop-when:` continuation would break the continuation off from the tag it
// belongs to. Everything after the axis's colon is the reason, so a reading that
// contains colons of its own survives whole.
//
// The reading is capped on the same argument patchedReasonCap rests on: it is
// prose, its tail costs nothing that cannot be read again from the report, and
// uncapped it would let one model's essay decide the width of a block somebody
// is meant to paste.
func candidateWithReading(block, reading string) string {
	tag, rest, hasRest := strings.Cut(block, "\n")
	tag += " — " + realignCandidateReadingLead + truncateString(reading, patchedReasonCap)
	if !hasRest {
		return tag
	}
	return tag + "\n" + rest
}

// realignWarnOnce announces one KIND of failure the first time it happens and
// stays quiet after that.
//
// It exists because an unreachable model fails IDENTICALLY for every package it
// is asked about, and the measured overlay would put 237 of them to it: one
// warning per package is 237 identical lines on top of the report the operator
// actually asked for. Nothing is hidden by the silence — formatRealignSummary
// states the total with its denominator, which is the number that matters.
//
// It is the same argument reviewCache.storeWarnOnce makes for a directory that
// will not be written, and the same one that prints undeclaredDivergenceCaveat
// once per section rather than once per row.
type realignWarnOnce struct{ said bool }

// say emits message unless this kind of failure has already been announced.
func (o *realignWarnOnce) say(message string) {
	if o.said {
		return
	}
	o.said = true
	// The message is an ARGUMENT and never a format string: it may carry a
	// model's or a CLI's own text.
	warnLogf("%s", message)
}
