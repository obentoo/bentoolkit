package tray_test

// App-level characterization tests for the network alerts of story 097,
// sub-task 5.2: NetworkManager's verdict, read on every change and once at
// startup, becomes "network down" after 30 s offline (60 s when the tray
// starts offline) and "network back" after 10 s online, only after a sent
// "network down", and never while notifications are paused. The harness is in
// app_health_s097_test.go; these tests run without a SystemReader, so every
// timer they advance is a network or a fetch timer.

import (
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/desktop/netmon"
	"github.com/obentoo/bentoolkit/internal/tray"
	"github.com/obentoo/bentoolkit/internal/tray/state"
)

// newS097Net is the shared harness with NetworkManager present and online
// and no SystemReader.
func newS097Net(t *testing.T) *s097Harness {
	s := newS097(t)
	s.net.set(netmon.Verdict{Online: true})
	return s
}

func (s *s097Harness) startNet() {
	s.t.Helper()
	s.start(nil, func(d *tray.Deps) { d.Sys = nil })
}

// setOnline reports a new verdict through Changed and waits until the loop
// has handled it.
func (s *s097Harness) setOnline(online bool) {
	s.t.Helper()
	s.net.set(netmon.Verdict{Online: online})
	s.net.changed <- struct{}{}
	s086WaitParked(s.appHarness)
}

// advance moves the clock by d in steps of at most 5 s, letting the loop
// handle each timer that fires.
func (s *s097Harness) advance(d time.Duration) {
	s.t.Helper()
	for d > 0 {
		step := min(d, 5*time.Second)
		s.clock.Advance(step)
		d -= step
		s086WaitParked(s.appHarness)
	}
}

func (s *s097Harness) netSent() []s097Sent { return s.out.matching(s097IsNetwork) }

// noNetwork asserts that no network notification was sent.
func (s *s097Harness) noNetwork(when string) {
	s.t.Helper()
	s.never("a network notification "+when, func() bool { return len(s.netSent()) > 0 })
}

// TestS097Network_R5_1_DownAfterThirtySecondsOffline: 30 s offline sends one
// normal-urgency "network down"; 29 s does not.
func TestS097Network_R5_1_DownAfterThirtySecondsOffline(t *testing.T) {
	s := newS097Net(t)
	s.startNet()
	s.setOnline(false)
	s.advance(29 * time.Second)
	s.noNetwork("after 29 s offline")
	s.advance(time.Second)
	s.waitFor("network down after 30 s offline", func() bool { return len(s.netSent()) == 1 })
	m := s.netSent()[0].Msg
	if !s097IsNetDown(m) || m.Urgency != 1 {
		t.Errorf("notification = %+v, want network down at urgency 1", m)
	}
	s.advance(60 * time.Second)
	if n := len(s.netSent()); n != 1 {
		t.Errorf("network notifications after 90 s offline = %d, want 1", n)
	}
}

// TestS097Network_R5_2_ShortOutagesSendNothing: an outage shorter than 30 s
// sends nothing, and two short outages do not add up to one long one.
func TestS097Network_R5_2_ShortOutagesSendNothing(t *testing.T) {
	t.Run("two short outages do not add up", func(t *testing.T) {
		s := newS097Net(t)
		s.startNet()
		s.setOnline(false)
		s.advance(20 * time.Second)
		s.setOnline(true)
		s.advance(5 * time.Second)
		s.setOnline(false)
		s.advance(25 * time.Second) // 45 s offline of the last 50, never 30 in a row
		s.noNetwork("after two outages of 20 s and 25 s")
		s.advance(5 * time.Second)
		s.waitFor("network down once the second outage reached 30 s", func() bool { return len(s.netSent()) == 1 })
	})
	t.Run("offline and back within the window", func(t *testing.T) {
		s := newS097Net(t)
		s.startNet()
		s.setOnline(false)
		s.advance(20 * time.Second)
		s.setOnline(true)
		s.advance(60 * time.Second)
		s.noNetwork("for a 20 s outage")
	})
}

// TestS097Network_R5_3_BackAfterTenSecondsReplacesDown: 10 s online after a
// sent "network down" sends one low-urgency "network back" replacing it; a
// flap restarts the 10 s.
func TestS097Network_R5_3_BackAfterTenSecondsReplacesDown(t *testing.T) {
	s := newS097Net(t)
	s.startNet()
	s.setOnline(false)
	s.advance(30 * time.Second)
	s.waitFor("network down", func() bool { return len(s.netSent()) == 1 })
	down := s.netSent()[0]

	s.setOnline(true)
	s.advance(5 * time.Second)
	s.setOnline(false)
	s.advance(5 * time.Second)
	s.setOnline(true)
	s.advance(9 * time.Second)
	if got := s.out.matching(s097IsNetBack); len(got) != 0 {
		t.Fatalf("network back sent before 10 s online in a row: %+v", got)
	}
	s.advance(time.Second)
	s.waitFor("network back", func() bool { return len(s.out.matching(s097IsNetBack)) == 1 })
	back := s.out.matching(s097IsNetBack)[0].Msg
	if back.Urgency != 0 || back.ReplacesID != down.ID {
		t.Errorf("network back = %+v, want urgency 0 replacing network down %d", back, down.ID)
	}
	s.advance(30 * time.Second)
	if n := len(s.out.matching(s097IsNetBack)); n != 1 {
		t.Errorf("network back sent %d times, want 1", n)
	}
	if n := len(s.out.matching(s097IsNetDown)); n != 1 {
		t.Errorf("network down sent %d times across one outage, want 1", n)
	}

	// A second outage after "back" is a new "down" that replaces nothing.
	s.setOnline(false)
	s.advance(30 * time.Second)
	s.waitFor("a second network down", func() bool { return len(s.out.matching(s097IsNetDown)) == 2 })
	if second := s.out.matching(s097IsNetDown)[1].Msg; second.ReplacesID != 0 || second.Urgency != 1 {
		t.Errorf("second network down = %+v, want urgency 1 replacing nothing (not %d)", second, down.ID)
	}
}

// TestS097Network_R5_4_NoBackWithoutDown: coming back from an outage that sent
// no "network down" sends no "network back".
func TestS097Network_R5_4_NoBackWithoutDown(t *testing.T) {
	s := newS097Net(t)
	s.startNet()
	s.setOnline(false)
	s.advance(10 * time.Second)
	s.setOnline(true)
	s.advance(60 * time.Second)
	s.noNetwork("for a 10 s outage and 60 s online")

	// The watch is alive: a full outage is announced, and its end is "back".
	s.setOnline(false)
	s.advance(30 * time.Second)
	s.waitFor("network down after a full outage", func() bool { return len(s.out.matching(s097IsNetDown)) == 1 })
	s.setOnline(true)
	s.advance(10 * time.Second)
	s.waitFor("network back after the full outage", func() bool { return len(s.out.matching(s097IsNetBack)) == 1 })
}

// TestS097Network_R5_5_StartingOfflineWaitsSixtySeconds: a tray started while
// offline waits 60 s, not 30, before the first "network down".
func TestS097Network_R5_5_StartingOfflineWaitsSixtySeconds(t *testing.T) {
	s := newS097Net(t)
	s.net.set(netmon.Verdict{Online: false})
	s.startNet()
	s.advance(59 * time.Second)
	s.noNetwork("59 s after starting offline")
	s.advance(time.Second)
	s.waitFor("network down 60 s after starting offline", func() bool { return len(s.out.matching(s097IsNetDown)) == 1 })
}

// TestS097Network_R5_6_NoNetworkManagerLogsOnceAndSendsNothing: without
// NetworkManager on the system bus, one startup INFO says network monitoring
// is unavailable and no network notification is ever sent.
func TestS097Network_R5_6_NoNetworkManagerLogsOnceAndSendsNothing(t *testing.T) {
	unavailable := regexp.MustCompile(`(?i)network.*(unavailable|not available)`)
	count := func(s *s097Harness) int {
		n := 0
		for _, l := range s.logs.lines() {
			if strings.Contains(l, "level=INFO") && unavailable.MatchString(l) {
				n++
			}
		}
		return n
	}
	for name, edit := range map[string]func(*tray.Deps){
		"NetworkManager absent": func(d *tray.Deps) { d.Sys = nil },
		"no network monitor":    func(d *tray.Deps) { d.Sys = nil; d.Net = nil },
	} {
		t.Run(name, func(t *testing.T) {
			s := newS097(t)
			s.net.set(netmon.Verdict{NMAbsent: true})
			s.start(nil, edit)
			if n := count(s); n != 1 {
				t.Fatalf("startup INFO lines saying network monitoring is unavailable = %d, want 1\n%s",
					n, strings.Join(s.logs.lines(), "\n"))
			}
			s.net.changed <- struct{}{}
			s.advance(4 * time.Minute) // past the scheduled fetch
			s.net.changed <- struct{}{}
			s.advance(time.Minute)
			if got := s.out.system(); len(got) != 0 {
				t.Errorf("system notifications without NetworkManager: %+v", got)
			}
			if n := count(s); n != 1 {
				t.Errorf("network-unavailable INFO lines after 5 min = %d, want 1", n)
			}
		})
	}
}

// TestS097Network_R7_1_PausedSendsNoNetworkNotification: while paused, a long
// outage sends no "network down", and its end no "network back".
func TestS097Network_R7_1_PausedSendsNoNetworkNotification(t *testing.T) {
	s := newS097Net(t)
	s.startNet()
	s.menu(202) // pause for 1 hour
	s.savedWhere("the pause", func(st state.State) bool { return !st.PauseUntil.IsZero() })
	s086WaitParked(s.appHarness)
	s.setOnline(false)
	s.advance(45 * time.Second)
	s.setOnline(true)
	s.advance(20 * time.Second)
	s.noNetwork("while paused")

	// After the pause the same outage is announced.
	s.menu(204) // resume
	s.savedWhere("the resume", func(st state.State) bool { return st.PauseUntil.IsZero() })
	s086WaitParked(s.appHarness)
	s.setOnline(false)
	s.advance(30 * time.Second)
	s.waitFor("network down after the pause", func() bool { return len(s.out.matching(s097IsNetDown)) == 1 })
}

// TestS097Network_R7_1_DownHeldByThePauseGetsNoBack: an outage whose "network
// down" the pause held, and that outlasts the pause, gets no "network back";
// the next outage is announced again.
func TestS097Network_R7_1_DownHeldByThePauseGetsNoBack(t *testing.T) {
	s := newS097Net(t)
	s.startNet()
	s.menu(202) // pause for 1 hour
	s.savedWhere("the pause", func(st state.State) bool { return !st.PauseUntil.IsZero() })
	s086WaitParked(s.appHarness)
	s.setOnline(false)
	s.advance(40 * time.Second)
	s.menu(204) // resume while still offline
	s.savedWhere("the resume", func(st state.State) bool { return st.PauseUntil.IsZero() })
	s086WaitParked(s.appHarness)
	s.advance(20 * time.Second)
	s.setOnline(true)
	s.advance(20 * time.Second)
	if got := s.out.matching(s097IsNetBack); len(got) != 0 {
		t.Fatalf("network back for an outage whose down the pause held: %+v", got)
	}

	s.setOnline(false)
	s.advance(30 * time.Second)
	s.waitFor("network down for the next outage", func() bool { return len(s.out.matching(s097IsNetDown)) == 1 })
}

// TestS097Network_R5_4_FailedDownSendWarnsAndGetsNoBack: a "network down" the
// server refused is a WARN and was never sent, so its end gets no "network
// back".
func TestS097Network_R5_4_FailedDownSendWarnsAndGetsNoBack(t *testing.T) {
	s := newS097Net(t)
	s.startNet()
	gone := errors.New("s097 notification server gone")
	s.out.setSendErr(gone)
	s.setOnline(false)
	s.advance(30 * time.Second)
	s.waitFor("the attempted network down", func() bool { return s.out.failedSends() >= 1 })
	s086WaitParked(s.appHarness)
	if n := s.countLines("level=WARN", gone.Error()); n != 1 {
		t.Errorf("WARN lines for the failed network down = %d, want 1\n%s", n, strings.Join(s.logs.lines(), "\n"))
	}
	s.out.setSendErr(nil)
	s.setOnline(true)
	s.advance(20 * time.Second)
	s.noNetwork("after a network down that failed to send")

	s.setOnline(false)
	s.advance(30 * time.Second)
	s.waitFor("network down for the next outage", func() bool { return len(s.out.matching(s097IsNetDown)) == 1 })
}

// TestS097Network_R6_13_DownAndBackAreLogged: "network down" and "network
// back" each log one INFO with monitor=network and a level.
func TestS097Network_R6_13_DownAndBackAreLogged(t *testing.T) {
	s := newS097Net(t)
	s.startNet()
	lines := func() int {
		n := 0
		for _, l := range s.logs.lines() {
			if strings.Contains(l, "level=INFO") && strings.Contains(l, "monitor=network") && strings.Contains(l, "state=") {
				n++
			}
		}
		return n
	}
	s.setOnline(false)
	s.advance(30 * time.Second)
	s.waitFor("network down", func() bool { return len(s.out.matching(s097IsNetDown)) == 1 })
	s086WaitParked(s.appHarness)
	if n := lines(); n != 1 {
		t.Fatalf("INFO lines with monitor=network and a level after down = %d, want 1\n%s", n, strings.Join(s.logs.lines(), "\n"))
	}
	s.setOnline(true)
	s.advance(10 * time.Second)
	s.waitFor("network back", func() bool { return len(s.out.matching(s097IsNetBack)) == 1 })
	s086WaitParked(s.appHarness)
	if n := lines(); n != 2 {
		t.Errorf("INFO lines with monitor=network and a level after back = %d, want 2", n)
	}
}

// TestS097Network_R9_2_DisabledNetworkMonitorNeverNotifies: Deps.Monitors
// decides: network off there sends nothing however long the outage, and
// network on there notifies though the configuration turns it off.
func TestS097Network_R9_2_DisabledNetworkMonitorNeverNotifies(t *testing.T) {
	t.Run("Deps.Monitors enables what Config disables", func(t *testing.T) {
		s := newS097Net(t)
		s.cfg = s097Config(t, "monitors:\n  network:\n    enabled: false\n")
		s.start(nil, func(d *tray.Deps) { d.Sys = nil; d.Monitors = s097Monitors(func(*config.Monitors) {}) })
		s.setOnline(false)
		s.advance(30 * time.Second)
		s.waitFor("network down", func() bool { return len(s.out.matching(s097IsNetDown)) == 1 })
	})
	t.Run("network disabled", func(t *testing.T) {
		s := newS097Net(t)
		s.start(nil, func(d *tray.Deps) {
			d.Sys = nil
			d.Monitors = s097Monitors(func(m *config.Monitors) { m.Network.Enabled = false })
		})
		s.setOnline(false)
		s.advance(2 * time.Minute)
		s.setOnline(true)
		s.advance(30 * time.Second)
		s.noNetwork("with the network monitor disabled")
	})
}

// TestS097Network_R9_3_ConfiguredWindowsAreHonoured: configured down, back
// and startup windows replace 30 s, 10 s and 60 s.
func TestS097Network_R9_3_ConfiguredWindowsAreHonoured(t *testing.T) {
	windows := s097Monitors(func(m *config.Monitors) {
		m.Network.DownAfter = 45 * time.Second
		m.Network.BackAfter = 20 * time.Second
		m.Network.DownAfterStartup = 90 * time.Second
	})
	t.Run("down and back", func(t *testing.T) {
		s := newS097Net(t)
		s.start(nil, func(d *tray.Deps) { d.Sys = nil; d.Monitors = windows })
		s.setOnline(false)
		s.advance(44 * time.Second)
		s.noNetwork("44 s into a 45 s down window")
		s.advance(time.Second)
		s.waitFor("network down at 45 s", func() bool { return len(s.out.matching(s097IsNetDown)) == 1 })
		s.setOnline(true)
		s.advance(19 * time.Second)
		if got := s.out.matching(s097IsNetBack); len(got) != 0 {
			t.Fatalf("network back 19 s into a 20 s back window: %+v", got)
		}
		s.advance(time.Second)
		s.waitFor("network back at 20 s", func() bool { return len(s.out.matching(s097IsNetBack)) == 1 })
	})
	t.Run("starting offline", func(t *testing.T) {
		s := newS097Net(t)
		s.net.set(netmon.Verdict{Online: false})
		s.start(nil, func(d *tray.Deps) { d.Sys = nil; d.Monitors = windows })
		s.advance(89 * time.Second)
		s.noNetwork("89 s into a 90 s startup window")
		s.advance(time.Second)
		s.waitFor("network down at 90 s", func() bool { return len(s.out.matching(s097IsNetDown)) == 1 })
	})
}
