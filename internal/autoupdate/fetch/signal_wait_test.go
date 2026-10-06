package fetch

import (
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
