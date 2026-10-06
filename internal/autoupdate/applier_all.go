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
// It runs serially when compile is set (the compile step prompts under sudo,
// and interleaved prompts would scramble) or when any bump resolves to a
// validation depth above `options` (concurrent builds contend for machine
// resources; the Applier decides the depth, so the rule is asked of it).
// Otherwise a worker pool of concurrency workers, clamped to [1, len(updates)],
// overlaps each Apply's network-bound `pkgdev manifest`. That is safe because
// the Applier's pending list and reporter are mutex-guarded, each Apply works
// in its own package directory, and workers write distinct result indices.
//
// Every Apply receives ctx and returns a failed result at once on a done ctx,
// so after a cancel each unstarted package still gets a non-nil, failed result
// in input order; an early break would leave nil results instead. A bump that requires another pending bump runs in a later
// wave (applyWaves), so its requirement gate sees what the earlier one published.
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
	// Serial when the compile step will prompt and escalate, and when ANY of
	// these bumps resolves to a depth that starts a build. The second rule has
	// nothing to do with prompts: concurrent builds contend for CPU and for space
	// under PORTAGE_TMPDIR, measured at 60 MB for one gst configure, and a worker
	// pool multiplies that on a machine that was never asked. Depths none and
	// options keep the pool, which is every run whose bumps are revisions and
	// patches.
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
