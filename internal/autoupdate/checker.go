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
	// llmClient handles LLM-based version extraction (optional)
	llmClient *LLMClient
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

// WithLLMClient sets a custom LLM client for the checker
func WithLLMClient(llm *LLMClient) CheckerOption {
	return func(c *Checker) error {
		c.llmClient = llm
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

	// R4.2: a non-empty llm_prompt has no effect on --check today (the LLM
	// is only invoked when llmClient is non-nil, which the CLI never wires).
	// Emit a Warn for each affected package so users discover the gap before
	// debugging a silent no-op. Sorted iteration keeps the diagnostic order
	// deterministic. De-duplication is per-Checker (the lifetime of one
	// `bentoo overlay autoupdate --check` run), not process-wide.
	if checker.llmClient == nil && checker.config != nil {
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
	// Try primary URL
	version, err := c.fetchAndParse(cfg.URL, cfg.Parser, cfg.Path, cfg.Pattern, cfg.Selector, cfg.XPath)
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

		version, err = c.fetchAndParse(cfg.FallbackURL, cfg.FallbackParser, cfg.Path, fallbackPattern, cfg.Selector, cfg.XPath)
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

// fetchAndParse fetches content from a URL and parses it to extract version.
// It builds the parser via NewParserFromConfig so every configured parser type
// is supported — including "html", whose selector/xpath fields NewParser cannot
// express. Passing selector/xpath through is what lets a check scrape a version
// out of an HTML page (e.g. an XPath onto an href attribute).
func (c *Checker) fetchAndParse(url, parserType, path, pattern, selector, xpath string) (string, error) {
	// Fetch content
	content, err := c.fetchContent(url)
	if err != nil {
		return "", err
	}

	// Create parser. NewParserFromConfig handles json/regex/html uniformly and,
	// for html, wires selector/xpath plus the optional regex post-processing
	// (carried in pattern).
	parser, err := NewParserFromConfig(&PackageConfig{
		Parser:   parserType,
		Path:     path,
		Pattern:  pattern,
		Selector: selector,
		XPath:    xpath,
	})
	if err != nil {
		return "", fmt.Errorf("failed to create parser: %w", err)
	}

	// Parse content
	version, err := parser.Parse(content)
	if err != nil {
		return "", fmt.Errorf("failed to parse version: %w", err)
	}

	return version, nil
}

// fetchContent fetches content from a URL using the HTTP client with retry logic.
// The request is bounded by a child of the Checker's parent context (set via
// WithContext) with the configured per-operation timeout, so a cancelled parent
// context or an expired deadline aborts the in-flight HTTP call.
//
// Before issuing the request, fetchContent gates on the per-host rate limiter
// (R10.1): the host is parsed from the URL and c.rateLimiter.WaitHTTP blocks
// until a token is available. If the wait is cancelled by the context the
// context error is returned and no HTTP request is made (R10.2). A URL that
// fails to parse fails open (R10.1): a Warn line is logged and the fetch
// proceeds without a rate-limit wait rather than aborting.
func (c *Checker) fetchContent(rawURL string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(c.ctx, c.opTimeout)
	defer cancel()

	// Parse the host for per-host rate limiting. Fail open on a parse error:
	// an unparseable URL still gets a (rate-limit-free) attempt rather than
	// silently dropping the fetch.
	if parsed, err := url.Parse(rawURL); err != nil {
		warnLogf("rate limiter: could not parse URL %q for host extraction (%v); "+
			"proceeding without a rate-limit wait", rawURL, err)
	} else if waitErr := c.rateLimiter.WaitHTTP(ctx, parsed.Host); waitErr != nil {
		// The wait did not yield a token. If the context is done the wait was
		// cancelled (parent cancelled or deadline exceeded): return the
		// context error WITHOUT issuing the HTTP request (R10.2). Prefer the
		// raw context error so callers' errors.Is(err, context.Canceled /
		// .DeadlineExceeded) checks hold regardless of how the limiter wraps it.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("rate limiter wait cancelled: %w", ctxErr)
		}
		// A non-context wait failure (e.g. the request can never satisfy the
		// limiter's burst): surface it rather than issuing a doomed request.
		return nil, fmt.Errorf("rate limiter wait failed: %w", waitErr)
	}

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
