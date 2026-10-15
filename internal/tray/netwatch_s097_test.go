package tray

// Pinned contract (story 097, design.md "internal/tray/netwatch.go"; the
// constructor is pinned here because the design names no way to hand the
// configured windows to the watch):
//
//	func newNetWatch(start time.Time, t config.NetworkMonitor) *netWatch
//	func (w *netWatch) update(now time.Time, v netmon.Verdict) (deadline time.Duration, ok bool)
//	func (w *netWatch) due(now time.Time) netEvent // netNone | netDown | netBack
//	func (w *netWatch) markSent(ev netEvent, id uint32) // after a successful, unpaused Send
//	fields notifiedDown bool, notifID uint32 // set only by markSent(netDown, id)
//
// update returns the wait the App arms a.netAt with; due is what the App asks
// when any timer fires, so a stale timer must answer netNone. The tests call
// due at chosen instants regardless of what update returned. due only decides:
// a down counts as sent once markSent(netDown, id) says so, and the tests
// call markSent the way the App does after each successful Send.

import (
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/desktop/netmon"
)

var (
	nwT0      = time.Date(2026, 10, 14, 8, 0, 0, 0, time.UTC)
	nwOnline  = netmon.Verdict{Online: true}
	nwOffline = netmon.Verdict{Online: false}
)

func nwEventName(e netEvent) string {
	switch e {
	case netNone:
		return "none"
	case netDown:
		return "down"
	case netBack:
		return "back"
	}
	return "netEvent(?)"
}

func nwDefaults(t *testing.T) config.NetworkMonitor {
	t.Helper()
	m, warns := config.TrayConfig{}.GetMonitors()
	if len(warns) != 0 {
		t.Fatalf("GetMonitors() on an empty tray config warned: %v", warns)
	}
	n := m.Network
	if n.DownAfter != 30*time.Second || n.DownAfterStartup != 60*time.Second || n.BackAfter != 10*time.Second {
		t.Fatalf("network defaults = %v/%v/%v, want 30s/60s/10s", n.DownAfter, n.DownAfterStartup, n.BackAfter)
	}
	return n
}

func nwWantDue(t *testing.T, w *netWatch, off time.Duration, want netEvent) {
	t.Helper()
	if got := w.due(nwT0.Add(off)); got != want {
		t.Fatalf("due(t0+%v) = %s, want %s", off, nwEventName(got), nwEventName(want))
	}
}

func nwWantArm(t *testing.T, w *netWatch, off time.Duration, v netmon.Verdict, want time.Duration) {
	t.Helper()
	got, ok := w.update(nwT0.Add(off), v)
	if !ok || got != want {
		t.Fatalf("update(t0+%v, %+v) = (%v, %v), want (%v, true)", off, v, got, ok, want)
	}
}

// nwSendDown expects a down at off and marks it sent with id.
func nwSendDown(t *testing.T, w *netWatch, off time.Duration, id uint32) {
	t.Helper()
	nwWantDue(t, w, off, netDown)
	w.markSent(netDown, id)
}

// nwSendBack expects a back at off and marks it sent with id.
func nwSendBack(t *testing.T, w *netWatch, off time.Duration, id uint32) {
	t.Helper()
	nwWantDue(t, w, off, netBack)
	w.markSent(netBack, id)
}

// startedOnline returns a watch that saw the network online at start.
func nwStartedOnline(t *testing.T) *netWatch {
	t.Helper()
	w := newNetWatch(nwT0, nwDefaults(t))
	w.update(nwT0, nwOnline)
	return w
}

// TestNetWatchDownR5_1R5_2: an outage is announced once after 30 s; a 29 s
// outage is not announced at all. Hostile half first.
func TestNetWatchDownR5_1R5_2(t *testing.T) {
	const out = time.Hour
	t.Run("hostile: offline 29 s then online sends nothing", func(t *testing.T) {
		w := nwStartedOnline(t)
		w.update(nwT0.Add(out), nwOffline)
		w.update(nwT0.Add(out+29*time.Second), nwOnline)
		nwWantDue(t, w, out+30*time.Second, netNone)
		nwWantDue(t, w, out+40*time.Second, netNone)
	})
	t.Run("hostile: a repeated offline verdict does not push the deadline", func(t *testing.T) {
		w := nwStartedOnline(t)
		w.update(nwT0.Add(out), nwOffline)
		w.update(nwT0.Add(out+20*time.Second), netmon.Verdict{Online: false, Metered: true})
		nwWantDue(t, w, out+30*time.Second, netDown)
	})
	t.Run("offline 30 s sends one down", func(t *testing.T) {
		w := nwStartedOnline(t)
		nwWantArm(t, w, out, nwOffline, 30*time.Second)
		nwWantDue(t, w, out+29*time.Second, netNone)
		nwSendDown(t, w, out+30*time.Second, 1)
		nwWantDue(t, w, out+31*time.Second, netNone)
		nwWantDue(t, w, out+10*time.Minute, netNone)
	})
}

// TestNetWatchStartupWindowR5_5: starting offline waits 60 s, not 30 s; the
// longer window does not carry over to a later outage.
func TestNetWatchStartupWindowR5_5(t *testing.T) {
	t.Run("hostile: the 30 s window does not apply at startup", func(t *testing.T) {
		w := newNetWatch(nwT0, nwDefaults(t))
		nwWantArm(t, w, 0, nwOffline, 60*time.Second)
		nwWantDue(t, w, 30*time.Second, netNone)
		nwWantDue(t, w, 59*time.Second, netNone)
		nwWantDue(t, w, 60*time.Second, netDown)
	})
	t.Run("hostile: back online at 40 s after an offline start sends nothing", func(t *testing.T) {
		w := newNetWatch(nwT0, nwDefaults(t))
		w.update(nwT0, nwOffline)
		w.update(nwT0.Add(40*time.Second), nwOnline)
		nwWantDue(t, w, 50*time.Second, netNone)
		nwWantDue(t, w, 60*time.Second, netNone)
	})
	t.Run("a later outage uses the 30 s window", func(t *testing.T) {
		w := newNetWatch(nwT0, nwDefaults(t))
		w.update(nwT0, nwOffline)
		nwSendDown(t, w, 60*time.Second, 1)
		w.update(nwT0.Add(2*time.Minute), nwOnline)
		nwSendBack(t, w, 2*time.Minute+10*time.Second, 2)
		nwWantArm(t, w, time.Hour, nwOffline, 30*time.Second)
		nwWantDue(t, w, time.Hour+30*time.Second, netDown)
	})
}

// TestNetWatchBackR5_3R5_4: "back" follows only a down marked sent, after
// 10 s online, once, and exposes the down's id for the replace. Hostile half
// first: no back without a down, no back after a down that was decided but
// never sent (paused or failed Send), no back for an online spell under 10 s.
func TestNetWatchBackR5_3R5_4(t *testing.T) {
	const out = time.Hour
	down := out + 30*time.Second
	t.Run("hostile: no back without a sent down", func(t *testing.T) {
		w := nwStartedOnline(t)
		w.update(nwT0.Add(out), nwOffline)
		w.update(nwT0.Add(out+10*time.Second), nwOnline)
		nwWantDue(t, w, out+20*time.Second, netNone)
		nwWantDue(t, w, out+30*time.Second, netNone)
		nwWantDue(t, w, out+time.Minute, netNone)
	})
	t.Run("hostile: a down decided but never sent gives no back", func(t *testing.T) {
		w := nwStartedOnline(t)
		w.update(nwT0.Add(out), nwOffline)
		nwWantDue(t, w, down, netDown)
		if w.notifiedDown || w.notifID != 0 {
			t.Fatalf("after due returned down with no markSent: notifiedDown=%v notifID=%d, want false/0", w.notifiedDown, w.notifID)
		}
		back := down + time.Minute
		w.update(nwT0.Add(back), nwOnline)
		nwWantDue(t, w, back+10*time.Second, netNone)
		nwWantDue(t, w, back+time.Hour, netNone)
	})
	t.Run("hostile: no back without any outage", func(t *testing.T) {
		w := nwStartedOnline(t)
		w.update(nwT0.Add(out), nwOnline)
		nwWantDue(t, w, out+10*time.Second, netNone)
	})
	t.Run("hostile: online 9 s then offline again sends no back", func(t *testing.T) {
		w := nwStartedOnline(t)
		w.update(nwT0.Add(out), nwOffline)
		nwSendDown(t, w, down, 1)
		w.update(nwT0.Add(down+time.Minute), nwOnline)
		w.update(nwT0.Add(down+time.Minute+9*time.Second), nwOffline)
		nwWantDue(t, w, down+time.Minute+10*time.Second, netNone)
	})
	t.Run("online 10 s after a down sends one back", func(t *testing.T) {
		w := nwStartedOnline(t)
		w.update(nwT0.Add(out), nwOffline)
		nwSendDown(t, w, down, 7)
		if !w.notifiedDown || w.notifID != 7 {
			t.Fatalf("after markSent(netDown, 7): notifiedDown=%v notifID=%d, want true/7", w.notifiedDown, w.notifID)
		}
		back := down + time.Minute
		nwWantArm(t, w, back, nwOnline, 10*time.Second)
		nwWantDue(t, w, back+9*time.Second, netNone)
		nwWantDue(t, w, back+10*time.Second, netBack)
		if w.notifID != 7 {
			t.Fatalf("when back is due notifID = %d, want 7 (the down it replaces)", w.notifID)
		}
		w.markSent(netBack, 8)
		nwWantDue(t, w, back+11*time.Second, netNone)
		nwWantDue(t, w, back+time.Hour, netNone)
	})
}

// TestNetWatchNMAbsentR5_6: a NetworkManager-absent verdict disables the watch.
// Its Online field is false, and that must not read as an outage.
func TestNetWatchNMAbsentR5_6(t *testing.T) {
	w := newNetWatch(nwT0, nwDefaults(t))
	if d, ok := w.update(nwT0, netmon.Verdict{NMAbsent: true}); ok {
		t.Fatalf("update(NMAbsent) = (%v, true), want no timer armed", d)
	}
	nwWantDue(t, w, 30*time.Second, netNone)
	nwWantDue(t, w, 60*time.Second, netNone)
	nwWantDue(t, w, 10*time.Minute, netNone)
}

// TestNetWatchNoVerdictYet: a timer firing before any verdict sends nothing.
func TestNetWatchNoVerdictYet(t *testing.T) {
	w := newNetWatch(nwT0, nwDefaults(t))
	nwWantDue(t, w, 2*time.Minute, netNone)
}

// TestNetWatchUsesConfiguredWindows: the windows come from config.
func TestNetWatchUsesConfiguredWindows(t *testing.T) {
	n := nwDefaults(t)
	n.DownAfter = 45 * time.Second
	n.BackAfter = 20 * time.Second
	w := newNetWatch(nwT0, n)
	w.update(nwT0, nwOnline)
	nwWantArm(t, w, time.Hour, nwOffline, 45*time.Second)
	nwWantDue(t, w, time.Hour+30*time.Second, netNone)
	nwSendDown(t, w, time.Hour+45*time.Second, 1)
	back := time.Hour + 2*time.Minute
	nwWantArm(t, w, back, nwOnline, 20*time.Second)
	nwWantDue(t, w, back+10*time.Second, netNone)
	nwWantDue(t, w, back+20*time.Second, netBack)
}
