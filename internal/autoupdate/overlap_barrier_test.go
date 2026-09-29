package autoupdate

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// overlapBarrier makes a concurrency overlap certain instead of likely. Each
// worker of the code under test calls arrive, which counts it in and blocks
// it; the test opens the barrier once the overlap it asserts on is in place —
// N workers in flight at once, a leader with every follower joined — so the
// assertion reads that overlap itself rather than whatever a sleep let build
// up.
//
// A barrier the code under test never fills must not hang anything. The waits
// that open it are bounded by signalWaitDeadline and open it on their way out,
// pass or fail, before the caller's deferred teardown (a server.Close waiting
// on a held handler) can run; t.Cleanup opens it again for a test that fails
// before reaching them. Opening twice is a no-op.
type overlapBarrier struct {
	arrived atomic.Int64
	release chan struct{}
	once    sync.Once
}

// newOverlapBarrier returns a shut barrier that t's cleanup
// opens, so no worker can outlive the test blocked in arrive.
func newOverlapBarrier(t *testing.T) *overlapBarrier {
	t.Helper()
	b := &overlapBarrier{release: make(chan struct{})}
	t.Cleanup(b.open)
	return b
}

// arrive is the worker side: it counts the caller in and blocks it until the
// barrier opens. Once the barrier is open arrive only counts, so the work that
// follows the overlap runs unimpeded.
func (b *overlapBarrier) arrive() {
	b.arrived.Add(1)
	<-b.release
}

// open releases every worker held in arrive, and every later one.
func (b *overlapBarrier) open() {
	b.once.Do(func() { close(b.release) })
}

// openWhen waits until cond reports the overlap in place, then opens the
// barrier. It fails the test naming what, with cond's last description, when
// the overlap is not reached within signalWaitDeadline — and opens the barrier
// either way. It calls t.Fatalf, so it must run on the test's own goroutine.
func (b *overlapBarrier) openWhen(t *testing.T, what string, cond func() (bool, string)) {
	t.Helper()
	defer b.open()
	pollUntil(t, what, cond)
}

// openOnceArrived is openWhen for the common overlap: want workers held in
// arrive at once. what names the workers ("manifest runs").
func (b *overlapBarrier) openOnceArrived(t *testing.T, what string, want int64) {
	t.Helper()
	b.openWhen(t, fmt.Sprintf("%d %s in flight at once", want, what), func() (bool, string) {
		n := b.arrived.Load()
		return n >= want, fmt.Sprintf("%d arrived", n)
	})
}

// waitReturned waits for done, which the goroutine running the call under test
// closes when that call comes back, and fails the test naming what when it has
// not come back within signalWaitDeadline.
func waitReturned(t *testing.T, what string, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(signalWaitDeadline):
		t.Fatalf("waiting for %s to return: still running after %v", what, signalWaitDeadline)
	}
}
