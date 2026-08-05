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
	// PatchedReason is the declared text, shown in the report.
	PatchedReason string
	// Verified is the outcome of comparing the two ebuilds' bytes. It stays
	// NotVerified unless a content check ran.
	Verified Verification
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
	// Reason is the declared text, shown in the report.
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
	result.Verified = verifyAgainstLocalContent(result, prov, opts)

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
// It runs only when all three of the following hold, and any of them failing is
// simply NotVerified rather than an error (R4.4) — absence of evidence is not
// evidence, so the declaration stands unverified rather than being contradicted:
//
//   - the caller said where the overlay is (opts.OverlayPath). PackageInfo
//     carries no path, so without it there is no overlay-side file to open;
//   - the provider has the compared repository on disk, which is exactly the
//     capability provider.PackageDirProvider names. The git-clone and local
//     providers satisfy it and the API providers do not; failing that assertion
//     IS the "API-only" signal, and an API provider could supply content only at
//     the cost of one extra rate-limited request per package;
//   - the two versions are equal. Two different versions differ for reasons that
//     say nothing about whether we changed anything.
//
// Both reads are local: this issues no network request and takes no context.
//
// The comparison is raw, per the design: a copyright-year bump or a stray
// trailing newline alone reads as a divergence. Should that show up in practice
// the answer is a normalisation rule, which is a policy decision of its own —
// and the failure direction is the safe one, since the finding only ever warns.
func verifyAgainstLocalContent(result CompareResult, prov provider.Provider, opts CompareOptions) Verification {
	if opts.OverlayPath == "" {
		return NotVerified
	}
	// An empty version names no ebuild. It is the shape a not-in-remote or failed
	// comparison leaves behind — where RemoteVersion is empty too, so the
	// equality below would otherwise hold and send two doomed reads at a file
	// called "<pkg>-.ebuild".
	if result.LocalVersion == "" || result.LocalVersion != result.RemoteVersion {
		return NotVerified
	}

	dirProv, ok := prov.(provider.PackageDirProvider)
	if !ok {
		return NotVerified
	}
	// LocalPackagePath rather than a path joined out here: it guards with the
	// provider's own ensureRepo(), so it holds whether or not the version lookup
	// happened to run first, and it reports a package the repository does not
	// carry as an error instead of as a path that fails to open later.
	upstreamDir, err := dirProv.LocalPackagePath(result.Category, result.Package)
	if err != nil {
		return NotVerified
	}

	// <pkg>-<version>.ebuild is the filename shape both sides use — the same one
	// the provider's own scan matches when it lists versions. The version is
	// equal on both sides by the guard above, so one filename serves both reads.
	filename := result.Package + "-" + result.LocalVersion + ".ebuild"

	// Both paths are built from the CATEGORY AND PACKAGE DIRECTORY NAMES THE
	// SCANNER FOUND — result.Category/result.Package come from walking the
	// overlay, and the upstream side is resolved by the provider from those same
	// two names. Nothing here comes from a registry key, which matters because no
	// validation runs on that path: SplitPackageKey accepts "../x" happily and
	// LoadPackagesConfig never calls ValidatePackageConfig. The structure is what
	// keeps traversal absent, not a sanitiser. Keep it that way.
	ourPath := filepath.Join(opts.OverlayPath, result.Category, result.Package, filename)
	theirPath := filepath.Join(upstreamDir, filename)

	ours, err := os.ReadFile(ourPath) //nolint:gosec // path built from scanned overlay directory names, never from registry input
	if err != nil {
		return NotVerified
	}
	theirs, err := os.ReadFile(theirPath) //nolint:gosec // path resolved by the provider from scanned directory names, never from registry input
	if err != nil {
		return NotVerified
	}

	if bytes.Equal(ours, theirs) {
		return VerifiedIdentical
	}
	return VerifiedDiffers
}

// verdictSection is one Verdict-grouped section of the report: the packages
// that share a recommendation, under a heading that says what it is.
type verdictSection struct {
	verdict Verdict
	title   string
	// note is printed ONCE beneath the heading, for the whole section, rather
	// than on every row. The removal recommendation is one statement about a
	// group; repeating it per package would turn the advice into wallpaper the
	// operator learns to skip (R3.7).
	note        string
	headerColor *color.Color
}

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
		verdict:     VerdictRedundant,
		title:       "Redundant Packages (::gentoo ships the same or more)",
		note:        "→ Recommendation: remove these from the overlay. Nothing is deleted automatically — this is advice to act on.",
		headerColor: output.Warning,
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

// FormatReport formats a comparison report for terminal output.
//
// Packages are GROUPED BY VERDICT — what to do about each one — because that is
// the question a grouping can answer for a whole section at a time. The Status
// of each package keeps its own column beside the Verdict: the two are separate
// axes (D2), and the summary line below still counts by Status, exactly as it
// did when the sections themselves were the status partition.
func FormatReport(report *CompareReport) string {
	var sb strings.Builder

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
		delete(byVerdict, sec.verdict)
		if len(results) == 0 {
			continue
		}
		sb.WriteString(formatResultSection(results, sec.title, sec.note, sec.headerColor))
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

	sb.WriteString(formatStatusSummary(report.Results))

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

	return sb.String()
}

// formatVerificationFindings renders one line per verification finding, beneath
// the section's table.
//
// A finding is DERIVED from the two fields that already carry the facts —
// Verified (what the two ebuilds' bytes say) and Patched (what the registry
// says) — instead of being stored on CompareResult. There is therefore no third
// copy that can disagree with the two, and no state to keep in step.
//
// Only two of the four combinations say anything (R4.2, R4.3). The other two are
// silent on purpose: a divergence that is both real and declared is the system
// working, and a redundancy confirmed by identical bytes is a Verdict that has
// simply been checked rather than a problem.
//
// Nothing here can change a Verdict (R4.5) — it renders a finished CompareResult
// into a string.
func formatVerificationFindings(results []CompareResult) string {
	var sb strings.Builder

	for _, r := range results {
		switch {
		case r.Verified == VerifiedIdentical && r.Patched:
			// R4.2: the entry describes a divergence that no longer exists, so it
			// is suppressing a removal recommendation for nothing. Naming the
			// entry is the point of the line: that is what has to be edited.
			sb.WriteString(output.Sprintf(output.Warning,
				"⚠ %s/%s: stale declaration — %s declares a divergence, but the two %s ebuilds are byte-identical\n",
				r.Category, r.Package, declaringEntry(r), r.LocalVersion))
		case r.Verified == VerifiedDiffers && !r.Patched:
			// R4.3: the loud case. Nothing declares this package, so it is about
			// to be reported as a removal candidate — and its ebuild is not the
			// one ::gentoo ships.
			sb.WriteString(output.Sprintf(output.Warning,
				"⚠ %s/%s: undeclared divergence — our %s ebuild differs from ::gentoo's, yet no entry declares why\n",
				r.Category, r.Package, r.LocalVersion))
		}
	}

	return sb.String()
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
