package portal_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"sync"
	"testing"

	"github.com/godbus/dbus/v5"

	"github.com/obentoo/bentoolkit/internal/desktop/dbustest"
	"github.com/obentoo/bentoolkit/internal/desktop/portal"
)

const allowedHost = "obentoo.org"

type openCall struct {
	Parent  string
	URI     string
	Options map[string]dbus.Variant
}

// fakePortal is org.freedesktop.portal.Desktop's OpenURI on its own
// connection; fail makes every call return a D-Bus error.
type fakePortal struct {
	mu    sync.Mutex
	calls []openCall
	fail  bool
}

func (p *fakePortal) OpenURI(parent, uri string, options map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, openCall{parent, uri, options})
	if p.fail {
		return "", dbus.NewError("org.freedesktop.portal.Error.Failed", []any{"no handler"})
	}
	return "/org/freedesktop/portal/desktop/request/1_1/t", nil
}

func (p *fakePortal) recorded() []openCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.calls)
}

func startPortal(t *testing.T, addr string, fail bool) *fakePortal {
	t.Helper()
	conn, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	p := &fakePortal{fail: fail}
	if err := conn.Export(p, "/org/freedesktop/portal/desktop", "org.freedesktop.portal.OpenURI"); err != nil {
		t.Fatal(err)
	}
	if reply, err := conn.RequestName("org.freedesktop.portal.Desktop", dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("fake portal RequestName: %v %v", reply, err)
	}
	return p
}

// runRecorder stands in for exec: it records every command and returns err.
type runRecorder struct {
	mu    sync.Mutex
	calls [][]string
	err   error
}

func (r *runRecorder) run(_ context.Context, name string, args ...string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, append([]string{name}, args...))
	return r.err
}

func (r *runRecorder) recorded() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

func newOpener(t *testing.T, addr string, rec *runRecorder) *portal.Opener {
	t.Helper()
	conn, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return portal.New(conn, allowedHost, slog.New(slog.NewTextHandler(io.Discard, nil)), rec.run)
}

// TestOpen_RefusesForeignAndInsecureURLs is R7.3, authored first: every URL
// that is not https on exactly the feed host is refused before anything runs,
// including look-alikes that contain the host somewhere else.
func TestOpen_RefusesForeignAndInsecureURLs(t *testing.T) {
	addr := dbustest.Session(t)
	p := startPortal(t, addr, false)
	rec := &runRecorder{}
	o := newOpener(t, addr, rec)
	for _, u := range []string{
		"http://obentoo.org/notices/x/",
		"https://evil.example/notices/x/",
		"https://obentoo.org.evil.example/notices/x/",
		"https://evil.example/obentoo.org/",
		"https://evil.example/?next=https://obentoo.org/",
		"https://obentoo.org@evil.example/notices/x/",
		"https://sub.obentoo.org/notices/x/",
		"javascript:alert(1)",
		"file:///etc/passwd",
		"//obentoo.org/notices/x/",
		"",
	} {
		err := o.Open(context.Background(), u, "tok")
		if !errors.Is(err, portal.ErrURLRefused) {
			t.Errorf("Open(%q): err = %v, want ErrURLRefused", u, err)
		}
	}
	if c := p.recorded(); len(c) != 0 {
		t.Errorf("the portal was asked to open refused URLs: %+v", c)
	}
	if c := rec.recorded(); len(c) != 0 {
		t.Errorf("xdg-open ran for refused URLs: %q", c)
	}
}

// TestOpen_HostComparisonIgnoresCase is the converse: host names are
// case-insensitive, so the same host in capitals is the same host.
func TestOpen_HostComparisonIgnoresCase(t *testing.T) {
	addr := dbustest.Session(t)
	p := startPortal(t, addr, false)
	o := newOpener(t, addr, &runRecorder{})
	if err := o.Open(context.Background(), "https://OBENTOO.ORG/notices/x/", ""); err != nil {
		t.Fatalf("Open of the feed host in capitals: %v", err)
	}
	if len(p.recorded()) != 1 {
		t.Error("the portal was not called for the feed host in capitals")
	}
}

// TestOpen_UsesThePortalWithTheActivationToken is R7.1.
func TestOpen_UsesThePortalWithTheActivationToken(t *testing.T) {
	addr := dbustest.Session(t)
	p := startPortal(t, addr, false)
	rec := &runRecorder{}
	o := newOpener(t, addr, rec)
	const u = "https://obentoo.org/notices/2026-10-02-foo-cve/"
	if err := o.Open(context.Background(), u, "xdg-token-123"); err != nil {
		t.Fatalf("Open: %v", err)
	}
	calls := p.recorded()
	if len(calls) != 1 {
		t.Fatalf("portal calls = %+v, want one", calls)
	}
	if calls[0].URI != u || calls[0].Parent != "" {
		t.Errorf("OpenURI(%q, %q), want (\"\", %q)", calls[0].Parent, calls[0].URI, u)
	}
	if tok, _ := calls[0].Options["activation_token"].Value().(string); tok != "xdg-token-123" {
		t.Errorf("activation_token = %#v, want xdg-token-123", calls[0].Options["activation_token"].Value())
	}
	if c := rec.recorded(); len(c) != 0 {
		t.Errorf("xdg-open ran although the portal succeeded: %q", c)
	}
}

// TestOpen_FallsBackToXdgOpenWithOneArgument is R7.2, for a failing portal and
// for no portal at all.
func TestOpen_FallsBackToXdgOpenWithOneArgument(t *testing.T) {
	const u = "https://obentoo.org/notices/2026-10-02-foo-cve/?a=1&b=$(id)"
	for name, withPortal := range map[string]bool{"portal fails": true, "no portal": false} {
		t.Run(name, func(t *testing.T) {
			addr := dbustest.Session(t)
			if withPortal {
				startPortal(t, addr, true)
			}
			rec := &runRecorder{}
			o := newOpener(t, addr, rec)
			if err := o.Open(context.Background(), u, "tok"); err != nil {
				t.Fatalf("Open with a working fallback: %v", err)
			}
			calls := rec.recorded()
			if len(calls) != 1 || !slices.Equal(calls[0], []string{"xdg-open", u}) {
				t.Errorf("run calls = %q, want exactly [[xdg-open %s]]", calls, u)
			}
		})
	}
}

// TestOpen_BothFailingReportsTheFallbackError: when the portal and xdg-open
// both fail, the caller sees an error that still carries the xdg-open cause.
func TestOpen_BothFailingReportsTheFallbackError(t *testing.T) {
	addr := dbustest.Session(t)
	startPortal(t, addr, true)
	runErr := errors.New("xdg-open: not found")
	o := newOpener(t, addr, &runRecorder{err: runErr})
	err := o.Open(context.Background(), "https://obentoo.org/notices/x/", "")
	if err == nil {
		t.Fatal("Open reported success although nothing could open the URL")
	}
	if !errors.Is(err, runErr) {
		t.Errorf("err = %v, want it to wrap the xdg-open failure", err)
	}
}
