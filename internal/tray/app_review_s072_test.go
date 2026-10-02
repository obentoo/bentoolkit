package tray_test

// Regression tests from the tech review of sub-task 12.4 (story 072, refine
// v2): a stored deadline that silences the tray across restarts, a first run
// that loses its Retry-After at every restart, a failed Check now that moves
// a deadline earlier, a partial news read that lets the feed reconciliation
// delete a record the news still lists, and a 304 that keeps news records
// alive forever. These tests leave the harness's default state as it is (a
// format 1 file, loaded as established) unless they test the first run.

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/tray/feed"
	"github.com/obentoo/bentoolkit/internal/tray/state"
)

// TestApp_StartupWaitIsCappedButRetryAfterIsNot is R2.14 bounded: a stored
// next fetch further away than max(1.2 x interval, 24h) waits only that cap at
// start, with a WARN naming the stored time and the cap. A Retry-After within
// one process is still honoured in full (R2.13). Rand 0.5: startup delay 3m.
func TestApp_StartupWaitIsCappedButRetryAfterIsNot(t *testing.T) {
	// Hostile half first: a saturated Retry-After (feed.parseRetryAfter
	// returns ~292 years) or a clock that stepped back must not silence the
	// tray for good.
	for name, tc := range map[string]struct {
		interval string
		next     time.Time
		wait     time.Duration
	}{
		"centuries ahead with the default interval waits 24h": {
			next: appStart.AddDate(300, 0, 0), wait: 24 * time.Hour,
		},
		"centuries ahead with a 48h interval waits 1.2 x 48h": {
			interval: "48h", next: appStart.AddDate(300, 0, 0), wait: 57*time.Hour + 36*time.Minute,
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newApp(t)
			h.cfg = config.TrayConfig{Interval: tc.interval}
			h.store.initial.NextFetch = tc.next
			h.start()
			h.waitFor("a startup timer of "+tc.wait.String(), func() bool { return h.clock.hasPending(tc.wait) })
			h.clock.Advance(tc.wait - time.Second)
			h.never("a fetch before the capped startup wait", h.fetches(1))
			h.clock.Advance(time.Second)
			h.waitFor("the fetch at the capped startup wait", h.fetches(1))
			if !warned(h, "next_fetch=", "cap="+tc.wait.String()) {
				t.Errorf("no WARN naming the stored next_fetch and the cap %s:\n%s", tc.wait,
					strings.Join(h.logs.lines(), "\n"))
			}
		})
	}

	t.Run("a deadline under the cap is honoured in full without a WARN", func(t *testing.T) {
		h := newApp(t)
		h.store.initial.NextFetch = appStart.Add(20 * time.Hour)
		h.start()
		h.waitFor("a 20h startup timer", func() bool { return h.clock.hasPending(20 * time.Hour) })
		if warned(h, "next_fetch=") {
			t.Error("a stored next fetch under the cap was reported as capped")
		}
	})

	t.Run("a huge Retry-After within one process is not capped", func(t *testing.T) {
		h := newApp(t)
		retryAfter := 100 * 24 * time.Hour
		h.feed.next = func(int) (feed.Result, error) {
			return feed.Result{}, &feed.StatusError{Status: 503, RetryAfter: retryAfter}
		}
		h.start()
		h.checkNow()
		h.waitFor("a 100-day timer honouring Retry-After", func() bool { return h.clock.hasPending(retryAfter) })
		h.savedWhere("the 100-day deadline saved", func(st state.State) bool {
			return st.NextFetch.Equal(appStart.Add(retryAfter))
		})
	})
}

// TestApp_FirstRunPersistsItsDeadlineAndStaysAFirstRun is R2.13 and R2.14 on a
// fresh install whose server answers 503: the first run's Retry-After is
// saved, so a restart 30 s later does not refetch at its startup delay; and
// the saved state is still a first run (R6.9), so the first feed accepted
// after the restart records a release read without notifying it.
func TestApp_FirstRunPersistsItsDeadlineAndStaysAFirstRun(t *testing.T) {
	first := newApp(t)
	first.store.initial = state.State{} // no state file
	first.feed.next = func(int) (feed.Result, error) {
		return feed.Result{}, &feed.StatusError{Status: 503, RetryAfter: time.Hour}
	}
	first.start()
	first.checkNow()
	retryAt := appStart.Add(time.Hour)
	first.savedWhere("the first run's Retry-After deadline saved", func(st state.State) bool {
		return !st.NextFetch.Before(retryAt)
	})
	close(first.bus.lost)
	first.waitFor("the first process to stop after the bus loss", func() bool { return len(first.done) > 0 })
	saved, n := first.store.last()
	if n == 0 {
		t.Fatal("the first run saved nothing")
	}
	saved.Saved = true // what Store.Load reports for a file it read

	release := releaseNotice("2026-10-02-first-release", "First release")
	second := newApp(t)
	second.clock = &appClock{now: appStart.Add(30 * time.Second)}
	second.store.initial = saved
	second.feed.next = func(int) (feed.Result, error) { return okResult(1790812800, release), nil }
	second.start()
	second.waitFor("a startup timer", func() bool { return len(second.clock.pending()) > 0 })
	second.clock.Advance(3 * time.Minute)
	second.never("a fetch at the startup delay before the first run's Retry-After expired", second.fetches(1))
	second.clock.Advance(retryAt.Sub(second.clock.Now()))
	second.waitFor("the fetch once Retry-After expired", second.fetches(1))
	second.savedWhere("the release recorded read", func(st state.State) bool { return st.Notices[release.ID].Read })
	second.never("a release notified while the saved state is still a first run", func() bool {
		return second.notif.attempts() > 0
	})

	// The accepted feed ended the first run: the next release is notified.
	next := releaseNotice("2026-10-03-second-release", "Second release")
	second.feed.set(func(int) (feed.Result, error) { return okResult(1790812900, release, next), nil })
	second.checkNow()
	second.waitFor("the second release notified", func() bool { return slices.Contains(sentIDs(second), next.ID) })
}

// TestApp_FailedCheckNowKeepsALaterDeadline is R2.13 against Check now: a
// failure must never move a pending deadline (a server's Retry-After)
// earlier, while a 200 from Check now still resets the schedule (R2.11).
func TestApp_FailedCheckNowKeepsALaterDeadline(t *testing.T) {
	retryAt := appStart.Add(2 * time.Hour) // stored by a 503 with Retry-After: 7200
	for name, fail := range map[string]func(int) (feed.Result, error){
		"transport error": func(int) (feed.Result, error) { return feed.Result{}, errors.New("dial tcp: i/o timeout") },
		"503 with a shorter Retry-After": func(int) (feed.Result, error) {
			return feed.Result{}, &feed.StatusError{Status: 503, RetryAfter: time.Minute}
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newApp(t)
			h.store.initial.NextFetch, h.store.initial.Failures = retryAt, 1
			h.feed.next = fail
			h.start()
			h.checkNow()
			st := h.savedWhere("the failure saved", func(st state.State) bool { return st.Failures == 2 })
			if !st.NextFetch.Equal(retryAt) {
				t.Errorf("saved NextFetch = %v after a failed Check now, want the pending %v", st.NextFetch, retryAt)
			}
			h.clock.Advance(retryAt.Sub(appStart) - time.Second)
			h.never("a fetch before the pending deadline", h.fetches(2))
			h.clock.Advance(time.Second)
			h.waitFor("the fetch at the pending deadline", h.fetches(2))
		})
	}

	t.Run("a 200 from Check now still resets the schedule", func(t *testing.T) {
		h := newApp(t)
		h.store.initial.NextFetch, h.store.initial.Failures = retryAt, 1
		h.start()
		h.checkNow()
		want := appStart.Add(6 * time.Hour)
		st := h.savedWhere("the reset saved", func(st state.State) bool { return st.Failures == 0 })
		if !st.NextFetch.Equal(want) {
			t.Errorf("saved NextFetch = %v after a 200 from Check now, want the interval %v", st.NextFetch, want)
		}
	})
}

// TestApp_PartialNewsKeepsAFeedRecordTheNewsLastListed is R10.6 with a partial
// news read: the feed withdrew a notice the news still lists, and the news
// read of that check failed before reaching it. The record must stay, or the
// next complete read re-creates it as new and notifies it a second time.
func TestApp_PartialNewsKeepsAFeedRecordTheNewsLastListed(t *testing.T) {
	shared := releaseNotice("2026-09-10-shared", "Shared")
	other := releaseNotice("2026-10-02-other", "Other")
	h := newApp(t)
	setNews(h, nil, unreadNews(shared.ID))
	h.feed.next = func(int) (feed.Result, error) { return okResult(1790812800, shared), nil }
	h.start()
	h.checkNow()
	h.savedWhere("the shared notice notified", func(st state.State) bool { return st.Notices[shared.ID].Notified })

	setNews(h, errors.New("reading news item "+shared.ID+": parse error"))
	h.feed.set(func(int) (feed.Result, error) { return okResult(1790812900, other), nil }) // the site withdrew it
	h.checkNow()
	st := h.savedWhere("the withdrawal recorded", func(st state.State) bool { return st.Serial == 1790812900 })
	requireKept(t, st, "the last complete news read listed it, and the partial read says nothing", shared.ID)

	before := len(h.notif.messages())
	reads := h.news.readCount()
	setNews(h, nil, unreadNews(shared.ID))
	h.news.changed <- struct{}{}
	h.waitFor("the complete re-read", func() bool { return h.news.readCount() > reads })
	h.never("the shared notice notified a second time", func() bool {
		return slices.Contains(sentIDs(h)[before:], shared.ID)
	})
}

// TestApp_NotModifiedRefreshesOnlyFeedRecords is R10.4 after a restart: a 304
// with no feed in memory confirms the feed's records, not the news'. A news
// record no list carries must keep its LastSeen and age out.
func TestApp_NotModifiedRefreshesOnlyFeedRecords(t *testing.T) {
	old := appStart.Add(-89 * 24 * time.Hour)
	aged := func(id, source string) state.Record {
		r := sourcedRecord(id, source, true)
		r.LastSeen = old
		return r
	}
	h := newApp(t)
	h.store.initial.Notices = map[string]state.Record{
		"2026-07-01-news-gone":   aged("2026-07-01-news-gone", srcNews),
		"2026-07-02-news-listed": aged("2026-07-02-news-listed", srcNews),
		"2026-07-03-feed":        aged("2026-07-03-feed", srcFeed),
		"2026-07-04-migrated":    aged("2026-07-04-migrated", srcMigrated),
	}
	h.store.initial.ETag = `"e1"`
	setNews(h, nil, unreadNews("2026-07-02-news-listed"))
	h.feed.next = func(int) (feed.Result, error) { return feed.Result{NotModified: true, Status: 304, ETag: `"e1"`}, nil }
	h.start()
	h.checkNow()
	st := h.savedWhere("the check after the 304 saved", func(st state.State) bool {
		return st.Notices["2026-07-03-feed"].LastSeen.Equal(appStart)
	})
	if got := st.Notices["2026-07-01-news-gone"].LastSeen; !got.Equal(old) {
		t.Errorf("a news record no list carries got LastSeen %v from a 304, want it kept at %v", got, old)
	}
	for _, id := range []string{"2026-07-02-news-listed", "2026-07-03-feed", "2026-07-04-migrated"} {
		if got := st.Notices[id].LastSeen; !got.Equal(appStart) {
			t.Errorf("record %q LastSeen = %v, want refreshed to %v", id, got, appStart)
		}
	}

	// Two days on, the unlisted news record has been absent for 91 days.
	h.clock.Advance(2 * 24 * time.Hour)
	h.checkNow()
	st = h.savedWhere("the next check saved", func(st state.State) bool {
		return st.Notices["2026-07-03-feed"].LastSeen.After(appStart)
	})
	requireDeleted(t, st, "a news record absent from both sources for over 90 days is forgotten", "2026-07-01-news-gone")
}

// TestApp_FirstRunStoppedBeforeRefreshRefetchesTheFullFeed is R6.9 across a
// stop that lands between an accepted 200 and its refresh: the saved state
// holds the 200's ETag but is still a first run. The next process must not
// revalidate that ETag (a 304 never ends a first run, R2.12), so it fetches
// the full feed, ends the first run with it, and only then sends the stored
// ETag again (R2.3). A later new release is notified.
func TestApp_FirstRunStoppedBeforeRefreshRefetchesTheFullFeed(t *testing.T) {
	old := releaseNotice("2026-09-30-old", "Old")
	first := newApp(t)
	first.store.initial = state.State{} // no state file
	first.feed.next = func(int) (feed.Result, error) {
		first.cancel() // SIGTERM or Quit lands after the body was read and parsed
		return okResult(1790812800, old), nil
	}
	first.start()
	first.checkNow()
	first.waitFor("the first process to stop", func() bool { return len(first.done) > 0 })
	saved, n := first.store.last()
	if n == 0 {
		t.Fatal("the first process saved nothing")
	}
	if saved.Established {
		t.Fatal("a first run stopped before its refresh was saved as established; this test needs the unfinished first run")
	}
	saved.Saved = true // what Store.Load reports for a file it read

	second := newApp(t)
	second.store.initial = saved
	// The server answers 304 to the stored ETag, and the full feed otherwise.
	second.feed.next = func(int) (feed.Result, error) {
		second.feed.mu.Lock()
		etag := second.feed.etags[len(second.feed.etags)-1]
		second.feed.mu.Unlock()
		if etag == saved.ETag {
			return feed.Result{NotModified: true, Status: 304, ETag: saved.ETag}, nil
		}
		return okResult(1790812800, old), nil
	}
	second.start()
	second.checkNow()
	st := second.savedWhere("the first run ended by the full feed", func(st state.State) bool { return st.Established })
	if !st.Notices[old.ID].Read {
		t.Error("the release of the first run's feed was not recorded read")
	}
	second.feed.mu.Lock()
	sent := slices.Clone(second.feed.etags)
	second.feed.mu.Unlock()
	if sent[0] != "" {
		t.Errorf("the first run's fetch sent If-None-Match %q; want none, so the full feed ends the first run", sent[0])
	}

	// Established: the stored ETag is revalidated again, and a new release
	// published after it is notified, not recorded read.
	next := releaseNotice("2026-10-02-new", "New")
	second.feed.set(func(int) (feed.Result, error) { return okResult(1790812900, old, next), nil })
	second.checkNow()
	second.waitFor("the new release notified", func() bool { return slices.Contains(sentIDs(second), next.ID) })
	second.feed.mu.Lock()
	sent = slices.Clone(second.feed.etags)
	second.feed.mu.Unlock()
	if sent[1] != saved.ETag {
		t.Errorf("the fetch after the first run sent If-None-Match %q, want the stored %q", sent[1], saved.ETag)
	}
	if slices.Contains(sentIDs(second), old.ID) {
		t.Error("the release recorded read on the first run was notified later")
	}
}

// TestApp_NoFeedFirstRunWaitsForItsNews is R6.9 with no feed configured: a
// check whose news read failed (a missing news-*.unread) cannot end the first
// run, since nothing was recorded. When the list appears, its items are
// handled as a first run: recorded read, not notified.
func TestApp_NoFeedFirstRunWaitsForItsNews(t *testing.T) {
	h := newApp(t)
	h.noFeed = true
	h.store.initial = state.State{} // no state file
	setNews(h, errors.New("open /var/lib/gentoo/news/news-bentoo.unread: no such file or directory"))
	h.start()
	h.menu(200)
	st := h.savedWhere("the check with the news unreadable saved", func(state.State) bool { return h.news.readCount() >= 1 })
	if st.Established {
		t.Error("a first run with no feed and no news read was saved as established")
	}

	ids := []string{"2026-09-01-news-a", "2026-09-02-news-b"}
	setNews(h, nil, unreadNews(ids[0]), unreadNews(ids[1]))
	h.news.changed <- struct{}{}
	st = h.savedWhere("the news recorded read", func(st state.State) bool {
		return st.Notices[ids[0]].Read && st.Notices[ids[1]].Read
	})
	if !st.Established {
		t.Error("the check that read the news did not end the first run")
	}
	h.never("a news item notified on the first run", func() bool { return h.notif.attempts() > 0 })
}
