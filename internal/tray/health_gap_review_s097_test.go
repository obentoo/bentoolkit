package tray

// Regression test for the Task 5 tech review of story 097: the suspend gap of
// observe is measured on the wall clock.
//
// A real suspend moves the wall clock of two time.Now() readings apart while
// their monotonic readings stay close. Pure Go cannot build such a pair: Add
// moves both readings together, and only a change of the system clock moves
// the wall reading alone. So the test pins the helper that strips the
// monotonic reading, then checks that observe restarts a spell across a gap
// between two monotonic-bearing samples.

import (
	"strings"
	"testing"
	"time"
)

// TestS097TaskFiveReview_WallClockStripsTheMonotonicReading: the time the gap
// is measured on carries no monotonic reading, so Sub compares wall clocks.
func TestS097TaskFiveReview_WallClockStripsTheMonotonicReading(t *testing.T) {
	now := time.Now()
	if !strings.Contains(now.String(), "m=") {
		t.Fatalf("time.Now() = %s carries no monotonic reading; the premise of this test is gone", now)
	}
	w := wallClock(now)
	if strings.Contains(w.String(), "m=") {
		t.Errorf("wallClock(%s) = %s, still carries a monotonic reading", now, w)
	}
	if !w.Equal(now) {
		t.Errorf("wallClock(%s) = %s, want the same instant", now, w)
	}
}

// TestS097TaskFiveReview_GapOnMonotonicTimesRestartsTheSpell: a sample that
// arrives more than maxGap after the previous one, both read from time.Now(),
// restarts the pending spell instead of counting the gap as sustained.
func TestS097TaskFiveReview_GapOnMonotonicTimesRestartsTheSpell(t *testing.T) {
	const (
		window = time.Minute
		gapMax = time.Minute
	)
	var c condition
	start := time.Now()
	if _, changed := c.observe(start, levelWarning, window, time.Hour, gapMax, false); changed {
		t.Fatal("the first warning reading changed the condition, want a pending spell")
	}
	resumed := start.Add(3 * time.Hour)
	tr, changed := c.observe(resumed, levelWarning, window, time.Hour, gapMax, false)
	if changed || c.level != levelOK {
		t.Fatalf("after a 3 h gap: changed = %v (%+v), level = %v; want the spell restarted, still ok", changed, tr, c.level)
	}
	if !c.since.Equal(resumed) {
		t.Errorf("spell start after the gap = %s, want %s", c.since, resumed)
	}
}
