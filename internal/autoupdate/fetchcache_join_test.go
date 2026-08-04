package autoupdate

import (
	"bytes"
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// =============================================================================
// Story 024 / task 1.2 — single-flight join (S024-R2.1, R2.3, R2.4, R6.1)
// =============================================================================

// joinFetchProbe is the fetch func passed to bodyCache.do, instrumented with the
// only counter that can prove this story's claim: how many times the body was
// actually fetched. A test that asserts only "do returned bytes" would pass
// against no cache at all.
//
// The FIRST call blocks until release is closed, so a test can hold the leader
// in flight while the followers arrive and observe a genuine join rather than a
// retained-body hit.
type joinFetchProbe struct {
	calls   atomic.Int64
	entered chan struct{} // closed when the first fetch begins
	release chan struct{} // closed by the test to let the first fetch return
	body    []byte
}

func newJoinFetchProbe(body string) *joinFetchProbe {
	return &joinFetchProbe{
		entered: make(chan struct{}),
		release: make(chan struct{}),
		body:    []byte(body),
	}
}

func (p *joinFetchProbe) fetch() ([]byte, error) {
	if p.calls.Add(1) == 1 {
		close(p.entered)
		<-p.release
	}
	return p.body, nil
}

func (p *joinFetchProbe) count() int64 { return p.calls.Load() }

// TestBodyCacheJoin proves the three arrival states of one key: the first caller
// fetches, callers arriving while that fetch is in flight WAIT for it instead of
// issuing their own (S024-R2.3), and a caller arriving afterwards is served from
// the retained body with no fetch at all (S024-R2.1).
//
// Run under -race: the join is the one piece of shared mutable state this story
// adds.
func TestBodyCacheJoin(t *testing.T) {
	const callers = 8
	const payload = `{"version":"1.2.3"}`

	c := newBodyCache(1<<20, 8<<20)
	probe := newJoinFetchProbe(payload)
	key := bodyKey("https://gitlab.example.test/repository/tags?per_page=20", nil)
	ctx := context.Background()

	bodies := make([][]byte, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup

	// The leader arrives first and is held inside fetch.
	wg.Add(1)
	go func() {
		defer wg.Done()
		bodies[0], errs[0] = c.do(ctx, key, probe.fetch)
	}()

	select {
	case <-probe.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the first caller never entered fetch; do did not invoke the fetch func")
	}

	// The followers arrive while the leader's fetch is still in flight.
	for i := 1; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			bodies[i], errs[i] = c.do(ctx, key, probe.fetch)
		}(i)
	}

	// Give every follower time to reach its wait. Without this pause a follower
	// could arrive after the leader completed and be counted as a Hit rather
	// than a Join — the same body either way, but a weaker assertion below.
	time.Sleep(150 * time.Millisecond)
	close(probe.release)
	wg.Wait()

	// The defining assertion: ONE fetch served all eight readers.
	if got := probe.count(); got != 1 {
		t.Fatalf("fetch was invoked %d time(s) for %d concurrent readers of one identity; want exactly 1", got, callers)
	}

	for i := 0; i < callers; i++ {
		if errs[i] != nil {
			t.Fatalf("caller %d returned an unexpected error: %v", i, errs[i])
		}
		if !bytes.Equal(bodies[i], []byte(payload)) {
			t.Errorf("caller %d received %q, want the shared body %q", i, bodies[i], payload)
		}
	}

	// S024-R6.1: the counters are the per-run evidence of the saving.
	stats := c.snapshot()
	if stats.Misses != 1 {
		t.Errorf("stats.Misses = %d, want 1 (exactly one caller became the leader)", stats.Misses)
	}
	if stats.Joins != callers-1 {
		t.Errorf("stats.Joins = %d, want %d (every follower waited on the in-flight fetch)", stats.Joins, callers-1)
	}
	if stats.Hits != 0 {
		t.Errorf("stats.Hits = %d, want 0 (no body was retained yet when the followers arrived)", stats.Hits)
	}

	// A caller arriving after the fetch completed is served from the retained
	// body: no fetch, and the counter proves it.
	late, err := c.do(ctx, key, probe.fetch)
	if err != nil {
		t.Fatalf("the late caller returned an unexpected error: %v", err)
	}
	if !bytes.Equal(late, []byte(payload)) {
		t.Errorf("the late caller received %q, want the retained body %q", late, payload)
	}
	if got := probe.count(); got != 1 {
		t.Errorf("fetch was invoked %d time(s) after a completed fetch; a retained body must cost no fetch", got)
	}
	if stats := c.snapshot(); stats.Hits != 1 {
		t.Errorf("stats.Hits = %d after a read served from the retained body, want 1", stats.Hits)
	}

	// Two identities never couple: a second key fetches on its own and gets its
	// own bytes back.
	var otherCalls atomic.Int64
	const otherPayload = `{"version":"9.9.9"}`
	otherKey := bodyKey("https://gitlab.example.test/repository/other/tags", nil)
	otherBody, err := c.do(ctx, otherKey, func() ([]byte, error) {
		otherCalls.Add(1)
		return []byte(otherPayload), nil
	})
	if err != nil {
		t.Fatalf("the second identity returned an unexpected error: %v", err)
	}
	if got := otherCalls.Load(); got != 1 {
		t.Errorf("the second identity invoked fetch %d time(s), want exactly 1", got)
	}
	if !bytes.Equal(otherBody, []byte(otherPayload)) {
		t.Errorf("the second identity received %q, want %q — bodies were crossed between keys", otherBody, otherPayload)
	}
}
