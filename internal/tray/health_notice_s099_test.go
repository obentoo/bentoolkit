package tray

// Story 099, sub-task 4.1: levelNotice, a low-urgency condition level between
// ok and warning that carries the pending-updates conditions (R2.4, R2.10,
// R5.2). Driven through condition.observe with window 0, as the update
// conditions are.
//
// Hostile halves first: a notice must not read as ok in the log lines, must
// not be reminded like a critical, must not be sent later when it was raised
// while paused, and adding it must not move the urgency of a warning or a
// critical raised from ok.

import (
	"testing"
	"time"
)

const (
	s099NoticeReminder = time.Hour
	s099NoticeMaxGap   = 2 * time.Hour
	s099NoticeStep     = 10 * time.Minute
)

var s099NoticeT0 = time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)

// s099Observe feeds one reading at window 0 and stamps notifiedAt after a
// Notify the way the App does after a successful Send.
func s099Observe(c *condition, now time.Time, got level, paused bool) (transition, bool) {
	tr, ok := c.observe(now, got, 0, s099NoticeReminder, s099NoticeMaxGap, paused)
	if tr.Notify {
		c.notifiedAt = now
	}
	return tr, ok
}

func s099OKCondition() *condition {
	return &condition{level: levelOK, candidate: levelOK}
}

// TestS099NoticeStringIsNotOK is the hostile half of the log spelling: the
// level is a distinct word, never the "ok" a default branch would give.
func TestS099NoticeStringIsNotOK(t *testing.T) {
	want := map[level]string{
		levelOK:       "ok",
		levelNotice:   "notice",
		levelWarning:  "warning",
		levelCritical: "critical",
	}
	seen := map[string]level{}
	for l, w := range want {
		got := l.String()
		if got != w {
			t.Errorf("level(%d).String() = %q, want %q", int(l), got, w)
		}
		if other, dup := seen[got]; dup {
			t.Errorf("level(%d) and level(%d) both spell %q", int(l), int(other), got)
		}
		seen[got] = l
	}
}

// TestS099NoticeOrderedBetweenOKAndWarning: severity comparisons stay
// symbolic, so the notice must sit strictly between ok and warning.
func TestS099NoticeOrderedBetweenOKAndWarning(t *testing.T) {
	//nolint:staticcheck // QF1001: the ordering requirement is stated positively and negated, not restated as its own failure.
	if !(levelOK < levelNotice && levelNotice < levelWarning && levelWarning < levelCritical) {
		t.Errorf("levels ok=%d notice=%d warning=%d critical=%d, want ok < notice < warning < critical",
			int(levelOK), int(levelNotice), int(levelWarning), int(levelCritical))
	}
	if levelOK != 0 {
		t.Errorf("levelOK = %d, want the zero value", int(levelOK))
	}
}

// TestS099NoticeUrgencyIsLow: an active notice notifies at low urgency; the
// warning and critical urgencies are unchanged.
func TestS099NoticeUrgencyIsLow(t *testing.T) {
	for _, tc := range []struct {
		l    level
		want byte
	}{
		{levelNotice, urgencyLow},
		{levelWarning, urgencyNormal},
		{levelCritical, urgencyCritical},
	} {
		if got := levelUrgency(tc.l); got != tc.want {
			t.Errorf("levelUrgency(%s) = %d, want %d", tc.l, got, tc.want)
		}
	}
}

// TestS099NoticeRaisedFromOKNotifiesAtLowUrgency is R2.4 and R5.2: with
// window 0 the first notice reading raises the condition and notifies once,
// at low urgency.
func TestS099NoticeRaisedFromOKNotifiesAtLowUrgency(t *testing.T) {
	c := s099OKCondition()
	tr, ok := s099Observe(c, s099NoticeT0, levelNotice, false)
	if !ok {
		t.Fatal("first notice reading at window 0: observe reported no change")
	}
	if tr.From != levelOK || tr.To != levelNotice {
		t.Errorf("transition %s -> %s, want ok -> notice", tr.From, tr.To)
	}
	if !tr.Notify {
		t.Fatal("notice raised from ok, not paused: no notification")
	}
	if tr.Urgency != urgencyLow {
		t.Errorf("notice notification urgency = %d, want low (%d)", tr.Urgency, urgencyLow)
	}
	if tr.Withdraw {
		t.Error("raising a notice withdrew a notification")
	}
	if c.level != levelNotice {
		t.Errorf("condition level after the raise = %s, want notice", c.level)
	}
}

// TestS099NoticeIsNeverReminded is the hostile half of the reminder: only a
// critical is reminded, so a notice held for two days notifies once only.
func TestS099NoticeIsNeverReminded(t *testing.T) {
	c := s099OKCondition()
	if tr, _ := s099Observe(c, s099NoticeT0, levelNotice, false); !tr.Notify {
		t.Fatal("notice raised from ok: no first notification")
	}
	for off := s099NoticeStep; off <= 48*time.Hour; off += s099NoticeStep {
		tr, ok := s099Observe(c, s099NoticeT0.Add(off), levelNotice, false)
		if tr.Notify || tr.Withdraw || (ok && tr.From != tr.To) {
			t.Fatalf("notice held @%v: unexpected event %+v (a notice is never reminded)", off, tr)
		}
	}
}

// TestS099NoticeRaisedWhilePausedIsNeverSent is R2.10: a notice raised while
// paused sends nothing, and is not sent later once the pause ends.
func TestS099NoticeRaisedWhilePausedIsNeverSent(t *testing.T) {
	c := s099OKCondition()
	tr, ok := s099Observe(c, s099NoticeT0, levelNotice, true)
	if !ok || tr.To != levelNotice {
		t.Fatalf("paused notice reading: transition %+v ok=%v, want the condition raised to notice", tr, ok)
	}
	if tr.Notify {
		t.Errorf("notice raised while paused notified at urgency %d", tr.Urgency)
	}
	for off := s099NoticeStep; off <= 3*time.Hour; off += s099NoticeStep {
		if tr, _ := s099Observe(c, s099NoticeT0.Add(off), levelNotice, false); tr.Notify {
			t.Fatalf("notice raised while paused was sent @%v after the pause ended", off)
		}
	}
	if c.level != levelNotice {
		t.Errorf("condition level = %s, want notice kept active (its tooltip line stays)", c.level)
	}
}

// TestS099NoticeClearingWithdraws: a notice that clears withdraws its
// notification, like any active level.
func TestS099NoticeClearingWithdraws(t *testing.T) {
	c := s099OKCondition()
	s099Observe(c, s099NoticeT0, levelNotice, false)
	tr, ok := s099Observe(c, s099NoticeT0.Add(s099NoticeStep), levelOK, false)
	if !ok || tr.From != levelNotice || tr.To != levelOK {
		t.Fatalf("clearing reading: transition %+v ok=%v, want notice -> ok", tr, ok)
	}
	if !tr.Withdraw {
		t.Error("notice cleared: notification not withdrawn")
	}
	if tr.Notify {
		t.Error("notice cleared: a notification was sent")
	}
}

// TestS099NoticeLeavesWarningAndCriticalUrgencies is the converse of the low
// urgency: once the raise from ok follows the level's urgency, a warning must
// still notify at normal urgency and a critical at critical urgency.
func TestS099NoticeLeavesWarningAndCriticalUrgencies(t *testing.T) {
	for _, tc := range []struct {
		got  level
		want byte
	}{
		{levelWarning, urgencyNormal},
		{levelCritical, urgencyCritical},
	} {
		c := s099OKCondition()
		tr, ok := s099Observe(c, s099NoticeT0, tc.got, false)
		if !ok || !tr.Notify {
			t.Errorf("%s raised from ok at window 0: transition %+v ok=%v, want a notification", tc.got, tr, ok)
			continue
		}
		if tr.Urgency != tc.want {
			t.Errorf("%s raised from ok: urgency = %d, want %d", tc.got, tr.Urgency, tc.want)
		}
	}
}
