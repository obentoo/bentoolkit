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
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
)

// Error variables for analyzer errors
var (
	// ErrSchemaExists is returned when a schema already exists for a package
	ErrSchemaExists = errors.New("schema already exists for package")
	// ErrNoDataSources is returned when no data sources are found for a package
	ErrNoDataSources = errors.New("no data sources found for package")
	// ErrAnalysisFailed is returned when LLM analysis fails
	ErrAnalysisFailed = errors.New("LLM analysis failed")
)

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

// NewAnalyzer creates a new analyzer instance for the given overlay.
func NewAnalyzer(overlayPath string, opts ...AnalyzerOption) (*Analyzer, error) {
	// Determine config directory
	configDir := filepath.Join(os.Getenv("HOME"), ".config", "bentoo", "autoupdate")

	analyzer := &Analyzer{
		overlayPath: overlayPath,
		configDir:   configDir,
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
			_ = a.cache.Set(pkg, schema, source.URL)
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
func (a *Analyzer) fetchContent(source DataSource) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Apply rate limiting
	if err := a.rateLimiter.WaitHTTPForURL(ctx, source.URL); err != nil {
		return nil, fmt.Errorf("rate limit error: %w", err)
	}

	return a.fetchContentFromURL(source.URL)
}

// fetchContentFromURL fetches content from a URL.
func (a *Analyzer) fetchContentFromURL(url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	return content, nil
}

// analyzeContent analyzes content and generates a schema.
func (a *Analyzer) analyzeContent(content []byte, meta *EbuildMetadata, hint string, source *DataSource) (*PackageConfig, error) {
	// If LLM client is available, use it for analysis
	if a.llmClient != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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
func (a *Analyzer) AnalyzeAll(opts AnalyzeOptions) ([]AnalyzeResult, error) {
	// Find packages without schemas
	packagesToAnalyze, err := a.findPackagesWithoutSchemas()
	if err != nil {
		return nil, fmt.Errorf("failed to find packages: %w", err)
	}

	if len(packagesToAnalyze) == 0 {
		return []AnalyzeResult{}, nil
	}

	// Process packages in parallel with max 3 concurrent
	const maxConcurrent = 3
	results := make([]AnalyzeResult, len(packagesToAnalyze))
	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup
	var mu sync.Mutex
	resultIdx := 0

	for _, pkg := range packagesToAnalyze {
		wg.Add(1)
		go func(pkg string, idx int) {
			defer wg.Done()

			// Acquire semaphore
			sem <- struct{}{}
			defer func() { <-sem }()

			result, _ := a.Analyze(pkg, opts)

			mu.Lock()
			results[idx] = *result
			mu.Unlock()
		}(pkg, resultIdx)
		resultIdx++
	}

	wg.Wait()

	return results, nil
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
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
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
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("failed to encode config: %w", err)
	}
	f.Close()

	// Rename to final path (atomic on most filesystems)
	if err := os.Rename(tmpPath, configPath); err != nil {
		os.Remove(tmpPath)
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
