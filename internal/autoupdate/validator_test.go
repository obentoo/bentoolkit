package autoupdate

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

// =============================================================================
// Property-Based Tests
// =============================================================================

// TestSchemaValidationFlow tests Property 16: Schema Validation Flow
// **Feature: autoupdate-analyzer, Property 16: Schema Validation Flow**
// **Validates: Requirements 12.1, 12.2, 12.3**
//
// For any generated schema, the analyzer SHALL:
// (1) test version extraction
// (2) compare with ebuild version
// (3) mark as validated if versions match
func TestSchemaValidationFlow(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: When extraction succeeds and versions match, validation is successful
	properties.Property("validation succeeds when extracted version matches ebuild version", prop.ForAll(
		func(version string) bool {
			// Create JSON content with the version
			content, err := json.Marshal(map[string]interface{}{
				"version": version,
			})
			if err != nil {
				t.Logf("Failed to marshal JSON: %v", err)
				return false
			}

			// Create schema for JSON extraction
			schema := &PackageConfig{
				Parser: "json",
				Path:   "version",
			}

			// Validate schema
			result := ValidateSchema(content, schema, version)

			// Check all three steps of validation flow:
			// 1. Version extraction succeeded (ExtractedVersion is set)
			// 2. Version comparison was performed (VersionsMatch is set)
			// 3. Validation marked as successful (Valid is true)
			return result.ExtractedVersion == version &&
				result.VersionsMatch == true &&
				result.Valid == true &&
				result.Error == nil
		},
		genVersion(),
	))

	// Property: When extraction succeeds but versions don't match, validation fails with mismatch error
	properties.Property("validation fails with mismatch when versions differ", prop.ForAll(
		func(extractedVersion, ebuildVersion string) bool {
			// Skip if versions happen to be equal
			if extractedVersion == ebuildVersion {
				return true
			}

			// Create JSON content with extracted version
			content, err := json.Marshal(map[string]interface{}{
				"version": extractedVersion,
			})
			if err != nil {
				t.Logf("Failed to marshal JSON: %v", err)
				return false
			}

			// Create schema for JSON extraction
			schema := &PackageConfig{
				Parser: "json",
				Path:   "version",
			}

			// Validate schema with different ebuild version
			result := ValidateSchema(content, schema, ebuildVersion)

			// Check validation flow:
			// 1. Version extraction succeeded
			// 2. Versions don't match
			// 3. Validation marked as failed
			return result.ExtractedVersion == extractedVersion &&
				result.EbuildVersion == ebuildVersion &&
				result.VersionsMatch == false &&
				result.Valid == false &&
				errors.Is(result.Error, ErrVersionMismatch)
		},
		genVersion(),
		genDifferentVersion(),
	))

	// Property: When extraction fails, validation fails with extraction error
	properties.Property("validation fails when extraction fails", prop.ForAll(
		func(ebuildVersion string) bool {
			// Create invalid JSON content
			content := []byte(`{invalid json}`)

			// Create schema for JSON extraction
			schema := &PackageConfig{
				Parser: "json",
				Path:   "version",
			}

			// Validate schema
			result := ValidateSchema(content, schema, ebuildVersion)

			// Check validation flow:
			// 1. Extraction failed
			// 2. Validation marked as failed
			return result.ExtractedVersion == "" &&
				result.Valid == false &&
				errors.Is(result.Error, ErrExtractionFailed)
		},
		genVersion(),
	))

	// Property: Version with 'v' prefix matches version without prefix
	properties.Property("version with v prefix matches version without prefix", prop.ForAll(
		func(version string) bool {
			versionWithPrefix := "v" + version

			// Create JSON content with prefixed version
			content, err := json.Marshal(map[string]interface{}{
				"tag_name": versionWithPrefix,
			})
			if err != nil {
				t.Logf("Failed to marshal JSON: %v", err)
				return false
			}

			// Create schema for JSON extraction
			schema := &PackageConfig{
				Parser: "json",
				Path:   "tag_name",
			}

			// Validate schema with non-prefixed ebuild version
			result := ValidateSchema(content, schema, version)

			// Versions should match after normalization
			return result.VersionsMatch == true && result.Valid == true
		},
		genVersion(),
	))

	properties.TestingRun(t)
}

// =============================================================================
// Unit Tests
// =============================================================================

// TestValidateSchemaSuccess tests successful schema validation
func TestValidateSchemaSuccess(t *testing.T) {
	content := []byte(`{"version": "1.2.3"}`)
	schema := &PackageConfig{
		Parser: "json",
		Path:   "version",
	}

	result := ValidateSchema(content, schema, "1.2.3")

	if !result.Valid {
		t.Errorf("Expected validation to succeed, got error: %v", result.Error)
	}
	if result.ExtractedVersion != "1.2.3" {
		t.Errorf("Expected extracted version '1.2.3', got %q", result.ExtractedVersion)
	}
	if !result.VersionsMatch {
		t.Error("Expected versions to match")
	}
}

// TestValidateSchemaVersionMismatch tests validation with version mismatch
func TestValidateSchemaVersionMismatch(t *testing.T) {
	content := []byte(`{"version": "2.0.0"}`)
	schema := &PackageConfig{
		Parser: "json",
		Path:   "version",
	}

	result := ValidateSchema(content, schema, "1.0.0")

	if result.Valid {
		t.Error("Expected validation to fail due to version mismatch")
	}
	if result.ExtractedVersion != "2.0.0" {
		t.Errorf("Expected extracted version '2.0.0', got %q", result.ExtractedVersion)
	}
	if result.VersionsMatch {
		t.Error("Expected versions not to match")
	}
	if !errors.Is(result.Error, ErrVersionMismatch) {
		t.Errorf("Expected ErrVersionMismatch, got %v", result.Error)
	}
}

// TestValidateSchemaExtractionFailed tests validation when extraction fails
func TestValidateSchemaExtractionFailed(t *testing.T) {
	content := []byte(`{"other": "value"}`)
	schema := &PackageConfig{
		Parser: "json",
		Path:   "version",
	}

	result := ValidateSchema(content, schema, "1.0.0")

	if result.Valid {
		t.Error("Expected validation to fail due to extraction failure")
	}
	if result.ExtractedVersion != "" {
		t.Errorf("Expected empty extracted version, got %q", result.ExtractedVersion)
	}
	if !errors.Is(result.Error, ErrExtractionFailed) {
		t.Errorf("Expected ErrExtractionFailed, got %v", result.Error)
	}
}

// TestValidateSchemaWithVPrefix tests validation with 'v' prefix in extracted version
func TestValidateSchemaWithVPrefix(t *testing.T) {
	content := []byte(`{"tag_name": "v1.2.3"}`)
	schema := &PackageConfig{
		Parser: "json",
		Path:   "tag_name",
	}

	result := ValidateSchema(content, schema, "1.2.3")

	if !result.Valid {
		t.Errorf("Expected validation to succeed with v prefix, got error: %v", result.Error)
	}
	if !result.VersionsMatch {
		t.Error("Expected versions to match after stripping v prefix")
	}
}

// TestValidateSchemaRegex tests validation with regex parser
func TestValidateSchemaRegex(t *testing.T) {
	content := []byte(`pkgver=3.1.4`)
	schema := &PackageConfig{
		Parser:  "regex",
		Pattern: `pkgver=([0-9.]+)`,
	}

	result := ValidateSchema(content, schema, "3.1.4")

	if !result.Valid {
		t.Errorf("Expected validation to succeed, got error: %v", result.Error)
	}
	if result.ExtractedVersion != "3.1.4" {
		t.Errorf("Expected extracted version '3.1.4', got %q", result.ExtractedVersion)
	}
}

// TestValidateSchemaHTML tests validation with HTML parser
func TestValidateSchemaHTML(t *testing.T) {
	content := []byte(`<html><body><span class="version">2.5.0</span></body></html>`)
	schema := &PackageConfig{
		Parser:   "html",
		Selector: ".version",
	}

	result := ValidateSchema(content, schema, "2.5.0")

	if !result.Valid {
		t.Errorf("Expected validation to succeed, got error: %v", result.Error)
	}
	if result.ExtractedVersion != "2.5.0" {
		t.Errorf("Expected extracted version '2.5.0', got %q", result.ExtractedVersion)
	}
}

// TestTestExtraction tests the TestExtraction function
func TestTestExtraction(t *testing.T) {
	tests := []struct {
		name     string
		content  []byte
		schema   *PackageConfig
		expected string
		wantErr  bool
	}{
		{
			name:    "JSON extraction",
			content: []byte(`{"version": "1.0.0"}`),
			schema: &PackageConfig{
				Parser: "json",
				Path:   "version",
			},
			expected: "1.0.0",
			wantErr:  false,
		},
		{
			name:    "Regex extraction",
			content: []byte(`version=2.0.0`),
			schema: &PackageConfig{
				Parser:  "regex",
				Pattern: `version=([0-9.]+)`,
			},
			expected: "2.0.0",
			wantErr:  false,
		},
		{
			name:    "HTML extraction",
			content: []byte(`<div id="ver">3.0.0</div>`),
			schema: &PackageConfig{
				Parser:   "html",
				Selector: "#ver",
			},
			expected: "3.0.0",
			wantErr:  false,
		},
		{
			name:    "Failed extraction",
			content: []byte(`no version here`),
			schema: &PackageConfig{
				Parser: "json",
				Path:   "version",
			},
			expected: "",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := TestExtraction(tt.content, tt.schema)
			if (err != nil) != tt.wantErr {
				t.Errorf("TestExtraction() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if result != tt.expected {
				t.Errorf("TestExtraction() = %q, want %q", result, tt.expected)
			}
		})
	}
}

// TestCompareVersionStrings tests version string comparison
func TestCompareVersionStrings(t *testing.T) {
	tests := []struct {
		extracted string
		ebuild    string
		expected  bool
	}{
		{"1.0.0", "1.0.0", true},
		{"v1.0.0", "1.0.0", true},
		{"V1.0.0", "1.0.0", true},
		{"1.0.0", "v1.0.0", true},
		{"version-1.0.0", "1.0.0", true},
		{"release-1.0.0", "1.0.0", true},
		{"1.0.0", "2.0.0", false},
		{"1.0.0", "1.0.1", false},
		{" 1.0.0 ", "1.0.0", true}, // whitespace handling
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s_vs_%s", tt.extracted, tt.ebuild), func(t *testing.T) {
			result := compareVersionStrings(tt.extracted, tt.ebuild)
			if result != tt.expected {
				t.Errorf("compareVersionStrings(%q, %q) = %v, want %v",
					tt.extracted, tt.ebuild, result, tt.expected)
			}
		})
	}
}

// TestValidateSchemaWithFallback tests validation with fallback
func TestValidateSchemaWithFallback(t *testing.T) {
	// Primary content that fails extraction
	primaryContent := []byte(`{invalid json}`)

	// Fallback content that succeeds
	fallbackContent := []byte(`pkgver=1.0.0`)

	schema := &PackageConfig{
		Parser:          "json",
		Path:            "version",
		FallbackURL:     "https://fallback.example.com",
		FallbackParser:  "regex",
		FallbackPattern: `pkgver=([0-9.]+)`,
	}

	result := ValidateSchemaWithFallback(primaryContent, fallbackContent, schema, "1.0.0")

	if !result.Valid {
		t.Errorf("Expected validation to succeed with fallback, got error: %v", result.Error)
	}
	if result.ExtractedVersion != "1.0.0" {
		t.Errorf("Expected extracted version '1.0.0', got %q", result.ExtractedVersion)
	}
}

// =============================================================================
// Test Generators
// =============================================================================

// genDifferentVersion generates a version that's likely different from genVersion
func genDifferentVersion() gopter.Gen {
	return gen.RegexMatch(`^[4-9]{1,2}\.[0-9]{1,2}\.[0-9]{1,2}$`)
}
