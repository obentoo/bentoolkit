package sni_test

import (
	"bytes"
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"

	"github.com/obentoo/bentoolkit/internal/desktop/dbustest"
	"github.com/obentoo/bentoolkit/internal/desktop/sni"
)

const (
	watcherName  = "org.kde.StatusNotifierWatcher"
	watcherPath  = dbus.ObjectPath("/StatusNotifierWatcher")
	sniIface     = "org.kde.StatusNotifierItem"
	sniPath      = dbus.ObjectPath("/StatusNotifierItem")
	extensionPkg = "gnome-shell-extension-appindicator"
)

// Three distinct 1x1 icon sets, so the test can tell which one is shown.
var itemIcons = sni.IconSet{
	Plain:    []sni.Pixmap{{Width: 1, Height: 1, Data: []byte{0xff, 1, 1, 1}}},
	Unread:   []sni.Pixmap{{Width: 1, Height: 1, Data: []byte{0xff, 2, 2, 2}}},
	Critical: []sni.Pixmap{{Width: 1, Height: 1, Data: []byte{0xff, 3, 3, 3}}},
}

type registration struct{ Sender, Service string }

// itemWatcher is a fake StatusNotifierWatcher on its own connection.
type itemWatcher struct {
	conn *dbus.Conn
	mu   sync.Mutex
	regs []registration
}

func (w *itemWatcher) RegisterStatusNotifierItem(sender dbus.Sender, service string) *dbus.Error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.regs = append(w.regs, registration{string(sender), service})
	return nil
}

func (w *itemWatcher) RegisterStatusNotifierHost(service string) *dbus.Error { return nil }

func (w *itemWatcher) registrations() []registration {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.regs)
}

func startWatcher(t *testing.T, addr string) *itemWatcher {
	t.Helper()
	conn, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	w := &itemWatcher{conn: conn}
	if err := conn.Export(w, watcherPath, watcherName); err != nil {
		t.Fatal(err)
	}
	if _, err := prop.Export(conn, watcherPath, prop.Map{watcherName: {
		"IsStatusNotifierHostRegistered": {Value: true},
		"ProtocolVersion":                {Value: int32(0)},
		"RegisteredStatusNotifierItems":  {Value: []string{}},
	}}); err != nil {
		t.Fatal(err)
	}
	if reply, err := conn.RequestName(watcherName, dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("fake watcher RequestName: %v %v", reply, err)
	}
	return w
}

type itemLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *itemLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *itemLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// startItem connects, builds the item and starts it; Start must return (the
// watcher tracking runs in the background).
func startItem(t *testing.T, addr string) (*sni.Item, *dbus.Conn, *itemLog) {
	t.Helper()
	conn, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	logs := &itemLog{}
	item := sni.New(conn, slog.New(slog.NewTextHandler(logs, nil)), itemIcons)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- item.Start(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return within 5s; watcher tracking must run in the background")
	}
	return item, conn, logs
}

func observer(t *testing.T, addr string) *dbus.Conn {
	t.Helper()
	conn, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func waitFor(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %v waiting for %s", d, what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func itemProp(t *testing.T, obs *dbus.Conn, item *dbus.Conn, name string) dbus.Variant {
	t.Helper()
	v, err := obs.Object(item.Names()[0], sniPath).GetProperty(sniIface + "." + name)
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return v
}

type wirePixmap struct {
	W, H int32
	D    []byte
}

func pixmaps(t *testing.T, v dbus.Variant) []wirePixmap {
	t.Helper()
	var px []wirePixmap
	if err := dbus.Store([]any{v.Value()}, &px); err != nil {
		t.Fatalf("decoding a(iiay) from %s: %v", v.Signature(), err)
	}
	return px
}

// shownIcon is what a host displays: the attention pixmap while the status is
// NeedsAttention, the ordinary pixmap otherwise.
func shownIcon(t *testing.T, obs, item *dbus.Conn) (string, []wirePixmap) {
	t.Helper()
	status, _ := itemProp(t, obs, item, "Status").Value().(string)
	if status == "NeedsAttention" {
		return status, pixmaps(t, itemProp(t, obs, item, "AttentionIconPixmap"))
	}
	return status, pixmaps(t, itemProp(t, obs, item, "IconPixmap"))
}

func sameIcon(px []wirePixmap, want []sni.Pixmap) bool {
	if len(px) != len(want) {
		return false
	}
	for i := range px {
		if px[i].W != want[i].Width || px[i].H != want[i].Height || !bytes.Equal(px[i].D, want[i].Data) {
			return false
		}
	}
	return true
}

// TestItem_RegistersWithItsUniqueName is R8.1 and the registration GOTCHA:
// the argument is the connection's unique name.
func TestItem_RegistersWithItsUniqueName(t *testing.T) {
	addr := dbustest.Session(t)
	w := startWatcher(t, addr)
	item, conn, _ := startItem(t, addr)
	waitFor(t, 5*time.Second, "a registration", func() bool { return len(w.registrations()) > 0 })
	r := w.registrations()[0]
	if r.Service != conn.Names()[0] {
		t.Errorf("registered %q, want the unique name %q", r.Service, conn.Names()[0])
	}
	if !item.Available() {
		t.Error("Available = false after registering with a watcher")
	}
}

// TestItem_ExportsTheSpecProperties is R8.1's fixed properties.
func TestItem_ExportsTheSpecProperties(t *testing.T) {
	addr := dbustest.Session(t)
	startWatcher(t, addr)
	item, conn, _ := startItem(t, addr)
	item.SetState(sni.View{})
	obs := observer(t, addr)
	want := map[string]any{
		"Id":         "bentoo-tray",
		"Title":      "bentoo",
		"Category":   "ApplicationStatus",
		"ItemIsMenu": true,
		"Menu":       dbus.ObjectPath("/MenuBar"),
	}
	for name, w := range want {
		if got := itemProp(t, obs, conn, name).Value(); got != w {
			t.Errorf("%s = %#v, want %#v", name, got, w)
		}
	}
}

// TestItem_StateSelectsIconStatusAndTooltip is R8.4, R8.5, R8.7 and R8.1's
// tooltip, walking plain -> unread -> critical -> plain. Each set of pixmaps
// is distinct, so a collapsed variant is caught.
func TestItem_StateSelectsIconStatusAndTooltip(t *testing.T) {
	addr := dbustest.Session(t)
	startWatcher(t, addr)
	item, conn, _ := startItem(t, addr)
	obs := observer(t, addr)
	steps := []struct {
		name   string
		view   sni.View
		status string
		icon   []sni.Pixmap
	}{
		{"none unread", sni.View{}, "Active", itemIcons.Plain},
		{"unread, none critical", sni.View{Unread: 7}, "NeedsAttention", itemIcons.Unread},
		{"a critical unread", sni.View{Unread: 7, Critical: true}, "NeedsAttention", itemIcons.Critical},
		{"all read again", sni.View{}, "Active", itemIcons.Plain},
	}
	for _, s := range steps {
		item.SetState(s.view)
		status, icon := shownIcon(t, obs, conn)
		if status != s.status {
			t.Errorf("%s: Status = %q, want %q", s.name, status, s.status)
		}
		if !sameIcon(icon, s.icon) {
			t.Errorf("%s: shown icon = %+v, want %+v", s.name, icon, s.icon)
		}
	}

	item.SetState(sni.View{Unread: 7})
	var tip struct {
		Icon        string
		Pix         []wirePixmap
		Title, Text string
	}
	if err := dbus.Store([]any{itemProp(t, obs, conn, "ToolTip").Value()}, &tip); err != nil {
		t.Fatalf("decoding ToolTip (sa(iiay)ss): %v", err)
	}
	if !strings.Contains(tip.Title+" "+tip.Text, "7") {
		t.Errorf("tooltip %q / %q does not state the unread count 7", tip.Title, tip.Text)
	}
}

// TestItem_EmitsNewStatusAndNewToolTip: hosts follow the dedicated SNI
// signals, not PropertiesChanged.
func TestItem_EmitsNewStatusAndNewToolTip(t *testing.T) {
	addr := dbustest.Session(t)
	startWatcher(t, addr)
	item, conn, _ := startItem(t, addr)
	item.SetState(sni.View{})
	obs := observer(t, addr)
	if err := obs.AddMatchSignal(dbus.WithMatchSender(conn.Names()[0]), dbus.WithMatchInterface(sniIface)); err != nil {
		t.Fatal(err)
	}
	ch := make(chan *dbus.Signal, 64)
	obs.Signal(ch)

	item.SetState(sni.View{Unread: 1})
	seen := map[string][]any{}
	deadline := time.After(3 * time.Second)
	// A signal with no arguments (NewToolTip) arrives with a nil Body, so
	// presence in the map, not a non-nil Body, is what says it was seen.
	for !hasSignal(seen, "NewStatus") || !hasSignal(seen, "NewToolTip") {
		select {
		case s := <-ch:
			seen[s.Name[strings.LastIndex(s.Name, ".")+1:]] = s.Body
		case <-deadline:
			t.Fatalf("signals seen within 3s: %v; want NewStatus and NewToolTip", seen)
		}
	}
	if len(seen["NewStatus"]) != 1 || seen["NewStatus"][0] != "NeedsAttention" {
		t.Errorf("NewStatus body = %v, want [NeedsAttention]", seen["NewStatus"])
	}
}

// TestItem_NoWatcherWarnsOnceThenRegistersWhenOneAppears is R8.2 and R8.3.
func TestItem_NoWatcherWarnsOnceThenRegistersWhenOneAppears(t *testing.T) {
	addr := dbustest.Session(t)
	item, conn, logs := startItem(t, addr)
	if item.Available() {
		t.Error("Available = true with no watcher on the bus")
	}
	for i := 0; i < 3; i++ {
		item.SetState(sni.View{Unread: i})
	}
	count := func() int {
		n := 0
		for _, line := range strings.Split(logs.String(), "\n") {
			if strings.Contains(line, extensionPkg) {
				n++
				if !strings.Contains(line, "level=WARN") {
					t.Errorf("the missing-watcher line is not a WARN: %s", line)
				}
			}
		}
		return n
	}
	if n := count(); n != 1 {
		t.Fatalf("%d log lines name %s, want exactly 1:\n%s", n, extensionPkg, logs.String())
	}

	w := startWatcher(t, addr)
	waitFor(t, 5*time.Second, "registration after the watcher appeared", func() bool { return len(w.registrations()) > 0 })
	if got := w.registrations()[0].Service; got != conn.Names()[0] {
		t.Errorf("registered %q, want %q", got, conn.Names()[0])
	}
	waitFor(t, 2*time.Second, "Available after registration", item.Available)
	if n := count(); n != 1 {
		t.Errorf("after the watcher appeared, %d lines name %s, want still 1", n, extensionPkg)
	}
}

// TestItem_ReRegistersWhenTheWatcherReturns is R8.3: the watcher leaves (the
// AppIndicator extension does this on every screen lock) and a new one takes
// the name; the item registers with it within 5 s. Along the way the item's
// connection receives one-argument NameAcquired/NameLost signals and a forged
// one-argument NameOwnerChanged, which must not kill its signal loop.
func TestItem_ReRegistersWhenTheWatcherReturns(t *testing.T) {
	addr := dbustest.Session(t)
	first := startWatcher(t, addr)
	_, conn, _ := startItem(t, addr)
	waitFor(t, 5*time.Second, "the first registration", func() bool { return len(first.registrations()) == 1 })

	if _, err := conn.RequestName("org.obentoo.Test.Churn", dbus.NameFlagDoNotQueue); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ReleaseName("org.obentoo.Test.Churn"); err != nil {
		t.Fatal(err)
	}
	forger := observer(t, addr)
	_ = forger.Emit("/org/freedesktop/DBus", "org.freedesktop.DBus.NameOwnerChanged", watcherName)

	if err := first.conn.Close(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	second := startWatcher(t, addr)
	waitFor(t, 5*time.Second, "re-registration with the new watcher", func() bool { return len(second.registrations()) > 0 })
	if got := second.registrations()[0].Service; got != conn.Names()[0] {
		t.Errorf("re-registered %q, want %q", got, conn.Names()[0])
	}
}

// hasSignal reports whether a signal named name was recorded in seen.
func hasSignal(seen map[string][]any, name string) bool {
	_, ok := seen[name]
	return ok
}
