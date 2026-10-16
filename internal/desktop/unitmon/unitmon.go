package unitmon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

// systemd's well-known name, manager object and interfaces. The system and
// the user manager carry the same name, each on its own bus.
const (
	sdName         = "org.freedesktop.systemd1"
	sdPath         = dbus.ObjectPath("/org/freedesktop/systemd1")
	sdUnitPrefix   = "/org/freedesktop/systemd1/unit"
	sdManagerIface = "org.freedesktop.systemd1.Manager"
	sdUnitIface    = "org.freedesktop.systemd1.Unit"
)

// The manager members this package calls or listens to.
const (
	memberSubscribe         = "Subscribe"
	memberListUnitsFiltered = "ListUnitsFiltered"
	memberUnitRemoved       = "UnitRemoved"
	signalUnitRemoved       = sdManagerIface + "." + memberUnitRemoved
)

// The standard properties interface systemd reports unit changes on.
const (
	propsIface         = "org.freedesktop.DBus.Properties"
	memberPropsChanged = "PropertiesChanged"
	signalPropsChanged = propsIface + "." + memberPropsChanged
)

// propActiveState is the one unit property whose change matters.
const propActiveState = "ActiveState"

// stateFailed is the ActiveState Failed asks the manager to filter on.
const stateFailed = "failed"

// signalBuffer is the capacity of the channel handed to conn.Signal. godbus
// parks each signal that finds the channel full in a goroutine of its own, so
// the channel is buffered and its single reader never blocks.
const signalBuffer = 64

// setupTimeout bounds the bus calls New makes (match rules and Subscribe), so
// a manager that never answers cannot stall the caller. A variable only so
// tests can shorten it.
var setupTimeout = 10 * time.Second

// cleanupTimeout bounds removing the match rules after a failed setup. The
// removal runs under a context of its own: when Subscribe timed out the setup
// context is already done, and godbus does not even send a call on a done
// context, so reusing it would leave the rules on the bus.
const cleanupTimeout = 2 * time.Second

// managerCallFlags is set on every call to the manager. Without
// FlagNoAutoStart a call to an absent manager makes the bus try to activate
// it, and systemd's activation file (Exec=/bin/false) turns "not running"
// into a spawn error that cannot be told apart from a real failure.
const managerCallFlags = dbus.FlagNoAutoStart

// D-Bus errors that mean the manager is not on the bus at all.
const (
	errServiceUnknown = "org.freedesktop.DBus.Error.ServiceUnknown"
	errNameHasNoOwner = "org.freedesktop.DBus.Error.NameHasNoOwner"
)

// errAlreadySubscribed is systemd's answer to a second Subscribe from the same
// connection; the subscription it asks for is already in place.
const errAlreadySubscribed = "org.freedesktop.systemd1.AlreadySubscribed"

// Unit is one unit the manager reports failed.
type Unit struct {
	Name string // the unit name as systemd reports it, e.g. "sshd.service"
}

// unitRow is one entry of ListUnitsFiltered's a(ssssssouso) reply.
type unitRow struct {
	Name, Description, LoadState, ActiveState, SubState, Following string
	Path                                                           dbus.ObjectPath
	JobID                                                          uint32
	JobType                                                        string
	JobPath                                                        dbus.ObjectPath
}

// Monitor lists one systemd manager's failed units and reports when a unit's
// ActiveState may have changed. It is safe for concurrent use.
type Monitor struct {
	conn    *dbus.Conn // nil for Absent
	changed chan struct{}
}

// Absent returns a Monitor for a manager that is not watched (no bus, the
// monitor disabled, or the manager not running): Present is false, Failed is
// empty, neither returns an error, and Changed never fires.
func Absent() *Monitor {
	return &Monitor{changed: make(chan struct{}, 1)}
}

// New watches the systemd manager on conn, a bus connection the caller keeps
// open and closes. It adds match rules for unit ActiveState changes and
// UnitRemoved, then calls Manager.Subscribe, without which systemd emits no
// unit signal. Changes that happen before New returns are not reported: list
// them with Failed after New.
//
// A failed match rule or Subscribe is logged once at WARN naming the bus name
// and leaves Changed silent; Present and Failed still work. A Subscribe
// refused because nothing owns the name (the manager is absent) logs nothing,
// so the caller's own report of the absence is the only line. Nothing else is
// ever logged. The caller passes log.With("manager", ...) so the WARN names
// the manager. A nil log discards.
func New(conn *dbus.Conn, log *slog.Logger) *Monitor {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	m := &Monitor{conn: conn, changed: make(chan struct{}, 1)}

	ctx, cancel := context.WithTimeout(context.Background(), setupTimeout)
	defer cancel()

	added, err := addMatches(ctx, conn)
	if err != nil {
		removeErr := removeMatches(conn, added)
		log.Warn("unit change notifications unavailable: adding the systemd signal match rules failed",
			"name", sdName, "error", errors.Join(err, removeErr))
		return m
	}
	if err := subscribe(ctx, conn); err != nil {
		removeErr := removeMatches(conn, added)
		if managerAbsent(err) && removeErr == nil {
			return m
		}
		log.Warn("unit change notifications unavailable: subscribing to the systemd manager failed",
			"name", sdName, "error", errors.Join(err, removeErr))
		return m
	}

	signals := make(chan *dbus.Signal, signalBuffer)
	conn.Signal(signals)
	go m.read(signals)
	return m
}

// matchRules are the signals Monitor listens to. Every rule names systemd as
// the sender, so the bus routes no broadcast look-alike from another process.
// A signal sent directly to this connection bypasses match rules, so a
// look-alike can still arrive that way; at worst it causes one extra,
// coalesced re-list, as unit names always come from ListUnitsFiltered sent to
// the well-known name.
func matchRules() [][]dbus.MatchOption {
	return [][]dbus.MatchOption{
		{
			dbus.WithMatchSender(sdName),
			dbus.WithMatchPathNamespace(sdUnitPrefix),
			dbus.WithMatchInterface(propsIface),
			dbus.WithMatchMember(memberPropsChanged),
			dbus.WithMatchArg(0, sdUnitIface),
		},
		{
			dbus.WithMatchSender(sdName),
			dbus.WithMatchObjectPath(sdPath),
			dbus.WithMatchInterface(sdManagerIface),
			dbus.WithMatchMember(memberUnitRemoved),
		},
	}
}

// addMatches adds every match rule and returns the ones it added, so a
// failure part-way can be undone.
func addMatches(ctx context.Context, conn *dbus.Conn) ([][]dbus.MatchOption, error) {
	var added [][]dbus.MatchOption
	for _, rule := range matchRules() {
		if err := conn.AddMatchSignalContext(ctx, rule...); err != nil {
			return added, fmt.Errorf("adding a signal match rule for %s: %w", sdName, err)
		}
		added = append(added, rule)
	}
	return added, nil
}

// subscribe asks the manager to emit unit signals. A connection that is
// already subscribed counts as subscribed.
func subscribe(ctx context.Context, conn *dbus.Conn) error {
	err := conn.Object(sdName, sdPath).
		CallWithContext(ctx, sdManagerIface+"."+memberSubscribe, managerCallFlags).
		Store()
	var dbusErr dbus.Error
	if errors.As(err, &dbusErr) && dbusErr.Name == errAlreadySubscribed {
		return nil
	}
	return err
}

// removeMatches removes the rules addMatches added, joining every error. It
// runs under a fresh cleanupTimeout context, never the setup one, which may
// already be done.
func removeMatches(conn *dbus.Conn, rules [][]dbus.MatchOption) error {
	if len(rules) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	var errs []error
	for _, rule := range rules {
		if err := conn.RemoveMatchSignalContext(ctx, rule...); err != nil {
			errs = append(errs, fmt.Errorf("removing a signal match rule for %s: %w", sdName, err))
		}
	}
	return errors.Join(errs...)
}

// managerAbsent reports whether err says nothing owns the manager's name.
func managerAbsent(err error) bool {
	var dbusErr dbus.Error
	if !errors.As(err, &dbusErr) {
		return false
	}
	return dbusErr.Name == errServiceUnknown || dbusErr.Name == errNameHasNoOwner
}

// Changed fires after the manager reports a unit ActiveState change or an
// unloaded unit. Changes that arrive before the receiver reads coalesce into
// one. The channel is closed when the bus connection is lost (or closed by
// the caller); it is never closed by Absent or after a failed setup, where
// it simply never fires.
func (m *Monitor) Changed() <-chan struct{} {
	return m.changed
}

// Present reports whether the manager's name is owned on the bus.
func (m *Monitor) Present(ctx context.Context) (bool, error) {
	if m.conn == nil {
		return false, nil
	}
	var owned bool
	if err := m.conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.NameHasOwner", 0, sdName).Store(&owned); err != nil {
		return false, fmt.Errorf("asking the bus whether %s is running: %w", sdName, err)
	}
	return owned, nil
}

// Failed lists the units the manager reports in the failed state. Names come
// from the manager's reply, never from the escaped unit object paths.
func (m *Monitor) Failed(ctx context.Context) ([]Unit, error) {
	if m.conn == nil {
		return nil, nil
	}
	var rows []unitRow
	err := m.conn.Object(sdName, sdPath).
		CallWithContext(ctx, sdManagerIface+"."+memberListUnitsFiltered, managerCallFlags, []string{stateFailed}).
		Store(&rows)
	if err != nil {
		return nil, fmt.Errorf("listing the failed units of %s: %w", sdName, err)
	}
	units := make([]Unit, 0, len(rows))
	for _, r := range rows {
		units = append(units, Unit{Name: r.Name})
	}
	return units, nil
}

// read is the single reader of the connection's signal channel and the only
// sender on, and closer of, m.changed. It exits when godbus closes the
// channel or the connection's context ends (on a connection already closed,
// conn.Signal registers nothing and the channel never closes), and closes
// m.changed as it does, so the caller sees the bus loss. New starts it at
// most once per Monitor, so the close happens at most once.
func (m *Monitor) read(signals <-chan *dbus.Signal) {
	defer close(m.changed)
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
			select {
			case m.changed <- struct{}{}:
			default: // a change is already pending; this one coalesces into it
			}
		case <-done:
			return
		}
	}
}

// relevant reports whether sig is a Unit PropertiesChanged under the unit
// namespace that changes or invalidates ActiveState, or the manager's
// UnitRemoved. godbus delivers every signal the connection receives to every
// channel, including those other monitors' rules let in, so anything else,
// including a malformed body, is ignored.
func relevant(sig *dbus.Signal) bool {
	if sig == nil {
		return false
	}
	switch sig.Name {
	case signalUnitRemoved:
		return sig.Path == sdPath
	case signalPropsChanged:
		return strings.HasPrefix(string(sig.Path), sdUnitPrefix+"/") && changesActiveState(sig.Body)
	default:
		return false
	}
}

// changesActiveState reports whether a PropertiesChanged body is for the Unit
// interface and holds ActiveState in its changed dict or invalidated list.
func changesActiveState(body []any) bool {
	if len(body) < 2 {
		return false
	}
	if iface, ok := body[0].(string); !ok || iface != sdUnitIface {
		return false
	}
	changed, ok := body[1].(map[string]dbus.Variant)
	if !ok {
		return false
	}
	if _, ok := changed[propActiveState]; ok {
		return true
	}
	if len(body) >= 3 {
		if invalidated, ok := body[2].([]string); ok && slices.Contains(invalidated, propActiveState) {
			return true
		}
	}
	return false
}
