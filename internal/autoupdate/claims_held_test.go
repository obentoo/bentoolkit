package autoupdate

import (
	"reflect"
	"testing"
)

// =============================================================================
// Story 026 / task 1.1 — a held entry is pinned, not skipped
// (S026-R1.1, R1.2, R1.3, R2.1, R3.1)
// =============================================================================

// TestReconcileHeldEntries pins the distinction story 026 exists to draw: `hold`
// means "do not auto-bump this package", not "do not record what it holds".
//
// The three real records this is about — dev-build/gn, dev-lang/ghc and
// sci-ml/stable-diffusion-cpp — are all held, all pinless, and all have exactly
// one ebuild on disk. Before this story they were structurally unpinnable: the
// reconciliation returned before looking at them, so no number of --check runs
// could ever fill the pin in.
//
// The expected UnclaimedEbuild count is the subtle half. A pinless held entry
// still RESOLVES to its ebuild, and unclaimedIn collects claims from
// resolveClaims — which has never skipped held or disabled entries. So the
// ebuild is claimed by its resolved version even with an empty pin, and
// reporting it as unclaimed would be wrong. Reconcile was the only place that
// pretended a held entry holds nothing.
func TestReconcileHeldEntries(t *testing.T) {
	tests := []struct {
		name string
		dirs map[string][]sweepEbuild
		cfgs map[string]PackageConfig
		want []Divergence
	}{
		{
			// The shape of all three real records: held, pinless, one ebuild.
			// The empty Pin is what makes it a stale pin rather than a match —
			// the same reading the first reconciliation applies to every pinless
			// active entry.
			name: "a held entry with an ebuild is a stale pin (R1.1, R1.2)",
			dirs: map[string][]sweepEbuild{
				"dev-lang/ghc": {{version: "9.14.1"}},
			},
			cfgs: map[string]PackageConfig{
				"dev-lang/ghc": heldEntry("", ""),
			},
			want: []Divergence{
				{Key: "dev-lang/ghc", Kind: StalePin, Pin: "", Disk: "9.14.1"},
			},
		},
		{
			// Not reported as unclaimed: resolveClaims counts a held entry's
			// RESOLVED version as a claim, pin or no pin. If this case ever
			// grows an UnclaimedEbuild, the claim collection has started
			// skipping held entries and the sweep would be about to offer a
			// held package's only ebuild up for removal.
			name: "a held entry claims its own ebuild, so nothing is unclaimed",
			dirs: map[string][]sweepEbuild{
				"dev-build/gn": {{version: "0.2501"}},
			},
			cfgs: map[string]PackageConfig{
				"dev-build/gn": heldEntry("", ""),
			},
			want: []Divergence{
				{Key: "dev-build/gn", Kind: StalePin, Pin: "", Disk: "0.2501"},
			},
		},
		{
			// A held entry whose pin already matches disk is not a divergence at
			// all — the steady state after this story has run once.
			name: "a held entry with a correct pin produces nothing",
			dirs: map[string][]sweepEbuild{
				"sci-ml/stable-diffusion-cpp": {{version: "0_pre782"}},
			},
			cfgs: map[string]PackageConfig{
				"sci-ml/stable-diffusion-cpp": heldEntry("0_pre782", ""),
			},
			want: nil,
		},
		{
			// R1.3: a held entry whose directory is gone lands in the no-ebuild
			// class, which stalePinBatch never writes — so the missing ebuild
			// cannot erase the pin the entry still carries.
			name: "a held entry with no ebuild is a no-ebuild divergence (R1.3)",
			dirs: map[string][]sweepEbuild{},
			cfgs: map[string]PackageConfig{
				"dev-build/gn": heldEntry("0.2501", ""),
			},
			want: []Divergence{
				{Key: "dev-build/gn", Kind: NoEbuild, Pin: "0.2501"},
			},
		},
		{
			// R3.1: the other half of the skip stays exactly as it was. A
			// disabled entry has no ebuild to record and the orphan
			// reconciliation owns it, so it is still invisible here — including
			// its directory, which must not be scanned for unclaimed ebuilds.
			name: "a disabled entry still produces nothing (R3.1)",
			dirs: map[string][]sweepEbuild{
				"net-misc/rclone": {{version: "1.70.0"}, {version: "1.71.1"}},
			},
			cfgs: map[string]PackageConfig{
				"net-misc/rclone": offEntry("", ""),
			},
			want: nil,
		},
		{
			// Both at once, which is the registry's real shape: 21 disabled
			// entries and 3 held ones. Only the held one appears.
			name: "held and disabled together: only the held entry is reported",
			dirs: map[string][]sweepEbuild{
				"net-misc/rclone": {{version: "1.71.1"}},
				"dev-lang/ghc":    {{version: "9.14.1"}},
			},
			cfgs: map[string]PackageConfig{
				"net-misc/rclone": offEntry("", ""),
				"dev-lang/ghc":    heldEntry("", ""),
			},
			want: []Divergence{
				{Key: "dev-lang/ghc", Kind: StalePin, Pin: "", Disk: "9.14.1"},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			overlay := writeReconcileOverlay(t, tc.dirs)
			got := Reconcile(overlay, tc.cfgs)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Reconcile() =\n  %+v\nwant\n  %+v", got, tc.want)
			}
		})
	}
}

// TestReconcileLeavesHoldAndEnabledAlone pins R2.1: the reconciliation reports
// what a held entry holds and changes nothing about the decision to hold it.
//
// Reconcile takes the config map by value but the PackageConfig values are
// copied per iteration, so a mutation would have to be deliberate — which is
// exactly why it is worth a test. Story 021's R3.5 promise was that a held entry
// is a maintainer decision the tool does not second-guess; this story narrows
// that promise to the decision itself rather than to the record of what is on
// disk, and the promise must still hold in its narrowed form.
func TestReconcileLeavesHoldAndEnabledAlone(t *testing.T) {
	overlay := writeReconcileOverlay(t, map[string][]sweepEbuild{
		"dev-lang/ghc":    {{version: "9.14.1"}},
		"net-misc/rclone": {{version: "1.71.1"}},
	})

	held := heldEntry("", "")
	off := offEntry("", "")
	cfgs := map[string]PackageConfig{
		"dev-lang/ghc":    held,
		"net-misc/rclone": off,
	}

	_ = Reconcile(overlay, cfgs)

	// Copied out of the map before the checks: IsHeld/IsEnabled have pointer
	// receivers and a map value is not addressable.
	ghc := cfgs["dev-lang/ghc"]
	rclone := cfgs["net-misc/rclone"]

	if !ghc.IsHeld() {
		t.Error("the held entry lost its hold: reconciliation must report what a held entry holds, never un-hold it (S026-R2.1)")
	}
	if ghc.Enabled != nil {
		t.Error("the held entry gained an enabled assignment: hold and enabled are independent states (S026-R2.1)")
	}
	if rclone.IsEnabled() {
		t.Error("the disabled entry was re-enabled by the reconciliation (S026-R2.1)")
	}
}
