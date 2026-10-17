package unitmon

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/obentoo/bentoolkit/internal/desktop/dbustest"
)

// Regression tests for the 3.1 fix round. They live in package unitmon, not
// unitmon_test, so they can shorten setupTimeout and read managerAbsent.

// Same bounds as the pre-authored file: -race -shuffle=on on a loaded host.
const (
	fixFireWithin = 5 * time.Second
	fixQuietFor   = time.Second
	fixCallBudget = 5 * time.Second
)

// fixUnitPath is the object path the fake emits unit changes from. It only
// needs to sit under the unit namespace.
const fixUnitPath = dbus.ObjectPath(sdUnitPrefix + "/sshd_2eservice")

// ---------------------------------------------------------------------------
// A minimal fake manager that records the header flags of every call.
// ---------------------------------------------------------------------------

type fixCall struct {
	Member string
	Flags  dbus.Flags
}

type fixManager struct {
	conn *dbus.Conn

	mu        sync.Mutex
	calls     []fixCall
	subscribe func() error // nil answers success
}

func (f *fixManager) recorded() []fixCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *fixManager) setSubscribe(fn func() error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subscribe = fn
}

func (f *fixManager) subscribeCalled() bool {
	for _, c := range f.recorded() {
		if c.Member == memberSubscribe {
			return true
		}
	}
	return false
}

func (f *fixManager) LookupObject(path dbus.ObjectPath) (dbus.ServerObject, bool) {
	return fixObject{f: f, path: path}, true
}

type fixObject struct {
	f    *fixManager
	path dbus.ObjectPath
}

func (o fixObject) LookupInterface(name string) (dbus.Interface, bool) {
	if o.path != sdPath || name != sdManagerIface {
		return nil, false
	}
	return fixIface{f: o.f}, true
}

type fixIface struct{ f *fixManager }

func (i fixIface) LookupMethod(member string) (dbus.Method, bool) {
	switch member {
	case memberSubscribe, memberListUnitsFiltered:
		return &fixMethod{f: i.f, member: member}, true
	default:
		return nil, false
	}
}

type fixMethod struct {
	f      *fixManager
	member string
}

// DecodeArguments is where the message, and so its header flags, is visible.
func (m *fixMethod) DecodeArguments(_ *dbus.Conn, _ string, msg *dbus.Message, args []any) ([]any, error) {
	m.f.mu.Lock()
	m.f.calls = append(m.f.calls, fixCall{Member: m.member, Flags: msg.Flags})
	m.f.mu.Unlock()
	return args, nil
}

func (m *fixMethod) Call(...any) ([]any, error) {
	switch m.member {
	case memberSubscribe:
		m.f.mu.Lock()
		fn := m.f.subscribe
		m.f.mu.Unlock()
		if fn != nil {
			if err := fn(); err != nil {
				return nil, err
			}
		}
		return nil, nil
	default:
		return []any{[]unitRow{}}, nil
	}
}

func (*fixMethod) NumArguments() int     { return 0 }
func (*fixMethod) NumReturns() int       { return 0 }
func (*fixMethod) ArgumentValue(int) any { return nil }
func (*fixMethod) ReturnValue(int) any   { return nil }

// startFixManager owns org.freedesktop.systemd1 on addr. Unlike the
// pre-authored fake it emits unit changes whether or not anyone subscribed:
// these tests are about what reaches the monitor's connection.
func startFixManager(t *testing.T, addr string) *fixManager {
	t.Helper()
	f := &fixManager{}
	conn, err := dbus.Connect(addr, dbus.WithHandler(f))
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

// emitActiveState broadcasts a relevant unit ActiveState change.
func (f *fixManager) emitActiveState(t *testing.T) {
	t.Helper()
	if err := f.conn.Emit(fixUnitPath, signalPropsChanged, sdUnitIface,
		map[string]dbus.Variant{propActiveState: dbus.MakeVariant(stateFailed)}, []string{}); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// Helpers.
// ---------------------------------------------------------------------------

type fixLogs struct {
	mu   sync.Mutex
	recs []string
}

func (l *fixLogs) Enabled(context.Context, slog.Level) bool { return true }

func (l *fixLogs) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s", r.Level, r.Message)
	r.Attrs(func(a slog.Attr) bool {
		fmt.Fprintf(&b, " %s=%v", a.Key, a.Value)
		return true
	})
	l.mu.Lock()
	defer l.mu.Unlock()
	l.recs = append(l.recs, b.String())
	return nil
}

func (l *fixLogs) WithAttrs([]slog.Attr) slog.Handler { return l }
func (l *fixLogs) WithGroup(string) slog.Handler      { return l }

func (l *fixLogs) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.recs)
}

func fixConnect(t *testing.T, addr string) *dbus.Conn {
	t.Helper()
	conn, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func fixFired(ch <-chan struct{}, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ch:
		return true
	case <-timer.C:
		return false
	}
}

// fixReceives reports whether a unit PropertiesChanged reaches ch within d,
// draining anything else.
func fixReceives(ch <-chan *dbus.Signal, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		select {
		case sig := <-ch:
			if sig != nil && sig.Name == signalPropsChanged && sig.Path == fixUnitPath {
				return true
			}
		case <-timer.C:
			return false
		}
	}
}

// ---------------------------------------------------------------------------
// Tests.
// ---------------------------------------------------------------------------

// TestS098UnitmonFixAlreadySubscribedIsSuccess: systemd answers a second
// Subscribe from the same connection with AlreadySubscribed. That is a working
// subscription, not a failure: New logs nothing and Changed fires.
func TestS098UnitmonFixAlreadySubscribedIsSuccess(t *testing.T) {
	addr := dbustest.Session(t)
	sd := startFixManager(t, addr)
	sd.setSubscribe(func() error {
		return dbus.NewError(errAlreadySubscribed, []any{"Client is already subscribed."})
	})
	logs := &fixLogs{}
	m := New(fixConnect(t, addr), slog.New(logs))

	if !sd.subscribeCalled() {
		t.Fatal("New returned without calling Subscribe")
	}
	if recs := logs.all(); len(recs) != 0 {
		t.Fatalf("New logged %q for an AlreadySubscribed answer; want nothing", recs)
	}
	sd.emitActiveState(t)
	if !fixFired(m.Changed(), fixFireWithin) {
		t.Fatal("Changed did not fire after an AlreadySubscribed answer")
	}
}

// TestS098UnitmonFixCleanupSurvivesSetupTimeout: when Subscribe outlives the
// setup context, the match rules New added must still be removed. godbus sends
// no call on a done context, so a cleanup under the setup context leaked the
// rules and kept unit signals flowing to the shared connection.
func TestS098UnitmonFixCleanupSurvivesSetupTimeout(t *testing.T) {
	saved := setupTimeout
	setupTimeout = 500 * time.Millisecond
	t.Cleanup(func() { setupTimeout = saved })

	addr := dbustest.Session(t)
	sd := startFixManager(t, addr)
	release := make(chan struct{})
	// Registered after the fake's own cleanup, so it runs first and the
	// blocked handler returns before the fake's connection closes.
	t.Cleanup(func() { close(release) })
	sd.setSubscribe(func() error {
		<-release
		return nil
	})

	logs := &fixLogs{}
	conn := fixConnect(t, addr)
	m := New(conn, slog.New(logs))

	recs := logs.all()
	if len(recs) != 1 || !strings.Contains(recs[0], "WARN") || !strings.Contains(recs[0], sdName) {
		t.Fatalf("logs after a timed-out Subscribe = %q; want exactly one WARN naming %s", recs, sdName)
	}
	if strings.Contains(recs[0], "removing a signal match rule") {
		t.Fatalf("the match-rule cleanup failed: %q", recs[0])
	}

	signals := make(chan *dbus.Signal, signalBuffer)
	conn.Signal(signals)
	sd.emitActiveState(t)
	if fixReceives(signals, fixQuietFor) {
		t.Fatal("a unit signal still reached the connection: New left its match rules on the bus after Subscribe timed out")
	}
	if fixFired(m.Changed(), 0) {
		t.Fatal("Changed fired although the subscription failed")
	}

	// Control: the same emission does arrive once the rule is back, so the
	// quiet above is the removed rule, not a lost signal.
	ctx, cancel := context.WithTimeout(context.Background(), fixCallBudget)
	defer cancel()
	if err := conn.AddMatchSignalContext(ctx, matchRules()[0]...); err != nil {
		t.Fatal(err)
	}
	sd.emitActiveState(t)
	if !fixReceives(signals, fixFireWithin) {
		t.Fatal("control: the unit signal did not arrive with the match rule in place")
	}
}

// TestS098UnitmonFixManagerCallsDoNotAutoStart: every call to the manager
// carries NoAutoStart, so an absent manager is never activated.
func TestS098UnitmonFixManagerCallsDoNotAutoStart(t *testing.T) {
	addr := dbustest.Session(t)
	sd := startFixManager(t, addr)
	m := New(fixConnect(t, addr), nil)
	ctx, cancel := context.WithTimeout(context.Background(), fixCallBudget)
	defer cancel()
	if _, err := m.Failed(ctx); err != nil {
		t.Fatalf("Failed: %v", err)
	}

	seen := map[string]bool{}
	for _, c := range sd.recorded() {
		seen[c.Member] = true
		if c.Flags&dbus.FlagNoAutoStart == 0 {
			t.Errorf("%s was sent with flags %#x; want NoAutoStart set", c.Member, c.Flags)
		}
	}
	for _, member := range []string{memberSubscribe, memberListUnitsFiltered} {
		if !seen[member] {
			t.Errorf("%s never reached the fake manager; calls: %+v", member, sd.recorded())
		}
	}
}

// TestS098UnitmonFixActivatableAbsentManagerIsSilent reproduces the host
// layout: sys-apps/systemd installs an activation file for
// org.freedesktop.systemd1 whose Exec is /bin/false. With no manager running,
// New must log nothing (the caller's INFO is the only line), and the bus's
// answer to a NoAutoStart call must read as "absent".
func TestS098UnitmonFixActivatableAbsentManagerIsSilent(t *testing.T) {
	addr := fixActivatingBus(t)

	logs := &fixLogs{}
	conn := fixConnect(t, addr)
	m := New(conn, slog.New(logs))
	if recs := logs.all(); len(recs) != 0 {
		t.Fatalf("New on a bus with an activatable but absent manager logged %q; want nothing", recs)
	}

	ctx, cancel := context.WithTimeout(context.Background(), fixCallBudget)
	defer cancel()
	present, err := m.Present(ctx)
	if err != nil || present {
		t.Fatalf("Present() = %v, %v; want false and no error", present, err)
	}
	err = conn.Object(sdName, sdPath).CallWithContext(ctx, sdManagerIface+"."+memberSubscribe, managerCallFlags).Store()
	if err == nil || !managerAbsent(err) {
		t.Fatalf("Subscribe with NoAutoStart to an absent manager = %v; want an error managerAbsent accepts", err)
	}

	// Control: without NoAutoStart the bus does activate, and the spawn error
	// is not one managerAbsent accepts. This is what used to log the WARN.
	err = conn.Object(sdName, sdPath).CallWithContext(ctx, sdManagerIface+"."+memberSubscribe, 0).Store()
	if err == nil || managerAbsent(err) {
		t.Fatalf("control: Subscribe without NoAutoStart = %v; want an activation error managerAbsent rejects", err)
	}
}

// fixActivatingBus starts a private dbus-daemon whose service directory holds
// systemd's activation file, Exec=/bin/false as sys-apps/systemd ships it.
// dbustest's buses have no service directory, so this one is started here.
func fixActivatingBus(t *testing.T) string {
	t.Helper()
	daemon, err := exec.LookPath("dbus-daemon")
	if err != nil {
		// dbustest owns the skip-or-fail policy (fail in CI): defer to it.
		_ = dbustest.Session(t)
		t.Fatalf("dbus-daemon not found but dbustest did not skip: %v", err)
	}
	falseBin, err := exec.LookPath("false")
	if err != nil {
		t.Skipf("no false binary to activate: %v", err)
	}

	dir, err := os.MkdirTemp("", "unitmonfix-") //nolint:usetesting // t.TempDir() can push the bus socket path past the 108-byte sun_path limit; removed by t.Cleanup below.
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	services := filepath.Join(dir, "services")
	if err := os.Mkdir(services, 0o700); err != nil {
		t.Fatal(err)
	}
	service := "[D-BUS Service]\nName=" + sdName + "\nExec=" + falseBin + "\n"
	if err := os.WriteFile(filepath.Join(services, sdName+".service"), []byte(service), 0o600); err != nil {
		t.Fatal(err)
	}
	conf := `<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-Bus Bus Configuration 1.0//EN"
 "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig>
  <type>session</type>
  <listen>unix:dir=` + dir + `</listen>
  <auth>EXTERNAL</auth>
  <servicedir>` + services + `</servicedir>
  <policy context="default">
    <allow send_destination="*" eavesdrop="true"/>
    <allow eavesdrop="true"/>
    <allow own="*"/>
  </policy>
</busconfig>
`
	confPath := filepath.Join(dir, "bus.conf")
	if err := os.WriteFile(confPath, []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}

	// No test context: it is cancelled before the client cleanups run, and
	// the cleanup below must kill the daemon after them.
	cmd := exec.Command(daemon, "--config-file="+confPath, "--nofork", "--print-address=1")
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
	select {
	case addr := <-lines:
		if addr == "" {
			t.Fatal("dbus-daemon printed no address")
		}
		return addr
	case <-time.After(fixCallBudget):
		t.Fatal("dbus-daemon did not print its address")
		return ""
	}
}
