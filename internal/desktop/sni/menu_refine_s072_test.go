package sni_test

// Refine 12.5 of story 072: stable menu ids (R9.1, R9.7). A notice keeps its
// menu item id for the process lifetime, keyed by its notice ID; a click is
// resolved to a notice at click time, and an id that lists nothing now
// resolves to an empty NoticeID. Hostile halves come first: the id must not
// collapse two notices that render alike, nor split one notice whose title or
// position changed, nor follow a position after a notice is removed.

import (
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/obentoo/bentoolkit/internal/desktop/sni"
)

// firstStableNoticeID is the lowest id design.md gives a notice entry.
const firstStableNoticeID int32 = 1000

// stableFixedIDs are the menu ids that are not notice entries.
var stableFixedIDs = map[int32]bool{
	idMore: true, idCheckNow: true, idMarkAll: true, idPauseHour: true,
	idPauseTmrw: true, idResume: true, idQuit: true,
}

// stableNoticeIDs returns the ids of the notice entries in menu order: every
// non-separator child that is not a fixed action.
func stableNoticeIDs(nodes []menuNode) []int32 {
	var out []int32
	for _, n := range nodes {
		if n.kind() != "separator" && !stableFixedIDs[n.ID] {
			out = append(out, n.ID)
		}
	}
	return out
}

// stableShow sets entries, reads the layout back and returns the notice
// entry ids in menu order, failing unless there is one per entry and each
// is at or above firstStableNoticeID.
func stableShow(h menuHarness, entries ...sni.MenuEntry) []int32 {
	h.t.Helper()
	h.item.SetState(sni.View{Unread: len(entries), Entries: entries})
	_, nodes := h.layout()
	got := stableNoticeIDs(nodes)
	if len(got) != len(entries) {
		h.t.Fatalf("menu lists %d notice entries %v for %d notices", len(got), got, len(entries))
	}
	for _, id := range got {
		if id < firstStableNoticeID {
			h.t.Errorf("notice entry id %d is below %d (design.md: notice ids from 1000 upward)", id, firstStableNoticeID)
		}
	}
	return got
}

// stableClick sends a dbusmenu click on id and returns the event it yields.
// The call's own reply is not asserted: the contract is the event.
func stableClick(h menuHarness, id int32) sni.Event {
	h.t.Helper()
	_ = h.object().Call(menuIface+".Event", 0, id, "clicked", dbus.MakeVariant(""), uint32(0)).Err
	select {
	case ev := <-h.item.Events():
		return ev
	case <-time.After(3 * time.Second):
		h.t.Fatalf("no event within 3s of a click on id %d", id)
		return sni.Event{}
	}
}

// TestMenu_NoticeIDIsKeyedByNoticeNotByLabel is the wrongly-collapse half of
// R9.7 on a DERIVED value: a notice with a blank title is labelled with its
// notice ID, so it renders exactly like a second notice whose title is that
// same text. Two notices must keep two ids, and each must keep its own when
// listed alone, whatever the label.
func TestMenu_NoticeIDIsKeyedByNoticeNotByLabel(t *testing.T) {
	h := newMenu(t)
	blank := sni.MenuEntry{NoticeID: "2026-10-02-alike", Title: ""}
	alike := sni.MenuEntry{NoticeID: "2026-10-01-other", Title: "2026-10-02-alike"}

	both := stableShow(h, blank, alike)
	if both[0] == both[1] {
		t.Fatalf("two notices rendering the same label share id %d", both[0])
	}
	if got := stableShow(h, alike); got[0] != both[1] {
		t.Errorf("notice %q listed alone has id %d, want its own id %d (not %d, the like-labelled notice's)",
			alike.NoticeID, got[0], both[1], both[0])
	}
	if got := stableShow(h, blank); got[0] != both[0] {
		t.Errorf("notice %q listed alone has id %d, want its own id %d (not %d, the like-labelled notice's)",
			blank.NoticeID, got[0], both[0], both[1])
	}
}

// TestMenu_RemovedNoticeIDIsNeitherShiftedNorReused is the wrongly-collapse
// half against positions: when the first notice goes away the second keeps
// its id rather than taking the vacated slot's, and a new notice never
// inherits the id of one that is no longer listed.
func TestMenu_RemovedNoticeIDIsNeitherShiftedNorReused(t *testing.T) {
	h := newMenu(t)
	a := sni.MenuEntry{NoticeID: "2026-10-03-a", Title: "Notice A"}
	b := sni.MenuEntry{NoticeID: "2026-10-02-b", Title: "Notice B"}
	c := sni.MenuEntry{NoticeID: "2026-10-01-c", Title: "Notice C"}

	first := stableShow(h, a, b)
	idA, idB := first[0], first[1]
	if got := stableShow(h, b); got[0] != idB {
		t.Errorf("after %q was removed, %q has id %d, want its own %d (it took the removed notice's slot %d)",
			a.NoticeID, b.NoticeID, got[0], idB, idA)
	}
	idC := stableShow(h, c)[0]
	if idC == idA || idC == idB {
		t.Errorf("new notice %q got id %d, already given to another notice (A=%d, B=%d)", c.NoticeID, idC, idA, idB)
	}
}

// TestMenu_RetitledNoticeKeepsItsID is the wrongly-split half: the same
// notice with a new title, now in another position, is the same entry.
func TestMenu_RetitledNoticeKeepsItsID(t *testing.T) {
	h := newMenu(t)
	a := sni.MenuEntry{NoticeID: "2026-10-03-a", Title: "Old title"}
	b := sni.MenuEntry{NoticeID: "2026-10-02-b", Title: "Notice B"}

	first := stableShow(h, a, b)
	a.Title = "New title"
	second := stableShow(h, b, a)
	if second[1] != first[0] {
		t.Errorf("retitled notice %q has id %d, want %d", a.NoticeID, second[1], first[0])
	}
	if second[0] != first[1] {
		t.Errorf("notice %q has id %d after moving up, want %d", b.NoticeID, second[0], first[1])
	}
}

// TestMenu_NoticeKeepsItsIDAcrossReorderAndRelisting is the benign case of
// R9.7: across two SetState calls that reorder the entries every notice keeps
// its id, and one that drops out and is listed again later in the process
// gets the id it had.
func TestMenu_NoticeKeepsItsIDAcrossReorderAndRelisting(t *testing.T) {
	h := newMenu(t)
	a := sni.MenuEntry{NoticeID: "2026-10-03-a", Title: "Notice A"}
	b := sni.MenuEntry{NoticeID: "2026-10-02-b", Title: "Notice B"}
	c := sni.MenuEntry{NoticeID: "2026-10-01-c", Title: "Notice C"}

	first := stableShow(h, a, b, c)
	want := map[string]int32{a.NoticeID: first[0], b.NoticeID: first[1], c.NoticeID: first[2]}
	if first[0] == first[1] || first[1] == first[2] || first[0] == first[2] {
		t.Fatalf("notice ids %v are not distinct", first)
	}

	second := stableShow(h, c, a, b)
	for k, e := range []sni.MenuEntry{c, a, b} {
		if second[k] != want[e.NoticeID] {
			t.Errorf("after reordering, %q has id %d, want %d", e.NoticeID, second[k], want[e.NoticeID])
		}
	}

	stableShow(h, b)
	if got := stableShow(h, a, b); got[0] != want[a.NoticeID] {
		t.Errorf("relisted %q has id %d, want the id it had, %d", a.NoticeID, got[0], want[a.NoticeID])
	}
}

// TestMenu_ClickResolvesNoticeIDAtClickTime: a click carries the notice its
// id stands for in the current menu, and a click on the id of a notice the
// last SetState removed carries no notice at all. Hostile: with positional
// ids the removed notice's id now lists another notice, and the click would
// open that one.
func TestMenu_ClickResolvesNoticeIDAtClickTime(t *testing.T) {
	h := newMenu(t)
	a := sni.MenuEntry{NoticeID: "2026-10-03-a", Title: "Notice A"}
	b := sni.MenuEntry{NoticeID: "2026-10-02-b", Title: "Notice B"}

	first := stableShow(h, a, b)
	idA, idB := first[0], first[1]
	if ev := stableClick(h, idB); ev.ItemID != idB || ev.NoticeID != b.NoticeID {
		t.Errorf("click on %d = %+v, want ItemID %d and NoticeID %q", idB, ev, idB, b.NoticeID)
	}

	stableShow(h, b)
	if ev := stableClick(h, idA); ev.ItemID != idA || ev.NoticeID != "" {
		t.Errorf("click on %d (notice %q, removed) = %+v, want ItemID %d and an empty NoticeID", idA, a.NoticeID, ev, idA)
	}
	if ev := stableClick(h, idB); ev.NoticeID != b.NoticeID {
		t.Errorf("click on %d after the removal = %+v, want NoticeID %q", idB, ev, b.NoticeID)
	}
	if ev := stableClick(h, idCheckNow); ev.ItemID != idCheckNow || ev.NoticeID != "" {
		t.Errorf("click on Check now = %+v, want ItemID %d and no NoticeID", ev, idCheckNow)
	}
}

// TestMenu_RetitledEntryEmitsItemsPropertiesUpdatedBeforeLayoutUpdated: GNOME
// fetches the label of an id it already knows only when
// ItemsPropertiesUpdated names it, so a retitled notice that keeps its id
// must be named, with the new label, before LayoutUpdated.
func TestMenu_RetitledEntryEmitsItemsPropertiesUpdatedBeforeLayoutUpdated(t *testing.T) {
	h := newMenu(t)
	a := sni.MenuEntry{NoticeID: "2026-10-03-a", Title: "Old title"}
	b := sni.MenuEntry{NoticeID: "2026-10-02-b", Title: "Notice B"}
	// Not stableShow: this test isolates the signals, not the id range.
	h.item.SetState(sni.View{Unread: 2, Entries: []sni.MenuEntry{a, b}})
	_, nodes := h.layout()
	idA := stableNoticeIDs(nodes)[0]

	if err := h.obs.AddMatchSignal(dbus.WithMatchSender(h.conn.Names()[0]), dbus.WithMatchInterface(menuIface)); err != nil {
		t.Fatal(err)
	}
	ch := make(chan *dbus.Signal, 64)
	h.obs.Signal(ch)

	a.Title = "New title"
	h.item.SetState(sni.View{Unread: 2, Entries: []sni.MenuEntry{b, a}})

	var members []string
	named := false
	for done := false; !done; {
		select {
		case s := <-ch:
			member := s.Name[len(menuIface)+1:]
			members = append(members, member)
			switch member {
			case "LayoutUpdated":
				done = true
			case "ItemsPropertiesUpdated":
				var updated []struct {
					ID    int32
					Props map[string]dbus.Variant
				}
				if err := dbus.Store(s.Body[:1], &updated); err != nil {
					t.Fatalf("ItemsPropertiesUpdated body %v: %v", s.Body, err)
				}
				for _, u := range updated {
					if l, _ := u.Props["label"].Value().(string); u.ID == idA && l == a.Title {
						named = true
					}
				}
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("no LayoutUpdated within 3s; signals seen %v", members)
		}
	}
	if !named {
		t.Errorf("signals %v: no ItemsPropertiesUpdated before LayoutUpdated names id %d with label %q", members, idA, a.Title)
	}
	if _, nodes := h.layout(); stableNoticeIDs(nodes)[1] != idA {
		t.Errorf("retitled notice is no longer id %d: %v", idA, stableNoticeIDs(nodes))
	}
}
