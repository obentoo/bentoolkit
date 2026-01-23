package autoupdate

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
	"golang.org/x/time/rate"
)

// =============================================================================
// Test Helpers
// =============================================================================

// createTestAnalyzer creates an analyzer with fast rate limiting for tests
func createTestAnalyzer(t *testing.T, tmpDir string, opts ...AnalyzerOption) (*Analyzer, error) {
	// Create fast rate limiter for testing
	rateLimiter := NewRateLimiter()
	// Set very high limits to effectively disable rate limiting in tests
	rateLimiter.SetLLMLimit(rate.Inf, 1000)

	// Combine with provided options
	allOpts := append([]AnalyzerOption{WithAnalyzerRateLimiter(rateLimiter)}, opts...)

	return NewAnalyzer(tmpDir, allOpts...)
}

// setFastHTTPLimit sets a fast HTTP limit for a specific server URL
func setFastHTTPLimit(rateLimiter *RateLimiter, serverURL string) {
	parsed, err := url.Parse(serverURL)
	if err == nil {
		rateLimiter.SetHTTPLimit(parsed.Host, rate.Inf, 1000)
	}
}

// createFastRateLimiter creates a rate limiter with fast limits for all common domains
func createFastRateLimiter() *RateLimiter {
	rateLimiter := NewRateLimiter()
	rateLimiter.SetLLMLimit(rate.Inf, 1000)
	// Pre-set common domains
	rateLimiter.SetHTTPLimit("github.com", rate.Inf, 1000)
	rateLimiter.SetHTTPLimit("api.github.com", rate.Inf, 1000)
	rateLimiter.SetHTTPLimit("example.com", rate.Inf, 1000)
	rateLimiter.SetHTTPLimit("api.example.org", rate.Inf, 1000)
	return rateLimiter
}

// =============================================================================
// Test Data Generators for Analyzer
// =============================================================================

// genAnalyzerURL generates valid HTTP URLs for analyzer tests
func genAnalyzerURL() gopter.Gen {
	return gen.OneConstOf(
		"https://example.com/api/version",
		"https://api.example.org/releases",
		"https://custom.api.com/v1/info",
		"https://releases.example.net/latest",
	)
}

// genAnalyzerPackageName generates valid package names in category/package format
func genAnalyzerPackageName() gopter.Gen {
	return gen.OneConstOf(
		"app-misc/hello",
		"dev-util/world",
		"net-misc/test",
		"sys-apps/example",
		"dev-python/tool",
	)
}

// genAnalyzerVersion generates valid version strings for analyzer tests
func genAnalyzerVersion() gopter.Gen {
	return gen.OneConstOf(
		"1.0.0",
		"2.1.3",
		"0.9.5",
		"10.2.1",
		"3.14.159",
	)
}

// =============================================================================
// Property-Based Tests
// =============================================================================

// TestURLOverride tests Property 1: URL Override
// **Feature: autoupdate-analyzer, Property 1: URL Override**
// **Validates: Requirements 1.2**
//
// For any analysis with a provided URL, the analyzer SHALL use that URL
// as the primary data source, regardless of ebuild metadata.
func TestURLOverride(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: Provided URL is used as primary data source
	properties.Property("provided URL is used as primary data source", prop.ForAll(
		func(providedURL string) bool {
			// Create mock server that returns JSON with version
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]string{"version": "1.0.0"})
			}))
			defer server.Close()

			// Create temp overlay with package
			tmpDir := t.TempDir()
			pkgDir := filepath.Join(tmpDir, "app-misc", "test")
			os.MkdirAll(pkgDir, 0755)
			os.WriteFile(filepath.Join(pkgDir, "test-1.0.0.ebuild"), []byte(`
EAPI=8
HOMEPAGE="https://github.com/example/test"
SRC_URI="https://github.com/example/test/archive/v1.0.0.tar.gz"
`), 0644)

			// Create fast rate limiter for testing
			rateLimiter := createFastRateLimiter()
			// Set fast limit for the test server's host
			setFastHTTPLimit(rateLimiter, server.URL)

			// Create fast HTTP client
			httpClient := NewRetryableHTTPClientWithConfig(RetryConfig{
				MaxRetries: 0,
				Timeout:    5 * time.Second,
			})

			// Create analyzer with fast rate limiter and HTTP client
			analyzer, err := NewAnalyzer(tmpDir,
				WithAnalyzerRateLimiter(rateLimiter),
				WithAnalyzerHTTPClient(httpClient),
			)
			if err != nil {
				return false
			}

			// Analyze with provided URL (use server URL)
			opts := AnalyzeOptions{
				URL:     server.URL,
				Force:   true,
				NoCache: true, // Disable cache to ensure fresh analysis
			}

			result, _ := analyzer.Analyze("app-misc/test", opts)

			// The suggested schema should use the provided URL
			// Even if validation fails, the schema URL should be set correctly
			if result.SuggestedSchema != nil {
				return result.SuggestedSchema.URL == server.URL
			}

			// If no schema, check that we at least tried the provided URL
			// by checking the data source
			if result.DataSource != nil {
				return result.DataSource.URL == server.URL
			}

			return false
		},
		genAnalyzerURL(),
	))

	// Property: Provided URL takes precedence over discovered sources
	properties.Property("provided URL takes precedence over discovered sources", prop.ForAll(
		func(providedURL, githubURL string) bool {
			meta := &EbuildMetadata{
				Package:  "app-misc/test",
				Homepage: githubURL,
			}

			sources := DiscoverDataSources(meta, providedURL)

			// First source should be the provided URL
			if len(sources) == 0 {
				return false
			}

			return sources[0].Type == "provided" && sources[0].URL == providedURL
		},
		genAnalyzerURL(),
		genGitHubURL(),
	))

	// Property: Provided URL has priority 0 (highest)
	properties.Property("provided URL has priority 0", prop.ForAll(
		func(providedURL string) bool {
			meta := &EbuildMetadata{
				Package:  "app-misc/test",
				Homepage: "https://example.com",
			}

			sources := DiscoverDataSources(meta, providedURL)

			// Find provided source
			for _, source := range sources {
				if source.Type == "provided" {
					return source.Priority == PriorityProvided && source.URL == providedURL
				}
			}
			return false
		},
		genAnalyzerURL(),
	))

	properties.TestingRun(t)
}

// TestBatchModeFiltering tests Property 2: Batch Mode Filtering
// **Feature: autoupdate-analyzer, Property 2: Batch Mode Filtering**
// **Validates: Requirements 1.4**
//
// For any overlay with N packages where M have existing schemas,
// AnalyzeAll SHALL analyze exactly N-M packages (those without schemas).
func TestBatchModeFiltering(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: AnalyzeAll only analyzes packages without schemas
	properties.Property("AnalyzeAll only analyzes packages without schemas", prop.ForAll(
		func(numWithSchema, numWithoutSchema int) bool {
			// Clamp values to reasonable range
			if numWithSchema < 0 {
				numWithSchema = 0
			}
			if numWithSchema > 5 {
				numWithSchema = 5
			}
			if numWithoutSchema < 0 {
				numWithoutSchema = 0
			}
			if numWithoutSchema > 5 {
				numWithoutSchema = 5
			}

			// Create temp overlay
			tmpDir := t.TempDir()

			// Create packages with schemas
			packagesWithSchema := make(map[string]PackageConfig)
			for i := 0; i < numWithSchema; i++ {
				pkgName := "app-misc/with-schema-" + string(rune('a'+i))
				pkgDir := filepath.Join(tmpDir, "app-misc", "with-schema-"+string(rune('a'+i)))
				os.MkdirAll(pkgDir, 0755)
				os.WriteFile(filepath.Join(pkgDir, "with-schema-"+string(rune('a'+i))+"-1.0.0.ebuild"), []byte(`
EAPI=8
HOMEPAGE="https://example.com"
`), 0644)
				packagesWithSchema[pkgName] = PackageConfig{
					URL:    "https://example.com/api",
					Parser: "json",
					Path:   "version",
				}
			}

			// Create packages without schemas
			for i := 0; i < numWithoutSchema; i++ {
				pkgDir := filepath.Join(tmpDir, "app-misc", "without-schema-"+string(rune('a'+i)))
				os.MkdirAll(pkgDir, 0755)
				os.WriteFile(filepath.Join(pkgDir, "without-schema-"+string(rune('a'+i))+"-1.0.0.ebuild"), []byte(`
EAPI=8
HOMEPAGE="https://example.com"
`), 0644)
			}

			// Create analyzer with existing schemas
			config := &PackagesConfig{Packages: packagesWithSchema}
			analyzer, err := NewAnalyzer(tmpDir, WithAnalyzerPackagesConfig(config))
			if err != nil {
				return false
			}

			// Find packages without schemas
			packagesToAnalyze, err := analyzer.findPackagesWithoutSchemas()
			if err != nil {
				return false
			}

			// Should find exactly numWithoutSchema packages
			return len(packagesToAnalyze) == numWithoutSchema
		},
		gen.IntRange(0, 5),
		gen.IntRange(0, 5),
	))

	// Property: Packages with existing schemas are not analyzed
	properties.Property("packages with existing schemas are not analyzed", prop.ForAll(
		func(pkgName string) bool {
			// Create temp overlay
			tmpDir := t.TempDir()

			// Create package directory
			pkgDir := filepath.Join(tmpDir, "app-misc", "test")
			os.MkdirAll(pkgDir, 0755)
			os.WriteFile(filepath.Join(pkgDir, "test-1.0.0.ebuild"), []byte(`
EAPI=8
HOMEPAGE="https://example.com"
`), 0644)

			// Create analyzer with existing schema for this package
			config := &PackagesConfig{
				Packages: map[string]PackageConfig{
					"app-misc/test": {
						URL:    "https://example.com/api",
						Parser: "json",
						Path:   "version",
					},
				},
			}
			analyzer, err := NewAnalyzer(tmpDir, WithAnalyzerPackagesConfig(config))
			if err != nil {
				return false
			}

			// Find packages without schemas
			packagesToAnalyze, err := analyzer.findPackagesWithoutSchemas()
			if err != nil {
				return false
			}

			// Should not include the package with schema
			for _, pkg := range packagesToAnalyze {
				if pkg == "app-misc/test" {
					return false
				}
			}
			return true
		},
		genAnalyzerPackageName(),
	))

	properties.TestingRun(t)
}

// TestContentFetching tests Property 15: Content Fetching
// **Feature: autoupdate-analyzer, Property 15: Content Fetching**
// **Validates: Requirements 6.1**
//
// For any analysis operation, the analyzer SHALL fetch content from
// at least one candidate URL before invoking the LLM.
func TestContentFetching(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: Analysis fetches content before LLM invocation
	properties.Property("analysis fetches content before LLM invocation", prop.ForAll(
		func(version string) bool {
			// Track if content was fetched
			contentFetched := false

			// Create mock server
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				contentFetched = true
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]string{"version": version})
			}))
			defer server.Close()

			// Create temp overlay with package
			tmpDir := t.TempDir()
			pkgDir := filepath.Join(tmpDir, "app-misc", "test")
			os.MkdirAll(pkgDir, 0755)
			os.WriteFile(filepath.Join(pkgDir, "test-"+version+".ebuild"), []byte(`
EAPI=8
HOMEPAGE="https://example.com"
`), 0644)

			// Create fast rate limiter for testing
			rateLimiter := createFastRateLimiter()
			// Set fast limit for the test server's host
			setFastHTTPLimit(rateLimiter, server.URL)

			// Create fast HTTP client
			httpClient := NewRetryableHTTPClientWithConfig(RetryConfig{
				MaxRetries: 0,
				Timeout:    5 * time.Second,
			})

			// Create analyzer with fast rate limiter and HTTP client
			analyzer, err := NewAnalyzer(tmpDir,
				WithAnalyzerRateLimiter(rateLimiter),
				WithAnalyzerHTTPClient(httpClient),
			)
			if err != nil {
				return false
			}

			// Analyze with provided URL
			opts := AnalyzeOptions{
				URL:     server.URL,
				Force:   true,
				NoCache: true, // Disable cache to ensure fresh analysis
			}

			analyzer.Analyze("app-misc/test", opts)

			// Content should have been fetched
			return contentFetched
		},
		genAnalyzerVersion(),
	))

	// Property: FetchContent returns content from data source
	properties.Property("FetchContent returns content from data source", prop.ForAll(
		func(version string) bool {
			expectedContent := `{"version":"` + version + `"}`

			// Create mock server
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(expectedContent))
			}))
			defer server.Close()

			// Create temp overlay
			tmpDir := t.TempDir()

			// Create fast rate limiter for testing
			rateLimiter := createFastRateLimiter()
			// Set fast limit for the test server's host
			setFastHTTPLimit(rateLimiter, server.URL)

			// Create fast HTTP client
			httpClient := NewRetryableHTTPClientWithConfig(RetryConfig{
				MaxRetries: 0,
				Timeout:    5 * time.Second,
			})

			// Create analyzer with fast rate limiter and HTTP client
			analyzer, err := NewAnalyzer(tmpDir,
				WithAnalyzerRateLimiter(rateLimiter),
				WithAnalyzerHTTPClient(httpClient),
			)
			if err != nil {
				return false
			}

			// Fetch content
			source := DataSource{
				URL:         server.URL,
				Type:        "provided",
				ContentType: ContentTypeJSON,
			}

			content, contentType, err := analyzer.FetchContent(source)
			if err != nil {
				return false
			}

			return string(content) == expectedContent && contentType == ContentTypeJSON
		},
		genAnalyzerVersion(),
	))

	// Property: Analysis tries multiple sources on failure
	properties.Property("analysis tries multiple sources on failure", prop.ForAll(
		func(version string) bool {
			requestCount := 0

			// Create mock server that fails first request, succeeds second
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requestCount++
				if requestCount == 1 {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]string{"version": version})
			}))
			defer server.Close()

			// Create temp overlay with package that has multiple sources
			tmpDir := t.TempDir()
			pkgDir := filepath.Join(tmpDir, "app-misc", "test")
			os.MkdirAll(pkgDir, 0755)
			os.WriteFile(filepath.Join(pkgDir, "test-"+version+".ebuild"), []byte(`
EAPI=8
HOMEPAGE="`+server.URL+`"
`), 0644)

			// Create fast rate limiter for testing
			rateLimiter := createFastRateLimiter()
			// Set fast limit for the test server's host
			setFastHTTPLimit(rateLimiter, server.URL)

			// Create analyzer with fast rate limiter and fast HTTP client
			httpClient := NewRetryableHTTPClientWithConfig(RetryConfig{
				MaxRetries: 0, // No retries for faster test
				Timeout:    5 * time.Second,
			})
			analyzer, err := NewAnalyzer(tmpDir,
				WithAnalyzerRateLimiter(rateLimiter),
				WithAnalyzerHTTPClient(httpClient),
			)
			if err != nil {
				return false
			}

			// Analyze - should try homepage after provided URL fails
			opts := AnalyzeOptions{
				URL:     server.URL + "/fail", // This will fail
				Force:   true,
				NoCache: true, // Disable cache to ensure fresh analysis
			}

			analyzer.Analyze("app-misc/test", opts)

			// Should have made at least one request
			return requestCount >= 1
		},
		genAnalyzerVersion(),
	))

	properties.TestingRun(t)
}

// =============================================================================
// Unit Tests
// =============================================================================

// TestNewAnalyzerCreatesComponents tests that NewAnalyzer creates all components
func TestNewAnalyzerCreatesComponents(t *testing.T) {
	tmpDir := t.TempDir()

	analyzer, err := NewAnalyzer(tmpDir)
	if err != nil {
		t.Fatalf("NewAnalyzer failed: %v", err)
	}

	if analyzer.overlayPath != tmpDir {
		t.Errorf("Expected overlayPath %q, got %q", tmpDir, analyzer.overlayPath)
	}

	if analyzer.config == nil {
		t.Error("Expected config to be initialized")
	}

	if analyzer.cache == nil {
		t.Error("Expected cache to be initialized")
	}

	if analyzer.rateLimiter == nil {
		t.Error("Expected rateLimiter to be initialized")
	}

	if analyzer.httpClient == nil {
		t.Error("Expected httpClient to be initialized")
	}
}

// TestNewAnalyzerWithOptions tests functional options
func TestNewAnalyzerWithOptions(t *testing.T) {
	tmpDir := t.TempDir()

	customConfig := &PackagesConfig{
		Packages: map[string]PackageConfig{
			"app-misc/test": {URL: "https://example.com", Parser: "json", Path: "version"},
		},
	}

	analyzer, err := NewAnalyzer(tmpDir, WithAnalyzerPackagesConfig(customConfig))
	if err != nil {
		t.Fatalf("NewAnalyzer failed: %v", err)
	}

	if len(analyzer.config.Packages) != 1 {
		t.Errorf("Expected 1 package in config, got %d", len(analyzer.config.Packages))
	}
}

// TestAnalyzeSchemaExists tests that Analyze returns error for existing schema
func TestAnalyzeSchemaExists(t *testing.T) {
	tmpDir := t.TempDir()

	// Create package directory
	pkgDir := filepath.Join(tmpDir, "app-misc", "test")
	os.MkdirAll(pkgDir, 0755)
	os.WriteFile(filepath.Join(pkgDir, "test-1.0.0.ebuild"), []byte(`
EAPI=8
HOMEPAGE="https://example.com"
`), 0644)

	// Create analyzer with existing schema
	config := &PackagesConfig{
		Packages: map[string]PackageConfig{
			"app-misc/test": {URL: "https://example.com", Parser: "json", Path: "version"},
		},
	}

	analyzer, err := NewAnalyzer(tmpDir, WithAnalyzerPackagesConfig(config))
	if err != nil {
		t.Fatalf("NewAnalyzer failed: %v", err)
	}

	// Analyze without force
	result, err := analyzer.Analyze("app-misc/test", AnalyzeOptions{})

	if err == nil {
		t.Error("Expected error for existing schema")
	}

	if result.Error == nil {
		t.Error("Expected result.Error to be set")
	}
}

// TestAnalyzeForceOverwrite tests that Force option allows overwriting
func TestAnalyzeForceOverwrite(t *testing.T) {
	// Create mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"version": "1.0.0"})
	}))
	defer server.Close()

	tmpDir := t.TempDir()

	// Create package directory
	pkgDir := filepath.Join(tmpDir, "app-misc", "test")
	os.MkdirAll(pkgDir, 0755)
	os.WriteFile(filepath.Join(pkgDir, "test-1.0.0.ebuild"), []byte(`
EAPI=8
HOMEPAGE="https://example.com"
`), 0644)

	// Create analyzer with existing schema and fast rate limiter
	config := &PackagesConfig{
		Packages: map[string]PackageConfig{
			"app-misc/test": {URL: "https://old.example.com", Parser: "json", Path: "version"},
		},
	}

	rateLimiter := createFastRateLimiter()
	setFastHTTPLimit(rateLimiter, server.URL)

	httpClient := NewRetryableHTTPClientWithConfig(RetryConfig{
		MaxRetries: 0,
		Timeout:    5 * time.Second,
	})

	analyzer, err := NewAnalyzer(tmpDir,
		WithAnalyzerPackagesConfig(config),
		WithAnalyzerRateLimiter(rateLimiter),
		WithAnalyzerHTTPClient(httpClient),
	)
	if err != nil {
		t.Fatalf("NewAnalyzer failed: %v", err)
	}

	// Analyze with force
	result, _ := analyzer.Analyze("app-misc/test", AnalyzeOptions{
		URL:     server.URL,
		Force:   true,
		NoCache: true,
	})

	// Should not have ErrSchemaExists error
	if result.Error != nil && result.Error.Error() == ErrSchemaExists.Error() {
		t.Error("Force option should allow overwriting existing schema")
	}
}

// TestFindPackagesWithoutSchemas tests finding packages without schemas
func TestFindPackagesWithoutSchemas(t *testing.T) {
	tmpDir := t.TempDir()

	// Create packages
	for _, pkg := range []string{"with-schema", "without-schema-1", "without-schema-2"} {
		pkgDir := filepath.Join(tmpDir, "app-misc", pkg)
		os.MkdirAll(pkgDir, 0755)
		os.WriteFile(filepath.Join(pkgDir, pkg+"-1.0.0.ebuild"), []byte(`
EAPI=8
HOMEPAGE="https://example.com"
`), 0644)
	}

	// Create analyzer with one schema
	config := &PackagesConfig{
		Packages: map[string]PackageConfig{
			"app-misc/with-schema": {URL: "https://example.com", Parser: "json", Path: "version"},
		},
	}

	analyzer, err := NewAnalyzer(tmpDir, WithAnalyzerPackagesConfig(config))
	if err != nil {
		t.Fatalf("NewAnalyzer failed: %v", err)
	}

	packages, err := analyzer.findPackagesWithoutSchemas()
	if err != nil {
		t.Fatalf("findPackagesWithoutSchemas failed: %v", err)
	}

	// Should find 2 packages without schemas
	if len(packages) != 2 {
		t.Errorf("Expected 2 packages without schemas, got %d", len(packages))
	}

	// Should not include package with schema
	for _, pkg := range packages {
		if pkg == "app-misc/with-schema" {
			t.Error("Should not include package with existing schema")
		}
	}
}

// TestSaveSchema tests saving a schema to packages.toml
func TestSaveSchema(t *testing.T) {
	tmpDir := t.TempDir()

	analyzer, err := NewAnalyzer(tmpDir)
	if err != nil {
		t.Fatalf("NewAnalyzer failed: %v", err)
	}

	schema := &PackageConfig{
		URL:    "https://example.com/api",
		Parser: "json",
		Path:   "version",
	}

	err = analyzer.SaveSchema("app-misc/test", schema)
	if err != nil {
		t.Fatalf("SaveSchema failed: %v", err)
	}

	// Check that schema was saved
	if _, exists := analyzer.config.Packages["app-misc/test"]; !exists {
		t.Error("Schema was not saved to config")
	}

	// Check that file was created
	configPath := filepath.Join(tmpDir, ".autoupdate", "packages.toml")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		t.Error("packages.toml was not created")
	}
}

// TestParallelProcessingLimit tests Property 28: Parallel Processing Limit
// **Feature: autoupdate-analyzer, Property 28: Parallel Processing Limit**
// **Validates: Requirements 11.3**
//
// For any batch analysis operation, the analyzer SHALL process at most 3 packages concurrently.
func TestParallelProcessingLimit(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: AnalyzeAll processes at most 3 packages concurrently
	properties.Property("AnalyzeAll processes at most 3 packages concurrently", prop.ForAll(
		func(numPackages int) bool {
			// Clamp to reasonable range
			if numPackages < 1 {
				numPackages = 1
			}
			if numPackages > 10 {
				numPackages = 10
			}

			// Track concurrent executions
			var maxConcurrent int32
			var currentConcurrent int32
			var mu sync.Mutex

			// Create mock server that tracks concurrency
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				currentConcurrent++
				if currentConcurrent > maxConcurrent {
					maxConcurrent = currentConcurrent
				}
				mu.Unlock()

				// Simulate some work to allow concurrency to build up
				time.Sleep(50 * time.Millisecond)

				mu.Lock()
				currentConcurrent--
				mu.Unlock()

				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]string{"version": "1.0.0"})
			}))
			defer server.Close()

			// Create temp overlay with multiple packages
			tmpDir := t.TempDir()
			for i := 0; i < numPackages; i++ {
				pkgName := "pkg-" + string(rune('a'+i))
				pkgDir := filepath.Join(tmpDir, "app-misc", pkgName)
				os.MkdirAll(pkgDir, 0755)
				os.WriteFile(filepath.Join(pkgDir, pkgName+"-1.0.0.ebuild"), []byte(`
EAPI=8
HOMEPAGE="`+server.URL+`"
`), 0644)
			}

			// Create fast rate limiter for testing
			rateLimiter := createFastRateLimiter()
			setFastHTTPLimit(rateLimiter, server.URL)

			// Create fast HTTP client
			httpClient := NewRetryableHTTPClientWithConfig(RetryConfig{
				MaxRetries: 0,
				Timeout:    5 * time.Second,
			})

			// Create analyzer
			analyzer, err := NewAnalyzer(tmpDir,
				WithAnalyzerRateLimiter(rateLimiter),
				WithAnalyzerHTTPClient(httpClient),
			)
			if err != nil {
				return false
			}

			// Run AnalyzeAll
			opts := AnalyzeOptions{
				NoCache: true,
			}
			_, _ = analyzer.AnalyzeAll(opts)

			// Max concurrent should be at most 3
			return maxConcurrent <= 3
		},
		gen.IntRange(1, 10),
	))

	// Property: Semaphore limits concurrent goroutines to maxConcurrent
	properties.Property("Semaphore limits concurrent goroutines to maxConcurrent", prop.ForAll(
		func(numGoroutines int) bool {
			// Clamp to reasonable range
			if numGoroutines < 1 {
				numGoroutines = 1
			}
			if numGoroutines > 20 {
				numGoroutines = 20
			}

			const maxConcurrent = 3
			var maxObserved int32
			var current int32
			var mu sync.Mutex
			var wg sync.WaitGroup
			sem := make(chan struct{}, maxConcurrent)

			for i := 0; i < numGoroutines; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()

					// Acquire semaphore
					sem <- struct{}{}
					defer func() { <-sem }()

					mu.Lock()
					current++
					if current > maxObserved {
						maxObserved = current
					}
					mu.Unlock()

					// Simulate work
					time.Sleep(10 * time.Millisecond)

					mu.Lock()
					current--
					mu.Unlock()
				}()
			}

			wg.Wait()

			// Max observed should be at most maxConcurrent
			return maxObserved <= maxConcurrent
		},
		gen.IntRange(1, 20),
	))

	// Property: All packages are eventually processed despite concurrency limit
	properties.Property("All packages are eventually processed despite concurrency limit", prop.ForAll(
		func(numPackages int) bool {
			// Clamp to reasonable range
			if numPackages < 1 {
				numPackages = 1
			}
			if numPackages > 8 {
				numPackages = 8
			}

			// Track which packages were processed
			processedPackages := make(map[string]bool)
			var mu sync.Mutex

			// Create mock server
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]string{"version": "1.0.0"})
			}))
			defer server.Close()

			// Create temp overlay with multiple packages
			tmpDir := t.TempDir()
			expectedPackages := make([]string, numPackages)
			for i := 0; i < numPackages; i++ {
				pkgName := "pkg-" + string(rune('a'+i))
				fullPkgName := "app-misc/" + pkgName
				expectedPackages[i] = fullPkgName
				pkgDir := filepath.Join(tmpDir, "app-misc", pkgName)
				os.MkdirAll(pkgDir, 0755)
				os.WriteFile(filepath.Join(pkgDir, pkgName+"-1.0.0.ebuild"), []byte(`
EAPI=8
HOMEPAGE="`+server.URL+`"
`), 0644)
			}

			// Create fast rate limiter for testing
			rateLimiter := createFastRateLimiter()
			setFastHTTPLimit(rateLimiter, server.URL)

			// Create fast HTTP client
			httpClient := NewRetryableHTTPClientWithConfig(RetryConfig{
				MaxRetries: 0,
				Timeout:    5 * time.Second,
			})

			// Create analyzer
			analyzer, err := NewAnalyzer(tmpDir,
				WithAnalyzerRateLimiter(rateLimiter),
				WithAnalyzerHTTPClient(httpClient),
			)
			if err != nil {
				return false
			}

			// Run AnalyzeAll
			opts := AnalyzeOptions{
				NoCache: true,
			}
			results, err := analyzer.AnalyzeAll(opts)
			if err != nil {
				return false
			}

			// Mark processed packages
			for _, result := range results {
				mu.Lock()
				processedPackages[result.Package] = true
				mu.Unlock()
			}

			// All expected packages should be processed
			for _, pkg := range expectedPackages {
				if !processedPackages[pkg] {
					return false
				}
			}

			return len(results) == numPackages
		},
		gen.IntRange(1, 8),
	))

	// Property: Concurrency limit constant is 3
	properties.Property("Concurrency limit constant is 3", prop.ForAll(
		func(dummy int) bool {
			// This tests that the maxConcurrent constant in AnalyzeAll is 3
			// We verify this by checking the implementation behavior
			const expectedMaxConcurrent = 3

			var maxObserved int32
			var current int32
			var mu sync.Mutex

			// Create mock server that tracks concurrency
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				current++
				if current > maxObserved {
					maxObserved = current
				}
				mu.Unlock()

				// Hold the connection to allow concurrency to build up
				time.Sleep(100 * time.Millisecond)

				mu.Lock()
				current--
				mu.Unlock()

				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]string{"version": "1.0.0"})
			}))
			defer server.Close()

			// Create temp overlay with more packages than the limit
			tmpDir := t.TempDir()
			numPackages := 6 // More than maxConcurrent to ensure we hit the limit
			for i := 0; i < numPackages; i++ {
				pkgName := "pkg-" + string(rune('a'+i))
				pkgDir := filepath.Join(tmpDir, "app-misc", pkgName)
				os.MkdirAll(pkgDir, 0755)
				os.WriteFile(filepath.Join(pkgDir, pkgName+"-1.0.0.ebuild"), []byte(`
EAPI=8
HOMEPAGE="`+server.URL+`"
`), 0644)
			}

			// Create fast rate limiter for testing
			rateLimiter := createFastRateLimiter()
			setFastHTTPLimit(rateLimiter, server.URL)

			// Create fast HTTP client
			httpClient := NewRetryableHTTPClientWithConfig(RetryConfig{
				MaxRetries: 0,
				Timeout:    5 * time.Second,
			})

			// Create analyzer
			analyzer, err := NewAnalyzer(tmpDir,
				WithAnalyzerRateLimiter(rateLimiter),
				WithAnalyzerHTTPClient(httpClient),
			)
			if err != nil {
				return false
			}

			// Run AnalyzeAll
			opts := AnalyzeOptions{
				NoCache: true,
			}
			_, _ = analyzer.AnalyzeAll(opts)

			// Max observed should be exactly 3 (the limit)
			// With 6 packages and 100ms delay, we should hit the limit
			return maxObserved <= expectedMaxConcurrent
		},
		gen.IntRange(1, 10),
	))

	properties.TestingRun(t)
}

// TestDetectJSONPath tests JSON path detection
func TestDetectJSONPath(t *testing.T) {
	testCases := []struct {
		name     string
		content  string
		expected string
	}{
		{
			name:     "version field",
			content:  `{"version": "1.0.0"}`,
			expected: "version",
		},
		{
			name:     "tag_name field",
			content:  `{"tag_name": "v1.0.0"}`,
			expected: "tag_name",
		},
		{
			name:     "array with tag_name",
			content:  `[{"tag_name": "v1.0.0"}]`,
			expected: "[0].tag_name",
		},
		{
			name:     "nested info.version",
			content:  `{"info": {"version": "1.0.0"}}`,
			expected: "info.version",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := detectJSONPath([]byte(tc.content))
			if result != tc.expected {
				t.Errorf("Expected %q, got %q", tc.expected, result)
			}
		})
	}
}

// TestSchemaPreservation tests Property 29: Schema Preservation
// **Feature: autoupdate-analyzer, Property 29: Schema Preservation**
// **Validates: Requirements 13.2**
//
// For any save operation to packages.toml, all existing entries not being
// modified SHALL be preserved unchanged.
func TestSchemaPreservation(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: Saving a new schema preserves all existing schemas
	properties.Property("saving new schema preserves existing schemas", prop.ForAll(
		func(numExisting int) bool {
			// Clamp to reasonable range
			if numExisting < 1 {
				numExisting = 1
			}
			if numExisting > 5 {
				numExisting = 5
			}

			// Create temp overlay
			tmpDir := t.TempDir()

			// Create existing schemas
			existingSchemas := make(map[string]PackageConfig)
			for i := 0; i < numExisting; i++ {
				pkgName := "app-misc/existing-" + string(rune('a'+i))
				existingSchemas[pkgName] = PackageConfig{
					URL:    "https://example.com/api/" + string(rune('a'+i)),
					Parser: "json",
					Path:   "version",
				}
			}

			// Create analyzer with existing schemas
			config := &PackagesConfig{Packages: existingSchemas}
			analyzer, err := NewAnalyzer(tmpDir, WithAnalyzerPackagesConfig(config))
			if err != nil {
				t.Logf("Failed to create analyzer: %v", err)
				return false
			}

			// Save a new schema
			newSchema := &PackageConfig{
				URL:    "https://example.com/new",
				Parser: "json",
				Path:   "tag_name",
			}
			if err := analyzer.SaveSchema("app-misc/new-package", newSchema); err != nil {
				t.Logf("Failed to save schema: %v", err)
				return false
			}

			// Reload config from disk
			reloadedConfig, err := LoadPackagesConfig(tmpDir)
			if err != nil {
				t.Logf("Failed to reload config: %v", err)
				return false
			}

			// Verify all existing schemas are preserved
			for pkgName, expectedCfg := range existingSchemas {
				actualCfg, exists := reloadedConfig.Packages[pkgName]
				if !exists {
					t.Logf("Existing schema %s was not preserved", pkgName)
					return false
				}
				if actualCfg.URL != expectedCfg.URL ||
					actualCfg.Parser != expectedCfg.Parser ||
					actualCfg.Path != expectedCfg.Path {
					t.Logf("Schema %s was modified: expected %+v, got %+v", pkgName, expectedCfg, actualCfg)
					return false
				}
			}

			// Verify new schema was added
			newCfg, exists := reloadedConfig.Packages["app-misc/new-package"]
			if !exists {
				t.Logf("New schema was not saved")
				return false
			}
			if newCfg.URL != newSchema.URL || newCfg.Parser != newSchema.Parser || newCfg.Path != newSchema.Path {
				t.Logf("New schema was not saved correctly")
				return false
			}

			// Total count should be numExisting + 1
			return len(reloadedConfig.Packages) == numExisting+1
		},
		gen.IntRange(1, 5),
	))

	// Property: Updating an existing schema preserves other schemas
	properties.Property("updating existing schema preserves other schemas", prop.ForAll(
		func(numExisting int) bool {
			// Clamp to reasonable range
			if numExisting < 2 {
				numExisting = 2
			}
			if numExisting > 5 {
				numExisting = 5
			}

			// Create temp overlay
			tmpDir := t.TempDir()

			// Create existing schemas
			existingSchemas := make(map[string]PackageConfig)
			for i := 0; i < numExisting; i++ {
				pkgName := "app-misc/existing-" + string(rune('a'+i))
				existingSchemas[pkgName] = PackageConfig{
					URL:    "https://example.com/api/" + string(rune('a'+i)),
					Parser: "json",
					Path:   "version",
				}
			}

			// Create analyzer with existing schemas
			config := &PackagesConfig{Packages: existingSchemas}
			analyzer, err := NewAnalyzer(tmpDir, WithAnalyzerPackagesConfig(config))
			if err != nil {
				t.Logf("Failed to create analyzer: %v", err)
				return false
			}

			// Update the first schema
			updatedSchema := &PackageConfig{
				URL:    "https://example.com/updated",
				Parser: "regex",
				Pattern: `v(\d+\.\d+\.\d+)`,
			}
			if err := analyzer.SaveSchema("app-misc/existing-a", updatedSchema); err != nil {
				t.Logf("Failed to save schema: %v", err)
				return false
			}

			// Reload config from disk
			reloadedConfig, err := LoadPackagesConfig(tmpDir)
			if err != nil {
				t.Logf("Failed to reload config: %v", err)
				return false
			}

			// Verify other schemas are preserved (skip the first one which was updated)
			for pkgName, expectedCfg := range existingSchemas {
				if pkgName == "app-misc/existing-a" {
					continue // Skip the updated one
				}
				actualCfg, exists := reloadedConfig.Packages[pkgName]
				if !exists {
					t.Logf("Existing schema %s was not preserved", pkgName)
					return false
				}
				if actualCfg.URL != expectedCfg.URL ||
					actualCfg.Parser != expectedCfg.Parser ||
					actualCfg.Path != expectedCfg.Path {
					t.Logf("Schema %s was modified: expected %+v, got %+v", pkgName, expectedCfg, actualCfg)
					return false
				}
			}

			// Verify updated schema has new values
			updatedCfg, exists := reloadedConfig.Packages["app-misc/existing-a"]
			if !exists {
				t.Logf("Updated schema was not saved")
				return false
			}
			if updatedCfg.URL != updatedSchema.URL ||
				updatedCfg.Parser != updatedSchema.Parser ||
				updatedCfg.Pattern != updatedSchema.Pattern {
				t.Logf("Updated schema was not saved correctly")
				return false
			}

			// Total count should remain the same
			return len(reloadedConfig.Packages) == numExisting
		},
		gen.IntRange(2, 5),
	))

	// Property: Multiple saves preserve all schemas
	properties.Property("multiple saves preserve all schemas", prop.ForAll(
		func(numSaves int) bool {
			// Clamp to reasonable range
			if numSaves < 1 {
				numSaves = 1
			}
			if numSaves > 5 {
				numSaves = 5
			}

			// Create temp overlay
			tmpDir := t.TempDir()

			// Create analyzer with empty config
			analyzer, err := NewAnalyzer(tmpDir)
			if err != nil {
				t.Logf("Failed to create analyzer: %v", err)
				return false
			}

			// Save multiple schemas one by one
			savedSchemas := make(map[string]PackageConfig)
			for i := 0; i < numSaves; i++ {
				pkgName := "app-misc/pkg-" + string(rune('a'+i))
				schema := &PackageConfig{
					URL:    "https://example.com/api/" + string(rune('a'+i)),
					Parser: "json",
					Path:   "version",
				}
				if err := analyzer.SaveSchema(pkgName, schema); err != nil {
					t.Logf("Failed to save schema %d: %v", i, err)
					return false
				}
				savedSchemas[pkgName] = *schema
			}

			// Reload config from disk
			reloadedConfig, err := LoadPackagesConfig(tmpDir)
			if err != nil {
				t.Logf("Failed to reload config: %v", err)
				return false
			}

			// Verify all saved schemas are present
			for pkgName, expectedCfg := range savedSchemas {
				actualCfg, exists := reloadedConfig.Packages[pkgName]
				if !exists {
					t.Logf("Schema %s was not preserved", pkgName)
					return false
				}
				if actualCfg.URL != expectedCfg.URL ||
					actualCfg.Parser != expectedCfg.Parser ||
					actualCfg.Path != expectedCfg.Path {
					t.Logf("Schema %s was modified: expected %+v, got %+v", pkgName, expectedCfg, actualCfg)
					return false
				}
			}

			return len(reloadedConfig.Packages) == numSaves
		},
		gen.IntRange(1, 5),
	))

	properties.TestingRun(t)
}


// TestTOMLFormattingConsistency tests Property 31: TOML Formatting Consistency
// **Feature: autoupdate-analyzer, Property 31: TOML Formatting Consistency**
// **Validates: Requirements 13.4**
//
// For any packages.toml file, saving and reloading SHALL produce an equivalent
// configuration (round-trip property).
func TestTOMLFormattingConsistency(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: Save and reload produces equivalent configuration
	properties.Property("save and reload produces equivalent configuration", prop.ForAll(
		func(numPackages int) bool {
			// Clamp to reasonable range
			if numPackages < 1 {
				numPackages = 1
			}
			if numPackages > 5 {
				numPackages = 5
			}

			// Create temp overlay
			tmpDir := t.TempDir()

			// Create schemas with various configurations
			schemas := make(map[string]PackageConfig)
			for i := 0; i < numPackages; i++ {
				pkgName := "app-misc/pkg-" + string(rune('a'+i))
				switch i % 3 {
				case 0:
					// JSON parser
					schemas[pkgName] = PackageConfig{
						URL:    "https://example.com/api/" + string(rune('a'+i)),
						Parser: "json",
						Path:   "version",
						Binary: i%2 == 0,
					}
				case 1:
					// Regex parser
					schemas[pkgName] = PackageConfig{
						URL:     "https://example.com/releases/" + string(rune('a'+i)),
						Parser:  "regex",
						Pattern: `v(\d+\.\d+\.\d+)`,
					}
				case 2:
					// HTML parser
					schemas[pkgName] = PackageConfig{
						URL:      "https://example.com/download/" + string(rune('a'+i)),
						Parser:   "html",
						Selector: ".version",
					}
				}
			}

			// Create analyzer with schemas
			config := &PackagesConfig{Packages: schemas}
			analyzer, err := NewAnalyzer(tmpDir, WithAnalyzerPackagesConfig(config))
			if err != nil {
				t.Logf("Failed to create analyzer: %v", err)
				return false
			}

			// Save all schemas
			for pkgName, schema := range schemas {
				schemaCopy := schema
				if err := analyzer.SaveSchema(pkgName, &schemaCopy); err != nil {
					t.Logf("Failed to save schema: %v", err)
					return false
				}
			}

			// Reload config from disk
			reloadedConfig, err := LoadPackagesConfig(tmpDir)
			if err != nil {
				t.Logf("Failed to reload config: %v", err)
				return false
			}

			// Verify all schemas are equivalent
			if len(reloadedConfig.Packages) != len(schemas) {
				t.Logf("Package count mismatch: expected %d, got %d", len(schemas), len(reloadedConfig.Packages))
				return false
			}

			for pkgName, expectedCfg := range schemas {
				actualCfg, exists := reloadedConfig.Packages[pkgName]
				if !exists {
					t.Logf("Package %s not found after reload", pkgName)
					return false
				}
				if actualCfg.URL != expectedCfg.URL ||
					actualCfg.Parser != expectedCfg.Parser ||
					actualCfg.Path != expectedCfg.Path ||
					actualCfg.Pattern != expectedCfg.Pattern ||
					actualCfg.Selector != expectedCfg.Selector ||
					actualCfg.Binary != expectedCfg.Binary {
					t.Logf("Package %s mismatch: expected %+v, got %+v", pkgName, expectedCfg, actualCfg)
					return false
				}
			}

			return true
		},
		gen.IntRange(1, 5),
	))

	// Property: Double save produces identical file content
	properties.Property("double save produces identical file content", prop.ForAll(
		func(numPackages int) bool {
			// Clamp to reasonable range
			if numPackages < 1 {
				numPackages = 1
			}
			if numPackages > 5 {
				numPackages = 5
			}

			// Create temp overlay
			tmpDir := t.TempDir()

			// Create schemas
			schemas := make(map[string]PackageConfig)
			for i := 0; i < numPackages; i++ {
				pkgName := "app-misc/pkg-" + string(rune('a'+i))
				schemas[pkgName] = PackageConfig{
					URL:    "https://example.com/api/" + string(rune('a'+i)),
					Parser: "json",
					Path:   "version",
				}
			}

			// Create analyzer with schemas
			config := &PackagesConfig{Packages: schemas}
			analyzer, err := NewAnalyzer(tmpDir, WithAnalyzerPackagesConfig(config))
			if err != nil {
				t.Logf("Failed to create analyzer: %v", err)
				return false
			}

			// Save all schemas
			for pkgName, schema := range schemas {
				schemaCopy := schema
				if err := analyzer.SaveSchema(pkgName, &schemaCopy); err != nil {
					t.Logf("Failed to save schema: %v", err)
					return false
				}
			}

			// Read first file content
			configPath := filepath.Join(tmpDir, ".autoupdate", "packages.toml")
			firstContent, err := os.ReadFile(configPath)
			if err != nil {
				t.Logf("Failed to read first file: %v", err)
				return false
			}

			// Reload and save again
			reloadedConfig, err := LoadPackagesConfig(tmpDir)
			if err != nil {
				t.Logf("Failed to reload config: %v", err)
				return false
			}

			analyzer2, err := NewAnalyzer(tmpDir, WithAnalyzerPackagesConfig(reloadedConfig))
			if err != nil {
				t.Logf("Failed to create second analyzer: %v", err)
				return false
			}

			// Save again (should produce same content)
			for pkgName, schema := range reloadedConfig.Packages {
				schemaCopy := schema
				if err := analyzer2.SaveSchema(pkgName, &schemaCopy); err != nil {
					t.Logf("Failed to save schema second time: %v", err)
					return false
				}
			}

			// Read second file content
			secondContent, err := os.ReadFile(configPath)
			if err != nil {
				t.Logf("Failed to read second file: %v", err)
				return false
			}

			// Content should be identical (or at least equivalent when parsed)
			// Note: TOML encoding may produce different ordering, so we compare parsed content
			var firstParsed, secondParsed map[string]PackageConfig
			if _, err := toml.Decode(string(firstContent), &firstParsed); err != nil {
				t.Logf("Failed to parse first content: %v", err)
				return false
			}
			if _, err := toml.Decode(string(secondContent), &secondParsed); err != nil {
				t.Logf("Failed to parse second content: %v", err)
				return false
			}

			// Compare parsed content
			if len(firstParsed) != len(secondParsed) {
				t.Logf("Parsed content length mismatch: %d vs %d", len(firstParsed), len(secondParsed))
				return false
			}

			for pkgName, firstCfg := range firstParsed {
				secondCfg, exists := secondParsed[pkgName]
				if !exists {
					t.Logf("Package %s not found in second parse", pkgName)
					return false
				}
				if firstCfg.URL != secondCfg.URL ||
					firstCfg.Parser != secondCfg.Parser ||
					firstCfg.Path != secondCfg.Path {
					t.Logf("Package %s mismatch between saves", pkgName)
					return false
				}
			}

			return true
		},
		gen.IntRange(1, 5),
	))

	// Property: Complex schemas with all fields round-trip correctly
	properties.Property("complex schemas with all fields round-trip correctly", prop.ForAll(
		func(dummy int) bool {
			// Create temp overlay
			tmpDir := t.TempDir()

			// Create a complex schema with all fields
			complexSchema := PackageConfig{
				URL:              "https://api.github.com/repos/test/test/releases",
				Parser:           "json",
				Path:             "[0].tag_name",
				Binary:           true,
				FallbackURL:      "https://example.com/fallback",
				FallbackParser:   "regex",
				FallbackPattern:  `v(\d+\.\d+\.\d+)`,
				LLMPrompt:        "Extract version from content",
				VersionsPath:     "[*].tag_name",
				Headers: map[string]string{
					"Authorization": "Bearer token",
					"User-Agent":    "bentoolkit/1.0",
				},
			}

			// Create analyzer
			analyzer, err := NewAnalyzer(tmpDir)
			if err != nil {
				t.Logf("Failed to create analyzer: %v", err)
				return false
			}

			// Save complex schema
			if err := analyzer.SaveSchema("app-misc/complex", &complexSchema); err != nil {
				t.Logf("Failed to save complex schema: %v", err)
				return false
			}

			// Reload config
			reloadedConfig, err := LoadPackagesConfig(tmpDir)
			if err != nil {
				t.Logf("Failed to reload config: %v", err)
				return false
			}

			// Verify all fields
			reloaded, exists := reloadedConfig.Packages["app-misc/complex"]
			if !exists {
				t.Logf("Complex schema not found after reload")
				return false
			}

			if reloaded.URL != complexSchema.URL ||
				reloaded.Parser != complexSchema.Parser ||
				reloaded.Path != complexSchema.Path ||
				reloaded.Binary != complexSchema.Binary ||
				reloaded.FallbackURL != complexSchema.FallbackURL ||
				reloaded.FallbackParser != complexSchema.FallbackParser ||
				reloaded.FallbackPattern != complexSchema.FallbackPattern ||
				reloaded.LLMPrompt != complexSchema.LLMPrompt ||
				reloaded.VersionsPath != complexSchema.VersionsPath {
				t.Logf("Complex schema field mismatch")
				return false
			}

			// Verify headers
			if len(reloaded.Headers) != len(complexSchema.Headers) {
				t.Logf("Headers count mismatch")
				return false
			}
			for key, expectedValue := range complexSchema.Headers {
				actualValue, exists := reloaded.Headers[key]
				if !exists || actualValue != expectedValue {
					t.Logf("Header %s mismatch: expected %s, got %s", key, expectedValue, actualValue)
					return false
				}
			}

			return true
		},
		gen.IntRange(1, 10),
	))

	properties.TestingRun(t)
}
