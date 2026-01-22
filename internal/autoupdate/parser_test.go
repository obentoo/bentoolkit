package autoupdate

import (
	"encoding/json"
	"fmt"
	"regexp"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

// =============================================================================
// Property-Based Tests
// =============================================================================

// genVersion generates valid version strings (Gentoo format)
func genVersion() gopter.Gen {
	return gen.RegexMatch(`^[0-9]{1,3}\.[0-9]{1,3}(\.[0-9]{1,3})?$`)
}

// genSimpleFieldName generates simple field names for JSON
func genSimpleFieldName() gopter.Gen {
	return gen.RegexMatch(`^[a-z][a-z0-9_]{0,10}$`)
}

// TestJSONParserExtraction tests Property 2: JSON Parser Extraction
// **Feature: ebuild-autoupdate, Property 2: JSON Parser Extraction**
// **Validates: Requirements 2.1**
func TestJSONParserExtraction(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: Simple field extraction
	properties.Property("JSON parser extracts version from simple field", prop.ForAll(
		func(fieldName, version string) bool {
			// Create JSON with version at field
			data := map[string]interface{}{
				fieldName: version,
			}
			content, err := json.Marshal(data)
			if err != nil {
				t.Logf("Failed to marshal JSON: %v", err)
				return false
			}

			// Parse with JSONParser
			parser := &JSONParser{Path: fieldName}
			result, err := parser.Parse(content)
			if err != nil {
				t.Logf("Parse failed: %v", err)
				return false
			}

			return result == version
		},
		genSimpleFieldName(),
		genVersion(),
	))

	// Property: Nested field extraction
	properties.Property("JSON parser extracts version from nested field", prop.ForAll(
		func(outerField, innerField, version string) bool {
			// Create nested JSON
			data := map[string]interface{}{
				outerField: map[string]interface{}{
					innerField: version,
				},
			}
			content, err := json.Marshal(data)
			if err != nil {
				t.Logf("Failed to marshal JSON: %v", err)
				return false
			}

			// Parse with JSONParser
			path := fmt.Sprintf("%s.%s", outerField, innerField)
			parser := &JSONParser{Path: path}
			result, err := parser.Parse(content)
			if err != nil {
				t.Logf("Parse failed for path %q: %v", path, err)
				return false
			}

			return result == version
		},
		genSimpleFieldName(),
		genSimpleFieldName(),
		genVersion(),
	))

	// Property: Array index extraction
	properties.Property("JSON parser extracts version from array index", prop.ForAll(
		func(fieldName string, index int, version string) bool {
			// Ensure index is valid (0-4)
			index = index % 5
			if index < 0 {
				index = -index
			}

			// Create array with version at index
			arr := make([]interface{}, index+1)
			for i := range arr {
				arr[i] = map[string]interface{}{"dummy": "value"}
			}
			arr[index] = version

			data := map[string]interface{}{
				fieldName: arr,
			}
			content, err := json.Marshal(data)
			if err != nil {
				t.Logf("Failed to marshal JSON: %v", err)
				return false
			}

			// Parse with JSONParser
			path := fmt.Sprintf("%s[%d]", fieldName, index)
			parser := &JSONParser{Path: path}
			result, err := parser.Parse(content)
			if err != nil {
				t.Logf("Parse failed for path %q: %v", path, err)
				return false
			}

			return result == version
		},
		genSimpleFieldName(),
		gen.IntRange(0, 4),
		genVersion(),
	))

	properties.TestingRun(t)
}

// TestRegexParserExtraction tests Property 3: Regex Parser Extraction
// **Feature: ebuild-autoupdate, Property 3: Regex Parser Extraction**
// **Validates: Requirements 2.2**
func TestRegexParserExtraction(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: Simple version extraction with prefix
	properties.Property("Regex parser extracts version with prefix pattern", prop.ForAll(
		func(prefix, version string) bool {
			// Create content with version
			content := fmt.Sprintf("%s%s", prefix, version)

			// Create pattern that captures version after prefix
			pattern := regexp.QuoteMeta(prefix) + `([0-9]+\.[0-9]+(?:\.[0-9]+)?)`
			parser := &RegexParser{Pattern: pattern}
			result, err := parser.Parse([]byte(content))
			if err != nil {
				t.Logf("Parse failed: %v", err)
				return false
			}

			return result == version
		},
		gen.RegexMatch(`^[a-z_]{1,10}=`),
		genVersion(),
	))

	// Property: Version extraction from multiline content
	properties.Property("Regex parser extracts version from multiline content", prop.ForAll(
		func(version string) bool {
			// Create multiline content
			content := fmt.Sprintf("some header\nversion=%s\nsome footer", version)

			pattern := `version=([0-9]+\.[0-9]+(?:\.[0-9]+)?)`
			parser := &RegexParser{Pattern: pattern}
			result, err := parser.Parse([]byte(content))
			if err != nil {
				t.Logf("Parse failed: %v", err)
				return false
			}

			return result == version
		},
		genVersion(),
	))

	properties.TestingRun(t)
}

// =============================================================================
// Unit Tests - JSONParser
// =============================================================================

// TestJSONParserSimpleField tests simple field extraction
func TestJSONParserSimpleField(t *testing.T) {
	content := []byte(`{"tag_name": "v1.2.3"}`)
	parser := &JSONParser{Path: "tag_name"}

	result, err := parser.Parse(content)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result != "v1.2.3" {
		t.Errorf("Expected 'v1.2.3', got %q", result)
	}
}

// TestJSONParserNestedField tests nested field extraction
func TestJSONParserNestedField(t *testing.T) {
	content := []byte(`{"data": {"release": {"version": "2.0.0"}}}`)
	parser := &JSONParser{Path: "data.release.version"}

	result, err := parser.Parse(content)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result != "2.0.0" {
		t.Errorf("Expected '2.0.0', got %q", result)
	}
}

// TestJSONParserArrayIndex tests array index extraction
func TestJSONParserArrayIndex(t *testing.T) {
	content := []byte(`{"notes": [{"version": "1.0.0"}, {"version": "1.1.0"}]}`)
	parser := &JSONParser{Path: "notes[0].version"}

	result, err := parser.Parse(content)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result != "1.0.0" {
		t.Errorf("Expected '1.0.0', got %q", result)
	}
}

// TestJSONParserArrayIndexSecond tests second array element extraction
func TestJSONParserArrayIndexSecond(t *testing.T) {
	content := []byte(`{"releases": ["1.0.0", "2.0.0", "3.0.0"]}`)
	parser := &JSONParser{Path: "releases[1]"}

	result, err := parser.Parse(content)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result != "2.0.0" {
		t.Errorf("Expected '2.0.0', got %q", result)
	}
}

// TestJSONParserNumericValue tests numeric value extraction
func TestJSONParserNumericValue(t *testing.T) {
	content := []byte(`{"version": 123}`)
	parser := &JSONParser{Path: "version"}

	result, err := parser.Parse(content)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result != "123" {
		t.Errorf("Expected '123', got %q", result)
	}
}

// TestJSONParserFloatValue tests float value extraction
func TestJSONParserFloatValue(t *testing.T) {
	content := []byte(`{"version": 1.5}`)
	parser := &JSONParser{Path: "version"}

	result, err := parser.Parse(content)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result != "1.5" {
		t.Errorf("Expected '1.5', got %q", result)
	}
}

// TestJSONParserEmptyPath tests error on empty path
func TestJSONParserEmptyPath(t *testing.T) {
	content := []byte(`{"version": "1.0.0"}`)
	parser := &JSONParser{Path: ""}

	_, err := parser.Parse(content)
	if err == nil {
		t.Error("Expected error for empty path")
	}
}

// TestJSONParserInvalidJSON tests error on invalid JSON
func TestJSONParserInvalidJSON(t *testing.T) {
	content := []byte(`{invalid json}`)
	parser := &JSONParser{Path: "version"}

	_, err := parser.Parse(content)
	if err == nil {
		t.Error("Expected error for invalid JSON")
	}
}

// TestJSONParserMissingField tests error on missing field
func TestJSONParserMissingField(t *testing.T) {
	content := []byte(`{"other": "value"}`)
	parser := &JSONParser{Path: "version"}

	_, err := parser.Parse(content)
	if err == nil {
		t.Error("Expected error for missing field")
	}
}

// TestJSONParserArrayOutOfBounds tests error on array index out of bounds
func TestJSONParserArrayOutOfBounds(t *testing.T) {
	content := []byte(`{"items": ["a", "b"]}`)
	parser := &JSONParser{Path: "items[5]"}

	_, err := parser.Parse(content)
	if err == nil {
		t.Error("Expected error for array index out of bounds")
	}
}

// TestJSONParserExpectedArray tests error when expecting array but got object
func TestJSONParserExpectedArray(t *testing.T) {
	content := []byte(`{"items": {"key": "value"}}`)
	parser := &JSONParser{Path: "items[0]"}

	_, err := parser.Parse(content)
	if err == nil {
		t.Error("Expected error when expecting array but got object")
	}
}

// TestJSONParserExpectedObject tests error when expecting object but got array
func TestJSONParserExpectedObject(t *testing.T) {
	content := []byte(`{"items": ["a", "b"]}`)
	parser := &JSONParser{Path: "items.key"}

	_, err := parser.Parse(content)
	if err == nil {
		t.Error("Expected error when expecting object but got array")
	}
}

// TestJSONParserUnclosedBracket tests error on unclosed bracket
func TestJSONParserUnclosedBracket(t *testing.T) {
	content := []byte(`{"items": ["a"]}`)
	parser := &JSONParser{Path: "items[0"}

	_, err := parser.Parse(content)
	if err == nil {
		t.Error("Expected error for unclosed bracket")
	}
}

// TestJSONParserNonNumericIndex tests error on non-numeric array index
func TestJSONParserNonNumericIndex(t *testing.T) {
	content := []byte(`{"items": ["a"]}`)
	parser := &JSONParser{Path: "items[abc]"}

	_, err := parser.Parse(content)
	if err == nil {
		t.Error("Expected error for non-numeric array index")
	}
}

// =============================================================================
// Unit Tests - RegexParser
// =============================================================================

// TestRegexParserSimple tests simple regex extraction
func TestRegexParserSimple(t *testing.T) {
	content := []byte(`pkgver=1.2.3`)
	parser := &RegexParser{Pattern: `pkgver=([0-9.]+)`}

	result, err := parser.Parse(content)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result != "1.2.3" {
		t.Errorf("Expected '1.2.3', got %q", result)
	}
}

// TestRegexParserMultiline tests multiline content extraction
func TestRegexParserMultiline(t *testing.T) {
	content := []byte(`# Package info
pkgname=mypackage
pkgver=2.0.0
pkgrel=1`)
	parser := &RegexParser{Pattern: `pkgver=([0-9.]+)`}

	result, err := parser.Parse(content)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result != "2.0.0" {
		t.Errorf("Expected '2.0.0', got %q", result)
	}
}

// TestRegexParserVersionPrefix tests version with 'v' prefix
func TestRegexParserVersionPrefix(t *testing.T) {
	content := []byte(`Latest release: v3.1.4`)
	parser := &RegexParser{Pattern: `v([0-9]+\.[0-9]+\.[0-9]+)`}

	result, err := parser.Parse(content)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result != "3.1.4" {
		t.Errorf("Expected '3.1.4', got %q", result)
	}
}

// TestRegexParserEmptyPattern tests error on empty pattern
func TestRegexParserEmptyPattern(t *testing.T) {
	content := []byte(`version=1.0.0`)
	parser := &RegexParser{Pattern: ""}

	_, err := parser.Parse(content)
	if err == nil {
		t.Error("Expected error for empty pattern")
	}
}

// TestRegexParserInvalidPattern tests error on invalid regex pattern
func TestRegexParserInvalidPattern(t *testing.T) {
	content := []byte(`version=1.0.0`)
	parser := &RegexParser{Pattern: `[invalid`}

	_, err := parser.Parse(content)
	if err == nil {
		t.Error("Expected error for invalid regex pattern")
	}
}

// TestRegexParserNoCaptureGroup tests error when no capture group
func TestRegexParserNoCaptureGroup(t *testing.T) {
	content := []byte(`version=1.0.0`)
	parser := &RegexParser{Pattern: `version=[0-9.]+`}

	_, err := parser.Parse(content)
	if err == nil {
		t.Error("Expected error for pattern without capture group")
	}
}

// TestRegexParserNoMatch tests error when pattern doesn't match
func TestRegexParserNoMatch(t *testing.T) {
	content := []byte(`no version here`)
	parser := &RegexParser{Pattern: `version=([0-9.]+)`}

	_, err := parser.Parse(content)
	if err == nil {
		t.Error("Expected error when pattern doesn't match")
	}
}

// TestRegexParserEmptyCaptureGroup tests error when capture group matches empty string
func TestRegexParserEmptyCaptureGroup(t *testing.T) {
	content := []byte(`version=`)
	parser := &RegexParser{Pattern: `version=([0-9.]*)`}

	_, err := parser.Parse(content)
	if err == nil {
		t.Error("Expected error when capture group matches empty string")
	}
}

// =============================================================================
// Unit Tests - NewParser Factory
// =============================================================================

// TestNewParserJSON tests creating JSON parser
func TestNewParserJSON(t *testing.T) {
	parser, err := NewParser("json", "version")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	jsonParser, ok := parser.(*JSONParser)
	if !ok {
		t.Fatal("Expected JSONParser type")
	}
	if jsonParser.Path != "version" {
		t.Errorf("Expected path 'version', got %q", jsonParser.Path)
	}
}

// TestNewParserRegex tests creating regex parser
func TestNewParserRegex(t *testing.T) {
	parser, err := NewParser("regex", `version=([0-9.]+)`)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	regexParser, ok := parser.(*RegexParser)
	if !ok {
		t.Fatal("Expected RegexParser type")
	}
	if regexParser.Pattern != `version=([0-9.]+)` {
		t.Errorf("Expected pattern 'version=([0-9.]+)', got %q", regexParser.Pattern)
	}
}

// TestNewParserInvalidType tests error on invalid parser type
func TestNewParserInvalidType(t *testing.T) {
	_, err := NewParser("invalid", "path")
	if err == nil {
		t.Error("Expected error for invalid parser type")
	}
}

// TestNewParserRegexInvalidPattern tests error on invalid regex pattern
func TestNewParserRegexInvalidPattern(t *testing.T) {
	_, err := NewParser("regex", `[invalid`)
	if err == nil {
		t.Error("Expected error for invalid regex pattern")
	}
}

// TestNewParserRegexNoCaptureGroup tests error when regex has no capture group
func TestNewParserRegexNoCaptureGroup(t *testing.T) {
	_, err := NewParser("regex", `version=[0-9.]+`)
	if err == nil {
		t.Error("Expected error for regex without capture group")
	}
}

// =============================================================================
// Unit Tests - ParseVersion
// =============================================================================

// TestParseVersionJSON tests ParseVersion with JSON config
func TestParseVersionJSON(t *testing.T) {
	content := []byte(`{"version": "1.0.0"}`)
	cfg := &PackageConfig{
		Parser: "json",
		Path:   "version",
	}

	result, err := ParseVersion(content, cfg)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result != "1.0.0" {
		t.Errorf("Expected '1.0.0', got %q", result)
	}
}

// TestParseVersionRegex tests ParseVersion with regex config
func TestParseVersionRegex(t *testing.T) {
	content := []byte(`pkgver=2.0.0`)
	cfg := &PackageConfig{
		Parser:  "regex",
		Pattern: `pkgver=([0-9.]+)`,
	}

	result, err := ParseVersion(content, cfg)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result != "2.0.0" {
		t.Errorf("Expected '2.0.0', got %q", result)
	}
}

// TestParseVersionFallback tests ParseVersion with fallback
func TestParseVersionFallback(t *testing.T) {
	// Content that doesn't match JSON but matches regex
	content := []byte(`pkgver=3.0.0`)
	cfg := &PackageConfig{
		Parser:          "json",
		Path:            "version",
		FallbackParser:  "regex",
		FallbackPattern: `pkgver=([0-9.]+)`,
	}

	result, err := ParseVersion(content, cfg)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result != "3.0.0" {
		t.Errorf("Expected '3.0.0', got %q", result)
	}
}

// TestParseVersionAllFail tests ParseVersion when all parsers fail
func TestParseVersionAllFail(t *testing.T) {
	content := []byte(`no version here`)
	cfg := &PackageConfig{
		Parser:          "json",
		Path:            "version",
		FallbackParser:  "regex",
		FallbackPattern: `pkgver=([0-9.]+)`,
	}

	_, err := ParseVersion(content, cfg)
	if err == nil {
		t.Error("Expected error when all parsers fail")
	}
}
