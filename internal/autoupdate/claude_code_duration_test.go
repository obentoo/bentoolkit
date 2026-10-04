package autoupdate

import (
	"context"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"
)

// =============================================================================
// S048 sub-task 1.3 — every invocation records how long it took
// =============================================================================
//
// S048-R2.1 asks that a `claude` invocation record its wall-clock duration
// alongside its outcome, BY ANY OUTCOME. The measurement task that follows reads
// those numbers to decide the review's budget, and it cannot be decided from the
// failures alone: the failures are exactly the runs that hit the ceiling, and
// the value a new ceiling must clear is the distribution of the ones that did
// not. A recording that covered only failures would leave the story's own
// measurement unmeasurable, so the successful invocation is the case that
// matters most here.
//
// THE ASSERTION SURFACE IS THE PACKAGE'S LOG SEAM. `run` returns
// (string, error), so a successful call has no return channel for a duration and
// widening the signature would publish a number no caller consumes (design D5).
// The sink is therefore a log line, and this package already exposes its loggers
// as package variables precisely so tests can read them — infoLogf
// (analyzer.go:52) and warnLogf (header_allowlist.go:53). Both are captured
// below, because which level a duration belongs at is the implementer's call and
// nothing in S048-R2.1 depends on the answer.

// durationToken matches one Go duration literal as time.Duration renders it, so
// a logged elapsed time can be read back and checked as a NUMBER rather than as
// the presence of a word.
var durationToken = regexp.MustCompile(`\d+(?:\.\d+)?(?:ns|µs|us|ms|s|m|h)`)

// namesTheInvocation reports whether a log line says what it timed. A duration
// with no subject is not a measurement anyone can use six weeks later, when the
// question is which invocation cost what. The accepted set is wide because the
// wording is the implementer's; what it may not do is publish a bare number.
func namesTheInvocation(line string) bool {
	low := strings.ToLower(line)
	return strings.Contains(low, "claude") || strings.Contains(low, "cli") || strings.Contains(low, "invocation")
}

// recordedElapsedIn scans captured log lines for a recorded duration that falls
// inside [lo, hi] and belongs to a line naming the invocation.
//
// EVERY duration token on a line is considered, not the first, and the window is
// what makes the check mean something. A line may legitimately carry more than
// one duration — the elapsed time and the budget it ran against — and a test
// that read the first token would pass on a line that published only the budget,
// which is a number the caller already knew. The callers below choose a budget
// far outside the window for exactly this reason.
func recordedElapsedIn(lines []string, lo, hi time.Duration) (time.Duration, string, bool) {
	for _, line := range lines {
		if !namesTheInvocation(line) {
			continue
		}
		for _, tok := range durationToken.FindAllString(line, -1) {
			d, err := time.ParseDuration(tok)
			if err != nil {
				continue
			}
			if d >= lo && d <= hi {
				return d, line, true
			}
		}
	}
	return 0, "", false
}

// captureRunLogs captures both package log sinks for the duration of the test
// and returns a function collecting everything either of them received.
func captureRunLogs(t *testing.T) func() []string {
	t.Helper()
	info := captureInfoLogs(t)
	warn := captureWarnLogs(t)
	return func() []string {
		return append(info.all(), warn.all()...)
	}
}

// childWorkTime is how long the scripted child runs before it finishes. It is
// long enough to be told apart from zero on a loaded machine and short enough to
// keep the suite fast.
const childWorkTime = 200 * time.Millisecond

// TestRun_ASuccessfulInvocationRecordsItsDuration is the case S048-R2.1 exists
// for and the one no failure path can substitute for.
func TestRun_ASuccessfulInvocationRecordsItsDuration(t *testing.T) {
	lines := captureRunLogs(t)

	seam, _ := scriptedSeam(`sleep 0.2; printf '%s' '{"type":"result","is_error":false,"result":"ok"}'`)
	c := newTestClient(t, LLMConfig{}, WithClaudeCodeExecCommand(seam), WithClaudeCodeTimeout(30*time.Second))

	out, err := c.run(t.Context(), "instr", []byte("content"), "")
	if err != nil {
		t.Fatalf("the scripted invocation failed (%v); this test is about what a SUCCESSFUL call records", err)
	}
	if out != "ok" {
		t.Fatalf("result = %q, want %q; the success path must be unchanged by the recording", out, "ok")
	}

	got := lines()
	elapsed, line, ok := recordedElapsedIn(got, 100*time.Millisecond, 5*time.Second)
	if !ok {
		t.Fatalf("an invocation that ran for about %s recorded no duration; %d log line(s) were emitted: %q\n"+
			"the budget this story must set is derived from the runs that SUCCEED, so a recording that skips "+
			"them leaves the measurement with nothing to read (S048-R2.1)", childWorkTime, len(got), got)
	}
	if elapsed < childWorkTime/2 {
		t.Errorf("the recorded duration %s is far below the %s the child actually ran (line: %q); a number that "+
			"does not track the wall clock cannot be used to choose a budget (S048-R2.1)", elapsed, childWorkTime, line)
	}
}

// TestRun_AFailedInvocationRecordsItsDuration is the other converse: a run that
// ended badly still says what it cost. Without it the distribution the
// measurement reads would be missing precisely the expensive tail.
func TestRun_AFailedInvocationRecordsItsDuration(t *testing.T) {
	lines := captureRunLogs(t)

	seam, _ := scriptedSeam(`sleep 0.2; exit 3`)
	c := newTestClient(t, LLMConfig{}, WithClaudeCodeExecCommand(seam), WithClaudeCodeTimeout(30*time.Second))

	if _, err := c.run(t.Context(), "instr", []byte("content"), ""); err == nil {
		t.Fatalf("the scripted invocation exited 3 but run returned no error; this test is about what a FAILED call records")
	}

	got := lines()
	elapsed, line, ok := recordedElapsedIn(got, 100*time.Millisecond, 5*time.Second)
	if !ok {
		t.Fatalf("a failed invocation that ran for about %s recorded no duration; %d log line(s) were emitted: %q\n"+
			"S048-R2.1 says every outcome, and a failure that cost real time is a reading the budget decision "+
			"needs (S048-R2.1)", childWorkTime, len(got), got)
	}
	if elapsed < childWorkTime/2 {
		t.Errorf("the recorded duration %s is far below the %s the failing child actually ran (line: %q) (S048-R2.1)",
			elapsed, childWorkTime, line)
	}
}

// TestRun_ADeadlineKilledInvocationRecordsItsDuration is the third outcome, and
// the reason it is written despite proving less than the two above.
//
// The pair already covers "finished well" and "finished badly", and a recording
// added for them can still miss the outcome that returns through a different
// branch entirely — the one where this program kills its own child. That is the
// outcome the 2026-08-27 run consisted of five of, so a duration ledger blind to
// it would be blind to the whole observation that opened this story.
//
// WHAT IT CANNOT PROVE, stated rather than implied: a deadline-killed run lasts
// as long as its budget, so its elapsed time and its budget are the same number
// and no assertion can tell a line publishing one from a line publishing the
// other. This test therefore asserts that a duration was recorded at all, and
// leaves the "is it really the wall clock" question to the two tests above,
// where the child's runtime and the budget are two orders of magnitude apart.
func TestRun_ADeadlineKilledInvocationRecordsItsDuration(t *testing.T) {
	lines := captureRunLogs(t)

	seam := func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sleep", "3600")
	}
	c := newTestClient(t, LLMConfig{}, WithClaudeCodeExecCommand(seam), WithClaudeCodeTimeout(300*time.Millisecond))

	if _, err := c.run(t.Context(), "instr", []byte("content"), ""); err == nil {
		t.Fatalf("a child that outlives its 300ms budget returned no error; there is no killed invocation to time")
	}

	got := lines()
	if _, _, ok := recordedElapsedIn(got, 100*time.Millisecond, 5*time.Second); !ok {
		t.Fatalf("an invocation killed by its own deadline recorded no duration; %d log line(s) were emitted: %q\n"+
			"the runs that hit the ceiling are the ones the 2026-08-27 observation is made of, and a ledger "+
			"that omits them cannot answer whether the ceiling is the problem (S048-R2.1)", len(got), got)
	}
}
