package tray_test

// Story 098, sub-task 5.4 (R2.9): a manager's Changed channel closing means
// its bus connection was lost. The loop stops selecting it (no spin), logs
// one WARN naming the bus and the manager, keeps that manager's conditions,
// and keeps serving the other manager.

import (
	"slices"
	"strings"
	"testing"
)

// TestS098Units_R2_9_SystemBusLostKeepsConditionsAndWarnsOnce: closing the
// system manager's Changed keeps its failed unit, withdraws nothing, logs one
// WARN naming the system bus and manager, and the user manager still raises.
func TestS098Units_R2_9_SystemBusLostKeepsConditionsAndWarnsOnce(t *testing.T) {
	s := newS098(t)
	system, user := newS098Units("a.service"), newS098Units()
	s.startUnits(system, user)
	if n := len(s.unitsSent()); n != 1 {
		t.Fatalf("units notifications at start-up = %d, want 1", n)
	}
	calls := system.count()

	close(system.changed)
	lost := func() int { return s.warns("monitor=units", "bus=system", "manager=system") }
	s.waitFor("the bus-lost WARN", func() bool { return lost() >= 1 })
	s086WaitParked(s.appHarness)
	s.never("a second bus-lost WARN (the loop selecting the closed channel again)", func() bool { return lost() > 1 })

	if n := system.count(); n != calls {
		t.Errorf("Failed called %d more times after the bus was lost; a closed Changed is not a change", n-calls)
	}
	if closed := s.out.closedIDs(); len(closed) != 0 {
		t.Errorf("losing the system bus withdrew %v; its conditions must be kept", closed)
	}
	if a := s.alertLines(); len(a) != 1 || !strings.Contains(a[0], "a.service") {
		t.Errorf("tooltip alerts after the system bus was lost = %q, want the a.service line kept", a)
	}
	if n := len(s.transitions("units")); n != 1 {
		t.Errorf("units transition lines = %d, want 1 (the start-up raise only)", n)
	}

	user.set("pipewire.service")
	s.fire(user)
	sent := s.unitsSent()
	if len(sent) != 2 || !strings.Contains(sent[1].Msg.Body, "pipewire.service") || !strings.Contains(sent[1].Msg.Body, "2") {
		t.Errorf("units notifications after the user unit failed = %+v, want a second naming pipewire.service and the count 2", sent)
	}
	if sent[1].Msg.ReplacesID != sent[0].ID {
		t.Errorf("the user unit's send replaces %d, want the shared id %d", sent[1].Msg.ReplacesID, sent[0].ID)
	}
}

// TestS098Units_R2_9_SessionBusLostNamesTheSessionBus: the user manager's
// Changed closing logs one WARN naming the session bus and the user manager,
// and keeps its condition; the system manager is untouched.
func TestS098Units_R2_9_SessionBusLostNamesTheSessionBus(t *testing.T) {
	s := newS098(t)
	system, user := newS098Units(), newS098Units("pipewire.service")
	s.startUnits(system, user)

	close(user.changed)
	lost := func() int { return s.warns("monitor=units", "bus=session", "manager=user") }
	s.waitFor("the bus-lost WARN", func() bool { return lost() >= 1 })
	s086WaitParked(s.appHarness)
	s.never("a second bus-lost WARN", func() bool { return lost() > 1 })
	if n := s.warns("bus=system"); n != 0 {
		t.Errorf("%d WARN lines name the system bus; only the session one was lost", n)
	}
	if a := s.alertLines(); len(a) != 1 || !strings.Contains(a[0], "pipewire.service") {
		t.Errorf("tooltip alerts = %q, want the pipewire.service line kept", a)
	}

	system.set("a.service")
	s.fire(system)
	if sent := s.unitsSent(); len(sent) != 2 || !strings.Contains(sent[1].Msg.Body, "a.service") {
		t.Errorf("the system manager stopped being served after the session bus was lost: %+v", sent)
	}
	system.set()
	s.fire(system)
	if closed := s.out.closedIDs(); !slices.Equal(closed, []uint32(nil)) {
		t.Errorf("a.service clearing closed %v while the kept pipewire.service stays failed", closed)
	}
}
