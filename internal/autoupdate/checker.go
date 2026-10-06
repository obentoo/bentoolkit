// Package autoupdate provides version checking functionality for ebuild autoupdate.
package autoupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
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

	"github.com/obentoo/bentoolkit/internal/autoupdate/ebuilds"
	"github.com/obentoo/bentoolkit/internal/autoupdate/fetch"
	"github.com/obentoo/bentoolkit/internal/autoupdate/llm"
	"github.com/obentoo/bentoolkit/internal/autoupdate/parse"
	"github.com/obentoo/bentoolkit/internal/autoupdate/registry"
	appconfig "github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/ebuild"
	"github.com/obentoo/bentoolkit/internal/common/github"
	"github.com/obentoo/bentoolkit/internal/common/logging"
	"github.com/obentoo/bentoolkit/internal/common/provider"
	"github.com/sony/gobreaker"
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
	// ErrFetchFailed is returned when fetching upstream version fails
	ErrFetchFailed = errors.New("failed to fetch upstream version")
	// ErrUpstreamUnreachable is wrapped beside ErrFetchFailed when the fetch
	// failed in transport — retries exhausted, a timeout, an open circuit
	// breaker — rather than on what the record asked for. Nothing in the record
	// is wrong, so the interactive registry repair must not offer to rewrite it.
	ErrUpstreamUnreachable = errors.New("upstream unreachable")
	// ErrRequirementUnresolved is returned (wrapped) when a record's `requires`
	// entry could not be captured for the detected version. The bump is held,
	// like an unresolved aux value: a pending entry with a partial requirement
	// set could not be told apart from a complete one.
	ErrRequirementUnresolved = errors.New("requirement unresolved")
	// ErrBaseVersionUnresolved is returned when an entry declares where its base
	// version lives (base_from, or commit_version_pattern) and that source yields
	// nothing. It is deliberately fatal for the check: falling back to the
	// ebuild's own base is what let six Khronos packages drift up to seven
	// releases behind while still reporting "up to date".
	ErrBaseVersionUnresolved = errors.New("declared base version source resolved nothing")
	// ErrAuxUnresolved is recorded (wrapped, on CheckResult.Error) when a package
	// declares aux_pattern or commit_sha_path and that value could not be
	// resolved. The bump is held rather than queued: applying it would ship the
	// previous release's value under the new version.
	ErrAuxUnresolved = errors.New("auxiliary value unresolved")
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
	// Skipped names the packages.toml key ("hold = true" or "enabled = false")
	// that kept CheckPackage from checking the package at all. When set, nothing
	// was fetched, nothing was written to the cache or pending list, and every
	// other field except Package is zero-valued.
	Skipped string
	// Requirements is the state of each `requires` entry captured for this
	// update, in atom order; nil for a record without requires or a bump that
	// was held.
	Requirements []RequirementState
}

// RequirementState is whether the version a bump requires is available yet.
type RequirementState struct {
	// Package is the required "category/package".
	Package string
	// Version is the version captured for it.
	Version string
	// State is "present" (the overlay or ::gentoo satisfies the pin), "pending"
	// (a pending entry of that package will) or "missing" (neither).
	State string
}

// The three values RequirementState.State takes.
const (
	RequirementPresent = "present"
	RequirementPending = "pending"
	RequirementMissing = "missing"
)

// DefaultOpTimeout is the default per-operation timeout applied to a single
// outbound HTTP fetch when no explicit timeout is configured on the Checker.
const DefaultOpTimeout = 30 * time.Second

// deriveOpTimeout sizes the per-operation budget so that every retry attempt can
// run within it: perReq×(MaxRetries+1) for the attempts, plus the cumulative
// exponential backoff between them, plus one second of slack. The slack ensures
// the per-request timeout (not the operation budget) is what fires on a slow
// host, so the failure surfaces as the clearer "max retries exceeded" rather than
// a premature "context deadline exceeded". rc carries the retry parameters from
// the HTTP client; a zero/blank rc still yields a budget >= perReq. The backoff
// is summed from backoffCeilingFor, the same ceiling the retry wait draws its
// jitter below, so a jittered wait never exceeds what the budget allows for.
func deriveOpTimeout(perReq time.Duration, rc fetch.RetryConfig) time.Duration {
	maxRetries := rc.MaxRetries
	if maxRetries < 0 {
		maxRetries = 0
	}
	attempts := maxRetries + 1
	total := perReq * time.Duration(attempts)

	// Sum the longest wait the retry loop can take before each retry.
	for i := 1; i <= maxRetries; i++ {
		total += fetch.BackoffCeilingFor(rc, i)
	}

	return total + time.Second
}

// operationTimeout resolves the per-operation budget for a package: the
// per-package override (cfg.Timeout seconds) when set, otherwise the Checker's
// global budget (c.opTimeout, derived from the configured per-request timeout).
func (c *Checker) operationTimeout(cfg *registry.PackageConfig) time.Duration {
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
	config *registry.PackagesConfig
	// typeFilter, when non-empty ("bin" or "source"), restricts CheckAll to
	// packages of that resolved type. Empty checks every package. Set via
	// WithTypeFilter.
	typeFilter string
	// cache manages version query caching
	cache *fetch.Cache
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
	llmClient llm.LLMProvider
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
	httpClient *fetch.RetryableHTTPClient
	// configDir is the directory for storing cache and pending files
	configDir string
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
	// bodies deduplicates upstream response bodies within this Checker's
	// lifetime — one command invocation (S024-R2.4). fetchContent consults it
	// BEFORE the rate-limiter wait, so a read answered from an already-fetched
	// body costs neither a request nor a token (S024-R2.1, S024-R2.2).
	//
	// It is distinct from `cache` above: that one is the on-disk, TTL'd record of
	// resolved VERSIONS across runs; this one is in-memory, has no TTL, holds raw
	// response BYTES, and is discarded when the run ends.
	//
	// NIL IS THE OFF SWITCH, and it is a supported state rather than a defect:
	// fetchContent then calls fetchContentUncached directly, which is the
	// pre-story path byte for byte. NewChecker builds one in the struct literal
	// below, so deduplication is on by default.
	bodies *fetch.BodyCache

	// hostSlots caps the requests in flight to one host (DefaultPerHostConcurrency
	// unless WithPerHostConcurrency says otherwise). Nil means no cap.
	hostSlots *hostSlots

	// gentooPath is the ::gentoo tree a requirement may be satisfied by. Empty
	// means the overlay alone is consulted.
	gentooPath string

	// log receives the checker's diagnostics. Set via WithLogger; NewChecker
	// leaves it discarding when the option is absent. Read it through logger().
	log *slog.Logger
}

// logger returns the checker's logger, or a discarding one for a checker that
// was not built by NewChecker.
func (c *Checker) logger() *slog.Logger {
	return logging.OrDiscard(c.log)
}

// CheckerOption is a functional option for configuring Checker
type CheckerOption func(*Checker) error

// WithLogger sets the logger the checker reports its diagnostics to, and hands
// it to the HTTP client the checker uses. Nil keeps the default, which discards
// them (and leaves an injected client's own logger alone).
func WithLogger(l *slog.Logger) CheckerOption {
	return func(c *Checker) error {
		c.log = l
		return nil
	}
}

// WithCache sets a custom cache for the checker
func WithCache(cache *fetch.Cache) CheckerOption {
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
func WithLLMClient(llm llm.LLMProvider) CheckerOption {
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
func WithHTTPClient(client *fetch.RetryableHTTPClient) CheckerOption {
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
func WithPackagesConfig(config *registry.PackagesConfig) CheckerOption {
	return func(c *Checker) error {
		c.config = config
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

// WithGentooPath sets the ::gentoo tree consulted, after the overlay, when the
// check report decides whether a required version is present.
func WithGentooPath(path string) CheckerOption {
	return func(c *Checker) error {
		c.gentooPath = path
		return nil
	}
}

// WithPerHostConcurrency caps the requests in flight to one host at n; n < 1
// removes the cap.
func WithPerHostConcurrency(n int) CheckerOption {
	return func(c *Checker) error {
		c.hostSlots = newHostSlots(n)
		return nil
	}
}

// WithFetchCache turns per-run deduplication of upstream response bodies on or
// off. It is ON by default (S024-R7.2) — NewChecker builds a cache in its struct
// literal — so the only reason to pass this option at all is to turn it OFF.
//
// DISABLED IS THE ABSENCE OF A CACHE, not a flag consulted somewhere on the
// fetch path: false sets the field to nil, and fetchContent then calls
// fetchContentUncached directly. That is what makes the off switch worth
// trusting as a bisection tool — the disabled path is not a second
// implementation to audit, it is the code that shipped before this story, byte
// for byte, with no join, no sharing and no retention.
//
// true INSTALLS a cache only when none is present rather than replacing one that
// is. The option is therefore idempotent: applying it after the default
// construction — which is every real call — keeps the single cache already
// there, along with the bodies and counters it has accumulated. Rebuilding
// unconditionally would silently discard a run's shared bodies, so that every
// read taken before the option was applied would be paid for a second time.
//
// It cannot fail: a bool has no invalid value. The error in the return type is
// the CheckerOption signature, not a possibility.
func WithFetchCache(enabled bool) CheckerOption {
	return func(c *Checker) error {
		if !enabled {
			c.bodies = nil
			return nil
		}
		if c.bodies == nil {
			c.bodies = fetch.NewDefaultBodyCache()
		}
		return nil
	}
}

// NewChecker creates a new checker instance for the given overlay.
// It loads the packages configuration and initializes cache and pending list.
func NewChecker(overlayPath string, opts ...CheckerOption) (*Checker, error) {
	// Determine config directory
	configDir, err := appconfig.AutoupdateDir()
	if err != nil {
		return nil, fmt.Errorf("resolving the autoupdate state directory: %w", err)
	}

	checker := &Checker{
		overlayPath: overlayPath,
		configDir:   configDir,
		opTimeout:   DefaultOpTimeout,
		concurrency: DefaultConcurrency,
		// Per-run body deduplication is ON by default (S024-R2.1). It is built
		// HERE, in the literal, rather than after the options loop, so that
		// "disabled" can be expressed as the ABSENCE of a cache — an option that
		// simply sets the field back to nil — instead of a flag threaded through
		// the fetch path. fetchContent then has one branch on one field, and the
		// off state is the pre-story code verbatim.
		bodies:    fetch.NewDefaultBodyCache(),
		hostSlots: newHostSlots(DefaultPerHostConcurrency),
	}

	// Apply options first to allow overriding configDir
	for _, opt := range opts {
		if err := opt(checker); err != nil {
			return nil, fmt.Errorf("failed to apply checker option: %w", err)
		}
	}

	// Load packages configuration if not provided
	if checker.config == nil {
		config, err := registry.LoadPackagesConfig(overlayPath)
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
		cacheOpts := []fetch.CacheOption{}
		if checker.cacheTTL > 0 {
			cacheOpts = append(cacheOpts, fetch.WithTTL(checker.cacheTTL))
		}
		cache, err := fetch.NewCache(checker.configDir, cacheOpts...)
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
		checker.httpClient = fetch.NewRetryableHTTPClient()
	}
	// The client reports through the checker's logger — an injected client
	// too, since there is one logger per invocation. Without WithLogger the
	// client keeps its own logger, and a client the checker built discards.
	if checker.log != nil {
		checker.httpClient.SetLogger(checker.log)
	}
	checker.log = logging.OrDiscard(checker.log)

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
			checker.log.Warn("resolving GitHub token: failed; continuing with unauthenticated GitHub API access", "err", err)
		}
		if token != "" {
			checker.httpClient.SetGitHubToken(token)
		}
	}

	// Initialize the HTTP rate limiter if not injected. A Checker must never
	// have a nil rateLimiter: fetchContent unconditionally waits on it (S001-R10.3).
	if checker.rateLimiter == nil {
		checker.rateLimiter = fetch.NewRateLimiter()
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
			checker.log.Warn("package sets llm_prompt but no LLM is wired into "+
				"the check path; this field is consumed only by "+
				"'bentoo overlay analyze' (see docs/autoupdate.md)", "package", name)
		}
	}

	return checker, nil
}

// fetchFailure wraps a failed upstream fetch as ErrFetchFailed and, when the
// failure was the transport's, as ErrUpstreamUnreachable too.
func fetchFailure(err error) error {
	if isUpstreamUnreachable(err) {
		return fmt.Errorf("%w: %w: %w", ErrFetchFailed, ErrUpstreamUnreachable, err)
	}
	return fmt.Errorf("%w: %w", ErrFetchFailed, err)
}

// isUpstreamUnreachable reports whether err is a transport failure: the
// retrying client gave up (ErrMaxRetriesExceeded covers refused and reset
// connections, TLS EOF and retried 429/5xx statuses), a request or operation
// timed out, a Retry-After was too long, or the host's circuit breaker is open.
//
// A host the resolver says does not exist is NOT one: that is almost always a
// mistyped url, which is exactly what the registry repair is for. A cancelled
// context is not one either — the operator stopped the run.
func isUpstreamUnreachable(err error) bool {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	return errors.Is(err, fetch.ErrMaxRetriesExceeded) ||
		errors.Is(err, fetch.ErrRequestTimeout) ||
		errors.Is(err, fetch.ErrRetryAfterTooLong) ||
		errors.Is(err, gobreaker.ErrOpenState) ||
		errors.Is(err, gobreaker.ErrTooManyRequests) ||
		errors.Is(err, context.DeadlineExceeded)
}

// CheckPackage checks a single package for updates.
// If force is true, the cache is bypassed and upstream is queried directly.
// ctx bounds every upstream fetch, rate-limit wait and LLM call this check
// makes; the Checker holds no context of its own, so one Checker can serve
// calls with different lifetimes.
func (c *Checker) CheckPackage(ctx context.Context, pkg string, force bool) (*CheckResult, error) {
	result := &CheckResult{
		Package: pkg,
	}

	// Get package configuration
	pkgConfig, exists := c.config.Packages[pkg]
	if !exists {
		result.Error = fmt.Errorf("%w: %s", ebuilds.ErrPackageNotFound, pkg)
		return result, result.Error
	}

	// An explicit `--check <pkg>` honours the same two keys CheckAll filters on.
	// It used to check a held or disabled package like any other and queue its
	// update in pending.json, so the maintainer's "do not auto-bump" held only
	// for the full scan.
	if reason := registry.RefusedBy(c.config.Packages, pkg); reason != "" {
		result.Skipped = reason
		return result, nil
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
		info, err := c.fetchCommitInfo(ctx, &pkgConfig)
		if err != nil {
			// An unresolved base is a configuration fault, not a transport one;
			// wrapping it as ErrFetchFailed would hide that from callers that
			// branch on the sentinel (and from anyone reading the message).
			if errors.Is(err, ErrBaseVersionUnresolved) {
				result.Error = err
			} else {
				result.Error = fetchFailure(err)
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
			if cur := currentEbuildCommit(c.logger(), c.overlayPath, pkg, c.seriesFor(pkg)); cur != "" &&
				strings.EqualFold(cur, info.SHA) {
				result.HasUpdate = false
			}
		}

		if result.HasUpdate {
			if err := c.addToPending(pkg, currentVersion, newVersion, info.SHA, "", nil); err != nil {
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
				sha := c.resolveAuxSHA(ctx, &pkgConfig, result)
				aux := c.resolveAuxValue(ctx, &pkgConfig, result)
				reqs, reqErr := c.resolveRequirements(ctx, pkg, &pkgConfig, result)
				c.settleRequirements(pkg, &pkgConfig, reqs, result)
				if held := heldBump(pkg, &pkgConfig, sha, aux); held != nil {
					result.Error = errors.Join(result.Error, held)
				} else if reqErr != nil {
					result.Error = errors.Join(result.Error, reqErr)
				} else if err := c.addToPending(pkg, currentVersion, cachedVersion, sha, aux, reqs); err != nil {
					// Log but don't fail the check
					result.Error = fmt.Errorf("failed to add to pending: %w", err)
				}
			}

			return result, nil
		}
	}

	// Fetch upstream version
	upstreamVersion, err := c.fetchUpstreamVersion(ctx, pkg, &pkgConfig)
	if err != nil {
		result.Error = fetchFailure(err)
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
		sha := c.resolveAuxSHA(ctx, &pkgConfig, result)
		aux := c.resolveAuxValue(ctx, &pkgConfig, result)
		reqs, reqErr := c.resolveRequirements(ctx, pkg, &pkgConfig, result)
		c.settleRequirements(pkg, &pkgConfig, reqs, result)
		if held := heldBump(pkg, &pkgConfig, sha, aux); held != nil {
			// Joined, not overwritten: result.Error may already hold the cache
			// write error, in which case the helper did not record its own cause.
			result.Error = errors.Join(result.Error, held)
		} else if reqErr != nil {
			result.Error = errors.Join(result.Error, reqErr)
		} else if err := c.addToPending(pkg, currentVersion, upstreamVersion, sha, aux, reqs); err != nil {
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
	best, err := ebuilds.SelectCurrentEbuild(c.logger(), c.overlayPath, pkg, c.seriesFor(pkg))
	if err != nil {
		return "", err
	}
	return best.Version, nil
}

// DisableOrphans marks each package as disabled (enabled = false) and, where the
// record states no origin yet, records the checker as the origin of that disable
// (disabled_by = "auto") — an entry that already names a human origin keeps it,
// or this function would erase the intent it exists to respect — both in the
// overlay's packages.toml and in the in-memory config, so a package whose ebuild
// was removed from the overlay stops being processed on subsequent runs — and so
// the reconciliation in CheckAll can tell this disable from one a human wrote
// and is free to clear it when the ebuild comes back. The file edit is a single
// atomic, comment-preserving write for the whole batch. A nil or empty slice is
// a no-op. The in-memory config is only updated after the file write succeeds,
// so a failed write leaves both views consistent.
//
// Both keys are mirrored in memory, not just enabled: within a single run the
// reconciliation READS the in-memory origin, so writing the pair to the file and
// only half of it to memory would make the checker's own disable look like a
// legacy human one on the very next scan.
func (c *Checker) DisableOrphans(pkgs []string) error {
	if len(pkgs) == 0 {
		return nil
	}
	if err := registry.DisablePackagesInConfig(c.overlayPath, pkgs); err != nil {
		return err
	}
	disabled := false
	for _, pkg := range pkgs {
		if cfg, ok := c.config.Packages[pkg]; ok {
			cfg.Enabled = &disabled
			// Matching the writer: DisablePackagesInConfig stamps the automatic
			// origin only where the record states none, so an entry that already
			// names a human origin keeps it and stays out of the reconciliation.
			if cfg.DisabledBy == "" {
				cfg.DisabledBy = registry.DisabledByAuto
			}
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
// edit is a comment-preserving DELETION of both keys of the disable — the
// `enabled = false` assignment and the `disabled_by` origin beside it: enabled
// is the default, spelled by the key's absence, so writing `enabled = true`
// would state nothing new and would leave a redundant-enabled finding for
// --lint --fix to undo (EnablePackagesInConfig likewise inserts nothing for a
// section that lacks the key), and an origin left behind on an enabled record
// would claim a state the record is no longer in. A nil or empty slice is a
// no-op. The in-memory config is updated only after the file write succeeds, so
// a failed write leaves both views consistent — including the origin, which the
// reconciliation READS on the next entry of the same run, so a stale in-memory
// copy would be misread as a human's decision.
//
// Callers must exclude held packages (hold = true): a hold is an explicit
// maintainer decision that the overlay reconciliation must never flip. Callers
// must likewise pass only packages reconcilesAutomatically accepts; this
// function performs the revive it is asked for and does not second-guess it.
func (c *Checker) ReviveDisabled(pkgs []string) error {
	if len(pkgs) == 0 {
		return nil
	}
	if err := registry.EnablePackagesInConfig(c.overlayPath, pkgs); err != nil {
		return err
	}
	enabled := true
	for _, pkg := range pkgs {
		if cfg, ok := c.config.Packages[pkg]; ok {
			cfg.Enabled = &enabled
			cfg.DisabledBy = "" // the origin of a disable that no longer exists is nothing
			c.config.Packages[pkg] = cfg
		}
	}
	return nil
}

// reconcilesAutomatically reports whether the overlay reconciliation may clear
// this entry's disable. It is true for exactly one shape: disabled, not held,
// and stamped with the origin the checker writes for its own bookkeeping
// (disabled_by = "auto").
//
// The direction is the safety property. An origin this predicate does not
// recognise — including the ABSENT origin every record predating the field
// carries — reads as a human decision and survives untouched, so the fail-safe
// holds with no migration behind it. The opposite reading is the defect: a scan
// cleared a deliberate pin and bumped the package, and www-client/orion-bin's
// slot dependency stayed broken for ten days (story 043, R1.2/R1.3).
//
// Hold is asked here rather than left to the caller so one predicate states the
// whole rule, and a held entry answers false even when it carries the automatic
// origin: hold is never auto-flipped, whatever the origin says.
//
// It deliberately does NOT ask whether the ebuild is back in the overlay. That
// question needs the overlay itself, and leaving it to CheckAll — where
// getCurrentVersion is reachable — keeps this a pure function of the record, so
// the policy can be read and tested without a fixture on disk.
func reconcilesAutomatically(pkg registry.PackageConfig) bool {
	return !pkg.IsEnabled() && !pkg.IsHeld() && pkg.DisabledBy == registry.DisabledByAuto
}

// frozenDisableMessage is the notice logFrozenDisables writes. The entries it
// is about travel as attributes.
//
// It names the missing KEY as well as the entries, because "left disabled" on
// its own reads as a fault in the package: the entry is repairable, and
// stamping it with the automatic origin is the repair. Without that sentence
// the fail-safe would introduce a silent regression of its own — every record
// disabled before the field existed quietly stops reconciling.
const frozenDisableMessage = "left package(s) disabled — the reconciliation clears only a disable it recorded itself, " +
	"and these state no origin, so each reads as a deliberate decision; add disabled_by = \"" + registry.DisabledByAuto +
	"\" to any entry the checker should be free to re-enable when its ebuild returns"

// logFrozenDisables writes the single line naming every entry the
// reconciliation left disabled although its ebuild is present, because the
// record states no origin (R1.4). An empty set writes nothing, so a run with
// nothing to report says nothing instead of printing an empty list.
func logFrozenDisables(log *slog.Logger, pkgs []string) {
	if len(pkgs) == 0 {
		return
	}
	logging.OrDiscard(log).Info(frozenDisableMessage, "count", len(pkgs), "packages", strings.Join(pkgs, ", "))
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
//
// "Present" has a third shape, and it is skipped just as silently: an entry
// whose ":slot" or `series` filter matches NOTHING (ErrSlotNotFound /
// ErrSeriesNotFound). The package directory is there, holding the ebuilds of
// another line, so the entry is not an orphan — and it is not a fault either,
// because a disabled line-filtered entry matching nothing is its expected state
// (a release line upstream has not opened yet, or one whose package upstream
// dropped). A genuine config mistake is caught where it acts: on the enabled
// path, where CheckAll surfaces the same sentinel and Reconcile records it as
// NoEbuild.
//
// Every network call is best-effort:
// a package whose upstream fetch fails, or that ::gentoo does not carry at all
// (provider.ErrNotFound), is silently skipped rather than aborting the whole
// scan. Other provider errors are surfaced as soft notes in the returned error
// without dropping the candidates gathered so far. The result is sorted by
// package name for deterministic output.
func (c *Checker) FindRevivableOrphans(ctx context.Context, prov provider.Provider) ([]ReviveCandidate, error) {
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
		category, pkgName, ok := ebuilds.SplitPkgAtom(pkg)
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
		//
		// The lookup has THREE outcomes, not two. Besides "present" and
		// ErrNoEbuildFound, a ":slot" or `series` filter that matches nothing
		// yields ErrSlotNotFound/ErrSeriesNotFound: the package DIRECTORY is
		// there, carrying the ebuilds of another line. Such an entry is neither
		// revivable — seeding a ::gentoo base beside a sibling line that is
		// already newer is the very mistake the "present" skip prevents — nor a
		// failure worth a soft note, so it is skipped in silence like the other
		// two. See the doc comment above for where a real config error is caught
		// instead. The skip must stay HERE, ahead of fetchUpstreamVersion: that
		// function applies the same `series` to the upstream value and fails with
		// ErrNoVersionFound, so letting the flow continue would only retitle the
		// note from "overlay lookup failed" to "upstream fetch failed" — and cost
		// one request per scan to say it.
		if _, err := c.getCurrentVersion(pkg); err == nil {
			continue // ebuild still present: disabled but not orphaned, skip silently
		} else if errors.Is(err, ebuilds.ErrSlotNotFound) || errors.Is(err, ebuilds.ErrSeriesNotFound) {
			continue // the entry's filter selects no line: not an orphan, not a failure
		} else if !errors.Is(err, ebuilds.ErrNoEbuildFound) {
			notes = append(notes, fmt.Sprintf("%s: overlay lookup failed: %v", pkg, err))
			continue
		}

		// Best-effort upstream fetch; a failure just drops this package from the
		// report (it remains disabled, exactly as before).
		upstream, err := c.fetchUpstreamVersion(ctx, pkg, &cfg)
		if err != nil {
			notes = append(notes, fmt.Sprintf("%s: upstream fetch failed: %v", pkg, err))
			continue
		}

		// Highest version ::gentoo currently carries. A package ::gentoo does not
		// have is simply not revivable from a gentoo base, so skip it silently.
		versions, err := prov.GetPackageVersions(ctx, category, pkgName)
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
	best, err := ebuilds.SelectCurrentEbuild(c.logger(), c.overlayPath, pkg, c.seriesFor(pkg))
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
func (c *Checker) resolveType(pkg string, cfg *registry.PackageConfig) string {
	if cfg.Type != "" {
		return cfg.Type
	}
	path, err := c.currentEbuildPath(pkg)
	if err != nil {
		return "source"
	}
	content, err := os.ReadFile(path) //nolint:gosec // G304: path is the current ebuild selectCurrentEbuild listed in the package directory of a key splitPkgAtom confines
	if err != nil {
		return "source"
	}
	if ebuilds.DetectBinaryPackage(content) {
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
// result as a warning rather than "up to date" (ebuild.CompareVersions would
// otherwise order junk below every real version and silently report no update —
// see ebuild.IsValidVersion).
func (c *Checker) compareVersions(upstream, current string) (hasUpdate, comparable bool) {
	u := parse.StripVersionPrefix(strings.TrimSpace(upstream))
	cur := parse.StripVersionPrefix(strings.TrimSpace(current))
	if !ebuild.IsValidVersion(u) || !ebuild.IsValidVersion(cur) {
		return false, false
	}
	return ebuild.CompareVersions(u, cur) > 0, true
}

// addToPending adds an update to the pending list.
// commitHash is non-empty only for track="commit" packages or version-tracked
// packages with commit_sha_path; auxValue is non-empty only for packages with
// aux_var/aux_pattern. Both are stored in PendingUpdate so the applier can
// substitute the corresponding variable in the copied ebuild. requires carries
// the versions captured for the record's `requires` entries (nil without any).
func (c *Checker) addToPending(pkg, currentVersion, newVersion, commitHash, auxValue string, requires map[string]string) error {
	update := PendingUpdate{
		Package:        pkg,
		CurrentVersion: currentVersion,
		NewVersion:     newVersion,
		CommitHash:     commitHash,
		AuxValue:       auxValue,
		Requires:       requires,
		Status:         StatusPending,
		DetectedAt:     time.Now(),
	}
	return c.pending.Add(update)
}

// heldBump returns an error wrapping ErrAuxUnresolved when pkg declares
// commit_sha_path or aux_pattern and the matching value came back empty, and
// nil otherwise. CheckPackage then leaves the pending list alone: a held bump
// is not a failed check, and an entry queued by an earlier successful check
// stays as it was. The value is fetched again on the next check, cache hit
// included, so the bump resolves itself once upstream serves it.
func heldBump(pkg string, cfg *registry.PackageConfig, sha, aux string) error {
	if cfg.CommitSHAPath != "" && sha == "" {
		return fmt.Errorf("%w for %s: commit_sha_path %q resolved nothing, bump held until it resolves", ErrAuxUnresolved, pkg, cfg.CommitSHAPath)
	}
	if cfg.AuxPattern != "" && aux == "" {
		return fmt.Errorf("%w for %s: aux_pattern resolved nothing, bump held until it resolves", ErrAuxUnresolved, pkg)
	}
	return nil
}

// resolveAuxSHA fetches the auxiliary commit SHA for a version-tracked package
// that declares commit_sha_path (e.g. cursor's BUILD_ID, which is part of the
// download URL and changes with every release). It returns "" when no SHA path
// is configured. A fetch/parse failure is recorded on result.Error and returns
// "", which makes CheckPackage hold the bump (see heldBump) instead of queueing
// it with the previous release's SHA.
//
// Commit-tracked packages (track="commit") resolve their SHA via fetchCommitInfo
// instead and never reach this path.
func (c *Checker) resolveAuxSHA(ctx context.Context, cfg *registry.PackageConfig, result *CheckResult) string {
	if cfg.CommitSHAPath == "" {
		return ""
	}
	content, err := c.fetchContent(ctx, cfg.URL, cfg.Headers, packageCredentialScope(cfg), c.operationTimeout(cfg))
	if err != nil {
		if result.Error == nil {
			result.Error = fmt.Errorf("failed to fetch commit sha: %w", err)
		}
		return ""
	}
	sha, err := (&parse.JSONParser{Path: cfg.CommitSHAPath}).Parse(content)
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
// fetch/parse failure is recorded on result.Error and returns "", which makes
// CheckPackage hold the bump (see heldBump) instead of queueing it with the
// previous release's value.
func (c *Checker) resolveAuxValue(ctx context.Context, cfg *registry.PackageConfig, result *CheckResult) string {
	if cfg.AuxPattern == "" {
		return ""
	}
	source, headers := cfg.URL, cfg.Headers
	if cfg.AuxURL != "" {
		source = strings.ReplaceAll(cfg.AuxURL, fetch.VersionPlaceholder, url.PathEscape(result.UpstreamVersion))
		headers = nonCredentialHeaders(cfg.Headers)
	}
	content, err := c.fetchContent(ctx, source, headers, packageCredentialScope(cfg), c.operationTimeout(cfg))
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
			result.Error = fmt.Errorf("aux_pattern %q matched no capture group in %s", cfg.AuxPattern, source)
		}
		return ""
	}
	return strings.TrimSpace(string(m[1]))
}

// resolveRequirements captures the version of every package record pkg
// requires, from the release its own version came from. It returns nil, nil for
// a record without `requires`.
//
// Each entry's pattern is applied to the record's own url — a body-cache hit,
// since the version was parsed from it this run — or to the entry's url, with
// "{version}" replaced by the detected version and no credential header sent.
// The record's own url is read even when `mirrors` served the version, so a
// primary that is down holds the bump rather than reading another page. Any
// entry that cannot be captured fails the whole set: the caller then holds the
// bump instead of queueing a partial requirement set.
func (c *Checker) resolveRequirements(ctx context.Context, pkg string, cfg *registry.PackageConfig, result *CheckResult) (map[string]string, error) {
	if len(cfg.Requires) == 0 {
		return nil, nil
	}
	atoms := make([]string, 0, len(cfg.Requires))
	for atom := range cfg.Requires {
		atoms = append(atoms, atom)
	}
	sort.Strings(atoms)

	captured := make(map[string]string, len(atoms))
	for _, atom := range atoms {
		spec := cfg.Requires[atom]
		source, headers := cfg.URL, cfg.Headers
		if spec.URL != "" {
			source = strings.ReplaceAll(spec.URL, fetch.VersionPlaceholder, url.PathEscape(result.UpstreamVersion))
			headers = nonCredentialHeaders(cfg.Headers)
		}
		content, err := c.fetchContent(ctx, source, headers, packageCredentialScope(cfg), c.operationTimeout(cfg))
		if err != nil {
			return nil, fmt.Errorf("%w for %s requiring %s: fetching %s: %w", ErrRequirementUnresolved, pkg, atom, hostForError(source), err)
		}
		pattern := strings.ReplaceAll(spec.Pattern, fetch.VersionPlaceholder, regexp.QuoteMeta(result.UpstreamVersion))
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("%w for %s requiring %s: pattern %q: %w", ErrRequirementUnresolved, pkg, atom, spec.Pattern, err)
		}
		m := re.FindSubmatch(content)
		if len(m) < 2 {
			return nil, fmt.Errorf("%w for %s requiring %s: pattern %q matched nothing in %s for version %s",
				ErrRequirementUnresolved, pkg, atom, spec.Pattern, hostForError(source), result.UpstreamVersion)
		}
		version := strings.TrimSpace(string(m[1]))
		if !ebuild.IsValidVersion(version) {
			return nil, fmt.Errorf("%w for %s requiring %s: captured %q is not a Gentoo version", ErrRequirementUnresolved, pkg, atom, version)
		}
		if spec.Pin == "~" && ebuilds.RevisionSuffixRegex.MatchString(version) {
			return nil, fmt.Errorf("%w for %s requiring %s: captured %q carries a revision, which a ~ pin cannot match", ErrRequirementUnresolved, pkg, atom, version)
		}
		captured[atom] = version
	}
	return captured, nil
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
func currentEbuildCommit(log *slog.Logger, overlayPath, pkg, series string) string {
	best, err := ebuilds.SelectCurrentEbuild(log, overlayPath, pkg, series)
	if err != nil || best.Path == "" {
		return ""
	}
	content, err := os.ReadFile(best.Path)
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
func (c *Checker) resolveBaseFromTag(ctx context.Context, cfg *registry.PackageConfig, headSHA string) (string, bool, error) {
	content, err := c.fetchContent(ctx, cfg.BaseURL, cfg.Headers, packageCredentialScope(cfg), c.operationTimeout(cfg))
	if err != nil {
		return "", false, fmt.Errorf("base version tags %s: %w", cfg.BaseURL, err)
	}

	re, err := regexp.Compile(cfg.BaseTagPattern)
	if err != nil {
		return "", false, fmt.Errorf("invalid base_tag_pattern %q: %w", cfg.BaseTagPattern, err)
	}

	names, shas, err := parseTagListing(content)
	if err != nil {
		return "", false, fmt.Errorf("%w: %w (%s)", ErrBaseVersionUnresolved, err, cfg.BaseURL)
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
func (c *Checker) fetchCommitInfo(ctx context.Context, cfg *registry.PackageConfig) (*commitInfo, error) {
	content, err := c.fetchContent(ctx, cfg.URL, cfg.Headers, packageCredentialScope(cfg), c.operationTimeout(cfg))
	if err != nil {
		return nil, err
	}

	// Extract date of the latest commit (path points to [0].commit.committer.date
	// or [0].committed_date, etc.) then apply transforms to get YYYYMMDD.
	dateParser := &parse.JSONParser{Path: cfg.Path}
	raw, err := dateParser.Parse(content)
	if err != nil {
		return nil, fmt.Errorf("commit date: %w", err)
	}
	date := parse.ApplyTransforms(c.logger(), raw, cfg.Transform)
	if date == "" {
		return nil, fmt.Errorf("commit date: empty after transform (raw: %q)", raw)
	}

	// Extract SHA of the latest commit.
	shaParser := &parse.JSONParser{Path: cfg.CommitSHAPath}
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
	case cfg.BaseFrom == "none":
		// Declared as having no base source: NewBase stays empty and the base
		// carried by the current ebuild is kept, which is the correct outcome
		// rather than a fallback. Handled before the legacy branch below so the
		// declaration wins outright — validation already rejects the pattern
		// fields that branch keys on, and a case this explicit cannot be reached
		// by accident when they are added back.

	case cfg.BaseFrom == "file":
		base, err := c.resolveBaseFromFile(ctx, cfg)
		if err != nil {
			return nil, err
		}
		info.NewBase = base

	case cfg.BaseFrom == "tag":
		base, exact, err := c.resolveBaseFromTag(ctx, cfg, sha)
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
func (c *Checker) resolveBaseFromFile(ctx context.Context, cfg *registry.PackageConfig) (string, error) {
	content, err := c.fetchContent(ctx, cfg.BaseURL, cfg.Headers, packageCredentialScope(cfg), c.operationTimeout(cfg))
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
		val, err := parse.NavigateJSONPath(elem, messageRelPath)
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
func (c *Checker) fetchUpstreamVersion(ctx context.Context, pkg string, cfg *registry.PackageConfig) (string, error) {
	version, err := c.fetchUpstreamVersionRaw(ctx, pkg, cfg)
	if err != nil {
		return "", err
	}

	// The tag prefix ("v3.2.3", "release-1.2") is stripped here, on the same
	// single convergence point, and not left to each consumer. compareVersions
	// and the applier already strip defensively, but the RAW value used to flow
	// into CheckResult, the pending list and the validation plan — where
	// ClassifyForDepth cannot read "v3.2.3" as a Gentoo version and charges the
	// bump the deepest class, pricing a patch bump as major in the plan the
	// operator confirms. Stripping before applySuffix keeps the suffix logic
	// working on a bare version, and the strip is idempotent for entries whose
	// transform already removed the prefix.
	version = parse.NormalizeUpstreamVersion(version)
	version = parse.ApplySuffix(c.logger(), version, cfg)

	// An entry restricted to a release line must never report a version from
	// another one: it would be compared against — and could bump — the ebuild of
	// a line it does not track. The select path already drops such candidates
	// per candidate; this covers the paths that yield a single value (first
	// match, script, fallback, LLM). Failing loudly is the point: the entry's
	// source moved, or its series is wrong, and both need a human.
	if m := ebuilds.NewSeriesMatcher(c.logger(), cfg.Series); m.Active() && !m.Matches(parse.StripVersionPrefix(version)) {
		return "", fmt.Errorf("%w: upstream version %q is outside this entry's series %q",
			parse.ErrNoVersionFound, version, cfg.Series)
	}
	return version, nil
}

// fetchUpstreamVersionRaw fetches and parses the upstream version for a package.
// It tries the primary URL/parser first, then fallback if configured, then LLM if available.
func (c *Checker) fetchUpstreamVersionRaw(ctx context.Context, pkg string, cfg *registry.PackageConfig) (string, error) {
	// The script parser drives a headless browser itself, so it bypasses
	// fetchContent/fetchAndParse entirely (and therefore transform/select, which
	// the script handles in JS — see ValidatePackageConfig). It has no fallback
	// or LLM stage: the script is the single source of truth.
	if cfg.Parser == "script" {
		return c.probeWithMirrors(cfg, func(m *registry.PackageConfig) (string, error) {
			return c.parseLive(ctx, m)
		})
	}

	// Try primary URL, then its mirrors
	version, err := c.probeWithMirrors(cfg, func(m *registry.PackageConfig) (string, error) {
		return c.fetchAndParse(ctx, m.URL, m)
	})
	if err == nil {
		return version, nil
	}
	primaryErr := err

	// A credential refusal is a verdict on the record, not a failed source:
	// neither the fallback nor the LLM stage may run after it (S052-R2.2).
	if errors.Is(err, fetch.ErrCredentialHostMismatch) {
		return "", fmt.Errorf("all version extraction methods failed: %w", err)
	}

	// Try fallback URL if configured
	if cfg.FallbackURL != "" && cfg.FallbackParser != "" {
		fallbackPattern := cfg.FallbackPattern
		if fallbackPattern == "" && cfg.FallbackParser == "json" {
			fallbackPattern = cfg.Path // Use primary path for JSON fallback
		}

		version, err = c.fetchAndParse(ctx, cfg.FallbackURL, fallbackConfig(cfg, fallbackPattern))
		if err == nil {
			return version, nil
		}
	}

	// Try LLM if configured and available
	if c.llmClient != nil && cfg.LLMPrompt != "" {
		// Fetch content from primary URL for LLM
		content, err := c.fetchContent(ctx, cfg.URL, cfg.Headers, packageCredentialScope(cfg), c.operationTimeout(cfg))
		if err == nil {
			version, err = c.llmClient.ExtractVersion(ctx, content, cfg.LLMPrompt)
			if err == nil {
				return version, nil
			}
		}
	}

	// All methods failed
	return "", fmt.Errorf("all version extraction methods failed: %w", primaryErr)
}

// nonCredentialHeaders returns headers without the credential-bearing ones
// (isAllowedHeaderName), literal value or ${VAR} alike, for a request to a host
// outside the record's credential scope: a mirror or the fallback_url.
func nonCredentialHeaders(headers map[string]string) map[string]string {
	var out map[string]string
	for name, value := range headers {
		if fetch.IsAllowedHeaderName(name) || fetch.ContainsCRLF(name) {
			continue
		}
		if out == nil {
			out = make(map[string]string, len(headers))
		}
		out[name] = value
	}
	return out
}

// probeWithMirrors runs probe against cfg and then, while it keeps failing,
// against each of cfg.Mirrors in order, returning the first version found.
//
// A mirror is the same record with url swapped and its credential headers
// dropped. A credential refusal on url stops the chain: it is a verdict on the
// record, not a failed source (S052-R2.2). When every source failed, the error
// is a transport failure only if every attempt was one — a mirror timing out
// must not hide that url itself answered with something the record cannot
// read, which is the record's fault and the registry repair's business.
func (c *Checker) probeWithMirrors(cfg *registry.PackageConfig, probe func(*registry.PackageConfig) (string, error)) (string, error) {
	version, err := probe(cfg)
	if err == nil || len(cfg.Mirrors) == 0 || errors.Is(err, fetch.ErrCredentialHostMismatch) {
		return version, err
	}

	errs := []error{err}
	for _, mirror := range cfg.Mirrors {
		mc := *cfg
		mc.URL = mirror
		mc.Headers = nonCredentialHeaders(cfg.Headers)
		mc.Mirrors = nil
		v, merr := probe(&mc)
		if merr == nil {
			c.logger().Warn("upstream unavailable; version read from a mirror",
				"host", hostForError(cfg.URL), "err", err, "mirror", hostForError(mirror))
			return v, nil
		}
		errs = append(errs, fmt.Errorf("mirror %s: %w", hostForError(mirror), merr))
	}

	for i, e := range errs {
		if isUpstreamUnreachable(e) {
			continue
		}
		var others []string
		for j, o := range errs {
			if j != i {
				others = append(others, o.Error())
			}
		}
		return "", fmt.Errorf("%w (also failed: %s)", e, strings.Join(others, "; "))
	}
	return "", errors.Join(errs...)
}

// fallbackConfig derives the config fetchAndParse runs the fallback URL with.
// It swaps in the fallback parser and pattern and keeps everything else that
// shapes the fetch or the answer, so the fallback is the same probe pointed at
// another source:
//   - path/selector/xpath, transform and select, as before;
//   - timeout, so a record that raised its budget for a slow host keeps it;
//   - series and suffix/suffix_when, so select = max filters an out-of-line
//     candidate per candidate instead of returning it and failing the whole
//     check at fetchUpstreamVersion's series guard;
//   - headers that cannot carry a credential, so a custom User-Agent or
//     Accept reaches the fallback. Every credential-bearing header
//     (isAllowedHeaderName) is dropped, literal value or ${VAR} alike: the
//     fallback host is deliberately outside the record's credential scope (see
//     packageCredentialScope).
func fallbackConfig(cfg *registry.PackageConfig, pattern string) *registry.PackageConfig {
	return &registry.PackageConfig{
		Parser:     cfg.FallbackParser,
		Path:       cfg.Path,
		Pattern:    pattern,
		Selector:   cfg.Selector,
		XPath:      cfg.XPath,
		Transform:  cfg.Transform,
		Select:     cfg.Select,
		Headers:    nonCredentialHeaders(cfg.Headers),
		Timeout:    cfg.Timeout,
		Series:     cfg.Series,
		Suffix:     cfg.Suffix,
		SuffixWhen: cfg.SuffixWhen,
	}
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
func (c *Checker) fetchAndParse(ctx context.Context, rawURL string, cfg *registry.PackageConfig) (string, error) {
	// Fetch content
	content, err := c.fetchContent(ctx, rawURL, cfg.Headers, packageCredentialScope(cfg), c.operationTimeout(cfg))
	if err != nil {
		return "", err
	}

	// select path: collect all candidates, transform each, then pick one.
	if cfg.Select != "" && cfg.Select != "first" {
		extractor, exErr := parse.NewSelectExtractor(cfg)
		if exErr != nil {
			return "", fmt.Errorf("failed to create select extractor: %w", exErr)
		}
		if extractor != nil {
			cands, cErr := extractor.ExtractVersions(content)
			if cErr != nil {
				return "", fmt.Errorf("failed to extract version candidates: %w", cErr)
			}
			best := parse.SelectVersion(c.logger(), cands, cfg)
			if best == "" {
				return "", fmt.Errorf("%w: no comparable version among %d candidate(s) for select=%q",
					parse.ErrNoVersionFound, len(cands), cfg.Select)
			}
			return best, nil
		}
		// Not list-capable (e.g. parser="script"): warn and use first match.
		c.logger().Warn("select requested but the parser cannot extract a list; using first match",
			"select", cfg.Select, "parser", cfg.Parser)
	}

	// Create parser. NewParserFromConfig handles json/regex/html uniformly.
	parser, err := parse.NewParserFromConfig(cfg)
	if err != nil {
		return "", fmt.Errorf("failed to create parser: %w", err)
	}

	// Parse content, then apply transform to the single extracted version.
	version, err := parser.Parse(content)
	if err != nil {
		return "", fmt.Errorf("failed to parse version: %w", err)
	}
	version = parse.ApplyTransforms(c.logger(), version, cfg.Transform)

	return version, nil
}

// parseLive runs a parser="script" check. It resolves the script body (inline,
// or "@file.js" loaded from <overlay>/.autoupdate/scripts/), gates on the
// per-host rate limiter exactly like fetchContent, then evaluates the script
// against the rendered page under a child context bounded by opTimeout.
//
// The chromedp headless-browser backend is opt-in: in a binary built without
// the `chromedp` tag, newLiveEvaluator returns ErrScriptSupportNotBuilt and this
// surfaces as the package's check error.
//
// A fresh evaluator is created per call and closed afterward (if it implements
// io.Closer); reusing one browser across the batch is a future optimization, but
// the script-package count is tiny (the LibreOffice group), so launch cost is
// acceptable and per-call isolation avoids shared-state concurrency hazards.
func (c *Checker) parseLive(ctx context.Context, cfg *registry.PackageConfig) (string, error) {
	scriptsDir := filepath.Join(c.overlayPath, ".autoupdate", "scripts")
	body, err := resolveScript(cfg.Script, scriptsDir)
	if err != nil {
		return "", err
	}

	if c.bodies == nil {
		return c.evaluateLive(ctx, cfg, body)
	}
	// Two records running the same script on the same page — libreoffice and
	// libreoffice-l10n track one release listing — get one browser and one
	// navigation, through the run's body cache that HTTP reads already share.
	// The key cannot collide with an HTTP body's: it carries a prefix no URL has.
	// The lookup precedes the rate-limit wait for the reason fetchContent's does.
	digest := sha256.Sum256([]byte(body))
	key := "script" + fetch.KeySeparator + hex.EncodeToString(digest[:]) + fetch.KeySeparator + fetch.BodyKey(cfg.URL, cfg.Headers)
	out, err := c.bodies.Do(ctx, key, func() ([]byte, error) {
		v, err := c.evaluateLive(ctx, cfg, body)
		return []byte(v), err
	})
	return string(out), err
}

// evaluateLive renders cfg.URL in a headless browser and evaluates body there,
// after the host's rate-limit token and connection slot.
func (c *Checker) evaluateLive(ctx context.Context, cfg *registry.PackageConfig, body string) (string, error) {
	// Gate on the per-host rate limiter (same policy as fetchContent), waiting on
	// the parent context so the wait is signal-cancellable and not charged to the
	// per-operation timeout. Fail open on an unparseable URL.
	if parsed, perr := url.Parse(cfg.URL); perr != nil {
		c.logger().Warn("rate limiter: could not parse URL for host extraction; proceeding without a rate-limit wait",
			"url", cfg.URL, "err", perr)
	} else if werr := c.rateLimiter.WaitHTTP(ctx, parsed.Host); werr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", fmt.Errorf("rate limiter wait cancelled: %w", ctxErr)
		}
		return "", fmt.Errorf("rate limiter wait failed: %w", werr)
	}
	release, err := c.hostSlots.acquire(ctx, cfg.URL)
	if err != nil {
		return "", fmt.Errorf("waiting for a connection slot to %s: %w", hostForError(cfg.URL), err)
	}
	defer release()

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
	opCtx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()

	parser := &ScriptParser{URL: cfg.URL, Script: body, Headers: cfg.Headers, eval: eval}
	return parser.ParseLive(opCtx)
}

// fetchContent is the single door every upstream read in this package goes
// through, and therefore the one place per-run deduplication can be installed
// without touching a caller: base_url, fallback_url and the intra-package
// auxiliary reads all gain it by already being here (S024-R2.5).
//
// It answers a read from a body this run already fetched when one exists, and
// otherwise delegates to fetchContentUncached — the pre-story path, unchanged.
//
// THE RETURNED BODY IS SHARED, NOT COPIED, AND MUST BE TREATED AS READ-ONLY
// (story 024 assumption A1). Every record that asked for the same fetch identity
// receives the SAME backing array, so a caller that mutates it corrupts what
// every other record sees — and does so silently: nothing fails loudly, one
// package simply starts reporting another package's version. Every consumer
// downstream of this function reads without mutating today (parseJSON,
// parseRegex, goquery, htmlquery); a future one that needs to write must copy
// first. Copying per caller here would reinstate exactly the memory cost the
// cache exists to remove, so the contract is the mechanism rather than an
// optimisation layered on top of one.
//
// THE LOOKUP PRECEDES THE RATE-LIMITER WAIT, and that ordering is the entire
// point rather than an implementation detail: the wait lives inside
// fetchContentUncached, so a read served from a retained body acquires no token
// (S024-R2.2). At one token per 6 s per host, a hit that still queued for a
// token would save the request and none of the wall-clock time this exists for.
// UB1 is preserved by the same arrangement: every request that IS issued still
// goes through the limiter, at the same interval and burst, because the only way
// to reach the request is through fetchContentUncached.
//
// EACH CALLER'S CLOSURE CAPTURES ITS OWN opTimeout. That is what makes S024-R3.4
// fall out of the structure instead of needing machinery: a caller released by a
// leader whose fetch FAILED invokes its own closure, with its own full
// per-operation budget, and so cannot inherit an already-expired deadline it did
// not set. Nothing here extends, shares or reuses a context.
//
// A nil bodies cache means deduplication is off, and that path is the pre-story
// behaviour byte for byte — no join, no sharing, no retention.
//
// The error is returned exactly as the underlying fetch produced it: bodyCache.do
// neither caches nor shares a failure, so callers' errors.Is checks against
// context.Canceled, context.DeadlineExceeded and ErrResponseTooLarge keep holding.
//
// scope is the package's credential scope (packageCredentialScope). The binding
// check runs FIRST, before the cache is consulted (S052-R2.3): the key is built
// from the URL and the unexpanded declared headers, not from the scope, so a
// record bound elsewhere shares its key with a record that legitimately fetched
// the same URL, and would otherwise be served that record's body. A refusal is
// returned before the join, so the sentence above stays true — no refusal is
// cached or shared — and the key needs no scope term (S052-R9.4).
//
// ctx is the CALLER'S context, and a done one ends the read before the cache is
// consulted (story 059, R3.1). Without that check a retained body would answer a
// cancelled call: one Checker serves many calls, so a live call that filled the
// cache would let a later cancelled call succeed. The refusal keeps the raw
// context error as its cause, so errors.Is(err, context.Canceled) and
// errors.Is(err, context.DeadlineExceeded) hold for every caller.
func (c *Checker) fetchContent(ctx context.Context, rawURL string, headers map[string]string, scope fetch.CredentialScope, opTimeout time.Duration) ([]byte, error) {
	if err := fetch.CheckCredentialBinding(rawURL, headers, scope); err != nil {
		return nil, err
	}

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("fetch from %s cancelled before it started: %w", hostForError(rawURL), err)
	}

	if c.bodies == nil {
		return c.fetchContentUncached(ctx, rawURL, headers, scope, opTimeout)
	}

	return c.bodies.Do(ctx, fetch.BodyKey(rawURL, headers), func() ([]byte, error) {
		return c.fetchContentUncached(ctx, rawURL, headers, scope, opTimeout)
	})
}

// fetchContentUncached fetches content from a URL using the HTTP client with retry logic.
//
// It is the fetch itself, with no deduplication of any kind: every call issues a
// request. It is reached only through fetchContent, which decides whether a
// request is needed at all; calling it directly would bypass the join and is
// what the arrangement above exists to make unnecessary.
//
// It first gates on the per-host rate limiter (S001-R10.1), waiting on the caller's
// parent context (the ctx parameter) so the wait is signal-cancellable but
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
// URLs, the configured GitHub token. Passing them through the header-applying
// GET (rather than the bare GetWithContext) is what actually puts the User-Agent,
// the Authorization token, and any TOML-declared headers on the wire. scope is
// handed to that GET so the client re-checks the credential binding against the
// package's hosts, not the request's own (S052-R1.4).
func (c *Checker) fetchContentUncached(ctx context.Context, rawURL string, headers map[string]string, scope fetch.CredentialScope, opTimeout time.Duration) ([]byte, error) {
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
		c.logger().Warn("rate limiter: could not parse URL for host extraction; proceeding without a rate-limit wait",
			"url", rawURL, "err", err)
	} else if waitErr := c.rateLimiter.WaitHTTP(ctx, parsed.Host); waitErr != nil {
		// The wait did not yield a token. If the parent context is done the wait
		// was cancelled (parent cancelled or deadline exceeded): return the
		// context error WITHOUT issuing the HTTP request (S001-R10.2). Prefer the raw
		// context error so callers' errors.Is(err, context.Canceled /
		// .DeadlineExceeded) checks hold regardless of how the limiter wraps it.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("rate limiter wait cancelled: %w", ctxErr)
		}
		// A non-context wait failure (e.g. the request can never satisfy the
		// limiter's burst): surface it rather than issuing a doomed request.
		return nil, fmt.Errorf("rate limiter wait failed: %w", waitErr)
	}

	// Then a slot among the host's in-flight requests, on the parent context for
	// the same reason as the token: time queued behind a slow host's other
	// requests is not this request's round-trip. Held until the body is read.
	release, err := c.hostSlots.acquire(ctx, rawURL)
	if err != nil {
		return nil, fmt.Errorf("waiting for a connection slot to %s: %w", hostForError(rawURL), err)
	}
	defer release()

	// The per-operation timeout bounds only the HTTP round-trip; its deadline
	// starts now, after the rate-limit token has been acquired. opTimeout is the
	// per-package or global budget the caller resolved via operationTimeout.
	opCtx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()

	resp, err := c.httpClient.GetWithHeadersScopedContext(opCtx, rawURL, headers, scope)
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
	content, err := fetch.ReadBodyForStatus(resp, acceptedStatuses...)
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
// context-cancellable select: if the caller's context (the ctx parameter) is
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
func (c *Checker) CheckAll(ctx context.Context, force bool) BatchResult[CheckResult] {
	// Reconcile status with the overlay BEFORE filtering, for the entries the
	// checker disabled ITSELF: for those, the overlay — not packages.toml — is
	// the source of truth for whether the package exists, so once the ebuild is
	// re-added the bookkeeping is cleared here and the filter below picks the
	// entry up in this same run.
	//
	// `enabled = false` is NOT, on its own, bookkeeping the overlay may override.
	// It is a two-valued key carrying three states, and reading every false as
	// stale is what let a scan clear a pin a human had written and bump the
	// package: dev-libs/icu-compat and media-libs/libjxl-compat lost their
	// disable that way and broke www-client/orion-bin's slot dependency for ten
	// days. What separates the two is the RECORDED ORIGIN, not the key — a
	// disable a human wrote means what hold means, leave it alone — so
	// reconcilesAutomatically clears only a disable stamped as the checker's own
	// (R1.2, R1.3). Hold keeps its own meaning unchanged: never auto-flipped,
	// origin or no origin.
	//
	// Absent means deliberate, which is the fail-safe direction but also freezes
	// every entry disabled before the origin field existed. Those are collected
	// and named once below (R1.4) rather than left silent, since a legacy entry
	// that ought to reconcile is a repair someone must make by hand.
	var revived, frozen []string
	for name, pkg := range c.config.Packages {
		if pkg.IsEnabled() || pkg.IsHeld() {
			continue
		}
		if _, err := c.getCurrentVersion(name); err != nil {
			continue // still absent from the overlay: a true orphan, disabled for the reason it states
		}
		switch {
		case reconcilesAutomatically(pkg):
			revived = append(revived, name) // ebuild present again → reconcile to enabled
		case pkg.DisabledBy == "":
			// Ebuild present, disable unexplained: left alone, and reported.
			// An entry naming a non-automatic origin is left alone SILENTLY —
			// its record already says who decided, so there is nothing to repair.
			frozen = append(frozen, name)
		}
	}
	if len(revived) > 0 {
		sort.Strings(revived)
		if err := c.ReviveDisabled(revived); err != nil {
			c.logger().Warn("failed to re-enable package(s) whose ebuild reappeared in the overlay",
				"count", len(revived), "err", err)
		} else {
			for _, p := range revived {
				c.logger().Info("re-enabled package: its ebuild is present in the overlay again", "package", p)
			}
		}
	}
	// One line for the whole run, deliberately unlike the per-package revive
	// lines above: a revive is an action taken on one entry, while this is a
	// standing condition of the registry that a maintainer fixes in one pass —
	// and on a registry with ~90 such entries, one line per entry would bury the
	// scan's actual output.
	if len(frozen) > 0 {
		sort.Strings(frozen)
		logFrozenDisables(c.logger(), frozen)
	}

	// Narrow the package set up front so excluded packages incur no network
	// fetch and are absent from progress and totals. Three filters apply:
	//   - enabled = false: always skipped, silently (no log, no count);
	//   - hold = true: maintainer-held, skipped silently like a disabled entry;
	//   - type filter (when active): keep only the matching bin/source class.
	pkgs := make(map[string]registry.PackageConfig, len(c.config.Packages))
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
		if err := ctx.Err(); err != nil {
			mu.Lock()
			failures[name] = err
			mu.Unlock()
			continue
		}
		// Cancellable semaphore acquisition: also record a context failure if
		// the parent context is cancelled while waiting for a free slot.
		select {
		case <-ctx.Done():
			mu.Lock()
			failures[name] = ctx.Err()
			mu.Unlock()
			continue
		case sem <- struct{}{}:
		}

		wg.Add(1)
		go func(n string, p registry.PackageConfig) {
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

			result, err := c.CheckPackage(ctx, n, force)

			mu.Lock()
			switch {
			case err != nil && errors.Is(err, ebuilds.ErrNoEbuildFound):
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
			c.logger().Warn("failed to auto-disable orphaned package(s) in packages.toml", "count", len(orphaned), "err", err)
		}
	}

	// A requirement found missing by one worker may be pending by now: the
	// required package can have been checked, and queued, after the package
	// that requires it. Settle again against the final pending list so the
	// report never depends on check order.
	c.resettleMissing(results)

	// Deterministic final ordering, independent of completion order.
	sort.Slice(results, func(i, j int) bool {
		return results[i].Package < results[j].Package
	})

	// One line per run, at DEBUG, reporting what the body deduplication did
	// (S024-R6.1). It is emitted after wg.Wait above, so every worker has joined
	// and the figures are final rather than a mid-flight sample.
	//
	// CheckAll has a SINGLE exit — the return below — so a plain call here emits
	// exactly once per run. If a second return is ever added, this must become a
	// defer at the top of the function; otherwise that new path would silently
	// report nothing.
	c.logFetchCacheStats()

	return BatchResult[CheckResult]{Items: results, Failures: failures}
}

// logFetchCacheStats emits this run's body-deduplication counters ONCE, at
// DEBUG, and at no other level (S024-R6.1, S024-R6.2, S024-R6.3).
//
// DEBUG IS THE CEILING, not a default that could be nudged up later. A miss is
// the normal state of a cold cache and a refused retention is a memory bound
// doing its job — neither is a defect, so neither is a warning. The failure this
// rule exists to prevent is concrete: 411 records at one line per read would
// push the warnings that do need a human clean off the screen. It is the same
// reason the figures are summarised once per run instead of per read, and the
// reason bodyCache itself logs nothing whatsoever.
//
// All six counters go on one line, in the order a reader asks about them: what
// the cache saved (hits, joins), what it still cost (misses, refetches), and
// what it declined to keep. The two refusals are spelled out per limit rather
// than summed, because "not retained" without the limit that refused it does not
// tell an operator which number to raise (S024-R6.2) — and the names say
// not_retained precisely because a refusal never affects the body its caller
// received, only whether the next caller must fetch it again.
//
// A nil cache emits NOTHING — not a line of zeros. Deduplication is off, so
// there are no figures to report, and six zeros would read like a cache that ran
// and achieved nothing rather than one that was never there.
func (c *Checker) logFetchCacheStats() {
	if c.bodies == nil {
		return
	}

	stats := c.bodies.Snapshot()
	c.logger().Debug("fetch cache",
		"hits", stats.Hits, "joins", stats.Joins, "misses", stats.Misses, "refetches", stats.Refetches,
		"not_retained_oversize", stats.Oversize, "not_retained_budget_full", stats.BudgetFull)
}

// Config returns the packages configuration.
func (c *Checker) Config() *registry.PackagesConfig {
	return c.config
}

// Cache returns the cache instance.
func (c *Checker) Cache() *fetch.Cache {
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

// packageCredentialScope is the scope of a package record: the hostnames of
// its url and base_url (S052-R1.4). An empty or unparseable field contributes
// no host, so a record whose url cannot be read has no own host and any
// BENTOO_* reference in it is refused. fallback_url is deliberately not a
// scope host: a BENTOO_* credential stays with the record's primary source.
func packageCredentialScope(cfg *registry.PackageConfig) fetch.CredentialScope {
	var scope fetch.CredentialScope
	if cfg == nil {
		return scope
	}
	for _, raw := range []string{cfg.URL, cfg.BaseURL} {
		if raw == "" {
			continue
		}
		if u, err := url.Parse(raw); err == nil && u.Hostname() != "" {
			scope.OwnHosts = append(scope.OwnHosts, u.Hostname())
		}
	}
	return scope
}
