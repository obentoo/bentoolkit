package tray

import (
	"sync/atomic"
	"testing"
	"time"
)

// jumpClock is the real clock plus an offset a test can move forward, as a
// resume from suspend moves the wall clock without the monotonic one.
type jumpClock struct{ offset atomic.Int64 }

func (j *jumpClock) now() time.Time { return time.Now().Add(time.Duration(j.offset.Load())) }

// TestWallAfter_FiresAfterAWallClockJump: a one-hour timer fires right after
// the wall clock jumps past its deadline, without waiting an hour of
// monotonic time.
func TestWallAfter_FiresAfterAWallClockJump(t *testing.T) {
	j := &jumpClock{}
	done := make(chan struct{})
	defer close(done)
	ch := wallAfter(time.Hour, j.now, 10*time.Millisecond, done)
	select {
	case <-ch:
		t.Fatal("fired before its deadline")
	case <-time.After(50 * time.Millisecond):
	}
	j.offset.Store(int64(2 * time.Hour)) // resumed from a long suspend
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("did not fire after the wall clock passed its deadline")
	}
}

// TestWallAfter_FiresNotBeforeTheDeadline: without a jump it waits its full
// duration.
func TestWallAfter_FiresNotBeforeTheDeadline(t *testing.T) {
	start := time.Now()
	ch := wallAfter(80*time.Millisecond, time.Now, 10*time.Millisecond, nil)
	select {
	case <-ch:
		if d := time.Since(start); d < 80*time.Millisecond {
			t.Errorf("fired after %s, want at least 80ms", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("never fired")
	}
}

// TestWallAfter_DoneStopsAPendingTimer: closing Done ends the timer's
// goroutine without firing.
func TestWallAfter_DoneStopsAPendingTimer(t *testing.T) {
	done := make(chan struct{})
	ch := wallAfter(time.Hour, time.Now, 10*time.Millisecond, done)
	close(done)
	select {
	case <-ch:
		t.Fatal("a stopped timer fired")
	case <-time.After(100 * time.Millisecond):
	}
}
