// Package autoupdate provides schema validation functionality for ebuild autoupdate.
package autoupdate

import (
	"errors"
	"fmt"
	"strings"
)

// Error variables for validation errors
var (
	// ErrValidationFailed is returned when schema validation fails
	ErrValidationFailed = errors.New("schema validation failed")
	// ErrExtractionFailed is returned when version extraction fails during validation
	ErrExtractionFailed = errors.New("version extraction failed")
	// ErrVersionMismatch is returned when extracted version doesn't match ebuild version
	ErrVersionMismatch = errors.New("extracted version does not match ebuild version")
)

// ValidationResult represents the result of schema validation.
// It contains information about whether the schema successfully extracted
// a version and whether it matches the expected ebuild version.
type ValidationResult struct {
	// Valid indicates if the schema validation was successful
	Valid bool
	// ExtractedVersion is the version extracted using the schema
	ExtractedVersion string
	// EbuildVersion is the current version from the ebuild
	EbuildVersion string
	// VersionsMatch indicates if extracted version matches ebuild version
	VersionsMatch bool
	// Error contains any error that occurred during validation
	Error error
}

// ValidateSchema tests a schema by extracting version from content and comparing
// it with the ebuild version. This implements the schema validation flow:
// 1. Test version extraction using the schema
// 2. Compare the extracted version with the ebuild version
// 3. Mark as validated if versions match
//
// Parameters:
//   - content: The raw content fetched from the URL
//   - schema: The PackageConfig schema to validate
//   - ebuildVersion: The current version from the ebuild file
//
// Returns a ValidationResult with the validation outcome.
func ValidateSchema(content []byte, schema *PackageConfig, ebuildVersion string) *ValidationResult {
	result := &ValidationResult{
		EbuildVersion: ebuildVersion,
	}

	// Step 1: Test version extraction
	extractedVersion, err := TestExtraction(content, schema)
	if err != nil {
		result.Error = fmt.Errorf("%w: %v", ErrExtractionFailed, err)
		return result
	}
	result.ExtractedVersion = extractedVersion

	// Step 2: Compare extracted version with ebuild version
	result.VersionsMatch = compareVersionStrings(extractedVersion, ebuildVersion)

	// Step 3: Mark as validated if versions match
	if result.VersionsMatch {
		result.Valid = true
	} else {
		result.Error = fmt.Errorf("%w: extracted %q, expected %q",
			ErrVersionMismatch, extractedVersion, ebuildVersion)
	}

	return result
}

// TestExtraction attempts to extract version using the schema configuration.
// It creates the appropriate parser based on the schema and extracts the version
// from the provided content.
//
// Parameters:
//   - content: The raw content to extract version from
//   - schema: The PackageConfig defining how to extract the version
//
// Returns the extracted version string or an error if extraction fails.
func TestExtraction(content []byte, schema *PackageConfig) (string, error) {
	// Use ParseVersion which handles primary and fallback parsers
	version, err := ParseVersion(content, schema)
	if err != nil {
		return "", err
	}

	// Clean up the extracted version (remove common prefixes like 'v')
	version = normalizeVersion(version)

	return version, nil
}

// compareVersionStrings compares two version strings for equality.
// It handles common variations like 'v' prefix and normalizes both versions
// before comparison.
func compareVersionStrings(extracted, ebuild string) bool {
	// Normalize both versions
	normalizedExtracted := normalizeVersion(extracted)
	normalizedEbuild := normalizeVersion(ebuild)

	// Direct comparison
	if normalizedExtracted == normalizedEbuild {
		return true
	}

	// Try comparing with common version prefixes stripped
	strippedExtracted := stripVersionPrefix(normalizedExtracted)
	strippedEbuild := stripVersionPrefix(normalizedEbuild)

	return strippedExtracted == strippedEbuild
}

// normalizeVersion normalizes a version string by trimming whitespace
// and converting to lowercase for consistent comparison.
func normalizeVersion(version string) string {
	return strings.TrimSpace(version)
}

// stripVersionPrefix removes common version prefixes like 'v', 'V', 'version-', etc.
func stripVersionPrefix(version string) string {
	// Common prefixes to strip (ordered from longest to shortest to avoid partial matches)
	prefixes := []string{
		"version-", "Version-",
		"release-", "Release-",
		"ver-", "Ver-",
		"ver.", "Ver.",
		"v", "V",
	}

	for _, prefix := range prefixes {
		if strings.HasPrefix(version, prefix) {
			return strings.TrimPrefix(version, prefix)
		}
	}

	return version
}

// ValidateSchemaWithFallback validates a schema and tries fallback URL if primary fails.
// This is useful when the primary URL might be temporarily unavailable.
//
// Parameters:
//   - primaryContent: Content from the primary URL
//   - fallbackContent: Content from the fallback URL (can be nil)
//   - schema: The PackageConfig schema to validate
//   - ebuildVersion: The current version from the ebuild file
//
// Returns a ValidationResult with the validation outcome.
func ValidateSchemaWithFallback(primaryContent, fallbackContent []byte, schema *PackageConfig, ebuildVersion string) *ValidationResult {
	// Try primary first
	result := ValidateSchema(primaryContent, schema, ebuildVersion)
	if result.Valid {
		return result
	}

	// If primary failed and we have fallback content, try fallback
	if fallbackContent != nil && schema.FallbackURL != "" && schema.FallbackParser != "" {
		fallbackSchema := &PackageConfig{
			URL:     schema.FallbackURL,
			Parser:  schema.FallbackParser,
			Path:    schema.Path,
			Pattern: schema.FallbackPattern,
		}
		fallbackResult := ValidateSchema(fallbackContent, fallbackSchema, ebuildVersion)
		if fallbackResult.Valid {
			return fallbackResult
		}
	}

	return result
}
