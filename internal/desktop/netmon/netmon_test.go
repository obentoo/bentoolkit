package netmon_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"

	"github.com/obentoo/bentoolkit/internal/desktop/dbustest"
	"github.com/obentoo/bentoolkit/internal/desktop/netmon"
)

const (
	nmName  = "org.freedesktop.NetworkManager"
	nmPath  = dbus.ObjectPath("/org/freedesktop/NetworkManager")
	nmIface = "org.freedesktop.NetworkManager"
)

// fakeNM exports NetworkManager's Connectivity, Metered and State properties
// on the test's "system" bus (a second private dbus-daemon).
type fakeNM struct {
	conn  *dbus.Conn
	props *prop.Properties
}

func startNM(t *testing.T, addr string, connectivity, metered uint32) *fakeNM {
	t.Helper()
	conn, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	props, err := prop.Export(conn, nmPath, prop.Map{nmIface: {
		"Connectivity": {Value: connectivity, Emit: prop.EmitTrue},
		"Metered":      {Value: metered, Emit: prop.EmitTrue},
		"State":        {Value: uint32(70), Emit: prop.EmitTrue},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if reply, err := conn.RequestName(nmName, dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("fake NetworkManager RequestName: %v %v", reply, err)
	}
	return &fakeNM{conn: conn, props: props}
}

func (f *fakeNM) set(t *testing.T, name string, v uint32) {
	t.Helper()
	f.props.SetMust(nmIface, name, v)
}

func newMonitor(t *testing.T, addr string) *netmon.Monitor {
	t.Helper()
	conn, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return netmon.New(conn, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// TestAllowed_ConnectivityDecidesOnline is R3.2, with the hostile case first:
// 0 (unknown) means connectivity checking is disabled, which is common on
// Gentoo, and must read as online or the tray never fetches.
func TestAllowed_ConnectivityDecidesOnline(t *testing.T) {
	cases := []struct {
		connectivity uint32
		online       bool
	}{
		{0, true},  // unknown: checking disabled
		{1, false}, // none
		{2, false}, // portal
		{3, false}, // limited
		{4, true},  // full
	}
	for _, c := range cases {
		addr := dbustest.Session(t)
		startNM(t, addr, c.connectivity, 2)
		v, err := newMonitor(t, addr).Allowed(context.Background())
		if err != nil {
			t.Fatalf("Allowed with Connectivity=%d: %v", c.connectivity, err)
		}
		if v.Online != c.online || v.Metered || v.NMAbsent {
			t.Errorf("Connectivity=%d: Verdict = %+v, want Online=%v, not metered, NM present", c.connectivity, v, c.online)
		}
	}
}

// TestAllowed_MeteredYesAndGuessYes is R3.1: 1 (yes) and 3 (guess-yes) are
// metered; 0 (unknown), 2 (no) and 4 (guess-no) are not.
func TestAllowed_MeteredYesAndGuessYes(t *testing.T) {
	for metered, want := range map[uint32]bool{0: false, 1: true, 2: false, 3: true, 4: false} {
		addr := dbustest.Session(t)
		startNM(t, addr, 4, metered)
		v, err := newMonitor(t, addr).Allowed(context.Background())
		if err != nil {
			t.Fatalf("Allowed with Metered=%d: %v", metered, err)
		}
		if v.Metered != want || !v.Online {
			t.Errorf("Metered=%d: Verdict = %+v, want Metered=%v and online", metered, v, want)
		}
	}
}

// TestAllowed_AbsentNetworkManager is R3.4: no owner is a verdict, not an
// error.
func TestAllowed_AbsentNetworkManager(t *testing.T) {
	addr := dbustest.Session(t)
	v, err := newMonitor(t, addr).Allowed(context.Background())
	if err != nil {
		t.Fatalf("Allowed without NetworkManager: %v", err)
	}
	if !v.NMAbsent {
		t.Errorf("Verdict = %+v, want NMAbsent", v)
	}
}

// TestAllowed_AbsentMonitorIsAlwaysAbsent: netmon.Absent() stands in when
// there is no system bus at all (R3.4): always NMAbsent, never a change.
func TestAllowed_AbsentMonitorIsAlwaysAbsent(t *testing.T) {
	m := netmon.Absent()
	for i := 0; i < 2; i++ {
		v, err := m.Allowed(context.Background())
		if err != nil || !v.NMAbsent {
			t.Fatalf("Absent().Allowed = %+v, %v; want NMAbsent and no error", v, err)
		}
	}
	if fired(m.Changed(), 300*time.Millisecond) {
		t.Error("Absent().Changed fired")
	}
}

func fired(ch <-chan struct{}, d time.Duration) bool {
	select {
	case <-ch:
		return true
	case <-time.After(d):
		return false
	}
}

// TestChanged_IgnoresUnrelatedChanges is the hostile half of R3.3, authored
// first: a property change that is neither Connectivity nor Metered, or a
// Connectivity change on another object, must not trigger a fetch.
func TestChanged_IgnoresUnrelatedChanges(t *testing.T) {
	addr := dbustest.Session(t)
	nm := startNM(t, addr, 4, 2)
	m := newMonitor(t, addr)
	ch := m.Changed()
	if _, err := m.Allowed(context.Background()); err != nil {
		t.Fatal(err)
	}

	nm.set(t, "State", 60)
	if err := nm.conn.Emit("/org/freedesktop/NetworkManager/Devices/1",
		"org.freedesktop.DBus.Properties.PropertiesChanged",
		nmIface, map[string]dbus.Variant{"Connectivity": dbus.MakeVariant(uint32(1))}, []string{}); err != nil {
		t.Fatal(err)
	}
	if fired(ch, 500*time.Millisecond) {
		t.Fatal("Changed fired for an unrelated property or object")
	}
	// Liveness: the monitor still reacts to a relevant change.
	nm.set(t, "Connectivity", 1)
	if !fired(ch, 3*time.Second) {
		t.Fatal("Changed did not fire for a Connectivity change")
	}
}

// TestChanged_FiresForConnectivityAndMetered is R3.3's trigger for both
// properties.
func TestChanged_FiresForConnectivityAndMetered(t *testing.T) {
	for _, property := range []string{"Connectivity", "Metered"} {
		t.Run(property, func(t *testing.T) {
			addr := dbustest.Session(t)
			nm := startNM(t, addr, 1, 2)
			m := newMonitor(t, addr)
			ch := m.Changed()
			if _, err := m.Allowed(context.Background()); err != nil {
				t.Fatal(err)
			}
			nm.set(t, property, 3)
			if !fired(ch, 3*time.Second) {
				t.Fatalf("Changed did not fire within 3s of a %s change", property)
			}
		})
	}
}
