package notify_test

// Story 097, sub-task 3.1: system notifications replace their own earlier
// notification (R6.6), are withdrawn with CloseNotification (R6.12) and are
// told apart from notices when clicked (R8.6). A replacement or a withdrawal
// names the notification by id AND condition key: the Notifier acts only on
// an id it recorded under the current server owner for that same key.
//
// Hostile halves come first. A replaces_id or a CloseNotification aimed at an
// id this Notifier does not own on the current server would overwrite or
// remove another application's notification; one aimed at an id a restarted
// server handed to another of OUR notifications would overwrite or remove the
// wrong condition (or a notice); a system click must never be taken for a
// burst-summary click, which also carries an empty NoticeID.
//
// Reuses newNotifier, noticeMsg, nextEvent, notifyCall and the nf* constants
// from notify_test.go.

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/obentoo/bentoolkit/internal/desktop/dbustest"
	"github.com/obentoo/bentoolkit/internal/desktop/notify"
)

// s097Server is an org.freedesktop.Notifications that follows the spec on
// replaces_id (a non-zero one is returned as the notification's id) and on
// CloseNotification (it emits NotificationClosed with reason 3). closeErr,
// when set, is returned by every CloseNotification instead.
type s097Server struct {
	conn     *dbus.Conn
	mu       sync.Mutex
	calls    []notifyCall
	closes   []uint32
	next     uint32
	closeErr *dbus.Error
}

func (f *s097Server) Notify(appName string, replaces uint32, appIcon, summary, body string,
	actions []string, hints map[string]dbus.Variant, timeout int32) (uint32, *dbus.Error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := replaces
	if id == 0 {
		f.next++
		id = f.next
	}
	f.calls = append(f.calls, notifyCall{appName, replaces, appIcon, summary, body, actions, hints, timeout, id})
	return id, nil
}

func (f *s097Server) CloseNotification(id uint32) *dbus.Error {
	f.mu.Lock()
	f.closes = append(f.closes, id)
	closeErr := f.closeErr
	f.mu.Unlock()
	if closeErr != nil {
		return closeErr
	}
	_ = f.conn.Emit(nfPath, nfIface+".NotificationClosed", id, uint32(3))
	return nil
}

func (f *s097Server) GetCapabilities() ([]string, *dbus.Error) {
	return []string{"actions", "body"}, nil
}

func (f *s097Server) GetServerInformation() (string, string, string, string, *dbus.Error) {
	return "s097-fake", "bentoo", "1", "1.2", nil
}

func (f *s097Server) recorded() []notifyCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *s097Server) closed() []uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.closes)
}

func (f *s097Server) setCloseErr(e *dbus.Error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closeErr = e
}

func (f *s097Server) emit(t *testing.T, member string, args ...any) {
	t.Helper()
	if err := f.conn.Emit(nfPath, nfIface+"."+member, args...); err != nil {
		t.Fatalf("emitting %s: %v", member, err)
	}
}

func startS097Server(t *testing.T, addr string) *s097Server {
	t.Helper()
	conn, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	f := &s097Server{conn: conn}
	if err := conn.Export(f, nfPath, nfIface); err != nil {
		t.Fatal(err)
	}
	if reply, err := conn.RequestName(nfName, dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("s097 server RequestName: %v %v", reply, err)
	}
	return f
}

// stopS097Server drops the server's connection, as a crashing daemon does,
// and waits until the bus reports the name has no owner.
func stopS097Server(t *testing.T, addr string, f *s097Server) {
	t.Helper()
	_ = f.conn.Close()
	probe, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = probe.Close() }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var has bool
		if err := probe.BusObject().Call("org.freedesktop.DBus.NameHasOwner", 0, nfName).Store(&has); err != nil {
			t.Fatalf("NameHasOwner: %v", err)
		}
		if !has {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the notification server name still has an owner 3s after its connection closed")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func s097Ctx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func s097Send(t *testing.T, n *notify.Notifier, m notify.Message) uint32 {
	t.Helper()
	id, err := n.Send(s097Ctx(t), m)
	if err != nil {
		t.Fatalf("Send(%q): %v", m.Summary, err)
	}
	return id
}

// Condition keys, as the tray builds them: one per monitor and target.
const (
	keyRoot    = "disk:/"
	keyPortage = "disk:/var/tmp/portage"
	keyMemory  = "memory"
	keyCPU     = "cpu"
)

// systemMsg is a system notification for the condition key: no notice behind
// it, so NoticeID is empty, exactly like a burst summary's.
func systemMsg(summary, key string, replaces uint32, urgency byte) notify.Message {
	return notify.Message{
		Summary: summary, Body: "b", Urgency: urgency, System: true, Key: key, ReplacesID: replaces,
		Actions: []notify.Action{{Key: "default", Label: "Open"}},
	}
}

// lastCall returns the Notify call srv received last.
func lastCall(t *testing.T, srv *s097Server) notifyCall {
	t.Helper()
	calls := srv.recorded()
	if len(calls) == 0 {
		t.Fatal("the server received no Notify call")
	}
	return calls[len(calls)-1]
}

// syncWith proves every signal srv emitted before this call has been
// handled by n, and that n follows srv as the current owner: it sends a probe,
// has srv click it and waits for the click. The Notifier believes only the
// current owner's signals and handles them in order, so the event arrives
// only after every earlier signal and owner change was applied.
func syncWith(t *testing.T, n *notify.Notifier, srv *s097Server, tag string) {
	t.Helper()
	probe := "probe-" + tag
	deadline := time.Now().Add(3 * time.Second)
	for {
		id := s097Send(t, n, noticeMsg(probe, probe, "b", 0))
		srv.emit(t, "ActionInvoked", id, "default")
		select {
		case ev := <-n.Events():
			if ev.NoticeID == probe {
				return
			}
			t.Fatalf("syncing: unexpected event %+v", ev)
		case <-time.After(300 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatalf("syncing %s: the Notifier never reported a click from the current server", tag)
		}
	}
}

// noEvent fails if n reports any event within d.
func noEvent(t *testing.T, n *notify.Notifier, d time.Duration) {
	t.Helper()
	select {
	case ev := <-n.Events():
		t.Fatalf("unexpected event %+v", ev)
	case <-time.After(d):
	}
}

// TestS097Notify_ReplacesIDNeverTargetsAForeignID is R6.6's hostile half: an
// id this Notifier never received from the server belongs to another
// application, and must go out as replaces_id 0.
func TestS097Notify_ReplacesIDNeverTargetsAForeignID(t *testing.T) {
	addr := dbustest.Session(t)
	srv := startS097Server(t, addr)
	n := newNotifier(t, addr)

	own := s097Send(t, n, systemMsg("Low disk space on /", keyRoot, 0, 1))
	foreign := own + 41
	id := s097Send(t, n, systemMsg("Low disk space on /", keyRoot, foreign, 2))
	if c := lastCall(t, srv); c.Replaces != 0 {
		t.Errorf("replaces_id = %d for an id this Notifier never sent, want 0 (another application's notification would be overwritten)", c.Replaces)
	}
	if id == foreign {
		t.Errorf("Send returned the foreign id %d", foreign)
	}
}

// TestS097Notify_ReplacesIDOfAClosedNotificationIsZero: once the server has
// reported the notification closed, its id is no longer this Notifier's to
// replace.
func TestS097Notify_ReplacesIDOfAClosedNotificationIsZero(t *testing.T) {
	addr := dbustest.Session(t)
	srv := startS097Server(t, addr)
	n := newNotifier(t, addr)

	id := s097Send(t, n, systemMsg("Memory almost exhausted", keyMemory, 0, 1))
	srv.emit(t, "NotificationClosed", id, uint32(2)) // dismissed by the user
	syncWith(t, n, srv, "closed")

	s097Send(t, n, systemMsg("Memory almost exhausted", keyMemory, id, 2))
	if c := lastCall(t, srv); c.Replaces != 0 {
		t.Errorf("replaces_id = %d for a notification the server reported closed, want 0", c.Replaces)
	}
}

// TestS097Notify_StaleReplacesIDAfterServerRestartIsZero is the design's
// replace gotcha: a restarted server numbers from 1 again, so the id an
// earlier server assigned now names another application's notification.
func TestS097Notify_StaleReplacesIDAfterServerRestartIsZero(t *testing.T) {
	addr := dbustest.Session(t)
	first := startS097Server(t, addr)
	n := newNotifier(t, addr)

	stale := s097Send(t, n, systemMsg("Low disk space on /var/tmp/portage", keyPortage, 0, 1))
	stopS097Server(t, addr, first)
	second := startS097Server(t, addr)

	// Another application is first on the new server and is handed the id
	// the old server gave us.
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
	syncWith(t, n, second, "restart")

	s097Send(t, n, systemMsg("Low disk space on /var/tmp/portage", keyPortage, stale, 2))
	if c := lastCall(t, second); c.Replaces != 0 {
		t.Errorf("replaces_id = %d after the server restarted, want 0: id %d now belongs to another application", c.Replaces, stale)
	}
}

// TestS097Notify_ReplacesIDOfALiveOwnNotificationIsSent is R6.6's benign
// half: a later notification for the same condition replaces the earlier one
// and keeps its id.
func TestS097Notify_ReplacesIDOfALiveOwnNotificationIsSent(t *testing.T) {
	addr := dbustest.Session(t)
	srv := startS097Server(t, addr)
	n := newNotifier(t, addr)

	first := s097Send(t, n, systemMsg("Low disk space on /", keyRoot, 0, 1))
	second := s097Send(t, n, systemMsg("Low disk space on /", keyRoot, first, 2))
	if c := lastCall(t, srv); c.Replaces != first {
		t.Errorf("replaces_id = %d, want %d (the earlier notification of the same condition)", c.Replaces, first)
	}
	if second != first {
		t.Errorf("Send returned %d, want the replaced id %d", second, first)
	}
	// A notice sent without ReplacesID is never a replacement.
	s097Send(t, n, noticeMsg("2026-10-14-notice", "N", "b", 1))
	if c := lastCall(t, srv); c.Replaces != 0 {
		t.Errorf("a message without ReplacesID went out with replaces_id %d, want 0", c.Replaces)
	}
}

// TestS097Notify_SystemClickIsNotASummaryClick is R8.6's hostile half: a
// system notification and a burst summary both have an empty NoticeID, and
// only Event.System tells the tray which one was clicked.
func TestS097Notify_SystemClickIsNotASummaryClick(t *testing.T) {
	addr := dbustest.Session(t)
	srv := startS097Server(t, addr)
	n := newNotifier(t, addr)

	sys := s097Send(t, n, systemMsg("Low disk space on /", keyRoot, 0, 2))
	summary := s097Send(t, n, notify.Message{
		Summary: "5 new notices", Body: "b", Urgency: 1,
		Actions: []notify.Action{{Key: "default", Label: "Open"}},
	})
	notice := s097Send(t, n, noticeMsg("2026-10-14-cve", "CVE", "b", 2))

	srv.emit(t, "ActionInvoked", sys, "default")
	srv.emit(t, "ActionInvoked", summary, "default")
	srv.emit(t, "ActionInvoked", notice, "default")

	if ev := nextEvent(t, n); !ev.System || ev.NoticeID != "" || ev.Action != "default" {
		t.Errorf("system click: event = %+v, want System=true, empty NoticeID, action default", ev)
	}
	if ev := nextEvent(t, n); ev.System || ev.NoticeID != "" || ev.Action != "default" {
		t.Errorf("summary click: event = %+v, want System=false (a summary click opens the notices index)", ev)
	}
	if ev := nextEvent(t, n); ev.System || ev.NoticeID != "2026-10-14-cve" {
		t.Errorf("notice click: event = %+v, want System=false for notice 2026-10-14-cve", ev)
	}
}

// TestS097Notify_ReplacedSystemNotificationStaysSystem is the converse: a
// replacement keeps one notification, so one click yields one system event.
func TestS097Notify_ReplacedSystemNotificationStaysSystem(t *testing.T) {
	addr := dbustest.Session(t)
	srv := startS097Server(t, addr)
	n := newNotifier(t, addr)

	id := s097Send(t, n, systemMsg("Low disk space on /", keyRoot, 0, 1))
	if again := s097Send(t, n, systemMsg("Low disk space on /", keyRoot, id, 2)); again != id {
		t.Fatalf("the replacement got id %d, want %d", again, id)
	}
	srv.emit(t, "ActionInvoked", id, "default")
	if ev := nextEvent(t, n); !ev.System || ev.NoticeID != "" {
		t.Errorf("click on a replaced system notification: event = %+v, want System=true", ev)
	}
	noEvent(t, n, 300*time.Millisecond)
}

// TestS097Notify_CloseNeverTargetsAStaleID is R6.12's hostile half: after a
// server restart the withdrawn condition's id names another application's
// notification, which Close must leave alone; it is not an error.
func TestS097Notify_CloseNeverTargetsAStaleID(t *testing.T) {
	addr := dbustest.Session(t)
	first := startS097Server(t, addr)
	n := newNotifier(t, addr)

	stale := s097Send(t, n, systemMsg("CPU saturated", keyCPU, 0, 1))
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
	syncWith(t, n, second, "close-restart")

	if err := n.Close(s097Ctx(t), stale, keyCPU); err != nil {
		t.Errorf("Close of a stale id: %v, want nil (the notification is already gone)", err)
	}
	if got := second.closed(); slices.Contains(got, stale) {
		t.Errorf("CloseNotification(%d) reached the new server, closing another application's notification (closes: %v)", stale, got)
	}
}

// TestS097Notify_CloseWithdrawsExactlyThatNotification is R6.12's benign
// half: Close asks the server to close that id and no other.
func TestS097Notify_CloseWithdrawsExactlyThatNotification(t *testing.T) {
	addr := dbustest.Session(t)
	srv := startS097Server(t, addr)
	n := newNotifier(t, addr)

	keep := s097Send(t, n, systemMsg("Memory almost exhausted", keyMemory, 0, 2))
	gone := s097Send(t, n, systemMsg("Low disk space on /", keyRoot, 0, 1))
	if err := n.Close(s097Ctx(t), gone, keyRoot); err != nil {
		t.Fatalf("Close(%d): %v", gone, err)
	}
	if got := srv.closed(); !slices.Equal(got, []uint32{gone}) {
		t.Errorf("CloseNotification calls = %v, want exactly [%d] (not %d)", got, gone, keep)
	}

	// The server's NotificationClosed for the withdrawn id ends it: a later
	// click on that id is not reported, one on the kept id still is.
	srv.emit(t, "ActionInvoked", gone, "default")
	srv.emit(t, "ActionInvoked", keep, "default")
	if ev := nextEvent(t, n); !ev.System {
		t.Errorf("event = %+v, want the kept system notification's click", ev)
	}
	noEvent(t, n, 300*time.Millisecond)
}

// TestS097Notify_CloseUnknownIDIsNotAnError: an id the server already
// expired, or never had, is not an error.
func TestS097Notify_CloseUnknownIDIsNotAnError(t *testing.T) {
	addr := dbustest.Session(t)
	startS097Server(t, addr)
	n := newNotifier(t, addr)
	if err := n.Close(s097Ctx(t), 4242, keyRoot); err != nil {
		t.Errorf("Close(4242) of an unknown id = %v, want nil", err)
	}
}

// TestS097Notify_CloseReturnsTheServerErrorWrapped is Q4: some servers answer
// CloseNotification with an error; Close returns it with its cause kept, and
// the message says what was being closed.
func TestS097Notify_CloseReturnsTheServerErrorWrapped(t *testing.T) {
	addr := dbustest.Session(t)
	srv := startS097Server(t, addr)
	n := newNotifier(t, addr)

	id := s097Send(t, n, systemMsg("Low disk space on /", keyRoot, 0, 1))
	srv.setCloseErr(dbus.NewError("org.freedesktop.DBus.Error.Failed", []any{"no such notification"}))
	err := n.Close(s097Ctx(t), id, keyRoot)
	if err == nil {
		t.Fatal("Close returned nil although the server answered with an error")
	}
	var dbusErr dbus.Error
	if !errors.As(err, &dbusErr) {
		var dbusErrPtr *dbus.Error
		if !errors.As(err, &dbusErrPtr) {
			t.Errorf("Close error %q does not wrap the server's dbus.Error", err)
		}
	}
	msg := err.Error()
	if !strings.Contains(msg, strconv.FormatUint(uint64(id), 10)) && !strings.Contains(msg, nfName) {
		t.Errorf("Close error %q names neither the id %d nor %s", msg, id, nfName)
	}
}

// TestS097Notify_CloseWithNoServerReturns: with nobody owning the name the
// call fails or there is nothing to withdraw; Close must return either way.
func TestS097Notify_CloseWithNoServerReturns(t *testing.T) {
	addr := dbustest.Session(t)
	srv := startS097Server(t, addr)
	n := newNotifier(t, addr)
	id := s097Send(t, n, systemMsg("Low disk space on /", keyRoot, 0, 1))
	stopS097Server(t, addr, srv)

	// The Notifier may not have heard the owner change yet; either way the
	// notification is gone, so nil (nothing to withdraw) or an error (the
	// call failed) are both acceptable. What is not acceptable is a hang.
	done := make(chan error, 1)
	go func() { done <- n.Close(s097Ctx(t), id, keyRoot) }()
	select {
	case <-done:
	case <-time.After(6 * time.Second):
		t.Fatal("Close did not return within 6s with no server on the bus")
	}
}

// TestS097Notify_StaleIDHeldByOurOtherNotificationIsLeftAlone is the own
// collision after a restart: the new server numbers from 1 again and hands
// the stale id to ANOTHER of this Notifier's notifications, a different
// condition or a notice. The id is ours under the current owner, but not for
// this key, so the disk condition's replacement goes out as a new
// notification and its withdrawal closes nothing.
func TestS097Notify_StaleIDHeldByOurOtherNotificationIsLeftAlone(t *testing.T) {
	for _, tc := range []struct {
		name  string
		other notify.Message
		check func(t *testing.T, ev notify.Event)
	}{
		{
			name:  "another condition",
			other: systemMsg("Memory almost exhausted", keyMemory, 0, 2),
			check: func(t *testing.T, ev notify.Event) {
				if !ev.System || ev.NoticeID != "" {
					t.Errorf("click on the memory notification: event = %+v, want System=true", ev)
				}
			},
		},
		{
			name:  "a notice",
			other: noticeMsg("2026-10-14-cve", "CVE", "b", 2),
			check: func(t *testing.T, ev notify.Event) {
				if ev.System || ev.NoticeID != "2026-10-14-cve" {
					t.Errorf("click on the notice: event = %+v, want notice 2026-10-14-cve", ev)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			addr := dbustest.Session(t)
			first := startS097Server(t, addr)
			n := newNotifier(t, addr)

			stale := s097Send(t, n, systemMsg("Low disk space on /", keyRoot, 0, 1))
			stopS097Server(t, addr, first)
			second := startS097Server(t, addr)

			if got := s097Send(t, n, tc.other); got != stale {
				t.Fatalf("fixture: our other notification got id %d, want the stale id %d", got, stale)
			}
			syncWith(t, n, second, "own-collision")

			s097Send(t, n, systemMsg("Low disk space on /", keyRoot, stale, 2))
			if c := lastCall(t, second); c.Replaces != 0 {
				t.Errorf("replaces_id = %d: the disk condition overwrote our %s, which the new server numbered %d", c.Replaces, tc.name, stale)
			}
			if err := n.Close(s097Ctx(t), stale, keyRoot); err != nil {
				t.Errorf("Close(%d, %q) = %v, want nil (nothing of this key to withdraw)", stale, keyRoot, err)
			}
			if got := second.closed(); slices.Contains(got, stale) {
				t.Errorf("CloseNotification(%d) withdrew our %s (closes: %v)", stale, tc.name, got)
			}

			// Our other notification is still ours and still clickable.
			second.emit(t, "ActionInvoked", stale, "default")
			tc.check(t, nextEvent(t, n))
		})
	}
}

// TestS097Notify_KeyMismatchOnALiveOwnIDIsIgnored: with no restart at all,
// an id recorded for one condition is not another condition's to replace or
// withdraw. The same id with its own key is (the benign half).
func TestS097Notify_KeyMismatchOnALiveOwnIDIsIgnored(t *testing.T) {
	addr := dbustest.Session(t)
	srv := startS097Server(t, addr)
	n := newNotifier(t, addr)

	id := s097Send(t, n, systemMsg("Low disk space on /", keyRoot, 0, 1))
	s097Send(t, n, systemMsg("Memory almost exhausted", keyMemory, id, 2))
	if c := lastCall(t, srv); c.Replaces != 0 {
		t.Errorf("replaces_id = %d: the memory condition replaced the disk condition's notification", c.Replaces)
	}
	if err := n.Close(s097Ctx(t), id, keyMemory); err != nil {
		t.Errorf("Close with a mismatched key = %v, want nil", err)
	}
	if got := srv.closed(); len(got) != 0 {
		t.Errorf("CloseNotification calls = %v for a mismatched key, want none", got)
	}

	if err := n.Close(s097Ctx(t), id, keyRoot); err != nil {
		t.Fatalf("Close(%d, %q): %v", id, keyRoot, err)
	}
	if got := srv.closed(); !slices.Equal(got, []uint32{id}) {
		t.Errorf("CloseNotification calls = %v, want exactly [%d] for the matching key", got, id)
	}
}

// TestS097Notify_EmptyKeyNeverReplaces: notices carry no key, and an empty
// key names no condition, so it neither replaces nor withdraws anything, even
// an id recorded with an empty key too.
func TestS097Notify_EmptyKeyNeverReplaces(t *testing.T) {
	addr := dbustest.Session(t)
	srv := startS097Server(t, addr)
	n := newNotifier(t, addr)

	notice := s097Send(t, n, noticeMsg("2026-10-14-a", "A", "b", 1))
	again := noticeMsg("2026-10-14-b", "B", "b", 1)
	again.ReplacesID = notice
	s097Send(t, n, again)
	if c := lastCall(t, srv); c.Replaces != 0 {
		t.Errorf("a notice with ReplacesID %d went out with replaces_id %d, want 0 (empty key)", notice, c.Replaces)
	}

	sys := s097Send(t, n, systemMsg("Low disk space on /", "", 0, 1))
	s097Send(t, n, systemMsg("Low disk space on /", "", sys, 2))
	if c := lastCall(t, srv); c.Replaces != 0 {
		t.Errorf("a keyless system message went out with replaces_id %d, want 0", c.Replaces)
	}

	for _, id := range []uint32{notice, sys} {
		if err := n.Close(s097Ctx(t), id, ""); err != nil {
			t.Errorf("Close(%d, \"\") = %v, want nil", id, err)
		}
	}
	if got := srv.closed(); len(got) != 0 {
		t.Errorf("CloseNotification calls = %v with an empty key, want none", got)
	}
}
