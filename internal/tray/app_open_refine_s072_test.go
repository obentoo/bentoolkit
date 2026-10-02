package tray_test

// Refine v2 of story 072, sub-task 12.2: an opening refused by the portal's
// lockdown policy leaves the notice unread and is logged at WARN with the
// notice ID (R7.5). Reuses app_test.go's harness (newApp, withCriticalNotice,
// appOpener, warned, fooCVE, bentooFoo, okResult) and app_contract_test.go's
// startWithOpener. Helper names carry an S072Policy suffix so they cannot
// collide with the other Refine sub-tasks' test files.

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/godbus/dbus/v5"

	"github.com/obentoo/bentoolkit/internal/desktop/dbustest"
	"github.com/obentoo/bentoolkit/internal/desktop/notify"
	"github.com/obentoo/bentoolkit/internal/desktop/portal"
	"github.com/obentoo/bentoolkit/internal/desktop/sni"
	"github.com/obentoo/bentoolkit/internal/gentoo/pkgdb"
	"github.com/obentoo/bentoolkit/internal/tray/feed"
	"github.com/obentoo/bentoolkit/internal/tray/state"
)

// refusedByPolicyS072Policy is what the real Opener returns for a lockdown:
// the sentinel wrapped with the URL, as callers must match with errors.Is.
func refusedByPolicyS072Policy() error {
	return fmt.Errorf("opening %s through org.freedesktop.portal.Desktop: %w", fooCVE().URL, portal.ErrRefusedByPolicy)
}

// savedReadS072Policy reports whether the last saved state has id read.
func savedReadS072Policy(h *appHarness, id string) bool {
	st, n := h.store.last()
	return n > 0 && st.Notices[id].Read
}

// TestApp_OpenRefusedByPolicyKeepsTheNoticeUnread is R7.5 at the loop, the
// hostile half first: a refused opening opened nothing, so it must not be
// recorded as read like a success, and unlike a user cancel it is a WARN
// naming the notice.
func TestApp_OpenRefusedByPolicyKeepsTheNoticeUnread(t *testing.T) {
	h := withCriticalNotice(t)
	id := fooCVE().ID
	h.open.mu.Lock()
	h.open.err = refusedByPolicyS072Policy()
	h.open.mu.Unlock()

	h.notif.events <- notify.Event{NoticeID: id, Action: "default", ActivationToken: "tok-1"}
	h.waitFor("the opener call", func() bool { return len(h.open.opened()) == 1 })
	h.waitFor("a WARN naming the refused notice", func() bool { return warned(h, id) })

	h.never("the refused notice saved as read", func() bool { return savedReadS072Policy(h, id) })
	if st, _ := h.store.last(); st.Notices[id].Read {
		t.Error("a policy refusal marked the notice read")
	}
	h.viewWhere("the refused notice still unread", func(v sni.View) bool { return v.Unread == 1 })
	if warned(h, id, "not https") {
		t.Error("a policy refusal was logged as a URL that is not https on the feed host")
	}
}

// TestApp_OpenAfterAPolicyRefusalStillMarksRead is the converse: the refusal
// keeps the notice unread, it does not make it unopenable. Once the policy
// allows it, the same notice opens and becomes read (R7.4).
func TestApp_OpenAfterAPolicyRefusalStillMarksRead(t *testing.T) {
	h := withCriticalNotice(t)
	id := fooCVE().ID
	h.open.mu.Lock()
	h.open.err = refusedByPolicyS072Policy()
	h.open.mu.Unlock()
	h.notif.events <- notify.Event{NoticeID: id, Action: "default"}
	h.waitFor("a WARN naming the refused notice", func() bool { return warned(h, id) })
	h.never("the refused notice saved as read", func() bool { return savedReadS072Policy(h, id) })

	h.open.mu.Lock()
	h.open.err = nil
	h.open.mu.Unlock()
	h.notif.events <- notify.Event{NoticeID: id, Action: "default"}
	h.waitFor("the second opener call", func() bool { return len(h.open.opened()) == 2 })
	h.savedWhere("the notice read after the allowed opening", func(st state.State) bool { return st.Notices[id].Read })
}

// lockedPortalS072Policy is org.freedesktop.portal.Desktop's OpenURI under a
// lockdown: every call is answered with NotAllowed.
type lockedPortalS072Policy struct {
	mu    sync.Mutex
	calls int
}

func (p *lockedPortalS072Policy) OpenURI(string, string, map[string]dbus.Variant) (dbus.ObjectPath, *dbus.Error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return "", dbus.NewError("org.freedesktop.portal.Error.NotAllowed", []any{"OpenURI is locked down"})
}

func (p *lockedPortalS072Policy) count() int { p.mu.Lock(); defer p.mu.Unlock(); return p.calls }

// runsS072Policy records the commands the Opener's fallback runs.
type runsS072Policy struct {
	mu    sync.Mutex
	calls [][]string
}

func (r *runsS072Policy) run(_ context.Context, name string, args ...string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, append([]string{name}, args...))
	return nil
}

func (r *runsS072Policy) recorded() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

// TestApp_OpenThroughALockedDownPortalKeepsTheNoticeUnread is R7.5 across the
// real boundary: the App wired to the real portal.Opener, on a private bus
// whose portal is locked down. The old code fell back to xdg-open, which
// "succeeded", and the notice was marked read.
func TestApp_OpenThroughALockedDownPortalKeepsTheNoticeUnread(t *testing.T) {
	addr := dbustest.Session(t)
	portalConn, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = portalConn.Close() })
	locked := &lockedPortalS072Policy{}
	if err := portalConn.Export(locked, "/org/freedesktop/portal/desktop", "org.freedesktop.portal.OpenURI"); err != nil {
		t.Fatal(err)
	}
	if reply, err := portalConn.RequestName("org.freedesktop.portal.Desktop", dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("fake portal RequestName: %v %v", reply, err)
	}
	openerConn, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = openerConn.Close() })
	runs := &runsS072Policy{}
	opener := portal.New(openerConn, "obentoo.org", nil, runs.run)

	h := newApp(t)
	h.pkgs.list = []pkgdb.Package{bentooFoo}
	h.feed.next = func(int) (feed.Result, error) { return okResult(1790812800, fooCVE()), nil }
	startWithOpener(h, opener)
	h.checkNow()
	h.waitFor("the critical notification", func() bool { return len(h.notif.messages()) == 1 })
	id := fooCVE().ID

	h.notif.events <- notify.Event{NoticeID: id, Action: "default", ActivationToken: "tok-1"}
	h.waitFor("the opening's outcome", func() bool {
		return locked.count() == 1 && (warned(h, id) || savedReadS072Policy(h, id))
	})

	if c := runs.recorded(); len(c) != 0 {
		t.Errorf("xdg-open ran behind a locked-down portal: %q", c)
	}
	if savedReadS072Policy(h, id) {
		t.Error("the notice was marked read although the portal refused to open it")
	}
	if !warned(h, id) {
		t.Error("the policy refusal was not logged at WARN with the notice ID")
	}
}
