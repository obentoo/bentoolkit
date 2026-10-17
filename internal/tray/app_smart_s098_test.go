package tray_test

// Story 098, sub-task 5.5: udisks2 drives are evaluated at start-up and on
// every Changed; a failing one is a critical condition keyed by its object
// path, which notifies while paused and reminds once per reminder interval.
//
// Pinned contract (design.md "internal/tray/sample.go and app.go",
// "internal/desktop/smartmon"): Deps gains Smart (Drives(ctx)
// ([]smartmon.Drive, error), Changed() <-chan struct{}); nil means absent.
// The notification key is "smart:<object path>".
//
// Hostile halves first: two drives reporting the same model and serial are
// still two drives (keyed by object path), a failed call is not an empty
// list, and a critical with no further signal still reminds; only then the
// plain raise and clear.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/obentoo/bentoolkit/internal/desktop/smartmon"
	"github.com/obentoo/bentoolkit/internal/tray"
)

var (
	_ tray.SmartMonitor = (*smartmon.Monitor)(nil)
	_ tray.SmartMonitor = (*s098Smart)(nil)
)

const (
	s098DriveA dbus.ObjectPath = "/org/freedesktop/UDisks2/drives/WDC_WD40EFRX_WD_A"
	s098DriveB dbus.ObjectPath = "/org/freedesktop/UDisks2/drives/WDC_WD40EFRX_WD_B"
	s098DriveN dbus.ObjectPath = "/org/freedesktop/UDisks2/drives/Samsung_SSD_980_S1"
)

// s098Smart is udisks2: Drives returns what the test sets, Changed is the
// signal the test fires. hang makes Drives wait for its context.
type s098Smart struct {
	mu       sync.Mutex
	drives   []smartmon.Drive
	err      error
	hang     bool
	calls    int
	deadline bool
	changed  chan struct{}
}

func newS098Smart(drives ...smartmon.Drive) *s098Smart {
	return &s098Smart{drives: drives, changed: make(chan struct{}, 1), deadline: true}
}

func (m *s098Smart) Drives(ctx context.Context) ([]smartmon.Drive, error) {
	m.mu.Lock()
	m.calls++
	if _, ok := ctx.Deadline(); !ok {
		m.deadline = false
	}
	hang, err, list := m.hang, m.err, slices.Clone(m.drives)
	m.mu.Unlock()
	if hang {
		<-ctx.Done()
		return nil, fmt.Errorf("calling GetManagedObjects: %w", ctx.Err())
	}
	if err != nil {
		return nil, err
	}
	return list, nil
}

func (m *s098Smart) Changed() <-chan struct{} { return m.changed }

func (m *s098Smart) set(drives ...smartmon.Drive) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.drives, m.err = drives, nil
}
func (m *s098Smart) setErr(err error) { m.mu.Lock(); defer m.mu.Unlock(); m.err = err }
func (m *s098Smart) setHang(h bool)   { m.mu.Lock(); defer m.mu.Unlock(); m.hang = h }
func (m *s098Smart) count() int       { m.mu.Lock(); defer m.mu.Unlock(); return m.calls }
func (m *s098Smart) hadDeadline() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.deadline
}

func s098Healthy(p dbus.ObjectPath, model, serial string) smartmon.Drive {
	return smartmon.Drive{Path: p, Model: model, Serial: serial}
}

func s098Failing(p dbus.ObjectPath, model, serial string) smartmon.Drive {
	return smartmon.Drive{Path: p, Model: model, Serial: serial, Failing: true}
}

// startSmart runs the App with the fake udisks2 and waits for its start-up
// evaluation.
func (s *s098Harness) startSmart(m *s098Smart) {
	s.t.Helper()
	s.start(nil, func(d *tray.Deps) { d.Smart = m })
	s.waitFor("the start-up SMART evaluation", func() bool { return m.count() >= 1 })
	s086WaitParked(s.appHarness)
}

// fireSmart signals Changed and waits until the loop has re-read the drives.
func (s *s098Harness) fireSmart(m *s098Smart) {
	s.t.Helper()
	n := m.count()
	m.changed <- struct{}{}
	s.waitFor("the SMART evaluation after Changed", func() bool { return m.count() > n })
	s086WaitParked(s.appHarness)
}

func (s *s098Harness) smartSent(p dbus.ObjectPath) []s097Sent {
	return s.out.matching(s098Keyed("smart:" + string(p)))
}

// TestS098Smart_R5_4_SameModelAndSerialOnTwoPathsIsTwoDrives: two drives
// whose bridges report one model and serial are two conditions under their
// own object paths; one recovering withdraws only its own notification.
func TestS098Smart_R5_4_SameModelAndSerialOnTwoPathsIsTwoDrives(t *testing.T) {
	s := newS098(t)
	m := newS098Smart(s098Failing(s098DriveA, "WDC WD40EFRX", "WD-123"), s098Failing(s098DriveB, "WDC WD40EFRX", "WD-123"))
	s.startSmart(m)
	a, b := s.smartSent(s098DriveA), s.smartSent(s098DriveB)
	if len(a) != 1 || len(b) != 1 {
		t.Fatalf("SMART notifications: A %+v, B %+v, want 1 each", a, b)
	}
	if b[0].Msg.ReplacesID == a[0].ID || a[0].Msg.ReplacesID == b[0].ID {
		t.Errorf("one drive's notification replaced the other's: A %+v, B %+v", a[0], b[0])
	}
	m.set(s098Healthy(s098DriveA, "WDC WD40EFRX", "WD-123"), s098Failing(s098DriveB, "WDC WD40EFRX", "WD-123"))
	s.fireSmart(m)
	if closed := s.out.closedIDs(); !slices.Equal(closed, []uint32{a[0].ID}) {
		t.Errorf("closed %v after drive A recovered, want only A's %d", closed, a[0].ID)
	}
	if keys := s.out.closedKeys(); !slices.Equal(keys, []string{"smart:" + string(s098DriveA)}) {
		t.Errorf("close keys = %q, want only drive A's", keys)
	}
}

// TestS098Smart_R4_2_R4_4_R4_7_FailingDriveAtStartupIsCritical: start-up
// evaluates every drive once; a failing one sends one critical notification
// naming its model, serial and "SMART reports failing"; a healthy one sends
// nothing.
func TestS098Smart_R4_2_R4_4_R4_7_FailingDriveAtStartupIsCritical(t *testing.T) {
	s := newS098(t)
	m := newS098Smart(s098Healthy(s098DriveA, "WDC WD40EFRX", "WD-AAA"), s098Failing(s098DriveB, "ST4000DM004", "ZFN0XYZ"))
	s.startSmart(m)
	if n := m.count(); n != 1 {
		t.Errorf("Drives called %d times at start-up, want once", n)
	}
	if !m.hadDeadline() {
		t.Error("Drives was called without a deadline; a udisks2 that never answers would stall the loop")
	}
	if got := s.smartSent(s098DriveA); len(got) != 0 {
		t.Errorf("the healthy drive notified: %+v", got)
	}
	sent := s.smartSent(s098DriveB)
	if len(sent) != 1 {
		t.Fatalf("SMART notifications for the failing drive = %+v, want 1", sent)
	}
	msg := sent[0].Msg
	if msg.Urgency != 2 || msg.ReplacesID != 0 || msg.Summary == "" {
		t.Errorf("SMART notification = %+v, want urgency 2 (critical) replacing nothing", msg)
	}
	for _, want := range []string{"ST4000DM004", "ZFN0XYZ", "SMART reports failing"} {
		if !strings.Contains(msg.Summary+" "+msg.Body, want) {
			t.Errorf("SMART notification %q / %q does not state %q", msg.Summary, msg.Body, want)
		}
	}
	if v := s.viewNow(); len(v.Alerts) != 1 || !v.AlertCritical {
		t.Errorf("view = %+v, want one critical alert line", v)
	}
	s.wantTransitions("smart", [2]string{"ok", "critical"})
	if l := s.transitions("smart")[0]; !strings.Contains(l, "target="+string(s098DriveB)) {
		t.Errorf("SMART transition line %q does not carry the drive's object path", l)
	}
}

// TestS098Smart_R4_3_R4_4_NVMeWarningsAreNamed: an NVMe critical warning list
// raises a critical naming every warning.
func TestS098Smart_R4_3_R4_4_NVMeWarningsAreNamed(t *testing.T) {
	s := newS098(t)
	m := newS098Smart(smartmon.Drive{Path: s098DriveN, Model: "Samsung SSD 980", Serial: "S64DNX0R", Warnings: []string{"spare", "temperature"}})
	s.startSmart(m)
	sent := s.smartSent(s098DriveN)
	if len(sent) != 1 || sent[0].Msg.Urgency != 2 {
		t.Fatalf("SMART notifications for the NVMe drive = %+v, want 1 critical", sent)
	}
	text := sent[0].Msg.Summary + " " + sent[0].Msg.Body
	for _, want := range []string{"Samsung SSD 980", "S64DNX0R", "spare", "temperature"} {
		if !strings.Contains(text, want) {
			t.Errorf("NVMe SMART notification %q does not state %q", text, want)
		}
	}
}

// TestS098Smart_R4_1_R4_5_ChangedRaisesAndClears: a drive turning failing,
// or added already failing, notifies on Changed; a drive turning healthy or
// removed withdraws its own notification and sends nothing.
func TestS098Smart_R4_1_R4_5_ChangedRaisesAndClears(t *testing.T) {
	s := newS098(t)
	m := newS098Smart(s098Healthy(s098DriveA, "WDC", "A1"))
	s.startSmart(m)
	m.set(s098Failing(s098DriveA, "WDC", "A1"))
	s.fireSmart(m)
	a := s.smartSent(s098DriveA)
	if len(a) != 1 {
		t.Fatalf("SMART notifications after drive A turned failing = %+v, want 1", a)
	}
	m.set(s098Failing(s098DriveA, "WDC", "A1"), s098Failing(s098DriveB, "WDC", "B1"))
	s.fireSmart(m)
	b := s.smartSent(s098DriveB)
	if len(b) != 1 {
		t.Fatalf("SMART notifications for the added failing drive B = %+v, want 1", b)
	}

	m.set(s098Healthy(s098DriveA, "WDC", "A1"), s098Failing(s098DriveB, "WDC", "B1"))
	s.fireSmart(m)
	m.set(s098Healthy(s098DriveA, "WDC", "A1"))
	s.fireSmart(m)
	if closed := s.out.closedIDs(); !slices.Equal(closed, []uint32{a[0].ID, b[0].ID}) {
		t.Errorf("closed %v, want A's %d (healthy) then B's %d (removed)", closed, a[0].ID, b[0].ID)
	}
	if len(s.smartSent(s098DriveA)) != 1 || len(s.smartSent(s098DriveB)) != 1 {
		t.Errorf("a clear sent a SMART notification: A %+v, B %+v", s.smartSent(s098DriveA), s.smartSent(s098DriveB))
	}
	if a := s.alertLines(); len(a) != 0 {
		t.Errorf("tooltip alerts = %q, want none", a)
	}
}

// TestS098Smart_R5_1_CriticalOverridesPauseAndRemindsWithoutASignal: a
// failing drive notifies while paused, and reminds once per reminder
// interval even when udisks2 sends no further signal.
func TestS098Smart_R5_1_CriticalOverridesPauseAndRemindsWithoutASignal(t *testing.T) {
	s := newS098(t)
	s.cfg = s097Config(t, "monitors:\n  reminder: 15m\n")
	m := newS098Smart(s098Healthy(s098DriveA, "WDC", "A1"))
	s.startSmart(m)
	s.pause()
	m.set(s098Failing(s098DriveA, "WDC", "A1"))
	s.fireSmart(m)
	first := s.smartSent(s098DriveA)
	if len(first) != 1 || first[0].Msg.Urgency != 2 {
		t.Fatalf("SMART notifications while paused = %+v, want 1 critical", first)
	}
	s.tickUntil("a SMART reminder", 35, func() bool { return len(s.smartSent(s098DriveA)) >= 2 })
	if r := s.smartSent(s098DriveA)[1]; r.Msg.ReplacesID != first[0].ID || r.Msg.Urgency != 2 {
		t.Errorf("reminder = %+v, want urgency 2 replacing %d", r.Msg, first[0].ID)
	}
	if elapsed := s.clock.Now().Sub(appStart); elapsed < 15*time.Minute {
		t.Errorf("the reminder came %v after start, before the 15 min interval", elapsed)
	}
}

// TestS098Smart_FailedCallKeepsConditionsAndWarnsOncePerStreak: a failing
// Drives call keeps every SMART condition and logs one WARN per streak.
func TestS098Smart_FailedCallKeepsConditionsAndWarnsOncePerStreak(t *testing.T) {
	s := newS098(t)
	m := newS098Smart(s098Failing(s098DriveA, "WDC", "A1"))
	s.startSmart(m)
	if len(s.smartSent(s098DriveA)) != 1 {
		t.Fatalf("SMART notifications = %+v, want 1", s.smartSent(s098DriveA))
	}
	cause := fmt.Errorf("calling GetManagedObjects: %w", errors.New("name has no owner"))
	m.setErr(cause)
	s.fireSmart(m)
	s.fireSmart(m)
	s.fireSmart(m)
	if len(s.out.closedIDs()) != 0 {
		t.Errorf("a failed call withdrew %v; it is not an empty list", s.out.closedIDs())
	}
	warns := func() int { return s.warns("monitor=smart", "name has no owner") }
	if n := warns(); n != 1 {
		t.Fatalf("SMART WARN lines after 3 failed calls = %d, want 1\n%s", n, strings.Join(s.logs.lines(), "\n"))
	}
	m.set(s098Failing(s098DriveA, "WDC", "A1"))
	s.fireSmart(m)
	m.setErr(cause)
	s.fireSmart(m)
	if n := warns(); n != 2 {
		t.Errorf("SMART WARN lines after fail, succeed, fail = %d, want 2", n)
	}
}

// TestS098Smart_HungUDisksCostsOneCallTimeout: a udisks2 that never answers
// costs one call deadline and a WARN; the loop then serves the next event.
func TestS098Smart_HungUDisksCostsOneCallTimeout(t *testing.T) {
	t.Cleanup(tray.SetCallTimeout(100 * time.Millisecond))
	s := newS098(t)
	m := newS098Smart()
	s.startSmart(m)
	m.setHang(true)
	s.fireSmart(m)
	s.waitFor("the WARN of the timed-out call", func() bool { return s.warns("monitor=smart") >= 1 })
	m.setHang(false)
	m.set(s098Failing(s098DriveA, "WDC", "A1"))
	s.fireSmart(m)
	if n := len(s.smartSent(s098DriveA)); n != 1 {
		t.Errorf("SMART notifications after the hung call = %d, want 1: the loop must keep serving", n)
	}
}

// TestS098Smart_LongModelIsTruncated: a drive model is cut to 128 bytes
// before it reaches the notification.
func TestS098Smart_LongModelIsTruncated(t *testing.T) {
	long := strings.Repeat("M", 300)
	s := newS098(t)
	m := newS098Smart(s098Failing(s098DriveA, long, "SER1"))
	s.startSmart(m)
	sent := s.smartSent(s098DriveA)
	if len(sent) != 1 {
		t.Fatalf("SMART notifications = %+v, want 1", sent)
	}
	text := sent[0].Msg.Summary + " " + sent[0].Msg.Body
	if strings.Contains(text, strings.Repeat("M", 129)) {
		t.Errorf("SMART notification carries more than 128 bytes of the model")
	}
	if !strings.Contains(text, strings.Repeat("M", 100)) || !strings.Contains(text, "SER1") {
		t.Errorf("SMART notification %q lost the model or the serial", text)
	}
}
