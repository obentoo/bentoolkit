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
//
// Story 079: a bump that requires another pending bump runs in a later wave
// (applyWaves), after the one it requires has finished, so the requirement
// gate of the later wave sees what the earlier one published. A batch with no
// `requires` is a single wave and runs exactly as before.
func (a *Applier) ApplyAll(ctx context.Context, updates []PendingUpdate, compile bool, concurrency int) ([]*ApplyResult, int) {
	results := make([]*ApplyResult, len(updates))
	serial := compile || a.SerialApplyRequired(updates)

	pins := func(pkg, atom string) string {
		pin, ok := a.RequirePin(pkg, atom)
		if !ok {
			a.logger().Warn("package no longer requires the atom in packages.toml; not ordering it after that package",
				"package", pkg, "atom", atom)
		}
		return pin
	}
	failures := 0
	for _, wave := range applyWaves(updates, pins) {
		failures += a.applyWave(ctx, updates, wave, results, compile, serial, concurrency)
	}
	return results, failures
}

// applyWave applies the entries of updates named by wave, writing each result
// at its original index in results, and returns how many failed.
func (a *Applier) applyWave(ctx context.Context, updates []PendingUpdate, wave []int, results []*ApplyResult, compile, serial bool, concurrency int) int {
	// Serial when the compile step will prompt and escalate, and — since story
	// 033 — when ANY of these bumps resolves to a depth that starts a build
	// (D14). The second rule has nothing to do with prompts: concurrent builds
	// contend for CPU and for space under PORTAGE_TMPDIR, measured at 60 MB for
	// one gst configure, and a worker pool multiplies that on a machine that was
	// never asked. Depths none and options keep the pool, which is every run whose
	// bumps are revisions and patches.
	if serial {
		failures := 0
		for _, i := range wave {
			// `compile`, not a literal true: this branch is reached for two
			// different reasons, and a depth-driven serial run must not acquire the
			// privileged compile step the operator never asked for.
			result, err := a.Apply(ctx, updates[i].Package, compile)
			if err != nil {
				failures++
			}
			results[i] = result
		}
		return failures
	}

	// Concurrent path: a bounded worker pool over an index queue. Workers write
	// results[i] at distinct indices (no lock) and tally failures atomically.
	jobs := concurrency
	if jobs < 1 {
		jobs = 1
	}
	if jobs > len(wave) {
		jobs = len(wave)
	}

	var failures int64
	queue := make(chan int, len(wave))
	for _, i := range wave {
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
	return int(failures)
}

// applyWaves orders a batch into waves of indices into updates, each wave to
// complete before the next starts. Entry B depends on entry A when A's atom is a
// key of B.Requires and A's new version satisfies the pin B's record declares
// for it (pins answers that operator; "" adds no edge). Kahn's algorithm, in
// input order; entries left in a cycle form one final wave, where the
// requirement gate refuses whatever is still unmet.
func applyWaves(updates []PendingUpdate, pins func(pkg, atom string) string) [][]int {
	n := len(updates)
	dependents := make([][]int, n)
	indegree := make([]int, n)
	for b, ub := range updates {
		for atom, want := range ub.Requires {
			pin := pins(ub.Package, atom)
			if pin == "" {
				continue
			}
			for a, ua := range updates {
				if a != b && PackageAtom(ua.Package) == atom && VersionSatisfies(pin, ua.NewVersion, want) {
					dependents[a] = append(dependents[a], b)
					indegree[b]++
				}
			}
		}
	}

	done := make([]bool, n)
	var waves [][]int
	for remaining := n; remaining > 0; {
		var wave []int
		for i := range updates {
			if !done[i] && indegree[i] == 0 {
				wave = append(wave, i)
			}
		}
		if len(wave) == 0 { // a cycle: everything left runs last, together
			for i := range updates {
				if !done[i] {
					wave = append(wave, i)
				}
			}
		}
		for _, i := range wave {
			done[i] = true
			for _, d := range dependents[i] {
				indegree[d]--
			}
		}
		remaining -= len(wave)
		waves = append(waves, wave)
	}
	return waves
}
