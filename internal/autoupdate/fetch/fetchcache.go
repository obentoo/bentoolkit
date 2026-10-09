package fetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/textproto"
	"sort"
	"strings"
	"sync"
)

// -----------------------------------------------------------------------------
// Fetch identity
// -----------------------------------------------------------------------------

// KeySeparator joins the raw URL to the header digest inside a cache key. A NUL
// byte is used because no URL can contain one — net/url rejects it outright
// ("invalid control character in URL"), so no URL can spell the boundary itself
// and impersonate another identity's header term.
const KeySeparator = "\x00"

// BodyKey derives the identity two upstream reads must share before either may
// be handed the other's bytes: rawURL + NUL + hex(sha256(canonicalHeaders(headers))).
// Two reads share a key only when the requests they would issue are
// byte-identical. The URL is carried verbatim, so identity follows the URL
// actually requested and no registry edit can leave the key disagreeing with it.
//
// Collapsing wrongly hands one entry another entry's bytes. Range is the sharp
// edge: the checker accepts a 206, and a 206 fragment handed to a caller that
// asked for the whole file looks like a successful read of a truncated document.
// Keying on the declared headers makes that impossible by construction.
//
// The header term is hashed so a key is safe to log at DEBUG by construction
// (today ${VAR} placeholders are still unexpanded here). Deliberately absent from
// the key: the client's default headers (fixed per Checker), the GitHub token (a
// function of the URL, which is in the key) and ${VAR} expansions (the
// environment is fixed mid-run). A per-package User-Agent, per-package tokens or
// live expansion would force the key onto the effective header set applyHeaders
// produces. A nil map is a valid "no headers declared".
func BodyKey(rawURL string, headers map[string]string) string {
	digest := sha256.Sum256([]byte(canonicalHeaders(headers)))
	return rawURL + KeySeparator + hex.EncodeToString(digest[:])
}

// canonicalHeaders renders a declared header map as sorted "Name: value\n"
// lines, so two records that wrote the same headers differently resolve to a
// single fetch.
//
// The NAME is trimmed and passed through textproto.CanonicalMIMEHeaderKey, as
// setHeader does when it builds the request. The trim is load-bearing:
// CanonicalMIMEHeaderKey leaves input with a leading space untouched, so
// " accept" would otherwise key apart from "Accept". The ORDER is fixed by
// sorting whole lines, because Go randomises map iteration; sorting lines rather
// than names keeps a map holding two spellings of one name stable. The VALUE is
// never touched: "Application/JSON" and "application/json" are different bytes
// on the wire. Nil and empty maps both render to "", one key for "no headers".
//
// Known and left open: a value containing a newline could collide with a
// two-header map, but net/http refuses to send such a request, so the colliding
// record could never fetch anything to be confused about.
func canonicalHeaders(headers map[string]string) string {
	// len covers nil and empty alike, which is what collapses "no headers" onto
	// a single representation and therefore a single key.
	if len(headers) == 0 {
		return ""
	}

	lines := make([]string, 0, len(headers))
	for name, value := range headers {
		canonical := textproto.CanonicalMIMEHeaderKey(strings.TrimSpace(name))
		lines = append(lines, canonical+": "+value+"\n")
	}
	sort.Strings(lines)

	return strings.Join(lines, "")
}

// -----------------------------------------------------------------------------
// Single-flight join
// -----------------------------------------------------------------------------

// bodyEntry is one key's state. done is closed exactly once: by the leader — the
// caller that found the key absent and therefore issued the fetch — or, for an
// entry published by retain, before that entry is ever reachable, so nobody can
// wait on it.
//
// An entry only ever survives its leader when the body was admitted: a failure
// and a refused admission both delete the key. So a waiter released with
// ok == true always finds a usable body, and ok == false always means "fetch it
// yourself" — never "the request you wanted failed", which would make one
// record's outcome depend on another's.
//
// body and ok are written once, by the leader, before close(done), and are read
// only after done has been observed closed — either by receiving from it or by
// finding it closed under the cache mutex. Both observations are ordered after
// the close, and the close is ordered after the writes, so the two fields are
// safely published without having to be re-guarded at every read.
type bodyEntry struct {
	done chan struct{}
	body []byte // the admitted body; nil when the fetch failed
	ok   bool   // false = the leader failed; the waiter must fetch on its own
}

// settled reports whether the leader has finished with this entry, i.e. whether
// done is already closed. The read is non-blocking, which is what makes it safe
// to call while the cache mutex is held: it distinguishes the "done" state from
// the "in flight" one without ever waiting for the fetch it is asking about.
func (e *bodyEntry) settled() bool {
	select {
	case <-e.done:
		return true
	default:
		return false
	}
}

// BodyCacheStats is DEBUG-level observability, and the assertion surface tests
// use to prove a request was or was not issued. Counts are per bodyCache, and a
// bodyCache lives exactly as long as one Checker, so these are per-run figures
// that are never carried across invocations.
type BodyCacheStats struct {
	Hits       int // served from a retained body
	Joins      int // waited on an in-flight fetch
	Misses     int // became a leader and fetched
	Refetches  int // a leader failed, so a waiter fetched on its own
	Oversize   int // fetched, returned, not retained (per-body cap)
	BudgetFull int // fetched, returned, not retained (total budget)
}

// BodyCache deduplicates response bodies within the lifetime of one Checker —
// that is, one command invocation. It is not persisted and has no TTL: a run is
// short enough that an upstream endpoint is treated as immutable for its
// duration, which is the same assumption every concurrent HTTP client makes.
//
// It knows nothing about HTTP, packages or the registry: it is handed a key and
// a function that produces bytes. That is what keeps its concurrency contract
// provable without a server, and what keeps the two fetch paths that must never
// be cached — multi-megabyte distfiles and the analyzer's one-shot reads — out
// of reach by construction, since neither goes through a Checker's fetch door.
type BodyCache struct {
	mu      sync.Mutex
	entries map[string]*bodyEntry
	perBody int64 // largest body admitted, in bytes
	budget  int64 // total bytes admitted before admission stops
	used    int64 // bytes retained so far; charged only by admitLocked, never repaid
	stats   BodyCacheStats
}

// newBodyCache returns an empty cache with the given retention limits, in bytes.
// DefaultFetchCachePerBody and DefaultFetchCacheBudget are the figures a real
// run passes; a test passes its own so a boundary can be crossed in a few dozen
// bytes instead of a few million.
//
// The limits bound RETENTION only: neither can make a fetch fail or be skipped,
// so a cache built with limits of zero still deduplicates the callers that
// arrive while a fetch is in flight — it simply keeps nothing of substance once
// that fetch returns.
func newBodyCache(perBody, budget int64) *BodyCache {
	return &BodyCache{
		entries: make(map[string]*bodyEntry),
		perBody: perBody,
		budget:  budget,
	}
}

// Snapshot returns the per-run counters by value. The copy is deliberate and is
// taken under the mutex: handing out a pointer into live state would let a
// reader observe counters torn mid-update, and would let a caller mutate the
// figures the run is reporting.
func (c *BodyCache) Snapshot() BodyCacheStats {
	c.mu.Lock()
	defer c.mu.Unlock()

	// bodyCacheStats is all value fields, so this assignment is the copy.
	return c.stats
}

// Do returns the body for key, fetching it at most once across concurrent
// callers: the first caller to arrive leads and calls fetch, callers arriving
// while it is in flight wait for its result, and later callers are served the
// retained body with no fetch and no rate-limit token.
//
// THIS FUNCTION TAKES NO LOCK, and the mutex is never held across fetch or the
// wait: a lock held there would pass every test while serialising all records
// behind one network round trip. Each locking helper (classify, publish,
// countRefetch, retain) is short and holds neither a fetch nor a wait. The wait
// is bounded by the parent ctx, never a per-operation timeout: queue time charged
// to an HTTP deadline once failed packages before any request was issued.
//
// An error is neither cached nor shared: a waiter released by a failed leader
// fetches on its own. No outcome is logged — a cold cache misses on every read;
// the counters are the observability surface, and the caller logs its failures.
//
// THE RETURNED SLICE IS SHARED AND READ-ONLY: a mutation would silently make one
// package report another's version, and copying per waiter would restore the
// memory cost the cache removes.
func (c *BodyCache) Do(ctx context.Context, key string, fetch func() ([]byte, error)) ([]byte, error) {
	entry, how := c.classify(key)

	switch how {
	case arriveLead:
		return c.lead(key, entry, fetch)

	case arriveHit:
		return entry.body, nil

	case arriveWait:
		select {
		case <-entry.done:
		case <-ctx.Done():
			// The run itself is over — a SIGINT, or the parent deadline. Return
			// WITHOUT issuing a request of our own, and carry the
			// RAW context error as the cause rather than replacing it, exactly
			// as the rate-limiter wait does at checker.go:1692. The wrapper says
			// what was being waited for; errors.Is(err, context.Canceled) and
			// errors.Is(err, context.DeadlineExceeded) keep holding for callers
			// regardless of how that sentence is phrased.
			return nil, fmt.Errorf("cancelled while waiting for an in-flight fetch of the same identity: %w", ctx.Err())
		}
		if entry.ok {
			return entry.body, nil
		}
		// The leader failed. Its error is not ours, so fall through and fetch
		// rather than rejoining: N callers of a permanently failing identity
		// then produce exactly N fetches instead of a number that depends on
		// which goroutine happened to arrive when.

	case arriveAlone:
	}

	// arriveAlone, and a waiter released by a failed leader.
	return c.fetchAlone(key, fetch)
}

// arrival names what a caller found when it looked its key up: the design's
// three states, plus the one guard on their invariant. Naming them turns do
// into a dispatch, which is what keeps every mutex-guarded region in a helper of
// its own — classify here, publish, countRefetch and retain below — so "the lock
// spans neither the fetch nor the wait" is checked by reading four short
// functions instead of by tracing every return path in do.
type arrival int

const (
	arriveLead  arrival = iota // absent: this caller fetches on everyone's behalf
	arriveWait                 // in flight: wait on the leader's channel
	arriveHit                  // done: the retained body is ready, no fetch, no token
	arriveAlone                // no join is available: fetch without one
)

// classify looks key up, counts the arrival, and — when the key is absent —
// publishes an in-flight entry so that every later arrival joins instead of
// fetching. Publishing before the lock is dropped is what makes the join work
// at all.
//
// This is the only place the cache mutex is taken to classify an arrival, and it
// is taken with a deferred unlock: no return path can leak the lock, and nothing
// slow can be introduced inside it without that being obvious, because there is
// no fetch and no wait anywhere in this function to hide behind.
func (c *BodyCache) classify(key string) (*bodyEntry, arrival) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, known := c.entries[key]
	if !known {
		entry = &bodyEntry{done: make(chan struct{})}
		c.entries[key] = entry
		c.stats.Misses++
		return entry, arriveLead
	}

	if !entry.settled() {
		c.stats.Joins++
		return entry, arriveWait
	}

	// A settled entry still reachable through the map always carries a usable
	// body, because publish deletes the key and closes done inside ONE critical
	// section: a failure is gone from the map before any other caller can look.
	// This branch is therefore a guard on that invariant, not a path today's
	// code can take. It is kept because it costs one comparison and turns a
	// future violation into a redundant fetch instead of a nil body returned
	// with a nil error — a wrong version reported without one loud symptom.
	if !entry.ok {
		return nil, arriveAlone
	}

	c.stats.Hits++
	return entry, arriveHit
}

// errFetchPanicked is the outcome recorded for an entry whose leader panicked.
// It is never returned to anybody: the panic is not recovered here, so it keeps
// unwinding — with its original stack intact — past the publication, out of do,
// and into CheckAll's per-package recover (checker.go:1859-1865), which records
// it as that one package's failure exactly as it did before this cache existed.
// Recovering and re-panicking would reach the same handler while replacing the
// stack that says where the panic actually came from.
//
// The value exists so that publish's failure branch is entered by NAMING what
// happened rather than by passing a bare non-nil error. What must never happen
// instead is publishing ok == true over a nil body: that is a wrong answer
// returned with no error at all, which is strictly worse than the block it would
// be curing.
var errFetchPanicked = errors.New("the leading fetch panicked")

// lead performs the single fetch for key and publishes its outcome to every
// caller waiting on entry.
//
// The fetch runs with the cache mutex RELEASED, which is the entire point: it is
// a network call bounded by a per-operation timeout and preceded by a rate-limit
// token that can take seconds to acquire, and holding a process-wide lock across
// it would serialise every other key in the run behind this one — reinstating
// the queue this cache exists to remove.
//
// The deferred publication is the guarantee that no waiter can outlive its
// leader. A panic inside fetch — from the fetch itself or from
// anything it calls — would otherwise unwind straight past the publication and
// leave every waiter blocked on a channel nobody will ever close: not a wrong
// answer but a hung run, which is worse, because a hung run reports nothing at
// all. The guard releases them as a FAILURE, so each fetches on its own and the
// panic costs exactly one package's result.
func (c *BodyCache) lead(key string, entry *bodyEntry, fetch func() ([]byte, error)) ([]byte, error) {
	// Set immediately after the normal publication, so the deferred one runs
	// only when fetch never returned — never twice, which would close a closed
	// channel and turn a recoverable panic into a fatal one.
	published := false
	defer func() {
		if !published {
			c.publish(key, entry, nil, errFetchPanicked)
		}
	}()

	body, err := fetch()
	c.publish(key, entry, body, err)
	published = true

	if err != nil {
		return nil, err
	}
	return body, nil
}

// fetchAlone is lead's counterpart: the caller fetches for itself rather than
// for everyone. It is the one path that leaves the join, taken by a waiter a
// failed leader released and by the arriveAlone guard, and it is deliberately
// the uncached behaviour byte for byte — sharing is an optimisation, and where
// it does not apply, nothing else changes.
//
// The count is taken once, before the fetch and whatever the fetch then returns,
// because this function is called exactly once per do call. That is what makes
// the figure deterministic rather than a range: see countRefetch.
func (c *BodyCache) fetchAlone(key string, fetch func() ([]byte, error)) ([]byte, error) {
	c.countRefetch()

	body, err := fetch()
	if err != nil {
		return nil, err
	}

	// A direct fetch that succeeds is still retained under the normal rules, so
	// one failure does not turn an identity into a permanent bypass in which
	// every later caller refetches although a good body is already in hand.
	c.retain(key, body)
	return body, nil
}

// countRefetch records that a caller left the join to issue a request of its
// own, because the entry it found had FAILED: the leader's error is not the
// waiter's, so the waiter pays for its own fetch.
//
// It is called at most once per do call, which is a contract rather than an
// implementation detail. A waiter that could rejoin — loop back into do and wait
// on a second leader — might be released by several failures in turn and counted
// several times, so with one failing leader and three waiters both the fetch
// count (2 to 4) and the refetch count (3 to 6) would depend on which goroutine
// happened to wake first. Counting once, on the one path that leaves the join,
// makes N callers of a permanently failing identity produce exactly N fetches
// and N-1 refetches, every time.
func (c *BodyCache) countRefetch() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.stats.Refetches++
}

// retain publishes a body that was fetched OUTSIDE the join — by a caller a
// failed leader released — so that later readers of the same identity are served
// from it. Without this, a single failure would turn an identity into a
// permanent bypass: every caller after it would fetch on its own even though a
// good body was already in hand.
//
// The entry is inserted ALREADY SETTLED — done is closed before the entry is
// reachable — so no caller can ever wait on it, and classify finds a finished
// entry carrying a usable body.
//
// It inserts ONLY when the key is absent, and that is a correctness rule rather
// than an optimisation. The slot may meanwhile belong to a leader that is still
// in flight; overwriting it would leave that leader publishing into an entry the
// map no longer holds, and its failure path would then delete a key it no longer
// owns — discarding this good body. Skipping costs nothing worth having: whoever
// holds the slot is either fetching this very identity or already retains it.
func (c *BodyCache) retain(key string, body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, taken := c.entries[key]; taken {
		return
	}
	if !c.admitLocked(body) {
		return
	}

	done := make(chan struct{})
	close(done)
	c.entries[key] = &bodyEntry{done: done, body: body, ok: true}
}

// The retention limits a real run uses, in bytes. They bound MEMORY, never
// correctness: a body over a limit is still fetched and returned to its caller
// unchanged, it is simply not kept.
//
// Both figures come from measuring the registry:
//
//   - 2 MiB per body — the most widely shared body, the 170-way GStreamer tags
//     listing, measures 33.7 KB, some sixty times under the cap. The largest
//     payload, www-misc/warsaw at ~8.2 MB, is declared by exactly ONE record, so
//     retaining it would save no fetch while costing more than the rest combined.
//   - 64 MiB total — the 230 distinct URLs at tens of KB each land near ~10 MB,
//     so this is a ceiling on pathology, not a working figure.
//
// They are comfortable rather than tuned: confirm them against a real run's
// counters (Oversize and BudgetFull staying at zero) before treating them as
// settled.
const (
	// DefaultFetchCachePerBody is the largest response body a run will retain
	// for reuse, in bytes: 2 MiB (2097152).
	DefaultFetchCachePerBody int64 = 2 << 20

	// DefaultFetchCacheBudget is the total number of bytes a run will retain
	// across all identities before admission stops, in bytes: 64 MiB
	// (67108864).
	DefaultFetchCacheBudget int64 = 64 << 20
)

// NewDefaultBodyCache builds the cache a real run uses, at the limits above.
//
// It exists so the two places that install one — NewChecker's struct literal and
// WithFetchCache(true) — cannot drift apart. "Enabled explicitly" has to mean
// exactly "enabled by default"; with the limits spelled out at both sites that
// would hold only by coincidence, and a future edit to one argument list would
// make an option that claims to be the default quietly install something else.
func NewDefaultBodyCache() *BodyCache {
	return newBodyCache(DefaultFetchCachePerBody, DefaultFetchCacheBudget)
}

// admitLocked reports whether a freshly fetched body may be RETAINED for callers
// that have not arrived yet, and charges it against the run budget when it may.
// Both retention paths (publish and retain) ask it, so the limits have one home.
//
// ADMISSION IS ABOUT MEMORY, NEVER CORRECTNESS: the body is already on its way
// back to its caller byte for byte, and a refusal costs one later fetch of that
// identity — which is why call sites express it by forgetting the key.
//
// Both bounds are INCLUSIVE ("at most"). The per-body cap is asked FIRST, so a
// body breaking both is recorded as Oversize: that limit would refuse it even in
// an empty cache, so it is the one an operator would have to change.
//
// THERE IS NO EVICTION: a run is bounded, and evicting the 170-way GStreamer body
// for a one-off would trade 169 avoided fetches for one. Nor is there a latch: a
// smaller body that fits after a refusal is still admitted, so the outcome does
// not depend on the order records are checked in.
//
// The caller holds c.mu, so two admissions cannot both take the last free bytes.
func (c *BodyCache) admitLocked(body []byte) bool {
	// int is never wider than int64 on any platform Go targets, so this
	// conversion widens and cannot overflow. Neither can the sum below: used is
	// bounded by budget and size by perBody, both far under int64's range.
	size := int64(len(body))

	if size > c.perBody {
		c.stats.Oversize++
		return false
	}
	if c.used+size > c.budget {
		c.stats.BudgetFull++
		return false
	}

	c.used += size
	return true
}

// publish records the leader's outcome on entry and releases everyone waiting
// on it. It runs under the mutex, including close(entry.done), so that an
// entry's terminal state and the signal announcing it become visible together:
// a caller holding the mutex sees either an unsettled entry or a settled one
// whose body and ok are already written, never a half-published one. Closing a
// channel never blocks, so holding the lock across it costs nothing.
func (c *BodyCache) publish(key string, entry *bodyEntry, body []byte, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err != nil {
		// A failure is neither retained nor shared.
		// Removing the key leaves the next arrival to become a leader in turn
		// rather than inherit a corpse, and leaves the waiters, released with ok
		// still false, to fetch on their own — so no entry ever fails because of
		// another entry. Note what is deliberately NOT done here: entry.body is
		// left nil AND entry.ok is left false, because an entry that claimed ok
		// over a nil body would be a wrong answer carrying no error at all.
		delete(c.entries, key)
	} else {
		// The body reaches the leader and every waiter either way — they all
		// already hold this entry. Admission decides only whether it is KEPT for
		// callers who have not arrived yet; a refused body is still returned
		// unchanged, and the key goes so that a later read fetches again rather
		// than finding an entry nobody kept anything in.
		entry.body = body
		entry.ok = true
		if !c.admitLocked(body) {
			delete(c.entries, key)
		}
	}

	close(entry.done)
}
