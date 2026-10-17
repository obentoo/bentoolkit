package tray

// Story 099, sub-task 5.1, fix round 1 (F1): a repo_revisions file that was
// absent at an earlier sample and appears later was written by a sync, the
// first one ever, and reports a change (R1.1).

import (
	"testing"
	"time"
)

func TestS099Sched_R1_1_RevisionsCreatedAfterAbsentReportsOnce(t *testing.T) {
	s := &checkScheduler{}
	if s.revisions(time.Time{}, false) {
		t.Fatal("the first sample, with repo_revisions absent, reported a change")
	}
	if s.revisions(time.Time{}, false) {
		t.Fatal("repo_revisions still absent reported a change")
	}
	if !s.revisions(s099T0, true) {
		t.Fatal("repo_revisions created after an absent sample did not report a change; its creation is a sync")
	}
	if s.revisions(s099T0, true) {
		t.Fatal("the created file's unchanged mtime reported a change twice")
	}
}

func TestS099Sched_R1_1_RevisionsReappearingKeepsTheLastMtime(t *testing.T) {
	t1 := s099T0.Add(-time.Hour)
	t2 := s099T0.Add(time.Hour)

	same := &checkScheduler{}
	same.revisions(t1, true)
	if same.revisions(time.Time{}, false) {
		t.Fatal("repo_revisions disappearing reported a change")
	}
	if same.revisions(t1, true) {
		t.Error("repo_revisions reappearing with the mtime last seen reported a change")
	}

	moved := &checkScheduler{}
	moved.revisions(t1, true)
	moved.revisions(time.Time{}, false)
	if !moved.revisions(t2, true) {
		t.Error("repo_revisions reappearing with a new mtime did not report a change")
	}
	if moved.revisions(t2, true) {
		t.Error("the reappeared file's unchanged mtime reported a change twice")
	}
}
