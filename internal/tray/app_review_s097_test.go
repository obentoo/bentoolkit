package tray_test

// Regression tests for the Task 5 tech review of story 097: a "network down"
// whose Send failed during a steady outage, and a disk path that moves onto
// a filesystem already watched under an earlier path. They reuse the
// harnesses of app_health_s097_test.go and app_netalert_s097_test.go.

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestS097TaskFiveReview_FailedDownIsRetriedWhileOffline: a "network down"
// whose one Send failed is retried while NetworkManager keeps reporting the
// same offline verdict, so the outage is still announced once the
// notification server is back. The failure streak logs one WARN, however
// many retries fail.
func TestS097TaskFiveReview_FailedDownIsRetriedWhileOffline(t *testing.T) {
	s := newS097Net(t)
	s.startNet()
	gone := errors.New("s097 review: notification server gone")
	s.out.setSendErr(gone)
	s.setOnline(false)
	s.advance(30 * time.Second)
	s.waitFor("the failed down", func() bool { return s.out.failedSends() >= 1 })
	s086WaitParked(s.appHarness)

	s.advance(5 * time.Minute) // the server is still gone: every retry fails
	if n := s.out.failedSends(); n < 2 {
		t.Fatalf("failed down attempts after 5 min offline = %d, want at least 2 (retried)", n)
	}
	if n := s.countLines("level=WARN", gone.Error()); n != 1 {
		t.Errorf("WARN lines over a streak of %d failed downs = %d, want 1\n%s",
			s.out.failedSends(), n, strings.Join(s.logs.lines(), "\n"))
	}

	s.out.setSendErr(nil) // the notification server is back
	s.advance(5 * time.Minute)
	if n := len(s.out.matching(s097IsNetDown)); n != 1 {
		t.Fatalf("downs sent once the server is back, still offline = %d, want 1", n)
	}
	failed := s.out.failedSends()
	s.advance(5 * time.Minute)
	if n := len(s.out.matching(s097IsNetDown)); n != 1 || s.out.failedSends() != failed {
		t.Errorf("after the down was sent: downs = %d, failed = %d (was %d); want 1 and no new attempt",
			n, s.out.failedSends(), failed)
	}
}

// TestS097TaskFiveReview_DedupedPathKeepsNoAlert: a disk path that was
// critical on its own filesystem and later resolves to a filesystem already
// watched under an earlier path is no longer evaluated, so its condition is
// dropped: its notification is withdrawn, its tooltip line and the critical
// icon go, and an INFO line records its return to ok.
func TestS097TaskFiveReview_DedupedPathKeepsNoAlert(t *testing.T) {
	s := newS097(t)
	s.sys.setDisk(s097Disk("/", 1, s097DiskOK))
	s.sys.setDisk(s097Disk("/var/tmp/portage", 2, s097DiskCrit))
	s.start([]string{"/", "/var/tmp/portage"}, nil)
	s.tickUntil("critical disk", 8, func() bool { return len(s.out.matching(s097IsDiskOf("/var/tmp/portage"))) == 1 })

	// The tmpfs is unmounted: the path now lives on /'s filesystem.
	s.sys.setDisk(s097Disk("/var/tmp/portage", 1, s097DiskOK))
	s.ticks(2)
	v := s.viewNow()
	if len(v.Alerts) != 0 || v.AlertCritical {
		t.Errorf("deduplicated path: alerts = %q, critical = %v; want none", v.Alerts, v.AlertCritical)
	}
	if !slices.Contains(s.out.closedKeys(), "disk:/var/tmp/portage") {
		t.Errorf("closed notification keys = %v, want disk:/var/tmp/portage withdrawn", s.out.closedKeys())
	}
	if n := s.countLines("level=INFO", "monitor=disk", "target=/var/tmp/portage", "state=ok"); n != 1 {
		t.Errorf("INFO lines of the deduplicated path returning to ok = %d, want 1\n%s",
			n, strings.Join(s.logs.lines(), "\n"))
	}

	// It is not withdrawn again on later samples.
	closed := len(s.out.closedKeys())
	s.ticks(3)
	if n := len(s.out.closedKeys()); n != closed {
		t.Errorf("closes after later samples = %d, want %d (no repeat)", n, closed)
	}
}
