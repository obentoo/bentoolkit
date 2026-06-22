package autoupdate

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/obentoo/bentoolkit/internal/common/github"
)

// FetchVerdictKind is the three-state outcome of classifying a manifest fetch
// failure. The zero value is FetchInconclusive: the classifier is fail-open by
// construction (AD3), so any unhandled or partial path folds back to "I don't
// know", never to a confident irreparable/recoverable judgement.
type FetchVerdictKind int

const (
	// FetchInconclusive means the failure could not be confidently classified
	// (transient signal, unknown fetcher output, discovery error, cancellation,
	// …). It is the zero value so every fall-through is safe. Callers treat it
	// as "do not change today's behaviour".
	FetchInconclusive FetchVerdictKind = iota
	// FetchIrreparable means the distfile is judged genuinely unobtainable: the
	// upstream resource is gone (404/410/no-such-file) and no compatible
	// replacement was discoverable (non-GitHub mirror, missing tag, or a release
	// tag holding only incompatible assets).
	FetchIrreparable
	// FetchRecoverable means the failing distfile is gone but a compatible
	// replacement asset was discovered on the matching GitHub release tag.
	// DiscoveredURL carries the replacement; CurrentURL carries the failing one.
	FetchRecoverable
)

// FetchVerdict is the result of classifying a fetch failure. CurrentURL is the
// failing distfile URL extracted from the fetcher output; DiscoveredURL is the
// compatible replacement (set only for FetchRecoverable). Reason is a short,
// human-facing explanation, primarily for the irreparable/inconclusive paths.
type FetchVerdict struct {
	Kind          FetchVerdictKind
	CurrentURL    string
	DiscoveredURL string
	Reason        string
}

// FetchClassifyRequest is the input to a FetchClassifier. ManifestError is the
// raw error string from the failed manifest run, shaped like
// "command failed: exit status 1\nOutput: <fetcher output>"; the classifier
// reads the fetcher output out of it to judge availability.
type FetchClassifyRequest struct {
	Package       string
	Version       string
	EbuildPath    string
	ManifestError string
}

// FetchClassifier turns a manifest fetch failure into a three-state verdict.
// Implementations MUST be fail-open: Classify returns no error and never panics
// — every internal failure folds to FetchInconclusive (AD3).
type FetchClassifier interface {
	Classify(ctx context.Context, req FetchClassifyRequest) FetchVerdict
}

// availability is the parsed availability signal of the failing fetch, derived
// solely from the fetcher's textual output. The zero value is
// availabilityInconclusive, keeping the parser fail-open.
type availability int

const (
	// availabilityInconclusive means the output carried a transient/unknown
	// signal, a success signal, or nothing parseable — we cannot say the
	// resource is gone. Zero value (fail-open).
	availabilityInconclusive availability = iota
	// availabilityGone means every recognized signal in the output is an
	// explicit not-found (404/410/No such file), with no transient/unknown or
	// success signal present.
	availabilityGone
)

// transientSignals are substrings that, when present, force an inconclusive
// availability: the fetch may simply have failed to complete (network/DNS/
// server) rather than the resource being gone. Conservative by design — any one
// match favours inconclusive over gone.
var transientSignals = []string{
	"timed out",
	"unable to resolve",
	"Connection refused",
}

// fiveXXStatusRe matches an HTTP 5xx status code (server-side / transient) in
// the fetcher output, e.g. "503 Service Unavailable".
var fiveXXStatusRe = regexp.MustCompile(`\b5\d\d\b`)

// twoXXStatusRe matches an HTTP 2xx status code (success / partial) in the
// fetcher output, e.g. "200 OK".
var twoXXStatusRe = regexp.MustCompile(`\b2\d\d\b`)

// goneSignals are substrings that mark an explicit upstream not-found. They only
// take effect when no transient/unknown or success signal is present.
var goneSignals = []string{
	"404",
	"410",
	"No such file",
}

// classifyAvailability inspects the manifest fetcher output and reports whether
// the failing distfile is explicitly gone or the situation is inconclusive. The
// token rule is deliberately conservative and fail-open (favour inconclusive):
//
//  1. ANY transient/unknown signal (timed out, unable to resolve, Connection
//     refused, or a 5xx status) → inconclusive.
//  2. else a success signal (a 2xx status) → inconclusive.
//  3. else an explicit not-found (404, 410, No such file) → gone.
//  4. else (unrecognized / empty / no parseable signal) → inconclusive.
func classifyAvailability(output string) availability {
	// 1. Transient/unknown signals win outright.
	for _, sig := range transientSignals {
		if strings.Contains(output, sig) {
			return availabilityInconclusive
		}
	}
	if fiveXXStatusRe.MatchString(output) {
		return availabilityInconclusive
	}

	// 2. A success status means the fetch (at least partially) worked — not gone.
	if twoXXStatusRe.MatchString(output) {
		return availabilityInconclusive
	}

	// 3. Only now does an explicit not-found mark the resource gone.
	for _, sig := range goneSignals {
		if strings.Contains(output, sig) {
			return availabilityGone
		}
	}

	// 4. Nothing recognized → fail-open.
	return availabilityInconclusive
}

// fetchReleaseAsset is the classifier's internal view of a GitHub release asset:
// the upstream filename and its direct download URL. It mirrors
// github.ReleaseAsset so a renamed distfile can be matched against a release
// tag's assets without leaking the github package type into the verdict surface.
type fetchReleaseAsset struct {
	Name        string
	DownloadURL string
}

// releaseAssetLister lists the assets attached to a single GitHub release tag.
// It is the classifier's sole discovery dependency, injected so Classify can be
// exercised without a network call. The production implementation wraps a real
// *github.Client (see newHTTPFetchClassifier).
type releaseAssetLister func(ctx context.Context, owner, repo, tag string) ([]fetchReleaseAsset, error)

// Discovery sentinels mapped from the underlying lister so Classify can branch
// on cause without depending on the github package's error values.
var (
	// errTagNotFound means the release tag/repo genuinely does not exist (the
	// lister's 404). Classify treats it as FetchIrreparable: there is no tag to
	// recover an asset from.
	errTagNotFound = errors.New("release tag not found")
	// errDiscoveryUnavailable means discovery could not complete for a reason
	// other than a 404 (rate limit, network, auth, malformed response, …).
	// Classify folds it to FetchInconclusive (fail-open): we cannot conclude the
	// distfile is irreparable just because we failed to look.
	errDiscoveryUnavailable = errors.New("release discovery unavailable")
)

// githubReleaseURLRe extracts a GitHub release-download URL from fetcher output,
// capturing owner, repo, tag and filename. Example match:
//
//	https://github.com/godotengine/godot/releases/download/4.7/old.tar.gz
//	                   └ owner ─┘ └ repo ┘            └tag┘ └  file  ┘
var githubReleaseURLRe = regexp.MustCompile(`https?://github\.com/([^/\s]+)/([^/\s]+)/releases/download/([^/\s]+)/(\S+)`)

// archiveSuffixes is the known-suffix list used to compute a distfile's archive
// extension robustly. Ordered longest-first so a compound suffix wins over its
// tail (old.tar.gz → ".tar.gz", not ".gz"). archiveSuffixFor relies on this
// ordering.
var archiveSuffixes = []string{
	".tar.gz",
	".tar.xz",
	".tar.bz2",
	".tar.zst",
	".tar.lz",
	".tar.lzma",
	".tgz",
	".txz",
	".tbz2",
	".zip",
	".zst",
	".7z",
	".gz",
	".xz",
	".bz2",
	".lz",
	".lzma",
}

// archiveSuffixFor returns the longest known archive suffix that name ends with,
// or "" when none matches. The longest match is what makes a compatible-asset
// comparison robust: it compares ".tar.gz" against ".tar.gz", never collapsing
// to ".gz".
func archiveSuffixFor(name string) string {
	best := ""
	for _, suf := range archiveSuffixes {
		if strings.HasSuffix(name, suf) && len(suf) > len(best) {
			best = suf
		}
	}
	return best
}

// httpFetchClassifier classifies a fetch failure by combining the parsed
// availability of the fetcher output with GitHub release discovery (via the
// injected lister). It performs no I/O of its own beyond delegating to lister.
type httpFetchClassifier struct {
	lister releaseAssetLister
}

// Compile-time assurance that httpFetchClassifier satisfies FetchClassifier.
var _ FetchClassifier = (*httpFetchClassifier)(nil)

// newHTTPFetchClassifierWithLister builds a classifier backed by the supplied
// asset lister. It is the test/seam constructor; production code uses
// newHTTPFetchClassifier, which supplies a lister backed by a real GitHub
// client.
func newHTTPFetchClassifierWithLister(lister releaseAssetLister) *httpFetchClassifier {
	return &httpFetchClassifier{lister: lister}
}

// newHTTPFetchClassifier builds a production classifier whose discovery is
// backed by a real *github.Client. GetReleaseAssets is intentionally ctx-less
// (the package idiom bounds it by the client's HTTP timeout), so the wrapper
// accepts the lister's ctx but does not forward it; cancellation is honoured by
// the ctx.Err() check at the top of Classify. Errors are mapped to the
// classifier's sentinels: a github.ErrNotFound (the tag 404) becomes
// errTagNotFound; anything else is wrapped as errDiscoveryUnavailable so
// Classify can fail open.
func newHTTPFetchClassifier(gh *github.Client) *httpFetchClassifier {
	lister := func(_ context.Context, owner, repo, tag string) ([]fetchReleaseAsset, error) {
		assets, err := gh.GetReleaseAssets(owner, repo, tag)
		if err != nil {
			if errors.Is(err, github.ErrNotFound) {
				return nil, errTagNotFound
			}
			return nil, fmt.Errorf("%w: %v", errDiscoveryUnavailable, err)
		}
		out := make([]fetchReleaseAsset, 0, len(assets))
		for _, a := range assets {
			out = append(out, fetchReleaseAsset{Name: a.Name, DownloadURL: a.DownloadURL})
		}
		return out, nil
	}
	return newHTTPFetchClassifierWithLister(lister)
}

// Classify turns a manifest fetch failure into a three-state verdict. It is
// fail-open: it returns no error, never panics, and folds every uncertain path
// to FetchInconclusive (AD3). The decision order is:
//
//  1. Cancelled context → FetchInconclusive (R2.5; checked first).
//  2. Parse availability from the fetcher output. Not "gone" →
//     FetchInconclusive (transient/success/unknown leaves today's behaviour).
//  3. Gone, but no GitHub release-download URL in the output (e.g. an ftp://
//     mirror) → FetchIrreparable (no v1 discovery path).
//  4. Gone + a GitHub URL → list the tag's assets:
//     - errTagNotFound → FetchIrreparable (the tag truly does not exist).
//     - any other lister error → FetchInconclusive (fail-open).
//     - a compatible asset (same archive extension as the failing file) →
//     FetchRecoverable. None compatible → FetchIrreparable.
func (h *httpFetchClassifier) Classify(ctx context.Context, req FetchClassifyRequest) FetchVerdict {
	// 1. Cancellation check FIRST (R2.5): a cancelled ctx can never yield a
	//    confident verdict.
	if ctx.Err() != nil {
		return FetchVerdict{Kind: FetchInconclusive, Reason: "context cancelled"}
	}

	output := manifestOutputOf(req.ManifestError)

	// 2. Only an explicit "gone" availability is actionable; everything else
	//    (transient, success, unknown) folds to inconclusive.
	if classifyAvailability(output) != availabilityGone {
		return FetchVerdict{Kind: FetchInconclusive, Reason: "availability not gone"}
	}

	// 3. Find the failing GitHub release-download URL. Without one there is no v1
	//    discovery path (e.g. an ftp:// mirror distfile) → irreparable.
	m := githubReleaseURLRe.FindStringSubmatch(output)
	if m == nil {
		return FetchVerdict{
			Kind:   FetchIrreparable,
			Reason: irreparableReason(output),
		}
	}
	currentURL, owner, repo, tag, file := m[0], m[1], m[2], m[3], m[4]

	// 4. Discover the tag's assets through the injected lister.
	assets, err := h.lister(ctx, owner, repo, tag)
	if err != nil {
		if errors.Is(err, errTagNotFound) {
			return FetchVerdict{
				Kind:       FetchIrreparable,
				CurrentURL: currentURL,
				Reason:     fmt.Sprintf("release tag %q not found", tag),
			}
		}
		// Any other discovery error is fail-open: we could not look, so we cannot
		// conclude the distfile is irreparable.
		return FetchVerdict{
			Kind:       FetchInconclusive,
			CurrentURL: currentURL,
			Reason:     "release discovery unavailable",
		}
	}

	// Match a compatible replacement: same archive extension as the failing file.
	if asset, ok := pickCompatibleAsset(file, assets); ok {
		return FetchVerdict{
			Kind:          FetchRecoverable,
			CurrentURL:    currentURL,
			DiscoveredURL: asset.DownloadURL,
			Reason:        fmt.Sprintf("discovered compatible asset %q on tag %q", asset.Name, tag),
		}
	}

	return FetchVerdict{
		Kind:       FetchIrreparable,
		CurrentURL: currentURL,
		Reason:     fmt.Sprintf("no compatible asset for %q on tag %q", file, tag),
	}
}

// manifestOutputOf returns the fetcher output carried by a manifest error
// string. The error is shaped "command failed: <err>\nOutput: <output>"; the
// portion after the first "Output:" is the fetcher output. Passing the whole
// string to classifyAvailability is also safe (the prefix carries no status
// tokens), so when no "Output:" marker is present the full string is returned
// unchanged rather than dropped.
func manifestOutputOf(manifestErr string) string {
	const marker = "Output:"
	if idx := strings.Index(manifestErr, marker); idx >= 0 {
		return strings.TrimLeft(manifestErr[idx+len(marker):], " ")
	}
	return manifestErr
}

// pickCompatibleAsset returns the asset whose name shares the failing file's
// archive suffix, and ok=false when none qualifies. Compatibility is the longest
// known archive suffix of file matched against the asset name via HasSuffix, so
// a "*.tar.gz" failure matches a "*.tar.gz" asset but not a "*.zip" one. When
// several qualify the nearest-name (shortest, then lexically smallest) is chosen
// for determinism; the tests supply exactly one compatible asset.
func pickCompatibleAsset(file string, assets []fetchReleaseAsset) (fetchReleaseAsset, bool) {
	suffix := archiveSuffixFor(file)
	if suffix == "" {
		return fetchReleaseAsset{}, false
	}

	var best fetchReleaseAsset
	found := false
	for _, a := range assets {
		if !strings.HasSuffix(a.Name, suffix) {
			continue
		}
		if !found || nearerName(a.Name, best.Name) {
			best = a
			found = true
		}
	}
	return best, found
}

// nearerName reports whether candidate is the "nearer" asset name than current:
// shorter wins, ties broken lexically. It only provides a deterministic tie-break
// when a tag holds multiple compatible assets.
func nearerName(candidate, current string) bool {
	if len(candidate) != len(current) {
		return len(candidate) < len(current)
	}
	return candidate < current
}

// irreparableReason builds a short reason for a gone, non-GitHub distfile,
// naming the failing distfile when one can be extracted from the output.
func irreparableReason(output string) string {
	if name := lastDistfileName(output); name != "" {
		return fmt.Sprintf("distfile %q is gone and no GitHub release discovery is available", name)
	}
	return "distfile is gone and no GitHub release discovery is available"
}

// distfileURLRe loosely matches any URL in the fetcher output so a non-GitHub
// (e.g. ftp:// mirror) distfile can be named in the irreparable reason. It is
// purely cosmetic — it never drives the verdict.
var distfileURLRe = regexp.MustCompile(`\b(?:https?|ftp)://\S+`)

// lastDistfileName returns the basename of the last URL found in output, or ""
// when none is present. Best-effort and cosmetic (used only for the reason
// string); any trailing punctuation from the surrounding log line is trimmed.
func lastDistfileName(output string) string {
	matches := distfileURLRe.FindAllString(output, -1)
	if len(matches) == 0 {
		return ""
	}
	last := matches[len(matches)-1]
	last = strings.TrimRight(last, `:.,)"'`)
	if idx := strings.LastIndex(last, "/"); idx >= 0 && idx+1 < len(last) {
		return last[idx+1:]
	}
	return last
}
