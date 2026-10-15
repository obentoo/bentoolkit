package notify_test

// Story 097, sub-task 9.1: a notification record is valid only under the
// server owner that answered it (R6.15, with R6.6 and R6.12).
//
// A notification server started with --replace takes
// org.freedesktop.Notifications while the old one is still connected. A
// Notify the old server accepted before the handoff can be answered after
// it: that reply reaches the Notifier after the owner change, yet the id in
// it was assigned by a server that no longer owns the name. The new server
// numbers its own notifications from 1, so the same id may already be
// another application's there. Recording that reply would let the next Send
// for the condition pass replaces_id to the new server, let Close withdraw
// the other application's notification, and turn its clicks into ours.
//
// Hostile halves come first: the late reply of the old owner (wrongly kept),
// then a late reply of the CURRENT owner with other traffic around it
// (wrongly dropped). Benign: no owner change, and a fresh Send after the
// handoff.
//
// The race is made deterministic: the old server holds its Notify until the
// test has proved the Notifier applied the owner change, then answers.
//
// Reuses newNotifier, noticeMsg, notifyCall and the nf* constants from
// notify_test.go, and s097Ctx, systemMsg and the key* constants from
// notify_system_s097_test.go.

import (
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/obentoo/bentoolkit/internal/desktop/dbustest"
	"github.com/obentoo/bentoolkit/internal/desktop/notify"
)

// handoffServer is an org.freedesktop.Notifications that follows the spec on
// replaces_id and CloseNotification, can be replaced by another server
// (--replace) while it stays connected, and can hold one Notify until the
// test releases it. It assigns the id before it holds, so the id a held call
// returns is known from the moment it arrives.
type handoffServer struct {
	conn   *dbus.Conn
	mu     sync.Mutex
	next   uint32
	calls  []notifyCall
	closes []uint32
	// arrived and release are set by holdNext and consumed by the next
	// Notify: it closes arrived, then waits for release.
	arrived chan uint32
	release chan struct{}
}

func (f *handoffServer) Notify(appName string, replaces uint32, appIcon, summary, body string,
	actions []string, hints map[string]dbus.Variant, timeout int32) (uint32, *dbus.Error) {
	f.mu.Lock()
	id := replaces
	if id == 0 {
		f.next++
		id = f.next
	}
	f.calls = append(f.calls, notifyCall{appName, replaces, appIcon, summary, body, actions, hints, timeout, id})
	arrived, release := f.arrived, f.release
	f.arrived, f.release = nil, nil
	f.mu.Unlock()
	if arrived != nil {
		arrived <- id
		<-release
	}
	return id, nil
}

func (f *handoffServer) CloseNotification(id uint32) *dbus.Error {
	f.mu.Lock()
	f.closes = append(f.closes, id)
	f.mu.Unlock()
	_ = f.conn.Emit(nfPath, nfIface+".NotificationClosed", id, uint32(3))
	return nil
}

func (f *handoffServer) GetCapabilities() ([]string, *dbus.Error) {
	return []string{"actions", "body"}, nil
}

func (f *handoffServer) GetServerInformation() (string, string, string, string, *dbus.Error) {
	return "s097-handoff", "bentoo", "1", "1.2", nil
}

// holdNext makes the next Notify wait. The returned channel yields the id
// that call will return once it has arrived; release lets it answer, and is
// also run at cleanup so a failing test never leaves the call hanging.
func (f *handoffServer) holdNext(t *testing.T) (arrived <-chan uint32, release func()) {
	t.Helper()
	a, r := make(chan uint32, 1), make(chan struct{})
	f.mu.Lock()
	f.arrived, f.release = a, r
	f.mu.Unlock()
	var once sync.Once
	release = func() { once.Do(func() { close(r) }) }
	t.Cleanup(release)
	return a, release
}

func (f *handoffServer) recorded() []notifyCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *handoffServer) closed() []uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.closes)
}

func (f *handoffServer) last(t *testing.T) notifyCall {
	t.Helper()
	calls := f.recorded()
	if len(calls) == 0 {
		t.Fatal("the server received no Notify call")
	}
	return calls[len(calls)-1]
}

func (f *handoffServer) emit(t *testing.T, member string, args ...any) {
	t.Helper()
	if err := f.conn.Emit(nfPath, nfIface+"."+member, args...); err != nil {
		t.Fatalf("emitting %s: %v", member, err)
	}
}

// startFirstServer starts the server that owns the name first and lets a
// later server take it over.
func startFirstServer(t *testing.T, addr string) *handoffServer {
	t.Helper()
	return startHandoffServer(t, addr, dbus.NameFlagAllowReplacement|dbus.NameFlagDoNotQueue)
}

// replaceServer starts a server that takes the name from the current owner,
// as `dunst --replace` does; the old owner stays connected.
func replaceServer(t *testing.T, addr string) *handoffServer {
	t.Helper()
	return startHandoffServer(t, addr, dbus.NameFlagReplaceExisting|dbus.NameFlagDoNotQueue)
}

func startHandoffServer(t *testing.T, addr string, flags dbus.RequestNameFlags) *handoffServer {
	t.Helper()
	conn, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	f := &handoffServer{conn: conn}
	if err := conn.Export(f, nfPath, nfIface); err != nil {
		t.Fatal(err)
	}
	if reply, err := conn.RequestName(nfName, flags); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("handoff server RequestName: %v %v", reply, err)
	}
	return f
}

// foreignNotify shows another application's notification on whoever owns
// the name and returns its id.
func foreignNotify(t *testing.T, addr string) uint32 {
	t.Helper()
	other, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Close() })
	var id uint32
	if err := other.Object(nfName, nfPath).Call(nfIface+".Notify", 0,
		"other-app", uint32(0), "", "theirs", "", []string{}, map[string]dbus.Variant{}, int32(-1)).Store(&id); err != nil {
		t.Fatalf("foreign Notify: %v", err)
	}
	return id
}

// syncOwner proves n follows srv as the current owner and has handled every
// signal srv emitted before the call: a probe notice is sent and srv clicks
// it. The Notifier believes only the current owner's signals and handles
// them in order. Events reported before the probe's click are returned.
func syncOwner(t *testing.T, n *notify.Notifier, srv *handoffServer, tag string) []notify.Event {
	t.Helper()
	probe := "probe-" + tag
	var before []notify.Event
	deadline := time.Now().Add(3 * time.Second)
	for {
		id, err := n.Send(s097Ctx(t), noticeMsg(probe, probe, "b", 0))
		if err != nil {
			t.Fatalf("syncing %s: probe Send: %v", tag, err)
		}
		srv.emit(t, "ActionInvoked", id, "default")
		wait := time.After(300 * time.Millisecond)
	collect:
		for {
			select {
			case ev := <-n.Events():
				if ev.NoticeID == probe {
					return before
				}
				before = append(before, ev)
			case <-wait:
				break collect
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("syncing %s: the Notifier never reported a click from the current server", tag)
		}
	}
}

// sendResult is what an asynchronous Send returned.
type sendResult struct {
	id  uint32
	err error
}

// lateReply is the fixture of every hostile test of the old owner: the old
// server accepts a system notification for key and holds its reply; a
// second server takes the name (first stays connected) and another
// application is handed, on the new server, the very id the old server is
// about to return; once the Notifier has applied the owner change, the old
// server answers. It returns the servers and that colliding id.
func lateReply(t *testing.T, key string) (n *notify.Notifier, first, second *handoffServer, stale uint32) {
	t.Helper()
	addr := dbustest.Session(t)
	first = startFirstServer(t, addr)
	n = newNotifier(t, addr)

	arrived, release := first.holdNext(t)
	done := make(chan sendResult, 1)
	ctx := s097Ctx(t)
	go func() {
		id, err := n.Send(ctx, systemMsg("Low disk space", key, 0, 1))
		done <- sendResult{id, err}
	}()
	select {
	case stale = <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("fixture: the old server never received the Notify")
	}

	second = replaceServer(t, addr)
	if foreign := foreignNotify(t, addr); foreign != stale {
		t.Fatalf("fixture: the foreign notification on the new server got id %d, want the id %d the old server is about to return", foreign, stale)
	}
	if evs := syncOwner(t, n, second, "handoff"); len(evs) != 0 {
		t.Fatalf("fixture: unexpected events before the handoff probe: %+v", evs)
	}

	release()
	select {
	case r := <-done:
		// The reply itself is not an error: the old server did show it.
		if r.err != nil {
			t.Fatalf("fixture: the Send answered by the old server failed: %v", r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("fixture: the Send answered by the old server never returned")
	}
	return n, first, second, stale
}

// TestS097RecordOwner_LateReplyFromTheOldOwnerIsNotReplaced is R6.15's
// hostile half: the id answered by the server that lost the name must not go
// out as replaces_id; on the new server it is another application's.
func TestS097RecordOwner_LateReplyFromTheOldOwnerIsNotReplaced(t *testing.T) {
	n, first, second, stale := lateReply(t, keyRoot)
	firstCalls := len(first.recorded())

	id, err := n.Send(s097Ctx(t), systemMsg("Low disk space", keyRoot, stale, 2))
	if err != nil {
		t.Fatalf("Send after the handoff: %v", err)
	}
	if c := second.last(t); c.Replaces != 0 || c.Summary != "Low disk space" {
		t.Errorf("last Notify on the new server = replaces_id %d, summary %q: want our message with replaces_id 0 (id %d was answered by the old owner and is another application's on the new one)", c.Replaces, c.Summary, stale)
	}
	if id == stale {
		t.Errorf("Send returned %d, the other application's id on the new server", id)
	}
	if got := len(first.recorded()); got != firstCalls {
		t.Errorf("the old server received %d more Notify calls after it lost the name, want 0", got-firstCalls)
	}
}

// TestS097RecordOwner_LateReplyFromTheOldOwnerIsNotClosed is R6.15 for
// R6.12: withdrawing the condition must not send CloseNotification for the
// id the old owner answered, to either server.
func TestS097RecordOwner_LateReplyFromTheOldOwnerIsNotClosed(t *testing.T) {
	n, first, second, stale := lateReply(t, keyPortage)

	if err := n.Close(s097Ctx(t), stale, keyPortage); err != nil {
		t.Errorf("Close(%d) of a notification answered by the old owner = %v, want nil with no call", stale, err)
	}
	if got := second.closed(); len(got) != 0 {
		t.Errorf("CloseNotification reached the new server: %v (id %d is another application's there)", got, stale)
	}
	if got := first.closed(); len(got) != 0 {
		t.Errorf("CloseNotification reached the old server: %v, want none (it no longer owns the name)", got)
	}
}

// TestS097RecordOwner_LateReplyFromTheOldOwnerIsNotRecorded: the late reply
// is not recorded at all, so a click the new server reports for its own
// notification with that id is not taken for a click on ours.
func TestS097RecordOwner_LateReplyFromTheOldOwnerIsNotRecorded(t *testing.T) {
	n, _, second, stale := lateReply(t, keyMemory)

	second.emit(t, "ActionInvoked", stale, "default") // the other application's notification
	if evs := syncOwner(t, n, second, "after-late-reply"); len(evs) != 0 {
		t.Errorf("a click on the new server's notification %d was reported as ours: %+v (the old owner's late reply was recorded)", stale, evs)
	}
}

// TestS097RecordOwner_HandoffAfterTheReplyDropsTheRecord: a system
// notification answered and recorded BEFORE the handoff stops being
// replaceable or closable once the name has a new owner, even though its
// server is still connected.
func TestS097RecordOwner_HandoffAfterTheReplyDropsTheRecord(t *testing.T) {
	addr := dbustest.Session(t)
	first := startFirstServer(t, addr)
	n := newNotifier(t, addr)

	stale, err := n.Send(s097Ctx(t), systemMsg("CPU saturated", keyCPU, 0, 1))
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	second := replaceServer(t, addr)
	if foreign := foreignNotify(t, addr); foreign != stale {
		t.Fatalf("fixture: the foreign notification on the new server got id %d, want %d", foreign, stale)
	}
	if evs := syncOwner(t, n, second, "handoff"); len(evs) != 0 {
		t.Fatalf("fixture: unexpected events: %+v", evs)
	}
	firstCalls := len(first.recorded())

	if err := n.Close(s097Ctx(t), stale, keyCPU); err != nil {
		t.Errorf("Close(%d) after the handoff = %v, want nil with no call", stale, err)
	}
	if got, old := second.closed(), first.closed(); len(got) != 0 || len(old) != 0 {
		t.Errorf("CloseNotification sent after the handoff: new server %v, old server %v; want none", got, old)
	}
	if _, err := n.Send(s097Ctx(t), systemMsg("CPU saturated", keyCPU, stale, 2)); err != nil {
		t.Fatalf("Send after the handoff: %v", err)
	}
	if c := second.last(t); c.Replaces != 0 {
		t.Errorf("replaces_id = %d on the new server after the handoff, want 0", c.Replaces)
	}
	if got := len(first.recorded()); got != firstCalls {
		t.Errorf("the old server received %d Notify calls after it lost the name, want 0", got-firstCalls)
	}
}

// TestS097RecordOwner_LateReplyFromTheCurrentOwnerIsKept is the converse:
// with no owner change, a reply that arrives late, after other Sends and
// signals of the same server were handled, is still recorded, replaced and
// withdrawn.
func TestS097RecordOwner_LateReplyFromTheCurrentOwnerIsKept(t *testing.T) {
	addr := dbustest.Session(t)
	srv := startFirstServer(t, addr)
	n := newNotifier(t, addr)

	arrived, release := srv.holdNext(t)
	done := make(chan sendResult, 1)
	ctx := s097Ctx(t)
	go func() {
		id, err := n.Send(ctx, systemMsg("Low disk space", keyRoot, 0, 1))
		done <- sendResult{id, err}
	}()
	select {
	case <-arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("fixture: the server never received the Notify")
	}
	other := s097Send(t, n, systemMsg("Memory almost exhausted", keyMemory, 0, 1))
	if evs := syncOwner(t, n, srv, "same-owner"); len(evs) != 0 {
		t.Fatalf("fixture: unexpected events: %+v", evs)
	}
	release()
	var held uint32
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("the held Send failed: %v", r.err)
		}
		held = r.id
	case <-time.After(5 * time.Second):
		t.Fatal("the held Send never returned")
	}

	if got := s097Send(t, n, systemMsg("Low disk space", keyRoot, held, 2)); got != held {
		t.Errorf("the replacement got id %d, want %d", got, held)
	}
	if c := srv.last(t); c.Replaces != held {
		t.Errorf("replaces_id = %d, want %d: the current owner's late reply was dropped", c.Replaces, held)
	}
	if err := n.Close(s097Ctx(t), held, keyRoot); err != nil {
		t.Fatalf("Close(%d): %v", held, err)
	}
	if err := n.Close(s097Ctx(t), other, keyMemory); err != nil {
		t.Fatalf("Close(%d): %v", other, err)
	}
	if got := srv.closed(); !slices.Equal(got, []uint32{held, other}) {
		t.Errorf("CloseNotification calls = %v, want [%d %d]", got, held, other)
	}
}

// TestS097RecordOwner_NoOwnerChangeStillReplacesAndCloses is the 3.1
// contract, unchanged: same key, same owner, replaced and closed exactly
// that id.
func TestS097RecordOwner_NoOwnerChangeStillReplacesAndCloses(t *testing.T) {
	addr := dbustest.Session(t)
	srv := startFirstServer(t, addr)
	n := newNotifier(t, addr)

	id := s097Send(t, n, systemMsg("Low disk space", keyPortage, 0, 1))
	s097Send(t, n, systemMsg("Memory almost exhausted", keyMemory, 0, 1))
	if got := s097Send(t, n, systemMsg("Low disk space", keyPortage, id, 2)); got != id {
		t.Errorf("the replacement got id %d, want %d", got, id)
	}
	if c := srv.last(t); c.Replaces != id {
		t.Errorf("replaces_id = %d, want %d", c.Replaces, id)
	}
	if err := n.Close(s097Ctx(t), id, keyPortage); err != nil {
		t.Fatalf("Close(%d): %v", id, err)
	}
	if got := srv.closed(); !slices.Equal(got, []uint32{id}) {
		t.Errorf("CloseNotification calls = %v, want exactly [%d]", got, id)
	}
}

// TestS097RecordOwner_FreshSendAfterHandoffRecordsUnderTheNewOwner: after
// the handoff (and the old owner's late reply), a fresh Send for the key is
// recorded under the new owner; the next Send for that key replaces it there
// and Close withdraws it there.
func TestS097RecordOwner_FreshSendAfterHandoffRecordsUnderTheNewOwner(t *testing.T) {
	n, first, second, stale := lateReply(t, keyRoot)

	fresh := s097Send(t, n, systemMsg("Low disk space", keyRoot, 0, 2))
	if fresh == stale {
		t.Fatalf("fixture: the fresh notification got the colliding id %d", stale)
	}
	if got := s097Send(t, n, systemMsg("Low disk space", keyRoot, fresh, 2)); got != fresh {
		t.Errorf("the replacement got id %d, want %d", got, fresh)
	}
	if c := second.last(t); c.Replaces != fresh {
		t.Errorf("replaces_id on the new server = %d, want %d (recorded under the new owner)", c.Replaces, fresh)
	}
	if err := n.Close(s097Ctx(t), fresh, keyRoot); err != nil {
		t.Fatalf("Close(%d): %v", fresh, err)
	}
	if got := second.closed(); !slices.Equal(got, []uint32{fresh}) {
		t.Errorf("CloseNotification on the new server = %v, want exactly [%d]", got, fresh)
	}
	if got := first.closed(); len(got) != 0 {
		t.Errorf("CloseNotification reached the old server: %v", got)
	}
}
