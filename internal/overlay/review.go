package overlay

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/obentoo/bentoolkit/internal/common/provider"
)

// reviewCacheDirFor names the directory this pass caches notes in.
//
// It is a package var, and a test seam rather than configuration, for one reason:
// the alternative is a test suite that reads and writes the developer's real
// ~/.cache/bentoo/compare, where one run's stored note would silently answer the
// next run's assertion about how many times a reviewer was called. Production
// never assigns it.
var reviewCacheDirFor = defaultReviewCacheDir

// AnnotateReviews attaches a model's reading to every undeclared divergence the
// report holds — where the difference came from, what it does, and, when it is
// ours, the `patched` text that would declare it.
//
// A NIL REVIEWER RETURNS IMMEDIATELY: `--no-review` and "no `claude` on PATH"
// then share one no-op path instead of two conditions in cmd/ that could
// disagree. It runs AFTER CompareWithProvider and SEQUENTIALLY, so commentary
// order never depends on which of ten concurrent `claude` processes finished
// first; if that grows too slow, the answer is a bounded worker pool.
//
// It returns NOTHING. Every failure is a way of having no reading: the row
// records ReadingFailed, the deterministic report goes out unchanged, and the
// exit status does not move. It warns about no review outcome — each is a fact
// on the row already — only about the cache having nowhere to live, a fact about
// this machine that belongs to no row.
//
// It writes only Review and Reading, so it cannot change a Verdict, a count or
// the grouping. It writes NO FILE: the declaration is a PROPOSAL, because the
// overlay repository auto-commits and pushes within minutes.
func AnnotateReviews(ctx context.Context, report *CompareReport, reviewer DivergenceReviewer, prov provider.Provider, opts CompareOptions) {
	if report == nil || reviewer == nil {
		return
	}

	// The findings to submit, collected BEFORE anything is opened. The predicate
	// is the report's own (isUndeclaredDivergence), so the set the model sees
	// cannot drift from the set the report reports on, and an empty set keeps the
	// run from touching — or warning about — the cache at all.
	//
	// The same walk RE-ASSERTS ReadingNotComparable on pairs the content check
	// refused, through noteContentRefusal. It re-asserts rather than decides:
	// deciding here was a bug, because a machine without `claude` returns above
	// and its refused pairs then read as "nobody asked". The producer records
	// them; this call covers a report some other caller assembled, and can write
	// no value but the one already there.
	//
	// No result is in both sets: isUndeclaredDivergence requires VerifiedDiffers
	// and noteContentRefusal acts on NotVerified.
	var pending []int
	for i := range report.Results {
		if isUndeclaredDivergence(report.Results[i]) {
			pending = append(pending, i)
			continue
		}
		noteContentRefusal(&report.Results[i])
	}
	if len(pending) == 0 {
		return
	}

	// The caller's context, and NO deadline of this pass's own. A review is a
	// network round trip behind a CLI, so cancelling the compare must abort it —
	// that is what this carries. The per-invocation TIMEOUT belongs to the
	// adapter, which knows what it is invoking: autoupdate.ClaudeCodeClient
	// already wraps every call in one.
	//
	// WHERE that budget comes from is cmd/'s to answer and not this package's: it
	// resolves the operator's configured review timeout — the
	// `autoupdate.review.timeout` key, falling back to config.DefaultReviewTimeout
	// when it is unset, zero or negative — and sets that value on the client it
	// builds. Naming a number here would name one this package cannot
	// see and a configuration file can move.
	//
	// A second deadline here would be a second thing to tune for one round trip,
	// and "exceeds its timeout" needs none — an expired deadline surfaces
	// as an error from ReviewDivergence and is handled below with every other
	// error.

	// ONE cache for the run. Two would warn twice about the same broken file, and
	// the "once per run" guard inside a reviewCache is scoped to the value.
	//
	// newReviewCache("") is a deliberately silent in-memory-only cache, so the
	// reason it has nowhere to live is stated HERE or nowhere: the caller that
	// could not name a directory is the one holding the reason.
	dir, err := reviewCacheDirFor()
	if err != nil {
		opts.logger().Warn("overlay: the review notes have nowhere to be cached; this run still reviews every divergence, stores nothing, and the next one will ask again", "err", err)
		dir = ""
	}
	cache := newReviewCache(dir, opts.Logger)

	for n, i := range pending {
		// Indexed rather than ranged over a copy: this pass exists to write two
		// fields back onto the report the caller is holding.
		r := &report.Results[i]

		if ctx.Err() != nil {
			// Ctrl-C. Every remaining call would fail on this same context, so
			// carrying on would ask the model once per package for an answer that
			// cannot arrive.
			//
			// EVERY REMAINING PENDING RESULT is marked, not only the one the loop
			// stopped on: the state is recorded on EACH CompareResult, not on the
			// ones the loop happened to reach. Leaving the rest at the
			// zero would say "nobody asked" about reviews this run DID request and
			// then abandoned — the exact conflation this field exists to remove —
			// and ReadingFailed is "a reading was attempted and did not come
			// back", which a cancellation is.
			for _, j := range pending[n:] {
				report.Results[j].Reading = ReadingFailed
				report.Results[j].ReviewFailure = ReviewCancelled
			}
			return
		}

		req, ok := reviewRequestFor(*r, prov, opts)
		if !ok {
			// Rare by construction — the comparison read both of these files a
			// moment ago to conclude they differ — so this is a file that moved
			// underneath the run.
			//
			// It reads as FAILED and not as ReadingNotComparable: the content
			// check did not refuse this pair, it found a difference in it, and the
			// row still says VerifiedDiffers. A reading was requested here and did
			// not happen, which is what ReadingFailed means.
			r.Reading = ReadingFailed
			r.ReviewFailure = ReviewEbuildUnreadable
			continue
		}

		// A cached note is the same question already answered: the key is
		// the two files' content, so while neither has changed the answer cannot
		// have. An entry that says nothing is treated as a miss — a hand-edited or
		// half-written one must not suppress a question forever, and nothing here
		// expires.
		//
		// A hit is ReadingDone like any other answer: the state records that the
		// difference WAS explained, not that a model was invoked, so two runs over
		// an unchanged overlay cannot disagree about whether anybody read it.
		if note, hit := cache.get(req); hit && reviewNoteSpeaks(note) {
			r.Review = note
			r.Reading = ReadingDone
			continue
		}

		// ONE branch for both ways of not getting an answer. A reply that parsed
		// and said nothing — no classification or no summary — leaves the operator
		// exactly where an error does; two branches here would be two spellings of
		// one state, and the pair could then drift.
		//
		// The STATE stays one. WHY it failed is a separate field, ReviewFailure, set beside it, so the operator is told
		// the cause without the state being split into several.
		note, err := reviewer.ReviewDivergence(ctx, req)
		if err != nil || !reviewNoteSpeaks(note) {
			r.Reading = ReadingFailed
			if err != nil {
				r.ReviewFailure = classifyReviewError(err)
				r.FailureText = redactFailure(err.Error(), opts.Redact)
			} else {
				r.ReviewFailure = ReviewUnusableReply
			}
			continue
		}

		r.Review = note
		r.Reading = ReadingDone
		// Stored only once it is worth storing. A cache with no expiry would keep
		// an empty answer for as long as neither ebuild changed, which is the one
		// failure that would not heal itself on the next run.
		cache.put(req, note)
	}
}

// reviewRequestFor builds the request for one finding, re-reading both ebuilds.
// ok is false when either cannot be had, which the caller reports as a package
// with no reading rather than as an error.
//
// The two files are re-read through resolvePackagePaths — the one path
// construction in this package, shared with the content check and the authorship
// pass — rather than carried on every CompareResult. Eight findings on the
// measured overlay means eight small reads, so the buffers would be paid for by
// every one of the 300-odd results to save nothing.
func reviewRequestFor(result CompareResult, prov provider.Provider, opts CompareOptions) (ReviewRequest, bool) {
	paths, ok := resolvePackagePaths(result, prov, opts)
	if !ok {
		return ReviewRequest{}, false
	}

	ours, err := os.ReadFile(paths.ourEbuild())
	if err != nil {
		return ReviewRequest{}, false
	}
	theirs, err := os.ReadFile(paths.upstreamEbuild())
	if err != nil {
		return ReviewRequest{}, false
	}

	// The ORDER is the request's meaning: Origin names a side, and the cache's
	// fingerprint does not commute.
	return ReviewRequest{
		Category: result.Category,
		Package:  result.Package,
		Version:  result.LocalVersion,
		Ours:     ours,
		Theirs:   theirs,
	}, true
}

// reviewNoteSpeaks reports whether a note says anything worth printing.
//
// It is the ONE definition of "unusable", shared by the pass that attaches a
// note and the renderer that prints one, so a note the annotator accepted can
// never be one the report ignores. A note needs both halves — which side the
// divergence came from and what it does — and a reply missing
// either has answered neither — OriginUnknown is the value that means "nobody
// said", not a classification.
//
// The whitespace trim matters because a model's "one line" can be a newline.
func reviewNoteSpeaks(note ReviewNote) bool {
	return note.Origin != OriginUnknown && strings.TrimSpace(note.Summary) != ""
}

// DivergenceReviewer is the seam through which a model's reading of one
// divergence reaches this package: all internal/overlay knows about an LLM.
//
// It is declared HERE, in the consumer, rather than as a method on
// autoupdate.LLMProvider: a method there would force openai.go and ollama.go to
// implement a review they never serve, and holding one would make this package
// import autoupdate — an edge kept absent in BOTH directions (review_test.go
// fences it). The production adapter lives in cmd/, which imports both halves.
//
// The CONTEXT is the caller's, so a cancelled compare aborts the review rather
// than holding the run open. A nil DivergenceReviewer is not an error anywhere:
// it is how `--no-review` and an absent `claude` CLI reach one no-op path.
//
// An implementation that knows WHY a review failed says so by wrapping one of
// ErrReviewTimedOut, ErrReviewCouldNotStart, ErrReviewExitedNonZero or
// ErrReviewUnusableReply, or context.Canceled for a run that was stopped, in
// the error it returns; anything else is recorded as ReviewOther. The sentinels
// are this package's own so it never has to import the client that fails.
type DivergenceReviewer interface {
	ReviewDivergence(ctx context.Context, req ReviewRequest) (ReviewNote, error)
}

// ReviewRequest is one divergence put to a reviewer: which package, at which
// version, and the two ebuilds that differ.
//
// It carries the two files' BYTES and never the line counts beside them, held at
// the type: a prompt assembled from the SIZE of a difference would
// be a computation on that size, which is exactly what the counts may never feed
// — see compare_diff_counts_fence_test.go, which fences DiffAdded/DiffRemoved to
// the one function that fills them and the one that prints them.
//
// The bytes are also what the review cache keys on (review_cache.go), so the
// request holds precisely what the answer depends on and nothing else.
type ReviewRequest struct {
	// Category, Package and Version identify the package for the reviewer's
	// prose. They are NOT part of the cache's index: the classification is a
	// reading of the two files, keyed on their content alone.
	Category, Package, Version string
	// Ours and Theirs are the two ebuilds at Version — ours from the overlay,
	// theirs from ::gentoo — read through resolvePackagePaths, the one path
	// construction in this package. The ORDER is meaningful: Origin below names a
	// side, so a request with the two swapped would invert the answer.
	Ours, Theirs []byte
}

// ReviewNote is a model's reading of one divergence: COMMENTARY, printed beside
// a finding, and never an input to anything the report decides. The same
// package grouping, the same Verdicts and the same removal recommendations hold
// whether or not a review ran.
//
// Its two prose fields are operator-facing text a MODEL produced. They are
// printed as ARGUMENTS and never as a format string — like every other piece of
// report text — they reach no shell and no command, and Declaration is capped on
// the line the way PatchedReason is (patchedReasonCap).
//
// Nothing here is ever written to a file. The review PROPOSES declaration text
// and the operator applies it: the overlay repository auto-commits and pushes within
// minutes, so a declaration written by this program would be published before
// anyone could read it.
//
// The JSON tags exist because the review cache persists this type
// (review_cache.go), and they are the same three lowercase names the CLI adapter
// in cmd/ decodes from the model's reply, so one spelling serves both.
type ReviewNote struct {
	// Origin is where the divergence came from, as the model read it.
	Origin ReviewOrigin `json:"origin"`
	// Summary is one line saying what the divergence does.
	Summary string `json:"summary"`
	// Declaration is proposed `patched` text for the registry. It is
	// empty unless Origin is OriginOverlay: there is nothing of ours to declare
	// about a change that is not ours. It is a PROPOSAL — text on a terminal,
	// never a file.
	Declaration string `json:"declaration"`
}

// ReviewOrigin is which side a divergence came from, as the model classified
// it. It is the model's reading and not a finding of this package's own:
// Authorship, next door, is what the overlay's CONTENT proves, and the two are
// deliberately different types so a classification can never be mistaken for a
// proof.
type ReviewOrigin int

const (
	// OriginUnknown is the ZERO VALUE, and it says nothing about anybody. Every
	// note nobody filled — every result no review reached, every run with
	// `--no-review`, every package whose reviewer errored — carries it, and it
	// reads as "nothing was said" rather than as a classification.
	//
	// This is the same argument AuthorshipUnproved makes as its own zero value,
	// and here it has a second edge: the review attaches a proposed `patched`
	// declaration to an OVERLAY-origin note. Had OriginOverlay been the zero
	// value, every un-reviewed finding would both accuse us of the change and
	// invite a declaration of it.
	OriginUnknown ReviewOrigin = iota
	// OriginOverlay means the divergence is work of ours, carried on top of
	// ::gentoo's ebuild. This is the classification a declaration is proposed
	// for.
	OriginOverlay
	// OriginUpstream means ::gentoo moved and our copy did not — the in-place
	// revision, with no revbump, that the undeclared-divergence caveat warns a
	// small diff is usually caused by.
	OriginUpstream
	// OriginBoth means each side changed: our copy carries work of ours AND has
	// fallen behind an upstream revision.
	OriginBoth
)

// String returns the report's word for an origin.
//
// The four words are this feature's vocabulary in both directions: the cache
// stores one (see MarshalText) and the CLI adapter in cmd/ decodes one from the
// model's reply. A renderer wanting different prose — "::gentoo" for upstream,
// say — builds it from these, rather than these being built for one renderer.
func (o ReviewOrigin) String() string {
	switch o {
	case OriginUnknown:
		return "unknown"
	case OriginOverlay:
		return "overlay"
	case OriginUpstream:
		return "upstream"
	case OriginBoth:
		return "both"
	default:
		// A value with no word — only reachable if a fifth origin is added without
		// a case here — reads as "unknown" rather than as a bare integer, matching
		// Verdict.String() and CompareStatus.String().
		return "unknown"
	}
}

// MarshalText encodes an origin as its WORD, so encoding/json stores
// "overlay" rather than 1.
//
// That follows directly from the review cache having no expiry: the key IS the
// two files' content, so an entry stays reachable for as long as neither file
// changes, and there is no TTL that would ever retire it. An integer encoding
// would silently change meaning the day a constant is inserted into the block
// above — a note that said "upstream" would come back reading "both", years
// later, with nothing to flush it. A word survives a reordering.
func (o ReviewOrigin) MarshalText() ([]byte, error) {
	return []byte(o.String()), nil
}

// UnmarshalText decodes the word MarshalText wrote.
//
// An unrecognised word is an ERROR rather than a quiet fall back to
// OriginUnknown. The only thing that decodes one is the review cache, which
// reads a failed parse as a corrupt cache: warn once, treat every lookup as a
// miss, ask again. That costs one request. Degrading to OriginUnknown would
// instead keep a note whose classification has been erased — and, with no
// expiry, keep it forever — while printing no proposed declaration for a
// divergence that may well be ours.
func (o *ReviewOrigin) UnmarshalText(text []byte) error {
	switch string(text) {
	case "unknown":
		*o = OriginUnknown
	case "overlay":
		*o = OriginOverlay
	case "upstream":
		*o = OriginUpstream
	case "both":
		*o = OriginBoth
	default:
		return fmt.Errorf("unknown review origin %q", text)
	}
	return nil
}

// The review-outcome sentinels a DivergenceReviewer (or RealignReviewer) wraps
// to say why it failed. They are declared here, not in the package that runs
// the model, so this package reads no client's errors
// (TestOverlayImportsNoAutoupdate); cmd/ translates the client's outcome into
// these.
var (
	// ErrReviewTimedOut: the reviewer's own budget elapsed.
	ErrReviewTimedOut = errors.New("review timed out")
	// ErrReviewCouldNotStart: the reviewer's process never started.
	ErrReviewCouldNotStart = errors.New("review could not start")
	// ErrReviewExitedNonZero: the reviewer's process exited with a failure status.
	ErrReviewExitedNonZero = errors.New("review exited non-zero")
	// ErrReviewUnusableReply: the reviewer answered with nothing usable.
	ErrReviewUnusableReply = errors.New("review reply unusable")
)

// ReviewFailure is why a review did not come back. It sits BESIDE
// Reading == ReadingFailed and never splits that state. Its zero value, ReviewFailureNone, is every result whose
// review did not fail.
type ReviewFailure int

const (
	// ReviewFailureNone: no review failed.
	ReviewFailureNone ReviewFailure = iota
	// ReviewTimedOut: ErrReviewTimedOut.
	ReviewTimedOut
	// ReviewCouldNotStart: ErrReviewCouldNotStart.
	ReviewCouldNotStart
	// ReviewExitedNonZero: ErrReviewExitedNonZero.
	ReviewExitedNonZero
	// ReviewUnusableReply: ErrReviewUnusableReply, or a note that says nothing.
	ReviewUnusableReply
	// ReviewCancelled: the run was cancelled (context.Canceled, or the pass's
	// context was done before the review was asked).
	ReviewCancelled
	// ReviewEbuildUnreadable: the two ebuilds could not be re-read.
	ReviewEbuildUnreadable
	// ReviewOther: an error none of the above matches.
	ReviewOther
)

// String spells the cause in the report's vocabulary; the zero value spells
// nothing.
func (f ReviewFailure) String() string {
	switch f {
	case ReviewTimedOut:
		return "timed out"
	case ReviewCouldNotStart:
		return "could not start"
	case ReviewExitedNonZero:
		return "exited non-zero"
	case ReviewUnusableReply:
		return "empty or unusable reply"
	case ReviewCancelled:
		return "cancelled"
	case ReviewEbuildUnreadable:
		return "ebuild unreadable"
	case ReviewOther:
		return "other"
	}
	return ""
}

// classifyReviewError maps a reviewer's error to its ReviewFailure. It reads
// sentinels only, never the text: a sentence that says "ran out of time"
// without ErrReviewTimedOut is ReviewOther, and so is a bare
// context.DeadlineExceeded, because a deadline is not a cancellation. The four
// review sentinels are checked before context.Canceled, so an error carrying
// both reads as the sentinel's cause.
func classifyReviewError(err error) ReviewFailure {
	switch {
	case errors.Is(err, ErrReviewTimedOut):
		return ReviewTimedOut
	case errors.Is(err, ErrReviewCouldNotStart):
		return ReviewCouldNotStart
	case errors.Is(err, ErrReviewExitedNonZero):
		return ReviewExitedNonZero
	case errors.Is(err, ErrReviewUnusableReply):
		return ReviewUnusableReply
	case errors.Is(err, context.Canceled):
		return ReviewCancelled
	}
	return ReviewOther
}
