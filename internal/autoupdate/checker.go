// Package autoupdate provides version checking functionality for ebuild autoupdate.
package autoupdate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/ebuild"
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
}

// DefaultOpTimeout is the default per-operation timeout applied to a single
// outbound HTTP fetch when no explicit timeout is configured on the Checker.
const DefaultOpTimeout = 30 * time.Second

// DefaultConcurrency is the default number of packages CheckAll processes in
// parallel when no explicit concurrency is configured on the Checker.
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
	// configured at all (R5.3). When a provider was configured but failed to
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
	opTimeout time.Duration
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
	// (R2.1, R2.2). Set via WithCacheTTL. Zero (the absence sentinel) keeps the
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
// fires only when NO provider was configured (R5.3); when a provider was
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
// for each outbound fetch. A non-positive duration is rejected.
func WithOpTimeout(d time.Duration) CheckerOption {
	return func(c *Checker) error {
		if d <= 0 {
			return fmt.Errorf("checker op timeout must be positive, got %v", d)
		}
		c.opTimeout = d
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
// (R2.1). A non-positive duration is rejected at construction time (R2.2),
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
	// the user-configured `autoupdate.cache_ttl` is honoured (R2.1). When the
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

	// Initialize the HTTP rate limiter if not injected. A Checker must never
	// have a nil rateLimiter: fetchContent unconditionally waits on it (R10.3).
	if checker.rateLimiter == nil {
		checker.rateLimiter = NewRateLimiter()
	}

	// R5.3 / R4.2: a non-empty llm_prompt only drives --check when an LLM
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
				if err := c.addToPending(pkg, currentVersion, cachedVersion); err != nil {
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
		if err := c.addToPending(pkg, currentVersion, upstreamVersion); err != nil {
			// Log but don't fail the check
			if result.Error == nil {
				result.Error = fmt.Errorf("failed to add to pending: %w", err)
			}
		}
	}

	return result, nil
}

// getCurrentVersion finds the current version of a package in the overlay.
// It looks for ebuild files in the package directory and returns the highest version.
func (c *Checker) getCurrentVersion(pkg string) (string, error) {
	// Parse package name (category/package)
	parts := strings.Split(pkg, "/")
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid package name format: %s", pkg)
	}
	category := parts[0]
	pkgName := parts[1]

	// Build package directory path
	pkgDir := filepath.Join(c.overlayPath, category, pkgName)

	// Read directory entries
	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("%w: %s", ErrNoEbuildFound, pkg)
		}
		return "", fmt.Errorf("failed to read package directory: %w", err)
	}

	// Find all ebuild files and extract versions
	var highestVersion string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if !strings.HasSuffix(name, ".ebuild") {
			continue
		}

		// Skip live ebuilds (9999)
		if strings.Contains(name, "-9999.ebuild") {
			continue
		}

		// Parse ebuild path to extract version
		ebuildPath := filepath.Join(category, pkgName, name)
		eb, err := ebuild.ParsePath(ebuildPath)
		if err != nil {
			continue // Skip invalid ebuild files
		}

		// Compare with highest version found so far
		if highestVersion == "" || ebuild.CompareVersions(eb.Version, highestVersion) > 0 {
			highestVersion = eb.Version
		}
	}

	if highestVersion == "" {
		return "", fmt.Errorf("%w: %s", ErrNoEbuildFound, pkg)
	}

	return highestVersion, nil
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
func (c *Checker) addToPending(pkg, currentVersion, newVersion string) error {
	update := PendingUpdate{
		Package:        pkg,
		CurrentVersion: currentVersion,
		NewVersion:     newVersion,
		Status:         StatusPending,
		DetectedAt:     time.Now(),
	}
	return c.pending.Add(update)
}

// fetchUpstreamVersion fetches and parses the upstream version for a package.
// It tries the primary URL/parser first, then fallback if configured, then LLM if available.
func (c *Checker) fetchUpstreamVersion(pkg string, cfg *PackageConfig) (string, error) {
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
		content, err := c.fetchContent(cfg.URL)
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
	content, err := c.fetchContent(rawURL)
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
			best := selectVersion(cands, cfg.Transform, cfg.Select)
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

	eval, err := newLiveEvaluator(c.opTimeout)
	if err != nil {
		return "", err
	}
	if closer, ok := eval.(io.Closer); ok {
		defer closer.Close()
	}

	// opTimeout bounds only the navigation/evaluation, starting after the token.
	ctx, cancel := context.WithTimeout(c.ctx, c.opTimeout)
	defer cancel()

	parser := &ScriptParser{URL: cfg.URL, Script: body, Headers: cfg.Headers, eval: eval}
	return parser.ParseLive(ctx)
}

// fetchContent fetches content from a URL using the HTTP client with retry logic.
//
// It first gates on the per-host rate limiter (R10.1), waiting on the Checker's
// parent context (set via WithContext) so the wait is signal-cancellable but
// NOT bounded by the per-operation timeout. The host is parsed from the URL and
// c.rateLimiter.WaitHTTP blocks until a token is available; if the wait is
// cancelled by the parent context the context error is returned and no HTTP
// request is made (R10.2). A URL that fails to parse fails open (R10.1): a Warn
// line is logged and the fetch proceeds without a rate-limit wait.
//
// Only after a token is acquired does the per-operation timeout start: the HTTP
// request is bounded by a child of the parent context with that timeout, so a
// cancelled parent or an expired deadline aborts the in-flight call. This keeps
// time spent queued behind the rate limiter from being charged against the HTTP
// deadline, which previously made packages sharing a host fail spuriously.
func (c *Checker) fetchContent(rawURL string) ([]byte, error) {
	// Gate on the per-host rate limiter FIRST, waiting on the parent context
	// rather than an opTimeout-bounded one. The wait must not be charged against
	// the per-request HTTP deadline: when many packages share a host, a queued
	// package can wait several limiter intervals, and folding that into
	// opTimeout made late packages fail with "context deadline exceeded" before
	// any request was issued. The parent context still carries SIGINT/SIGTERM,
	// so a cancelled wait aborts without issuing the request (R10.2).
	//
	// Fail open on a parse error: an unparseable URL still gets a
	// (rate-limit-free) attempt rather than silently dropping the fetch.
	if parsed, err := url.Parse(rawURL); err != nil {
		warnLogf("rate limiter: could not parse URL %q for host extraction (%v); "+
			"proceeding without a rate-limit wait", rawURL, err)
	} else if waitErr := c.rateLimiter.WaitHTTP(c.ctx, parsed.Host); waitErr != nil {
		// The wait did not yield a token. If the parent context is done the wait
		// was cancelled (parent cancelled or deadline exceeded): return the
		// context error WITHOUT issuing the HTTP request (R10.2). Prefer the raw
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
	// starts now, after the rate-limit token has been acquired.
	ctx, cancel := context.WithTimeout(c.ctx, c.opTimeout)
	defer cancel()

	resp, err := c.httpClient.GetWithContext(ctx, rawURL)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP request returned status %d", resp.StatusCode)
	}

	content, err := io.ReadAll(resp.Body)
	if err != nil {
		// Translate an http.MaxBytesReader overflow into ErrResponseTooLarge
		// (R11.3); GetWithContext caps the body at httputil.MaxBodyBytes.
		return nil, fmt.Errorf("failed to read response body: %w", classifyBodyReadError(err))
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
	var (
		sem      = make(chan struct{}, c.concurrency)
		wg       sync.WaitGroup
		mu       sync.Mutex
		results  = make([]CheckResult, 0, len(c.config.Packages))
		failures = make(map[string]error)
		progress atomic.Uint64
		total    = uint64(len(c.config.Packages))
	)

	for name, pkg := range c.config.Packages {
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
			if err != nil {
				failures[n] = err
			} else {
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
