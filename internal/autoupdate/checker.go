// Package autoupdate provides version checking functionality for ebuild autoupdate.
package autoupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/ebuild"
	"github.com/obentoo/bentoolkit/internal/common/github"
	"github.com/obentoo/bentoolkit/internal/common/logger"
	"github.com/obentoo/bentoolkit/internal/common/provider"
)

// httpRateLimiter is the minimal surface fetchContent needs from a rate
// limiter: block until a per-host token is available or the context is
// cancelled. The concrete *RateLimiter satisfies it; defining the interface
// here (rather than depending on the concrete type) keeps fetchContent
// testable with a recording/blocking fake without touching rate_limiter.go.
type httpRateLimiter interface {
	WaitHTTP(ctx context.Context, domain string) error
}

// Error variables for checker errors
var (
	// ErrPackageNotFound is returned when a package is not found in the configuration
	ErrPackageNotFound = errors.New("package not found in configuration")
	// ErrNoEbuildFound is returned when no ebuild file is found for a package
	ErrNoEbuildFound = errors.New("no ebuild file found for package")
	// ErrFetchFailed is returned when fetching upstream version fails
	ErrFetchFailed = errors.New("failed to fetch upstream version")
	// ErrBaseVersionUnresolved is returned when an entry declares where its base
	// version lives (base_from, or commit_version_pattern) and that source yields
	// nothing. It is deliberately fatal for the check: falling back to the
	// ebuild's own base is what let six Khronos packages drift up to seven
	// releases behind while still reporting "up to date".
	ErrBaseVersionUnresolved = errors.New("declared base version source resolved nothing")
)

// CheckResult represents the result of checking a single package for updates.
type CheckResult struct {
	// Package is the full package name (category/package)
	Package string
	// CurrentVersion is the version currently in the overlay
	CurrentVersion string
	// UpstreamVersion is the version found upstream
	UpstreamVersion string
	// HasUpdate is true if upstream version is newer than current
	HasUpdate bool
	// NotComparable is true when the upstream value could not be ordered against
	// the current version (e.g. a tag like "INKSCAPE_1_4_4" or an unparseable
	// string). When set, HasUpdate is false and the package was NOT added to the
	// pending list: the result is surfaced as a warning so a silent false
	// "up to date" never masks a real update behind a bad parser config.
	NotComparable bool
	// Error contains any error that occurred during checking
	Error error
	// FromCache is true if the upstream version was retrieved from cache
	FromCache bool
	// Type classifies the package as "bin" or "source", resolved from the
	// config's type field or auto-detected from the ebuild. Empty only when the
	// current ebuild could not be read.
	Type string
	// Orphaned is true when the package no longer has any ebuild in the overlay
	// (getCurrentVersion returned ErrNoEbuildFound). The checker auto-disables
	// the entry (enabled = false) in packages.toml and surfaces the package as
	// an informational result rather than a recurring hard failure. When set,
	// all other fields except Package are zero-valued.
	Orphaned bool
}

// DefaultOpTimeout is the default per-operation timeout applied to a single
// outbound HTTP fetch when no explicit timeout is configured on the Checker.
const DefaultOpTimeout = 30 * time.Second

// deriveOpTimeout sizes the per-operation budget so that every retry attempt can
// run within it: perReq×(MaxRetries+1) for the attempts, plus the cumulative
// exponential backoff between them, plus one second of slack. The slack ensures
// the per-request timeout (not the operation budget) is what fires on a slow
// host, so the failure surfaces as the clearer "max retries exceeded" rather than
// a premature "context deadline exceeded". rc carries the retry parameters from
// the HTTP client; a zero/blank rc still yields a budget >= perReq.
func deriveOpTimeout(perReq time.Duration, rc RetryConfig) time.Duration {
	maxRetries := rc.MaxRetries
	if maxRetries < 0 {
		maxRetries = 0
	}
	attempts := maxRetries + 1
	total := perReq * time.Duration(attempts)

	// Sum the backoff delays the retry loop would sleep between attempts, mirroring
	// calculateDelay: BaseDelay×2^(i-1), capped at MaxDelay.
	for i := 1; i <= maxRetries; i++ {
		multiplier := 1 << (i - 1) // 2^(i-1): 1, 2, 4, ...
		delay := rc.BaseDelay * time.Duration(multiplier)
		if rc.MaxDelay > 0 && delay > rc.MaxDelay {
			delay = rc.MaxDelay
		}
		total += delay
	}

	return total + time.Second
}

// operationTimeout resolves the per-operation budget for a package: the
// per-package override (cfg.Timeout seconds) when set, otherwise the Checker's
// global budget (c.opTimeout, derived from the configured per-request timeout).
func (c *Checker) operationTimeout(cfg *PackageConfig) time.Duration {
	if cfg != nil && cfg.Timeout > 0 {
		return time.Duration(cfg.Timeout) * time.Second
	}
	return c.opTimeout
}

// hostForError extracts the host from a URL for diagnostic messages, falling
// back to the raw URL when it cannot be parsed. It never returns query strings,
// so it will not leak a credential carried as a query parameter.
func hostForError(rawURL string) string {
	if u, err := url.Parse(rawURL); err == nil && u.Host != "" {
		return u.Host
	}
	return rawURL
}

// sentRequestDeclaresRange reports whether the request that actually reached the
// wire asked the server for a byte range, which is what makes a 206 answer
// legitimate (S020-R1.1, S020-R1.2). It reads the request recorded on the response instead
// of predicting the wire from the per-package header map, because that map is
// not what the server sees:
//
//   - setHeader (httpclient.go) DROPS any header whose name contains CR or LF,
//     so a "Range\n" key sends no Range at all. A map-based guess would open the
//     gate for a header that was never on the wire — the fail-open trap this
//     predicate closes by construction (S020-R2.3).
//   - setHeader also applies strings.TrimSpace and
//     textproto.CanonicalMIMEHeaderKey to the name, so " Range " and "RANGE"
//     both arrive as the canonical "Range" (S020-R2.1, S020-R2.2).
//   - applyHeaders (httpclient.go) additionally contributes c.defaultHeaders,
//     a source the per-package map never sees at all (S020-R3.1).
//
// Header.Get canonicalises its lookup key too, so casing and padding are already
// resolved by the time this reads it: one Get answers every spelling, with no
// normalisation logic here to drift out of step with setHeader's.
//
// Absent evidence is not permission (S020-R1.4): a nil response, or one whose Request
// the transport did not record, reads as "no Range declared", so an unsolicited
// 206 fails safe on the status error rather than being accepted on a guess.
func sentRequestDeclaresRange(resp *http.Response) bool {
	if resp == nil || resp.Request == nil {
		return false
	}
	return resp.Request.Header.Get("Range") != ""
}

// DefaultConcurrency is the default number of packages processed in parallel
// when no explicit concurrency is configured. It governs both --check (CheckAll's
// per-package HTTP fan-out) and the --apply all worker pool. For --check, per-host
// rate limiting — not this number — bounds the request rate to any single provider
// (GitHub ~10/s, GitLab ~3/s), so the tuned limiters stay saturated. For --apply
// each worker runs a `pkgdev manifest` that fetches distfiles over the network and
// bypasses those limiters, so a moderate default keeps concurrent downloads from
// overwhelming a single host. 10 balances throughput against both.
const DefaultConcurrency = 10

// maxConcurrency is the upper bound accepted by WithConcurrency. It caps the
// number of in-flight per-package goroutines (and therefore the burst of
// outbound HTTP requests) to a sane ceiling regardless of caller input.
const maxConcurrency = 100

// ProgressCallback reports batch progress as a check proceeds. It is invoked
// once per package as that package's work completes, with done being the
// cumulative count of packages finished so far and total the number of
// packages in the batch.
//
// Because CheckAll runs packages concurrently, the callback may fire from
// multiple goroutines and the per-invocation order is not deterministic.
// However done is sourced from an atomic counter, so the value observed by any
// single invocation is monotone non-decreasing: each callback sees a strictly
// larger done than every callback that ran before it.
type ProgressCallback func(done, total uint64)

// Checker handles version checking operations for packages.
// It coordinates between configuration, cache, pending list, and upstream sources.
type Checker struct {
	// overlayPath is the path to the overlay directory
	overlayPath string
	// config holds the packages configuration
	config *PackagesConfig
	// typeFilter, when non-empty ("bin" or "source"), restricts CheckAll to
	// packages of that resolved type. Empty checks every package. Set via
	// WithTypeFilter.
	typeFilter string
	// cache manages version query caching
	cache *Cache
	// pending manages pending updates
	pending *PendingList
	// llmClient handles LLM-based version extraction (optional). It is the
	// LLMProvider interface (AD2), so ANY configured provider — not only the
	// legacy claude *LLMClient — can drive --check's LLM extraction path.
	// MUST stay an UNTYPED nil when absent: the nil-guards below and in
	// fetchUpstreamVersion rely on `== nil` / `!= nil`, which a typed-nil
	// interface (e.g. a (*ClaudeClient)(nil) boxed by a failed constructor)
	// would defeat. WithLLMClient therefore rejects a nil argument, and the
	// CLI wires it only on a successful, non-nil construction.
	llmClient LLMProvider
	// llmProviderConfigured records that the CLI attempted to configure an LLM
	// provider for this run (autoupdate.llm.provider was non-empty), regardless
	// of whether construction ultimately succeeded. It gates the "unused
	// llm_prompt" Warn: that diagnostic must fire ONLY when no provider was
	// configured at all (S003-R5.3). When a provider was configured but failed to
	// build, runCheck emits its own failure Warn, so suppressing the construction
	// Warn here avoids a confusing double-warn. Set via WithLLMProviderConfigured;
	// defaults false so existing direct callers are unaffected.
	llmProviderConfigured bool
	// httpClient handles HTTP requests with retry logic
	httpClient *RetryableHTTPClient
	// configDir is the directory for storing cache and pending files
	configDir string
	// ctx is the parent context for all outbound HTTP/LLM calls. It is set via
	// WithContext and originates in cmd/ (signal.NotifyContext), so a SIGINT or
	// deadline cancels every in-flight request. Defaults to context.Background().
	ctx context.Context
	// opTimeout bounds a single outbound operation. Each fetch derives a child
	// context via context.WithTimeout(ctx, opTimeout). Defaults to DefaultOpTimeout.
	// When httpReqTimeout is set and WithOpTimeout was not, NewChecker replaces this
	// with a value derived from httpReqTimeout that is large enough for every retry
	// attempt to fit (see deriveOpTimeout).
	opTimeout time.Duration
	// opTimeoutExplicit records that WithOpTimeout set opTimeout directly, so
	// NewChecker must not overwrite it with the derived budget.
	opTimeoutExplicit bool
	// httpReqTimeout, when positive, is the per-request HTTP timeout (the cap on a
	// single attempt) applied to the HTTP client and used to derive the per-operation
	// budget. Zero keeps the client's own default (DefaultHTTPTimeout). Set via
	// WithHTTPRequestTimeout, wired by the CLI from autoupdate.http_timeout / --timeout.
	httpReqTimeout time.Duration
	// rateLimiter gates the HTTP hot path: fetchContent waits on it (per host)
	// before every outbound request so parallel checks do not hammer a single
	// host. It is injectable via WithRateLimiter and is never nil after
	// NewChecker (a default 1-req/6s-per-host limiter is created when absent).
	rateLimiter httpRateLimiter
	// concurrency bounds the number of packages CheckAll processes in parallel.
	// It is set via WithConcurrency (validated to 1..maxConcurrency) and
	// defaults to DefaultConcurrency.
	concurrency int
	// progressCallback, when non-nil, is invoked once per package as CheckAll
	// completes that package's work. It is set via WithProgressCallback. It may
	// be called concurrently from worker goroutines; see ProgressCallback.
	progressCallback ProgressCallback
	// cacheTTL, when positive, is passed to the default Cache construction so
	// the user-configured TTL from ~/.config/bentoo/config.yaml reaches Cache.TTL
	// (S002-R2.1, S002-R2.2). Set via WithCacheTTL. Zero (the absence sentinel) keeps the
	// default 1-hour TTL. It is ignored when a Cache is injected via WithCache,
	// since that injected Cache carries its own TTL.
	cacheTTL time.Duration
}

// CheckerOption is a functional option for configuring Checker
type CheckerOption func(*Checker) error

// WithCache sets a custom cache for the checker
func WithCache(cache *Cache) CheckerOption {
	return func(c *Checker) error {
		c.cache = cache
		return nil
	}
}

// WithPendingList sets a custom pending list for the checker
func WithPendingList(pending *PendingList) CheckerOption {
	return func(c *Checker) error {
		c.pending = pending
		return nil
	}
}

// WithLLMClient sets the LLM provider used by --check's version-extraction
// fallback. It accepts any LLMProvider (AD2), so a non-claude provider — which
// the pre-refactor *LLMClient signature could not express — is now valid; the
// legacy *LLMClient still satisfies the interface and remains accepted.
//
// A nil provider is ignored (the field is left untouched, i.e. nil), mirroring
// WithRateLimiter's nil rejection. This is defence-in-depth: the CLI must wire
// this option only with a successfully constructed, non-nil provider, because a
// typed-nil interface (a nil concrete pointer boxed by a failed constructor)
// would pass `!= nil` and make fetchUpstreamVersion call ExtractVersion on a
// nil receiver. Refusing nil here keeps llmClient an untyped nil when no usable
// provider exists.
func WithLLMClient(llm LLMProvider) CheckerOption {
	return func(c *Checker) error {
		if llm != nil {
			c.llmClient = llm
		}
		return nil
	}
}

// WithLLMProviderConfigured records whether the CLI attempted to configure an
// LLM provider for this run (true when autoupdate.llm.provider was non-empty),
// independent of whether the provider was successfully built and wired via
// WithLLMClient. It exists to gate the "unused llm_prompt" Warn so that warning
// fires only when NO provider was configured (S003-R5.3); when a provider was
// configured but failed to construct, runCheck logs its own failure Warn and
// this flag suppresses the duplicate construction Warn. Defaults false, so
// callers that omit it preserve the pre-refactor warn behaviour.
func WithLLMProviderConfigured(configured bool) CheckerOption {
	return func(c *Checker) error {
		c.llmProviderConfigured = configured
		return nil
	}
}

// WithHTTPClient sets a custom HTTP client for the checker
func WithHTTPClient(client *RetryableHTTPClient) CheckerOption {
	return func(c *Checker) error {
		c.httpClient = client
		return nil
	}
}

// WithRateLimiter sets a custom HTTP rate limiter for the checker. The limiter
// is consulted (per host) before every outbound fetch. A nil limiter is
// rejected; when this option is not supplied NewChecker installs a default
// limiter, so the Checker's rateLimiter is never nil after construction.
func WithRateLimiter(limiter httpRateLimiter) CheckerOption {
	return func(c *Checker) error {
		if limiter == nil {
			return errors.New("checker rate limiter must not be nil")
		}
		c.rateLimiter = limiter
		return nil
	}
}

// WithConfigDir sets the configuration directory for cache and pending files
func WithConfigDir(dir string) CheckerOption {
	return func(c *Checker) error {
		c.configDir = dir
		return nil
	}
}

// WithPackagesConfig sets a custom packages configuration
func WithPackagesConfig(config *PackagesConfig) CheckerOption {
	return func(c *Checker) error {
		c.config = config
		return nil
	}
}

// WithContext sets the parent context for the checker. The context threads
// through every outbound HTTP and LLM call, so cancelling it (e.g. on SIGINT or
// a deadline) aborts all in-flight requests. A nil context is rejected.
func WithContext(ctx context.Context) CheckerOption {
	return func(c *Checker) error {
		if ctx == nil {
			return errors.New("checker context must not be nil")
		}
		c.ctx = ctx
		return nil
	}
}

// WithOpTimeout sets the per-operation timeout used to derive a child context
// for each outbound fetch. A non-positive duration is rejected. Setting it marks
// the budget as explicit, so NewChecker will not overwrite it with the value
// derived from WithHTTPRequestTimeout.
func WithOpTimeout(d time.Duration) CheckerOption {
	return func(c *Checker) error {
		if d <= 0 {
			return fmt.Errorf("checker op timeout must be positive, got %v", d)
		}
		c.opTimeout = d
		c.opTimeoutExplicit = true
		return nil
	}
}

// WithHTTPRequestTimeout sets the per-request HTTP timeout — the cap on a single
// outbound attempt. NewChecker applies it to the HTTP client and, unless
// WithOpTimeout overrode the budget explicitly, derives the per-operation budget
// from it so every retry attempt fits within the deadline (see deriveOpTimeout).
// A non-positive duration is a no-op (the client keeps its default), so callers
// that wire this option unconditionally can pass an unresolved zero safely.
func WithHTTPRequestTimeout(d time.Duration) CheckerOption {
	return func(c *Checker) error {
		if d > 0 {
			c.httpReqTimeout = d
		}
		return nil
	}
}

// WithConcurrency sets the maximum number of packages CheckAll processes in
// parallel. n must be in the inclusive range [1, maxConcurrency]; a value
// outside that range is rejected. When this option is not supplied the Checker
// uses DefaultConcurrency.
func WithConcurrency(n int) CheckerOption {
	return func(c *Checker) error {
		if n < 1 || n > maxConcurrency {
			return fmt.Errorf("checker concurrency must be in range [1, %d], got %d", maxConcurrency, n)
		}
		c.concurrency = n
		return nil
	}
}

// WithTypeFilter restricts CheckAll to packages of the given type, "bin" or
// "source". An empty string (the default) checks every package. Type is
// resolved from each package's configured type field, falling back to ebuild
// auto-detection. An unrecognized value is rejected.
func WithTypeFilter(t string) CheckerOption {
	return func(c *Checker) error {
		switch t {
		case "", "bin", "source":
			c.typeFilter = t
			return nil
		default:
			return fmt.Errorf("checker type filter must be 'bin' or 'source', got %q", t)
		}
	}
}

// WithProgressCallback sets a callback invoked once per package as CheckAll
// completes that package's work. A nil callback disables progress reporting.
// See ProgressCallback for the concurrency contract.
func WithProgressCallback(cb ProgressCallback) CheckerOption {
	return func(c *Checker) error {
		c.progressCallback = cb
		return nil
	}
}

// WithCacheTTL sets the TTL applied to the default Cache constructed by
// NewChecker when no Cache is injected via WithCache. It enables
// `autoupdate.cache_ttl` from ~/.config/bentoo/config.yaml to reach Cache.TTL
// (S002-R2.1). A non-positive duration is rejected at construction time (S002-R2.2),
// mirroring WithOpTimeout's validation; the CLI guards the value upstream via
// AutoupdateConfig.GetCacheTTL, so this is defence-in-depth for direct callers.
func WithCacheTTL(d time.Duration) CheckerOption {
	return func(c *Checker) error {
		if d <= 0 {
			return fmt.Errorf("checker cache TTL must be positive, got %v", d)
		}
		c.cacheTTL = d
		return nil
	}
}

// NewChecker creates a new checker instance for the given overlay.
// It loads the packages configuration and initializes cache and pending list.
func NewChecker(overlayPath string, opts ...CheckerOption) (*Checker, error) {
	// Determine config directory
	configDir := filepath.Join(os.Getenv("HOME"), ".config", "bentoo", "autoupdate")

	checker := &Checker{
		overlayPath: overlayPath,
		configDir:   configDir,
		ctx:         context.Background(), // SAFE: default parent; replaced by WithContext when cmd/ wires signal.NotifyContext
		opTimeout:   DefaultOpTimeout,
		concurrency: DefaultConcurrency,
	}

	// Apply options first to allow overriding configDir
	for _, opt := range opts {
		if err := opt(checker); err != nil {
			return nil, fmt.Errorf("failed to apply checker option: %w", err)
		}
	}

	// Load packages configuration if not provided
	if checker.config == nil {
		config, err := LoadPackagesConfig(overlayPath)
		if err != nil {
			return nil, fmt.Errorf("failed to load packages config: %w", err)
		}
		checker.config = config
	}

	// Initialize cache if not provided. When WithCacheTTL set cacheTTL to a
	// positive value, thread it through to the underlying Cache via WithTTL so
	// the user-configured `autoupdate.cache_ttl` is honoured (S002-R2.1). When the
	// option was not supplied (cacheTTL == 0), keep the default 1-hour TTL.
	if checker.cache == nil {
		cacheOpts := []CacheOption{}
		if checker.cacheTTL > 0 {
			cacheOpts = append(cacheOpts, WithTTL(checker.cacheTTL))
		}
		cache, err := NewCache(checker.configDir, cacheOpts...)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize cache: %w", err)
		}
		checker.cache = cache
	}

	// Initialize pending list if not provided
	if checker.pending == nil {
		pending, err := NewPendingList(checker.configDir)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize pending list: %w", err)
		}
		checker.pending = pending
	}

	// Initialize HTTP client if not provided
	if checker.httpClient == nil {
		checker.httpClient = NewRetryableHTTPClient()
	}

	// Apply the configured per-request HTTP timeout to the client and size the
	// per-operation budget from it. Without this, the default per-request timeout
	// and the per-operation budget are equal, so the first slow request consumes
	// the whole budget and the retry attempts never run (they fail with "context
	// deadline exceeded"). Deriving a larger budget gives the retries room to run.
	if checker.httpReqTimeout > 0 {
		checker.httpClient.SetRequestTimeout(checker.httpReqTimeout)
		if !checker.opTimeoutExplicit {
			checker.opTimeout = deriveOpTimeout(checker.httpReqTimeout, checker.httpClient.Config())
		}
	}

	// Authenticate api.github.com requests. Anonymous GitHub API access is capped
	// at 60 req/h per IP, which the batch checker exhausts quickly; the server
	// then answers HTTP 403. The token is resolved from GITHUB_TOKEN/GH_TOKEN via
	// the secrets chain (github.ResolveToken, the single source of truth); a
	// resolution error warns and continues with unauthenticated access. An
	// injected client that already carries a token is left untouched.
	if checker.httpClient.GetGitHubToken() == "" {
		token, err := github.ResolveToken()
		if err != nil {
			warnLogf("resolving GitHub token: %v; continuing with unauthenticated GitHub API access", err)
		}
		if token != "" {
			checker.httpClient.SetGitHubToken(token)
		}
	}

	// Initialize the HTTP rate limiter if not injected. A Checker must never
	// have a nil rateLimiter: fetchContent unconditionally waits on it (S001-R10.3).
	if checker.rateLimiter == nil {
		checker.rateLimiter = NewRateLimiter()
	}

	// S003-R5.3 / S002-R4.2: a non-empty llm_prompt only drives --check when an LLM
	// provider is wired (llmClient != nil). Warn for each affected package so
	// users discover an UNUSED llm_prompt before debugging a silent no-op — but
	// ONLY when no provider was configured for this run (llmProviderConfigured
	// is false). When a provider WAS configured:
	//   - and built successfully, llmClient != nil already suppresses this Warn
	//     and the prompt is honoured;
	//   - and failed to build, runCheck emits its own "provider unavailable"
	//     Warn, so gating on llmProviderConfigured here prevents a confusing
	//     double-warn.
	// Sorted iteration keeps the diagnostic order deterministic. De-duplication
	// is per-Checker (the lifetime of one `bentoo overlay autoupdate --check`
	// run), not process-wide.
	if checker.llmClient == nil && !checker.llmProviderConfigured && checker.config != nil {
		names := make([]string, 0, len(checker.config.Packages))
		for name, pkgCfg := range checker.config.Packages {
			if pkgCfg.LLMPrompt != "" {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		for _, name := range names {
			warnLogf("package %q sets llm_prompt but no LLM is wired into "+
				"the check path; this field is consumed only by "+
				"'bentoo overlay analyze' (see README)", name)
		}
	}

	return checker, nil
}

// CheckPackage checks a single package for updates.
// If force is true, the cache is bypassed and upstream is queried directly.
func (c *Checker) CheckPackage(pkg string, force bool) (*CheckResult, error) {
	result := &CheckResult{
		Package: pkg,
	}

	// Get package configuration
	pkgConfig, exists := c.config.Packages[pkg]
	if !exists {
		result.Error = fmt.Errorf("%w: %s", ErrPackageNotFound, pkg)
		return result, result.Error
	}

	// Get current version from overlay
	currentVersion, err := c.getCurrentVersion(pkg)
	if err != nil {
		result.Error = fmt.Errorf("failed to get current version: %w", err)
		return result, result.Error
	}
	result.CurrentVersion = currentVersion

	// Classify the package (bin vs source) for reporting and filtering. This is
	// metadata only and never blocks the check, so it runs after the version is
	// known and ignores its own errors via resolveType's "source" default.
	result.Type = c.resolveType(pkg, &pkgConfig)

	// Commit-tracked packages always fetch fresh (no cache): the SHA must be
	// current so the applier can substitute it in the ebuild, and caching only
	// the date without the SHA would leave the pending entry unusable.
	if pkgConfig.Track == "commit" {
		info, err := c.fetchCommitInfo(&pkgConfig)
		if err != nil {
			// An unresolved base is a configuration fault, not a transport one;
			// wrapping it as ErrFetchFailed would hide that from callers that
			// branch on the sentinel (and from anyone reading the message).
			if errors.Is(err, ErrBaseVersionUnresolved) {
				result.Error = err
			} else {
				result.Error = fmt.Errorf("%w: %v", ErrFetchFailed, err)
			}
			return result, result.Error
		}

		base := extractSnapshotBase(currentVersion)
		suffix := extractSnapshotSuffix(currentVersion)
		// Adopt the resolved base when it is newer than the ebuild's. The
		// one-way ratchet is deliberate: a momentarily wrong upstream (a
		// reverted bump, a file mid-edit) must not be able to walk the overlay
		// backwards, and a real downgrade is rare enough to want a human.
		if info.NewBase != "" && ebuild.CompareVersions(info.NewBase, base) > 0 {
			base = info.NewBase
		}
		// A tracked commit that IS a release tag gets the bare version, not a
		// snapshot one. vulkan-headers pinned 11d6898, which is exactly tag
		// v1.4.358, yet shipped as 1.4.358_p20260731 — and _p orders ABOVE its
		// base, so the name claimed to be newer than the very release it was.
		//
		// Restricted to _p on purpose. A _pre package's version-bump commit
		// OPENS the cycle rather than closing it (zed's "Bump Zed to v1.15.0"
		// precedes the 1.15.0 release by weeks), so there the snapshot form
		// stays correct.
		newVersion := base + suffix + info.Date
		if info.BaseIsExactTag && suffix == "_p" {
			newVersion = base
		}
		result.UpstreamVersion = newVersion

		// Write to cache so the UI can display the latest known state,
		// even though this entry is never read back as a cache hit.
		if err := c.cache.Set(pkg, newVersion, pkgConfig.URL); err != nil {
			result.Error = fmt.Errorf("failed to update cache: %w", err)
		}

		hasUpdate, comparable := c.compareVersions(newVersion, currentVersion)
		result.HasUpdate = hasUpdate
		result.NotComparable = !comparable

		// Version comparison alone cannot decide a commit-tracked package once a
		// bare release version can be emitted: the overlay would hold 1.4.358
		// while the next check builds 1.4.358_p<today>, which compares NEWER and
		// would re-bump the same commit every single day.
		//
		// So an ebuild already pinned to the tracked commit is up to date — but
		// ONLY while its base version still agrees. A base correction is exactly
		// the case where the commit does not move and the version must: when the
		// registry started reading vulkan-tools' CMakeLists, the pinned commit
		// was already current while the ebuild still said 1.4.354 against
		// upstream's 1.4.357. Suppressing on the SHA alone would have frozen
		// that package at the wrong version for good.
		if result.HasUpdate && extractSnapshotBase(currentVersion) == base {
			if cur := currentEbuildCommit(c.overlayPath, pkg, c.seriesFor(pkg)); cur != "" &&
				strings.EqualFold(cur, info.SHA) {
				result.HasUpdate = false
			}
		}

		if result.HasUpdate {
			if err := c.addToPending(pkg, currentVersion, newVersion, info.SHA, ""); err != nil {
				if result.Error == nil {
					result.Error = fmt.Errorf("failed to add to pending: %w", err)
				}
			}
		}

		return result, nil
	}

	// Check cache first (unless force is true)
	if !force {
		if cachedVersion, ok := c.cache.Get(pkg); ok {
			result.UpstreamVersion = cachedVersion
			result.FromCache = true
			hasUpdate, comparable := c.compareVersions(cachedVersion, currentVersion)
			result.HasUpdate = hasUpdate
			result.NotComparable = !comparable

			// Add to pending if update available
			if result.HasUpdate {
				sha := c.resolveAuxSHA(&pkgConfig, result)
				aux := c.resolveAuxValue(&pkgConfig, result)
				if err := c.addToPending(pkg, currentVersion, cachedVersion, sha, aux); err != nil {
					// Log but don't fail the check
					result.Error = fmt.Errorf("failed to add to pending: %w", err)
				}
			}

			return result, nil
		}
	}

	// Fetch upstream version
	upstreamVersion, err := c.fetchUpstreamVersion(pkg, &pkgConfig)
	if err != nil {
		result.Error = fmt.Errorf("%w: %v", ErrFetchFailed, err)
		return result, result.Error
	}
	result.UpstreamVersion = upstreamVersion

	// Update cache
	if err := c.cache.Set(pkg, upstreamVersion, pkgConfig.URL); err != nil {
		// Log but don't fail the check
		result.Error = fmt.Errorf("failed to update cache: %w", err)
	}

	// Compare versions
	hasUpdate, comparable := c.compareVersions(upstreamVersion, currentVersion)
	result.HasUpdate = hasUpdate
	result.NotComparable = !comparable

	// Add to pending if update available
	if result.HasUpdate {
		sha := c.resolveAuxSHA(&pkgConfig, result)
		aux := c.resolveAuxValue(&pkgConfig, result)
		if err := c.addToPending(pkg, currentVersion, upstreamVersion, sha, aux); err != nil {
			// Log but don't fail the check
			if result.Error == nil {
				result.Error = fmt.Errorf("failed to add to pending: %w", err)
			}
		}
	}

	return result, nil
}

// seriesFor returns the release-line filter configured for pkg, or "" when the
// entry declares none (or is absent, which happens in tests that drive the
// checker without a config).
func (c *Checker) seriesFor(pkg string) string {
	if c.config == nil {
		return ""
	}
	return c.config.Packages[pkg].Series
}

// getCurrentVersion finds the current version of a package in the overlay.
// It looks for ebuild files in the package directory and returns the highest
// version, restricted to the slot when pkg's key carries a ":slot" suffix and to
// the release line when the entry declares a `series`.
func (c *Checker) getCurrentVersion(pkg string) (string, error) {
	best, err := selectCurrentEbuild(c.overlayPath, pkg, c.seriesFor(pkg))
	if err != nil {
		return "", err
	}
	return best.Version, nil
}

// DisableOrphans marks each package as disabled (enabled = false) both in the
// overlay's packages.toml and in the in-memory config, so a package whose ebuild
// was removed from the overlay stops being processed on subsequent runs. The
// file edit is a single atomic, comment-preserving write for the whole batch.
// A nil or empty slice is a no-op. The in-memory config is only updated after
// the file write succeeds, so a failed write leaves both views consistent.
func (c *Checker) DisableOrphans(pkgs []string) error {
	if len(pkgs) == 0 {
		return nil
	}
	if err := DisablePackagesInConfig(c.overlayPath, pkgs); err != nil {
		return err
	}
	disabled := false
	for _, pkg := range pkgs {
		if cfg, ok := c.config.Packages[pkg]; ok {
			cfg.Enabled = &disabled
			c.config.Packages[pkg] = cfg
		}
	}
	return nil
}

// ReviveDisabled re-enables each named package in the overlay's packages.toml
// and in the in-memory config. It is the inverse of DisableOrphans: a package
// that was auto-disabled when its ebuild vanished is reconciled back to enabled
// once that ebuild is present in the overlay again, because the overlay — not
// packages.toml — is the source of truth for whether a package exists. The file
// edit is a comment-preserving DELETION of the existing `enabled = false`
// assignment: enabled is the default, spelled by the key's absence, so writing
// `enabled = true` would state nothing new and would leave a redundant-enabled
// finding for --lint --fix to undo (EnablePackagesInConfig likewise inserts
// nothing for a section that lacks the key). A nil or empty slice is a no-op.
// The in-memory config is updated only after the file write succeeds, so a
// failed write leaves both views consistent.
//
// Callers must exclude held packages (hold = true): a hold is an explicit
// maintainer decision that the overlay reconciliation must never flip.
func (c *Checker) ReviveDisabled(pkgs []string) error {
	if len(pkgs) == 0 {
		return nil
	}
	if err := EnablePackagesInConfig(c.overlayPath, pkgs); err != nil {
		return err
	}
	enabled := true
	for _, pkg := range pkgs {
		if cfg, ok := c.config.Packages[pkg]; ok {
			cfg.Enabled = &enabled
			c.config.Packages[pkg] = cfg
		}
	}
	return nil
}

// ReviveCandidate describes a disabled (orphaned) packages.toml entry whose
// upstream release is strictly newer than the highest version ::gentoo still
// carries. It is a passive report: FindRevivableOrphans never mutates the
// config or the overlay, it only flags entries a later revive step could
// resurrect.
type ReviveCandidate struct {
	// Package is the full package name (category/package).
	Package string
	// GentooVersion is the highest version found in ::gentoo for the package.
	GentooVersion string
	// UpstreamVersion is the version reported by the package's upstream source.
	UpstreamVersion string
}

// FindRevivableOrphans scans the disabled entries in the config and reports
// those an autoupdate could revive: the entry's upstream version is strictly
// newer than the highest version ::gentoo still ships. The normal check path
// skips disabled entries forever (CheckAll: `if !pkg.IsEnabled() { continue }`),
// so without this report a package removed from the overlay would never surface
// an upstream bump that ::gentoo has not yet caught up to.
//
// A candidate must be BOTH disabled AND actually absent from the overlay (a
// true orphan): an enabled entry is handled by the regular check flow, and a
// disabled entry whose ebuild is still present is not revivable from a ::gentoo
// base (that would seed an older version over the newer one already shipped).
// Every network call is best-effort:
// a package whose upstream fetch fails, or that ::gentoo does not carry at all
// (provider.ErrNotFound), is silently skipped rather than aborting the whole
// scan. Other provider errors are surfaced as soft notes in the returned error
// without dropping the candidates gathered so far. The result is sorted by
// package name for deterministic output.
func (c *Checker) FindRevivableOrphans(prov provider.Provider) ([]ReviveCandidate, error) {
	// Iterate in sorted order so soft-error notes (and any debugging) are
	// deterministic; the final slice is sorted again before return.
	names := make([]string, 0, len(c.config.Packages))
	for name := range c.config.Packages {
		names = append(names, name)
	}
	sort.Strings(names)

	var (
		candidates []ReviveCandidate
		notes      []string
	)
	for _, pkg := range names {
		cfg := c.config.Packages[pkg]
		// Only orphaned (disabled) entries are revivable; enabled entries are
		// handled by the normal check flow.
		if cfg.IsEnabled() {
			continue
		}

		// Split category/package the same way getCurrentVersion does, dropping
		// any ":slot" suffix so the ::gentoo lookup below gets a real path.
		category, pkgName, ok := splitPkgAtom(pkg)
		if !ok {
			notes = append(notes, fmt.Sprintf("%s: invalid package name format", pkg))
			continue
		}

		// A genuinely orphaned package has NO ebuild left in the overlay. A
		// disabled entry whose ebuild is still present (e.g. a manually-disabled
		// package, or one re-added after being auto-disabled) is NOT revivable:
		// seeding an older ::gentoo base over the newer overlay ebuild would be
		// wrong. Only ErrNoEbuildFound — the package actually removed — qualifies.
		// Checking the overlay first also skips the upstream/gentoo lookups for
		// packages that are still present.
		if _, err := c.getCurrentVersion(pkg); err == nil {
			continue // ebuild still present: disabled but not orphaned, skip silently
		} else if !errors.Is(err, ErrNoEbuildFound) {
			notes = append(notes, fmt.Sprintf("%s: overlay lookup failed: %v", pkg, err))
			continue
		}

		// Best-effort upstream fetch; a failure just drops this package from the
		// report (it remains disabled, exactly as before).
		upstream, err := c.fetchUpstreamVersion(pkg, &cfg)
		if err != nil {
			notes = append(notes, fmt.Sprintf("%s: upstream fetch failed: %v", pkg, err))
			continue
		}

		// Highest version ::gentoo currently carries. A package ::gentoo does not
		// have is simply not revivable from a gentoo base, so skip it silently.
		versions, err := prov.GetPackageVersions(category, pkgName)
		if err != nil {
			if errors.Is(err, provider.ErrNotFound) {
				continue
			}
			notes = append(notes, fmt.Sprintf("%s: gentoo lookup failed: %v", pkg, err))
			continue
		}
		gentooMax := maxGentooVersion(versions)
		if gentooMax == "" {
			continue
		}

		// Only report when upstream is strictly newer AND the two versions are
		// orderable; an unparseable side must never be reported as revivable.
		hasUpdate, comparable := c.compareVersions(upstream, gentooMax)
		if hasUpdate && comparable {
			candidates = append(candidates, ReviveCandidate{
				Package:         pkg,
				GentooVersion:   gentooMax,
				UpstreamVersion: upstream,
			})
		}
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Package < candidates[j].Package
	})

	if len(notes) > 0 {
		return candidates, fmt.Errorf("revive scan had %d soft error(s): %s",
			len(notes), strings.Join(notes, "; "))
	}
	return candidates, nil
}

// maxGentooVersion returns the highest version from versions using the same
// Gentoo-aware ordering getCurrentVersion uses to pick the highest ebuild.
// Unparseable entries are skipped; "" means no comparable version was found.
func maxGentooVersion(versions []string) string {
	var best string
	for _, v := range versions {
		v = strings.TrimSpace(v)
		if !ebuild.IsValidVersion(v) {
			continue
		}
		if best == "" || ebuild.CompareVersions(v, best) > 0 {
			best = v
		}
	}
	return best
}

// currentEbuildPath returns the absolute path of the highest-version, non-live
// ebuild for pkg. It shares getCurrentVersion's selection but yields the file
// path so callers can read the ebuild's contents (e.g. to auto-detect type).
func (c *Checker) currentEbuildPath(pkg string) (string, error) {
	best, err := selectCurrentEbuild(c.overlayPath, pkg, c.seriesFor(pkg))
	if err != nil {
		return "", err
	}
	return best.Path, nil
}

// resolveType classifies pkg as "bin" or "source". An explicit config type
// wins; otherwise the current ebuild is auto-detected via detectBinaryPackage.
// On any read error it defaults to "source", so an unreadable ebuild is never
// silently dropped from a "source" filter (and a real fetch error surfaces
// later through the normal check path).
func (c *Checker) resolveType(pkg string, cfg *PackageConfig) string {
	if cfg.Type != "" {
		return cfg.Type
	}
	path, err := c.currentEbuildPath(pkg)
	if err != nil {
		return "source"
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return "source"
	}
	if detectBinaryPackage(content) {
		return "bin"
	}
	return "source"
}

// compareVersions compares upstream and current versions. Both sides are
// normalized (whitespace trimmed, a leading "v"/"version-"/etc. prefix
// stripped) before the Gentoo-style comparison so a tag like "v6.6.91" is
// compared against an ebuild "6.6.91" correctly.
//
// hasUpdate is true only when upstream is strictly newer than current.
// comparable is false when either side is not a well-formed version that can be
// ordered; in that case hasUpdate is always false and the caller MUST treat the
// result as a warning rather than "up to date" (parseVersion would otherwise
// coerce junk to 0.0.0 and silently report no update — see ebuild.IsValidVersion).
func (c *Checker) compareVersions(upstream, current string) (hasUpdate, comparable bool) {
	u := stripVersionPrefix(strings.TrimSpace(upstream))
	cur := stripVersionPrefix(strings.TrimSpace(current))
	if !ebuild.IsValidVersion(u) || !ebuild.IsValidVersion(cur) {
		return false, false
	}
	return ebuild.CompareVersions(u, cur) > 0, true
}

// addToPending adds an update to the pending list.
// commitHash is non-empty only for track="commit" packages or version-tracked
// packages with commit_sha_path; auxValue is non-empty only for packages with
// aux_var/aux_pattern. Both are stored in PendingUpdate so the applier can
// substitute the corresponding variable in the copied ebuild.
func (c *Checker) addToPending(pkg, currentVersion, newVersion, commitHash, auxValue string) error {
	update := PendingUpdate{
		Package:        pkg,
		CurrentVersion: currentVersion,
		NewVersion:     newVersion,
		CommitHash:     commitHash,
		AuxValue:       auxValue,
		Status:         StatusPending,
		DetectedAt:     time.Now(),
	}
	return c.pending.Add(update)
}

// resolveAuxSHA fetches the auxiliary commit SHA for a version-tracked package
// that declares commit_sha_path (e.g. cursor's BUILD_ID, which is part of the
// download URL and changes with every release). It returns "" when no SHA path
// is configured. A fetch/parse failure is recorded on result.Error but does not
// abort the update: the apply simply skips the SHA substitution.
//
// Commit-tracked packages (track="commit") resolve their SHA via fetchCommitInfo
// instead and never reach this path.
func (c *Checker) resolveAuxSHA(cfg *PackageConfig, result *CheckResult) string {
	if cfg.CommitSHAPath == "" {
		return ""
	}
	content, err := c.fetchContent(cfg.URL, cfg.Headers, c.operationTimeout(cfg))
	if err != nil {
		if result.Error == nil {
			result.Error = fmt.Errorf("failed to fetch commit sha: %w", err)
		}
		return ""
	}
	sha, err := (&JSONParser{Path: cfg.CommitSHAPath}).Parse(content)
	if err != nil {
		if result.Error == nil {
			result.Error = fmt.Errorf("failed to parse commit sha at %q: %w", cfg.CommitSHAPath, err)
		}
		return ""
	}
	return strings.TrimSpace(sha)
}

// resolveAuxValue captures the free-text auxiliary value for a package that
// declares aux_var/aux_pattern (e.g. betterbird's MY_BUILD="esr-bbNN" or
// nomachine's MY_P build number). It returns "" when no aux_pattern is
// configured. Unlike resolveAuxSHA it is parser-agnostic: the aux_pattern regex
// is applied directly to the fetched body, so regex/html sources work. A
// fetch/parse failure is recorded on result.Error but does not abort the
// update; the apply simply skips the substitution.
func (c *Checker) resolveAuxValue(cfg *PackageConfig, result *CheckResult) string {
	if cfg.AuxPattern == "" {
		return ""
	}
	content, err := c.fetchContent(cfg.URL, cfg.Headers, c.operationTimeout(cfg))
	if err != nil {
		if result.Error == nil {
			result.Error = fmt.Errorf("failed to fetch aux value: %w", err)
		}
		return ""
	}
	re, err := regexp.Compile(cfg.AuxPattern)
	if err != nil {
		// Should not happen: validateConfig already compiled it. Defensive.
		if result.Error == nil {
			result.Error = fmt.Errorf("invalid aux_pattern %q: %w", cfg.AuxPattern, err)
		}
		return ""
	}
	m := re.FindSubmatch(content)
	if len(m) < 2 {
		if result.Error == nil {
			result.Error = fmt.Errorf("aux_pattern %q matched no capture group in %s", cfg.AuxPattern, cfg.URL)
		}
		return ""
	}
	return strings.TrimSpace(string(m[1]))
}

// extractSnapshotBase strips the _p<date> or _pre<date> suffix from a Gentoo
// snapshot version so the base release version can be reused for a new bump.
// "1.4.352_p20260526"   → "1.4.352"
// "26.2.0_pre20260529"  → "26.2.0"
// "3.13.99_p20260517"   → "3.13.99"
// Returns the version unchanged when no snapshot suffix is found.
func extractSnapshotBase(version string) string {
	if i := strings.Index(version, "_p"); i >= 0 {
		return version[:i]
	}
	return version
}

// extractSnapshotSuffix returns the snapshot suffix used by version: "_pre"
// when the version contains "_pre" (pre-release snapshot, version < base in
// Gentoo ordering), or "_p" otherwise (post-release snapshot, version > base).
// Preserving the suffix lets commit-tracked pre-release packages keep their
// _pre ordering so the autoupdate correctly fires when the stable tag arrives.
func extractSnapshotSuffix(version string) string {
	if strings.Contains(version, "_pre") {
		return "_pre"
	}
	return "_p"
}

// commitInfo holds the result of a fetchCommitInfo call.
type commitInfo struct {
	// Date is the commit date formatted as YYYYMMDD (after cfg.Transform).
	Date string
	// SHA is the full 40-char hex commit hash.
	SHA string
	// NewBase is the base version detected in a commit title via
	// CommitVersionPattern. Empty when no version bump was found.
	NewBase string
	// BaseIsExactTag reports that the tracked commit IS the commit a release tag
	// points at, i.e. the ebuild would be packaging that release itself rather
	// than a snapshot taken after it. Only base_from="tag" can know this.
	BaseIsExactTag bool
}

// ebuildCommitRegex matches the commit-hash assignment a snapshot ebuild pins,
// across the four variable names the overlay uses. It is the read counterpart of
// substituteCommitHash's write, and deliberately shares its narrow anchoring.
var ebuildCommitRegex = regexp.MustCompile(
	`(?m)^\s*(?:EGIT_COMMIT|GIT_COMMIT|BUILD_ID)="([0-9a-fA-F]{40})"|^\s*COMMIT=([0-9a-fA-F]{40})\b`)

// currentEbuildCommit returns the 40-hex commit SHA pinned by pkg's current
// ebuild, or "" when there is no ebuild, no such assignment, or the file cannot
// be read.
//
// Returning "" on every failure is the safe direction: the only caller uses a
// match to SUPPRESS an update, so an unreadable ebuild leaves the normal version
// comparison in charge rather than silently freezing the package.
func currentEbuildCommit(overlayPath, pkg, series string) string {
	best, err := selectCurrentEbuild(overlayPath, pkg, series)
	if err != nil || best.Path == "" {
		return ""
	}
	content, err := os.ReadFile(best.Path) //nolint:gosec // path comes from the overlay dir listing
	if err != nil {
		return ""
	}
	m := ebuildCommitRegex.FindSubmatch(content)
	if m == nil {
		return ""
	}
	if len(m[1]) > 0 {
		return string(m[1])
	}
	return string(m[2])
}

// gitRef is one entry of a GitHub /git/refs/tags listing. The object SHA is the
// tag object's for an annotated tag and the commit's for a lightweight one —
// which is why an exact-match test has to accept either.
type gitRef struct {
	Ref    string `json:"ref"`
	Object struct {
		SHA  string `json:"sha"`
		Type string `json:"type"`
	} `json:"object"`
}

// gitLabTag is one entry of a GitLab repository/tags listing.
type gitLabTag struct {
	Name   string `json:"name"`
	Commit struct {
		ID string `json:"id"`
	} `json:"commit"`
}

// resolveBaseFromTag fetches cfg.BaseURL (a tag listing) and returns the highest
// version captured by cfg.BaseTagPattern, plus whether one of those tags points
// exactly at headSHA.
//
// It deliberately does NOT emulate `git describe`: it takes the highest tag of
// the family rather than the highest tag that is an ancestor of the tracked
// commit. Ancestry cannot be read from the tag listing, so proving it would cost
// a /compare call per candidate on every check, and the answer is not even
// clean — measured on SPIRV-Tools, `compare vulkan-sdk-1.4.357.0...main` reports
// "diverged" (ahead 22, behind 1) because the tag lives on a release branch, so
// a strict ancestor test would reject the correct tag and fall seven releases
// back. Highest-of-family matches what the release actually is for every package
// this serves, and the exact-tag test below is precise regardless.
func (c *Checker) resolveBaseFromTag(cfg *PackageConfig, headSHA string) (string, bool, error) {
	content, err := c.fetchContent(cfg.BaseURL, cfg.Headers, c.operationTimeout(cfg))
	if err != nil {
		return "", false, fmt.Errorf("base version tags %s: %w", cfg.BaseURL, err)
	}

	re, err := regexp.Compile(cfg.BaseTagPattern)
	if err != nil {
		return "", false, fmt.Errorf("invalid base_tag_pattern %q: %w", cfg.BaseTagPattern, err)
	}

	names, shas, err := parseTagListing(content)
	if err != nil {
		return "", false, fmt.Errorf("%w: %v (%s)", ErrBaseVersionUnresolved, err, cfg.BaseURL)
	}

	var best, bestSHA string
	exact := false
	for i, name := range names {
		m := re.FindStringSubmatch(name)
		if len(m) < 2 {
			continue
		}
		v := strings.TrimSpace(m[1])
		if !ebuild.IsValidVersion(v) {
			continue
		}
		// An exact hit is recorded whichever tag it is: the tracked commit
		// carrying ANY release tag of the family is what makes the version a
		// release rather than a snapshot.
		if headSHA != "" && shas[i] != "" && strings.EqualFold(shas[i], headSHA) {
			exact = true
		}
		if best == "" || ebuild.CompareVersions(v, best) > 0 {
			best, bestSHA = v, shas[i]
		}
	}

	if best == "" {
		return "", false, fmt.Errorf("%w: base_tag_pattern %q matched no tag among %d at %s",
			ErrBaseVersionUnresolved, cfg.BaseTagPattern, len(names), cfg.BaseURL)
	}
	// Only the HIGHEST tag matching the head makes this a release build: an
	// older tag pointing at the same commit would still leave newer releases
	// ahead of it.
	if exact && !strings.EqualFold(bestSHA, headSHA) {
		exact = false
	}
	return best, exact, nil
}

// parseTagListing accepts either shape of tag listing the registry points at:
// GitHub's /git/refs/tags (a list of refs) and GitLab's repository/tags. It
// returns parallel name/SHA slices; a SHA is "" when the listing does not
// expose a usable one.
func parseTagListing(content []byte) (names, shas []string, err error) {
	var refs []gitRef
	if e := json.Unmarshal(content, &refs); e == nil && len(refs) > 0 && refs[0].Ref != "" {
		for _, r := range refs {
			name := strings.TrimPrefix(r.Ref, "refs/tags/")
			sha := r.Object.SHA
			// An annotated tag's object is the tag, not the commit; there is no
			// commit SHA in this listing, so drop it rather than compare the
			// wrong thing and call a snapshot a release.
			if r.Object.Type == "tag" {
				sha = ""
			}
			names = append(names, name)
			shas = append(shas, sha)
		}
		return names, shas, nil
	}

	var tags []gitLabTag
	if e := json.Unmarshal(content, &tags); e == nil && len(tags) > 0 && tags[0].Name != "" {
		for _, t := range tags {
			names = append(names, t.Name)
			shas = append(shas, t.Commit.ID)
		}
		return names, shas, nil
	}

	return nil, nil, errors.New("tag listing is neither a GitHub refs array nor a GitLab tags array")
}

// fetchCommitInfo fetches cfg.URL once (expected to be a JSON array of commits)
// and extracts the date, SHA, and — when CommitVersionPattern is set — the
// highest base version found in commit titles since the last snapshot.
// Called only when cfg.Track == "commit".
func (c *Checker) fetchCommitInfo(cfg *PackageConfig) (*commitInfo, error) {
	content, err := c.fetchContent(cfg.URL, cfg.Headers, c.operationTimeout(cfg))
	if err != nil {
		return nil, err
	}

	// Extract date of the latest commit (path points to [0].commit.committer.date
	// or [0].committed_date, etc.) then apply transforms to get YYYYMMDD.
	dateParser := &JSONParser{Path: cfg.Path}
	raw, err := dateParser.Parse(content)
	if err != nil {
		return nil, fmt.Errorf("commit date: %w", err)
	}
	date := applyTransforms(raw, cfg.Transform)
	if date == "" {
		return nil, fmt.Errorf("commit date: empty after transform (raw: %q)", raw)
	}

	// Extract SHA of the latest commit.
	shaParser := &JSONParser{Path: cfg.CommitSHAPath}
	sha, err := shaParser.Parse(content)
	if err != nil {
		return nil, fmt.Errorf("commit sha: %w", err)
	}
	sha = strings.TrimSpace(sha)
	if sha == "" {
		return nil, fmt.Errorf("commit sha: empty value at path %q", cfg.CommitSHAPath)
	}

	info := &commitInfo{Date: date, SHA: sha}

	// Resolve the base version from its declared source. base_from = "file"
	// fetches a second URL; everything else reads the commit list already in
	// hand. An entry that declares a source and gets nothing back fails the
	// check — see ErrBaseVersionUnresolved for why silence is not an option.
	switch {
	case cfg.BaseFrom == "file":
		base, err := c.resolveBaseFromFile(cfg)
		if err != nil {
			return nil, err
		}
		info.NewBase = base

	case cfg.BaseFrom == "tag":
		base, exact, err := c.resolveBaseFromTag(cfg, sha)
		if err != nil {
			return nil, err
		}
		info.NewBase, info.BaseIsExactTag = base, exact

	case cfg.CommitVersionPattern != "" && cfg.CommitMessagePath != "":
		// Covers both base_from = "commit_message" and the legacy form that
		// predates the field, so existing registries keep working unchanged.
		info.NewBase = scanCommitsForVersion(content, cfg.CommitMessagePath, cfg.CommitVersionPattern)
		if info.NewBase == "" {
			return nil, fmt.Errorf("%w: commit_version_pattern %q matched no commit title at %q in %s "+
				"(the bump may have fallen outside the fetch window — raise per_page, or move to base_from=\"file\")",
				ErrBaseVersionUnresolved, cfg.CommitVersionPattern, cfg.CommitMessagePath, cfg.URL)
		}
	}

	return info, nil
}

// resolveBaseFromFile fetches cfg.BaseURL and extracts the base version with
// cfg.BasePattern. It is the strongest of the base-version providers: a single
// request against a file the upstream itself maintains, with no dependency on
// how many commits fit in a window or on which tag an ancestor carries.
//
// ValidatePackageConfig has already checked that the pattern compiles and has
// exactly one capture group, so the failures left here are runtime ones: the
// file moved, the branch was renamed, or upstream restructured its version
// declaration. All three must be loud — a base that silently stops advancing
// looks identical to one that is simply up to date.
func (c *Checker) resolveBaseFromFile(cfg *PackageConfig) (string, error) {
	content, err := c.fetchContent(cfg.BaseURL, cfg.Headers, c.operationTimeout(cfg))
	if err != nil {
		return "", fmt.Errorf("base version file %s: %w", cfg.BaseURL, err)
	}

	re, err := regexp.Compile(cfg.BasePattern)
	if err != nil {
		// Unreachable via a validated config; defensive for direct construction.
		return "", fmt.Errorf("invalid base_pattern %q: %w", cfg.BasePattern, err)
	}

	m := re.FindSubmatch(content)
	if len(m) < 2 {
		return "", fmt.Errorf("%w: base_pattern %q matched nothing in %s",
			ErrBaseVersionUnresolved, cfg.BasePattern, cfg.BaseURL)
	}

	base := strings.TrimSpace(string(m[1]))
	if !ebuild.IsValidVersion(base) {
		return "", fmt.Errorf("%w: base_pattern %q captured %q from %s, which is not a valid Gentoo version",
			ErrBaseVersionUnresolved, cfg.BasePattern, base, cfg.BaseURL)
	}
	return base, nil
}

// scanCommitsForVersion iterates over a JSON array of commit objects and
// returns the highest Gentoo-comparable version found in commit titles via
// versionPattern (one capture group). messageRelPath is the JSON path
// relative to each array element that yields the commit title string (e.g.
// "commit.message" for GitHub, "title" for GitLab). Returns "" when no match
// is found or when the content cannot be parsed as an array.
func scanCommitsForVersion(content []byte, messageRelPath, versionPattern string) string {
	re, err := regexp.Compile(versionPattern)
	if err != nil {
		return ""
	}

	// Unmarshal into a generic array; non-array responses (e.g. error JSON)
	// produce a harmless empty result rather than a hard failure.
	var commits []json.RawMessage
	if err := json.Unmarshal(content, &commits); err != nil {
		return ""
	}

	var best string
	for _, raw := range commits {
		// Unmarshal each element into interface{} for navigateJSONPath.
		var elem interface{}
		if err := json.Unmarshal(raw, &elem); err != nil {
			continue
		}
		val, err := navigateJSONPath(elem, messageRelPath)
		if err != nil {
			continue
		}
		msg, ok := val.(string)
		if !ok || msg == "" {
			continue
		}
		m := re.FindStringSubmatch(msg)
		if len(m) < 2 {
			continue
		}
		v := strings.TrimSpace(m[1])
		if !ebuild.IsValidVersion(v) {
			continue
		}
		if best == "" || ebuild.CompareVersions(v, best) > 0 {
			best = v
		}
	}
	return best
}

// fetchUpstreamVersion fetches and parses the upstream version for a package,
// then applies the record's pre-release suffix.
//
// The suffix is applied here, on the single value every extraction path
// converges to, so that script/fallback/LLM results are marked exactly like the
// primary one — those paths bypass fetchAndParse and would otherwise emit a bare
// version for a record that declares a development channel. selectVersion also
// applies it per candidate so "max" orders the final values; applySuffix is
// idempotent, so the second pass is a no-op.
func (c *Checker) fetchUpstreamVersion(pkg string, cfg *PackageConfig) (string, error) {
	version, err := c.fetchUpstreamVersionRaw(pkg, cfg)
	if err != nil {
		return "", err
	}
	version = applySuffix(version, cfg)

	// An entry restricted to a release line must never report a version from
	// another one: it would be compared against — and could bump — the ebuild of
	// a line it does not track. The select path already drops such candidates
	// per candidate; this covers the paths that yield a single value (first
	// match, script, fallback, LLM). Failing loudly is the point: the entry's
	// source moved, or its series is wrong, and both need a human.
	if m := newSeriesMatcher(cfg.Series); m.active() && !m.matches(stripVersionPrefix(version)) {
		return "", fmt.Errorf("%w: upstream version %q is outside this entry's series %q",
			ErrNoVersionFound, version, cfg.Series)
	}
	return version, nil
}

// fetchUpstreamVersionRaw fetches and parses the upstream version for a package.
// It tries the primary URL/parser first, then fallback if configured, then LLM if available.
func (c *Checker) fetchUpstreamVersionRaw(pkg string, cfg *PackageConfig) (string, error) {
	// The script parser drives a headless browser itself, so it bypasses
	// fetchContent/fetchAndParse entirely (and therefore transform/select, which
	// the script handles in JS — see ValidatePackageConfig). It has no fallback
	// or LLM stage: the script is the single source of truth.
	if cfg.Parser == "script" {
		return c.parseLive(cfg)
	}

	// Try primary URL
	version, err := c.fetchAndParse(cfg.URL, cfg)
	if err == nil {
		return version, nil
	}
	primaryErr := err

	// Try fallback URL if configured
	if cfg.FallbackURL != "" && cfg.FallbackParser != "" {
		fallbackPattern := cfg.FallbackPattern
		if fallbackPattern == "" && cfg.FallbackParser == "json" {
			fallbackPattern = cfg.Path // Use primary path for JSON fallback
		}

		// Derive a config for the fallback URL: it swaps in the fallback
		// parser/pattern but keeps the primary path/selector/xpath and the
		// transform/select post-processing so the fallback behaves consistently.
		fallbackCfg := &PackageConfig{
			Parser:    cfg.FallbackParser,
			Path:      cfg.Path,
			Pattern:   fallbackPattern,
			Selector:  cfg.Selector,
			XPath:     cfg.XPath,
			Transform: cfg.Transform,
			Select:    cfg.Select,
		}
		version, err = c.fetchAndParse(cfg.FallbackURL, fallbackCfg)
		if err == nil {
			return version, nil
		}
	}

	// Try LLM if configured and available
	if c.llmClient != nil && cfg.LLMPrompt != "" {
		// Fetch content from primary URL for LLM
		content, err := c.fetchContent(cfg.URL, cfg.Headers, c.operationTimeout(cfg))
		if err == nil {
			version, err = c.llmClient.ExtractVersion(content, cfg.LLMPrompt)
			if err == nil {
				return version, nil
			}
		}
	}

	// All methods failed
	return "", fmt.Errorf("all version extraction methods failed: %w", primaryErr)
}

// fetchAndParse fetches content from rawURL and extracts a version from it.
//
// It takes the whole *PackageConfig so it can apply the post-extraction stages:
//   - select: when cfg.Select is "max"/"last", every candidate is extracted
//     (via newSelectExtractor, reusing the version_history.go list extractors),
//     each is transformed, and selectVersion picks one. A parser that cannot
//     produce a list warns and falls through to first-match.
//   - transform: cfg.Transform regex substitutions run on the single extracted
//     version (the select path transforms per candidate inside selectVersion).
//
// The parser itself is built via NewParserFromConfig so every configured parser
// type is supported — including "html", whose selector/xpath fields wire the
// scrape plus optional regex post-processing (carried in Pattern).
func (c *Checker) fetchAndParse(rawURL string, cfg *PackageConfig) (string, error) {
	// Fetch content
	content, err := c.fetchContent(rawURL, cfg.Headers, c.operationTimeout(cfg))
	if err != nil {
		return "", err
	}

	// select path: collect all candidates, transform each, then pick one.
	if cfg.Select != "" && cfg.Select != "first" {
		extractor, exErr := newSelectExtractor(cfg)
		if exErr != nil {
			return "", fmt.Errorf("failed to create select extractor: %w", exErr)
		}
		if extractor != nil {
			cands, cErr := extractor.ExtractVersions(content)
			if cErr != nil {
				return "", fmt.Errorf("failed to extract version candidates: %w", cErr)
			}
			best := selectVersion(cands, cfg)
			if best == "" {
				return "", fmt.Errorf("%w: no comparable version among %d candidate(s) for select=%q",
					ErrNoVersionFound, len(cands), cfg.Select)
			}
			return best, nil
		}
		// Not list-capable (e.g. parser="script"): warn and use first match.
		warnLogf("select=%q requested but parser %q cannot extract a list; using first match",
			cfg.Select, cfg.Parser)
	}

	// Create parser. NewParserFromConfig handles json/regex/html uniformly.
	parser, err := NewParserFromConfig(cfg)
	if err != nil {
		return "", fmt.Errorf("failed to create parser: %w", err)
	}

	// Parse content, then apply transform to the single extracted version.
	version, err := parser.Parse(content)
	if err != nil {
		return "", fmt.Errorf("failed to parse version: %w", err)
	}
	version = applyTransforms(version, cfg.Transform)

	return version, nil
}

// parseLive runs a parser="script" check. It resolves the script body (inline,
// or "@file.js" loaded from <overlay>/.autoupdate/scripts/), gates on the
// per-host rate limiter exactly like fetchContent, then evaluates the script
// against the rendered page under a child context bounded by opTimeout.
//
// The headless-browser backend is opt-in: in a binary built without the
// `playwright` tag, newLiveEvaluator returns ErrScriptSupportNotBuilt and this
// surfaces as the package's check error.
//
// A fresh evaluator is created per call and closed afterward (if it implements
// io.Closer); reusing one browser across the batch is a future optimization, but
// the script-package count is tiny (the LibreOffice group), so launch cost is
// acceptable and per-call isolation avoids shared-state concurrency hazards.
func (c *Checker) parseLive(cfg *PackageConfig) (string, error) {
	scriptsDir := filepath.Join(c.overlayPath, ".autoupdate", "scripts")
	body, err := resolveScript(cfg.Script, scriptsDir)
	if err != nil {
		return "", err
	}

	// Gate on the per-host rate limiter (same policy as fetchContent), waiting on
	// the parent context so the wait is signal-cancellable and not charged to the
	// per-operation timeout. Fail open on an unparseable URL.
	if parsed, perr := url.Parse(cfg.URL); perr != nil {
		warnLogf("rate limiter: could not parse URL %q for host extraction (%v); "+
			"proceeding without a rate-limit wait", cfg.URL, perr)
	} else if werr := c.rateLimiter.WaitHTTP(c.ctx, parsed.Host); werr != nil {
		if ctxErr := c.ctx.Err(); ctxErr != nil {
			return "", fmt.Errorf("rate limiter wait cancelled: %w", ctxErr)
		}
		return "", fmt.Errorf("rate limiter wait failed: %w", werr)
	}

	// Honour a per-package timeout for the script/browser path too, falling back
	// to the global budget when unset.
	opTimeout := c.operationTimeout(cfg)
	eval, err := newLiveEvaluator(opTimeout)
	if err != nil {
		return "", err
	}
	if closer, ok := eval.(io.Closer); ok {
		defer closer.Close()
	}

	// opTimeout bounds only the navigation/evaluation, starting after the token.
	ctx, cancel := context.WithTimeout(c.ctx, opTimeout)
	defer cancel()

	parser := &ScriptParser{URL: cfg.URL, Script: body, Headers: cfg.Headers, eval: eval}
	return parser.ParseLive(ctx)
}

// fetchContent fetches content from a URL using the HTTP client with retry logic.
//
// It first gates on the per-host rate limiter (S001-R10.1), waiting on the Checker's
// parent context (set via WithContext) so the wait is signal-cancellable but
// NOT bounded by the per-operation timeout. The host is parsed from the URL and
// c.rateLimiter.WaitHTTP blocks until a token is available; if the wait is
// cancelled by the parent context the context error is returned and no HTTP
// request is made (S001-R10.2). A URL that fails to parse fails open (S001-R10.1): a Warn
// line is logged and the fetch proceeds without a rate-limit wait.
//
// Only after a token is acquired does the per-operation timeout start: the HTTP
// request is bounded by a child of the parent context with that timeout, so a
// cancelled parent or an expired deadline aborts the in-flight call. This keeps
// time spent queued behind the rate limiter from being charged against the HTTP
// deadline, which previously made packages sharing a host fail spuriously.
//
// headers carries the per-package custom headers from packages.toml (cfg.Headers);
// they are merged with the client's default User-Agent and, for api.github.com
// URLs, the configured GitHub token. Passing them through GetWithHeadersContext
// (rather than the bare GetWithContext) is what actually puts the User-Agent,
// the Authorization token, and any TOML-declared headers on the wire.
func (c *Checker) fetchContent(rawURL string, headers map[string]string, opTimeout time.Duration) ([]byte, error) {
	// Gate on the per-host rate limiter FIRST, waiting on the parent context
	// rather than an opTimeout-bounded one. The wait must not be charged against
	// the per-request HTTP deadline: when many packages share a host, a queued
	// package can wait several limiter intervals, and folding that into
	// opTimeout made late packages fail with "context deadline exceeded" before
	// any request was issued. The parent context still carries SIGINT/SIGTERM,
	// so a cancelled wait aborts without issuing the request (S001-R10.2).
	//
	// Fail open on a parse error: an unparseable URL still gets a
	// (rate-limit-free) attempt rather than silently dropping the fetch.
	if parsed, err := url.Parse(rawURL); err != nil {
		warnLogf("rate limiter: could not parse URL %q for host extraction (%v); "+
			"proceeding without a rate-limit wait", rawURL, err)
	} else if waitErr := c.rateLimiter.WaitHTTP(c.ctx, parsed.Host); waitErr != nil {
		// The wait did not yield a token. If the parent context is done the wait
		// was cancelled (parent cancelled or deadline exceeded): return the
		// context error WITHOUT issuing the HTTP request (S001-R10.2). Prefer the raw
		// context error so callers' errors.Is(err, context.Canceled /
		// .DeadlineExceeded) checks hold regardless of how the limiter wraps it.
		if ctxErr := c.ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("rate limiter wait cancelled: %w", ctxErr)
		}
		// A non-context wait failure (e.g. the request can never satisfy the
		// limiter's burst): surface it rather than issuing a doomed request.
		return nil, fmt.Errorf("rate limiter wait failed: %w", waitErr)
	}

	// The per-operation timeout bounds only the HTTP round-trip; its deadline
	// starts now, after the rate-limit token has been acquired. opTimeout is the
	// per-package or global budget the caller resolved via operationTimeout.
	ctx, cancel := context.WithTimeout(c.ctx, opTimeout)
	defer cancel()

	resp, err := c.httpClient.GetWithHeadersContext(ctx, rawURL, headers)
	if err != nil {
		// Name the host and the per-request cap so a timeout points the user at
		// the slow endpoint and the knob to raise (autoupdate.http_timeout /
		// --timeout, or a per-package timeout in packages.toml).
		return nil, fmt.Errorf("HTTP request to %s failed (per-request timeout %s): %w",
			hostForError(rawURL), c.httpClient.Config().Timeout, err)
	}
	defer resp.Body.Close()

	// 206 counts as success ONLY as the answer to a Range the request that
	// reached the server actually carried (S020-R1.1, S020-R1.2): a record may ask for a
	// byte range to read a pattern that lives near the front of a large file, and
	// the server answers that partial request with 206 rather than 200.
	// www-misc/warsaw is the motivating case — an 8.2 MB payload whose version
	// string sits ~590 KB in (S019-UB4).
	//
	// The evidence is the request recorded on resp, NOT the headers map above:
	// the map is what was asked for, the response carries what was actually sent,
	// and applyHeaders rewrites and supplements the one into the other. Observing
	// the wire keeps the acceptance policy in a single place, unable to drift out
	// of step with header application again — sentRequestDeclaresRange documents
	// each way the two diverge.
	//
	// An UNSOLICITED 206 — one no Range asked for, a protocol violation seen
	// behind some CDNs and proxies — must fail safe with the status error
	// instead (S020-R1.3). Its body is a fragment by definition, and a truncated
	// fragment does not look like a failure to the version parser: it looks
	// like a successful read, so the checker would silently record a stale or
	// simply wrong version with no error anywhere to show for it.
	acceptedStatuses := []int{http.StatusOK}
	if sentRequestDeclaresRange(resp) {
		acceptedStatuses = append(acceptedStatuses, http.StatusPartialContent)
	}

	// readBodyForStatus enumerates the accepted codes rather than admitting the
	// whole 2xx range (204/205 have no body by definition — see its doc), then
	// reads the body and translates an http.MaxBytesReader overflow into
	// ErrResponseTooLarge (S019-R3.1, S019-R3.2, S001-R11.3). The cap is imposed upstream by
	// GetWithHeadersContext, not here. Its errors are already phrased for the
	// user, so they are returned as-is rather than re-wrapped.
	content, err := readBodyForStatus(resp, acceptedStatuses...)
	if err != nil {
		return nil, err
	}

	return content, nil
}

// CheckAll checks all packages in the configuration for updates.
// If force is true, the cache is bypassed for all packages.
//
// It returns a BatchResult: successfully checked packages land in Items, while
// a per-package failure is recorded in Failures keyed by the package name.
//
// Packages are processed concurrently, bounded by the Checker's concurrency
// limit (see WithConcurrency). The semaphore is acquired with a
// context-cancellable select: if the Checker's parent context (WithContext) is
// already cancelled, the remaining packages are not dispatched — each is
// recorded in Failures with the context error instead — so a SIGINT mid-scan
// stops the batch promptly. Every worker recovers panics raised by
// CheckPackage and records them as a failure, so a single misbehaving package
// cannot crash the process. All writes to the shared result maps are
// mutex-guarded.
//
// Items are sorted lexically by package name before the BatchResult is
// returned, so the output is deterministic regardless of completion order. The
// returned BatchResult is fully populated only after every worker goroutine
// has joined (wg.Wait), so callers may invoke its methods (ExitCode,
// FormatFailures) directly.
func (c *Checker) CheckAll(force bool) BatchResult[CheckResult] {
	// Reconcile status with the overlay BEFORE filtering: the overlay — not
	// packages.toml — is the source of truth for whether a package exists. A
	// package auto-disabled (enabled = false) when its ebuild vanished must not
	// stay disabled forever once that ebuild is re-added; here it is re-enabled so
	// the status follows the file rather than the other way around. A held package
	// (hold = true) is left untouched: that is a deliberate maintainer decision,
	// not stale bookkeeping. The in-memory rewrite makes the filter below pick the
	// revived packages up in this same run.
	var revived []string
	for name, pkg := range c.config.Packages {
		if pkg.IsEnabled() || pkg.IsHeld() {
			continue
		}
		if _, err := c.getCurrentVersion(name); err == nil {
			revived = append(revived, name) // ebuild present again → reconcile to enabled
		}
	}
	if len(revived) > 0 {
		sort.Strings(revived)
		if err := c.ReviveDisabled(revived); err != nil {
			warnLogf("failed to re-enable %d package(s) whose ebuild reappeared in the overlay: %v", len(revived), err)
		} else {
			for _, p := range revived {
				logger.Info("re-enabled %q: its ebuild is present in the overlay again", p)
			}
		}
	}

	// Narrow the package set up front so excluded packages incur no network
	// fetch and are absent from progress and totals. Three filters apply:
	//   - enabled = false: always skipped, silently (no log, no count);
	//   - hold = true: maintainer-held, skipped silently like a disabled entry;
	//   - type filter (when active): keep only the matching bin/source class.
	pkgs := make(map[string]PackageConfig, len(c.config.Packages))
	for name, pkg := range c.config.Packages {
		if !pkg.IsEnabled() || pkg.IsHeld() {
			continue
		}
		if c.typeFilter != "" && c.resolveType(name, &pkg) != c.typeFilter {
			continue
		}
		pkgs[name] = pkg
	}

	var (
		sem      = make(chan struct{}, c.concurrency)
		wg       sync.WaitGroup
		mu       sync.Mutex
		results  = make([]CheckResult, 0, len(pkgs))
		failures = make(map[string]error)
		orphaned []string
		progress atomic.Uint64
		total    = uint64(len(pkgs))
	)

	for name, pkg := range pkgs {
		// A select with both cases ready picks at random, so check the context
		// deterministically first: an already-cancelled context must mark
		// EVERY remaining package as a failure, not just roughly half of them.
		if err := c.ctx.Err(); err != nil {
			mu.Lock()
			failures[name] = err
			mu.Unlock()
			continue
		}
		// Cancellable semaphore acquisition: also record a context failure if
		// the parent context is cancelled while waiting for a free slot.
		select {
		case <-c.ctx.Done():
			mu.Lock()
			failures[name] = c.ctx.Err()
			mu.Unlock()
			continue
		case sem <- struct{}{}:
		}

		wg.Add(1)
		go func(n string, p PackageConfig) {
			defer wg.Done()
			defer func() { <-sem }()
			// A panic in CheckPackage (or anything it calls) must not crash
			// the process: recover it and record a per-package failure.
			defer func() {
				if r := recover(); r != nil {
					mu.Lock()
					failures[n] = fmt.Errorf("panic: %v", r)
					mu.Unlock()
				}
			}()

			result, err := c.CheckPackage(n, force)

			mu.Lock()
			switch {
			case err != nil && errors.Is(err, ErrNoEbuildFound):
				// The ebuild was removed from the overlay. Don't record a
				// recurring failure: queue the package for auto-disable after
				// the run and surface it as an informational result so it does
				// not count toward the failure exit code.
				orphaned = append(orphaned, n)
				results = append(results, CheckResult{Package: n, Orphaned: true})
			case err != nil:
				failures[n] = err
			default:
				results = append(results, *result)
			}
			mu.Unlock()

			if c.progressCallback != nil {
				c.progressCallback(progress.Add(1), total)
			}
		}(name, pkg)
	}

	// Join every worker before touching the shared state so the BatchResult is
	// fully populated and safe to return.
	wg.Wait()

	// Auto-disable packages whose ebuild vanished from the overlay. A single
	// batched write keeps the hand-maintained packages.toml's comments intact;
	// a failure here is non-fatal — the run's results still stand and the entry
	// is simply retried (and re-reported) next time.
	if len(orphaned) > 0 {
		if err := c.DisableOrphans(orphaned); err != nil {
			warnLogf("failed to auto-disable %d orphaned package(s) in packages.toml: %v", len(orphaned), err)
		}
	}

	// Deterministic final ordering, independent of completion order.
	sort.Slice(results, func(i, j int) bool {
		return results[i].Package < results[j].Package
	})

	return BatchResult[CheckResult]{Items: results, Failures: failures}
}

// Config returns the packages configuration.
func (c *Checker) Config() *PackagesConfig {
	return c.config
}

// Cache returns the cache instance.
func (c *Checker) Cache() *Cache {
	return c.cache
}

// Pending returns the pending list instance.
func (c *Checker) Pending() *PendingList {
	return c.pending
}

// OverlayPath returns the overlay path.
func (c *Checker) OverlayPath() string {
	return c.overlayPath
}
