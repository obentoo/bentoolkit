package tray_test

// Story 098, sub-task 5.5, beyond the authored file: the SMART Changed
// channel closing (the system bus lost), a partial drive list, a changed
// NVMe warning set on an active condition, and the monitor switched off.

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/obentoo/bentoolkit/internal/desktop/smartmon"
	"github.com/obentoo/bentoolkit/internal/tray"
)

// s098PartialSmart is udisks2 with one malformed drive: Drives returns the
// well-formed drives together with partial, as smartmon.Monitor does.
type s098PartialSmart struct {
	*s098Smart
	pmu     sync.Mutex
	partial error
}

func (m *s098PartialSmart) Drives(ctx context.Context) ([]smartmon.Drive, error) {
	list, err := m.s098Smart.Drives(ctx)
	if err != nil {
		return nil, err
	}
	m.pmu.Lock()
	defer m.pmu.Unlock()
	return list, m.partial
}

func (m *s098PartialSmart) setPartial(err error) {
	m.pmu.Lock()
	defer m.pmu.Unlock()
	m.partial = err
}

// TestS098Smart_R2_9_SystemBusLostKeepsConditionsAndWarnsOnce: closing the
// SMART Changed channel logs one WARN naming the system bus, calls udisks2
// no more, withdraws nothing, keeps the tooltip line, and the failing drive
// is still reminded about on the sample timer.
func TestS098Smart_R2_9_SystemBusLostKeepsConditionsAndWarnsOnce(t *testing.T) {
	s := newS098(t)
	s.cfg = s097Config(t, "monitors:\n  reminder: 15m\n")
	m := newS098Smart(s098Failing(s098DriveA, "WDC WD40EFRX", "WD-123"))
	s.startSmart(m)
	if n := len(s.smartSent(s098DriveA)); n != 1 {
		t.Fatalf("SMART notifications at start-up = %d, want 1", n)
	}
	calls := m.count()

	close(m.changed)
	lost := func() int { return s.warns("monitor=smart", "bus=system") }
	s.waitFor("the bus-lost WARN", func() bool { return lost() >= 1 })
	s086WaitParked(s.appHarness)
	s.never("a second bus-lost WARN (the loop selecting the closed channel again)", func() bool { return lost() > 1 })

	if n := m.count(); n != calls {
		t.Errorf("Drives called %d more times after the bus was lost; a closed Changed is not a change", n-calls)
	}
	if closed := s.out.closedIDs(); len(closed) != 0 {
		t.Errorf("losing the system bus withdrew %v; the SMART conditions must be kept", closed)
	}
	if a := s.alertLines(); len(a) != 1 || !strings.Contains(a[0], "WDC WD40EFRX") {
		t.Errorf("tooltip alerts after the system bus was lost = %q, want the drive's line kept", a)
	}
	if n := len(s.transitions("smart")); n != 1 {
		t.Errorf("SMART transition lines = %d, want 1 (the start-up raise only)", n)
	}
	s.tickUntil("a SMART reminder after the bus was lost", 35, func() bool { return len(s.smartSent(s098DriveA)) >= 2 })
	if n := m.count(); n != calls {
		t.Errorf("the reminder called Drives %d times; it re-observes without a bus call", n-calls)
	}
}

// TestS098Smart_R4_5_PartialListKeepsTheUnlistedActiveCondition: a list
// returned with an error clears only the listed drives that read healthy;
// the active condition of a drive it left out is kept, with one WARN. A
// complete list without that drive then clears it.
func TestS098Smart_R4_5_PartialListKeepsTheUnlistedActiveCondition(t *testing.T) {
	s := newS098(t)
	m := &s098PartialSmart{s098Smart: newS098Smart(s098Failing(s098DriveA, "WDC", "A1"), s098Failing(s098DriveB, "ST4000", "B1"))}
	s.start(nil, func(d *tray.Deps) { d.Smart = m })
	s.waitFor("the start-up SMART evaluation", func() bool { return m.count() >= 1 })
	s086WaitParked(s.appHarness)
	a, b := s.smartSent(s098DriveA), s.smartSent(s098DriveB)
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("SMART notifications at start-up: A %+v, B %+v, want 1 each", a, b)
	}

	m.set(s098Healthy(s098DriveA, "WDC", "A1"))
	m.setPartial(errors.New("reading drive B: property Model: unexpected D-Bus type"))
	s.fireSmart(m.s098Smart)
	if closed := s.out.closedIDs(); !slices.Equal(closed, []uint32{a[0].ID}) {
		t.Errorf("closed %v after a partial list, want only the listed healthy A's %d", closed, a[0].ID)
	}
	if lines := s.alertLines(); len(lines) != 1 || !strings.Contains(lines[0], "ST4000") {
		t.Errorf("tooltip alerts after a partial list = %q, want drive B's line kept", lines)
	}
	if n := s.warns("monitor=smart", "unexpected D-Bus type"); n != 1 {
		t.Errorf("SMART WARN lines after a partial list = %d, want 1\n%s", n, strings.Join(s.logs.lines(), "\n"))
	}

	m.setPartial(nil)
	s.fireSmart(m.s098Smart)
	if closed := s.out.closedIDs(); !slices.Equal(closed, []uint32{a[0].ID, b[0].ID}) {
		t.Errorf("closed %v after a complete list without B, want A's %d then B's %d", closed, a[0].ID, b[0].ID)
	}
	if n := len(s.smartSent(s098DriveA)) + len(s.smartSent(s098DriveB)); n != 2 {
		t.Errorf("SMART notifications = %d, want the 2 start-up ones only", n)
	}
}

// TestS098Smart_R4_4_ChangedNVMeWarningsResendWhilePaused: a changed NVMe
// warning set on an active condition sends again, replacing the standing
// notification, even while paused; the same set again sends nothing.
func TestS098Smart_R4_4_ChangedNVMeWarningsResendWhilePaused(t *testing.T) {
	s := newS098(t)
	nvme := func(w ...string) smartmon.Drive {
		return smartmon.Drive{Path: s098DriveN, Model: "Samsung SSD 980", Serial: "S64DNX0R", Warnings: w}
	}
	m := newS098Smart(nvme("spare"))
	s.startSmart(m)
	s.pause()
	m.set(nvme("spare", "temperature"))
	s.fireSmart(m)
	sent := s.smartSent(s098DriveN)
	if len(sent) != 2 {
		t.Fatalf("SMART notifications after the warning set changed = %+v, want 2", sent)
	}
	if r := sent[1].Msg; r.ReplacesID != sent[0].ID || r.Urgency != 2 || !strings.Contains(r.Body, "temperature") {
		t.Errorf("resend = %+v, want urgency 2 replacing %d and naming temperature", r, sent[0].ID)
	}
	if lines := s.alertLines(); len(lines) != 1 || !strings.Contains(lines[0], "temperature") {
		t.Errorf("tooltip alerts = %q, want one line naming temperature", lines)
	}
	s.fireSmart(m)
	if n := len(s.smartSent(s098DriveN)); n != 2 {
		t.Errorf("SMART notifications after the same warning set again = %d, want 2", n)
	}
	if n := len(s.transitions("smart")); n != 1 {
		t.Errorf("SMART transition lines = %d, want 1: a changed warning set moves no level", n)
	}
}

// TestS098Smart_DisabledMonitorCallsNothing: with the SMART monitor switched
// off, udisks2 is never listed, not at start-up and not on Changed.
func TestS098Smart_DisabledMonitorCallsNothing(t *testing.T) {
	s := newS098(t)
	s.cfg = s097Config(t, "monitors:\n  smart:\n    enabled: false\n")
	m := newS098Smart(s098Failing(s098DriveA, "WDC", "A1"))
	s.start(nil, func(d *tray.Deps) { d.Smart = m })
	m.changed <- struct{}{}
	s.never("a Drives call with the SMART monitor off", func() bool { return m.count() > 0 })
	if n := len(s.smartSent(s098DriveA)); n != 0 {
		t.Errorf("SMART notifications with the monitor off = %d, want 0", n)
	}
}
