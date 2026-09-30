package autoupdate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
)

// signalWaitDeadline bounds every wait these helpers perform. A replacement for
// a sleep must never wait forever: a barrier that cannot fail turns a flaky
// test into a hung one.
const signalWaitDeadline = 5 * time.Second

// pollUntil re-evaluates cond until it reports true, and fails the test when
// signalWaitDeadline passes first. It exists for the conditions no channel
// announces — a counter inside the code under test, a file a child process
// creates — so a test can wait on the condition itself instead of sleeping for
// a guess at how long it takes.
//
// cond returns, besides whether the condition holds, a description of what it
// saw; the failure message carries the last one, so a timeout says how far the
// run got rather than only that it did not finish. what names the condition
// being waited for. pollUntil calls t.Fatalf, so it must run on the test's own
// goroutine.
func pollUntil(t *testing.T, what string, cond func() (bool, string)) {
	t.Helper()
	deadline := time.Now().Add(signalWaitDeadline)
	for {
		ok, seen := cond()
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("waiting for %s: not reached within %v; last seen: %s", what, signalWaitDeadline, seen)
		}
		time.Sleep(2 * time.Millisecond) // polling: cond() has not yet reported the condition named in what
	}
}

// blockingChildWithReadyFile returns an exec factory whose child announces that
// it is running, by creating readyFile, and then blocks for an hour. It is the
// "sleep 3600" stub with a start signal: a test that cancels the child can wait
// for readyFile (see waitForReadyFile) and so cancel a child that is genuinely
// running, instead of guessing how long a spawn takes.
//
// The path travels as a positional argument rather than inside the script or
// the environment: a caller may replace the child's environment wholesale, and
// an argument needs no quoting. `: >` is a shell builtin, so creating the file
// does not depend on PATH; exec keeps the sleeping process the one the context
// kills.
func blockingChildWithReadyFile(readyFile string) func(ctx context.Context, name string, arg ...string) *exec.Cmd {
	return func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", `: > "$1" && exec sleep 3600`, "sh", readyFile)
	}
}

// waitForReadyFile waits until the child started by blockingChildWithReadyFile
// has created readyFile, failing the test with what the stat saw when it does
// not within signalWaitDeadline. returned reports whether the call under test
// has already come back, which is the usual reason a child never starts.
func waitForReadyFile(t *testing.T, readyFile string, returned func() bool) {
	t.Helper()
	pollUntil(t, "the stub child to create its ready file "+readyFile, func() (bool, string) {
		_, err := os.Stat(readyFile)
		if err == nil {
			return true, ""
		}
		if !errors.Is(err, os.ErrNotExist) {
			return false, fmt.Sprintf("stat error %v", err)
		}
		return false, fmt.Sprintf("no ready file yet (call under test already returned: %t)", returned())
	})
}
