// Package autoupdate provides version history extraction functionality for ebuild autoupdate.
package autoupdate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/antchfx/htmlquery"
)

// MaxVersionHistoryLimit is the maximum number of versions to extract from history.
// Per Requirement 9.3, version history is limited to 10 versions.
const MaxVersionHistoryLimit = 10

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

	// Limit to MaxVersionHistoryLimit
	if len(versions) > MaxVersionHistoryLimit {
		versions = versions[:MaxVersionHistoryLimit]
	}

	return versions, nil
}

// extractVersionsFromPath extracts versions from JSON data using the configured path.
func (e *JSONVersionHistoryExtractor) extractVersionsFromPath(data interface{}) ([]string, error) {
	path := e.VersionsPath

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
			if len(versions) >= MaxVersionHistoryLimit {
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
		if len(versions) >= MaxVersionHistoryLimit {
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

	var versions []string
	selection.Each(func(i int, s *goquery.Selection) {
		if len(versions) >= MaxVersionHistoryLimit {
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

	var versions []string
	for _, node := range nodes {
		if len(versions) >= MaxVersionHistoryLimit {
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
