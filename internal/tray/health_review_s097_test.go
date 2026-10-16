package tray

// Regression tests for the Task 4 tech review of story 097: readings that
// change while staying on one side of the current level, the time a
// condition has been active, and network events the App skips instead of
// sending. They reuse the drivers of health_observe_s097_test.go and
// netwatch_s097_test.go.

import (
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/desktop/netmon"
)

// alternate feeds one sample every obsSample from from to to, both included,
// taking a for even steps and b for odd ones.
func (d *obsDriver) alternate(from, to time.Duration, a, b level) {
	for i, off := 0, from; off <= to; i, off = i+1, off+obsSample {
		got := a
		if i%2 == 1 {
			got = b
		}
		d.at(off, got, false)
	}
}

// TestS097TaskFourReview_AlternatingWarningCriticalRaisesWarning: readings
// that alternate between warning and critical are all at least warning, so
// the warning is raised after one window and critical never is.
func TestS097TaskFourReview_AlternatingWarningCriticalRaisesWarning(t *testing.T) {
	d := newObsDriver()
	d.alternate(0, time.Hour, levelWarning, levelCritical)
	obsWantOne(t, d.take(), obsEvent{at: obsWindow, from: levelOK, to: levelWarning, notify: true, urgency: 1}, true)
	if d.c.level != levelWarning {
		t.Fatalf("level after an hour of warning/critical readings = %s, want warning", obsLevelText(d.c.level))
	}
}

// TestS097TaskFourReview_CriticalFallsOnWarningOKReadings: an active
// critical whose readings alternate between ok and warning is no longer
// critical after one window; it falls silently to warning, the least
// improved level of the spell, and sends nothing more.
func TestS097TaskFourReview_CriticalFallsOnWarningOKReadings(t *testing.T) {
	d := newObsDriver()
	d.run(0, obsWindow, levelCritical, false)
	d.take()
	start := obsWindow + obsSample
	d.alternate(start, start+2*time.Hour, levelWarning, levelOK)
	obsWantOne(t, d.take(), obsEvent{at: start + obsWindow, from: levelCritical, to: levelWarning}, false)
	if d.c.level != levelWarning {
		t.Fatalf("level after two hours of ok/warning readings = %s, want warning", obsLevelText(d.c.level))
	}
}

// TestS097TaskFourReview_EscalationMidWindowRaisesWarningFirst: a warning
// spell that turns critical half-way raises the warning when its window
// ends, and critical only once critical has held a window of its own,
// counted from the move to warning.
func TestS097TaskFourReview_EscalationMidWindowRaisesWarningFirst(t *testing.T) {
	d := newObsDriver()
	d.at(0, levelWarning, false)
	d.at(30*time.Second, levelWarning, false)
	d.at(60*time.Second, levelCritical, false)
	obsWantOne(t, d.take(), obsEvent{at: 60 * time.Second, from: levelOK, to: levelWarning, notify: true, urgency: 1}, true)
	d.at(90*time.Second, levelCritical, false)
	obsWantQuiet(t, d.take(), "critical held 30 s after the warning was raised")
	d.at(120*time.Second, levelCritical, false)
	obsWantOne(t, d.take(), obsEvent{at: 120 * time.Second, from: levelWarning, to: levelCritical, notify: true, urgency: 2}, true)
}

// TestS097TaskFourReview_ActiveSinceSurvivesSamples: how long a condition
// has lasted counts from the start of the spell that raised it, holds across
// later samples and a warning/critical move, and resets on a clear.
func TestS097TaskFourReview_ActiveSinceSurvivesSamples(t *testing.T) {
	d := newObsDriver()
	if got := d.c.lasted(obsT0); got != 0 {
		t.Fatalf("lasted() of an ok condition = %v, want 0", got)
	}
	d.run(0, 5*time.Minute, levelWarning, false)
	if got := d.c.lasted(obsT0.Add(5 * time.Minute)); got != 5*time.Minute {
		t.Fatalf("lasted() 5 min into a warning = %v, want 5m0s", got)
	}
	d.run(5*time.Minute+obsSample, 7*time.Minute, levelCritical, false)
	if d.c.level != levelCritical {
		t.Fatalf("level = %s, want critical", obsLevelText(d.c.level))
	}
	if got := d.c.lasted(obsT0.Add(7 * time.Minute)); got != 7*time.Minute {
		t.Fatalf("lasted() after the escalation = %v, want 7m0s", got)
	}
	d.run(7*time.Minute+obsSample, 9*time.Minute, levelOK, false)
	if d.c.level != levelOK {
		t.Fatalf("level = %s, want ok", obsLevelText(d.c.level))
	}
	if got := d.c.lasted(obsT0.Add(9 * time.Minute)); got != 0 {
		t.Fatalf("lasted() after the clear = %v, want 0", got)
	}
	d.run(10*time.Minute, 11*time.Minute, levelWarning, false)
	if got := d.c.lasted(obsT0.Add(11 * time.Minute)); got != time.Minute {
		t.Fatalf("lasted() of a new warning = %v, want 1m0s", got)
	}
}

// TestS097TaskFourReview_LateFirstVerdictWaitsDownAfter: a first offline
// verdict that arrives after the startup window (NetworkManager came up
// late) still waits the plain down window instead of firing at once; one
// inside the startup window keeps the startup deadline.
func TestS097TaskFourReview_LateFirstVerdictWaitsDownAfter(t *testing.T) {
	t.Run("late", func(t *testing.T) {
		w := newNetWatch(nwT0, nwDefaults(t))
		w.update(nwT0, netmon.Verdict{NMAbsent: true})
		late := 5 * time.Minute
		nwWantArm(t, w, late, nwOffline, 30*time.Second)
		nwWantDue(t, w, late, netNone)
		nwWantDue(t, w, late+29*time.Second, netNone)
		nwWantDue(t, w, late+30*time.Second, netDown)
	})
	t.Run("inside the startup window", func(t *testing.T) {
		w := newNetWatch(nwT0, nwDefaults(t))
		nwWantArm(t, w, 5*time.Second, nwOffline, 55*time.Second)
		nwWantDue(t, w, 59*time.Second, netNone)
		nwWantDue(t, w, 60*time.Second, netDown)
	})
	t.Run("straddling the startup window", func(t *testing.T) {
		w := newNetWatch(nwT0, nwDefaults(t))
		nwWantArm(t, w, 45*time.Second, nwOffline, 30*time.Second)
		nwWantDue(t, w, 60*time.Second, netNone)
		nwWantDue(t, w, 75*time.Second, netDown)
	})
}

// TestS097TaskFourReview_SkippedBackReleasesTheDown: a back the App skips
// (paused, or its Send failed) hands back the down's id to close and leaves
// the watch ready to announce the next outage with a fresh down.
func TestS097TaskFourReview_SkippedBackReleasesTheDown(t *testing.T) {
	w := nwStartedOnline(t)
	w.update(nwT0.Add(time.Minute), nwOffline)
	nwSendDown(t, w, 90*time.Second, 7)
	w.update(nwT0.Add(2*time.Minute), nwOnline)
	nwWantDue(t, w, 2*time.Minute+10*time.Second, netBack)
	if id := w.skip(netBack); id != 7 {
		t.Fatalf("skip(netBack) = %d, want 7 (the down to close)", id)
	}
	if w.notifiedDown || w.notifID != 0 {
		t.Fatalf("after skip(netBack): notifiedDown=%v notifID=%d, want false/0", w.notifiedDown, w.notifID)
	}
	nwWantDue(t, w, 3*time.Minute, netNone)

	out := 3 * time.Hour
	nwWantArm(t, w, out, nwOffline, 30*time.Second)
	nwSendDown(t, w, out+30*time.Second, 9)
	nwWantArm(t, w, out+time.Minute, nwOnline, 10*time.Second)
	nwWantDue(t, w, out+time.Minute+10*time.Second, netBack)
	if w.notifID != 9 {
		t.Fatalf("the new back replaces id %d, want 9", w.notifID)
	}
}

// TestS097TaskFourReview_SkippedDownStaysHandled: a down the App skips is
// not decided again for the same outage, however late the next verdict or
// timer comes, gets no back, and a later outage is announced normally.
func TestS097TaskFourReview_SkippedDownStaysHandled(t *testing.T) {
	w := nwStartedOnline(t)
	out := time.Hour
	w.update(nwT0.Add(out), nwOffline)
	nwWantDue(t, w, out+30*time.Second, netDown)
	if id := w.skip(netDown); id != 0 {
		t.Fatalf("skip(netDown) = %d, want 0", id)
	}
	nwWantDue(t, w, out+31*time.Second, netNone)
	if d, ok := w.update(nwT0.Add(out+3*time.Hour), netmon.Verdict{Online: false, Metered: true}); ok {
		t.Fatalf("update with the same outage = (%v, true), want no timer", d)
	}
	nwWantDue(t, w, out+3*time.Hour, netNone)

	w.update(nwT0.Add(out+4*time.Hour), nwOnline)
	nwWantDue(t, w, out+4*time.Hour+10*time.Second, netNone)

	again := out + 5*time.Hour
	nwWantArm(t, w, again, nwOffline, 30*time.Second)
	nwWantDue(t, w, again+30*time.Second, netDown)
}

// TestS097TaskFourReview_SkipNoneIsInert: skipping netNone changes nothing.
func TestS097TaskFourReview_SkipNoneIsInert(t *testing.T) {
	n := config.NetworkMonitor{Enabled: true, DownAfter: 30 * time.Second, DownAfterStartup: time.Minute, BackAfter: 10 * time.Second}
	w := newNetWatch(nwT0, n)
	w.update(nwT0, nwOnline)
	w.update(nwT0.Add(time.Hour), nwOffline)
	nwSendDown(t, w, time.Hour+30*time.Second, 4)
	if id := w.skip(netNone); id != 0 || !w.notifiedDown || w.notifID != 4 {
		t.Fatalf("skip(netNone) = %d with notifiedDown=%v notifID=%d, want 0 and true/4 untouched", id, w.notifiedDown, w.notifID)
	}
}
