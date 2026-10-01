package tray_test

// Refine v2, sub-task 12.4: restart-safe scheduling (R2.7, R2.13, R2.14) and
// reconciliation of unread records by source (R10.6). The R6.8 summary
// coverage (every covered notice marked Notified, no resend) is already
// pinned by TestApp_SummaryMarksEveryCoveredNoticeNotified in
// app_contract_test.go and is not repeated here.

import (
	"errors"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/gentoo/pkgdb"
	"github.com/obentoo/bentoolkit/internal/notices"
	"github.com/obentoo/bentoolkit/internal/tray/feed"
	"github.com/obentoo/bentoolkit/internal/tray/state"
)

// Record sources as state.json spells them (design.md internal/tray/state).
const (
	srcFeed     = "feed"
	srcNews     = "news"
	srcMigrated = "" // a record loaded from a format 1 file
)

// sourcedRecord is a stored record of the given source. It is already
// notified, so the checks under test send nothing for it and only the
// reconciliation decides whether it stays.
func sourcedRecord(id, source string, read bool) state.Record {
	return state.Record{
		Title: id, Summary: id + " summary", URL: "https://obentoo.org/notices/" + id + "/",
		Severity: "info", Type: "announcement", Source: source,
		Read: read, Notified: true, LastSeen: appStart.Add(-time.Hour), Updated: appStart.Add(-time.Hour),
	}
}

// unreadNews is an unread portage news item of the bentoo repository.
func unreadNews(id string) notices.Notice {
	return notices.Notice{
		ID: id, Title: id, Summary: id + " summary", URL: "https://obentoo.org/notices/" + id + "/",
		Type: "news", Severity: "info", Source: notices.SourceNews,
		Published: appStart.Add(-time.Hour), Updated: appStart.Add(-time.Hour),
	}
}

// withRecords makes the harness load a saved state holding recs.
func withRecords(h *appHarness, recs map[string]state.Record) {
	h.store.initial = state.State{Format: state.Format, Saved: true, Notices: maps.Clone(recs)}
}

// setNews replaces what the next News.Unread returns.
func setNews(h *appHarness, err error, items ...notices.Notice) {
	h.news.mu.Lock()
	defer h.news.mu.Unlock()
	h.news.items, h.news.err = items, err
}

// requireKept and requireDeleted check one record of a saved state.
func requireKept(t *testing.T, st state.State, why string, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if _, ok := st.Notices[id]; !ok {
			t.Errorf("record %q was deleted; want it kept: %s", id, why)
		}
	}
}

func requireDeleted(t *testing.T, st state.State, why string, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if _, ok := st.Notices[id]; ok {
			t.Errorf("record %q was kept; want it deleted: %s", id, why)
		}
	}
}

// ---------- scheduling across restarts ----------

// TestApp_RestartHonoursTheStoredNextFetch is R2.14: the first fetch waits
// max(StartupDelay, NextFetch - now). Rand 0.5 makes the startup delay 3m.
func TestApp_RestartHonoursTheStoredNextFetch(t *testing.T) {
	// Hostile half first: a stored deadline later than the startup delay
	// must not be cut short by it.
	t.Run("two hours ahead waits two hours", func(t *testing.T) {
		h := newApp(t)
		withRecords(h, nil)
		h.store.initial.NextFetch = appStart.Add(2 * time.Hour)
		h.start()
		h.waitFor("a startup timer", func() bool { return len(h.clock.pending()) > 0 })
		h.clock.Advance(3 * time.Minute)
		h.never("a fetch at the startup delay although the stored next fetch is 2h ahead", h.fetches(1))
		h.clock.Advance(2*time.Hour - 3*time.Minute - time.Second)
		h.never("a fetch before the stored next fetch", h.fetches(1))
		h.clock.Advance(time.Second)
		h.waitFor("the fetch at the stored next fetch", h.fetches(1))
	})
	// The converse: a stored deadline earlier than the startup delay must
	// not make the first fetch come sooner than the delay.
	for name, next := range map[string]time.Time{
		"one minute ahead uses the startup delay": appStart.Add(time.Minute),
		"in the past uses the startup delay":      appStart.Add(-time.Hour),
	} {
		t.Run(name, func(t *testing.T) {
			h := newApp(t)
			withRecords(h, nil)
			h.store.initial.NextFetch = next
			h.start()
			h.waitFor("a startup timer", func() bool { return len(h.clock.pending()) > 0 })
			h.clock.Advance(3*time.Minute - time.Second)
			h.never("a fetch before the startup delay", h.fetches(1))
			h.clock.Advance(time.Second)
			h.waitFor("the fetch at the startup delay", h.fetches(1))
		})
	}
}

// TestApp_EveryScheduleStoresItsDeadline: the interval, the backoff and a
// Retry-After all land in the saved NextFetch (R2.7, R2.13, R2.14). The
// startup fetch runs at appStart+3m.
func TestApp_EveryScheduleStoresItsDeadline(t *testing.T) {
	fetchedAt := appStart.Add(3 * time.Minute)
	for name, tc := range map[string]struct {
		result func(int) (feed.Result, error)
		wait   time.Duration
	}{
		"interval after a 200": {
			result: func(int) (feed.Result, error) { return okResult(1790812800), nil },
			wait:   6 * time.Hour, // Rand 0.5: 6h x 1.0
		},
		"backoff after a failure": {
			result: func(int) (feed.Result, error) { return feed.Result{}, errors.New("network down") },
			wait:   5 * time.Minute,
		},
		"Retry-After of a 503": {
			result: func(int) (feed.Result, error) {
				return feed.Result{}, &feed.StatusError{Status: 503, RetryAfter: 3600 * time.Second}
			},
			wait: time.Hour,
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newApp(t)
			withRecords(h, nil)
			h.feed.next = tc.result
			h.start()
			h.waitFor("the startup timer", func() bool { return h.clock.hasPending(3 * time.Minute) })
			h.clock.Advance(3 * time.Minute)
			h.waitFor("the startup fetch", h.fetches(1))
			want := fetchedAt.Add(tc.wait)
			st := h.savedWhere("NextFetch "+want.Format(time.RFC3339)+" saved", func(st state.State) bool {
				return st.NextFetch.Equal(want)
			})
			if !st.NextFetch.Equal(want) {
				t.Errorf("saved NextFetch = %v, want %v", st.NextFetch, want)
			}
		})
	}
}

// TestApp_RetryAfterSurvivesARestart is R2.13 across a restart: a 503 with
// Retry-After: 3600, then the process restarts 30 s later (systemd
// RestartSec=30 after a bus loss). Hostile: the restart loop, where every
// start refetched after the 1-5 minute startup delay.
func TestApp_RetryAfterSurvivesARestart(t *testing.T) {
	first := newApp(t)
	withRecords(first, nil)
	first.feed.next = func(int) (feed.Result, error) {
		return feed.Result{}, &feed.StatusError{Status: 503, RetryAfter: 3600 * time.Second}
	}
	first.start()
	first.checkNow()
	retryAt := appStart.Add(time.Hour)
	first.savedWhere("the Retry-After deadline saved", func(st state.State) bool { return !st.NextFetch.Before(retryAt) })
	close(first.bus.lost)
	// Polled, not received: the harness cleanup receives Run's result.
	first.waitFor("the first process to stop after the bus loss", func() bool { return len(first.done) > 0 })
	saved, _ := first.store.last()
	saved.Saved = true

	second := newApp(t)
	second.clock = &appClock{now: appStart.Add(30 * time.Second)}
	second.store.initial = saved
	second.start()
	second.waitFor("a startup timer", func() bool { return len(second.clock.pending()) > 0 })
	second.clock.Advance(3 * time.Minute)
	second.never("a fetch at the startup delay before Retry-After expired", second.fetches(1))
	second.clock.Advance(retryAt.Sub(second.clock.Now()) - time.Second)
	second.never("a fetch one second before Retry-After expired", second.fetches(1))
	second.clock.Advance(time.Second)
	second.waitFor("the fetch once Retry-After expired", second.fetches(1))
}

// ---------- reconciliation by source ----------

// TestApp_FeedReconciliationDeletesUnreadFeedRecordsOnly is R10.6 for the
// feed: after a 200 accepted in this process, an unread "feed" record whose
// notice the feed withdrew, or that no longer applies, is deleted. Read
// records, news records and migrated records are not the feed's to delete.
func TestApp_FeedReconciliationDeletesUnreadFeedRecordsOnly(t *testing.T) {
	listed := releaseNotice("2026-10-02-listed", "Still listed")
	upgraded := bentooFoo
	upgraded.Version = "1.3" // fooCVE is listed but no longer applies

	t.Run("news read", func(t *testing.T) {
		h := newApp(t)
		withRecords(h, map[string]state.Record{
			"2026-09-01-withdrawn":      sourcedRecord("2026-09-01-withdrawn", srcFeed, false),
			"2026-09-02-withdrawn-read": sourcedRecord("2026-09-02-withdrawn-read", srcFeed, true),
			fooCVE().ID:                 sourcedRecord(fooCVE().ID, srcFeed, false),
			listed.ID:                   sourcedRecord(listed.ID, srcFeed, false),
			"2026-09-03-news-listed":    sourcedRecord("2026-09-03-news-listed", srcNews, false),
			"2026-09-04-migrated":       sourcedRecord("2026-09-04-migrated", srcMigrated, false),
		})
		h.pkgs.list = []pkgdb.Package{upgraded}
		setNews(h, nil, unreadNews("2026-09-03-news-listed"))
		h.feed.next = func(int) (feed.Result, error) { return okResult(1790812800, listed, fooCVE()), nil }
		h.start()
		h.checkNow()
		st := h.savedWhere("the withdrawn feed record deleted", func(st state.State) bool {
			_, ok := st.Notices["2026-09-01-withdrawn"]
			return !ok
		})
		requireKept(t, st, "a migrated record listed by neither source is left to Prune", "2026-09-04-migrated")
		requireKept(t, st, "read records are never reconciled", "2026-09-02-withdrawn-read")
		requireKept(t, st, "a news record listed in the unread news is not the feed's to delete", "2026-09-03-news-listed")
		requireKept(t, st, "a feed record still listed and applicable stays", listed.ID)
		requireDeleted(t, st, "a feed record that no longer applies goes", fooCVE().ID)
	})

	t.Run("news unreadable", func(t *testing.T) {
		h := newApp(t)
		withRecords(h, map[string]state.Record{
			"2026-09-01-withdrawn":      sourcedRecord("2026-09-01-withdrawn", srcFeed, false),
			"2026-09-02-withdrawn-read": sourcedRecord("2026-09-02-withdrawn-read", srcFeed, true),
			"2026-09-05-news-unknown":   sourcedRecord("2026-09-05-news-unknown", srcNews, false),
		})
		setNews(h, errors.New("open /var/lib/gentoo/news/news-bentoo.unread: permission denied"))
		h.feed.next = func(int) (feed.Result, error) { return okResult(1790812800, listed), nil }
		h.start()
		h.checkNow()
		st := h.savedWhere("the 200 recorded", func(st state.State) bool { return st.Serial == 1790812800 })
		// Hostile half: the feed alone decides about feed records, but must
		// not delete a news record just because the feed does not list it.
		requireKept(t, st, "with the news unread list unknown no news record may be deleted", "2026-09-05-news-unknown")
		requireKept(t, st, "read records are never reconciled", "2026-09-02-withdrawn-read")
		requireDeleted(t, st, "a 200 alone decides about feed records; the news read is irrelevant", "2026-09-01-withdrawn")
	})
}

// TestApp_NotModifiedLeavesFeedRecordsAlone is R10.6 after a restart: a 304
// does not list the feed, so no feed record is deleted; the news read of the
// same check still reconciles the news records.
func TestApp_NotModifiedLeavesFeedRecordsAlone(t *testing.T) {
	recs := map[string]state.Record{
		"2026-09-01-feed-a":       sourcedRecord("2026-09-01-feed-a", srcFeed, false),
		"2026-09-02-feed-b":       sourcedRecord("2026-09-02-feed-b", srcFeed, false),
		"2026-09-03-migrated":     sourcedRecord("2026-09-03-migrated", srcMigrated, false),
		"2026-09-04-news-listed":  sourcedRecord("2026-09-04-news-listed", srcNews, false),
		"2026-09-05-news-dropped": sourcedRecord("2026-09-05-news-dropped", srcNews, false),
	}
	notModified := func(int) (feed.Result, error) {
		return feed.Result{NotModified: true, Status: 304, ETag: `"e1"`}, nil
	}

	t.Run("news unreadable: nothing deleted", func(t *testing.T) {
		h := newApp(t)
		withRecords(h, recs)
		h.store.initial.ETag, h.store.initial.Serial = `"e1"`, 1790812800
		setNews(h, errors.New("open /var/lib/gentoo/news/news-bentoo.unread: permission denied"))
		h.feed.next = notModified
		h.start()
		h.checkNow()
		st := h.savedWhere("the check after the 304 saved", func(st state.State) bool {
			return st.Notices["2026-09-01-feed-a"].LastSeen.Equal(appStart)
		})
		requireKept(t, st, "a 304 deletes nothing", slices.Collect(maps.Keys(recs))...)
	})

	t.Run("news read: only the dropped news record deleted", func(t *testing.T) {
		h := newApp(t)
		withRecords(h, recs)
		h.store.initial.ETag, h.store.initial.Serial = `"e1"`, 1790812800
		setNews(h, nil, unreadNews("2026-09-04-news-listed"))
		h.feed.next = notModified
		h.start()
		h.checkNow()
		st := h.savedWhere("the check after the 304 saved", func(st state.State) bool {
			return st.Notices["2026-09-04-news-listed"].LastSeen.Equal(appStart)
		})
		requireKept(t, st, "a 304 does not list the feed, so no feed record is withdrawn",
			"2026-09-01-feed-a", "2026-09-02-feed-b")
		requireKept(t, st, "a migrated record listed by neither source is left to Prune", "2026-09-03-migrated")
		requireKept(t, st, "a news record still unread stays", "2026-09-04-news-listed")
		requireDeleted(t, st, "a news record absent from a complete unread list goes", "2026-09-05-news-dropped")
	})
}

// TestApp_NewsReconciliationDeletesUnlistedNewsRecords is R10.6 for the
// news: after a complete News.Unread, an unread "news" record absent from
// the list (read with eselect) is deleted; no feed fetch is needed.
func TestApp_NewsReconciliationDeletesUnlistedNewsRecords(t *testing.T) {
	recs := map[string]state.Record{
		"2026-09-01-news-listed":  sourcedRecord("2026-09-01-news-listed", srcNews, false),
		"2026-09-02-news-gone":    sourcedRecord("2026-09-02-news-gone", srcNews, false),
		"2026-09-03-news-read":    sourcedRecord("2026-09-03-news-read", srcNews, true),
		"2026-09-04-feed-only":    sourcedRecord("2026-09-04-feed-only", srcFeed, false),
		"2026-09-05-migrated-old": sourcedRecord("2026-09-05-migrated-old", srcMigrated, false),
	}

	// Hostile half first: a partial read is not the current list.
	t.Run("partial read deletes nothing", func(t *testing.T) {
		h := newApp(t)
		withRecords(h, recs)
		h.start()
		h.waitFor("the startup timer", func() bool { return len(h.clock.pending()) > 0 })
		reads := h.news.readCount()
		setNews(h, errors.New("reading /var/db/repos/bentoo/metadata/news/2026-09-02-news-gone: no such file"),
			unreadNews("2026-09-01-news-listed"))
		h.news.changed <- struct{}{}
		h.waitFor("a re-read of the unread list", func() bool { return h.news.readCount() > reads })
		st := h.savedWhere("the check after the partial read saved", func(st state.State) bool {
			return st.Notices["2026-09-01-news-listed"].LastSeen.Equal(appStart)
		})
		requireKept(t, st, "a partial news read is not the current unread list", slices.Collect(maps.Keys(recs))...)
		if h.feed.count() != 0 {
			t.Errorf("a news change fetched the feed %d times", h.feed.count())
		}
	})

	t.Run("complete read deletes the unlisted news record", func(t *testing.T) {
		h := newApp(t)
		withRecords(h, recs)
		h.start()
		h.waitFor("the startup timer", func() bool { return len(h.clock.pending()) > 0 })
		reads := h.news.readCount()
		setNews(h, nil, unreadNews("2026-09-01-news-listed"))
		h.news.changed <- struct{}{}
		h.waitFor("a re-read of the unread list", func() bool { return h.news.readCount() > reads })
		st := h.savedWhere("the check after the news read saved", func(st state.State) bool {
			return st.Notices["2026-09-01-news-listed"].LastSeen.Equal(appStart)
		})
		requireKept(t, st, "the news list says nothing about feed records", "2026-09-04-feed-only")
		requireKept(t, st, "a migrated record listed by neither source is left to Prune", "2026-09-05-migrated-old")
		requireKept(t, st, "read records are never reconciled", "2026-09-03-news-read")
		requireKept(t, st, "a news record still unread stays", "2026-09-01-news-listed")
		requireDeleted(t, st, "a news record absent from a complete unread list goes", "2026-09-02-news-gone")
		if h.feed.count() != 0 {
			t.Errorf("a news change fetched the feed %d times", h.feed.count())
		}
	})
}

// TestApp_MigratedRecordTakesTheSourceOfTheListCarryingIt is R10.5 meeting
// R10.6: a format 1 record (Source "") takes the source of whichever list
// next carries it, a record both lists carry is "feed" (R4.3), one neither
// carries keeps "" for Prune, and a new record is stamped with its source.
func TestApp_MigratedRecordTakesTheSourceOfTheListCarryingIt(t *testing.T) {
	inFeed := releaseNotice("2026-09-01-in-feed", "In the feed")
	shared := releaseNotice("2026-09-03-shared", "In both")
	newFeed := releaseNotice("2026-10-02-new-feed", "New in the feed")

	h := newApp(t)
	withRecords(h, map[string]state.Record{
		inFeed.ID:            sourcedRecord(inFeed.ID, srcMigrated, false),
		"2026-09-02-in-news": sourcedRecord("2026-09-02-in-news", srcMigrated, false),
		shared.ID:            sourcedRecord(shared.ID, srcMigrated, false),
		"2026-09-04-in-none": sourcedRecord("2026-09-04-in-none", srcMigrated, false),
	})
	setNews(h, nil, unreadNews("2026-09-02-in-news"), unreadNews(shared.ID), unreadNews("2026-10-02-new-news"))
	h.feed.next = func(int) (feed.Result, error) { return okResult(1790812800, inFeed, shared, newFeed), nil }
	h.start()
	h.checkNow()
	st := h.savedWhere("the new records saved", func(st state.State) bool {
		_, f := st.Notices[newFeed.ID]
		_, n := st.Notices["2026-10-02-new-news"]
		return f && n
	})
	// Hostile half first: the record both lists carry must not become
	// "news", and the one neither carries must not be claimed or deleted.
	for id, want := range map[string]string{
		shared.ID:             srcFeed,
		"2026-09-04-in-none":  srcMigrated,
		inFeed.ID:             srcFeed,
		"2026-09-02-in-news":  srcNews,
		newFeed.ID:            srcFeed,
		"2026-10-02-new-news": srcNews,
	} {
		r, ok := st.Notices[id]
		switch {
		case !ok:
			t.Errorf("record %q was deleted; want Source %q", id, want)
		case r.Source != want:
			t.Errorf("record %q has Source %q, want %q", id, r.Source, want)
		}
	}
}
