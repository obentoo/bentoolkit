// Package autoupdate provides version history extraction functionality for ebuild autoupdate.
package autoupdate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/antchfx/htmlquery"
)

// MaxVersionHistoryLimit is the maximum number of versions to extract from history.
// Per Requirement 9.3, version history is limited to 10 versions.
const MaxVersionHistoryLimit = 10

// effectiveLimit resolves an extractor's Limit field to a concrete cap, using the
// convention shared by every VersionHistoryExtractor:
//   - 0  → default to MaxVersionHistoryLimit (preserves historic behavior);
//   - <0 → unlimited (used by the "select" path, which must see the full list so
//     select="max" is not defeated by truncation of an ascending list);
//   - >0 → that exact value.
// A returned value <= 0 means "no cap": loops gate on `lim > 0 && len >= lim`.
func effectiveLimit(limit int) int {
	if limit == 0 {
		return MaxVersionHistoryLimit
	}
	return limit
}

// VersionHistoryExtractor defines the interface for extracting version history.
type VersionHistoryExtractor interface {
	// ExtractVersions extracts a list of versions from content.
	// Returns at most MaxVersionHistoryLimit versions.
	ExtractVersions(content []byte) ([]string, error)
}

// JSONVersionHistoryExtractor extracts version history using a JSON path.
// The path should point to an array of version strings or objects with version fields.
type JSONVersionHistoryExtractor struct {
	// VersionsPath is the JSON path to the version array (e.g., "[*].tag_name")
	VersionsPath string
	// Limit caps the number of versions returned. See effectiveLimit for the
	// 0/<0/>0 convention. Zero value keeps the historic 10-version cap.
	Limit int
}

// ExtractVersions extracts version history from JSON content using the configured path.
// Returns at most MaxVersionHistoryLimit versions.
func (e *JSONVersionHistoryExtractor) ExtractVersions(content []byte) ([]string, error) {
	if e.VersionsPath == "" {
		return nil, ErrInvalidJSONPath
	}

	// Parse JSON into generic interface
	var data interface{}
	if err := json.Unmarshal(content, &data); err != nil {
		return nil, fmt.Errorf("failed to parse JSON: %w", err)
	}

	// Handle wildcard path [*].field
	versions, err := e.extractVersionsFromPath(data)
	if err != nil {
		return nil, err
	}

	// Apply the configured cap (0 → default 10; <0 → unlimited).
	if lim := effectiveLimit(e.Limit); lim > 0 && len(versions) > lim {
		versions = versions[:lim]
	}

	return versions, nil
}

// extractVersionsFromPath extracts versions from JSON data using the configured path.
func (e *JSONVersionHistoryExtractor) extractVersionsFromPath(data interface{}) ([]string, error) {
	path := e.VersionsPath
	lim := effectiveLimit(e.Limit)

	// Handle wildcard array path: [*].field or [*]
	if strings.HasPrefix(path, "[*]") {
		arr, ok := data.([]interface{})
		if !ok {
			return nil, fmt.Errorf("%w: expected array for [*] path", ErrJSONPathNotFound)
		}

		// Get the remaining path after [*]
		remainingPath := strings.TrimPrefix(path, "[*]")
		remainingPath = strings.TrimPrefix(remainingPath, ".")

		var versions []string
		for _, item := range arr {
			var version string

			if remainingPath == "" {
				// Direct array of versions
				version, ok = toString(item)
				if !ok {
					continue // Skip non-string items
				}
			} else {
				// Navigate to nested field
				result, navErr := navigateJSONPath(item, remainingPath)
				if navErr != nil {
					continue // Skip items where path doesn't exist
				}
				version, ok = toString(result)
				if !ok {
					continue // Skip non-string values
				}
			}

			versions = append(versions, version)

			// Stop if we have enough versions
			if lim > 0 && len(versions) >= lim {
				break
			}
		}

		if len(versions) == 0 {
			return nil, fmt.Errorf("%w: no versions found at path", ErrJSONPathNotFound)
		}

		return versions, nil
	}

	// Handle regular path that points to an array
	result, err := navigateJSONPath(data, path)
	if err != nil {
		return nil, err
	}

	// Result should be an array
	arr, ok := result.([]interface{})
	if !ok {
		return nil, fmt.Errorf("%w: expected array at path", ErrJSONPathNotFound)
	}

	var versions []string
	for _, item := range arr {
		version, ok := toString(item)
		if !ok {
			continue // Skip non-string items
		}
		if version != "" {
			versions = append(versions, version)
		}
		if lim > 0 && len(versions) >= lim {
			break
		}
	}

	if len(versions) == 0 {
		return nil, fmt.Errorf("%w: no versions found in array", ErrJSONPathNotFound)
	}

	return versions, nil
}

// HTMLVersionHistoryExtractor extracts version history using CSS selector.
// The selector should match multiple elements containing version strings.
type HTMLVersionHistoryExtractor struct {
	// VersionsSelector is the CSS selector for version elements
	VersionsSelector string
	// Regex is an optional regex pattern to apply to each extracted text
	Regex string
	// Limit caps the number of versions returned (see effectiveLimit).
	Limit int
}

// ExtractVersions extracts version history from HTML content using the configured selector.
// Returns at most MaxVersionHistoryLimit versions.
func (e *HTMLVersionHistoryExtractor) ExtractVersions(content []byte) ([]string, error) {
	if e.VersionsSelector == "" {
		return nil, ErrNoSelectorOrXPath
	}

	// Parse HTML document
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(content))
	if err != nil {
		return nil, fmt.Errorf("failed to parse HTML: %w", err)
	}

	// Find all elements matching selector
	selection := doc.Find(e.VersionsSelector)
	if selection.Length() == 0 {
		return nil, fmt.Errorf("%w: %s", ErrNoElementFound, e.VersionsSelector)
	}

	lim := effectiveLimit(e.Limit)
	var versions []string
	selection.Each(func(i int, s *goquery.Selection) {
		if lim > 0 && len(versions) >= lim {
			return
		}

		text := strings.TrimSpace(s.Text())
		if text == "" {
			return
		}

		// Apply regex if configured
		if e.Regex != "" {
			parser := &HTMLParser{Regex: e.Regex}
			extracted, err := parser.applyRegex(text)
			if err == nil && extracted != "" {
				text = extracted
			}
		}

		if text != "" {
			versions = append(versions, text)
		}
	})

	if len(versions) == 0 {
		return nil, fmt.Errorf("%w: no versions found", ErrNoVersionFound)
	}

	return versions, nil
}

// XPathVersionHistoryExtractor extracts version history using XPath expression.
// The xpath should match multiple nodes containing version strings.
type XPathVersionHistoryExtractor struct {
	// VersionsXPath is the XPath expression for version nodes
	VersionsXPath string
	// Regex is an optional regex pattern to apply to each extracted text
	Regex string
	// Limit caps the number of versions returned (see effectiveLimit).
	Limit int
}

// ExtractVersions extracts version history from HTML content using the configured XPath.
// Returns at most MaxVersionHistoryLimit versions.
func (e *XPathVersionHistoryExtractor) ExtractVersions(content []byte) ([]string, error) {
	if e.VersionsXPath == "" {
		return nil, ErrNoSelectorOrXPath
	}

	// Parse HTML document
	doc, err := htmlquery.Parse(bytes.NewReader(content))
	if err != nil {
		return nil, fmt.Errorf("failed to parse HTML: %w", err)
	}

	// Find all nodes matching XPath
	nodes, err := htmlquery.QueryAll(doc, e.VersionsXPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidXPath, err)
	}

	if len(nodes) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrNoElementFound, e.VersionsXPath)
	}

	lim := effectiveLimit(e.Limit)
	var versions []string
	for _, node := range nodes {
		if lim > 0 && len(versions) >= lim {
			break
		}

		text := strings.TrimSpace(htmlquery.InnerText(node))
		if text == "" {
			continue
		}

		// Apply regex if configured
		if e.Regex != "" {
			parser := &HTMLParser{Regex: e.Regex}
			extracted, err := parser.applyRegex(text)
			if err == nil && extracted != "" {
				text = extracted
			}
		}

		if text != "" {
			versions = append(versions, text)
		}
	}

	if len(versions) == 0 {
		return nil, fmt.Errorf("%w: no versions found", ErrNoVersionFound)
	}

	return versions, nil
}

// RegexVersionHistoryExtractor extracts version history using a regex pattern.
// Every match of the first capture group becomes a candidate version. This is the
// list-extraction counterpart of RegexParser, used by the "select" path so a
// pattern like `gn-([0-9.]+)\.tar\.xz` yields every version on the page.
type RegexVersionHistoryExtractor struct {
	// Pattern is the regex pattern with at least one capture group.
	Pattern string
	// Limit caps the number of versions returned (see effectiveLimit).
	Limit int
	// compiled is the compiled regex (cached after first use).
	compiled *regexp.Regexp
}

// ExtractVersions extracts every first-capture-group match from content.
// It compiles the pattern lazily so the extractor is safe to use as the entry
// point (RegexParser.Parse compiles on its own path).
func (e *RegexVersionHistoryExtractor) ExtractVersions(content []byte) ([]string, error) {
	if e.Pattern == "" {
		return nil, ErrInvalidRegexPattern
	}
	if e.compiled == nil {
		re, err := regexp.Compile(e.Pattern)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidRegexPattern, err)
		}
		if re.NumSubexp() < 1 {
			return nil, ErrNoCaptureGroup
		}
		e.compiled = re
	}

	lim := effectiveLimit(e.Limit)
	all := e.compiled.FindAllSubmatch(content, -1)
	versions := make([]string, 0, len(all))
	for _, m := range all {
		if len(m) >= 2 && len(m[1]) > 0 {
			versions = append(versions, string(m[1]))
			if lim > 0 && len(versions) >= lim {
				break
			}
		}
	}

	if len(versions) == 0 {
		return nil, ErrRegexNoMatch
	}
	return versions, nil
}

// newSelectExtractor builds a list extractor for the "select" path, dispatching
// on cfg.Parser and reusing the primary path/pattern/selector fields. The cap is
// disabled (Limit=-1) so select="max" sees the whole list and is not defeated by
// truncation of an ascending list. Returns (nil, nil) when the parser cannot
// produce a list (e.g. "script"); callers then fall back to first-match behavior.
func newSelectExtractor(cfg *PackageConfig) (VersionHistoryExtractor, error) {
	switch cfg.Parser {
	case "json":
		// JSONVersionHistoryExtractor walks an array; a primary path like
		// "[0].name" selects one element, so map it to the wildcard form
		// "[*].name" (or bare "[*]") to collect every element's field.
		path := cfg.Path
		switch {
		case path == "" || path == "[0]":
			path = "[*]"
		case strings.HasPrefix(path, "[0]."):
			path = "[*]." + strings.TrimPrefix(path, "[0].")
		case strings.HasPrefix(path, "[*]"):
			// already a wildcard path
		default:
			// A non-indexed path (e.g. "tags") is assumed to point at an array.
		}
		return &JSONVersionHistoryExtractor{VersionsPath: path, Limit: -1}, nil
	case "regex":
		return &RegexVersionHistoryExtractor{Pattern: cfg.Pattern, Limit: -1}, nil
	case "html":
		if cfg.XPath != "" {
			return &XPathVersionHistoryExtractor{VersionsXPath: cfg.XPath, Regex: cfg.Pattern, Limit: -1}, nil
		}
		return &HTMLVersionHistoryExtractor{VersionsSelector: cfg.Selector, Regex: cfg.Pattern, Limit: -1}, nil
	default:
		return nil, nil // not list-capable (e.g. "script")
	}
}

// NewVersionHistoryExtractor creates a version history extractor from a PackageConfig.
// It uses VersionsPath for JSON parser or VersionsSelector for HTML parser.
func NewVersionHistoryExtractor(cfg *PackageConfig) (VersionHistoryExtractor, error) {
	// Check if version history is configured
	if cfg.VersionsPath == "" && cfg.VersionsSelector == "" {
		return nil, nil // No version history configured
	}

	// Use VersionsPath for JSON-based extraction
	if cfg.VersionsPath != "" {
		return &JSONVersionHistoryExtractor{
			VersionsPath: cfg.VersionsPath,
		}, nil
	}

	// Use VersionsSelector for HTML-based extraction
	if cfg.VersionsSelector != "" {
		return &HTMLVersionHistoryExtractor{
			VersionsSelector: cfg.VersionsSelector,
			Regex:            cfg.Pattern,
		}, nil
	}

	return nil, nil
}

// ExtractVersionHistory extracts version history from content using the configured extractor.
// Returns nil if no version history is configured.
func ExtractVersionHistory(content []byte, cfg *PackageConfig) ([]string, error) {
	extractor, err := NewVersionHistoryExtractor(cfg)
	if err != nil {
		return nil, err
	}

	if extractor == nil {
		return nil, nil // No version history configured
	}

	return extractor.ExtractVersions(content)
}

// HasVersionHistoryConfig checks if a PackageConfig has version history configuration.
func HasVersionHistoryConfig(cfg *PackageConfig) bool {
	return cfg != nil && (cfg.VersionsPath != "" || cfg.VersionsSelector != "")
}
