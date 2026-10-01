// Package netmon reports whether NetworkManager considers the machine online
// and the connection metered, and when either of those changes (S072-R3).
// Policy (skip, fetch, log) is the caller's: this package only reports.
package netmon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/godbus/dbus/v5"
)

// NetworkManager's well-known name, manager object and interface.
const (
	nmName  = "org.freedesktop.NetworkManager"
	nmPath  = dbus.ObjectPath("/org/freedesktop/NetworkManager")
	nmIface = "org.freedesktop.NetworkManager"
)

// The standard properties interface NetworkManager reports changes on.
const (
	propsIface         = "org.freedesktop.DBus.Properties"
	memberPropsChanged = "PropertiesChanged"
	signalPropsChanged = propsIface + "." + memberPropsChanged
)

// The manager properties a Verdict is made of.
const (
	propConnectivity = "Connectivity"
	propMetered      = "Metered"
)

// NMConnectivityState values.
const (
	connectivityUnknown = 0 // checking is disabled: treated as online
	connectivityFull    = 4
)

// NMMetered values that count as metered.
const (
	meteredYes      = 1
	meteredGuessYes = 3
)

// signalBuffer is the capacity of the channel handed to conn.Signal. godbus
// parks each signal that finds the channel full in a goroutine of its own, so
// the channel is buffered and its single reader never blocks.
const signalBuffer = 64

// Verdict is what NetworkManager says about the network. When NMAbsent is
// true NetworkManager is not on the bus and Online and Metered carry no
// information.
type Verdict struct {
	Online, Metered, NMAbsent bool
}

// Monitor reads NetworkManager's verdict on one system bus connection and
// reports its relevant changes. It is safe for concurrent use.
type Monitor struct {
	conn    *dbus.Conn // nil for Absent
	log     *slog.Logger
	changed chan struct{}
}

// Absent returns a Monitor for a process with no system bus: Allowed always
// reports NMAbsent and Changed never fires (R3.4).
func Absent() *Monitor {
	return &Monitor{log: slog.New(slog.DiscardHandler), changed: make(chan struct{}, 1)}
}

// New subscribes to NetworkManager's property changes on sys, a private
// system bus connection the caller keeps open and closes. NetworkManager need
// not be running: the match rule names its well-known name, which the bus
// resolves when a signal is sent, so a NetworkManager that starts later is
// heard. A failed subscription is logged at WARN and leaves Changed silent;
// Allowed still works. A nil log discards.
func New(sys *dbus.Conn, log *slog.Logger) *Monitor {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	m := &Monitor{conn: sys, log: log, changed: make(chan struct{}, 1)}
	if err := sys.AddMatchSignal(
		dbus.WithMatchSender(nmName),
		dbus.WithMatchObjectPath(nmPath),
		dbus.WithMatchInterface(propsIface),
		dbus.WithMatchMember(memberPropsChanged),
		dbus.WithMatchArg(0, nmIface),
	); err != nil {
		log.Warn("network change notifications unavailable: subscribing to NetworkManager property changes failed",
			"name", nmName, "path", string(nmPath), "error", err)
		return m
	}
	signals := make(chan *dbus.Signal, signalBuffer)
	sys.Signal(signals)
	go m.read(signals)
	return m
}

// Changed fires after NetworkManager reports a change to Connectivity or
// Metered (R3.3). Changes that arrive before the receiver reads coalesce into
// one. The channel is never closed.
func (m *Monitor) Changed() <-chan struct{} {
	return m.changed
}

// Allowed reads NetworkManager's Connectivity and Metered properties.
// Connectivity none, portal or limited is offline; unknown (checking disabled)
// and full are online (R3.2). Metered yes or guess-yes is metered (R3.1).
// NetworkManager not on the bus is a Verdict with NMAbsent, not an error
// (R3.4).
func (m *Monitor) Allowed(ctx context.Context) (Verdict, error) {
	if m.conn == nil {
		return Verdict{NMAbsent: true}, nil
	}
	var owned bool
	if err := m.conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.NameHasOwner", 0, nmName).Store(&owned); err != nil {
		return Verdict{}, fmt.Errorf("asking the system bus whether %s is running: %w", nmName, err)
	}
	if !owned {
		return Verdict{NMAbsent: true}, nil
	}

	connectivity, err := m.property(ctx, propConnectivity)
	if err != nil {
		return absentOr(err)
	}
	metered, err := m.property(ctx, propMetered)
	if err != nil {
		return absentOr(err)
	}
	return Verdict{
		Online:  connectivity == connectivityUnknown || connectivity == connectivityFull,
		Metered: metered == meteredYes || metered == meteredGuessYes,
	}, nil
}

// property reads one uint32 property of NetworkManager's manager object.
func (m *Monitor) property(ctx context.Context, name string) (uint32, error) {
	var v dbus.Variant
	err := m.conn.Object(nmName, nmPath).CallWithContext(ctx, propsIface+".Get", 0, nmIface, name).Store(&v)
	if err != nil {
		return 0, fmt.Errorf("reading NetworkManager property %s: %w", name, err)
	}
	u, ok := v.Value().(uint32)
	if !ok {
		return 0, fmt.Errorf("reading NetworkManager property %s: got type %s, want uint32", name, v.Signature())
	}
	return u, nil
}

// absentOr maps a NetworkManager that left the bus between NameHasOwner and
// the property read to NMAbsent; any other error is returned.
func absentOr(err error) (Verdict, error) {
	var dbusErr dbus.Error
	if errors.As(err, &dbusErr) {
		switch dbusErr.Name {
		case "org.freedesktop.DBus.Error.ServiceUnknown", "org.freedesktop.DBus.Error.NameHasNoOwner":
			return Verdict{NMAbsent: true}, nil
		}
	}
	return Verdict{}, err
}

// read is the single reader of the connection's signal channel. It exits when
// godbus closes the channel or the connection's context ends (on a connection
// already closed, conn.Signal registers nothing and the channel never closes).
func (m *Monitor) read(signals <-chan *dbus.Signal) {
	done := m.conn.Context().Done()
	for {
		select {
		case sig, ok := <-signals:
			if !ok {
				return
			}
			if !relevant(sig) {
				continue
			}
			m.log.Debug("NetworkManager connectivity or metered state changed")
			select {
			case m.changed <- struct{}{}:
			default: // a change is already pending; this one coalesces into it
			}
		case <-done:
			return
		}
	}
}

// relevant reports whether sig is a PropertiesChanged of NetworkManager's
// manager object that changes or invalidates Connectivity or Metered. godbus
// delivers every signal the connection receives to every channel, so anything
// else, including a malformed body, is ignored.
func relevant(sig *dbus.Signal) bool {
	if sig == nil || sig.Path != nmPath || sig.Name != signalPropsChanged || len(sig.Body) < 2 {
		return false
	}
	if iface, ok := sig.Body[0].(string); !ok || iface != nmIface {
		return false
	}
	changed, ok := sig.Body[1].(map[string]dbus.Variant)
	if !ok {
		return false
	}
	for _, name := range []string{propConnectivity, propMetered} {
		if _, ok := changed[name]; ok {
			return true
		}
	}
	if len(sig.Body) >= 3 {
		if invalidated, ok := sig.Body[2].([]string); ok {
			if slices.Contains(invalidated, propConnectivity) || slices.Contains(invalidated, propMetered) {
				return true
			}
		}
	}
	return false
}
