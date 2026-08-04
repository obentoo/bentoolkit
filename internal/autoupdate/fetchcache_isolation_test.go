package autoupdate

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// =============================================================================
// Story 024 / task 1.3 — failure and cancellation isolation
// (S024-R3.1, R3.2, R3.3, R3.5, R5.1, R6.2, R6.3)
// =============================================================================

// errIsolationFetch is the injected upstream failure. The point of the whole
// sub-task is that it reaches exactly one caller and infects no other.
var errIsolationFetch = errors.New("injected upstream failure")

// TestBodyCacheIsolation pins the guarantee that makes a shared fetch safe: a
// shared fetch is an optimisation, never a coupling. No caller's outcome may
// depend on another caller's — not on its failure, not on its panic, not on its
// deadline.
//
// Every assertion below is anchored on a CALL COUNTER rather than on the
// returned bytes, because "a request was (or was not) issued" is the entire
// claim: a test that only inspected return values would pass against a cache
// that shares errors, which is precisely what S024-R3 forbids.
func TestBodyCacheIsolation(t *testing.T) {
	const payload = `{"version":"3.1.4"}`

	// -------------------------------------------------------------------------
	// S024-R3.1 — the leader's failure is returned to the leader alone; every
	// waiter performs its OWN fetch instead of inheriting the error.
	// -------------------------------------------------------------------------
	t.Run("a failed fetch is not shared with the waiters", func(t *testing.T) {
		const callers = 4

		c := newBodyCache(1<<20, 8<<20)
		key := bodyKey("https://example.test/always-fails", nil)
		ctx := context.Background()

		var calls atomic.Int64
		entered := make(chan struct{})
		release := make(chan struct{})
		fetch := func() ([]byte, error) {
			if calls.Add(1) == 1 {
				close(entered)
				<-release
			}
			return nil, errIsolationFetch
		}

		errs := make([]error, callers)
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[0] = c.do(ctx, key, fetch)
		}()

		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			t.Fatal("the leader never entered fetch")
		}

		for i := 1; i < callers; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				_, errs[i] = c.do(ctx, key, fetch)
			}(i)
		}
		time.Sleep(150 * time.Millisecond) // let every waiter reach its wait
		close(release)

		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("callers did not return after the leader's fetch failed — a failure must release the waiters")
		}

		// The defining assertion: every waiter issued its own fetch.
		if got := calls.Load(); got != callers {
			t.Errorf("fetch was invoked %d time(s) for %d callers of a failing identity; "+
				"want %d — each waiter must fetch on its own (S024-R3.1)", got, callers, callers)
		}
		for i, err := range errs {
			if !errors.Is(err, errIsolationFetch) {
				t.Errorf("caller %d returned %v, want the injected fetch failure", i, err)
			}
		}
		if stats := c.snapshot(); stats.Refetches != callers-1 {
			t.Errorf("stats.Refetches = %d, want %d (one per waiter that refetched after the leader failed)",
				stats.Refetches, callers-1)
		}
	})

	// -------------------------------------------------------------------------
	// S024-R3.2 — a failure leaves nothing behind: the next read of the same
	// identity issues a fresh request and succeeds on its own merits.
	// -------------------------------------------------------------------------
	t.Run("a failure is not retained, so the next read fetches again", func(t *testing.T) {
		c := newBodyCache(1<<20, 8<<20)
		key := bodyKey("https://example.test/fails-once", nil)
		ctx := context.Background()

		var calls atomic.Int64
		fetch := func() ([]byte, error) {
			if calls.Add(1) == 1 {
				return nil, errIsolationFetch
			}
			return []byte(payload), nil
		}

		if _, err := c.do(ctx, key, fetch); !errors.Is(err, errIsolationFetch) {
			t.Fatalf("first read returned %v, want the injected fetch failure", err)
		}
		body, err := c.do(ctx, key, fetch)
		if err != nil {
			t.Fatalf("second read returned %v; a cached FAILURE would have poisoned this identity", err)
		}
		if !bytes.Equal(body, []byte(payload)) {
			t.Errorf("second read returned %q, want %q", body, payload)
		}
		if got := calls.Load(); got != 2 {
			t.Errorf("fetch was invoked %d time(s), want 2 (the failed attempt plus a fresh one)", got)
		}
		// The successful body IS retained: a third read costs nothing.
		if _, err := c.do(ctx, key, fetch); err != nil {
			t.Fatalf("third read returned %v, want the retained body", err)
		}
		if got := calls.Load(); got != 2 {
			t.Errorf("fetch was invoked %d time(s) after a successful fetch was retained, want 2", got)
		}
	})

	// -------------------------------------------------------------------------
	// S024-R3.5 — a panicking leader must release its waiters rather than leave
	// them blocked on a dead leader forever.
	// -------------------------------------------------------------------------
	t.Run("a panicking leader releases its waiters", func(t *testing.T) {
		const waiters = 3

		c := newBodyCache(1<<20, 8<<20)
		key := bodyKey("https://example.test/panics", nil)
		ctx := context.Background()

		var calls atomic.Int64
		entered := make(chan struct{})
		release := make(chan struct{})
		fetch := func() ([]byte, error) {
			if calls.Add(1) == 1 {
				close(entered)
				<-release
				panic("injected panic inside the leading fetch")
			}
			return []byte(payload), nil
		}

		var recovered atomic.Value
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					recovered.Store(true)
				}
			}()
			_, _ = c.do(ctx, key, fetch) //nolint:errcheck // the panic, not the error, is under test
		}()

		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			t.Fatal("the leader never entered fetch")
		}

		bodies := make([][]byte, waiters)
		errsOut := make([]error, waiters)
		for i := 0; i < waiters; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				bodies[i], errsOut[i] = c.do(ctx, key, fetch)
			}(i)
		}
		time.Sleep(150 * time.Millisecond)
		close(release)

		done := make(chan struct{})
		go func() { wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("waiters are still blocked after the leader panicked (S024-R3.5)")
		}

		if recovered.Load() == nil {
			t.Error("the leader's panic did not reach its own caller; it must propagate, not be swallowed")
		}
		for i := 0; i < waiters; i++ {
			if errsOut[i] != nil {
				t.Errorf("waiter %d returned %v; a released waiter must fetch on its own and succeed", i, errsOut[i])
				continue
			}
			if !bytes.Equal(bodies[i], []byte(payload)) {
				t.Errorf("waiter %d received %q, want %q", i, bodies[i], payload)
			}
		}
	})

	// -------------------------------------------------------------------------
	// S024-R5.1 / R3.3 — the wait is bounded by the parent context alone. A
	// cancelled context returns the context error and issues NO fetch of its own.
	// -------------------------------------------------------------------------
	t.Run("a cancelled context aborts the wait without fetching", func(t *testing.T) {
		c := newBodyCache(1<<20, 8<<20)
		key := bodyKey("https://example.test/slow", nil)

		var calls atomic.Int64
		entered := make(chan struct{})
		release := make(chan struct{})
		fetch := func() ([]byte, error) {
			if calls.Add(1) == 1 {
				close(entered)
				<-release
			}
			return []byte(payload), nil
		}

		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = c.do(context.Background(), key, fetch) //nolint:errcheck // the leader is only scenery here
		}()
		select {
		case <-entered:
		case <-time.After(10 * time.Second):
			t.Fatal("the leader never entered fetch")
		}

		waitCtx, cancel := context.WithCancel(context.Background())
		go func() {
			time.Sleep(50 * time.Millisecond)
			cancel()
		}()

		start := time.Now()
		_, err := c.do(waitCtx, key, fetch)
		elapsed := time.Since(start)

		if !errors.Is(err, context.Canceled) {
			t.Errorf("the cancelled waiter returned %v, want an error wrapping context.Canceled", err)
		}
		if elapsed > 5*time.Second {
			t.Errorf("the cancelled waiter took %v to return; the wait must end with the context", elapsed)
		}
		// The defining assertion: cancellation aborted WITHOUT a request of its own.
		if got := calls.Load(); got != 1 {
			t.Errorf("fetch was invoked %d time(s); a cancelled waiter must issue none (S024-R5.1)", got)
		}

		close(release)
		wg.Wait()
		cancel()
	})

	// -------------------------------------------------------------------------
	// S024-R6.3 — no cache outcome is a warning. A miss is normal, and one line
	// per read across 411 records would bury the warnings that matter.
	// -------------------------------------------------------------------------
	t.Run("no cache outcome is reported as a warning", func(t *testing.T) {
		logs := captureWarnLogs(t)

		c := newBodyCache(8, 8<<20) // tiny per-body cap: also exercises a refused body
		ctx := context.Background()

		okKey := bodyKey("https://example.test/ok", nil)
		var okCalls atomic.Int64
		okFetch := func() ([]byte, error) {
			okCalls.Add(1)
			return []byte("v1"), nil
		}
		if _, err := c.do(ctx, okKey, okFetch); err != nil { // miss
			t.Fatalf("unexpected error: %v", err)
		}
		if _, err := c.do(ctx, okKey, okFetch); err != nil { // hit
			t.Fatalf("unexpected error: %v", err)
		}

		bigKey := bodyKey("https://example.test/big", nil)
		if _, err := c.do(ctx, bigKey, func() ([]byte, error) {
			return []byte(payload), nil // longer than the 8-byte cap: refused, still returned
		}); err != nil {
			t.Fatalf("an over-cap body must still be returned to its caller: %v", err)
		}

		failKey := bodyKey("https://example.test/fail", nil)
		if _, err := c.do(ctx, failKey, func() ([]byte, error) {
			return nil, errIsolationFetch
		}); !errors.Is(err, errIsolationFetch) {
			t.Fatalf("unexpected error: %v", err)
		}

		if lines := logs.all(); len(lines) != 0 {
			t.Errorf("cache outcomes emitted %d warning line(s); none is a warning (S024-R6.3): %v",
				len(lines), lines)
		}
	})
}
