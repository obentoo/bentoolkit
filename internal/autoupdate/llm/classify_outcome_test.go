package llm

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// =============================================================================
// S048 sub-task 1.1 — the precedence lifted out of formatFixerError
// =============================================================================
//
// SURFACE THIS FILE PINS. These tests are written against one new package-level
// function:
//
//	classifyClaudeFailure(ctxErr, runErr error) <outcome>
//
// where <outcome> is a comparable value naming WHICH of the three failures
// happened — an elapsed deadline, a process that could not start, a process
// that ran and exited non-zero. The tests never name the outcome constants and
// never read a sentence: they compare the values the classifier returns to each
// other. That is deliberate. The thing S048-R1.3 asks for is that one answer
// exists and every caller inherits it; the spelling of the constants is the
// implementer's, and a test that pinned them would be pinning a decision the
// requirement does not make.
//
// WHY THE ASSERTIONS ARE ON THE OUTCOME AND NOT ON A SENTENCE. formatFixerError
// says "claude fixer aborted" because it speaks for the fixer. The review must
// say something else — a review that reports itself as a fixer is the same
// class of defect S048 exists to remove, arriving by another road (design D1).
// So what is shared is the ORDER, and the order is only testable on a value.

// startFailureError manufactures a REAL failure to start a process: `true` run
// from a working directory that does not exist. It is a real error from the
// real os/exec rather than an errors.New stand-in, because what makes this case
// the could-not-start case is a property of the error's TYPE — no *exec.ExitError
// is present — and a hand-written error would prove the classifier agrees with
// the test's own fiction instead of with os/exec.
func startFailureError(t *testing.T) error {
	t.Helper()
	cmd := exec.Command("true")
	cmd.Dir = filepath.Join(t.TempDir(), "vanished")
	err := cmd.Run()
	if err == nil {
		t.Fatalf("instrument: running in a nonexistent directory succeeded, so there is no start failure to classify")
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		t.Fatalf("instrument: the start failure carries an *exec.ExitError (%v), so it is not the case this fixture exists to build", err)
	}
	return err
}

// deadlineBeforeStartError manufactures the COLLISION fixture: a process spawned
// under a context whose deadline has already elapsed. os/exec refuses to start
// it and returns the context error itself, so the returned error is non-nil and
// carries no *exec.ExitError — couldNotStart(runErr) is true AND ctxErr is
// non-nil at the same moment. This is the fixture the precedence exists for, and
// it is built for real rather than simulated for the same reason as above.
func deadlineBeforeStartError(t *testing.T) (ctxErr, runErr error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()

	runErr = exec.CommandContext(ctx, "true").Run()
	if runErr == nil {
		t.Fatalf("instrument: a command spawned under an elapsed deadline ran successfully, so the collision this test is about did not occur")
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		t.Fatalf("instrument: the deadline-before-start failure carries an *exec.ExitError (%v); the collision needs a run error with none", runErr)
	}
	return ctx.Err(), runErr
}

// TestClassifyClaudeFailure_TheThreeOutcomesStayApart is S048-R1.2 and S048-R5.2:
// an elapsed deadline, a process that could not start and a process that exited
// non-zero are three answers, not one. A classifier that collapsed any pair
// would send an operator to the wrong remedy — waiting out a budget that never
// elapsed, or checking a PATH that is fine.
//
// The assertion is pairwise inequality between the values themselves, so it
// fails for its own reason (two causes rendered as one) rather than for a
// wording.
func TestClassifyClaudeFailure_TheThreeOutcomesStayApart(t *testing.T) {
	deadline := ClassifyClaudeFailure(context.DeadlineExceeded, exitErrWithCode(t, 1))
	couldNotStart := ClassifyClaudeFailure(nil, startFailureError(t))
	exited := ClassifyClaudeFailure(nil, exitErrWithCode(t, 1))

	if deadline == couldNotStart {
		t.Errorf("an elapsed deadline and a process that could not start classify identically (%v); "+
			"an operator told the binary would not start goes to check their PATH, and the cause was this "+
			"program's own budget (S048-R1.2)", deadline)
	}
	if deadline == exited {
		t.Errorf("an elapsed deadline and a non-zero exit classify identically (%v); the deadline would then be "+
			"reported as whatever the kernel left behind, which is the defect S048 was opened for (S048-R1.2)", deadline)
	}
	if couldNotStart == exited {
		t.Errorf("a process that could not start and one that ran and exited non-zero classify identically (%v); "+
			"the first has no exit code to print, so the second's framing renders a whole error where a number "+
			"was promised (S040-R5.6, S048-R1.2)", couldNotStart)
	}
}

// TestClassifyClaudeFailure_ADeadlineBeforeStartIsStillADeadline is the hostile
// half of the rule above, and the case the precedence exists to settle.
//
// When the budget elapses before Start, BOTH tests are true at once: the context
// carries an error and the run error carries no *exec.ExitError. Nothing about
// the two conditions alone says which wins. Design D1 rules that the deadline
// does, and the reason is the operator's next move: a process the program's own
// budget killed before it ran is a deadline, and reporting it as an unstartable
// binary sends someone to their PATH for a problem that is a number in a config
// file.
//
// Asserted as an EQUALITY against the plain-deadline outcome and an INEQUALITY
// against the could-not-start one, because the two halves fail differently: the
// first catches a deadline that split into a second kind of answer, the second
// catches one that collapsed into the wrong neighbour.
func TestClassifyClaudeFailure_ADeadlineBeforeStartIsStillADeadline(t *testing.T) {
	ctxErr, runErr := deadlineBeforeStartError(t)

	collision := ClassifyClaudeFailure(ctxErr, runErr)
	plainDeadline := ClassifyClaudeFailure(context.DeadlineExceeded, exitErrWithCode(t, 1))
	couldNotStart := ClassifyClaudeFailure(nil, startFailureError(t))

	if collision != plainDeadline {
		t.Errorf("a deadline that elapsed before Start classifies as %v, but a deadline that elapsed during the "+
			"call classifies as %v; one cause reported two ways means an operator reading the second sentence "+
			"cannot tell it is the same budget (S048-R1.1, S048-R1.2)", collision, plainDeadline)
	}
	if collision == couldNotStart {
		t.Errorf("a deadline that elapsed before Start classifies as %v, the same as a binary that could not "+
			"start; the precedence exists precisely because both conditions hold here, and answering with the "+
			"second sends the operator to check a PATH that is fine (design D1, S048-R1.2)", collision)
	}
}

// TestClassifyClaudeFailure_ACancelledParentIsNotBlamedOnTheHost covers the
// other context error that reaches this classifier. A run torn down by its
// caller is neither an unstartable binary nor a program that exited on its own
// account, and saying either would make someone look for a fault in a host that
// is fine.
//
// It deliberately does NOT assert that a cancellation and a deadline are the
// same outcome: formatFixerError renders both through one branch today, and a
// caller that wants to name them separately later must stay free to.
func TestClassifyClaudeFailure_ACancelledParentIsNotBlamedOnTheHost(t *testing.T) {
	cancelled := ClassifyClaudeFailure(context.Canceled, exitErrWithCode(t, 1))
	couldNotStart := ClassifyClaudeFailure(nil, startFailureError(t))
	exited := ClassifyClaudeFailure(nil, exitErrWithCode(t, 1))

	if cancelled == couldNotStart {
		t.Errorf("a cancelled parent classifies as %v, the same as a binary that could not start; the run was "+
			"torn down by its caller and the host is fine (S048-R1.2)", cancelled)
	}
	if cancelled == exited {
		t.Errorf("a cancelled parent classifies as %v, the same as a process that ran and exited non-zero; a "+
			"context error takes precedence over exit framing, which is the rule this lift is meant to preserve "+
			"(S009-R1.3, S048-R1.3)", cancelled)
	}
}
