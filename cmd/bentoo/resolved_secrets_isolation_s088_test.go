package main

// The secrets package records every value Lookup returns for the life of the
// process, and the invocation logger scrubs all of them. In one test binary a
// value one test resolved is therefore masked in every later test's stderr,
// and a test that greps its own output for an ordinary word fails only on the
// shuffle seeds that run the resolving test first.
//
// The probe is an invalid BENTOO_LOG_LEVEL: the invocation logger echoes it in
// its "unknown log level %q" warning, so a run's stderr carries a chosen word
// without any fixture beyond the harness.

import (
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/secrets"
)

const (
	// s088Earlier stands for a value an earlier test in the binary resolved.
	s088Earlier = "zq-s088-earlier"
	// s088Own is a value resolved by the test that then runs the CLI.
	s088Own = "zq-s088-own"
)

// s088Resolve makes secrets.Lookup return value once, recording it.
func s088Resolve(t *testing.T, name, value string) {
	t.Helper()
	t.Setenv(name, value)
	got, found, err := secrets.Lookup(name)
	if err != nil || !found || got != value {
		t.Fatalf("secrets.Lookup(%s) = %q, %v, %v; want %q, true, nil", name, got, found, err, value)
	}
}

// s088LevelWarning runs the CLI with level as BENTOO_LOG_LEVEL and returns the
// stderr line carrying the invalid-level warning.
func s088LevelWarning(t *testing.T, c *testCLI, level string) string {
	t.Helper()
	t.Setenv("BENTOO_LOG_LEVEL", level)
	_, stderr, code := c.Run("version")
	if code != 0 {
		t.Fatalf("bentoo version exited %d, want 0\nstderr:\n%s", code, stderr)
	}
	for line := range strings.SplitSeq(stderr, "\n") {
		if strings.Contains(line, "invalid BENTOO_LOG_LEVEL") {
			return line
		}
	}
	t.Fatalf("stderr has no invalid BENTOO_LOG_LEVEL warning\nstderr:\n%s", stderr)
	return ""
}

// TestS088_1_1_EarlierTestsSecretIsNotMaskedHere is the leak: a value resolved
// before this test's harness existed is another test's secret, and must not
// mask this test's output.
func TestS088_1_1_EarlierTestsSecretIsNotMaskedHere(t *testing.T) {
	s088Resolve(t, "BENTOO_S088_EARLIER", s088Earlier)
	c := newTestCLI(t)

	line := s088LevelWarning(t, c, s088Earlier)
	if !strings.Contains(line, `unknown log level \"`+s088Earlier+`\"`) {
		t.Errorf("the warning masks %q, a value an earlier test resolved, so this test cannot read its own output:\n%s", s088Earlier, line)
	}
}

// TestS088_1_1_OwnSecretStaysMasked is the hostile half: isolation narrows the
// scrub list to this test's values, it never switches redaction off.
func TestS088_1_1_OwnSecretStaysMasked(t *testing.T) {
	c := newTestCLI(t)
	s088Resolve(t, "BENTOO_S088_OWN", s088Own)

	line := s088LevelWarning(t, c, s088Own)
	if strings.Contains(line, s088Own) {
		t.Errorf("the warning shows %q, a value this test resolved; it must stay redacted:\n%s", s088Own, line)
	}
}
