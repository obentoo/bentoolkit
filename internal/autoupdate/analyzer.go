// Package autoupdate provides intelligent package analysis using LLM to generate update schemas.
package autoupdate

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/antchfx/xpath"

	"github.com/obentoo/bentoolkit/internal/autoupdate/ebuilds"
	"github.com/obentoo/bentoolkit/internal/autoupdate/fetch"
	"github.com/obentoo/bentoolkit/internal/autoupdate/llm"
	"github.com/obentoo/bentoolkit/internal/autoupdate/parse"
	"github.com/obentoo/bentoolkit/internal/autoupdate/registry"
	appconfig "github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/fileutil"
	"github.com/obentoo/bentoolkit/internal/common/logging"
)

// Error variables for analyzer errors
var (
	// ErrSchemaExists is returned when a schema already exists for a package
	ErrSchemaExists = errors.New("schema already exists for package")
	// ErrNoDataSources is returned when no data sources are found for a package
	ErrNoDataSources = errors.New("no data sources found for package")
	// ErrAnalysisFailed is returned when LLM analysis fails
	ErrAnalysisFailed = errors.New("LLM analysis failed")
	// ErrInvalidPattern is returned when an LLM-generated regex pattern is
	// invalid: it fails to compile, exceeds MaxPatternLen, or uses
	// backreferences (which RE2 does not support).
	ErrInvalidPattern = errors.New("invalid regex pattern")
)

// MaxPatternLen is the maximum allowed length, in characters, of an
// LLM-generated regex pattern. Patterns longer than this are rejected as a
// basic ReDoS prophylaxis.
const MaxPatternLen = 512

// backrefPattern matches a regex backreference (\1 .. \9). RE2 (Go's regexp
// engine) does not support backreferences, so a pattern containing one always
// fails to compile; this expression lets validatePattern emit an explicit,
// actionable diagnostic instead of an opaque compiler error.
var backrefPattern = regexp.MustCompile(`\\[1-9]`)

// validatePattern checks that an LLM-generated regex pattern is safe to persist
// and later compile. An empty pattern is valid (the parser simply does not use
// regex post-processing). A non-empty pattern is rejected, with a wrapped
// ErrInvalidPattern, when it:
//   - exceeds MaxPatternLen characters (basic ReDoS prophylaxis), or
//   - contains a backreference (\1 .. \9), which RE2 does not support, or
//   - fails to compile under Go's regexp engine.
//
// Note (AD-5): catastrophic-backtracking shapes such as "(a+)+$" are NOT
// rejected. Go's regexp is RE2, which executes every pattern in time linear in
// the input length, so such shapes are safe and remain valid.
func validatePattern(p string) error {
	if p == "" {
		return nil
	}
	if len(p) > MaxPatternLen {
		return fmt.Errorf("%w: pattern length %d exceeds maximum %d", ErrInvalidPattern, len(p), MaxPatternLen)
	}
	if backrefPattern.MatchString(p) {
		return fmt.Errorf("%w: backreferences not supported", ErrInvalidPattern)
	}
	if _, err := regexp.Compile(p); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidPattern, err)
	}
	return nil
}

// validateXPath checks that an LLM-generated XPath expression is safe to
// persist. An empty expression is valid (the parser uses a CSS selector or no
// XPath at all). A non-empty expression that fails to compile is rejected with
// a wrapped ErrInvalidXPath. Compilation uses xpath.Compile, the same engine
// htmlquery uses internally when HTMLParser evaluates an XPath query.
func validateXPath(x string) error {
	if x == "" {
		return nil
	}
	if _, err := xpath.Compile(x); err != nil {
		return fmt.Errorf("%w: %w", parse.ErrInvalidXPath, err)
	}
	return nil
}

// AnalyzeOptions configures the analysis behavior.
type AnalyzeOptions struct {
	// URL overrides the URL for analysis
	URL string
	// Hint provides user guidance to the LLM
	Hint string
	// NoCache bypasses all caches
	NoCache bool
	// Force overwrites existing schema
	Force bool
	// DryRun shows schema without saving
	DryRun bool
}

// AnalyzeResult represents the result of analyzing a package.
type AnalyzeResult struct {
	// Package is the full package name (category/package)
	Package string
	// SuggestedSchema is the schema suggested by analysis
	SuggestedSchema *registry.PackageConfig
	// Validated indicates if the schema was validated successfully
	Validated bool
	// ExtractedVersion is the version extracted using the schema
	ExtractedVersion string
	// EbuildVersion is the current version from the ebuild
	EbuildVersion string
	// Error contains any error that occurred during analysis
	Error error
	// DataSource is the data source used for analysis
	DataSource *DataSource
	// FromCache indicates if the result was from cache
	FromCache bool
}

// DefaultLLMTimeout is the default per-operation timeout applied to a single
// LLM analysis call when no explicit timeout is configured on the Analyzer.
const DefaultLLMTimeout = 60 * time.Second

// Analyzer handles package analysis and schema generation.
// It coordinates between ebuild metadata extraction, data source discovery,
// LLM analysis, and schema validation.
type Analyzer struct {
	// overlayPath is the path to the overlay directory
	overlayPath string
	// config holds the packages configuration
	config *registry.PackagesConfig
	// llmClient handles LLM-based analysis
	llmClient llm.LLMProvider
	// httpClient handles HTTP requests with retry logic
	httpClient *fetch.RetryableHTTPClient
	// cache manages LLM analysis caching
	cache *AnalysisCache
	// rateLimiter manages request rate limiting
	rateLimiter *fetch.RateLimiter
	// configDir is the directory for storing cache files
	configDir string
	// opTimeout bounds a single outbound HTTP operation. Defaults to
	// DefaultOpTimeout.
	opTimeout time.Duration
	// llmTimeout bounds a single LLM analysis operation. Defaults to
	// DefaultLLMTimeout.
	llmTimeout time.Duration
	// analyzeFn is the per-package analysis AnalyzeAll runs in each worker.
	// NewAnalyzer binds it to Analyze; it is a seam for in-package tests only,
	// which replace it to hold, count or panic inside a worker. No option sets
	// it. The ctx it receives is the AnalyzeAll call's own.
	analyzeFn func(ctx context.Context, pkg string, opts AnalyzeOptions) (*AnalyzeResult, error)
	// log receives the analyzer's diagnostics. Set via WithAnalyzerLogger;
	// NewAnalyzer leaves it discarding when the option is absent.
	log *slog.Logger
}

// logger returns the analyzer's logger, or a discarding one for an analyzer
// that was not built by NewAnalyzer.
func (a *Analyzer) logger() *slog.Logger {
	return logging.OrDiscard(a.log)
}

// WithAnalyzerLogger sets the logger the analyzer reports its diagnostics to,
// and hands it to the analysis cache and HTTP client the analyzer uses. Nil
// keeps the default, which discards them (and leaves an injected cache's or
// client's own logger alone).
func WithAnalyzerLogger(l *slog.Logger) AnalyzerOption {
	return func(a *Analyzer) error {
		a.log = l
		return nil
	}
}

// AnalyzerOption is a functional option for configuring Analyzer.
type AnalyzerOption func(*Analyzer) error

// WithAnalyzerLLMClient sets a custom LLM client for the analyzer.
func WithAnalyzerLLMClient(llm llm.LLMProvider) AnalyzerOption {
	return func(a *Analyzer) error {
		a.llmClient = llm
		return nil
	}
}

// WithAnalyzerHTTPClient sets a custom HTTP client for the analyzer.
func WithAnalyzerHTTPClient(client *fetch.RetryableHTTPClient) AnalyzerOption {
	return func(a *Analyzer) error {
		a.httpClient = client
		return nil
	}
}

// WithAnalyzerCache sets a custom analysis cache for the analyzer.
func WithAnalyzerCache(cache *AnalysisCache) AnalyzerOption {
	return func(a *Analyzer) error {
		a.cache = cache
		return nil
	}
}

// WithAnalyzerRateLimiter sets a custom rate limiter for the analyzer.
func WithAnalyzerRateLimiter(limiter *fetch.RateLimiter) AnalyzerOption {
	return func(a *Analyzer) error {
		a.rateLimiter = limiter
		return nil
	}
}

// WithAnalyzerConfigDir sets the configuration directory for the analyzer.
func WithAnalyzerConfigDir(dir string) AnalyzerOption {
	return func(a *Analyzer) error {
		a.configDir = dir
		return nil
	}
}

// WithAnalyzerPackagesConfig sets a custom packages configuration.
func WithAnalyzerPackagesConfig(config *registry.PackagesConfig) AnalyzerOption {
	return func(a *Analyzer) error {
		a.config = config
		return nil
	}
}

// WithAnalyzerOpTimeout sets the per-operation timeout used to derive a child
// context for each outbound HTTP fetch. A non-positive duration is rejected.
func WithAnalyzerOpTimeout(d time.Duration) AnalyzerOption {
	return func(a *Analyzer) error {
		if d <= 0 {
			return fmt.Errorf("analyzer op timeout must be positive, got %v", d)
		}
		a.opTimeout = d
		return nil
	}
}

// WithAnalyzerLLMTimeout sets the per-operation timeout used to derive a child
// context for each LLM analysis call. A non-positive duration is rejected.
func WithAnalyzerLLMTimeout(d time.Duration) AnalyzerOption {
	return func(a *Analyzer) error {
		if d <= 0 {
			return fmt.Errorf("analyzer LLM timeout must be positive, got %v", d)
		}
		a.llmTimeout = d
		return nil
	}
}

// NewAnalyzer creates a new analyzer instance for the given overlay.
func NewAnalyzer(overlayPath string, opts ...AnalyzerOption) (*Analyzer, error) {
	// Determine config directory
	configDir, err := appconfig.AutoupdateDir()
	if err != nil {
		return nil, fmt.Errorf("resolving the autoupdate state directory: %w", err)
	}

	analyzer := &Analyzer{
		overlayPath: overlayPath,
		configDir:   configDir,
		opTimeout:   DefaultOpTimeout,
		llmTimeout:  DefaultLLMTimeout,
	}
	// Bound after the literal: the method value needs the pointer the literal
	// creates.
	analyzer.analyzeFn = analyzer.Analyze

	// Apply options first to allow overriding configDir
	for _, opt := range opts {
		if err := opt(analyzer); err != nil {
			return nil, fmt.Errorf("failed to apply analyzer option: %w", err)
		}
	}

	// Load packages configuration if not provided
	if analyzer.config == nil {
		config, err := registry.LoadPackagesConfig(overlayPath)
		if err != nil {
			// If config doesn't exist, create empty one
			if errors.Is(err, registry.ErrPackagesConfigNotFound) {
				analyzer.config = &registry.PackagesConfig{
					Packages: make(map[string]registry.PackageConfig),
				}
			} else {
				return nil, fmt.Errorf("failed to load packages config: %w", err)
			}
		} else {
			analyzer.config = config
		}
	}

	// Initialize analysis cache if not provided
	if analyzer.cache == nil {
		cache, err := NewAnalysisCache(analyzer.configDir)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize analysis cache: %w", err)
		}
		analyzer.cache = cache
	}

	// Initialize rate limiter if not provided
	if analyzer.rateLimiter == nil {
		analyzer.rateLimiter = fetch.NewRateLimiter()
	}

	// Initialize HTTP client if not provided
	if analyzer.httpClient == nil {
		analyzer.httpClient = fetch.NewRetryableHTTPClient()
	}

	// The cache and the client — injected ones too, since there is one logger
	// per invocation — report through the analyzer's logger. Without
	// WithAnalyzerLogger they keep their own, and what the analyzer built
	// discards.
	if analyzer.log != nil {
		analyzer.cache.log = analyzer.log
		analyzer.httpClient.SetLogger(analyzer.log)
	}
	analyzer.log = logging.OrDiscard(analyzer.log)

	return analyzer, nil
}

// Analyze analyzes a single package and suggests a schema. Every fetch and
// LLM call it makes is bounded by a child of ctx, so cancelling ctx (e.g. on
// SIGINT) aborts the analysis in flight.
func (a *Analyzer) Analyze(ctx context.Context, pkg string, opts AnalyzeOptions) (*AnalyzeResult, error) {
	result := &AnalyzeResult{
		Package: pkg,
	}

	// Check if schema already exists (unless force is set)
	if !opts.Force {
		if _, exists := a.config.Packages[pkg]; exists {
			result.Error = fmt.Errorf("%w: %s", ErrSchemaExists, pkg)
			return result, result.Error
		}
	}

	// Check analysis cache first (unless NoCache is set)
	if !opts.NoCache {
		if cachedSchema, ok := a.cache.GetWithBypass(pkg, opts.NoCache); ok {
			result.SuggestedSchema = cachedSchema
			result.FromCache = true
			// Still need to validate the cached schema
			return a.validateResult(ctx, result)
		}
	}

	// Extract ebuild metadata
	meta, err := ebuilds.ExtractEbuildMetadata(a.overlayPath, pkg)
	if err != nil {
		result.Error = fmt.Errorf("failed to extract ebuild metadata: %w", err)
		return result, result.Error
	}
	result.EbuildVersion = meta.Version

	// Discover data sources
	sources := DiscoverDataSources(meta, opts.URL)
	if len(sources) == 0 {
		result.Error = fmt.Errorf("%w: %s", ErrNoDataSources, pkg)
		return result, result.Error
	}

	// Try each data source until one succeeds
	var lastErr error
	for _, source := range sources {
		// Fetch content from data source
		content, err := a.fetchContent(ctx, source)
		if err != nil {
			lastErr = err
			continue
		}

		// Analyze content with LLM (if available)
		schema, err := a.analyzeContent(ctx, content, meta, opts.Hint, &source)
		if err != nil {
			lastErr = err
			continue
		}

		// Carry the ebuild-level binary detection already done by
		// ExtractEbuildMetadata into the suggested record, so a binary package
		// is suggested (and saved) as type = "bin". Only "bin" is
		// written: an absent type means "auto-detect from the ebuild", which is
		// exactly what the checker's resolveType does for a source package, so
		// pinning type = "source" would add a redundant claim the maintainer
		// then has to keep true.
		if meta.IsBinary {
			schema.Type = "bin"
		}

		result.SuggestedSchema = schema
		result.DataSource = &source

		// Cache the analysis result
		if !opts.NoCache && a.cache != nil {
			if cacheErr := a.cache.Set(pkg, schema, source.URL); cacheErr != nil {
				a.logger().Debug("cache write failed", "package", pkg, "err", cacheErr)
			}
		}

		// Validate the schema
		return a.validateResult(ctx, result)
	}

	// All sources failed
	if lastErr != nil {
		result.Error = fmt.Errorf("all data sources failed: %w", lastErr)
	} else {
		result.Error = fmt.Errorf("%w: %s", ErrNoDataSources, pkg)
	}
	return result, result.Error
}

// validateResult validates the suggested schema against the ebuild version.
func (a *Analyzer) validateResult(ctx context.Context, result *AnalyzeResult) (*AnalyzeResult, error) {
	if result.SuggestedSchema == nil {
		return result, result.Error
	}

	// Get ebuild version if not already set
	if result.EbuildVersion == "" {
		meta, err := ebuilds.ExtractEbuildMetadata(a.overlayPath, result.Package)
		if err != nil {
			result.Error = fmt.Errorf("failed to extract ebuild metadata for validation: %w", err)
			return result, result.Error
		}
		result.EbuildVersion = meta.Version
	}

	// Fetch content for validation
	content, err := a.fetchContentFromURL(ctx, result.SuggestedSchema.URL)
	if err != nil {
		result.Error = fmt.Errorf("failed to fetch content for validation: %w", err)
		return result, result.Error
	}

	// Validate schema
	validationResult := parse.ValidateSchema(content, result.SuggestedSchema, result.EbuildVersion)
	result.ExtractedVersion = validationResult.ExtractedVersion
	result.Validated = validationResult.Valid

	if !validationResult.Valid && validationResult.Error != nil {
		// Don't overwrite existing error
		if result.Error == nil {
			result.Error = validationResult.Error
		}
	}

	return result, nil
}

// fetchContent fetches content from a data source with rate limiting.
// The rate-limit wait is bounded by a child of the caller's ctx, so a
// cancelled ctx aborts the wait. RateLimiter.WaitHTTP reports a cancelled wait
// as ErrRateLimitExceeded alone, dropping the context's error; a wait that
// failed on a done context is therefore reported with that context's error as
// its cause, so errors.Is(err, context.Canceled) and
// errors.Is(err, context.DeadlineExceeded) hold for every caller.
func (a *Analyzer) fetchContent(ctx context.Context, source DataSource) ([]byte, error) {
	opCtx, cancel := context.WithTimeout(ctx, a.opTimeout)
	defer cancel()

	// Apply rate limiting
	if err := a.rateLimiter.WaitHTTPForURL(opCtx, source.URL); err != nil {
		if ctxErr := opCtx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("fetch from %s cancelled during the rate-limit wait: %w", source.URL, ctxErr)
		}
		return nil, fmt.Errorf("rate limit error for %s: %w", source.URL, err)
	}

	return a.fetchContentFromURL(ctx, source.URL)
}

// fetchContentFromURL fetches content from a URL. The request is bounded by a
// child of the caller's ctx with the configured per-operation timeout, so a
// cancelled ctx or an expired deadline aborts the in-flight HTTP call.
func (a *Analyzer) fetchContentFromURL(ctx context.Context, url string) ([]byte, error) {
	opCtx, cancel := context.WithTimeout(ctx, a.opTimeout)
	defer cancel()

	resp, err := a.httpClient.GetWithContext(opCtx, url)
	if err != nil {
		return nil, fmt.Errorf("HTTP request to %s failed: %w", url, err)
	}
	defer resp.Body.Close()

	// 200 alone is accepted here. The analyzer never declares a
	// Range, so unlike the checker it has no 206 to honour: a 206 at this call
	// site could only be unsolicited, and its body is a fragment the version
	// parser would read as a complete, successful answer.
	//
	// readBodyForStatus does the status check, the body read and the
	// translation of an http.MaxBytesReader overflow into ErrResponseTooLarge;
	// the cap itself is imposed upstream by GetWithContext at
	// httpx.MaxBodyBytes, not here. Its errors are already phrased for the
	// user, so they are returned as-is rather than re-wrapped.
	content, err := fetch.ReadBodyForStatus(resp, http.StatusOK)
	if err != nil {
		return nil, err
	}

	return content, nil
}

// analyzeContent analyzes content and generates a schema.
// The LLM call, and its rate-limit wait, are bounded by a child of the
// caller's ctx with the configured LLM timeout.
func (a *Analyzer) analyzeContent(ctx context.Context, content []byte, meta *ebuilds.EbuildMetadata, hint string, source *DataSource) (*registry.PackageConfig, error) {
	// If LLM client is available, use it for analysis
	if a.llmClient != nil {
		opCtx, cancel := context.WithTimeout(ctx, a.llmTimeout)
		defer cancel()

		// Apply LLM rate limiting
		if err := a.rateLimiter.WaitLLM(opCtx); err != nil {
			// WaitLLM drops the context's error, as WaitHTTP does (see
			// fetchContent); restore it as the cause.
			if ctxErr := opCtx.Err(); ctxErr != nil {
				return nil, fmt.Errorf("LLM analysis of %s cancelled during the rate-limit wait: %w", meta.Package, ctxErr)
			}
			return nil, fmt.Errorf("LLM rate limit error: %w", err)
		}

		analysis, err := a.llmClient.AnalyzeContent(opCtx, content, meta, hint)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrAnalysisFailed, err)
		}

		return a.schemaFromAnalysis(analysis, source)
	}

	// Fallback: generate schema based on content type
	return a.generateDefaultSchema(content, source)
}

// schemaFromAnalysis converts LLM analysis to PackageConfig.
func (a *Analyzer) schemaFromAnalysis(analysis *llm.SchemaAnalysis, source *DataSource) (*registry.PackageConfig, error) {
	schema := &registry.PackageConfig{
		URL:    source.URL,
		Parser: analysis.ParserType,
	}

	switch analysis.ParserType {
	case "json":
		schema.Path = analysis.Path
	case "regex":
		schema.Pattern = analysis.Pattern
	case "html":
		if analysis.Selector != "" {
			schema.Selector = analysis.Selector
		}
		if analysis.XPath != "" {
			schema.XPath = analysis.XPath
		}
		if analysis.Pattern != "" {
			schema.Pattern = analysis.Pattern
		}
	}

	// Validate LLM-generated pattern/XPath before the schema can be persisted
	// or cached. On failure the wrapped sentinel propagates to the caller,
	// which treats the analysis as failed and skips the cache write.
	if err := validatePattern(schema.Pattern); err != nil {
		return nil, err
	}
	if err := validateXPath(schema.XPath); err != nil {
		return nil, err
	}

	// Set fallback if provided by LLM analysis
	if analysis.FallbackType != "" {
		schema.FallbackParser = analysis.FallbackType
		schema.FallbackPattern = analysis.FallbackConfig
	}

	// Enhance schema with fallback if not already set
	// This ensures every schema has a fallback configured
	EnhanceSchemaWithFallback(schema)

	return schema, nil
}

// generateDefaultSchema generates a default schema based on content type.
func (a *Analyzer) generateDefaultSchema(content []byte, source *DataSource) (*registry.PackageConfig, error) {
	schema := &registry.PackageConfig{
		URL: source.URL,
	}

	// Determine parser based on content type
	switch source.ContentType {
	case ContentTypeJSON:
		schema.Parser = "json"
		// Try common JSON paths
		schema.Path = detectJSONPath(content)
		if schema.Path == "" {
			schema.Path = "version"
		}
	case ContentTypeHTML:
		schema.Parser = "html"
		// Default to a common version selector
		schema.Selector = ".version"
	default:
		// Default to regex
		schema.Parser = "regex"
		schema.Pattern = `(\d+\.\d+(?:\.\d+)?)`
	}

	// Enhance schema with fallback
	EnhanceSchemaWithFallback(schema)

	return schema, nil
}

// detectJSONPath attempts to detect the JSON path for version.
func detectJSONPath(content []byte) string {
	// Common paths to try
	commonPaths := []string{
		"version",
		"tag_name",
		"name",
		"[0].tag_name",
		"[0].name",
		"info.version",
		"dist-tags.latest",
		"crate.max_version",
	}

	for _, path := range commonPaths {
		parser := &parse.JSONParser{Path: path}
		if _, err := parser.Parse(content); err == nil {
			return path
		}
	}

	return ""
}

// AnalyzeAll analyzes all packages without schemas, at most 3 at a time.
//
// It returns a BatchResult: analyzed packages land in Items, a per-package
// failure is recorded in Failures keyed by package name and the batch goes on.
// A failure to enumerate the packages is a single synthetic Failures entry,
// which yields a total-failure exit code.
//
// The pool follows CheckAll's model:
//   - a slot is taken BEFORE a worker starts, so at most 3 analyses run at
//     once and a package waiting for its turn holds no goroutine;
//   - once ctx is done, no package without a slot is started: each is recorded
//     in Failures wrapping the context's error, and running analyses finish;
//   - a panic in one analysis is recovered as that package's failure ("panic:
//     <value>"), so it neither stops the others nor crashes the process;
//   - Items are sorted by Package, independent of which analysis finished first.
//
// Every write to the shared BatchResult is mutex-guarded, and it is returned
// only after every worker goroutine has joined (wg.Wait), so callers may
// safely invoke its methods (ExitCode, FormatFailures) on the returned value.
func (a *Analyzer) AnalyzeAll(ctx context.Context, opts AnalyzeOptions) BatchResult[AnalyzeResult] {
	batch := BatchResult[AnalyzeResult]{
		Items:    []AnalyzeResult{},
		Failures: make(map[string]error),
	}

	// Find packages without schemas
	packagesToAnalyze, err := a.findPackagesWithoutSchemas()
	if err != nil {
		// Enumeration failure: no per-package processing happened. Record it
		// as a synthetic failure so ExitCode reports a total failure (2).
		batch.Failures[""] = fmt.Errorf("failed to find packages: %w", err)
		return batch
	}

	if len(packagesToAnalyze) == 0 {
		return batch
	}

	// Process packages in parallel with max 3 concurrent
	const maxConcurrent = 3
	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup
	var mu sync.Mutex

	recordFailure := func(pkg string, err error) {
		mu.Lock()
		batch.Failures[pkg] = err
		mu.Unlock()
	}

	// acquireSlot waits for a free slot and returns nil holding it, or returns
	// the context's error holding none. The context is read before the wait
	// and again once a slot is held: a select whose cases are both ready picks
	// one at random, so the select alone would start a package after a cancel
	// whenever a slot happened to be free. The second read gives such a slot
	// back; the first skips the wait altogether once the context is done.
	acquireSlot := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case sem <- struct{}{}:
		}
		if err := ctx.Err(); err != nil {
			<-sem
			return err
		}
		return nil
	}

	for _, pkg := range packagesToAnalyze {
		// The slot is taken here, before the goroutine exists.
		if err := acquireSlot(); err != nil {
			recordFailure(pkg, fmt.Errorf("analysis of %s not started: %w", pkg, err))
			continue
		}

		wg.Add(1)
		go func(pkg string) {
			defer wg.Done()
			defer func() { <-sem }()
			// A panic in one package's analysis must not crash the process
			// or lose the rest of the batch: it becomes that package's
			// failure, wrapping the panic value when that is an error.
			defer func() {
				if r := recover(); r != nil {
					cause, ok := r.(error)
					if !ok {
						cause = fmt.Errorf("%v", r)
					}
					recordFailure(pkg, fmt.Errorf("analysis of %s: panic: %w", pkg, cause))
				}
			}()

			result, err := a.analyzeFn(ctx, pkg, opts)
			if err != nil {
				recordFailure(pkg, err)
				return
			}

			// Dereferenced before mu is locked, so that a nil result panics
			// outside the lock and the recover above can still record it.
			item := *result
			mu.Lock()
			batch.Items = append(batch.Items, item)
			mu.Unlock()
		}(pkg)
	}

	// Join every worker before returning so the BatchResult is fully
	// populated and its methods are safe to call.
	wg.Wait()

	// Deterministic final ordering, independent of completion order.
	sort.Slice(batch.Items, func(i, j int) bool {
		return batch.Items[i].Package < batch.Items[j].Package
	})

	return batch
}

// findPackagesWithoutSchemas finds all packages in the overlay that don't have schemas.
func (a *Analyzer) findPackagesWithoutSchemas() ([]string, error) {
	var packages []string

	// Walk the overlay directory
	entries, err := os.ReadDir(a.overlayPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read overlay directory: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		// Skip special directories
		name := entry.Name()
		if name == "profiles" || name == "metadata" || name == ".git" || name == ".autoupdate" || strings.HasPrefix(name, ".") {
			continue
		}

		// This is a category directory
		categoryPath := filepath.Join(a.overlayPath, name)
		pkgEntries, err := os.ReadDir(categoryPath)
		if err != nil {
			continue
		}

		for _, pkgEntry := range pkgEntries {
			if !pkgEntry.IsDir() {
				continue
			}

			pkg := name + "/" + pkgEntry.Name()

			// Check if package has a schema
			if _, exists := a.config.Packages[pkg]; !exists {
				// Check if package has ebuilds
				pkgPath := filepath.Join(categoryPath, pkgEntry.Name())
				if hasEbuilds(pkgPath) {
					packages = append(packages, pkg)
				}
			}
		}
	}

	return packages, nil
}

// hasEbuilds checks if a directory contains ebuild files.
func hasEbuilds(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}

	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".ebuild") {
			return true
		}
	}

	return false
}

// SaveSchema saves a validated schema to packages.toml.
func (a *Analyzer) SaveSchema(pkg string, schema *registry.PackageConfig) error {
	// Update in-memory config
	a.config.Packages[pkg] = *schema

	// Save to file
	return a.savePackagesConfig()
}

// savePackagesConfig saves the packages configuration to disk.
//
// Records are written one at a time, in sorted key order, each rendered by
// RenderRecord — the same function `overlay analyze` prints its suggestion with,
// so the file this produces is the file the linter accepts. Going through
// the TOML encoder instead is what used to break that: it ordered fields by
// struct declaration, turned a populated headers/meta into a sub-table the
// record scanner read as a phantom record, and wrote `timeout = 0` into every
// entry.
//
// The write is atomic — a temp file renamed over the registry only once every
// record is on disk and the file is closed — because this path rewrites all 411
// records at once and a partial write is a destroyed registry.
func (a *Analyzer) savePackagesConfig() error {
	configPath := filepath.Join(a.overlayPath, ".autoupdate", "packages.toml")

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(configPath), 0o750); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	pkgs := make([]string, 0, len(a.config.Packages))
	for pkg := range a.config.Packages {
		pkgs = append(pkgs, pkg)
	}
	sort.Strings(pkgs)

	var buf strings.Builder
	for i, pkg := range pkgs {
		if i > 0 {
			buf.WriteString("\n")
		}
		cfg := a.config.Packages[pkg]
		buf.WriteString(registry.RenderRecord(pkg, &cfg))
	}

	// The registry keeps the mode it already has; a new one is created 0644,
	// readable by the rest of the box whatever the umask.
	mode := os.FileMode(0o644)
	if info, err := os.Stat(configPath); err == nil {
		mode = info.Mode().Perm()
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("failed to stat %s: %w", configPath, err)
	}

	// Written atomically (see writePackagesConfigAtomically): a crash never
	// leaves a truncated registry to be committed and published.
	if err := fileutil.WriteFileAtomic(configPath, []byte(buf.String()), mode); err != nil {
		return fmt.Errorf("failed to write %s: %w", configPath, err)
	}
	return nil
}

// LoadAndMergeSchema loads existing config, adds/updates a schema, and saves.
// This ensures existing entries are preserved when adding new schemas.
func (a *Analyzer) LoadAndMergeSchema(pkg string, schema *registry.PackageConfig) error {
	// Reload config from disk to get latest state
	existingConfig, err := registry.LoadPackagesConfig(a.overlayPath)
	if err != nil && !errors.Is(err, registry.ErrPackagesConfigNotFound) {
		return fmt.Errorf("failed to load existing config: %w", err)
	}

	// Merge existing config with in-memory config
	if existingConfig != nil {
		for existingPkg, existingCfg := range existingConfig.Packages {
			// Only add if not already in memory (preserve in-memory changes)
			if _, exists := a.config.Packages[existingPkg]; !exists {
				a.config.Packages[existingPkg] = existingCfg
			}
		}
	}

	// Add/update the new schema
	a.config.Packages[pkg] = *schema

	// Save to file
	return a.savePackagesConfig()
}

// Config returns the packages configuration.
func (a *Analyzer) Config() *registry.PackagesConfig {
	return a.config
}

// OverlayPath returns the overlay path.
func (a *Analyzer) OverlayPath() string {
	return a.overlayPath
}

// Cache returns the analysis cache.
func (a *Analyzer) Cache() *AnalysisCache {
	return a.cache
}

// FetchContent fetches content from a data source (exported for testing). The
// fetch is bounded by ctx.
func (a *Analyzer) FetchContent(ctx context.Context, source DataSource) ([]byte, string, error) {
	content, err := a.fetchContent(ctx, source)
	if err != nil {
		return nil, "", err
	}
	return content, source.ContentType, nil
}
