package fetch

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

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
