package fixer

// Authored for story 092, sub-task 7.2, test-after: an abandoned callback's
// queued lines are counted in stranded, so the WARN's count covers every line
// offered and never handed to the callback, not only those the queue refused.

import (
	"testing"
	"time"
)

func TestAgentStreamStrandedCountsQueuedLinesOfAnAbandonedCallback(t *testing.T) {
	const total = progressQueue + 50
	release := make(chan struct{})
	defer close(release)
	f := startProgressForwarder(func(string) { <-release })
	for range total {
		f.offer("line")
	}
	dropped, abandoned := f.finish(50 * time.Millisecond)
	if !abandoned {
		t.Fatal("fixture: a blocked callback was not abandoned")
	}
	f.mu.Lock()
	started := f.started
	f.mu.Unlock()
	if started > 1 {
		t.Fatalf("fixture: %d lines handed to a callback blocked on its first", started)
	}
	if got := dropped + f.stranded + started; got != total {
		t.Errorf("dropped %d + stranded %d + handed %d = %d, want every one of the %d lines offered",
			dropped, f.stranded, started, got, total)
	}
	if f.stranded < progressQueue-1 {
		t.Errorf("stranded = %d, want at least %d: the queue was full when the callback was abandoned", f.stranded, progressQueue-1)
	}
}
