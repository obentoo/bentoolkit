package notify

// Story 097, tech review round 1 of sub-task 9.1: a Notify sent while the
// Notifier knows of no server owner is answered by the server that took the
// name meanwhile (D-Bus activation starts it on demand). Its NameOwnerChanged
// precedes the reply on the wire, but the reader applies it at its own pace,
// before or after Send records the reply. Either way the record must end up
// under that server, as it did before records carried an owner: otherwise
// its clicks are lost, the next notification for the key is added instead of
// replacing it, and Close withdraws nothing.
//
// The reader is driven by hand, so both orders are forced. A server started
// after the Notifier read "no owner" stands in for an activated one: the
// Notifier cannot tell them apart, and the private bus keeps no service
// directories. The hostile case, two owner changes since the send, must
// still drop the reply.

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/obentoo/bentoolkit/internal/desktop/dbustest"
)

// actKey is the condition key every test here sends under.
const actKey = "disk:/"

// actServer is a minimal org.freedesktop.Notifications that honours
// replaces_id, records its calls and can hold the next Notify until the test
// releases it.
type actServer struct {
	conn     *dbus.Conn
	mu       sync.Mutex
	next     uint32
	replaces []uint32
	closes   []uint32
	hold     chan struct{}
	arrived  chan uint32
}

func (f *actServer) Notify(_ string, replaces uint32, _, _, _ string,
	_ []string, _ map[string]dbus.Variant, _ int32) (uint32, *dbus.Error) {
	f.mu.Lock()
	id := replaces
	if id == 0 {
		f.next++
		id = f.next
	}
	f.replaces = append(f.replaces, replaces)
	hold, arrived := f.hold, f.arrived
	f.hold, f.arrived = nil, nil
	f.mu.Unlock()
	if hold != nil {
		arrived <- id
		<-hold
	}
	return id, nil
}

func (f *actServer) CloseNotification(id uint32) *dbus.Error {
	f.mu.Lock()
	f.closes = append(f.closes, id)
	f.mu.Unlock()
	_ = f.conn.Emit(serverPath, serverIface+"."+memberNotificationClosed, id, uint32(3))
	return nil
}

func (f *actServer) GetCapabilities() ([]string, *dbus.Error) {
	return []string{"actions", "body"}, nil
}

// holdNext makes the next Notify wait: the returned channel yields its id on
// arrival, and release (also run at cleanup) lets it answer.
func (f *actServer) holdNext(t *testing.T) (<-chan uint32, func()) {
	t.Helper()
	hold, arrived := make(chan struct{}), make(chan uint32, 1)
	f.mu.Lock()
	f.hold, f.arrived = hold, arrived
	f.mu.Unlock()
	var once sync.Once
	release := func() { once.Do(func() { close(hold) }) }
	t.Cleanup(release)
	return arrived, release
}

func (f *actServer) lastReplaces(t *testing.T) uint32 {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.replaces) == 0 {
		t.Fatal("the server received no Notify call")
	}
	return f.replaces[len(f.replaces)-1]
}

func (f *actServer) closed() []uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.closes)
}

// startActServer connects a server to addr and takes serverName with flags.
func startActServer(t *testing.T, addr string, flags dbus.RequestNameFlags) *actServer {
	t.Helper()
	conn, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	f := &actServer{conn: conn}
	if err := conn.Export(f, serverPath, serverIface); err != nil {
		t.Fatal(err)
	}
	if reply, err := conn.RequestName(serverName, flags); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("server RequestName: %v %v", reply, err)
	}
	return f
}

// manualNotifier is a Notifier set up as New does, except that no reader
// runs: the test applies the connection's signals with pump.
type manualNotifier struct {
	*Notifier
	signals chan *dbus.Signal
}

func newManualNotifier(t *testing.T, addr string) manualNotifier {
	t.Helper()
	conn, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	for _, rule := range matchRules() {
		if err := conn.AddMatchSignal(rule...); err != nil {
			t.Fatal(err)
		}
	}
	n := &Notifier{
		conn:   conn,
		log:    slog.New(slog.DiscardHandler),
		events: make(chan Event, eventBuffer),
		sent:   make(map[uint32]sentNotification),
		tokens: make(map[uint32]string),
	}
	// Large enough that godbus never defers a delivery: every signal that
	// precedes a reply on the wire is in the channel when the reply returns.
	signals := make(chan *dbus.Signal, 1024)
	conn.Signal(signals)
	if err := n.loadOwner(); err != nil {
		t.Fatal(err)
	}
	return manualNotifier{Notifier: n, signals: signals}
}

// pump applies every signal the bus and srvs sent before the call. A round
// trip to the bus driver, and one to each server by its unique name, puts
// everything they sent earlier in the channel first.
func (m manualNotifier) pump(t *testing.T, srvs ...*actServer) {
	t.Helper()
	ctx := actCtx(t)
	var id string
	if err := m.conn.BusObject().CallWithContext(ctx, busIface+".GetId", 0).Store(&id); err != nil {
		t.Fatalf("bus round trip: %v", err)
	}
	for _, s := range srvs {
		var caps []string
		if err := m.conn.Object(s.conn.Names()[0], serverPath).
			CallWithContext(ctx, serverIface+".GetCapabilities", 0).Store(&caps); err != nil {
			t.Fatalf("server round trip: %v", err)
		}
	}
	for {
		select {
		case sig := <-m.signals:
			m.handle(sig)
		default:
			return
		}
	}
}

func actCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func actMsg(replaces uint32) Message {
	return Message{Summary: "Low disk space", Body: "b", Urgency: 1, System: true, Key: actKey, ReplacesID: replaces}
}

// viewOwner returns the owner the Notifier currently believes in.
func (m manualNotifier) viewOwner() string {
	o, _ := m.ownerView()
	return o
}

// requireNoOwner fails unless n started with no server on the bus.
func requireNoOwner(t *testing.T, m manualNotifier) {
	t.Helper()
	if o := m.viewOwner(); o != "" {
		t.Fatalf("fixture: the Notifier sees owner %q, want none", o)
	}
}

// assertKeptUnderTheServer checks that id, answered by srv for a Notify sent
// while no owner was known, is clicked, replaced and withdrawn on srv.
func assertKeptUnderTheServer(t *testing.T, m manualNotifier, srv *actServer, id uint32) {
	t.Helper()
	if err := srv.conn.Emit(serverPath, serverIface+"."+memberActionInvoked, id, "default"); err != nil {
		t.Fatal(err)
	}
	m.pump(t, srv)
	select {
	case ev := <-m.Events():
		if !ev.System || ev.Action != "default" {
			t.Errorf("click on %d reported as %+v, want a system click on default", id, ev)
		}
	default:
		t.Errorf("the click on notification %d was not reported: its record was lost", id)
	}

	got, err := m.Send(actCtx(t), actMsg(id))
	if err != nil {
		t.Fatalf("second Send: %v", err)
	}
	if r := srv.lastReplaces(t); r != id || got != id {
		t.Errorf("second Send for the key = replaces_id %d, id %d: want a replacement of %d", r, got, id)
	}
	if err := m.Close(actCtx(t), id, actKey); err != nil {
		t.Fatalf("Close(%d): %v", id, err)
	}
	if c := srv.closed(); !slices.Equal(c, []uint32{id}) {
		t.Errorf("CloseNotification calls = %v, want exactly [%d]", c, id)
	}
}

// TestS097Activation_OwnerChangeAppliedAfterTheReplyKeepsTheRecord: Send
// records the reply while the Notifier still sees no owner; the owner change
// is applied afterwards.
func TestS097Activation_OwnerChangeAppliedAfterTheReplyKeepsTheRecord(t *testing.T) {
	addr := dbustest.Session(t)
	m := newManualNotifier(t, addr)
	requireNoOwner(t, m)
	srv := startActServer(t, addr, dbus.NameFlagDoNotQueue)

	id, err := m.Send(actCtx(t), actMsg(0))
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	m.pump(t, srv)
	if o := m.viewOwner(); o != srv.conn.Names()[0] {
		t.Fatalf("fixture: owner after pump = %q, want the server %q", o, srv.conn.Names()[0])
	}
	assertKeptUnderTheServer(t, m, srv, id)
}

// TestS097Activation_OwnerChangeAppliedBeforeTheReplyKeepsTheRecord: the
// Notify goes out while the Notifier sees no owner; the owner change is
// applied before the reply reaches Send.
func TestS097Activation_OwnerChangeAppliedBeforeTheReplyKeepsTheRecord(t *testing.T) {
	addr := dbustest.Session(t)
	m := newManualNotifier(t, addr)
	requireNoOwner(t, m)
	srv := startActServer(t, addr, dbus.NameFlagDoNotQueue)

	arrived, release := srv.holdNext(t)
	done := make(chan uint32, 1)
	errc := make(chan error, 1)
	ctx := actCtx(t)
	go func() {
		id, err := m.Send(ctx, actMsg(0))
		errc <- err
		done <- id
	}()
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("fixture: the server never received the Notify")
	}
	m.pump(t)
	if o := m.viewOwner(); o != srv.conn.Names()[0] {
		t.Fatalf("fixture: owner before the reply = %q, want the server %q", o, srv.conn.Names()[0])
	}
	release()
	if err := <-errc; err != nil {
		t.Fatalf("Send: %v", err)
	}
	assertKeptUnderTheServer(t, m, srv, <-done)
}

// TestS097Activation_TwoOwnerChangesSinceTheSendDropTheReply is the hostile
// half: the server that answered was replaced before the Notifier knew of
// either, and the new server handed the same id to another application. The
// reply cannot be told apart from the new server's, so it is dropped, in
// both orders of reply and owner changes.
func TestS097Activation_TwoOwnerChangesSinceTheSendDropTheReply(t *testing.T) {
	for _, tc := range []struct {
		name      string
		pumpFirst bool
	}{
		{"changes applied after the reply", false},
		{"changes applied before the reply", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			addr := dbustest.Session(t)
			m := newManualNotifier(t, addr)
			requireNoOwner(t, m)
			first := startActServer(t, addr, dbus.NameFlagAllowReplacement|dbus.NameFlagDoNotQueue)

			arrived, release := first.holdNext(t)
			done := make(chan uint32, 1)
			errc := make(chan error, 1)
			ctx := actCtx(t)
			go func() {
				id, err := m.Send(ctx, actMsg(0))
				errc <- err
				done <- id
			}()
			var stale uint32
			select {
			case stale = <-arrived:
			case <-time.After(5 * time.Second):
				t.Fatal("fixture: the first server never received the Notify")
			}
			second := startActServer(t, addr, dbus.NameFlagReplaceExisting|dbus.NameFlagDoNotQueue)
			other, err := dbus.Connect(addr)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = other.Close() })
			var foreign uint32
			if err := other.Object(serverName, serverPath).CallWithContext(actCtx(t), serverIface+".Notify", 0,
				"other-app", uint32(0), "", "theirs", "", []string{}, map[string]dbus.Variant{}, int32(-1)).Store(&foreign); err != nil {
				t.Fatalf("foreign Notify: %v", err)
			}
			if foreign != stale {
				t.Fatalf("fixture: the foreign notification got id %d, want %d", foreign, stale)
			}
			if tc.pumpFirst {
				m.pump(t)
			}
			release()
			if err := <-errc; err != nil {
				t.Fatalf("Send: %v", err)
			}
			if got := <-done; got != stale {
				t.Fatalf("fixture: Send returned %d, want %d", got, stale)
			}
			m.pump(t, second)
			if o := m.viewOwner(); o != second.conn.Names()[0] {
				t.Fatalf("fixture: owner = %q, want the second server %q", o, second.conn.Names()[0])
			}

			if err := second.conn.Emit(serverPath, serverIface+"."+memberActionInvoked, stale, "default"); err != nil {
				t.Fatal(err)
			}
			m.pump(t, second)
			select {
			case ev := <-m.Events():
				t.Errorf("the other application's click on %d was reported as ours: %+v", stale, ev)
			default:
			}
			if _, err := m.Send(actCtx(t), actMsg(stale)); err != nil {
				t.Fatalf("Send after the handoff: %v", err)
			}
			if r := second.lastReplaces(t); r != 0 {
				t.Errorf("replaces_id = %d on the second server, want 0 (id %d is another application's there)", r, stale)
			}
			if err := m.Close(actCtx(t), stale, actKey); err != nil {
				t.Errorf("Close(%d) = %v, want nil with no call", stale, err)
			}
			if c, o := second.closed(), first.closed(); len(c) != 0 || len(o) != 0 {
				t.Errorf("CloseNotification sent: second %v, first %v; want none", c, o)
			}
		})
	}
}
