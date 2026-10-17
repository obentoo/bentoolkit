package smartmon_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/obentoo/bentoolkit/internal/desktop/dbustest"
	"github.com/obentoo/bentoolkit/internal/desktop/smartmon"
)

// udisks2's well-known name, root object and the interfaces a drive carries.
const (
	udName       = "org.freedesktop.UDisks2"
	udRoot       = dbus.ObjectPath("/org/freedesktop/UDisks2")
	omIface      = "org.freedesktop.DBus.ObjectManager"
	propsIface   = "org.freedesktop.DBus.Properties"
	propsChanged = propsIface + ".PropertiesChanged"
	ifacesAdded  = omIface + ".InterfacesAdded"
	ifacesGone   = omIface + ".InterfacesRemoved"
	driveIface   = "org.freedesktop.UDisks2.Drive"
	ataIface     = "org.freedesktop.UDisks2.Drive.Ata"
	nvmeIface    = "org.freedesktop.UDisks2.NVMe.Controller"
	blockIface   = "org.freedesktop.UDisks2.Block"
	managerIface = "org.freedesktop.UDisks2.Manager"
)

// The methods R4.9 forbids: each needs polkit authorization.
var s098Privileged = []string{"SmartUpdate", "SmartSelftestStart", "SmartSetEnabled"}

// The only members a read-only client has any reason to call on udisks2.
var s098ReadOnly = []string{"GetManagedObjects", "Get", "GetAll", "Introspect"}

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
// looked up, so a call to a method the fake does not implement, or a call
// with the wrong arguments, is still seen. Arguments pass through undecoded.
// ---------------------------------------------------------------------------

type s098MethodFunc func(path dbus.ObjectPath, args []any) ([]any, error)

type s098Call struct {
	Path   dbus.ObjectPath
	Iface  string
	Member string
}

type s098FakeBus struct {
	mu      sync.Mutex
	calls   []s098Call
	methods map[string]s098MethodFunc // iface + "." + member, any path
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
	return s098Method{fn: fn, path: i.path}, true
}

type s098Method struct {
	fn   s098MethodFunc
	path dbus.ObjectPath
}

func (m s098Method) DecodeArguments(_ *dbus.Conn, _ string, _ *dbus.Message, args []any) ([]any, error) {
	return args, nil
}
func (m s098Method) Call(args ...any) ([]any, error) { return m.fn(m.path, args) }
func (s098Method) NumArguments() int                 { return 0 }
func (s098Method) NumReturns() int                   { return 0 }
func (s098Method) ArgumentValue(int) any             { return nil }
func (s098Method) ReturnValue(int) any               { return nil }

// ---------------------------------------------------------------------------
// A fake org.freedesktop.UDisks2.
// ---------------------------------------------------------------------------

type s098Ifaces = map[string]map[string]dbus.Variant

type fakeUDisks struct {
	conn *dbus.Conn
	bus  *s098FakeBus

	mu      sync.Mutex
	objects map[dbus.ObjectPath]s098Ifaces
	listErr *dbus.Error
}

func s098DrivePath(id string) dbus.ObjectPath {
	return dbus.ObjectPath("/org/freedesktop/UDisks2/drives/" + id)
}

func s098DriveProps(model, serial string) map[string]dbus.Variant {
	return map[string]dbus.Variant{
		"Model":         dbus.MakeVariant(model),
		"Serial":        dbus.MakeVariant(serial),
		"Vendor":        dbus.MakeVariant(""),
		"ConnectionBus": dbus.MakeVariant(""),
		"Removable":     dbus.MakeVariant(false),
	}
}

func s098Ata(model, serial string, supported, enabled, failing bool) s098Ifaces {
	return s098Ifaces{
		driveIface: s098DriveProps(model, serial),
		ataIface: {
			"SmartSupported":   dbus.MakeVariant(supported),
			"SmartEnabled":     dbus.MakeVariant(enabled),
			"SmartFailing":     dbus.MakeVariant(failing),
			"SmartTemperature": dbus.MakeVariant(float64(310)),
		},
	}
}

func s098NVMe(model, serial string, warnings []string) s098Ifaces {
	if warnings == nil {
		warnings = []string{}
	}
	return s098Ifaces{
		driveIface: s098DriveProps(model, serial),
		nvmeIface: {
			"SmartCriticalWarning": dbus.MakeVariant(warnings),
			"SmartTemperature":     dbus.MakeVariant(uint16(310)),
			"State":                dbus.MakeVariant("live"),
		},
	}
}

// startUDisks owns org.freedesktop.UDisks2 on addr and serves the object
// manager, the standard properties interface and, so a test can catch a call
// to them, the privileged SMART methods (which succeed, as they would for a
// client polkit lets through).
func startUDisks(t *testing.T, addr string, objects map[dbus.ObjectPath]s098Ifaces) *fakeUDisks {
	t.Helper()
	f := &fakeUDisks{bus: newS098FakeBus(), objects: map[dbus.ObjectPath]s098Ifaces{}}
	for p, ifs := range objects {
		f.objects[p] = ifs
	}
	f.bus.handle(omIface, "GetManagedObjects", func(path dbus.ObjectPath, _ []any) ([]any, error) {
		if path != udRoot {
			return nil, dbus.NewError("org.freedesktop.DBus.Error.UnknownMethod", []any{"no object manager at " + string(path)})
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.listErr != nil {
			return nil, f.listErr
		}
		out := map[dbus.ObjectPath]map[string]map[string]dbus.Variant{}
		for p, ifs := range f.objects {
			out[p] = s098Copy(ifs)
		}
		return []any{out}, nil
	})
	f.bus.handle(propsIface, "GetAll", func(path dbus.ObjectPath, args []any) ([]any, error) {
		iface, _ := firstString(args)
		f.mu.Lock()
		defer f.mu.Unlock()
		props, ok := f.objects[path][iface]
		if !ok {
			return nil, dbus.NewError("org.freedesktop.DBus.Error.InvalidArgs", []any{"no interface " + iface})
		}
		return []any{maps.Clone(props)}, nil
	})
	f.bus.handle(propsIface, "Get", func(path dbus.ObjectPath, args []any) ([]any, error) {
		iface, _ := firstString(args)
		var name string
		if len(args) > 1 {
			name, _ = args[1].(string)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		v, ok := f.objects[path][iface][name]
		if !ok {
			return nil, dbus.NewError("org.freedesktop.DBus.Error.InvalidArgs", []any{"no property " + name})
		}
		return []any{v}, nil
	})
	for _, iface := range []string{ataIface, nvmeIface} {
		for _, member := range s098Privileged {
			f.bus.handle(iface, member, func(dbus.ObjectPath, []any) ([]any, error) { return nil, nil })
		}
	}

	conn, err := dbus.Connect(addr, dbus.WithHandler(f.bus))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if reply, err := conn.RequestName(udName, dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("fake udisks2 RequestName: %v %v", reply, err)
	}
	f.conn = conn
	return f
}

// s098Copy deep-copies an object's interfaces, so a reply being encoded
// never shares a map with a later setProp.
func s098Copy(ifs s098Ifaces) map[string]map[string]dbus.Variant {
	out := make(map[string]map[string]dbus.Variant, len(ifs))
	for iface, props := range ifs {
		out[iface] = maps.Clone(props)
	}
	return out
}

func firstString(args []any) (string, bool) {
	if len(args) == 0 {
		return "", false
	}
	s, ok := args[0].(string)
	return s, ok
}

func (f *fakeUDisks) refuseList() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listErr = dbus.NewError("org.freedesktop.DBus.Error.AccessDenied", []any{"denied by the test"})
}

// setProp changes one property and emits PropertiesChanged from the object.
func (f *fakeUDisks) setProp(t *testing.T, path dbus.ObjectPath, iface, name string, v any) {
	t.Helper()
	f.mu.Lock()
	f.objects[path][iface][name] = dbus.MakeVariant(v)
	f.mu.Unlock()
	if err := f.conn.Emit(path, propsChanged, iface, map[string]dbus.Variant{name: dbus.MakeVariant(v)}, []string{}); err != nil {
		t.Fatal(err)
	}
}

// add exports a new object and announces it with InterfacesAdded on the root.
func (f *fakeUDisks) add(t *testing.T, path dbus.ObjectPath, ifs s098Ifaces) {
	t.Helper()
	f.mu.Lock()
	f.objects[path] = ifs
	f.mu.Unlock()
	if err := f.conn.Emit(udRoot, ifacesAdded, path, s098Copy(ifs)); err != nil {
		t.Fatal(err)
	}
}

// remove drops an object and announces it with InterfacesRemoved on the root.
func (f *fakeUDisks) remove(t *testing.T, path dbus.ObjectPath) {
	t.Helper()
	f.mu.Lock()
	var names []string
	for iface := range f.objects[path] {
		names = append(names, iface)
	}
	delete(f.objects, path)
	f.mu.Unlock()
	if err := f.conn.Emit(udRoot, ifacesGone, path, names); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// A private bus on which udisks2 is activatable but not running: its service
// directory names the service, as an installed udisks2 does. Nothing here
// ever asks the bus to start it (Exec is /bin/false).
// ---------------------------------------------------------------------------

const s098ActivatableConf = `<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-Bus Bus Configuration 1.0//EN"
 "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig>
  <type>session</type>
  <listen>unix:dir=%DIR%</listen>
  <auth>EXTERNAL</auth>
  <servicedir>%DIR%/services</servicedir>
  <policy context="default">
    <allow send_destination="*" eavesdrop="true"/>
    <allow eavesdrop="true"/>
    <allow own="*"/>
  </policy>
</busconfig>
`

func s098BusWithActivatable(t *testing.T, name string) string {
	t.Helper()
	bin, err := exec.LookPath("dbus-daemon")
	if err != nil {
		if ci, perr := strconv.ParseBool(os.Getenv("CI")); perr == nil && ci {
			t.Fatalf("dbus-daemon is required in CI: %v", err)
		}
		t.Skipf("dbus-daemon not installed: %v", err)
	}
	dir, err := os.MkdirTemp("", "s098-") //nolint:usetesting // t.TempDir() can push the bus socket path past the 108-byte sun_path limit; removed by t.Cleanup below.
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.Mkdir(filepath.Join(dir, "services"), 0o700); err != nil {
		t.Fatal(err)
	}
	service := "[D-BUS Service]\nName=" + name + "\nExec=/bin/false\n"
	if err := os.WriteFile(filepath.Join(dir, "services", name+".service"), []byte(service), 0o600); err != nil {
		t.Fatal(err)
	}
	conf := filepath.Join(dir, "bus.conf")
	if err := os.WriteFile(conf, []byte(strings.ReplaceAll(s098ActivatableConf, "%DIR%", dir)), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin, "--config-file="+conf, "--nofork", "--print-address=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	lines := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(stdout).ReadString('\n')
		lines <- strings.TrimSpace(line)
	}()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case addr := <-lines:
		if addr == "" {
			t.Fatal("dbus-daemon printed no address")
		}
		return addr
	case <-timer.C:
		t.Fatal("dbus-daemon did not print its address within 10s")
	}
	return ""
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

func s098New(t *testing.T, addr string, logs *s098Logs) (*smartmon.Monitor, *dbus.Conn) {
	t.Helper()
	if logs == nil {
		logs = &s098Logs{}
	}
	conn := s098Connect(t, addr)
	return smartmon.New(conn, logs.logger()), conn
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

func s098ByPath(t *testing.T, drives []smartmon.Drive) map[dbus.ObjectPath]smartmon.Drive {
	t.Helper()
	out := map[dbus.ObjectPath]smartmon.Drive{}
	for _, d := range drives {
		if _, dup := out[d.Path]; dup {
			t.Fatalf("Drives() lists %s more than once: %+v", d.Path, drives)
		}
		out[d.Path] = d
	}
	return out
}

func s098Drives(t *testing.T, m *smartmon.Monitor) map[dbus.ObjectPath]smartmon.Drive {
	t.Helper()
	drives, err := m.Drives(s098Ctx(t))
	if err != nil {
		t.Fatalf("Drives: %v", err)
	}
	return s098ByPath(t, drives)
}

// ---------------------------------------------------------------------------
// Hostile halves first.
// ---------------------------------------------------------------------------

// TestS098SmartmonIgnoresAtaSmartUnlessSupportedAndEnabled is R4.6's hostile
// half: a drive whose SmartFailing reads true is not failing while SMART is
// unsupported (a USB-bridged SATA disk) or disabled, and nothing is logged
// for it. The last drive is the benign control: supported, enabled, failing.
func TestS098SmartmonIgnoresAtaSmartUnlessSupportedAndEnabled(t *testing.T) {
	addr := dbustest.Session(t)
	neither, unsupported, disabled, real := s098DrivePath("usb_bridge"), s098DrivePath("unsupported"), s098DrivePath("disabled"), s098DrivePath("failing")
	startUDisks(t, addr, map[dbus.ObjectPath]s098Ifaces{
		neither:     s098Ata("JMicron Bridge", "SN-A", false, false, true),
		unsupported: s098Ata("Old Disk", "SN-B", false, true, true),
		disabled:    s098Ata("Quiet Disk", "SN-C", true, false, true),
		real:        s098Ata("Dying Disk", "SN-D", true, true, true),
	})
	logs := &s098Logs{}
	m, _ := s098New(t, addr, logs)

	drives := s098Drives(t, m)
	for _, p := range []dbus.ObjectPath{neither, unsupported, disabled} {
		d, ok := drives[p]
		if !ok {
			t.Errorf("Drives() omits %s", p)
			continue
		}
		if d.Failing {
			t.Errorf("%s reads Failing with SMART unsupported or disabled: %+v", p, d)
		}
	}
	if d, ok := drives[real]; !ok || !d.Failing {
		t.Errorf("Drives()[%s] = %+v, %v; want Failing (supported, enabled, SmartFailing true)", real, d, ok)
	}
	logs.mu.Lock()
	recs := slices.Clone(logs.recs)
	logs.mu.Unlock()
	if len(recs) != 0 {
		t.Errorf("Drives() logged %+v; want nothing", recs)
	}
}

// TestS098SmartmonKeepsLookAlikeDrivesApart is the identity rule of R4.2/R4.5
// (a drive is its object path), both converse halves:
//   - wrongly collapse: two drives with the same model and serial (cheap USB
//     bridges report one) are two drives with their own verdicts;
//   - wrongly split: an NVMe drive whose identity lives on Drive and whose
//     SMART lives on NVMe.Controller of the same object is one drive.
func TestS098SmartmonKeepsLookAlikeDrivesApart(t *testing.T) {
	addr := dbustest.Session(t)
	twinA, twinB, nvme := s098DrivePath("twin_a"), s098DrivePath("twin_b"), s098DrivePath("nvme0")
	startUDisks(t, addr, map[dbus.ObjectPath]s098Ifaces{
		twinA: s098Ata("Generic Disk", "000000000000", true, true, true),
		twinB: s098Ata("Generic Disk", "000000000000", true, true, false),
		nvme:  s098NVMe("Samsung SSD 990 PRO", "S7XXNJ0W123456", []string{"spare"}),
	})
	m, _ := s098New(t, addr, nil)

	drives, err := m.Drives(s098Ctx(t))
	if err != nil {
		t.Fatalf("Drives: %v", err)
	}
	if len(drives) != 3 {
		t.Fatalf("Drives() = %+v; want 3 entries (two look-alike disks, one NVMe drive)", drives)
	}
	byPath := s098ByPath(t, drives)
	if !byPath[twinA].Failing || byPath[twinB].Failing {
		t.Errorf("look-alike drives merged their verdicts: %s=%+v, %s=%+v", twinA, byPath[twinA], twinB, byPath[twinB])
	}
	got := byPath[nvme]
	if got.Model != "Samsung SSD 990 PRO" || got.Serial != "S7XXNJ0W123456" || !slices.Equal(got.Warnings, []string{"spare"}) {
		t.Errorf("NVMe drive = %+v; want Model and Serial from Drive and Warnings [spare] from NVMe.Controller", got)
	}
}

// TestS098SmartmonListsOnlyDrives: block devices, the manager and jobs are
// udisks2 objects too, and none of them is a drive.
func TestS098SmartmonListsOnlyDrives(t *testing.T) {
	addr := dbustest.Session(t)
	drive := s098DrivePath("WDC_WD40")
	startUDisks(t, addr, map[dbus.ObjectPath]s098Ifaces{
		drive: s098Ata("WDC WD40", "WD-1", true, true, false),
		"/org/freedesktop/UDisks2/block_devices/sda": {blockIface: {
			"Device": dbus.MakeVariant([]byte("/dev/sda\x00")),
			"Drive":  dbus.MakeVariant(drive),
		}},
		"/org/freedesktop/UDisks2/Manager": {managerIface: {"Version": dbus.MakeVariant("2.10.1")}},
		"/org/freedesktop/UDisks2/jobs/1":  {"org.freedesktop.UDisks2.Job": {"Operation": dbus.MakeVariant("ata-smart-refresh")}},
	})
	m, _ := s098New(t, addr, nil)

	drives := s098Drives(t, m)
	if len(drives) != 1 {
		t.Fatalf("Drives() = %+v; want only %s", drives, drive)
	}
	if d := drives[drive]; d.Model != "WDC WD40" || d.Serial != "WD-1" || d.Failing {
		t.Errorf("Drives()[%s] = %+v; want WDC WD40 / WD-1, healthy", drive, d)
	}
}

// TestS098SmartmonNeverCallsPrivilegedMethods is R4.9: across start-up,
// listing, every kind of change signal and a re-listing, udisks2 receives no
// SmartUpdate, SmartSelftestStart or SmartSetEnabled — and no call at all
// beyond the read-only members.
func TestS098SmartmonNeverCallsPrivilegedMethods(t *testing.T) {
	addr := dbustest.Session(t)
	ata, nvme := s098DrivePath("ata"), s098DrivePath("nvme")
	ud := startUDisks(t, addr, map[dbus.ObjectPath]s098Ifaces{
		ata:  s098Ata("ATA Disk", "A1", true, true, false),
		nvme: s098NVMe("NVMe Disk", "N1", nil),
	})
	m, _ := s098New(t, addr, nil)
	ctx := s098Ctx(t)

	if _, err := m.Available(ctx); err != nil {
		t.Fatal(err)
	}
	s098Drives(t, m)
	ud.setProp(t, ata, ataIface, "SmartFailing", true)
	ud.setProp(t, nvme, nvmeIface, "SmartCriticalWarning", []string{"temperature"})
	ud.add(t, s098DrivePath("late"), s098Ata("Late Disk", "L1", true, true, false))
	ud.remove(t, s098DrivePath("late"))
	if !s098Fired(m.Changed(), fireWithin) {
		t.Fatal("Changed did not fire")
	}
	s098Drives(t, m)
	// Let any reaction to the signals reach the fake before reading its log.
	time.Sleep(quietFor)

	for _, c := range ud.bus.recorded() {
		if slices.Contains(s098Privileged, c.Member) {
			t.Errorf("udisks2 received privileged call %s.%s on %s", c.Iface, c.Member, c.Path)
		} else if !slices.Contains(s098ReadOnly, c.Member) {
			t.Errorf("udisks2 received non-read-only call %s.%s on %s", c.Iface, c.Member, c.Path)
		}
	}
}

// TestS098SmartmonRejectsMistypedSmartProperties is the Q4 error path for a
// property of the wrong D-Bus type: SmartCriticalWarning is a string list
// (not the bitmask byte NVMe uses on the wire), and a mistyped SmartFailing
// cannot be read as healthy. Either is an error naming the object path and
// the property, never a silently healthy drive.
func TestS098SmartmonRejectsMistypedSmartProperties(t *testing.T) {
	t.Run("nvme warning as byte", func(t *testing.T) {
		addr := dbustest.Session(t)
		p := s098DrivePath("nvme_byte")
		ifs := s098NVMe("NVMe Disk", "N1", nil)
		ifs[nvmeIface]["SmartCriticalWarning"] = dbus.MakeVariant(byte(0x01))
		startUDisks(t, addr, map[dbus.ObjectPath]s098Ifaces{p: ifs})
		m, _ := s098New(t, addr, nil)

		drives, err := m.Drives(s098Ctx(t))
		if err == nil {
			t.Fatalf("Drives() = %+v, nil; want an error for a byte SmartCriticalWarning", drives)
		}
		if !strings.Contains(err.Error(), string(p)) || !strings.Contains(err.Error(), "SmartCriticalWarning") {
			t.Errorf("error %q does not name %s and SmartCriticalWarning", err, p)
		}
	})
	t.Run("ata failing as string", func(t *testing.T) {
		addr := dbustest.Session(t)
		p := s098DrivePath("ata_string")
		ifs := s098Ata("ATA Disk", "A1", true, true, false)
		ifs[ataIface]["SmartFailing"] = dbus.MakeVariant("true")
		startUDisks(t, addr, map[dbus.ObjectPath]s098Ifaces{p: ifs})
		m, _ := s098New(t, addr, nil)

		drives, err := m.Drives(s098Ctx(t))
		if err == nil {
			t.Fatalf("Drives() = %+v, nil; want an error for a string SmartFailing", drives)
		}
		if !strings.Contains(err.Error(), string(p)) || !strings.Contains(err.Error(), "SmartFailing") {
			t.Errorf("error %q does not name %s and SmartFailing", err, p)
		}
	})
}

// TestS098SmartmonDrivesErrorNamesUDisks2 is the Q4 error path for the
// listing call: wrapped (the dbus.Error survives errors.As) and naming the
// bus name.
func TestS098SmartmonDrivesErrorNamesUDisks2(t *testing.T) {
	addr := dbustest.Session(t)
	ud := startUDisks(t, addr, map[dbus.ObjectPath]s098Ifaces{
		s098DrivePath("a"): s098Ata("ATA Disk", "A1", true, true, true),
	})
	ud.refuseList()
	m, _ := s098New(t, addr, nil)

	drives, err := m.Drives(s098Ctx(t))
	if err == nil {
		t.Fatalf("Drives() = %+v, nil; want an error", drives)
	}
	var dbusErr dbus.Error
	if !errors.As(err, &dbusErr) || dbusErr.Name != "org.freedesktop.DBus.Error.AccessDenied" {
		t.Errorf("Drives error %v does not wrap the D-Bus error (%%w)", err)
	}
	if !strings.Contains(err.Error(), udName) {
		t.Errorf("Drives error %q does not name %s", err, udName)
	}
}

// TestS098SmartmonChangedIgnoresIrrelevantSignals is the hostile half of
// R4.1: signals that only look like a SMART change or a hot-plug must not
// fire Changed. Each case is a near miss of a relevant signal.
func TestS098SmartmonChangedIgnoresIrrelevantSignals(t *testing.T) {
	addr := dbustest.Session(t)
	drive := s098DrivePath("disk")
	ud := startUDisks(t, addr, map[dbus.ObjectPath]s098Ifaces{
		drive: s098Ata("ATA Disk", "A1", true, true, false),
		"/org/freedesktop/UDisks2/block_devices/sda": {blockIface: {"Drive": dbus.MakeVariant(drive)}},
	})
	m, _ := s098New(t, addr, nil)
	ch := m.Changed()

	// A change on the Drive interface, which carries no SMART verdict.
	if err := ud.conn.Emit(drive, propsChanged, driveIface,
		map[string]dbus.Variant{"TimeMediaDetected": dbus.MakeVariant(uint64(42))}, []string{}); err != nil {
		t.Fatal(err)
	}
	// A SMART-shaped change on an object outside the drives namespace.
	if err := ud.conn.Emit("/org/freedesktop/UDisks2/block_devices/sda", propsChanged, ataIface,
		map[string]dbus.Variant{"SmartFailing": dbus.MakeVariant(true)}, []string{}); err != nil {
		t.Fatal(err)
	}
	// An InterfacesAdded that is not on the object manager's root.
	if err := ud.conn.Emit("/org/freedesktop/UDisks2/Manager", ifacesAdded, s098DrivePath("ghost"),
		map[string]map[string]dbus.Variant(s098Ata("Ghost", "G1", true, true, true))); err != nil {
		t.Fatal(err)
	}
	// Perfect SMART and hot-plug signals, sent by a process that is not
	// udisks2.
	foreign := s098Connect(t, addr)
	if err := foreign.Emit(drive, propsChanged, ataIface,
		map[string]dbus.Variant{"SmartFailing": dbus.MakeVariant(true)}, []string{}); err != nil {
		t.Fatal(err)
	}
	if err := foreign.Emit(udRoot, ifacesAdded, s098DrivePath("ghost"),
		map[string]map[string]dbus.Variant(s098Ata("Ghost", "G1", true, true, true))); err != nil {
		t.Fatal(err)
	}

	if s098Fired(ch, quietFor) {
		t.Fatal("Changed fired for a signal that is not udisks2 reporting a SMART change or a drive added or removed")
	}
	ud.setProp(t, drive, ataIface, "SmartFailing", true)
	if !s098Fired(ch, fireWithin) {
		t.Fatal("Changed did not fire for a real SmartFailing change after the ignored ones")
	}
}

// TestS098SmartmonChangedIgnoresSignalsMeantForOtherMonitors is the shared
// connection hostile case: netmon and unitmon add their own match rules on
// the same system connection, and godbus hands every signal the connection
// receives to every registered channel.
func TestS098SmartmonChangedIgnoresSignalsMeantForOtherMonitors(t *testing.T) {
	addr := dbustest.Session(t)
	drive := s098DrivePath("disk")
	ud := startUDisks(t, addr, map[dbus.ObjectPath]s098Ifaces{
		drive: s098Ata("ATA Disk", "A1", true, true, false),
	})
	m, conn := s098New(t, addr, nil)
	ch := m.Changed()

	// Another monitor's rule on the same connection: anything under
	// NetworkManager's namespace, from any sender.
	if err := conn.AddMatchSignal(dbus.WithMatchPathNamespace("/org/freedesktop/NetworkManager")); err != nil {
		t.Fatal(err)
	}
	other := s098Connect(t, addr)
	if err := other.Emit("/org/freedesktop/NetworkManager/Devices/1", propsChanged, ataIface,
		map[string]dbus.Variant{"SmartFailing": dbus.MakeVariant(true)}, []string{}); err != nil {
		t.Fatal(err)
	}
	if err := other.Emit("/org/freedesktop/NetworkManager", ifacesAdded, s098DrivePath("ghost"),
		map[string]map[string]dbus.Variant(s098Ata("Ghost", "G1", true, true, true))); err != nil {
		t.Fatal(err)
	}
	if err := other.Emit("/org/freedesktop/NetworkManager", ifacesGone, drive, []string{driveIface, ataIface}); err != nil {
		t.Fatal(err)
	}

	if s098Fired(ch, quietFor) {
		t.Fatal("Changed fired for a signal delivered through another monitor's match rule")
	}
	ud.setProp(t, drive, ataIface, "SmartFailing", true)
	if !s098Fired(ch, fireWithin) {
		t.Fatal("Changed did not fire for a real SmartFailing change")
	}
}

// TestS098SmartmonNewOnFailedSubscriptionWarnsOnce: a match rule that cannot
// be added (here, on a closed connection) is one WARN naming udisks2, and
// Changed stays silent instead of firing or blocking.
func TestS098SmartmonNewOnFailedSubscriptionWarnsOnce(t *testing.T) {
	addr := dbustest.Session(t)
	startUDisks(t, addr, nil)
	conn := s098Connect(t, addr)
	_ = conn.Close()
	logs := &s098Logs{}

	m := smartmon.New(conn, logs.logger())
	if s098Fired(m.Changed(), quietFor) {
		t.Error("Changed fired on a monitor whose subscription failed")
	}
	warns := logs.atOrAbove(slog.LevelWarn)
	if len(warns) != 1 {
		t.Fatalf("got %d WARN-or-above records, want exactly 1: %+v", len(warns), warns)
	}
	if warns[0].Level != slog.LevelWarn || !strings.Contains(strings.ToLower(warns[0].Text), "udisks2") {
		t.Errorf("record = %+v; want one WARN naming udisks2", warns[0])
	}
}

// ---------------------------------------------------------------------------
// Benign halves.
// ---------------------------------------------------------------------------

// TestS098SmartmonReportsVerdicts is R4.2/R4.3/R4.7: the start-up listing
// reports model, serial and verdict of every drive — a failing ATA drive, a
// healthy ATA drive, an NVMe drive with critical warnings and a healthy one.
func TestS098SmartmonReportsVerdicts(t *testing.T) {
	addr := dbustest.Session(t)
	ataBad, ataOK, nvmeBad, nvmeOK := s098DrivePath("ata_bad"), s098DrivePath("ata_ok"), s098DrivePath("nvme_bad"), s098DrivePath("nvme_ok")
	startUDisks(t, addr, map[dbus.ObjectPath]s098Ifaces{
		ataBad:  s098Ata("ST2000DM008", "ZFL1ABCD", true, true, true),
		ataOK:   s098Ata("WDC WD40EFRX", "WD-WCC7K0", true, true, false),
		nvmeBad: s098NVMe("WD_BLACK SN850X", "23104F800001", []string{"spare", "temperature"}),
		nvmeOK:  s098NVMe("KINGSTON SNV2S", "50026B7686", nil),
	})
	m, _ := s098New(t, addr, nil)

	drives := s098Drives(t, m)
	if len(drives) != 4 {
		t.Fatalf("Drives() = %+v; want 4 drives", drives)
	}
	if d := drives[ataBad]; d.Model != "ST2000DM008" || d.Serial != "ZFL1ABCD" || !d.Failing || len(d.Warnings) != 0 {
		t.Errorf("failing ATA drive = %+v", d)
	}
	if d := drives[ataOK]; d.Model != "WDC WD40EFRX" || d.Serial != "WD-WCC7K0" || d.Failing || len(d.Warnings) != 0 {
		t.Errorf("healthy ATA drive = %+v", d)
	}
	if d := drives[nvmeBad]; d.Model != "WD_BLACK SN850X" || d.Serial != "23104F800001" || d.Failing ||
		!slices.Equal(d.Warnings, []string{"spare", "temperature"}) {
		t.Errorf("warning NVMe drive = %+v; want Warnings [spare temperature]", d)
	}
	if d := drives[nvmeOK]; d.Model != "KINGSTON SNV2S" || d.Serial != "50026B7686" || d.Failing || len(d.Warnings) != 0 {
		t.Errorf("healthy NVMe drive = %+v", d)
	}
}

// TestS098SmartmonChangedFiresOnSmartAndHotplug is R4.1's trigger: a SMART
// property change on either interface, a drive added after start-up and a
// drive removed each fire Changed, and Drives then reflects the new state.
func TestS098SmartmonChangedFiresOnSmartAndHotplug(t *testing.T) {
	t.Run("ata failing", func(t *testing.T) {
		addr := dbustest.Session(t)
		p := s098DrivePath("ata")
		ud := startUDisks(t, addr, map[dbus.ObjectPath]s098Ifaces{p: s098Ata("ATA Disk", "A1", true, true, false)})
		m, _ := s098New(t, addr, nil)
		if c := cap(m.Changed()); c != 1 {
			t.Errorf("cap(Changed()) = %d, want 1 (coalescing)", c)
		}
		ud.setProp(t, p, ataIface, "SmartFailing", true)
		if !s098Fired(m.Changed(), fireWithin) {
			t.Fatal("Changed did not fire for SmartFailing")
		}
		if !s098Drives(t, m)[p].Failing {
			t.Fatal("Drives() after the change does not report the drive failing")
		}
		// R4.5: back to healthy.
		ud.setProp(t, p, ataIface, "SmartFailing", false)
		if !s098Fired(m.Changed(), fireWithin) {
			t.Fatal("Changed did not fire when SmartFailing returned to false")
		}
		if s098Drives(t, m)[p].Failing {
			t.Fatal("Drives() still reports the recovered drive failing")
		}
	})
	t.Run("nvme warning", func(t *testing.T) {
		addr := dbustest.Session(t)
		p := s098DrivePath("nvme")
		ud := startUDisks(t, addr, map[dbus.ObjectPath]s098Ifaces{p: s098NVMe("NVMe Disk", "N1", nil)})
		m, _ := s098New(t, addr, nil)
		ud.setProp(t, p, nvmeIface, "SmartCriticalWarning", []string{"readonly"})
		if !s098Fired(m.Changed(), fireWithin) {
			t.Fatal("Changed did not fire for SmartCriticalWarning")
		}
		if got := s098Drives(t, m)[p].Warnings; !slices.Equal(got, []string{"readonly"}) {
			t.Fatalf("Warnings after the change = %v, want [readonly]", got)
		}
	})
	t.Run("drive added", func(t *testing.T) {
		addr := dbustest.Session(t)
		ud := startUDisks(t, addr, nil)
		m, _ := s098New(t, addr, nil)
		p := s098DrivePath("hotplug")
		ud.add(t, p, s098Ata("Hotplug Disk", "H1", true, true, true))
		if !s098Fired(m.Changed(), fireWithin) {
			t.Fatal("Changed did not fire for InterfacesAdded")
		}
		if d, ok := s098Drives(t, m)[p]; !ok || !d.Failing {
			t.Fatalf("Drives()[%s] = %+v, %v; want the added failing drive", p, d, ok)
		}
	})
	t.Run("drive removed", func(t *testing.T) {
		addr := dbustest.Session(t)
		p := s098DrivePath("unplugged")
		ud := startUDisks(t, addr, map[dbus.ObjectPath]s098Ifaces{p: s098Ata("Unplugged Disk", "U1", true, true, true)})
		m, _ := s098New(t, addr, nil)
		ud.remove(t, p)
		if !s098Fired(m.Changed(), fireWithin) {
			t.Fatal("Changed did not fire for InterfacesRemoved")
		}
		if _, ok := s098Drives(t, m)[p]; ok {
			t.Fatalf("Drives() still lists the removed drive %s", p)
		}
	})
}

// TestS098SmartmonAvailable is R4.8: udisks2 is available when it owns its
// name OR the bus can activate it (it is D-Bus activated, so an installed
// udisks2 is often not running yet); absent otherwise, with no error.
func TestS098SmartmonAvailable(t *testing.T) {
	t.Run("activatable only", func(t *testing.T) {
		addr := s098BusWithActivatable(t, udName)
		m, _ := s098New(t, addr, nil)
		ok, err := m.Available(s098Ctx(t))
		if err != nil || !ok {
			t.Fatalf("Available() with udisks2 activatable but not running = %v, %v; want true", ok, err)
		}
	})
	t.Run("owned", func(t *testing.T) {
		addr := dbustest.Session(t)
		startUDisks(t, addr, nil)
		m, _ := s098New(t, addr, nil)
		ok, err := m.Available(s098Ctx(t))
		if err != nil || !ok {
			t.Fatalf("Available() with udisks2 running = %v, %v; want true", ok, err)
		}
	})
	t.Run("absent", func(t *testing.T) {
		addr := dbustest.Session(t)
		m, _ := s098New(t, addr, nil)
		ok, err := m.Available(s098Ctx(t))
		if err != nil || ok {
			t.Fatalf("Available() with no udisks2 = %v, %v; want false and no error", ok, err)
		}
	})
	t.Run("closed connection", func(t *testing.T) {
		addr := dbustest.Session(t)
		startUDisks(t, addr, nil)
		m, conn := s098New(t, addr, nil)
		_ = conn.Close()
		ok, err := m.Available(s098Ctx(t))
		if err == nil {
			t.Fatalf("Available() on a closed connection = %v, nil; want an error", ok)
		}
		if !strings.Contains(err.Error(), udName) {
			t.Errorf("Available error %q does not name %s", err, udName)
		}
	})
}

// TestS098SmartmonQuietWhileHealthy: the monitor returns errors and leaves
// logging to its caller; a healthy start-up, listing and change log nothing
// at INFO or above.
func TestS098SmartmonQuietWhileHealthy(t *testing.T) {
	addr := dbustest.Session(t)
	p := s098DrivePath("ata")
	ud := startUDisks(t, addr, map[dbus.ObjectPath]s098Ifaces{p: s098Ata("ATA Disk", "A1", true, true, false)})
	logs := &s098Logs{}
	m, _ := s098New(t, addr, logs)

	if _, err := m.Available(s098Ctx(t)); err != nil {
		t.Fatal(err)
	}
	s098Drives(t, m)
	ud.setProp(t, p, ataIface, "SmartFailing", true)
	if !s098Fired(m.Changed(), fireWithin) {
		t.Fatal("Changed did not fire")
	}
	s098Drives(t, m)
	if recs := logs.atOrAbove(slog.LevelInfo); len(recs) != 0 {
		t.Fatalf("healthy operation logged %+v; want nothing at INFO or above", recs)
	}
}

// TestS098SmartmonAbsentIsSilent: Absent stands in when there is no system
// bus or udisks2: unavailable, no drives, no error, and Changed never fires.
func TestS098SmartmonAbsentIsSilent(t *testing.T) {
	m := smartmon.Absent()
	for range 2 {
		ok, err := m.Available(s098Ctx(t))
		if err != nil || ok {
			t.Fatalf("Absent().Available() = %v, %v; want false and no error", ok, err)
		}
		drives, err := m.Drives(s098Ctx(t))
		if err != nil || len(drives) != 0 {
			t.Fatalf("Absent().Drives() = %+v, %v; want none and no error", drives, err)
		}
	}
	if m.Changed() == nil {
		t.Fatal("Absent().Changed() is nil")
	}
	if s098Fired(m.Changed(), 300*time.Millisecond) {
		t.Fatal("Absent().Changed fired")
	}
}
