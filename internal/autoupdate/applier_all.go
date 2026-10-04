package autoupdate

import (
	"context"
	"sync"
	"sync/atomic"
)

// ApplyAll applies every pending update through the shared Applier and
// returns the per-package results in input order plus the number of hard
// failures (an Apply returning a non-nil error). It is the concurrency seam of
// `bentoo overlay autoupdate --apply all` (cmd/bentoo runApplyAll).
//
// It runs serially for either of two reasons. With compile == true the compile
// step prompts for confirmation and runs under sudo, and interleaving those
// across goroutines would scramble the prompts. Since story 033 it is also
// serial when any of these bumps resolves to a validation depth above `options`
// (D14) — a rule about machine resources rather than about prompts, and the
// reason it is asked of the Applier: the depth a bump gets is the applier's
// decision, and a second copy of that logic here would be a copy that drifts.
//
// Otherwise the applies are dispatched across a bounded worker pool (mirroring
// overlay.RegenerateManifests) so each Apply's slow, network-bound `pkgdev
// manifest` step overlaps. concurrency caps the live workers and is clamped to
// [1, len(updates)].
//
// Concurrency safety: the Applier's pending list and reporter are mutex-guarded,
// each Apply's file work is scoped to its own package directory, and workers
// write results to distinct slice indices — so beyond the atomic failure tally
// no additional locking is needed.
//
// Cancellation (audit B8): every Apply receives ctx, and an Apply on a done ctx
// returns at once with a failed result wrapping ctx.Err() before it touches the
// overlay. So once ctx ends, every package not yet begun still gets its own
// non-nil result, in input order, and counts as a failure — no package after
// the cancel is applied, and the loop needs no second check or early break
// (a break would leave nil results).
func (a *Applier) ApplyAll(ctx context.Context, updates []PendingUpdate, compile bool, concurrency int) ([]*ApplyResult, int) {
	results := make([]*ApplyResult, len(updates))

	// Serial when the compile step will prompt and escalate, and — since story
	// 033 — when ANY of these bumps resolves to a depth that starts a build
	// (D14). The second rule has nothing to do with prompts: concurrent builds
	// contend for CPU and for space under PORTAGE_TMPDIR, measured at 60 MB for
	// one gst configure, and a worker pool multiplies that on a machine that was
	// never asked. Depths none and options keep the pool, which is every run whose
	// bumps are revisions and patches.
	if compile || a.SerialApplyRequired(updates) {
		failures := 0
		for i, u := range updates {
			// `compile`, not a literal true: this branch is now reached for two
			// different reasons, and a depth-driven serial run must not acquire the
			// privileged compile step the operator never asked for.
			result, err := a.Apply(ctx, u.Package, compile)
			if err != nil {
				failures++
			}
			results[i] = result
		}
		return results, failures
	}

	// Concurrent path: a bounded worker pool over an index queue. Workers write
	// results[i] at distinct indices (no lock) and tally failures atomically.
	jobs := concurrency
	if jobs < 1 {
		jobs = 1
	}
	if jobs > len(updates) {
		jobs = len(updates)
	}

	var failures int64
	queue := make(chan int, len(updates))
	for i := range updates {
		queue <- i
	}
	close(queue)

	var wg sync.WaitGroup
	for w := 0; w < jobs; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range queue {
				result, err := a.Apply(ctx, updates[i].Package, false)
				results[i] = result
				if err != nil {
					atomic.AddInt64(&failures, 1)
				}
			}
		}()
	}
	wg.Wait()

	return results, int(failures)
}
