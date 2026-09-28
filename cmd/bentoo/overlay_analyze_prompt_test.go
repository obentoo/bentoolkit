package main

import (
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// Story 054, R4.3 at the prompts: overlay analyze holds a signal context while
// it analyzes, and while that handler is registered a Ctrl+C only cancels the
// context — a prompt blocked on stdin would never see it. So the handler is
// released BEFORE a prompt reads: the answer is typed only once release has
// run, and a prompt that reads first gets no answer and says no.
func TestConfirmAfterReleaseReleasesSignalsBeforeReading(t *testing.T) {
	var released atomic.Bool
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = oldStdin; _ = r.Close() })

	go func() {
		defer w.Close()
		deadline := time.Now().Add(3 * time.Second)
		for !released.Load() && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond) // polling: the prompt under test has been reached (released)
		}
		if released.Load() {
			_, _ = w.WriteString("y\n")
		}
	}()

	if !confirmAfterRelease(func() { released.Store(true) }, "Save?") {
		t.Fatal("the prompt read stdin before the signal handler was released: a Ctrl+C there would be swallowed (R4.3)")
	}
}
