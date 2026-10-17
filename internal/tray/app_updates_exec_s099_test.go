package tray_test

// Story 099, sub-task 5.3, Executor-authored: the parts of the @world check
// no pre-authored test pins. The INFO line of a switch between the count and
// "does not resolve", the emerge scan made only while a check waits, a run
// killed by an outside signal, an absolute command path gone since start-up,
// and a repo_revisions stamp that cannot be read.

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/gentoo/news"
	"github.com/obentoo/bentoolkit/internal/gentoo/pending"
	"github.com/obentoo/bentoolkit/internal/tray"
)

func TestS099World_X_SwitchBetweenCountAndUnresolvedLogsTheTransition(t *testing.T) {
	s := newS099(t)
	broken := pending.Result{ExitCode: 1, Duration: time.Second}
	s.run.set(s099EmergePath, s099Seq(s099World(s099Qt), broken, s099World(s099Qt)))
	s.startWorld(nil)
	s.firstCheck(s099EmergePath, "updates")
	s.resync(s099EmergePath, "updates", 2)
	s.resync(s099EmergePath, "updates", 3)
	s.wantTransitions("updates", "",
		[2]string{"ok", "notice"}, [2]string{"notice", "notice"}, [2]string{"notice", "notice"})
	var details []string
	for _, l := range s.logs.lines() {
		if strings.Contains(l, `msg="system condition changed"`) && strings.Contains(l, "monitor=updates ") {
			d := ""
			if _, rest, ok := strings.Cut(l, "detail="); ok {
				d, _, _ = strings.Cut(rest, " ")
			}
			details = append(details, d)
		}
	}
	if want := []string{"", "unresolved", "updates"}; strings.Join(details, ",") != strings.Join(want, ",") {
		t.Errorf("transition details = %q, want %q", details, want)
	}
}

func TestS099World_X_EmergeScannedOnlyWhileACheckWaits(t *testing.T) {
	s := newS099(t)
	var scans atomic.Int32
	s.startWorld(func(d *tray.Deps) {
		d.Sys = nil
		d.EmergeRunning = func() (bool, error) { scans.Add(1); return false, nil }
	})
	s.samples(9) // before the 5-minute start delay: nothing waits
	if n := scans.Load(); n != 0 {
		t.Fatalf("emerge scans before any check was due = %d, want 0", n)
	}
	s.firstCheck(s099EmergePath, "updates")
	if n := scans.Load(); n != 1 {
		t.Fatalf("emerge scans for the start-up check = %d, want 1", n)
	}
	s.samples(5) // idle again: nothing waits
	if n := scans.Load(); n != 1 {
		t.Errorf("emerge scans with no check waiting = %d, want still 1", n)
	}
}

func TestS099World_X_ChecksOffScanNothing(t *testing.T) {
	s := newS099(t)
	s.cfg = s097Config(t, "monitors:\n  updates:\n    enabled: false\n  glsa:\n    enabled: false\n")
	var scans atomic.Int32
	s.startUpd(func(d *tray.Deps) {
		d.Updates = s099WorldCmd()
		d.Sys = nil
		d.EmergeRunning = func() (bool, error) { scans.Add(1); return true, nil }
		// a watched repository keeps the sample timer running
		d.Repos = []news.Repo{{Name: "gentoo", Location: "/s099-no-such-root/var/db/repos/gentoo"}}
	})
	s.samples(12)
	s.bump()
	s.samples(2)
	if n := scans.Load(); n != 0 {
		t.Errorf("emerge scans with updates and glsa disabled = %d, want 0", n)
	}
}

func TestS099World_X_KilledBySignalIsAFailedCheckNotUnresolved(t *testing.T) {
	s := newS099(t)
	killed := pending.Result{ExitCode: -1, Duration: 3 * time.Second, Stderr: []byte("Killed\n")}
	s.run.set(s099EmergePath, s099Seq(s099World(s099Qt), killed, killed, s099World(s099Qt)))
	s.startWorld(nil)
	s.firstCheck(s099EmergePath, "updates")
	info := s.viewNow().Info
	s.next(s099EmergePath, 2)
	s.waitLines("the failed-check WARN", 1, "level=WARN", "monitor=updates")
	s.next(s099EmergePath, 3)
	s.next(s099EmergePath, 4)
	s.checked("updates", 2)
	if n := s.warnsFor("updates"); n != 1 {
		t.Errorf("updates WARN lines over a 2-run kill streak = %d, want 1", n)
	}
	if !s.logLine(func(l string) bool {
		return strings.Contains(l, "level=WARN") && strings.Contains(l, s099EmergePath) && strings.Contains(l, "signal")
	}) {
		t.Error("the WARN does not name the command and the signal")
	}
	for _, m := range s.updatesSent() {
		if strings.Contains(s099Text(m.Msg), "emerge -puDN") {
			t.Fatalf("a run killed by a signal was read as \"does not resolve\": %q", s099Text(m.Msg))
		}
	}
	if n := len(s.updatesSent()); n != 1 {
		t.Errorf("updates notifications = %d, want 1: the same set throughout", n)
	}
	if v := s.viewNow(); strings.Join(v.Info, "|") != strings.Join(info, "|") {
		t.Errorf("view Info = %q, want it kept as %q", v.Info, info)
	}
}

func TestS099World_X_CommandGoneSinceStartupDisables(t *testing.T) {
	s := newS099(t)
	s.run.set(s099EmergePath, s099Fixed(pending.Result{}, s099StartErr(s099EmergePath, fs.ErrNotExist)))
	s.startWorld(nil)
	s.untilRun(s099EmergePath, 1, 14)
	s.waitLines("the disable WARN", 1, "level=WARN", "monitor=updates")
	for range 3 {
		s.bump()
		s.samples(2)
	}
	if n := s.run.count(s099EmergePath); n != 1 {
		t.Errorf("emerge run %d times after its path vanished, want 1", n)
	}
	if n := s.warnsFor("updates", "disabled"); n != 1 {
		t.Errorf("disable WARN lines = %d, want 1", n)
	}
}

func TestS099World_X_UnreadableRepoRevisionsWarnsOncePerStreak(t *testing.T) {
	s := newS099(t)
	file := filepath.Join(t.TempDir(), "plain")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(file, "repo_revisions") // stat fails with ENOTDIR, not "not exist"
	s.startWorld(func(d *tray.Deps) { d.RepoRevisions = bad })
	s.firstCheck(s099EmergePath, "updates") // the start-up check still runs
	s.samples(3)
	if n := s.warnsFor("updates", "path="+bad); n != 1 {
		t.Errorf("repo_revisions WARN lines over one failure streak = %d, want 1", n)
	}
	if n := s.run.count(s099EmergePath); n != 1 {
		t.Errorf("emerge ran %d times; a failing stat is no sync", n)
	}
}

func TestS099World_X_MissingRepoRevisionsIsSilent(t *testing.T) {
	s := newS099(t)
	s.startWorld(func(d *tray.Deps) { d.RepoRevisions = filepath.Join(t.TempDir(), "absent") })
	s.firstCheck(s099EmergePath, "updates")
	s.samples(3)
	if n := s.warnsFor("updates"); n != 0 {
		t.Errorf("updates WARN lines with no repo_revisions = %d, want 0", n)
	}
}
