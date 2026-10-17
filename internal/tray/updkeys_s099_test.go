package tray

// Story 099, sub-task 5.2: notification keys and the tooltip split
// (R5.4, R5.5, R2.12, R5.3).
//
// Pinned contract (design.md, "internal/tray/health.go and sample.go"):
// monitors "updates", "glsa", "sync"; notificationKey returns "updates" for
// the one updates condition (count or "does not resolve"), "glsa" for every
// GLSA (one shared note) and "sync:<repo>" per repository. alerts() returns
// (alerts []string, critical bool, info []string): story 098's lines, then
// the GLSA lines, then the sync lines in alerts; the updates line in info.
//
// Hostile halves first: near-identical repositories must stay apart and no
// 099 key may reach any 097/098 key (a withdraw would close the wrong
// notification); then the converse, two GLSA ids and the two updates texts
// that must come together on one key.

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/config"
)

func TestS099Keys_R5_5_SimilarRepositoriesNeverShareAKey(t *testing.T) {
	pairs := [][2]string{
		{"gentoo", "gentoo-zh"},
		{"guru", "guru "},
		{"bentoo", "Bentoo"},
		{"a", "a:b"},
	}
	for _, p := range pairs {
		a := notificationKey(condKey{Monitor: "sync", Target: p[0]})
		b := notificationKey(condKey{Monitor: "sync", Target: p[1]})
		if a == b {
			t.Errorf("repositories %q and %q share the key %q", p[0], p[1], a)
		}
	}
}

// TestS099Keys_R5_5_NoKeyCollidesAcrossMonitors: every 097, 098 and 099
// key, including a repository whose name spells another monitor's key, is
// distinct; the comparison runs over the whole set, not pairs, so a third
// element reaching the same rendered key is caught.
func TestS099Keys_R5_5_NoKeyCollidesAcrossMonitors(t *testing.T) {
	keys := []condKey{
		{Monitor: "updates"},
		{Monitor: "glsa", Target: "202610-01"},
		{Monitor: "sync", Target: "gentoo"},
		{Monitor: "sync", Target: "updates"},
		{Monitor: "sync", Target: "glsa"},
		{Monitor: "sync", Target: "cpu"},
		{Monitor: "sync", Target: "units"},
		{Monitor: "sync", Target: "emerge"},
		{Monitor: "sync", Target: "/x"},
		{Monitor: "sync", Target: "202610-01"},
		{Monitor: "disk", Target: "/x"},
		{Monitor: "disk", Target: "sync:gentoo"},
		{Monitor: "readonly", Target: "/x"},
		{Monitor: "smart", Target: "/x"},
		{Monitor: "memory"},
		{Monitor: "cpu"},
		{Monitor: "network"},
		{Monitor: "emerge"},
		{Monitor: "units", Target: "system:a.service"},
	}
	seen := map[string]condKey{}
	for _, k := range keys {
		got := notificationKey(k)
		if other, dup := seen[got]; dup {
			t.Errorf("notificationKey(%+v) = notificationKey(%+v) = %q", k, other, got)
		}
		seen[got] = k
	}
}

func TestS099Keys_R3_8_R5_5_EveryGLSASharesOneKey(t *testing.T) {
	a := notificationKey(condKey{Monitor: "glsa", Target: "202610-01"})
	b := notificationKey(condKey{Monitor: "glsa", Target: "202509-17"})
	if a != b || a != "glsa" {
		t.Errorf("GLSA keys = %q and %q, want both %q (one shared note)", a, b, "glsa")
	}
}

func TestS099Keys_R5_5_ExactKeys(t *testing.T) {
	cases := []struct {
		key  condKey
		want string
	}{
		{condKey{Monitor: "updates"}, "updates"},
		{condKey{Monitor: "glsa", Target: "202610-01"}, "glsa"},
		{condKey{Monitor: "sync", Target: "gentoo"}, "sync:gentoo"},
		{condKey{Monitor: "sync", Target: "gentoo-zh"}, "sync:gentoo-zh"},
		{condKey{Monitor: "cpu"}, "cpu"},
		{condKey{Monitor: "memory"}, "memory"},
	}
	for _, tc := range cases {
		if got := notificationKey(tc.key); got != tc.want {
			t.Errorf("notificationKey(%+v) = %q, want %q", tc.key, got, tc.want)
		}
	}
}

// s099KeysApp is an App with no running loop, for alerts().
func s099KeysApp(t *testing.T, diskPaths ...string) *App {
	t.Helper()
	mon, _ := config.TrayConfig{}.GetMonitors()
	a := New(Deps{DiskPaths: diskPaths, Monitors: &mon})
	a.network = newNetWatch(time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC), mon.Network)
	return a
}

func s099Active(a *App, k condKey, l level, text string) {
	a.health[k] = &condition{level: l, text: text}
}

func TestS099Keys_R5_4_TooltipOrderGLSAThenSyncAfter098(t *testing.T) {
	a := s099KeysApp(t, "/")
	s099Active(a, condKey{Monitor: "sync", Target: "guru"}, levelWarning, "SYNC guru")
	s099Active(a, condKey{Monitor: "updates"}, levelNotice, "UPDATES 12")
	s099Active(a, condKey{Monitor: "glsa", Target: "202610-02"}, levelWarning, "GLSA 202610-02")
	s099Active(a, condKey{Monitor: "emerge"}, levelWarning, "EMERGE")
	s099Active(a, condKey{Monitor: "sync", Target: "gentoo"}, levelWarning, "SYNC gentoo")
	s099Active(a, condKey{Monitor: "disk", Target: "/"}, levelWarning, "DISK /")
	s099Active(a, condKey{Monitor: "glsa", Target: "202610-01"}, levelWarning, "GLSA 202610-01")

	var first []string
	for i := range 20 { // map order must not leak into the tooltip
		lines, critical, info := a.alerts()
		if critical {
			t.Fatal("critical = true with only warnings and a notice active")
		}
		if !slices.Equal(info, []string{"UPDATES 12"}) {
			t.Fatalf("info = %q, want only the updates line", info)
		}
		if slices.Contains(lines, "UPDATES 12") {
			t.Fatalf("alerts = %q carry the updates line; it belongs to info (R2.12)", lines)
		}
		if len(lines) != 6 {
			t.Fatalf("alerts = %q, want 6 lines", lines)
		}
		if !slices.Equal(lines[:2], []string{"DISK /", "EMERGE"}) {
			t.Fatalf("alerts = %q, want story 097/098 lines first", lines)
		}
		for j, l := range lines[2:4] {
			if !strings.HasPrefix(l, "GLSA ") {
				t.Fatalf("alerts[%d] = %q, want the GLSA lines right after story 098's", j+2, l)
			}
		}
		for j, l := range lines[4:] {
			if !strings.HasPrefix(l, "SYNC ") {
				t.Fatalf("alerts[%d] = %q, want the sync lines after the GLSA lines", j+4, l)
			}
		}
		if i == 0 {
			first = lines
		} else if !slices.Equal(lines, first) {
			t.Fatalf("alerts order changed between calls: %q then %q", first, lines)
		}
	}
}

func TestS099Keys_R2_12_DoesNotResolveGoesToInfoToo(t *testing.T) {
	a := s099KeysApp(t)
	s099Active(a, condKey{Monitor: "updates"}, levelNotice, "WORLD BROKEN")
	lines, critical, info := a.alerts()
	if len(lines) != 0 || critical {
		t.Errorf("alerts = %q critical %v, want none: a notice never alerts", lines, critical)
	}
	if !slices.Equal(info, []string{"WORLD BROKEN"}) {
		t.Errorf("info = %q, want the \"does not resolve\" line", info)
	}
}

func TestS099Keys_R5_4_ClearedAndTextlessAreNotListed(t *testing.T) {
	a := s099KeysApp(t)
	s099Active(a, condKey{Monitor: "glsa", Target: "202610-01"}, levelOK, "")
	s099Active(a, condKey{Monitor: "sync", Target: "gentoo"}, levelWarning, "")
	s099Active(a, condKey{Monitor: "updates"}, levelOK, "")
	lines, _, info := a.alerts()
	if len(lines) != 0 || len(info) != 0 {
		t.Errorf("alerts = %q info = %q, want both empty", lines, info)
	}
}
