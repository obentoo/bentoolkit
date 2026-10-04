package overlay

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// TestCompareReportInterrupted guards CompareReport.Interrupted: the run-level
// fact that says whether CompareWithProvider reached the end of the package list
// it was handed (S047-R5.1, S047-R5.2).
//
// It lives in its own file rather than in compare_test.go because it guards one
// field across four situations and carries the recorded evidence below;
// compare_test.go already covers the comparison surface, and the two concerns
// read better apart. The name begins "TestCompare" deliberately: the sub-task's
// validation command is `go test ./internal/overlay/ -run TestCompare -race`,
// whose regexp is unanchored, so a name like TestInterruptedCompareReport would
// be skipped by the very command meant to gate this work.
//
// # What the four sub-tests are for
//
// The first two prove the field is WRITTEN when a run is cut short — mid-scan
// and before dispatch begins. The third proves an untouched run reports false,
// without which a hardcoded `Interrupted = true` would pass everything. The
// fourth pins the reason the field exists at all: a report with a zero gap and
// Interrupted set is the state the tempting derivation misreads.
//
// # Why the fourth is a value, not a run
//
// Today's dispatch loop sets Interrupted and breaks BEFORE dispatching the
// package it is on, so every run it interrupts leaves a gap of at least one.
// That is an accident of the loop's shape, not a contract — a future refactor
// that aborts in-flight workers instead of only dispatch would make the zero-gap
// case reachable — and asserting it as a rule would force that refactor to
// delete an assertion. So the trap is pinned where the consumer actually meets
// it: on the CompareReport value, which is what the report adapter reads.
//
// # Recorded evidence that this guard bites (S047-R8.1)
//
// A green test proves nothing about what it would catch, and the story artifacts
// holding the Red (.draft/) are not committed — `git ls-files .epic/` returns
// nothing — so a pointer to them is a pointer to nothing for anyone who cloned
// this. It is written down here instead, the way
// internal/common/report/render/contract_test.go writes down its own.
//
// Measured on 2026-09-04, against sub-task 1.3. Two mutations, each applied on
// its own to internal/overlay/compare.go, run with `go test ./internal/overlay/
// -run TestCompare -race -count=1`, and reverted immediately — the restore
// verified both times by md5sum returning the pre-mutation digest
// fe5da645e6b57150038efeea65492f38.
//
// Mutation 1 — the field is never written, i.e. this sub-task's logic reverted
// while the field stays declared. A local `var cancelled bool` was reinstated
// and the loop's two `report.Interrupted = true`, its `if report.Interrupted`
// break and the final `if report.Interrupted` return all went back to reading
// it. Observed:
//
//	--- FAIL: TestCompareReportInterrupted (0.06s)
//	    --- FAIL: TestCompareReportInterrupted/a_run_cancelled_mid-scan_reports_Interrupted (0.06s)
//	        compare_interrupted_test.go:120: Interrupted = false on a run cancelled after 10 of 200 packages; want true
//	    --- FAIL: TestCompareReportInterrupted/a_run_cancelled_before_dispatch_reports_Interrupted (0.00s)
//	        compare_interrupted_test.go:148: Interrupted = false on a run cancelled before dispatch; want true
//
// Mutation 2 — the field is written unconditionally. A bare
// `report.Interrupted = true` was inserted immediately above the final
// `if report.Interrupted` return, so dispatch behaves exactly as before and the
// error return is untouched (ctx.Err() is nil on a run nothing cancelled) while
// every report claims it was cut short. Observed:
//
//	--- FAIL: TestCompareReportInterrupted (0.06s)
//	    --- FAIL: TestCompareReportInterrupted/a_run_that_reached_the_end_reports_not_Interrupted (0.00s)
//	        compare_interrupted_test.go:180: Interrupted = true on a run that compared every package; want false
//
// Mutation 1 leaves sub-test 3 green and mutation 2 leaves sub-tests 1 and 2
// green, which is why both are recorded: neither half of the rule is provable by
// the other's failure. A third mutation — flipping the two loop assignments to
// `= false` — was tried and DISCARDED: it drains the semaphore without acquiring
// it and deadlocks wg.Wait(), so it measures a broken program rather than a
// broken rule.
func TestCompareReportInterrupted(t *testing.T) {
	t.Run("a run cancelled mid-scan reports Interrupted", func(t *testing.T) {
		// Same fixture shape as TestCompareWithProvider_ContextCancel: enough
		// packages, each held in flight until the cancel, that dispatch is still
		// going when the cancel lands.
		const numPkgs = 200
		prov := &fakeProvider{hook: holdUntilCancelled(t), versions: map[string][]string{}}
		pkgs := make([]PackageInfo, 0, numPkgs)
		for i := 0; i < numPkgs; i++ {
			name := fmt.Sprintf("pkg%03d", i)
			prov.versions["cat/"+name] = []string{"1.0"}
			pkgs = append(pkgs, PackageInfo{Category: "cat", Package: name, LatestVersion: "1.0"})
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// Cancel once the first provider call shows dispatch has begun.
		report, err := compareCancelledOnceDispatched(t, ctx, pkgs, prov, CompareOptions{
			Concurrency:   5,
			IncludeSynced: true,
		}, cancel)

		if !errors.Is(err, context.Canceled) {
			t.Fatalf("CompareWithProvider returned err = %v; want context.Canceled", err)
		}
		// A fixture precondition, not a rule about the gap: if this machine ran
		// the whole list before the cancel landed there was no interruption to
		// observe and the sub-test would prove nothing.
		if report.ComparedPackages >= numPkgs {
			t.Fatalf("fixture did not cut the run short: ComparedPackages = %d of %d", report.ComparedPackages, numPkgs)
		}
		if !report.Interrupted {
			t.Errorf("Interrupted = false on a run cancelled after %d of %d packages; want true",
				report.ComparedPackages, numPkgs)
		}
	})

	t.Run("a run cancelled before dispatch reports Interrupted", func(t *testing.T) {
		// Same fixture shape as TestCompareWithProvider_ContextCancelledUpfront.
		prov := &fakeProvider{versions: map[string][]string{}}
		pkgs := []PackageInfo{
			{Category: "cat", Package: "a", LatestVersion: "1.0"},
			{Category: "cat", Package: "b", LatestVersion: "1.0"},
		}

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // cancelled before the call

		report, err := CompareWithProvider(ctx, pkgs, prov, CompareOptions{})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("CompareWithProvider returned err = %v; want context.Canceled", err)
		}
		if report.ComparedPackages != 0 {
			t.Fatalf("fixture dispatched work on a pre-cancelled ctx: ComparedPackages = %d", report.ComparedPackages)
		}
		// The whole list is unreached, so the gap is the whole list — and the
		// field says the same thing the gap does here. The point of the sub-test
		// is that it says it at all: a run that established nothing must not
		// arrive as a complete report of zero packages.
		if !report.Interrupted {
			t.Errorf("Interrupted = false on a run cancelled before dispatch; want true")
		}
		if gap := report.TotalPackages - report.ComparedPackages; gap != len(pkgs) {
			t.Errorf("TotalPackages - ComparedPackages = %d; want %d", gap, len(pkgs))
		}
	})

	t.Run("a run that reached the end reports not Interrupted", func(t *testing.T) {
		// Three packages, one per outcome that the collector must still count:
		// up-to-date, absent from the remote, and an outright provider failure.
		// The options deliberately EXCLUDE the first two from Results, so this
		// also pins the claim the field's doc makes about the gap being exact —
		// ComparedPackages sits outside the include filter, not inside it.
		prov := &fakeProvider{
			versions: map[string][]string{"cat/synced": {"1.0"}},
			errs:     map[string]error{"cat/broken": errors.New("provider is down")},
		}
		pkgs := []PackageInfo{
			{Category: "cat", Package: "synced", LatestVersion: "1.0"},
			{Category: "cat", Package: "absent", LatestVersion: "1.0"},
			{Category: "cat", Package: "broken", LatestVersion: "1.0"},
		}

		report, err := CompareWithProvider(t.Context(), pkgs, prov, CompareOptions{
			// IncludeSynced and IncludeNotInRemote both left false.
		})
		if err != nil {
			t.Fatalf("CompareWithProvider returned an error: %v", err)
		}

		if report.Interrupted {
			t.Errorf("Interrupted = true on a run that compared every package; want false")
		}
		if report.ComparedPackages != len(pkgs) {
			t.Errorf("ComparedPackages = %d; want %d — every result reaching the collector is counted, "+
				"including StatusNotInRemote and StatusError", report.ComparedPackages, len(pkgs))
		}
		if gap := report.TotalPackages - report.ComparedPackages; gap != 0 {
			t.Errorf("TotalPackages - ComparedPackages = %d; want 0 on a run that reached the end", gap)
		}
		if report.NotInRemoteCount != 1 || report.ErrorCount != 1 || report.UpToDateCount != 1 {
			t.Errorf("per-status counts = {notInRemote:%d error:%d upToDate:%d}; want one of each",
				report.NotInRemoteCount, report.ErrorCount, report.UpToDateCount)
		}
		// Two of the three were filtered out of the table and counted anyway.
		if len(report.Results) != 1 {
			t.Errorf("len(Results) = %d; want 1 (only the error row survives the filter)", len(report.Results))
		}
	})

	t.Run("a zero gap does not mean the run finished", func(t *testing.T) {
		// The two candidate answers to "did this run finish?". The first is the
		// derivation cmd/bentoo/overlay_manifest_report.go warns about; the
		// second is the rule.
		completeFromGap := func(r CompareReport) bool { return r.TotalPackages-r.ComparedPackages == 0 }
		completeFromField := func(r CompareReport) bool { return !r.Interrupted }

		cutShortAfterItsLast := CompareReport{TotalPackages: 3, ComparedPackages: 3, Interrupted: true}
		reachedTheEnd := CompareReport{TotalPackages: 3, ComparedPackages: 3}

		if !completeFromGap(cutShortAfterItsLast) {
			t.Fatalf("fixture is not the trap: its gap must be zero, got %d",
				cutShortAfterItsLast.TotalPackages-cutShortAfterItsLast.ComparedPackages)
		}
		if completeFromField(cutShortAfterItsLast) {
			t.Errorf("!Interrupted = true on a run cut short after its last package; want false")
		}
		// The disagreement IS the reason the field exists: without it the
		// operator who pressed ctrl+c is told their scan ended normally.
		if completeFromGap(cutShortAfterItsLast) == completeFromField(cutShortAfterItsLast) {
			t.Errorf("the gap derivation and the field agree on a run cut short after its last package; " +
				"they must not, or the field records nothing the counts do not already say")
		}
		// On a run that genuinely reached the end the two agree, which is what
		// makes the disagreement above information rather than noise.
		if !completeFromGap(reachedTheEnd) || !completeFromField(reachedTheEnd) {
			t.Errorf("a run that reached the end read as incomplete: fromGap=%t fromField=%t",
				completeFromGap(reachedTheEnd), completeFromField(reachedTheEnd))
		}
	})
}
