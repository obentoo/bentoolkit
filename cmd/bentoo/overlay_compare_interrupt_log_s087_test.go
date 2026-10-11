package main

// Story 087, sub-task 4.3 (R6.4, R10.7): a comparison interrupted by a
// cancellation (its error matches context.Canceled) logs the interruption line
// the repository-resolution path logs, registryInterruptedMsg, instead of the
// generic "comparing packages: failed". The exit code stays 1.
//
// Hermetic: s086World's world (HOME, XDG_CONFIG_HOME and PATH under t.TempDir,
// a local ::gentoo), so the comparison is reached without any download; the
// interruption is a context that is already cancelled, as in
// TestS086CompareInterruptedComparisonExitsOne.

import (
	"context"
	"strings"
	"testing"
	"time"
)

const s087d43Failed = `msg="comparing packages: failed"`

func s087d43OutputFlags(t *testing.T) {
	t.Helper()
	q, v, nc := quiet, verbose, noColor
	t.Cleanup(func() { quiet, verbose, noColor = q, v, nc })
	quiet, verbose, noColor = false, false, true
}

// TestS087_4_3_DeadlineExceededComparisonStaysAFailure is the hostile half: a
// comparison that ends on an expired deadline was not interrupted by the
// operator. Its error matches context.DeadlineExceeded, not context.Canceled,
// so it keeps the generic failure line and is not reported as an
// interruption.
func TestS087_4_3_DeadlineExceededComparisonStaysAFailure(t *testing.T) {
	s086World(t)
	s087d43OutputFlags(t)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	t.Cleanup(cancel)

	res := s086RunIn(t, nil, ctx)

	if res.code != 1 {
		t.Errorf("exit code = %d, want 1\nlogs:\n%s", res.code, res.logs)
	}
	s086WantLines(t, "logs", res.logs,
		`level=INFO msg="Comparing with upstream" repository=gentoo`,
		`level=ERROR `+s087d43Failed+` err="context deadline exceeded"`)
	s086Absent(t, "logs", res.logs, registryInterruptedMsg)
}

// TestS087_4_3_InterruptedComparisonLogsInterruption pins R6.4: the comparison
// is reached (scan and "Comparing with upstream" logged), the cancellation is
// logged as the same ERROR line the repository-resolution path logs, the
// generic failure line is gone, and the run still exits 1 with no report.
func TestS087_4_3_InterruptedComparisonLogsInterruption(t *testing.T) {
	s086World(t)
	s087d43OutputFlags(t)

	res := s086Run(t, nil, true)

	if res.code != 1 {
		t.Errorf("exit code = %d, want 1\nlogs:\n%s", res.code, res.logs)
	}
	s086WantLines(t, "logs", res.logs,
		`level=INFO msg="Scanning Bentoo overlay"`,
		`level=INFO msg="Comparing with upstream" repository=gentoo`,
		`level=ERROR msg="`+registryInterruptedMsg+`"`)
	s086Absent(t, "logs", res.logs, s087d43Failed, "GitHub API rate limit exceeded.")
	if n := strings.Count(res.logs, `msg="`+registryInterruptedMsg+`"`); n != 1 {
		t.Errorf("interruption records = %d, want 1\nlogs:\n%s", n, res.logs)
	}
	if res.stdout != "" {
		t.Errorf("stdout = %q, want empty", res.stdout)
	}
}
