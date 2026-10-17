package tray_test

// Story 098, sub-task 6.1 fix round: bentoo-tray logs one WARN when its
// start-up question to a systemd manager or to udisks2 fails, and hands the
// App Deps.SystemUnitsProbeFailed, UserUnitsProbeFailed or SmartProbeFailed.
// The flag pre-arms that monitor's listing-failure streak, the way
// EmergeStartFailed pre-arms the emerge one: the first failed listing after
// a failed probe logs nothing, a successful listing ends the streak silently,
// and the next failure starts a new streak with one WARN.
//
// Hostile halves first: each flag pre-arms its own monitor only, so a crossed
// flag (system for user) still WARNs for the manager it did not cover; and
// without a flag the first failed listing WARNs, so the silence is the
// flag's doing.

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/tray"
)

// startUnitsWith runs the App with both managers and the probe flags edit
// sets, and waits for each manager's start-up reconciliation.
func (s *s098Harness) startUnitsWith(system, user *s098Units, edit func(*tray.Deps)) {
	s.t.Helper()
	s.start(nil, func(d *tray.Deps) {
		d.SystemUnits, d.UserUnits = system, user
		edit(d)
	})
	for _, u := range []*s098Units{system, user} {
		s.waitFor("the start-up unit reconciliation", func() bool { return u.count() >= 1 })
	}
	s086WaitParked(s.appHarness)
}

// TestS098Units_ProbeFailedPreArmsOnlyItsManager: both managers fail their
// first listing; only the one whose probe failed stays silent.
func TestS098Units_ProbeFailedPreArmsOnlyItsManager(t *testing.T) {
	cases := []struct {
		name           string
		edit           func(*tray.Deps)
		silent, warned string
	}{
		{"system probe failed", func(d *tray.Deps) { d.SystemUnitsProbeFailed = true }, "system", "user"},
		{"user probe failed", func(d *tray.Deps) { d.UserUnitsProbeFailed = true }, "user", "system"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newS098(t)
			cause := fmt.Errorf("calling ListUnitsFiltered: %w", errors.New("connection reset by peer"))
			system, user := newS098Units(), newS098Units()
			system.setErr(cause)
			user.setErr(cause)
			s.startUnitsWith(system, user, tc.edit)
			warns := func(manager string) int {
				return s.warns("monitor=units", "manager="+manager, "connection reset by peer")
			}
			if n := warns(tc.silent); n != 0 {
				t.Errorf("WARN lines for the %s manager after its failed probe and failed first listing = %d, want 0\n%s",
					tc.silent, n, strings.Join(s.logs.lines(), "\n"))
			}
			if n := warns(tc.warned); n != 1 {
				t.Errorf("WARN lines for the %s manager, whose probe did not fail = %d, want 1: the flag must pre-arm its own manager only\n%s",
					tc.warned, n, strings.Join(s.logs.lines(), "\n"))
			}
		})
	}
}

// TestS098Units_ProbeFailedStreakEndsOnSuccess: after the pre-armed streak,
// a successful listing ends it without a line and the next failure WARNs.
func TestS098Units_ProbeFailedStreakEndsOnSuccess(t *testing.T) {
	s := newS098(t)
	cause := fmt.Errorf("calling ListUnitsFiltered: %w", errors.New("connection reset by peer"))
	system, user := newS098Units(), newS098Units()
	system.setErr(cause)
	s.startUnitsWith(system, user, func(d *tray.Deps) { d.SystemUnitsProbeFailed = true })
	s.fire(system)
	warns := func() int { return s.warns("monitor=units", "manager=system") }
	if n := warns(); n != 0 {
		t.Fatalf("system WARN lines while the pre-armed streak lasts = %d, want 0\n%s", n, strings.Join(s.logs.lines(), "\n"))
	}
	system.set()
	s.fire(system)
	if n := warns(); n != 0 {
		t.Errorf("system WARN lines after the listing succeeded = %d, want 0: ending a streak logs nothing", n)
	}
	system.setErr(cause)
	s.fire(system)
	s.fire(system)
	if n := warns(); n != 1 {
		t.Errorf("system WARN lines after a new failure streak = %d, want 1\n%s", n, strings.Join(s.logs.lines(), "\n"))
	}
}

// TestS098Units_NoProbeFlagWarnsOnTheFirstFailedListing is the control: with
// no flag set, a failed first listing logs its WARN.
func TestS098Units_NoProbeFlagWarnsOnTheFirstFailedListing(t *testing.T) {
	s := newS098(t)
	system, user := newS098Units(), newS098Units()
	system.setErr(errors.New("connection reset by peer"))
	s.startUnitsWith(system, user, func(*tray.Deps) {})
	if n := s.warns("monitor=units", "manager=system"); n != 1 {
		t.Errorf("system WARN lines after a failed first listing and no probe flag = %d, want 1", n)
	}
}

// TestS098Smart_ProbeFailedPreArmsTheStreak: a failed Available probe makes
// the first failed Drives call silent; a complete list ends the streak with
// no line, and the next failure WARNs once.
func TestS098Smart_ProbeFailedPreArmsTheStreak(t *testing.T) {
	s := newS098(t)
	cause := fmt.Errorf("calling GetManagedObjects: %w", errors.New("name has no owner"))
	m := newS098Smart()
	m.setErr(cause)
	s.start(nil, func(d *tray.Deps) {
		d.Smart = m
		d.SmartProbeFailed = true
	})
	s.waitFor("the start-up SMART evaluation", func() bool { return m.count() >= 1 })
	s086WaitParked(s.appHarness)
	s.fireSmart(m)
	warns := func() int { return s.warns("monitor=smart") }
	if n := warns(); n != 0 {
		t.Fatalf("SMART WARN lines after a failed probe and two failed listings = %d, want 0\n%s", n, strings.Join(s.logs.lines(), "\n"))
	}
	m.set(s098Healthy(s098DriveA, "WDC", "A1"))
	s.fireSmart(m)
	if n := warns(); n != 0 {
		t.Errorf("SMART WARN lines after a complete list = %d, want 0: ending a streak logs nothing", n)
	}
	m.setErr(cause)
	s.fireSmart(m)
	s.fireSmart(m)
	if n := warns(); n != 1 {
		t.Errorf("SMART WARN lines after a new failure streak = %d, want 1\n%s", n, strings.Join(s.logs.lines(), "\n"))
	}
}

// TestS098Smart_NoProbeFlagWarnsOnTheFirstFailedListing is the control: with
// no flag set, a failed first Drives call logs its WARN.
func TestS098Smart_NoProbeFlagWarnsOnTheFirstFailedListing(t *testing.T) {
	s := newS098(t)
	m := newS098Smart()
	m.setErr(errors.New("name has no owner"))
	s.startSmart(m)
	if n := s.warns("monitor=smart", "name has no owner"); n != 1 {
		t.Errorf("SMART WARN lines after a failed first listing and no probe flag = %d, want 1", n)
	}
}
