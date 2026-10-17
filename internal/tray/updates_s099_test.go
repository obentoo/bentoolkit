package tray

// Story 099, sub-task 5.1: the pure scheduler that decides when an update
// check runs (R1.1-R1.5, R1.11).
//
// Pinned contract (design.md, "internal/tray/updates.go"):
//
//	type checkScheduler struct {
//		pending, running, again bool
//		startAt, revStamp      time.Time // startAt: start + 5 min, zero once fired
//	}
//	func (s *checkScheduler) trigger()
//	func (s *checkScheduler) due(now time.Time, emerge bool) bool
//	func (s *checkScheduler) started()
//	func (s *checkScheduler) finished()
//	func (s *checkScheduler) revisions(mtime time.Time, ok bool) bool
//
// The caller starts a check whenever due reports true, calling started, and
// calls finished when its result arrives. Each test drives the scheduler the
// way the App does and counts the checks that start.

import (
	"testing"
	"time"
)

var s099T0 = time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)

const s099StartDelay = 5 * time.Minute

// s099Sched is a scheduler as the App builds it at start-up.
func s099Sched() *checkScheduler {
	return &checkScheduler{startAt: s099T0.Add(s099StartDelay)}
}

// s099Step asks due once and, when it reports true, starts the check.
func s099Step(s *checkScheduler, now time.Time, emerge bool) bool {
	if !s.due(now, emerge) {
		return false
	}
	s.started()
	return true
}

// s099RunCount drives samples every 30 s from now for n samples with no
// emerge running, finishing each started check at the next sample, and
// counts the checks started.
func s099RunCount(s *checkScheduler, now time.Time, n int) int {
	started := 0
	running := false
	for i := range n {
		at := now.Add(time.Duration(i) * 30 * time.Second)
		if running {
			s.finished()
			running = false
		}
		if s099Step(s, at, false) {
			started++
			running = true
		}
	}
	if running {
		s.finished()
	}
	return started
}

func TestS099Sched_R1_2_NothingDueWithoutATrigger(t *testing.T) {
	s := &checkScheduler{} // zero startAt: the start delay already fired
	for i := range 10 {
		if s.due(s099T0.Add(time.Duration(i)*time.Hour), false) {
			t.Fatalf("due at sample %d with no trigger and no start delay pending", i)
		}
	}
}

func TestS099Sched_R1_2_StartDelayFiresExactlyOnce(t *testing.T) {
	s := s099Sched()
	if s.due(s099T0.Add(s099StartDelay-time.Second), false) {
		t.Fatal("due one second before the 5-minute start delay elapsed")
	}
	if !s099Step(s, s099T0.Add(s099StartDelay), false) {
		t.Fatal("not due once the 5-minute start delay elapsed")
	}
	s.finished()
	if n := s099RunCount(s, s099T0.Add(s099StartDelay+time.Minute), 200); n != 0 {
		t.Errorf("the start delay fired %d more checks after the first; it must fire once", n)
	}
	if !s.startAt.IsZero() {
		t.Errorf("startAt = %v after it fired, want zero", s.startAt)
	}
}

func TestS099Sched_R1_2_R1_4_StartDelayHeldByEmergeStillFiresOnce(t *testing.T) {
	s := s099Sched()
	for i := range 20 { // the delay elapses while an emerge runs
		if s.due(s099T0.Add(s099StartDelay+time.Duration(i)*30*time.Second), true) {
			t.Fatalf("due at held sample %d while emerge runs", i)
		}
	}
	at := s099T0.Add(s099StartDelay + time.Hour)
	if n := s099RunCount(s, at, 50); n != 1 {
		t.Errorf("after the emerge ended the held start delay started %d checks, want exactly 1", n)
	}
}

func TestS099Sched_R1_1_R1_3_ATriggerMakesOneCheckDue(t *testing.T) {
	s := &checkScheduler{}
	s.trigger()
	now := s099T0.Add(time.Hour)
	if !s.due(now, false) {
		t.Fatal("not due after a trigger with no emerge and no check running")
	}
	if n := s099RunCount(s, now, 50); n != 1 {
		t.Errorf("one trigger started %d checks, want exactly 1", n)
	}
}

func TestS099Sched_R1_1_R1_3_TriggersBeforeAStartCollapseIntoOne(t *testing.T) {
	s := s099Sched()
	s.trigger()
	s.trigger()
	s.trigger() // a sync, an emerge exit and the start delay all at once
	if n := s099RunCount(s, s099T0.Add(s099StartDelay), 50); n != 1 {
		t.Errorf("three triggers and the start delay before any check started %d checks, want 1", n)
	}
}

func TestS099Sched_R1_4_EmergeHoldsThenTheFirstFreeSampleReleases(t *testing.T) {
	s := &checkScheduler{}
	s.trigger()
	now := s099T0.Add(time.Hour)
	for i := range 30 {
		if s.due(now.Add(time.Duration(i)*30*time.Second), true) {
			t.Fatalf("due at sample %d while an emerge runs", i)
		}
	}
	if !s.due(now.Add(time.Hour), false) {
		t.Fatal("the held check is not due at the first sample with no emerge")
	}
}

func TestS099Sched_R1_4_TriggerDuringEmergeIsNotLost(t *testing.T) {
	s := &checkScheduler{}
	now := s099T0.Add(time.Hour)
	s.due(now, true)
	s.trigger() // emerge exits successfully while still seen at this sample
	s.due(now.Add(30*time.Second), true)
	if !s.due(now.Add(time.Minute), false) {
		t.Fatal("a trigger that arrived while emerge ran was dropped")
	}
}

func TestS099Sched_R1_5_NoSecondCheckWhileOneRuns(t *testing.T) {
	s := &checkScheduler{}
	s.trigger()
	now := s099T0.Add(time.Hour)
	if !s099Step(s, now, false) {
		t.Fatal("first check not due")
	}
	for i := range 40 {
		s.trigger()
		if s.due(now.Add(time.Duration(i+1)*30*time.Second), false) {
			t.Fatalf("a second check was due at sample %d while the first runs", i)
		}
	}
}

func TestS099Sched_R1_11_TriggersDuringARunYieldExactlyOneMore(t *testing.T) {
	for _, during := range []int{1, 2, 7} {
		s := &checkScheduler{}
		s.trigger()
		now := s099T0.Add(time.Hour)
		if !s099Step(s, now, false) {
			t.Fatal("first check not due")
		}
		for range during {
			s.trigger()
		}
		s.finished()
		if n := s099RunCount(s, now.Add(time.Minute), 50); n != 1 {
			t.Errorf("%d triggers during a run: %d checks followed it, want exactly 1", during, n)
		}
	}
}

func TestS099Sched_R1_11_NoTriggerDuringARunYieldsNoMore(t *testing.T) {
	s := &checkScheduler{}
	s.trigger()
	now := s099T0.Add(time.Hour)
	s099Step(s, now, false)
	s.finished()
	if n := s099RunCount(s, now.Add(time.Minute), 50); n != 0 {
		t.Errorf("a run with no trigger during it was followed by %d checks, want 0", n)
	}
}

func TestS099Sched_R1_11_StartDelayElapsingDuringARunYieldsExactlyOneMore(t *testing.T) {
	s := s099Sched()
	s.trigger()
	early := s099T0.Add(time.Minute) // a sync before the start delay elapsed
	if !s099Step(s, early, false) {
		t.Fatal("the triggered check is not due")
	}
	s.due(s099T0.Add(s099StartDelay), false) // the delay elapses mid-run
	s.finished()
	if n := s099RunCount(s, s099T0.Add(s099StartDelay+time.Minute), 50); n != 1 {
		t.Errorf("the start delay elapsing during a run started %d checks after it, want 1", n)
	}
}

func TestS099Sched_R1_11_TriggersDuringARunHeldByEmergeStillOne(t *testing.T) {
	s := &checkScheduler{}
	s.trigger()
	now := s099T0.Add(time.Hour)
	s099Step(s, now, false)
	s.trigger()
	s.trigger()
	s.finished()
	for i := range 10 {
		if s.due(now.Add(time.Duration(i)*30*time.Second), true) {
			t.Fatalf("the follow-up check was due at sample %d while emerge runs", i)
		}
	}
	if n := s099RunCount(s, now.Add(time.Hour), 50); n != 1 {
		t.Errorf("follow-up after an emerge hold started %d checks, want 1", n)
	}
}

func TestS099Sched_R1_1_RevisionsFirstSightIsSilentLaterChangesReport(t *testing.T) {
	s := &checkScheduler{}
	t1 := s099T0.Add(-24 * time.Hour)
	t2 := s099T0.Add(time.Hour)
	if s.revisions(t1, true) {
		t.Fatal("the first sight of repo_revisions reported a change")
	}
	if s.revisions(t1, true) {
		t.Fatal("an unchanged repo_revisions mtime reported a change")
	}
	if !s.revisions(t2, true) {
		t.Fatal("a later repo_revisions mtime did not report a change")
	}
	if s.revisions(t2, true) {
		t.Fatal("the same new mtime reported a change twice")
	}
	if !s.revisions(t1, true) {
		t.Fatal("an mtime moving backwards (a restored file) did not report a change")
	}
}

func TestS099Sched_R1_1_AbsentRevisionsNeverReport(t *testing.T) {
	s := &checkScheduler{}
	for range 5 {
		if s.revisions(time.Time{}, false) {
			t.Fatal("an absent repo_revisions reported a change")
		}
	}
	seen := &checkScheduler{}
	seen.revisions(s099T0, true)
	if seen.revisions(time.Time{}, false) {
		t.Error("repo_revisions disappearing reported a change; only a new mtime is a sync")
	}
}
