package tray_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/desktop/netmon"
	"github.com/obentoo/bentoolkit/internal/desktop/notify"
	"github.com/obentoo/bentoolkit/internal/desktop/sni"
	"github.com/obentoo/bentoolkit/internal/gentoo/pkgdb"
	"github.com/obentoo/bentoolkit/internal/notices"
	"github.com/obentoo/bentoolkit/internal/tray"
	"github.com/obentoo/bentoolkit/internal/tray/feed"
	"github.com/obentoo/bentoolkit/internal/tray/state"
)

// ---------- manual clock ----------

type appTimer struct {
	at time.Time
	d  time.Duration
	ch chan time.Time
}

type appClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*appTimer
}

func (c *appClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *appClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &appTimer{at: c.now.Add(d), d: d, ch: make(chan time.Time, 1)}
	if d <= 0 {
		t.ch <- c.now
		return t.ch
	}
	c.timers = append(c.timers, t)
	return t.ch
}

// Advance moves the clock and fires every timer that is due.
func (c *appClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	keep := c.timers[:0]
	for _, t := range c.timers {
		if !t.at.After(c.now) {
			t.ch <- c.now
		} else {
			keep = append(keep, t)
		}
	}
	c.timers = keep
}

func (c *appClock) hasPending(d time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, t := range c.timers {
		if t.d == d {
			return true
		}
	}
	return false
}

func (c *appClock) pending() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []time.Duration
	for _, t := range c.timers {
		out = append(out, t.d)
	}
	return out
}

// ---------- fakes ----------

type appFeed struct {
	mu    sync.Mutex
	calls int
	etags []string
	next  func(call int) (feed.Result, error)
}

func (f *appFeed) Fetch(_ context.Context, etag string) (feed.Result, error) {
	f.mu.Lock()
	f.calls++
	f.etags = append(f.etags, etag)
	n, fn := f.calls, f.next
	f.mu.Unlock()
	return fn(n)
}

func (f *appFeed) count() int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls }
func (f *appFeed) set(fn func(int) (feed.Result, error)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next = fn
}

type appNews struct {
	mu      sync.Mutex
	items   []notices.Notice
	err     error
	reads   int
	changed chan struct{}
}

func (n *appNews) Unread(context.Context) ([]notices.Notice, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.reads++
	return slices.Clone(n.items), n.err
}
func (n *appNews) Changed() <-chan struct{} { return n.changed }
func (n *appNews) readCount() int           { n.mu.Lock(); defer n.mu.Unlock(); return n.reads }

type appPkgs struct {
	list    []pkgdb.Package
	skipped int
	err     error
}

func (p *appPkgs) Installed(context.Context) ([]pkgdb.Package, int, error) {
	return p.list, p.skipped, p.err
}

type appNotifier struct {
	mu     sync.Mutex
	sent   []notify.Message
	tries  int
	err    error
	events chan notify.Event
}

func (n *appNotifier) Send(_ context.Context, m notify.Message) (uint32, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.tries++
	if n.err != nil {
		return 0, n.err
	}
	n.sent = append(n.sent, m)
	return uint32(len(n.sent)), nil
}
func (n *appNotifier) Events() <-chan notify.Event { return n.events }
func (n *appNotifier) messages() []notify.Message {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.sent)
}
func (n *appNotifier) attempts() int  { n.mu.Lock(); defer n.mu.Unlock(); return n.tries }
func (n *appNotifier) setErr(e error) { n.mu.Lock(); defer n.mu.Unlock(); n.err = e }

type appIcon struct {
	mu       sync.Mutex
	views    []sni.View
	events   chan sni.Event
	startErr error
}

func (i *appIcon) Start(context.Context) error { return i.startErr }
func (i *appIcon) SetState(v sni.View) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.views = append(i.views, v)
}
func (i *appIcon) Events() <-chan sni.Event { return i.events }
func (i *appIcon) last() (sni.View, bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if len(i.views) == 0 {
		return sni.View{}, false
	}
	return i.views[len(i.views)-1], true
}

type openCall struct{ URL, Token string }

type appOpener struct {
	mu    sync.Mutex
	calls []openCall
	err   error
}

func (o *appOpener) Open(_ context.Context, url, token string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls = append(o.calls, openCall{url, token})
	return o.err
}
func (o *appOpener) opened() []openCall {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.calls)
}

type appNet struct {
	mu      sync.Mutex
	v       netmon.Verdict
	err     error
	changed chan struct{}
}

func (n *appNet) Allowed(context.Context) (netmon.Verdict, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.v, n.err
}
func (n *appNet) Changed() <-chan struct{} { return n.changed }
func (n *appNet) set(v netmon.Verdict)     { n.mu.Lock(); defer n.mu.Unlock(); n.v = v }

type appStore struct {
	mu       sync.Mutex
	initial  state.State
	saves    []state.State
	saveErr  error
	attempts int
}

// Load returns initial as state.Store.Load would: a format 1 state was written
// past its first run, so it loads Established (R10.5, R6.9).
func (s *appStore) Load() (state.State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.initial
	st.Notices = maps.Clone(s.initial.Notices)
	if st.Format == 1 {
		st.Established = true
	}
	return st, nil
}
func (s *appStore) Save(st state.State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attempts++
	if s.saveErr != nil {
		return s.saveErr
	}
	st.Notices = maps.Clone(st.Notices)
	s.saves = append(s.saves, st)
	return nil
}
func (s *appStore) last() (state.State, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.saves) == 0 {
		return state.State{}, 0
	}
	return s.saves[len(s.saves)-1], len(s.saves)
}

type appBus struct {
	mu       sync.Mutex
	acquire  bool
	released int
	lost     chan struct{}
}

func (b *appBus) Acquire(context.Context) (bool, error) { return b.acquire, nil }
func (b *appBus) Release() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.released++
	return nil
}
func (b *appBus) Lost() <-chan struct{} { return b.lost }
func (b *appBus) releases() int         { b.mu.Lock(); defer b.mu.Unlock(); return b.released }

type appLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *appLog) Write(p []byte) (int, error) { l.mu.Lock(); defer l.mu.Unlock(); return l.b.Write(p) }
func (l *appLog) lines() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Split(l.b.String(), "\n")
}

// ---------- harness ----------

var appStart = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

type appHarness struct {
	t      *testing.T
	clock  *appClock
	feed   *appFeed
	news   *appNews
	pkgs   *appPkgs
	notif  *appNotifier
	icon   *appIcon
	open   *appOpener
	net    *appNet
	store  *appStore
	bus    *appBus
	logs   *appLog
	cfg    config.TrayConfig
	rand   float64
	noFeed bool                // Deps.Feed = nil: the feed URL was refused (R2.9)
	netDep tray.NetworkMonitor // overrides h.net when set (netmon.Absent())
	cancel context.CancelFunc
	done   chan error
}

func newApp(t *testing.T) *appHarness {
	h := &appHarness{
		t:     t,
		clock: &appClock{now: appStart},
		feed:  &appFeed{},
		news:  &appNews{changed: make(chan struct{}, 4)},
		pkgs:  &appPkgs{},
		notif: &appNotifier{events: make(chan notify.Event, 8)},
		icon:  &appIcon{events: make(chan sni.Event, 8)},
		open:  &appOpener{},
		net:   &appNet{v: netmon.Verdict{Online: true}, changed: make(chan struct{}, 4)},
		store: &appStore{initial: state.State{Format: 1, Saved: true, Notices: map[string]state.Record{}}},
		bus:   &appBus{acquire: true, lost: make(chan struct{})},
		logs:  &appLog{},
		rand:  0.5,
		done:  make(chan error, 1),
	}
	h.feed.next = func(int) (feed.Result, error) { return okResult(1790812800), nil }
	return h
}

func (h *appHarness) start() {
	h.t.Helper()
	var fetcher tray.FeedFetcher = h.feed
	if h.noFeed {
		fetcher = nil
	}
	var network tray.NetworkMonitor = h.net
	if h.netDep != nil {
		network = h.netDep
	}
	deps := tray.Deps{
		Log:    slog.New(slog.NewTextHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Clock:  h.clock,
		Rand:   func() float64 { return h.rand },
		Config: h.cfg,
		Feed:   fetcher,
		News:   h.news,
		Pkgs:   h.pkgs,
		Notify: h.notif,
		Icon:   h.icon,
		Open:   h.open,
		Net:    network,
		Store:  h.store,
		Bus:    h.bus,
	}
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

func (h *appHarness) waitFor(what string, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			h.t.Fatalf("timed out waiting for %s (pending timers %v)", what, h.clock.pending())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (h *appHarness) never(what string, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		if cond() {
			h.t.Fatalf("unexpectedly: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (h *appHarness) menu(id int32)             { h.icon.events <- sni.Event{ItemID: id} }
func (h *appHarness) fetches(n int) func() bool { return func() bool { return h.feed.count() >= n } }

// checkNow triggers Check now and waits for the fetch it causes.
func (h *appHarness) checkNow() {
	h.t.Helper()
	before := h.feed.count()
	h.menu(200)
	h.waitFor("the Check now fetch", h.fetches(before+1))
}

func (h *appHarness) savedWhere(what string, cond func(state.State) bool) state.State {
	h.t.Helper()
	var st state.State
	h.waitFor(what, func() bool {
		var n int
		st, n = h.store.last()
		return n > 0 && cond(st)
	})
	return st
}

func (h *appHarness) viewWhere(what string, cond func(sni.View) bool) sni.View {
	h.t.Helper()
	var v sni.View
	h.waitFor(what, func() bool {
		var ok bool
		v, ok = h.icon.last()
		return ok && cond(v)
	})
	return v
}

func (h *appHarness) logLine(cond func(string) bool) bool {
	for _, l := range h.logs.lines() {
		if cond(l) {
			return true
		}
	}
	return false
}

func okResult(serial int64, items ...notices.Notice) feed.Result {
	return feed.Result{
		Status: 200, ETag: `"e1"`, Bytes: 321,
		Feed: notices.Feed{Serial: serial, Expires: appStart.Add(30 * 24 * time.Hour), Items: items},
	}
}

func fooCVE() notices.Notice {
	return notices.Notice{
		ID: "2026-10-02-foo-cve", Title: "foo heap overflow", Summary: "Upgrade foo.",
		URL: "https://obentoo.org/notices/2026-10-02-foo-cve/", Type: "security", Severity: "critical",
		Affects:   []notices.Affects{{CP: "dev-libs/foo", Ranges: []notices.Range{{Op: "<", Ver: "1.2.3"}}}},
		Source:    notices.SourceFeed,
		Published: appStart.Add(-time.Hour), Updated: appStart.Add(-time.Hour),
	}
}

func releaseNotice(id, title string) notices.Notice {
	return notices.Notice{
		ID: id, Title: title, Summary: title + " summary", URL: "https://obentoo.org/notices/" + id + "/",
		Type: "release", Severity: "warning", Source: notices.SourceFeed,
		Published: appStart.Add(-time.Hour), Updated: appStart.Add(-time.Hour),
	}
}

var bentooFoo = pkgdb.Package{Category: "dev-libs", Name: "foo", Version: "1.1", Slot: "0", Repo: "bentoo"}

// withCriticalNotice wires a feed carrying fooCVE and an installed bentoo foo,
// starts the App, runs Check now and waits for the notification.
func withCriticalNotice(t *testing.T) *appHarness {
	h := newApp(t)
	h.pkgs.list = []pkgdb.Package{bentooFoo}
	h.feed.next = func(int) (feed.Result, error) { return okResult(1790812800, fooCVE()), nil }
	h.start()
	h.checkNow()
	h.waitFor("the critical notification", func() bool { return len(h.notif.messages()) == 1 })
	return h
}

// ---------- lifecycle ----------

// TestApp_AlreadyRunningReturnsErrAlreadyRunning is R1.2 at the loop.
func TestApp_AlreadyRunningReturnsErrAlreadyRunning(t *testing.T) {
	h := newApp(t)
	h.bus.acquire = false
	h.start()
	select {
	case err := <-h.done:
		if !errors.Is(err, tray.ErrAlreadyRunning) {
			t.Errorf("Run = %v, want ErrAlreadyRunning", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run kept running although another instance owns the name")
	}
	if h.feed.count() != 0 {
		t.Error("a second instance fetched the feed")
	}
}

// TestApp_CancelSavesAndReleasesWithinFiveSeconds is R1.4 for a signal.
func TestApp_CancelSavesAndReleasesWithinFiveSeconds(t *testing.T) {
	h := newApp(t)
	h.start()
	h.waitFor("the startup timer", func() bool { return len(h.clock.pending()) > 0 })
	_, savesBefore := h.store.last()
	h.cancel()
	select {
	case err := <-h.done:
		if err != nil {
			t.Errorf("Run after cancellation = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return within 5s of cancellation")
	}
	if _, n := h.store.last(); n <= savesBefore {
		t.Error("state was not saved on stop")
	}
	if h.bus.releases() == 0 {
		t.Error("the bus name was not released on stop")
	}
}

// TestApp_BusLostReturnsErrBusLost is R1.5.
func TestApp_BusLostReturnsErrBusLost(t *testing.T) {
	h := newApp(t)
	h.start()
	h.waitFor("the startup timer", func() bool { return len(h.clock.pending()) > 0 })
	_, savesBefore := h.store.last()
	close(h.bus.lost)
	select {
	case err := <-h.done:
		if !errors.Is(err, tray.ErrBusLost) {
			t.Errorf("Run = %v, want ErrBusLost", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run kept running after the bus was lost")
	}
	if _, n := h.store.last(); n <= savesBefore {
		t.Error("state was not saved when the bus was lost")
	}
}

// TestApp_MenuQuitStopsCleanly is R9.5/R1.4 for Quit (300).
func TestApp_MenuQuitStopsCleanly(t *testing.T) {
	h := newApp(t)
	h.start()
	h.menu(300)
	select {
	case err := <-h.done:
		if err != nil {
			t.Errorf("Run after Quit = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Quit did not stop Run within 5s")
	}
	if _, n := h.store.last(); n == 0 {
		t.Error("state was not saved on Quit")
	}
	if h.bus.releases() == 0 {
		t.Error("the bus name was not released on Quit")
	}
}

// ---------- scheduling ----------

// TestApp_FirstFetchWaitsForTheStartupDelay is R2.1 wired: Rand 0.5 means a
// 3-minute delay, and nothing is fetched a second earlier.
func TestApp_FirstFetchWaitsForTheStartupDelay(t *testing.T) {
	h := newApp(t)
	h.start()
	h.waitFor("a 3m startup timer", func() bool { return h.clock.hasPending(3 * time.Minute) })
	h.clock.Advance(3*time.Minute - time.Second)
	h.never("a fetch before the startup delay", h.fetches(1))
	h.clock.Advance(time.Second)
	h.waitFor("the first fetch", h.fetches(1))
}

// TestApp_SuccessSchedulesTheJitteredIntervalAndRevalidates is R2.2 and R2.3
// wired: Rand 0 gives 0.8 x 6h, and the next fetch sends the stored ETag.
func TestApp_SuccessSchedulesTheJitteredIntervalAndRevalidates(t *testing.T) {
	h := newApp(t)
	h.rand = 0
	h.start()
	h.waitFor("a 1m startup timer", func() bool { return h.clock.hasPending(time.Minute) })
	h.clock.Advance(time.Minute)
	h.waitFor("the first fetch", h.fetches(1))
	next := 4*time.Hour + 48*time.Minute
	h.waitFor("the 4h48m interval timer", func() bool { return h.clock.hasPending(next) })
	h.clock.Advance(next)
	h.waitFor("the second fetch", h.fetches(2))
	h.feed.mu.Lock()
	etags := slices.Clone(h.feed.etags)
	h.feed.mu.Unlock()
	if etags[1] != `"e1"` {
		t.Errorf("second fetch sent ETag %q, want the stored \"e1\"", etags[1])
	}
}

// TestApp_FailuresBackOffAndCheckNowResets is R2.7 and R2.11.
func TestApp_FailuresBackOffAndCheckNowResets(t *testing.T) {
	h := newApp(t)
	h.feed.next = func(int) (feed.Result, error) { return feed.Result{}, errors.New("network down") }
	h.start()
	h.waitFor("the startup timer", func() bool { return h.clock.hasPending(3 * time.Minute) })
	h.clock.Advance(3 * time.Minute)
	h.waitFor("the first fetch", h.fetches(1))
	h.waitFor("a 5m backoff", func() bool { return h.clock.hasPending(5 * time.Minute) })
	h.clock.Advance(5 * time.Minute)
	h.waitFor("the second fetch", h.fetches(2))
	h.waitFor("a 10m backoff", func() bool { return h.clock.hasPending(10 * time.Minute) })
	h.savedWhere("Failures 2 saved", func(st state.State) bool { return st.Failures == 2 })
	if !h.logLine(func(l string) bool { return strings.Contains(l, "level=WARN") && strings.Contains(l, "network down") }) {
		t.Error("the failed fetch was not logged at WARN with its cause")
	}

	h.feed.set(func(int) (feed.Result, error) { return okResult(1790812800), nil })
	h.checkNow() // no clock advance: Check now fetches immediately
	h.savedWhere("Failures reset to 0", func(st state.State) bool { return st.Failures == 0 })
	h.waitFor("the 6h interval after success", func() bool { return h.clock.hasPending(6 * time.Hour) })
}

// TestApp_RetryAfterExtendsTheBackoff is R2.13 wired.
func TestApp_RetryAfterExtendsTheBackoff(t *testing.T) {
	h := newApp(t)
	h.feed.next = func(int) (feed.Result, error) {
		return feed.Result{}, &feed.StatusError{Status: 503, RetryAfter: 2 * time.Hour}
	}
	h.start()
	h.checkNow()
	h.waitFor("a 2h backoff honouring Retry-After", func() bool { return h.clock.hasPending(2 * time.Hour) })
	if !h.logLine(func(l string) bool { return strings.Contains(l, "level=WARN") && strings.Contains(l, "503") }) {
		t.Error("the 503 was not logged at WARN with its status")
	}
}

// TestApp_MeteredOrOfflineSkipsAndNetworkChangeRefetches is R3.1–R3.3.
func TestApp_MeteredOrOfflineSkipsAndNetworkChangeRefetches(t *testing.T) {
	for name, v := range map[string]netmon.Verdict{
		"metered": {Online: true, Metered: true},
		"offline": {Online: false},
	} {
		t.Run(name, func(t *testing.T) {
			h := newApp(t)
			h.net.set(v)
			h.start()
			h.waitFor("the startup timer", func() bool { return h.clock.hasPending(3 * time.Minute) })
			h.clock.Advance(3 * time.Minute)
			h.never("a scheduled fetch on a "+name+" network", h.fetches(1))
			if !h.logLine(func(l string) bool {
				return strings.Contains(l, "level=INFO") && strings.Contains(strings.ToLower(l), "skip")
			}) {
				t.Error("the skipped fetch was not logged at INFO")
			}
			h.net.set(netmon.Verdict{Online: true})
			h.net.changed <- struct{}{}
			time.Sleep(100 * time.Millisecond)
			h.clock.Advance(time.Minute)
			h.waitFor("a fetch within 1 minute of the network becoming usable", h.fetches(1))
		})
	}
}

// TestApp_NetworkChecksThatDoNotBlockFetching: skip_metered false fetches on a
// metered network, NetworkManager absent fetches on schedule (R3.4), and Check
// now fetches on a metered network regardless (R3.5).
func TestApp_NetworkChecksThatDoNotBlockFetching(t *testing.T) {
	no := false
	cases := map[string]struct {
		cfg config.TrayConfig
		v   netmon.Verdict
	}{
		"skip_metered false": {config.TrayConfig{SkipMetered: &no}, netmon.Verdict{Online: true, Metered: true}},
		"no NetworkManager":  {config.TrayConfig{}, netmon.Verdict{NMAbsent: true}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			h := newApp(t)
			h.cfg = c.cfg
			h.net.set(c.v)
			h.start()
			h.waitFor("the startup timer", func() bool { return h.clock.hasPending(3 * time.Minute) })
			h.clock.Advance(3 * time.Minute)
			h.waitFor("the scheduled fetch", h.fetches(1))
		})
	}
	t.Run("check now on a metered network", func(t *testing.T) {
		h := newApp(t)
		h.net.set(netmon.Verdict{Online: true, Metered: true})
		h.start()
		h.checkNow()
	})
}

// ---------- notices ----------

// TestApp_ApplicableNoticeIsNotifiedShownAndLogged is the success-metric path:
// a critical security notice for an installed bentoo package produces a
// critical notification and a red icon, and both events are logged (R13.3).
func TestApp_ApplicableNoticeIsNotifiedShownAndLogged(t *testing.T) {
	h := withCriticalNotice(t)
	m := h.notif.messages()[0]
	if m.NoticeID != fooCVE().ID || m.Urgency != 2 || m.Summary != fooCVE().Title {
		t.Errorf("notification = %+v, want the critical notice at urgency 2", m)
	}
	h.viewWhere("a critical unread view", func(v sni.View) bool { return v.Unread == 1 && v.Critical })
	st := h.savedWhere("the notice recorded as notified", func(st state.State) bool { return st.Notices[fooCVE().ID].Notified })
	if st.Serial != 1790812800 || st.ETag != `"e1"` {
		t.Errorf("saved serial/etag = %d/%q, want the accepted feed's", st.Serial, st.ETag)
	}
	if !h.logLine(func(l string) bool {
		return strings.Contains(l, "status=200") && strings.Contains(l, "bytes=") && strings.Contains(l, "duration=") && strings.Contains(l, "outcome=")
	}) {
		t.Errorf("no fetch log line with status, bytes, duration and outcome:\n%s", strings.Join(h.logs.lines(), "\n"))
	}
	if !h.logLine(func(l string) bool { return strings.Contains(l, fooCVE().ID) && strings.Contains(l, "urgency=") }) {
		t.Error("no notification log line with the notice ID and urgency")
	}
}

// TestApp_NoticeForAnotherRepositoryIsNotNotified is the hostile half of the
// same path: the package is installed, but from gentoo.
func TestApp_NoticeForAnotherRepositoryIsNotNotified(t *testing.T) {
	h := newApp(t)
	gentooFoo := bentooFoo
	gentooFoo.Repo = "gentoo"
	h.pkgs.list = []pkgdb.Package{gentooFoo}
	h.feed.next = func(int) (feed.Result, error) { return okResult(1790812800, fooCVE()), nil }
	h.start()
	h.checkNow()
	h.never("a notification for a package from another repository", func() bool { return h.notif.attempts() > 0 })
	if v, ok := h.icon.last(); ok && v.Unread != 0 {
		t.Errorf("view = %+v, want nothing unread", v)
	}
}

// TestApp_RejectedFeedKeepsPreviousNotices is R2.4 wired: a lower serial is
// rejected with a WARN naming both serials and nothing is notified.
func TestApp_RejectedFeedKeepsPreviousNotices(t *testing.T) {
	h := newApp(t)
	h.store.initial.Serial = 1790812800
	h.pkgs.list = []pkgdb.Package{bentooFoo}
	h.feed.next = func(int) (feed.Result, error) { return okResult(1790000000, fooCVE()), nil }
	h.start()
	h.checkNow()
	h.never("a notification from a rolled-back feed", func() bool { return h.notif.attempts() > 0 })
	if !h.logLine(func(l string) bool {
		return strings.Contains(l, "level=WARN") && strings.Contains(l, "1790000000") && strings.Contains(l, "1790812800")
	}) {
		t.Error("the rollback was not logged at WARN with both serials")
	}
}

// TestApp_FailedNotificationIsRetriedAtTheNextCheck is R6.12 wired.
func TestApp_FailedNotificationIsRetriedAtTheNextCheck(t *testing.T) {
	h := newApp(t)
	h.pkgs.list = []pkgdb.Package{bentooFoo}
	h.feed.next = func(int) (feed.Result, error) { return okResult(1790812800, fooCVE()), nil }
	h.notif.setErr(errors.New("org.freedesktop.Notifications not provided"))
	h.start()
	h.checkNow()
	h.waitFor("a notification attempt", func() bool { return h.notif.attempts() >= 1 })
	h.notif.setErr(nil)
	h.checkNow()
	h.waitFor("the retried notification", func() bool { return len(h.notif.messages()) == 1 })
}

// TestApp_NewsChangeReadsTheUnreadList is R4.1 wired: a change re-reads the
// list and a new news item is notified.
func TestApp_NewsChangeReadsTheUnreadList(t *testing.T) {
	h := newApp(t)
	h.start()
	h.waitFor("the startup timer", func() bool { return len(h.clock.pending()) > 0 })
	reads := h.news.readCount()
	h.news.mu.Lock()
	h.news.items = []notices.Notice{{ID: "2026-10-02-nodejs-slotted", Title: "Node.js is now slotted", Type: "news", Severity: "info", Source: notices.SourceNews, URL: "https://obentoo.org/notices/2026-10-02-nodejs-slotted/"}}
	h.news.mu.Unlock()
	h.news.changed <- struct{}{}
	h.waitFor("a re-read of the unread list", func() bool { return h.news.readCount() > reads })
	h.waitFor("the news notification", func() bool {
		for _, m := range h.notif.messages() {
			if m.NoticeID == "2026-10-02-nodejs-slotted" {
				return true
			}
		}
		return false
	})
}

// ---------- actions ----------

// TestApp_OpenActionOpensWithTokenThenMarksRead is R7.1 and R7.4 wired.
func TestApp_OpenActionOpensWithTokenThenMarksRead(t *testing.T) {
	h := withCriticalNotice(t)
	h.notif.events <- notify.Event{NoticeID: fooCVE().ID, Action: "default", ActivationToken: "tok-1"}
	h.waitFor("the opener call", func() bool { return len(h.open.opened()) == 1 })
	if c := h.open.opened()[0]; c.URL != fooCVE().URL || c.Token != "tok-1" {
		t.Errorf("Open(%q, %q), want (%q, tok-1)", c.URL, c.Token, fooCVE().URL)
	}
	h.savedWhere("the opened notice saved as read", func(st state.State) bool { return st.Notices[fooCVE().ID].Read })
	h.viewWhere("nothing unread", func(v sni.View) bool { return v.Unread == 0 && !v.Critical })
}

// TestApp_MarkAsReadActionDoesNotOpen: the second notification action.
func TestApp_MarkAsReadActionDoesNotOpen(t *testing.T) {
	h := withCriticalNotice(t)
	h.notif.events <- notify.Event{NoticeID: fooCVE().ID, Action: "mark-read"}
	h.savedWhere("the notice saved as read", func(st state.State) bool { return st.Notices[fooCVE().ID].Read })
	if c := h.open.opened(); len(c) != 0 {
		t.Errorf("Mark as read opened %+v", c)
	}
}

// TestApp_RefusedOpenIsLoggedWithTheNoticeID is R7.3's log.
func TestApp_RefusedOpenIsLoggedWithTheNoticeID(t *testing.T) {
	h := withCriticalNotice(t)
	h.open.mu.Lock()
	h.open.err = errors.New("url refused")
	h.open.mu.Unlock()
	h.notif.events <- notify.Event{NoticeID: fooCVE().ID, Action: "default"}
	h.waitFor("a WARN naming the notice", func() bool {
		return h.logLine(func(l string) bool { return strings.Contains(l, "level=WARN") && strings.Contains(l, fooCVE().ID) })
	})
}

// TestApp_MenuNoticeEntryOpensIt is R9.1's "each opening its notice".
func TestApp_MenuNoticeEntryOpensIt(t *testing.T) {
	h := withCriticalNotice(t)
	h.viewWhere("the notice listed in the menu", func(v sni.View) bool {
		return len(v.Entries) == 1 && v.Entries[0].NoticeID == fooCVE().ID
	})
	h.icon.events <- sni.Event{ItemID: 1000, NoticeID: fooCVE().ID}
	h.waitFor("the opener call", func() bool { return len(h.open.opened()) == 1 })
	if c := h.open.opened()[0]; c.URL != fooCVE().URL {
		t.Errorf("menu entry 1000 opened %q, want %q", c.URL, fooCVE().URL)
	}
}

// TestApp_MenuMarkAllAsRead is R9.2.
func TestApp_MenuMarkAllAsRead(t *testing.T) {
	h := withCriticalNotice(t)
	h.menu(201)
	h.viewWhere("nothing unread", func(v sni.View) bool { return v.Unread == 0 })
	h.savedWhere("every notice saved as read", func(st state.State) bool {
		for _, r := range st.Notices {
			if !r.Read {
				return false
			}
		}
		return len(st.Notices) > 0
	})
}

// TestApp_MenuPauseForOneHourHoldsThenReleases is R9.3 (202), R6.10 and
// R6.11 wired: a notice arriving during the pause is held and sent when the
// pause ends.
func TestApp_MenuPauseForOneHourHoldsThenReleases(t *testing.T) {
	h := newApp(t)
	h.start()
	h.menu(202)
	h.savedWhere("PauseUntil = now + 1h", func(st state.State) bool { return st.PauseUntil.Equal(appStart.Add(time.Hour)) })
	h.viewWhere("a paused view", func(v sni.View) bool { return v.Paused })

	held := releaseNotice("2026-10-02-bentoolkit-0-32", "bentoolkit released")
	h.feed.set(func(int) (feed.Result, error) { return okResult(1790812800, held), nil })
	h.checkNow()
	h.never("a notification during the pause", func() bool { return len(h.notif.messages()) > 0 })

	h.clock.Advance(time.Hour)
	h.waitFor("the held notice sent at the end of the pause", func() bool { return len(h.notif.messages()) == 1 })
	h.viewWhere("the pause ended in the view", func(v sni.View) bool { return !v.Paused })
}

// TestApp_MenuPauseUntilTomorrowIsTheNextLocal0800 is R9.3 (203), including
// the hostile hour: before 08:00 the next 08:00 is today.
func TestApp_MenuPauseUntilTomorrowIsTheNextLocal0800(t *testing.T) {
	cases := map[string]struct{ now, want time.Time }{
		"evening":       {time.Date(2026, 10, 1, 20, 0, 0, 0, time.Local), time.Date(2026, 10, 2, 8, 0, 0, 0, time.Local)},
		"early morning": {time.Date(2026, 10, 1, 7, 0, 0, 0, time.Local), time.Date(2026, 10, 1, 8, 0, 0, 0, time.Local)},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			h := newApp(t)
			h.clock.now = c.now
			h.start()
			h.menu(203)
			h.savedWhere("PauseUntil = the next local 08:00", func(st state.State) bool { return st.PauseUntil.Equal(c.want) })
		})
	}
}

// TestApp_MenuResumeEndsThePause is R9.4 (204).
func TestApp_MenuResumeEndsThePause(t *testing.T) {
	h := newApp(t)
	h.start()
	h.menu(202)
	h.viewWhere("a paused view", func(v sni.View) bool { return v.Paused })
	h.menu(204)
	h.viewWhere("an unpaused view", func(v sni.View) bool { return !v.Paused })
	h.savedWhere("the pause cleared", func(st state.State) bool { return !st.PauseUntil.After(appStart) })
}

// ---------- cross-artifact review additions ----------

func announcement() notices.Notice {
	return notices.Notice{
		ID: "2026-10-02-hello", Title: "Hello everyone", Summary: "An announcement.",
		URL: "https://obentoo.org/notices/2026-10-02-hello/", Type: "announcement", Severity: "info",
		Source: notices.SourceFeed, Published: appStart.Add(-time.Hour), Updated: appStart.Add(-time.Hour),
	}
}

func sentIDs(h *appHarness) []string {
	var ids []string
	for _, m := range h.notif.messages() {
		ids = append(ids, m.NoticeID)
	}
	return ids
}

func warned(h *appHarness, words ...string) bool {
	return h.logLine(func(l string) bool {
		if !strings.Contains(l, "level=WARN") {
			return false
		}
		for _, w := range words {
			if !strings.Contains(l, w) {
				return false
			}
		}
		return true
	})
}

// TestApp_FirstRunEndsAfterTheFirstSave: with no state file the first check
// is a first run (a release is recorded read, not notified); after the first
// successful save it no longer is, so the next new release is notified.
func TestApp_FirstRunEndsAfterTheFirstSave(t *testing.T) {
	h := newApp(t)
	h.store.initial = state.State{} // no state file: Saved == false
	first := releaseNotice("2026-10-02-first", "First release")
	h.feed.next = func(int) (feed.Result, error) { return okResult(1790812800, first), nil }
	h.start()
	h.checkNow()
	h.savedWhere("the first save", func(st state.State) bool { return st.Notices[first.ID].Read })
	h.never("a first-run notification for a release", func() bool { return h.notif.attempts() > 0 })

	second := releaseNotice("2026-10-03-second", "Second release")
	h.feed.set(func(int) (feed.Result, error) { return okResult(1790812900, first, second), nil })
	h.checkNow()
	h.waitFor("the second release notified", func() bool { return slices.Contains(sentIDs(h), second.ID) })
	if slices.Contains(sentIDs(h), first.ID) {
		t.Error("the release recorded read on the first run was notified later")
	}
}

// TestApp_NilFeedRunsOnNewsAlone: a refused feed URL leaves Deps.Feed nil; the
// loop keeps running on the news source and Check now does not crash it.
func TestApp_NilFeedRunsOnNewsAlone(t *testing.T) {
	h := newApp(t)
	h.noFeed = true
	h.start()
	h.news.mu.Lock()
	h.news.items = []notices.Notice{{ID: "2026-10-02-news-only", Title: "News only", Type: "news", Severity: "info",
		Source: notices.SourceNews, URL: "https://obentoo.org/notices/2026-10-02-news-only/"}}
	h.news.mu.Unlock()
	h.news.changed <- struct{}{}
	h.waitFor("the news item notified without a feed", func() bool { return slices.Contains(sentIDs(h), "2026-10-02-news-only") })
	h.menu(200)
	h.clock.Advance(10 * time.Minute)
	h.never("Run returning with a nil feed", func() bool { return len(h.done) > 0 })
}

// TestApp_InstalledErrorAppliesOnlyNoAffectsNotices is R5.5: an unreadable
// package database means only notices without affects apply, with a WARN.
func TestApp_InstalledErrorAppliesOnlyNoAffectsNotices(t *testing.T) {
	h := newApp(t)
	h.pkgs.list = []pkgdb.Package{bentooFoo}
	h.pkgs.err = errors.New("open /var/db/pkg: permission denied")
	h.feed.next = func(int) (feed.Result, error) { return okResult(1790812800, fooCVE(), announcement()), nil }
	h.start()
	h.checkNow()
	h.waitFor("the no-affects notice notified", func() bool { return slices.Contains(sentIDs(h), announcement().ID) })
	h.never("a notice with affects applied without a package list", func() bool { return slices.Contains(sentIDs(h), fooCVE().ID) })
	if !warned(h, "/var/db/pkg") {
		t.Error("the unreadable package database was not logged at WARN naming it")
	}
}

// TestApp_SkippedPackagesAreWarnedWithTheirCount: skipped > 0 is a WARN with
// the count, and the readable packages still match.
func TestApp_SkippedPackagesAreWarnedWithTheirCount(t *testing.T) {
	h := newApp(t)
	h.pkgs.list = []pkgdb.Package{bentooFoo}
	h.pkgs.skipped = 7
	h.feed.next = func(int) (feed.Result, error) { return okResult(1790812800, fooCVE()), nil }
	h.start()
	h.checkNow()
	h.waitFor("the critical notice notified", func() bool { return slices.Contains(sentIDs(h), fooCVE().ID) })
	if !warned(h, "7") {
		t.Error("skipped package entries were not logged at WARN with their count")
	}
}

// TestApp_PartialNewsIsKept is R4.4 for a partial read: items returned with an
// error are kept, and the error is a WARN.
func TestApp_PartialNewsIsKept(t *testing.T) {
	h := newApp(t)
	h.start()
	h.waitFor("the startup timer", func() bool { return len(h.clock.pending()) > 0 })
	h.news.mu.Lock()
	h.news.items = []notices.Notice{{ID: "2026-10-02-partial", Title: "Partial", Type: "news", Severity: "info",
		Source: notices.SourceNews, URL: "https://obentoo.org/notices/2026-10-02-partial/"}}
	h.news.err = errors.New("reading /var/db/repos/bentoo/metadata/news/2026-10-02-gone: no such file")
	h.news.mu.Unlock()
	h.news.changed <- struct{}{}
	h.waitFor("the readable news item notified", func() bool { return slices.Contains(sentIDs(h), "2026-10-02-partial") })
	if !warned(h, "2026-10-02-gone") {
		t.Error("the partial news read was not logged at WARN")
	}
}

// TestApp_NewsFailureFallsBackToTheFeed is R4.4: no news at all still lets
// the feed through.
func TestApp_NewsFailureFallsBackToTheFeed(t *testing.T) {
	h := newApp(t)
	h.news.err = errors.New("open /var/lib/gentoo/news/news-bentoo.unread: permission denied")
	h.pkgs.list = []pkgdb.Package{bentooFoo}
	h.feed.next = func(int) (feed.Result, error) { return okResult(1790812800, fooCVE()), nil }
	h.start()
	h.checkNow()
	h.waitFor("the feed notice notified", func() bool { return slices.Contains(sentIDs(h), fooCVE().ID) })
	if !warned(h, "news-bentoo.unread") {
		t.Error("the unreadable news list was not logged at WARN naming its path")
	}
}

// TestApp_SummaryAndMoreOpenTheNoticeIndex: the R6.8 summary notification
// (empty NoticeID) and menu item 100 open the notices index on the feed host.
func TestApp_SummaryAndMoreOpenTheNoticeIndex(t *testing.T) {
	h := newApp(t)
	h.start()
	h.notif.events <- notify.Event{NoticeID: "", Action: "default", ActivationToken: "tok-s"}
	h.waitFor("the summary opening the index", func() bool { return len(h.open.opened()) == 1 })
	if c := h.open.opened()[0]; c.URL != "https://obentoo.org/notices/" || c.Token != "tok-s" {
		t.Errorf("summary opened (%q, %q), want (https://obentoo.org/notices/, tok-s)", c.URL, c.Token)
	}
	h.menu(100)
	h.waitFor("menu 100 opening the index", func() bool { return len(h.open.opened()) == 2 })
	if c := h.open.opened()[1]; c.URL != "https://obentoo.org/notices/" {
		t.Errorf("menu 100 opened %q, want https://obentoo.org/notices/", c.URL)
	}
}

// TestApp_NetworkCheckErrorStillFetches: an error from NetworkManager is a
// WARN, not a skipped fetch.
func TestApp_NetworkCheckErrorStillFetches(t *testing.T) {
	h := newApp(t)
	h.net.err = errors.New("org.freedesktop.NetworkManager: timeout")
	h.start()
	h.waitFor("the startup timer", func() bool { return h.clock.hasPending(3 * time.Minute) })
	h.clock.Advance(3 * time.Minute)
	h.waitFor("the scheduled fetch despite the network error", h.fetches(1))
	if !warned(h, "NetworkManager") {
		t.Error("the network check error was not logged at WARN")
	}
}

// TestApp_IconStartErrorKeepsNotifying: a tray icon that cannot start leaves
// notifications working (a WARN, not a stop).
func TestApp_IconStartErrorKeepsNotifying(t *testing.T) {
	h := newApp(t)
	h.icon.startErr = errors.New("exporting /StatusNotifierItem: denied")
	h.pkgs.list = []pkgdb.Package{bentooFoo}
	h.feed.next = func(int) (feed.Result, error) { return okResult(1790812800, fooCVE()), nil }
	h.start()
	h.waitFor("the startup timer", func() bool { return h.clock.hasPending(3 * time.Minute) })
	h.clock.Advance(3 * time.Minute)
	h.waitFor("the notification without an icon", func() bool { return slices.Contains(sentIDs(h), fooCVE().ID) })
	if !warned(h, "StatusNotifierItem") {
		t.Error("the icon start failure was not logged at WARN")
	}
	if len(h.done) > 0 {
		t.Error("Run stopped because the icon failed to start")
	}
}

// TestApp_SaveErrorIsRetriedAtTheNextChange: a failed save is a WARN, and the
// next change saves the whole state.
func TestApp_SaveErrorIsRetriedAtTheNextChange(t *testing.T) {
	h := withCriticalNotice(t)
	h.store.mu.Lock()
	h.store.saveErr = errors.New("writing state.json: no space left on device")
	before := h.store.attempts
	h.store.mu.Unlock()
	h.notif.events <- notify.Event{NoticeID: fooCVE().ID, Action: "mark-read"}
	h.waitFor("a failed save attempt", func() bool { h.store.mu.Lock(); defer h.store.mu.Unlock(); return h.store.attempts > before })
	h.waitFor("a WARN for the failed save", func() bool { return warned(h, "no space left") })
	h.store.mu.Lock()
	h.store.saveErr = nil
	h.store.mu.Unlock()
	h.menu(202) // the next change
	h.savedWhere("the retried save carrying the earlier change", func(st state.State) bool {
		return st.Notices[fooCVE().ID].Read && !st.PauseUntil.IsZero()
	})
}

// TestApp_AbsentNetworkMonitorLogsOnce is R3.4 wired: with netmon.Absent()
// every scheduled fetch happens and the INFO is logged once, not per check.
func TestApp_AbsentNetworkMonitorLogsOnce(t *testing.T) {
	h := newApp(t)
	h.netDep = netmon.Absent()
	h.rand = 0
	h.start()
	h.waitFor("the startup timer", func() bool { return h.clock.hasPending(time.Minute) })
	h.clock.Advance(time.Minute)
	h.waitFor("the first fetch", h.fetches(1))
	next := 4*time.Hour + 48*time.Minute
	h.waitFor("the interval timer", func() bool { return h.clock.hasPending(next) })
	h.clock.Advance(next)
	h.waitFor("the second fetch", h.fetches(2))
	n := 0
	for _, l := range h.logs.lines() {
		if strings.Contains(l, "level=INFO") && strings.Contains(l, "NetworkManager") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d INFO lines mention NetworkManager, want exactly 1", n)
	}
}
