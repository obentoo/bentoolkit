package autoupdate

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

// =============================================================================
// R8 property-based tests for validatePattern (Task T7).
//
// Properties:
//  1. Every oversize pattern (> MaxPatternLen) is rejected.
//  2. Every backreference-bearing pattern is rejected.
//  3. Every valid, bounded RE2 pattern is accepted.
// =============================================================================

// genOversizePattern generates patterns strictly longer than MaxPatternLen.
// The body is a run of 'a' characters, which on its own compiles fine — so the
// only reason validatePattern can reject it is the length guard.
func genOversizePattern() gopter.Gen {
	return gen.IntRange(1, 256).Map(func(extra int) string {
		return strings.Repeat("a", MaxPatternLen+extra)
	})
}

// genBackrefPattern generates short, otherwise-valid patterns that embed a
// backreference \1..\9. The surrounding literal text is drawn from a safe
// alphabet that never itself introduces a backslash.
func genBackrefPattern() gopter.Gen {
	return gopter.CombineGens(
		gen.RegexMatch(`[a-z]{0,8}`),
		gen.IntRange(1, 9),
		gen.RegexMatch(`[a-z]{0,8}`),
	).Map(func(vals []interface{}) string {
		prefix := vals[0].(string)
		digit := vals[1].(int)
		suffix := vals[2].(string)
		return prefix + `\` + string(rune('0'+digit)) + suffix
	})
}

// genValidBoundedPattern generates patterns that always compile under RE2 and
// stay well under MaxPatternLen. The alphabet is restricted to literal letters,
// digits, dots and the safe quantifiers/anchors '+', '*', '^', '$' — it never
// emits a backslash, so a generated value can never be a backreference, and the
// quantifiers only ever follow a literal, so the result always compiles.
func genValidBoundedPattern() gopter.Gen {
	return gen.RegexMatch(`\^?[a-z0-9](\.?[a-z0-9][+*]?){0,30}\$?`)
}

// TestValidatePatternProperties runs the three R8 properties for validatePattern.
func TestValidatePatternProperties(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 200
	properties := gopter.NewProperties(parameters)

	properties.Property("every oversize pattern is rejected", prop.ForAll(
		func(pattern string) bool {
			if len(pattern) <= MaxPatternLen {
				return false // generator invariant violated
			}
			err := validatePattern(pattern)
			return err != nil && errors.Is(err, ErrInvalidPattern)
		},
		genOversizePattern(),
	))

	properties.Property("every backreference pattern is rejected", prop.ForAll(
		func(pattern string) bool {
			err := validatePattern(pattern)
			if err == nil || !errors.Is(err, ErrInvalidPattern) {
				return false
			}
			return strings.Contains(err.Error(), "backreferences not supported")
		},
		genBackrefPattern(),
	))

	properties.Property("every valid bounded RE2 pattern is accepted", prop.ForAll(
		func(pattern string) bool {
			// Generator invariant: bounded length, compiles, no backreference.
			if len(pattern) > MaxPatternLen {
				return false
			}
			if _, err := regexp.Compile(pattern); err != nil {
				return false
			}
			return validatePattern(pattern) == nil
		},
		genValidBoundedPattern(),
	))

	properties.TestingRun(t)
}
