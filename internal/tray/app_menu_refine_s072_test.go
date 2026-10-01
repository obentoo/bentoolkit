package tray_test

// Refine 12.5 of story 072 (R9.1, R9.7): the App opens the notice a menu
// event names in NoticeID, resolved by the sni package at click time, and
// never maps an item id to a position in the last view.

import (
	"testing"

	"github.com/obentoo/bentoolkit/internal/desktop/sni"
	"github.com/obentoo/bentoolkit/internal/gentoo/pkgdb"
	"github.com/obentoo/bentoolkit/internal/tray/feed"
)

// menuRefineIndexURL is what menu 100 opens; used as a marker that the loop
// has handled every event sent before it.
const menuRefineIndexURL = "https://obentoo.org/notices/"

// TestApp_MenuEventWithEmptyNoticeIDOpensNothing is R9.7's "ignore a click on
// an id that no longer lists a notice", hostile first: the old positional id
// 1, which the current view still fills with a notice, and a notice-range id
// both carry an empty NoticeID, and neither opens anything.
func TestApp_MenuEventWithEmptyNoticeIDOpensNothing(t *testing.T) {
	h := withCriticalNotice(t)
	h.viewWhere("the notice listed in the menu", func(v sni.View) bool {
		return len(v.Entries) == 1 && v.Entries[0].NoticeID == fooCVE().ID
	})

	h.icon.events <- sni.Event{ItemID: 1}
	h.icon.events <- sni.Event{ItemID: 1000}
	h.menu(100) // marker: handled after the two above
	h.waitFor("the marker opening the index", func() bool {
		for _, c := range h.open.opened() {
			if c.URL == menuRefineIndexURL {
				return true
			}
		}
		return false
	})
	h.never("a menu event with an empty NoticeID opened something", func() bool {
		for _, c := range h.open.opened() {
			if c.URL != menuRefineIndexURL {
				return true
			}
		}
		return false
	})
}

// TestApp_MenuEventOpensItsNoticeIDNotThePosition is the converse: an event
// naming a listed notice opens that notice, even when its item id is the one
// a positional mapping would give to the other listed notice.
func TestApp_MenuEventOpensItsNoticeIDNotThePosition(t *testing.T) {
	h := newApp(t)
	h.pkgs.list = []pkgdb.Package{bentooFoo}
	rel := releaseNotice("2026-10-01-bentoolkit-0-33", "bentoolkit 0.33 released")
	h.feed.next = func(int) (feed.Result, error) { return okResult(1790812800, fooCVE(), rel), nil }
	h.start()
	h.checkNow()
	h.viewWhere("both notices listed", func(v sni.View) bool { return len(v.Entries) == 2 })

	h.icon.events <- sni.Event{ItemID: 1000, NoticeID: rel.ID}
	h.waitFor("the opener call", func() bool { return len(h.open.opened()) >= 1 })
	h.never("a second opening", func() bool { return len(h.open.opened()) > 1 })
	if c := h.open.opened()[0]; c.URL != rel.URL {
		t.Errorf("menu event naming %q opened %q, want %q", rel.ID, c.URL, rel.URL)
	}

	h.icon.events <- sni.Event{ItemID: 1001, NoticeID: fooCVE().ID}
	h.waitFor("the second opener call", func() bool { return len(h.open.opened()) >= 2 })
	if c := h.open.opened()[1]; c.URL != fooCVE().URL {
		t.Errorf("menu event naming %q opened %q, want %q", fooCVE().ID, c.URL, fooCVE().URL)
	}
}
