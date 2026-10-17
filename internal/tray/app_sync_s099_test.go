package tray_test

// Story 099, sub-task 5.5: a repository not synced within max_age raises a
// warning condition of its own (R4.4-R4.6, R5.1-R5.3, R5.6). Harness and
// contract: app_harness_s099_test.go (Deps.Repos, Deps.SyncTime).
//
// Hostile halves first (R5.5 keys each repository apart): gentoo and
// gentoo-zh, one a prefix of the other, must stay two conditions with two
// notifications, and withdrawing one must not close the other; the
// converse, one repository seen stale at several samples, must stay one
// condition with one notification.

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/gentoo/news"
	"github.com/obentoo/bentoolkit/internal/tray"
)

const (
	s099GentooLoc = "/s099-no-such-root/var/db/repos/gentoo"
	s099ZhLoc     = "/s099-no-such-root/var/db/repos/gentoo-zh"
	s099Day       = 24 * time.Hour
)

var s099Repos = []news.Repo{{Name: "gentoo", Location: s099GentooLoc}, {Name: "gentoo-zh", Location: s099ZhLoc}}

func (s *s099Harness) startSync(repos []news.Repo, edit func(*tray.Deps)) {
	s.t.Helper()
	s.startUpd(func(d *tray.Deps) {
		d.Repos = repos
		if edit != nil {
			edit(d)
		}
	})
}

// sampleFor samples once and waits until location was asked at least once more.
func (s *s099Harness) sampleFor(location string) {
	s.t.Helper()
	before := s.syncs.count(location)
	s.sample()
	s.waitFor("the sync time read of "+location, func() bool { return s.syncs.count(location) > before })
}

func TestS099Sync_R5_5_PrefixNamedRepositoriesStayApart(t *testing.T) {
	s := newS099(t)
	s.syncs.set(s099GentooLoc, appStart.Add(-8*s099Day-3*time.Hour))
	s.syncs.set(s099ZhLoc, appStart.Add(-time.Hour))
	s.startSync(s099Repos, nil)
	s.sampleFor(s099GentooLoc)

	if n := len(s.sentTo("sync:gentoo-zh")); n != 0 {
		t.Fatalf("gentoo-zh, synced an hour ago, got %d notifications", n)
	}
	g := s.sentTo("sync:gentoo")
	if len(g) != 1 {
		t.Fatalf("sync:gentoo notifications = %d, want 1", len(g))
	}

	s.syncs.set(s099ZhLoc, appStart.Add(-10*s099Day))
	s.sampleFor(s099ZhLoc)
	zh := s.sentTo("sync:gentoo-zh")
	if len(zh) != 1 {
		t.Fatalf("sync:gentoo-zh notifications = %d, want 1", len(zh))
	}
	if zh[0].Msg.ReplacesID != 0 {
		t.Errorf("gentoo-zh's notification ReplacesID = %d, want 0: it must not replace gentoo's", zh[0].Msg.ReplacesID)
	}
	if !strings.Contains(s099Text(zh[0].Msg), "gentoo-zh") {
		t.Errorf("gentoo-zh's notification %q does not name gentoo-zh", s099Text(zh[0].Msg))
	}
	if n := len(s.sentTo("sync:gentoo")); n != 1 {
		t.Errorf("gentoo-zh going stale resent gentoo's notification (%d sends)", n)
	}

	s.syncs.set(s099GentooLoc, s.clock.Now().Add(-time.Minute)) // gentoo synced
	s.sampleFor(s099GentooLoc)
	if s.closedFor("sync:gentoo") != 1 || s.closedFor("sync:gentoo-zh") != 0 {
		t.Errorf("closes: gentoo %d, gentoo-zh %d; want gentoo's withdrawn only", s.closedFor("sync:gentoo"), s.closedFor("sync:gentoo-zh"))
	}
	for _, id := range s.out.closedIDs() {
		if id == zh[0].ID {
			t.Errorf("gentoo's sync closed gentoo-zh's notification id %d", id)
		}
	}
	lines := s.viewNow().Alerts
	if len(lines) != 1 || !strings.Contains(lines[0], "gentoo-zh") {
		t.Errorf("tooltip alerts = %q, want only gentoo-zh's line", lines)
	}
	s.wantTransitions("sync", "gentoo", [2]string{"ok", "warning"}, [2]string{"warning", "ok"})
	s.wantTransitions("sync", "gentoo-zh", [2]string{"ok", "warning"})
}

func TestS099Sync_R4_4_StaleAcrossSamplesStaysOneNotification(t *testing.T) {
	s := newS099(t)
	s.syncs.set(s099GentooLoc, appStart.Add(-9*s099Day))
	s.startSync(s099Repos[:1], nil)
	for range 6 {
		s.sampleFor(s099GentooLoc)
	}
	if n := len(s.sentTo("sync:gentoo")); n != 1 {
		t.Errorf("sync:gentoo notifications over 6 stale samples = %d, want 1", n)
	}
	if l := s.viewNow().Alerts; len(l) != 1 {
		t.Errorf("tooltip alerts = %q, want one gentoo line", l)
	}
}

func TestS099Sync_R4_4_R4_5_WarningNamesRepositoryAndWholeDays(t *testing.T) {
	s := newS099(t)
	s.syncs.set(s099GentooLoc, appStart.Add(-8*s099Day-5*time.Hour))
	s.startSync(s099Repos[:1], nil)
	s.sampleFor(s099GentooLoc)
	g := s.sentTo("sync:gentoo")
	if len(g) != 1 {
		t.Fatalf("sync:gentoo notifications = %d, want 1", len(g))
	}
	text := s099Text(g[0].Msg)
	if !strings.Contains(text, "gentoo") || !strings.Contains(text, "8 day") {
		t.Errorf("sync notification %q, want the repository name and \"8 days\"", text)
	}
	if strings.Contains(text, "9 day") {
		t.Errorf("sync notification %q rounds 8 days 5 hours up; whole days only", text)
	}
	if g[0].Msg.Urgency != 1 {
		t.Errorf("sync urgency = %d, want 1 (normal: a warning)", g[0].Msg.Urgency)
	}
	v := s.viewNow()
	if len(v.Alerts) != 1 || v.AlertCritical || len(v.Info) != 0 {
		t.Errorf("view Alerts %q critical %v Info %q, want one warning alert (NeedsAttention), no info", v.Alerts, v.AlertCritical, v.Info)
	}
}

func TestS099Sync_R4_4_WithinMaxAgeRaisesNothing(t *testing.T) {
	s := newS099(t)
	s.syncs.set(s099GentooLoc, appStart.Add(-6*s099Day-23*time.Hour))
	s.startSync(s099Repos[:1], nil)
	s.sampleFor(s099GentooLoc)
	s.sampleFor(s099GentooLoc)
	if n := len(s.sentTo("sync:gentoo")); n != 0 {
		t.Errorf("a repository synced 6 days 23 hours ago raised %d notifications, want 0 (default 7 days)", n)
	}
}

func TestS099Sync_R4_6_SyncedAgainWithdraws(t *testing.T) {
	s := newS099(t)
	s.syncs.set(s099GentooLoc, appStart.Add(-8*s099Day))
	s.startSync(s099Repos[:1], nil)
	s.sampleFor(s099GentooLoc)
	g := s.sentTo("sync:gentoo")
	if len(g) != 1 {
		t.Fatalf("sync:gentoo notifications = %d, want 1", len(g))
	}
	s.syncs.set(s099GentooLoc, s.clock.Now())
	s.sampleFor(s099GentooLoc)
	if s.closedFor("sync:gentoo") != 1 {
		t.Errorf("sync:gentoo closes = %d, want 1", s.closedFor("sync:gentoo"))
	}
	if l := s.viewNow().Alerts; len(l) != 0 {
		t.Errorf("tooltip alerts = %q after the sync, want none", l)
	}
}

func TestS099Sync_R6_3_CustomMaxAge(t *testing.T) {
	t.Run("72h makes 4 days stale", func(t *testing.T) {
		s := newS099(t)
		s.cfg = s097Config(t, "monitors:\n  sync:\n    max_age: 72h\n")
		s.syncs.set(s099GentooLoc, appStart.Add(-4*s099Day))
		s.startSync(s099Repos[:1], nil)
		s.sampleFor(s099GentooLoc)
		if n := len(s.sentTo("sync:gentoo")); n != 1 {
			t.Errorf("with max_age 72h a 4-day-old sync raised %d notifications, want 1", n)
		}
	})
	t.Run("720h keeps 10 days fresh", func(t *testing.T) {
		s := newS099(t)
		s.cfg = s097Config(t, "monitors:\n  sync:\n    max_age: 720h\n")
		s.syncs.set(s099GentooLoc, appStart.Add(-10*s099Day))
		s.startSync(s099Repos[:1], nil)
		s.sampleFor(s099GentooLoc)
		if n := len(s.sentTo("sync:gentoo")); n != 0 {
			t.Errorf("with max_age 720h a 10-day-old sync raised %d notifications, want 0", n)
		}
	})
}

func TestS099Sync_ReadErrorWarnsOncePerStreakAndKeepsTheCondition(t *testing.T) {
	s := newS099(t)
	s.syncs.set(s099GentooLoc, appStart.Add(-8*s099Day))
	s.startSync(s099Repos[:1], nil)
	s.sampleFor(s099GentooLoc)
	if len(s.sentTo("sync:gentoo")) != 1 {
		t.Fatal("no stale notification to keep")
	}
	boom := errors.New("stat " + s099GentooLoc + "/metadata/timestamp.chk: permission denied")
	s.syncs.setErr(s099GentooLoc, boom)
	s.sampleFor(s099GentooLoc)
	s.sampleFor(s099GentooLoc)
	s.sampleFor(s099GentooLoc)
	if n := s.warnsFor("sync"); n != 1 {
		t.Errorf("sync WARN lines over a 3-sample failure streak = %d, want 1", n)
	}
	if !s.logLine(func(l string) bool {
		return strings.Contains(l, "level=WARN") && strings.Contains(l, "monitor=sync") &&
			strings.Contains(l, "gentoo") && strings.Contains(l, s099GentooLoc)
	}) {
		t.Error("the sync WARN does not name the repository and its path")
	}
	if s.closedFor("sync:gentoo") != 0 || len(s.viewNow().Alerts) != 1 {
		t.Errorf("a failed read cleared the condition: closes %d, alerts %q", s.closedFor("sync:gentoo"), s.viewNow().Alerts)
	}
	s.syncs.set(s099GentooLoc, appStart.Add(-8*s099Day))
	s.sampleFor(s099GentooLoc)
	s.syncs.setErr(s099GentooLoc, boom)
	s.sampleFor(s099GentooLoc)
	if n := s.warnsFor("sync"); n != 2 {
		t.Errorf("sync WARN lines after a second streak = %d, want 2", n)
	}
	if n := len(s.sentTo("sync:gentoo")); n != 1 {
		t.Errorf("sync:gentoo notifications = %d, want 1 throughout", n)
	}
}

func TestS099Sync_R4_3_NoSyncTimeIsSkippedSilently(t *testing.T) {
	s := newS099(t)
	s.syncs.setNone(s099GentooLoc)
	s.startSync(s099Repos[:1], nil)
	s.sampleFor(s099GentooLoc)
	s.sampleFor(s099GentooLoc)
	if n := s.countLines("monitor=sync"); n != 0 {
		t.Errorf("a repository with no sync time logged %d lines at sample time, want 0 (cmd logs the INFO)", n)
	}
	if n := len(s.sentTo("sync:gentoo")); n != 0 {
		t.Errorf("a repository with no sync time raised %d notifications", n)
	}
}

func TestS099Sync_R6_2_DisabledReadsNothing(t *testing.T) {
	s := newS099(t)
	s.cfg = s097Config(t, "monitors:\n  sync:\n    enabled: false\n")
	s.syncs.set(s099GentooLoc, appStart.Add(-30*s099Day))
	s.startSync(s099Repos[:1], nil)
	s.samples(4)
	if n := s.syncs.count(s099GentooLoc); n != 0 {
		t.Errorf("sync time read %d times with the sync monitor disabled", n)
	}
	if n := len(s.sentTo("sync:gentoo")); n != 0 {
		t.Errorf("disabled sync monitor raised %d notifications", n)
	}
}

func TestS099Sync_R5_1_PauseSilencesAndKeepsTheLine(t *testing.T) {
	s := newS099(t)
	s.syncs.set(s099GentooLoc, appStart.Add(-8*s099Day))
	s.startSync(s099Repos[:1], nil)
	s.pause()
	s.sampleFor(s099GentooLoc)
	if n := len(s.sentTo("sync:gentoo")); n != 0 {
		t.Errorf("sync notifications while paused = %d, want 0", n)
	}
	if l := s.viewNow().Alerts; len(l) != 1 {
		t.Errorf("tooltip alerts while paused = %q, want the gentoo line", l)
	}
}
