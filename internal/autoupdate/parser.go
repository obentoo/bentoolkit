// Package autoupdate provides version parsing functionality for ebuild autoupdate.
package autoupdate

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Error variables for parser errors
var (
	// ErrJSONPathNotFound is returned when the JSON path does not exist in the document
	ErrJSONPathNotFound = errors.New("JSON path not found in response")
	// ErrRegexNoMatch is returned when the regex pattern does not match the content
	ErrRegexNoMatch = errors.New("regex pattern did not match")
	// ErrNoVersionFound is returned when no version could be extracted from upstream
	ErrNoVersionFound = errors.New("could not extract version from upstream")
	// ErrInvalidJSONPath is returned when the JSON path syntax is invalid
	ErrInvalidJSONPath = errors.New("invalid JSON path syntax")
	// ErrInvalidRegexPattern is returned when the regex pattern is invalid
	ErrInvalidRegexPattern = errors.New("invalid regex pattern")
	// ErrNoCaptureGroup is returned when the regex pattern has no capture group
	ErrNoCaptureGroup = errors.New("regex pattern must contain at least one capture group")
)

// Parser defines the interface for version extraction from content.
// Implementations extract version strings from different content formats.
type Parser interface {
	// Parse extracts a version string from the given content.
	// Returns the extracted version or an error if extraction fails.
	Parse(content []byte) (string, error)
}

// JSONParser extracts version using a JSON path.
// The path supports dot notation and array indexing (e.g., "notes[0].version").
type JSONParser struct {
	// Path is the JSON path to the version field (e.g., "notes[0].version", "tag_name")
	Path string
}

// Parse extracts a version string from JSON content using the configured path.
// Supports nested objects, array indexing, and simple field access.
func (p *JSONParser) Parse(content []byte) (string, error) {
	if p.Path == "" {
		return "", ErrInvalidJSONPath
	}

	// Parse JSON into generic interface
	var data interface{}
	if err := json.Unmarshal(content, &data); err != nil {
		return "", fmt.Errorf("failed to parse JSON: %w", err)
	}

	// Navigate the path
	result, err := navigateJSONPath(data, p.Path)
	if err != nil {
		return "", err
	}

	// Convert result to string
	version, ok := toString(result)
	if !ok {
		return "", fmt.Errorf("%w: value at path is not a string", ErrJSONPathNotFound)
	}

	return version, nil
}

// navigateJSONPath navigates through JSON data following the given path.
// Supports dot notation (field.subfield) and array indexing (field[0]).
func navigateJSONPath(data interface{}, path string) (interface{}, error) {
	if path == "" {
		return data, nil
	}

	// Parse path segments
	segments, err := parseJSONPath(path)
	if err != nil {
		return nil, err
	}

	current := data
	for _, seg := range segments {
		switch seg.segType {
		case segmentField:
			obj, ok := current.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("%w: expected object at %q", ErrJSONPathNotFound, seg.value)
			}
			val, exists := obj[seg.value]
			if !exists {
				return nil, fmt.Errorf("%w: field %q not found", ErrJSONPathNotFound, seg.value)
			}
			current = val

		case segmentIndex:
			arr, ok := current.([]interface{})
			if !ok {
				return nil, fmt.Errorf("%w: expected array at index %d", ErrJSONPathNotFound, seg.index)
			}
			if seg.index < 0 || seg.index >= len(arr) {
				return nil, fmt.Errorf("%w: array index %d out of bounds (length %d)", ErrJSONPathNotFound, seg.index, len(arr))
			}
			current = arr[seg.index]
		}
	}

	return current, nil
}

// segmentType represents the type of path segment
type segmentType int

const (
	segmentField segmentType = iota
	segmentIndex
)

// pathSegment represents a single segment in a JSON path
type pathSegment struct {
	segType segmentType
	value   string // field name for segmentField
	index   int    // array index for segmentIndex
}

// parseJSONPath parses a JSON path string into segments.
// Examples: "version", "notes[0].version", "data.releases[0].tag"
func parseJSONPath(path string) ([]pathSegment, error) {
	var segments []pathSegment
	remaining := path

	for remaining != "" {
		// Skip leading dot
		remaining = strings.TrimPrefix(remaining, ".")

		if remaining == "" {
			break
		}

		// Check for array index at start (shouldn't happen in valid paths)
		if remaining[0] == '[' {
			return nil, fmt.Errorf("%w: unexpected '[' at start", ErrInvalidJSONPath)
		}

		// Find field name (until dot, bracket, or end)
		fieldEnd := len(remaining)
		for i, c := range remaining {
			if c == '.' || c == '[' {
				fieldEnd = i
				break
			}
		}

		if fieldEnd > 0 {
			fieldName := remaining[:fieldEnd]
			if fieldName == "" {
				return nil, fmt.Errorf("%w: empty field name", ErrInvalidJSONPath)
			}
			segments = append(segments, pathSegment{segType: segmentField, value: fieldName})
			remaining = remaining[fieldEnd:]
		}

		// Check for array index
		for strings.HasPrefix(remaining, "[") {
			// Find closing bracket
			closeBracket := strings.Index(remaining, "]")
			if closeBracket == -1 {
				return nil, fmt.Errorf("%w: unclosed bracket", ErrInvalidJSONPath)
			}

			indexStr := remaining[1:closeBracket]
			index, err := strconv.Atoi(indexStr)
			if err != nil {
				return nil, fmt.Errorf("%w: invalid array index %q", ErrInvalidJSONPath, indexStr)
			}
			if index < 0 {
				return nil, fmt.Errorf("%w: negative array index", ErrInvalidJSONPath)
			}

			segments = append(segments, pathSegment{segType: segmentIndex, index: index})
			remaining = remaining[closeBracket+1:]
		}
	}

	if len(segments) == 0 {
		return nil, fmt.Errorf("%w: empty path", ErrInvalidJSONPath)
	}

	return segments, nil
}

// toString converts an interface{} to a string if possible
func toString(v interface{}) (string, bool) {
	switch val := v.(type) {
	case string:
		return val, true
	case float64:
		// JSON numbers are float64
		if val == float64(int64(val)) {
			return strconv.FormatInt(int64(val), 10), true
		}
		return strconv.FormatFloat(val, 'f', -1, 64), true
	case int:
		return strconv.Itoa(val), true
	case int64:
		return strconv.FormatInt(val, 10), true
	case bool:
		return strconv.FormatBool(val), true
	default:
		return "", false
	}
}

// RegexParser extracts version using a regular expression with capture group.
// The first capture group in the pattern is used as the version.
type RegexParser struct {
	// Pattern is the regex pattern with at least one capture group
	Pattern string
	// compiled is the compiled regex (cached after first use)
	compiled *regexp.Regexp
}

// Parse extracts a version string from content using the configured regex pattern.
// The first capture group match is returned as the version.
func (p *RegexParser) Parse(content []byte) (string, error) {
	if p.Pattern == "" {
		return "", ErrInvalidRegexPattern
	}

	// Compile regex if not already compiled
	if p.compiled == nil {
		re, err := regexp.Compile(p.Pattern)
		if err != nil {
			return "", fmt.Errorf("%w: %v", ErrInvalidRegexPattern, err)
		}
		p.compiled = re
	}

	// Check that pattern has at least one capture group
	if p.compiled.NumSubexp() < 1 {
		return "", ErrNoCaptureGroup
	}

	// Find submatch
	matches := p.compiled.FindSubmatch(content)
	if matches == nil || len(matches) < 2 {
		return "", ErrRegexNoMatch
	}

	// Return first capture group
	version := string(matches[1])
	if version == "" {
		return "", fmt.Errorf("%w: capture group matched empty string", ErrRegexNoMatch)
	}

	return version, nil
}

// NewParser creates a parser based on the specified type.
// parserType must be "json", "regex", or "html".
// pathOrPattern is the JSON path for json parser or regex pattern for regex parser.
// For HTML parser, use NewParserFromConfig instead.
func NewParser(parserType, pathOrPattern string) (Parser, error) {
	switch parserType {
	case "json":
		return &JSONParser{Path: pathOrPattern}, nil
	case "regex":
		// Validate regex pattern upfront
		re, err := regexp.Compile(pathOrPattern)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidRegexPattern, err)
		}
		if re.NumSubexp() < 1 {
			return nil, ErrNoCaptureGroup
		}
		return &RegexParser{Pattern: pathOrPattern, compiled: re}, nil
	case "html":
		// HTML parser requires selector or xpath, use NewParserFromConfig
		return nil, fmt.Errorf("%w: use NewParserFromConfig for html parser", ErrInvalidParserType)
	default:
		return nil, fmt.Errorf("%w: got %q", ErrInvalidParserType, parserType)
	}
}

// NewParserFromConfig creates a parser from a PackageConfig.
// This supports all parser types including HTML which requires additional fields.
func NewParserFromConfig(cfg *PackageConfig) (Parser, error) {
	switch cfg.Parser {
	case "json":
		return &JSONParser{Path: cfg.Path}, nil
	case "regex":
		re, err := regexp.Compile(cfg.Pattern)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidRegexPattern, err)
		}
		if re.NumSubexp() < 1 {
			return nil, ErrNoCaptureGroup
		}
		return &RegexParser{Pattern: cfg.Pattern, compiled: re}, nil
	case "html":
		return NewHTMLParser(cfg.Selector, cfg.XPath, cfg.Pattern)
	default:
		return nil, fmt.Errorf("%w: got %q", ErrInvalidParserType, cfg.Parser)
	}
}

// ParseVersion attempts to extract version using configured parsers with fallback logic.
// It tries the primary parser first, then fallback parser if configured, and returns
// the first successful result.
func ParseVersion(content []byte, cfg *PackageConfig) (string, error) {
	// Try primary parser
	parser, err := NewParserFromConfig(cfg)
	if err != nil {
		return "", fmt.Errorf("failed to create primary parser: %w", err)
	}

	version, err := parser.Parse(content)
	if err == nil {
		return version, nil
	}

	primaryErr := err

	// Try fallback parser if configured
	if cfg.FallbackParser != "" {
		fallbackCfg := &PackageConfig{
			Parser:   cfg.FallbackParser,
			Path:     cfg.Path,
			Pattern:  cfg.FallbackPattern,
			Selector: cfg.Selector,
			XPath:    cfg.XPath,
		}
		fallbackParser, err := NewParserFromConfig(fallbackCfg)
		if err != nil {
			return "", fmt.Errorf("primary parser failed (%w), fallback parser creation failed: %v", primaryErr, err)
		}

		version, err = fallbackParser.Parse(content)
		if err == nil {
			return version, nil
		}
	}

	// All parsers failed
	return "", fmt.Errorf("%w: %v", ErrNoVersionFound, primaryErr)
}
