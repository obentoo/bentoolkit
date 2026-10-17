package tray

// Story 099, sub-task 5.1: waiting(now), the pure query the App uses to scan
// /proc for a running emerge only when a check could start (design.md,
// "Review decisions").

import (
	"testing"
	"time"
)

func TestS099Sched_WaitingFalseWithNothingPending(t *testing.T) {
	s := &checkScheduler{}
	for i := range 5 {
		if s.waiting(s099T0.Add(time.Duration(i) * time.Hour)) {
			t.Fatalf("waiting at sample %d with no trigger and no start delay", i)
		}
	}
	early := s099Sched()
	if early.waiting(s099T0.Add(s099StartDelay - time.Second)) {
		t.Error("waiting one second before the start delay elapsed")
	}
}

func TestS099Sched_WaitingTrueWhenPendingOrStartDelayElapsed(t *testing.T) {
	s := &checkScheduler{}
	s.trigger()
	if !s.waiting(s099T0) {
		t.Error("not waiting after a trigger")
	}
	d := s099Sched()
	if !d.waiting(s099T0.Add(s099StartDelay)) {
		t.Error("not waiting once the start delay elapsed")
	}
}

func TestS099Sched_WaitingFalseWhileRunning(t *testing.T) {
	s := &checkScheduler{}
	s.trigger()
	s099Step(s, s099T0, false)
	s.trigger() // queued as the one more check, not startable yet
	if s.waiting(s099T0.Add(time.Minute)) {
		t.Error("waiting while a check runs")
	}
	s.finished()
	if !s.waiting(s099T0.Add(2 * time.Minute)) {
		t.Error("not waiting after the run ended with a trigger queued")
	}
}

func TestS099Sched_WaitingChangesNothing(t *testing.T) {
	s := s099Sched()
	at := s099T0.Add(s099StartDelay)
	for range 3 {
		s.waiting(at)
	}
	if s.startAt.IsZero() || s.pending {
		t.Fatalf("waiting fired the start delay (startAt=%v pending=%v); it must be pure", s.startAt, s.pending)
	}
	if n := s099RunCount(s, at, 20); n != 1 {
		t.Errorf("after waiting was asked, the start delay started %d checks, want 1", n)
	}
}

func TestS099Sched_WaitingAgreesWithDueWithoutEmerge(t *testing.T) {
	cases := map[string]func() *checkScheduler{
		"idle":          func() *checkScheduler { return &checkScheduler{} },
		"start delay":   s099Sched,
		"triggered":     func() *checkScheduler { s := &checkScheduler{}; s.trigger(); return s },
		"running":       func() *checkScheduler { s := &checkScheduler{}; s.trigger(); s.started(); return s },
		"running+again": func() *checkScheduler { s := &checkScheduler{}; s.trigger(); s.started(); s.trigger(); return s },
	}
	for name, mk := range cases {
		for _, at := range []time.Time{s099T0, s099T0.Add(s099StartDelay)} {
			s := mk()
			w := s.waiting(at)
			if d := s.due(at, false); w != d {
				t.Errorf("%s at %v: waiting=%v but due(no emerge)=%v", name, at.Sub(s099T0), w, d)
			}
		}
	}
}
