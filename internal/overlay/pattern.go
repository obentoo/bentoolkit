// Package overlay provides business logic for overlay management operations.
package overlay

import (
	"fmt"
	"strings"
)

// PatternValidator validates package patterns for safety.
// It ensures patterns are not too broad and have sufficient specificity
// to prevent accidental bulk operations on unintended packages.
type PatternValidator struct{}

// ValidationError represents a pattern validation failure.
type ValidationError struct {
	Pattern string // The pattern that failed validation
	Reason  string // Human-readable explanation of why validation failed
}

// Error implements the error interface for ValidationError.
func (e *ValidationError) Error() string {
	return fmt.Sprintf("invalid pattern '%s': %s", e.Pattern, e.Reason)
}

// NewPatternValidator creates a new PatternValidator instance.
func NewPatternValidator() *PatternValidator {
	return &PatternValidator{}
}

// Validate checks if a package pattern is safe to use.
// It returns nil if the pattern is valid, or a ValidationError if invalid.
//
// Validation rules:
//   - Patterns without wildcards are always valid
//   - Patterns with wildcards must have at least 3 characters before the first wildcard
//   - Patterns with wildcards must have at least one complete token before the wildcard
//   - A bare "*" pattern is rejected as too broad
//
// Examples of valid patterns: "gst-*", "python-*", "lib*", "gst-plugins-*", "mypackage"
// Examples of invalid patterns: "*", "g*", "gs*", "a-*"
func (v *PatternValidator) Validate(pattern string) error {
	// Empty pattern is invalid
	if pattern == "" {
		return &ValidationError{
			Pattern: pattern,
			Reason:  "pattern cannot be empty",
		}
	}

	// Check if pattern contains wildcards
	wildcardPos := findFirstWildcard(pattern)
	if wildcardPos == -1 {
		// No wildcards - pattern is always valid (exact match)
		return nil
	}

	// Bare "*" or "?" is too broad
	if pattern == "*" || pattern == "?" {
		return &ValidationError{
			Pattern: pattern,
			Reason:  "pattern is too broad; must specify at least one complete token before wildcards",
		}
	}

	// Count characters before first wildcard
	prefixLen := countPrefixLength(pattern)
	if prefixLen < 3 {
		return &ValidationError{
			Pattern: pattern,
			Reason:  fmt.Sprintf("pattern must have at least 3 characters before wildcards (found %d)", prefixLen),
		}
	}

	// Check for complete token before wildcard
	if !isTokenComplete(pattern) {
		return &ValidationError{
			Pattern: pattern,
			Reason:  "pattern must have at least one complete token (separated by '-' or '_') before wildcards",
		}
	}

	return nil
}

// findFirstWildcard returns the position of the first wildcard character (* or ?)
// in the pattern, or -1 if no wildcard is found.
func findFirstWildcard(pattern string) int {
	for i, ch := range pattern {
		if ch == '*' || ch == '?' {
			return i
		}
	}
	return -1
}

// countPrefixLength returns the number of characters before the first wildcard.
// If no wildcard exists, returns the length of the entire pattern.
func countPrefixLength(pattern string) int {
	pos := findFirstWildcard(pattern)
	if pos == -1 {
		return len(pattern)
	}
	return pos
}

// isTokenComplete checks if the pattern has at least one complete token
// before the first wildcard. A token is a sequence of characters separated
// by '-' or '_' delimiters.
//
// Examples:
//   - "gst-*" -> true (complete token "gst" before wildcard)
//   - "python_*" -> true (complete token "python" before wildcard)
//   - "lib*" -> true (complete token "lib" before wildcard, no delimiter needed at end)
//   - "gs*" -> false (only 2 chars, but this is caught by prefix length check)
//   - "a-*" -> false (token "a" is too short to be meaningful)
func isTokenComplete(pattern string) bool {
	wildcardPos := findFirstWildcard(pattern)
	if wildcardPos == -1 {
		// No wildcard, pattern is complete
		return true
	}

	prefix := pattern[:wildcardPos]
	if len(prefix) == 0 {
		return false
	}

	// Check if prefix ends with a delimiter (meaning we have a complete token before it)
	if strings.HasSuffix(prefix, "-") || strings.HasSuffix(prefix, "_") {
		// Token before delimiter must be at least 2 characters
		// e.g., "a-*" is invalid, but "ab-*" is valid
		tokenEnd := len(prefix) - 1
		if tokenEnd < 2 {
			return false
		}
		return true
	}

	// No delimiter at end - the entire prefix is the token
	// It must be at least 3 characters (already checked by countPrefixLength)
	return len(prefix) >= 3
}
