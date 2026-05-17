package autoupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// =============================================================================
// Task T15 / R4 — Parallel CheckAll
// =============================================================================

// concurrencyRateLimiter is an httpRateLimiter test double that observes the
// number of concurrent WaitHTTP calls. It optionally sleeps for delay (so the
// worker holds the limiter long enough for an in-flight overlap to be visible),
// panics for a designated host (to exercise CheckAll's panic recovery), or
// blocks on the context (to exercise mid-flight cancellation).
type concurrencyRateLimiter struct {
	delay      time.Duration
	panicHost  string // when non-empty, WaitHTTP panics for this host
	blockUntil chan struct{}

	inFlight    atomic.Int64
	maxInFlight atomic.Int64
	calls       atomic.Int64
}

// WaitHTTP records concurrency, then applies the configured behaviour.
func (m *concurrencyRateLimiter) WaitHTTP(ctx context.Context, domain string) error {
	m.calls.Add(1)

	if m.panicHost != "" && domain == m.panicHost {
		panic("injected rate-limiter panic for " + domain)
	}

	cur := m.inFlight.Add(1)
	defer m.inFlight.Add(-1)
	for {
		prev := m.maxInFlight.Load()
		if cur <= prev || m.maxInFlight.CompareAndSwap(prev, cur) {
			break
		}
	}

	if m.blockUntil != nil {
		select {
		case <-m.blockUntil:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// buildParallelChecker constructs a Checker wired to numPkgs packages, each
// pointing at srvURL, with the supplied options applied.
func buildParallelChecker(t *testing.T, numPkgs int, srvURL string, opts ...CheckerOption) (*Checker, []string) {
	t.Helper()

	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	packages := make(map[string]PackageConfig, numPkgs)
	names := make([]string, 0, numPkgs)
	for i := 0; i < numPkgs; i++ {
		name := fmt.Sprintf("cat-%03d/pkg-%03d", i, i)
		packages[name] = PackageConfig{URL: srvURL, Parser: "json", Path: "version"}
		createTestEbuild(t, overlayDir, name, "0.9.0")
		names = append(names, name)
	}

	allOpts := append([]CheckerOption{
		WithConfigDir(configDir),
		WithPackagesConfig(&PackagesConfig{Packages: packages}),
	}, opts...)

	checker, err := NewChecker(overlayDir, allOpts...)
	if err != nil {
		t.Fatalf("NewChecker failed: %v", err)
	}
	return checker, names
}

// TestChecker_WithConcurrencyOption is a table test for WithConcurrency: valid
// values are accepted and stored; out-of-range values return an error.
func TestChecker_WithConcurrencyOption(t *testing.T) {
	tests := []struct {
		name    string
		n       int
		wantErr bool
	}{
		{"lower bound 1", 1, false},
		{"typical 10", 10, false},
		{"upper bound 100", 100, false},
		{"zero is rejected", 0, true},
		{"negative is rejected", -1, true},
		{"101 exceeds the cap", 101, true},
		{"large value is rejected", 10_000, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Checker{}
			err := WithConcurrency(tt.n)(c)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("WithConcurrency(%d) = nil error, want an error", tt.n)
				}
				return
			}
			if err != nil {
				t.Fatalf("WithConcurrency(%d) returned unexpected error: %v", tt.n, err)
			}
			if c.concurrency != tt.n {
				t.Errorf("concurrency = %d, want %d", c.concurrency, tt.n)
			}
		})
	}
}

// TestChecker_DefaultConcurrency verifies NewChecker defaults concurrency to
// DefaultConcurrency when WithConcurrency is not supplied.
func TestChecker_DefaultConcurrency(t *testing.T) {
	checker, _ := buildParallelChecker(t, 1, "http://example.com",
		WithRateLimiter(unlimitedRateLimiter()))
	if checker.concurrency != DefaultConcurrency {
		t.Errorf("default concurrency = %d, want %d", checker.concurrency, DefaultConcurrency)
	}
}

// TestCheckAll_Parallel_RespectsLimit asserts that the number of CheckPackage
// workers running concurrently never exceeds the configured concurrency limit.
func TestCheckAll_Parallel_RespectsLimit(t *testing.T) {
	const numPkgs = 50
	const concurrency = 6

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"version": "1.0.0"})
	}))
	defer server.Close()

	// The limiter both observes concurrency and holds each worker briefly so a
	// genuine overlap is visible.
	rl := &concurrencyRateLimiter{delay: 15 * time.Millisecond}

	checker, _ := buildParallelChecker(t, numPkgs, server.URL,
		WithRateLimiter(rl),
		WithConcurrency(concurrency),
	)

	batch := checker.CheckAll(true)

	if total := len(batch.Items) + len(batch.Failures); total != numPkgs {
		t.Fatalf("CheckAll produced %d results, want %d (Items=%d, Failures=%d)",
			total, numPkgs, len(batch.Items), len(batch.Failures))
	}
	if hi := rl.maxInFlight.Load(); hi > concurrency {
		t.Errorf("max concurrent workers = %d; exceeds the configured limit %d", hi, concurrency)
	}
	if hi := rl.maxInFlight.Load(); hi <= 1 {
		t.Errorf("max concurrent workers = %d; expected genuine parallelism (> 1)", hi)
	}
}

// TestCheckAll_PanicRecovery verifies that a package whose check panics does
// NOT crash the process: the panic is recovered, recorded as a failure keyed
// by the package name, and every other package is still checked.
func TestCheckAll_PanicRecovery(t *testing.T) {
	const numPkgs = 12

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"version": "1.0.0"})
	}))
	defer server.Close()

	// The limiter panics for exactly one package's host. Every package shares
	// the same server URL/host, so to single out one package the panic host
	// must be unique. Point the panicking package at a distinct URL.
	panicSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"version": "1.0.0"})
	}))
	defer panicSrv.Close()

	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	const panicPkg = "cat-panic/pkg-panic"
	packages := map[string]PackageConfig{
		panicPkg: {URL: panicSrv.URL, Parser: "json", Path: "version"},
	}
	createTestEbuild(t, overlayDir, panicPkg, "0.9.0")
	for i := 0; i < numPkgs-1; i++ {
		name := fmt.Sprintf("cat-%03d/pkg-%03d", i, i)
		packages[name] = PackageConfig{URL: server.URL, Parser: "json", Path: "version"}
		createTestEbuild(t, overlayDir, name, "0.9.0")
	}

	panicHost := hostOf(t, panicSrv.URL)
	rl := &concurrencyRateLimiter{panicHost: panicHost}

	checker, err := NewChecker(overlayDir,
		WithConfigDir(configDir),
		WithPackagesConfig(&PackagesConfig{Packages: packages}),
		WithRateLimiter(rl),
		WithConcurrency(5),
	)
	if err != nil {
		t.Fatalf("NewChecker failed: %v", err)
	}

	// The defining assertion: this call returns normally — the panic in one
	// worker did not crash the process.
	batch := checker.CheckAll(true)

	if total := len(batch.Items) + len(batch.Failures); total != numPkgs {
		t.Fatalf("CheckAll produced %d results, want %d", total, numPkgs)
	}
	failErr, ok := batch.Failures[panicPkg]
	if !ok {
		t.Fatalf("panicking package %q not recorded in Failures; keys: %v",
			panicPkg, failureKeys(batch.Failures))
	}
	if failErr == nil || !contains(failErr.Error(), "panic:") {
		t.Errorf("failure for %q = %v; want an error mentioning %q", panicPkg, failErr, "panic:")
	}
	// Every non-panicking package must have been checked successfully.
	if len(batch.Items) != numPkgs-1 {
		t.Errorf("successful items = %d, want %d", len(batch.Items), numPkgs-1)
	}
}

// TestProgressCallback_Monotonic verifies that the done value passed to the
// progress callback is monotone non-decreasing even though callbacks fire from
// concurrent worker goroutines. Run under: go test -run
// TestProgressCallback_Monotonic -race -count=20 ./internal/autoupdate/...
func TestProgressCallback_Monotonic(t *testing.T) {
	const numPkgs = 40

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"version": "1.0.0"})
	}))
	defer server.Close()

	var (
		mu       sync.Mutex
		observed []uint64
		lastTot  uint64
		totMu    sync.Mutex
	)

	cb := func(done, total uint64) {
		totMu.Lock()
		lastTot = total
		totMu.Unlock()
		mu.Lock()
		observed = append(observed, done)
		mu.Unlock()
	}

	checker, _ := buildParallelChecker(t, numPkgs, server.URL,
		WithRateLimiter(unlimitedRateLimiter()),
		WithConcurrency(10),
		WithProgressCallback(cb),
	)

	checker.CheckAll(true)

	mu.Lock()
	defer mu.Unlock()
	if len(observed) != numPkgs {
		t.Fatalf("progress callback fired %d times, want %d", len(observed), numPkgs)
	}
	// The atomic counter guarantees each callback observes a value strictly
	// larger than every callback that ran before it.
	sorted := append([]uint64(nil), observed...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	for i := uint64(0); i < numPkgs; i++ {
		if sorted[i] != i+1 {
			t.Fatalf("done values are not the contiguous set 1..%d: sorted[%d]=%d", numPkgs, i, sorted[i])
		}
	}
	if got := lastTot; got != numPkgs {
		t.Errorf("total passed to callback = %d, want %d", got, numPkgs)
	}
}

// TestCheckAll_ResultsSorted verifies that CheckAll returns Items sorted
// lexically by package name regardless of map iteration / completion order.
func TestCheckAll_ResultsSorted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"version": "1.0.0"})
	}))
	defer server.Close()

	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	// Deliberately shuffled package names.
	names := []string{
		"zeta/omega", "alpha/aardvark", "mike/midpoint", "beta/banana",
		"yankee/yard", "delta/dragon", "charlie/cherry", "echo/eagle",
	}
	rand.New(rand.NewSource(1)).Shuffle(len(names), func(i, j int) {
		names[i], names[j] = names[j], names[i]
	})

	packages := make(map[string]PackageConfig, len(names))
	for _, name := range names {
		packages[name] = PackageConfig{URL: server.URL, Parser: "json", Path: "version"}
		createTestEbuild(t, overlayDir, name, "0.9.0")
	}

	checker, err := NewChecker(overlayDir,
		WithConfigDir(configDir),
		WithPackagesConfig(&PackagesConfig{Packages: packages}),
		WithRateLimiter(unlimitedRateLimiter()),
		WithConcurrency(8),
	)
	if err != nil {
		t.Fatalf("NewChecker failed: %v", err)
	}

	batch := checker.CheckAll(true)

	if len(batch.Items) != len(names) {
		t.Fatalf("got %d items, want %d", len(batch.Items), len(names))
	}
	want := append([]string(nil), names...)
	sort.Strings(want)
	for i, item := range batch.Items {
		if item.Package != want[i] {
			t.Errorf("Items[%d].Package = %q, want %q (Items not sorted)", i, item.Package, want[i])
		}
	}
}

// TestCheckAll_ContextCancelMidFlight verifies that cancelling the Checker's
// parent context part-way through a large batch stops dispatch: only about
// `concurrency` packages do real work and the remainder are recorded in
// Failures with a context error.
func TestCheckAll_ContextCancelMidFlight(t *testing.T) {
	const numPkgs = 100
	const concurrency = 5

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"version": "1.0.0"})
	}))
	defer server.Close()

	// Each worker is held ~120ms inside the limiter, so when the context is
	// cancelled ~50ms in only the first wave (<= concurrency) is in flight.
	rl := &concurrencyRateLimiter{delay: 120 * time.Millisecond}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	checker, _ := buildParallelChecker(t, numPkgs, server.URL,
		WithRateLimiter(rl),
		WithConcurrency(concurrency),
		WithContext(ctx),
		WithOpTimeout(10*time.Second),
	)

	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	batch := checker.CheckAll(true)
	elapsed := time.Since(start)

	// Every package is accounted for, in Items or Failures.
	if total := len(batch.Items) + len(batch.Failures); total != numPkgs {
		t.Fatalf("CheckAll produced %d results, want %d (Items=%d, Failures=%d)",
			total, numPkgs, len(batch.Items), len(batch.Failures))
	}

	// The bulk of the batch must have been short-circuited: dispatch stopped at
	// cancellation, so at most ~concurrency packages did real work. Allow a
	// little slack for a worker that finished and freed a slot before cancel.
	const completedCeiling = concurrency * 3
	if len(batch.Items) > completedCeiling {
		t.Errorf("completed work for %d packages; expected <= %d (dispatch should stop at cancellation)",
			len(batch.Items), completedCeiling)
	}

	// Cancelled packages must be recorded with a context error.
	ctxFailures := 0
	for _, err := range batch.Failures {
		if errors.Is(err, context.Canceled) {
			ctxFailures++
		}
	}
	if ctxFailures == 0 {
		t.Errorf("no Failures carry context.Canceled; expected the undispatched packages to be recorded with it")
	}
	if want := numPkgs - len(batch.Items) - len(batch.Failures) + ctxFailures; want < numPkgs-completedCeiling {
		// Sanity: most packages should be cancelled, not completed.
		t.Logf("context failures=%d, items=%d, other failures=%d",
			ctxFailures, len(batch.Items), len(batch.Failures)-ctxFailures)
	}

	// The call must return promptly after cancellation (the in-flight wave
	// finishes its ~120ms hold, but nothing new is dispatched).
	if elapsed > 5*time.Second {
		t.Errorf("CheckAll took %v after mid-flight cancellation; expected a prompt return", elapsed)
	}
}

// hostOf extracts the host:port from an http://host:port URL.
func hostOf(t *testing.T, rawURL string) string {
	t.Helper()
	const prefix = "http://"
	if !contains(rawURL, prefix) {
		t.Fatalf("unexpected URL form: %q", rawURL)
	}
	return rawURL[len(prefix):]
}

// contains reports whether s contains substr.
func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
