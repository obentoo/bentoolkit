package report

import "testing"

// TestTallyTotalSumsTheFourCounts pins R5.1: the tally has FOUR columns, and
// Total is their sum. A Total that forgets one column is the shape RC2
// describes — a count that cannot be reconciled against the plan.
func TestTallyTotalSumsTheFourCounts(t *testing.T) {
	tally := Tally{Proved: 3, Errored: 2, Inconclusive: 5, Skipped: 1}

	if got, want := tally.Total(), 11; got != want {
		t.Fatalf("Total() = %d, want %d (3 proved + 2 errored + 5 inconclusive + 1 skipped)", got, want)
	}
}

// TestTallyTotalIsZeroForTheEmptyTally guards the zero value. A Total that
// counts a phantom passes the sum test above and fails here.
func TestTallyTotalIsZeroForTheEmptyTally(t *testing.T) {
	if got := (Tally{}).Total(); got != 0 {
		t.Fatalf("Total() = %d for the empty tally, want 0", got)
	}
}

// TestReconcilesWhenTheCountsMatchThePlan pins R5.5: every planned package is
// counted exactly once, in exactly one column.
func TestReconcilesWhenTheCountsMatchThePlan(t *testing.T) {
	r := Report{
		Plan:  make([]PlanEntry, 4),
		Tally: Tally{Proved: 1, Errored: 1, Inconclusive: 1, Skipped: 1},
	}

	if !r.Reconciles() {
		t.Fatalf("Reconciles() = false, want true: a tally of %d over a plan of %d", r.Tally.Total(), len(r.Plan))
	}
}

// TestReconcilesIsFalseWhenAPackageWentUncounted is the negative half, and it
// is the half that carries the weight: a Reconciles that returns true
// unconditionally passes the positive test on its own.
func TestReconcilesIsFalseWhenAPackageWentUncounted(t *testing.T) {
	r := Report{
		Plan:  make([]PlanEntry, 4),
		Tally: Tally{Proved: 1, Errored: 1, Inconclusive: 1},
	}

	if r.Reconciles() {
		t.Fatalf("Reconciles() = true, want false: the tally counts %d of %d planned packages", r.Tally.Total(), len(r.Plan))
	}
}
