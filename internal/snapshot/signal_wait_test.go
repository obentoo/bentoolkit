package snapshot

import (
	"errors"
	"fmt"
	"os"
	"testing"
	"time"
)

// signalWaitDeadline bounds every wait these helpers perform. A replacement for
// a sleep must never wait forever: a barrier that cannot fail turns a flaky
// test into a hung one. It mirrors internal/autoupdate's helper of the same
// name; test files cannot be imported across packages.
const signalWaitDeadline = 5 * time.Second

// pollUntil re-evaluates cond until it reports true, and fails the test when
// signalWaitDeadline passes first. It exists for the conditions no channel
// announces — a file a child process creates — so a test can wait on the
// condition itself instead of sleeping for a guess at how long it takes.
//
// cond returns, besides whether the condition holds, a description of what it
// saw; the failure message carries the last one. what names the condition
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

// waitForReadyFile waits until a stub child has created readyFile, failing the
// test with what the stat saw when it does not within signalWaitDeadline.
// returned reports whether the call under test has already come back, which
// is the usual reason a child never starts.
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
