package autoupdate

import (
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

// genHTMLVersion generates valid version strings for HTML tests
func genHTMLVersion() gopter.Gen {
	return gen.RegexMatch(`^[0-9]{1,3}\.[0-9]{1,3}(\.[0-9]{1,3})?$`)
}

// genSimpleClassName generates simple CSS class names
func genSimpleClassName() gopter.Gen {
	return gen.RegexMatch(`^[a-z][a-z0-9_-]{0,10}$`)
}

// genSimpleTagName generates simple HTML tag names
func genSimpleTagName() gopter.Gen {
	return gen.OneConstOf("div", "span", "p", "a", "h1", "h2", "h3", "strong", "em")
}

// TestHTMLCSSExtraction tests Property 8: HTML CSS Selector Extraction
// **Feature: autoupdate-analyzer, Property 8: HTML CSS Selector Extraction**
// **Validates: Requirements 4.1**
func TestHTMLCSSExtraction(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: CSS selector extracts text content from matching element
	properties.Property("CSS selector extracts version from class selector", prop.ForAll(
		func(className, version string) bool {
			// Create HTML with version in element with class
			html := fmt.Sprintf(`<!DOCTYPE html>
<html>
<body>
<div class="%s">%s</div>
</body>
</html>`, className, version)

			// Parse with HTMLParser using CSS selector
			parser := &HTMLParser{Selector: "." + className}
			result, err := parser.Parse([]byte(html))
			if err != nil {
				t.Logf("Parse failed: %v", err)
				return false
			}

			return result == version
		},
		genSimpleClassName(),
		genHTMLVersion(),
	))

	// Property: CSS selector extracts text from ID selector
	properties.Property("CSS selector extracts version from ID selector", prop.ForAll(
		func(idName, version string) bool {
			// Create HTML with version in element with ID
			html := fmt.Sprintf(`<!DOCTYPE html>
<html>
<body>
<span id="%s">%s</span>
</body>
</html>`, idName, version)

			// Parse with HTMLParser using ID selector
			parser := &HTMLParser{Selector: "#" + idName}
			result, err := parser.Parse([]byte(html))
			if err != nil {
				t.Logf("Parse failed: %v", err)
				return false
			}

			return result == version
		},
		genSimpleClassName(),
		genHTMLVersion(),
	))

	// Property: CSS selector extracts text from tag selector
	properties.Property("CSS selector extracts version from tag selector", prop.ForAll(
		func(tagName, version string) bool {
			// Create HTML with version in specific tag
			html := fmt.Sprintf(`<!DOCTYPE html>
<html>
<body>
<%s>%s</%s>
</body>
</html>`, tagName, version, tagName)

			// Parse with HTMLParser using tag selector
			parser := &HTMLParser{Selector: tagName}
			result, err := parser.Parse([]byte(html))
			if err != nil {
				t.Logf("Parse failed: %v", err)
				return false
			}

			return result == version
		},
		genSimpleTagName(),
		genHTMLVersion(),
	))

	properties.TestingRun(t)
}

// =============================================================================
// Unit Tests - HTMLParser CSS Selector
// =============================================================================

// TestHTMLParserCSSSimpleClass tests simple class selector extraction
func TestHTMLParserCSSSimpleClass(t *testing.T) {
	html := []byte(`<html><body><div class="version">1.2.3</div></body></html>`)
	parser := &HTMLParser{Selector: ".version"}

	result, err := parser.Parse(html)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result != "1.2.3" {
		t.Errorf("Expected '1.2.3', got %q", result)
	}
}

// TestHTMLParserCSSNestedSelector tests nested selector extraction
func TestHTMLParserCSSNestedSelector(t *testing.T) {
	html := []byte(`<html><body>
		<div class="release">
			<span class="version">2.0.0</span>
		</div>
	</body></html>`)
	parser := &HTMLParser{Selector: ".release .version"}

	result, err := parser.Parse(html)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result != "2.0.0" {
		t.Errorf("Expected '2.0.0', got %q", result)
	}
}

// TestHTMLParserCSSIDSelector tests ID selector extraction
func TestHTMLParserCSSIDSelector(t *testing.T) {
	html := []byte(`<html><body><span id="current-version">3.1.4</span></body></html>`)
	parser := &HTMLParser{Selector: "#current-version"}

	result, err := parser.Parse(html)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result != "3.1.4" {
		t.Errorf("Expected '3.1.4', got %q", result)
	}
}

// TestHTMLParserCSSAttributeSelector tests attribute selector extraction
func TestHTMLParserCSSAttributeSelector(t *testing.T) {
	html := []byte(`<html><body><a href="/download" data-version="4.0.0">Download</a></body></html>`)
	parser := &HTMLParser{Selector: "a[data-version]"}

	result, err := parser.Parse(html)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result != "Download" {
		t.Errorf("Expected 'Download', got %q", result)
	}
}

// TestHTMLParserCSSNoMatch tests error when no element matches
func TestHTMLParserCSSNoMatch(t *testing.T) {
	html := []byte(`<html><body><div>No version here</div></body></html>`)
	parser := &HTMLParser{Selector: ".version"}

	_, err := parser.Parse(html)
	if err == nil {
		t.Error("Expected error for no matching element")
	}
	if !errors.Is(err, ErrNoElementFound) {
		t.Errorf("Expected ErrNoElementFound, got: %v", err)
	}
}

// TestHTMLParserCSSWhitespace tests whitespace trimming
func TestHTMLParserCSSWhitespace(t *testing.T) {
	html := []byte(`<html><body><div class="version">  5.0.0  </div></body></html>`)
	parser := &HTMLParser{Selector: ".version"}

	result, err := parser.Parse(html)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result != "5.0.0" {
		t.Errorf("Expected '5.0.0', got %q", result)
	}
}

// TestHTMLXPathExtraction tests Property 9: HTML XPath Extraction
// **Feature: autoupdate-analyzer, Property 9: HTML XPath Extraction**
// **Validates: Requirements 4.2**
func TestHTMLXPathExtraction(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: XPath extracts text content from matching element by class
	properties.Property("XPath extracts version from element with class", prop.ForAll(
		func(className, version string) bool {
			// Create HTML with version in element with class
			html := fmt.Sprintf(`<!DOCTYPE html>
<html>
<body>
<div class="%s">%s</div>
</body>
</html>`, className, version)

			// Parse with HTMLParser using XPath
			xpath := fmt.Sprintf(`//div[@class="%s"]`, className)
			parser := &HTMLParser{XPath: xpath}
			result, err := parser.Parse([]byte(html))
			if err != nil {
				t.Logf("Parse failed: %v", err)
				return false
			}

			return result == version
		},
		genSimpleClassName(),
		genHTMLVersion(),
	))

	// Property: XPath extracts text from element by ID
	properties.Property("XPath extracts version from element with ID", prop.ForAll(
		func(idName, version string) bool {
			// Create HTML with version in element with ID
			html := fmt.Sprintf(`<!DOCTYPE html>
<html>
<body>
<span id="%s">%s</span>
</body>
</html>`, idName, version)

			// Parse with HTMLParser using XPath
			xpath := fmt.Sprintf(`//span[@id="%s"]`, idName)
			parser := &HTMLParser{XPath: xpath}
			result, err := parser.Parse([]byte(html))
			if err != nil {
				t.Logf("Parse failed: %v", err)
				return false
			}

			return result == version
		},
		genSimpleClassName(),
		genHTMLVersion(),
	))

	// Property: XPath extracts text from nested element
	properties.Property("XPath extracts version from nested element", prop.ForAll(
		func(outerClass, innerClass, version string) bool {
			// Create HTML with nested structure
			html := fmt.Sprintf(`<!DOCTYPE html>
<html>
<body>
<div class="%s">
	<span class="%s">%s</span>
</div>
</body>
</html>`, outerClass, innerClass, version)

			// Parse with HTMLParser using XPath
			xpath := fmt.Sprintf(`//div[@class="%s"]/span[@class="%s"]`, outerClass, innerClass)
			parser := &HTMLParser{XPath: xpath}
			result, err := parser.Parse([]byte(html))
			if err != nil {
				t.Logf("Parse failed: %v", err)
				return false
			}

			return result == version
		},
		genSimpleClassName(),
		genSimpleClassName(),
		genHTMLVersion(),
	))

	properties.TestingRun(t)
}

// =============================================================================
// Unit Tests - HTMLParser XPath
// =============================================================================

// TestHTMLParserXPathSimple tests simple XPath extraction
func TestHTMLParserXPathSimple(t *testing.T) {
	html := []byte(`<html><body><div class="version">1.2.3</div></body></html>`)
	parser := &HTMLParser{XPath: `//div[@class="version"]`}

	result, err := parser.Parse(html)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result != "1.2.3" {
		t.Errorf("Expected '1.2.3', got %q", result)
	}
}

// TestHTMLParserXPathNested tests nested XPath extraction
func TestHTMLParserXPathNested(t *testing.T) {
	html := []byte(`<html><body>
		<div class="release">
			<span class="version">2.0.0</span>
		</div>
	</body></html>`)
	parser := &HTMLParser{XPath: `//div[@class="release"]/span[@class="version"]`}

	result, err := parser.Parse(html)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result != "2.0.0" {
		t.Errorf("Expected '2.0.0', got %q", result)
	}
}

// TestHTMLParserXPathByID tests XPath extraction by ID
func TestHTMLParserXPathByID(t *testing.T) {
	html := []byte(`<html><body><span id="current-version">3.1.4</span></body></html>`)
	parser := &HTMLParser{XPath: `//*[@id="current-version"]`}

	result, err := parser.Parse(html)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result != "3.1.4" {
		t.Errorf("Expected '3.1.4', got %q", result)
	}
}

// TestHTMLParserXPathText tests XPath text() function
func TestHTMLParserXPathText(t *testing.T) {
	html := []byte(`<html><body><div class="version">4.0.0</div></body></html>`)
	parser := &HTMLParser{XPath: `//div[@class="version"]/text()`}

	result, err := parser.Parse(html)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result != "4.0.0" {
		t.Errorf("Expected '4.0.0', got %q", result)
	}
}

// TestHTMLParserXPathNoMatch tests error when no element matches
func TestHTMLParserXPathNoMatch(t *testing.T) {
	html := []byte(`<html><body><div>No version here</div></body></html>`)
	parser := &HTMLParser{XPath: `//div[@class="version"]`}

	_, err := parser.Parse(html)
	if err == nil {
		t.Error("Expected error for no matching element")
	}
	if !errors.Is(err, ErrNoElementFound) {
		t.Errorf("Expected ErrNoElementFound, got: %v", err)
	}
}

// TestHTMLParserXPathInvalid tests error on invalid XPath
func TestHTMLParserXPathInvalid(t *testing.T) {
	html := []byte(`<html><body><div>content</div></body></html>`)
	parser := &HTMLParser{XPath: `//[invalid`}

	_, err := parser.Parse(html)
	if err == nil {
		t.Error("Expected error for invalid XPath")
	}
	if !errors.Is(err, ErrInvalidXPath) {
		t.Errorf("Expected ErrInvalidXPath, got: %v", err)
	}
}

// TestHTMLParserXPathWhitespace tests whitespace trimming
func TestHTMLParserXPathWhitespace(t *testing.T) {
	html := []byte(`<html><body><div class="version">  5.0.0  </div></body></html>`)
	parser := &HTMLParser{XPath: `//div[@class="version"]`}

	result, err := parser.Parse(html)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result != "5.0.0" {
		t.Errorf("Expected '5.0.0', got %q", result)
	}
}

// TestFirstMatchBehavior tests Property 10: First Match Behavior
// **Feature: autoupdate-analyzer, Property 10: First Match Behavior**
// **Validates: Requirements 4.3**
func TestFirstMatchBehavior(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: CSS selector returns first match when multiple elements match
	properties.Property("CSS selector returns first match from multiple elements", prop.ForAll(
		func(className, firstVersion, secondVersion string) bool {
			// Create HTML with multiple elements matching the same selector
			html := fmt.Sprintf(`<!DOCTYPE html>
<html>
<body>
<div class="%s">%s</div>
<div class="%s">%s</div>
<div class="%s">other</div>
</body>
</html>`, className, firstVersion, className, secondVersion, className)

			// Parse with HTMLParser using CSS selector
			parser := &HTMLParser{Selector: "." + className}
			result, err := parser.Parse([]byte(html))
			if err != nil {
				t.Logf("Parse failed: %v", err)
				return false
			}

			// Should return the first match
			return result == firstVersion
		},
		genSimpleClassName(),
		genHTMLVersion(),
		genHTMLVersion(),
	))

	// Property: XPath returns first match when multiple elements match
	properties.Property("XPath returns first match from multiple elements", prop.ForAll(
		func(className, firstVersion, secondVersion string) bool {
			// Create HTML with multiple elements matching the same XPath
			html := fmt.Sprintf(`<!DOCTYPE html>
<html>
<body>
<span class="%s">%s</span>
<span class="%s">%s</span>
<span class="%s">other</span>
</body>
</html>`, className, firstVersion, className, secondVersion, className)

			// Parse with HTMLParser using XPath
			xpath := fmt.Sprintf(`//span[@class="%s"]`, className)
			parser := &HTMLParser{XPath: xpath}
			result, err := parser.Parse([]byte(html))
			if err != nil {
				t.Logf("Parse failed: %v", err)
				return false
			}

			// Should return the first match
			return result == firstVersion
		},
		genSimpleClassName(),
		genHTMLVersion(),
		genHTMLVersion(),
	))

	properties.TestingRun(t)
}

// =============================================================================
// Unit Tests - First Match Behavior
// =============================================================================

// TestHTMLParserCSSFirstMatch tests CSS selector returns first match
func TestHTMLParserCSSFirstMatch(t *testing.T) {
	html := []byte(`<html><body>
		<div class="version">1.0.0</div>
		<div class="version">2.0.0</div>
		<div class="version">3.0.0</div>
	</body></html>`)
	parser := &HTMLParser{Selector: ".version"}

	result, err := parser.Parse(html)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result != "1.0.0" {
		t.Errorf("Expected '1.0.0' (first match), got %q", result)
	}
}

// TestHTMLParserXPathFirstMatch tests XPath returns first match
func TestHTMLParserXPathFirstMatch(t *testing.T) {
	html := []byte(`<html><body>
		<span class="release">1.0.0</span>
		<span class="release">2.0.0</span>
		<span class="release">3.0.0</span>
	</body></html>`)
	parser := &HTMLParser{XPath: `//span[@class="release"]`}

	result, err := parser.Parse(html)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result != "1.0.0" {
		t.Errorf("Expected '1.0.0' (first match), got %q", result)
	}
}

// TestHTMLParserCSSFirstMatchNested tests first match in nested structure
func TestHTMLParserCSSFirstMatchNested(t *testing.T) {
	html := []byte(`<html><body>
		<ul class="releases">
			<li>1.0.0</li>
			<li>2.0.0</li>
			<li>3.0.0</li>
		</ul>
	</body></html>`)
	parser := &HTMLParser{Selector: ".releases li"}

	result, err := parser.Parse(html)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result != "1.0.0" {
		t.Errorf("Expected '1.0.0' (first match), got %q", result)
	}
}

// TestHTMLParserXPathFirstMatchNested tests first match in nested XPath
func TestHTMLParserXPathFirstMatchNested(t *testing.T) {
	html := []byte(`<html><body>
		<ul class="releases">
			<li>1.0.0</li>
			<li>2.0.0</li>
			<li>3.0.0</li>
		</ul>
	</body></html>`)
	parser := &HTMLParser{XPath: `//ul[@class="releases"]/li`}

	result, err := parser.Parse(html)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result != "1.0.0" {
		t.Errorf("Expected '1.0.0' (first match), got %q", result)
	}
}

// TestRegexPostProcessing tests Property 11: Regex Post-Processing
// **Feature: autoupdate-analyzer, Property 11: Regex Post-Processing**
// **Validates: Requirements 4.4**
func TestRegexPostProcessing(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: Regex with capture group extracts the first capture group
	properties.Property("Regex extracts first capture group from version text", prop.ForAll(
		func(className, prefix, version, suffix string) bool {
			// Create HTML with version embedded in text
			text := prefix + "v" + version + suffix
			html := fmt.Sprintf(`<!DOCTYPE html>
<html>
<body>
<div class="%s">%s</div>
</body>
</html>`, className, text)

			// Parse with HTMLParser using CSS selector and regex with capture group
			// Regex handles both 2-part (1.2) and 3-part (1.2.3) versions
			parser := &HTMLParser{
				Selector: "." + className,
				Regex:    `v([0-9]+\.[0-9]+(?:\.[0-9]+)?)`,
			}
			result, err := parser.Parse([]byte(html))
			if err != nil {
				t.Logf("Parse failed: %v", err)
				return false
			}

			// Should return the captured version (without 'v' prefix)
			return result == version
		},
		genSimpleClassName(),
		gen.OneConstOf("Version: ", "Release ", "Latest: ", ""),
		genHTMLVersion(),
		gen.OneConstOf(" released", "-beta", " is out", ""),
	))

	// Property: Regex with capture group works with XPath
	properties.Property("Regex extracts first capture group with XPath", prop.ForAll(
		func(idName, prefix, version string) bool {
			// Create HTML with version embedded in text
			text := prefix + version + " available"
			html := fmt.Sprintf(`<!DOCTYPE html>
<html>
<body>
<span id="%s">%s</span>
</body>
</html>`, idName, text)

			// Parse with HTMLParser using XPath and regex with capture group
			xpath := fmt.Sprintf(`//span[@id="%s"]`, idName)
			parser := &HTMLParser{
				XPath: xpath,
				Regex: `([0-9]+\.[0-9]+(?:\.[0-9]+)?)`,
			}
			result, err := parser.Parse([]byte(html))
			if err != nil {
				t.Logf("Parse failed: %v", err)
				return false
			}

			// Should return the captured version
			return result == version
		},
		genSimpleClassName(),
		gen.OneConstOf("Version ", "v", "release-", ""),
		genHTMLVersion(),
	))

	// Property: Regex without capture group returns full match
	properties.Property("Regex without capture group returns full match", prop.ForAll(
		func(className, version string) bool {
			// Create HTML with version
			html := fmt.Sprintf(`<!DOCTYPE html>
<html>
<body>
<div class="%s">Version %s</div>
</body>
</html>`, className, version)

			// Parse with HTMLParser using regex without capture group
			// Regex handles both 2-part (1.2) and 3-part (1.2.3) versions
			parser := &HTMLParser{
				Selector: "." + className,
				Regex:    `[0-9]+\.[0-9]+(?:\.[0-9]+)?`,
			}
			result, err := parser.Parse([]byte(html))
			if err != nil {
				t.Logf("Parse failed: %v", err)
				return false
			}

			// Should return the full match
			return result == version
		},
		genSimpleClassName(),
		genHTMLVersion(),
	))

	properties.TestingRun(t)
}

// =============================================================================
// Unit Tests - Regex Post-Processing
// =============================================================================

// TestHTMLParserRegexPostProcessing tests regex post-processing
func TestHTMLParserRegexPostProcessing(t *testing.T) {
	html := []byte(`<html><body><div class="version">Version: v1.2.3-beta</div></body></html>`)
	parser := &HTMLParser{
		Selector: ".version",
		Regex:    `v([0-9]+\.[0-9]+\.[0-9]+)`,
	}

	result, err := parser.Parse(html)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result != "1.2.3" {
		t.Errorf("Expected '1.2.3', got %q", result)
	}
}

// TestHTMLParserRegexPostProcessingXPath tests regex with XPath
func TestHTMLParserRegexPostProcessingXPath(t *testing.T) {
	html := []byte(`<html><body><span id="ver">Release 2.0.0 is out!</span></body></html>`)
	parser := &HTMLParser{
		XPath: `//*[@id="ver"]`,
		Regex: `([0-9]+\.[0-9]+\.[0-9]+)`,
	}

	result, err := parser.Parse(html)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result != "2.0.0" {
		t.Errorf("Expected '2.0.0', got %q", result)
	}
}

// TestHTMLParserRegexNoMatch tests error when regex doesn't match
func TestHTMLParserRegexNoMatch(t *testing.T) {
	html := []byte(`<html><body><div class="version">No version here</div></body></html>`)
	parser := &HTMLParser{
		Selector: ".version",
		Regex:    `v([0-9]+\.[0-9]+\.[0-9]+)`,
	}

	_, err := parser.Parse(html)
	if err == nil {
		t.Error("Expected error when regex doesn't match")
	}
	if !errors.Is(err, ErrRegexNoMatch) {
		t.Errorf("Expected ErrRegexNoMatch, got: %v", err)
	}
}

// TestHTMLParserInvalidRegex tests error on invalid regex
func TestHTMLParserInvalidRegex(t *testing.T) {
	html := []byte(`<html><body><div class="version">1.0.0</div></body></html>`)
	parser := &HTMLParser{
		Selector: ".version",
		Regex:    `[invalid`,
	}

	_, err := parser.Parse(html)
	if err == nil {
		t.Error("Expected error for invalid regex")
	}
	if !errors.Is(err, ErrInvalidRegexPattern) {
		t.Errorf("Expected ErrInvalidRegexPattern, got: %v", err)
	}
}

// TestNoMatchError tests Property 12: No Match Error
// **Feature: autoupdate-analyzer, Property 12: No Match Error**
// **Validates: Requirements 4.6**
func TestNoMatchError(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: CSS selector returns ErrNoElementFound when no elements match
	properties.Property("CSS selector returns ErrNoElementFound for non-matching selector", prop.ForAll(
		func(existingClass, searchClass string) bool {
			// Ensure the classes are different
			if existingClass == searchClass {
				return true // Skip this case
			}

			// Create HTML with one class
			html := fmt.Sprintf(`<!DOCTYPE html>
<html>
<body>
<div class="%s">1.0.0</div>
</body>
</html>`, existingClass)

			// Parse with HTMLParser using a different class selector
			parser := &HTMLParser{Selector: "." + searchClass}
			_, err := parser.Parse([]byte(html))

			// Should return ErrNoElementFound
			return errors.Is(err, ErrNoElementFound)
		},
		genSimpleClassName(),
		genSimpleClassName(),
	))

	// Property: XPath returns ErrNoElementFound when no elements match
	properties.Property("XPath returns ErrNoElementFound for non-matching xpath", prop.ForAll(
		func(existingID, searchID string) bool {
			// Ensure the IDs are different
			if existingID == searchID {
				return true // Skip this case
			}

			// Create HTML with one ID
			html := fmt.Sprintf(`<!DOCTYPE html>
<html>
<body>
<span id="%s">1.0.0</span>
</body>
</html>`, existingID)

			// Parse with HTMLParser using a different ID in XPath
			xpath := fmt.Sprintf(`//span[@id="%s"]`, searchID)
			parser := &HTMLParser{XPath: xpath}
			_, err := parser.Parse([]byte(html))

			// Should return ErrNoElementFound
			return errors.Is(err, ErrNoElementFound)
		},
		genSimpleClassName(),
		genSimpleClassName(),
	))

	// Property: CSS selector returns ErrNoElementFound for empty HTML body
	properties.Property("CSS selector returns ErrNoElementFound for empty body", prop.ForAll(
		func(className string) bool {
			// Create HTML with empty body
			html := `<!DOCTYPE html>
<html>
<body>
</body>
</html>`

			// Parse with HTMLParser using any class selector
			parser := &HTMLParser{Selector: "." + className}
			_, err := parser.Parse([]byte(html))

			// Should return ErrNoElementFound
			return errors.Is(err, ErrNoElementFound)
		},
		genSimpleClassName(),
	))

	// Property: XPath returns ErrNoElementFound for empty HTML body
	properties.Property("XPath returns ErrNoElementFound for empty body", prop.ForAll(
		func(idName string) bool {
			// Create HTML with empty body
			html := `<!DOCTYPE html>
<html>
<body>
</body>
</html>`

			// Parse with HTMLParser using any ID in XPath
			xpath := fmt.Sprintf(`//span[@id="%s"]`, idName)
			parser := &HTMLParser{XPath: xpath}
			_, err := parser.Parse([]byte(html))

			// Should return ErrNoElementFound
			return errors.Is(err, ErrNoElementFound)
		},
		genSimpleClassName(),
	))

	properties.TestingRun(t)
}

// =============================================================================
// Unit Tests - Error Cases
// =============================================================================

// TestHTMLParserNoSelectorOrXPath tests error when neither is provided
func TestHTMLParserNoSelectorOrXPath(t *testing.T) {
	html := []byte(`<html><body><div>content</div></body></html>`)
	parser := &HTMLParser{}

	_, err := parser.Parse(html)
	if err == nil {
		t.Error("Expected error when neither selector nor xpath is provided")
	}
	if !errors.Is(err, ErrNoSelectorOrXPath) {
		t.Errorf("Expected ErrNoSelectorOrXPath, got: %v", err)
	}
}

// TestHTMLParserEmptyResult tests error when result is empty after trimming
func TestHTMLParserEmptyResult(t *testing.T) {
	html := []byte(`<html><body><div class="version">   </div></body></html>`)
	parser := &HTMLParser{Selector: ".version"}

	_, err := parser.Parse(html)
	if err == nil {
		t.Error("Expected error for empty result")
	}
	if !errors.Is(err, ErrNoVersionFound) {
		t.Errorf("Expected ErrNoVersionFound, got: %v", err)
	}
}

// =============================================================================
// Unit Tests - NewHTMLParser Factory
// =============================================================================

// TestNewHTMLParserWithSelector tests creating parser with selector
func TestNewHTMLParserWithSelector(t *testing.T) {
	parser, err := NewHTMLParser(".version", "", "")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if parser.Selector != ".version" {
		t.Errorf("Expected selector '.version', got %q", parser.Selector)
	}
}

// TestNewHTMLParserWithXPath tests creating parser with XPath
func TestNewHTMLParserWithXPath(t *testing.T) {
	parser, err := NewHTMLParser("", `//div[@class="version"]`, "")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if parser.XPath != `//div[@class="version"]` {
		t.Errorf("Expected xpath '//div[@class=\"version\"]', got %q", parser.XPath)
	}
}

// TestNewHTMLParserWithRegex tests creating parser with regex
func TestNewHTMLParserWithRegex(t *testing.T) {
	parser, err := NewHTMLParser(".version", "", `v([0-9.]+)`)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if parser.Regex != `v([0-9.]+)` {
		t.Errorf("Expected regex 'v([0-9.]+)', got %q", parser.Regex)
	}
	if parser.compiled == nil {
		t.Error("Expected compiled regex to be set")
	}
}

// TestNewHTMLParserNoSelectorOrXPath tests error when neither is provided
func TestNewHTMLParserNoSelectorOrXPath(t *testing.T) {
	_, err := NewHTMLParser("", "", "")
	if err == nil {
		t.Error("Expected error when neither selector nor xpath is provided")
	}
	if !errors.Is(err, ErrNoSelectorOrXPath) {
		t.Errorf("Expected ErrNoSelectorOrXPath, got: %v", err)
	}
}

// TestNewHTMLParserInvalidRegex tests error on invalid regex
func TestNewHTMLParserInvalidRegex(t *testing.T) {
	_, err := NewHTMLParser(".version", "", `[invalid`)
	if err == nil {
		t.Error("Expected error for invalid regex")
	}
	if !errors.Is(err, ErrInvalidRegexPattern) {
		t.Errorf("Expected ErrInvalidRegexPattern, got: %v", err)
	}
}

// =============================================================================
// Unit Tests - Integration with ParseVersion
// =============================================================================

// TestParseVersionHTML tests ParseVersion with HTML config
func TestParseVersionHTML(t *testing.T) {
	content := []byte(`<html><body><div class="version">1.0.0</div></body></html>`)
	cfg := &PackageConfig{
		Parser:   "html",
		Selector: ".version",
	}

	result, err := ParseVersion(content, cfg)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result != "1.0.0" {
		t.Errorf("Expected '1.0.0', got %q", result)
	}
}

// TestParseVersionHTMLWithXPath tests ParseVersion with HTML XPath config
func TestParseVersionHTMLWithXPath(t *testing.T) {
	content := []byte(`<html><body><span id="ver">2.0.0</span></body></html>`)
	cfg := &PackageConfig{
		Parser: "html",
		XPath:  `//*[@id="ver"]`,
	}

	result, err := ParseVersion(content, cfg)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result != "2.0.0" {
		t.Errorf("Expected '2.0.0', got %q", result)
	}
}

// TestParseVersionHTMLWithRegex tests ParseVersion with HTML and regex
func TestParseVersionHTMLWithRegex(t *testing.T) {
	content := []byte(`<html><body><div class="version">Version: v3.0.0</div></body></html>`)
	cfg := &PackageConfig{
		Parser:   "html",
		Selector: ".version",
		Pattern:  `v([0-9]+\.[0-9]+\.[0-9]+)`,
	}

	result, err := ParseVersion(content, cfg)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result != "3.0.0" {
		t.Errorf("Expected '3.0.0', got %q", result)
	}
}

// TestParseVersionHTMLFallback tests ParseVersion with HTML fallback
func TestParseVersionHTMLFallback(t *testing.T) {
	// Content that doesn't match JSON but matches HTML
	content := []byte(`<html><body><div class="version">4.0.0</div></body></html>`)
	cfg := &PackageConfig{
		Parser:         "json",
		Path:           "version",
		FallbackParser: "html",
		Selector:       ".version",
	}

	result, err := ParseVersion(content, cfg)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if result != "4.0.0" {
		t.Errorf("Expected '4.0.0', got %q", result)
	}
}
