// Package autoupdate provides HTML parsing functionality for ebuild autoupdate.
package autoupdate

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/antchfx/htmlquery"
)

// Error variables for HTML parser errors
var (
	// ErrInvalidSelector is returned when the CSS selector syntax is invalid
	ErrInvalidSelector = errors.New("invalid CSS selector")
	// ErrInvalidXPath is returned when the XPath expression syntax is invalid
	ErrInvalidXPath = errors.New("invalid XPath expression")
	// ErrNoElementFound is returned when no element matches the selector/xpath
	ErrNoElementFound = errors.New("no element found matching selector")
	// ErrNoSelectorOrXPath is returned when neither selector nor xpath is provided
	ErrNoSelectorOrXPath = errors.New("either selector or xpath must be provided")
)

// HTMLParser extracts version using CSS selector or XPath expression.
// It supports optional regex post-processing to extract version from matched text.
type HTMLParser struct {
	// Selector is the CSS selector for extracting version
	Selector string
	// XPath is the XPath expression for extracting version (alternative to Selector)
	XPath string
	// Regex is an optional regex pattern to apply to the extracted text
	Regex string
	// compiled is the compiled regex (cached after first use)
	compiled *regexp.Regexp
}

// Parse extracts a version string from HTML content.
// It uses CSS selector if provided, otherwise falls back to XPath.
// If Regex is configured, it applies the regex to the extracted text.
func (p *HTMLParser) Parse(content []byte) (string, error) {
	// Validate that at least one extraction method is provided
	if p.Selector == "" && p.XPath == "" {
		return "", ErrNoSelectorOrXPath
	}

	var text string
	var err error

	// Use CSS selector if provided, otherwise use XPath
	if p.Selector != "" {
		text, err = p.parseWithCSS(content)
	} else {
		text, err = p.parseWithXPath(content)
	}

	if err != nil {
		return "", err
	}

	// Apply regex post-processing if configured
	if p.Regex != "" {
		text, err = p.applyRegex(text)
		if err != nil {
			return "", err
		}
	}

	// Trim whitespace from result
	text = strings.TrimSpace(text)
	if text == "" {
		return "", ErrNoVersionFound
	}

	return text, nil
}

// parseWithCSS extracts text content using CSS selector (goquery).
// Returns the text content of the first matching element.
func (p *HTMLParser) parseWithCSS(content []byte) (string, error) {
	// Parse HTML document
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(content))
	if err != nil {
		return "", fmt.Errorf("failed to parse HTML: %w", err)
	}

	// Find elements matching selector
	selection := doc.Find(p.Selector)
	if selection.Length() == 0 {
		return "", fmt.Errorf("%w: %s", ErrNoElementFound, p.Selector)
	}

	// Return text content of first match
	text := selection.First().Text()
	return text, nil
}

// parseWithXPath extracts text content using XPath expression (htmlquery).
// Returns the text content of the first matching node.
func (p *HTMLParser) parseWithXPath(content []byte) (string, error) {
	// Parse HTML document
	doc, err := htmlquery.Parse(bytes.NewReader(content))
	if err != nil {
		return "", fmt.Errorf("failed to parse HTML: %w", err)
	}

	// Find nodes matching XPath
	nodes, err := htmlquery.QueryAll(doc, p.XPath)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidXPath, err)
	}

	if len(nodes) == 0 {
		return "", fmt.Errorf("%w: %s", ErrNoElementFound, p.XPath)
	}

	// Return text content of first match
	text := htmlquery.InnerText(nodes[0])
	return text, nil
}

// applyRegex applies the configured regex pattern to the text.
// Returns the first capture group if present, otherwise the full match.
func (p *HTMLParser) applyRegex(text string) (string, error) {
	// Compile regex if not already compiled
	if p.compiled == nil {
		re, err := regexp.Compile(p.Regex)
		if err != nil {
			return "", fmt.Errorf("%w: %v", ErrInvalidRegexPattern, err)
		}
		p.compiled = re
	}

	// Find submatch
	matches := p.compiled.FindStringSubmatch(text)
	if matches == nil {
		return "", fmt.Errorf("%w: pattern %q did not match text", ErrRegexNoMatch, p.Regex)
	}

	// Return first capture group if present, otherwise full match
	if len(matches) > 1 && matches[1] != "" {
		return matches[1], nil
	}

	// Return full match if no capture group
	if matches[0] != "" {
		return matches[0], nil
	}

	return "", fmt.Errorf("%w: pattern matched empty string", ErrRegexNoMatch)
}

// NewHTMLParser creates a new HTMLParser with the given configuration.
// At least one of selector or xpath must be provided.
func NewHTMLParser(selector, xpath, regex string) (*HTMLParser, error) {
	if selector == "" && xpath == "" {
		return nil, ErrNoSelectorOrXPath
	}

	parser := &HTMLParser{
		Selector: selector,
		XPath:    xpath,
		Regex:    regex,
	}

	// Pre-compile regex if provided
	if regex != "" {
		re, err := regexp.Compile(regex)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidRegexPattern, err)
		}
		parser.compiled = re
	}

	return parser, nil
}
