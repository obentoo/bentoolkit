package tray_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/desktop/netmon"
	"github.com/obentoo/bentoolkit/internal/tray"
	"github.com/obentoo/bentoolkit/internal/tray/state"
)

// The tests in this file drive Run through its name request with a bus whose
// Acquire blocks until the test answers for the bus. Like godbus, the gated
// Acquire returns the context's error when its context is done first, so a
// stop signal that reaches the request abandons it. Nothing here sleeps: the
// test learns that Acquire is blocked from the context it hands over.

const s083BusName = "org.obentoo.BentooTray"

type s083Answer struct {
	acquired bool
	err      error
}

// s083Bus is appBus with a gated Acquire. entered receives the context of
// every Acquire call; answer is what the bus replies.
type s083Bus struct {
	*appBus
	entered chan context.Context
	answer  chan s083Answer
}

func newS083Bus() *s083Bus {
	return &s083Bus{
		appBus:  &appBus{lost: make(chan struct{})},
		entered: make(chan context.Context, 1),
		answer:  make(chan s083Answer, 1),
	}
}

func (b *s083Bus) Acquire(ctx context.Context) (bool, error) {
	b.entered <- ctx
	select {
	case a := <-b.answer:
		// A reply racing a done context loses, as with godbus: the caller
		// that cancelled no longer waits for it.
		if err := ctx.Err(); err != nil {
			return false, fmt.Errorf("requesting bus name %s: %w", s083BusName, err)
		}
		return a.acquired, a.err
	case <-ctx.Done():
		return false, fmt.Errorf("requesting bus name %s: %w", s083BusName, ctx.Err())
	}
}

// s083Icon counts Start calls; the rest is appIcon.
type s083Icon struct {
	*appIcon
	smu    sync.Mutex
	starts int
}

func (i *s083Icon) Start(ctx context.Context) error {
	i.smu.Lock()
	i.starts++
	i.smu.Unlock()
	return i.appIcon.Start(ctx)
}

func (i *s083Icon) started() int { i.smu.Lock(); defer i.smu.Unlock(); return i.starts }

// s083Store is the real state.Store, with its saves counted.
type s083Store struct {
	inner *state.Store
	mu    sync.Mutex
	saves int
}

func (s *s083Store) Load() (state.State, error) { return s.inner.Load() }
func (s *s083Store) Save(st state.State) error {
	s.mu.Lock()
	s.saves++
	s.mu.Unlock()
	return s.inner.Save(st)
}
func (s *s083Store) saved() int { s.mu.Lock(); defer s.mu.Unlock(); return s.saves }

// s083Net records the deadline left on every Allowed call.
type s083Net struct {
	*appNet
	calls chan time.Duration
}

func (n *s083Net) Allowed(ctx context.Context) (netmon.Verdict, error) {
	left := time.Duration(-1)
	if dl, ok := ctx.Deadline(); ok {
		left = time.Until(dl)
	}
	select {
	case n.calls <- left:
	default:
	}
	return n.appNet.Allowed(ctx)
}

// s083Logs captures every record the App logs, at every level.
type s083Logs struct {
	mu   sync.Mutex
	recs []slog.Record
}

type s083Handler struct{ l *s083Logs }

func (h s083Handler) Enabled(context.Context, slog.Level) bool { return true }
func (h s083Handler) Handle(_ context.Context, r slog.Record) error {
	h.l.mu.Lock()
	defer h.l.mu.Unlock()
	h.l.recs = append(h.l.recs, r.Clone())
	return nil
}
func (h s083Handler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h s083Handler) WithGroup(string) slog.Handler      { return h }

// count returns how many records at level carry msg.
func (l *s083Logs) count(level slog.Level, msg string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, r := range l.recs {
		if r.Level == level && r.Message == msg {
			n++
		}
	}
	return n
}

func (l *s083Logs) atLeast(level slog.Level) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, r := range l.recs {
		if r.Level >= level {
			out = append(out, r.Level.String()+" "+r.Message)
		}
	}
	return out
}

func (l *s083Logs) all() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var b strings.Builder
	for _, r := range l.recs {
		fmt.Fprintf(&b, "%s %s\n", r.Level, r.Message)
	}
	return b.String()
}

type s083Run struct {
	bus    *s083Bus
	icon   *s083Icon
	logs   *s083Logs
	cancel context.CancelFunc
	done   chan error
}

// s083Start runs an App over the appHarness fakes with the gated bus, a
// start-counting icon and a capturing logger; store and net replace the
// harness's when not nil.
func s083Start(t *testing.T, h *appHarness, store tray.StateStore, net tray.NetworkMonitor) *s083Run {
	t.Helper()
	r := &s083Run{
		bus:  newS083Bus(),
		icon: &s083Icon{appIcon: h.icon},
		logs: &s083Logs{},
		done: make(chan error, 1),
	}
	if store == nil {
		store = h.store
	}
	if net == nil {
		net = h.net
	}
	deps := tray.Deps{
		Log:    slog.New(s083Handler{r.logs}),
		Clock:  h.clock,
		Rand:   func() float64 { return h.rand },
		Config: h.cfg,
		Feed:   h.feed,
		News:   h.news,
		Pkgs:   h.pkgs,
		Notify: h.notif,
		Icon:   r.icon,
		Open:   h.open,
		Net:    net,
		Store:  store,
		Bus:    r.bus,
	}
	ctx, cancel := context.WithCancel(t.Context())
	r.cancel = cancel
	go func() { r.done <- tray.New(deps).Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case r.bus.answer <- s083Answer{acquired: true}:
		default:
		}
		select {
		case <-r.done:
		case <-time.After(10 * time.Second):
			t.Error("Run did not return at cleanup")
		}
	})
	return r
}

// requested waits for the gated Acquire to be entered and returns its context.
func (r *s083Run) requested(t *testing.T) context.Context {
	t.Helper()
	select {
	case ctx := <-r.bus.entered:
		return ctx
	case err := <-r.done:
		t.Fatalf("Run returned %v before requesting the bus name", err)
	case <-time.After(5 * time.Second):
		t.Fatal("Run never requested the bus name")
	}
	return nil
}

// result waits for Run to return, failing after within.
func (r *s083Run) result(t *testing.T, within time.Duration) error {
	t.Helper()
	select {
	case err := <-r.done:
		r.done <- err // the cleanup reads it again
		return err
	case <-time.After(within):
		t.Fatalf("Run did not return within %v; log:\n%s", within, r.logs.all())
	}
	return nil
}

// s083SeedState writes a non-empty saved state at mode 0644 and returns its
// path: Save writes 0600, so a 0600 file afterwards proves it was rewritten,
// and its content proves what was written.
func s083SeedState(t *testing.T) (string, state.State) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bentoo-notices", "state.json")
	seed := state.State{
		ETag:        `"s083"`,
		Serial:      1790812800,
		Established: true,
		Failures:    2,
		Notices: map[string]state.Record{
			"2026-10-02-foo-cve": {
				Title: "foo heap overflow", Summary: "Upgrade foo.", URL: "https://obentoo.org/notices/2026-10-02-foo-cve/",
				Severity: "critical", Type: "security", Source: "feed", Read: true, Notified: true,
				LastSeen: appStart.Add(-time.Hour), Updated: appStart.Add(-2 * time.Hour),
			},
		},
	}
	if err := state.Open(path).Save(seed); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	return path, seed
}

// TestS083App_SignalDuringNameRequestThenGrantedStopsCleanly is R1.1 and
// R1.3: a stop signal while the name request is in flight, then the bus
// grants the name. Run waits for the answer, loads and saves the real state,
// releases the name once, logs the stop and never starts the icon.
func TestS083App_SignalDuringNameRequestThenGrantedStopsCleanly(t *testing.T) {
	h := newApp(t)
	path, seed := s083SeedState(t)
	store := &s083Store{inner: state.Open(path)}
	r := s083Start(t, h, store, nil)

	r.requested(t)
	r.cancel()
	r.bus.answer <- s083Answer{acquired: true}

	if err := r.result(t, 5*time.Second); err != nil {
		t.Fatalf("Run = %v, want nil: a stop signal during the name request is a stop; log:\n%s", err, r.logs.all())
	}
	if n := r.bus.releases(); n != 1 {
		t.Errorf("the bus name was released %d times, want exactly 1", n)
	}
	if n := r.icon.started(); n != 0 {
		t.Errorf("the tray icon was started %d times, want 0 after a stop signal", n)
	}
	if store.saved() == 0 {
		t.Error("the state was not saved on the stop")
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("state file mode = %v, want 0600 (the stop must rewrite it)", fi.Mode().Perm())
	}
	got, err := state.Open(path).Load()
	if err != nil {
		t.Fatalf("reloading the saved state: %v", err)
	}
	if got.ETag != seed.ETag || got.Serial != seed.Serial || got.Failures != seed.Failures || !got.Established {
		t.Errorf("saved state = etag %q serial %d failures %d established %v, want the loaded %q %d %d true",
			got.ETag, got.Serial, got.Failures, got.Established, seed.ETag, seed.Serial, seed.Failures)
	}
	rec, ok := got.Notices["2026-10-02-foo-cve"]
	if !ok || rec.Title != "foo heap overflow" || !rec.Read || !rec.Notified {
		t.Errorf("saved notices = %v, want the loaded record kept (read, notified)", got.Notices)
	}
	if n := r.logs.count(slog.LevelInfo, "bentoo-tray stopped"); n != 1 {
		t.Errorf("INFO %q logged %d times, want 1; log:\n%s", "bentoo-tray stopped", n, r.logs.all())
	}
	if n := r.logs.count(slog.LevelInfo, "bentoo-tray started"); n != 0 {
		t.Errorf("the tray logged it started after a stop signal; log:\n%s", r.logs.all())
	}
	if errs := r.logs.atLeast(slog.LevelError); len(errs) > 0 {
		t.Errorf("ERROR records on a signalled stop: %v", errs)
	}
}

// TestS083App_NameRequestOutlivesTheSignalButIsBoundedByTwoSeconds is R1.1
// and R1.2: the name request carries a deadline of at most 2 s before any
// signal, and the run's cancellation does not reach it.
func TestS083App_NameRequestOutlivesTheSignalButIsBoundedByTwoSeconds(t *testing.T) {
	h := newApp(t)
	r := s083Start(t, h, nil, nil)

	ctx := r.requested(t)
	dl, ok := ctx.Deadline()
	if !ok {
		t.Fatal("the name request has no deadline")
	}
	if left := time.Until(dl); left > 2*time.Second || left <= 0 {
		t.Errorf("the name request's deadline is %v away, want within (0, 2s]", left)
	}
	r.cancel()
	if err := ctx.Err(); err != nil {
		t.Errorf("the stop signal reached the name request (ctx.Err() = %v); it must wait for the bus's answer", err)
	}
	if _, ok := ctx.Deadline(); !ok {
		t.Error("the name request lost its deadline")
	}
	r.bus.answer <- s083Answer{acquired: true}
	if err := r.result(t, 5*time.Second); err != nil {
		t.Errorf("Run = %v, want nil", err)
	}
}

// TestS083App_OtherBusCallsKeepTheFiveSecondCallTimeout is R4.5: the
// network check, a loop bus call, still gets the 5-second call timeout, not
// the name request's 2-second bound.
func TestS083App_OtherBusCallsKeepTheFiveSecondCallTimeout(t *testing.T) {
	h := newApp(t)
	net := &s083Net{appNet: h.net, calls: make(chan time.Duration, 1)}
	r := s083Start(t, h, nil, net)

	r.requested(t)
	r.bus.answer <- s083Answer{acquired: true}
	// The scheduled first check asks the network before fetching.
	h.waitFor("the startup timer", func() bool { return len(h.clock.pending()) > 0 })
	h.clock.Advance(24 * time.Hour)
	select {
	case left := <-net.calls:
		if left <= 3*time.Second || left > 5*time.Second {
			t.Errorf("the network check's deadline is %v away, want the 5s call timeout", left)
		}
	case err := <-r.done:
		t.Fatalf("Run returned %v before the network check", err)
	case <-time.After(5 * time.Second):
		t.Fatalf("no network check at the scheduled check; log:\n%s", r.logs.all())
	}
}

// TestS083App_SignalDuringNameRequestThenRefusedIsAlreadyRunning is R4.1
// under a signal: the bus answers that another instance owns the name, so Run
// returns ErrAlreadyRunning as it does without a signal, saves nothing and
// releases nothing.
func TestS083App_SignalDuringNameRequestThenRefusedIsAlreadyRunning(t *testing.T) {
	h := newApp(t)
	r := s083Start(t, h, nil, nil)

	r.requested(t)
	r.cancel()
	r.bus.answer <- s083Answer{acquired: false}

	err := r.result(t, 5*time.Second)
	if !errors.Is(err, tray.ErrAlreadyRunning) {
		t.Fatalf("Run = %v, want ErrAlreadyRunning", err)
	}
	if _, n := h.store.last(); n != 0 {
		t.Errorf("a second instance saved the state %d times, want 0", n)
	}
	if n := r.bus.releases(); n != 0 {
		t.Errorf("a second instance released the name %d times, want 0", n)
	}
	if n := r.icon.started(); n != 0 {
		t.Errorf("a second instance started the icon %d times, want 0", n)
	}
}

// TestS083App_HungNameRequestFailsWithDeadlineExceeded is R2.3 at the App: a
// bus that never answers the name request, with no signal, ends Run within
// the request's bound with an error wrapping context.DeadlineExceeded, not
// context.Canceled, naming the bus name.
func TestS083App_HungNameRequestFailsWithDeadlineExceeded(t *testing.T) {
	const bound = 100 * time.Millisecond
	t.Cleanup(tray.SetNameRequestTimeout(bound)) // restored after Run's cleanup
	h := newApp(t)
	r := s083Start(t, h, nil, nil)

	ctx := r.requested(t)
	if dl, ok := ctx.Deadline(); !ok || time.Until(dl) > bound {
		t.Errorf("the name request's deadline is not the %v bound (set %v)", bound, ok)
	}
	err := r.result(t, 3*time.Second)
	if err == nil {
		t.Fatal("Run = nil for a bus that never answered the name request")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Run = %v, want an error wrapping context.DeadlineExceeded", err)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, tray.ErrAlreadyRunning) {
		t.Errorf("Run = %v: a timed-out request is neither a stop nor another instance", err)
	}
	if !strings.Contains(err.Error(), s083BusName) {
		t.Errorf("Run = %v, want the bus name %s named", err, s083BusName)
	}
	if n := r.icon.started(); n != 0 {
		t.Errorf("the icon was started %d times after a failed name request", n)
	}
}
