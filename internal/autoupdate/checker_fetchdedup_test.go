package autoupdate

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// =============================================================================
// Story 024 / task 2.1 — fetchContent consults the cache BEFORE the rate limiter
// (S024-R2.1, R2.2, R2.4, R2.5, R3.4, S024-UB1)
// =============================================================================

// TestCheckerFetchDedup drives the real choke point — Checker.fetchContent —
// against a counting httptest server and a counting rate limiter.
//
// Two counters carry the whole story, and neither can be satisfied by inspecting
// the returned bytes:
//
//   - the SERVER counter proves no HTTP request was issued for a repeated read
//     (S024-R2.1);
//   - the LIMITER counter proves the lookup happens BEFORE the rate-limiter
//     wait (S024-R2.2). That ordering is where the wall-clock win lives: at one
//     token per 6 s per host, a hit that still queued for a token would save the
//     request and none of the 18 minutes.
func TestCheckerFetchDedup(t *testing.T) {
	const payload = `{"version":"2.0.0"}`

	// -------------------------------------------------------------------------
	// S024-R2.1 / R2.2 / UB1 — the second read of one identity costs neither a
	// request nor a token; the first read still pays for both.
	// -------------------------------------------------------------------------
	t.Run("a repeated read issues no request and takes no rate-limit token", func(t *testing.T) {
		var requests atomic.Int64
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			requests.Add(1)
			fmt.Fprint(w, payload)
		}))
		defer server.Close()

		limiter := &recordingRateLimiter{}
		checker := newRateLimitTestChecker(t, server.URL, WithRateLimiter(limiter))

		first, err := checker.fetchContent(server.URL, nil, checker.operationTimeout(nil))
		if err != nil {
			t.Fatalf("first fetchContent returned an unexpected error: %v", err)
		}
		second, err := checker.fetchContent(server.URL, nil, checker.operationTimeout(nil))
		if err != nil {
			t.Fatalf("second fetchContent returned an unexpected error: %v", err)
		}

		if !bytes.Equal(first, []byte(payload)) || !bytes.Equal(second, []byte(payload)) {
			t.Errorf("fetchContent returned %q and %q, want %q for both reads", first, second, payload)
		}
		if got := requests.Load(); got != 1 {
			t.Errorf("the server saw %d request(s) for two reads of one identity; want exactly 1 (S024-R2.1)", got)
		}
		// UB1 keeps the limiter gating every request that IS issued: exactly one
		// request went out, so exactly one token was taken — no more, no fewer.
		if got := limiter.callCount(); got != 1 {
			t.Errorf("WaitHTTP was called %d time(s); want 1 — a read answered from a "+
				"retained body must not queue for a token (S024-R2.2)", got)
		}
	})

	// -------------------------------------------------------------------------
	// S024-R1.1 through the checker: declared headers are part of the identity,
	// so a Range read and a full read are two requests, not one shared body.
	// -------------------------------------------------------------------------
	t.Run("reads with different declared headers stay separate requests", func(t *testing.T) {
		var requests atomic.Int64
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			requests.Add(1)
			fmt.Fprint(w, payload)
		}))
		defer server.Close()

		limiter := &recordingRateLimiter{}
		checker := newRateLimitTestChecker(t, server.URL, WithRateLimiter(limiter))

		if _, err := checker.fetchContent(server.URL, map[string]string{"Range": "bytes=0-15"},
			checker.operationTimeout(nil)); err != nil {
			t.Fatalf("the Range read returned an unexpected error: %v", err)
		}
		if _, err := checker.fetchContent(server.URL, nil, checker.operationTimeout(nil)); err != nil {
			t.Fatalf("the full read returned an unexpected error: %v", err)
		}

		if got := requests.Load(); got != 2 {
			t.Errorf("the server saw %d request(s); want 2 — a partial read must never be "+
				"served to a caller that asked for the whole document (S024-R1.3)", got)
		}
		if got := limiter.callCount(); got != 2 {
			t.Errorf("WaitHTTP was called %d time(s); want 2 — both requests were really issued (UB1)", got)
		}
	})

	// -------------------------------------------------------------------------
	// S024-R2.3 — the shape that matters in production: many packages sharing one
	// URL, in flight at once under the concurrency semaphore.
	// -------------------------------------------------------------------------
	t.Run("concurrent reads of one identity issue a single request", func(t *testing.T) {
		const readers = 12

		var requests atomic.Int64
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			requests.Add(1)
			time.Sleep(80 * time.Millisecond) // hold the leader so the others really overlap
			fmt.Fprint(w, payload)
		}))
		defer server.Close()

		limiter := &recordingRateLimiter{}
		checker := newRateLimitTestChecker(t, server.URL,
			WithRateLimiter(limiter),
			WithContext(context.Background()),
		)

		bodies := make([][]byte, readers)
		errs := make([]error, readers)
		var wg sync.WaitGroup
		for i := 0; i < readers; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				bodies[i], errs[i] = checker.fetchContent(server.URL, nil, checker.operationTimeout(nil))
			}(i)
		}
		wg.Wait()

		for i := range errs {
			if errs[i] != nil {
				t.Fatalf("concurrent reader %d returned an unexpected error: %v", i, errs[i])
			}
			if !bytes.Equal(bodies[i], []byte(payload)) {
				t.Errorf("concurrent reader %d received %q, want %q", i, bodies[i], payload)
			}
		}
		if got := requests.Load(); got != 1 {
			t.Errorf("the server saw %d request(s) for %d concurrent readers; want exactly 1 (S024-R2.3)", got, readers)
		}
		if got := limiter.callCount(); got != 1 {
			t.Errorf("WaitHTTP was called %d time(s) for %d concurrent readers; want 1 (S024-R2.2)", got, readers)
		}
	})

	// -------------------------------------------------------------------------
	// S024-R3.2 / R3.4 — a failed read is not retained: the next read of that
	// identity issues its own request, gated by the limiter exactly as before
	// this story, and succeeds on its own merits.
	// -------------------------------------------------------------------------
	t.Run("a failed read is not retained and the next read goes out again", func(t *testing.T) {
		var requests atomic.Int64
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			// 404 rather than 5xx: the retryable client does not retry it, so the
			// counter below reads as one request per read, with no backoff wait.
			if requests.Add(1) == 1 {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			fmt.Fprint(w, payload)
		}))
		defer server.Close()

		limiter := &recordingRateLimiter{}
		checker := newRateLimitTestChecker(t, server.URL, WithRateLimiter(limiter))

		if _, err := checker.fetchContent(server.URL, nil, checker.operationTimeout(nil)); err == nil {
			t.Fatal("expected the first read to fail on the 404, got nil")
		}

		body, err := checker.fetchContent(server.URL, nil, checker.operationTimeout(nil))
		if err != nil {
			t.Fatalf("the second read returned %v; a cached FAILURE would have poisoned this identity (S024-R3.2)", err)
		}
		if !bytes.Equal(body, []byte(payload)) {
			t.Errorf("the second read returned %q, want %q", body, payload)
		}
		if got := requests.Load(); got != 2 {
			t.Errorf("the server saw %d request(s); want 2 — the failed read must be re-issued, not replayed", got)
		}
		if got := limiter.callCount(); got != 2 {
			t.Errorf("WaitHTTP was called %d time(s); want 2 — a re-issued request is gated like any other (UB1)", got)
		}
	})
}
