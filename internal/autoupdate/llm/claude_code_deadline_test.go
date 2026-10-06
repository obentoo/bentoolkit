package llm

import (
	"context"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"
)

// =============================================================================
// S048 sub-task 1.2 — run says WHICH failure killed the invocation
// =============================================================================
//
// The measured defect, 2026-08-27: five reviews in one run exceeded their budget
// and every one reached the operator as
//
//	the claude CLI could not read the two ebuilds: ... claude CLI failed: signal: killed
//
// Nothing in that sentence is the cause. `run` never reads ctx.Err(), so a child
// this program's own deadline killed arrives at the generic failure branch
// carrying only what the kernel left behind, and the wrapper above it supplies a
// noun about file reading that no code path checked.
//
// These tests exercise the real ClaudeCodeClient.run through the real exec seam
// (execCommand, claude_code.go:113) with real child processes — a blocking
// sleep, a binary that is not there, a shell that exits non-zero. No message is
// simulated: each assertion reads the error a real invocation produced.

// mentionsReading matches the word this story's message must never contain
// again. It is a word match rather than a substring one on purpose: "already"
// contains the four letters r-e-a-d, and a guard that fired on it would be
// noise an implementer learns to work around.
var mentionsReading = regexp.MustCompile(`(?i)\bread(s|ing)?\b`)

// namesAnElapsedBudget reports whether msg says that time ran out. The set is
// wide because S048-R1.1 constrains the FACT reported, not the phrasing, and
// this package already spells the same fact two ways — "aborted: context
// deadline exceeded" (manifest_fixer.go:469) and "ran out of time: its %s
// budget ... elapsed" (bump_reviewer.go:621). What is asserted narrowly, below,
// is the budget's value, because that is the part an operator cannot infer.
func namesAnElapsedBudget(msg string) bool {
	low := strings.ToLower(msg)
	for _, phrase := range []string{"deadline", "timed out", "timeout", "ran out of time", "elapsed", "budget"} {
		if strings.Contains(low, phrase) {
			return true
		}
	}
	return false
}

// blockingSeam returns an exec seam whose child outlives any test budget, so the
// only thing that can end the call is the client's own deadline.
func blockingSeam() func(ctx context.Context, name string, arg ...string) *exec.Cmd {
	return func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sleep", "3600")
	}
}

// unstartableBinary is a path that does not exist, so os/exec fails before the
// child's first instruction and returns an error carrying no *exec.ExitError.
const unstartableBinary = "/nonexistent/bentoo-s048-claude"

// unstartableSeam returns an exec seam that spawns a binary which is not there.
func unstartableSeam() func(ctx context.Context, name string, arg ...string) *exec.Cmd {
	return func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		return exec.CommandContext(ctx, unstartableBinary)
	}
}

// runUnderBudget performs one real invocation through the seam and returns the
// error message it produced.
func runUnderBudget(t *testing.T, seam func(ctx context.Context, name string, arg ...string) *exec.Cmd, budget time.Duration) string {
	t.Helper()
	c := newTestClient(t, LLMConfig{}, WithClaudeCodeExecCommand(seam), WithClaudeCodeTimeout(budget))
	_, err := c.run(t.Context(), "instr", []byte("content"), "")
	if err == nil {
		t.Fatalf("the invocation returned no error, so there is no failure message to read")
	}
	return err.Error()
}

// TestRun_AnElapsedDeadlineSaysSoAndNamesTheBudget is S048-R1.1: the sentence
// reports the deadline as the cause and names the budget that elapsed.
//
// The budget is asserted by its value and not merely by the word "budget". An
// operator who has raised autoupdate.review.timeout needs to know WHICH value
// was in force, and once that key exists the number is the only part of the
// sentence they cannot work out from the code. The value is read from the
// client's own timeout field, so a configured budget and the package default
// are free to differ.
func TestRun_AnElapsedDeadlineSaysSoAndNamesTheBudget(t *testing.T) {
	const budget = 250 * time.Millisecond

	msg := runUnderBudget(t, blockingSeam(), budget)

	if !namesAnElapsedBudget(msg) {
		t.Errorf("a review killed by its own %s deadline reported %q; it names no elapsed budget, so whoever "+
			"reads it has no way to tell this program's deadline from a failure of the CLI (S048-R1.1)", budget, msg)
	}
	if !strings.Contains(msg, budget.String()) {
		t.Errorf("the deadline message %q does not name the budget %s that elapsed; the remedy is to raise that "+
			"number, and an operator cannot judge a value the message withholds (S048-R1.1)", msg, budget)
	}
	if mentionsReading.MatchString(msg) {
		t.Errorf("the deadline message %q still speaks of reading; nothing was read and nothing failed to be "+
			"read — the deadline elapsed — and that word is what sent the 2026-08-27 investigation to the "+
			"filesystem (S048-R1.4)", msg)
	}
}

// TestRun_ABinaryThatCannotStartIsSaidAsThatAndNotAsADeadline is S048-R1.2 and
// Success Metric 2: the two failures must be distinguishable BY THEIR TEXT
// ALONE.
//
// The distinctness is asserted as an inequality between the two messages a real
// invocation produced, not as two independent substring checks. Two checks that
// each look for their own phrase both pass against a message that carries both
// phrases, and against two messages that differ only in a suffix an operator
// would never notice; only comparing the strings to each other asks the question
// the metric asks.
func TestRun_ABinaryThatCannotStartIsSaidAsThatAndNotAsADeadline(t *testing.T) {
	// A budget far longer than the spawn attempt, so nothing here can be a
	// deadline: the only thing that failed is starting the process.
	startMsg := runUnderBudget(t, unstartableSeam(), 30*time.Second)
	deadlineMsg := runUnderBudget(t, blockingSeam(), 250*time.Millisecond)

	if startMsg == deadlineMsg {
		t.Fatalf("a binary that could not start and a review killed by its deadline both report %q; the two "+
			"remedies are opposite — fix the host, or raise the budget — and an operator holding one sentence "+
			"cannot choose between them (S048-R1.2, Success Metric 2)", startMsg)
	}
	if !strings.Contains(startMsg, "could not start") {
		t.Errorf("a claude binary that is not there reported %q; the process never ran, so there is no exit "+
			"status to frame it with, and this package already says this failure as what it is (S040-R5.6, "+
			"S048-R1.2)", startMsg)
	}
	if !strings.Contains(startMsg, unstartableBinary) {
		t.Errorf("the start failure %q lost its cause; the operator still needs to read WHAT could not start "+
			"and why (S048-R1.2)", startMsg)
	}
}

// TestRun_ADeadlineThatElapsesBeforeStartIsStillADeadline is the hostile fixture
// for the rule above, and the one place where both conditions hold at once.
//
// When the budget is gone before Start, os/exec refuses to spawn and hands back
// the context error itself: the run error carries no *exec.ExitError, so the
// could-not-start test is true, AND the context is expired, so the deadline test
// is true. Reporting this as an unstartable binary would send an operator to
// check a PATH that is fine, for a run their own budget ended. The deadline
// wins, and the sentence says only that (design D1).
func TestRun_ADeadlineThatElapsesBeforeStartIsStillADeadline(t *testing.T) {
	// One nanosecond is gone before execCommand returns, so the deadline elapses
	// on the near side of Start rather than during the call.
	const budget = time.Nanosecond

	msg := runUnderBudget(t, blockingSeam(), budget)

	if !namesAnElapsedBudget(msg) {
		t.Errorf("a deadline that elapsed before the child started reported %q, which names no elapsed budget; "+
			"the run ended on this program's own timer either way, and only the moment differed (S048-R1.1)", msg)
	}
	if !strings.Contains(msg, budget.String()) {
		t.Errorf("the message %q does not name the %s budget that elapsed before Start; the budget is the same "+
			"fact whichever side of the spawn it runs out on (S048-R1.1)", msg, budget)
	}
	if strings.Contains(msg, "could not start") {
		t.Errorf("a deadline that elapsed before Start is reported as %q — a failure to start; both conditions "+
			"are true here and the precedence exists to settle which is said, because this answer sends the "+
			"operator to their PATH for a number in a config file (design D1, S048-R1.2)", msg)
	}
	if mentionsReading.MatchString(msg) {
		t.Errorf("the message %q speaks of reading for a run that never started (S048-R1.4)", msg)
	}
}

// TestRun_ANonZeroExitStillReadsAsItDidBefore is the regression half: the
// outcome S048 does NOT touch keeps its exact wording.
//
// Pinned byte for byte, captured from the code as it stands on 2026-09-06.
// A classification inserted ahead of the exit-code framing is free to swallow
// this case by accident — every failure arrives at the same branch today — and
// an exit status silently reworded as a deadline would be this story's own
// defect, mirrored.
func TestRun_ANonZeroExitStillReadsAsItDidBefore(t *testing.T) {
	cases := []struct {
		name   string
		script string
		want   string
	}{
		{
			name:   "no diagnostics",
			script: `exit 3`,
			want:   "LLM API request failed: claude CLI failed: exit status 3",
		},
		{
			name:   "with stderr",
			script: `printf 'diag on stderr' >&2; exit 3`,
			want:   "LLM API request failed: claude CLI failed: exit status 3: diag on stderr",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seam, _ := scriptedSeam(tc.script)
			got := runUnderBudget(t, seam, 30*time.Second)
			if got != tc.want {
				t.Errorf("a non-zero exit now reads as\n got: %q\nwant: %q\nthe exit-code framing is what an "+
					"operator and four existing tests already read; S048 adds two causes ahead of it and "+
					"changes none of it (S048-R1.2, S048-R4.2)", got, tc.want)
			}
		})
	}
}

// TestRun_TheThreeOutcomesAreThreeSentences asks the question the two pairwise
// tests above cannot: whether a THIRD failure collides with the pair they
// separated.
//
// Success Metric 2 is written about two sentences, and a fix that answers it
// exactly — making the deadline text differ from the could-not-start text — says
// nothing about the outcome that was already there. If the exit-status message
// were reworded into either of them, or either of them into it, the pair stays
// distinct and the guarantee still breaks: an operator would read one sentence
// for two different causes. Three real invocations, three real failures, three
// strings compared to each other.
func TestRun_TheThreeOutcomesAreThreeSentences(t *testing.T) {
	exitSeam, _ := scriptedSeam(`exit 3`)

	msgs := map[string]string{
		"an elapsed deadline":        runUnderBudget(t, blockingSeam(), 250*time.Millisecond),
		"a binary that cannot start": runUnderBudget(t, unstartableSeam(), 30*time.Second),
		"a non-zero exit":            runUnderBudget(t, exitSeam, 30*time.Second),
	}

	seen := map[string]string{}
	for cause, msg := range msgs {
		if other, dup := seen[msg]; dup {
			t.Errorf("%s and %s both report %q; three causes with three remedies reaching an operator as one "+
				"sentence is the whole failure S048 was opened for (S048-R1.1, S048-R1.2)", cause, other, msg)
			continue
		}
		seen[msg] = cause
	}
}
