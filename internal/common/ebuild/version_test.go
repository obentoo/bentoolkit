package ebuild

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// versionOrderCase is one ordered pair: CompareVersions(a, b) must be want, and
// CompareVersions(b, a) must be -want. Every case is asserted in both argument
// orders, because a comparator that is right one way round and wrong the other
// is what makes a sort's answer depend on its input order.
type versionOrderCase struct {
	algorithm string // the PMS algorithm (3.1 to 3.7) the case exercises, or the invalid-input rule
	a, b      string
	want      int
	why       string
}

// pmsOrderingCases pins the PMS section 3.3 ordering on VALID versions. Every
// expected value was checked against Portage's own vercmp (portage.versions)
// on 2026-09-23, in both argument orders.
//
// For every rule that says two versions are the SAME or DIFFERENT, the table
// carries both hostile halves: pairs that look alike and must stay apart, and
// pairs that look different and must come together.
func pmsOrderingCases() []versionOrderCase {
	return []versionOrderCase{
		// ---- 3.1: the whole comparison ----
		{"3.1", "1.0", "1.0", 0, "a version equals itself"},
		{"3.1", "2.0", "1.0", 1, "the first component decides when it differs"},

		// ---- 3.2: the first numeric component, as an unbounded integer ----
		{"3.2", "10.0", "9.0", 1, "integer order, not string order: 10 > 9"},
		{"3.2", "01.0", "1.0", 0, "leading zeros on the first component are ignored"},
		{"3.2", "0.1", "00.1", 0, "a first component of zeros is zero"},
		{"3.2", "10", "1", 1, "trailing-zero stripping must never reach the first component: 10 is not 1"},
		{"3.2", "100.0", "1.0", 1, "trailing-zero stripping must never reach the first component: 100 is not 1"},
		{"3.2", "99999999999999999999", "1", 1, "a component beyond int64 is still ordered by value, not coerced to 0"},
		{"3.2", "99999999999999999999", "99999999999999999998", 1, "two components beyond int64 that differ in the last digit must not collapse"},
		{"3.2", "00000000000000000000001", "1", 0, "a long digit string that is 1 after its leading zeros is 1"},
		{"3.2", "100000000000000000000", "99999999999999999999", 1, "the longer stripped digit string is the greater number"},
		{"3.2", "00009", "10", -1, "length is measured after stripping leading zeros: 9 < 10"},

		// ---- 3.3: the other numeric components ----
		{"3.3", "1.0.0", "1.0", 1, "with every shared component equal, more components is greater"},
		{"3.3", "1.0", "1", 1, "one more component, even a zero, is greater"},
		{"3.3", "1.00", "1.0.0", -1, "a component that compares equal after stripping does not end the comparison; the count still decides"},
		{"3.3", "1.01", "1.1", -1, "a component with a leading 0 compares as a string: 01 < 1"},
		{"3.3", "1.001", "1.01", -1, "string comparison after trailing zeros are stripped: 001 < 01"},
		{"3.3", "1.010", "1.01", 0, "trailing zeros are stripped from a leading-zero component: 010 reads as 01"},
		{"3.3", "1.0", "1.00", 0, "0 and 00 both strip to nothing"},
		{"3.3", "1.0.1", "1.00.1", 0, "the equality holds in a middle component too"},
		{"3.3", "1.10", "1.9", 1, "neither component starts with 0, so they compare as integers: 10 > 9"},
		{"3.3", "1.10", "1.1", 1, "without a leading 0 no trailing zero is stripped: 10 is not 1"},
		{"3.3", "1.100", "1.1", 1, "without a leading 0 no trailing zero is stripped: 100 is not 1"},
		{"3.3", "1.10", "1.01", 1, "one side leading with 0 switches both to strings: 1 > 01"},
		{"3.3", "1.99999999999999999999", "1.99999999999999999998", 1, "a non-first component beyond int64 is ordered by value"},
		{"3.3", "1.0.0", "1.0z", 1, "the component count decides before the letter is read"},

		// ---- 3.4: the trailing letter ----
		{"3.4", "1.1.1w", "1.1.1v", 1, "openssl's scheme: the letter orders by its ASCII value"},
		{"3.4", "1.0a", "1.0", 1, "a letter is greater than no letter"},
		{"3.4", "1.0a", "1.00a", 0, "equal components and the same letter are the same version"},
		{"3.4", "1.0z", "1.1", -1, "a letter never outranks a greater numeric component"},
		{"3.4", "1.0a_p1", "1.0b", -1, "the letter decides before any suffix is read"},

		// ---- 3.5 and 3.6: every suffix, in order ----
		{"3.6", "1.0_alpha1", "1.0_beta1", -1, "_alpha < _beta"},
		{"3.6", "1.0_beta1", "1.0_pre1", -1, "_beta < _pre"},
		{"3.6", "1.0_pre1", "1.0_rc1", -1, "_pre < _rc"},
		{"3.6", "1.0_rc1", "1.0_p1", -1, "_rc < _p"},
		{"3.6", "1.0_alpha99", "1.0_beta1", -1, "the suffix type decides before its number"},
		{"3.6", "1.0_rc99", "1.0_p0", -1, "the suffix type decides before its number, even against _p0"},
		{"3.6", "1.0_rc", "1.0_rc0", 0, "a missing suffix number reads as 0"},
		{"3.6", "1.0_p0", "1.0_p", 0, "a missing suffix number reads as 0, for _p too"},
		{"3.6", "1.0_rc01", "1.0_rc1", 0, "a suffix number ignores leading zeros"},
		{"3.6", "1.0_rc2", "1.0_rc10", -1, "suffix numbers compare as integers: 2 < 10"},
		{"3.6", "1.0_p20260923123456789012", "1.0_p20260923123456789011", 1, "a suffix number beyond int64 is ordered by value"},
		{"3.5", "1.0_rc1_p1", "1.0_rc1_p2", -1, "equal first suffixes hand the decision to the second pair"},
		{"3.5", "1.0_alpha1_beta2", "1.0_alpha1_beta1", 1, "equal first suffixes hand the decision to the second pair"},
		{"3.5", "1.0_rc1_p1", "1.0_rc2", -1, "a first suffix that differs decides; an extra suffix cannot rescue it"},
		{"3.5", "1.0_rc1_p1", "1.0_rc1", 1, "an extra _p suffix is greater"},
		{"3.5", "1.0_p0", "1.0", 1, "an extra _p suffix is greater even at number 0; _p0 is not the bare version"},
		{"3.5", "1.0_p", "1.0", 1, "an extra _p suffix is greater even with no number"},
		{"3.5", "1.0_rc1_beta", "1.0_rc1", -1, "an extra suffix other than _p is lower"},
		{"3.5", "1.0_rc1", "1.0", -1, "a release candidate is lower than the release"},
		{"3.5", "1.0_alpha0", "1.0", -1, "an extra _alpha is lower even at number 0"},

		// ---- 3.7: the revision ----
		{"3.7", "1.0-r10", "1.0-r9", 1, "revisions compare as integers: 10 > 9"},
		{"3.7", "1.0-r01", "1.0-r1", 0, "a revision ignores leading zeros"},
		{"3.7", "1.0-r1", "1.0", 1, "any revision above 0 is greater than none"},
		{"3.7", "1.71.1-r0", "1.71.1", 0, "a missing revision reads as -r0; the autoupdate registry pins 1.71.1-r0"},
		{"3.7", "1.0-r00", "1.0", 0, "-r00 is -r0"},
		{"3.7", "1.0-r99999999999999999999", "1.0-r99999999999999999998", 1, "a revision beyond int64 is ordered by value"},
		{"3.7", "1.0_p1", "1.0-r5", 1, "the suffix decides before the revision"},
		{"3.7", "1.0_rc1-r5", "1.0", -1, "a revision cannot lift a release candidate above the release"},
		{"3.7", "1.0a-r1", "1.0b", -1, "the letter decides before the revision"},
		{"3.7", "1.0.0", "1.0-r0", 1, "1.0-r0 equals 1.0, and 1.0.0 is still above both"},
		{"3.7", "1.0-r0", "1.00", 0, "1.0-r0, 1.0 and 1.00 are one version"},

		// ---- surrounding whitespace on a valid version ----
		{"3.1", "  1.0  ", "1.0", 0, "a valid version is compared in the trimmed form IsValidVersion accepts"},
		{"3.1", "\t1.0\n", "1.0", 0, "any surrounding whitespace, not only spaces"},
		{"3.1", " 1.1 ", "1.0", 1, "a padded valid version is not treated as invalid, which would order it lowest"},
		{"3.1", "  1.0  ", "1.0.0", -1, "the trimmed form is ordered by PMS like any other"},
	}
}

// invalidOrderingCases pins the rule for strings IsValidVersion rejects: an
// invalid string orders below every valid version, and two invalid strings
// order byte-wise on their untrimmed text, so two distinct invalid strings
// never compare equal.
func invalidOrderingCases() []versionOrderCase {
	return []versionOrderCase{
		// ---- exactly one argument invalid: it is the lower ----
		{"invalid", "latest", "0", -1, "a word is below the lowest valid version"},
		{"invalid", "1.0-foo", "0.1", -1, "an invalid string is below a valid one even when its digits read higher"},
		{"invalid", "", "0", -1, "the empty string is below the lowest valid version"},
		{"invalid", " ", "0", -1, "whitespace alone trims to the empty string, which is invalid"},
		{"invalid", "1..0", "0", -1, "an empty component is invalid"},
		{"invalid", "1.0.0-foo", "0", -1, "trailing junk is invalid"},
		{"invalid", "v6.6.91", "1.0", -1, "a leading v is invalid"},
		{"invalid", "INKSCAPE_1_4_4", "0", -1, "an upstream tag name is invalid"},
		{"invalid", "140.11.0esr-bb23", "1", -1, "a build suffix is invalid"},
		{"invalid", "1.0_", "0", -1, "an underscore with no suffix type is invalid"},
		{"invalid", "1.0-r", "0", -1, "a revision marker with no number is invalid"},
		{"invalid", "-r1", "0", -1, "a revision with no version is invalid"},
		{"invalid", "1.0A", "0", -1, "an uppercase letter is invalid"},
		{"invalid", "99999999999999999999-foo", "0", -1, "an invalid string with a huge leading number is still below 0"},
		{"invalid", "١.٠", "0", -1, "non-ASCII digits are invalid"},

		// ---- both arguments invalid: byte-wise, untrimmed ----
		{"invalid", "abc", "abd", -1, "two invalid strings order byte-wise"},
		{"invalid", "abc", "abc", 0, "an invalid string equals itself"},
		{"invalid", "latest", "latest", 0, "an invalid string equals itself"},
		{"invalid", "1.0-foo", "1.0-bar", 1, "two invalid strings that share a numeric head must not collapse into one"},
		{"invalid", " abc", "abc", -1, "invalid strings are compared untrimmed, so padding keeps them apart"},
		{"invalid", "abc ", "abc", 1, "invalid strings are compared untrimmed, so padding keeps them apart"},
		{"invalid", "1..0", "1.0-foo", -1, "byte order: '.' sorts before '0'"},
		{"invalid", "", "latest", -1, "the empty string is the lowest invalid string"},
	}
}

// totalityPool is input no version grammar anticipates. Every pair drawn from
// it must compare without panicking, inside {-1, 0, 1}, and consistently in
// both orders.
func totalityPool() []string {
	return []string{
		"", " ", "0", "1.0", "1.0.0", "latest", "1..0", "1.0-foo",
		"\x00", "\xff\xfe", "1.0\x00", "_", "-", ".", "-r", "_p", "1.0_", "1.0-r",
		"1.0_rc_", "1.0_rc1_", "1.0__rc1", "1.0-r1-r2", "1.0aa", "a1.0",
		strings.Repeat("9", 400),
		"1." + strings.Repeat("0", 300),
		"1.0_rc" + strings.Repeat("9", 100),
		"1.0-r" + strings.Repeat("0", 100) + "1",
		strings.Repeat("1.", 200) + "1",
		strings.Repeat("_p", 50),
	}
}

// assertVersionOrder checks one pair in both argument orders.
func assertVersionOrder(t *testing.T, a, b string, want int, why string) {
	t.Helper()
	if got := CompareVersions(a, b); got != want {
		t.Errorf("CompareVersions(%q, %q) = %d, want %d — %s", a, b, got, want, why)
	}
	if got := CompareVersions(b, a); got != -want {
		t.Errorf("CompareVersions(%q, %q) = %d, want %d (the reverse order) — %s", b, a, got, -want, why)
	}
}

// TestCompareVersionsPMS pins the PMS section 3.3 algorithm on valid versions,
// with at least one case per algorithm 3.1 to 3.7, each in both orders.
func TestCompareVersionsPMS(t *testing.T) {
	seen := map[string]bool{}
	for _, tc := range pmsOrderingCases() {
		seen[tc.algorithm] = true
		t.Run(tc.algorithm+" "+tc.a+" vs "+tc.b, func(t *testing.T) {
			// A fixture that is not a valid version tests the invalid-input
			// rule instead of PMS, and would pass or fail for the wrong reason.
			if !IsValidVersion(tc.a) || !IsValidVersion(tc.b) {
				t.Fatalf("fixture error: %q and %q must both pass IsValidVersion to pin the PMS ordering", tc.a, tc.b)
			}
			assertVersionOrder(t, tc.a, tc.b, tc.want, tc.why)
		})
	}
	for _, algorithm := range []string{"3.1", "3.2", "3.3", "3.4", "3.5", "3.6", "3.7"} {
		if !seen[algorithm] {
			t.Errorf("the table has no case for PMS algorithm %s", algorithm)
		}
	}
}

// TestCompareVersionsInvalidInput pins the comparator's behaviour on strings
// IsValidVersion rejects: it is total, never panics, orders an invalid string
// below every valid one, and orders two invalid strings byte-wise.
func TestCompareVersionsInvalidInput(t *testing.T) {
	for _, tc := range invalidOrderingCases() {
		t.Run(tc.a+" vs "+tc.b, func(t *testing.T) {
			if IsValidVersion(tc.a) {
				t.Fatalf("fixture error: %q passes IsValidVersion, so it cannot pin the invalid-input rule", tc.a)
			}
			assertVersionOrder(t, tc.a, tc.b, tc.want, tc.why)
		})
	}

	t.Run("total on arbitrary input", func(t *testing.T) {
		pool := totalityPool()
		for _, a := range pool {
			for _, b := range pool {
				forward := compareWithoutPanic(t, a, b)
				backward := compareWithoutPanic(t, b, a)
				if forward < -1 || forward > 1 {
					t.Errorf("CompareVersions(%q, %q) = %d, want one of -1, 0, 1", a, b, forward)
				}
				if forward != -backward {
					t.Errorf("CompareVersions(%q, %q) = %d but CompareVersions(%q, %q) = %d; swapping the arguments must negate the result", a, b, forward, b, a, backward)
				}
				if a == b && forward != 0 {
					t.Errorf("CompareVersions(%q, %q) = %d, want 0 for a string compared with itself", a, b, forward)
				}
			}
		}
	})
}

// compareWithoutPanic calls CompareVersions and turns a panic into a test
// error that names the pair, instead of a crash that names nothing.
func compareWithoutPanic(t *testing.T, a, b string) (result int) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("CompareVersions(%q, %q) panicked: %v", a, b, r)
			result = 0
		}
	}()
	return CompareVersions(a, b)
}

// TestCompareVersionsDocsStateTheRule reads the doc comments of CompareVersions
// and IsValidVersion from version.go. The comparison's callers decide what
// "newest" means from these comments, so they must name the PMS ordering and
// the invalid-input rule, and must not keep the claim that an unparseable
// string parses to a near-zero version.
func TestCompareVersionsDocsStateTheRule(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "version.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse version.go: %v", err)
	}
	docs := map[string]string{}
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Doc != nil {
			docs[fn.Name.Name] = fn.Doc.Text()
		}
	}

	compareDoc, ok := docs["CompareVersions"]
	if !ok {
		t.Fatal("CompareVersions has no doc comment in version.go")
	}
	validDoc, ok := docs["IsValidVersion"]
	if !ok {
		t.Fatal("IsValidVersion has no doc comment in version.go")
	}

	if !strings.Contains(compareDoc, "PMS") {
		t.Errorf("the CompareVersions doc does not name PMS, the ordering it implements:\n%s", compareDoc)
	}
	lower := strings.ToLower(compareDoc)
	if !strings.Contains(lower, "invalid") && !strings.Contains(compareDoc, "IsValidVersion") {
		t.Errorf("the CompareVersions doc does not state how input IsValidVersion rejects is ordered:\n%s", compareDoc)
	}
	if strings.Contains(strings.ToLower(validDoc), "near-zero") {
		t.Errorf("the IsValidVersion doc still claims an unparseable string parses to a near-zero version; invalid input now orders below every valid version:\n%s", validDoc)
	}
}

// FuzzCompareVersions asserts, on arbitrary strings, that the comparator never
// panics, returns -1, 0 or 1, is reflexive and antisymmetric, is transitive
// over any three inputs, orders invalid input below valid input and two
// invalid strings byte-wise, and compares a valid version in its trimmed form.
//
// Its seeds run in every plain `go test`, so the table values above are
// re-checked for these properties with no fuzzing flag.
func FuzzCompareVersions(f *testing.F) {
	var values []string
	seen := map[string]bool{}
	add := func(v string) {
		if !seen[v] {
			seen[v] = true
			values = append(values, v)
		}
	}
	for _, tc := range pmsOrderingCases() {
		add(tc.a)
		add(tc.b)
		f.Add(tc.a, tc.b, tc.a)
	}
	for _, tc := range invalidOrderingCases() {
		add(tc.a)
		add(tc.b)
		f.Add(tc.a, tc.b, tc.a)
	}
	for _, v := range totalityPool() {
		add(v)
	}
	// The extra seeds the fuzz target is required to carry: a bare 9999, a
	// 25-digit date, and the invalid strings callers are most likely to meet.
	for _, v := range []string{"9999", "2026092312345678901234567", "", "latest", "1.0-foo", "1..0"} {
		add(v)
	}
	for i := range values {
		f.Add(values[i], values[(i+1)%len(values)], values[(i+2)%len(values)])
	}

	f.Fuzz(func(t *testing.T, a, b, c string) {
		inputs := [3]string{a, b, c}
		var cmp [3][3]int
		for i, x := range inputs {
			for j, y := range inputs {
				cmp[i][j] = compareWithoutPanic(t, x, y)
				if r := cmp[i][j]; r < -1 || r > 1 {
					t.Fatalf("CompareVersions(%q, %q) = %d, want one of -1, 0, 1", x, y, r)
				}
			}
		}

		for i, x := range inputs {
			if cmp[i][i] != 0 {
				t.Errorf("CompareVersions(%q, %q) = %d, want 0 for a string compared with itself", x, x, cmp[i][i])
			}
			for j, y := range inputs {
				if cmp[i][j] != -cmp[j][i] {
					t.Errorf("CompareVersions(%q, %q) = %d but CompareVersions(%q, %q) = %d; swapping the arguments must negate the result", x, y, cmp[i][j], y, x, cmp[j][i])
				}

				validX, validY := IsValidVersion(x), IsValidVersion(y)
				switch {
				case validX && !validY && cmp[i][j] != 1:
					t.Errorf("CompareVersions(%q, %q) = %d, want 1: an invalid string orders below every valid version", x, y, cmp[i][j])
				case !validX && !validY && cmp[i][j] != strings.Compare(x, y):
					t.Errorf("CompareVersions(%q, %q) = %d, want %d: two invalid strings order byte-wise, untrimmed", x, y, cmp[i][j], strings.Compare(x, y))
				}
				if validX {
					if trimmed := CompareVersions(strings.TrimSpace(x), y); trimmed != cmp[i][j] {
						t.Errorf("CompareVersions(%q, %q) = %d but its trimmed form gives %d; a valid version compares as its trimmed form", x, y, cmp[i][j], trimmed)
					}
				}
			}
		}

		// Transitivity over every ordering of the three inputs.
		for i := range inputs {
			for j := range inputs {
				for k := range inputs {
					if cmp[i][j] <= 0 && cmp[j][k] <= 0 && cmp[i][k] > 0 {
						t.Errorf("not transitive: CompareVersions(%q, %q) = %d and CompareVersions(%q, %q) = %d, but CompareVersions(%q, %q) = %d",
							inputs[i], inputs[j], cmp[i][j], inputs[j], inputs[k], cmp[j][k], inputs[i], inputs[k], cmp[i][k])
					}
				}
			}
		}
	})
}
