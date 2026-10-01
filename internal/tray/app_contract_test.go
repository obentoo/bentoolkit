package tray_test

// Contract tests from the tech reviews of tasks 5-8, beside the pre-authored
// app_test.go: deadlines on adapter calls, openings off the loop, summary
// coverage, stale records and goroutines left behind by Run.

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/desktop/netmon"
	"github.com/obentoo/bentoolkit/internal/desktop/notify"
	"github.com/obentoo/bentoolkit/internal/desktop/portal"
	"github.com/obentoo/bentoolkit/internal/desktop/sni"
	"github.com/obentoo/bentoolkit/internal/gentoo/pkgdb"
	"github.com/obentoo/bentoolkit/internal/notices"
	"github.com/obentoo/bentoolkit/internal/tray"
	"github.com/obentoo/bentoolkit/internal/tray/feed"
	"github.com/obentoo/bentoolkit/internal/tray/state"
)

// hungNet is a NetworkManager that never answers.
type hungNet struct{ changed chan struct{} }

func (hungNet) Allowed(ctx context.Context) (netmon.Verdict, error) {
	<-ctx.Done()
	return netmon.Verdict{}, fmt.Errorf("org.freedesktop.NetworkManager: %w", ctx.Err())
}
func (n hungNet) Changed() <-chan struct{} { return n.changed }

// blockingOpener blocks every Open until release is closed or its context
// ends, and reports each call on started.
type blockingOpener struct {
	release chan struct{}
	started chan string
	err     error
}

func (o *blockingOpener) Open(ctx context.Context, url, _ string) error {
	o.started <- url
	select {
	case <-o.release:
		return o.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// startWithOpener starts the harness's App with opener in place of h.open.
func startWithOpener(h *appHarness, opener tray.Opener) {
	h.t.Helper()
	startWithDeps(h, func(d *tray.Deps) { d.Open = opener })
}

// startWithDeps starts the harness's App with its Deps changed by edit.
func startWithDeps(h *appHarness, edit func(*tray.Deps)) {
	h.t.Helper()
	deps := tray.Deps{
		Log:   slog.New(slog.NewTextHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Clock: h.clock, Rand: func() float64 { return h.rand }, Config: h.cfg,
		Feed: h.feed, News: h.news, Pkgs: h.pkgs, Notify: h.notif, Icon: h.icon,
		Open: h.open, Net: h.net, Store: h.store, Bus: h.bus,
	}
	edit(&deps)
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() { h.done <- tray.New(deps).Run(ctx) }()
	h.t.Cleanup(func() {
		cancel()
		select {
		case <-h.done:
		case <-time.After(5 * time.Second):
		}
	})
}

// TestApp_HungNetworkCheckTimesOutAndFetches: a NetworkManager that never
// answers costs one call deadline, a WARN, and the fetch still happens.
func TestApp_HungNetworkCheckTimesOutAndFetches(t *testing.T) {
	t.Cleanup(tray.SetCallTimeout(100 * time.Millisecond))
	h := newApp(t)
	h.netDep = hungNet{changed: make(chan struct{})}
	h.start()
	h.waitFor("the startup timer", func() bool { return h.clock.hasPending(3 * time.Minute) })
	h.clock.Advance(3 * time.Minute)
	h.waitFor("the fetch after the network check timed out", h.fetches(1))
	if !warned(h, "NetworkManager", "deadline exceeded") {
		t.Error("the hung network check was not logged at WARN")
	}
}

// TestApp_SummaryMarksEveryCoveredNoticeNotified: a burst of four becomes one
// summary, every covered notice is recorded as notified, and the next check
// sends nothing again.
func TestApp_SummaryMarksEveryCoveredNoticeNotified(t *testing.T) {
	h := newApp(t)
	var burst []notices.Notice
	for i := range 4 {
		burst = append(burst, releaseNotice(fmt.Sprintf("2026-10-02-rel-%d", i), fmt.Sprintf("Release %d", i)))
	}
	h.feed.next = func(int) (feed.Result, error) { return okResult(1790812800, burst...), nil }
	h.start()
	h.checkNow()
	h.waitFor("the summary", func() bool { return len(h.notif.messages()) == 1 })
	if m := h.notif.messages()[0]; m.NoticeID != "" || len(m.Covers) != 4 {
		t.Fatalf("message = %+v, want one summary covering 4", m)
	}
	h.savedWhere("every covered notice notified", func(st state.State) bool {
		for _, n := range burst {
			if !st.Notices[n.ID].Notified {
				return false
			}
		}
		return true
	})
	h.checkNow()
	h.never("the summary sent again", func() bool { return h.notif.attempts() > 1 })
}

// TestApp_SlowOpenDoesNotBlockTheLoop: an opening in progress leaves the
// loop serving, and the notice is read once the opening succeeds (R7.4).
func TestApp_SlowOpenDoesNotBlockTheLoop(t *testing.T) {
	h := newApp(t)
	h.pkgs.list = []pkgdb.Package{bentooFoo}
	h.feed.next = func(int) (feed.Result, error) { return okResult(1790812800, fooCVE()), nil }
	opener := &blockingOpener{release: make(chan struct{}), started: make(chan string, 1)}
	startWithOpener(h, opener)
	h.checkNow()
	h.waitFor("the critical notification", func() bool { return len(h.notif.messages()) == 1 })

	h.notif.events <- notify.Event{NoticeID: fooCVE().ID, Action: "default"}
	<-opener.started
	h.menu(202) // served while Open is still blocked
	h.savedWhere("the pause saved during the opening", func(st state.State) bool {
		return !st.PauseUntil.IsZero() && !st.Notices[fooCVE().ID].Read
	})
	close(opener.release)
	h.savedWhere("the opened notice read", func(st state.State) bool { return st.Notices[fooCVE().ID].Read })
}

// TestApp_CancelledOpenIsNotRead: a dismissed app chooser opened nothing, so
// the notice stays unread and no failure is logged.
func TestApp_CancelledOpenIsNotRead(t *testing.T) {
	h := withCriticalNotice(t)
	h.open.mu.Lock()
	h.open.err = fmt.Errorf("opening %s: %w", fooCVE().URL, portal.ErrCancelled)
	h.open.mu.Unlock()
	h.notif.events <- notify.Event{NoticeID: fooCVE().ID, Action: "default"}
	h.waitFor("the cancellation logged", func() bool {
		return h.logLine(func(l string) bool { return strings.Contains(l, "level=INFO") && strings.Contains(l, "cancelled") })
	})
	if st, _ := h.store.last(); st.Notices[fooCVE().ID].Read {
		t.Error("a cancelled opening marked the notice read")
	}
	if warned(h, fooCVE().ID) {
		t.Error("a cancelled opening was logged as a failure")
	}
}

// TestApp_UpgradedPackageDropsTheUnreadNotice: once the installed package is
// out of range, its unread notice leaves the state and the icon.
func TestApp_UpgradedPackageDropsTheUnreadNotice(t *testing.T) {
	h := withCriticalNotice(t)
	upgraded := bentooFoo
	upgraded.Version = "1.3"
	h.pkgs.list = []pkgdb.Package{upgraded} // read by the App only inside a check
	h.checkNow()
	h.savedWhere("the stale record dropped", func(st state.State) bool {
		_, ok := st.Notices[fooCVE().ID]
		return !ok
	})
	h.viewWhere("nothing unread", func(v sni.View) bool { return v.Unread == 0 })
}

// TestApp_RunLeavesNoGoroutines: Run returns within 5 s of cancellation with
// an opening in flight, and no goroutine of the App outlives it.
func TestApp_RunLeavesNoGoroutines(t *testing.T) {
	h := newApp(t)
	opener := &blockingOpener{release: make(chan struct{}), started: make(chan string, 1)}
	startWithOpener(h, opener)
	h.menu(100)
	<-opener.started
	began := time.Now()
	h.cancel()
	select {
	case err := <-h.done:
		h.done <- err // for the harness's cleanup, which waits on it
		if err != nil {
			t.Fatalf("Run = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return within 5s with an opening in flight")
	}
	if d := time.Since(began); d > 5*time.Second {
		t.Errorf("stop took %s", d)
	}
	h.waitFor("no App goroutine left", func() bool { return !slices.ContainsFunc(goroutineStacks(), isAppStack) })
}

func goroutineStacks() []string {
	buf := make([]byte, 1<<20)
	return strings.Split(string(buf[:runtime.Stack(buf, true)]), "\n\n")
}

// isAppStack matches a goroutine running code of package tray itself: the
// App, guard's click helper or a SystemClock timer (not tray_test).
func isAppStack(s string) bool { return strings.Contains(s, "bentoolkit/internal/tray.") }

// slowFeed is a fetch that takes up to 12 s: it returns when release is
// closed, when its context ends, or after 12 s, and reports its start.
type slowFeed struct {
	started chan struct{}
	release chan struct{}
}

func (f *slowFeed) Fetch(ctx context.Context, _ string) (feed.Result, error) {
	f.started <- struct{}{}
	select {
	case <-f.release:
		return okResult(1790812800), nil
	case <-ctx.Done():
		return feed.Result{}, ctx.Err()
	case <-time.After(12 * time.Second):
		return okResult(1790812800), nil
	}
}

// TestApp_QuitDuringASlowFetchStopsWithinFiveSeconds is R1.4 for Quit while
// a check is blocked: the click cancels the fetch, and Run saves, releases
// the name and returns nil within 5 s.
func TestApp_QuitDuringASlowFetchStopsWithinFiveSeconds(t *testing.T) {
	h := newApp(t)
	slow := &slowFeed{started: make(chan struct{}, 1), release: make(chan struct{})}
	startWithDeps(h, func(d *tray.Deps) { d.Feed = slow })
	h.menu(200)
	<-slow.started
	began := time.Now()
	h.menu(300)
	select {
	case err := <-h.done:
		h.done <- err // for the harness's cleanup, which waits on it
		if err != nil {
			t.Fatalf("Run after Quit = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Quit during a slow fetch did not stop Run within 5s")
	}
	t.Logf("Quit honoured after %s", time.Since(began))
	if _, n := h.store.last(); n == 0 {
		t.Error("state was not saved on Quit")
	}
	if h.bus.releases() == 0 {
		t.Error("the bus name was not released on Quit")
	}
	if warned(h, "feed fetch failed") {
		t.Error("the fetch cancelled by Quit was logged as a feed failure")
	}
}

// TestApp_ClicksDuringACheckAreHandledAfterIt: a click other than Quit
// arriving while a check blocks is handled once the check returns.
func TestApp_ClicksDuringACheckAreHandledAfterIt(t *testing.T) {
	h := newApp(t)
	slow := &slowFeed{started: make(chan struct{}, 1), release: make(chan struct{})}
	startWithDeps(h, func(d *tray.Deps) { d.Feed = slow })
	h.menu(200)
	<-slow.started
	h.menu(202)
	close(slow.release)
	h.savedWhere("the pause clicked during the check", func(st state.State) bool {
		return st.PauseUntil.Equal(appStart.Add(time.Hour))
	})
}

// gatedFeed blocks each fetch until the test sends one token on step (or its
// context ends), and reports each start on started.
type gatedFeed struct {
	started chan struct{}
	step    chan struct{}
	mu      sync.Mutex
	calls   int
}

func newGatedFeed() *gatedFeed {
	return &gatedFeed{started: make(chan struct{}, 16), step: make(chan struct{})}
}

func (f *gatedFeed) Fetch(ctx context.Context, _ string) (feed.Result, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	f.started <- struct{}{}
	select {
	case <-f.step:
		return okResult(1790812800), nil
	case <-ctx.Done():
		return feed.Result{}, ctx.Err()
	}
}

func (f *gatedFeed) count() int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls }

// waitClicksHeld waits until the helper of the running check has read every
// queued click, so they are replayed rather than read by the loop.
func waitClicksHeld(h *appHarness) {
	h.t.Helper()
	h.waitFor("the clicks held by the running check", func() bool { return len(h.icon.events) == 0 })
}

// TestApp_HeldClicksKeepTheirOrderAroundAHeldCheckNow: Check now and Pause
// held during a check, then Resume clicked during the replayed fetch, end
// unpaused: Pause, older, is not applied after Resume.
func TestApp_HeldClicksKeepTheirOrderAroundAHeldCheckNow(t *testing.T) {
	h := newApp(t)
	gf := newGatedFeed()
	startWithDeps(h, func(d *tray.Deps) { d.Feed = gf })
	h.menu(200)
	<-gf.started
	h.menu(200)
	h.menu(202)
	waitClicksHeld(h)
	gf.step <- struct{}{} // first fetch done: Pause applied, then the held Check now fetches
	<-gf.started
	h.menu(204)
	waitClicksHeld(h)
	gf.step <- struct{}{}
	h.viewWhere("unpaused after Resume, the newest click", func(v sni.View) bool { return !v.Paused })
	h.never("Pause re-applied after the newer Resume", func() bool {
		st, _ := h.store.last()
		v, _ := h.icon.last()
		return !st.PauseUntil.IsZero() || v.Paused
	})
}

// TestApp_HeldCheckNowsMergeIntoOneFetch: three Check now clicks held during
// a check cost one more fetch, not three.
func TestApp_HeldCheckNowsMergeIntoOneFetch(t *testing.T) {
	h := newApp(t)
	gf := newGatedFeed()
	startWithDeps(h, func(d *tray.Deps) { d.Feed = gf })
	h.menu(200)
	<-gf.started
	h.menu(200)
	h.menu(200)
	h.menu(200)
	waitClicksHeld(h)
	gf.step <- struct{}{}
	<-gf.started
	gf.step <- struct{}{}
	h.never("a third fetch for the held Check nows", func() bool { return gf.count() > 2 })
}

// TestApp_ResumeThenQuitDuringACheckSavesTheResume: Resume clicked just
// before Quit while a check blocks still ends the pause in the saved state.
func TestApp_ResumeThenQuitDuringACheckSavesTheResume(t *testing.T) {
	h := newApp(t)
	h.store.initial.PauseUntil = appStart.Add(5 * time.Hour)
	gf := newGatedFeed()
	startWithDeps(h, func(d *tray.Deps) { d.Feed = gf })
	h.menu(200)
	<-gf.started
	h.menu(204)
	h.menu(300)
	select {
	case err := <-h.done:
		h.done <- err // for the harness's cleanup, which waits on it
		if err != nil {
			t.Fatalf("Run after Quit = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Quit did not stop Run within 5s")
	}
	st, n := h.store.last()
	if n == 0 || !st.PauseUntil.IsZero() {
		t.Errorf("saved PauseUntil = %v (saves %d), want the pause ended by Resume", st.PauseUntil, n)
	}
}

// TestApp_DefaultClockTimersEndWithRun: with Deps.Clock nil the App uses the
// wall clock, and its abandoned timers (one per Check now) end with Run.
func TestApp_DefaultClockTimersEndWithRun(t *testing.T) {
	h := newApp(t)
	startWithDeps(h, func(d *tray.Deps) { d.Clock = nil })
	for range 3 {
		h.checkNow()
	}
	h.waitFor("the wall-clock timers running", func() bool {
		return slices.ContainsFunc(goroutineStacks(), func(s string) bool { return strings.Contains(s, "tray.wallAfter") })
	})
	h.cancel()
	select {
	case err := <-h.done:
		h.done <- err
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return within 5s")
	}
	h.waitFor("no App goroutine left", func() bool { return !slices.ContainsFunc(goroutineStacks(), isAppStack) })
}
