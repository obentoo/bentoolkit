package main

// Story 059, sub-task 5.1 (R4.3, R4.4, R4.6, R4.7; audit B8): once the context
// given to applyAllPackages ends, the pool starts no package it had not already
// begun. Every package still gets a result, in input order; each one never
// started is a failure caused by context.Canceled and is counted in the
// failure total; and its overlay directory and pending entry are left alone.
//
// The run is interrupted from inside the exec seam, at the very first child
// the batch spawns. That is the earliest point at which at least one Apply has
// begun, and at most `concurrency` of them can have: every Apply spawns a
// manifest child before it can finish, so no worker can have moved on to
// another package yet. Packages at index >= concurrency therefore never began.
// A pool that ignored its context (or handed Apply a context of its own) would
// apply them.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"testing"
)

func TestApplyAllPackagesStopsDispatchingOnCancel(t *testing.T) {
	const n = 6
	for _, concurrency := range []int{1, 3} {
		t.Run(fmt.Sprintf("concurrency %d", concurrency), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var interrupt sync.Once
			factory := func(c context.Context, name string, arg ...string) *exec.Cmd {
				interrupt.Do(cancel)
				return exec.CommandContext(c, "true")
			}
			applier, updates := setupApplyAllTest(t, n, factory)

			results, failures := applyAllPackages(ctx, applier, updates, false, concurrency)

			if len(results) != n {
				t.Fatalf("got %d results for %d packages; want one per package (R4.4)", len(results), n)
			}
			notApplied := 0
			for i, r := range results {
				if r == nil {
					t.Errorf("results[%d] is nil; want a result for %s (R4.4)", i, updates[i].Package)
					continue
				}
				if r.Package != updates[i].Package {
					t.Errorf("results[%d].Package = %q; want %q (input order, R4.4)", i, r.Package, updates[i].Package)
				}
				if !r.Success {
					notApplied++
				}
			}
			if failures != notApplied {
				t.Errorf("failure total = %d; want %d, one per package that was not applied (R4.7)", failures, notApplied)
			}

			for i := concurrency; i < n; i++ {
				pkg := updates[i].Package
				r := results[i]
				if r == nil {
					continue
				}
				if r.Success {
					t.Errorf("%s was applied after the batch's context ended; want it never started (R4.3)", pkg)
				}
				if !errors.Is(r.Error, context.Canceled) {
					t.Errorf("%s: Error = %v; want errors.Is(err, context.Canceled) (R4.6)", pkg, r.Error)
				}
				if _, err := os.Stat(applier.EbuildPath(pkg, "2.0.0")); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("%s: a 2.0.0 ebuild exists after the batch was cancelled before it began (stat err = %v) (R4.3)", pkg, err)
				}
				if !applier.Pending().Has(pkg) {
					t.Errorf("%s: its pending entry was removed although it never started", pkg)
				}
			}
		})
	}
}
