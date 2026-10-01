package sni_test

// Tech review of 12.5, story 072 (R9.1, R9.7). GNOME's AppIndicator extension
// keeps a menu item it once fetched until a layout fetch drops it, and on menu
// open it refetches a label only for an id new to it or one a held
// ItemsPropertiesUpdated named. A notice that leaves the menu while it is
// closed and comes back retitled is neither, so the signal must name it.

import (
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/obentoo/bentoolkit/internal/desktop/sni"
)

// reviewUpdated is one element of ItemsPropertiesUpdated's first argument.
type reviewUpdated struct {
	ID    int32
	Props map[string]dbus.Variant
}

// reviewWatch subscribes to the item's dbusmenu signals. Call it after a
// round trip to the item (h.layout), so no signal of an earlier SetState is
// still on its way.
func reviewWatch(h menuHarness) <-chan *dbus.Signal {
	h.t.Helper()
	if err := h.obs.AddMatchSignal(dbus.WithMatchSender(h.conn.Names()[0]), dbus.WithMatchInterface(menuIface)); err != nil {
		h.t.Fatal(err)
	}
	ch := make(chan *dbus.Signal, 64)
	h.obs.Signal(ch)
	return ch
}

// reviewUpdatesBeforeLayout collects the ItemsPropertiesUpdated entries
// received before the next LayoutUpdated, and the signal members seen.
func reviewUpdatesBeforeLayout(h menuHarness, ch <-chan *dbus.Signal) ([]reviewUpdated, []string) {
	h.t.Helper()
	var updated []reviewUpdated
	var members []string
	for {
		select {
		case s := <-ch:
			member := s.Name[len(menuIface)+1:]
			members = append(members, member)
			switch member {
			case "LayoutUpdated":
				return updated, members
			case "ItemsPropertiesUpdated":
				var u []reviewUpdated
				if err := dbus.Store(s.Body[:1], &u); err != nil {
					h.t.Fatalf("ItemsPropertiesUpdated body %v: %v", s.Body, err)
				}
				updated = append(updated, u...)
			}
		case <-time.After(3 * time.Second):
			h.t.Fatalf("no LayoutUpdated within 3s; signals seen %v", members)
			return nil, nil
		}
	}
}

// TestMenu_RelistedRetitledNoticeIsNamedInItemsPropertiesUpdated: notice X is
// listed as "Old title"; eleven newer unread notices push it out of the ten
// listed; the feed retitles it and it re-enters the top ten. Its id is one
// GNOME may still hold with the old label, so the ItemsPropertiesUpdated sent
// before LayoutUpdated must name it with the new one. Hostile: the previous
// menu lacks X, so a diff against the previous menu alone never names it.
func TestMenu_RelistedRetitledNoticeIsNamedInItemsPropertiesUpdated(t *testing.T) {
	h := newMenu(t)
	x := sni.MenuEntry{NoticeID: "2026-09-20-x", Title: "Old title"}

	h.item.SetState(sni.View{Unread: 1, Entries: []sni.MenuEntry{x}})
	_, nodes := h.layout()
	listed := stableNoticeIDs(nodes)
	if len(listed) != 1 {
		t.Fatalf("menu lists notice entries %v, want X alone", listed)
	}
	idX := listed[0]

	newer := make([]sni.MenuEntry, 0, 11)
	for k := range 11 {
		newer = append(newer, sni.MenuEntry{
			NoticeID: "2026-10-" + string(rune('a'+k)) + "-newer",
			Title:    "Newer notice " + string(rune('A'+k)),
		})
	}
	pushed := append(append([]sni.MenuEntry{}, newer...), x)
	h.item.SetState(sni.View{Unread: len(pushed), Entries: pushed})
	_, nodes = h.layout()
	if _, ok := find(nodes, idX); ok {
		t.Fatalf("X (id %d) is still listed behind %d newer notices", idX, len(newer))
	}

	ch := reviewWatch(h)
	x.Title = "New title"
	back := append([]sni.MenuEntry{x}, newer[:9]...)
	h.item.SetState(sni.View{Unread: len(back), Entries: back})

	updated, members := reviewUpdatesBeforeLayout(h, ch)
	named := false
	for _, u := range updated {
		if l, _ := u.Props["label"].Value().(string); u.ID == idX && l == x.Title {
			named = true
		}
	}
	if !named {
		t.Errorf("signals %v, updated %+v: no ItemsPropertiesUpdated before LayoutUpdated names id %d with label %q",
			members, updated, idX, x.Title)
	}
	if n, ok := find(h.layoutNodes(), idX); !ok || n.label() != x.Title {
		t.Errorf("X back in the menu = %+v (present %v), want id %d labelled %q", n.Props, ok, idX, x.Title)
	}
}

// TestMenu_UnchangedRelistedNoticeIsNotNamed is the converse: a notice that
// leaves and comes back with the label it had is not named, since a host that
// still holds it holds the right label. It pins that the fix compares labels
// rather than naming every returning id.
func TestMenu_UnchangedRelistedNoticeIsNotNamed(t *testing.T) {
	h := newMenu(t)
	a := sni.MenuEntry{NoticeID: "2026-10-03-a", Title: "Notice A"}
	b := sni.MenuEntry{NoticeID: "2026-10-02-b", Title: "Notice B"}

	h.item.SetState(sni.View{Unread: 2, Entries: []sni.MenuEntry{a, b}})
	_, nodes := h.layout()
	idA := stableNoticeIDs(nodes)[0]
	h.item.SetState(sni.View{Unread: 1, Entries: []sni.MenuEntry{b}})
	h.layout()

	ch := reviewWatch(h)
	h.item.SetState(sni.View{Unread: 2, Entries: []sni.MenuEntry{a, b}})
	updated, members := reviewUpdatesBeforeLayout(h, ch)
	for _, u := range updated {
		if u.ID == idA {
			t.Errorf("signals %v: ItemsPropertiesUpdated names id %d, relisted with its unchanged label: %+v", members, idA, u.Props)
		}
	}
}

// TestMenu_EventGroupOnStaleNoticeIDDeliversEmptyNoticeID: a host still
// showing an older menu may send EventGroup for the id of a notice no longer
// listed. That id was given, so it is not an id error, and the click reaches
// Events with an empty NoticeID for the reader to ignore (R9.7). An id never
// given is an id error and yields no event.
func TestMenu_EventGroupOnStaleNoticeIDDeliversEmptyNoticeID(t *testing.T) {
	h := newMenu(t)
	a := sni.MenuEntry{NoticeID: "2026-10-03-a", Title: "Notice A"}
	b := sni.MenuEntry{NoticeID: "2026-10-02-b", Title: "Notice B"}

	first := stableShow(h, a, b)
	idA := first[0]
	stableShow(h, b)

	const neverGiven int32 = 99999
	events := []struct {
		ID        int32
		EventID   string
		Data      dbus.Variant
		Timestamp uint32
	}{
		{idA, "clicked", dbus.MakeVariant(""), 0},
		{neverGiven, "clicked", dbus.MakeVariant(""), 0},
	}
	var idErrors []int32
	if err := h.object().Call(menuIface+".EventGroup", 0, events).Store(&idErrors); err != nil {
		t.Fatalf("EventGroup: %v", err)
	}
	for _, id := range idErrors {
		if id == idA {
			t.Errorf("idErrors %v lists %d, the id of removed notice %q, which was given", idErrors, idA, a.NoticeID)
		}
	}
	if len(idErrors) != 1 || idErrors[0] != neverGiven {
		t.Errorf("idErrors = %v, want exactly [%d], the id never given", idErrors, neverGiven)
	}

	select {
	case ev := <-h.item.Events():
		if ev.ItemID != idA || ev.NoticeID != "" {
			t.Errorf("event = %+v, want ItemID %d and an empty NoticeID", ev, idA)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("no event within 3s of an EventGroup click on stale id %d", idA)
	}
	select {
	case ev := <-h.item.Events():
		t.Errorf("unexpected second event %+v: the id never given must yield none", ev)
	case <-time.After(200 * time.Millisecond):
	}
}

// layoutNodes is layout without the revision.
func (h menuHarness) layoutNodes() []menuNode {
	h.t.Helper()
	_, nodes := h.layout()
	return nodes
}
