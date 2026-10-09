package autoupdate

import (
	"context"
	"net/url"
	"sync"
)

// DefaultPerHostConcurrency caps the requests the checker has in flight to one
// host at a time. The rate limiter paces how often a request may START, not how
// many are still running: on a host that stops answering, every request it
// admits hangs for its whole timeout and retry budget, so a run with
// --concurrency 40 piles dozens of connections onto the one server that is
// already struggling. Six leaves GitHub — paced at ten starts a second — its
// throughput at normal latency.
const DefaultPerHostConcurrency = 6

// hostSlots is a counting semaphore per host. A nil *hostSlots, or one built
// with a limit below 1, never blocks.
type hostSlots struct {
	limit int
	mu    sync.Mutex
	slots map[string]chan struct{}
}

func newHostSlots(limit int) *hostSlots {
	if limit < 1 {
		return nil
	}
	return &hostSlots{limit: limit, slots: make(map[string]chan struct{})}
}

// acquire blocks until rawURL's host has a free slot or ctx ends, and returns
// the function that frees it. An unparseable URL is not limited: the request
// fails on its own, and refusing it here would hide why.
func (h *hostSlots) acquire(ctx context.Context, rawURL string) (func(), error) {
	if h == nil {
		return func() {}, nil
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return func() {}, nil //nolint:nilerr // an unparseable URL is not limited: the request fails on its own and says why
	}
	h.mu.Lock()
	slot, ok := h.slots[u.Host]
	if !ok {
		slot = make(chan struct{}, h.limit)
		h.slots[u.Host] = slot
	}
	h.mu.Unlock()

	select {
	case slot <- struct{}{}:
		return func() { <-slot }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
