package overlay

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	udiff "github.com/aymanbagabas/go-udiff"
	"github.com/obentoo/bentoolkit/internal/common/ebuild"
	"github.com/obentoo/bentoolkit/internal/common/github"
	"github.com/obentoo/bentoolkit/internal/common/provider"
	"github.com/obentoo/bentoolkit/internal/common/secrets"
)

// DefaultCompareConcurrency is the number of packages CompareWithProvider
// processes in parallel when CompareOptions.Concurrency is not set (<= 0).
const DefaultCompareConcurrency = 10

// CompareResult represents the result of comparing a package between overlays
type CompareResult struct {
	Category      string
	Package       string
	LocalVersion  string // Version in Bentoo overlay
	RemoteVersion string // Version in Gentoo repository
	Status        CompareStatus

	// The fields below carry the second axis: not what the two versions are —
	// that is Status, and it is unchanged — but what is known about the package
	// and what that implies. They are filled from CompareOptions.Divergence,
	// which the caller supplies; this package reads no registry of its own.

	// Verdict is the recommendation for this package.
	Verdict Verdict
	// Patched reports that a registry entry declares a divergence worth keeping.
	Patched bool
	// PatchedBy names the registry key that declared it, so a package with
	// several entries says which one is speaking.
	PatchedBy string
	// PatchedReason is the declared text. It is rendered on the declaration line
	// beneath the section (formatVerificationFindings' third case), capped at
	// patchedReasonCap — unlike PatchedBy, which is printed whole.
	PatchedReason string
	// Verified is the outcome of comparing the two ebuilds' bytes. It stays
	// NotVerified unless a content check ran.
	Verified Verification
	// DiffAdded and DiffRemoved are the line counts of that difference, ours
	// against upstream's: added is what our ebuild has and ::gentoo's does not.
	// They are meaningful ONLY when Verified == VerifiedDiffers and are zero
	// otherwise — a check that did not run has no magnitude to report, and a
	// byte-identical pair has none to find.
	//
	// They exist because the bare fact "differs" cannot be acted on. A one-line
	// difference is almost always ::gentoo revising an ebuild in place, without a
	// revbump, after we copied it; a four-hundred-line one is work of our own. The
	// report cannot tell those two apart — see formatVerificationFindings — but
	// the operator can, at a glance, once the size is on the line.
	DiffAdded   int
	DiffRemoved int

	// Authorship is what the overlay's own CONTENT proves about where the
	// difference came from. The two ebuilds' bytes cannot say it — they are
	// symmetric — but the files/ tree beside them can, so this is filled by
	// AnnotateAuthorship (authorship.go) after the comparison, never by it.
	//
	// AuthorshipUnproved, the zero value, means THE REPORT CANNOT TELL. It is
	// never a finding of "the change is upstream's": ::gentoo revises ebuilds in
	// place without a revbump, so a difference nothing here proves may still be
	// entirely ours and merely unprovable from the files. Reading unproved as
	// "not ours" would authorise exactly the removal this check exists to stop.
	Authorship Authorship
	// ProvedBy names the file that proves it, relative to the package directory
	// ("files/<name>"), so the operator can confirm the claim by looking instead
	// of re-deriving the path from what ${FILESDIR} expands to (R2.2). It is
	// empty whenever Authorship is unproved: there is then no file to name.
	ProvedBy string
	// Review is a MODEL's reading of an undeclared divergence: printed beside the
	// finding and never an input to anything this report decides (R5.8). It sits
	// on the result for the reason Authorship does — the pass that fills it
	// (AnnotateReviews, review.go) runs after the comparison and writes back onto
	// the report the caller is holding — and it is deliberately a THIRD field
	// beside Authorship rather than a widening of it: Authorship is what the
	// overlay's content PROVES, this is what a model SAYS, and a type that could
	// hold either would let one be read as the other.
	//
	// The zero ReviewNote means NOTHING WAS SAID, exactly as AuthorshipUnproved
	// does: every result no review reached — every `--no-review` run, every run
	// with no `claude` on PATH, every package whose reviewer errored, and every
	// finding that is not an undeclared divergence — carries it, and the renderer
	// prints nothing for it.
	Review ReviewNote
	// Reading is WHETHER ANYBODY READ this result, and when nobody did, why
	// not. It is a FOURTH field rather than something derived from the three
	// above for the reason the doc on Review just gave: the zero ReviewNote
	// covers four unrelated causes at once, so "is Review empty?" answers "no
	// review reached this" and cannot answer "did anybody read this?" — a
	// package nobody asked about and a package whose reviewer was killed both
	// carry the zero note, and only this field tells them apart (S047-R3.1).
	//
	// It is filled by AnnotateReviews (review.go), the same pass that fills
	// Review (S047-R4.1). Its zero value, ReadingNotRequested, is what every
	// result that pass never reached already carries, so a run with no review
	// is correct without anything being set.
	Reading Reading

	// LookupCause is why the upstream lookup failed. It is meaningful only when
	// Status == StatusError and is LookupCauseNone otherwise: a lookup that
	// succeeded has no cause to report.
	LookupCause LookupCause
	// ReviewFailure is why the review of this result did not come back. It is
	// meaningful only when Reading == ReadingFailed and is ReviewFailureNone
	// otherwise; Reading keeps its four values. Filled by AnnotateReviews.
	ReviewFailure ReviewFailure
	// FailureText is the full Error() text of whatever failed on this row —
	// the upstream lookup when Status == StatusError, the review when Reading
	// == ReadingFailed — after every CompareOptions.Redact value has been
	// scrubbed from it. It is empty on a row with no failure. It is kept whole
	// here; only a renderer's table cell may cut it.
	FailureText string

	// The fields below carry the BASELINE REVIEW: what our ebuild was measured
	// against, and what that measurement found. They are filled by one pass,
	// AnnotateBaseline (annotate_baseline.go), which runs after the comparison
	// and ONLY when the review was requested — exactly as AnnotateAuthorship and
	// AnnotateReviews already do for the two fields above.
	//
	// EVERY ONE OF THEM RENDERS NOTHING AT ITS ZERO VALUE, and that is not a
	// nicety: FormatReport takes a *CompareReport and never learns which flags
	// were passed, so a zero value is the only mechanism by which `overlay
	// compare` without a review can keep printing exactly what it printed
	// yesterday (R7.2). A field read by no renderer would satisfy that promise
	// and empty it of meaning, so each of these is rendered — see
	// formatBaselineFindings.

	// Baseline is the ::gentoo ebuild this package was measured against (R1.1).
	// Its zero value is "no review ran, or ::gentoo does not carry this
	// package"; the run-level NoBaselineCount below is what turns the second of
	// those into a number, because the two are indistinguishable per package by
	// construction.
	Baseline Baseline
	// Axes are the structural differences between our ebuild and that baseline
	// — inherit, options, IUSE, dependencies (R2.4). A nil slice means nothing
	// was compared or nothing differs, which is the same thing to a report that
	// prints only what it found.
	Axes []AxisFinding
	// Classified is what the three-way reduction concluded about the
	// differences: how many fell into each class and how much evidence it had
	// (R2.4, R2.5).
	Classified Classified
	// Declarations are the `# BENTOO-DIVERGENCE:` tags our ebuild carries, with
	// Expired decided against the ::gentoo tree (R3.1, R3.3). They are the
	// EBUILD axis and never CompareOptions.Divergence, which is the registry
	// one.
	Declarations []DeclaredDivergence
	// RealignVerdict is a MODEL's opinion on whether an undeclared divergence is
	// still justified, and why (R4.1). It is written by the realignment reviewer
	// (task 5.1) and never by AnnotateBaseline: a verdict nobody produced is
	// worse than none, so an unreachable model leaves it empty (R4.4).
	//
	// It is COMMENTARY. Nothing that decides a Verdict or an exit code may read
	// it (R4.3), on the same fence DiffAdded/DiffRemoved already sit behind.
	RealignVerdict string
	// Others are the repositories other than ::gentoo that carry this package,
	// for the 84 of 321 packages ::gentoo does not (R6.1). They are INFORMATIVE
	// ONLY and no realignment is ever proposed from one (R6.2): a repository
	// outside ::gentoo has not been through the same review.
	//
	// They are filled by task 6.2's OtherRepositories, which needs the list of
	// locally available repositories — something AnnotateBaseline is not given
	// and deliberately does not go looking for.
	Others []OtherRepo
}

// CompareStatus indicates the comparison result
type CompareStatus int

const (
	// StatusUpToDate means local version equals remote version
	StatusUpToDate CompareStatus = iota
	// StatusOutdated means local version is older than remote
	StatusOutdated
	// StatusNewer means local version is newer than remote
	StatusNewer
	// StatusNotInRemote means package doesn't exist in remote
	StatusNotInRemote
	// StatusError means an error occurred during comparison
	StatusError
)

// String returns a human-readable status
func (s CompareStatus) String() string {
	switch s {
	case StatusUpToDate:
		return "up-to-date"
	case StatusOutdated:
		return "outdated"
	case StatusNewer:
		return "newer"
	case StatusNotInRemote:
		return "not-in-remote"
	case StatusError:
		return "error"
	default:
		return "unknown"
	}
}

// Divergence is what the caller knows about one atom's relationship to the
// upstream repository. It is supplied by the caller: this package never reads
// packages.toml, so internal/overlay keeps zero import edges to
// internal/autoupdate in either direction.
//
// There is deliberately no Known field. Absence from the caller's map IS the
// unknown state, and it reaches deriveVerdict as its known parameter — a
// boolean stored beside the map would be a second copy of what the lookup
// already reports, and a second thing to desynchronise.
type Divergence struct {
	// Patched reports that at least one registry entry for this atom declares
	// a divergence that must survive a bump.
	Patched bool
	// Reason is the declared text. It reaches the report through
	// CompareResult.PatchedReason and is capped there, not here: this type
	// carries what the registry said, and the width of a terminal is not its
	// concern.
	Reason string
	// Entry names the registry key that declared it, so a package with several
	// entries says which one is speaking.
	Entry string
}

// Verdict is the recommendation for one overlay package: not what its versions
// are (that is CompareStatus) but what should be done about it. The two are
// separate axes — a package can be up-to-date with ::gentoo and still worth
// keeping, precisely because it carries a divergence.
type Verdict int

const (
	// VerdictKeep means the overlay's copy earns its place.
	VerdictKeep Verdict = iota
	// VerdictRedundant means ::gentoo delivers the same or more and we changed
	// nothing, so the overlay copy is a removal candidate.
	VerdictRedundant
	// VerdictNeedsRebase means ::gentoo moved ahead and we carry changes that
	// must be re-applied on top of the newer ebuild.
	VerdictNeedsRebase
	// VerdictUnknown means nothing is recorded about this package, or the
	// comparison itself failed. Either way no recommendation is supported.
	VerdictUnknown
)

// String returns the report's word for the verdict.
func (v Verdict) String() string {
	switch v {
	case VerdictKeep:
		return "keep"
	case VerdictRedundant:
		return "redundant"
	case VerdictNeedsRebase:
		return "needs-rebase"
	case VerdictUnknown:
		return "unknown"
	default:
		// A value with no word — only reachable if a verdict is added without a
		// case here — reads as "unknown" rather than as a bare integer, matching
		// CompareStatus.String() above.
		return "unknown"
	}
}

// Verification is the tri-state a content check needs: a Verdict confirmed
// against the two ebuilds' bytes reads differently from one taken on trust.
// The check that produces it is wired in separately; the vocabulary lives here
// with the rest of the compare types.
type Verification int

const (
	// NotVerified means no content was compared: no local copy, no upstream
	// copy at that version, an unreadable file, or versions that differ.
	NotVerified Verification = iota
	// VerifiedIdentical means the two ebuilds are byte-for-byte equal.
	VerifiedIdentical
	// VerifiedDiffers means the two ebuilds differ.
	VerifiedDiffers
)

// Authorship is what the two package directories prove about WHO WROTE the
// difference — a different question from Verification, which only says that a
// difference exists. Verification compares two symmetric files and so can never
// answer it; the overlay's files/ tree sometimes can.
//
// The vocabulary lives here with the rest of the compare types, exactly as
// Verification does, while the pass that fills it is wired in separately
// (AnnotateAuthorship, authorship.go).
type Authorship int

const (
	// AuthorshipUnproved means nothing in the content proves where the
	// difference came from. It is the ZERO VALUE by construction, so every
	// result nobody examined — the identical ones, every API-only run — reads as
	// "cannot tell" rather than as a claim about anybody. It is NEVER "the
	// change is upstream's" (R2.3).
	AuthorshipUnproved Authorship = iota
	// AuthorshipOverlay means our ebuild references a file under ${FILESDIR}
	// that ::gentoo does not provide for that package. A reference to a patch
	// upstream never had cannot have been inherited from upstream, which is the
	// one thing two ebuilds and two directories can prove between them (R2.1).
	AuthorshipOverlay
)

// Reading is WHETHER ANYBODY EXPLAINED the difference — a third question again,
// and separate from both its neighbours above on purpose. Verification says
// whether the two ebuilds DIFFER; Authorship says what the content proves about
// who wrote the difference; Reading says whether a reading of it was even
// attempted, and when it was not, why not.
//
// It is a type of its own because CompareResult.Review cannot answer that
// question. The zero ReviewNote covers four unrelated causes at once — a
// `--no-review` run, no `claude` on PATH, a reviewer that errored, and a
// finding that is not an undeclared divergence — so reading "Review is empty"
// as "nobody looked" reports the same thing for a package nobody was ever asked
// about and for one whose review was killed mid-flight. That conflation is the
// defect this vocabulary exists to remove (S047-R3.1), and collapsing Reading
// back into Verification or into Review would reproduce it exactly.
//
// The vocabulary lives here with the rest of the compare types, exactly as
// Verification and Authorship do, while the pass that fills it is wired in
// separately (AnnotateReviews, review.go).
type Reading int

const (
	// ReadingNotRequested means no reading was ever asked for: no reviewer was
	// configured, or the run passed `--no-review`.
	//
	// It is the ZERO VALUE by construction, the same way AuthorshipUnproved is,
	// so every result no annotation pass reached reads as "not requested"
	// without anyone setting it. That is what keeps "nobody asked" to ONE
	// spelling: were it placed anywhere but first, an untouched result and an
	// annotated one would disagree about the same fact, and a consumer counting
	// unread packages would be right about some of them and silently wrong
	// about the rest.
	ReadingNotRequested Reading = iota
	// ReadingNotComparable means the content check refused the pair, so there
	// was never a difference to hand a reader in the first place — one of the
	// causes NotVerified above lists. It is NOT a failure: nothing was asked of
	// anybody, and nobody let anybody down.
	ReadingNotComparable
	// ReadingFailed means a reading WAS attempted and did not come back — the
	// reviewer errored, timed out, or was killed.
	//
	// Holding it apart from ReadingNotComparable is the point of the type. Both
	// end with no note on the result, so anything that reports only that
	// absence says the same thing about a pair nothing could compare and about
	// a review that died mid-flight — and only the second is a run worth
	// repeating.
	ReadingFailed
	// ReadingDone means a reading returned a usable note.
	ReadingDone
)

// deriveVerdict maps the two axes — the version comparison and what the
// registry records about the package — onto exactly one recommendation. It is
// a pure, total function of its arguments (no I/O, no receiver, no error path)
// so the whole policy is one readable table instead of conditions scattered
// through the report.
//
// known is false when the caller's divergence map has no entry for the atom.
func deriveVerdict(status CompareStatus, div Divergence, known bool) Verdict {
	// Two whole slices of the table collapse before the per-status rules are
	// consulted, and they are stated here — first, separately — so that a later
	// edit to the switch below cannot quietly weaken them:
	//
	//   - nothing recorded about the package: silence is not "not patched", and
	//     reading it that way would confidently recommend deleting an ebuild
	//     nobody has described yet;
	//   - the comparison failed: there is no version relationship to reason from.
	//
	// Neither can ever yield VerdictRedundant, which is the only verdict that
	// recommends removing something.
	if !known || status == StatusError {
		return VerdictUnknown
	}

	switch status {
	case StatusUpToDate:
		// ::gentoo ships the same version, so the copy earns its place only if
		// it carries a divergence.
		if div.Patched {
			return VerdictKeep
		}
		return VerdictRedundant
	case StatusOutdated:
		// ::gentoo moved ahead: with no divergence its ebuild supersedes ours;
		// with one, ours owes a bump plus a re-application of the delta.
		if div.Patched {
			return VerdictNeedsRebase
		}
		return VerdictRedundant
	case StatusNewer, StatusNotInRemote:
		// Ahead of ::gentoo, or ours alone — being ahead or unique is itself the
		// reason the overlay copy exists, patched or not.
		return VerdictKeep
	default:
		// Unreachable while CompareStatus has its five values. A sixth added
		// later must not silently inherit a recommendation.
		return VerdictUnknown
	}
}

// CompareOptions configures the comparison behavior
type CompareOptions struct {
	// OnlyOutdated filters results to only show outdated packages
	OnlyOutdated bool
	// IncludeSynced includes packages that have the same version (up-to-date)
	// When true, StatusUpToDate packages are included in results
	// This is independent of OnlyOutdated - both can be combined
	IncludeSynced bool
	// IncludeNotInRemote includes packages that don't exist in remote
	IncludeNotInRemote bool
	// ProgressCallback, when non-nil, is invoked once per package as that
	// package's comparison completes. done is the cumulative count of packages
	// finished so far and total is the number of packages in the batch.
	// Because CompareWithProvider runs packages concurrently, the callback may
	// fire from multiple goroutines and the per-invocation order is not
	// deterministic; done is sourced from an atomic counter, so the value
	// observed by any single invocation is monotone non-decreasing.
	ProgressCallback func(done, total uint64)
	// Concurrency bounds the number of packages CompareWithProvider processes
	// in parallel. A value <= 0 is treated as DefaultCompareConcurrency.
	Concurrency int
	// Ctx is the parent context for the comparison. It originates in cmd/
	// (signal.NotifyContext), so cancelling it aborts an in-flight comparison.
	// When nil it is treated as context.Background() by the consumer.
	Ctx context.Context
	// Divergence is what the caller knows about each package, keyed by the bare
	// "category/package" atom. The caller builds it once, before the comparison
	// starts, and the comparison only reads it — so it is shared across the
	// per-package goroutines without a lock.
	//
	// Absence from the map IS the unknown state: nothing is recorded about that
	// package, which is not the same as "recorded as not patched". A nil map
	// therefore says nothing is known about anything, which is exactly the
	// degraded mode when the registry cannot be read — and costs no special case.
	Divergence map[string]Divergence
	// OverlayPath is the root of the overlay the compared packages were scanned
	// from. PackageInfo carries only Category/Package/Versions/LatestVersion, so
	// a check that must open the local ebuild gets its overlay-side root here.
	OverlayPath string
	// Redact lists values removed from any recorded failure text with
	// secrets.Scrub, so a token echoed inside an error reaches no report. nil
	// redacts nothing; an empty entry is ignored.
	Redact []string
}

// CompareReport contains the full comparison report
type CompareReport struct {
	TotalPackages    int
	ComparedPackages int
	OutdatedCount    int
	NewerCount       int
	UpToDateCount    int
	NotInRemoteCount int
	ErrorCount       int
	Results          []CompareResult

	// Findings is everything the run established for the operator, as VALUES
	// rather than as the lines this package used to compose and colour
	// (S046-R5.1, R5.2). One entry per compared package says how its two
	// versions relate; a package that also carries a divergence, a stale
	// declaration or a registry declaration carries a second entry saying so.
	//
	// # It is a MATERIALISED VIEW of Results, and that is what keeps it honest
	//
	// compareFindings is a pure function of the results, and this field holds
	// nothing that function would not produce again from the same slice. Two
	// places refresh it: CompareWithProvider, once the results are sorted, and
	// EstablishFindings, which the caller runs after the annotation passes have
	// written Authorship, Review and the rest back onto the report it is
	// holding. A finding built at comparison time cannot know what a pass that
	// had not run yet would discover, and a stale list is worse than none: it
	// would report an undeclared divergence as unproved after the content had
	// proved it ours.
	//
	// # The ORDER is information
	//
	// Findings follow Results, which CompareWithProvider sorts by
	// category/package. A set-shaped answer would leave every renderer to invent
	// an order of its own, which is how two modes come to disagree about what one
	// run said.
	//
	// # It is the COMPUTATION, not the view
	//
	// A caller that narrows Results for display — `overlay compare` does, for
	// --only-redundant and --only-patched — narrows what its TABLE shows and
	// nothing this field says, exactly as it narrows none of the counts above.
	// The findings are what the comparison established about the overlay; which
	// rows the operator asked to look at is a different question.
	Findings []Finding

	// Per-Verdict counts sit ALONGSIDE the per-Status counts above and never
	// replace or restate them: the two answer different questions ("how do the
	// versions relate?" vs "what should be done about it?"), so every compared
	// package is counted exactly once on each axis. Deriving either from the
	// other would silently change what the existing summary lines mean.
	VerdictKeepCount        int
	VerdictRedundantCount   int
	VerdictNeedsRebaseCount int
	VerdictUnknownCount     int

	// BaselineSkipped is non-empty when the review could not run at all: no
	// ::gentoo tree could be located, so NOTHING was examined. The text names
	// what was looked for (R1.5), and MarkBaselineSkipped is what writes it.
	//
	// It is the report's only RUN-level state, and it is deliberately not the
	// per-package one. A baseline that exists but will not read is
	// Baseline.Unexamined on that package alone (D2) and never reaches here:
	// collapsing the two would have one unreadable ebuild declare that a run
	// which examined 320 packages examined none.
	//
	// Its ZERO VALUE renders nothing (formatBaselineSkipped), which is what keeps
	// a run that requested no review printing exactly what it printed yesterday
	// (R7.2). Without it the same run prints "All packages are up-to-date!" over
	// a comparison that never happened.
	BaselineSkipped string

	// NoBaselineCount is how many results ::gentoo carries no version of at all
	// (R6.4) — 84 of 321 packages on the measured overlay. It is written by
	// AnnotateBaseline and is, by construction, the number of Results whose
	// Baseline.Found is false: any second definition would be a second answer to
	// one question.
	//
	// It is a RUN-level count because the per-package fact cannot be printed. A
	// package ::gentoo does not carry has the ZERO Baseline, which is also what
	// a package no review examined has, so a per-package line would say "no
	// baseline" over every row of a plain `overlay compare`. Counted here it is
	// reported once, with its denominator, and renders nothing at 0 (R7.2).
	NoBaselineCount int

	// RealignAsked and RealignNoVerdict are the realignment review's own
	// arithmetic: how many divergences were put to the model, and how many of
	// those came back with no verdict (R4.4). Both are written by
	// AnnotateRealignVerdicts and by nothing else.
	//
	// The second exists because an unreachable model is EXIT 0 (D9) and leaves an
	// EMPTY RealignVerdict on every package it could not judge — which renders as
	// silence, and silence reads as "every divergence was judged and none
	// objected". This is the number that says otherwise, and the first is the
	// denominator without which it cannot be read: 12 unjudged out of 12 and 12
	// out of 237 are different reports.
	//
	// They are RUN-level for the reason NoBaselineCount is: the per-package fact
	// is an absent field, indistinguishable from the same absent field on every
	// row of a plain `overlay compare`. Counted here they are stated once and
	// render nothing at 0 (R7.2).
	RealignAsked     int
	RealignNoVerdict int
	// RealignNoVerdictBy splits RealignNoVerdict by why each divergence came
	// back with no verdict, in the divergence review's words (ReviewFailure).
	// Its values sum to RealignNoVerdict. It is nil when nothing went
	// unanswered, and like the two counts above it is written only by
	// AnnotateRealignVerdicts.
	RealignNoVerdictBy map[ReviewFailure]int

	// Interrupted reports that the run stopped dispatching before it reached the
	// end of the package list it was handed: the context fired mid-scan, so some
	// packages were never compared at all.
	//
	// It is the one RUN-level fact CompareWithProvider writes itself. The three
	// above are written by annotation passes that run after it returns; this one
	// is knowable only inside the dispatch loop, the only code that sees the
	// difference between "the list ended" and "we stopped".
	//
	// # Complete is !Interrupted, and NEVER NotEvaluated == 0
	//
	// A run cut short in the instant after its LAST package completed lost
	// nothing to look at: the gap below is zero and the run was still cut short.
	// Deriving completeness from that gap would tell the operator who pressed
	// ctrl+c that their scan ended normally. ManifestResult.Interrupted carries
	// the same fact for the same reason, and the report envelope negates this
	// field rather than reading any count (S047-R5.1).
	//
	// # The unreached gap is TotalPackages - ComparedPackages, exactly
	//
	// ComparedPackages++ runs once per result reaching the collector: before the
	// switch that splits results by Status, and outside the include filter that
	// decides what lands in Results. So StatusNotInRemote and StatusError are
	// counted like any other status, and a row filtered out of Results is
	// counted too. A package missing from that number is therefore a package
	// whose worker never ran, and the subtraction has nothing else in it
	// (S047-R5.2).
	//
	// # False is the safe zero value, which is why the field is not named Complete
	//
	// A CompareReport built by hand — in a test, or by a caller assembling one —
	// gets false, and false here means "not interrupted". A Complete bool would
	// default to "this run was cut short" and quietly draw the interrupted block
	// over every such report.
	Interrupted bool
}

// LookupCause is why an upstream lookup failed: a closed vocabulary, so an
// operator can tell "wait for the rate limit" from "fix the token" from "check
// the network" without reading the error text.
type LookupCause int

const (
	// LookupCauseNone is the zero value: no lookup failed.
	LookupCauseNone LookupCause = iota
	// LookupRateLimited: the host refused for rate limiting (provider.ErrRateLimit).
	LookupRateLimited
	// LookupAuth: the host rejected the credentials (provider.ErrUnauthorized).
	LookupAuth
	// LookupNetwork: the request never got an HTTP answer (a net.Error).
	LookupNetwork
	// LookupNotFound: the package is absent upstream (provider.ErrNotFound).
	// comparePackageVersions turns that into StatusNotInRemote first, so it is
	// reachable only through classifyLookupError itself.
	LookupNotFound
	// LookupOther: any failure none of the above matches.
	LookupOther
)

// String returns the cause's word; see lookupCauseWord.
func (c LookupCause) String() string { return lookupCauseWord(c) }

// lookupCauseWord spells a LookupCause in the report's vocabulary. The zero
// value spells nothing.
func lookupCauseWord(c LookupCause) string {
	switch c {
	case LookupRateLimited:
		return "rate-limited"
	case LookupAuth:
		return "auth"
	case LookupNetwork:
		return "network"
	case LookupNotFound:
		return "not found upstream"
	case LookupOther:
		return "other"
	}
	return ""
}

// classifyLookupError maps a failed GetPackageVersions to its LookupCause. It
// reads sentinels and types only, never the text: an error that merely reads
// like a rate limit is "other". A 401 matches ErrAPIError too, which is why
// only the specific sentinels are tested and ErrAPIError falls to "other".
func classifyLookupError(err error) LookupCause {
	var netErr net.Error
	switch {
	case errors.Is(err, provider.ErrRateLimit):
		return LookupRateLimited
	case errors.Is(err, provider.ErrUnauthorized):
		return LookupAuth
	case errors.As(err, &netErr):
		return LookupNetwork
	case errors.Is(err, provider.ErrNotFound):
		return LookupNotFound
	}
	return LookupOther
}

// redactFailure scrubs every value in values out of s with secrets.Scrub.
func redactFailure(s string, values []string) string {
	for _, v := range values {
		s = secrets.Scrub(s, v)
	}
	return s
}

// githubProviderAdapter adapts a *github.Client to the provider.Provider interface,
// allowing Compare() to delegate to CompareWithProvider().
type githubProviderAdapter struct {
	client *github.Client
}

// GetPackageVersions returns all ebuild versions for a package via the GitHub client.
// Maps github.ErrNotFound to provider.ErrNotFound for interface compatibility,
// and every other github sentinel to its provider counterpart without changing
// the client's text (see `type translatedErr`).
func (a *githubProviderAdapter) GetPackageVersions(category, pkg string) ([]string, error) {
	versions, err := a.client.GetPackageVersions(category, pkg)
	switch {
	case err == nil:
		return versions, nil
	case errors.Is(err, github.ErrNotFound):
		return nil, provider.ErrNotFound
	case errors.Is(err, github.ErrRateLimit):
		return nil, translatedErr{err: err, sentinel: provider.ErrRateLimit}
	// A 401 matches both ErrUnauthorized and ErrAPIError, so it is tested first.
	case errors.Is(err, github.ErrUnauthorized):
		return nil, translatedErr{err: err, sentinel: errors.Join(provider.ErrUnauthorized, provider.ErrAPIError)}
	case errors.Is(err, github.ErrAPIError):
		return nil, translatedErr{err: err, sentinel: provider.ErrAPIError}
	}
	return versions, err
}

// translatedErr makes a github client error also match a provider sentinel
// while keeping Error() byte-identical to the client's text. Wrapping with
// fmt.Errorf("%w: %w", ...) would prepend the provider sentinel's own sentence.
type translatedErr struct {
	err      error
	sentinel error
}

func (e translatedErr) Error() string { return e.err.Error() }

func (e translatedErr) Unwrap() []error { return []error{e.err, e.sentinel} }

// GetName returns the provider name.
func (a *githubProviderAdapter) GetName() string { return "github" }

// SupportsAPI returns true since GitHub uses an API.
func (a *githubProviderAdapter) SupportsAPI() bool { return true }

// Close is a no-op for the GitHub adapter (no resources to release).
func (a *githubProviderAdapter) Close() error { return nil }

// Compare compares local packages against a remote GitHub repository
func Compare(localPackages []PackageInfo, client *github.Client, opts CompareOptions) (*CompareReport, error) {
	return CompareWithProvider(localPackages, &githubProviderAdapter{client: client}, opts)
}

// CompareWithProvider compares local packages against an upstream repository using any Provider.
//
// Packages are compared concurrently, bounded by opts.Concurrency (a value <= 0
// is treated as DefaultCompareConcurrency). The semaphore is acquired with a
// context-cancellable select: when opts.Ctx is cancelled the remaining packages
// are not dispatched and the comparison returns the partial report together
// with the context error, so a SIGINT aborts a long scan. That stop is also
// recorded on the report itself, as Interrupted, so a caller reading the report
// alone can tell a partial scan from a complete one. All writes to the shared
// report from the worker goroutines are mutex-guarded, and results are sorted by
// category/package before returning so the output is deterministic regardless of
// completion order.
func CompareWithProvider(localPackages []PackageInfo, prov provider.Provider, opts CompareOptions) (*CompareReport, error) {
	report := &CompareReport{
		TotalPackages: len(localPackages),
		Results:       []CompareResult{},
	}

	// A nil opts.Ctx is treated as context.Background() (additive field, R3.3).
	ctx := opts.Ctx
	if ctx == nil {
		ctx = context.Background() // SAFE: opts.Ctx is an additive field; nil means "no cancellation requested"
	}

	// Sanitize the concurrency limit: a non-positive value means "use the
	// default" so a zero-valued CompareOptions still behaves sensibly.
	concurrency := opts.Concurrency
	if concurrency <= 0 {
		concurrency = DefaultCompareConcurrency
	}

	var (
		sem      = make(chan struct{}, concurrency)
		wg       sync.WaitGroup
		mu       sync.Mutex
		progress atomic.Uint64
		total    = uint64(len(localPackages))
	)

	// report.Interrupted records whether the context fired while packages were
	// still being dispatched. It is both the loop's own flag and the fact the
	// report carries away, deliberately kept as ONE variable: a local `cancelled`
	// copied into the field afterwards is two places to state one thing, and the
	// error return below could then disagree with what the report says.
	//
	// No lock: it is written only by this loop, on the goroutine that called
	// CompareWithProvider, and read only after wg.Wait(). The workers never touch
	// it, and it is a distinct memory location from the sibling fields they do
	// write under mu.
	for _, pkg := range localPackages {
		// A select with both cases ready picks at random, so check the context
		// deterministically first: a context cancelled before (or during) the
		// call must stop dispatch on EVERY iteration, not just roughly half.
		if ctx.Err() != nil {
			report.Interrupted = true
			break
		}
		// Cancellable semaphore acquisition: also stop dispatching if the
		// caller's context is cancelled while waiting for a free slot.
		select {
		case <-ctx.Done():
			report.Interrupted = true
		case sem <- struct{}{}:
		}
		if report.Interrupted {
			break
		}

		wg.Add(1)
		go func(p PackageInfo) {
			defer wg.Done()
			defer func() { <-sem }()

			result := comparePackageWithProvider(p, prov, opts)

			// Filter based on options using switch for clarity.
			include := false
			switch result.Status {
			case StatusOutdated:
				include = true // Always include outdated (primary use case)
			case StatusUpToDate:
				include = opts.IncludeSynced
			case StatusNewer:
				include = !opts.OnlyOutdated // Include if not filtering to outdated only
			case StatusNotInRemote:
				include = opts.IncludeNotInRemote
			case StatusError:
				include = true // Always include errors for visibility
			}

			mu.Lock()
			report.ComparedPackages++
			switch result.Status {
			case StatusOutdated:
				report.OutdatedCount++
			case StatusNewer:
				report.NewerCount++
			case StatusUpToDate:
				report.UpToDateCount++
			case StatusNotInRemote:
				report.NotInRemoteCount++
			case StatusError:
				report.ErrorCount++
			}
			// The second axis, counted beside the first rather than instead of
			// it: this package has just been counted once above by Status and is
			// now counted once here by Verdict.
			switch result.Verdict {
			case VerdictKeep:
				report.VerdictKeepCount++
			case VerdictRedundant:
				report.VerdictRedundantCount++
			case VerdictNeedsRebase:
				report.VerdictNeedsRebaseCount++
			case VerdictUnknown:
				report.VerdictUnknownCount++
			}
			if include {
				report.Results = append(report.Results, result)
			}
			mu.Unlock()

			if opts.ProgressCallback != nil {
				opts.ProgressCallback(progress.Add(1), total)
			}
		}(pkg)
	}

	// Join every worker before touching the shared report so it is fully
	// populated and safe to read.
	wg.Wait()

	// Sort results by category/package for deterministic output.
	sortCompareResults(report.Results)

	// The findings the caller receives, established AFTER the sort so their order
	// is the results' order rather than whichever order the goroutines finished
	// in (R5.1). A partial report gets them too: a cancelled scan established
	// facts about everything it did reach, and dropping them would make a SIGINT
	// cost more than the packages it did not get to.
	EstablishFindings(report)

	if report.Interrupted {
		return report, ctx.Err()
	}
	return report, nil
}

// EstablishFindings rebuilds report.Findings from the report, and is what a
// caller runs after the annotation passes have written back onto it.
//
// It exists because the comparison and the passes that annotate it happen at
// different times. CompareWithProvider fills Status, Verdict, Patched and the
// content check; AnnotateAuthorship, AnnotateReviews, AnnotateBaseline and
// AnnotateRealignVerdicts each run afterwards, on the report the caller is
// holding, and each writes facts a finding built before them could not have
// known. Rebuilding is the whole of keeping up: the findings are a pure function
// of the report, so a second call over an annotated report produces the list the
// first call would have produced had the annotations been there.
//
// It is IDEMPOTENT and it never appends: the field is replaced, so calling it
// twice cannot double the list. A nil report is a no-op rather than a panic — a
// caller that has nothing to establish findings over is not an error condition.
//
// # It is the ONE place report.Findings is written
//
// The baseline review appends nothing of its own, and may not: `overlay compare`
// calls this once more after all four passes have run, and an appended list
// would be silently discarded by that call — a producer that had done its work
// correctly, reporting nothing. So every producer writes a pure function of the
// report instead, and this composes them.
//
// # The ORDER is the report's own order
//
// The run-level outcome first, because it opens the rendered report and because
// "nothing was compared against ::gentoo" qualifies everything printed under it;
// then the comparison's findings, one per package plus its exceptions; then the
// baseline review's, which is what the section prints beneath the same table.
// Nothing here sorts: report.Results was sorted by CompareWithProvider, and a
// second arrangement invented here is how two renderers come to disagree about
// what one run said.
func EstablishFindings(report *CompareReport) {
	if report == nil {
		return
	}
	findings := baselineRunFindings(report)
	findings = append(findings, compareFindings(report.Results)...)
	findings = append(findings, baselineResultsFindings(report.Results)...)
	report.Findings = findings
}

// sortCompareResults sorts compare results in place by category then package.
func sortCompareResults(results []CompareResult) {
	sort.Slice(results, func(i, j int) bool {
		if results[i].Category != results[j].Category {
			return results[i].Category < results[j].Category
		}
		return results[i].Package < results[j].Package
	})
}

// comparePackageWithProvider compares a single package using a Provider and
// annotates the outcome with what the caller knows about that package.
//
// The two axes are computed by two functions on purpose. comparePackageVersions
// below decides Status and is deliberately NOT given opts, so nothing it does
// can ever come to depend on the registry: the version comparison keeps its
// current four values and their current meanings by construction, not by
// convention.
func comparePackageWithProvider(pkg PackageInfo, prov provider.Provider, opts CompareOptions) CompareResult {
	result := comparePackageVersions(pkg, prov)
	// comparePackageVersions has no opts, so the text it recorded is scrubbed
	// here, before anything else can read it.
	result.FailureText = redactFailure(result.FailureText, opts.Redact)

	// Content verification runs HERE, on a result that so far carries only the
	// version comparison, and it is deliberately placed above the annotation
	// below: it therefore cannot read the declaration it is checking, and the
	// Verdict — computed from Status and the declaration alone — cannot read it
	// back. One mechanism decides, the other only checks (D4, R4.5), and the
	// order of these three statements is what makes that structural rather than
	// a convention someone must remember.
	check := verifyAgainstLocalContent(result, prov, opts)
	result.Verified = check.state
	result.DiffAdded, result.DiffRemoved = check.added, check.removed

	// A REFUSAL IS RECORDED WHERE IT HAPPENS, on the line after the check that
	// refused (S047-R3.3). Everything about this package's content is decided by
	// the statement above, so nothing further along the run can be asked to
	// remember it — see noteContentRefusal for the report that went out complete
	// while six pairs had gone uncompared.
	noteContentRefusal(&result)

	// The map is keyed by the bare "category/package" atom. A miss is the
	// unknown state — nothing is recorded about this package — and it reaches
	// deriveVerdict as known == false, which no per-status rule can turn into a
	// recommendation to remove anything. A nil map misses on every package,
	// which is the whole of the degraded mode.
	div, known := opts.Divergence[pkg.Category+"/"+pkg.Package]
	result.Patched = div.Patched
	if div.Patched {
		// Only a declared patch has a declaring entry to name; copying these
		// unconditionally would attribute a patch to a package that has none.
		result.PatchedBy = div.Entry
		result.PatchedReason = div.Reason
	}
	result.Verdict = deriveVerdict(result.Status, div, known)

	return result
}

// comparePackageVersions resolves how the local package's latest version relates
// to the provider's copy. It sets Status and the two version fields, and knows
// nothing about divergence.
func comparePackageVersions(pkg PackageInfo, prov provider.Provider) CompareResult {
	result := CompareResult{
		Category:     pkg.Category,
		Package:      pkg.Package,
		LocalVersion: pkg.LatestVersion,
	}

	// Fetch remote versions
	remoteVersions, err := prov.GetPackageVersions(pkg.Category, pkg.Package)
	if err != nil {
		if errors.Is(err, provider.ErrNotFound) {
			result.Status = StatusNotInRemote
			return result
		}
		result.Status = StatusError
		result.LookupCause = classifyLookupError(err)
		result.FailureText = err.Error()
		return result
	}

	if len(remoteVersions) == 0 {
		result.Status = StatusNotInRemote
		return result
	}

	// Find latest remote version (ignoring live/9999 ebuilds)
	remoteLatest := FindLatestVersionFiltered(remoteVersions, true)
	result.RemoteVersion = remoteLatest

	// If remote only has live versions, consider up-to-date
	if remoteLatest == "" {
		result.Status = StatusUpToDate
		result.RemoteVersion = "9999 (live only)"
		return result
	}

	// Compare versions
	cmp := ebuild.CompareVersions(pkg.LatestVersion, remoteLatest)
	switch {
	case cmp < 0:
		result.Status = StatusOutdated
	case cmp > 0:
		result.Status = StatusNewer
	default:
		result.Status = StatusUpToDate
	}

	return result
}

// verifyAgainstLocalContent compares the overlay's ebuild against the upstream
// copy of the same version, byte for byte, and reports which of the three
// verification states holds (R4.1). It never decides anything: its result is
// reported beside the Verdict, never instead of it (R4.5).
//
// It runs only when the package resolves on both sides (resolvePackagePaths,
// which states the three conditions and why each one is a condition) and both
// ebuilds read. Any of that failing is simply NotVerified rather than an error
// (R4.4) — absence of evidence is not evidence, so the declaration stands
// unverified rather than being contradicted.
//
// Both reads are local: this issues no network request and takes no context.
//
// The comparison is raw, per the design: a copyright-year bump or a stray
// trailing newline alone reads as a divergence. Should that show up in practice
// the answer is a normalisation rule, which is a policy decision of its own —
// and the failure direction is the safe one, since the finding only ever warns.
//
// PruneVerification (prune.go) asks a different question — whether deleting the
// overlay's whole package would lose anything, across every shared version and
// the files/ tree — but the two agree wherever they overlap, since both are
// bytes.Equal over the same two files. It is separate code precisely so that a
// removal criterion cannot change what this compare reports.
func verifyAgainstLocalContent(result CompareResult, prov provider.Provider, opts CompareOptions) contentCheck {
	paths, ok := resolvePackagePaths(result, prov, opts)
	if !ok {
		return contentCheck{}
	}

	ours, err := os.ReadFile(paths.ourEbuild()) //nolint:gosec // path built from scanned overlay directory names, never from registry input
	if err != nil {
		return contentCheck{}
	}
	theirs, err := os.ReadFile(paths.upstreamEbuild()) //nolint:gosec // path resolved by the provider from scanned directory names, never from registry input
	if err != nil {
		return contentCheck{}
	}

	if bytes.Equal(ours, theirs) {
		return contentCheck{state: VerifiedIdentical}
	}

	// bytes.Equal above is what DECIDES; the line counts only describe. They are
	// computed after it, from the same two buffers, so a diff that failed to
	// produce a single edit — impossible for two unequal buffers, but not worth
	// depending on — could never turn a difference into an identity.
	added, removed := diffLineCounts(theirs, ours)
	return contentCheck{state: VerifiedDiffers, added: added, removed: removed}
}

// noteContentRefusal records, on one result, that the content check RAN AND
// REFUSED the pair — as opposed to nobody having asked for a reading
// (S047-R3.3, S047-R4.1).
//
// # It is called where the refusal happens, and that placement IS the fix
//
// It used to be written only inside AnnotateReviews (review.go), which returns
// at its first statement when the reviewer is nil. A reviewer is nil in two
// unrelated situations: `--no-review`, which is the operator narrowing the run,
// and no `claude` on PATH, which is most CI and which nobody narrowed. In the
// second one the content check had still run and still refused pairs, and
// nothing wrote it down — so `func compareNotEvaluated`
// (cmd/bentoo/overlay_compare_report.go) counted zero, the document went out
// `complete: true, not_evaluated: 0`, and the same overlay reported six
// unestablished facts with a reviewer and none without one. A fact that the
// presence of a CLI can erase is not a fact about the run (S047-R5.1,
// S047-R5.2) — and `CompareRun.Unread` was counting those same six packages on
// the wire, so the document contradicted itself.
//
// The counterpart is Verified itself: NotVerified means "the check ran and
// could not compare", never "no check was attempted", precisely because
// verifyAgainstLocalContent runs on every dispatched package whatever else the
// run was asked to do. This writes the reading in the same pass, on the same
// condition, so the two can never come to disagree.
//
// # It cannot mark a run the operator merely NARROWED
//
// The population is exactly `Verified == NotVerified`. Every other result keeps
// ReadingNotRequested, which `func compareNotEvaluated` deliberately does not
// count — so `--no-review` over a run that refused nothing still reports
// itself complete (S047-R5.3). The distinction being drawn is "nobody asked"
// against "the check refused", and only this end of the code knows which.
//
// # It can never overwrite a reading
//
// Its population and the reviewed one are DISJOINT BY CONSTRUCTION:
// isUndeclaredDivergence requires VerifiedDiffers, and no result is both
// VerifiedDiffers and NotVerified. AnnotateReviews calls this same function
// over the report it is handed — one rule, one spelling, two callers, on the
// argument that made isUndeclaredDivergence one function — so a report a caller
// assembled without CompareWithProvider still says which of its rows were
// refused, and the second application writes the value the first one already
// wrote.
func noteContentRefusal(result *CompareResult) {
	if result == nil {
		return
	}
	if result.Verified == NotVerified {
		result.Reading = ReadingNotComparable
	}
}

// contentCheck is one content comparison's finding: the verification state and,
// when the two ebuilds differ, the size of that difference.
//
// It exists so verifyAgainstLocalContent can report the magnitude without
// growing a second return value that every non-differing path would have to
// spell out as zero. The zero contentCheck is NotVerified with no magnitude,
// which is exactly what each of that function's early exits means: the package
// failing to resolve on both sides, or either ebuild failing to read.
type contentCheck struct {
	state          Verification
	added, removed int
}

// packagePaths locates one compared package on both sides: the overlay
// directory holding our copy, the upstream directory the provider resolved, and
// the ebuild filename the two of them share at the compared version.
type packagePaths struct {
	ourDir      string
	upstreamDir string
	// ebuildName is <pkg>-<version>.ebuild, the filename shape both sides use —
	// the same one the provider's own scan matches when it lists versions. One
	// name serves both sides because resolvePackagePaths only returns when the
	// two versions are equal.
	ebuildName string
}

// ourEbuild and upstreamEbuild are the two files at the compared version.
func (p packagePaths) ourEbuild() string      { return filepath.Join(p.ourDir, p.ebuildName) }
func (p packagePaths) upstreamEbuild() string { return filepath.Join(p.upstreamDir, p.ebuildName) }

// resolvePackagePaths locates one compared package on both sides. ok is false
// when any part of that cannot be had.
//
// It is ONE resolution in ONE place because two passes need exactly these
// strings: verifyAgainstLocalContent above, which reads both ebuilds, and
// AnnotateAuthorship (authorship.go), which reads ours and looks for the files
// it references under upstream's files/. Resolved twice, they would be two
// things to keep in step, and the one that drifted would report on a package it
// had never opened.
//
// A false ok is never an error in either caller, and both read it as the same
// thing — nothing is known. The content check reports NotVerified (R4.4) and the
// authorship check reports unproved (R2.3). Three conditions must hold, each
// failing for its own reason:
//
//   - the caller said where the overlay is (opts.OverlayPath). PackageInfo
//     carries no path, so without it there is no overlay-side file to open;
//   - the two versions are equal. Two different versions differ for reasons that
//     say nothing about whether we changed anything. An EMPTY version is refused
//     by the same guard for a second reason: it names no ebuild at all, and it
//     is the shape a not-in-remote or failed comparison leaves behind — where
//     RemoteVersion is empty too, so the equality would otherwise hold and send
//     doomed reads at a file called "<pkg>-.ebuild";
//   - the provider has the compared repository on disk, which is exactly the
//     capability provider.PackageDirProvider names. The git-clone and local
//     providers satisfy it and the API providers do not; failing that assertion
//     IS the "API-only" signal, and an API provider could supply content only at
//     the cost of one extra rate-limited request per package.
//
// Everything here is local: it issues no network request and takes no context.
func resolvePackagePaths(result CompareResult, prov provider.Provider, opts CompareOptions) (packagePaths, bool) {
	if opts.OverlayPath == "" {
		return packagePaths{}, false
	}
	if result.LocalVersion == "" || result.LocalVersion != result.RemoteVersion {
		return packagePaths{}, false
	}

	dirProv, ok := prov.(provider.PackageDirProvider)
	if !ok {
		return packagePaths{}, false
	}
	// LocalPackagePath rather than a path joined out here: it guards with the
	// provider's own ensureRepo(), so it holds whether or not the version lookup
	// happened to run first, and it reports a package the repository does not
	// carry as an error instead of as a path that fails to open later.
	upstreamDir, err := dirProv.LocalPackagePath(result.Category, result.Package)
	if err != nil {
		return packagePaths{}, false
	}

	// Both directories are built from the CATEGORY AND PACKAGE DIRECTORY NAMES
	// THE SCANNER FOUND — result.Category/result.Package come from walking the
	// overlay, and the upstream side is resolved by the provider from those same
	// two names. Nothing here comes from a registry key, which matters because no
	// validation runs on that path: SplitPackageKey accepts "../x" happily and
	// LoadPackagesConfig never calls ValidatePackageConfig. The structure is what
	// keeps traversal absent, not a sanitiser. Keep it that way — including in
	// whatever is joined ONTO these directories: the ebuild name below is built
	// from the same two scanned strings, and the only other thing appended to
	// them anywhere is a filename the ebuild's own text spells out, which
	// ebuildFilesdirRefs refuses to let escape ${FILESDIR}.
	return packagePaths{
		ourDir:      filepath.Join(opts.OverlayPath, result.Category, result.Package),
		upstreamDir: upstreamDir,
		ebuildName:  result.Package + "-" + result.LocalVersion + ".ebuild",
	}, true
}

// diffLineCounts reports how many lines ours holds that theirs does not, and
// vice versa, as a real line diff rather than a count of differing bytes.
//
// The argument ORDER is the report's point of view: theirs is the "before" and
// ours the "after", so added is what our overlay carries on top of ::gentoo's
// ebuild — the same orientation as `diff -u <gentoo> <ours>`, which is what an
// operator checking the finding by hand will run.
//
// udiff.Lines is already this repository's diff (cmd/bentoo/overlay_autoupdate_lintfix.go
// renders the registry repair with it), so this adds no dependency and no second
// notion of what a line difference is. Each Edit replaces the byte range
// [Start,End) of before with New, and udiff.Lines aligns those ranges to line
// boundaries, so counting the lines on each side of every edit yields the totals.
//
// THE ORIENTATION MATCHES `diff`; THE MAGNITUDE NEED NOT. lcs.DiffLines stops
// searching for a minimal edit script after maxDiffs = 100 (lcs/old.go) and
// returns a valid but larger one past that point. Measured on
// net-libs/nodejs-26.7.0, our biggest real divergence: this reports +622/-254
// where GNU diff reports +430/-62. Both are correct edit scripts — the line
// totals reconcile either way — but only the small ones agree.
//
// That is acceptable HERE and would not be elsewhere, because of what the number
// is for. The question it answers is "one line, or hundreds?", and it is asked
// precisely to separate a copy that fell behind from work of our own. A count
// inflated at the top of that range still answers it; an operator who wants the
// exact edit script runs `diff -u`, which is what the caveat beneath the finding
// tells them to do anyway. Nothing downstream computes on these values.
//
// Neither count is a verdict and neither feeds one: a large diff does not
// authorise anything and a small one forbids nothing. See CompareResult.DiffAdded.
func diffLineCounts(theirs, ours []byte) (added, removed int) {
	for _, e := range udiff.Lines(string(theirs), string(ours)) {
		removed += countLines(string(theirs)[e.Start:e.End])
		added += countLines(e.New)
	}
	return added, removed
}

// countLines counts the lines in a diff fragment.
//
// A fragment is line-aligned and therefore normally ends in "\n", which would
// make a plain Count off by nothing — but the LAST fragment of a file with no
// trailing newline does not, and that line is still a line. An empty fragment is
// zero lines, not one: it is the shape of a pure insertion's "before" side, and
// counting it as a line would report a removal that did not happen.
func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

// patchedReasonCap bounds the declared reason on a detail line.
//
// The two halves of a declaration fail differently, so they are bounded
// differently. PatchedBy is an identifier the operator greps the registry for
// and is printed VERBATIM: a shortened key looks usable and is not, which is
// worse than printing none — the same argument that kept it out of a table
// column. PatchedReason is unbounded operator prose, so losing its tail costs
// nothing the registry cannot supply in full, while printing it whole would let
// one entry's essay decide the width of the entire report.
//
// 72 follows the readability caps in calculateColumnWidths: wide enough for a
// real sentence, narrow enough to leave the line readable beside the table.
const patchedReasonCap = 72

// isUndeclaredDivergence reports whether a result is the finding this report
// calls an undeclared divergence: the two ebuilds differ and no registry entry
// says why.
//
// It is ONE predicate in ONE place because two things ask it — the two loud arms
// of formatVerificationFindings below, and AnnotateReviews (review.go), which
// submits exactly this set to the model (R5.1). Spelled twice, they would be two
// things to keep in step, and the one that drifted would either ask a model
// about a package the report never warned about or print commentary under a
// finding that does not exist.
//
// It reads neither DiffAdded nor DiffRemoved, and must not: the size of a
// difference decides nothing (R1.3, compare_diff_counts_fence_test.go).
func isUndeclaredDivergence(r CompareResult) bool {
	return r.Verified == VerifiedDiffers && !r.Patched
}

// compareFindings is where every finding this comparison establishes is written
// down, once, as a value (S046-R5.1). It is the ONE definition of what the
// report has to say about a package, and the rendered lines beneath each table
// are produced from its output rather than composed a second time — so the
// sentence an operator reads and the sentence a caller receives cannot come to
// disagree.
//
// A finding is DERIVED from the fields that already carry the facts — Status and
// the two versions (how they relate), Verified (what the two ebuilds' bytes
// say), Patched (what the registry says), Authorship (what the overlay's files/
// tree proves) and Review (what a model said) — instead of being stored on
// CompareResult. There is therefore no further copy that can disagree with them,
// and no state to keep in step; CompareReport.Findings holds the output of this
// function and nothing else, which is why EstablishFindings can simply rebuild
// it after an annotation pass.
//
// # Every package gets a FindingCompared, and only some get a second entry
//
// The row-level entry is what makes the list a complete account of the run
// rather than a list of its exceptions: a consumer holding it can say what
// happened to each package without also holding Results. The four loud findings
// below are additional, so a package that carries one appears twice — once for
// how its versions relate, once for what is wrong.
//
// Only two of the four verification combinations say anything (R4.2, R4.3). The
// other two are silent on purpose: a divergence that is both real and declared
// is the system working, and a redundancy confirmed by identical bytes is a
// Verdict that has simply been checked rather than a problem.
//
// The loud one of the two then splits on Authorship (R2.2, R2.3). It is one
// finding with two things to say: the content either proved the difference
// originates here — in which case the finding names the file that proves it — or
// it did not, which is not a finding about ::gentoo.
//
// A THIRD case (R3.8) states the declaration itself, and it is what makes a
// patched package visible at all. Verification needs a local copy of the
// compared repository; with an API-only provider Verified is always NotVerified,
// so before this case existed a patched package printed exactly like an
// unpatched one — same Verdict "keep", no column, no line — and the operator was
// back to answering "does this carry changes of our own?" from memory.
//
// The case ORDER is the whole mechanism, twice over. "stale" is tested first, so
// a declaration already known to be obsolete keeps its warning and is not also
// restated as fact one line below. And the PROVED undeclared case is tested
// before the unproved one, which is only a narrowing of it: a package whose
// authorship the content settled must not fall through to the sentence that says
// nobody knows.
//
// Nothing here can change a Verdict (R4.5) — it turns finished CompareResults
// into values.
func compareFindings(results []CompareResult) []Finding {
	findings := make([]Finding, 0, len(results))

	for _, r := range results {
		atom := r.Category + "/" + r.Package
		findings = append(findings, Finding{
			Kind:     FindingCompared,
			Atom:     atom,
			Detail:   comparedDetail(r),
			Version:  r.LocalVersion,
			Upstream: r.RemoteVersion,
		})

		switch {
		case r.Verified == VerifiedIdentical && r.Patched:
			// R4.2: the entry describes a divergence that no longer exists, so it
			// is suppressing a removal recommendation for nothing. Naming the
			// entry is the point of the finding: that is what has to be edited.
			entry := declaringEntry(r)
			findings = append(findings, Finding{
				Kind:  FindingStaleDeclaration,
				Atom:  atom,
				Entry: entry,
				Detail: fmt.Sprintf("stale declaration — %s declares a divergence, but the two %s ebuilds are byte-identical",
					entry, r.LocalVersion),
				Version: r.LocalVersion,
				// The declared reason is still what the registry says this
				// divergence DOES, even though the bytes now say it does not exist.
				// A renderer proposing the entry be edited is more use to whoever
				// has to edit it when it can quote what the entry claims.
				Effect: declared(r.PatchedReason),
			})
		case isUndeclaredDivergence(r) && r.Authorship == AuthorshipOverlay:
			// R2.2: the same finding as below, except that the overlay's own
			// content settled the question the two ebuilds could not. Our ebuild
			// references a file ::gentoo does not ship for this package, and a
			// reference to a file upstream never had cannot have been inherited
			// from upstream.
			//
			// The finding therefore STATES the proof rather than decorating a
			// warning with a filename. It says what removal would cost — this is
			// the package `prune` is about to offer to delete — and it names the
			// file, package-relative and verbatim in ProvedBy, so the claim is
			// confirmed with one `ls` instead of a re-derivation of what
			// ${FILESDIR} expands to.
			//
			// The filename is EVIDENCE and not the headline. What the divergence
			// does is Effect, filled from the model's reading below where there is
			// one, and honestly empty where there is not.
			findings = append(findings, undeclaredFinding(r, atom, r.DiffAdded, r.DiffRemoved, fmt.Sprintf(
				"undeclared divergence (+%d/-%d), proved ours — our %s ebuild references %s, which ::gentoo does not ship, so removing this package would discard work of our own that no entry declares",
				r.DiffAdded, r.DiffRemoved, r.LocalVersion, r.ProvedBy)))
		case isUndeclaredDivergence(r):
			// R4.3: the loud case. Nothing declares this package, so it is about
			// to be reported as a removal candidate — and its ebuild is not the
			// one ::gentoo ships.
			//
			// The finding carries the SIZE of the difference and stops there.
			// Nothing in the content proved whose change it is — which is not a
			// finding that the change is ::gentoo's (R2.3) — and the caveat the
			// renderer prints once beneath the section says so rather than hedging
			// on every row.
			findings = append(findings, undeclaredFinding(r, atom, r.DiffAdded, r.DiffRemoved, fmt.Sprintf(
				"undeclared divergence (+%d/-%d) — our %s ebuild differs from ::gentoo's, and no entry declares why",
				r.DiffAdded, r.DiffRemoved, r.LocalVersion)))
		case r.Patched:
			// R3.8: the declaration, stated wherever it has not already been
			// contradicted above. This is the common case in practice — every
			// API-only run reaches it — so it carries the whole weight of R2.2's
			// "SHALL name the declaring entry in the report".
			//
			// The declared reason is the most trusted answer to "what does this
			// divergence do?" there is, because a maintainer committed it on
			// purpose, so it is an Effect rather than a tail glued onto Detail.
			entry := declaringEntry(r)
			findings = append(findings, Finding{
				Kind:    FindingDeclaredDivergence,
				Atom:    atom,
				Entry:   entry,
				Detail:  "patched — declared by " + entry,
				Version: r.LocalVersion,
				Effect:  declared(r.PatchedReason),
			})
		}
	}

	return findings
}

// undeclaredFinding is the two undeclared arms' shared shape: the same kind, the
// same evidence fields and the same model commentary, differing only in the
// sentence each arm writes.
//
// One constructor because the two arms are ONE finding with two things to say,
// and because the fields they share are exactly the ones a divergent copy is
// judged on — a second spelling would be free to drift, and the arm that drifted
// would drop the size, the proof or the reading from half the report.
//
// # added and removed are PARAMETERS rather than read off r, deliberately
//
// compare_diff_counts_fence_test.go permits exactly two functions to name
// DiffAdded and DiffRemoved — the one that fills them and the one that states
// them — because anything between those two ends is a computation on the SIZE of
// a divergence, which R1.3 forbids outright. Taking them as plain ints keeps this
// constructor off that list: compareFindings is the single place the fields are
// read, and this one only copies what it was handed.
func undeclaredFinding(r CompareResult, atom string, added, removed int, detail string) Finding {
	f := Finding{
		Kind:       FindingUndeclaredDivergence,
		Atom:       atom,
		Detail:     detail,
		Version:    r.LocalVersion,
		Upstream:   r.RemoteVersion,
		Added:      added,
		Removed:    removed,
		Authorship: r.Authorship,
		ProvedBy:   r.ProvedBy,
	}

	// A model's reading of this same difference (R5.2-R5.4). The same judgement
	// the annotator applies (reviewNoteSpeaks, review.go) is held here too: a note
	// missing its classification or its summary has answered neither R5.2 nor
	// R5.3, and a finding-shaped value stating nothing is worse than none. A note
	// that arrived by some other route — a hand-edited cache, a later caller — is
	// refused on the same terms.
	//
	// Everything below renders NOTHING at its zero value, which is every run no
	// review reached.
	if !reviewNoteSpeaks(r.Review) {
		return f
	}
	f.Origin = r.Review.Origin
	f.Effect = reviewed(r.Review.Summary)

	// R5.4 attaches a proposal to ONE classification. `both` is deliberately not
	// it: a copy that carries work of ours AND has fallen behind ::gentoo needs
	// the rebase first, and declaring `patched` on it would record the whole
	// difference as intentional, permanently suppressing the recommendation for
	// the half that is merely stale. ReviewNote.Declaration already says it is
	// empty unless the origin is the overlay; this refuses to carry one
	// regardless, so a model that fills the field in anyway cannot get it onto
	// the finding.
	if r.Review.Origin == OriginOverlay {
		f.Proposal = oneLine(r.Review.Declaration)
	}
	return f
}

// comparedDetail is the row-level finding's sentence: how the overlay's copy of
// one package relates to ::gentoo's, in words.
//
// The table prints the same relationship as two version columns and a status
// word, and this says it as a sentence for the consumers that have no table — a
// JSON export, a Markdown file, a log line. It states the relationship and NOT
// the recommendation: what should be done about a package is the Verdict, which
// is derived from this plus the registry, and a sentence that carried both would
// be a second place for the recommendation to be decided.
func comparedDetail(r CompareResult) string {
	switch r.Status {
	case StatusOutdated:
		return fmt.Sprintf("::gentoo ships %s and the overlay carries %s", r.RemoteVersion, r.LocalVersion)
	case StatusNewer:
		return fmt.Sprintf("the overlay carries %s, ahead of ::gentoo's %s", r.LocalVersion, r.RemoteVersion)
	case StatusUpToDate:
		return fmt.Sprintf("the overlay and ::gentoo both carry %s", r.LocalVersion)
	case StatusNotInRemote:
		return fmt.Sprintf("::gentoo carries no version of this package; the overlay carries %s", r.LocalVersion)
	case StatusError:
		// A result built without a cause (by hand, or by a caller that predates
		// LookupCause) keeps the sentence it always had.
		if r.LookupCause == LookupCauseNone {
			return "the comparison failed, so nothing is known about how the two versions relate"
		}
		text := oneLine(r.FailureText)
		if text == "" {
			return fmt.Sprintf("the upstream lookup failed (%s)", lookupCauseWord(r.LookupCause))
		}
		return fmt.Sprintf("the upstream lookup failed (%s): %s", lookupCauseWord(r.LookupCause), text)
	default:
		// Unreachable while CompareStatus has its five values. A sixth added later
		// says that it is unaccounted for rather than claiming one of the five.
		return "the comparison reported a relationship this report has no sentence for"
	}
}

// declaringEntry names the registry entry that declared the divergence, falling
// back to the bare atom when the caller supplied a declaration without one — a
// finding whose subject is blank reads as a bug in the report rather than as the
// missing entry name it actually is.
//
// The name is operator-written text on its way to a terminal. It is written as a
// VALUE and never as a format string, and it reaches no shell and no command.
func declaringEntry(r CompareResult) string {
	if r.PatchedBy != "" {
		return r.PatchedBy
	}
	return r.Category + "/" + r.Package
}

// oneLine collapses every run of whitespace into a single space, so a model's
// prose occupies exactly the one line the report gave it.
//
// This is not cosmetic. The report's STRUCTURE is its lines — "⚠ " opens a
// finding this tool stands behind — so a summary carrying a newline would print
// a second line indistinguishable from one, about a package that need not even
// exist. Model output reaches a terminal, and the terminal reads lines.
//
// strings.Fields splits on every kind of whitespace, which is what makes a
// carriage return and a tab as harmless as a newline.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// The two colour pickers that used to sit here — one mapping a CompareStatus to
// a colour, one a Verdict — are GONE, and their absence is the point (S046-R5.2).
//
// A library that can answer "what colour is `redundant`?" has decided how its
// facts look before its caller has seen them, and a cell that arrives already
// carrying an escape sequence cannot be exported to JSON, written into Markdown
// or logged plain — the three things R5.2 exists to make possible. Status and
// Verdict are values with String() methods; whoever renders them picks what they
// look like, and from story 047 that is the report.

// truncateString truncates a string to maxLen with ellipsis
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-1] + "…"
}
