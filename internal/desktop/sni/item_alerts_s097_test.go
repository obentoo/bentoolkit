package sni_test

// Story 097, sub-task 3.2: system alerts drive the item's status, icon
// variant and tooltip (R8.1, R8.2, R8.3, R8.7, R6.8), alongside the unread
// notice count of story 072. The alert-count wording (View.AlertTitle) comes
// from the caller; the item only appends it to the title.
//
// Hostile halves first: a critical notice that is no longer unread must not
// make a warning alert look critical, and unread notices must keep the item
// in attention when the alerts clear. The tooltip description is derived
// text: markup in a path must not become markup, and an alert line carrying a
// newline must not pass for two alerts.
//
// Reuses startWatcher, startItem, observer, itemProp, shownIcon, sameIcon,
// hasSignal, wirePixmap, itemIcons and the sni* constants from item_test.go.

import (
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"

	"github.com/obentoo/bentoolkit/internal/desktop/dbustest"
	"github.com/obentoo/bentoolkit/internal/desktop/sni"
)

// s097Tip is the decoded ToolTip property, (sa(iiay)ss).
type s097Tip struct {
	Icon        string
	Pix         []wirePixmap
	Title, Text string
}

func s097ToolTip(t *testing.T, obs, item *dbus.Conn) s097Tip {
	t.Helper()
	var tip s097Tip
	if err := dbus.Store([]any{itemProp(t, obs, item, "ToolTip").Value()}, &tip); err != nil {
		t.Fatalf("decoding ToolTip (sa(iiay)ss): %v", err)
	}
	return tip
}

// s097Expect checks the status and the icon a host shows.
func s097Expect(t *testing.T, obs, item *dbus.Conn, step, status string, icon []sni.Pixmap, iconName string) {
	t.Helper()
	gotStatus, gotIcon := shownIcon(t, obs, item)
	if gotStatus != status {
		t.Errorf("%s: Status = %q, want %q", step, gotStatus, status)
	}
	if !sameIcon(gotIcon, icon) {
		t.Errorf("%s: shown icon = %+v, want the %s variant", step, gotIcon, iconName)
	}
}

func s097Item(t *testing.T) (*sni.Item, *dbus.Conn, *dbus.Conn) {
	t.Helper()
	addr := dbustest.Session(t)
	startWatcher(t, addr)
	item, conn, _ := startItem(t, addr)
	return item, conn, observer(t, addr)
}

// TestS097Item_ReadCriticalNoticeDoesNotMakeAWarningAlertCritical is R8.2's
// hostile half: View.Critical describes unread notices only, so with none
// unread a warning alert shows the warning variant, not the critical one.
func TestS097Item_ReadCriticalNoticeDoesNotMakeAWarningAlertCritical(t *testing.T) {
	item, conn, obs := s097Item(t)
	item.SetState(sni.View{Unread: 0, Critical: true, Alerts: []string{"Low disk space on /"}})
	s097Expect(t, obs, conn, "warning alert, critical flag with no unread notice",
		"NeedsAttention", itemIcons.Unread, "unread (warning)")
}

// TestS097Item_CriticalAlertWinsOverAWarningNotice is the converse: a
// critical alert shows the critical variant even when the unread notices are
// not critical.
func TestS097Item_CriticalAlertWinsOverAWarningNotice(t *testing.T) {
	item, conn, obs := s097Item(t)
	item.SetState(sni.View{Unread: 3, Alerts: []string{"Memory almost exhausted"}, AlertCritical: true})
	s097Expect(t, obs, conn, "critical alert, non-critical unread notices",
		"NeedsAttention", itemIcons.Critical, "critical")
}

// TestS097Item_AlertsDriveAttentionAndClearingReturnsPlain is R8.1, R8.2,
// R6.8 and R8.7 as one walk. Unread notices with no alert left keep the
// item in attention: only both empty returns it to plain/Active.
func TestS097Item_AlertsDriveAttentionAndClearingReturnsPlain(t *testing.T) {
	item, conn, obs := s097Item(t)
	disk := "Low disk space on /var/tmp/portage"
	mem := "Memory almost exhausted"
	steps := []struct {
		name     string
		view     sni.View
		status   string
		icon     []sni.Pixmap
		iconName string
	}{
		{"nothing", sni.View{}, "Active", itemIcons.Plain, "plain"},
		{"one warning alert, no unread", sni.View{Alerts: []string{disk}}, "NeedsAttention", itemIcons.Unread, "unread (warning)"},
		{"a critical alert joins", sni.View{Alerts: []string{disk, mem}, AlertCritical: true}, "NeedsAttention", itemIcons.Critical, "critical"},
		{"critical falls back to warning", sni.View{Alerts: []string{disk, mem}}, "NeedsAttention", itemIcons.Unread, "unread (warning)"},
		{"alerts cleared, notices unread", sni.View{Unread: 2}, "NeedsAttention", itemIcons.Unread, "unread"},
		{"alerts cleared, nothing unread", sni.View{}, "Active", itemIcons.Plain, "plain"},
	}
	for _, s := range steps {
		item.SetState(s.view)
		s097Expect(t, obs, conn, s.name, s.status, s.icon, s.iconName)
	}
}

// s097BaseTitle is the title the item shows for unread notices alone: the
// unread-count text an alert title is appended to.
func s097BaseTitle(t *testing.T, item *sni.Item, obs, conn *dbus.Conn, unread int) string {
	t.Helper()
	item.SetState(sni.View{Unread: unread})
	base := s097ToolTip(t, obs, conn).Title
	if base == "" {
		t.Fatalf("no alert, %d unread: empty tooltip title", unread)
	}
	return base
}

// TestS097Item_EmptyAlertTitleAddsNothing is the hostile half of R8.3's
// title: the alert-count wording is the tray's (from its message catalog),
// so with alerts present but no AlertTitle the title is exactly the
// unread-count text; the item invents no suffix of its own.
func TestS097Item_EmptyAlertTitleAddsNothing(t *testing.T) {
	item, conn, obs := s097Item(t)
	for _, unread := range []int{7, 0} {
		base := s097BaseTitle(t, item, obs, conn, unread)
		item.SetState(sni.View{Unread: unread, Alerts: []string{"Low disk space on /", "CPU saturated"}})
		if got := s097ToolTip(t, obs, conn).Title; got != base {
			t.Errorf("%d unread, two alerts, empty AlertTitle: title = %q, want exactly %q", unread, got, base)
		}
	}
}

// TestS097Item_TooltipTitleCarriesTheAlertTitle is R8.3 and the GNOME
// gotcha: the AppIndicator extension shows only the title, so the caller's
// AlertTitle follows the unread-count text there; without alerts it is gone.
func TestS097Item_TooltipTitleCarriesTheAlertTitle(t *testing.T) {
	item, conn, obs := s097Item(t)
	base := s097BaseTitle(t, item, obs, conn, 7)

	for _, tc := range []struct {
		alerts []string
		title  string
	}{
		{[]string{"Low disk space on /"}, "1 system alert"},
		{[]string{"Low disk space on /", "CPU saturated"}, "2 system alerts"},
	} {
		item.SetState(sni.View{Unread: 7, Alerts: tc.alerts, AlertTitle: tc.title})
		got := s097ToolTip(t, obs, conn).Title
		if !strings.HasPrefix(got, base) || !strings.HasSuffix(got, tc.title) || len(got) <= len(base) {
			t.Errorf("AlertTitle %q: title = %q, want %q followed by %q", tc.title, got, base, tc.title)
		}
		if strings.Count(got, "system alert") != 1 {
			t.Errorf("AlertTitle %q: title = %q carries the alert count more than once", tc.title, got)
		}
	}

	item.SetState(sni.View{Unread: 7})
	tip := s097ToolTip(t, obs, conn)
	if tip.Title != base {
		t.Errorf("alerts cleared: title = %q, want %q", tip.Title, base)
	}
	if tip.Text != "" {
		t.Errorf("alerts cleared: description = %q, want empty", tip.Text)
	}
}

// TestS097Item_TooltipDescriptionEscapesMarkup is R8.3 and the tooltip
// gotcha: KDE renders the description as rich text, so &, < and > in an
// alert (a path) are escaped, and text that already looks like an entity is
// escaped again. One line per alert, in order.
func TestS097Item_TooltipDescriptionEscapesMarkup(t *testing.T) {
	item, conn, obs := s097Item(t)
	item.SetState(sni.View{Alerts: []string{
		"Low disk space on /mnt/a&b<i>c</i>",
		"Path /srv/&lt;x&gt; > 90%",
	}})
	want := "Low disk space on /mnt/a&amp;b&lt;i&gt;c&lt;/i&gt;\nPath /srv/&amp;lt;x&amp;gt; &gt; 90%"
	if got := s097ToolTip(t, obs, conn).Text; got != want {
		t.Errorf("description = %q, want %q", got, want)
	}
}

// TestS097Item_AlertWithANewlineCannotFakeAnotherLine is the hostile half of
// the derived description: alerts are joined by newlines, so an alert whose
// text carries one (a legal character in a Linux path) would render exactly
// like two alerts. One alert stays one line.
func TestS097Item_AlertWithANewlineCannotFakeAnotherLine(t *testing.T) {
	item, conn, obs := s097Item(t)

	item.SetState(sni.View{Alerts: []string{"Low disk space on /mnt/a\nMemory almost exhausted"}})
	one := s097ToolTip(t, obs, conn).Text
	if n := strings.Count(one, "\n"); n != 0 {
		t.Errorf("one alert rendered as %d lines: %q", n+1, one)
	}

	item.SetState(sni.View{Alerts: []string{"Low disk space on /mnt/a", "Memory almost exhausted"}})
	two := s097ToolTip(t, obs, conn).Text
	if n := strings.Count(two, "\n"); n != 1 {
		t.Errorf("two alerts rendered as %d lines, want 2: %q", n+1, two)
	}
	if one == two {
		t.Errorf("one alert with a newline and two alerts render identically: %q", one)
	}
}

// s097WatchItem subscribes obs to the item's signals after a round trip, so
// no signal of an earlier SetState is still on its way.
func s097WatchItem(t *testing.T, obs, conn *dbus.Conn) chan *dbus.Signal {
	t.Helper()
	_ = s097ToolTip(t, obs, conn)
	if err := obs.AddMatchSignal(dbus.WithMatchSender(conn.Names()[0]), dbus.WithMatchInterface(sniIface)); err != nil {
		t.Fatal(err)
	}
	ch := make(chan *dbus.Signal, 64)
	obs.Signal(ch)
	return ch
}

// s097AwaitNewToolTip waits up to 3s for NewToolTip.
func s097AwaitNewToolTip(t *testing.T, ch chan *dbus.Signal, what string) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case s := <-ch:
			if strings.HasSuffix(s.Name, ".NewToolTip") {
				return
			}
		case <-deadline:
			t.Fatalf("no NewToolTip within 3s after %s", what)
		}
	}
}

// s097NoNewToolTip fails if NewToolTip arrives within 300ms.
func s097NoNewToolTip(t *testing.T, ch chan *dbus.Signal, what string) {
	t.Helper()
	quiet := time.After(300 * time.Millisecond)
	for {
		select {
		case s := <-ch:
			if strings.HasSuffix(s.Name, ".NewToolTip") {
				t.Fatalf("NewToolTip emitted for %s", what)
			}
		case <-quiet:
			return
		}
	}
}

// TestS097Item_DescriptionOnlyChangeEmitsNewToolTip: the unread count and
// AlertTitle are unchanged, so the title is too; only the description
// differs, and hosts must still be told to re-read the tooltip. The same view
// again changes nothing and emits nothing.
func TestS097Item_DescriptionOnlyChangeEmitsNewToolTip(t *testing.T) {
	item, conn, obs := s097Item(t)
	item.SetState(sni.View{Unread: 1, Alerts: []string{"Low disk space on /"}, AlertTitle: "1 system alert"})
	ch := s097WatchItem(t, obs, conn)

	second := sni.View{Unread: 1, Alerts: []string{"Low disk space on /var/tmp/portage"}, AlertTitle: "1 system alert"}
	item.SetState(second)
	s097AwaitNewToolTip(t, ch, "a description-only change")
	if got := s097ToolTip(t, obs, conn).Text; got != "Low disk space on /var/tmp/portage" {
		t.Errorf("description = %q after the change, want the new alert", got)
	}

	item.SetState(second)
	s097NoNewToolTip(t, ch, "an unchanged view")
}

// TestS097Item_AlertTitleOnlyChangeEmitsNewToolTip: same unread count, same
// alert lines, only the caller's AlertTitle differs; the title changes, so
// hosts are told.
func TestS097Item_AlertTitleOnlyChangeEmitsNewToolTip(t *testing.T) {
	item, conn, obs := s097Item(t)
	alerts := []string{"Low disk space on /", "CPU saturated"}
	item.SetState(sni.View{Unread: 1, Alerts: alerts})
	ch := s097WatchItem(t, obs, conn)

	item.SetState(sni.View{Unread: 1, Alerts: alerts, AlertTitle: "2 system alerts"})
	s097AwaitNewToolTip(t, ch, "an AlertTitle-only change")
	if got := s097ToolTip(t, obs, conn).Title; !strings.HasSuffix(got, "2 system alerts") {
		t.Errorf("title = %q after the change, want it to end with %q", got, "2 system alerts")
	}
}
