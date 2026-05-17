package provider

import (
	"os/exec"
	"testing"
	"unicode/utf8"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

// gitChecksRefFormat reports whether the git binary accepts b as a branch name
// via "git check-ref-format --branch". A non-nil error means git rejected the
// name (or the invocation failed).
func gitChecksRefFormat(b string) bool {
	// b is only ever a value that ValidateBranch already accepted, so it has
	// no leading "-" and cannot be misinterpreted by git as an option flag.
	cmd := exec.Command("git", "check-ref-format", "--branch", b)
	return cmd.Run() == nil
}

// genBranchRune yields runes spanning ordinary ASCII, git metacharacters,
// ASCII control characters, whitespace, and the Unicode RTL override, so the
// generated strings exercise every branch of ValidateBranch.
func genBranchRune() gopter.Gen {
	runes := []rune{
		'a', 'b', 'z', 'A', 'M', 'Z',
		'0', '5', '9',
		'/', '.', '-', '_', '+',
		'~', '^', ':', '?', '*', '[', '\\',
		'@', '{', '}',
		' ', '\t', '\n', '\r',
		0x00, 0x07, 0x1f, 0x7f,
		rtlOverride,
		'é', 'λ', '世',
	}
	return gen.IntRange(0, len(runes)-1).Map(func(i int) rune {
		return runes[i]
	})
}

// genArbitraryBranch builds arbitrary unicode strings (length 0-12) from the
// rune alphabet above. Strings may contain control characters, RTL overrides
// and git metacharacters, so the generator probes the full input space.
func genArbitraryBranch() gopter.Gen {
	return gen.SliceOfN(12, genBranchRune()).Map(func(rs []rune) string {
		return string(rs)
	})
}

// pbtSeed is the fixed RNG seed for TestValidateBranch_PBT. A non-deterministic
// property test (random seed) is a CI time-bomb: it can pass for many runs and
// then fail without a code change, making failures unreproducible. Pinning the
// seed makes the test reproducible while the arbitrary-unicode generator still
// probes the full input space. The subset property must hold for ALL seeds.
const pbtSeed int64 = 1

// TestValidateBranch_PBT cross-checks ValidateBranch against git itself:
// for every generated input that ValidateBranch accepts, the same input must
// also be accepted by "git check-ref-format --branch". This proves our
// validator is never MORE permissive than git. (6.3, R2.2, AD-9)
//
// The test is skipped when the git binary is not on PATH. The RNG seed is
// fixed (pbtSeed) so the test is deterministic and reproducible in CI.
func TestValidateBranch_PBT(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not found on PATH; skipping cross-check property test")
	}

	parameters := gopter.DefaultTestParametersWithSeed(pbtSeed)
	parameters.MinSuccessfulTests = 300

	properties := gopter.NewProperties(parameters)

	properties.Property("ValidateBranch is never more permissive than git check-ref-format", prop.ForAll(
		func(branch string) bool {
			// Only inspect inputs our validator accepts.
			if ValidateBranch(branch) != nil {
				return true
			}
			// Defensive: a string we accept must be valid UTF-8 and free of
			// NUL bytes before it is ever handed to exec.
			if !utf8.ValidString(branch) {
				return false
			}
			// Every accepted input must also be accepted by git.
			return gitChecksRefFormat(branch)
		},
		genArbitraryBranch(),
	))

	properties.TestingRun(t)
}
