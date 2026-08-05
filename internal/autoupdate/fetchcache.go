// Package autoupdate provides per-run deduplication of upstream response bodies.
package autoupdate

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
// Fetch identity (S024-R1.1 … S024-R1.4)
// -----------------------------------------------------------------------------

// keySeparator joins the raw URL to the header digest inside a cache key. A NUL
// byte is used because no URL can contain one — net/url rejects it outright
// ("invalid control character in URL"), so no URL can spell the boundary itself
// and impersonate another identity's header term.
const keySeparator = "\x00"

// bodyKey derives the identity that two upstream reads must share before either
// may be handed the other's bytes: the URL about to be requested, plus a digest
// of the headers the record declared (S024-R1.1). Two reads land on one key only
// when the requests they would issue are byte-identical; anything else gets its
// own key, and therefore its own fetch.
//
// The shape is rawURL + NUL + hex(sha256(canonicalHeaders(headers))). The URL is
// carried verbatim rather than hashed because identity is derived from the URL
// that is actually about to be requested: no record has to declare that it
// shares a source with another record, and no registry edit can leave the key
// disagreeing with reality (S024-R1.4).
//
// Getting this wrong in the collapsing direction is not a lost optimisation — it
// is one entry silently receiving another entry's bytes. The Range case is the
// sharp edge. A record may ask for a window near the front of a large file, and
// the checker deliberately accepts the resulting 206 (S020-R1.1). A 206 fragment
// handed to a caller that asked for the whole file does not look like a failure;
// it looks like a successful read of a truncated document, and every downstream
// parser would treat it as one. Keying on the declared headers makes that
// impossible by construction (S024-R1.3).
//
// The header term is hashed so that a key is safe to log at DEBUG. Today
// cfg.Headers holds the literal TOML text with ${VAR} placeholders still
// unexpanded — SubstituteEnvVars runs later, inside applyHeaders, at
// request-build time — so no key contains a credential. Hashing removes the need
// to keep re-verifying that as the registry grows: safe by construction rather
// than by care.
//
// Deliberately absent from the key, each with the tripwire that would force it in:
//
//   - the client's default headers (User-Agent): fixed for the Checker's whole
//     lifetime, so they cannot distinguish two reads. Tripwire: a per-package
//     User-Agent override.
//   - the GitHub token: applied only when isGitHubAPIURL(url) holds, which is a
//     function of the URL, and the URL IS in the key — so the token cannot vary
//     while the key stays put. Tripwire: per-package tokens.
//   - ${VAR} expansions: the environment does not change mid-run, so the
//     unexpanded text and the expanded value are in one-to-one correspondence.
//     Tripwire: expansion from a live source.
//
// If any tripwire fires, this key must be rebuilt from the *effective* header set
// that applyHeaders produces, not from the declared one.
//
// bodyKey has no error path: every input is already a string, and a nil map is a
// valid way to say "this record declares no headers".
func bodyKey(rawURL string, headers map[string]string) string {
	digest := sha256.Sum256([]byte(canonicalHeaders(headers)))
	return rawURL + keySeparator + hex.EncodeToString(digest[:])
}

// canonicalHeaders renders a declared header map as sorted "Name: value\n"
// lines. It is what makes two records that wrote the same headers differently
// resolve to a single fetch (S024-R1.2).
//
// Exactly two things are normalised:
//
//   - the NAME is trimmed and passed through textproto.CanonicalMIMEHeaderKey,
//     the same pair of steps setHeader applies when it builds the request, so a
//     map spelling a header "accept" keys identically to one spelling it
//     "Accept". The trim is load-bearing rather than cosmetic:
//     CanonicalMIMEHeaderKey returns its input untouched when it contains a
//     character no header name may hold, and a leading space is such a
//     character — so " accept" would otherwise key apart from "Accept" while
//     producing the very same request.
//   - the ORDER is fixed by sorting the finished lines, because Go randomises
//     map iteration. Without the sort the same map yields a different key on
//     nearly every call, which would defeat the cache rather than corrupt it.
//
// The VALUE is opaque and is never touched: "Application/JSON" and
// "application/json" are different bytes on the wire, so they are different
// fetches. Sorting whole lines rather than names also keeps the rendering stable
// in the pathological case where one map holds two spellings of one header name.
//
// A nil map and an empty map both render to the empty string, so "no headers
// declared" has exactly one representation and therefore exactly one key.
//
// Known, and deliberately left open: a value containing a newline could spell a
// second line and so collide with a two-header map. It is not a body-confusion
// vector, because net/http refuses to issue such a request at all ("invalid
// header field value"), so the colliding record could never have fetched
// anything of its own to be confused about. A length-prefixed encoding would
// close it, at the cost of a canonical form no one can read.
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
// Single-flight join (S024-R2.1, S024-R2.3, S024-R2.4, S024-R6.1)
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
// record's outcome depend on another's (S024-R3.1).
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

// bodyCacheStats is DEBUG-level observability, and the assertion surface tests
// use to prove a request was or was not issued (S024-R6.1). Counts are per
// bodyCache, and a bodyCache lives exactly as long as one Checker, so these are
// per-run figures that are never carried across invocations (S024-R2.4).
type bodyCacheStats struct {
	Hits       int // served from a retained body
	Joins      int // waited on an in-flight fetch
	Misses     int // became a leader and fetched
	Refetches  int // a leader failed, so a waiter fetched on its own
	Oversize   int // fetched, returned, not retained (per-body cap)
	BudgetFull int // fetched, returned, not retained (total budget)
}

// bodyCache deduplicates response bodies within the lifetime of one Checker —
// that is, one command invocation. It is not persisted and has no TTL: a run is
// short enough that an upstream endpoint is treated as immutable for its
// duration, which is the same assumption every concurrent HTTP client makes
// (S024-R2.4).
//
// It knows nothing about HTTP, packages or the registry: it is handed a key and
// a function that produces bytes. That is what keeps its concurrency contract
// provable without a server, and what keeps the two fetch paths that must never
// be cached — multi-megabyte distfiles and the analyzer's one-shot reads — out
// of reach by construction, since neither goes through a Checker's fetch door.
type bodyCache struct {
	mu      sync.Mutex
	entries map[string]*bodyEntry
	perBody int64 // largest body admitted, in bytes
	budget  int64 // total bytes admitted before admission stops
	used    int64 // bytes retained so far; charged only by admitLocked, never repaid
	stats   bodyCacheStats
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
func newBodyCache(perBody, budget int64) *bodyCache {
	return &bodyCache{
		entries: make(map[string]*bodyEntry),
		perBody: perBody,
		budget:  budget,
	}
}

// snapshot returns the per-run counters by value. The copy is deliberate and is
// taken under the mutex: handing out a pointer into live state would let a
// reader observe counters torn mid-update, and would let a caller mutate the
// figures the run is reporting.
func (c *bodyCache) snapshot() bodyCacheStats {
	c.mu.Lock()
	defer c.mu.Unlock()

	// bodyCacheStats is all value fields, so this assignment is the copy.
	return c.stats
}

// do returns the body for key, fetching it at most once across concurrent
// callers (S024-R2.1). fetch is invoked by the first caller to arrive; callers
// that arrive while that fetch is in flight wait for its result instead of
// issuing their own (S024-R2.3); callers that arrive after it completed are
// served from the retained body with no fetch and no rate-limit wait.
//
// Three states per key, and the transitions are the whole contract:
//
//	absent    -> become the leader: publish an in-flight entry, call fetch,
//	             then admit or discard the body
//	in flight -> wait on the leader's channel, bounded by ctx
//	done      -> return the retained bytes, no fetch, no token
//
// THE MUTEX IS NEVER HELD ACROSS fetch, AND NEVER ACROSS THE WAIT. It guards
// only the classification (classify) and the publication (publish), both of
// which are map operations measured in nanoseconds. This is the one property no
// assertion in this package can catch: a do that held the lock across the call
// would satisfy every test — one fetch, shared bytes, correct counters — while
// serialising all 411 records behind a single 6-second network round trip,
// which is precisely the queue this cache exists to remove. So the code is
// arranged to make it checkable rather than merely asserted: THIS FUNCTION
// TAKES NO LOCK AT ALL, and every helper that does — classify, publish,
// countRefetch, retain — is small enough to read whole and contains neither a
// fetch nor a wait.
//
// The wait is bounded by ctx alone — the parent context, never the caller's
// per-operation timeout. That is the policy the rate-limiter wait already
// follows, and for the same reason: waiting behind another caller is queue time,
// and charging queue time to an HTTP deadline is what once made packages sharing
// a host fail before a single request had been issued.
//
// A shared fetch is an optimisation, never a coupling: an error is neither
// cached nor shared, so a waiter released by a failed leader fetches on its own
// rather than inheriting a failure it did not cause (S024-R3.1).
//
// NO OUTCOME HERE IS EVER LOGGED — not a miss, not a failure, not a refetch
// (S024-R6.3). A miss is the normal state of a cold cache, and one line per read
// across 411 records would bury the warnings that matter. The counters in
// bodyCacheStats are the whole observability surface; the caller that owns the
// failed request logs it, once, where it already did before this cache existed.
//
// THE RETURNED SLICE IS SHARED, NOT COPIED, AND IS READ-ONLY. Every caller of
// one identity receives the same backing array, so mutating it would corrupt
// what every other record sees. This holds today for every consumer downstream
// of fetchContent — parseJSON, parseRegex, goquery and htmlquery all read
// without mutating — and it is stated here because it is the one invariant a
// future caller could break silently: nothing would fail loudly, one package
// would simply start reporting another package's version. Copying per waiter
// would restore exactly the memory cost the cache exists to remove, so the
// contract is the mechanism, not an optimisation on top of one.
func (c *bodyCache) do(ctx context.Context, key string, fetch func() ([]byte, error)) ([]byte, error) {
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
			// WITHOUT issuing a request of our own (S024-R5.1), and carry the
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
func (c *bodyCache) classify(key string) (*bodyEntry, arrival) {
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
// leader (S024-R3.5). A panic inside fetch — from the fetch itself or from
// anything it calls — would otherwise unwind straight past the publication and
// leave every waiter blocked on a channel nobody will ever close: not a wrong
// answer but a hung run, which is worse, because a hung run reports nothing at
// all. The guard releases them as a FAILURE, so each fetches on its own and the
// panic costs exactly one package's result.
func (c *bodyCache) lead(key string, entry *bodyEntry, fetch func() ([]byte, error)) ([]byte, error) {
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
// the pre-story behaviour byte for byte — sharing is an optimisation, and where
// it does not apply, nothing else changes.
//
// The count is taken once, before the fetch and whatever the fetch then returns,
// because this function is called exactly once per do call. That is what makes
// the figure deterministic rather than a range: see countRefetch.
func (c *bodyCache) fetchAlone(key string, fetch func() ([]byte, error)) ([]byte, error) {
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
// waiter's, so the waiter pays for its own fetch (S024-R3.1).
//
// It is called at most once per do call, which is a contract rather than an
// implementation detail. A waiter that could rejoin — loop back into do and wait
// on a second leader — might be released by several failures in turn and counted
// several times, so with one failing leader and three waiters both the fetch
// count (2 to 4) and the refetch count (3 to 6) would depend on which goroutine
// happened to wake first. Counting once, on the one path that leaves the join,
// makes N callers of a permanently failing identity produce exactly N fetches
// and N-1 refetches, every time.
func (c *bodyCache) countRefetch() {
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
func (c *bodyCache) retain(key string, body []byte) {
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
// correctness: a body over a limit is still fetched and still returned to its
// caller unchanged, it is simply not kept (S024-R4.2).
//
// Both figures come from measuring the registry rather than from taste (story
// assumption A4), and the measurement is recorded here because it is the only
// thing that makes them reviewable:
//
//   - 2 MiB per body — the most widely shared body in the registry, the 170-way
//     GStreamer tags listing, measures 33.7 KB: some sixty times under the cap,
//     so the deduplication this cache exists for is nowhere near it. The largest
//     payload the registry knows of, www-misc/warsaw at ~8.2 MB, is declared by
//     exactly ONE record — retaining it would save no fetch at all while costing
//     more memory than everything else in the run put together.
//   - 64 MiB total — the 230 distinct URLs at tens of KB each land near ~10 MB,
//     so this is a ceiling on pathology, not a working figure.
//
// The numbers are therefore comfortable rather than tuned, and A4 asks for them
// to be confirmed against a real run's counters — Oversize and BudgetFull both
// staying at zero — before they are treated as settled.
const (
	// DefaultFetchCachePerBody is the largest response body a run will retain
	// for reuse, in bytes: 2 MiB (2097152).
	DefaultFetchCachePerBody int64 = 2 << 20

	// DefaultFetchCacheBudget is the total number of bytes a run will retain
	// across all identities before admission stops, in bytes: 64 MiB
	// (67108864).
	DefaultFetchCacheBudget int64 = 64 << 20
)

// newDefaultBodyCache builds the cache a real run uses, at the limits above.
//
// It exists so the two places that install one — NewChecker's struct literal and
// WithFetchCache(true) — cannot drift apart. "Enabled explicitly" has to mean
// exactly "enabled by default"; with the limits spelled out at both sites that
// would hold only by coincidence, and a future edit to one argument list would
// make an option that claims to be the default quietly install something else.
func newDefaultBodyCache() *bodyCache {
	return newBodyCache(DefaultFetchCachePerBody, DefaultFetchCacheBudget)
}

// admitLocked reports whether a freshly fetched body may be RETAINED for callers
// that have not arrived yet, and charges it against the run budget when it may.
// It is the one question both retention paths ask — publish, for the body a
// leader shared, and retain, for a body fetched outside the join — so the limits
// have exactly one home and cannot drift apart.
//
// ADMISSION IS ABOUT MEMORY, NEVER ABOUT CORRECTNESS. Whatever this returns, the
// body has already been fetched and is already on its way back to its caller
// byte for byte: neither limit can make a fetch fail, be skipped, or be
// truncated. A refusal costs exactly one later fetch of that identity and
// nothing else (S024-R4.2), which is why the call sites express it by forgetting
// the key (publish deletes it, retain declines to insert one) rather than by
// altering what anybody receives.
//
// Both bounds are INCLUSIVE, matching "at most" in S024-R4.1: a body of exactly
// perBody bytes is retained, and so is one that exactly fills what is left of
// the budget.
//
// The per-body cap is asked FIRST, so a body that breaks both limits is recorded
// as Oversize. That is the limit that would have refused it even in an empty
// cache, and therefore the one an operator would have to change (S024-R6.2);
// blaming the budget would send them to raise a number that was never the
// obstacle.
//
// THERE IS NO EVICTION, deliberately. When the budget is reached admission stops
// and everything already retained is kept (S024-R4.3). A run is bounded, so
// there is no long tail to reclaim — and evicting the 170-way GStreamer body to
// make room for a one-off would invert the point of the cache, trading 169
// avoided fetches for one.
//
// "Admission stops" is a consequence of the test, not a latch: a body refused
// for want of room does not close the door behind it, so a smaller one arriving
// afterwards that genuinely fits is still admitted. Latching would refuse bodies
// the budget can hold — spending the run's remaining memory on nothing — and
// would make the outcome depend on the order the records happen to be checked
// in, which is exactly what a bound on memory should not do.
//
// The caller holds c.mu: the decision reads and charges the shared budget, and
// that has to be atomic with the map write it governs. Two admissions racing for
// the last free bytes would otherwise both see room and both take it, leaving
// used above budget with no way to notice.
func (c *bodyCache) admitLocked(body []byte) bool {
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
func (c *bodyCache) publish(key string, entry *bodyEntry, body []byte, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err != nil {
		// A failure is neither retained nor shared (S024-R3.1, S024-R3.2).
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
