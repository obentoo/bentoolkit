package notify_test

// Story 097, sub-task 3.1, tech review fix round 1: the races between a
// replacement and the signals or owner changes around it.
//
// A replaced notification can end just as its replacement is sent: the user
// dismisses it, or it expires. dunst and Plasma then keep the replaces_id for
// the replacement, so the server's NotificationClosed for the OLD notification
// and the reply naming the NEW one carry the same id, the signal first. The
// reader and Send race for the Notifier's lock, and when Send records first
// the old signal must not delete the replacement's record. A fresh id is the
// converse: a NotificationClosed older than its reply is that notification
// closed at once, and still ends it.
//
// Reuses newNotifier, noticeMsg and the nf* constants from notify_test.go and
// the s097 helpers from notify_system_s097_test.go.

import (
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/obentoo/bentoolkit/internal/desktop/dbustest"
	"github.com/obentoo/bentoolkit/internal/desktop/notify"
)

// raceRounds is how many replacements each race test sends. Before the fix
// about one in four replacements lost its record, so a hundred rounds fail
// with near certainty; after it, none may.
const raceRounds = 100

// raceServer is an org.freedesktop.Notifications whose replaced notification
// has just been dismissed: a Notify with a non-zero replaces_id emits
// NotificationClosed for that id, then returns the same id for the
// replacement. With closeAtOnce, a fresh notification is closed before its
// reply leaves. CloseNotification records the id and emits
// NotificationClosed with reason 3, as the spec says.
type raceServer struct {
	conn        *dbus.Conn
	mu          sync.Mutex
	next        uint32
	closeAtOnce bool
	replaces    []uint32
	closes      []uint32
}

func (f *raceServer) Notify(appName string, replaces uint32, appIcon, summary, body string,
	actions []string, hints map[string]dbus.Variant, timeout int32) (uint32, *dbus.Error) {
	f.mu.Lock()
	f.replaces = append(f.replaces, replaces)
	id, fresh := replaces, replaces == 0
	if fresh {
		f.next++
		id = f.next
	}
	closeFirst := !fresh || f.closeAtOnce
	f.mu.Unlock()
	if closeFirst {
		// Reason 2: dismissed by the user.
		_ = f.conn.Emit(nfPath, nfIface+".NotificationClosed", id, uint32(2))
	}
	return id, nil
}

func (f *raceServer) CloseNotification(id uint32) *dbus.Error {
	f.mu.Lock()
	f.closes = append(f.closes, id)
	f.mu.Unlock()
	_ = f.conn.Emit(nfPath, nfIface+".NotificationClosed", id, uint32(3))
	return nil
}

func (f *raceServer) GetCapabilities() ([]string, *dbus.Error) {
	return []string{"actions", "body"}, nil
}

func (f *raceServer) setCloseAtOnce(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closeAtOnce = v
}

func (f *raceServer) lastReplaces(t *testing.T) uint32 {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.replaces) == 0 {
		t.Fatal("the server received no Notify call")
	}
	return f.replaces[len(f.replaces)-1]
}

func (f *raceServer) closed() []uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.closes)
}

func (f *raceServer) emit(t *testing.T, member string, args ...any) {
	t.Helper()
	if err := f.conn.Emit(nfPath, nfIface+"."+member, args...); err != nil {
		t.Fatalf("emitting %s: %v", member, err)
	}
}

func startRaceServer(t *testing.T, addr string) *raceServer {
	t.Helper()
	conn, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	f := &raceServer{conn: conn}
	if err := conn.Export(f, nfPath, nfIface); err != nil {
		t.Fatal(err)
	}
	if reply, err := conn.RequestName(nfName, dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("race server RequestName: %v %v", reply, err)
	}
	return f
}

// syncRace proves every signal srv emitted before this call has been applied
// by n: a fresh probe notice is clicked, and the Notifier handles the
// server's signals in order. srv must not have closeAtOnce set.
func syncRace(t *testing.T, n *notify.Notifier, srv *raceServer, tag string) {
	t.Helper()
	probe := "probe-" + tag
	id := s097Send(t, n, noticeMsg(probe, probe, "b", 0))
	srv.emit(t, "ActionInvoked", id, "default")
	select {
	case ev := <-n.Events():
		if ev.NoticeID != probe {
			t.Fatalf("syncing %s: unexpected event %+v", tag, ev)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("syncing %s: the probe click was never reported", tag)
	}
}

// TestS097NotifyReplaceRace_OldCloseKeepsTheReplacementClickable: the
// NotificationClosed of the replaced notification must not delete the
// replacement's record, whichever of the reader and Send takes the lock
// first; a click on the replacement is still reported.
func TestS097NotifyReplaceRace_OldCloseKeepsTheReplacementClickable(t *testing.T) {
	addr := dbustest.Session(t)
	srv := startRaceServer(t, addr)
	n := newNotifier(t, addr)

	for i := range raceRounds {
		key := "disk:/race/" + strconv.Itoa(i)
		id := s097Send(t, n, systemMsg("Low disk space", key, 0, 1))
		if got := s097Send(t, n, systemMsg("Low disk space", key, id, 2)); got != id {
			t.Fatalf("round %d: the replacement got id %d, want %d", i, got, id)
		}
		srv.emit(t, "ActionInvoked", id, "default")
		select {
		case ev := <-n.Events():
			if !ev.System || ev.NoticeID != "" {
				t.Fatalf("round %d: event = %+v, want the replacement's system click", i, ev)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("round %d: the click on replacement %d was never reported: the replaced notification's NotificationClosed deleted the replacement's record", i, id)
		}
	}
}

// TestS097NotifyReplaceRace_ReplacementCanStillBeWithdrawn: once the old
// NotificationClosed has been handled, Close of the replacement still reaches
// the server; otherwise the replacement stays on screen for good.
func TestS097NotifyReplaceRace_ReplacementCanStillBeWithdrawn(t *testing.T) {
	addr := dbustest.Session(t)
	srv := startRaceServer(t, addr)
	n := newNotifier(t, addr)

	for i := range raceRounds {
		key := "memory/race/" + strconv.Itoa(i)
		id := s097Send(t, n, systemMsg("Memory almost exhausted", key, 0, 1))
		s097Send(t, n, systemMsg("Memory almost exhausted", key, id, 2))
		syncRace(t, n, srv, "withdraw-"+strconv.Itoa(i))
		if err := n.Close(s097Ctx(t), id, key); err != nil {
			t.Fatalf("round %d: Close(%d, %q): %v", i, id, key, err)
		}
		if got := srv.closed(); len(got) != i+1 || got[i] != id {
			t.Fatalf("round %d: CloseNotification calls = %v, want %d last: the live replacement was not withdrawn", i, got, id)
		}
	}
}

// TestS097NotifyReplaceRace_FreshIDClosedBeforeItsReplyIsGone is the
// converse: a fresh notification the server closed before its reply left is
// over, whichever of the reader and Send takes the lock first. It is neither
// replaced, withdrawn nor reported when clicked.
func TestS097NotifyReplaceRace_FreshIDClosedBeforeItsReplyIsGone(t *testing.T) {
	addr := dbustest.Session(t)
	srv := startRaceServer(t, addr)
	n := newNotifier(t, addr)

	for i := range 20 {
		key := "cpu/race/" + strconv.Itoa(i)
		srv.setCloseAtOnce(true)
		id := s097Send(t, n, systemMsg("CPU saturated", key, 0, 1))
		srv.setCloseAtOnce(false)
		syncRace(t, n, srv, "fresh-"+strconv.Itoa(i))

		if err := n.Close(s097Ctx(t), id, key); err != nil {
			t.Fatalf("round %d: Close(%d) of a notification closed at once = %v, want nil", i, id, err)
		}
		if got := srv.closed(); slices.Contains(got, id) {
			t.Fatalf("round %d: CloseNotification(%d) sent for a notification the server closed at once (closes: %v)", i, id, got)
		}
		s097Send(t, n, systemMsg("CPU saturated", key, id, 2))
		if r := srv.lastReplaces(t); r != 0 {
			t.Fatalf("round %d: replaces_id = %d for a notification the server closed at once, want 0", i, r)
		}
		srv.emit(t, "ActionInvoked", id, "default")
		noEvent(t, n, 100*time.Millisecond)
	}
}

// TestS097NotifyOwnerRace_ReplaceAndCloseNeverReachTheNewOwner: the server
// restarts between the ownership check of a Send or Close and its bus call,
// and the new server hands the checked id to another application. The call
// must go to the owner that was checked, never by the well-known name to the
// new one: the replacement goes out again as a new notification, the
// withdrawal fails, and the other application's notification is untouched.
func TestS097NotifyOwnerRace_ReplaceAndCloseNeverReachTheNewOwner(t *testing.T) {
	addr := dbustest.Session(t)
	first := startS097Server(t, addr)
	n := newNotifier(t, addr)

	stale := s097Send(t, n, systemMsg("Low disk space on /", keyRoot, 0, 1))
	checked := notify.CaptureOwnerView(n, stale)
	stopS097Server(t, addr, first)
	second := startS097Server(t, addr)

	other, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Close() })
	var foreign uint32
	if err := other.Object(nfName, nfPath).Call(nfIface+".Notify", 0,
		"other-app", uint32(0), "", "theirs", "", []string{}, map[string]dbus.Variant{}, int32(-1)).Store(&foreign); err != nil {
		t.Fatalf("foreign Notify: %v", err)
	}
	if foreign != stale {
		t.Fatalf("fixture: the foreign notification got id %d, want the stale id %d", foreign, stale)
	}
	syncWith(t, n, second, "owner-race")

	notify.RestoreOwnerView(n, checked)
	id, err := n.Send(s097Ctx(t), systemMsg("Low disk space on /", keyRoot, stale, 2))
	if err != nil {
		t.Fatalf("Send after the checked owner left: %v, want the message sent again as a new notification", err)
	}
	if c := lastCall(t, second); c.Replaces != 0 || c.Summary != "Low disk space on /" {
		t.Errorf("last Notify on the new server = replaces_id %d, summary %q: want our message with replaces_id 0 (id %d is another application's)", c.Replaces, c.Summary, stale)
	}
	if id == stale {
		t.Errorf("Send returned %d, the other application's id", id)
	}

	notify.RestoreOwnerView(n, checked)
	if err := n.Close(s097Ctx(t), stale, keyRoot); err == nil {
		t.Errorf("Close(%d) to the owner that left returned nil, want the call's error", stale)
	}
	if got := second.closed(); slices.Contains(got, stale) {
		t.Errorf("CloseNotification(%d) reached the new server, closing another application's notification (closes: %v)", stale, got)
	}
}
