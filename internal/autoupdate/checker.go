// Package autoupdate provides version checking functionality for ebuild autoupdate.
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
	"time"

	"github.com/obentoo/bentoolkit/internal/common/ebuild"
)

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
	// Error contains any error that occurred during checking
	Error error
	// FromCache is true if the upstream version was retrieved from cache
	FromCache bool
}

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


// NewChecker creates a new checker instance for the given overlay.
// It loads the packages configuration and initializes cache and pending list.
func NewChecker(overlayPath string, opts ...CheckerOption) (*Checker, error) {
	// Determine config directory
	configDir := filepath.Join(os.Getenv("HOME"), ".config", "bentoo", "autoupdate")

	checker := &Checker{
		overlayPath: overlayPath,
		configDir:   configDir,
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

	// Initialize cache if not provided
	if checker.cache == nil {
		cache, err := NewCache(checker.configDir)
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
			result.HasUpdate = c.compareVersions(cachedVersion, currentVersion)

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
	result.HasUpdate = c.compareVersions(upstreamVersion, currentVersion)

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

// compareVersions compares upstream and current versions.
// Returns true if upstream is newer than current.
func (c *Checker) compareVersions(upstream, current string) bool {
	return ebuild.CompareVersions(upstream, current) > 0
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
	version, err := c.fetchAndParse(cfg.URL, cfg.Parser, cfg.Path, cfg.Pattern)
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

		version, err = c.fetchAndParse(cfg.FallbackURL, cfg.FallbackParser, cfg.Path, fallbackPattern)
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
func (c *Checker) fetchAndParse(url, parserType, path, pattern string) (string, error) {
	// Fetch content
	content, err := c.fetchContent(url)
	if err != nil {
		return "", err
	}

	// Create parser
	pathOrPattern := path
	if parserType == "regex" {
		pathOrPattern = pattern
	}

	parser, err := NewParser(parserType, pathOrPattern)
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
func (c *Checker) fetchContent(url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := c.httpClient.GetWithContext(ctx, url)
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

// CheckAll checks all packages in the configuration for updates.
// If force is true, the cache is bypassed for all packages.
func (c *Checker) CheckAll(force bool) ([]CheckResult, error) {
	results := make([]CheckResult, 0, len(c.config.Packages))

	for pkg := range c.config.Packages {
		result, _ := c.CheckPackage(pkg, force)
		results = append(results, *result)
	}

	return results, nil
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
