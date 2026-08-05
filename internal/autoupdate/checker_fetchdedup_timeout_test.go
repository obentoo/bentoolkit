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
// Story 024 / task 2.1 — a follower fetches under ITS OWN budget
// (S024-R3.3, S024-R3.4)
// =============================================================================

// TestCheckerFetchDedupFollowerKeepsItsOwnBudget constructs the one scenario
// R3.3 and R3.4 actually describe, and that no other test in this story builds:
// two callers of a single identity declaring DIFFERENT per-operation timeouts,
// where the short-budget leader times out and the long-budget follower then has
// to succeed on its own.
//
// The cross-review found this gap, and it is worth stating why it matters. A
// follower that wrongly reused the leader's context would inherit an ALREADY
// EXPIRED deadline and fail instantly — and it would still pass every other
// test in this story, because every one of them gives all callers the same
// budget and a leader that succeeds. The defect only becomes visible when the
// two budgets differ and the short one loses.
//
// Latent rather than urgent, and recorded as such: measurement M4 found zero
// registry records declaring `timeout`, so today every caller resolves to the
// same global budget. This test exists so the day one record declares one is
// not the day the behaviour is discovered.
//
// The assertions are anchored on the CACHE COUNTERS rather than on elapsed
// time. A timing-based test of "the follower got its full budget" would be
// flaky on a loaded machine; `Joins` and `Refetches` say the same thing
// exactly, and say it about the code path rather than about the clock.
func TestCheckerFetchDedupFollowerKeepsItsOwnBudget(t *testing.T) {
	const payload = `{"version":"7.0.0"}`

	// The leader's budget is short enough to expire while it is parked in the
	// handler; the follower's is long enough that only a REUSED, already-expired
	// deadline could make it fail.
	const leaderBudget = 250 * time.Millisecond
	const followerBudget = 30 * time.Second

	var requests atomic.Int64
	leaderArrived := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseServer := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseServer)

	// Every request parks until the test releases it. Parking ALL of them — not
	// only the first — is what makes this robust against the retry client: the
	// leader's retries park and expire exactly like its first attempt, so the
	// leader cannot accidentally succeed on attempt two.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			close(leaderArrived)
		}
		select {
		case <-release:
		case <-r.Context().Done():
			return // this caller's own budget ran out; write nothing
		}
		fmt.Fprint(w, payload)
	}))
	defer server.Close()

	limiter := &recordingRateLimiter{}
	checker := newRateLimitTestChecker(t, server.URL,
		WithRateLimiter(limiter),
		WithContext(context.Background()),
	)

	var leaderErr, followerErr error
	var followerBody []byte
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		_, leaderErr = checker.fetchContent(server.URL, nil, leaderBudget)
	}()

	select {
	case <-leaderArrived:
	case <-time.After(10 * time.Second):
		t.Fatal("the leader's request never reached the server")
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		followerBody, followerErr = checker.fetchContent(server.URL, nil, followerBudget)
	}()

	// Prove the follower genuinely JOINED the in-flight fetch. Without this the
	// test could pass by scheduling luck — a follower that arrived after the
	// leader had already failed would simply become a leader itself and would
	// have exercised nothing this test exists to check.
	awaitCounter(t, checker, "Joins", func(s bodyCacheStats) int { return s.Joins },
		"the follower never joined the leader's in-flight fetch; the scenario under test did not happen")

	// The follower has left the join and is issuing its OWN request, so the
	// server may stop parking. Waiting for this counter rather than sleeping is
	// what keeps the handoff deterministic.
	awaitCounter(t, checker, "Refetches", func(s bodyCacheStats) int { return s.Refetches },
		"the leader's failure never released the follower to fetch on its own")
	releaseServer()

	wg.Wait()

	// The leader spent its own short budget and got nothing. Its error is not
	// matched with errors.Is: the retry client wraps a final failure with
	// ErrMaxRetriesExceeded using %v rather than %w, so the sentinel does not
	// survive that path. That the leader failed at all is the whole premise
	// here; which sentinel it carries is story 019's concern, not this one.
	if leaderErr == nil {
		t.Fatal("the leader was expected to exhaust its short budget and fail, got nil")
	}

	// THE DEFINING ASSERTION (S024-R3.4). A follower that reused the leader's
	// expired context fails here and nowhere else in this story.
	if followerErr != nil {
		t.Fatalf("the follower returned %v — it must issue its own request under its OWN full "+
			"budget (%s), not under the leader's expired one (%s)", followerErr, followerBudget, leaderBudget)
	}
	if !bytes.Equal(followerBody, []byte(payload)) {
		t.Errorf("the follower received %q, want %q", followerBody, payload)
	}

	// Exactly one caller left the join to fetch alone: the follower, once
	// (S024-R3.1's "at most once per do call").
	if stats := checker.bodies.snapshot(); stats.Refetches != 1 {
		t.Errorf("stats.Refetches = %d, want 1 — one follower, released once, fetching once", stats.Refetches)
	}
}

// awaitCounter blocks until the named cache counter is at least 1, failing the
// test with reason if it never is. It polls rather than hooks because the
// counters are the only observation point the cache offers, and a poll on an
// in-memory integer costs nothing beside a network round trip.
func awaitCounter(t *testing.T, c *Checker, name string, read func(bodyCacheStats) int, reason string) {
	t.Helper()

	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		if read(c.bodies.snapshot()) >= 1 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("%s (waited 10s for stats.%s to reach 1)", reason, name)
}
