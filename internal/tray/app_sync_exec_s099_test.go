package tray_test

// Executor tests for the sync paths the authored tests leave open: sync ages
// observed beside a scheduled update check, the keys of every sync log line,
// a repository whose sync time vanished while stale, and a sync time ahead
// of the clock.

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/tray"
)

func TestS099Sync_X_ObservedBesideTheUpdateCheck(t *testing.T) {
	s := newS099(t)
	s.syncs.set(s099GentooLoc, appStart.Add(-9*s099Day))
	s.startSync(s099Repos[:1], func(d *tray.Deps) {
		d.Updates = s099WorldCmd()
		d.GLSA = s099GLSACmd()
	})
	s.sampleFor(s099GentooLoc) // long before the start-up check is due
	if n := len(s.sentTo("sync:gentoo")); n != 1 {
		t.Fatalf("sync:gentoo notifications with the update checks on = %d, want 1", n)
	}
	before := s.syncs.count(s099GentooLoc)
	s.samples(3)
	if n := s.syncs.count(s099GentooLoc) - before; n != 3 {
		t.Errorf("sync time reads over 3 samples = %d, want 3: one per sample", n)
	}
	if n := s.run.total(); n != 0 {
		t.Errorf("commands run before the start-up delay = %d, want 0", n)
	}
}

func TestS099Sync_X_LinesCarryTargetStateAndOneLevelKey(t *testing.T) {
	s := newS099(t)
	s.syncs.set(s099GentooLoc, appStart.Add(-8*s099Day))
	s.startSync(s099Repos[:1], nil)
	s.sampleFor(s099GentooLoc)
	s.syncs.setErr(s099GentooLoc, errors.New("permission denied"))
	s.sampleFor(s099GentooLoc)
	s.syncs.set(s099GentooLoc, s.clock.Now())
	s.sampleFor(s099GentooLoc)
	if n := s.warnsFor("sync", "target=gentoo", "state=warning", "repository=gentoo", "path="+s099GentooLoc); n != 1 {
		t.Errorf("sync WARN lines with target, state, repository and path = %d, want 1", n)
	}
	n := 0
	for _, l := range s.logs.lines() {
		if !strings.Contains(l, "monitor=sync") || strings.Contains(l, "level=DEBUG") {
			continue
		}
		n++
		if !strings.Contains(l, " target=") || !strings.Contains(l, " state=") {
			t.Errorf("sync line lacks target or state: %s", l)
		}
		if k := len(s099LevelKey.FindAllString(l, -1)); k != 1 {
			t.Errorf("sync line carries %d level keys, want 1: %s", k, l)
		}
	}
	if n != 3 { // ok->warning, the WARN, warning->ok
		t.Errorf("sync lines above DEBUG = %d, want 3", n)
	}
}

func TestS099Sync_X_NoSyncTimeKeepsAStandingCondition(t *testing.T) {
	s := newS099(t)
	s.syncs.set(s099GentooLoc, appStart.Add(-8*s099Day))
	s.startSync(s099Repos[:1], nil)
	s.sampleFor(s099GentooLoc)
	s.syncs.setNone(s099GentooLoc)
	s.sampleFor(s099GentooLoc)
	s.sampleFor(s099GentooLoc)
	if n := s.closedFor("sync:gentoo"); n != 0 {
		t.Errorf("a vanished sync time closed the stale notification %d times; it is skipped", n)
	}
	if l := s.viewNow().Alerts; len(l) != 1 {
		t.Errorf("tooltip alerts = %q, want the gentoo line kept", l)
	}
	if n := s.warnsFor("sync"); n != 0 {
		t.Errorf("sync WARN lines = %d, want 0", n)
	}
}

func TestS099Sync_X_SyncTimeAheadOfTheClockIsFresh(t *testing.T) {
	s := newS099(t)
	s.syncs.set(s099GentooLoc, appStart.Add(2*s099Day))
	s.startSync(s099Repos[:1], nil)
	s.sampleFor(s099GentooLoc)
	if n := len(s.sentTo("sync:gentoo")); n != 0 {
		t.Errorf("a sync time ahead of the clock raised %d notifications", n)
	}
	if l := s.viewNow().Alerts; len(l) != 0 {
		t.Errorf("tooltip alerts = %q, want none", l)
	}
}

func TestS099Sync_X_TooltipLineNamesTheRepositoryAndDays(t *testing.T) {
	s := newS099(t)
	s.syncs.set(s099ZhLoc, appStart.Add(-12*s099Day-time.Hour))
	s.startSync(s099Repos[1:], nil)
	s.sampleFor(s099ZhLoc)
	l := s.viewNow().Alerts
	if len(l) != 1 || !strings.Contains(l[0], "gentoo-zh") || !strings.Contains(l[0], "12 day") {
		t.Errorf("tooltip alerts = %q, want one line naming gentoo-zh and 12 days", l)
	}
}

func TestS099Sync_X_FailedSendLineCarriesTheStateKey(t *testing.T) {
	s := newS099(t)
	s.syncs.set(s099GentooLoc, appStart.Add(-8*s099Day))
	s.out.setSendErr(errors.New("notification server gone"))
	s.startSync(s099Repos[:1], nil)
	s.sampleFor(s099GentooLoc)
	if n := s.out.failedSends(); n != 1 {
		t.Fatalf("failed sends = %d, want 1 (the raise of gentoo)", n)
	}
	var lines []string
	for _, l := range s.logs.lines() {
		if strings.Contains(l, `msg="sending a system notification failed"`) {
			lines = append(lines, l)
		}
	}
	if len(lines) != 1 {
		t.Fatalf("failed-send lines = %d, want 1:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	l := lines[0]
	for _, part := range []string{"level=WARN", " monitor=sync ", " target=gentoo ", " state=warning "} {
		if !strings.Contains(l, part) {
			t.Errorf("failed-send line lacks %q: %s", part, l)
		}
	}
	if k := len(s099LevelKey.FindAllString(l, -1)); k != 1 {
		t.Errorf("failed-send line carries %d level keys, want 1: %s", k, l)
	}
}

// TestS099Sync_X_QuitDuringASyncTimeReadDropsIt: a Quit chosen while a
// repository's sync time is being read drops what the read returned, so a
// stale time read then neither raises a condition nor sends a notification.
func TestS099Sync_X_QuitDuringASyncTimeReadDropsIt(t *testing.T) {
	s := newS099(t)
	s.syncs.set(s099GentooLoc, appStart.Add(-9*s099Day))
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	s.startSync(s099Repos, func(d *tray.Deps) {
		d.SyncTime = func(location string) (time.Time, bool, error) {
			select {
			case started <- struct{}{}:
			default:
			}
			<-release
			return s.syncs.SyncTime(location)
		}
	})
	s.waitFor("the sample timer", func() bool { return s.clock.hasPending(s097Sample) })
	s.clock.Advance(s097Sample)
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("the sync time read did not start")
	}
	s.menu(300)
	// The menu helper takes the click off the buffered channel and cancels
	// the sample at once; the grace covers the instructions between the two.
	s.waitFor("the Quit click taken", func() bool { return len(s.icon.events) == 0 })
	time.Sleep(100 * time.Millisecond)
	close(release)
	select {
	case err := <-s.done:
		s.done <- err
		if err != nil {
			t.Fatalf("Run = %v, want nil", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not stop after Quit during a sync time read")
	}
	if n := len(s.out.system()) + s.out.failedSends(); n != 0 {
		t.Errorf("notifications sent or tried after a Quit during the read = %d, want 0", n)
	}
	if n := s.countLines(`msg="system condition changed"`, "monitor=sync"); n != 0 {
		t.Errorf("sync transitions after a Quit during the read = %d, want 0", n)
	}
	if n := s.syncs.count(s099ZhLoc); n != 0 {
		t.Errorf("gentoo-zh read %d times after the Quit, want 0", n)
	}
}
