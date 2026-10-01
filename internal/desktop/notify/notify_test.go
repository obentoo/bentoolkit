package notify_test

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

	"github.com/obentoo/bentoolkit/internal/desktop/dbustest"
	"github.com/obentoo/bentoolkit/internal/desktop/notify"
)

const (
	nfName  = "org.freedesktop.Notifications"
	nfPath  = dbus.ObjectPath("/org/freedesktop/Notifications")
	nfIface = "org.freedesktop.Notifications"
)

// notifyCall is one recorded Notify invocation.
type notifyCall struct {
	AppName   string
	Replaces  uint32
	AppIcon   string
	Summary   string
	Body      string
	Actions   []string
	Hints     map[string]dbus.Variant
	Timeout   int32
	ReturnsID uint32
}

// fakeServer is a minimal org.freedesktop.Notifications on its own connection.
type fakeServer struct {
	conn  *dbus.Conn
	mu    sync.Mutex
	calls []notifyCall
	next  uint32
}

func (f *fakeServer) Notify(appName string, replaces uint32, appIcon, summary, body string,
	actions []string, hints map[string]dbus.Variant, timeout int32) (uint32, *dbus.Error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	f.calls = append(f.calls, notifyCall{appName, replaces, appIcon, summary, body, actions, hints, timeout, f.next})
	return f.next, nil
}

func (f *fakeServer) GetCapabilities() ([]string, *dbus.Error) {
	return []string{"actions", "body", "body-markup"}, nil
}

func (f *fakeServer) GetServerInformation() (string, string, string, string, *dbus.Error) {
	return "fake", "bentoo", "1", "1.2", nil
}

func (f *fakeServer) CloseNotification(id uint32) *dbus.Error { return nil }

func (f *fakeServer) recorded() []notifyCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *fakeServer) emit(t *testing.T, member string, args ...any) {
	t.Helper()
	if err := f.conn.Emit(nfPath, nfIface+"."+member, args...); err != nil {
		t.Fatalf("emitting %s: %v", member, err)
	}
}

func startServer(t *testing.T, addr string) *fakeServer {
	t.Helper()
	conn, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	f := &fakeServer{conn: conn}
	if err := conn.Export(f, nfPath, nfIface); err != nil {
		t.Fatal(err)
	}
	if reply, err := conn.RequestName(nfName, dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("fake server RequestName: %v %v", reply, err)
	}
	return f
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func newNotifier(t *testing.T, addr string) *notify.Notifier {
	t.Helper()
	conn, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	n, err := notify.New(conn, slog.New(slog.NewTextHandler(&lockedBuffer{}, nil)))
	if err != nil {
		t.Fatalf("notify.New: %v", err)
	}
	return n
}

func noticeMsg(id, summary, body string, urgency byte) notify.Message {
	return notify.Message{
		NoticeID: id, Summary: summary, Body: body, Urgency: urgency,
		Actions: []notify.Action{{Key: "default", Label: "Open"}, {Key: "mark-read", Label: "Mark as read"}},
	}
}

// TestNotify_SendCarriesHintsActionsAndEscapedText is R6.5, R6.6 and R6.7 on
// the wire.
func TestNotify_SendCarriesHintsActionsAndEscapedText(t *testing.T) {
	addr := dbustest.Session(t)
	srv := startServer(t, addr)
	n := newNotifier(t, addr)

	id, err := n.Send(context.Background(), noticeMsg("2026-10-02-foo-cve", "foo <1.2.3> & bar", "<b>upgrade</b> & reboot", 2))
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	calls := srv.recorded()
	if len(calls) != 1 {
		t.Fatalf("server saw %d Notify calls, want 1", len(calls))
	}
	c := calls[0]
	if id != c.ReturnsID {
		t.Errorf("Send returned id %d, the server assigned %d", id, c.ReturnsID)
	}
	if c.AppName != "bentoo" || c.AppIcon != "bentoo-tray" || c.Replaces != 0 || c.Timeout != -1 {
		t.Errorf("app_name/app_icon/replaces_id/timeout = %q/%q/%d/%d, want bentoo/bentoo-tray/0/-1",
			c.AppName, c.AppIcon, c.Replaces, c.Timeout)
	}
	if c.Summary != "foo <1.2.3> & bar" {
		t.Errorf("summary = %q, want it plain (R6.14)", c.Summary)
	}
	// fakeServer advertises body-markup, so the body is escaped (R6.6).
	if c.Body != "&lt;b&gt;upgrade&lt;/b&gt; &amp; reboot" {
		t.Errorf("body = %q, want it escaped", c.Body)
	}
	if want := []string{"default", "Open", "mark-read", "Mark as read"}; !slices.Equal(c.Actions, want) {
		t.Errorf("actions = %q, want %q", c.Actions, want)
	}
	if u, ok := c.Hints["urgency"].Value().(byte); !ok || u != 2 {
		t.Errorf("urgency hint = %#v, want byte 2", c.Hints["urgency"].Value())
	}
	if de, ok := c.Hints["desktop-entry"].Value().(string); !ok || de != "bentoo-tray" {
		t.Errorf("desktop-entry hint = %#v, want \"bentoo-tray\"", c.Hints["desktop-entry"].Value())
	}
}

// TestNotify_UrgencyAndActionsArePassedThrough: every urgency reaches the
// server as sent, and a summary notification's single action stays single.
func TestNotify_UrgencyAndActionsArePassedThrough(t *testing.T) {
	addr := dbustest.Session(t)
	srv := startServer(t, addr)
	n := newNotifier(t, addr)
	for _, u := range []byte{0, 1, 2} {
		if _, err := n.Send(context.Background(), noticeMsg("2026-10-02-u", "s", "b", u)); err != nil {
			t.Fatal(err)
		}
	}
	summary := notify.Message{Summary: "5 new notices", Body: "b", Urgency: 1, Actions: []notify.Action{{Key: "default", Label: "Open"}}}
	if _, err := n.Send(context.Background(), summary); err != nil {
		t.Fatal(err)
	}
	calls := srv.recorded()
	if len(calls) != 4 {
		t.Fatalf("server saw %d calls, want 4", len(calls))
	}
	for i, u := range []byte{0, 1, 2} {
		if got, _ := calls[i].Hints["urgency"].Value().(byte); got != u {
			t.Errorf("call %d urgency = %d, want %d", i, got, u)
		}
	}
	if !slices.Equal(calls[3].Actions, []string{"default", "Open"}) {
		t.Errorf("summary actions = %q, want only [default Open]", calls[3].Actions)
	}
}

// TestEscape_EscapesMarkupOnly is R6.6, including the hostile case: text that
// already looks like an entity is notice-supplied text, not markup, and must
// be escaped too.
func TestEscape_EscapesMarkupOnly(t *testing.T) {
	cases := map[string]string{
		"a & b":            "a &amp; b",
		"<script>":         "&lt;script&gt;",
		"&lt;":             "&amp;lt;",
		"&amp;":            "&amp;amp;",
		`quotes " and '`:   `quotes " and '`,
		"":                 "",
		"plain text 1.2.3": "plain text 1.2.3",
	}
	for in, want := range cases {
		if got := notify.Escape(in); got != want {
			t.Errorf("Escape(%q) = %q, want %q", in, got, want)
		}
	}
}

func nextEvent(t *testing.T, n *notify.Notifier) notify.Event {
	t.Helper()
	select {
	case ev := <-n.Events():
		return ev
	case <-time.After(3 * time.Second):
		t.Fatal("no notification event within 3s")
		return notify.Event{}
	}
}

// TestNotify_EventsBelongToTheirOwnNotification is the hostile half of the
// broadcast GOTCHA: signals for another application's notification are
// ignored, a malformed signal does not kill the reader, and a token is
// attached only to the action of the notification it was issued for.
func TestNotify_EventsBelongToTheirOwnNotification(t *testing.T) {
	addr := dbustest.Session(t)
	srv := startServer(t, addr)
	n := newNotifier(t, addr)
	ctx := context.Background()
	idA, err := n.Send(ctx, noticeMsg("2026-10-02-a", "A", "a", 1))
	if err != nil {
		t.Fatal(err)
	}
	idB, err := n.Send(ctx, noticeMsg("2026-10-02-b", "B", "b", 1))
	if err != nil {
		t.Fatal(err)
	}

	srv.emit(t, "ActionInvoked", uint32(9999), "default") // another application's notification
	srv.emit(t, "ActionInvoked", idA)                     // malformed: one argument
	srv.emit(t, "ActivationToken", idB, "token-for-b")    // token for B arrives first
	srv.emit(t, "ActionInvoked", idA, "default")          // A opened: must not carry B's token
	srv.emit(t, "ActionInvoked", idB, "default")          // B opened: carries its token
	srv.emit(t, "ActionInvoked", idB, "mark-read")

	ev := nextEvent(t, n)
	if ev.NoticeID != "2026-10-02-a" || ev.Action != "default" {
		t.Fatalf("first event = %+v, want A/default (a foreign or malformed signal leaked through)", ev)
	}
	if ev.ActivationToken != "" {
		t.Errorf("A's event carries token %q, which was issued for B", ev.ActivationToken)
	}
	ev = nextEvent(t, n)
	if ev.NoticeID != "2026-10-02-b" || ev.Action != "default" || ev.ActivationToken != "token-for-b" {
		t.Errorf("second event = %+v, want B/default with token-for-b", ev)
	}
	ev = nextEvent(t, n)
	if ev.NoticeID != "2026-10-02-b" || ev.Action != "mark-read" {
		t.Errorf("third event = %+v, want B/mark-read", ev)
	}
}

// TestNotify_ServerAbsentIsAnErrorNamingTheNotice is R6.12's trigger: New
// works without a server (it may appear later), and Send fails naming the
// notice.
func TestNotify_ServerAbsentIsAnErrorNamingTheNotice(t *testing.T) {
	addr := dbustest.Session(t)
	n := newNotifier(t, addr)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := n.Send(ctx, noticeMsg("2026-10-02-nobody-listens", "s", "b", 1))
	if err == nil {
		t.Fatal("Send succeeded with no notification server on the bus")
	}
	if !strings.Contains(err.Error(), "2026-10-02-nobody-listens") {
		t.Errorf("error %q does not name the notice", err)
	}
}
