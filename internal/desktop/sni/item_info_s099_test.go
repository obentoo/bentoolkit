package sni_test

// Story 099, sub-task 4.2: View.Info carries informational tooltip lines
// (pending @world updates) shown after the alerts. They never change the
// status or the icon variant (R2.12, R5.3).
//
// Hostile halves first: an info line whose text is exactly an alert's must
// still not ask for attention; info lines must not soften an alert's
// attention nor its critical variant; with no alert the description must not
// open on an empty line; an info line carrying a newline or markup must not
// fake a second line or become markup.
//
// Reuses s097Item, s097Expect and s097ToolTip from item_alerts_s097_test.go
// and itemIcons from item_test.go.

import (
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/desktop/sni"
)

const (
	s099Updates = "12 package updates pending"
	s099Disk    = "Low disk space on /var/tmp/portage"
	s099Mem     = "Memory almost exhausted"
)

// TestS099Item_InfoAloneStaysPlain is R2.12's hostile half: the same text that
// asks for attention as an alert stays plain/Active as an info line.
func TestS099Item_InfoAloneStaysPlain(t *testing.T) {
	item, conn, obs := s097Item(t)

	item.SetState(sni.View{Alerts: []string{s099Updates}})
	s097Expect(t, obs, conn, "the text as an alert", "NeedsAttention", itemIcons.Unread, "unread (warning)")

	item.SetState(sni.View{Info: []string{s099Updates}})
	s097Expect(t, obs, conn, "the same text as an info line", "Active", itemIcons.Plain, "plain")

	item.SetState(sni.View{Info: []string{s099Updates, "@world does not resolve"}})
	s097Expect(t, obs, conn, "two info lines", "Active", itemIcons.Plain, "plain")
}

// TestS099Item_InfoKeepsTheAlertStatusAndVariant is the converse: info lines
// beside alerts leave the warning and critical looks exactly as alerts alone.
func TestS099Item_InfoKeepsTheAlertStatusAndVariant(t *testing.T) {
	item, conn, obs := s097Item(t)
	steps := []struct {
		name     string
		view     sni.View
		status   string
		icon     []sni.Pixmap
		iconName string
	}{
		{"warning alert and info", sni.View{Alerts: []string{s099Disk}, Info: []string{s099Updates}},
			"NeedsAttention", itemIcons.Unread, "unread (warning)"},
		{"critical alert and info", sni.View{Alerts: []string{s099Disk, s099Mem}, AlertCritical: true, Info: []string{s099Updates}},
			"NeedsAttention", itemIcons.Critical, "critical"},
		{"alerts cleared, info stays", sni.View{Info: []string{s099Updates}},
			"Active", itemIcons.Plain, "plain"},
	}
	for _, s := range steps {
		item.SetState(s.view)
		s097Expect(t, obs, conn, s.name, s.status, s.icon, s.iconName)
	}
}

// TestS099Item_InfoWithUnreadNoticesActsAsUnreadAlone: with unread notices,
// adding info lines changes neither the status nor the variant, critical or
// not.
func TestS099Item_InfoWithUnreadNoticesActsAsUnreadAlone(t *testing.T) {
	item, conn, obs := s097Item(t)
	for _, tc := range []struct {
		name     string
		base     sni.View
		icon     []sni.Pixmap
		iconName string
	}{
		{"unread notices", sni.View{Unread: 2}, itemIcons.Unread, "unread"},
		{"a critical unread notice", sni.View{Unread: 1, Critical: true}, itemIcons.Critical, "critical"},
	} {
		item.SetState(tc.base)
		s097Expect(t, obs, conn, tc.name+" alone", "NeedsAttention", tc.icon, tc.iconName)
		withInfo := tc.base
		withInfo.Info = []string{s099Updates}
		item.SetState(withInfo)
		s097Expect(t, obs, conn, tc.name+" and an info line", "NeedsAttention", tc.icon, tc.iconName)
	}
}

// TestS099Item_TooltipListsAlertsThenInfo is R5.4's display half: one line per
// alert in order, then one line per info line in order.
func TestS099Item_TooltipListsAlertsThenInfo(t *testing.T) {
	item, conn, obs := s097Item(t)
	item.SetState(sni.View{
		Alerts: []string{s099Disk, s099Mem},
		Info:   []string{s099Updates, "second info"},
	})
	want := s099Disk + "\n" + s099Mem + "\n" + s099Updates + "\nsecond info"
	if got := s097ToolTip(t, obs, conn).Text; got != want {
		t.Errorf("description = %q, want %q", got, want)
	}
}

// TestS099Item_InfoOnlyDescriptionHasNoBlankLine is the hostile half of the
// join: with no alert the description is the info lines alone, not an empty
// alert line followed by them.
func TestS099Item_InfoOnlyDescriptionHasNoBlankLine(t *testing.T) {
	item, conn, obs := s097Item(t)
	item.SetState(sni.View{Info: []string{s099Updates}})
	if got := s097ToolTip(t, obs, conn).Text; got != s099Updates {
		t.Errorf("info-only description = %q, want exactly %q", got, s099Updates)
	}
	item.SetState(sni.View{Alerts: []string{s099Disk}})
	if got := s097ToolTip(t, obs, conn).Text; got != s099Disk {
		t.Errorf("alert-only description = %q, want exactly %q (no trailing info separator)", got, s099Disk)
	}
}

// TestS099Item_InfoLinesAreEscapedAndOneLineEach: info lines get the alerts'
// treatment, so markup stays text and a newline cannot fake another line.
func TestS099Item_InfoLinesAreEscapedAndOneLineEach(t *testing.T) {
	item, conn, obs := s097Item(t)

	item.SetState(sni.View{Info: []string{"x11-libs/gtk+ & <b>dev-qt/qtbase</b>"}})
	want := "x11-libs/gtk+ &amp; &lt;b&gt;dev-qt/qtbase&lt;/b&gt;"
	if got := s097ToolTip(t, obs, conn).Text; got != want {
		t.Errorf("escaped info description = %q, want %q", got, want)
	}

	item.SetState(sni.View{Info: []string{"12 package updates pending\nMemory almost exhausted"}})
	one := s097ToolTip(t, obs, conn).Text
	if n := strings.Count(one, "\n"); n != 0 {
		t.Errorf("one info line rendered as %d lines: %q", n+1, one)
	}
	item.SetState(sni.View{Info: []string{"12 package updates pending", "Memory almost exhausted"}})
	if two := s097ToolTip(t, obs, conn).Text; two == one {
		t.Errorf("one info line with a newline and two info lines render identically: %q", one)
	}
}

// TestS099Item_InfoDoesNotChangeTheTitle: the item adds no text of its own, so
// info lines leave the tooltip title as the unread count alone gives it.
func TestS099Item_InfoDoesNotChangeTheTitle(t *testing.T) {
	item, conn, obs := s097Item(t)
	item.SetState(sni.View{Unread: 3})
	base := s097ToolTip(t, obs, conn).Title
	item.SetState(sni.View{Unread: 3, Info: []string{s099Updates}})
	if got := s097ToolTip(t, obs, conn).Title; got != base {
		t.Errorf("title with an info line = %q, want %q", got, base)
	}
}
