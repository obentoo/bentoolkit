package overlay

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/obentoo/bentoolkit/internal/common/provider"
)

// reviewCacheDirFor names the directory this pass caches notes in.
//
// It is a package var, like warnLogf next door and for the same narrow reason:
// the alternative is a test suite that reads and writes the developer's real
// ~/.cache/bentoo/compare, where one run's stored note would silently answer the
// next run's assertion about how many times a reviewer was called. Production
// never assigns it.
var reviewCacheDirFor = defaultReviewCacheDir

// AnnotateReviews attaches a model's reading to every undeclared divergence the
// report holds (R5.1) — where the difference came from, what it does, and, when
// it is ours, the `patched` text that would declare it (R5.2-R5.4).
//
// A NIL REVIEWER RETURNS IMMEDIATELY, and that is the point of the parameter's
// type. `--no-review` (R5.6) and "no `claude` on PATH" (R5.5) are two ways of
// having no reviewer, and a nil one gives them a single no-op path instead of
// two conditions in cmd/ that could disagree.
//
// It runs AFTER CompareWithProvider returns and is SEQUENTIAL. Inside the
// comparison it would inherit ten-way concurrency, which here means ten
// concurrent `claude` processes and a report whose commentary order depends on
// which review finished first. Eight invocations at a few seconds each is the
// measured overlay's whole cost, and if that ever stops being acceptable the
// answer is a bounded worker pool, not moving this back inside the comparison.
//
// It returns NOTHING, exactly as AnnotateAuthorship does. Every failure here is
// a way of having no reading — a reviewer that errored, ran out of time, or
// answered with nothing usable — and design.md's error table answers all of them
// with one warning naming the package and then the deterministic report (R5.5).
// None is fatal and none changes the exit status: the report the operator asked
// for is already complete without the commentary.
//
// Nothing it writes can change a Verdict, a count or the grouping (R5.8). It
// writes exactly one field, Review, onto results the comparison has finished
// with, and reads no other.
//
// It writes NO FILE (R5.4). The declaration it may attach is a PROPOSAL for the
// operator to apply: the overlay repository auto-commits and pushes within
// minutes, so a declaration written here would be published before anyone could
// read it.
//
// _Requirements: R5.1, R5.2, R5.3, R5.4, R5.5, R5.8_
func AnnotateReviews(report *CompareReport, reviewer DivergenceReviewer, prov provider.Provider, opts CompareOptions) {
	if report == nil || reviewer == nil {
		return
	}

	// The findings R5.1 submits, collected BEFORE anything is opened. The
	// predicate is the report's own (isUndeclaredDivergence, compare.go), so the
	// set the model sees cannot drift from the set the report warns about; and
	// knowing the set is empty here is what keeps a run with nothing to review
	// from touching the cache at all — and therefore from warning about one.
	var pending []int
	for i := range report.Results {
		if isUndeclaredDivergence(report.Results[i]) {
			pending = append(pending, i)
		}
	}
	if len(pending) == 0 {
		return
	}

	// The caller's context, and NO deadline of this pass's own. A review is a
	// network round trip behind a CLI, so cancelling the compare must abort it —
	// that is what this carries. The per-invocation TIMEOUT belongs to the
	// adapter, which knows what it is invoking: autoupdate.ClaudeCodeClient
	// already wraps every call in one (DefaultClaudeCodeTimeout, 120s). A second
	// deadline here would be a second thing to tune for one round trip, and R5.5's
	// "exceeds its timeout" needs none — an expired deadline surfaces as an error
	// from ReviewDivergence and is handled below with every other error.
	ctx := opts.Ctx
	if ctx == nil {
		ctx = context.Background() // SAFE: CompareOptions.Ctx is an additive field; nil means "no cancellation requested", exactly as CompareWithProvider reads it
	}

	// ONE cache for the run. Two would warn twice about the same broken file, and
	// the "once per run" guard inside a reviewCache is scoped to the value.
	//
	// newReviewCache("") is a deliberately silent in-memory-only cache, so the
	// reason it has nowhere to live is stated HERE or nowhere: the caller that
	// could not name a directory is the one holding the reason.
	dir, err := reviewCacheDirFor()
	if err != nil {
		warnLogf("overlay: the review notes have nowhere to be cached (%v); this run still reviews every divergence, stores nothing, and the next one will ask again", err)
		dir = ""
	}
	cache := newReviewCache(dir)

	for n, i := range pending {
		// Indexed rather than ranged over a copy: this pass exists to write one
		// field back onto the report the caller is holding.
		r := &report.Results[i]
		atom := r.Category + "/" + r.Package

		if err := ctx.Err(); err != nil {
			// Ctrl-C. Every remaining call would fail on this same context, so
			// carrying on would bury the report under one identical warning per
			// package. Stopping leaves the rest carrying the zero note, which reads
			// as "nothing was said" — which is true.
			warnLogf("overlay: the review stopped (%v); %d of %d undeclared divergences carry no reading, and the report is otherwise complete",
				err, len(pending)-n, len(pending))
			return
		}

		req, ok := reviewRequestFor(*r, prov, opts)
		if !ok {
			// Rare by construction — the comparison read both of these files a
			// moment ago to conclude they differ — so this is a file that moved
			// underneath the run, and the operator is better told than left
			// wondering why one finding carries no reading.
			warnLogf("overlay: the two %s ebuilds for %s could not be re-read for review; its finding carries no reading, and the report is otherwise complete",
				r.LocalVersion, atom)
			continue
		}

		// A cached note is the same question already answered (R5.7): the key is
		// the two files' content, so while neither has changed the answer cannot
		// have. An entry that says nothing is treated as a miss — a hand-edited or
		// half-written one must not suppress a question forever, and nothing here
		// expires.
		if note, hit := cache.get(req); hit && reviewNoteSpeaks(note) {
			r.Review = note
			continue
		}

		note, err := reviewer.ReviewDivergence(ctx, req)
		if err != nil {
			// The error is an ARGUMENT, never a format string: it may carry a
			// model's or a CLI's own text.
			warnLogf("overlay: the review of %s failed (%v); its finding carries no reading, and the report is otherwise complete", atom, err)
			continue
		}
		if !reviewNoteSpeaks(note) {
			warnLogf("overlay: the review of %s came back without a classification or a summary; its finding carries no reading, and the report is otherwise complete", atom)
			continue
		}

		r.Review = note
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

	ours, err := os.ReadFile(paths.ourEbuild()) //nolint:gosec // path built from scanned overlay directory names, never from registry input
	if err != nil {
		return ReviewRequest{}, false
	}
	theirs, err := os.ReadFile(paths.upstreamEbuild()) //nolint:gosec // path resolved by the provider from scanned directory names, never from registry input
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
// never be one the report ignores. A note needs both halves: R5.2 asks which
// side the divergence came from and R5.3 asks what it does, and a reply missing
// either has answered neither — OriginUnknown is the value that means "nobody
// said", not a classification.
//
// The whitespace trim matters because a model's "one line" can be a newline.
func reviewNoteSpeaks(note ReviewNote) bool {
	return note.Origin != OriginUnknown && strings.TrimSpace(note.Summary) != ""
}

// DivergenceReviewer is the seam through which a model's reading of one
// divergence reaches this package. It is the whole of what internal/overlay
// knows about an LLM: a function that takes two ebuilds and returns three
// strings.
//
// It is declared HERE, in the consumer, rather than as a method on
// autoupdate.LLMProvider, for two separate reasons that both bind. A method on
// LLMProvider would force openai.go and ollama.go to implement a review they
// will never serve; and holding one would make this package name an autoupdate
// type, which is the import edge 025 R2.4 keeps absent in BOTH directions
// (review_test.go fences it). The project already has the shape for a capability
// declared by its consumer — provider.PackageDirProvider, which
// resolvePackagePaths discovers by type assertion — and this follows it.
//
// The only production implementation is an adapter over
// autoupdate.NewClaudeCodeClient, and it lives in
// cmd/bentoo/overlay_compare_review.go: cmd/ already imports both halves, so the
// one new edge sits where an edge already exists.
//
// The CONTEXT is the caller's. A review is a network round trip behind a CLI, so
// a cancelled compare must abort it rather than hold the run open — the same
// spine CompareOptions.Ctx carries through the comparison itself.
//
// A nil DivergenceReviewer is not an error anywhere. It is how `--no-review`
// (R5.6) and an absent `claude` CLI (R5.5) reach one no-op path instead of two.
type DivergenceReviewer interface {
	ReviewDivergence(ctx context.Context, req ReviewRequest) (ReviewNote, error)
}

// ReviewRequest is one divergence put to a reviewer: which package, at which
// version, and the two ebuilds that differ.
//
// It carries the two files' BYTES and never the line counts beside them. That is
// R1.3 held at the type: a prompt assembled from the SIZE of a difference would
// be a computation on that size, which is exactly what the counts may never feed
// — see compare_diff_counts_fence_test.go, which fences DiffAdded/DiffRemoved to
// the one function that fills them and the one that prints them.
//
// The bytes are also what the review cache keys on (review_cache.go), so the
// request holds precisely what the answer depends on and nothing else.
type ReviewRequest struct {
	// Category, Package and Version identify the package for the reviewer's
	// prose. They are NOT part of the cache's index: the classification is a
	// reading of the two files, and R5.7 keys it on their content alone.
	Category, Package, Version string
	// Ours and Theirs are the two ebuilds at Version — ours from the overlay,
	// theirs from ::gentoo — read through resolvePackagePaths, the one path
	// construction in this package. The ORDER is meaningful: Origin below names a
	// side, so a request with the two swapped would invert the answer.
	Ours, Theirs []byte
}

// ReviewNote is a model's reading of one divergence: COMMENTARY, printed beside
// a finding, and never an input to anything the report decides (R5.8). The same
// package grouping, the same Verdicts and the same removal recommendations hold
// whether or not a review ran.
//
// Its two prose fields are operator-facing text a MODEL produced. They are
// printed as ARGUMENTS and never as a format string — like every other piece of
// report text — they reach no shell and no command, and Declaration is capped on
// the line the way PatchedReason is (patchedReasonCap).
//
// Nothing here is ever written to a file. R5.4 PROPOSES declaration text and the
// operator applies it: the overlay repository auto-commits and pushes within
// minutes, so a declaration written by this program would be published before
// anyone could read it.
//
// The JSON tags exist because the review cache persists this type
// (review_cache.go), and they are the same three lowercase names the CLI adapter
// in cmd/ decodes from the model's reply, so one spelling serves both.
type ReviewNote struct {
	// Origin is where the divergence came from, as the model read it (R5.2).
	Origin ReviewOrigin `json:"origin"`
	// Summary is one line saying what the divergence does (R5.3).
	Summary string `json:"summary"`
	// Declaration is proposed `patched` text for the registry (R5.4). It is
	// empty unless Origin is OriginOverlay: there is nothing of ours to declare
	// about a change that is not ours. It is a PROPOSAL — text on a terminal,
	// never a file.
	Declaration string `json:"declaration"`
}

// ReviewOrigin is which side a divergence came from, as the model classified it
// (R5.2). It is the model's reading and not a finding of this package's own:
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
	// and here it has a second edge: R5.4 attaches a proposed `patched`
	// declaration to an OVERLAY-origin note. Had OriginOverlay been the zero
	// value, every un-reviewed finding would both accuse us of the change and
	// invite a declaration of it.
	OriginUnknown ReviewOrigin = iota
	// OriginOverlay means the divergence is work of ours, carried on top of
	// ::gentoo's ebuild. This is the classification R5.4 proposes a declaration
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
// divergence that may well be ours (R5.4).
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
