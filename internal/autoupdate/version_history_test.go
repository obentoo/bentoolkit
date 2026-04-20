package autoupdate

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

// =============================================================================
// Property-Based Tests
// =============================================================================

// TestVersionHistoryConfiguration tests Property 22: Version History Configuration
// **Feature: autoupdate-analyzer, Property 22: Version History Configuration**
// **Validates: Requirements 9.1, 9.2**
// For any data source that provides version history (GitHub releases, PyPI),
// the generated schema SHALL include versions_path or versions_selector field.
func TestVersionHistoryConfiguration(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: JSON version history extraction with versions_path
	properties.Property("JSON extractor extracts versions using versions_path", prop.ForAll(
		func(versions []string) bool {
			if len(versions) == 0 {
				return true // Skip empty arrays
			}

			// Create JSON array with versions
			data := make([]map[string]interface{}, len(versions))
			for i, v := range versions {
				data[i] = map[string]interface{}{"tag_name": v}
			}
			content, err := json.Marshal(data)
			if err != nil {
				t.Logf("Failed to marshal JSON: %v", err)
				return false
			}

			// Create config with versions_path
			cfg := &PackageConfig{
				Parser:       "json",
				Path:         "[0].tag_name",
				VersionsPath: "[*].tag_name",
			}

			// Verify HasVersionHistoryConfig returns true
			if !HasVersionHistoryConfig(cfg) {
				t.Log("HasVersionHistoryConfig returned false for config with VersionsPath")
				return false
			}

			// Extract version history
			extracted, err := ExtractVersionHistory(content, cfg)
			if err != nil {
				t.Logf("ExtractVersionHistory failed: %v", err)
				return false
			}

			// Verify we got versions
			if len(extracted) == 0 {
				t.Log("No versions extracted")
				return false
			}

			// Verify first version matches
			if extracted[0] != versions[0] {
				t.Logf("First version mismatch: expected %q, got %q", versions[0], extracted[0])
				return false
			}

			return true
		},
		gen.SliceOfN(5, genVersion()),
	))

	// Property: HTML version history extraction with versions_selector
	properties.Property("HTML extractor extracts versions using versions_selector", prop.ForAll(
		func(versions []string) bool {
			if len(versions) == 0 {
				return true // Skip empty arrays
			}

			// Create HTML with version elements
			html := "<html><body>"
			for _, v := range versions {
				html += fmt.Sprintf(`<span class="version">%s</span>`, v)
			}
			html += "</body></html>"

			// Create config with versions_selector
			cfg := &PackageConfig{
				Parser:           "html",
				Selector:         ".version",
				VersionsSelector: ".version",
			}

			// Verify HasVersionHistoryConfig returns true
			if !HasVersionHistoryConfig(cfg) {
				t.Log("HasVersionHistoryConfig returned false for config with VersionsSelector")
				return false
			}

			// Extract version history
			extracted, err := ExtractVersionHistory([]byte(html), cfg)
			if err != nil {
				t.Logf("ExtractVersionHistory failed: %v", err)
				return false
			}

			// Verify we got versions
			if len(extracted) == 0 {
				t.Log("No versions extracted")
				return false
			}

			// Verify first version matches
			if extracted[0] != versions[0] {
				t.Logf("First version mismatch: expected %q, got %q", versions[0], extracted[0])
				return false
			}

			return true
		},
		gen.SliceOfN(5, genVersion()),
	))

	// Property: Config without version history returns nil extractor
	properties.Property("Config without version history fields returns nil extractor", prop.ForAll(
		func(url, path string) bool {
			cfg := &PackageConfig{
				URL:    url,
				Parser: "json",
				Path:   path,
				// No VersionsPath or VersionsSelector
			}

			// Verify HasVersionHistoryConfig returns false
			if HasVersionHistoryConfig(cfg) {
				t.Log("HasVersionHistoryConfig returned true for config without version history")
				return false
			}

			// Verify NewVersionHistoryExtractor returns nil
			extractor, err := NewVersionHistoryExtractor(cfg)
			if err != nil {
				t.Logf("NewVersionHistoryExtractor returned error: %v", err)
				return false
			}
			if extractor != nil {
				t.Log("NewVersionHistoryExtractor returned non-nil extractor")
				return false
			}

			return true
		},
		gen.RegexMatch(`^https://example\.com/api/[a-z]+$`),
		gen.RegexMatch(`^[a-z]+\.[a-z]+$`),
	))

	properties.TestingRun(t)
}

// =============================================================================
// Unit Tests - JSON Version History
// =============================================================================

// TestJSONVersionHistoryWildcardPath tests JSON extraction with [*].field path
func TestJSONVersionHistoryWildcardPath(t *testing.T) {
	content := []byte(`[
		{"tag_name": "v1.0.0"},
		{"tag_name": "v1.1.0"},
		{"tag_name": "v1.2.0"}
	]`)

	extractor := &JSONVersionHistoryExtractor{
		VersionsPath: "[*].tag_name",
	}

	versions, err := extractor.ExtractVersions(content)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	expected := []string{"v1.0.0", "v1.1.0", "v1.2.0"}
	if len(versions) != len(expected) {
		t.Fatalf("Expected %d versions, got %d", len(expected), len(versions))
	}

	for i, v := range expected {
		if versions[i] != v {
			t.Errorf("Version %d: expected %q, got %q", i, v, versions[i])
		}
	}
}

// TestJSONVersionHistoryDirectArray tests JSON extraction from direct array
func TestJSONVersionHistoryDirectArray(t *testing.T) {
	content := []byte(`["1.0.0", "1.1.0", "1.2.0"]`)

	extractor := &JSONVersionHistoryExtractor{
		VersionsPath: "[*]",
	}

	versions, err := extractor.ExtractVersions(content)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	expected := []string{"1.0.0", "1.1.0", "1.2.0"}
	if len(versions) != len(expected) {
		t.Fatalf("Expected %d versions, got %d", len(expected), len(versions))
	}

	for i, v := range expected {
		if versions[i] != v {
			t.Errorf("Version %d: expected %q, got %q", i, v, versions[i])
		}
	}
}

// TestJSONVersionHistoryNestedPath tests JSON extraction with nested path
func TestJSONVersionHistoryNestedPath(t *testing.T) {
	content := []byte(`{
		"releases": [
			{"version": "2.0.0"},
			{"version": "2.1.0"}
		]
	}`)

	extractor := &JSONVersionHistoryExtractor{
		VersionsPath: "releases",
	}

	// This should fail because releases is an array of objects, not strings
	_, err := extractor.ExtractVersions(content)
	if err == nil {
		t.Error("Expected error for array of objects without field path")
	}
}

// TestJSONVersionHistoryEmptyPath tests error on empty path
func TestJSONVersionHistoryEmptyPath(t *testing.T) {
	content := []byte(`["1.0.0"]`)

	extractor := &JSONVersionHistoryExtractor{
		VersionsPath: "",
	}

	_, err := extractor.ExtractVersions(content)
	if err == nil {
		t.Error("Expected error for empty path")
	}
}

// TestJSONVersionHistoryInvalidJSON tests error on invalid JSON
func TestJSONVersionHistoryInvalidJSON(t *testing.T) {
	content := []byte(`{invalid}`)

	extractor := &JSONVersionHistoryExtractor{
		VersionsPath: "[*].version",
	}

	_, err := extractor.ExtractVersions(content)
	if err == nil {
		t.Error("Expected error for invalid JSON")
	}
}

// TestJSONVersionHistoryNotArray tests error when path doesn't point to array
func TestJSONVersionHistoryNotArray(t *testing.T) {
	content := []byte(`{"version": "1.0.0"}`)

	extractor := &JSONVersionHistoryExtractor{
		VersionsPath: "[*].version",
	}

	_, err := extractor.ExtractVersions(content)
	if err == nil {
		t.Error("Expected error when data is not an array")
	}
}

// =============================================================================
// Unit Tests - HTML Version History
// =============================================================================

// TestHTMLVersionHistoryCSS tests HTML extraction with CSS selector
func TestHTMLVersionHistoryCSS(t *testing.T) {
	content := []byte(`
		<html>
		<body>
			<div class="releases">
				<span class="version">1.0.0</span>
				<span class="version">1.1.0</span>
				<span class="version">1.2.0</span>
			</div>
		</body>
		</html>
	`)

	extractor := &HTMLVersionHistoryExtractor{
		VersionsSelector: ".version",
	}

	versions, err := extractor.ExtractVersions(content)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	expected := []string{"1.0.0", "1.1.0", "1.2.0"}
	if len(versions) != len(expected) {
		t.Fatalf("Expected %d versions, got %d", len(expected), len(versions))
	}

	for i, v := range expected {
		if versions[i] != v {
			t.Errorf("Version %d: expected %q, got %q", i, v, versions[i])
		}
	}
}

// TestHTMLVersionHistoryCSSWithRegex tests HTML extraction with regex post-processing
func TestHTMLVersionHistoryCSSWithRegex(t *testing.T) {
	content := []byte(`
		<html>
		<body>
			<span class="release">Release v1.0.0</span>
			<span class="release">Release v1.1.0</span>
		</body>
		</html>
	`)

	extractor := &HTMLVersionHistoryExtractor{
		VersionsSelector: ".release",
		Regex:            `v(\d+\.\d+\.\d+)`,
	}

	versions, err := extractor.ExtractVersions(content)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	expected := []string{"1.0.0", "1.1.0"}
	if len(versions) != len(expected) {
		t.Fatalf("Expected %d versions, got %d", len(expected), len(versions))
	}

	for i, v := range expected {
		if versions[i] != v {
			t.Errorf("Version %d: expected %q, got %q", i, v, versions[i])
		}
	}
}

// TestHTMLVersionHistoryNoMatch tests error when selector doesn't match
func TestHTMLVersionHistoryNoMatch(t *testing.T) {
	content := []byte(`<html><body><p>No versions here</p></body></html>`)

	extractor := &HTMLVersionHistoryExtractor{
		VersionsSelector: ".version",
	}

	_, err := extractor.ExtractVersions(content)
	if err == nil {
		t.Error("Expected error when selector doesn't match")
	}
}

// TestHTMLVersionHistoryEmptySelector tests error on empty selector
func TestHTMLVersionHistoryEmptySelector(t *testing.T) {
	content := []byte(`<html><body></body></html>`)

	extractor := &HTMLVersionHistoryExtractor{
		VersionsSelector: "",
	}

	_, err := extractor.ExtractVersions(content)
	if err == nil {
		t.Error("Expected error for empty selector")
	}
}

// =============================================================================
// Unit Tests - XPath Version History
// =============================================================================

// TestXPathVersionHistory tests XPath extraction
func TestXPathVersionHistory(t *testing.T) {
	content := []byte(`
		<html>
		<body>
			<div class="releases">
				<span class="version">2.0.0</span>
				<span class="version">2.1.0</span>
			</div>
		</body>
		</html>
	`)

	extractor := &XPathVersionHistoryExtractor{
		VersionsXPath: "//span[@class='version']",
	}

	versions, err := extractor.ExtractVersions(content)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	expected := []string{"2.0.0", "2.1.0"}
	if len(versions) != len(expected) {
		t.Fatalf("Expected %d versions, got %d", len(expected), len(versions))
	}

	for i, v := range expected {
		if versions[i] != v {
			t.Errorf("Version %d: expected %q, got %q", i, v, versions[i])
		}
	}
}

// TestXPathVersionHistoryWithRegex tests XPath extraction with regex
func TestXPathVersionHistoryWithRegex(t *testing.T) {
	content := []byte(`
		<html>
		<body>
			<a href="#">Version 3.0.0</a>
			<a href="#">Version 3.1.0</a>
		</body>
		</html>
	`)

	extractor := &XPathVersionHistoryExtractor{
		VersionsXPath: "//a",
		Regex:         `(\d+\.\d+\.\d+)`,
	}

	versions, err := extractor.ExtractVersions(content)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	expected := []string{"3.0.0", "3.1.0"}
	if len(versions) != len(expected) {
		t.Fatalf("Expected %d versions, got %d", len(expected), len(versions))
	}

	for i, v := range expected {
		if versions[i] != v {
			t.Errorf("Version %d: expected %q, got %q", i, v, versions[i])
		}
	}
}

// TestXPathVersionHistoryNoMatch tests error when XPath doesn't match
func TestXPathVersionHistoryNoMatch(t *testing.T) {
	content := []byte(`<html><body><p>No versions</p></body></html>`)

	extractor := &XPathVersionHistoryExtractor{
		VersionsXPath: "//span[@class='version']",
	}

	_, err := extractor.ExtractVersions(content)
	if err == nil {
		t.Error("Expected error when XPath doesn't match")
	}
}

// TestXPathVersionHistoryEmptyXPath tests error on empty XPath
func TestXPathVersionHistoryEmptyXPath(t *testing.T) {
	content := []byte(`<html><body></body></html>`)

	extractor := &XPathVersionHistoryExtractor{
		VersionsXPath: "",
	}

	_, err := extractor.ExtractVersions(content)
	if err == nil {
		t.Error("Expected error for empty XPath")
	}
}

// =============================================================================
// Unit Tests - Factory Functions
// =============================================================================

// TestNewVersionHistoryExtractorJSON tests factory for JSON extractor
func TestNewVersionHistoryExtractorJSON(t *testing.T) {
	cfg := &PackageConfig{
		Parser:       "json",
		Path:         "[0].tag_name",
		VersionsPath: "[*].tag_name",
	}

	extractor, err := NewVersionHistoryExtractor(cfg)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if extractor == nil {
		t.Fatal("Expected non-nil extractor")
	}

	_, ok := extractor.(*JSONVersionHistoryExtractor)
	if !ok {
		t.Error("Expected JSONVersionHistoryExtractor type")
	}
}

// TestNewVersionHistoryExtractorHTML tests factory for HTML extractor
func TestNewVersionHistoryExtractorHTML(t *testing.T) {
	cfg := &PackageConfig{
		Parser:           "html",
		Selector:         ".version",
		VersionsSelector: ".version",
	}

	extractor, err := NewVersionHistoryExtractor(cfg)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if extractor == nil {
		t.Fatal("Expected non-nil extractor")
	}

	_, ok := extractor.(*HTMLVersionHistoryExtractor)
	if !ok {
		t.Error("Expected HTMLVersionHistoryExtractor type")
	}
}

// TestNewVersionHistoryExtractorNone tests factory when no version history configured
func TestNewVersionHistoryExtractorNone(t *testing.T) {
	cfg := &PackageConfig{
		Parser: "json",
		Path:   "version",
	}

	extractor, err := NewVersionHistoryExtractor(cfg)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if extractor != nil {
		t.Error("Expected nil extractor when no version history configured")
	}
}

// TestHasVersionHistoryConfig tests the HasVersionHistoryConfig function
func TestHasVersionHistoryConfig(t *testing.T) {
	tests := []struct {
		name     string
		cfg      *PackageConfig
		expected bool
	}{
		{
			name:     "nil config",
			cfg:      nil,
			expected: false,
		},
		{
			name: "no version history",
			cfg: &PackageConfig{
				Parser: "json",
				Path:   "version",
			},
			expected: false,
		},
		{
			name: "with versions_path",
			cfg: &PackageConfig{
				Parser:       "json",
				Path:         "[0].tag_name",
				VersionsPath: "[*].tag_name",
			},
			expected: true,
		},
		{
			name: "with versions_selector",
			cfg: &PackageConfig{
				Parser:           "html",
				Selector:         ".version",
				VersionsSelector: ".version",
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := HasVersionHistoryConfig(tt.cfg)
			if result != tt.expected {
				t.Errorf("Expected %v, got %v", tt.expected, result)
			}
		})
	}
}

// TestExtractVersionHistoryNoConfig tests ExtractVersionHistory with no config
func TestExtractVersionHistoryNoConfig(t *testing.T) {
	cfg := &PackageConfig{
		Parser: "json",
		Path:   "version",
	}

	versions, err := ExtractVersionHistory([]byte(`{"version": "1.0.0"}`), cfg)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if versions != nil {
		t.Error("Expected nil versions when no version history configured")
	}
}

// =============================================================================
// Property-Based Tests - Version History Limit
// =============================================================================

// TestVersionHistoryLimit tests Property 23: Version History Limit
// **Feature: autoupdate-analyzer, Property 23: Version History Limit**
// **Validates: Requirements 9.3**
// For any version history extraction, the result SHALL contain at most 10 versions.
func TestVersionHistoryLimit(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: JSON version history is limited to MaxVersionHistoryLimit
	properties.Property("JSON version history is limited to 10 versions", prop.ForAll(
		func(numVersions int) bool {
			// Generate between 1 and 50 versions
			numVersions = (numVersions % 50) + 1

			// Create JSON array with versions
			data := make([]map[string]interface{}, numVersions)
			for i := 0; i < numVersions; i++ {
				data[i] = map[string]interface{}{"tag_name": fmt.Sprintf("v%d.0.0", i+1)}
			}
			content, err := json.Marshal(data)
			if err != nil {
				t.Logf("Failed to marshal JSON: %v", err)
				return false
			}

			// Extract version history
			extractor := &JSONVersionHistoryExtractor{
				VersionsPath: "[*].tag_name",
			}
			versions, err := extractor.ExtractVersions(content)
			if err != nil {
				t.Logf("ExtractVersions failed: %v", err)
				return false
			}

			// Verify limit is respected
			if len(versions) > MaxVersionHistoryLimit {
				t.Logf("Got %d versions, expected at most %d", len(versions), MaxVersionHistoryLimit)
				return false
			}

			// Verify we got the expected number
			expectedCount := numVersions
			if expectedCount > MaxVersionHistoryLimit {
				expectedCount = MaxVersionHistoryLimit
			}
			if len(versions) != expectedCount {
				t.Logf("Got %d versions, expected %d", len(versions), expectedCount)
				return false
			}

			return true
		},
		gen.IntRange(1, 100),
	))

	// Property: HTML version history is limited to MaxVersionHistoryLimit
	properties.Property("HTML version history is limited to 10 versions", prop.ForAll(
		func(numVersions int) bool {
			// Generate between 1 and 50 versions
			numVersions = (numVersions % 50) + 1

			// Create HTML with version elements
			html := "<html><body>"
			for i := 0; i < numVersions; i++ {
				html += fmt.Sprintf(`<span class="version">v%d.0.0</span>`, i+1)
			}
			html += "</body></html>"

			// Extract version history
			extractor := &HTMLVersionHistoryExtractor{
				VersionsSelector: ".version",
			}
			versions, err := extractor.ExtractVersions([]byte(html))
			if err != nil {
				t.Logf("ExtractVersions failed: %v", err)
				return false
			}

			// Verify limit is respected
			if len(versions) > MaxVersionHistoryLimit {
				t.Logf("Got %d versions, expected at most %d", len(versions), MaxVersionHistoryLimit)
				return false
			}

			// Verify we got the expected number
			expectedCount := numVersions
			if expectedCount > MaxVersionHistoryLimit {
				expectedCount = MaxVersionHistoryLimit
			}
			if len(versions) != expectedCount {
				t.Logf("Got %d versions, expected %d", len(versions), expectedCount)
				return false
			}

			return true
		},
		gen.IntRange(1, 100),
	))

	// Property: XPath version history is limited to MaxVersionHistoryLimit
	properties.Property("XPath version history is limited to 10 versions", prop.ForAll(
		func(numVersions int) bool {
			// Generate between 1 and 50 versions
			numVersions = (numVersions % 50) + 1

			// Create HTML with version elements
			html := "<html><body>"
			for i := 0; i < numVersions; i++ {
				html += fmt.Sprintf(`<span class="ver">v%d.0.0</span>`, i+1)
			}
			html += "</body></html>"

			// Extract version history
			extractor := &XPathVersionHistoryExtractor{
				VersionsXPath: "//span[@class='ver']",
			}
			versions, err := extractor.ExtractVersions([]byte(html))
			if err != nil {
				t.Logf("ExtractVersions failed: %v", err)
				return false
			}

			// Verify limit is respected
			if len(versions) > MaxVersionHistoryLimit {
				t.Logf("Got %d versions, expected at most %d", len(versions), MaxVersionHistoryLimit)
				return false
			}

			// Verify we got the expected number
			expectedCount := numVersions
			if expectedCount > MaxVersionHistoryLimit {
				expectedCount = MaxVersionHistoryLimit
			}
			if len(versions) != expectedCount {
				t.Logf("Got %d versions, expected %d", len(versions), expectedCount)
				return false
			}

			return true
		},
		gen.IntRange(1, 100),
	))

	properties.TestingRun(t)
}

// =============================================================================
// Unit Tests - Version History Limit
// =============================================================================

// TestJSONVersionHistoryLimitExact tests exact limit behavior
func TestJSONVersionHistoryLimitExact(t *testing.T) {
	// Create JSON with exactly MaxVersionHistoryLimit versions
	data := make([]map[string]interface{}, MaxVersionHistoryLimit)
	for i := 0; i < MaxVersionHistoryLimit; i++ {
		data[i] = map[string]interface{}{"tag_name": fmt.Sprintf("v%d.0.0", i+1)}
	}
	content, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("Failed to marshal JSON: %v", err)
	}

	extractor := &JSONVersionHistoryExtractor{
		VersionsPath: "[*].tag_name",
	}
	versions, err := extractor.ExtractVersions(content)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if len(versions) != MaxVersionHistoryLimit {
		t.Errorf("Expected %d versions, got %d", MaxVersionHistoryLimit, len(versions))
	}
}

// TestJSONVersionHistoryLimitExceeded tests limit when exceeded
func TestJSONVersionHistoryLimitExceeded(t *testing.T) {
	// Create JSON with more than MaxVersionHistoryLimit versions
	numVersions := MaxVersionHistoryLimit + 5
	data := make([]map[string]interface{}, numVersions)
	for i := 0; i < numVersions; i++ {
		data[i] = map[string]interface{}{"tag_name": fmt.Sprintf("v%d.0.0", i+1)}
	}
	content, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("Failed to marshal JSON: %v", err)
	}

	extractor := &JSONVersionHistoryExtractor{
		VersionsPath: "[*].tag_name",
	}
	versions, err := extractor.ExtractVersions(content)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if len(versions) != MaxVersionHistoryLimit {
		t.Errorf("Expected %d versions (limit), got %d", MaxVersionHistoryLimit, len(versions))
	}

	// Verify we got the first MaxVersionHistoryLimit versions
	for i := 0; i < MaxVersionHistoryLimit; i++ {
		expected := fmt.Sprintf("v%d.0.0", i+1)
		if versions[i] != expected {
			t.Errorf("Version %d: expected %q, got %q", i, expected, versions[i])
		}
	}
}

// TestHTMLVersionHistoryLimitExceeded tests HTML limit when exceeded
func TestHTMLVersionHistoryLimitExceeded(t *testing.T) {
	// Create HTML with more than MaxVersionHistoryLimit versions
	numVersions := MaxVersionHistoryLimit + 5
	html := "<html><body>"
	for i := 0; i < numVersions; i++ {
		html += fmt.Sprintf(`<span class="version">v%d.0.0</span>`, i+1)
	}
	html += "</body></html>"

	extractor := &HTMLVersionHistoryExtractor{
		VersionsSelector: ".version",
	}
	versions, err := extractor.ExtractVersions([]byte(html))
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if len(versions) != MaxVersionHistoryLimit {
		t.Errorf("Expected %d versions (limit), got %d", MaxVersionHistoryLimit, len(versions))
	}
}

// TestXPathVersionHistoryLimitExceeded tests XPath limit when exceeded
func TestXPathVersionHistoryLimitExceeded(t *testing.T) {
	// Create HTML with more than MaxVersionHistoryLimit versions
	numVersions := MaxVersionHistoryLimit + 5
	html := "<html><body>"
	for i := 0; i < numVersions; i++ {
		html += fmt.Sprintf(`<span class="ver">v%d.0.0</span>`, i+1)
	}
	html += "</body></html>"

	extractor := &XPathVersionHistoryExtractor{
		VersionsXPath: "//span[@class='ver']",
	}
	versions, err := extractor.ExtractVersions([]byte(html))
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if len(versions) != MaxVersionHistoryLimit {
		t.Errorf("Expected %d versions (limit), got %d", MaxVersionHistoryLimit, len(versions))
	}
}

// TestMaxVersionHistoryLimitConstant verifies the constant value
func TestMaxVersionHistoryLimitConstant(t *testing.T) {
	// Per Requirement 9.3, the limit should be 10
	if MaxVersionHistoryLimit != 10 {
		t.Errorf("MaxVersionHistoryLimit should be 10, got %d", MaxVersionHistoryLimit)
	}
}
