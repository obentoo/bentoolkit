package overlay

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	udiff "github.com/aymanbagabas/go-udiff"
	"github.com/fatih/color"
	"github.com/obentoo/bentoolkit/internal/common/ebuild"
	"github.com/obentoo/bentoolkit/internal/common/github"
	"github.com/obentoo/bentoolkit/internal/common/output"
	"github.com/obentoo/bentoolkit/internal/common/provider"
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
}

// githubProviderAdapter adapts a *github.Client to the provider.Provider interface,
// allowing Compare() to delegate to CompareWithProvider().
type githubProviderAdapter struct {
	client *github.Client
}

// GetPackageVersions returns all ebuild versions for a package via the GitHub client.
// Maps github.ErrNotFound to provider.ErrNotFound for interface compatibility.
func (a *githubProviderAdapter) GetPackageVersions(category, pkg string) ([]string, error) {
	versions, err := a.client.GetPackageVersions(category, pkg)
	if err == github.ErrNotFound {
		return nil, provider.ErrNotFound
	}
	return versions, err
}

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
// with the context error, so a SIGINT aborts a long scan. All writes to the
// shared report are mutex-guarded, and results are sorted by category/package
// before returning so the output is deterministic regardless of completion
// order.
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
		// cancelled records whether the context fired while packages were
		// still being dispatched, so the partial report is returned with the
		// context error (preserving the T9 early-cancellation contract).
		cancelled bool
	)

	for _, pkg := range localPackages {
		// A select with both cases ready picks at random, so check the context
		// deterministically first: a context cancelled before (or during) the
		// call must stop dispatch on EVERY iteration, not just roughly half.
		if ctx.Err() != nil {
			cancelled = true
			break
		}
		// Cancellable semaphore acquisition: also stop dispatching if the
		// caller's context is cancelled while waiting for a free slot.
		select {
		case <-ctx.Done():
			cancelled = true
		case sem <- struct{}{}:
		}
		if cancelled {
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

	if cancelled {
		return report, ctx.Err()
	}
	return report, nil
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
		if err == provider.ErrNotFound {
			result.Status = StatusNotInRemote
			return result
		}
		result.Status = StatusError
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

// verdictSection is one Verdict-grouped section of the report: the packages
// that share a recommendation, under a heading that says what it is.
type verdictSection struct {
	verdict Verdict
	// title and note are what the section prints when it is rendered UNDIVIDED,
	// which for three of the four sections is always.
	title string
	// note is printed ONCE beneath the heading, for the whole section, rather
	// than on every row. The removal recommendation is one statement about a
	// group; repeating it per package would turn the advice into wallpaper the
	// operator learns to skip (R3.7).
	note        string
	headerColor *color.Color
	// split, where non-nil, is the pair of headings this section takes when a
	// content check divided it in two. See splitHeadings.
	split *splitHeadings
}

// splitHeadings is the two headings a section prints instead of its own when a
// content check divided it: one over the packages the check cleared, one over the
// rest (R3.1, R3.2).
//
// It hangs off verdictSection as a POINTER, and nil — every section but the
// redundant one — means "this is never divided". The alternative was an
// `if sec.verdict == VerdictRedundant` inside FormatReport's loop, and the comment
// on verdictSections below argues against exactly that: what each section says and
// when it says it is held as DATA precisely so that a section is described in one
// place, rather than half here and half in a condition somewhere else. A section
// that never splits then costs one nil field and no branch anyone has to remember.
//
// It carries WORDING only. Which package belongs to which group is
// splitRedundantSection's, and this type could not answer it — which is what keeps
// the arrangement of the report separate from the reading of the evidence.
type splitHeadings struct {
	// identicalTitle/identicalNote head the group the content check cleared. Once
	// a section splits, this note is the only removal recommendation in the report.
	identicalTitle, identicalNote string
	// differingTitle/differingNote head the rest — the packages whose ebuilds
	// differ AND the packages no check reached, which is why neither string may
	// claim that all of them differ.
	differingTitle, differingNote string
}

// The redundant section's three headings and four notes.
//
// They are constants rather than literals inside verdictSections because the
// split prints two tables where the section used to print one, and each piece has
// to be recognisable on its own: the removal recommendation is asserted ABSENT
// from the group the content check did not clear, and a sentence that exists only
// inline cannot be named by the test that checks it. Same reason
// undeclaredDivergenceCaveat is a constant.
const (
	// redundantTitle heads the section when it is NOT divided — R3.4's case,
	// which is every run without a local ::gentoo tree to read. It is
	// byte-identical to what it has always printed: this story changes nothing
	// about what such a run can know.
	redundantTitle = "Redundant Packages (::gentoo ships the same or more)"

	// The two halves of a divided section. Each says what the CONTENT CHECK found,
	// because that is what puts a package under one heading rather than the other.
	// The verdict cannot: it is the same for every row of both tables (R3.5).
	redundantIdenticalTitle = "Redundant Packages — the content check cleared these (our ebuild is ::gentoo's, byte for byte)"
	// True of a package that was never COMPARED as much as of one that differs,
	// because both land in this group (splitRedundantSection says why). "did not
	// clear these" covers both; "these differ" would be a false statement about
	// every unchecked one, and the unchecked ones are the reason the wording is
	// worth arguing about — the live section holds none today.
	redundantDifferingTitle = "Redundant Packages — the content check did not clear these"
)

const (
	// redundantRemovalAdvice is the removal recommendation itself, and the only
	// place this report spells one. Both notes that carry it are BUILT from it, so
	// the two cannot drift apart, and it can be asserted absent from the table it
	// no longer covers — which is the entire point of the split.
	redundantRemovalAdvice = "→ Recommendation: remove these from the overlay"

	// redundantPruneAdvice names the command that acts on the recommendation
	// (R3.3). It is appended to every note that carries one, because a
	// recommendation the operator can only follow with `rm -r` is one they follow
	// with `rm -r`.
	//
	// `overlay prune` is the right thing to point at because it reads MORE than
	// this report did. The check behind these tables compared ONE file — the ebuild
	// at the compared version (resolvePackagePaths) — while prune compares every
	// version the two trees share plus the whole files/ tree, and refuses outright
	// a package it could not compare. So the command the advice names is stricter
	// than the advice, which is the only direction that is safe.
	redundantPruneAdvice = "Nothing is deleted here: act on it with 'bentoo overlay prune', which decides on content — every version the two trees share, plus the whole files/ tree — and never on the verdict alone."

	// redundantIdenticalNote is the recommendation where the content supports it:
	// the same advice as ever, now covering only the packages a byte comparison
	// cleared.
	redundantIdenticalNote = redundantRemovalAdvice +
		" — at the compared version our ebuild is byte-identical to ::gentoo's, so this copy holds nothing of ours. " +
		redundantPruneAdvice

	// redundantUncheckedNote is R3.4's: the same recommendation, resting on the
	// version alone, saying so. Before it existed, an API-only run's advice read
	// exactly like advice the bytes had confirmed.
	redundantUncheckedNote = redundantRemovalAdvice +
		". No content was checked in this run — nothing compared our ebuilds against ::gentoo's, so this rests on the " +
		"version alone and cannot say whether a copy holds work of ours. " +
		redundantPruneAdvice

	// redundantUncheckedCase is the half of the note below that speaks for a
	// package no check reached. It is a constant of its own because the group holds
	// both kinds and only this clause covers the second one: a note rewritten
	// without it would assert a difference for every row beneath it, and the test
	// that catches that names this string.
	redundantUncheckedCase = "or nothing compared the two"

	// redundantDifferingNote recommends no removal and states what is unresolved
	// (R3.2). It carries no advice to remove anything — not even a negated one,
	// which is the form a hurried reader turns back into permission — and it names
	// both of the group's cases rather than the loud one only.
	redundantDifferingNote = "→ No removal advice for these: ::gentoo ships the version, but the content check did not clear them — " +
		"either our ebuild differs from ::gentoo's, " + redundantUncheckedCase +
		". What is unresolved is whether this copy holds work of ours; only a diff settles it, and where the two " +
		"differ the finding beneath the table says by how much."
)

// verdictSections fixes which sections exist and the order they print in.
//
// Redundant comes FIRST because it is the only group asking the operator to do
// something; the others follow by how much attention they want. Holding the
// order as data rather than as a chain of ifs also means an empty group simply
// never prints — which is exactly what leaves the report silent about removals
// when nothing is redundant, with no second condition to keep in step.
//
// Nothing here deletes, disables or modifies anything (R6.1): the section says
// what it recommends and stops.
var verdictSections = []verdictSection{
	{
		verdict: VerdictRedundant,
		// The undivided pair is R3.4's: what the section prints when no content was
		// checked, which is every API-only run.
		title:       redundantTitle,
		note:        redundantUncheckedNote,
		headerColor: output.Warning,
		// The only section a content check can divide, because it is the only one
		// whose heading asks for anything. A recommendation is exactly what must
		// not cover a package the evidence does not reach.
		split: &splitHeadings{
			identicalTitle: redundantIdenticalTitle,
			identicalNote:  redundantIdenticalNote,
			differingTitle: redundantDifferingTitle,
			differingNote:  redundantDifferingNote,
		},
	},
	{
		verdict:     VerdictNeedsRebase,
		title:       "Needs Rebase (our changes must be re-applied on top of ::gentoo's version)",
		headerColor: output.Info,
	},
	{
		verdict:     VerdictKeep,
		title:       "Keep (the overlay copy earns its place)",
		headerColor: output.Success,
	},
	{
		verdict:     VerdictUnknown,
		title:       "Unknown (no recommendation)",
		note:        "→ Nothing on record describes these packages, or the comparison failed — neither supports advice either way.",
		headerColor: output.Dim,
	},
}

// splitRedundantSection divides the redundant section into the packages a
// content check cleared for removal and the packages it did not (R3.1, R3.2).
// split is false when NO result in the section carries a verification, and the
// caller then renders the section undivided, stating that no content was checked
// (R3.4).
//
// It exists because the section as it stands cannot be acted on. Measured on the
// live overlay: `overlay compare` recommends removing 74 packages and warns,
// beneath that very table, that 8 of them differ from ::gentoo in content. The
// recommendation never says which 8 to skip and the warning never says which 66
// are safe, so an operator who follows the advice deletes work of ours and an
// operator who heeds the warning follows none of the advice. Splitting the group
// is what makes the removal recommendation cover only what was checked.
//
// It groups on Verified and on nothing else. Authorship is filled only where
// Verified == VerifiedDiffers (AnnotateAuthorship, authorship.go), so every
// identical result and every unchecked one carries the zero AuthorshipUnproved
// and could not tell the two groups apart; and the two degrade TOGETHER — with no
// provider.PackageDirProvider, Verified is NotVerified and Authorship is unproved
// on every package at once, which is the API-provider case split reports.
//
// A package the check never reached joins DIFFERING, which is the one judgement
// call the requirements leave open and is settled by what each heading says.
// R3.1's group is the packages whose compared ebuilds ARE identical, under a
// heading that recommends removal: a package nobody compared is not in that set,
// and listing it there would have the report vouch for a check it never ran.
// R3.2's group recommends nothing and states what is unresolved, which is exactly
// true of a package no check reached. The costs are as asymmetric here as they are
// in proveAuthorship: one package parked under "unresolved" costs a manual diff,
// while one unchecked package under "safe to remove" advises deleting something
// nobody looked at. It changes no live number today — all 74 redundant packages
// are up-to-date at equal versions, so all 74 are checked, 66 identical and 8
// differing — but a redundant package that is merely OUTDATED is never content-
// compared (resolvePackagePaths refuses two different versions) and would land
// here the moment one appears.
//
// The groups are BUILT rather than partitioned in place. results is the caller's
// slice — FormatReport's own byVerdict entry — and an in-place partition would
// rewrite its backing array, leaving the section holding one package twice and
// missing another. A function that only groups must not be able to do that.
//
// It decides nothing (R3.5, inherited from 025 R4.5). Every Verdict is read and
// none is written; the declaration in the registry and the version comparison
// keep deciding, exactly as they do with this grouping absent.
func splitRedundantSection(results []CompareResult) (identical, differing []CompareResult, split bool) {
	// Whether to split at all is asked FIRST, over the whole section, because it
	// is a question about the run rather than about any package: either the
	// comparison had ::gentoo's files to read or it did not. Deriving it from the
	// groups afterwards — "differing is empty, so nothing was checked" — would
	// read an all-identical section as an unchecked one and drop the removal
	// recommendation on 66 packages that had just been cleared.
	for _, r := range results {
		if r.Verified != NotVerified {
			split = true
			break
		}
	}
	if !split {
		// Both groups nil, and the caller has the whole section already. Returning
		// the section in one of them would let a caller that ignored split render a
		// heading — either heading — over packages nothing is known about.
		return nil, nil, false
	}

	for _, r := range results {
		if r.Verified == VerifiedIdentical {
			identical = append(identical, r)
			continue
		}
		differing = append(differing, r)
	}
	// Appending in results order preserves the category/package sort
	// CompareWithProvider applied, so both tables read in the order the single
	// table did.
	return identical, differing, true
}

// FormatReport formats a comparison report for terminal output.
//
// Packages are GROUPED BY VERDICT — what to do about each one — because that is
// the question a grouping can answer for a whole section at a time. The Status
// of each package keeps its own column beside the Verdict: the two are separate
// axes (D2), and the summary line below still counts by Status, exactly as it
// did when the sections themselves were the status partition.
//
// PRECONDITION — an empty report.Results is rendered as "All packages are
// up-to-date!", and that sentence is only true for a caller that has not
// narrowed the slice. It IS true for an ordinary run: with the default
// IncludeSynced == false, a comparison in which every package is synced returns
// no results at all, which is what TestFormatReportEmpty pins.
//
// A caller that FILTERED must therefore test for emptiness itself before calling
// — runCompare does, via reportEmptyCompare, which can name the filter that
// emptied the set. This function is given no filter state and could not name one
// even if it tried, which is why the check belongs to the caller and this is a
// documented precondition rather than a branch here.
func FormatReport(report *CompareReport) string {
	var sb strings.Builder

	// The run-level outcome opens the report, and it is written BEFORE the early
	// return below because that return is the sentence it exists to correct: a
	// review with no ::gentoo tree to read examined nothing, and "All packages
	// are up-to-date!" is exactly how "we could not look" must never render
	// (R1.5). It prints nothing at the zero value, which is every run that
	// requested no review (R7.2).
	sb.WriteString(formatBaselineSkipped(report))

	if len(report.Results) == 0 {
		sb.WriteString(output.Sprintf(output.Success, "All packages are up-to-date!"))
		return sb.String()
	}

	// Group by verdict. Appending in Results order preserves the sort
	// CompareWithProvider already applied, so rows stay in category/package
	// order inside each section.
	byVerdict := make(map[Verdict][]CompareResult, len(verdictSections))
	for _, r := range report.Results {
		byVerdict[r.Verdict] = append(byVerdict[r.Verdict], r)
	}

	for _, sec := range verdictSections {
		results := byVerdict[sec.verdict]
		// Whether it had rows or not, this verdict now has a section: dropping it
		// from the map leaves behind exactly the verdicts that have none.
		//
		// It is dropped for a section rendered as TWO tables on exactly the same
		// terms — the verdict has been printed, however many tables it took. Skipping
		// the delete on that path would send all 74 redundant packages through the
		// "unlisted" fallback below and print every one of them a second time.
		delete(byVerdict, sec.verdict)
		if len(results) == 0 {
			continue
		}
		sb.WriteString(formatVerdictSection(sec, results))
	}

	// A verdict with no section above — reachable only if a fifth one is added
	// without listing it in verdictSections — is still printed rather than
	// silently dropped: the Total below counts every result, so a vanished row
	// would make the report disagree with its own summary. Results is walked
	// again instead of the map so the order stays deterministic.
	var unlisted []CompareResult
	for _, r := range report.Results {
		if _, unsectioned := byVerdict[r.Verdict]; unsectioned {
			unlisted = append(unlisted, r)
		}
	}
	if len(unlisted) > 0 {
		sb.WriteString(formatResultSection(unlisted, "Other Packages", "", output.Info))
	}

	// How much of the overlay the baseline review could not measure (R6.4),
	// stated once with its denominator. It prints NOTHING at the zero count,
	// which is every run that requested no review, so the summary line below
	// keeps following the last table exactly as it does today (R7.2).
	sb.WriteString(formatBaselineSummary(report))

	// How much of the run's whole diff was actually explained (R2.3, R2.5),
	// stated once with the number the three counts are a share of. It prints
	// NOTHING when no package carries a classification, which is every run that
	// requested no review, so the summary line below keeps following the last
	// table exactly as it does today (R7.2).
	if block := joinReportLines(runClassificationLines(report)); block != "" {
		sb.WriteString("\n" + block)
	}

	// How many divergences the model was asked about and did not judge (R4.4),
	// stated once with its denominator. It prints NOTHING when every question was
	// answered and nothing at all when none was asked — which is every run that
	// requested no review — so the summary line below keeps following the last
	// table exactly as it does today (R7.2).
	sb.WriteString(formatRealignSummary(report))

	sb.WriteString(formatStatusSummary(report.Results))

	return sb.String()
}

// formatVerdictSection renders one verdict's results: two tables where a content
// check divided the section, one table where it did not.
//
// The division is what makes the redundant section ACTIONABLE. Measured on the
// live overlay, the one table it used to print recommended removing 74 packages
// and then warned, beneath itself, that 8 of them differ from ::gentoo in content
// — two true sentences the operator could act on neither of, because neither said
// which 8. Rendered as two tables, the recommendation covers the 66 the bytes
// cleared and the other 8 stand under a heading that recommends nothing.
//
// EVERY package the section held is still printed (R3.5): the split moves rows
// between tables and removes none, and nothing here reads or writes a Verdict.
//
// Both tables keep the SECTION's colour. A second colour would read as a second
// verdict, and there is only one: every row of both tables is `redundant`, which
// is exactly what the two notes are there to qualify.
func formatVerdictSection(sec verdictSection, results []CompareResult) string {
	if sec.split == nil {
		return formatResultSection(results, sec.title, sec.note, sec.headerColor)
	}

	identical, differing, split := splitRedundantSection(results)
	if !split {
		// R3.4: nothing was compared, so there is nothing to divide the section by,
		// and sec.note is the one that says so. Dividing it anyway would print
		// "the content check cleared these" over packages nobody opened.
		return formatResultSection(results, sec.title, sec.note, sec.headerColor)
	}

	var sb strings.Builder
	// The cleared group first, for the reason the redundant section itself comes
	// first: it is the one asking the operator to do something.
	//
	// An EMPTY group prints nothing at all, on the same terms as a verdict no
	// package carries. That is what makes an all-differing section print no removal
	// recommendation anywhere in the report, rather than a recommendation over an
	// empty table — and each table sizes its own columns (calculateColumnWidths is
	// per-section), so neither is padded to the other's widest package name.
	if len(identical) > 0 {
		sb.WriteString(formatResultSection(identical, sec.split.identicalTitle, sec.split.identicalNote, sec.headerColor))
	}
	if len(differing) > 0 {
		sb.WriteString(formatResultSection(differing, sec.split.differingTitle, sec.split.differingNote, sec.headerColor))
	}
	return sb.String()
}

// formatStatusSummary renders the report's closing line.
//
// It counts by STATUS — the same partition, over the same results, producing
// the same numbers as before the Verdict axis existed (UB3). The sections above
// are grouped by Verdict, and deriving these counts from that grouping instead
// would leave the old labels sitting over the new axis's numbers: still
// plausible, no longer true.
func formatStatusSummary(results []CompareResult) string {
	var outdated, synced, other int
	for _, r := range results {
		switch r.Status {
		case StatusOutdated:
			outdated++
		case StatusUpToDate:
			synced++
		default:
			other++
		}
	}

	var sb strings.Builder
	sb.WriteString("\n")
	if outdated > 0 {
		fmt.Fprintf(&sb, "Outdated: %s | ",
			output.Sprint(output.Warning, fmt.Sprintf("%d", outdated)))
	}
	if synced > 0 {
		fmt.Fprintf(&sb, "Up-to-date: %s | ",
			output.Sprint(output.Success, fmt.Sprintf("%d", synced)))
	}
	if other > 0 {
		fmt.Fprintf(&sb, "Other: %s | ",
			output.Sprint(output.Info, fmt.Sprintf("%d", other)))
	}
	fmt.Fprintf(&sb, "Total: %d\n", len(results))

	return sb.String()
}

// formatResultSection formats a section of results with a header, an optional
// section-wide note and a table.
func formatResultSection(results []CompareResult, title, note string, headerColor *color.Color) string {
	var sb strings.Builder
	w := calculateColumnWidths(results)

	// Header
	sb.WriteString(output.Sprintf(headerColor, "\n%s:\n", title))
	if note != "" {
		// Passed as an ARGUMENT, never as a format string, like every other
		// piece of text this report prints.
		sb.WriteString(output.Sprintf(headerColor, "%s\n", note))
	}
	sb.WriteString(formatSectionLine(w, "top"))
	sb.WriteString(formatSectionRow(w,
		"Package", "Category", "Bentoo Version", "Gentoo Version", "Status", "Verdict", true, nil, nil))
	sb.WriteString(formatSectionLine(w, "mid"))

	// Data rows
	for _, r := range results {
		pkg := truncateString(r.Package, w.pkg)
		cat := truncateString(r.Category, w.cat)
		local := truncateString(r.LocalVersion, w.local)
		remote := truncateString(r.RemoteVersion, w.remote)
		status := truncateString(r.Status.String(), w.status)
		verdict := truncateString(r.Verdict.String(), w.verdict)
		sb.WriteString(formatSectionRow(w,
			pkg, cat, local, remote, status, verdict, false,
			getStatusColor(r.Status), getVerdictColor(r.Verdict)))
	}

	sb.WriteString(formatSectionLine(w, "bottom"))

	// Verification findings go BENEATH the table rather than into a column of
	// it: they name the registry entry that declared the divergence, and a
	// column would truncate exactly the identifier the operator needs in order
	// to find that entry.
	sb.WriteString(formatVerificationFindings(results))

	// The baseline review, beneath those, for the same reason and on the same
	// terms: it names an ebuild path and quotes an ebuild's own text, neither of
	// which survives a column. It prints NOTHING unless a review filled the
	// fields it reads, so a run that requested none appends the empty string
	// here and the section ends exactly where it ends today (R7.2).
	sb.WriteString(formatBaselineFindings(results))

	return sb.String()
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

// undeclaredDivergenceCaveat is the sentence formatVerificationFindings prints
// once beneath a section that reported at least one UNPROVED undeclared
// divergence. It is a package-level constant so a test can assert on it without
// copying the wording.
const undeclaredDivergenceCaveat = "  ↳ a difference is not proof of authorship: ::gentoo also revises ebuilds in place, without a revbump, so a small diff is often our copy having fallen behind rather than work of ours. Diff before removing or declaring."

// The two openings a model's reading is printed under, indented beneath the
// finding they qualify.
//
// Both SAY WHOSE WORDS FOLLOW, and that is the whole reason they are worded at
// all. Everything else in this report is something the tool established: two
// files compared byte for byte, a registry entry consulted, a filename stat'ed.
// What comes after these two openings is a guess by a language model. An
// operator who cannot tell the two apart will act on the wrong one — and the
// line that invites an action, "declare this patched", is the guess.
//
// They are constants so a test can name them without copying the wording, on the
// same argument that made undeclaredDivergenceCaveat one.
const (
	reviewReadingLead  = "  ↳ model reading, not a finding of this report: "
	reviewProposalLead = "  ↳ proposed declaration, nothing here writes it — apply it yourself: "
)

// formatVerificationFindings renders one line per verification finding, beneath
// the section's table.
//
// A finding is DERIVED from the fields that already carry the facts — Verified
// (what the two ebuilds' bytes say), Patched (what the registry says) and
// Authorship (what the overlay's files/ tree proves) — instead of being stored
// on CompareResult. There is therefore no further copy that can disagree with
// them, and no state to keep in step.
//
// Only two of the four verification combinations say anything (R4.2, R4.3). The
// other two are silent on purpose: a divergence that is both real and declared
// is the system working, and a redundancy confirmed by identical bytes is a
// Verdict that has simply been checked rather than a problem.
//
// The loud one of the two then splits on Authorship (R2.2, R2.3). It is one
// finding with two things to say: the content either proved the difference
// originates here — in which case the line names the file that proves it — or it
// did not, which is not a finding about ::gentoo.
//
// A THIRD case (R3.8) renders the declaration itself, and it is what makes a
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
// Nothing here can change a Verdict (R4.5) — it renders a finished CompareResult
// into a string.
func formatVerificationFindings(results []CompareResult) string {
	var sb strings.Builder
	// Counted so the caveat below can print once for the whole section, on the
	// same argument that keeps the removal recommendation out of every row: a
	// sentence repeated eight times is a sentence nobody reads the ninth time.
	//
	// UNPROVED findings, not undeclared ones. The caveat exists to qualify an
	// ambiguity, and a proved divergence no longer has one.
	unproved := 0

	for _, r := range results {
		switch {
		case r.Verified == VerifiedIdentical && r.Patched:
			// R4.2: the entry describes a divergence that no longer exists, so it
			// is suppressing a removal recommendation for nothing. Naming the
			// entry is the point of the line: that is what has to be edited.
			sb.WriteString(output.Sprintf(output.Warning,
				"⚠ %s/%s: stale declaration — %s declares a divergence, but the two %s ebuilds are byte-identical\n",
				r.Category, r.Package, declaringEntry(r), r.LocalVersion))
		case isUndeclaredDivergence(r) && r.Authorship == AuthorshipOverlay:
			// R2.2: the same finding as below, except that the overlay's own
			// content settled the question the two ebuilds could not. Our ebuild
			// references a file ::gentoo does not ship for this package, and a
			// reference to a file upstream never had cannot have been inherited
			// from upstream.
			//
			// The line therefore STATES the proof rather than decorating the
			// warning with a filename. It says what removal would cost — this is
			// the package `prune` is about to offer to delete — and it names the
			// file, package-relative and verbatim, so the claim is confirmed with
			// one `ls` instead of a re-derivation of what ${FILESDIR} expands to.
			//
			// ProvedBy comes from ebuild TEXT, so it is passed as an ARGUMENT and
			// never as a format string, exactly like every other piece of text this
			// report prints. It is NOT counted below: nothing here is ambiguous.
			sb.WriteString(output.Sprintf(output.Warning,
				"⚠ %s/%s: undeclared divergence (+%d/-%d), proved ours — our %s ebuild references %s, which ::gentoo does not ship, so removing this package would discard work of our own that no entry declares\n",
				r.Category, r.Package, r.DiffAdded, r.DiffRemoved, r.LocalVersion, r.ProvedBy))
			// The model's reading of this same difference, beneath the finding it is
			// about (R5.2-R5.4). It renders nothing when no review ran, which is
			// every run without one.
			sb.WriteString(reviewCommentary(r))
		case isUndeclaredDivergence(r):
			// R4.3: the loud case. Nothing declares this package, so it is about
			// to be reported as a removal candidate — and its ebuild is not the
			// one ::gentoo ships.
			//
			// The line carries the SIZE of the difference and stops there. Nothing
			// in the content proved whose change it is — which is not a finding
			// that the change is ::gentoo's (R2.3) — and the sentence beneath the
			// section says so once rather than hedging on every row.
			unproved++
			sb.WriteString(output.Sprintf(output.Warning,
				"⚠ %s/%s: undeclared divergence (+%d/-%d) — our %s ebuild differs from ::gentoo's, and no entry declares why\n",
				r.Category, r.Package, r.DiffAdded, r.DiffRemoved, r.LocalVersion))
			// The reading is printed here for the same reason the caveat is printed
			// below: this is the ambiguous finding, and a model's guess at the
			// direction is exactly what the operator has otherwise to establish by
			// hand. It qualifies the ambiguity — it does not resolve it, which is
			// why the caveat still prints beneath the section.
			sb.WriteString(reviewCommentary(r))
		case r.Patched:
			// R3.8: the declaration, stated wherever it has not already been
			// contradicted above. This is the common case in practice — every
			// API-only run reaches it — so it carries the whole weight of R2.2's
			// "SHALL name the declaring entry in the report".
			//
			// Both strings are operator-written text on their way to a terminal,
			// passed as ARGUMENTS and never as a format string, reaching no shell
			// and no command.
			sb.WriteString(output.Sprintf(output.Info,
				"· %s/%s: patched — declared by %s%s\n",
				r.Category, r.Package, declaringEntry(r), declaredReason(r)))
		}
	}

	// The caveat the counts above cannot state on their own, printed once and
	// only where it applies.
	//
	// "Our ebuild differs" is symmetric and the report reads it as an accusation:
	// the operator changed something and failed to declare it. That is only half
	// the truth. ::gentoo revises ebuilds IN PLACE — a fix landing under the same
	// version, with no revbump — so a copy taken last week can differ today
	// without anyone here having touched it, and the version comparison, which
	// sees two equal version strings, cannot notice. Direction is not recoverable
	// from two files; it needs history, which this check does not have.
	//
	// So the report says what it knows and names the ambiguity instead of picking
	// a side. Declaring `patched` on a package that is merely stale would record a
	// divergence that does not exist and suppress its removal recommendation
	// permanently — the failure this sentence is here to prevent.
	//
	// Where the content PROVED authorship there is no such ambiguity, and the
	// caveat is then false about the very finding it sits under: a report that
	// qualifies what it has established teaches the operator to discount both. A
	// section whose divergences are all proved therefore prints no caveat — but
	// one unproved finding among them is enough to keep it, because a proof about
	// one package says nothing about the next. On the live overlay the section
	// holds three proved and five unproved, so it still prints.
	if unproved > 0 {
		// Passed as an ARGUMENT, never as a format string, like every other piece
		// of text this report prints.
		sb.WriteString(output.Sprintf(output.Warning, "%s\n", undeclaredDivergenceCaveat))
	}

	return sb.String()
}

// declaredReason renders the ": <reason>" tail of a declaration line, or "" when
// there is no reason to state.
//
// The empty case is reachable in production, not defensive padding: R1.3 rejects
// a whitespace-only reason at validation time, but LoadPackagesConfig
// (config.go:552-567) never calls ValidatePackageConfig, so the compare path
// sees entries that validation never judged. A dangling colon introducing
// nothing would read as a truncation bug rather than as the missing text it is.
func declaredReason(r CompareResult) string {
	reason := strings.TrimSpace(r.PatchedReason)
	if reason == "" {
		return ""
	}
	return ": " + truncateString(reason, patchedReasonCap)
}

// declaringEntry names the registry entry that declared the divergence, falling
// back to the bare atom when the caller supplied a declaration without one — a
// finding whose subject is blank reads as a bug in the report rather than as the
// missing entry name it actually is.
//
// The name is operator-written text on its way to a terminal. It is passed as an
// ARGUMENT and never as a format string, and it reaches no shell and no command.
func declaringEntry(r CompareResult) string {
	if r.PatchedBy != "" {
		return r.PatchedBy
	}
	return r.Category + "/" + r.Package
}

// reviewCommentary renders a model's reading of one finding — where the
// difference came from (R5.2), what it does (R5.3), and, only where it is ours,
// the `patched` text that would declare it (R5.4). It returns "" when there is
// nothing to print, which is every run no review reached.
//
// EVERY MODEL-PRODUCED STRING IS AN ARGUMENT and never a format string, exactly
// like ProvedBy, PatchedReason and every other piece of text this report prints.
// It reaches a terminal and nothing else: no shell, no command, no file.
//
// It writes NO FILE. R5.4 proposes; the operator applies. The overlay repository
// auto-commits and pushes within minutes, so a declaration this program wrote
// would be published before anyone could read it.
//
// Nothing here can change a Verdict, a count or which table a package sits in
// (R5.8): it turns one finished CompareResult into a string, and the string is
// two lines the report would otherwise not have printed.
func reviewCommentary(r CompareResult) string {
	note := r.Review
	// The same judgement the annotator applies (reviewNoteSpeaks, review.go), held
	// on this side too: a note missing its classification or its summary has
	// answered neither R5.2 nor R5.3, and a finding-shaped line stating nothing is
	// worse than no line. A note that arrived by some other route — a hand-edited
	// cache, a later caller — is refused here on the same terms.
	prose := reviewOriginProse(note.Origin)
	if !reviewNoteSpeaks(note) || prose == "" {
		return ""
	}

	var sb strings.Builder
	sb.WriteString(output.Sprintf(output.Dim, "%s%s — %s\n", reviewReadingLead, prose, oneLine(note.Summary)))

	// R5.4 attaches a proposal to ONE classification. `both` is deliberately not
	// it: a copy that carries work of ours AND has fallen behind ::gentoo needs the
	// rebase first, and declaring `patched` on it would record the whole difference
	// as intentional, permanently suppressing the recommendation for the half that
	// is merely stale. ReviewNote.Declaration already says it is empty unless the
	// origin is the overlay; this refuses to print one regardless, so a model that
	// fills the field in anyway cannot get it onto the line.
	if note.Origin != OriginOverlay {
		return sb.String()
	}
	declaration := truncateString(oneLine(note.Declaration), patchedReasonCap)
	if declaration == "" {
		// An overlay-origin note with nothing to propose. The reading above still
		// stands; an empty proposal line would read as a truncation bug.
		return sb.String()
	}
	// Capped by the SAME mechanism as the operator's own declared reason
	// (declaredReason, one function up), not by a second one: an unbounded
	// proposal would let a model's essay decide the width of the whole report.
	sb.WriteString(output.Sprintf(output.Dim, "%s%s\n", reviewProposalLead, declaration))

	return sb.String()
}

// reviewOriginProse is the report's sentence for a classification, or "" for one
// that says nothing.
//
// It is built HERE rather than by ReviewOrigin.String() because the two serve
// different readers. The four words String() returns — unknown, overlay,
// upstream, both — are this feature's wire and storage vocabulary: the cache
// persists one with no expiry, and the CLI adapter decodes one from the model's
// reply. Changing them would change what every note already on disk means.
// "::gentoo" is what an operator calls upstream, and it appears in no stored
// file.
func reviewOriginProse(o ReviewOrigin) string {
	switch o {
	case OriginOverlay:
		return "originates in the overlay"
	case OriginUpstream:
		return "originates in ::gentoo"
	case OriginBoth:
		return "originates on both sides"
	default:
		// OriginUnknown, and any value a later constant adds without a sentence
		// here. Both mean the report has nothing to say, and reviewCommentary
		// prints nothing rather than a line about an origin nobody defined.
		return ""
	}
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

// columnWidths holds the calculated column widths for table formatting.
type columnWidths struct {
	pkg, cat, local, remote, status, verdict int
}

// calculateColumnWidths computes the optimal column widths for a set of compare results.
// It uses minimum widths based on header labels and caps maximum widths for readability.
func calculateColumnWidths(results []CompareResult) columnWidths {
	w := columnWidths{
		pkg:    7,  // "Package"
		cat:    8,  // "Category"
		local:  14, // "Bentoo Version"
		remote: 14, // "Gentoo Version"
		status: 13, // "Status", widened to fit "not-in-remote"
		// "Verdict", widened to fit "needs-rebase". Unlike a package name, the
		// four verdict words are a closed vocabulary of at most 12 characters,
		// so the readability cap below would never bind — the same reason
		// Status has none.
		verdict: 12,
	}

	for _, r := range results {
		if len(r.Package) > w.pkg {
			w.pkg = len(r.Package)
		}
		if len(r.Category) > w.cat {
			w.cat = len(r.Category)
		}
		if len(r.LocalVersion) > w.local {
			w.local = len(r.LocalVersion)
		}
		if len(r.RemoteVersion) > w.remote {
			w.remote = len(r.RemoteVersion)
		}
		if len(r.Status.String()) > w.status {
			w.status = len(r.Status.String())
		}
		if len(r.Verdict.String()) > w.verdict {
			w.verdict = len(r.Verdict.String())
		}
	}

	// Cap widths for readability
	if w.pkg > 30 {
		w.pkg = 30
	}
	if w.cat > 20 {
		w.cat = 20
	}

	return w
}

// getStatusColor returns the appropriate color for a CompareStatus
func getStatusColor(status CompareStatus) *color.Color {
	switch status {
	case StatusUpToDate:
		return output.Success
	case StatusOutdated:
		return output.Warning
	case StatusNewer:
		return output.Info
	case StatusNotInRemote:
		return output.Dim
	case StatusError:
		return output.Error
	default:
		return nil
	}
}

// getVerdictColor returns the appropriate color for a Verdict. Redundant is the
// warning colour because it is the only verdict that asks for an action.
func getVerdictColor(verdict Verdict) *color.Color {
	switch verdict {
	case VerdictKeep:
		return output.Success
	case VerdictRedundant:
		return output.Warning
	case VerdictNeedsRebase:
		return output.Info
	case VerdictUnknown:
		return output.Dim
	default:
		return nil
	}
}

// formatSectionLine creates a horizontal line for the section table (the wide
// one, carrying both judgement columns: Status and Verdict).
func formatSectionLine(w columnWidths, position string) string {
	var left, mid, right, horiz string

	switch position {
	case "top":
		left, mid, right, horiz = "┌", "┬", "┐", "─"
	case "mid":
		left, mid, right, horiz = "├", "┼", "┤", "─"
	case "bottom":
		left, mid, right, horiz = "└", "┴", "┘", "─"
	}

	var sb strings.Builder
	sb.WriteString(left)
	// One entry per column, left to right: adding a column means adding it here
	// and to formatSectionRow, and nowhere else.
	for i, width := range []int{w.pkg, w.cat, w.local, w.remote, w.status, w.verdict} {
		if i > 0 {
			sb.WriteString(mid)
		}
		sb.WriteString(strings.Repeat(horiz, width+2))
	}
	sb.WriteString(right)
	sb.WriteString("\n")

	return sb.String()
}

// formatSectionRow creates a row of the section table. Only the two judgement
// columns are coloured: the facts are printed plain, the reading of them is not.
func formatSectionRow(w columnWidths, pkg, cat, local, remote, status, verdict string, header bool, statusColor, verdictColor *color.Color) string {
	if header {
		format := "│ %-*s │ %-*s │ %-*s │ %-*s │ %-*s │ %-*s │\n"
		row := fmt.Sprintf(format, w.pkg, pkg, w.cat, cat, w.local, local, w.remote, remote, w.status, status, w.verdict, verdict)
		return output.Sprint(output.Header, row)
	}

	return fmt.Sprintf("│ %-*s │ %-*s │ %-*s │ %-*s │ %s │ %s │\n",
		w.pkg, pkg, w.cat, cat, w.local, local, w.remote, remote,
		paddedCell(status, w.status, statusColor),
		paddedCell(verdict, w.verdict, verdictColor))
}

// paddedCell pads a cell to width and then colours it, in that order: padding a
// string that already carries escape codes would count them toward its width
// and misalign the table.
func paddedCell(s string, width int, c *color.Color) string {
	if c == nil {
		return fmt.Sprintf("%-*s", width, s)
	}
	return output.Sprintf(c, "%-*s", width, s)
}

// formatTableLine creates a horizontal table line
func formatTableLine(pkgW, catW, localW, remoteW int, position string) string {
	var left, mid, right, horiz string

	switch position {
	case "top":
		left, mid, right, horiz = "┌", "┬", "┐", "─"
	case "mid":
		left, mid, right, horiz = "├", "┼", "┤", "─"
	case "bottom":
		left, mid, right, horiz = "└", "┴", "┘", "─"
	}

	return fmt.Sprintf("%s%s%s%s%s%s%s%s%s\n",
		left, strings.Repeat(horiz, pkgW+2),
		mid, strings.Repeat(horiz, catW+2),
		mid, strings.Repeat(horiz, localW+2),
		mid, strings.Repeat(horiz, remoteW+2), right)
}

// formatTableRow creates a table row
func formatTableRow(pkgW, catW, localW, remoteW int, pkg, cat, local, remote string, header bool) string {
	format := "│ %-*s │ %-*s │ %-*s │ %-*s │\n"
	row := fmt.Sprintf(format, pkgW, pkg, catW, cat, localW, local, remoteW, remote)

	if header {
		return output.Sprint(output.Header, row)
	}
	return row
}

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

// FormatSummary formats a brief summary of the comparison
func FormatSummary(report *CompareReport) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "Scanned: %d packages\n", report.TotalPackages)
	fmt.Fprintf(&sb, "Compared: %d packages (exist in both repos)\n", report.ComparedPackages-report.NotInRemoteCount)

	if report.OutdatedCount > 0 {
		sb.WriteString(output.Sprintf(output.Warning, "Outdated: %d\n", report.OutdatedCount))
	}
	if report.NewerCount > 0 {
		sb.WriteString(output.Sprintf(output.Info, "Newer in Bentoo: %d\n", report.NewerCount))
	}
	if report.UpToDateCount > 0 {
		sb.WriteString(output.Sprintf(output.Success, "Up-to-date: %d\n", report.UpToDateCount))
	}

	return sb.String()
}
