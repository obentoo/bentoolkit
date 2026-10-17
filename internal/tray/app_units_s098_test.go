package tray_test

// Story 098, sub-task 5.4: failed systemd units of the system and the user
// manager raise warning conditions at start-up and on every Changed, and all
// of them share one notification.
//
// Pinned contract (design.md "internal/tray/sample.go and app.go",
// "internal/desktop/unitmon"): Deps gains SystemUnits and UserUnits
// (Failed(ctx) ([]unitmon.Unit, error), Changed() <-chan struct{}); nil means
// absent. Unit targets are "system:<unit>" and "user:<unit>"; the shared
// notification key is "units".
//
// Hostile halves first: the same unit name failing under both managers is two
// conditions (one clearing must not withdraw the other), a unit clearing while
// another stays must send and close nothing, a failed call is not an empty
// list; only then the plain raise and the last clear.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/desktop/unitmon"
	"github.com/obentoo/bentoolkit/internal/tray"
)

var (
	_ tray.UnitMonitor = (*unitmon.Monitor)(nil)
	_ tray.UnitMonitor = (*s098Units)(nil)
)

// s098Units is one manager: Failed returns the units the test sets, Changed
// is the signal the test fires. hang makes Failed wait for its context.
type s098Units struct {
	mu       sync.Mutex
	failed   []unitmon.Unit
	err      error
	hang     bool
	calls    int
	deadline bool // every call so far carried a deadline
	changed  chan struct{}
}

func newS098Units(names ...string) *s098Units {
	u := &s098Units{changed: make(chan struct{}, 1), deadline: true}
	u.set(names...)
	return u
}

func (u *s098Units) Failed(ctx context.Context) ([]unitmon.Unit, error) {
	u.mu.Lock()
	u.calls++
	if _, ok := ctx.Deadline(); !ok {
		u.deadline = false
	}
	hang, err, list := u.hang, u.err, slices.Clone(u.failed)
	u.mu.Unlock()
	if hang {
		<-ctx.Done()
		return nil, fmt.Errorf("listing failed units: %w", ctx.Err())
	}
	if err != nil {
		return nil, err
	}
	return list, nil
}

func (u *s098Units) Changed() <-chan struct{} { return u.changed }

func (u *s098Units) set(names ...string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.failed = nil
	for _, n := range names {
		u.failed = append(u.failed, unitmon.Unit{Name: n})
	}
	u.err = nil
}
func (u *s098Units) setErr(err error) { u.mu.Lock(); defer u.mu.Unlock(); u.err = err }
func (u *s098Units) setHang(h bool)   { u.mu.Lock(); defer u.mu.Unlock(); u.hang = h }
func (u *s098Units) count() int       { u.mu.Lock(); defer u.mu.Unlock(); return u.calls }
func (u *s098Units) hadDeadline() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.deadline
}

// startUnits runs the App with the two managers (nil stays absent) and waits
// for the start-up reconciliation of each present one.
func (s *s098Harness) startUnits(system, user *s098Units) {
	s.t.Helper()
	s.start(nil, func(d *tray.Deps) {
		if system != nil {
			d.SystemUnits = system
		}
		if user != nil {
			d.UserUnits = user
		}
	})
	for _, u := range []*s098Units{system, user} {
		if u != nil {
			s.waitFor("the start-up unit reconciliation", func() bool { return u.count() >= 1 })
		}
	}
	s086WaitParked(s.appHarness)
}

// fire signals Changed on u and waits until the loop has re-read it.
func (s *s098Harness) fire(u *s098Units) {
	s.t.Helper()
	n := u.count()
	u.changed <- struct{}{}
	s.waitFor("the unit reconciliation after Changed", func() bool { return u.count() > n })
	s086WaitParked(s.appHarness)
}

func (s *s098Harness) unitsSent() []s097Sent { return s.out.matching(s098Keyed("units")) }

// TestS098Units_R2_7_SameNameUnderBothManagersIsTwoConditions: foo.service
// failed in the system AND the user manager is two conditions; the system one
// clearing sends and closes nothing, the user one clearing last withdraws.
func TestS098Units_R2_7_SameNameUnderBothManagersIsTwoConditions(t *testing.T) {
	s := newS098(t)
	system, user := newS098Units("foo.service"), newS098Units("foo.service")
	s.startUnits(system, user)
	sent := s.unitsSent()
	if len(sent) != 2 {
		t.Fatalf("units notifications for foo.service in both managers = %+v, want 2 (one per condition)", sent)
	}
	if v := s.viewNow(); len(v.Alerts) != 2 {
		t.Errorf("tooltip alerts = %q, want one line per manager's foo.service", v.Alerts)
	}

	system.set()
	s.fire(system)
	if n := len(s.out.closedIDs()); n != 0 {
		t.Errorf("the system foo.service clearing closed %v while the user one stays failed", s.out.closedIDs())
	}
	if n := len(s.unitsSent()); n != 2 {
		t.Errorf("units notifications after one of two cleared = %d, want still 2: a clear sends nothing", n)
	}
	if v := s.viewNow(); len(v.Alerts) != 1 {
		t.Errorf("tooltip alerts = %q, want the user foo.service line only", v.Alerts)
	}

	user.set()
	s.fire(user)
	if closed := s.out.closedIDs(); !slices.Equal(closed, []uint32{sent[1].ID}) {
		t.Errorf("closed %v after the last unit cleared, want only the shared notification's current id %d", closed, sent[1].ID)
	}
	if keys := s.out.closedKeys(); !slices.Equal(keys, []string{"units"}) {
		t.Errorf("close keys = %q, want [units]", keys)
	}
}

// TestS098Units_R2_5_R2_6_R2_10_FailedAtStartupShareOneNotification: units
// already failed at start-up raise at once, each send replacing the shared
// notification and naming its unit, its manager and the failed count.
func TestS098Units_R2_5_R2_6_R2_10_FailedAtStartupShareOneNotification(t *testing.T) {
	s := newS098(t)
	system, user := newS098Units("sshd.service"), newS098Units("pipewire.service")
	s.startUnits(system, user)
	sent := s.unitsSent()
	if len(sent) != 2 {
		t.Fatalf("units notifications at start-up = %+v, want 2", sent)
	}
	if sent[0].Msg.ReplacesID != 0 || sent[1].Msg.ReplacesID != sent[0].ID {
		t.Errorf("start-up sends replace %d then %d, want 0 then %d: one shared notification",
			sent[0].Msg.ReplacesID, sent[1].Msg.ReplacesID, sent[0].ID)
	}
	for _, m := range sent {
		if m.Msg.Urgency != 1 || m.Msg.Summary == "" {
			t.Errorf("units notification = %+v, want urgency 1 (warning) with a summary", m.Msg)
		}
		body := m.Msg.Body
		sys := strings.Contains(body, "sshd.service") && strings.Contains(body, "system")
		usr := strings.Contains(body, "pipewire.service") && strings.Contains(body, "user")
		if !sys && !usr {
			t.Errorf("units body %q names neither sshd.service (system) nor pipewire.service (user)", body)
		}
	}
	if !strings.Contains(sent[1].Msg.Body, "2") {
		t.Errorf("the second units body %q does not state the 2 units failed", sent[1].Msg.Body)
	}
	if !system.hadDeadline() || !user.hadDeadline() {
		t.Error("Failed was called without a deadline; a manager that never answers would stall the loop")
	}
}

// TestS098Units_R2_3_R2_10_FailureOnChangedReplacesTheSharedNote: a unit
// failing after start-up notifies on Changed; a second one replaces the
// shared notification, naming the new unit and the count.
func TestS098Units_R2_3_R2_10_FailureOnChangedReplacesTheSharedNote(t *testing.T) {
	s := newS098(t)
	system := newS098Units()
	s.startUnits(system, nil)
	if n := len(s.unitsSent()); n != 0 {
		t.Fatalf("units notifications with nothing failed = %d, want 0", n)
	}
	system.set("a.service")
	s.fire(system)
	sent := s.unitsSent()
	if len(sent) != 1 || sent[0].Msg.ReplacesID != 0 || !strings.Contains(sent[0].Msg.Body, "a.service") {
		t.Fatalf("units notifications after a.service failed = %+v, want 1 naming a.service", sent)
	}
	system.set("a.service", "b.service")
	s.fire(system)
	sent = s.unitsSent()
	if len(sent) != 2 {
		t.Fatalf("units notifications after b.service failed too = %+v, want 2", sent)
	}
	if m := sent[1].Msg; m.ReplacesID != sent[0].ID || !strings.Contains(m.Body, "b.service") || !strings.Contains(m.Body, "2") {
		t.Errorf("second units notification = %+v, want it replacing %d, naming b.service and the count 2", m, sent[0].ID)
	}
	s.wantTransitions("units", [2]string{"ok", "warning"}, [2]string{"ok", "warning"})
	lines := s.transitions("units")
	if !strings.Contains(lines[0], "target=system:a.service") || !strings.Contains(lines[1], "target=system:b.service") {
		t.Errorf("units transition lines = %q, want targets system:a.service then system:b.service", lines)
	}
}

// TestS098Units_R2_11_R2_12_ClearingSendsNothingUntilTheLast: one of two
// failed units clearing sends and closes nothing; the last clearing
// withdraws the shared notification's current id.
func TestS098Units_R2_11_R2_12_ClearingSendsNothingUntilTheLast(t *testing.T) {
	s := newS098(t)
	system := newS098Units("a.service", "b.service")
	s.startUnits(system, nil)
	sent := s.unitsSent()
	if len(sent) != 2 {
		t.Fatalf("units notifications = %+v, want 2", sent)
	}
	system.set("b.service")
	s.fire(system)
	if n := len(s.unitsSent()); n != 2 || len(s.out.closedIDs()) != 0 {
		t.Errorf("a.service clearing: %d sends, closed %v; want no send and nothing closed", n, s.out.closedIDs())
	}
	system.set()
	s.fire(system)
	if closed := s.out.closedIDs(); !slices.Equal(closed, []uint32{sent[1].ID}) {
		t.Errorf("closed %v after the last unit cleared, want [%d]", closed, sent[1].ID)
	}
	if n := len(s.unitsSent()); n != 2 {
		t.Errorf("units notifications after the last clear = %d, want still 2", n)
	}
	if a := s.alertLines(); len(a) != 0 {
		t.Errorf("tooltip alerts = %q, want none", a)
	}
	s.wantTransitions("units",
		[2]string{"ok", "warning"}, [2]string{"ok", "warning"},
		[2]string{"warning", "ok"}, [2]string{"warning", "ok"})
}

// TestS098Units_R2_8_R2_9_FailedCallKeepsConditionsAndWarnsOncePerStreak: a
// failing Failed call keeps that manager's conditions and logs one WARN per
// streak naming the manager; the other manager, and an absent one, do not
// stop units from being watched.
func TestS098Units_R2_8_R2_9_FailedCallKeepsConditionsAndWarnsOncePerStreak(t *testing.T) {
	s := newS098(t)
	system, user := newS098Units("a.service"), newS098Units()
	s.startUnits(system, user)
	first := s.unitsSent()
	if len(first) != 1 {
		t.Fatalf("units notifications = %+v, want 1", first)
	}
	cause := fmt.Errorf("calling ListUnitsFiltered: %w", errors.New("connection reset by peer"))
	system.setErr(cause)
	s.fire(system)
	s.fire(system)
	s.fire(system)
	if len(s.out.closedIDs()) != 0 {
		t.Errorf("a failed call withdrew %v; it is not an empty list", s.out.closedIDs())
	}
	if a := s.alertLines(); len(a) != 1 {
		t.Errorf("tooltip alerts while the system manager fails = %q, want a.service kept", a)
	}
	// "=system": an attribute naming the system manager (the message text
	// "system monitor" does not count).
	warns := func() int { return s.warns("monitor=units", "=system", "connection reset by peer") }
	if n := warns(); n != 1 {
		t.Fatalf("units WARN lines after 3 failed calls = %d, want 1\n%s", n, strings.Join(s.logs.lines(), "\n"))
	}

	user.set("pipewire.service")
	s.fire(user)
	if sent := s.unitsSent(); len(sent) != 2 || !strings.Contains(sent[1].Msg.Body, "pipewire.service") {
		t.Errorf("the user manager stopped being watched while the system one fails: %+v", sent)
	}

	system.set("a.service")
	s.fire(system)
	system.setErr(cause)
	s.fire(system)
	if n := warns(); n != 2 {
		t.Errorf("units WARN lines after fail, succeed, fail = %d, want 2", n)
	}
}

// TestS098Units_R2_8_AbsentSystemManagerLeavesTheUserOne: with no system
// manager the user manager's failures still notify.
func TestS098Units_R2_8_AbsentSystemManagerLeavesTheUserOne(t *testing.T) {
	s := newS098(t)
	user := newS098Units("pipewire.service")
	s.startUnits(nil, user)
	if sent := s.unitsSent(); len(sent) != 1 || !strings.Contains(sent[0].Msg.Body, "user") {
		t.Errorf("units notifications with only the user manager = %+v, want 1 naming the user manager", sent)
	}
}

// TestS098Units_HungManagerCostsOneCallTimeout: a manager that never answers
// costs one call deadline and a WARN; the loop then serves the next event.
func TestS098Units_HungManagerCostsOneCallTimeout(t *testing.T) {
	t.Cleanup(tray.SetCallTimeout(100 * time.Millisecond))
	s := newS098(t)
	system := newS098Units()
	s.startUnits(system, nil)
	system.setHang(true)
	s.fire(system)
	s.waitFor("the WARN of the timed-out call", func() bool { return s.warns("monitor=units") >= 1 })
	system.setHang(false)
	system.set("a.service")
	s.fire(system)
	if n := len(s.unitsSent()); n != 1 {
		t.Errorf("units notifications after the hung call = %d, want 1: the loop must keep serving", n)
	}
}

// TestS098Units_R5_1_PauseSilencesUnitWarnings: a unit failing while paused
// shows in the tooltip, sends nothing, and is not sent on resume.
func TestS098Units_R5_1_PauseSilencesUnitWarnings(t *testing.T) {
	s := newS098(t)
	system := newS098Units()
	s.startUnits(system, nil)
	s.pause()
	system.set("a.service")
	s.fire(system)
	if got := s.unitsSent(); len(got) != 0 {
		t.Errorf("a unit warning was sent while paused: %+v", got)
	}
	if a := s.alertLines(); len(a) != 1 {
		t.Errorf("tooltip alerts while paused = %q, want the unit line", a)
	}
	s.resume()
	s.fire(system)
	if got := s.unitsSent(); len(got) != 0 {
		t.Errorf("resuming sent the unit warning raised during the pause: %+v", got)
	}
}

// TestS098Units_LongUnitNameIsTruncated: a unit name is cut to 128 bytes
// before it reaches the notification.
func TestS098Units_LongUnitNameIsTruncated(t *testing.T) {
	long := strings.Repeat("x", 300) + ".service"
	s := newS098(t)
	system := newS098Units(long)
	s.startUnits(system, nil)
	sent := s.unitsSent()
	if len(sent) != 1 {
		t.Fatalf("units notifications = %+v, want 1", sent)
	}
	body := sent[0].Msg.Body
	if strings.Contains(body, strings.Repeat("x", 129)) {
		t.Errorf("units body carries more than 128 bytes of the unit name (%d bytes)", len(body))
	}
	if !strings.Contains(body, strings.Repeat("x", 100)) {
		t.Errorf("units body %q lost the unit name altogether", body)
	}
}
