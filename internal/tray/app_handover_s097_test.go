package tray_test

// App-level tests for story 097, sub-task 13.1: a watched path whose reading
// fails while the filesystem it last resolved to is evaluated under an
// earlier watched path withdraws its condition (R2.11), so one filesystem
// never stands under two paths at once (R2.3). In every other failure case
// the failing path's condition is left as it was (R1.3, R2.9).
//
// The harness (s097Harness, s097Sys, s097Notifier) lives in
// app_health_s097_test.go; the downer* paths and helpers live in
// app_diskowner_s097_test.go.

import (
	"slices"
	"strings"
	"testing"
)

// handLater is a watched path listed after downerB on the same filesystem.
// It is not a substring of another watched path, nor the reverse.
const handLater = "/srv/build-scratch"

// handDropLines returns the INFO transition lines that move path's disk
// condition to ok.
func handDropLines(s *s097Harness, path string) []string {
	var out []string
	for _, l := range s.logs.lines() {
		if strings.Contains(l, "level=INFO") && strings.Contains(l, `msg="system condition changed"`) &&
			strings.Contains(l, "monitor=disk") && strings.Contains(l, "target="+path+" ") &&
			strings.Contains(l, "level=ok") {
			out = append(out, l)
		}
	}
	return out
}

// handBOwnsWithWarning starts the App over paths with downerA never read and
// downerB owning device 1 with its warning sent, and returns that warning's id.
func handBOwnsWithWarning(t *testing.T, s *s097Harness, paths []string) uint32 {
	t.Helper()
	s.sys.setDiskErr(downerA, errDownerIO)
	s.sys.setDisk(s097Disk(downerB, 1, s097DiskWarn))
	s.start(paths, nil)
	s.tickUntil("the disk warning of "+downerB, 8, func() bool {
		return len(s.out.matching(s097IsDiskOf(downerB))) == 1
	})
	s.tick()
	if got := s.out.matching(s097IsDiskOf(downerA)); len(got) != 0 {
		t.Fatalf("%s, never read, was announced: %+v", downerA, got)
	}
	return s.out.matching(s097IsDiskOf(downerB))[0].ID
}

// handUntouched fails unless downerB's warning bID still stands as it did:
// nothing sent or closed since sentBefore/closedBefore, no move of downerB to
// ok logged, and its tooltip line unchanged.
func handUntouched(t *testing.T, s *s097Harness, when string, bID uint32, sentBefore, closedBefore int, linesBefore []string, paths ...string) {
	t.Helper()
	if n := len(s.out.system()) - sentBefore; n != 0 {
		t.Errorf("%s: notifications sent = %d, want none (R1.3): %+v", when, n, s.out.system()[sentBefore:])
	}
	if closed := s.out.closedIDs(); slices.Contains(closed, bID) || len(closed) != closedBefore {
		t.Errorf("%s: closed = %v, want nothing (%s's warning %d stands, R1.3)", when, closed, downerB, bID)
	}
	if got := handDropLines(s, downerB); len(got) != 0 {
		t.Errorf("%s: %s's condition was moved to ok (R1.3): %q", when, downerB, got)
	}
	if lines := downerDiskLines(s, paths...); !slices.Equal(lines, linesBefore) {
		t.Errorf("%s: tooltip lines = %q, want %q unchanged", when, lines, linesBefore)
	}
	if open := downerOpenDisks(s); !slices.Equal(open, []string{downerB}) {
		t.Errorf("%s: open disk notifications = %v, want [%s]", when, open, downerB)
	}
}

// TestS097Handover_R2_11_FailingPathYieldsToEarlierOwner is the hostile half:
// B owns device 1 with its warning sent because A, listed first, never read.
// B starts failing as A reads device 1. A is the earlier owner now, so B's
// condition is withdrawn; B keeping it would leave the filesystem announced
// under two paths once A raises its own.
func TestS097Handover_R2_11_FailingPathYieldsToEarlierOwner(t *testing.T) {
	t.Run("A below the threshold: A announces, never both at one sample", func(t *testing.T) {
		s := newS097(t)
		handBOwnsWithWarning(t, s, []string{downerA, downerB})

		s.sys.setDiskErr(downerB, errDownerIO)
		s.sys.setDisk(s097Disk(downerA, 1, s097DiskWarn))
		for i := range 8 { // 4 min: well past the 1 min disk sustain window
			s.tick()
			downerOneFilesystemAtATime(t, s, "while "+downerB+" fails", downerA, downerB)
			if t.Failed() {
				t.Fatalf("after %d samples: sent %+v, closed %v", i+1, s.out.system(), s.out.closedIDs())
			}
		}
		if n := len(s.out.matching(s097IsDiskOf(downerA))); n != 1 {
			t.Errorf("warnings naming %s = %d, want 1 after its sustain window", downerA, n)
		}
		if n := len(s.out.matching(s097IsDiskOf(downerB))); n != 1 {
			t.Errorf("warnings naming %s = %d, want only the first one", downerB, n)
		}
		if open := downerOpenDisks(s); !slices.Equal(open, []string{downerA}) {
			t.Errorf("open disk notifications = %v, want [%s]", open, downerA)
		}
		if lines := downerDiskLines(s, downerA, downerB); len(lines) != 1 || !strings.Contains(lines[0], downerA) {
			t.Errorf("tooltip lines = %q, want only %s's line", lines, downerA)
		}
		if got := handDropLines(s, downerB); len(got) != 1 {
			t.Errorf("INFO lines moving %s to ok = %d, want 1 (R6.13): %q", downerB, len(got), got)
		}
	})

	t.Run("B's warning is withdrawn at the first sample A owns device 1", func(t *testing.T) {
		s := newS097(t)
		bID := handBOwnsWithWarning(t, s, []string{downerA, downerB})

		s.sys.setDiskErr(downerB, errDownerIO)
		s.sys.setDisk(s097Disk(downerA, 1, s097DiskWarn))
		s.tick()
		if !slices.Contains(s.out.closedIDs(), bID) {
			t.Errorf("after the first sample with %s failing and %s owning device 1: closed %v, want %s's warning %d withdrawn (R2.11)",
				downerB, downerA, s.out.closedIDs(), downerB, bID)
		}
		if !slices.Contains(s.out.closedKeys(), "disk:"+downerB) {
			t.Errorf("closed keys = %v, want disk:%s", s.out.closedKeys(), downerB)
		}
		if lines := downerDiskLines(s, downerA, downerB); len(lines) != 0 {
			t.Errorf("tooltip lines = %q, want none: %s's condition is withdrawn and %s's is still pending", lines, downerB, downerA)
		}
	})

	t.Run("the move to ok names monitor, target and level, and has no value", func(t *testing.T) {
		s := newS097(t)
		handBOwnsWithWarning(t, s, []string{downerA, downerB})

		s.sys.setDiskErr(downerB, errDownerIO)
		s.sys.setDisk(s097Disk(downerA, 1, s097DiskWarn))
		s.ticks(3)
		got := handDropLines(s, downerB)
		if len(got) != 1 {
			t.Fatalf("INFO lines moving %s to ok = %q, want 1\n%s", downerB, got, strings.Join(s.logs.lines(), "\n"))
		}
		line := got[0]
		for _, want := range []string{"monitor=disk", "target=" + downerB + " ", "level=ok", "from=warning"} {
			if !strings.Contains(line, want) {
				t.Errorf("INFO line %q lacks %q", line, want)
			}
		}
		if strings.Contains(line, " value=") {
			t.Errorf("INFO line %q carries a value key; %s has no reading", line, downerB)
		}
	})

	t.Run("A above the threshold: B withdrawn, nothing announced", func(t *testing.T) {
		s := newS097(t)
		bID := handBOwnsWithWarning(t, s, []string{downerA, downerB})

		s.sys.setDiskErr(downerB, errDownerIO)
		s.sys.setDisk(s097Disk(downerA, 1, s097DiskOK))
		s.ticks(6)
		if !slices.Contains(s.out.closedIDs(), bID) {
			t.Errorf("closed = %v, want %s's warning %d withdrawn: device 1 is %s's and it reads ok",
				s.out.closedIDs(), downerB, bID, downerA)
		}
		if open := downerOpenDisks(s); len(open) != 0 {
			t.Errorf("open disk notifications = %v, want none", open)
		}
		if lines := downerDiskLines(s, downerA, downerB); len(lines) != 0 {
			t.Errorf("tooltip lines = %q, want none", lines)
		}
	})

	t.Run("B reading again stays the duplicate", func(t *testing.T) {
		s := newS097(t)
		bID := handBOwnsWithWarning(t, s, []string{downerA, downerB})

		s.sys.setDiskErr(downerB, errDownerIO)
		s.sys.setDisk(s097Disk(downerA, 1, s097DiskWarn))
		s.tickUntil("the disk warning of "+downerA, 8, func() bool {
			return len(s.out.matching(s097IsDiskOf(downerA))) == 1
		})
		s.sys.setDisk(s097Disk(downerB, 1, s097DiskWarn))
		for i := range 6 {
			s.tick()
			downerOneFilesystemAtATime(t, s, "after "+downerB+" reads again", downerA, downerB)
			if t.Failed() {
				t.Fatalf("after %d samples: sent %+v, closed %v", i+1, s.out.system(), s.out.closedIDs())
			}
		}
		if !slices.Contains(s.out.closedIDs(), bID) {
			t.Errorf("closed = %v, want %s's warning %d withdrawn", s.out.closedIDs(), downerB, bID)
		}
		if n := len(s.out.matching(s097IsDiskOf(downerB))); n != 1 {
			t.Errorf("warnings naming %s = %d, want only the first one", downerB, n)
		}
	})
}

// TestS097Handover_R1_3_NoEarlierOwnerKeepsTheCondition is the converse: with
// no earlier path evaluating device 1 in this sample, B's failure leaves its
// condition, notification and tooltip line exactly as they were.
func TestS097Handover_R1_3_NoEarlierOwnerKeepsTheCondition(t *testing.T) {
	t.Run("the earlier path keeps failing, never read", func(t *testing.T) {
		s := newS097(t)
		bID := handBOwnsWithWarning(t, s, []string{downerA, downerB})
		sentBefore, closedBefore := len(s.out.system()), len(s.out.closedIDs())
		linesBefore := downerDiskLines(s, downerA, downerB)

		s.sys.setDiskErr(downerB, errDownerIO)
		s.ticks(8)
		handUntouched(t, s, "while "+downerA+" and "+downerB+" fail", bID, sentBefore, closedBefore, linesBefore, downerA, downerB)
	})

	t.Run("the earlier path reads another filesystem", func(t *testing.T) {
		s := newS097(t)
		s.sys.setDisk(s097Disk(downerA, 2, s097DiskOK))
		s.sys.setDisk(s097Disk(downerB, 1, s097DiskWarn))
		s.start([]string{downerA, downerB}, nil)
		s.tickUntil("the disk warning of "+downerB, 8, func() bool {
			return len(s.out.matching(s097IsDiskOf(downerB))) == 1
		})
		s.tick()
		bID := s.out.matching(s097IsDiskOf(downerB))[0].ID
		sentBefore, closedBefore := len(s.out.system()), len(s.out.closedIDs())
		linesBefore := downerDiskLines(s, downerA, downerB)

		s.sys.setDiskErr(downerB, errDownerIO)
		s.ticks(8)
		handUntouched(t, s, "while "+downerB+" fails and "+downerA+" reads device 2", bID, sentBefore, closedBefore, linesBefore, downerA, downerB)
	})
}

// TestS097Handover_R2_9_LaterPathOnTheDeviceLeavesBItsCondition: B claims its
// last device before any later path is evaluated, so a later path reading
// device 1 is the duplicate. B, failing, keeps its condition and the later
// path raises nothing.
func TestS097Handover_R2_9_LaterPathOnTheDeviceLeavesBItsCondition(t *testing.T) {
	s := newS097(t)
	s.sys.setDiskErr(handLater, errDownerIO)
	bID := handBOwnsWithWarning(t, s, []string{downerA, downerB, handLater})
	sentBefore, closedBefore := len(s.out.system()), len(s.out.closedIDs())
	linesBefore := downerDiskLines(s, downerA, downerB, handLater)

	s.sys.setDiskErr(downerB, errDownerIO)
	s.sys.setDisk(s097Disk(handLater, 1, s097DiskWarn))
	for i := range 8 {
		s.tick()
		downerOneFilesystemAtATime(t, s, "while "+downerB+" fails", downerA, downerB, handLater)
		if t.Failed() {
			t.Fatalf("after %d samples: sent %+v, closed %v", i+1, s.out.system(), s.out.closedIDs())
		}
	}
	if got := s.out.matching(s097IsDiskOf(handLater)); len(got) != 0 {
		t.Errorf("%s announced the filesystem %s still owns: %+v", handLater, downerB, got)
	}
	handUntouched(t, s, "while "+downerB+" fails", bID, sentBefore, closedBefore, linesBefore, downerA, downerB, handLater)
}
