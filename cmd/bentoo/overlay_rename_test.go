package main

import (
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

// TestCommandParsingCorrectness tests Property 1: Command Parsing Correctness
// **Feature: ebuild-rename, Property 1: Command Parsing Correctness**
// **Validates: Requirements 1.1, 1.3**
//
// For any valid command string in the format <category>:<package-pattern>:<old-version> => <new-version>,
// the parser should correctly extract all four components.
func TestCommandParsingCorrectness(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100

	properties := gopter.NewProperties(parameters)

	properties.Property("valid commands parse correctly", prop.ForAll(
		func(category, pattern, oldVer, newVer string) bool {
			// Build args array as the command would receive them
			args := []string{
				category + ":" + pattern + ":" + oldVer,
				"=>",
				newVer,
			}

			// Parse the arguments
			spec, err := ParseRenameArgs(args)
			if err != nil {
				return false
			}

			// Verify all components extracted correctly
			return spec.Category == category &&
				spec.PackagePattern == pattern &&
				spec.OldVersion == oldVer &&
				spec.NewVersion == newVer
		},
		genCategory(),
		genPackagePattern(),
		genVersion(),
		genVersion(),
	))

	properties.TestingRun(t)
}

// TestInvalidCommandRejection tests Property 2: Invalid Command Rejection
// **Feature: ebuild-rename, Property 2: Invalid Command Rejection**
// **Validates: Requirements 1.4**
//
// For any command string that does not match the expected format,
// the parser should reject it and return an error.
func TestInvalidCommandRejection(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100

	properties := gopter.NewProperties(parameters)

	// Test missing separator
	properties.Property("missing separator is rejected", prop.ForAll(
		func(category, pattern, oldVer, newVer, wrongSep string) bool {
			if wrongSep == "=>" {
				return true // Skip valid separator
			}
			args := []string{
				category + ":" + pattern + ":" + oldVer,
				wrongSep,
				newVer,
			}
			_, err := ParseRenameArgs(args)
			return err != nil
		},
		genCategory(),
		genPackagePattern(),
		genVersion(),
		genVersion(),
		genWrongSeparator(),
	))

	// Test missing colon components
	properties.Property("missing colon components is rejected", prop.ForAll(
		func(category, pattern, newVer string) bool {
			// Only one colon instead of two
			args := []string{
				category + ":" + pattern, // Missing :old-version
				"=>",
				newVer,
			}
			_, err := ParseRenameArgs(args)
			return err != nil
		},
		genCategory(),
		genPackagePattern(),
		genVersion(),
	))

	// Test wrong argument count
	properties.Property("wrong argument count is rejected", prop.ForAll(
		func(category, pattern, oldVer string) bool {
			// Only 2 arguments instead of 3
			args := []string{
				category + ":" + pattern + ":" + oldVer,
				"=>",
			}
			_, err := ParseRenameArgs(args)
			return err != nil
		},
		genCategory(),
		genPackagePattern(),
		genVersion(),
	))

	properties.TestingRun(t)
}

// TestParseRenameArgsUnit provides unit tests for specific edge cases
func TestParseRenameArgsUnit(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantErr     bool
		errContains string
		wantSpec    *struct {
			category, pattern, oldVer, newVer string
		}
	}{
		{
			name:    "valid simple command",
			args:    []string{"media-plugins:gst-*:1.24.11", "=>", "1.26.10"},
			wantErr: false,
			wantSpec: &struct {
				category, pattern, oldVer, newVer string
			}{"media-plugins", "gst-*", "1.24.11", "1.26.10"},
		},
		{
			name:    "valid global search",
			args:    []string{"*:python-*:3.11.0", "=>", "3.12.0"},
			wantErr: false,
			wantSpec: &struct {
				category, pattern, oldVer, newVer string
			}{"*", "python-*", "3.11.0", "3.12.0"},
		},
		{
			name:    "valid exact package name",
			args:    []string{"app-misc:hello:1.0", "=>", "2.0"},
			wantErr: false,
			wantSpec: &struct {
				category, pattern, oldVer, newVer string
			}{"app-misc", "hello", "1.0", "2.0"},
		},
		{
			name:        "missing separator",
			args:        []string{"media-plugins:gst-*:1.24.11", "->", "1.26.10"},
			wantErr:     true,
			errContains: "must be '=>'",
		},
		{
			name:        "wrong argument count - too few",
			args:        []string{"media-plugins:gst-*:1.24.11", "=>"},
			wantErr:     true,
			errContains: "invalid argument count",
		},
		{
			name:        "wrong argument count - too many",
			args:        []string{"media-plugins:gst-*:1.24.11", "=>", "1.26.10", "extra"},
			wantErr:     true,
			errContains: "invalid argument count",
		},
		{
			name:        "missing colon in spec",
			args:        []string{"media-plugins:gst-*", "=>", "1.26.10"},
			wantErr:     true,
			errContains: "must be in format",
		},
		{
			name:        "empty category",
			args:        []string{":gst-*:1.24.11", "=>", "1.26.10"},
			wantErr:     true,
			errContains: "category cannot be empty",
		},
		{
			name:        "empty package pattern",
			args:        []string{"media-plugins::1.24.11", "=>", "1.26.10"},
			wantErr:     true,
			errContains: "package pattern cannot be empty",
		},
		{
			name:        "empty old version",
			args:        []string{"media-plugins:gst-*:", "=>", "1.26.10"},
			wantErr:     true,
			errContains: "old version cannot be empty",
		},
		{
			name:        "empty new version",
			args:        []string{"media-plugins:gst-*:1.24.11", "=>", ""},
			wantErr:     true,
			errContains: "new version cannot be empty",
		},
		{
			name:        "whitespace only category",
			args:        []string{"   :gst-*:1.24.11", "=>", "1.26.10"},
			wantErr:     true,
			errContains: "category cannot be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec, err := ParseRenameArgs(tt.args)

			if tt.wantErr {
				if err == nil {
					t.Errorf("ParseRenameArgs() expected error containing %q, got nil", tt.errContains)
					return
				}
				if tt.errContains != "" && !contains(err.Error(), tt.errContains) {
					t.Errorf("ParseRenameArgs() error = %v, want error containing %q", err, tt.errContains)
				}
				return
			}

			if err != nil {
				t.Errorf("ParseRenameArgs() unexpected error = %v", err)
				return
			}

			if tt.wantSpec != nil {
				if spec.Category != tt.wantSpec.category {
					t.Errorf("Category = %q, want %q", spec.Category, tt.wantSpec.category)
				}
				if spec.PackagePattern != tt.wantSpec.pattern {
					t.Errorf("PackagePattern = %q, want %q", spec.PackagePattern, tt.wantSpec.pattern)
				}
				if spec.OldVersion != tt.wantSpec.oldVer {
					t.Errorf("OldVersion = %q, want %q", spec.OldVersion, tt.wantSpec.oldVer)
				}
				if spec.NewVersion != tt.wantSpec.newVer {
					t.Errorf("NewVersion = %q, want %q", spec.NewVersion, tt.wantSpec.newVer)
				}
			}
		})
	}
}

// contains checks if s contains substr
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && searchString(s, substr)))
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// Generators for property-based testing

// genCategory generates valid category names
func genCategory() gopter.Gen {
	return gen.OneGenOf(
		// Specific category names
		gen.RegexMatch(`[a-z]{3,10}-[a-z]{3,10}`),
		// Wildcard for global search
		gen.Const("*"),
	)
}

// genPackagePattern generates valid package patterns
func genPackagePattern() gopter.Gen {
	return gen.OneGenOf(
		// Pattern with wildcard: prefix-*
		gen.RegexMatch(`[a-z]{3,8}-\*`),
		// Pattern with wildcard: prefix*
		gen.RegexMatch(`[a-z]{3,8}\*`),
		// Exact package name
		gen.RegexMatch(`[a-z]{3,10}`),
		// Package with hyphen
		gen.RegexMatch(`[a-z]{3,6}-[a-z]{3,6}`),
	)
}

// genVersion generates valid Gentoo version strings
func genVersion() gopter.Gen {
	return gen.OneGenOf(
		// Simple version: 1.0, 2.1, etc.
		gen.RegexMatch(`[1-9]\.[0-9]`),
		// Three-part version: 1.0.0, 2.1.3, etc.
		gen.RegexMatch(`[1-9]\.[0-9]+\.[0-9]+`),
		// Version with patch: 1.24.11
		gen.RegexMatch(`[1-9]\.[0-9]{1,2}\.[0-9]{1,2}`),
	)
}

// genWrongSeparator generates invalid separators (not "=>")
func genWrongSeparator() gopter.Gen {
	return gen.OneGenOf(
		gen.Const("->"),
		gen.Const("="),
		gen.Const(">"),
		gen.Const(">>"),
		gen.Const("=="),
		gen.Const(":"),
		gen.Const(""),
	)
}
