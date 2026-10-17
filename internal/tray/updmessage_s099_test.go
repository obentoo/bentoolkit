package tray

// Story 099, sub-task 5.2: the notification and tooltip builders of the
// update monitors, and render handing the informational lines to the view.
// The authored key tests pin the keys and the alerts split; these pin what
// the messages say.

import (
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/obentoo/bentoolkit/internal/gentoo/pending"
)

func TestS099KeysMessage_UpdatesNamesCountFirstTwoAndMore(t *testing.T) {
	cases := []struct {
		names []string
		want  string // the names part, as body and line show it
	}{
		{[]string{"dev-qt/qtbase"}, "dev-qt/qtbase"},
		{[]string{"dev-qt/qtbase", "sys-libs/glibc"}, "dev-qt/qtbase, sys-libs/glibc"},
		{[]string{"dev-qt/qtbase", "sys-libs/glibc", "app-misc/jq"}, "dev-qt/qtbase, sys-libs/glibc and 1 more"},
		{[]string{"a/b", "c/d", "e/f", "g/h", "i/j"}, "a/b, c/d and 3 more"},
	}
	for _, tc := range cases {
		m, line := updatesSysMessage(tc.names, 7, urgencyLow)
		count := strconv.Itoa(len(tc.names))
		if m.Key != "updates" || m.ReplacesID != 7 || m.Urgency != urgencyLow || !m.System {
			t.Errorf("%v: message key/replaces/urgency/system = %q/%d/%d/%v", tc.names, m.Key, m.ReplacesID, m.Urgency, m.System)
		}
		for name, s := range map[string]string{"body": m.Body, "line": line} {
			if !strings.Contains(s, tc.want) || !strings.Contains(s, count) {
				t.Errorf("%v: %s = %q, want the count %s and %q", tc.names, name, s, count, tc.want)
			}
		}
		if len(tc.names) > 2 && strings.Contains(m.Body, tc.names[2]) {
			t.Errorf("%v: body %q names a third package", tc.names, m.Body)
		}
	}
}

func TestS099KeysMessage_WorldBrokenSharesTheUpdatesKey(t *testing.T) {
	m, line := worldBrokenSysMessage(9, urgencyLow)
	u, uline := updatesSysMessage([]string{"a/b"}, 9, urgencyLow)
	if m.Key != u.Key || m.Key != "updates" {
		t.Errorf("keys = %q and %q, want both \"updates\"", m.Key, u.Key)
	}
	if !strings.Contains(m.Body, "emerge -puDN @world") {
		t.Errorf("body = %q, want it to tell the user to run emerge -puDN @world", m.Body)
	}
	if m.Summary == u.Summary || line == uline || line == "" {
		t.Errorf("does-not-resolve reads like pending updates: summary %q vs %q, line %q vs %q", m.Summary, u.Summary, line, uline)
	}
}

func TestS099KeysMessage_GLSANamesIDTitleAtomsAndCount(t *testing.T) {
	g := pending.GLSA{ID: "202610-01", Title: "OpenSSL: Multiple Vulnerabilities (CVE-2026-1)", Atoms: []string{"dev-libs/openssl", "net-libs/nodejs"}}
	m, line := glsaSysMessage(g, 3, 4, urgencyNormal)
	if m.Key != "glsa" || m.ReplacesID != 4 {
		t.Errorf("key/replaces = %q/%d, want glsa/4", m.Key, m.ReplacesID)
	}
	for _, want := range []string{g.ID, g.Title, "dev-libs/openssl", "net-libs/nodejs", "3"} {
		if !strings.Contains(m.Body, want) {
			t.Errorf("body = %q, want %q in it", m.Body, want)
		}
	}
	if !strings.Contains(line, g.ID) || !strings.Contains(line, g.Title) {
		t.Errorf("line = %q, want the id and the title", line)
	}
}

func TestS099KeysMessage_SyncNamesRepositoryAndWholeDays(t *testing.T) {
	cases := []struct {
		age  time.Duration
		want string
	}{
		{8*24*time.Hour + 23*time.Hour, "8 days"},
		{7 * 24 * time.Hour, "7 days"},
		{24*time.Hour + time.Hour, "1 day "},
		{-time.Hour, "0 days"}, // a clock set back never reads negative
	}
	for _, tc := range cases {
		m, line := syncSysMessage("gentoo-zh", tc.age, 2, urgencyNormal)
		if m.Key != "sync:gentoo-zh" {
			t.Errorf("key = %q, want sync:gentoo-zh", m.Key)
		}
		if !strings.Contains(m.Summary, "gentoo-zh") {
			t.Errorf("summary = %q, want the repository", m.Summary)
		}
		for name, s := range map[string]string{"body": m.Body + " ", "line": line + " "} {
			if !strings.Contains(s, "gentoo-zh") || !strings.Contains(s, tc.want) {
				t.Errorf("age %v: %s = %q, want the repository and %q", tc.age, name, s, tc.want)
			}
		}
	}
}

// TestS099KeysMessage_HostFieldsAreClipped: package names, GLSA fields and
// repository names arrive from host commands and files; each is cut to
// maxField bytes, invalid UTF-8 becomes U+FFFD and a control character a
// space, so no field can break a notification line.
func TestS099KeysMessage_HostFieldsAreClipped(t *testing.T) {
	long := strings.Repeat("x", 300)
	hostile := "a\nb\x00c\xff"
	var texts []string
	m, line := updatesSysMessage([]string{"cat/" + long, hostile}, 0, urgencyLow)
	texts = append(texts, m.Body, line)
	g := pending.GLSA{ID: hostile, Title: long, Atoms: []string{long, hostile}}
	m, line = glsaSysMessage(g, 1, 0, urgencyNormal)
	texts = append(texts, m.Body, line)
	m, line = syncSysMessage(long+hostile, 9*24*time.Hour, 0, urgencyNormal)
	texts = append(texts, m.Summary, m.Body, line)
	for _, s := range texts {
		if !utf8.ValidString(s) || strings.ContainsAny(s, "\n\x00") {
			t.Errorf("text %q is not one valid UTF-8 line", s)
		}
		if strings.Contains(s, strings.Repeat("x", maxField+1)) {
			t.Errorf("text %q carries a field over %d bytes", s, maxField)
		}
	}
	if !strings.Contains(texts[0], "a b c�") {
		t.Errorf("body %q: want the control characters as spaces and the invalid byte as U+FFFD", texts[0])
	}
}

func TestS099KeysMessage_RenderSetsInfoApartFromAlerts(t *testing.T) {
	a := s099KeysApp(t)
	s099Active(a, condKey{Monitor: "updates"}, levelNotice, "UPDATES 3")
	s099Active(a, condKey{Monitor: "glsa", Target: "202610-01"}, levelWarning, "GLSA 202610-01")
	a.render()
	if !slices.Equal(a.view.Info, []string{"UPDATES 3"}) {
		t.Errorf("view.Info = %q, want the updates line", a.view.Info)
	}
	if !slices.Equal(a.view.Alerts, []string{"GLSA 202610-01"}) || a.view.AlertCritical {
		t.Errorf("view.Alerts = %q critical %v, want only the GLSA line, not critical", a.view.Alerts, a.view.AlertCritical)
	}

	delete(a.health, condKey{Monitor: "glsa", Target: "202610-01"})
	a.render()
	if len(a.view.Alerts) != 0 || a.view.AlertTitle != "" {
		t.Errorf("updates alone: Alerts = %q title %q, want none: a notice never counts as an alert", a.view.Alerts, a.view.AlertTitle)
	}
	if !slices.Equal(a.view.Info, []string{"UPDATES 3"}) {
		t.Errorf("updates alone: view.Info = %q", a.view.Info)
	}
}

// TestS099KeysMessage_TextlessNoticeIsNotAnInfoLine: an active notice with no
// text adds no info entry, which the tooltip would render as a blank line.
func TestS099KeysMessage_TextlessNoticeIsNotAnInfoLine(t *testing.T) {
	a := s099KeysApp(t)
	s099Active(a, condKey{Monitor: "updates"}, levelNotice, "")
	lines, critical, info := a.alerts()
	if len(lines) != 0 || critical || len(info) != 0 {
		t.Errorf("alerts = %q critical %v info = %q, want all empty", lines, critical, info)
	}
	a.render()
	if a.view.Info != nil {
		t.Errorf("view.Info = %q, want nil", a.view.Info)
	}
}
