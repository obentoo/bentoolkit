package tray

// A suspend stops the monotonic clock but not the wall clock. Every time a
// condition keeps must therefore be wall-clock only, or a later Sub on it
// would skip the time the machine slept: a critical reminder would come up to
// one reminder late, and lasted would undercount. Go cannot build a time.Time
// whose two readings diverge, so these tests feed times derived from
// time.Now() (each carries a monotonic reading) and check what observe keeps.

import (
	"strings"
	"testing"
	"time"
)

// hasMonotonic reports whether t carries a monotonic reading; String prints
// it as " m=±…" exactly when it does.
func hasMonotonic(t time.Time) bool {
	return strings.Contains(t.String(), " m=")
}

// driveToCritical feeds critical readings every obsSample from t0 until the
// condition reaches critical, and returns the offset of the move.
func driveToCritical(t *testing.T, c *condition, t0 time.Time) time.Duration {
	t.Helper()
	for off := time.Duration(0); off <= 2*obsWindow; off += obsSample {
		tr, _ := c.observe(t0.Add(off), levelCritical, obsWindow, obsReminder, obsMaxGap, false)
		if tr.To == levelCritical && tr.Notify {
			return off
		}
	}
	t.Fatalf("condition never reached critical within %v", 2*obsWindow)
	return 0
}

func TestS100ConditionKeepsNoMonotonicReading(t *testing.T) {
	t0 := time.Now()
	if !hasMonotonic(t0) {
		t.Fatal("time.Now() carries no monotonic reading on this platform; the test cannot tell")
	}
	c := &condition{}
	moved := driveToCritical(t, c, t0)

	// The reminder stamps notifiedAt again, from a sample observe took.
	tr, _ := c.observe(t0.Add(moved+obsReminder), levelCritical, obsWindow, obsReminder, obsMaxGap, false)
	if !tr.Notify {
		t.Fatalf("no reminder at %v after the critical notification", obsReminder)
	}

	for name, v := range map[string]time.Time{
		"since":       c.since,
		"lastSample":  c.lastSample,
		"activeSince": c.activeSince,
		"notifiedAt":  c.notifiedAt,
	} {
		if v.IsZero() {
			t.Errorf("%s is zero; the drive did not set it", name)
			continue
		}
		if hasMonotonic(v) {
			t.Errorf("%s keeps a monotonic reading (%s); after a suspend a Sub on it skips the time asleep", name, v)
		}
	}
}

func TestS100LastedIsWallClockDifference(t *testing.T) {
	t0 := time.Now()
	c := &condition{}
	driveToCritical(t, c, t0)

	at := 3 * time.Hour
	if got := c.lasted(t0.Add(at)); got != at {
		t.Fatalf("lasted = %v, want %v (the spell began at the first critical reading)", got, at)
	}
}

func TestS100ReminderAtExactlyTheInterval(t *testing.T) {
	t0 := time.Now()
	c := &condition{}
	moved := driveToCritical(t, c, t0)

	if tr, _ := c.observe(t0.Add(moved+obsReminder-time.Second), levelCritical, obsWindow, obsReminder, obsMaxGap, false); tr.Notify {
		t.Fatalf("reminded one second before %v", obsReminder)
	}
	if tr, _ := c.observe(t0.Add(moved+obsReminder), levelCritical, obsWindow, obsReminder, obsMaxGap, false); !tr.Notify {
		t.Fatalf("no reminder at exactly %v", obsReminder)
	}
}

func TestS100GapRestartsTheSpell(t *testing.T) {
	t0 := time.Now()
	c := &condition{}
	c.observe(t0, levelWarning, obsWindow, obsReminder, obsMaxGap, false)

	resumed := obsMaxGap + time.Second
	c.observe(t0.Add(resumed), levelWarning, obsWindow, obsReminder, obsMaxGap, false)
	if tr, ok := c.observe(t0.Add(resumed+obsWindow-time.Second), levelWarning, obsWindow, obsReminder, obsMaxGap, false); ok && tr.To == levelWarning {
		t.Fatalf("raised warning %v after the gap; the gap must restart the window", obsWindow-time.Second)
	}
	if tr, ok := c.observe(t0.Add(resumed+obsWindow), levelWarning, obsWindow, obsReminder, obsMaxGap, false); !ok || tr.To != levelWarning {
		t.Fatalf("no warning one window after the gap: %+v ok=%v", tr, ok)
	}
}
