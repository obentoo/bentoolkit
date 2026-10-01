package dbusx_test

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/obentoo/bentoolkit/internal/desktop/dbustest"
	"github.com/obentoo/bentoolkit/internal/desktop/dbusx"
)

const trayName = "org.obentoo.BentooTray"

func sessionConn(t *testing.T, addr string) *dbus.Conn {
	t.Helper()
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", addr)
	conn, err := dbusx.SessionBus(context.Background())
	if err != nil {
		t.Fatalf("SessionBus(%s): %v", addr, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func nameOwner(t *testing.T, conn *dbus.Conn, name string) string {
	t.Helper()
	var has bool
	if err := conn.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, name).Store(&has); err != nil {
		t.Fatal(err)
	}
	if !has {
		return ""
	}
	var owner string
	if err := conn.BusObject().Call("org.freedesktop.DBus.GetNameOwner", 0, name).Store(&owner); err != nil {
		t.Fatal(err)
	}
	return owner
}

// TestSessionBus_ConnectionsArePrivate is the hostile half of the shared-bus
// GOTCHA: two calls must be two connections, and closing one must not tear
// down the other (dbus.SessionBus() would return the same shared connection).
func TestSessionBus_ConnectionsArePrivate(t *testing.T) {
	addr := dbustest.Session(t)
	a := sessionConn(t, addr)
	b := sessionConn(t, addr)
	if a == b || a.Names()[0] == b.Names()[0] {
		t.Fatalf("two SessionBus calls share one connection (%s)", a.Names()[0])
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if err := b.BusObject().Call("org.freedesktop.DBus.GetId", 0).Err; err != nil {
		t.Errorf("closing one connection broke the other: %v", err)
	}
}

// TestSessionBus_UnreachableBusNamesTheAddress is R1.3.
func TestSessionBus_UnreachableBusNamesTheAddress(t *testing.T) {
	const bad = "unix:path=/nonexistent/bentoo-tray-test-bus"
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", bad)
	conn, err := dbusx.SessionBus(context.Background())
	if err == nil {
		_ = conn.Close()
		t.Fatal("SessionBus connected to a bus that does not exist")
	}
	if !strings.Contains(err.Error(), "/nonexistent/bentoo-tray-test-bus") {
		t.Errorf("error %q does not name the address it tried", err)
	}
}

// TestSystemBus_ConnectsToTheConfiguredAddress: the system connection honours
// DBUS_SYSTEM_BUS_ADDRESS, so the NetworkManager adapter can be tested.
func TestSystemBus_ConnectsToTheConfiguredAddress(t *testing.T) {
	addr := dbustest.Session(t)
	t.Setenv("DBUS_SYSTEM_BUS_ADDRESS", addr)
	conn, err := dbusx.SystemBus(context.Background())
	if err != nil {
		t.Fatalf("SystemBus: %v", err)
	}
	defer conn.Close()
	if len(conn.Names()) == 0 {
		t.Error("the system connection has no unique name")
	}
}

// TestOwner_SecondInstanceIsNotAcquiredAndDoesNotQueue is R1.2 and the
// DoNotQueue flag: the second owner is told no, the first keeps the name, and
// when the first releases, the second does NOT silently inherit it.
func TestOwner_SecondInstanceIsNotAcquiredAndDoesNotQueue(t *testing.T) {
	addr := dbustest.Session(t)
	c1, c2, observer := sessionConn(t, addr), sessionConn(t, addr), sessionConn(t, addr)
	ctx := context.Background()

	first := dbusx.NewOwner(c1)
	ok, err := first.Acquire(ctx)
	if err != nil || !ok {
		t.Fatalf("first Acquire = %v, %v; want true", ok, err)
	}
	if got := nameOwner(t, observer, trayName); got != c1.Names()[0] {
		t.Fatalf("%s owner = %q, want the first connection %q", trayName, got, c1.Names()[0])
	}

	second := dbusx.NewOwner(c2)
	ok, err = second.Acquire(ctx)
	if err != nil {
		t.Fatalf("second Acquire returned an error; an instance already running is not a failure: %v", err)
	}
	if ok {
		t.Fatal("second Acquire = true while another process owns the name")
	}
	if got := nameOwner(t, observer, trayName); got != c1.Names()[0] {
		t.Errorf("after a second Acquire the owner is %q, want the first connection", got)
	}

	if err := first.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if got := nameOwner(t, observer, trayName); got != "" {
		t.Errorf("after the first owner released, %q owns the name: the second request was queued", got)
	}
}

// startDaemon runs a dedicated hermetic dbus-daemon whose process the test can
// kill; like every D-Bus test here it fails instead of skipping in CI.
func startDaemon(t *testing.T) (string, *exec.Cmd) {
	t.Helper()
	return dbustest.StartKillable(t)
}

// TestOwner_LostFiresOnlyWhenTheBusGoesAway is R1.5: Lost stays open while the
// bus lives (and across unrelated name traffic), and closes when it dies.
func TestOwner_LostFiresOnlyWhenTheBusGoesAway(t *testing.T) {
	addr, daemon := startDaemon(t)
	conn := sessionConn(t, addr)
	owner := dbusx.NewOwner(conn)
	if ok, err := owner.Acquire(context.Background()); err != nil || !ok {
		t.Fatalf("Acquire = %v, %v", ok, err)
	}
	// Unrelated name traffic on the same connection (NameAcquired/NameLost for
	// another name) is not a lost bus.
	if _, err := conn.RequestName("org.obentoo.Test.Other", dbus.NameFlagDoNotQueue); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ReleaseName("org.obentoo.Test.Other"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-owner.Lost():
		t.Fatal("Lost fired while the bus is alive")
	case <-time.After(300 * time.Millisecond):
	}

	if err := daemon.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-owner.Lost():
	case <-time.After(5 * time.Second):
		t.Fatal("Lost did not fire within 5s of the bus dying")
	}
}
