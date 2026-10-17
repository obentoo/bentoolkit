package tray_test

// Executor test for the state key of the shared notification lines on the
// network, whose state no condition holds: it comes from the spell the
// network is in.

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestS099Notify_X_NetworkFailedSendStateFollowsTheSpell(t *testing.T) {
	s := newS097Net(t)
	s.startNet()
	gone := errors.New("s099 notification server gone")

	s.out.setSendErr(gone)
	s.setOnline(false)
	s.advance(30 * time.Second)
	s.waitFor("the attempted network down", func() bool { return s.out.failedSends() >= 1 })
	s086WaitParked(s.appHarness)
	if n := s.countLines("level=WARN", `msg="sending a system notification failed"`, " monitor=network ", " state=warning "); n != 1 {
		t.Errorf("failed down WARN with state=warning = %d lines, want 1\n%s", n, strings.Join(s.logs.lines(), "\n"))
	}

	// A down that was sent, then a back the server refuses.
	s.out.setSendErr(nil)
	for i := 0; i < 30 && len(s.out.matching(s097IsNetDown)) == 0; i++ {
		s.advance(5 * time.Second)
	}
	if n := len(s.out.matching(s097IsNetDown)); n != 1 {
		t.Fatalf("network down notifications after the retry = %d, want 1", n)
	}
	s.out.setSendErr(gone)
	s.setOnline(true)
	s.advance(20 * time.Second)
	s.waitFor("the attempted network back", func() bool { return s.out.failedSends() >= 2 })
	s086WaitParked(s.appHarness)
	if n := s.countLines("level=WARN", `msg="sending a system notification failed"`, " monitor=network ", " state=ok "); n != 1 {
		t.Errorf("failed back WARN with state=ok = %d lines, want 1\n%s", n, strings.Join(s.logs.lines(), "\n"))
	}
}
