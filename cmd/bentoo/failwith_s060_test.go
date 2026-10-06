package main

// Authored for story 060, sub-task 2.1 — R7.3, R7.4, R7.5, R7.6, R7.7.
//
// Written from the contract (design C7):
//
//	// failWith reports a code together with the diagnostic execute prints once.
//	// failWith(0, err) returns nil, as exitWith(0) does.
//	func failWith(code int, err error) error
//
// exitStatus gains an Unwrap that returns the cause; exitWith leaves it nil.
// execute keeps its identity test on the returned value and, for a bare status
// WITH a cause, prints the cause once through the logger. A cause that
// already carries an exit status is returned unchanged, so the inner code wins.
//
// The one-command tree here has no root pre-run, so no invocation logger is
// stored (story 062) and execute prints the cause as the bare line it always
// was, on os.Stderr. These tests therefore redirect file descriptor 2 (the
// testCLI captureStream helper), exactly as s058Execute does; a bytes.Buffer
// passed to execute would not see the line.
// The cause field itself is never named: the tests reach it through Unwrap,
// errors.Is and errors.As, which is the contract R7.6 states.
//
// Red on arrival: failWith does not exist.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/spf13/cobra"
)

// s060Execute runs a one-command tree whose RunE returns ret through the
// production execute, and returns what reached fd 2 (the logger's stream), what
// execute wrote to its own writer, and the exit code.
func s060Execute(t *testing.T, ret error) (fd2, writer string, code int) {
	t.Helper()
	root := &cobra.Command{
		Use:           "s060",
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE:          func(*cobra.Command, []string) error { return ret },
	}
	root.SetArgs([]string{})

	readErr := captureStream(t, 2, &os.Stderr)
	var buf bytes.Buffer
	code = execute(context.Background(), root, &buf)
	return readErr(), buf.String(), code
}

type s060CustomErr struct{ id int }

func (e *s060CustomErr) Error() string { return fmt.Sprintf("custom %d", e.id) }

// TestS060FailWithZeroIsSuccess is R7.3: status 0 is success whatever the cause.
func TestS060FailWithZeroIsSuccess(t *testing.T) {
	if err := failWith(0, errors.New("ignored")); err != nil {
		t.Errorf("failWith(0, err) = %v, want nil (R7.3)", err)
	}
	if err := failWith(0, nil); err != nil {
		t.Errorf("failWith(0, nil) = %v, want nil (R7.3)", err)
	}

	fd2, writer, code := s060Execute(t, failWith(0, errors.New("must not print")))
	if code != 0 || fd2 != "" || writer != "" {
		t.Errorf("execute(failWith(0, err)) = code %d, stderr %q, writer %q; want 0 and nothing printed (R7.3)", code, fd2, writer)
	}
}

// TestS060FailWithCarriesCodeAndCause is R7.6 and the code mapping: the
// returned value IS a bare exit status (so execute's identity test still
// applies), it maps to its code, its text is still "exit status <code>", and
// errors.Is / errors.As reach the cause through it.
func TestS060FailWithCarriesCodeAndCause(t *testing.T) {
	sentinel := errors.New("sentinel")
	for _, code := range []int{1, 2, 3} {
		custom := &s060CustomErr{id: code}
		cause := fmt.Errorf("step failed: %w", errors.Join(sentinel, custom))

		err := failWith(code, cause)

		if err == nil {
			t.Fatalf("failWith(%d, cause) = nil", code)
		}
		if _, bare := err.(*exitStatus); !bare { //nolint:errorlint // the identity is the contract execute relies on
			t.Errorf("failWith(%d, cause) returned %T, want the *exitStatus itself", code, err)
		}
		if got := exitCodeFor(err); got != code {
			t.Errorf("exitCodeFor(failWith(%d, cause)) = %d", code, got)
		}
		if want := fmt.Sprintf("exit status %d", code); err.Error() != want {
			t.Errorf("failWith(%d, cause).Error() = %q, want %q (unchanged from story 058)", code, err.Error(), want)
		}
		if errors.Unwrap(err) != cause { //nolint:errorlint // identity is the assertion: the value must come back unchanged, not merely match
			t.Errorf("errors.Unwrap(failWith(%d, cause)) = %v, want the cause itself (R7.6)", code, errors.Unwrap(err))
		}
		if !errors.Is(err, sentinel) {
			t.Errorf("errors.Is(failWith(%d, cause), sentinel) = false (R7.6)", code)
		}
		var got *s060CustomErr
		if !errors.As(err, &got) || got != custom {
			t.Errorf("errors.As(failWith(%d, cause), *s060CustomErr) did not reach the cause (R7.6)", code)
		}
	}
}

// TestS060FailWithNilCauseIsABareStatus: with no cause, failWith is exitWith.
func TestS060FailWithNilCauseIsABareStatus(t *testing.T) {
	err := failWith(2, nil)
	if got := exitCodeFor(err); got != 2 {
		t.Errorf("exitCodeFor(failWith(2, nil)) = %d, want 2", got)
	}
	if errors.Unwrap(err) != nil {
		t.Errorf("failWith(2, nil) carries a cause: %v", errors.Unwrap(err))
	}
	if errors.Unwrap(exitWith(2)) != nil {
		t.Errorf("exitWith(2) carries a cause: %v — a bare status has none (story 058)", errors.Unwrap(exitWith(2)))
	}
}

// TestS060FailWithInnerStatusWins is R7.4: a cause that already carries an
// exit status, directly or wrapped, decides the code. The hostile half is the
// wrapped one: once exitStatus has Unwrap, errors.As would otherwise find the
// OUTER status first and silently replace the intended code.
func TestS060FailWithInnerStatusWins(t *testing.T) {
	inner := failWith(2, errors.New("inner"))
	got := failWith(1, inner)
	if code := exitCodeFor(got); code != 2 {
		t.Errorf("exitCodeFor(failWith(1, failWith(2, ...))) = %d, want the inner 2 (R7.4)", code)
	}
	if got != inner { //nolint:errorlint // identity is the assertion: the value must come back unchanged, not merely match
		t.Errorf("failWith(1, <status>) = %v, want the cause returned unchanged", got)
	}

	wrapped := fmt.Errorf("apply: %w", exitWith(3))
	got = failWith(1, wrapped)
	if code := exitCodeFor(got); code != 3 {
		t.Errorf("exitCodeFor(failWith(1, wrapped exitWith(3))) = %d, want 3 (R7.4)", code)
	}
	if got != wrapped { //nolint:errorlint // identity is the assertion: the value must come back unchanged, not merely match
		t.Errorf("failWith(1, wrapped status) = %v, want the cause returned unchanged", got)
	}

	// The converse: a cause carrying no status keeps the code given.
	if code := exitCodeFor(failWith(4, fmt.Errorf("plain: %w", errors.New("x")))); code != 4 {
		t.Errorf("exitCodeFor(failWith(4, plain)) = %d, want 4", code)
	}
}

// TestS060FailWithExecutePrintsTheCauseOnce: execute prints a status's cause
// exactly once, through the logger (stderr), and nothing through its writer.
func TestS060FailWithExecutePrintsTheCauseOnce(t *testing.T) {
	const line = `--only must be "bin" or "source", got "both"`

	fd2, writer, code := s060Execute(t, failWith(1, fmt.Errorf("--only must be %q or %q, got %q", "bin", "source", "both")))

	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if fd2 != line+"\n" {
		t.Errorf("stderr = %q, want exactly %q — the cause printed once through logger.Error", fd2, line+"\n")
	}
	if writer != "" {
		t.Errorf("execute's writer got %q; the cause goes through the logger only, never twice", writer)
	}
}

// TestS060FailWithExecuteBareStatusStaysSilent is R7.5: a handler that printed
// its own diagnostic returns a bare status, and nothing more is printed.
func TestS060FailWithExecuteBareStatusStaysSilent(t *testing.T) {
	for _, ret := range []error{exitWith(1), failWith(1, nil)} {
		fd2, writer, code := s060Execute(t, ret)
		if code != 1 || fd2 != "" || writer != "" {
			t.Errorf("execute(%v) = code %d, stderr %q, writer %q; want 1 and nothing printed (R7.5)", ret, code, fd2, writer)
		}
	}
}

// TestS060FailWithExecuteNestedCausePrintsOnce is R7.7 with R7.4: the inner
// status's code wins and its diagnostic appears once, not once per layer.
func TestS060FailWithExecuteNestedCausePrintsOnce(t *testing.T) {
	fd2, writer, code := s060Execute(t, failWith(1, failWith(2, errors.New("inner line"))))

	if code != 2 {
		t.Errorf("exit code = %d, want the inner 2 (R7.4)", code)
	}
	if got := fd2 + writer; got != "inner line\n" {
		t.Errorf("printed %q, want %q exactly once (R7.7)", got, "inner line\n")
	}
}

// TestS060FailWithExecuteOtherErrorsUnchanged guards the story 058 printer: a
// non-status error still prints once through execute's writer, and a status
// wrapped in words of its own still prints the wrapper.
func TestS060FailWithExecuteOtherErrorsUnchanged(t *testing.T) {
	fd2, writer, code := s060Execute(t, errors.New("plain failure"))
	if code != 1 || writer != "plain failure\n" || fd2 != "" {
		t.Errorf("execute(plain) = code %d, writer %q, stderr %q; want 1, %q, \"\"", code, writer, fd2, "plain failure\n")
	}

	fd2, writer, code = s060Execute(t, fmt.Errorf("pull: %w", exitWith(3)))
	if code != 3 || writer != "pull: exit status 3\n" || fd2 != "" {
		t.Errorf("execute(wrapped status) = code %d, writer %q, stderr %q; want 3, %q, \"\"", code, writer, fd2, "pull: exit status 3\n")
	}
}
