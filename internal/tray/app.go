package tray

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"math/rand/v2" // nosemgrep: go.lang.security.audit.crypto.math_random.math-random-used -- default scheduling jitter, not a secret
	"net/url"
	"strings"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/desktop/netmon"
	"github.com/obentoo/bentoolkit/internal/desktop/notify"
	"github.com/obentoo/bentoolkit/internal/desktop/portal"
	"github.com/obentoo/bentoolkit/internal/desktop/sni"
	"github.com/obentoo/bentoolkit/internal/gentoo/pkgdb"
	"github.com/obentoo/bentoolkit/internal/notices"
	"github.com/obentoo/bentoolkit/internal/tray/feed"
	"github.com/obentoo/bentoolkit/internal/tray/schedule"
	"github.com/obentoo/bentoolkit/internal/tray/state"
)

var (
	// ErrAlreadyRunning is returned by Run when another process owns the
	// session bus name (R1.2).
	ErrAlreadyRunning = errors.New("another bentoo-tray instance owns the session bus name")
	// ErrBusLost is returned by Run when the session bus connection is lost
	// while running (R1.5).
	ErrBusLost = errors.New("the session bus connection was lost")
)

// Deadlines of the calls the loop makes. The adapters wait only as long as
// the context they are given, and a peer that never answers would otherwise
// stall the loop.
var (
	// callTimeout bounds one D-Bus call made from the loop: Acquire,
	// Allowed and Send.
	callTimeout = 5 * time.Second
	// fetchTimeout bounds one feed fetch; the fetcher's own client already
	// stops at feed.Timeout.
	fetchTimeout = feed.Timeout + 5*time.Second
	// localTimeout bounds a read of the package database or the news list.
	localTimeout = 30 * time.Second
	// openTimeout bounds one URL opening, which runs off the loop: the portal
	// may wait for an app chooser, and the xdg-open fallback for a browser.
	openTimeout = 2 * time.Minute
	// openGrace is how long a stop waits for openings in flight before it
	// cancels them, so a normal stop does not kill a browser being started.
	// openGrace, cancelledOpenWait and releaseTimeout together stay well
	// under R1.4's 5 s, leaving the save and the process exit their share.
	openGrace = time.Second
	// cancelledOpenWait is how long a stop waits for cancelled openings to
	// return before it gives up on them.
	cancelledOpenWait = 250 * time.Millisecond
	// releaseTimeout bounds the release of the bus name at a stop; Release
	// takes no context.
	releaseTimeout = time.Second
)

// Menu item ids, as design.md fixes them and package sni serves them.
// Notice entries have ids package sni gives them, and their clicks carry the
// notice in sni.Event.NoticeID.
const (
	menuMore          int32 = 100
	menuCheckNow      int32 = 200
	menuMarkAllRead   int32 = 201
	menuPauseHour     int32 = 202
	menuPauseTomorrow int32 = 203
	menuResume        int32 = 204
	menuQuit          int32 = 300
)

// pauseMorningHour is the local hour "Pause until tomorrow" ends at (R9.3).
const pauseMorningHour = 8

// FeedFetcher fetches the notices feed; *feed.Fetcher implements it.
type FeedFetcher interface {
	Fetch(ctx context.Context, etag string) (feed.Result, error)
}

// NewsReader reads the bentoo repository's unread news; *news.Reader
// implements it. Items returned together with an error are a partial result.
// Changed fires when the unread list changed.
type NewsReader interface {
	Unread(ctx context.Context) ([]notices.Notice, error)
	Changed() <-chan struct{}
}

// InstalledReader lists the installed packages; pkgdb.Reader implements it.
type InstalledReader interface {
	Installed(ctx context.Context) (pkgs []pkgdb.Package, skipped int, err error)
}

// Notifier sends desktop notifications and reports the actions taken on
// them; *notify.Notifier implements it.
type Notifier interface {
	Send(ctx context.Context, m notify.Message) (uint32, error)
	Events() <-chan notify.Event
}

// TrayIcon is the StatusNotifierItem; *sni.Item implements it. The context
// given to Start bounds the item's lifetime, not only the call.
type TrayIcon interface {
	Start(ctx context.Context) error
	SetState(v sni.View)
	Events() <-chan sni.Event
}

// Opener opens a notice URL; *portal.Opener implements it.
type Opener interface {
	Open(ctx context.Context, url, activationToken string) error
}

// NetworkMonitor reports NetworkManager's view of the network;
// *netmon.Monitor implements it.
type NetworkMonitor interface {
	Allowed(ctx context.Context) (netmon.Verdict, error)
	Changed() <-chan struct{}
}

// StateStore loads and saves the state file; *state.Store implements it.
type StateStore interface {
	Load() (state.State, error)
	Save(st state.State) error
}

// BusOwner owns the session bus name; *dbusx.Owner implements it.
type BusOwner interface {
	Acquire(ctx context.Context) (acquired bool, err error)
	Release() error
	Lost() <-chan struct{}
}

// Deps is everything the App talks to. Feed is nil when the feed URL was
// refused (R2.9): the App then runs on the offline news alone. A nil Log
// discards, a nil Clock is the SystemClock, a nil Rand is math/rand/v2's
// Float64 and a nil Net behaves as netmon.Absent(); every other field is
// required.
type Deps struct {
	Log    *slog.Logger
	Clock  Clock
	Rand   func() float64
	Config config.TrayConfig
	Feed   FeedFetcher
	News   NewsReader
	Pkgs   InstalledReader
	Notify Notifier
	Icon   TrayIcon
	Open   Opener
	Net    NetworkMonitor
	Store  StateStore
	Bus    BusOwner
}

// checkKind is what started a check.
type checkKind int

const (
	// checkScheduled is the fetch timer: the network is consulted, and a
	// skipped fetch is rescheduled one interval later.
	checkScheduled checkKind = iota
	// checkNetwork is a network change after a skipped fetch: the network is
	// consulted, and a fetch skipped again keeps the current timer.
	checkNetwork
	// checkManual is Check now: it fetches regardless of the network (R3.5).
	checkManual
	// checkLocal is a change of a local source: nothing is fetched.
	checkLocal
)

// openResult is the outcome of one opening, sent back to the loop.
type openResult struct {
	seq      uint64
	noticeID string // empty for the notices index
	url      string
	err      error
}

// App is bentoo-tray's event loop. All of its fields belong to the goroutine
// running Run. The only other goroutines it starts are the URL openings,
// which report back over a channel, and the bounded release of the bus name
// at a stop; neither touches a field.
type App struct {
	d        Deps
	log      *slog.Logger
	interval time.Duration
	indexURL string

	st state.State
	// feedItems is the last feed accepted in this process; haveFeed tells an
	// empty accepted feed from none.
	feedItems []notices.Notice
	haveFeed  bool
	// lastNews holds the IDs of the last complete News.Unread in this
	// process, nil before one. While a later read is partial or failed, the
	// feed reconciliation keeps the records it lists (R10.6).
	lastNews map[string]bool

	view     sni.View
	iconUp   bool
	skipped  bool // the last scheduled fetch was skipped for the network
	nmAbsent bool // the R3.4 INFO was logged

	fetchAt <-chan time.Time
	pauseAt <-chan time.Time
	// iconEvents is the menu's click channel, nil once closed. The loop
	// reads it, and guard's helper while a blocking branch runs.
	iconEvents <-chan sni.Event

	// openings holds the cancel of each opening in flight, by sequence.
	openings map[uint64]context.CancelFunc
	openSeq  uint64
	opened   chan openResult
	done     chan struct{}
}

// New returns an App over d.
func New(d Deps) *App {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.Clock == nil {
		d.Clock = SystemClock{}
	}
	if d.Rand == nil {
		d.Rand = rand.Float64
	}
	if d.Net == nil {
		d.Net = netmon.Absent()
	}
	interval, _ := d.Config.GetInterval() // its warnings are run's to log
	return &App{
		d:        d,
		log:      d.Log,
		interval: interval,
		indexURL: noticesIndex(d.Config.GetFeedURL()),
		openings: map[uint64]context.CancelFunc{},
		opened:   make(chan openResult),
		done:     make(chan struct{}),
	}
}

// noticesIndex is https://<feed host>/notices/, or empty when the feed URL
// has no host.
func noticesIndex(feedURL string) string {
	u, err := url.Parse(feedURL)
	if err != nil || u.Host == "" {
		return ""
	}
	return (&url.URL{Scheme: "https", Host: u.Host, Path: "/notices/"}).String()
}

// missingDeps names the required dependencies that are nil.
func (a *App) missingDeps() []string {
	var missing []string
	for _, dep := range []struct {
		name   string
		absent bool
	}{
		{"Deps.News", a.d.News == nil}, {"Deps.Pkgs", a.d.Pkgs == nil}, {"Deps.Notify", a.d.Notify == nil},
		{"Deps.Icon", a.d.Icon == nil}, {"Deps.Open", a.d.Open == nil}, {"Deps.Store", a.d.Store == nil},
		{"Deps.Bus", a.d.Bus == nil},
	} {
		if dep.absent {
			missing = append(missing, dep.name)
		}
	}
	return missing
}

// Run owns the bus name, loads the state, starts the icon and runs the loop
// until ctx is done, the user quits or the bus is lost. A stop saves the
// state and releases the name, returning nil (R1.4); a lost bus saves and
// returns ErrBusLost (R1.5); another owner of the name is ErrAlreadyRunning
// (R1.2). Any other error is a startup failure.
func (a *App) Run(ctx context.Context) error {
	if missing := a.missingDeps(); len(missing) > 0 {
		return fmt.Errorf("starting bentoo-tray: missing dependencies %s", strings.Join(missing, ", "))
	}
	// Everything started from here, the tray item's watcher tracking
	// included, ends when Run returns, also on Quit.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	acquireCtx, cancelAcquire := context.WithTimeout(ctx, callTimeout)
	acquired, err := a.d.Bus.Acquire(acquireCtx)
	cancelAcquire()
	if err != nil {
		return fmt.Errorf("starting bentoo-tray: %w", err)
	}
	if !acquired {
		return ErrAlreadyRunning
	}

	st, err := a.d.Store.Load()
	switch {
	case errors.Is(err, state.ErrCorrupt):
		a.log.Warn("the state file was damaged and moved aside; starting as a first run", "error", err)
	case err != nil:
		a.release()
		return fmt.Errorf("starting bentoo-tray: %w", err)
	}
	if st.Notices == nil {
		st.Notices = map[string]state.Record{}
	}
	a.st = st

	// The icon's context is its lifetime: sni keeps tracking the watcher
	// with it, so it is not bounded by a call deadline (Start bounds its own
	// calls).
	if err := a.d.Icon.Start(ctx); err != nil {
		a.log.Warn("the tray icon could not start; running with notifications only", "error", err)
	} else {
		a.iconUp = true
	}

	defer close(a.done)
	// A wall clock without a Done would leave every abandoned timer's
	// goroutine running until its deadline; these end with Run.
	if c, ok := a.d.Clock.(SystemClock); ok && c.Done == nil {
		c.Done = a.done
		a.d.Clock = c
	}

	now := a.d.Clock.Now()
	a.expirePause(now)
	if !a.st.PauseUntil.IsZero() {
		a.pauseAt = a.d.Clock.After(a.st.PauseUntil.Sub(now))
	}
	a.render()

	delay := max(schedule.StartupDelay(a.d.Rand()), a.storedWait(now))
	a.fetchAt = a.d.Clock.After(delay)
	a.log.Info("bentoo-tray started", "first_check_in", delay, "interval", a.interval,
		"feed", a.d.Feed != nil, "first_run", !a.st.Established)
	return a.loop(ctx)
}

// storedWait is how long the stored next fetch asks a new process to wait. A
// stored next fetch later than the startup delay wins, so a restart (a
// bus-loss loop under systemd included) cannot refetch before the interval,
// the backoff or the server's Retry-After (R2.14). It is capped at
// startupWaitCap, and the stored deadline with it: a saturated Retry-After
// (~292 years) or a wall clock that later stepped back would otherwise
// silence the tray across every restart. Within one process a Retry-After is
// honoured in full (R2.13); only a new process caps it.
func (a *App) storedWait(now time.Time) time.Duration {
	wait := a.st.NextFetch.Sub(now)
	limit := a.startupWaitCap()
	if wait <= limit {
		return wait
	}
	a.log.Warn("the stored next fetch is too far ahead; capping the wait before the first fetch",
		"next_fetch", a.st.NextFetch, "cap", limit, "now", now)
	a.st.NextFetch = now.Add(limit)
	return limit
}

// startupWaitCap is max(1.2 x the configured interval, 24h), saturating
// instead of overflowing for an absurd interval.
func (a *App) startupWaitCap() time.Duration {
	const floor = 24 * time.Hour
	extra := a.interval / 5
	if a.interval > math.MaxInt64-extra {
		return math.MaxInt64
	}
	return max(a.interval+extra, floor)
}

// loop is the single select every event goes through.
func (a *App) loop(ctx context.Context) error {
	notifyEvents := a.d.Notify.Events()
	a.iconEvents = a.d.Icon.Events()
	newsChanged := a.d.News.Changed()
	netChanged := a.d.Net.Changed()
	lost := a.d.Bus.Lost()
	for {
		if ctx.Err() != nil {
			return a.stop()
		}
		select {
		case <-ctx.Done():
			return a.stop()
		case <-lost:
			// Closing the connection at a stop also reports Lost: a stop
			// already under way wins.
			if ctx.Err() != nil {
				return a.stop()
			}
			return a.busLost()
		case <-a.fetchAt:
			if a.guard(ctx, func(ctx context.Context) { a.check(ctx, checkScheduled) }) {
				return a.stop()
			}
		case <-a.pauseAt:
			if a.guard(ctx, a.pauseTimer) {
				return a.stop()
			}
		case ev, ok := <-notifyEvents:
			if !ok {
				notifyEvents = nil
				continue
			}
			a.onNotification(ctx, ev)
		case ev, ok := <-a.iconEvents:
			if !ok {
				a.iconEvents = nil
				continue
			}
			if a.onMenu(ctx, ev) {
				return a.stop()
			}
		case _, ok := <-newsChanged:
			if !ok {
				newsChanged = nil
				continue
			}
			if a.guard(ctx, func(ctx context.Context) { a.check(ctx, checkLocal) }) {
				return a.stop()
			}
		case _, ok := <-netChanged:
			if !ok {
				netChanged = nil
				continue
			}
			if a.guard(ctx, a.onNetworkChanged) {
				return a.stop()
			}
		case r := <-a.opened:
			a.onOpened(r)
		}
	}
}

// stop ends a normal run: openings in flight get their grace, then the
// state is saved and the name released (R1.4).
func (a *App) stop() error {
	a.drainOpenings()
	a.save()
	a.release()
	a.log.Info("bentoo-tray stopped")
	return nil
}

// busLost ends a run whose connection is gone: the state is saved, and the
// name, gone with the connection, is not released (R1.5).
func (a *App) busLost() error {
	a.drainOpenings()
	a.save()
	return ErrBusLost
}

// release gives the bus name back, bounded by releaseTimeout.
func (a *App) release() {
	result := make(chan error, 1)
	go func() { result <- a.d.Bus.Release() }()
	timer := time.NewTimer(releaseTimeout)
	defer timer.Stop()
	select {
	case err := <-result:
		if err != nil {
			a.log.Warn("releasing the session bus name failed", "error", err)
		}
	case <-timer.C:
		a.log.Warn("releasing the session bus name timed out", "timeout", releaseTimeout)
	}
}

// ---------- checks ----------

// check runs one check: a fetch unless kind is checkLocal, then a refresh
// of the notices from both sources.
func (a *App) check(ctx context.Context, kind checkKind) {
	a.expirePause(a.d.Clock.Now())
	notModified := false
	if kind != checkLocal {
		notModified = a.fetch(ctx, kind)
	}
	if ctx.Err() != nil {
		return
	}
	a.refresh(ctx, notModified)
}

// fetch fetches the feed when the network allows it and schedules the next
// fetch. It reports whether the server answered 304.
func (a *App) fetch(ctx context.Context, kind checkKind) (notModified bool) {
	if a.d.Feed == nil {
		if kind == checkManual {
			a.log.Info("check now: no feed is configured; reading the offline news only")
		} else {
			a.fetchAt = a.d.Clock.After(a.nextInterval())
		}
		return false
	}
	if kind == checkScheduled || kind == checkNetwork {
		if !a.networkAllows(ctx) {
			a.skipped = true
			if kind == checkScheduled {
				a.fetchAt = a.d.Clock.After(a.nextInterval())
			}
			return false
		}
	}
	a.skipped = false

	// A first run revalidates nothing: only an accepted 200 ends it (R6.9),
	// and a 304 never does (R2.12). A stop between a first run's 200 and its
	// refresh saves that 200's ETag with the first run unfinished; sending it
	// would get 304s forever and keep every later check a first run.
	etag := a.st.ETag
	if !a.st.Established {
		etag = ""
	}
	start := a.d.Clock.Now()
	fetchCtx, cancel := context.WithTimeout(ctx, fetchTimeout)
	res, err := a.d.Feed.Fetch(fetchCtx, etag)
	cancel()
	elapsed := a.d.Clock.Now().Sub(start)
	if err != nil {
		if ctx.Err() != nil {
			return false // stopping: not a failure of the feed
		}
		a.fetchFailed(res, err, elapsed)
		return false
	}

	if res.NotModified {
		a.log.Info("feed fetched", "status", res.Status, "bytes", res.Bytes, "duration", elapsed,
			"outcome", "not-modified")
		if res.ETag != "" {
			a.st.ETag = res.ETag
		}
		a.fetchSucceeded()
		return true
	}

	if ok, reason := acceptFeed(a.st, res.Feed, a.d.Clock.Now()); !ok {
		a.st.Failures++
		wait := a.retryWait(schedule.Backoff(a.st.Failures, 0))
		a.log.Warn("feed rejected; keeping the previous notices", "status", res.Status, "bytes", res.Bytes,
			"duration", elapsed, "outcome", "rejected", "reason", reason, "serial", res.Feed.Serial,
			"last_serial", a.st.Serial, "expires", res.Feed.Expires, "failures", a.st.Failures, "retry_in", wait)
		a.scheduleFetch(wait)
		return false
	}

	a.log.Info("feed fetched", "status", res.Status, "bytes", res.Bytes, "duration", elapsed,
		"outcome", "accepted", "serial", res.Feed.Serial, "items", len(res.Feed.Items))
	a.st.Serial, a.st.ETag = res.Feed.Serial, res.ETag
	a.feedItems, a.haveFeed = res.Feed.Items, true
	a.fetchSucceeded()
	return false
}

// networkAllows asks NetworkManager whether to fetch now (R3.1-R3.4). A
// failed question fetches anyway.
func (a *App) networkAllows(ctx context.Context) bool {
	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	v, err := a.d.Net.Allowed(callCtx)
	cancel()
	switch {
	case err != nil:
		a.log.Warn("checking the network failed; fetching anyway", "error", err)
		return true
	case v.NMAbsent:
		// Online and Metered mean nothing without NetworkManager.
		if !a.nmAbsent {
			a.nmAbsent = true
			a.log.Info("NetworkManager is not on the system bus; fetching on schedule without network checks")
		}
		return true
	case !v.Online:
		a.log.Info("skipping the scheduled feed fetch: the network is offline; fetching when it comes back")
		return false
	case v.Metered && a.d.Config.GetSkipMetered():
		a.log.Info("skipping the scheduled feed fetch: the connection is metered; fetching when that changes")
		return false
	}
	return true
}

// fetchFailed logs a failed fetch and schedules the backoff (R2.6, R2.7,
// R2.13).
func (a *App) fetchFailed(res feed.Result, err error, elapsed time.Duration) {
	status, bytes := res.Status, res.Bytes
	var retryAfter time.Duration
	var se *feed.StatusError
	if errors.As(err, &se) {
		status, bytes, retryAfter = se.Status, max(bytes, se.Bytes), se.RetryAfter
	}
	a.st.Failures++
	wait := a.retryWait(schedule.Backoff(a.st.Failures, retryAfter))
	attrs := []any{"status", status, "bytes", bytes, "duration", elapsed, "outcome", "error",
		"failures", a.st.Failures, "retry_in", wait}
	var ie *notices.ItemError
	if errors.As(err, &ie) {
		attrs = append(attrs, "item", ie.ID, "field", ie.Field)
	}
	attrs = append(attrs, "error", err)
	a.log.Warn("feed fetch failed; keeping the previous notices", attrs...)
	a.scheduleFetch(wait)
}

// retryWait is the wait after a failed or rejected fetch: the backoff, or the
// pending deadline when that is later. A failure never moves the next fetch
// earlier, so a Check now that fails cannot cut short a server's Retry-After
// (R2.13); a Check now that succeeds still resets the schedule (R2.11).
func (a *App) retryWait(backoff time.Duration) time.Duration {
	return max(backoff, a.st.NextFetch.Sub(a.d.Clock.Now()))
}

// fetchSucceeded resets the backoff and schedules the next regular fetch
// (R2.2, R2.11).
func (a *App) fetchSucceeded() {
	a.st.Failures = 0
	a.scheduleFetch(a.nextInterval())
}

// scheduleFetch arms the fetch timer for wait after a fetch and stores the
// deadline in the state, so a restart waits for it too (R2.14). Only the
// schedules that follow a fetch go through here: a fetch skipped for the
// network, or a check with no feed configured, imposes no wait on the next
// process, which still fetches after its startup delay.
func (a *App) scheduleFetch(wait time.Duration) {
	a.st.NextFetch = a.d.Clock.Now().Add(wait)
	a.fetchAt = a.d.Clock.After(wait)
}

func (a *App) nextInterval() time.Duration {
	return schedule.NextInterval(a.interval, a.d.Rand())
}

// refresh merges the feed in memory with the unread news, keeps what
// applies, sends what is pending and saves.
func (a *App) refresh(ctx context.Context, notModified bool) {
	now := a.d.Clock.Now()
	news, newsOK := a.readNews(ctx)
	pkgs, pkgsOK := a.readPackages(ctx)

	merged := notices.Merge(a.feedItems, news)
	var applicable []notices.Notice
	for _, n := range merged {
		if notices.Applies(n, pkgs) {
			applicable = append(applicable, n)
		}
	}
	a.reconcile(news, applicable, newsOK, pkgsOK)

	msgs := decideNotifications(&a.st, applicable, !a.st.Established, a.paused(now), a.d.Config)
	a.claimSources(merged)
	a.markSeen(now, merged, notModified)
	a.st.Prune(now)
	// The first run ends with the check that accepted a feed (R6.9), or with
	// the first news read when no feed is configured: the news is all there
	// is. A news read that failed outright (a missing news-*.unread) recorded
	// nothing, so it cannot end it; a partial one recorded what it read. The
	// check that ends it is still a first run.
	if a.haveFeed || (a.d.Feed == nil && (newsOK || len(news) > 0)) {
		a.st.Established = true
	}
	a.send(ctx, msgs)
	a.render()
	a.save()
}

// readNews reads the unread news. A partial result is kept; a failed read
// leaves the feed alone (R4.4). ok reports a complete read.
func (a *App) readNews(ctx context.Context) (items []notices.Notice, ok bool) {
	readCtx, cancel := context.WithTimeout(ctx, localTimeout)
	defer cancel()
	items, err := a.d.News.Unread(readCtx)
	if err != nil {
		a.log.Warn("reading the unread news failed; continuing with what was read", "kept", len(items), "error", err)
		return items, false
	}
	return items, true
}

// readPackages reads the installed packages when a notice in memory has
// affects entries. A failed read is an empty list, so only notices with no
// affects apply (R5.5). ok reports a complete list, or that none was needed.
func (a *App) readPackages(ctx context.Context) (pkgs []pkgdb.Package, ok bool) {
	needed := false
	for _, n := range a.feedItems {
		needed = needed || len(n.Affects) > 0
	}
	if !needed {
		return nil, true
	}
	readCtx, cancel := context.WithTimeout(ctx, localTimeout)
	defer cancel()
	pkgs, skipped, err := a.d.Pkgs.Installed(readCtx)
	if err != nil {
		a.log.Warn("reading the installed packages failed; only notices without affects apply", "error", err)
		return nil, false
	}
	if skipped > 0 {
		a.log.Warn("some installed package entries could not be read and were skipped", "skipped", skipped)
	}
	return pkgs, true
}

// Record sources as state.json spells them; a record migrated from format 1
// has none until a list carries it.
const (
	sourceFeed = "feed"
	sourceNews = "news"
)

// reconcile deletes the unread records that no longer belong in the tray
// (R10.6). A record keeps no applicability, and decideNotifications and
// viewFor work from every record, so without this a notice whose package was
// upgraded or removed, or that the site withdrew, or a news item read with
// eselect, would stay unread (and, if held, be sent later) until Prune
// forgets it.
//
// Each source decides only about its own records, and only from a list known
// to be current:
//   - the feed, once a 200 was accepted in this process (after a restart and
//     a 304 its items are unknown): a "feed" record it no longer lists goes,
//     and so does one it lists that no longer applies, given a complete
//     package list;
//   - the news, after a complete News.Unread: a "news" record absent from the
//     unread list goes.
//
// A record the other source still lists is kept, so dropping it cannot
// re-create it as new and notify it again; after a partial or failed news
// read, "still lists" includes the last complete list read in this process. A migrated record (no source) is
// left to Prune, and so is every read record: Prune's 90 days still guard
// against notifying it again.
func (a *App) reconcile(news, applicable []notices.Notice, newsOK, pkgsOK bool) {
	inFeed := idSet(a.feedItems)
	inNews := idSet(news)
	applies := idSet(applicable)
	// The news a feed record may still be listed by: the current list when it
	// is complete; otherwise what it read plus the last complete list, since
	// the item the read failed on may be one of those. With no complete list
	// known the feed decides alone.
	newsKeeps := inNews
	if newsOK {
		a.lastNews = inNews
	} else {
		newsKeeps = maps.Clone(inNews)
		maps.Copy(newsKeeps, a.lastNews)
	}
	for id, r := range a.st.Notices {
		if r.Read {
			continue
		}
		var stale bool
		switch r.Source {
		case sourceFeed:
			stale = a.haveFeed && !newsKeeps[id] && (!inFeed[id] || (pkgsOK && !applies[id]))
		case sourceNews:
			stale = newsOK && !inNews[id] && !inFeed[id]
		}
		if stale {
			delete(a.st.Notices, id)
		}
	}
}

// claimSources sets the source of every record a list carries: the feed's
// when the feed lists it, the item in both sources included (R4.3), else the
// news'. A record keeps "feed" while the feed's items are unknown (after a
// restart and a 304), since the feed may still list it; a migrated record
// takes the source of whichever list next carries it, and one that neither
// carries keeps none (R10.5, R10.6).
func (a *App) claimSources(merged []notices.Notice) {
	for _, n := range merged {
		r, ok := a.st.Notices[n.ID]
		if !ok {
			continue
		}
		switch {
		case n.Source == notices.SourceFeed:
			r.Source = sourceFeed
		case r.Source == "" || a.haveFeed:
			r.Source = sourceNews
		}
		a.st.Notices[n.ID] = r
	}
}

func idSet(ns []notices.Notice) map[string]bool {
	set := make(map[string]bool, len(ns))
	for _, n := range ns {
		set[n.ID] = true
	}
	return set
}

// markSeen sets LastSeen for every record present in a source (R10.4). A
// 304 with no feed in memory (after a restart) confirms the last accepted
// feed without listing it; every record the feed may still list (source
// "feed", or "" for a migrated one) is then treated as seen, so an unchanged
// feed never ages its own notices out. A "news" record is seen only when the
// news lists it: a 304 says nothing about the news.
func (a *App) markSeen(now time.Time, merged []notices.Notice, notModified bool) {
	if notModified && !a.haveFeed {
		for id, r := range a.st.Notices {
			if r.Source == sourceFeed || r.Source == "" {
				r.LastSeen = now
				a.st.Notices[id] = r
			}
		}
	}
	for _, n := range merged {
		if r, ok := a.st.Notices[n.ID]; ok {
			r.LastSeen = now
			a.st.Notices[n.ID] = r
		}
	}
}

// send sends msgs and marks what was sent Notified: the message's notice, or
// every notice a summary covers. The first failure stops the round; what was
// not sent stays pending for the next check (R6.12).
func (a *App) send(ctx context.Context, msgs []notify.Message) {
	for _, m := range msgs {
		sendCtx, cancel := context.WithTimeout(ctx, callTimeout)
		_, err := a.d.Notify.Send(sendCtx, m)
		cancel()
		if err != nil {
			a.log.Warn("sending a notification failed; it stays pending until the next check",
				"notice", m.NoticeID, "urgency", m.Urgency, "covers", len(m.Covers), "error", err)
			return
		}
		ids := m.Covers
		if m.NoticeID != "" {
			ids = []string{m.NoticeID}
		}
		for _, id := range ids {
			if r, ok := a.st.Notices[id]; ok {
				r.Notified = true
				a.st.Notices[id] = r
			}
		}
		a.log.Info("notification sent", "notice", m.NoticeID, "urgency", m.Urgency,
			"covers", strings.Join(m.Covers, ","))
	}
}

// onNetworkChanged fetches when the network changes after a skipped fetch
// (R3.3); otherwise there is nothing to catch up on.
func (a *App) onNetworkChanged(ctx context.Context) {
	if !a.skipped {
		a.log.Debug("network changed; no fetch was skipped")
		return
	}
	a.check(ctx, checkNetwork)
}

// ---------- quit during blocking work ----------

// heldClicks is what guard's helper read from the menu while a blocking
// branch ran.
type heldClicks struct {
	events []sni.Event
	closed bool // the channel was closed
}

// guard runs fn, a branch that may block on I/O (a fetch, a network check,
// the local reads, sends), with a context a Quit click cancels, so Quit
// stops the tray within R1.4's 5 s even mid-check. While fn runs the loop
// does not read the menu, so a helper goroutine does: it only collects the
// clicks and cancels fn's context on Quit; it touches no App field. The
// clicks are then handled here, on the loop, in order. guard reports
// whether the user quit.
func (a *App) guard(ctx context.Context, fn func(context.Context)) (quit bool) {
	if a.iconEvents == nil {
		fn(ctx)
		return false
	}
	fnCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := make(chan struct{})
	held := make(chan heldClicks, 1)
	go holdClicks(a.iconEvents, stop, cancel, held)
	fn(fnCtx)
	close(stop)
	h := <-held
	if h.closed {
		a.iconEvents = nil
	}
	return a.replay(ctx, h.events)
}

// holdClicks collects menu clicks until stop is closed, calling cancel on
// Quit, then hands them over on held.
func holdClicks(events <-chan sni.Event, stop <-chan struct{}, cancel context.CancelFunc, held chan<- heldClicks) {
	var h heldClicks
	for {
		select {
		case <-stop:
			held <- h
			return
		case ev, ok := <-events:
			if !ok {
				h.closed, events = true, nil
				continue
			}
			h.events = append(h.events, ev)
			if ev.ItemID == menuQuit {
				cancel()
			}
		}
	}
}

// replay handles the clicks held during a blocking branch, keeping their
// order. Every click that cannot block is applied in order, Resume's state
// change included; the blocking work the clicks asked for then runs at most
// once, guarded: one fetch for any number of held Check nows (its refresh
// also sends what a resume released), else one release of the held
// notifications. Clicks arriving during that run are newer than all of
// these, so handling them after it keeps the order. On Quit the clicks
// before it are applied, the blocking work is skipped, and the tray quits.
func (a *App) replay(ctx context.Context, events []sni.Event) (quit bool) {
	checkNow, resumed := false, false
	for _, ev := range events {
		switch ev.ItemID {
		case menuQuit:
			a.log.Info("quit chosen from the menu")
			return true
		case menuCheckNow:
			checkNow = true
		case menuResume:
			resumed = a.clearPause() || resumed
		default:
			a.onMenu(ctx, ev) // cannot block: openings run off the loop
		}
	}
	switch {
	case checkNow:
		return a.guard(ctx, func(ctx context.Context) { a.check(ctx, checkManual) })
	case resumed:
		return a.guard(ctx, a.releaseHeld)
	}
	return false
}

// ---------- pause ----------

// paused reports whether a pause holds notifications at now.
func (a *App) paused(now time.Time) bool {
	return !a.st.PauseUntil.IsZero() && now.Before(a.st.PauseUntil)
}

// expirePause clears a pause whose end has passed, so viewFor stops showing
// it. It reports whether it cleared one.
func (a *App) expirePause(now time.Time) bool {
	if a.st.PauseUntil.IsZero() || now.Before(a.st.PauseUntil) {
		return false
	}
	a.st.PauseUntil = time.Time{}
	a.pauseAt = nil
	a.log.Info("notifications resumed: the pause ended")
	return true
}

// pauseTimer handles the pause-end timer: a pause that has ended releases
// what it held (R6.11).
func (a *App) pauseTimer(ctx context.Context) {
	now := a.d.Clock.Now()
	if a.st.PauseUntil.IsZero() {
		a.pauseAt = nil
		return
	}
	if !a.expirePause(now) {
		a.pauseAt = a.d.Clock.After(a.st.PauseUntil.Sub(now))
		return
	}
	a.releaseHeld(ctx)
}

// setPause holds notifications until until (R9.3).
func (a *App) setPause(until time.Time) {
	now := a.d.Clock.Now()
	a.st.PauseUntil = until
	a.pauseAt = a.d.Clock.After(until.Sub(now))
	a.log.Info("notifications paused", "until", until)
	a.render()
	a.save()
}

// resume ends a pause at the user's request (R9.4).
func (a *App) resume(ctx context.Context) {
	if a.clearPause() {
		a.releaseHeld(ctx)
	}
}

// clearPause ends a pause in the state only, reporting whether one was set.
func (a *App) clearPause() bool {
	if a.st.PauseUntil.IsZero() {
		return false
	}
	a.st.PauseUntil = time.Time{}
	a.pauseAt = nil
	a.log.Info("notifications resumed by the user")
	return true
}

// releaseHeld sends what a pause held, grouped as any check's (R6.11). A
// pause set again since (a later click) still holds what it holds.
func (a *App) releaseHeld(ctx context.Context) {
	a.send(ctx, decideNotifications(&a.st, nil, !a.st.Established, a.paused(a.d.Clock.Now()), a.d.Config))
	a.render()
	a.save()
}

// nextMorning is the next local 08:00 after now: today's when now is
// earlier, tomorrow's otherwise.
func nextMorning(now time.Time) time.Time {
	l := now.In(time.Local)
	t := time.Date(l.Year(), l.Month(), l.Day(), pauseMorningHour, 0, 0, 0, time.Local)
	if !t.After(now) {
		t = time.Date(l.Year(), l.Month(), l.Day()+1, pauseMorningHour, 0, 0, 0, time.Local)
	}
	return t
}

// ---------- actions ----------

// onNotification handles an action on a notification (R6.5, R7).
func (a *App) onNotification(ctx context.Context, ev notify.Event) {
	switch ev.Action {
	case actionDefault:
		if ev.NoticeID == "" {
			a.openIndex(ctx, ev.ActivationToken) // the R6.8 summary
			return
		}
		a.openNotice(ctx, ev.NoticeID, ev.ActivationToken)
	case actionMarkRead:
		a.markRead(ev.NoticeID)
	default:
		a.log.Debug("ignoring a notification action", "notice", ev.NoticeID, "action", ev.Action)
	}
}

// onMenu handles a menu click (R9). It reports whether the user quit.
func (a *App) onMenu(ctx context.Context, ev sni.Event) (quit bool) {
	switch id := ev.ItemID; {
	case ev.NoticeID != "":
		// Resolved by package sni at click time (R9.7): never a position in
		// a.view, which a check may have re-rendered since the menu was shown.
		a.openNotice(ctx, ev.NoticeID, "")
	case id == menuMore:
		a.openIndex(ctx, "")
	case id == menuCheckNow:
		return a.guard(ctx, func(ctx context.Context) { a.check(ctx, checkManual) })
	case id == menuMarkAllRead:
		a.markAllRead()
	case id == menuPauseHour:
		a.setPause(a.d.Clock.Now().Add(time.Hour))
	case id == menuPauseTomorrow:
		a.setPause(nextMorning(a.d.Clock.Now()))
	case id == menuResume:
		return a.guard(ctx, a.resume)
	case id == menuQuit:
		a.log.Info("quit chosen from the menu")
		return true
	default:
		// An unknown id, or one whose notice is no longer listed (R9.7).
		a.log.Debug("ignoring a click on a menu item that lists nothing", "item", id)
	}
	return false
}

// openNotice opens a notice's URL off the loop (R7.1).
func (a *App) openNotice(ctx context.Context, id, token string) {
	r, ok := a.st.Notices[id]
	if !ok {
		a.log.Warn("cannot open an unknown notice", "notice", id)
		return
	}
	a.startOpening(ctx, id, r.URL, token)
}

// openIndex opens https://<feed host>/notices/ off the loop.
func (a *App) openIndex(ctx context.Context, token string) {
	if a.indexURL == "" {
		a.log.Warn("cannot open the notices index: the feed URL has no host", "feed_url", a.d.Config.GetFeedURL())
		return
	}
	a.startOpening(ctx, "", a.indexURL, token)
}

// startOpening runs Open in its own goroutine: the portal can take a while
// and the xdg-open fallback can wait for the browser, and the loop must keep
// serving. The result comes back through a.opened.
//
// The opening keeps ctx's values but not its cancellation: a stop must not
// kill a browser xdg-open is starting. A stop cancels it only after
// openGrace (drainOpenings).
func (a *App) startOpening(ctx context.Context, id, target, token string) {
	a.openSeq++
	seq := a.openSeq
	openCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), openTimeout)
	a.openings[seq] = cancel
	go func() {
		defer cancel()
		err := a.d.Open.Open(openCtx, target, token)
		select {
		case a.opened <- openResult{seq: seq, noticeID: id, url: target, err: err}:
		case <-a.done:
		}
	}()
}

// onOpened records an opening's outcome: an opened notice is read (R7.4); a
// dismissed chooser opened nothing and is not a failure; a refused URL is a
// WARN naming the notice (R7.3); a refusal by the portal's policy opened
// nothing either, so the notice stays unread, with a WARN naming it (R7.5).
func (a *App) onOpened(r openResult) {
	delete(a.openings, r.seq)
	switch {
	case r.err == nil:
		a.log.Info("opened", "notice", r.noticeID, "url", r.url)
		if r.noticeID != "" {
			a.markRead(r.noticeID)
		}
	case errors.Is(r.err, portal.ErrCancelled):
		a.log.Info("opening cancelled by the user", "notice", r.noticeID, "url", r.url)
	case errors.Is(r.err, portal.ErrRefusedByPolicy):
		a.log.Warn("the desktop portal refused to open the URL by policy; the notice stays unread",
			"notice", r.noticeID, "url", r.url, "error", r.err)
	case errors.Is(r.err, portal.ErrURLRefused):
		a.log.Warn("refused to open a URL that is not https on the feed host", "notice", r.noticeID,
			"url", r.url, "error", r.err)
	default:
		a.log.Warn("opening failed", "notice", r.noticeID, "url", r.url, "error", r.err)
	}
}

// drainOpenings gives the openings in flight openGrace to finish and records
// their outcome, then cancels the rest and waits briefly for them.
func (a *App) drainOpenings() {
	if len(a.openings) == 0 {
		return
	}
	grace := time.NewTimer(openGrace)
	defer grace.Stop()
	for len(a.openings) > 0 {
		select {
		case r := <-a.opened:
			a.onOpened(r)
		case <-grace.C:
			for _, cancel := range a.openings {
				cancel()
			}
			a.awaitCancelledOpenings()
			return
		}
	}
}

// awaitCancelledOpenings records the cancelled openings that return within
// cancelledOpenWait; one that ignores its context is abandoned, and exits
// once it returns because a.done is closed.
func (a *App) awaitCancelledOpenings() {
	wait := time.NewTimer(cancelledOpenWait)
	defer wait.Stop()
	for len(a.openings) > 0 {
		select {
		case r := <-a.opened:
			a.onOpened(r)
		case <-wait.C:
			a.log.Warn("openings still running at stop were abandoned", "count", len(a.openings))
			return
		}
	}
}

// markRead marks one notice read.
func (a *App) markRead(id string) {
	r, ok := a.st.Notices[id]
	if !ok || r.Read {
		return
	}
	r.Read = true
	a.st.Notices[id] = r
	a.render()
	a.save()
}

// markAllRead marks every notice read (R9.2).
func (a *App) markAllRead() {
	for id, r := range a.st.Notices {
		r.Read = true
		a.st.Notices[id] = r
	}
	a.log.Info("all notices marked read", "count", len(a.st.Notices))
	a.render()
	a.save()
}

// ---------- output ----------

// render shows the state on the icon.
func (a *App) render() {
	a.view = viewFor(a.st)
	if a.iconUp {
		a.d.Icon.SetState(a.view)
	}
}

// save writes the state; a failure is retried by the next save, which
// writes the whole state again. A first run is written too, so its backoff and
// Retry-After survive a restart (R2.13, R2.14); state.Established, not the
// file's existence, tells a restarted first run from an established state.
func (a *App) save() {
	if err := a.d.Store.Save(a.st); err != nil {
		a.log.Warn("saving the state failed; retrying at the next change", "error", err)
		return
	}
	a.st.Saved = true
}
