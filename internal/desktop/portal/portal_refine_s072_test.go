package portal_test

// Refine v2 of story 072, sub-task 12.2: a portal policy refusal (lockdown,
// org.freedesktop.portal.Error.NotAllowed) is respected and never bypassed
// through xdg-open (R7.5), while every other error reply still falls back
// (R7.2). Reuses portal_test.go's runRecorder, newOpener and allowedHost.

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/godbus/dbus/v5"

	"github.com/obentoo/bentoolkit/internal/desktop/dbustest"
	"github.com/obentoo/bentoolkit/internal/desktop/portal"
)

// notAllowed is the error the portal replies with when an administrator has
// locked OpenURI down.
const notAllowed = "org.freedesktop.portal.Error.NotAllowed"

const refineURL = "https://obentoo.org/notices/2026-10-02-foo-cve/"

// replyingPortal is org.freedesktop.portal.Desktop's OpenURI answering every
// call with the D-Bus error errName carrying body.
type replyingPortal struct {
	mu      sync.Mutex
	uris    []string
	errName string
	body    []any
}

func (p *replyingPortal) OpenURI(_, uri string, _ map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.uris = append(p.uris, uri)
	return "", dbus.NewError(p.errName, p.body)
}

func (p *replyingPortal) called() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.uris)
}

func startReplyingPortal(t *testing.T, addr, errName string, body []any) *replyingPortal {
	t.Helper()
	conn, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	p := &replyingPortal{errName: errName, body: body}
	if err := conn.Export(p, "/org/freedesktop/portal/desktop", "org.freedesktop.portal.OpenURI"); err != nil {
		t.Fatal(err)
	}
	if reply, err := conn.RequestName("org.freedesktop.portal.Desktop", dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("fake portal RequestName: %v %v", reply, err)
	}
	return p
}

// TestOpener_PolicyRefusalNeverRunsXdgOpen is R7.5, the hostile half first:
// the old fallback treated a lockdown like any failure and ran xdg-open,
// bypassing the administrator's policy.
func TestOpener_PolicyRefusalNeverRunsXdgOpen(t *testing.T) {
	addr := dbustest.Session(t)
	p := startReplyingPortal(t, addr, notAllowed, []any{"OpenURI is locked down"})
	rec := &runRecorder{}
	o := newOpener(t, addr, rec)

	err := o.Open(context.Background(), refineURL, "tok")

	if c := rec.recorded(); len(c) != 0 {
		t.Errorf("xdg-open ran after a policy refusal: %q", c)
	}
	if !errors.Is(err, portal.ErrRefusedByPolicy) {
		t.Errorf("Open after NotAllowed: err = %v, want ErrRefusedByPolicy", err)
	}
	if c := p.called(); len(c) != 1 || c[0] != refineURL {
		t.Errorf("portal OpenURI calls = %q, want exactly one for %s", c, refineURL)
	}
}

// TestOpener_PolicyRefusalIsItsOwnOutcome: the refusal must not collapse into
// another outcome the caller treats differently — not a user cancel, not a
// refused URL, and never success.
func TestOpener_PolicyRefusalIsItsOwnOutcome(t *testing.T) {
	addr := dbustest.Session(t)
	startReplyingPortal(t, addr, notAllowed, []any{"OpenURI is locked down"})
	o := newOpener(t, addr, &runRecorder{})

	err := o.Open(context.Background(), refineURL, "")

	if err == nil {
		t.Fatal("Open reported success although the portal refused by policy")
	}
	if errors.Is(err, portal.ErrCancelled) {
		t.Errorf("a policy refusal reported as a user cancel: %v", err)
	}
	if errors.Is(err, portal.ErrURLRefused) {
		t.Errorf("a policy refusal reported as a refused URL: %v", err)
	}
}

// TestOpener_PolicyRefusalWhateverItsMessage is the wrong-split converse: the
// error name decides, so a NotAllowed reply with any message, or none, and
// with or without an activation token is the same refusal.
func TestOpener_PolicyRefusalWhateverItsMessage(t *testing.T) {
	cases := map[string]struct {
		body  []any
		token string
	}{
		"no message":            {body: nil, token: ""},
		"empty message":         {body: []any{""}, token: "tok"},
		"message without words": {body: []any{"Failed"}, token: ""},
		"other wording":         {body: []any{"Not allowed by the administrator"}, token: "tok"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			addr := dbustest.Session(t)
			startReplyingPortal(t, addr, notAllowed, tc.body)
			rec := &runRecorder{}
			o := newOpener(t, addr, rec)
			err := o.Open(context.Background(), refineURL, tc.token)
			if !errors.Is(err, portal.ErrRefusedByPolicy) {
				t.Errorf("err = %v, want ErrRefusedByPolicy", err)
			}
			if c := rec.recorded(); len(c) != 0 {
				t.Errorf("xdg-open ran after a policy refusal: %q", c)
			}
		})
	}
}

// TestOpener_OtherErrorRepliesStillFallBack is R7.2 against a wrong collapse:
// error names that resemble NotAllowed (a prefix, a suffix in another
// namespace, another case, a bus-level denial) and the portal's other errors
// are not a policy refusal, so xdg-open still runs with the URL alone.
func TestOpener_OtherErrorRepliesStillFallBack(t *testing.T) {
	for _, errName := range []string{
		"org.freedesktop.portal.Error.NotAllowedForThisApp",
		"org.freedesktop.portal.Error.Not",
		"org.example.Error.NotAllowed",
		"org.freedesktop.portal.Error.NotAllowed.Detail",
		"org.freedesktop.portal.error.notallowed",
		"org.freedesktop.DBus.Error.AccessDenied",
		"org.freedesktop.portal.Error.Failed",
		"org.freedesktop.portal.Error.NotFound",
		"org.freedesktop.portal.Error.InvalidArgument",
		"org.freedesktop.DBus.Error.UnknownMethod",
	} {
		t.Run(errName, func(t *testing.T) {
			addr := dbustest.Session(t)
			startReplyingPortal(t, addr, errName, []any{"Not allowed"})
			rec := &runRecorder{}
			o := newOpener(t, addr, rec)
			err := o.Open(context.Background(), refineURL, "tok")
			if errors.Is(err, portal.ErrRefusedByPolicy) {
				t.Errorf("%s taken for a policy refusal", errName)
			}
			if err != nil {
				t.Errorf("Open with a working fallback: %v", err)
			}
			calls := rec.recorded()
			if len(calls) != 1 || !slices.Equal(calls[0], []string{"xdg-open", refineURL}) {
				t.Errorf("run calls = %q, want exactly [[xdg-open %s]]", calls, refineURL)
			}
		})
	}
}
