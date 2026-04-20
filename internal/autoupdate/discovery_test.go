package autoupdate

import (
	"strings"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

// =============================================================================
// Test Data Generators for Discovery
// =============================================================================

// genGitHubURL generates valid GitHub repository URLs
func genGitHubURL() gopter.Gen {
	return gen.OneConstOf(
		"https://github.com/owner/repo",
		"https://github.com/example/project",
		"https://github.com/user/package",
		"https://github.com/org/tool",
	)
}

// genGitHubSrcURI generates valid GitHub archive URLs
func genGitHubSrcURI() gopter.Gen {
	return gen.OneConstOf(
		"https://github.com/owner/repo/archive/v1.0.0.tar.gz",
		"https://github.com/example/project/archive/refs/tags/v2.0.0.tar.gz",
		"https://github.com/user/package/releases/download/v1.0.0/package-1.0.0.tar.gz",
	)
}

// genPyPIURL generates valid PyPI project URLs
func genPyPIURL() gopter.Gen {
	return gen.OneConstOf(
		"https://pypi.org/project/requests",
		"https://pypi.org/project/flask",
		"https://pypi.io/project/django",
		"https://pypi.python.org/project/numpy",
	)
}

// genPyPISrcURI generates valid PyPI/pythonhosted URLs
func genPyPISrcURI() gopter.Gen { //nolint:unused // PBT helper
	return gen.OneConstOf(
		"https://files.pythonhosted.org/packages/source/r/requests/requests-2.28.0.tar.gz",
		"https://files.pythonhosted.org/packages/source/f/flask/flask-2.0.0.tar.gz",
	)
}

// genNPMURL generates valid npm package URLs
func genNPMURL() gopter.Gen {
	return gen.OneConstOf(
		"https://www.npmjs.com/package/typescript",
		"https://npmjs.org/package/express",
		"https://registry.npmjs.org/lodash",
	)
}

// genCratesURL generates valid crates.io URLs
func genCratesURL() gopter.Gen {
	return gen.OneConstOf(
		"https://crates.io/crates/serde",
		"https://crates.io/crates/tokio",
		"https://crates.io/crates/clap",
	)
}

// genPythonDependency generates Python-related dependencies
func genPythonDependency() gopter.Gen {
	return gen.OneConstOf(
		"dev-python/requests",
		"dev-python/flask",
		"dev-python/setuptools",
		"python-exec",
	)
}

// genNodeDependency generates Node.js-related dependencies
func genNodeDependency() gopter.Gen {
	return gen.OneConstOf(
		"net-libs/nodejs",
		"dev-nodejs/typescript",
		"dev-nodejs/npm",
	)
}

// genRustDependency generates Rust-related dependencies
func genRustDependency() gopter.Gen {
	return gen.OneConstOf(
		"dev-lang/rust",
		"virtual/rust",
		"dev-rust/cargo",
	)
}

// =============================================================================
// Property-Based Tests
// =============================================================================

// TestEcosystemDetection tests Property 3: Ecosystem Detection
// **Feature: autoupdate-analyzer, Property 3: Ecosystem Detection**
// **Validates: Requirements 2.2, 2.3, 2.4, 2.5**
//
// For any ebuild with ecosystem-specific indicators (GitHub URL, Python dependencies,
// npm SRC_URI, Rust dependencies), the corresponding API (GitHub releases, PyPI,
// npm registry, crates.io) SHALL be included in the discovered data sources.
func TestEcosystemDetection(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: GitHub URL in HOMEPAGE results in GitHub API source
	properties.Property("GitHub HOMEPAGE results in GitHub API source", prop.ForAll(
		func(githubURL string) bool {
			meta := &EbuildMetadata{
				Package:  "app-misc/test",
				Homepage: githubURL,
			}

			sources := DiscoverDataSources(meta, "")

			// Should have at least one GitHub source
			for _, source := range sources {
				if source.Type == "github" {
					return strings.Contains(source.URL, "api.github.com") &&
						strings.Contains(source.URL, "/releases")
				}
			}
			return false
		},
		genGitHubURL(),
	))

	// Property: GitHub URL in SRC_URI results in GitHub API source
	properties.Property("GitHub SRC_URI results in GitHub API source", prop.ForAll(
		func(githubSrcURI string) bool {
			meta := &EbuildMetadata{
				Package: "app-misc/test",
				SrcURI:  githubSrcURI,
			}

			sources := DiscoverDataSources(meta, "")

			// Should have at least one GitHub source
			for _, source := range sources {
				if source.Type == "github" {
					return strings.Contains(source.URL, "api.github.com") &&
						strings.Contains(source.URL, "/releases")
				}
			}
			return false
		},
		genGitHubSrcURI(),
	))

	// Property: PyPI URL in HOMEPAGE results in PyPI API source
	properties.Property("PyPI HOMEPAGE results in PyPI API source", prop.ForAll(
		func(pypiURL string) bool {
			meta := &EbuildMetadata{
				Package:  "dev-python/test",
				Homepage: pypiURL,
			}

			sources := DiscoverDataSources(meta, "")

			// Should have at least one PyPI source
			for _, source := range sources {
				if source.Type == "pypi" {
					return strings.Contains(source.URL, "pypi.org/pypi/") &&
						strings.HasSuffix(source.URL, "/json")
				}
			}
			return false
		},
		genPyPIURL(),
	))

	// Property: Python dependencies result in PyPI API source for dev-python packages
	properties.Property("Python dependencies result in PyPI API source", prop.ForAll(
		func(pythonDep string) bool {
			meta := &EbuildMetadata{
				Package:      "dev-python/mypackage",
				Homepage:     "https://example.com",
				Dependencies: []string{pythonDep},
			}

			sources := DiscoverDataSources(meta, "")

			// Should have at least one PyPI source
			for _, source := range sources {
				if source.Type == "pypi" {
					return strings.Contains(source.URL, "pypi.org/pypi/")
				}
			}
			return false
		},
		genPythonDependency(),
	))

	// Property: npm URL in HOMEPAGE results in npm registry source
	properties.Property("npm HOMEPAGE results in npm registry source", prop.ForAll(
		func(npmURL string) bool {
			meta := &EbuildMetadata{
				Package:  "dev-nodejs/test",
				Homepage: npmURL,
			}

			sources := DiscoverDataSources(meta, "")

			// Should have at least one npm source
			for _, source := range sources {
				if source.Type == "npm" {
					return strings.Contains(source.URL, "registry.npmjs.org")
				}
			}
			return false
		},
		genNPMURL(),
	))

	// Property: Node.js dependencies result in npm registry source for dev-nodejs packages
	properties.Property("Node.js dependencies result in npm registry source", prop.ForAll(
		func(nodeDep string) bool {
			meta := &EbuildMetadata{
				Package:      "dev-nodejs/mypackage",
				Homepage:     "https://example.com",
				Dependencies: []string{nodeDep},
			}

			sources := DiscoverDataSources(meta, "")

			// Should have at least one npm source
			for _, source := range sources {
				if source.Type == "npm" {
					return strings.Contains(source.URL, "registry.npmjs.org")
				}
			}
			return false
		},
		genNodeDependency(),
	))

	// Property: crates.io URL in HOMEPAGE results in crates.io API source
	properties.Property("crates.io HOMEPAGE results in crates.io API source", prop.ForAll(
		func(cratesURL string) bool {
			meta := &EbuildMetadata{
				Package:  "dev-rust/test",
				Homepage: cratesURL,
			}

			sources := DiscoverDataSources(meta, "")

			// Should have at least one crates source
			for _, source := range sources {
				if source.Type == "crates" {
					return strings.Contains(source.URL, "crates.io/api/v1/crates/")
				}
			}
			return false
		},
		genCratesURL(),
	))

	// Property: Rust dependencies result in crates.io API source for dev-rust packages
	properties.Property("Rust dependencies result in crates.io API source", prop.ForAll(
		func(rustDep string) bool {
			meta := &EbuildMetadata{
				Package:      "dev-rust/mypackage",
				Homepage:     "https://example.com",
				Dependencies: []string{rustDep},
			}

			sources := DiscoverDataSources(meta, "")

			// Should have at least one crates source
			for _, source := range sources {
				if source.Type == "crates" {
					return strings.Contains(source.URL, "crates.io/api/v1/crates/")
				}
			}
			return false
		},
		genRustDependency(),
	))

	properties.TestingRun(t)
}

// TestDataSourcePriority tests Property 4: Data Source Priority
// **Feature: autoupdate-analyzer, Property 4: Data Source Priority**
// **Validates: Requirements 2.6**
//
// For any set of discovered data sources, they SHALL be ordered by priority:
// provided URL (if any) > GitHub releases > PyPI/npm/crates.io > homepage.
func TestDataSourcePriority(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: Provided URL always has highest priority
	properties.Property("provided URL has highest priority", prop.ForAll(
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
		gen.OneConstOf("https://example.com/api/version", "https://custom.api.com/releases"),
		genGitHubURL(),
	))

	// Property: GitHub has higher priority than homepage
	properties.Property("GitHub has higher priority than homepage", prop.ForAll(
		func(githubURL string) bool {
			meta := &EbuildMetadata{
				Package:  "app-misc/test",
				Homepage: githubURL,
			}

			sources := DiscoverDataSources(meta, "")

			// Find GitHub and homepage sources
			var githubIdx, homepageIdx = -1, -1
			for i, source := range sources {
				if source.Type == "github" {
					githubIdx = i
				}
				if source.Type == "homepage" {
					homepageIdx = i
				}
			}

			// GitHub should exist and be before homepage (if homepage exists)
			if githubIdx == -1 {
				return false
			}
			// Homepage might not exist if it's covered by GitHub
			if homepageIdx == -1 {
				return true
			}
			return githubIdx < homepageIdx
		},
		genGitHubURL(),
	))

	// Property: PyPI has higher priority than homepage
	properties.Property("PyPI has higher priority than homepage", prop.ForAll(
		func(pypiURL string) bool {
			meta := &EbuildMetadata{
				Package:  "dev-python/test",
				Homepage: pypiURL,
			}

			sources := DiscoverDataSources(meta, "")

			// Find PyPI and homepage sources
			var pypiIdx, homepageIdx = -1, -1
			for i, source := range sources {
				if source.Type == "pypi" {
					pypiIdx = i
				}
				if source.Type == "homepage" {
					homepageIdx = i
				}
			}

			// PyPI should exist and be before homepage (if homepage exists)
			if pypiIdx == -1 {
				return false
			}
			// Homepage might not exist if it's covered by PyPI
			if homepageIdx == -1 {
				return true
			}
			return pypiIdx < homepageIdx
		},
		genPyPIURL(),
	))

	// Property: npm has higher priority than homepage
	properties.Property("npm has higher priority than homepage", prop.ForAll(
		func(npmURL string) bool {
			meta := &EbuildMetadata{
				Package:  "dev-nodejs/test",
				Homepage: npmURL,
			}

			sources := DiscoverDataSources(meta, "")

			// Find npm and homepage sources
			var npmIdx, homepageIdx = -1, -1
			for i, source := range sources {
				if source.Type == "npm" {
					npmIdx = i
				}
				if source.Type == "homepage" {
					homepageIdx = i
				}
			}

			// npm should exist and be before homepage (if homepage exists)
			if npmIdx == -1 {
				return false
			}
			// Homepage might not exist if it's covered by npm
			if homepageIdx == -1 {
				return true
			}
			return npmIdx < homepageIdx
		},
		genNPMURL(),
	))

	// Property: crates.io has higher priority than homepage
	properties.Property("crates.io has higher priority than homepage", prop.ForAll(
		func(cratesURL string) bool {
			meta := &EbuildMetadata{
				Package:  "dev-rust/test",
				Homepage: cratesURL,
			}

			sources := DiscoverDataSources(meta, "")

			// Find crates and homepage sources
			var cratesIdx, homepageIdx = -1, -1
			for i, source := range sources {
				if source.Type == "crates" {
					cratesIdx = i
				}
				if source.Type == "homepage" {
					homepageIdx = i
				}
			}

			// crates should exist and be before homepage (if homepage exists)
			if cratesIdx == -1 {
				return false
			}
			// Homepage might not exist if it's covered by crates
			if homepageIdx == -1 {
				return true
			}
			return cratesIdx < homepageIdx
		},
		genCratesURL(),
	))

	// Property: Sources are sorted by priority (ascending)
	properties.Property("sources are sorted by priority", prop.ForAll(
		func(providedURL, githubURL string) bool {
			meta := &EbuildMetadata{
				Package:  "app-misc/test",
				Homepage: githubURL,
			}

			sources := DiscoverDataSources(meta, providedURL)

			// Check that priorities are in ascending order
			for i := 1; i < len(sources); i++ {
				if sources[i].Priority < sources[i-1].Priority {
					return false
				}
			}
			return true
		},
		gen.OneConstOf("https://example.com/api/version", "https://custom.api.com/releases"),
		genGitHubURL(),
	))

	properties.TestingRun(t)
}

// =============================================================================
// Unit Tests - DiscoverDataSources
// =============================================================================

// TestDiscoverDataSourcesBasic tests basic data source discovery
func TestDiscoverDataSourcesBasic(t *testing.T) {
	meta := &EbuildMetadata{
		Package:  "app-misc/hello",
		Homepage: "https://github.com/example/hello",
	}

	sources := DiscoverDataSources(meta, "")

	if len(sources) == 0 {
		t.Fatal("Expected at least one data source")
	}

	// Should have GitHub source
	hasGitHub := false
	for _, source := range sources {
		if source.Type == "github" {
			hasGitHub = true
			if !strings.Contains(source.URL, "api.github.com") {
				t.Errorf("Expected GitHub API URL, got %q", source.URL)
			}
		}
	}
	if !hasGitHub {
		t.Error("Expected GitHub source")
	}
}

// TestDiscoverDataSourcesProvidedURL tests that provided URL has highest priority
func TestDiscoverDataSourcesProvidedURL(t *testing.T) {
	meta := &EbuildMetadata{
		Package:  "app-misc/hello",
		Homepage: "https://github.com/example/hello",
	}

	providedURL := "https://custom.api.com/version"
	sources := DiscoverDataSources(meta, providedURL)

	if len(sources) == 0 {
		t.Fatal("Expected at least one data source")
	}

	// First source should be the provided URL
	if sources[0].Type != "provided" {
		t.Errorf("Expected first source to be 'provided', got %q", sources[0].Type)
	}
	if sources[0].URL != providedURL {
		t.Errorf("Expected URL %q, got %q", providedURL, sources[0].URL)
	}
	if sources[0].Priority != PriorityProvided {
		t.Errorf("Expected priority %d, got %d", PriorityProvided, sources[0].Priority)
	}
}

// TestDiscoverDataSourcesPyPI tests PyPI source discovery
func TestDiscoverDataSourcesPyPI(t *testing.T) {
	testCases := []struct {
		name     string
		meta     *EbuildMetadata
		expected string
	}{
		{
			name: "PyPI homepage",
			meta: &EbuildMetadata{
				Package:  "dev-python/requests",
				Homepage: "https://pypi.org/project/requests",
			},
			expected: "https://pypi.org/pypi/requests/json",
		},
		{
			name: "Python dependencies",
			meta: &EbuildMetadata{
				Package:      "dev-python/mypackage",
				Homepage:     "https://example.com",
				Dependencies: []string{"dev-python/setuptools"},
			},
			expected: "https://pypi.org/pypi/mypackage/json",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			sources := DiscoverDataSources(tc.meta, "")

			hasPyPI := false
			for _, source := range sources {
				if source.Type == "pypi" {
					hasPyPI = true
					if source.URL != tc.expected {
						t.Errorf("Expected URL %q, got %q", tc.expected, source.URL)
					}
				}
			}
			if !hasPyPI {
				t.Error("Expected PyPI source")
			}
		})
	}
}

// TestDiscoverDataSourcesNPM tests npm source discovery
func TestDiscoverDataSourcesNPM(t *testing.T) {
	testCases := []struct {
		name     string
		meta     *EbuildMetadata
		expected string
	}{
		{
			name: "npm homepage",
			meta: &EbuildMetadata{
				Package:  "dev-nodejs/typescript",
				Homepage: "https://www.npmjs.com/package/typescript",
			},
			expected: "https://registry.npmjs.org/typescript",
		},
		{
			name: "Node.js dependencies",
			meta: &EbuildMetadata{
				Package:      "dev-nodejs/mypackage",
				Homepage:     "https://example.com",
				Dependencies: []string{"net-libs/nodejs"},
			},
			expected: "https://registry.npmjs.org/mypackage",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			sources := DiscoverDataSources(tc.meta, "")

			hasNPM := false
			for _, source := range sources {
				if source.Type == "npm" {
					hasNPM = true
					if source.URL != tc.expected {
						t.Errorf("Expected URL %q, got %q", tc.expected, source.URL)
					}
				}
			}
			if !hasNPM {
				t.Error("Expected npm source")
			}
		})
	}
}

// TestDiscoverDataSourcesCrates tests crates.io source discovery
func TestDiscoverDataSourcesCrates(t *testing.T) {
	testCases := []struct {
		name     string
		meta     *EbuildMetadata
		expected string
	}{
		{
			name: "crates.io homepage",
			meta: &EbuildMetadata{
				Package:  "dev-rust/serde",
				Homepage: "https://crates.io/crates/serde",
			},
			expected: "https://crates.io/api/v1/crates/serde",
		},
		{
			name: "Rust dependencies",
			meta: &EbuildMetadata{
				Package:      "dev-rust/mypackage",
				Homepage:     "https://example.com",
				Dependencies: []string{"dev-lang/rust"},
			},
			expected: "https://crates.io/api/v1/crates/mypackage",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			sources := DiscoverDataSources(tc.meta, "")

			hasCrates := false
			for _, source := range sources {
				if source.Type == "crates" {
					hasCrates = true
					if source.URL != tc.expected {
						t.Errorf("Expected URL %q, got %q", tc.expected, source.URL)
					}
				}
			}
			if !hasCrates {
				t.Error("Expected crates.io source")
			}
		})
	}
}

// TestDiscoverDataSourcesHomepageFallback tests homepage as fallback
func TestDiscoverDataSourcesHomepageFallback(t *testing.T) {
	meta := &EbuildMetadata{
		Package:  "app-misc/hello",
		Homepage: "https://example.com/hello",
	}

	sources := DiscoverDataSources(meta, "")

	// Should have homepage as fallback
	hasHomepage := false
	for _, source := range sources {
		if source.Type == "homepage" {
			hasHomepage = true
			if source.URL != meta.Homepage {
				t.Errorf("Expected URL %q, got %q", meta.Homepage, source.URL)
			}
			if source.Priority != PriorityHomepage {
				t.Errorf("Expected priority %d, got %d", PriorityHomepage, source.Priority)
			}
		}
	}
	if !hasHomepage {
		t.Error("Expected homepage source as fallback")
	}
}

// TestDiscoverDataSourcesNoDuplicateHomepage tests that homepage is not duplicated
func TestDiscoverDataSourcesNoDuplicateHomepage(t *testing.T) {
	meta := &EbuildMetadata{
		Package:  "app-misc/hello",
		Homepage: "https://github.com/example/hello",
	}

	sources := DiscoverDataSources(meta, "")

	// Should not have homepage source since it's covered by GitHub
	for _, source := range sources {
		if source.Type == "homepage" {
			t.Error("Homepage should not be added when covered by GitHub")
		}
	}
}

// TestDiscoverDataSourcesContentType tests content type detection
func TestDiscoverDataSourcesContentType(t *testing.T) {
	testCases := []struct {
		name     string
		url      string
		expected string
	}{
		{"GitHub API", "https://api.github.com/repos/owner/repo/releases", ContentTypeJSON},
		{"PyPI API", "https://pypi.org/pypi/requests/json", ContentTypeJSON},
		{"npm registry", "https://registry.npmjs.org/typescript", ContentTypeJSON},
		{"crates.io API", "https://crates.io/api/v1/crates/serde", ContentTypeJSON},
		{"Generic URL", "https://example.com/releases", ContentTypeHTML},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := detectContentType(tc.url)
			if result != tc.expected {
				t.Errorf("Expected %q, got %q", tc.expected, result)
			}
		})
	}
}
