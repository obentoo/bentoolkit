package unitmon_test

import (
	"log/slog"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/desktop/dbustest"
	"github.com/obentoo/bentoolkit/internal/desktop/unitmon"
)

// closeWithin bounds how long a lost bus may take to close Changed: godbus
// notices the dead socket on its next read, which is fast, but these tests
// run under -race -shuffle=on next to other D-Bus packages.
const closeWithin = 5 * time.Second

// s098CloseObserved drains ch until it is closed or d elapses, and reports
// whether it was closed. A pending change ahead of the close is fine.
func s098CloseObserved(ch <-chan struct{}, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return true
			}
		case <-timer.C:
			return false
		}
	}
}

// s098KillBus kills a bus started by dbustest.StartKillable and reaps it,
// which is what losing the system bus looks like to a client.
func s098KillBus(t *testing.T, kill func() error, wait func() error) {
	t.Helper()
	if err := kill(); err != nil {
		t.Fatalf("killing the bus: %v", err)
	}
	_ = wait() // a killed process always reports an error here
}

// TestS098UnitmonChangedClosesWhenTheBusIsLost is R2.9's monitor half: the
// tray's loop learns the system bus is gone only from Changed closing, so a
// watching Monitor must close it when its connection dies.
func TestS098UnitmonChangedClosesWhenTheBusIsLost(t *testing.T) {
	addr, cmd := dbustest.StartKillable(t)
	sd := startSystemd(t, addr, s098Unit("sshd.service", "active"))
	logs := &s098Logs{}
	m, _ := s098New(t, addr, logs)
	if !sd.subscribed() {
		t.Fatal("New returned without Manager.Subscribe; the monitor is not watching")
	}
	if warns := logs.atOrAbove(slog.LevelWarn); len(warns) != 0 {
		t.Fatalf("New logged %+v; want a watching monitor", warns)
	}
	if s098CloseObserved(m.Changed(), 100*time.Millisecond) {
		t.Fatal("Changed closed while the bus was still up")
	}

	s098KillBus(t, cmd.Process.Kill, cmd.Wait)

	if !s098CloseObserved(m.Changed(), closeWithin) {
		t.Fatalf("Changed was not closed within %s of losing the bus", closeWithin)
	}
}

// TestS098UnitmonChangedStaysOpenWithoutAReader: only a running reader may
// close Changed. Absent and a monitor whose setup failed never close it, even
// when the bus that failed setup goes away afterwards, so the caller does not
// mistake "never watched" for "bus lost".
func TestS098UnitmonChangedStaysOpenWithoutAReader(t *testing.T) {
	if s098CloseObserved(unitmon.Absent().Changed(), quietFor) {
		t.Error("Absent().Changed() was closed")
	}

	addr, cmd := dbustest.StartKillable(t)
	sd := startSystemd(t, addr, s098Unit("sshd.service", "active"))
	sd.refuse(&sd.subscribeErr)
	logs := &s098Logs{}
	m, _ := s098New(t, addr, logs)
	if warns := logs.atOrAbove(slog.LevelWarn); len(warns) != 1 {
		t.Fatalf("got %d WARN-or-above records, want the one failed-setup WARN: %+v", len(warns), warns)
	}

	s098KillBus(t, cmd.Process.Kill, cmd.Wait)

	if s098CloseObserved(m.Changed(), quietFor) {
		t.Error("Changed of a monitor whose setup failed was closed")
	}
}
