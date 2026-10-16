package smartmon

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

// udisks2's well-known name, object manager root and the namespace its drive
// objects live under.
const (
	udName        = "org.freedesktop.UDisks2"
	udRoot        = dbus.ObjectPath("/org/freedesktop/UDisks2")
	udDrives      = dbus.ObjectPath("/org/freedesktop/UDisks2/drives")
	udDrivePrefix = string(udDrives) + "/"
)

// The udisks2 interfaces a drive object carries. Model and Serial live on
// Drive; the SMART verdict lives on Drive.Ata or NVMe.Controller of the same
// object.
const (
	driveIface = "org.freedesktop.UDisks2.Drive"
	ataIface   = "org.freedesktop.UDisks2.Drive.Ata"
	nvmeIface  = "org.freedesktop.UDisks2.NVMe.Controller"
)

// The properties a Drive is made of.
const (
	propModel          = "Model"
	propSerial         = "Serial"
	propSmartSupported = "SmartSupported"
	propSmartEnabled   = "SmartEnabled"
	propSmartFailing   = "SmartFailing"
	propCriticalWarn   = "SmartCriticalWarning"
)

// The standard object manager interface udisks2 lists and hot-plugs on.
const (
	omIface                 = "org.freedesktop.DBus.ObjectManager"
	memberGetManagedObjects = "GetManagedObjects"
	memberIfacesAdded       = "InterfacesAdded"
	memberIfacesRemoved     = "InterfacesRemoved"
	signalIfacesAdded       = omIface + "." + memberIfacesAdded
	signalIfacesRemoved     = omIface + "." + memberIfacesRemoved
	methodGetManagedObjects = omIface + "." + memberGetManagedObjects
)

// The standard properties interface udisks2 reports SMART changes on.
const (
	propsIface         = "org.freedesktop.DBus.Properties"
	memberPropsChanged = "PropertiesChanged"
	signalPropsChanged = propsIface + "." + memberPropsChanged
)

// The bus driver methods Available asks.
const (
	methodNameHasOwner         = "org.freedesktop.DBus.NameHasOwner"
	methodListActivatableNames = "org.freedesktop.DBus.ListActivatableNames"
)

// verdictProps are, per SMART interface, the properties whose change can move
// a drive's verdict. A change to any other one (a temperature, a timestamp)
// is not reported.
var verdictProps = map[string][]string{
	ataIface:  {propSmartSupported, propSmartEnabled, propSmartFailing},
	nvmeIface: {propCriticalWarn},
}

// signalBuffer is the capacity of the channel handed to conn.Signal. godbus
// parks each signal that finds the channel full in a goroutine of its own, so
// the channel is buffered and its single reader never blocks.
const signalBuffer = 64

// setupTimeout bounds adding the match rules, so a bus that never answers
// cannot stall New.
const setupTimeout = 10 * time.Second

// cleanupTimeout bounds removing the match rules after a failed setup. The
// removal runs under a context of its own: godbus does not even send a call
// on a done context, and the setup one may have timed out.
const cleanupTimeout = 2 * time.Second

// errPropertyType is wrapped by every error about a property of a D-Bus type
// other than the one udisks2 documents.
var errPropertyType = errors.New("unexpected D-Bus type")

// Drive is one drive udisks2 exports and its SMART verdict.
type Drive struct {
	Path          dbus.ObjectPath // the udisks2 drive object: the drive's identity
	Model, Serial string
	Failing       bool     // ATA SmartFailing, only when SmartSupported && SmartEnabled
	Warnings      []string // NVMe SmartCriticalWarning names; empty when healthy
}

// Monitor lists udisks2's drives with their SMART verdict and reports when a
// verdict may have changed or a drive came or went. It is safe for concurrent
// use.
type Monitor struct {
	conn    *dbus.Conn // nil for Absent
	changed chan struct{}
}

// Absent returns a Monitor for a process with no system bus or no udisks2:
// Available is false, Drives is empty, neither returns an error, and Changed
// never fires.
func Absent() *Monitor {
	return &Monitor{changed: make(chan struct{}, 1)}
}

// New watches udisks2 on conn, a system bus connection the caller keeps open
// and closes. It adds match rules for SMART property changes on drive objects
// and for drives added or removed; udisks2 need not be running, as every rule
// names its well-known name, which the bus resolves when a signal is sent.
// Changes that happen before New returns are not reported: list them with
// Drives after New.
//
// A failed match rule is logged once at WARN naming udisks2 and leaves Changed
// silent; Available and Drives still work. Nothing else is ever logged. A nil
// log discards.
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
		log.Warn("disk health change notifications unavailable: adding the udisks2 signal match rules failed",
			"name", udName, "error", errors.Join(err, removeErr))
		return m
	}

	signals := make(chan *dbus.Signal, signalBuffer)
	conn.Signal(signals)
	go m.read(signals)
	return m
}

// matchRules are the signals Monitor listens to. Every rule names udisks2 as
// the sender, so the bus routes no broadcast look-alike from another process.
// A signal sent directly to this connection bypasses match rules; at worst it
// causes one extra, coalesced re-list, as verdicts always come from
// GetManagedObjects sent to the well-known name.
func matchRules() [][]dbus.MatchOption {
	var rules [][]dbus.MatchOption
	for _, iface := range []string{ataIface, nvmeIface} {
		rules = append(rules, []dbus.MatchOption{
			dbus.WithMatchSender(udName),
			dbus.WithMatchPathNamespace(udDrives),
			dbus.WithMatchInterface(propsIface),
			dbus.WithMatchMember(memberPropsChanged),
			dbus.WithMatchArg(0, iface),
		})
	}
	for _, member := range []string{memberIfacesAdded, memberIfacesRemoved} {
		rules = append(rules, []dbus.MatchOption{
			dbus.WithMatchSender(udName),
			dbus.WithMatchObjectPath(udRoot),
			dbus.WithMatchInterface(omIface),
			dbus.WithMatchMember(member),
			dbus.WithMatchArgPath(0, udDrivePrefix),
		})
	}
	return rules
}

// addMatches adds every match rule and returns the ones it added, so a
// failure part-way can be undone.
func addMatches(ctx context.Context, conn *dbus.Conn) ([][]dbus.MatchOption, error) {
	var added [][]dbus.MatchOption
	for _, rule := range matchRules() {
		if err := conn.AddMatchSignalContext(ctx, rule...); err != nil {
			return added, fmt.Errorf("adding a signal match rule for %s: %w", udName, err)
		}
		added = append(added, rule)
	}
	return added, nil
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
			errs = append(errs, fmt.Errorf("removing a signal match rule for %s: %w", udName, err))
		}
	}
	return errors.Join(errs...)
}

// Changed fires after udisks2 reports a SMART verdict property change on a
// drive, or a drive added or removed. Changes that arrive before the receiver
// reads coalesce into one. The channel is closed when the bus connection is
// lost (or closed by the caller); it is never closed by Absent or after a
// failed setup, where it simply never fires.
func (m *Monitor) Changed() <-chan struct{} {
	return m.changed
}

// Available reports whether udisks2 owns its name on the bus or the bus can
// start it: udisks2 is D-Bus activated, so an installed one is often not
// running until its first call, which Drives then makes.
func (m *Monitor) Available(ctx context.Context) (bool, error) {
	if m.conn == nil {
		return false, nil
	}
	var owned bool
	if err := m.conn.BusObject().CallWithContext(ctx, methodNameHasOwner, 0, udName).Store(&owned); err != nil {
		return false, fmt.Errorf("asking the bus whether %s is running: %w", udName, err)
	}
	if owned {
		return true, nil
	}
	var names []string
	if err := m.conn.BusObject().CallWithContext(ctx, methodListActivatableNames, 0).Store(&names); err != nil {
		return false, fmt.Errorf("asking the bus whether %s can be started: %w", udName, err)
	}
	return slices.Contains(names, udName), nil
}

// Drives lists every drive udisks2 exports, sorted by object path, from one
// GetManagedObjects call. The call carries no FlagNoAutoStart, so it starts
// an installed udisks2 that is not running yet.
//
// A drive with a property of an unexpected D-Bus type is left out and its
// error, naming the object path and the property, is joined into the returned
// error, while every well-formed drive is still returned: one bad drive never
// hides the others. A failed call returns no drives.
func (m *Monitor) Drives(ctx context.Context) ([]Drive, error) {
	if m.conn == nil {
		return nil, nil
	}
	var objects map[dbus.ObjectPath]map[string]map[string]dbus.Variant
	if err := m.conn.Object(udName, udRoot).CallWithContext(ctx, methodGetManagedObjects, 0).Store(&objects); err != nil {
		return nil, fmt.Errorf("listing the drives of %s: %w", udName, err)
	}
	var (
		drives []Drive
		errs   []error
	)
	for path, ifaces := range objects {
		if !strings.HasPrefix(string(path), udDrivePrefix) {
			continue
		}
		d, ok, err := decodeDrive(path, ifaces)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if ok {
			drives = append(drives, d)
		}
	}
	slices.SortFunc(drives, func(a, b Drive) int { return cmp.Compare(a.Path, b.Path) })
	return drives, errors.Join(errs...)
}

// decodeDrive builds the Drive of one drive object. ok is false for an object
// that carries no Drive interface (not a drive, or one being torn down).
func decodeDrive(path dbus.ObjectPath, ifaces map[string]map[string]dbus.Variant) (d Drive, ok bool, err error) {
	props, ok := ifaces[driveIface]
	if !ok {
		return Drive{}, false, nil
	}
	d.Path = path
	if d.Model, err = prop[string](path, driveIface, props, propModel); err != nil {
		return Drive{}, false, err
	}
	if d.Serial, err = prop[string](path, driveIface, props, propSerial); err != nil {
		return Drive{}, false, err
	}
	if ata, has := ifaces[ataIface]; has {
		if d.Failing, err = ataFailing(path, ata); err != nil {
			return Drive{}, false, err
		}
	}
	if nvme, has := ifaces[nvmeIface]; has {
		warnings, err := prop[[]string](path, nvmeIface, nvme, propCriticalWarn)
		if err != nil {
			return Drive{}, false, err
		}
		if len(warnings) > 0 {
			d.Warnings = warnings
		}
	}
	return d, true, nil
}

// ataFailing reads SmartFailing, which counts only while SMART is both
// supported and enabled: a USB-bridged disk reports SmartSupported false and
// whatever SmartFailing its bridge invents. SmartFailing is not even read
// otherwise, so such a drive can never cause an error either.
func ataFailing(path dbus.ObjectPath, props map[string]dbus.Variant) (bool, error) {
	supported, err := prop[bool](path, ataIface, props, propSmartSupported)
	if err != nil {
		return false, err
	}
	enabled, err := prop[bool](path, ataIface, props, propSmartEnabled)
	if err != nil {
		return false, err
	}
	if !supported || !enabled {
		return false, nil
	}
	return prop[bool](path, ataIface, props, propSmartFailing)
}

// prop reads one property as T. A missing property is T's zero value; one of
// another D-Bus type is an error naming the object path and the property.
func prop[T any](path dbus.ObjectPath, iface string, props map[string]dbus.Variant, name string) (T, error) {
	var zero T
	v, ok := props[name]
	if !ok {
		return zero, nil
	}
	got, ok := v.Value().(T)
	if !ok {
		return zero, fmt.Errorf("reading drive %s from %s: property %s.%s has type %q: %w",
			path, udName, iface, name, v.Signature().String(), errPropertyType)
	}
	return got, nil
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

// relevant reports whether sig is a SMART verdict change on a drive object or
// a drive added to or removed from udisks2's object manager. godbus delivers
// every signal the connection receives to every channel, including those
// other monitors' rules let in, so anything else, including a malformed body,
// is ignored.
func relevant(sig *dbus.Signal) bool {
	if sig == nil {
		return false
	}
	switch sig.Name {
	case signalPropsChanged:
		return strings.HasPrefix(string(sig.Path), udDrivePrefix) && changesVerdict(sig.Body)
	case signalIfacesAdded, signalIfacesRemoved:
		return sig.Path == udRoot && namesDrive(sig.Body)
	default:
		return false
	}
}

// changesVerdict reports whether a PropertiesChanged body is for a SMART
// interface and holds one of its verdict properties in its changed dict or
// invalidated list.
func changesVerdict(body []any) bool {
	if len(body) < 2 {
		return false
	}
	iface, ok := body[0].(string)
	if !ok {
		return false
	}
	names, ok := verdictProps[iface]
	if !ok {
		return false
	}
	changed, ok := body[1].(map[string]dbus.Variant)
	if !ok {
		return false
	}
	var invalidated []string
	if len(body) >= 3 {
		invalidated, _ = body[2].([]string)
	}
	for _, name := range names {
		if _, ok := changed[name]; ok || slices.Contains(invalidated, name) {
			return true
		}
	}
	return false
}

// namesDrive reports whether an InterfacesAdded or InterfacesRemoved body
// names an object under the drives namespace.
func namesDrive(body []any) bool {
	if len(body) < 1 {
		return false
	}
	path, ok := body[0].(dbus.ObjectPath)
	return ok && strings.HasPrefix(string(path), udDrivePrefix)
}
