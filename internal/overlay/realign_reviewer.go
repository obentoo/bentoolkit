package overlay

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"strings"

	"github.com/obentoo/bentoolkit/internal/common/provider"
)

// realignAllowedTools is the tool allow-list for the reviewing model, and it
// holds NOTHING THAT WRITES: the overlay repository auto-commits and pushes
// within minutes, so a model that could write a file could publish an
// unreviewed ebuild before anyone read it, going around the staged tree.
//
// On the list: Read opens ONE more file than the two the request carries — the
// eclass ::gentoo delegates to, which is issue #33's whole finding (our ebuild
// took 85 build options into its own hand) — and Grep and Glob find that file
// without the caller naming it. All three only read.
//
// Deliberately absent: Bash, because `bash -c 'echo > x'` writes and a list
// that admits a shell has narrowed nothing (even the registry fixer holds no
// Bash); and WebFetch with every other network tool, because this review is
// local and a verdict grounded in a page fetched at review time could not be
// reproduced by the operator checking it.
//
// It is a FUNCTION returning a fresh slice rather than a package var, so the
// list cannot be widened at a distance by an `append` in an unrelated file.
func realignAllowedTools() []string {
	return []string{"Read", "Grep", "Glob"}
}

// RealignRequest is one undeclared divergence put to the reviewing model: which
// package, at which version of ours, and the two ebuilds that differ.
//
// It carries the two files' BYTES and no measurement of them, exactly as
// ReviewRequest does: a prompt assembled from the SIZE of a difference would be
// a computation on that size, which is forbidden
// (compare_diff_counts_fence_test.go).
//
// The BASELINE side is ::gentoo's ebuild as ResolveBaseline named it, which need
// not be at our version: unlike the content check, the baseline comparison
// happens regardless of the version relationship. Version is OURS; how far the
// baseline is from us is already known (Baseline.Distance) and printed beside
// the verdict, so the operator reads it from the report rather than from a model.
//
// The two byte slices are also what the verdict cache keys on, so the request
// holds precisely what the answer depends on and nothing else.
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
// still justified and why, and the baseline text that would replace it
// when it is not.
//
// It is COMMENTARY and nothing else. Nothing that decides a Verdict or an exit
// code may read what this becomes on the result, which is the fence
// realign_reviewer_test.go holds over RealignVerdict — one mechanism decides,
// the other comments, and a disagreement between them stays legible only while
// that holds.
//
// The JSON tags exist because the verdict cache persists this type
// (review_cache.go). They are the same lowercase names the CLI adapter in cmd/
// decodes from the model's reply, so one spelling serves both.
type RealignNote struct {
	// Justified is the model's answer: does the divergence still earn its place?
	//
	// It is a plain bool with no third state, and Why below is what keeps that
	// honest: a note with no reason says nothing at all (realignNoteSpeaks), so
	// "false" can never arrive by default and be read as a judgement.
	Justified bool `json:"justified"`
	// Why is the model's reason, in its own words. A verdict without one
	// is an instruction rather than an input — "not justified" with nothing
	// behind it cannot be checked, argued with, or acted on — so a note that
	// carries no Why is treated as no note.
	Why string `json:"why"`
	// BaselineText is the ::gentoo text that would replace the divergence when
	// the model judges it unjustified. It is a PROPOSAL printed on a
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
// holding an autoupdate type would create an import edge kept absent in both
// directions (review_test.go fences it). The only production implementation is
// an adapter over the `claude` CLI in cmd/, which already imports both halves.
//
// It is a SECOND interface rather than a method on DivergenceReviewer because
// `--no-review` turns off the commentary and `--realign` turns on the
// judgement; one interface would make an implementation of either promise both.
//
// The CONTEXT is the caller's, so a cancelled compare aborts an in-flight
// review; the per-invocation TIMEOUT belongs to the adapter. A nil
// RealignReviewer is not an error anywhere: a run that asked for no verdicts
// and a machine with no `claude` on PATH reach one no-op path.
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
// the model: undeclared, or declared with a reason that has run out.
//
// This is the FILTER, and the filter is the whole cost control: 237 packages of
// full ebuild diff is not a prompt anyone should pay for, and a decision with a
// reason beside it is not re-litigated on every run.
//
// A package ::gentoo carries no version of is never asked about — there is
// nothing to realign towards — and that is checked FIRST because it is a fact
// about ::gentoo, while the rest is a fact about our ebuild.
//
// It deliberately does NOT consult isUndeclaredDivergence: that predicate reads
// Verified, which stays NotVerified whenever the two versions differ, and
// reusing it would silence the review on every package where ::gentoo has moved
// on — the case the review exists for.
//
// On today's overlay this admits everything, because zero declarations exist;
// the candidate declarations are what the first run buys with that price.
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
// the ::gentoo tree, so a caller that skipped that pass hands in
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
// expired divergence: whether it is still justified and why, and the ::gentoo
// text that would replace it when it is not. A nil reviewer returns at once.
//
// It returns NO ERROR: an unreachable model is exit 0, because the
// deterministic half of the report is complete without one, and
// formatRealignSummary says the verdicts are missing. Every failure is a way of
// HAVING NO VERDICT, left EMPTY and counted, never read as "justified". Like
// AnnotateBaseline, it ends by rebuilding report.Findings.
//
// Cost (237 packages and zero declarations on the first run) is bounded by ONE
// model call per package, the cache keyed on the two files' content, and the
// number printed BEFORE the pass starts. It is SEQUENTIAL: inside the comparison
// it would inherit ten-way concurrency — ten concurrent `claude` processes and a
// report order that depends on which finished first.
//
// It runs AnnotateBaseline first when the report is not annotated yet. It
// writes NO FILE — the overlay auto-commits, so the baseline text is a proposal
// — and decides nothing: RealignVerdict per result plus two run counters.
func AnnotateRealignVerdicts(ctx context.Context, report *CompareReport, rev RealignReviewer, prov provider.Provider, opts CompareOptions) {
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

	// THE BILL, STATED BEFORE IT IS PAID. "At most" is the honest word: the
	// cache answers some of these for free, and a pair that turns out to be
	// byte-identical is not a divergence to judge at all.
	//
	// It is the ONE line this pass prints before it starts, at INFO, and it is a
	// deliberate exception to "a library returns errors, it does not log". It is
	// not a diagnostic and not a finding: it is the BILL — how many model calls
	// this pass is about to make — and a bill has to arrive before the money is
	// spent. The report cannot carry it, because the report is printed when the
	// pass has finished; by then the cost has been paid and the operator has
	// agreed to nothing.
	log := opts.logger()
	log.Info("overlay: compared packages carry a divergence nothing declares; the realignment review will make at most that many model calls, one per package — fewer where neither ebuild has changed since the last run",
		"divergences", len(pending), "packages", len(report.Results))

	// The caller's context, and NO deadline of this pass's own: cancelling the
	// compare must abort an in-flight review, while the per-invocation timeout
	// belongs to the adapter, which knows what it is invoking. A second deadline
	// here would be a second thing to tune for one round trip.

	// ONE cache for the run, like AnnotateReviews': the "warn once" guard inside
	// a reviewCache is scoped to the value, so two would warn twice about the
	// same broken file.
	dir, err := reviewCacheDirFor()
	if err != nil {
		log.Warn("overlay: the realignment verdicts have nowhere to be cached; this run still judges every divergence, stores nothing, and the next one will ask again", "err", err)
		dir = ""
	}
	cache := newReviewCache(dir, log)

	// Each failure is announced ONCE PER RUN and counted thereafter. An
	// unreachable model fails identically for every package, and 237 identical
	// warnings would bury the report the operator actually asked for — the same
	// argument reviewCache.storeWarnOnce makes, and the same one that prints
	// undeclaredDivergenceCaveat once per section rather than once per row. The
	// count is not lost: formatRealignSummary states it with its denominator.
	// unansweredBy splits unanswered by the review cause that applies, in the
	// same words the divergence review uses (see `type ReviewFailure`).
	unansweredBy := map[ReviewFailure]int{}
	pass := &realignPass{rev: rev, opts: opts, log: log, cache: cache, unansweredBy: unansweredBy}
	for n, i := range pending {
		// Indexed rather than ranged over a copy: this pass exists to write one
		// field back onto the report the caller is holding.
		r := &report.Results[i]

		if err := ctx.Err(); err != nil {
			// Ctrl-C. Every remaining call would fail on this same context, so
			// carrying on would bury the report under one identical warning per
			// package. Stopping leaves the rest with an empty verdict, which reads
			// as "nothing was said" — which is true.
			log.Warn("overlay: the realignment review stopped; the remaining divergences carry no verdict, and the report is otherwise complete",
				"err", err, "unjudged", len(pending)-n, "divergences", len(pending))
			break
		}

		pass.judge(ctx, r)
	}

	report.RealignAsked, report.RealignNoVerdict = pass.asked, pass.unanswered
	// Written on every pass, like the two counts, so it always describes the
	// pass that just ran and sums to RealignNoVerdict; nil when none.
	report.RealignNoVerdictBy = nil
	if len(unansweredBy) > 0 {
		report.RealignNoVerdictBy = unansweredBy
	}

	// And the verdicts reach the caller as FINDINGS, not only as a field on each
	// result. Until this call they exist as RealignVerdict strings a
	// renderer has to know to look at, so a consumer walking report.Findings —
	// an export, a count, a second renderer — is told a model judged nothing.
	//
	// It is EstablishFindings rather than an append for the reason AnnotateBaseline
	// gives: the findings are a pure function of the report, so a rebuild after
	// this pass produces the list the earlier one would have produced had the
	// verdicts been there, and it never doubles. The early returns above skip it
	// on purpose — a pass that judged nothing changed nothing to re-establish.
	EstablishFindings(report)
}

// realignPass is the state one AnnotateRealignVerdicts run carries from one
// result to the next: what it asks with, the one-per-run warnings, and the
// counts it writes onto the report at the end.
type realignPass struct {
	rev   RealignReviewer
	opts  CompareOptions
	log   *slog.Logger
	cache *reviewCache

	unreadablePair, callFailed, silentAnswer realignWarnOnce

	asked, unanswered int
	unansweredBy      map[ReviewFailure]int
}

// judge asks for the verdict on one result and writes it onto r, counting the
// question and, when there is no verdict, the reason there is none.
func (p *realignPass) judge(ctx context.Context, r *CompareResult) {
	atom := r.Category + "/" + r.Package

	req, ok := realignRequestFor(*r, p.opts)
	if !ok {
		p.asked++
		p.unanswered++
		p.unansweredBy[ReviewEbuildUnreadable]++
		if p.unreadablePair.first() {
			p.log.Warn(
				"overlay: the two ebuilds behind a package could not be read for a realignment verdict; it carries none, and any further package in the same state is counted in the report's realignment summary rather than warned about again",
				"atom", atom)
		}
		return
	}
	if bytes.Equal(req.Ours, req.Baseline) {
		// The two files are the same file. There is no divergence here to
		// justify, and a verdict on one would be a judgement about nothing —
		// so this is not counted as unanswered either: nothing was asked.
		return
	}
	p.asked++

	// A cached verdict is the same question already answered: the key is the
	// two files' content, so while neither has changed the answer cannot have.
	// An entry that says nothing is treated as a miss — a hand-edited or
	// half-written one must not suppress a question forever, and nothing here
	// expires.
	if note, hit := p.cache.getRealign(req); hit && realignNoteSpeaks(note) {
		r.RealignVerdict = formatRealignVerdict(note)
		return
	}

	note, err := p.rev.ReviewRealignment(ctx, req)
	if err != nil {
		p.unanswered++
		p.unansweredBy[classifyReviewError(err)]++
		if p.callFailed.first() {
			p.log.Warn(
				"overlay: the realignment review of a package failed; it carries no verdict, the report is otherwise complete, and any further failure is counted in the report's realignment summary rather than warned about again",
				"atom", atom, "err", err)
		}
		return
	}
	if !realignNoteSpeaks(note) {
		p.unanswered++
		p.unansweredBy[ReviewUnusableReply]++
		if p.silentAnswer.first() {
			p.log.Warn(
				"overlay: the realignment review of a package came back with no reason; a verdict nobody argued for is not one, so it carries none, and any further silent answer is counted in the report's realignment summary rather than warned about again",
				"atom", atom)
		}
		return
	}

	r.RealignVerdict = formatRealignVerdict(note)
	// Stored only once it is worth storing. A cache with no expiry would keep
	// an empty answer for as long as neither ebuild changed, which is the one
	// failure that would not heal itself on the next run.
	p.cache.putRealign(req, note)
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
// precisely when ::gentoo is at another version, and the content is
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
	baseline, err := os.ReadFile(result.Baseline.Path)
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

// formatRealignVerdict renders one note as the sentence the report prints.
//
// Both halves are the model's own text, flattened onto one line: the report's
// STRUCTURE is its lines, so prose carrying a newline would print a second line
// indistinguishable from a finding this tool stands behind.
//
// The whole judgement is composed into the ONE field the fence watches
// (RealignVerdict) rather than spread across two, so "nothing that decides a
// Verdict reads the model's opinion" holds for all of the opinion.
//
// An unjustified verdict with no replacement text SAYS SO rather than printing
// nothing, which would read as "the divergence can simply be dropped".
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

// CandidateDeclarationsWithVerdict is CandidateDeclarations enriched with the
// model's reading, finishing the candidate proposal where the model already is.
//
// CandidateDeclarations builds a block from the axis and the deterministic
// finding ALONE — no model in its signature — because under `--no-review` no
// reading exists, and a candidate that needed one would be unsatisfiable on the
// run that most needs it. This adds to that; called on a result with no
// verdict it returns exactly what CandidateDeclarations returned.
//
// The block says WHAT diverges; the reason that is acceptable is what a
// maintainer has to write, and the model's reading is a first draft of it,
// labelled by realignCandidateReadingLead because the candidate is text
// somebody may paste into an ebuild as the recorded reason.
//
// It takes the whole result so a renderer cannot pair one package's axes with
// another's verdict. It writes NOTHING; like everything else in this file it
// returns text.
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

// first reports whether this kind of failure is being announced for the first
// time, and marks it announced. The caller writes the warning itself, so each
// message stays a constant at its own call site.
func (o *realignWarnOnce) first() bool {
	if o.said {
		return false
	}
	o.said = true
	return true
}
