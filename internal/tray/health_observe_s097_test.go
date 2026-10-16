package tray

// Pinned contract (story 097, design.md "internal/tray/health.go"):
//
//	type condition struct { level, candidate level; since, lastSample, notifiedAt time.Time; notifID uint32; text string }
//	type transition struct { Key condKey; From, To level; Notify, Withdraw bool; Urgency byte }
//	func (c *condition) observe(now time.Time, got level, window, reminder, maxGap time.Duration, paused bool) (transition, bool)
//
// A step counts as an event when observe returns ok with a level change, a
// Notify or a Withdraw; a step with ok == false, or ok with none of the three,
// is quiet. After every Notify the driver stamps notifiedAt the way the App
// does after a successful Send, so the reminder tests hold whether observe
// stamps it itself or leaves that to the caller. Transition.Key is filled by
// the caller and is not asserted. Urgency follows the notification spec:
// 1 normal, 2 critical.

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

const (
	obsSample   = 30 * time.Second
	obsMaxGap   = 2 * obsSample
	obsWindow   = time.Minute
	obsReminder = time.Hour
)

var obsT0 = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

type obsEvent struct {
	at               time.Duration
	from, to         level
	notify, withdraw bool
	urgency          byte
}

func (e obsEvent) String() string {
	s := fmt.Sprintf("@%v %s->%s", e.at, obsLevelText(e.from), obsLevelText(e.to))
	if e.notify {
		s += fmt.Sprintf(" notify(u=%d)", e.urgency)
	}
	if e.withdraw {
		s += " withdraw"
	}
	return s
}

func obsLevelText(l level) string {
	switch l {
	case levelOK:
		return "ok"
	case levelWarning:
		return "warning"
	case levelCritical:
		return "critical"
	}
	return fmt.Sprintf("level(%d)", int(l))
}

type obsDriver struct {
	c      *condition
	events []obsEvent
}

func newObsDriver() *obsDriver {
	return &obsDriver{c: &condition{level: levelOK, candidate: levelOK}}
}

// at feeds one sample taken off after obsT0 and records it when it is an event.
func (d *obsDriver) at(off time.Duration, got level, paused bool) {
	now := obsT0.Add(off)
	tr, ok := d.c.observe(now, got, obsWindow, obsReminder, obsMaxGap, paused)
	if !ok || (tr.From == tr.To && !tr.Notify && !tr.Withdraw) {
		return
	}
	if tr.Notify {
		d.c.notifiedAt = now
	}
	d.events = append(d.events, obsEvent{at: off, from: tr.From, to: tr.To, notify: tr.Notify, withdraw: tr.Withdraw, urgency: tr.Urgency})
}

// run feeds one sample every obsSample from from to to, both included.
func (d *obsDriver) run(from, to time.Duration, got level, paused bool) {
	for off := from; off <= to; off += obsSample {
		d.at(off, got, paused)
	}
}

// take returns the events recorded since the last take.
func (d *obsDriver) take() []obsEvent {
	ev := d.events
	d.events = nil
	return ev
}

func obsDump(ev []obsEvent) string {
	parts := make([]string, 0, len(ev))
	for _, e := range ev {
		parts = append(parts, e.String())
	}
	return "[" + strings.Join(parts, "; ") + "]"
}

func obsWantOne(t *testing.T, ev []obsEvent, want obsEvent, checkUrgency bool) {
	t.Helper()
	if len(ev) != 1 {
		t.Fatalf("events = %s, want exactly one %s", obsDump(ev), want)
	}
	got := ev[0]
	if got.at != want.at || got.from != want.from || got.to != want.to || got.notify != want.notify || got.withdraw != want.withdraw {
		t.Fatalf("event = %s, want %s", got, want)
	}
	if checkUrgency && got.urgency != want.urgency {
		t.Fatalf("event %s urgency = %d, want %d", got, got.urgency, want.urgency)
	}
}

func obsWantQuiet(t *testing.T, ev []obsEvent, what string) {
	t.Helper()
	if len(ev) != 0 {
		t.Fatalf("%s: events = %s, want none", what, obsDump(ev))
	}
}

// TestObserveSustainWindowR6_2: a warning reading must be seen continuously
// for the whole window. Hostile half first: just under the window, and an
// interrupted run whose pieces add up to more than the window, stay quiet.
func TestObserveSustainWindowR6_2(t *testing.T) {
	t.Run("hostile: just under the window is quiet", func(t *testing.T) {
		d := newObsDriver()
		d.at(0, levelWarning, false)
		d.at(obsWindow-time.Second, levelWarning, false)
		obsWantQuiet(t, d.take(), "59 s of warning")
		if d.c.level != levelOK {
			t.Fatalf("level = %s after 59 s, want ok", obsLevelText(d.c.level))
		}
	})
	t.Run("hostile: an ok reading in between restarts the window", func(t *testing.T) {
		d := newObsDriver()
		d.at(0, levelWarning, false)
		d.at(30*time.Second, levelOK, false)
		d.at(60*time.Second, levelWarning, false)
		d.at(90*time.Second, levelWarning, false)
		obsWantQuiet(t, d.take(), "warning, ok, warning, warning")
		d.at(120*time.Second, levelWarning, false)
		obsWantOne(t, d.take(), obsEvent{at: 120 * time.Second, from: levelOK, to: levelWarning, notify: true, urgency: 1}, true)
	})
	t.Run("at the window the warning is raised with one normal notification", func(t *testing.T) {
		d := newObsDriver()
		d.run(0, obsWindow, levelWarning, false)
		obsWantOne(t, d.take(), obsEvent{at: obsWindow, from: levelOK, to: levelWarning, notify: true, urgency: 1}, true)
		if d.c.level != levelWarning {
			t.Fatalf("level = %s, want warning", obsLevelText(d.c.level))
		}
	})
}

// TestObserveSampleGapRestartsWindow: a gap longer than maxGap (a suspend)
// restarts the candidate instead of counting the gap as sustained; a gap of
// exactly maxGap is still continuous. Hostile half first.
func TestObserveSampleGapRestartsWindow(t *testing.T) {
	t.Run("hostile: a 3 h suspend does not count as sustained", func(t *testing.T) {
		d := newObsDriver()
		d.at(0, levelWarning, false)
		d.at(30*time.Second, levelWarning, false)
		resume := 3 * time.Hour
		d.at(resume, levelWarning, false)
		d.at(resume+30*time.Second, levelWarning, false)
		obsWantQuiet(t, d.take(), "first samples after a suspend")
		d.at(resume+obsWindow, levelWarning, false)
		obsWantOne(t, d.take(), obsEvent{at: resume + obsWindow, from: levelOK, to: levelWarning, notify: true, urgency: 1}, true)
	})
	t.Run("hostile: a gap just over maxGap restarts", func(t *testing.T) {
		d := newObsDriver()
		d.at(0, levelWarning, false)
		d.at(obsMaxGap+time.Second, levelWarning, false)
		obsWantQuiet(t, d.take(), "a 61 s gap")
	})
	t.Run("a gap of exactly maxGap is continuous", func(t *testing.T) {
		d := newObsDriver()
		d.at(0, levelWarning, false)
		d.at(obsMaxGap, levelWarning, false)
		obsWantOne(t, d.take(), obsEvent{at: obsMaxGap, from: levelOK, to: levelWarning, notify: true, urgency: 1}, true)
	})
	t.Run("a gap also restarts a pending clear", func(t *testing.T) {
		d := newObsDriver()
		d.run(0, obsWindow, levelWarning, false)
		d.take()
		d.at(obsWindow+30*time.Second, levelOK, false)
		resume := obsWindow + 2*time.Hour
		d.at(resume, levelOK, false)
		obsWantQuiet(t, d.take(), "an ok reading split by a suspend")
		d.run(resume+30*time.Second, resume+obsWindow, levelOK, false)
		obsWantOne(t, d.take(), obsEvent{at: resume + obsWindow, from: levelWarning, to: levelOK, withdraw: true}, false)
	})
}

// TestObserveCriticalR6_3R7_2: a critical condition notifies at critical
// urgency as soon as its window passes, paused or not.
func TestObserveCriticalR6_3R7_2(t *testing.T) {
	for _, paused := range []bool{false, true} {
		t.Run(fmt.Sprintf("paused=%v", paused), func(t *testing.T) {
			d := newObsDriver()
			d.run(0, obsWindow, levelCritical, paused)
			obsWantOne(t, d.take(), obsEvent{at: obsWindow, from: levelOK, to: levelCritical, notify: true, urgency: 2}, true)
		})
	}
}

// TestObserveWarningNeverRepeatsR6_4: the reminder is for critical only.
func TestObserveWarningNeverRepeatsR6_4(t *testing.T) {
	d := newObsDriver()
	d.run(0, obsWindow, levelWarning, false)
	d.take()
	d.run(obsWindow+obsSample, obsWindow+3*obsReminder, levelWarning, false)
	obsWantQuiet(t, d.take(), "three hours of an active warning")
}

// TestObserveReminderR6_5: an active critical repeats once per reminder
// interval measured from the last notification, not earlier and not twice.
func TestObserveReminderR6_5(t *testing.T) {
	d := newObsDriver()
	d.run(0, obsWindow, levelCritical, false)
	d.take()
	first := obsWindow
	d.run(first+obsSample, first+obsReminder-obsSample, levelCritical, false)
	obsWantQuiet(t, d.take(), "critical just under one reminder interval")
	d.at(first+obsReminder, levelCritical, false)
	obsWantOne(t, d.take(), obsEvent{at: first + obsReminder, from: levelCritical, to: levelCritical, notify: true, urgency: 2}, true)
	d.run(first+obsReminder+obsSample, first+2*obsReminder-obsSample, levelCritical, false)
	obsWantQuiet(t, d.take(), "critical during the second interval")
	d.at(first+2*obsReminder, levelCritical, false)
	obsWantOne(t, d.take(), obsEvent{at: first + 2*obsReminder, from: levelCritical, to: levelCritical, notify: true, urgency: 2}, true)
}

// TestObserveEscalationAndFallbackR6_3R6_7R6_8: warning to critical notifies
// at once even inside the reminder interval; critical back to warning changes
// the level silently; a second escalation minutes later notifies again.
func TestObserveEscalationAndFallbackR6_3R6_7R6_8(t *testing.T) {
	d := newObsDriver()
	d.run(0, 60*time.Second, levelWarning, false)
	obsWantOne(t, d.take(), obsEvent{at: 60 * time.Second, from: levelOK, to: levelWarning, notify: true, urgency: 1}, true)

	d.run(90*time.Second, 150*time.Second, levelCritical, false)
	obsWantOne(t, d.take(), obsEvent{at: 150 * time.Second, from: levelWarning, to: levelCritical, notify: true, urgency: 2}, true)

	d.run(180*time.Second, 240*time.Second, levelWarning, false)
	obsWantOne(t, d.take(), obsEvent{at: 240 * time.Second, from: levelCritical, to: levelWarning}, false)
	if d.c.level != levelWarning {
		t.Fatalf("level after the fallback = %s, want warning", obsLevelText(d.c.level))
	}

	d.run(270*time.Second, 330*time.Second, levelCritical, false)
	obsWantOne(t, d.take(), obsEvent{at: 330 * time.Second, from: levelWarning, to: levelCritical, notify: true, urgency: 2}, true)
}

// TestObserveClearR6_11R6_12: clearing needs the window too, then withdraws
// without notifying, from warning and from critical.
func TestObserveClearR6_11R6_12(t *testing.T) {
	for _, active := range []level{levelWarning, levelCritical} {
		t.Run(obsLevelText(active), func(t *testing.T) {
			d := newObsDriver()
			d.run(0, obsWindow, active, false)
			d.take()
			// Hostile: one ok reading inside the window must not clear.
			d.at(obsWindow+30*time.Second, levelOK, false)
			d.at(obsWindow+60*time.Second, active, false)
			obsWantQuiet(t, d.take(), "a single ok reading")
			d.run(obsWindow+90*time.Second, obsWindow+150*time.Second, levelOK, false)
			obsWantOne(t, d.take(), obsEvent{at: obsWindow + 150*time.Second, from: active, to: levelOK, withdraw: true}, false)
		})
	}
}

// TestObservePausedWarningR7_1R7_4: a warning raised while paused changes the
// level (the icon still shows it) but never notifies, during the pause or
// after it; a later escalation still notifies.
func TestObservePausedWarningR7_1R7_4(t *testing.T) {
	d := newObsDriver()
	d.run(0, obsWindow, levelWarning, true)
	obsWantOne(t, d.take(), obsEvent{at: obsWindow, from: levelOK, to: levelWarning}, false)
	if !d.c.notifiedAt.IsZero() {
		t.Fatalf("notifiedAt = %v for a warning raised while paused, want zero", d.c.notifiedAt)
	}
	d.run(obsWindow+obsSample, 2*obsReminder, levelWarning, false)
	obsWantQuiet(t, d.take(), "the same warning after the pause ended")
	esc := 2*obsReminder + obsSample
	d.run(esc, esc+obsWindow, levelCritical, false)
	obsWantOne(t, d.take(), obsEvent{at: esc + obsWindow, from: levelWarning, to: levelCritical, notify: true, urgency: 2}, true)
}
