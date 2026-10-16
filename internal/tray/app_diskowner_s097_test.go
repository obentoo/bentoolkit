package tray_test

// App-level tests for story 097, sub-task 10.1: a watched path whose reading
// fails keeps owning the filesystem it last resolved to (R2.9), so a later
// watched path on that filesystem raises nothing of its own; a path that never
// read claims nothing (R2.9), two reading paths stay one condition under the
// first (R2.3), and the failing path's own condition is left as it was (R1.3).
//
// The harness (s097Harness, s097Sys, s097Notifier) lives in
// app_health_s097_test.go and app_render_s097_test.go.

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// Watched paths of these tests. None is a substring of another, so a tooltip
// line names exactly one of them.
const (
	downerA = "/var/tmp/portage"     // listed first: owns the filesystem
	downerB = "/var/cache/distfiles" // listed later on the same filesystem
	downerC = "/home"                // a filesystem of its own
)

var errDownerIO = errors.New("statfs: input/output error")

// downerOpenDisks returns the watched paths whose last disk notification is
// still open: sent and not withdrawn. A replacement keeps the key open.
func downerOpenDisks(s *s097Harness) []string {
	last := map[string]uint32{}
	var order []string
	for _, sent := range s.out.matching(s097IsDisk) {
		path := strings.TrimPrefix(sent.Msg.Key, "disk:")
		if _, seen := last[path]; !seen {
			order = append(order, path)
		}
		last[path] = sent.ID
	}
	closed := s.out.closedIDs()
	var open []string
	for _, p := range order {
		if !slices.Contains(closed, last[p]) {
			open = append(open, p)
		}
	}
	return open
}

// downerDiskLines returns the icon's tooltip lines that name one of paths.
func downerDiskLines(s *s097Harness, paths ...string) []string {
	var out []string
	for _, l := range s.viewNow().Alerts {
		for _, p := range paths {
			if strings.Contains(l, p) {
				out = append(out, l)
				break
			}
		}
	}
	return out
}

// downerOneFilesystemAtATime fails when two watched paths of one filesystem
// hold an open notification or a tooltip line at the same time.
func downerOneFilesystemAtATime(t *testing.T, s *s097Harness, when string, paths ...string) {
	t.Helper()
	if open := downerOpenDisks(s); len(open) > 1 {
		t.Errorf("%s: open disk notifications for one filesystem = %v, want at most one", when, open)
	}
	if lines := downerDiskLines(s, paths...); len(lines) > 1 {
		t.Errorf("%s: tooltip lines for one filesystem = %q, want at most one", when, lines)
	}
}

// TestS097DiskOwner_R2_9_FailingOwnerKeepsItsFilesystem is the hostile half:
// the owning path A is low on space with its warning sent; A's reading starts
// failing while B, on the same filesystem, reads the same figures. B must not
// become the owner and announce the filesystem a second time.
func TestS097DiskOwner_R2_9_FailingOwnerKeepsItsFilesystem(t *testing.T) {
	t.Run("a later path on the failing owner's filesystem raises nothing", func(t *testing.T) {
		s := newS097(t)
		s.sys.setDisk(s097Disk(downerA, 1, s097DiskWarn))
		s.sys.setDisk(s097Disk(downerB, 1, s097DiskWarn))
		s.start([]string{downerA, downerB}, nil)
		s.tickUntil("the disk warning of "+downerA, 8, func() bool {
			return len(s.out.matching(s097IsDiskOf(downerA))) == 1
		})
		s.tick()
		aID := s.out.matching(s097IsDiskOf(downerA))[0].ID
		sentBefore := len(s.out.system())
		closedBefore := len(s.out.closedIDs())
		linesBefore := downerDiskLines(s, downerA, downerB)
		if len(linesBefore) != 1 || !strings.Contains(linesBefore[0], downerA) {
			t.Fatalf("tooltip lines before the failure = %q, want one line naming %s", linesBefore, downerA)
		}

		s.sys.setDiskErr(downerA, errDownerIO)
		for i := range 8 { // 4 min: well past the 1 min disk sustain window
			s.tick()
			downerOneFilesystemAtATime(t, s, "while "+downerA+" fails", downerA, downerB)
			if t.Failed() {
				t.Fatalf("after %d failed samples of %s: sent %+v, closed %v", i+1, downerA, s.out.system(), s.out.closedIDs())
			}
		}
		if got := s.out.matching(s097IsDiskOf(downerB)); len(got) != 0 {
			t.Errorf("%s announced the filesystem %s still owns while its reading fails: %+v", downerB, downerA, got)
		}
		if n := len(s.out.system()) - sentBefore; n != 0 {
			t.Errorf("notifications sent while %s fails = %d, want none (R1.3, R2.9): %+v", downerA, n, s.out.system()[sentBefore:])
		}
		if slices.Contains(s.out.closedIDs(), aID) || len(s.out.closedIDs()) != closedBefore {
			t.Errorf("closed while %s fails = %v, want nothing (its warning %d stands, R1.3)", downerA, s.out.closedIDs(), aID)
		}
		if lines := downerDiskLines(s, downerA, downerB); !slices.Equal(lines, linesBefore) {
			t.Errorf("tooltip lines while %s fails = %q, want %q unchanged", downerA, lines, linesBefore)
		}

		// A reads again with the same figures: it still owns the filesystem,
		// its warning is neither repeated nor withdrawn.
		s.sys.setDisk(s097Disk(downerA, 1, s097DiskWarn))
		s.ticks(4)
		if n := len(s.out.system()) - sentBefore; n != 0 {
			t.Errorf("notifications sent after %s reads again = %d, want none: %+v", downerA, n, s.out.system()[sentBefore:])
		}
		if slices.Contains(s.out.closedIDs(), aID) {
			t.Errorf("%s's warning %d was withdrawn after it read again with the same figures", downerA, aID)
		}
		if open := downerOpenDisks(s); !slices.Equal(open, []string{downerA}) {
			t.Errorf("open disk notifications after %s reads again = %v, want [%s]", downerA, open, downerA)
		}
	})

	t.Run("a second failing path does not hand the filesystem on", func(t *testing.T) {
		// A and A2 both resolved to device 1, A first; both start failing.
		// A keeps the filesystem, A2 takes nothing, B stays the duplicate.
		const downerA2 = "/var/tmp/notmpfs"
		s := newS097(t)
		s.sys.setDisk(s097Disk(downerA, 1, s097DiskWarn))
		s.sys.setDisk(s097Disk(downerA2, 1, s097DiskWarn))
		s.sys.setDisk(s097Disk(downerB, 1, s097DiskWarn))
		s.start([]string{downerA, downerA2, downerB}, nil)
		s.tickUntil("the disk warning of "+downerA, 8, func() bool {
			return len(s.out.matching(s097IsDiskOf(downerA))) == 1
		})
		s.tick()
		sentBefore := len(s.out.system())

		s.sys.setDiskErr(downerA, errDownerIO)
		s.sys.setDiskErr(downerA2, errDownerIO)
		s.ticks(8)
		if n := len(s.out.system()) - sentBefore; n != 0 {
			t.Errorf("notifications sent while %s and %s fail = %d, want none: %+v",
				downerA, downerA2, n, s.out.system()[sentBefore:])
		}
		if lines := downerDiskLines(s, downerA, downerA2, downerB); len(lines) != 1 || !strings.Contains(lines[0], downerA) {
			t.Errorf("tooltip lines = %q, want only %s's line", lines, downerA)
		}
	})
}

// TestS097DiskOwner_R2_9_NeverReadPathClaimsNothing is the converse: a path
// that never read has no filesystem to keep, so the later path on that
// filesystem owns and announces it. When the first path reads, it takes the
// filesystem back without the two ever standing at once.
func TestS097DiskOwner_R2_9_NeverReadPathClaimsNothing(t *testing.T) {
	t.Run("the later path announces, then hands the filesystem back", func(t *testing.T) {
		s := newS097(t)
		s.sys.setDiskErr(downerA, errDownerIO)
		s.sys.setDisk(s097Disk(downerB, 1, s097DiskWarn))
		s.start([]string{downerA, downerB}, nil)
		s.tickUntil("the disk warning of "+downerB, 8, func() bool {
			return len(s.out.matching(s097IsDiskOf(downerB))) == 1
		})
		if got := s.out.matching(s097IsDiskOf(downerA)); len(got) != 0 {
			t.Fatalf("%s, never read, was announced: %+v", downerA, got)
		}

		s.sys.setDisk(s097Disk(downerA, 1, s097DiskWarn))
		for i := range 8 {
			s.tick()
			downerOneFilesystemAtATime(t, s, "after "+downerA+" reads", downerA, downerB)
			if t.Failed() {
				t.Fatalf("after %d samples: sent %+v, closed %v", i+1, s.out.system(), s.out.closedIDs())
			}
		}
		if !slices.Contains(s.out.closedKeys(), "disk:"+downerB) {
			t.Errorf("closed keys = %v, want %s's warning withdrawn once %s owns the filesystem",
				s.out.closedKeys(), downerB, downerA)
		}
		if n := len(s.out.matching(s097IsDiskOf(downerA))); n != 1 {
			t.Errorf("warnings naming %s = %d, want 1", downerA, n)
		}
		if open := downerOpenDisks(s); !slices.Equal(open, []string{downerA}) {
			t.Errorf("open disk notifications = %v, want [%s]", open, downerA)
		}
		if lines := downerDiskLines(s, downerA, downerB); len(lines) != 1 || !strings.Contains(lines[0], downerA) {
			t.Errorf("tooltip lines = %q, want only %s's line", lines, downerA)
		}
	})

	t.Run("a never-read path claims no device, not even device 0", func(t *testing.T) {
		s := newS097(t)
		s.sys.setDiskErr(downerA, errDownerIO)
		s.sys.setDisk(s097Disk(downerB, 0, s097DiskWarn))
		s.start([]string{downerA, downerB}, nil)
		s.tickUntil("the disk warning of "+downerB+" on device 0", 8, func() bool {
			return len(s.out.matching(s097IsDiskOf(downerB))) == 1
		})
	})
}

// TestS097DiskOwner_R2_3_BothReadingIsOneConditionUnderTheFirst: with every
// path reading, one filesystem is still one condition, named by the first
// watched path that resolved to it.
func TestS097DiskOwner_R2_3_BothReadingIsOneConditionUnderTheFirst(t *testing.T) {
	s := newS097(t)
	s.sys.setDisk(s097Disk(downerA, 1, s097DiskCrit))
	s.sys.setDisk(s097Disk(downerB, 1, s097DiskCrit))
	s.start([]string{downerA, downerB}, nil)
	s.tickUntil("the disk notification of "+downerA, 8, func() bool {
		return len(s.out.matching(s097IsDiskOf(downerA))) == 1
	})
	s.ticks(4)
	if got := s.out.matching(s097IsDisk); len(got) != 1 {
		t.Errorf("disk notifications = %+v, want one, naming %s", got, downerA)
	}
	if lines := downerDiskLines(s, downerA, downerB); len(lines) != 1 || !strings.Contains(lines[0], downerA) {
		t.Errorf("tooltip lines = %q, want only %s's line", lines, downerA)
	}
}

// TestS097DiskOwner_R2_9_FailingPathNeverClaimsAnotherDevice: the failing path
// keeps only the filesystem it last resolved to; a path on another filesystem
// is evaluated and announced as before.
func TestS097DiskOwner_R2_9_FailingPathNeverClaimsAnotherDevice(t *testing.T) {
	s := newS097(t)
	s.sys.setDisk(s097Disk(downerA, 1, s097DiskOK))
	s.sys.setDisk(s097Disk(downerC, 2, s097DiskOK))
	s.start([]string{downerA, downerC}, nil)
	s.ticks(2) // A has resolved to device 1

	s.sys.setDiskErr(downerA, errDownerIO)
	s.sys.setDisk(s097Disk(downerC, 2, s097DiskWarn))
	s.tickUntil("the disk warning of "+downerC, 8, func() bool {
		return len(s.out.matching(s097IsDiskOf(downerC))) == 1
	})
	if got := s.out.matching(s097IsDiskOf(downerA)); len(got) != 0 {
		t.Errorf("%s, failing, was announced: %+v", downerA, got)
	}
	if lines := downerDiskLines(s, downerA, downerC); len(lines) != 1 || !strings.Contains(lines[0], downerC) {
		t.Errorf("tooltip lines = %q, want only %s's line", lines, downerC)
	}
}
