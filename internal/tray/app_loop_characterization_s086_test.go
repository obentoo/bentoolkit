package tray_test

// Characterization tests for (*App).loop (story 086, sub-task 6.1). They pin
// the loop's current behaviour before it is split under the gocognit
// threshold: a closed event source is dropped and the loop keeps serving, a
// Quit held during each guarded branch stops the run, a stop or a bus loss
// that arrives while a branch is busy is honoured once it returns, and a bus
// loss reported together with a stop is a stop.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/desktop/netmon"
	"github.com/obentoo/bentoolkit/internal/notices"
	"github.com/obentoo/bentoolkit/internal/tray"
	"github.com/obentoo/bentoolkit/internal/tray/state"
)

// s086LoopParked reports whether a goroutine is parked in the select of
// (*App).loop itself, not in one reached through a branch it runs. A select
// with a closed channel among its cases never parks, so a parked loop has
// consumed every channel closed before the check.
func s086LoopParked() bool {
	for _, s := range goroutineStacks() {
		// Without GOTRACEBACK=system the runtime frames are hidden: the first
		// frame of a goroutine parked in a select is the function holding it.
		lines := strings.Split(s, "\n")
		if len(lines) > 1 && strings.Contains(lines[0], "[select") &&
			strings.Contains(lines[1], "internal/tray.(*App).loop(") {
			return true
		}
	}
	return false
}

// s086WaitParked waits until the loop is parked in its own select.
func s086WaitParked(h *appHarness) {
	h.t.Helper()
	h.waitFor("the loop parked in its select", s086LoopParked)
}

// s086Result waits for Run to return, and puts the result back for the
// harness's cleanup, which waits on it.
func s086Result(t *testing.T, h *appHarness) error {
	t.Helper()
	select {
	case err := <-h.done:
		h.done <- err
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return within 5s")
		return nil
	}
}

// s086Stopped asserts a normal stop: Run returned nil, the state was saved
// and the bus name released.
func s086Stopped(t *testing.T, h *appHarness) {
	t.Helper()
	if err := s086Result(t, h); err != nil {
		t.Fatalf("Run = %v, want nil (a normal stop)", err)
	}
	if _, n := h.store.last(); n == 0 {
		t.Error("the state was not saved on stop")
	}
	if h.bus.releases() == 0 {
		t.Error("the bus name was not released on stop")
	}
}

// s086GatedNews blocks every Unread until the test sends a token on step or
// the read's context ends, and reports each start on started.
type s086GatedNews struct {
	started chan struct{}
	step    chan struct{}
	changed chan struct{}
}

func newS086GatedNews() *s086GatedNews {
	return &s086GatedNews{started: make(chan struct{}, 16), step: make(chan struct{}), changed: make(chan struct{}, 4)}
}

func (n *s086GatedNews) Unread(ctx context.Context) ([]notices.Notice, error) {
	n.started <- struct{}{}
	select {
	case <-n.step:
		return nil, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (n *s086GatedNews) Changed() <-chan struct{} { return n.changed }

// s086GatedStore blocks the first Save after arm until release is closed, and
// reports that Save's start on started. Load and every other Save go to inner.
type s086GatedStore struct {
	inner   *appStore
	mu      sync.Mutex
	armed   bool
	started chan struct{}
	release chan struct{}
}

func (s *s086GatedStore) arm() { s.mu.Lock(); defer s.mu.Unlock(); s.armed = true }

func (s *s086GatedStore) Load() (state.State, error) { return s.inner.Load() }
func (s *s086GatedStore) Save(st state.State) error {
	s.mu.Lock()
	block := s.armed
	s.armed = false
	s.mu.Unlock()
	if block {
		s.started <- struct{}{}
		<-s.release
	}
	return s.inner.Save(st)
}

// s086CtxIcon records the context Run gives Start: Run's own context, which
// it also runs the loop with.
type s086CtxIcon struct {
	*appIcon
	mu  sync.Mutex
	ctx context.Context
}

func (i *s086CtxIcon) Start(ctx context.Context) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.ctx = ctx
	return nil
}

// s086StopLostBus reports Lost on the closing of Run's own context, as a
// connection closed at a stop does: both events become ready at once.
type s086StopLostBus struct {
	*appBus
	icon *s086CtxIcon
}

func (b *s086StopLostBus) Lost() <-chan struct{} {
	b.icon.mu.Lock()
	defer b.icon.mu.Unlock()
	return b.icon.ctx.Done()
}

// TestS086Loop_ClosedEventSourceIsDroppedAndTheLoopKeepsServing: each event
// channel the loop reads (notifications, menu clicks, news changes, network
// changes) can close; the loop drops it, does not treat the close as an
// event, and still runs the fetch timer and stops normally.
func TestS086Loop_ClosedEventSourceIsDroppedAndTheLoopKeepsServing(t *testing.T) {
	for name, closeIt := range map[string]func(h *appHarness){
		"notification events": func(h *appHarness) { close(h.notif.events) },
		"menu clicks":         func(h *appHarness) { close(h.icon.events) },
		"news changes":        func(h *appHarness) { close(h.news.changed) },
		"network changes":     func(h *appHarness) { close(h.net.changed) },
	} {
		t.Run(name, func(t *testing.T) {
			h := newApp(t)
			h.start()
			h.waitFor("the startup timer", func() bool { return h.clock.hasPending(3 * time.Minute) })
			s086WaitParked(h)
			closeIt(h)
			s086WaitParked(h) // parked again: the closed channel was consumed
			if n := h.news.readCount(); n != 0 {
				t.Errorf("closing the %s read the news %d times, want 0", name, n)
			}
			if n := h.feed.count(); n != 0 {
				t.Errorf("closing the %s fetched %d times, want 0", name, n)
			}
			h.clock.Advance(3 * time.Minute)
			h.waitFor("the scheduled fetch after the close", h.fetches(1))
			h.savedWhere("the check's save", func(state.State) bool { return true })
			h.cancel()
			s086Stopped(t, h)
		})
	}
}

// TestS086Loop_QuitHeldDuringTheScheduledFetchStops: Quit clicked while the
// fetch timer's check blocks cancels it and stops the run.
func TestS086Loop_QuitHeldDuringTheScheduledFetchStops(t *testing.T) {
	h := newApp(t)
	gf := newGatedFeed()
	startWithDeps(h, func(d *tray.Deps) { d.Feed = gf })
	h.waitFor("the startup timer", func() bool { return h.clock.hasPending(3 * time.Minute) })
	h.clock.Advance(3 * time.Minute)
	<-gf.started
	h.menu(300)
	s086Stopped(t, h)
	if n := gf.count(); n != 1 {
		t.Errorf("fetches = %d, want 1", n)
	}
}

// TestS086Loop_QuitHeldDuringThePauseEndStops: Quit clicked while the pause
// timer's branch blocks (here in its save) stops the run once it returns,
// and the pause it ended stays ended in the saved state.
func TestS086Loop_QuitHeldDuringThePauseEndStops(t *testing.T) {
	h := newApp(t)
	h.store.initial.PauseUntil = appStart.Add(time.Minute)
	gs := &s086GatedStore{inner: h.store, started: make(chan struct{}, 1), release: make(chan struct{})}
	startWithDeps(h, func(d *tray.Deps) { d.Store = gs })
	h.waitFor("the pause and startup timers", func() bool {
		return h.clock.hasPending(time.Minute) && h.clock.hasPending(3*time.Minute)
	})
	gs.arm()
	h.clock.Advance(time.Minute)
	<-gs.started
	h.menu(300)
	waitClicksHeld(h)
	close(gs.release)
	s086Stopped(t, h)
	st, _ := h.store.last()
	if !st.PauseUntil.IsZero() {
		t.Errorf("saved PauseUntil = %v, want zero: the pause ended", st.PauseUntil)
	}
	if n := h.feed.count(); n != 0 {
		t.Errorf("fetches = %d, want 0", n)
	}
}

// TestS086Loop_QuitHeldDuringANewsChangeStops: Quit clicked while the check
// a news change started blocks in the news read cancels it and stops.
func TestS086Loop_QuitHeldDuringANewsChangeStops(t *testing.T) {
	h := newApp(t)
	gn := newS086GatedNews()
	startWithDeps(h, func(d *tray.Deps) { d.News = gn })
	h.waitFor("the startup timer", func() bool { return h.clock.hasPending(3 * time.Minute) })
	gn.changed <- struct{}{}
	<-gn.started
	h.menu(300)
	s086Stopped(t, h)
	if n := h.feed.count(); n != 0 {
		t.Errorf("a news change fetched %d times, want 0", n)
	}
}

// TestS086Loop_QuitHeldDuringANetworkChangeFetchStops: after a scheduled
// fetch skipped offline, a network change fetches; Quit clicked while that
// fetch blocks cancels it and stops.
func TestS086Loop_QuitHeldDuringANetworkChangeFetchStops(t *testing.T) {
	h := newApp(t)
	h.net.set(netmon.Verdict{Online: false})
	gf := newGatedFeed()
	startWithDeps(h, func(d *tray.Deps) { d.Feed = gf })
	h.waitFor("the startup timer", func() bool { return h.clock.hasPending(3 * time.Minute) })
	h.clock.Advance(3 * time.Minute)
	h.savedWhere("the skipped check's save", func(state.State) bool { return true })
	if n := gf.count(); n != 0 {
		t.Fatalf("an offline scheduled check fetched %d times, want 0", n)
	}
	h.net.set(netmon.Verdict{Online: true})
	h.net.changed <- struct{}{}
	<-gf.started
	h.menu(300)
	s086Stopped(t, h)
	if n := gf.count(); n != 1 {
		t.Errorf("fetches = %d, want 1 (the network change's)", n)
	}
}

// TestS086Loop_StopWhileACheckBlocksStopsCleanly: a stop (Run's context
// cancelled, as by a signal) while a check blocks ends the check and stops
// normally.
func TestS086Loop_StopWhileACheckBlocksStopsCleanly(t *testing.T) {
	h := newApp(t)
	gf := newGatedFeed()
	startWithDeps(h, func(d *tray.Deps) { d.Feed = gf })
	h.waitFor("the startup timer", func() bool { return h.clock.hasPending(3 * time.Minute) })
	h.clock.Advance(3 * time.Minute)
	<-gf.started
	h.cancel()
	s086Stopped(t, h)
}

// TestS086Loop_BusLostWhileACheckBlocksEndsAfterIt: a bus loss while a check
// blocks is not acted on until the check returns; Run then saves, returns
// ErrBusLost and does not release the name, gone with the connection.
func TestS086Loop_BusLostWhileACheckBlocksEndsAfterIt(t *testing.T) {
	h := newApp(t)
	gf := newGatedFeed()
	startWithDeps(h, func(d *tray.Deps) { d.Feed = gf })
	h.waitFor("the startup timer", func() bool { return h.clock.hasPending(3 * time.Minute) })
	h.clock.Advance(3 * time.Minute)
	<-gf.started
	close(h.bus.lost)
	select {
	case err := <-h.done:
		t.Fatalf("Run returned %v while the check still blocked", err)
	default:
	}
	gf.step <- struct{}{}
	err := s086Result(t, h)
	if !errors.Is(err, tray.ErrBusLost) {
		t.Fatalf("Run = %v, want ErrBusLost", err)
	}
	if _, n := h.store.last(); n < 2 {
		t.Errorf("saves = %d, want the check's and the bus loss's", n)
	}
	if n := h.bus.releases(); n != 0 {
		t.Errorf("releases = %d, want 0 after a bus loss", n)
	}
}

// TestS086Loop_BusLostTogetherWithAStopIsAStop: a stop that closes the
// connection reports Lost too; whichever of the two the select takes, the
// stop wins: Run returns nil and releases the name. Repeated so both select
// orders are taken.
func TestS086Loop_BusLostTogetherWithAStopIsAStop(t *testing.T) {
	for range 16 {
		h := newApp(t)
		icon := &s086CtxIcon{appIcon: h.icon}
		startWithDeps(h, func(d *tray.Deps) {
			d.Icon = icon
			d.Bus = &s086StopLostBus{appBus: h.bus, icon: icon}
		})
		h.waitFor("the startup timer", func() bool { return h.clock.hasPending(3 * time.Minute) })
		s086WaitParked(h)
		h.cancel()
		s086Stopped(t, h) // this Run has returned: the next App's loop is alone
	}
}
