package sni_test

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/obentoo/bentoolkit/internal/desktop/dbustest"
	"github.com/obentoo/bentoolkit/internal/desktop/sni"
)

const (
	menuPath  = dbus.ObjectPath("/MenuBar")
	menuIface = "com.canonical.dbusmenu"
)

// menuIcons keeps this file independent of item_test.go, so 7.3's tests can
// be materialized on their own.
var menuIcons = sni.IconSet{
	Plain:    []sni.Pixmap{{Width: 1, Height: 1, Data: []byte{0xff, 1, 1, 1}}},
	Unread:   []sni.Pixmap{{Width: 1, Height: 1, Data: []byte{0xff, 2, 2, 2}}},
	Critical: []sni.Pixmap{{Width: 1, Height: 1, Data: []byte{0xff, 3, 3, 3}}},
}

// Fixed menu ids from design.md.
const (
	idMore      = 100
	idCheckNow  = 200
	idMarkAll   = 201
	idPauseHour = 202
	idPauseTmrw = 203
	idResume    = 204
	idQuit      = 300
)

// menuNode is one decoded dbusmenu layout node (ia{sv}av).
type menuNode struct {
	ID       int32
	Props    map[string]dbus.Variant
	Children []dbus.Variant
}

func (n menuNode) label() string { s, _ := n.Props["label"].Value().(string); return s }
func (n menuNode) kind() string  { s, _ := n.Props["type"].Value().(string); return s }

type menuHarness struct {
	t    *testing.T
	item *sni.Item
	conn *dbus.Conn
	obs  *dbus.Conn
}

func newMenu(t *testing.T) menuHarness {
	t.Helper()
	addr := dbustest.Session(t)
	conn, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	obs, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = obs.Close() })
	item := sni.New(conn, slog.New(slog.NewTextHandler(io.Discard, nil)), menuIcons)
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
		t.Fatal("Start did not return within 5s")
	}
	return menuHarness{t, item, conn, obs}
}

func (h menuHarness) object() dbus.BusObject { return h.obs.Object(h.conn.Names()[0], menuPath) }

// layout calls GetLayout(0, -1, []) and decodes the root's direct children.
// Each child must be a variant wrapping the (ia{sv}av) struct: an unwrapped
// child is the GNOME empty-menu GOTCHA and fails the decode.
func (h menuHarness) layout() (uint32, []menuNode) {
	h.t.Helper()
	var rev uint32
	var root menuNode
	if err := h.object().Call(menuIface+".GetLayout", 0, int32(0), int32(-1), []string{}).Store(&rev, &root); err != nil {
		h.t.Fatalf("GetLayout: %v", err)
	}
	nodes := make([]menuNode, 0, len(root.Children))
	for i, c := range root.Children {
		var n menuNode
		if err := dbus.Store([]any{c.Value()}, &n); err != nil {
			h.t.Fatalf("child %d (%s) is not a variant wrapping (ia{sv}av): %v", i, c.Signature(), err)
		}
		nodes = append(nodes, n)
	}
	return rev, nodes
}

func ids(nodes []menuNode) []int32 {
	var out []int32
	for _, n := range nodes {
		if n.kind() != "separator" {
			out = append(out, n.ID)
		}
	}
	return out
}

func find(nodes []menuNode, id int32) (menuNode, bool) {
	for _, n := range nodes {
		if n.ID == id && n.kind() != "separator" {
			return n, true
		}
	}
	return menuNode{}, false
}

// TestMenu_PausedShowsResumeInPlaceOfPauseEntries is R9.3, both directions,
// authored first: while paused Resume replaces both pause entries, and while
// not paused Resume is absent.
func TestMenu_PausedShowsResumeInPlaceOfPauseEntries(t *testing.T) {
	h := newMenu(t)
	h.item.SetState(sni.View{Paused: true})
	_, nodes := h.layout()
	got := ids(nodes)
	if !slices.Contains(got, idResume) {
		t.Errorf("paused menu ids %v lack Resume (%d)", got, idResume)
	}
	if slices.Contains(got, idPauseHour) || slices.Contains(got, idPauseTmrw) {
		t.Errorf("paused menu ids %v still offer a pause entry", got)
	}

	h.item.SetState(sni.View{Paused: false})
	_, nodes = h.layout()
	got = ids(nodes)
	if slices.Contains(got, idResume) {
		t.Errorf("unpaused menu ids %v offer Resume", got)
	}
	if !slices.Contains(got, idPauseHour) || !slices.Contains(got, idPauseTmrw) {
		t.Errorf("unpaused menu ids %v lack a pause entry", got)
	}
}

// TestMenu_ListsNoticesThenMoreThenActions is R9.1 and R9.5: the notice
// entries in the order given (ids 1..n, labelled by title), the "N more"
// entry stating the count, the fixed actions, Quit last.
func TestMenu_ListsNoticesThenMoreThenActions(t *testing.T) {
	h := newMenu(t)
	h.item.SetState(sni.View{
		Unread: 5,
		Entries: []sni.MenuEntry{
			{NoticeID: "2026-10-03-newest", Title: "Newest notice"},
			{NoticeID: "2026-10-02-older", Title: "Older notice"},
		},
		More: 3,
	})
	_, nodes := h.layout()
	got := ids(nodes)
	if len(got) < 3 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("menu ids = %v, want the notice entries 1 and 2 first", got)
	}
	for id, title := range map[int32]string{1: "Newest notice", 2: "Older notice"} {
		if n, _ := find(nodes, id); n.label() != title {
			t.Errorf("entry %d label = %q, want %q", id, n.label(), title)
		}
	}
	more, ok := find(nodes, idMore)
	if !ok || !strings.Contains(more.label(), "3") {
		t.Errorf("the more entry = %+v (present %v), want a label stating 3", more.Props, ok)
	}
	if slices.Index(got, idMore) < slices.Index(got, 2) {
		t.Errorf("menu ids = %v: the more entry precedes the notices", got)
	}
	for _, id := range []int32{idCheckNow, idMarkAll, idPauseHour, idPauseTmrw, idQuit} {
		n, ok := find(nodes, id)
		if !ok {
			t.Errorf("menu ids %v lack %d", got, id)
			continue
		}
		if n.label() == "" {
			t.Errorf("entry %d has no label", id)
		}
	}
	if got[len(got)-1] != idQuit {
		t.Errorf("menu ids = %v, want Quit (%d) last", got, idQuit)
	}
}

// TestMenu_NoMoreEntryWithoutOverflow is the converse of R9.1's overflow: with
// nothing beyond the listed entries there is no "0 more" line, and with no
// unread notices there are no notice entries.
func TestMenu_NoMoreEntryWithoutOverflow(t *testing.T) {
	h := newMenu(t)
	h.item.SetState(sni.View{Unread: 1, Entries: []sni.MenuEntry{{NoticeID: "2026-10-02-a", Title: "A"}}})
	_, nodes := h.layout()
	if _, ok := find(nodes, idMore); ok {
		t.Error("the more entry is shown with More = 0")
	}
	h.item.SetState(sni.View{})
	_, nodes = h.layout()
	for _, id := range ids(nodes) {
		if id >= 1 && id <= 10 {
			t.Errorf("notice entry %d shown with no unread notices", id)
		}
	}
}

// TestMenu_ClicksReachEventsAndOtherEventsDoNot: only "clicked" is an action;
// a hover is not a click.
func TestMenu_ClicksReachEventsAndOtherEventsDoNot(t *testing.T) {
	h := newMenu(t)
	h.item.SetState(sni.View{})
	call := func(id int32, eventID string) {
		t.Helper()
		if err := h.object().Call(menuIface+".Event", 0, id, eventID, dbus.MakeVariant(""), uint32(0)).Err; err != nil {
			t.Fatalf("Event(%d, %q): %v", id, eventID, err)
		}
	}
	call(idQuit, "hovered")
	call(idMarkAll, "opened")
	call(idCheckNow, "clicked")
	select {
	case ev := <-h.item.Events():
		if int64(ev.ItemID) != idCheckNow {
			t.Errorf("first event ItemID = %d, want %d (a non-click leaked through)", ev.ItemID, idCheckNow)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no event within 3s of a click")
	}
}

// TestMenu_LayoutUpdatedRevisionStrictlyIncreases: KDE shows its cached menu
// unless every change announces a newer revision, and GetLayout reports the
// latest one.
func TestMenu_LayoutUpdatedRevisionStrictlyIncreases(t *testing.T) {
	h := newMenu(t)
	h.item.SetState(sni.View{})
	if err := h.obs.AddMatchSignal(dbus.WithMatchSender(h.conn.Names()[0]), dbus.WithMatchInterface(menuIface), dbus.WithMatchMember("LayoutUpdated")); err != nil {
		t.Fatal(err)
	}
	ch := make(chan *dbus.Signal, 64)
	h.obs.Signal(ch)

	var revs []uint32
	for _, v := range []sni.View{{Unread: 1, Entries: []sni.MenuEntry{{NoticeID: "a", Title: "A"}}}, {Paused: true}, {}} {
		h.item.SetState(v)
		select {
		case s := <-ch:
			rev, ok := s.Body[0].(uint32)
			if !ok {
				t.Fatalf("LayoutUpdated body %v does not start with a uint32 revision", s.Body)
			}
			revs = append(revs, rev)
		case <-time.After(3 * time.Second):
			t.Fatalf("no LayoutUpdated within 3s of SetState(%+v)", v)
		}
	}
	for i := 1; i < len(revs); i++ {
		if revs[i] <= revs[i-1] {
			t.Errorf("revisions %v are not strictly increasing", revs)
		}
	}
	if rev, _ := h.layout(); rev != revs[len(revs)-1] {
		t.Errorf("GetLayout revision %d, want the last announced %d", rev, revs[len(revs)-1])
	}
}

// TestMenu_ExportsDbusmenuVersion3: hosts check the protocol version.
func TestMenu_ExportsDbusmenuVersion3(t *testing.T) {
	h := newMenu(t)
	v, err := h.object().GetProperty(menuIface + ".Version")
	if err != nil {
		t.Fatalf("reading Version: %v", err)
	}
	if v.Value() != uint32(3) {
		t.Errorf("Version = %#v, want uint32 3", v.Value())
	}
}
