package autoupdate

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// =============================================================================
// Task T14 / R10 — Rate-limit gate on the HTTP hot path
// =============================================================================

// recordingRateLimiter is a test double for httpRateLimiter. It records every
// host WaitHTTP was asked to gate, optionally blocks until the context is
// cancelled, and optionally returns a configured error. It is safe for
// concurrent use so it can be exercised under the -race detector.
type recordingRateLimiter struct {
	mu       sync.Mutex
	hosts    []string
	calls    atomic.Int64
	block    bool  // when true, WaitHTTP blocks until ctx is Done
	failWith error // when non-nil (and not blocking), WaitHTTP returns this
}

// WaitHTTP records the host and applies the configured behaviour.
func (m *recordingRateLimiter) WaitHTTP(ctx context.Context, domain string) error {
	m.calls.Add(1)
	m.mu.Lock()
	m.hosts = append(m.hosts, domain)
	m.mu.Unlock()

	if m.block {
		<-ctx.Done()
		return ctx.Err()
	}
	return m.failWith
}

// recordedHosts returns a copy of the hosts WaitHTTP was invoked with.
func (m *recordingRateLimiter) recordedHosts() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.hosts))
	copy(out, m.hosts)
	return out
}

// callCount returns the number of times WaitHTTP was invoked.
func (m *recordingRateLimiter) callCount() int64 { return m.calls.Load() }

// newRateLimitTestChecker builds a Checker wired to a single package whose URL
// is pkgURL, with the supplied options applied. It mirrors newContextTestChecker
// but lets callers omit the HTTP-server requirement.
func newRateLimitTestChecker(t *testing.T, pkgURL string, opts ...CheckerOption) *Checker {
	t.Helper()

	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pkgName := "test-cat/test-pkg"
	createTestEbuild(t, overlayDir, pkgName, "1.0.0")

	config := &PackagesConfig{
		Packages: map[string]PackageConfig{
			pkgName: {URL: pkgURL, Parser: "json", Path: "version"},
		},
	}

	allOpts := append([]CheckerOption{
		WithConfigDir(configDir),
		WithPackagesConfig(config),
	}, opts...)

	checker, err := NewChecker(overlayDir, allOpts...)
	if err != nil {
		t.Fatalf("NewChecker failed: %v", err)
	}
	return checker
}

// TestChecker_WithRateLimiterOption is a smoke test: the WithRateLimiter option
// installs the supplied limiter onto the Checker (sub-task 14.1).
func TestChecker_WithRateLimiterOption(t *testing.T) {
	mock := &recordingRateLimiter{}
	checker := newRateLimitTestChecker(t, "http://example.com", WithRateLimiter(mock))

	if checker.rateLimiter != mock {
		t.Fatalf("WithRateLimiter did not install the supplied limiter: got %v, want %v",
			checker.rateLimiter, mock)
	}
}

// TestChecker_WithRateLimiterOption_RejectsNil verifies a nil limiter is
// rejected by the option (the field must never be nil after construction).
func TestChecker_WithRateLimiterOption_RejectsNil(t *testing.T) {
	err := WithRateLimiter(nil)(&Checker{})
	if err == nil {
		t.Fatal("expected WithRateLimiter(nil) to return an error, got nil")
	}
}

// TestChecker_DefaultRateLimiter verifies that NewChecker installs a default
// rate limiter when WithRateLimiter is not supplied, so rateLimiter is never
// nil after construction (sub-task 14.2, R10.3).
func TestChecker_DefaultRateLimiter(t *testing.T) {
	checker := newRateLimitTestChecker(t, "http://example.com")

	if checker.rateLimiter == nil {
		t.Fatal("NewChecker left rateLimiter nil; a default limiter must be installed")
	}
	if _, ok := checker.rateLimiter.(*RateLimiter); !ok {
		t.Errorf("default rateLimiter has type %T, want *RateLimiter", checker.rateLimiter)
	}
}

// TestFetchContent_ParseHostFailure_FailsOpen verifies that a URL which fails
// url.Parse causes fetchContent to FAIL OPEN (R10.1): a Warn line is emitted
// and the rate-limit wait is skipped (the HTTP request is still attempted)
// rather than the fetch being aborted before the request.
func TestFetchContent_ParseHostFailure_FailsOpen(t *testing.T) {
	logs := captureWarnLogs(t)
	mock := &recordingRateLimiter{}

	// ":bad-url:" fails url.Parse with "missing protocol scheme".
	const malformedURL = ":bad-url:"
	checker := newRateLimitTestChecker(t, malformedURL, WithRateLimiter(mock))

	_, err := checker.fetchContent(malformedURL, nil)

	// The fetch proceeds to the HTTP layer (which itself rejects the malformed
	// URL), so an error is expected — but it must NOT be the rate-limiter wait
	// error: failing open means the rate-limit wait was skipped, not that the
	// fetch was aborted.
	if err == nil {
		t.Fatal("expected an error from fetchContent with a malformed URL, got nil")
	}
	if strings.Contains(err.Error(), "rate limiter wait") {
		t.Errorf("fetch should fail open (skip the wait), but it aborted on the wait: %v", err)
	}

	// Fail-open means WaitHTTP must NOT have been consulted.
	if got := mock.callCount(); got != 0 {
		t.Errorf("WaitHTTP was called %d time(s) for an unparseable URL; expected fail-open (0)", got)
	}

	// A Warn line must have been emitted identifying the unparseable URL.
	lines := logs.all()
	if len(lines) == 0 {
		t.Fatal("expected a Warn line for the unparseable URL, got none")
	}
	foundWarn := false
	for _, line := range lines {
		if strings.Contains(line, "rate limiter") && strings.Contains(line, malformedURL) {
			foundWarn = true
			break
		}
	}
	if !foundWarn {
		t.Errorf("no Warn line mentioned the unparseable URL %q; got lines: %v", malformedURL, lines)
	}
}

// TestFetchContent_CallsWaitHTTP verifies that fetchContent gates on the rate
// limiter before issuing the HTTP request and passes the URL's host (R10.1).
func TestFetchContent_CallsWaitHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"version":"2.0.0"}`)
	}))
	defer server.Close()

	mock := &recordingRateLimiter{}
	checker := newRateLimitTestChecker(t, server.URL, WithRateLimiter(mock))

	if _, err := checker.fetchContent(server.URL, nil); err != nil {
		t.Fatalf("fetchContent returned an unexpected error: %v", err)
	}

	hosts := mock.recordedHosts()
	if len(hosts) != 1 {
		t.Fatalf("expected WaitHTTP to be called exactly once, got %d call(s): %v", len(hosts), hosts)
	}

	// The host recorded by the limiter must match the server's host:port.
	wantHost := strings.TrimPrefix(server.URL, "http://")
	if hosts[0] != wantHost {
		t.Errorf("WaitHTTP was asked to gate host %q, want %q", hosts[0], wantHost)
	}
}

// TestFetchContent_RateLimitContextCancelled verifies that when the rate-limit
// wait is cancelled by the context, fetchContent returns the context error and
// issues NO HTTP request (R10.2): the httptest request counter stays at 0.
func TestFetchContent_RateLimitContextCancelled(t *testing.T) {
	var requestCount atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestCount.Add(1)
		fmt.Fprint(w, `{"version":"2.0.0"}`)
	}))
	defer server.Close()

	// A limiter that blocks until the context is cancelled.
	mock := &recordingRateLimiter{block: true}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	checker := newRateLimitTestChecker(t, server.URL,
		WithRateLimiter(mock),
		WithContext(ctx),
		WithOpTimeout(10*time.Second), // generous: the cancel, not the deadline, ends the wait
	)

	// Cancel the parent context shortly after the fetch starts blocking.
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := checker.fetchContent(server.URL, nil)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error when the rate-limit wait is cancelled, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected fetchContent error to wrap context.Canceled, got %v", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("fetchContent took %v after cancellation; expected a prompt return", elapsed)
	}

	// The defining assertion: no HTTP request was issued.
	if got := requestCount.Load(); got != 0 {
		t.Errorf("an HTTP request was issued despite the cancelled rate-limit wait: count=%d", got)
	}
}

// TestRateLimiter_LRUEvictionUnderLoad drives WaitHTTP with more distinct hosts
// than the configured domain cap and asserts the limiter evicts the oldest
// entries, never deadlocks, and keeps its internal state consistent
// (sub-task 14.5, gap #8).
//
// The default HTTP limiter is burst-1 at one token per 6s: the FIRST WaitHTTP
// for a fresh host consumes its free burst token and returns immediately, but a
// SECOND call for the same host would block ~6s. Every WaitHTTP below therefore
// targets a host it has never seen, so the test exercises the eviction and
// locking paths without paying the 6s rate-limit interval.
//
// Run under: go test -race -count=10 -run TestRateLimiter_LRUEvictionUnderLoad
func TestRateLimiter_LRUEvictionUnderLoad(t *testing.T) {
	const maxDomains = 30
	const seqHosts = 35 // > maxDomains: forces LRU eviction

	rl := NewRateLimiter(WithMaxDomains(maxDomains))
	ctx := context.Background()

	// Phase 1 — sequential inserts of distinct fresh hosts drive LRU eviction.
	for i := 0; i < seqHosts; i++ {
		host := fmt.Sprintf("seq-host-%02d.example.com", i)
		if err := rl.WaitHTTP(ctx, host); err != nil {
			t.Fatalf("WaitHTTP(%q) returned an unexpected error: %v", host, err)
		}
	}

	// 35 distinct hosts through a 30-entry limiter: the count must be capped at
	// exactly maxDomains — eviction bounded the map.
	if got := rl.DomainCount(); got != maxDomains {
		t.Fatalf("DomainCount=%d after %d distinct inserts; want %d (LRU eviction did not cap the map)",
			got, seqHosts, maxDomains)
	}

	// Eviction is least-recently-used: the oldest host (inserted first) was
	// dropped and the newest retained. AllowHTTP recreates a missing entry's
	// limiter with a fresh burst token, so it reports true for an evicted host
	// and false for a still-tracked host whose burst token was already spent.
	if rl.AllowHTTP("seq-host-34.example.com") {
		t.Error("AllowHTTP(seq-host-34)=true: the newest host should still be tracked with its burst token spent")
	}
	if !rl.AllowHTTP("seq-host-00.example.com") {
		t.Error("AllowHTTP(seq-host-00)=false: the oldest host should have been LRU-evicted")
	}

	// Phase 2 — concurrent load. Each goroutine uses its OWN unique hosts, so
	// every WaitHTTP hits a fresh host's free burst token and never blocks on
	// the 6s interval. The -race detector validates the limiter's locking and
	// eviction path under contention.
	const goroutines = 20
	const perGoroutine = 40
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				host := fmt.Sprintf("conc-%d-host-%02d.example.com", g, i)
				if err := rl.WaitHTTP(ctx, host); err != nil {
					t.Errorf("concurrent WaitHTTP(%q) failed: %v", host, err)
					return
				}
				// The tracked count must never exceed the cap, even mid-load.
				if c := rl.DomainCount(); c > maxDomains {
					t.Errorf("DomainCount=%d during concurrent load exceeds cap %d", c, maxDomains)
					return
				}
			}
		}(g)
	}

	// Guard against a deadlock: with fresh-host-only access the concurrent
	// phase completes near-instantly; a stall signals a locking bug.
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("concurrent rate-limit load did not finish within 15s — possible deadlock")
	}

	// Final consistency check: the map stayed bounded by the cap throughout.
	if got := rl.DomainCount(); got != maxDomains {
		t.Errorf("after concurrent load DomainCount=%d; want %d (map not bounded to the cap)",
			got, maxDomains)
	}
}
