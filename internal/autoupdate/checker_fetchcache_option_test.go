package autoupdate

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// =============================================================================
// Story 024 / task 2.2 — the Checker option and the per-run counters
// (S024-R6.1, R7.2)
// =============================================================================

// TestWithFetchCache pins the construction contract: deduplication is ON unless
// a caller asks for the pre-story behaviour, and "off" is expressed as the
// ABSENCE of a cache rather than as a branch threaded through the fetch path.
//
// The nil/non-nil assertions alone would be shallow — a field can be set and
// never consulted — so each one is paired with the counter that proves the
// construction reached the wire: how many requests the server actually saw.
func TestWithFetchCache(t *testing.T) {
	const payload = `{"version":"4.5.6"}`

	newCountingServer := func(t *testing.T, requests *atomic.Int64) *httptest.Server {
		t.Helper()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			requests.Add(1)
			fmt.Fprint(w, payload)
		}))
		t.Cleanup(srv.Close)
		return srv
	}

	// -------------------------------------------------------------------------
	// S024-R7.2 — enabled by default.
	// -------------------------------------------------------------------------
	t.Run("the cache is enabled by default", func(t *testing.T) {
		var requests atomic.Int64
		server := newCountingServer(t, &requests)

		checker := newRateLimitTestChecker(t, server.URL, WithRateLimiter(unlimitedRateLimiter()))
		if checker.bodies == nil {
			t.Fatal("NewChecker left the body cache nil; deduplication must be on by default (S024-R7.2)")
		}

		for i := 0; i < 3; i++ {
			if _, err := checker.fetchContent(server.URL, nil, checker.operationTimeout(nil)); err != nil {
				t.Fatalf("read %d returned an unexpected error: %v", i, err)
			}
		}
		if got := requests.Load(); got != 1 {
			t.Errorf("the server saw %d request(s) for three reads under the default construction; want 1", got)
		}
	})

	// -------------------------------------------------------------------------
	// S024-R7.1 (construction half) — off means the pre-story path, byte for
	// byte: one request per read.
	// -------------------------------------------------------------------------
	t.Run("WithFetchCache(false) restores one request per read", func(t *testing.T) {
		var requests atomic.Int64
		server := newCountingServer(t, &requests)

		limiter := &recordingRateLimiter{}
		checker := newRateLimitTestChecker(t, server.URL,
			WithRateLimiter(limiter),
			WithFetchCache(false),
		)
		if checker.bodies != nil {
			t.Fatal("WithFetchCache(false) left a cache installed; a disabled cache is a nil cache")
		}

		for i := 0; i < 3; i++ {
			if _, err := checker.fetchContent(server.URL, nil, checker.operationTimeout(nil)); err != nil {
				t.Fatalf("read %d returned an unexpected error: %v", i, err)
			}
		}
		if got := requests.Load(); got != 3 {
			t.Errorf("the server saw %d request(s) for three un-deduplicated reads; want 3 (S024-R7.1)", got)
		}
		if got := limiter.callCount(); got != 3 {
			t.Errorf("WaitHTTP was called %d time(s); want 3 — every issued request is gated (UB1)", got)
		}
	})

	t.Run("WithFetchCache(true) is the explicit form of the default", func(t *testing.T) {
		var requests atomic.Int64
		server := newCountingServer(t, &requests)

		checker := newRateLimitTestChecker(t, server.URL,
			WithRateLimiter(unlimitedRateLimiter()),
			WithFetchCache(true),
		)
		if checker.bodies == nil {
			t.Fatal("WithFetchCache(true) did not install a cache")
		}
	})

	// -------------------------------------------------------------------------
	// S024-R6.1 — the per-run counters are readable, and they describe the run
	// that just happened: how many reads fetched, and how many were answered
	// from a retained body.
	// -------------------------------------------------------------------------
	t.Run("the per-run counters describe the run", func(t *testing.T) {
		var requestsA, requestsB atomic.Int64
		serverA := newCountingServer(t, &requestsA)
		serverB := newCountingServer(t, &requestsB)

		checker := newRateLimitTestChecker(t, serverA.URL, WithRateLimiter(unlimitedRateLimiter()))

		for _, u := range []string{serverA.URL, serverA.URL, serverB.URL} {
			if _, err := checker.fetchContent(u, nil, checker.operationTimeout(nil)); err != nil {
				t.Fatalf("read of %s returned an unexpected error: %v", u, err)
			}
		}

		stats := checker.bodies.snapshot()
		if stats.Misses != 2 {
			t.Errorf("stats.Misses = %d, want 2 (two distinct identities were fetched)", stats.Misses)
		}
		if stats.Hits != 1 {
			t.Errorf("stats.Hits = %d, want 1 (the repeated read was answered from memory)", stats.Hits)
		}
		if stats.Refetches != 0 || stats.Oversize != 0 || stats.BudgetFull != 0 {
			t.Errorf("stats = %+v; want no refetches and no refused bodies in a clean run", stats)
		}
		// The counters must agree with the wire, not merely with themselves.
		if got := requestsA.Load(); got != 1 {
			t.Errorf("server A saw %d request(s) for two reads; want 1", got)
		}
		if got := requestsB.Load(); got != 1 {
			t.Errorf("server B saw %d request(s) for one read; want 1", got)
		}
	})

	// -------------------------------------------------------------------------
	// S024-R2.4 — the shared bodies are scoped to ONE Checker, i.e. to one
	// command invocation, and never leak across runs.
	// -------------------------------------------------------------------------
	t.Run("bodies are scoped to a single Checker", func(t *testing.T) {
		var requests atomic.Int64
		server := newCountingServer(t, &requests)

		first := newRateLimitTestChecker(t, server.URL, WithRateLimiter(unlimitedRateLimiter()))
		if _, err := first.fetchContent(server.URL, nil, first.operationTimeout(nil)); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		second := newRateLimitTestChecker(t, server.URL, WithRateLimiter(unlimitedRateLimiter()))
		if _, err := second.fetchContent(server.URL, nil, second.operationTimeout(nil)); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if got := requests.Load(); got != 2 {
			t.Errorf("the server saw %d request(s) across two Checkers; want 2 — "+
				"a body must not survive its run (S024-R2.4)", got)
		}
	})
}
