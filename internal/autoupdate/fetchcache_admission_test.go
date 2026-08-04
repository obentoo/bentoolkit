package autoupdate

import (
	"bytes"
	"context"
	"sync/atomic"
	"testing"
)

// =============================================================================
// Story 024 / task 1.4 — retention limits (S024-R4.1, R4.2, R4.3, R6.2)
// =============================================================================

// admissionProbe counts fetches and returns a fixed body. The counter is the
// evidence that a refused body cost a LATER fetch — and only that. A limit that
// changed a result rather than only a request would be the one way this story's
// memory bound could become a correctness bug.
type admissionProbe struct {
	calls atomic.Int64
	body  []byte
}

func newAdmissionProbe(size int, fill byte) *admissionProbe {
	return &admissionProbe{body: bytes.Repeat([]byte{fill}, size)}
}

func (p *admissionProbe) fetch() ([]byte, error) {
	p.calls.Add(1)
	return p.body, nil
}

func (p *admissionProbe) count() int64 { return p.calls.Load() }

// TestBodyCacheAdmission pins both retention limits and the invariant that
// binds them: admission is about MEMORY, never about correctness. A body the
// cache refuses is still handed back to its caller byte for byte; refusing it
// costs one later fetch and nothing else.
func TestBodyCacheAdmission(t *testing.T) {
	ctx := context.Background()

	// -------------------------------------------------------------------------
	// S024-R4.1 / R4.2 — over the per-body cap: returned, not retained.
	// -------------------------------------------------------------------------
	t.Run("a body over the per-body cap is returned but never retained", func(t *testing.T) {
		const perBody = 32
		c := newBodyCache(perBody, 1<<20)
		key := bodyKey("https://example.test/huge", nil)
		probe := newAdmissionProbe(perBody+1, 'A') // one byte over the cap

		first, err := c.do(ctx, key, probe.fetch)
		if err != nil {
			t.Fatalf("an over-cap body must still be returned to its caller: %v", err)
		}
		if !bytes.Equal(first, probe.body) {
			t.Errorf("the returned body was altered: got %d bytes, want the fetched %d", len(first), len(probe.body))
		}

		second, err := c.do(ctx, key, probe.fetch)
		if err != nil {
			t.Fatalf("second read of an over-cap identity: %v", err)
		}
		if !bytes.Equal(second, probe.body) {
			t.Error("the second read returned different bytes than the fetch produced")
		}
		// The defining assertion: nothing was retained, so the read cost a fetch.
		if got := probe.count(); got != 2 {
			t.Errorf("fetch was invoked %d time(s) for two reads of an over-cap identity; want 2 (nothing retained)", got)
		}
		stats := c.snapshot()
		if stats.Oversize != 2 {
			t.Errorf("stats.Oversize = %d, want 2 — the per-body cap must record WHICH limit refused the body (S024-R6.2)", stats.Oversize)
		}
		if stats.BudgetFull != 0 {
			t.Errorf("stats.BudgetFull = %d, want 0 — the total budget was nowhere near exhausted", stats.BudgetFull)
		}
		if stats.Hits != 0 {
			t.Errorf("stats.Hits = %d, want 0 — an unretained body can never produce a hit", stats.Hits)
		}
	})

	// -------------------------------------------------------------------------
	// S024-R4.1 — "at most 2 MiB": a body EXACTLY at the cap is admitted. The
	// boundary is stated as an inclusive bound, so it is pinned as one.
	// -------------------------------------------------------------------------
	t.Run("a body exactly at the per-body cap is retained", func(t *testing.T) {
		const perBody = 32
		c := newBodyCache(perBody, 1<<20)
		key := bodyKey("https://example.test/exact", nil)
		probe := newAdmissionProbe(perBody, 'B')

		if _, err := c.do(ctx, key, probe.fetch); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		body, err := c.do(ctx, key, probe.fetch)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !bytes.Equal(body, probe.body) {
			t.Error("the retained body does not match the fetched bytes")
		}
		if got := probe.count(); got != 1 {
			t.Errorf("fetch was invoked %d time(s); a body at exactly the cap must be retained (want 1)", got)
		}
		if stats := c.snapshot(); stats.Oversize != 0 || stats.Hits != 1 {
			t.Errorf("stats = %+v; want Oversize 0 and Hits 1 for a body admitted at the boundary", stats)
		}
	})

	// -------------------------------------------------------------------------
	// S024-R4.3 — when the budget is reached admission stops, and the bodies
	// already retained are KEPT (no eviction).
	// -------------------------------------------------------------------------
	t.Run("an exhausted budget stops admission and keeps what is already retained", func(t *testing.T) {
		const bodySize = 32
		const budget = 2 * bodySize // room for exactly two bodies
		c := newBodyCache(1<<20, budget)

		keyA := bodyKey("https://example.test/a", nil)
		keyB := bodyKey("https://example.test/b", nil)
		keyC := bodyKey("https://example.test/c", nil)
		probeA := newAdmissionProbe(bodySize, 'a')
		probeB := newAdmissionProbe(bodySize, 'b')
		probeC := newAdmissionProbe(bodySize, 'c')

		for _, step := range []struct {
			key   string
			probe *admissionProbe
		}{{keyA, probeA}, {keyB, probeB}, {keyC, probeC}} {
			if _, err := c.do(ctx, step.key, step.probe.fetch); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		}

		// C arrived with the budget full: returned to its caller unchanged...
		bodyC, err := c.do(ctx, keyC, probeC.fetch)
		if err != nil {
			t.Fatalf("a body refused by the budget must still be returned: %v", err)
		}
		if !bytes.Equal(bodyC, probeC.body) {
			t.Error("the budget-refused body was altered before being returned (S024-R4.2)")
		}
		// ...and not retained, so its second read cost a fetch.
		if got := probeC.count(); got != 2 {
			t.Errorf("fetch for the budget-refused identity was invoked %d time(s), want 2 (never retained)", got)
		}

		// The bodies admitted BEFORE the budget filled are still served without
		// a fetch: admission stops, retention is not undone (S024-R4.3).
		if _, err := c.do(ctx, keyA, probeA.fetch); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, err := c.do(ctx, keyB, probeB.fetch); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := probeA.count(); got != 1 {
			t.Errorf("the first retained identity was fetched %d time(s), want 1 — a full budget must not evict it", got)
		}
		if got := probeB.count(); got != 1 {
			t.Errorf("the second retained identity was fetched %d time(s), want 1 — a full budget must not evict it", got)
		}

		stats := c.snapshot()
		if stats.BudgetFull != 2 {
			t.Errorf("stats.BudgetFull = %d, want 2 — the budget must record WHICH limit refused each body (S024-R6.2)", stats.BudgetFull)
		}
		if stats.Oversize != 0 {
			t.Errorf("stats.Oversize = %d, want 0 — no body here exceeded the per-body cap", stats.Oversize)
		}
		if stats.Hits != 2 {
			t.Errorf("stats.Hits = %d, want 2 — the two admitted identities were re-read from memory", stats.Hits)
		}
	})
}
