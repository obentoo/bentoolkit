package overlay

import "testing"

// TestDeriveVerdict walks the whole 5x3 policy table: every CompareStatus
// against every divergence state (unknown, not patched, patched). The table IS
// the policy — deriveVerdict is a pure function of its two axes precisely so
// that the recommendation lives in one place instead of being scattered through
// the report — so the test enumerates it exhaustively rather than sampling it.
//
//	| Status            | unknown | not patched | patched      |
//	|-------------------|---------|-------------|--------------|
//	| StatusNewer       | Unknown | Keep        | Keep         |
//	| StatusUpToDate    | Unknown | Redundant   | Keep         |
//	| StatusOutdated    | Unknown | Redundant   | NeedsRebase  |
//	| StatusNotInRemote | Unknown | Keep        | Keep         |
//	| StatusError       | Unknown | Unknown     | Unknown      |
//
// The cells that carry the story are the `unknown` column and the `StatusError`
// row. 13 of the 313 overlay packages have no registry entry, and reading their
// silence as "not patched" would recommend deleting sys-devel/binutils the day
// ::gentoo catches up; a failed comparison supports no recommendation at all for
// the same reason. Both are pinned twice: once cell by cell, and once as the
// invariant that no such cell is ever Redundant (R2.3, R3.6).
//
// _Requirements: R2.3, R3.1, R3.2, R3.3, R3.4, R3.5, R3.6_
func TestDeriveVerdict(t *testing.T) {
	// The three divergence states. Absence from the caller's map is the unknown
	// state, which reaches deriveVerdict as known == false; there is no Known
	// field on Divergence, because a second copy of what the map lookup already
	// reports is a second thing to desynchronise.
	var (
		unknownDiv = Divergence{}
		plainDiv   = Divergence{}
		patchedDiv = Divergence{
			Patched: true,
			Reason:  "keeps our wayland-by-default patch",
			Entry:   "app-editors/zed@stable",
		}
	)

	cases := []struct {
		name   string
		status CompareStatus
		div    Divergence
		known  bool
		want   Verdict
	}{
		// bentoo ahead of ::gentoo — being ahead is why the overlay copy exists.
		{"newer / unknown", StatusNewer, unknownDiv, false, VerdictUnknown},
		{"newer / not patched", StatusNewer, plainDiv, true, VerdictKeep},
		{"newer / patched", StatusNewer, patchedDiv, true, VerdictKeep},

		// same version — the dangerous row: "up-to-date" means delete for an
		// unpatched package and keep for a patched one.
		{"up-to-date / unknown", StatusUpToDate, unknownDiv, false, VerdictUnknown},
		{"up-to-date / not patched", StatusUpToDate, plainDiv, true, VerdictRedundant},
		{"up-to-date / patched", StatusUpToDate, patchedDiv, true, VerdictKeep},

		// ::gentoo moved ahead — either it delivers more than we do, or we owe a
		// bump AND a re-application of the delta.
		{"outdated / unknown", StatusOutdated, unknownDiv, false, VerdictUnknown},
		{"outdated / not patched", StatusOutdated, plainDiv, true, VerdictRedundant},
		{"outdated / patched", StatusOutdated, patchedDiv, true, VerdictNeedsRebase},

		// ours alone.
		{"not-in-remote / unknown", StatusNotInRemote, unknownDiv, false, VerdictUnknown},
		{"not-in-remote / not patched", StatusNotInRemote, plainDiv, true, VerdictKeep},
		{"not-in-remote / patched", StatusNotInRemote, patchedDiv, true, VerdictKeep},

		// the comparison itself failed; nothing about it supports a recommendation.
		{"error / unknown", StatusError, unknownDiv, false, VerdictUnknown},
		{"error / not patched", StatusError, plainDiv, true, VerdictUnknown},
		{"error / patched", StatusError, patchedDiv, true, VerdictUnknown},
	}

	if len(cases) != 15 {
		t.Fatalf("the table has %d rows, want 15 (5 statuses x 3 divergence states)", len(cases))
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := deriveVerdict(tc.status, tc.div, tc.known)
			if got != tc.want {
				t.Errorf("deriveVerdict(%v, %+v, known=%v) = %v, want %v",
					tc.status, tc.div, tc.known, got, tc.want)
			}
		})
	}

	// The invariant, stated separately from the table so a future edit to the
	// table cannot quietly weaken it: nothing the system does not know about, and
	// nothing whose comparison failed, is ever recommended for removal.
	t.Run("no unknown or errored package is ever Redundant", func(t *testing.T) {
		for _, tc := range cases {
			if (!tc.known || tc.status == StatusError) && tc.want == VerdictRedundant {
				t.Errorf("table row %q expects VerdictRedundant for an unknown/errored package; it must never recommend removal", tc.name)
			}
			got := deriveVerdict(tc.status, tc.div, tc.known)
			if (!tc.known || tc.status == StatusError) && got == VerdictRedundant {
				t.Errorf("deriveVerdict(%v, known=%v) = VerdictRedundant; an unrecorded or failed comparison must never recommend removal", tc.status, tc.known)
			}
		}
	})

	// The four verdicts must be four distinct values: collapsing any two would
	// make the table above pass while the report loses a distinction it needs.
	t.Run("the four verdicts are distinct", func(t *testing.T) {
		seen := map[Verdict]string{}
		for name, v := range map[string]Verdict{
			"VerdictKeep":        VerdictKeep,
			"VerdictRedundant":   VerdictRedundant,
			"VerdictNeedsRebase": VerdictNeedsRebase,
			"VerdictUnknown":     VerdictUnknown,
		} {
			if prev, dup := seen[v]; dup {
				t.Errorf("%s and %s share the value %d", name, prev, int(v))
			}
			seen[v] = name
		}
	})
}
