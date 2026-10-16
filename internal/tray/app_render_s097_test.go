package tray_test

// App-level characterization tests for story 097, sub-task 5.3: active
// system conditions reach the icon as alert lines beside the notices, never
// as notices; Mark all read leaves them; a click on a system notification
// opens nothing; a pause keeps rendering them. The harness is in
// app_health_s097_test.go and app_netalert_s097_test.go.

import (
	"strings"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/desktop/notify"
	"github.com/obentoo/bentoolkit/internal/desktop/sni"
	"github.com/obentoo/bentoolkit/internal/gentoo/pkgdb"
	"github.com/obentoo/bentoolkit/internal/tray/feed"
	"github.com/obentoo/bentoolkit/internal/tray/messages"
	"github.com/obentoo/bentoolkit/internal/tray/state"
)

// viewNow is the last view the icon was given.
func (s *s097Harness) viewNow() sni.View {
	v, _ := s.icon.last()
	return v
}

// tickUntilView ticks until the icon's last view satisfies cond.
func (s *s097Harness) tickUntilView(what string, limit int, cond func(sni.View) bool) sni.View {
	s.t.Helper()
	s.tickUntil(what, limit, func() bool {
		v, ok := s.icon.last()
		return ok && cond(v)
	})
	return s.viewNow()
}

// TestS097Render_R8_1_R8_3_OneAlertLinePerConditionInOrder: each active
// condition is one alert line, disks in watched-path order before memory, and
// none of them is a notice.
func TestS097Render_R8_1_R8_3_OneAlertLinePerConditionInOrder(t *testing.T) {
	for name, paths := range map[string][]string{
		"root first":   {"/", "/var/tmp"},
		"tmpdir first": {"/var/tmp", "/"},
	} {
		t.Run(name, func(t *testing.T) {
			s := newS097(t)
			for i, p := range paths {
				s.sys.setDisk(s097Disk(p, uint64(i+1), s097DiskWarn))
			}
			s.sys.setMem(s097MemCrit(), nil)
			s.start(paths, nil)
			v := s.tickUntilView("three alert lines", 8, func(v sni.View) bool { return len(v.Alerts) == 3 })
			tmpAt := 0
			if paths[0] == "/" {
				tmpAt = 1
			}
			for i, line := range v.Alerts {
				if has := strings.Contains(line, "/var/tmp"); has != (i == tmpAt) {
					t.Errorf("alert lines %q: /var/tmp must be line %d only (disks in watched order, then memory)", v.Alerts, tmpAt)
					break
				}
			}
			if want := messages.Count(messages.SysAlertsOne, messages.SysAlertsMany, 3); v.AlertTitle != want {
				t.Errorf("AlertTitle = %q, want %q", v.AlertTitle, want)
			}
			if v.Unread != 0 || len(v.Entries) != 0 || v.More != 0 {
				t.Errorf("view = %+v: system conditions must not count or list as notices", v)
			}
		})
	}
}

// TestS097Render_R8_2_R8_7_CriticalFollowsTheWorstCondition: the critical
// variant shows while any condition is critical, gives way to the warning
// level when the critical one falls back (silently), and every alert goes when
// the last condition clears.
func TestS097Render_R8_2_R8_7_CriticalFollowsTheWorstCondition(t *testing.T) {
	s := newS097(t)
	s.sys.setDisk(s097Disk("/", 1, s097DiskWarn))
	s.sys.setMem(s097MemCrit(), nil)
	s.start([]string{"/"}, nil)
	s.tickUntilView("a critical view with two alerts", 8, func(v sni.View) bool {
		return len(v.Alerts) == 2 && v.AlertCritical
	})
	memSent := len(s.out.matching(s097IsMemory))

	s.sys.setMem(s097MemWarn(), nil)
	fallen := s.tickUntilView("the warning level shown after memory falls back", 8, func(v sni.View) bool {
		return len(v.Alerts) == 2 && !v.AlertCritical
	})
	if n := len(s.out.matching(s097IsMemory)); n != memSent {
		t.Errorf("falling back from critical to warning sent %d memory notifications, want none", n-memSent)
	}
	if want := messages.Count(messages.SysAlertsOne, messages.SysAlertsMany, 2); fallen.AlertTitle != want {
		t.Errorf("AlertTitle = %q, want %q", fallen.AlertTitle, want)
	}

	// The fallen memory line reads as a memory warning raised directly with
	// the same reading: warning wording, not the critical line kept.
	direct := newS097(t)
	direct.sys.setDisk(s097Disk("/", 1, s097DiskWarn))
	direct.sys.setMem(s097MemWarn(), nil)
	direct.start([]string{"/"}, nil)
	raised := direct.tickUntilView("a memory warning raised directly", 8, func(v sni.View) bool { return len(v.Alerts) == 2 })
	if fallen.Alerts[1] != raised.Alerts[1] {
		t.Errorf("memory line after falling to warning = %q, want the warning line %q", fallen.Alerts[1], raised.Alerts[1])
	}

	s.sys.setMem(s097MemOK(), nil)
	s.sys.setDisk(s097Disk("/", 1, s097DiskOK))
	v := s.tickUntilView("no alert once every condition cleared", 8, func(v sni.View) bool { return len(v.Alerts) == 0 })
	if v.AlertCritical || v.Unread != 0 || v.AlertTitle != "" {
		t.Errorf("view after the last clear = %+v, want the plain view", v)
	}
}

// TestS097Render_R8_4_R8_5_AlertsStayOutOfTheNotices: an alert beside an
// unread notice adds nothing to the count, the list or the saved state, and
// Mark all read clears the notice but not the alert.
func TestS097Render_R8_4_R8_5_AlertsStayOutOfTheNotices(t *testing.T) {
	s := newS097(t)
	s.pkgs.list = []pkgdb.Package{bentooFoo}
	s.feed.next = func(int) (feed.Result, error) { return okResult(1790812800, fooCVE()), nil }
	s.sys.setMem(s097MemCrit(), nil)
	s.start(nil, nil)
	s.checkNow()
	s.waitFor("the notice notification", func() bool {
		return len(s.out.matching(func(m notify.Message) bool { return m.NoticeID == fooCVE().ID })) == 1
	})
	v := s.tickUntilView("the memory alert beside the notice", 8, func(v sni.View) bool { return len(v.Alerts) == 1 })
	if v.Unread != 1 || len(v.Entries) != 1 {
		t.Errorf("view = %+v, want exactly the one unread notice listed and counted", v)
	}
	if want := messages.Count(messages.SysAlertsOne, messages.SysAlertsMany, 1); v.AlertTitle != want {
		t.Errorf("AlertTitle = %q, want %q", v.AlertTitle, want)
	}

	s.menu(201) // Mark all read
	v = s.viewWhere("the notice read", func(v sni.View) bool { return v.Unread == 0 })
	if len(v.Alerts) != 1 || !v.AlertCritical {
		t.Errorf("Mark all read changed the alerts: %+v", v)
	}
	st := s.savedWhere("the read notice saved", func(st state.State) bool { return st.Notices[fooCVE().ID].Read })
	if len(st.Notices) != 1 {
		t.Errorf("saved notices = %v, want only %s", st.Notices, fooCVE().ID)
	}
	if st.Format != 1 {
		t.Errorf("saved state format = %d, want it unchanged at 1", st.Format)
	}
}

// TestS097Render_R8_6_SystemClickOpensNothing: a click on a system
// notification opens nothing and logs no WARN, though it carries no notice ID
// like the burst summary, whose click still opens the notices index.
func TestS097Render_R8_6_SystemClickOpensNothing(t *testing.T) {
	s := newS097(t)
	s.start(nil, nil)
	warns := func() int { return s.countLines("level=WARN") }
	before := warns()
	s.out.events <- notify.Event{Action: "default", System: true}
	s.out.events <- notify.Event{Action: "mark-read", System: true}
	s.never("an opening for a system notification click", func() bool { return len(s.open.opened()) > 0 })
	s086WaitParked(s.appHarness)
	if n := warns() - before; n != 0 {
		t.Errorf("system notification clicks logged %d WARN lines, want none\n%s", n, strings.Join(s.logs.lines(), "\n"))
	}

	s.out.events <- notify.Event{Action: "default"} // the burst summary
	s.waitFor("the notices index opened for the burst summary", func() bool { return len(s.open.opened()) == 1 })
	if got := s.open.opened()[0].URL; got != "https://obentoo.org/notices/" {
		t.Errorf("burst summary click opened %q, want the notices index", got)
	}
}

// TestS097Render_R7_3_PauseStillShowsAlerts: while paused a warning is shown
// but not sent, a critical one is sent, and resuming does not send the warning
// raised during the pause.
func TestS097Render_R7_3_PauseStillShowsAlerts(t *testing.T) {
	s := newS097(t)
	s.sys.setDisk(s097Disk("/var/tmp", 1, s097DiskOK))
	s.start([]string{"/var/tmp"}, nil)
	s.menu(202) // pause for 1 hour
	s.savedWhere("the pause", func(st state.State) bool { return !st.PauseUntil.IsZero() })
	s086WaitParked(s.appHarness)

	s.sys.setDisk(s097Disk("/var/tmp", 1, s097DiskWarn))
	s.tickUntilView("the disk warning shown while paused", 6, func(v sni.View) bool { return len(v.Alerts) == 1 })
	if v := s.viewNow(); !v.Paused || !strings.Contains(v.Alerts[0], "/var/tmp") {
		t.Errorf("view = %+v, want the pause shown and a tooltip line naming /var/tmp", v)
	}
	if got := s.out.matching(s097IsDiskOf("/var/tmp")); len(got) != 0 {
		t.Errorf("a disk warning was sent while paused: %+v", got)
	}

	s.sys.setMem(s097MemCrit(), nil)
	s.tickUntil("a critical memory notification while paused", 8, func() bool { return len(s.out.matching(s097IsMemory)) == 1 })
	if m := s.out.matching(s097IsMemory)[0].Msg; m.Urgency != 2 {
		t.Errorf("memory notification while paused = %+v, want urgency 2", m)
	}
	s.tickUntilView("both alerts, critical", 2, func(v sni.View) bool { return len(v.Alerts) == 2 && v.AlertCritical })

	s.menu(204) // resume
	s.savedWhere("the resume", func(st state.State) bool { return st.PauseUntil.IsZero() })
	s086WaitParked(s.appHarness)
	s.ticks(2)
	if got := s.out.matching(s097IsDiskOf("/var/tmp")); len(got) != 0 {
		t.Errorf("resuming sent the warning raised during the pause: %+v", got)
	}
}

// TestS097Render_R5_7_OfflineAfterDownShowsAttention: after "network down"
// the icon carries a network alert until the network is back.
func TestS097Render_R5_7_OfflineAfterDownShowsAttention(t *testing.T) {
	s := newS097Net(t)
	s.startNet()
	s.setOnline(false)
	s.advance(30 * time.Second)
	s.waitFor("network down", func() bool { return len(s.out.matching(s097IsNetDown)) == 1 })
	s.viewWhere("the network alert while offline", func(v sni.View) bool { return len(v.Alerts) == 1 })
	s.advance(2 * time.Minute)
	if v := s.viewNow(); len(v.Alerts) != 1 {
		t.Errorf("view after 2 more minutes offline = %+v, want the network alert kept", v)
	}
	s.setOnline(true)
	s.advance(10 * time.Second)
	s.waitFor("network back", func() bool { return len(s.out.matching(s097IsNetBack)) == 1 })
	s.viewWhere("no alert once the network is back", func(v sni.View) bool { return len(v.Alerts) == 0 })
}
