// Package autoupdate provides data source discovery for ebuild autoupdate analysis.
package autoupdate

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// DataSource represents a candidate data source for version checking.
// It contains information about where to fetch version information and
// how to prioritize different sources.
type DataSource struct {
	// URL is the endpoint to query for version information
	URL string
	// Type identifies the source type: "github", "pypi", "npm", "crates", "homepage", "provided"
	Type string
	// Priority determines the order of sources (lower is higher priority)
	Priority int
	// ContentType is the expected content type (e.g., "application/json", "text/html")
	ContentType string
}

// Priority constants for data source ordering
const (
	// PriorityProvided is the highest priority for user-provided URLs
	PriorityProvided = 0
	// PriorityGitHub is the priority for GitHub releases API
	PriorityGitHub = 10
	// PriorityPyPI is the priority for PyPI API
	PriorityPyPI = 20
	// PriorityNPM is the priority for npm registry API
	PriorityNPM = 20
	// PriorityCrates is the priority for crates.io API
	PriorityCrates = 20
	// PriorityHomepage is the lowest priority for generic homepage scraping
	PriorityHomepage = 100
)

// Content type constants
const (
	ContentTypeJSON = "application/json"
	ContentTypeHTML = "text/html"
)

// Regular expressions for URL pattern matching
var (
	// githubURLRegex matches GitHub repository URLs
	githubURLRegex = regexp.MustCompile(`github\.com[/:]([^/]+)/([^/\s"'#?]+)`)
	// pypiURLRegex matches PyPI project URLs
	pypiURLRegex = regexp.MustCompile(`pypi\.(?:org|io|python\.org)/project/([^/\s"'#?]+)`)
	// pypiFilesRegex matches PyPI files URLs (pythonhosted.org)
	pypiFilesRegex = regexp.MustCompile(`files\.pythonhosted\.org/packages/.*?/([^/]+)-[\d]`)
	// npmURLRegex matches npm package URLs
	npmURLRegex = regexp.MustCompile(`(?:npmjs\.(?:org|com)|registry\.npmjs\.org)/(?:package/)?([^/\s"'#?]+)`)
	// cratesURLRegex matches crates.io URLs
	cratesURLRegex = regexp.MustCompile(`crates\.io/crates/([^/\s"'#?]+)`)
)

// DiscoverDataSources finds candidate URLs for version checking.
// It analyzes ebuild metadata and returns a prioritized list of data sources.
// If providedURL is non-empty, it is included as the highest priority source.
func DiscoverDataSources(meta *EbuildMetadata, providedURL string) []DataSource {
	var sources []DataSource

	// Add provided URL as highest priority if specified
	if providedURL != "" {
		sources = append(sources, DataSource{
			URL:         providedURL,
			Type:        "provided",
			Priority:    PriorityProvided,
			ContentType: detectContentType(providedURL),
		})
	}

	// Try to discover GitHub source
	if source := discoverGitHubSource(meta); source != nil {
		sources = append(sources, *source)
	}

	// Try to discover PyPI source
	if source := discoverPyPISource(meta); source != nil {
		sources = append(sources, *source)
	}

	// Try to discover npm source
	if source := discoverNPMSource(meta); source != nil {
		sources = append(sources, *source)
	}

	// Try to discover crates.io source
	if source := discoverCratesSource(meta); source != nil {
		sources = append(sources, *source)
	}

	// Add homepage as fallback if it's a valid URL
	if meta.Homepage != "" && isValidURL(meta.Homepage) {
		// Don't add homepage if it's already covered by a more specific source
		if !isURLCoveredBySource(meta.Homepage, sources) {
			sources = append(sources, DataSource{
				URL:         meta.Homepage,
				Type:        "homepage",
				Priority:    PriorityHomepage,
				ContentType: ContentTypeHTML,
			})
		}
	}

	// Sort by priority (lower is higher priority)
	sort.Slice(sources, func(i, j int) bool {
		return sources[i].Priority < sources[j].Priority
	})

	return sources
}

// discoverGitHubSource attempts to discover a GitHub releases API endpoint.
// It checks HOMEPAGE and SRC_URI for GitHub URLs and constructs the releases API URL.
func discoverGitHubSource(meta *EbuildMetadata) *DataSource {
	owner, repo, found := ExtractGitHubInfo(meta)
	if !found {
		return nil
	}

	// Construct GitHub releases API URL
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases", owner, repo)

	return &DataSource{
		URL:         apiURL,
		Type:        "github",
		Priority:    PriorityGitHub,
		ContentType: ContentTypeJSON,
	}
}

// discoverPyPISource attempts to discover a PyPI API endpoint.
// It checks HOMEPAGE, SRC_URI, and dependencies for PyPI indicators.
func discoverPyPISource(meta *EbuildMetadata) *DataSource {
	// Try to extract package name from PyPI URL in HOMEPAGE
	if matches := pypiURLRegex.FindStringSubmatch(meta.Homepage); matches != nil {
		pkgName := matches[1]
		return createPyPISource(pkgName)
	}

	// Try to extract package name from PyPI URL in SRC_URI
	if matches := pypiURLRegex.FindStringSubmatch(meta.SrcURI); matches != nil {
		pkgName := matches[1]
		return createPyPISource(pkgName)
	}

	// Try to extract package name from pythonhosted.org URL in SRC_URI
	if matches := pypiFilesRegex.FindStringSubmatch(meta.SrcURI); matches != nil {
		pkgName := matches[1]
		return createPyPISource(pkgName)
	}

	// Check dependencies for Python indicators
	hasPythonDep := false
	for _, dep := range meta.Dependencies {
		if pythonDepRegex.MatchString(dep) {
			hasPythonDep = true
			break
		}
	}

	// If we have Python dependencies but no PyPI URL, try to derive package name
	if hasPythonDep {
		// Try to extract package name from the package atom
		pkgName := extractPyPIPackageName(meta.Package)
		if pkgName != "" {
			return createPyPISource(pkgName)
		}
	}

	return nil
}

// createPyPISource creates a PyPI API data source for the given package name.
func createPyPISource(pkgName string) *DataSource {
	apiURL := fmt.Sprintf("https://pypi.org/pypi/%s/json", pkgName)
	return &DataSource{
		URL:         apiURL,
		Type:        "pypi",
		Priority:    PriorityPyPI,
		ContentType: ContentTypeJSON,
	}
}

// extractPyPIPackageName attempts to extract a PyPI package name from a Gentoo package atom.
// For example, "dev-python/requests" -> "requests"
func extractPyPIPackageName(pkg string) string {
	parts := strings.Split(pkg, "/")
	if len(parts) != 2 {
		return ""
	}

	// Only consider dev-python category
	if parts[0] != "dev-python" {
		return ""
	}

	return parts[1]
}

// discoverNPMSource attempts to discover an npm registry API endpoint.
// It checks HOMEPAGE, SRC_URI, and dependencies for npm indicators.
func discoverNPMSource(meta *EbuildMetadata) *DataSource {
	// Try to extract package name from npm URL in HOMEPAGE
	if matches := npmURLRegex.FindStringSubmatch(meta.Homepage); matches != nil {
		pkgName := matches[1]
		return createNPMSource(pkgName)
	}

	// Try to extract package name from npm URL in SRC_URI
	if matches := npmURLRegex.FindStringSubmatch(meta.SrcURI); matches != nil {
		pkgName := matches[1]
		return createNPMSource(pkgName)
	}

	// Check dependencies for Node.js indicators
	hasNodeDep := false
	for _, dep := range meta.Dependencies {
		if nodeDepRegex.MatchString(dep) {
			hasNodeDep = true
			break
		}
	}

	// If we have Node.js dependencies but no npm URL, try to derive package name
	if hasNodeDep {
		pkgName := extractNPMPackageName(meta.Package)
		if pkgName != "" {
			return createNPMSource(pkgName)
		}
	}

	return nil
}

// createNPMSource creates an npm registry API data source for the given package name.
func createNPMSource(pkgName string) *DataSource {
	apiURL := fmt.Sprintf("https://registry.npmjs.org/%s", pkgName)
	return &DataSource{
		URL:         apiURL,
		Type:        "npm",
		Priority:    PriorityNPM,
		ContentType: ContentTypeJSON,
	}
}

// extractNPMPackageName attempts to extract an npm package name from a Gentoo package atom.
// For example, "dev-nodejs/typescript" -> "typescript"
func extractNPMPackageName(pkg string) string {
	parts := strings.Split(pkg, "/")
	if len(parts) != 2 {
		return ""
	}

	// Only consider dev-nodejs category
	if parts[0] != "dev-nodejs" {
		return ""
	}

	return parts[1]
}

// discoverCratesSource attempts to discover a crates.io API endpoint.
// It checks HOMEPAGE, SRC_URI, and dependencies for Rust/crates.io indicators.
func discoverCratesSource(meta *EbuildMetadata) *DataSource {
	// Try to extract crate name from crates.io URL in HOMEPAGE
	if matches := cratesURLRegex.FindStringSubmatch(meta.Homepage); matches != nil {
		crateName := matches[1]
		return createCratesSource(crateName)
	}

	// Try to extract crate name from crates.io URL in SRC_URI
	if matches := cratesURLRegex.FindStringSubmatch(meta.SrcURI); matches != nil {
		crateName := matches[1]
		return createCratesSource(crateName)
	}

	// Check dependencies for Rust indicators
	hasRustDep := false
	for _, dep := range meta.Dependencies {
		if rustDepRegex.MatchString(dep) {
			hasRustDep = true
			break
		}
	}

	// If we have Rust dependencies but no crates.io URL, try to derive crate name
	if hasRustDep {
		crateName := extractCrateName(meta.Package)
		if crateName != "" {
			return createCratesSource(crateName)
		}
	}

	return nil
}

// createCratesSource creates a crates.io API data source for the given crate name.
func createCratesSource(crateName string) *DataSource {
	apiURL := fmt.Sprintf("https://crates.io/api/v1/crates/%s", crateName)
	return &DataSource{
		URL:         apiURL,
		Type:        "crates",
		Priority:    PriorityCrates,
		ContentType: ContentTypeJSON,
	}
}

// extractCrateName attempts to extract a crate name from a Gentoo package atom.
// For example, "dev-rust/serde" -> "serde"
func extractCrateName(pkg string) string {
	parts := strings.Split(pkg, "/")
	if len(parts) != 2 {
		return ""
	}

	// Only consider dev-rust category
	if parts[0] != "dev-rust" {
		return ""
	}

	return parts[1]
}

// detectContentType attempts to detect the expected content type from a URL.
// Returns ContentTypeJSON for known API endpoints, ContentTypeHTML otherwise.
func detectContentType(url string) string {
	// Check for known JSON API patterns
	jsonPatterns := []string{
		"api.github.com",
		"pypi.org/pypi/",
		"registry.npmjs.org",
		"crates.io/api/",
		".json",
	}

	for _, pattern := range jsonPatterns {
		if strings.Contains(url, pattern) {
			return ContentTypeJSON
		}
	}

	return ContentTypeHTML
}

// isValidURL checks if a string looks like a valid HTTP(S) URL.
func isValidURL(url string) bool {
	return strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://")
}

// isURLCoveredBySource checks if a URL is already covered by an existing source.
// This prevents adding duplicate sources (e.g., homepage that's already a GitHub URL).
func isURLCoveredBySource(url string, sources []DataSource) bool {
	for _, source := range sources {
		// Check if the URL matches the source type
		switch source.Type {
		case "github":
			if githubURLRegex.MatchString(url) {
				return true
			}
		case "pypi":
			if pypiURLRegex.MatchString(url) {
				return true
			}
		case "npm":
			if npmURLRegex.MatchString(url) {
				return true
			}
		case "crates":
			if cratesURLRegex.MatchString(url) {
				return true
			}
		}
	}
	return false
}
