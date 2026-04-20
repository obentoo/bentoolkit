package overlay

import (
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

// TestPatternSafetyValidation tests Property 3: Pattern Safety Validation
// **Feature: ebuild-rename, Property 3: Pattern Safety Validation**
// **Validates: Requirements 2.1, 2.2**
//
// For any pattern with < 3 chars or no complete token before wildcard,
// validator should reject it as unsafe.
func TestPatternSafetyValidation(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100

	properties := gopter.NewProperties(parameters)
	validator := NewPatternValidator()

	// Test patterns with fewer than 3 characters before wildcard
	properties.Property("patterns with < 3 chars before wildcard are rejected", prop.ForAll(
		func(prefix string) bool {
			pattern := prefix + "*"
			err := validator.Validate(pattern)
			// Should be rejected
			return err != nil
		},
		genShortPrefix(),
	))

	// Test patterns with single character token before delimiter and wildcard
	properties.Property("patterns with single char token before delimiter are rejected", prop.ForAll(
		func(char, delim string) bool {
			pattern := char + delim + "*"
			err := validator.Validate(pattern)
			// Should be rejected (e.g., "a-*", "b_*")
			return err != nil
		},
		genSingleChar(),
		gen.OneConstOf("-", "_"),
	))

	properties.TestingRun(t)
}

// TestValidPatternAcceptance tests Property 4: Valid Pattern Acceptance
// **Feature: ebuild-rename, Property 4: Valid Pattern Acceptance**
// **Validates: Requirements 2.4, 2.5**
//
// For any pattern with no wildcards OR ≥3 chars + complete token before wildcard,
// validator should accept it as valid.
func TestValidPatternAcceptance(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100

	properties := gopter.NewProperties(parameters)
	validator := NewPatternValidator()

	// Test patterns without wildcards (always valid)
	properties.Property("patterns without wildcards are accepted", prop.ForAll(
		func(pattern string) bool {
			err := validator.Validate(pattern)
			return err == nil
		},
		genPatternWithoutWildcard(),
	))

	// Test patterns with proper prefix before wildcard
	properties.Property("patterns with ≥3 chars and complete token before wildcard are accepted", prop.ForAll(
		func(prefix string) bool {
			pattern := prefix + "*"
			err := validator.Validate(pattern)
			return err == nil
		},
		genValidPrefix(),
	))

	// Test patterns with delimiter and proper token before wildcard
	properties.Property("patterns with complete token + delimiter before wildcard are accepted", prop.ForAll(
		func(token, delim string) bool {
			pattern := token + delim + "*"
			err := validator.Validate(pattern)
			return err == nil
		},
		genValidToken(),
		gen.OneConstOf("-", "_"),
	))

	properties.TestingRun(t)
}

// TestBareAsteriskRejection tests that bare "*" pattern is rejected
// **Feature: ebuild-rename, Property 3: Pattern Safety Validation**
// **Validates: Requirements 2.3**
func TestBareAsteriskRejection(t *testing.T) {
	validator := NewPatternValidator()

	tests := []struct {
		name    string
		pattern string
		wantErr bool
	}{
		{
			name:    "bare asterisk is rejected",
			pattern: "*",
			wantErr: true,
		},
		{
			name:    "bare question mark is rejected",
			pattern: "?",
			wantErr: true,
		},
		{
			name:    "empty pattern is rejected",
			pattern: "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validator.Validate(tt.pattern)
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate(%q) error = %v, wantErr %v", tt.pattern, err, tt.wantErr)
			}
			if tt.wantErr && err != nil {
				// Verify error message is helpful
				valErr, ok := err.(*ValidationError)
				if !ok {
					t.Errorf("expected ValidationError, got %T", err)
				}
				if valErr.Pattern != tt.pattern {
					t.Errorf("ValidationError.Pattern = %q, want %q", valErr.Pattern, tt.pattern)
				}
				if valErr.Reason == "" {
					t.Error("ValidationError.Reason should not be empty")
				}
			}
		})
	}
}

// TestPatternValidatorUnit provides unit tests for specific edge cases
func TestPatternValidatorUnit(t *testing.T) {
	validator := NewPatternValidator()

	tests := []struct {
		name    string
		pattern string
		wantErr bool
	}{
		// Valid patterns
		{name: "valid: gst-*", pattern: "gst-*", wantErr: false},
		{name: "valid: python-*", pattern: "python-*", wantErr: false},
		{name: "valid: lib*", pattern: "lib*", wantErr: false},
		{name: "valid: gst-plugins-*", pattern: "gst-plugins-*", wantErr: false},
		{name: "valid: exact name", pattern: "mypackage", wantErr: false},
		{name: "valid: exact name with hyphen", pattern: "my-package", wantErr: false},
		{name: "valid: abc*", pattern: "abc*", wantErr: false},
		{name: "valid: ab-*", pattern: "ab-*", wantErr: false},
		{name: "valid: ab_*", pattern: "ab_*", wantErr: false},

		// Invalid patterns
		{name: "invalid: bare *", pattern: "*", wantErr: true},
		{name: "invalid: g*", pattern: "g*", wantErr: true},
		{name: "invalid: gs*", pattern: "gs*", wantErr: true},
		{name: "invalid: a-*", pattern: "a-*", wantErr: true},
		{name: "invalid: empty", pattern: "", wantErr: true},
		{name: "invalid: bare ?", pattern: "?", wantErr: true},
		{name: "invalid: a_*", pattern: "a_*", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validator.Validate(tt.pattern)
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate(%q) error = %v, wantErr %v", tt.pattern, err, tt.wantErr)
			}
		})
	}
}

// Generators for property-based testing

// genSingleChar generates single lowercase character strings
func genSingleChar() gopter.Gen {
	return gen.RegexMatch(`[a-z]`)
}

// genShortPrefix generates prefixes with fewer than 3 characters
func genShortPrefix() gopter.Gen {
	return gen.OneGenOf(
		gen.Const(""),
		gen.RegexMatch(`[a-z]`),
		gen.RegexMatch(`[a-z]{2}`),
	)
}

// genValidPrefix generates valid prefixes (≥3 chars, no wildcards)
func genValidPrefix() gopter.Gen {
	return gen.RegexMatch(`[a-z]{3,8}`)
}

// genValidToken generates valid tokens (≥2 chars for use with delimiter)
func genValidToken() gopter.Gen {
	return gen.RegexMatch(`[a-z]{2,6}`)
}

// genPatternWithoutWildcard generates patterns without wildcards
func genPatternWithoutWildcard() gopter.Gen {
	return gen.OneGenOf(
		// Simple name
		gen.RegexMatch(`[a-z]{3,10}`),
		// Name with hyphen
		gen.RegexMatch(`[a-z]{2,5}-[a-z]{2,5}`),
		// Name with underscore
		gen.RegexMatch(`[a-z]{2,5}_[a-z]{2,5}`),
	)
}
