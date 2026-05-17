// Package autoupdate provides intelligent package analysis using LLM to generate update schemas.
package autoupdate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/antchfx/xpath"

	"github.com/obentoo/bentoolkit/internal/common/logger"
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

// infoLogf is the sink used to emit Info-level diagnostics from the cache
// revalidation path. It defaults to the shared logger and is a package-private
// variable so tests can capture the emitted lines. Its signature mirrors
// logger.Info exactly.
var infoLogf = logger.Info

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
		return fmt.Errorf("%w: %v", ErrInvalidPattern, err)
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
		return fmt.Errorf("%w: %v", ErrInvalidXPath, err)
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
	SuggestedSchema *PackageConfig
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
	config *PackagesConfig
	// llmClient handles LLM-based analysis
	llmClient LLMProvider
	// httpClient handles HTTP requests with retry logic
	httpClient *RetryableHTTPClient
	// cache manages LLM analysis caching
	cache *AnalysisCache
	// rateLimiter manages request rate limiting
	rateLimiter *RateLimiter
	// configDir is the directory for storing cache files
	configDir string
	// ctx is the parent context for all outbound HTTP/LLM calls. It is set via
	// WithAnalyzerContext and originates in cmd/ (signal.NotifyContext), so a
	// SIGINT or deadline cancels every in-flight request. Defaults to
	// context.Background().
	ctx context.Context
	// opTimeout bounds a single outbound HTTP operation. Defaults to
	// DefaultOpTimeout.
	opTimeout time.Duration
	// llmTimeout bounds a single LLM analysis operation. Defaults to
	// DefaultLLMTimeout.
	llmTimeout time.Duration
}

// AnalyzerOption is a functional option for configuring Analyzer.
type AnalyzerOption func(*Analyzer) error

// WithAnalyzerLLMClient sets a custom LLM client for the analyzer.
func WithAnalyzerLLMClient(llm LLMProvider) AnalyzerOption {
	return func(a *Analyzer) error {
		a.llmClient = llm
		return nil
	}
}

// WithAnalyzerHTTPClient sets a custom HTTP client for the analyzer.
func WithAnalyzerHTTPClient(client *RetryableHTTPClient) AnalyzerOption {
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
func WithAnalyzerRateLimiter(limiter *RateLimiter) AnalyzerOption {
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
func WithAnalyzerPackagesConfig(config *PackagesConfig) AnalyzerOption {
	return func(a *Analyzer) error {
		a.config = config
		return nil
	}
}

// WithAnalyzerContext sets the parent context for the analyzer. The context
// threads through every outbound HTTP and LLM call, so cancelling it (e.g. on
// SIGINT or a deadline) aborts all in-flight requests. A nil context is
// rejected.
func WithAnalyzerContext(ctx context.Context) AnalyzerOption {
	return func(a *Analyzer) error {
		if ctx == nil {
			return errors.New("analyzer context must not be nil")
		}
		a.ctx = ctx
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
	configDir := filepath.Join(os.Getenv("HOME"), ".config", "bentoo", "autoupdate")

	analyzer := &Analyzer{
		overlayPath: overlayPath,
		configDir:   configDir,
		ctx:         context.Background(), // SAFE: default parent; replaced by WithAnalyzerContext when cmd/ wires signal.NotifyContext
		opTimeout:   DefaultOpTimeout,
		llmTimeout:  DefaultLLMTimeout,
	}

	// Apply options first to allow overriding configDir
	for _, opt := range opts {
		if err := opt(analyzer); err != nil {
			return nil, fmt.Errorf("failed to apply analyzer option: %w", err)
		}
	}

	// Load packages configuration if not provided
	if analyzer.config == nil {
		config, err := LoadPackagesConfig(overlayPath)
		if err != nil {
			// If config doesn't exist, create empty one
			if errors.Is(err, ErrPackagesConfigNotFound) {
				analyzer.config = &PackagesConfig{
					Packages: make(map[string]PackageConfig),
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
		analyzer.rateLimiter = NewRateLimiter()
	}

	// Initialize HTTP client if not provided
	if analyzer.httpClient == nil {
		analyzer.httpClient = NewRetryableHTTPClient()
	}

	return analyzer, nil
}

// Analyze analyzes a single package and suggests a schema.
func (a *Analyzer) Analyze(pkg string, opts AnalyzeOptions) (*AnalyzeResult, error) {
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
			return a.validateResult(result, opts)
		}
	}

	// Extract ebuild metadata
	meta, err := ExtractEbuildMetadata(a.overlayPath, pkg)
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
		content, err := a.fetchContent(source)
		if err != nil {
			lastErr = err
			continue
		}

		// Analyze content with LLM (if available)
		schema, err := a.analyzeContent(content, meta, opts.Hint, &source)
		if err != nil {
			lastErr = err
			continue
		}

		result.SuggestedSchema = schema
		result.DataSource = &source

		// Cache the analysis result
		if !opts.NoCache && a.cache != nil {
			if cacheErr := a.cache.Set(pkg, schema, source.URL); cacheErr != nil {
				logger.Debug("cache write failed for %s: %v", pkg, cacheErr)
			}
		}

		// Validate the schema
		return a.validateResult(result, opts)
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
func (a *Analyzer) validateResult(result *AnalyzeResult, opts AnalyzeOptions) (*AnalyzeResult, error) {
	if result.SuggestedSchema == nil {
		return result, result.Error
	}

	// Get ebuild version if not already set
	if result.EbuildVersion == "" {
		meta, err := ExtractEbuildMetadata(a.overlayPath, result.Package)
		if err != nil {
			result.Error = fmt.Errorf("failed to extract ebuild metadata for validation: %w", err)
			return result, result.Error
		}
		result.EbuildVersion = meta.Version
	}

	// Fetch content for validation
	content, err := a.fetchContentFromURL(result.SuggestedSchema.URL)
	if err != nil {
		result.Error = fmt.Errorf("failed to fetch content for validation: %w", err)
		return result, result.Error
	}

	// Validate schema
	validationResult := ValidateSchema(content, result.SuggestedSchema, result.EbuildVersion)
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
// The rate-limit wait is bounded by a child of the Analyzer's parent context
// (set via WithAnalyzerContext), so a cancelled parent aborts the wait.
func (a *Analyzer) fetchContent(source DataSource) ([]byte, error) {
	ctx, cancel := context.WithTimeout(a.ctx, a.opTimeout)
	defer cancel()

	// Apply rate limiting
	if err := a.rateLimiter.WaitHTTPForURL(ctx, source.URL); err != nil {
		return nil, fmt.Errorf("rate limit error: %w", err)
	}

	return a.fetchContentFromURL(source.URL)
}

// fetchContentFromURL fetches content from a URL. The request is bounded by a
// child of the Analyzer's parent context (set via WithAnalyzerContext) with the
// configured per-operation timeout, so a cancelled parent context or an expired
// deadline aborts the in-flight HTTP call.
func (a *Analyzer) fetchContentFromURL(url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(a.ctx, a.opTimeout)
	defer cancel()

	resp, err := a.httpClient.GetWithContext(ctx, url)
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

// analyzeContent analyzes content and generates a schema.
func (a *Analyzer) analyzeContent(content []byte, meta *EbuildMetadata, hint string, source *DataSource) (*PackageConfig, error) {
	// If LLM client is available, use it for analysis
	if a.llmClient != nil {
		ctx, cancel := context.WithTimeout(a.ctx, a.llmTimeout)
		defer cancel()

		// Apply LLM rate limiting
		if err := a.rateLimiter.WaitLLM(ctx); err != nil {
			return nil, fmt.Errorf("LLM rate limit error: %w", err)
		}

		analysis, err := a.llmClient.AnalyzeContent(content, meta, hint)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrAnalysisFailed, err)
		}

		return a.schemaFromAnalysis(analysis, source)
	}

	// Fallback: generate schema based on content type
	return a.generateDefaultSchema(content, source)
}

// schemaFromAnalysis converts LLM analysis to PackageConfig.
func (a *Analyzer) schemaFromAnalysis(analysis *SchemaAnalysis, source *DataSource) (*PackageConfig, error) {
	schema := &PackageConfig{
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
func (a *Analyzer) generateDefaultSchema(content []byte, source *DataSource) (*PackageConfig, error) {
	schema := &PackageConfig{
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
		parser := &JSONParser{Path: path}
		if _, err := parser.Parse(content); err == nil {
			return path
		}
	}

	return ""
}

// AnalyzeAll analyzes all packages without schemas.
// It processes packages in parallel with a maximum of 3 concurrent analyses.
//
// It returns a BatchResult: successfully analyzed packages land in Items, while
// a per-package failure is recorded in Failures keyed by the package name and
// the batch continues with the remaining packages. A failure to enumerate the
// packages is surfaced as a single synthetic Failures entry, which yields a
// total-failure exit code. The returned BatchResult is fully populated only
// after every worker goroutine has joined (wg.Wait), so callers may safely
// invoke its methods (ExitCode, FormatFailures) on the returned value.
func (a *Analyzer) AnalyzeAll(opts AnalyzeOptions) BatchResult[AnalyzeResult] {
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

	for _, pkg := range packagesToAnalyze {
		wg.Add(1)
		go func(pkg string) {
			defer wg.Done()

			// Acquire semaphore
			sem <- struct{}{}
			defer func() { <-sem }()

			result, err := a.Analyze(pkg, opts)

			mu.Lock()
			if err != nil {
				batch.Failures[pkg] = err
			} else {
				batch.Items = append(batch.Items, *result)
			}
			mu.Unlock()
		}(pkg)
	}

	// Join every worker before returning so the BatchResult is fully
	// populated and its methods are safe to call.
	wg.Wait()

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
func (a *Analyzer) SaveSchema(pkg string, schema *PackageConfig) error {
	// Update in-memory config
	a.config.Packages[pkg] = *schema

	// Save to file
	return a.savePackagesConfig()
}

// savePackagesConfig saves the packages configuration to disk.
// It preserves existing entries and formats TOML consistently with sorted keys.
func (a *Analyzer) savePackagesConfig() error {
	configPath := filepath.Join(a.overlayPath, ".autoupdate", "packages.toml")

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(configPath), 0o750); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	// Convert to file format (top-level keys are package names)
	fileConfig := make(map[string]PackageConfig)
	for pkg, cfg := range a.config.Packages {
		fileConfig[pkg] = cfg
	}

	// Write to temp file first for atomic operation
	tmpPath := configPath + ".tmp"
	f, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}

	// Use TOML encoder with consistent formatting
	encoder := toml.NewEncoder(f)
	if err := encoder.Encode(fileConfig); err != nil {
		f.Close()          //nolint:errcheck
		os.Remove(tmpPath) //nolint:errcheck
		return fmt.Errorf("failed to encode config: %w", err)
	}
	f.Close() //nolint:errcheck

	// Rename to final path (atomic on most filesystems)
	if err := os.Rename(tmpPath, configPath); err != nil {
		os.Remove(tmpPath) //nolint:errcheck
		return fmt.Errorf("failed to rename config file: %w", err)
	}

	return nil
}

// LoadAndMergeSchema loads existing config, adds/updates a schema, and saves.
// This ensures existing entries are preserved when adding new schemas.
func (a *Analyzer) LoadAndMergeSchema(pkg string, schema *PackageConfig) error {
	// Reload config from disk to get latest state
	existingConfig, err := LoadPackagesConfig(a.overlayPath)
	if err != nil && !errors.Is(err, ErrPackagesConfigNotFound) {
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
func (a *Analyzer) Config() *PackagesConfig {
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

// FetchContent fetches content from a data source (exported for testing).
func (a *Analyzer) FetchContent(source DataSource) ([]byte, string, error) {
	content, err := a.fetchContent(source)
	if err != nil {
		return nil, "", err
	}
	return content, source.ContentType, nil
}
