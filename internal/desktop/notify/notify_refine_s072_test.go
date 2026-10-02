package notify_test

// Refine v2 of story 072, sub-task 12.1: notification text follows the spec.
// The summary is plain text (R6.14); the body is escaped only for a server
// that advertises the body-markup capability (R6.6), and the capability is
// asked again whenever the server's owner changes.
//
// Reuses notifyCall, newNotifier, noticeMsg and the nf* constants from
// notify_test.go. capsServer differs from fakeServer only in that its
// capabilities are chosen by the test.

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/obentoo/bentoolkit/internal/desktop/dbustest"
	"github.com/obentoo/bentoolkit/internal/desktop/notify"
)

// The text every test sends: each of the three markup characters, plus text
// that already looks like an entity (it is notice text, not markup).
const (
	hostileSummary = "foo <1.2.3> & bar &amp; baz"
	hostileBody    = "<b>upgrade</b> & reboot &lt;now&gt;"
	escapedBody    = "&lt;b&gt;upgrade&lt;/b&gt; &amp; reboot &amp;lt;now&amp;gt;"
)

// capsServer is an org.freedesktop.Notifications whose GetCapabilities
// answer is set by the test.
type capsServer struct {
	conn  *dbus.Conn
	caps  []string
	mu    sync.Mutex
	calls []notifyCall
	next  uint32
}

func (f *capsServer) Notify(appName string, replaces uint32, appIcon, summary, body string,
	actions []string, hints map[string]dbus.Variant, timeout int32) (uint32, *dbus.Error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	f.calls = append(f.calls, notifyCall{appName, replaces, appIcon, summary, body, actions, hints, timeout, f.next})
	return f.next, nil
}

func (f *capsServer) GetCapabilities() ([]string, *dbus.Error) {
	return slices.Clone(f.caps), nil
}

func (f *capsServer) GetServerInformation() (string, string, string, string, *dbus.Error) {
	return "caps-fake", "bentoo", "1", "1.2", nil
}

func (f *capsServer) CloseNotification(id uint32) *dbus.Error { return nil }

func (f *capsServer) recorded() []notifyCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

// startCapsServer puts a server advertising caps on the bus at addr.
func startCapsServer(t *testing.T, addr string, caps ...string) *capsServer {
	t.Helper()
	conn, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	f := &capsServer{conn: conn, caps: caps}
	if err := conn.Export(f, nfPath, nfIface); err != nil {
		t.Fatal(err)
	}
	if reply, err := conn.RequestName(nfName, dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("caps server RequestName: %v %v", reply, err)
	}
	return f
}

// stopCapsServer drops the server's connection, as a crashing daemon does,
// and waits until the bus reports the name has no owner.
func stopCapsServer(t *testing.T, addr string, f *capsServer) {
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

// sendOnce sends one message and returns what srv received for it.
func sendOnce(t *testing.T, n *notify.Notifier, srv *capsServer, m notify.Message) notifyCall {
	t.Helper()
	before := len(srv.recorded())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := n.Send(ctx, m); err != nil {
		t.Fatalf("Send: %v", err)
	}
	calls := srv.recorded()
	if len(calls) != before+1 {
		t.Fatalf("server saw %d new Notify calls, want 1", len(calls)-before)
	}
	return calls[len(calls)-1]
}

// sendUntilBody sends m to srv until the body that arrives is want, or 3s
// pass. The Notifier hears an owner change asynchronously, so the first Send
// after a restart may still precede it; within 3s it must not.
func sendUntilBody(t *testing.T, n *notify.Notifier, srv *capsServer, m notify.Message, want string) notifyCall {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, err := n.Send(ctx, m)
		cancel()
		if calls := srv.recorded(); err == nil && len(calls) > 0 {
			last := calls[len(calls)-1]
			if last.Body == want {
				return last
			}
			if time.Now().After(deadline) {
				return last
			}
		} else if time.Now().After(deadline) {
			t.Fatalf("no Notify call reached the new server within 3s (last error: %v)", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestNotifier_SummaryIsPlainText is R6.14. Hostile half first: a server
// that advertises body-markup is exactly where an escaped summary used to be
// sent (GNOME Shell, dunst and mako re-escape it and show a literal &amp;).
func TestNotifier_SummaryIsPlainText(t *testing.T) {
	for _, tc := range []struct {
		name string
		caps []string
	}{
		{"body-markup server", []string{"actions", "body", "body-markup"}},
		{"plain-body server", []string{"actions", "body"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			addr := dbustest.Session(t)
			srv := startCapsServer(t, addr, tc.caps...)
			n := newNotifier(t, addr)

			c := sendOnce(t, n, srv, noticeMsg("2026-10-02-summary", hostileSummary, "b", 1))
			if c.Summary != hostileSummary {
				t.Errorf("summary = %q, want %q unchanged (R6.14: the summary is plain text)", c.Summary, hostileSummary)
			}
		})
	}
}

// TestNotifier_BodyIsEscapedOnlyForBodyMarkup is R6.6. Hostile halves first:
// capability lists that merely resemble body-markup must not turn escaping
// on, and text that already looks like an entity must arrive as written
// to a plain-body server and escaped again for a body-markup one.
func TestNotifier_BodyIsEscapedOnlyForBodyMarkup(t *testing.T) {
	for _, tc := range []struct {
		name string
		caps []string
		want string
	}{
		{"no capabilities", nil, hostileBody},
		{"body without markup", []string{"actions", "body", "body-hyperlinks", "body-images"}, hostileBody},
		{"vendor capability that only contains the name", []string{"body", "x-vendor-body-markup", "body-markup-ext"}, hostileBody},
		{"body-markup advertised", []string{"actions", "body", "body-markup"}, escapedBody},
		{"body-markup alone", []string{"body-markup"}, escapedBody},
	} {
		t.Run(tc.name, func(t *testing.T) {
			addr := dbustest.Session(t)
			srv := startCapsServer(t, addr, tc.caps...)
			n := newNotifier(t, addr)

			c := sendOnce(t, n, srv, noticeMsg("2026-10-02-body", "s", hostileBody, 1))
			if c.Body != tc.want {
				t.Errorf("caps %q: body = %q, want %q", tc.caps, c.Body, tc.want)
			}
		})
	}
}

// TestNotifier_RestartThatDropsBodyMarkupChangesNextSend: the capabilities
// are those of the current owner, not of the server New first asked. The
// summary stays plain across the restart.
func TestNotifier_RestartThatDropsBodyMarkupChangesNextSend(t *testing.T) {
	addr := dbustest.Session(t)
	first := startCapsServer(t, addr, "actions", "body", "body-markup")
	n := newNotifier(t, addr)
	m := noticeMsg("2026-10-02-restart", hostileSummary, hostileBody, 1)

	if c := sendOnce(t, n, first, m); c.Body != escapedBody {
		t.Fatalf("before the restart: body = %q, want %q (the server advertises body-markup)", c.Body, escapedBody)
	}

	stopCapsServer(t, addr, first)
	second := startCapsServer(t, addr, "actions", "body")

	c := sendUntilBody(t, n, second, m, hostileBody)
	if c.Body != hostileBody {
		t.Errorf("after a restart without body-markup: body = %q, want %q unchanged (stale capabilities of the old server)", c.Body, hostileBody)
	}
	if c.Summary != hostileSummary {
		t.Errorf("after the restart: summary = %q, want %q unchanged", c.Summary, hostileSummary)
	}
}

// TestNotifier_ServerAppearingAfterNewIsAskedForCapabilities is the converse:
// New runs with no server on the bus (the tray starts before the desktop's
// notification daemon), a plain-body server appears, then it restarts as one
// that advertises body-markup, and the next body is escaped.
func TestNotifier_ServerAppearingAfterNewIsAskedForCapabilities(t *testing.T) {
	addr := dbustest.Session(t)
	n := newNotifier(t, addr)
	m := noticeMsg("2026-10-02-late", hostileSummary, hostileBody, 1)

	plain := startCapsServer(t, addr, "actions", "body")
	if c := sendUntilBody(t, n, plain, m, hostileBody); c.Body != hostileBody {
		t.Fatalf("server without body-markup appearing after New: body = %q, want %q unchanged", c.Body, hostileBody)
	}

	stopCapsServer(t, addr, plain)
	markup := startCapsServer(t, addr, "actions", "body", "body-markup")

	c := sendUntilBody(t, n, markup, m, escapedBody)
	if c.Body != escapedBody {
		t.Errorf("after a restart that adds body-markup: body = %q, want %q", c.Body, escapedBody)
	}
	if c.Summary != hostileSummary {
		t.Errorf("after the restart: summary = %q, want %q unchanged", c.Summary, hostileSummary)
	}
}
