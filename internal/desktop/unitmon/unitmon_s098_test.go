package unitmon_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/obentoo/bentoolkit/internal/desktop/dbustest"
	"github.com/obentoo/bentoolkit/internal/desktop/unitmon"
)

// systemd's well-known name, manager object and the interfaces its signals use.
const (
	sdName         = "org.freedesktop.systemd1"
	sdPath         = dbus.ObjectPath("/org/freedesktop/systemd1")
	sdManagerIface = "org.freedesktop.systemd1.Manager"
	sdUnitIface    = "org.freedesktop.systemd1.Unit"
	sdServiceIface = "org.freedesktop.systemd1.Service"
	propsChanged   = "org.freedesktop.DBus.Properties.PropertiesChanged"
	unitRemoved    = sdManagerIface + ".UnitRemoved"
)

// Generous bounds: these tests run under -race -shuffle=on next to other
// D-Bus packages, and signal delivery is slow on a loaded machine.
const (
	fireWithin = 5 * time.Second
	quietFor   = time.Second
	callBudget = 5 * time.Second
)

// ---------------------------------------------------------------------------
// A recording D-Bus handler. Every method call that reaches the fake's
// connection is recorded by path, interface and member BEFORE the method is
// looked up, so a call to a method the fake does not implement is still seen.
// Arguments are passed through undecoded: each fake method checks its own.
// ---------------------------------------------------------------------------

type s098MethodFunc func(args []any) ([]any, error)

type s098Call struct {
	Path   dbus.ObjectPath
	Iface  string
	Member string
}

type s098FakeBus struct {
	mu      sync.Mutex
	calls   []s098Call
	methods map[string]s098MethodFunc
}

func newS098FakeBus() *s098FakeBus {
	return &s098FakeBus{methods: map[string]s098MethodFunc{}}
}

func (f *s098FakeBus) handle(iface, member string, fn s098MethodFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.methods[iface+"."+member] = fn
}

func (f *s098FakeBus) recorded() []s098Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *s098FakeBus) LookupObject(path dbus.ObjectPath) (dbus.ServerObject, bool) {
	return s098Object{bus: f, path: path}, true
}

type s098Object struct {
	bus  *s098FakeBus
	path dbus.ObjectPath
}

func (o s098Object) LookupInterface(name string) (dbus.Interface, bool) {
	return s098Iface{bus: o.bus, path: o.path, iface: name}, true
}

type s098Iface struct {
	bus   *s098FakeBus
	path  dbus.ObjectPath
	iface string
}

func (i s098Iface) LookupMethod(member string) (dbus.Method, bool) {
	i.bus.mu.Lock()
	defer i.bus.mu.Unlock()
	i.bus.calls = append(i.bus.calls, s098Call{Path: i.path, Iface: i.iface, Member: member})
	fn, ok := i.bus.methods[i.iface+"."+member]
	if !ok {
		return nil, false
	}
	return s098Method{fn: fn}, true
}

type s098Method struct{ fn s098MethodFunc }

func (m s098Method) DecodeArguments(_ *dbus.Conn, _ string, _ *dbus.Message, args []any) ([]any, error) {
	return args, nil
}
func (m s098Method) Call(args ...any) ([]any, error) { return m.fn(args) }
func (s098Method) NumArguments() int                 { return 0 }
func (s098Method) NumReturns() int                   { return 0 }
func (s098Method) ArgumentValue(int) any             { return nil }
func (s098Method) ReturnValue(int) any               { return nil }

// ---------------------------------------------------------------------------
// A fake org.freedesktop.systemd1 manager.
// ---------------------------------------------------------------------------

// s098UnitRow is one entry of ListUnitsFiltered's a(ssssssouso) reply.
type s098UnitRow struct {
	Name, Description, LoadState, ActiveState, SubState, Following string
	Path                                                           dbus.ObjectPath
	JobID                                                          uint32
	JobType                                                        string
	JobPath                                                        dbus.ObjectPath
}

// s098UnitPath escapes a unit name the way systemd builds its object path:
// every byte outside [A-Za-z0-9] becomes _xx.
func s098UnitPath(name string) dbus.ObjectPath {
	var b strings.Builder
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9' && i > 0) {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "_%02x", c)
	}
	return dbus.ObjectPath("/org/freedesktop/systemd1/unit/" + b.String())
}

func s098Unit(name, active string) s098UnitRow {
	sub := "running"
	if active != "active" {
		sub = active
	}
	return s098UnitRow{
		Name: name, Description: name, LoadState: "loaded", ActiveState: active, SubState: sub,
		Path: s098UnitPath(name), JobPath: "/",
	}
}

type fakeSystemd struct {
	conn *dbus.Conn
	bus  *s098FakeBus

	mu           sync.Mutex
	units        []s098UnitRow
	subscribers  int
	subscribeErr *dbus.Error
	listErr      *dbus.Error
	filters      [][]string
}

// startSystemd owns org.freedesktop.systemd1 on addr and serves Subscribe and
// ListUnitsFiltered. Like systemd, it emits unit PropertiesChanged and
// UnitRemoved only once some client has called Subscribe.
func startSystemd(t *testing.T, addr string, units ...s098UnitRow) *fakeSystemd {
	t.Helper()
	f := &fakeSystemd{bus: newS098FakeBus(), units: slices.Clone(units)}
	f.bus.handle(sdManagerIface, "Subscribe", func([]any) ([]any, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.subscribeErr != nil {
			return nil, f.subscribeErr
		}
		f.subscribers++
		return nil, nil
	})
	f.bus.handle(sdManagerIface, "ListUnitsFiltered", func(args []any) ([]any, error) {
		if len(args) != 1 {
			return nil, dbus.NewError("org.freedesktop.DBus.Error.InvalidArgs", []any{"want one argument (as)"})
		}
		states, ok := args[0].([]string)
		if !ok {
			return nil, dbus.NewError("org.freedesktop.DBus.Error.InvalidArgs", []any{"want as"})
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.filters = append(f.filters, slices.Clone(states))
		if f.listErr != nil {
			return nil, f.listErr
		}
		rows := []s098UnitRow{}
		for _, u := range f.units {
			if len(states) == 0 || slices.Contains(states, u.LoadState) ||
				slices.Contains(states, u.ActiveState) || slices.Contains(states, u.SubState) {
				rows = append(rows, u)
			}
		}
		return []any{rows}, nil
	})

	conn, err := dbus.Connect(addr, dbus.WithHandler(f.bus))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if reply, err := conn.RequestName(sdName, dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("fake systemd RequestName: %v %v", reply, err)
	}
	f.conn = conn
	return f
}

// refuse makes the method whose error field is *field answer AccessDenied.
func (f *fakeSystemd) refuse(field **dbus.Error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	*field = dbus.NewError("org.freedesktop.DBus.Error.AccessDenied", []any{"denied by the test"})
}

func (f *fakeSystemd) subscribed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.subscribers > 0
}

// setActive changes a unit's ActiveState and announces it on the Unit
// interface, as systemd does for subscribed clients.
func (f *fakeSystemd) setActive(t *testing.T, name, active string) {
	t.Helper()
	f.mu.Lock()
	for i := range f.units {
		if f.units[i].Name == name {
			f.units[i].ActiveState = active
			f.units[i].SubState = active
		}
	}
	f.mu.Unlock()
	f.emitUnit(t, name, sdUnitIface, map[string]dbus.Variant{
		"ActiveState": dbus.MakeVariant(active),
		"SubState":    dbus.MakeVariant(active),
	}, []string{})
}

// emitUnit sends a PropertiesChanged from the unit's object, only when
// subscribed (systemd's behaviour).
func (f *fakeSystemd) emitUnit(t *testing.T, name, iface string, changed map[string]dbus.Variant, invalidated []string) {
	t.Helper()
	if !f.subscribed() {
		return
	}
	if err := f.conn.Emit(s098UnitPath(name), propsChanged, iface, changed, invalidated); err != nil {
		t.Fatal(err)
	}
}

// removeUnit unloads a unit and announces Manager.UnitRemoved.
func (f *fakeSystemd) removeUnit(t *testing.T, name string) {
	t.Helper()
	f.mu.Lock()
	f.units = slices.DeleteFunc(f.units, func(u s098UnitRow) bool { return u.Name == name })
	f.mu.Unlock()
	if !f.subscribed() {
		return
	}
	if err := f.conn.Emit(sdPath, unitRemoved, name, s098UnitPath(name)); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// Log capture.
// ---------------------------------------------------------------------------

type s098LogRecord struct {
	Level slog.Level
	Text  string
}

type s098Logs struct {
	mu   sync.Mutex
	recs []s098LogRecord
}

func (l *s098Logs) logger() *slog.Logger { return slog.New(&s098LogHandler{logs: l}) }

func (l *s098Logs) atOrAbove(level slog.Level) []s098LogRecord {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []s098LogRecord
	for _, r := range l.recs {
		if r.Level >= level {
			out = append(out, r)
		}
	}
	return out
}

type s098LogHandler struct {
	logs  *s098Logs
	attrs []slog.Attr
}

func (h *s098LogHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *s098LogHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Message)
	for _, a := range h.attrs {
		fmt.Fprintf(&b, " %s=%v", a.Key, a.Value)
	}
	r.Attrs(func(a slog.Attr) bool {
		fmt.Fprintf(&b, " %s=%v", a.Key, a.Value)
		return true
	})
	h.logs.mu.Lock()
	defer h.logs.mu.Unlock()
	h.logs.recs = append(h.logs.recs, s098LogRecord{Level: r.Level, Text: b.String()})
	return nil
}

func (h *s098LogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &s098LogHandler{logs: h.logs, attrs: append(slices.Clone(h.attrs), attrs...)}
}

func (h *s098LogHandler) WithGroup(string) slog.Handler { return h }

// ---------------------------------------------------------------------------
// Helpers.
// ---------------------------------------------------------------------------

func s098Connect(t *testing.T, addr string) *dbus.Conn {
	t.Helper()
	conn, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func s098New(t *testing.T, addr string, logs *s098Logs) (*unitmon.Monitor, *dbus.Conn) {
	t.Helper()
	if logs == nil {
		logs = &s098Logs{}
	}
	conn := s098Connect(t, addr)
	return unitmon.New(conn, logs.logger()), conn
}

func s098Ctx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), callBudget)
	t.Cleanup(cancel)
	return ctx
}

func s098Fired(ch <-chan struct{}, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ch:
		return true
	case <-timer.C:
		return false
	}
}

// s098WaitSubscribed fails the test unless the monitor subscribed within the
// bound: every signal test depends on it, as systemd sends nothing before.
func s098WaitSubscribed(t *testing.T, f *fakeSystemd) {
	t.Helper()
	deadline := time.Now().Add(fireWithin)
	for !f.subscribed() {
		if time.Now().After(deadline) {
			t.Fatalf("Manager.Subscribe was not called within %s; systemd emits no unit signal without it", fireWithin)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func s098Names(units []unitmon.Unit) []string {
	names := make([]string, 0, len(units))
	for _, u := range units {
		names = append(names, u.Name)
	}
	slices.Sort(names)
	return names
}

// ---------------------------------------------------------------------------
// Hostile halves first.
// ---------------------------------------------------------------------------

// TestS098UnitmonSubscribeIsCalledOnNew pins the systemd gotcha behind R2.1:
// the manager emits no unit PropertiesChanged until the client calls
// Manager.Subscribe on the manager object.
func TestS098UnitmonSubscribeIsCalledOnNew(t *testing.T) {
	addr := dbustest.Session(t)
	sd := startSystemd(t, addr)
	s098New(t, addr, nil)

	s098WaitSubscribed(t, sd)
	var onManager bool
	for _, c := range sd.bus.recorded() {
		if c.Member == "Subscribe" && c.Path == sdPath && c.Iface == sdManagerIface {
			onManager = true
		}
	}
	if !onManager {
		t.Fatalf("Subscribe was not called as %s.Subscribe on %s; calls: %+v", sdManagerIface, sdPath, sd.bus.recorded())
	}
}

// TestS098UnitmonChangedIgnoresIrrelevantSignals is the hostile half of R2.1
// and R2.3: signals that only look like a unit state change must not fire
// Changed. Each case is a near miss of a relevant signal.
func TestS098UnitmonChangedIgnoresIrrelevantSignals(t *testing.T) {
	addr := dbustest.Session(t)
	sd := startSystemd(t, addr, s098Unit("sshd.service", "active"))
	m, _ := s098New(t, addr, nil)
	s098WaitSubscribed(t, sd)
	ch := m.Changed()

	// A Unit PropertiesChanged that changes neither ActiveState nor
	// invalidates it.
	sd.emitUnit(t, "sshd.service", sdUnitIface,
		map[string]dbus.Variant{"ActiveEnterTimestamp": dbus.MakeVariant(uint64(42))}, []string{"Description"})
	// ActiveState on another interface of the same unit object (systemd
	// sends PropertiesChanged on Service, Socket, ... as well).
	sd.emitUnit(t, "sshd.service", sdServiceIface,
		map[string]dbus.Variant{"ActiveState": dbus.MakeVariant("failed")}, []string{})
	// A Unit-shaped ActiveState change from systemd's own connection, but on
	// an object outside the unit namespace.
	if err := sd.conn.Emit(sdPath, propsChanged, sdUnitIface,
		map[string]dbus.Variant{"ActiveState": dbus.MakeVariant("failed")}, []string{}); err != nil {
		t.Fatal(err)
	}
	// Another manager signal that is not UnitRemoved.
	if err := sd.conn.Emit(sdPath, sdManagerIface+".JobRemoved",
		uint32(7), dbus.ObjectPath("/org/freedesktop/systemd1/job/7"), "sshd.service", "done"); err != nil {
		t.Fatal(err)
	}
	// A perfect unit change and a perfect UnitRemoved, sent by a process that
	// is not systemd.
	foreign := s098Connect(t, addr)
	if err := foreign.Emit(s098UnitPath("sshd.service"), propsChanged, sdUnitIface,
		map[string]dbus.Variant{"ActiveState": dbus.MakeVariant("failed")}, []string{}); err != nil {
		t.Fatal(err)
	}
	if err := foreign.Emit(sdPath, unitRemoved, "sshd.service", s098UnitPath("sshd.service")); err != nil {
		t.Fatal(err)
	}

	if s098Fired(ch, quietFor) {
		t.Fatal("Changed fired for a signal that is not systemd reporting a unit ActiveState change or UnitRemoved")
	}

	// Liveness: the same monitor still reacts to a real change.
	sd.setActive(t, "sshd.service", "failed")
	if !s098Fired(ch, fireWithin) {
		t.Fatal("Changed did not fire for a real ActiveState change after the ignored ones")
	}
}

// TestS098UnitmonChangedIgnoresSignalsMeantForOtherMonitors is the shared
// connection hostile case: netmon and smartmon add their own match rules on
// the same system connection, and godbus hands every signal the connection
// receives to every registered channel. A signal that arrives through another
// monitor's rule must not fire Changed, even when its body looks like a unit
// ActiveState change.
func TestS098UnitmonChangedIgnoresSignalsMeantForOtherMonitors(t *testing.T) {
	addr := dbustest.Session(t)
	sd := startSystemd(t, addr, s098Unit("sshd.service", "active"))
	m, conn := s098New(t, addr, nil)
	s098WaitSubscribed(t, sd)
	ch := m.Changed()

	// Another monitor's rule on the same connection: anything under
	// NetworkManager's namespace, from any sender.
	if err := conn.AddMatchSignal(dbus.WithMatchPathNamespace("/org/freedesktop/NetworkManager")); err != nil {
		t.Fatal(err)
	}
	other := s098Connect(t, addr)
	if err := other.Emit("/org/freedesktop/NetworkManager/Devices/1", propsChanged, sdUnitIface,
		map[string]dbus.Variant{"ActiveState": dbus.MakeVariant("failed")}, []string{"ActiveState"}); err != nil {
		t.Fatal(err)
	}
	if err := other.Emit("/org/freedesktop/NetworkManager", "org.freedesktop.NetworkManager.UnitRemoved",
		"sshd.service", s098UnitPath("sshd.service")); err != nil {
		t.Fatal(err)
	}
	if err := other.Emit("/org/freedesktop/NetworkManager", propsChanged, "org.freedesktop.NetworkManager",
		map[string]dbus.Variant{"Connectivity": dbus.MakeVariant(uint32(4))}, []string{}); err != nil {
		t.Fatal(err)
	}

	if s098Fired(ch, quietFor) {
		t.Fatal("Changed fired for a signal delivered through another monitor's match rule")
	}
	sd.setActive(t, "sshd.service", "failed")
	if !s098Fired(ch, fireWithin) {
		t.Fatal("Changed did not fire for a real ActiveState change")
	}
}

// TestS098UnitmonFailedNamesComeFromTheReplyNotThePath is the hostile half of
// R2.3/R2.5 identity: near-identical unit names are different units, and a
// unit name is never decoded from its escaped object path.
func TestS098UnitmonFailedNamesComeFromTheReplyNotThePath(t *testing.T) {
	addr := dbustest.Session(t)
	// The path of this row deliberately does not decode to its name, as a
	// path that systemd escaped differently (or a unit aliased under another
	// object) would not: the name must be field 0 of the reply.
	odd := s098Unit("foo-bar@x.service", "failed")
	odd.Path = "/org/freedesktop/systemd1/unit/unit_5f42"
	startSystemd(t, addr,
		s098Unit("foo.service", "active"),
		s098Unit("foo.socket", "failed"),
		odd,
		s098Unit("sshd.service", "active"),
	)
	m, _ := s098New(t, addr, nil)

	units, err := m.Failed(s098Ctx(t))
	if err != nil {
		t.Fatalf("Failed: %v", err)
	}
	want := []string{"foo-bar@x.service", "foo.socket"}
	if got := s098Names(units); !slices.Equal(got, want) {
		t.Fatalf("Failed() = %v, want %v (foo.service is active; foo.socket is a different unit)", got, want)
	}
}

// TestS098UnitmonManagersStayApart is the hostile half of R2.1/R2.2: the
// system and the user manager are separate buses carrying the same name; a
// monitor must list and hear only its own manager.
func TestS098UnitmonManagersStayApart(t *testing.T) {
	sysAddr := dbustest.Session(t)
	userAddr := dbustest.Session(t)
	sysSD := startSystemd(t, sysAddr, s098Unit("system-only.service", "failed"), s098Unit("shared.service", "active"))
	userSD := startSystemd(t, userAddr, s098Unit("user-only.service", "failed"), s098Unit("shared.service", "active"))
	sysMon, _ := s098New(t, sysAddr, nil)
	userMon, _ := s098New(t, userAddr, nil)
	s098WaitSubscribed(t, sysSD)
	s098WaitSubscribed(t, userSD)

	sysUnits, err := sysMon.Failed(s098Ctx(t))
	if err != nil {
		t.Fatalf("system Failed: %v", err)
	}
	userUnits, err := userMon.Failed(s098Ctx(t))
	if err != nil {
		t.Fatalf("user Failed: %v", err)
	}
	if got := s098Names(sysUnits); !slices.Equal(got, []string{"system-only.service"}) {
		t.Errorf("system Failed() = %v, want [system-only.service]", got)
	}
	if got := s098Names(userUnits); !slices.Equal(got, []string{"user-only.service"}) {
		t.Errorf("user Failed() = %v, want [user-only.service]", got)
	}

	// A change on the user bus must not wake the system monitor.
	userSD.setActive(t, "shared.service", "failed")
	if !s098Fired(userMon.Changed(), fireWithin) {
		t.Fatal("user monitor's Changed did not fire for a user unit change")
	}
	if s098Fired(sysMon.Changed(), quietFor) {
		t.Fatal("system monitor's Changed fired for a change on the user bus")
	}
}

// TestS098UnitmonSubscribeFailureWarnsOnceAndStaysSilent is R2.8's failed
// half: a refused Subscribe is one WARN naming the manager's bus name, and
// Changed stays silent instead of firing or blocking.
func TestS098UnitmonSubscribeFailureWarnsOnceAndStaysSilent(t *testing.T) {
	addr := dbustest.Session(t)
	sd := startSystemd(t, addr, s098Unit("sshd.service", "active"))
	sd.refuse(&sd.subscribeErr)
	logs := &s098Logs{}
	m, _ := s098New(t, addr, logs)

	// A relevant-looking signal sent regardless of the refused subscription.
	if err := sd.conn.Emit(s098UnitPath("sshd.service"), propsChanged, sdUnitIface,
		map[string]dbus.Variant{"ActiveState": dbus.MakeVariant("failed")}, []string{}); err != nil {
		t.Fatal(err)
	}
	if s098Fired(m.Changed(), quietFor) {
		t.Error("Changed fired although the subscription failed")
	}

	warns := logs.atOrAbove(slog.LevelWarn)
	if len(warns) != 1 {
		t.Fatalf("got %d WARN-or-above records, want exactly 1: %+v", len(warns), warns)
	}
	if warns[0].Level != slog.LevelWarn {
		t.Errorf("record level = %v, want WARN", warns[0].Level)
	}
	if !strings.Contains(warns[0].Text, sdName) {
		t.Errorf("WARN %q does not name the manager's bus name %s", warns[0].Text, sdName)
	}
}

// TestS098UnitmonFailedErrorNamesTheManager is the Q4 error path: a refused
// ListUnitsFiltered is returned wrapped (the dbus.Error survives errors.As)
// and names the bus name.
func TestS098UnitmonFailedErrorNamesTheManager(t *testing.T) {
	addr := dbustest.Session(t)
	sd := startSystemd(t, addr, s098Unit("sshd.service", "failed"))
	sd.refuse(&sd.listErr)
	m, _ := s098New(t, addr, nil)

	units, err := m.Failed(s098Ctx(t))
	if err == nil {
		t.Fatalf("Failed() = %v, nil; want an error", units)
	}
	var dbusErr dbus.Error
	if !errors.As(err, &dbusErr) || dbusErr.Name != "org.freedesktop.DBus.Error.AccessDenied" {
		t.Errorf("Failed error %v does not wrap the D-Bus error (%%w)", err)
	}
	if !strings.Contains(err.Error(), sdName) {
		t.Errorf("Failed error %q does not name %s", err, sdName)
	}
}

// TestS098UnitmonPresentErrorNamesTheManager: NameHasOwner on a closed
// connection is an error naming the bus name, not a silent false.
func TestS098UnitmonPresentErrorNamesTheManager(t *testing.T) {
	addr := dbustest.Session(t)
	startSystemd(t, addr)
	m, conn := s098New(t, addr, nil)
	_ = conn.Close()

	present, err := m.Present(s098Ctx(t))
	if err == nil {
		t.Fatalf("Present() on a closed connection = %v, nil; want an error", present)
	}
	if !strings.Contains(err.Error(), sdName) {
		t.Errorf("Present error %q does not name %s", err, sdName)
	}
}

// ---------------------------------------------------------------------------
// Benign halves.
// ---------------------------------------------------------------------------

// TestS098UnitmonChangedFiresOnActiveStateChanges is R2.3/R2.4's trigger:
// an ActiveState in the changed set, an ActiveState in the invalidated list,
// and UnitRemoved each fire Changed, and Failed then reflects the new state.
func TestS098UnitmonChangedFiresOnActiveStateChanges(t *testing.T) {
	t.Run("changed", func(t *testing.T) {
		addr := dbustest.Session(t)
		sd := startSystemd(t, addr, s098Unit("cups.service", "active"))
		m, _ := s098New(t, addr, nil)
		s098WaitSubscribed(t, sd)
		if c := cap(m.Changed()); c != 1 {
			t.Errorf("cap(Changed()) = %d, want 1 (coalescing)", c)
		}

		sd.setActive(t, "cups.service", "failed")
		if !s098Fired(m.Changed(), fireWithin) {
			t.Fatal("Changed did not fire when ActiveState became failed")
		}
		units, err := m.Failed(s098Ctx(t))
		if err != nil {
			t.Fatal(err)
		}
		if got := s098Names(units); !slices.Equal(got, []string{"cups.service"}) {
			t.Fatalf("Failed() after the failure = %v, want [cups.service]", got)
		}

		// R2.4: leaving failed (restart, reset-failed).
		sd.setActive(t, "cups.service", "active")
		if !s098Fired(m.Changed(), fireWithin) {
			t.Fatal("Changed did not fire when the unit left the failed state")
		}
		units, err = m.Failed(s098Ctx(t))
		if err != nil {
			t.Fatal(err)
		}
		if len(units) != 0 {
			t.Fatalf("Failed() after the restart = %v, want none", s098Names(units))
		}
	})

	t.Run("invalidated", func(t *testing.T) {
		addr := dbustest.Session(t)
		sd := startSystemd(t, addr, s098Unit("cups.service", "active"))
		m, _ := s098New(t, addr, nil)
		s098WaitSubscribed(t, sd)

		sd.emitUnit(t, "cups.service", sdUnitIface, map[string]dbus.Variant{}, []string{"ActiveState"})
		if !s098Fired(m.Changed(), fireWithin) {
			t.Fatal("Changed did not fire for an invalidated ActiveState")
		}
	})

	t.Run("unit removed", func(t *testing.T) {
		addr := dbustest.Session(t)
		sd := startSystemd(t, addr, s098Unit("transient.service", "failed"))
		m, _ := s098New(t, addr, nil)
		s098WaitSubscribed(t, sd)

		sd.removeUnit(t, "transient.service")
		if !s098Fired(m.Changed(), fireWithin) {
			t.Fatal("Changed did not fire for UnitRemoved")
		}
		units, err := m.Failed(s098Ctx(t))
		if err != nil {
			t.Fatal(err)
		}
		if len(units) != 0 {
			t.Fatalf("Failed() after the unload = %v, want none", s098Names(units))
		}
	})
}

// TestS098UnitmonFailedListsUnitsFailedAtStart is R2.5: the startup listing
// returns every unit the manager already reports failed, and asks the
// manager to filter on the failed state.
func TestS098UnitmonFailedListsUnitsFailedAtStart(t *testing.T) {
	addr := dbustest.Session(t)
	sd := startSystemd(t, addr,
		s098Unit("a.service", "failed"),
		s098Unit("b.mount", "failed"),
		s098Unit("c.service", "active"),
		s098Unit("d.timer", "inactive"),
	)
	m, _ := s098New(t, addr, nil)

	units, err := m.Failed(s098Ctx(t))
	if err != nil {
		t.Fatalf("Failed: %v", err)
	}
	if got, want := s098Names(units), []string{"a.service", "b.mount"}; !slices.Equal(got, want) {
		t.Fatalf("Failed() = %v, want %v", got, want)
	}
	sd.mu.Lock()
	filters := slices.Clone(sd.filters)
	sd.mu.Unlock()
	if len(filters) == 0 || !slices.Contains(filters[len(filters)-1], "failed") {
		t.Errorf("ListUnitsFiltered filters = %v, want the failed state requested", filters)
	}
}

// TestS098UnitmonFailedEmptyWhenNothingFailed: a healthy manager is an empty
// list, not an error.
func TestS098UnitmonFailedEmptyWhenNothingFailed(t *testing.T) {
	addr := dbustest.Session(t)
	startSystemd(t, addr, s098Unit("sshd.service", "active"))
	m, _ := s098New(t, addr, nil)

	units, err := m.Failed(s098Ctx(t))
	if err != nil || len(units) != 0 {
		t.Fatalf("Failed() = %v, %v; want none and no error", s098Names(units), err)
	}
}

// TestS098UnitmonPresentReportsTheManager is R2.8's absent half: Present
// tells a bus carrying systemd from one that does not, with no error.
func TestS098UnitmonPresentReportsTheManager(t *testing.T) {
	t.Run("owned", func(t *testing.T) {
		addr := dbustest.Session(t)
		startSystemd(t, addr)
		m, _ := s098New(t, addr, nil)
		present, err := m.Present(s098Ctx(t))
		if err != nil || !present {
			t.Fatalf("Present() = %v, %v; want true", present, err)
		}
	})
	t.Run("absent", func(t *testing.T) {
		addr := dbustest.Session(t)
		logs := &s098Logs{}
		m, _ := s098New(t, addr, logs)
		present, err := m.Present(s098Ctx(t))
		if err != nil || present {
			t.Fatalf("Present() = %v, %v; want false and no error", present, err)
		}
		// An absent manager is the caller's one INFO: New's refused
		// Subscribe (the name has no owner) must not add a WARN.
		if warns := logs.atOrAbove(slog.LevelWarn); len(warns) != 0 {
			t.Errorf("New on a bus with no systemd logged %d WARN-or-above records, want 0: %+v", len(warns), warns)
		}
	})
}

// TestS098UnitmonQuietWhileHealthy: the monitor returns errors and leaves
// logging to its caller; a successful subscribe, listing and change log
// nothing at INFO or above.
func TestS098UnitmonQuietWhileHealthy(t *testing.T) {
	addr := dbustest.Session(t)
	sd := startSystemd(t, addr, s098Unit("sshd.service", "active"))
	logs := &s098Logs{}
	m, _ := s098New(t, addr, logs)
	s098WaitSubscribed(t, sd)

	if _, err := m.Failed(s098Ctx(t)); err != nil {
		t.Fatal(err)
	}
	sd.setActive(t, "sshd.service", "failed")
	if !s098Fired(m.Changed(), fireWithin) {
		t.Fatal("Changed did not fire")
	}
	if _, err := m.Present(s098Ctx(t)); err != nil {
		t.Fatal(err)
	}
	if recs := logs.atOrAbove(slog.LevelInfo); len(recs) != 0 {
		t.Fatalf("healthy operation logged %+v; want nothing at INFO or above", recs)
	}
}

// TestS098UnitmonAbsentIsSilent: Absent stands in for a manager that is not
// watched (no bus, disabled, or not present): not present, nothing failed,
// no error, and Changed never fires.
func TestS098UnitmonAbsentIsSilent(t *testing.T) {
	m := unitmon.Absent()
	for range 2 {
		present, err := m.Present(s098Ctx(t))
		if err != nil || present {
			t.Fatalf("Absent().Present() = %v, %v; want false and no error", present, err)
		}
		units, err := m.Failed(s098Ctx(t))
		if err != nil || len(units) != 0 {
			t.Fatalf("Absent().Failed() = %v, %v; want none and no error", s098Names(units), err)
		}
	}
	if m.Changed() == nil {
		t.Fatal("Absent().Changed() is nil")
	}
	if s098Fired(m.Changed(), 300*time.Millisecond) {
		t.Fatal("Absent().Changed fired")
	}
}
