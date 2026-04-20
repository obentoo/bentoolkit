package output

import (
	"strings"
	"testing"

	"github.com/fatih/color"
	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

// **Feature: overlay-improvements, Property 1: Color output matches status type**
// **Validates: Requirements 2.1**
func TestColorOutputMatchesStatusType(t *testing.T) {
	// Ensure colors are enabled for this test
	ForceColor()
	defer NoColor()

	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Map of status types to their expected ANSI color codes
	statusColorCodes := map[string]string{
		"Added":    "\x1b[32m", // Green
		"Modified": "\x1b[33m", // Yellow
		"Deleted":  "\x1b[31m", // Red
		"Renamed":  "\x1b[36m", // Cyan
	}

	// Generator for known status types
	statusGen := gen.OneConstOf("Added", "Modified", "Deleted", "Renamed")

	properties.Property("FormatStatus contains correct ANSI code for status type", prop.ForAll(
		func(status string) bool {
			formatted := FormatStatus(status)
			expectedCode := statusColorCodes[status]
			return strings.Contains(formatted, expectedCode)
		},
		statusGen,
	))

	properties.Property("StatusColor returns non-nil color for known status", prop.ForAll(
		func(status string) bool {
			c := StatusColor(status)
			return c != nil
		},
		statusGen,
	))

	properties.Property("FormatStatus output contains the status text", prop.ForAll(
		func(status string) bool {
			formatted := FormatStatus(status)
			return strings.Contains(formatted, status)
		},
		statusGen,
	))

	properties.TestingRun(t)
}

// **Feature: overlay-improvements, Property 2: No-color flag disables ANSI codes**
// **Validates: Requirements 2.3**
func TestNoColorFlagDisablesANSICodes(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Generator for known status types
	statusGen := gen.OneConstOf("Added", "Modified", "Deleted", "Renamed", "Untracked")

	// Generator for arbitrary strings to test with Sprint/Sprintf
	stringGen := gen.AnyString()

	properties.Property("FormatStatus contains no ANSI codes when NoColor is set", prop.ForAll(
		func(status string) bool {
			NoColor()
			defer ForceColor()

			formatted := FormatStatus(status)
			// ANSI escape sequences start with \x1b[ or \033[
			return !strings.Contains(formatted, "\x1b[") && !strings.Contains(formatted, "\033[")
		},
		statusGen,
	))

	properties.Property("Sprintf contains no ANSI codes when NoColor is set", prop.ForAll(
		func(text string) bool {
			NoColor()
			defer ForceColor()

			// Test with various color types
			colors := []*color.Color{Added, Modified, Deleted, Renamed, Success, Error, Info, Warning}
			for _, c := range colors {
				result := Sprintf(c, "%s", text)
				if strings.Contains(result, "\x1b[") || strings.Contains(result, "\033[") {
					return false
				}
			}
			return true
		},
		stringGen,
	))

	properties.Property("FormatPackage contains no ANSI codes when NoColor is set", prop.ForAll(
		func(category, pkg string) bool {
			NoColor()
			defer ForceColor()

			formatted := FormatPackage(category, pkg)
			return !strings.Contains(formatted, "\x1b[") && !strings.Contains(formatted, "\033[")
		},
		gen.AlphaString(),
		gen.AlphaString(),
	))

	properties.TestingRun(t)
}
