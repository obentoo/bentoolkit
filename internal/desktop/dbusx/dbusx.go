// Package dbusx opens bentoo-tray's private D-Bus connections and owns its
// well-known bus name (S072-R1).
package dbusx

import (
	"context"
	"fmt"
	"os"

	"github.com/godbus/dbus/v5"
)

// BusName is the well-known name a running bentoo-tray owns (R1.1).
const BusName = "org.obentoo.BentooTray"

// defaultSystemBusAddress is the system bus address the D-Bus specification
// fixes for when DBUS_SYSTEM_BUS_ADDRESS is unset.
const defaultSystemBusAddress = "unix:path=/var/run/dbus/system_bus_socket"

// signalBuffer is the capacity of the channel the Owner hands to conn.Signal.
// godbus fans every signal out to every registered channel and, when one is
// full, parks each further signal in a goroutine of its own; a buffer and a
// reader that never blocks keep that from piling up.
const signalBuffer = 64

// SessionBus opens a private connection to the session bus: never the
// process-wide shared one dbus.SessionBus() returns, so closing it cannot tear
// down a connection some other code holds.
//
// The address is DBUS_SESSION_BUS_ADDRESS or, when that is unset, the
// standard $XDG_RUNTIME_DIR/bus socket. Unlike dbus.ConnectSessionBus this
// never autolaunches a bus (a tray on a bus of its own would be invisible) and
// never rewrites the process environment. Every error names the address it
// tried (R1.3).
//
// ctx bounds only the connect (dial, auth, Hello); once SessionBus returns,
// the connection lives until it is closed or the bus goes away.
func SessionBus(ctx context.Context) (*dbus.Conn, error) {
	addr, err := sessionBusAddress()
	if err != nil {
		return nil, err
	}
	return connect(ctx, "session", addr)
}

// SystemBus opens a private connection to the system bus at
// DBUS_SYSTEM_BUS_ADDRESS, or at the standard socket when that is unset.
// Errors name the address tried; ctx bounds only the connect, as in
// SessionBus.
func SystemBus(ctx context.Context) (*dbus.Conn, error) {
	addr := os.Getenv("DBUS_SYSTEM_BUS_ADDRESS")
	if addr == "" {
		addr = defaultSystemBusAddress
	}
	return connect(ctx, "system", addr)
}

func sessionBusAddress() (string, error) {
	if addr := os.Getenv("DBUS_SESSION_BUS_ADDRESS"); addr != "" && addr != "autolaunch:" {
		return addr, nil
	}
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return "unix:path=" + dbus.EscapeBusAddressValue(dir+"/bus"), nil
	}
	return "", fmt.Errorf("connecting to the session bus: DBUS_SESSION_BUS_ADDRESS and XDG_RUNTIME_DIR are both unset")
}

// connect dials addr with ctx bounding the connect phase only. godbus closes a
// connection when the context given to dbus.WithContext ends, so the
// connection gets a context of its own (ctx's values, not its cancellation),
// cancelled by ctx only until connect returns: a caller that cancels ctx
// later (a run context at shutdown) does not tear the connection down under
// itself.
func connect(ctx context.Context, kind, addr string) (*dbus.Conn, error) {
	connCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stop := context.AfterFunc(ctx, cancel)
	conn, err := dbus.Connect(addr, dbus.WithContext(connCtx))
	if !stop() || err != nil {
		// ctx ended during the connect, or the connect failed.
		cancel()
		if conn != nil {
			_ = conn.Close()
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			if err == nil {
				return nil, fmt.Errorf("connecting to the %s bus at %s: %w", kind, addr, ctxErr)
			}
			return nil, fmt.Errorf("connecting to the %s bus at %s: %w: %w", kind, addr, ctxErr, err)
		}
		return nil, fmt.Errorf("connecting to the %s bus at %s: %w", kind, addr, err)
	}
	// Release connCtx with the connection it scopes.
	context.AfterFunc(conn.Context(), cancel) //nolint:contextcheck // deliberately the connection's own context, not ctx: connCtx must end with the connection.
	return conn, nil
}

// Owner claims BusName on one connection and reports when that connection is
// lost. It is safe for concurrent use.
type Owner struct {
	conn *dbus.Conn
	lost chan struct{}
}

// NewOwner watches conn for loss from the moment it is called. conn must be a
// private connection the caller closes when done.
func NewOwner(conn *dbus.Conn) *Owner {
	o := &Owner{conn: conn, lost: make(chan struct{})}
	signals := make(chan *dbus.Signal, signalBuffer)
	conn.Signal(signals)
	go o.watch(signals)
	return o
}

// watch is the single reader of the connection's signal channel. godbus
// closes the channel when the connection closes, whether the bus went away or
// the caller closed it, and that close is what Lost reports. The connection's
// context is watched too: on a connection already closed when NewOwner ran,
// conn.Signal registers nothing and the channel never closes, but Close has
// always cancelled the context.
func (o *Owner) watch(signals <-chan *dbus.Signal) {
	defer close(o.lost)
	done := o.conn.Context().Done()
	for {
		// No signal is of interest here, only the end of the connection.
		select {
		case _, ok := <-signals:
			if !ok {
				return
			}
		case <-done:
			return
		}
	}
}

// Acquire requests BusName without queueing (R1.1, R1.2). It returns true
// when this connection is the primary owner, including when it already was.
// Another owner is not an
// error: Acquire returns false, and because the request is not queued, this
// connection will not inherit the name when that owner leaves.
func (o *Owner) Acquire(ctx context.Context) (bool, error) {
	var reply uint32
	call := o.conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.RequestName", 0, BusName, uint32(dbus.NameFlagDoNotQueue))
	if err := call.Store(&reply); err != nil {
		return false, fmt.Errorf("requesting bus name %s: %w", BusName, err)
	}
	switch dbus.RequestNameReply(reply) {
	case dbus.RequestNameReplyPrimaryOwner, dbus.RequestNameReplyAlreadyOwner:
		return true, nil
	default:
		return false, nil
	}
}

// Release gives BusName back to the bus. Releasing a name this connection
// does not own is an error.
func (o *Owner) Release() error {
	reply, err := o.conn.ReleaseName(BusName)
	if err != nil {
		return fmt.Errorf("releasing bus name %s: %w", BusName, err)
	}
	if reply != dbus.ReleaseNameReplyReleased {
		return fmt.Errorf("releasing bus name %s: the bus replied %d, not released", BusName, reply)
	}
	return nil
}

// Lost is closed when the connection is gone: the bus died, or the connection
// was closed (R1.5).
func (o *Owner) Lost() <-chan struct{} {
	return o.lost
}
