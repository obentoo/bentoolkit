package autoupdate

// Authored for bentoo story 003 (autoupdate cooldown), sub-task 3.2 —
// R2.1, R2.2, R2.3.
//
// Contract: design.md "Applier integration" — collectHolders (claims.go) and
// PlanOverlaySweep (sweep.go) add Block.Held() to the held set, so --clean
// never deletes a masked version or its installable predecessor; Reconcile's
// unclaimed scan treats those versions as claimed. GOTCHA pinned here: the
// pin moves to the new version (setVersionsFn) BEFORE the --clean sweep, and
// the predecessor must survive that same run.
//
// Hostile halves first: the masked version AND its predecessor survive, but
// nothing else is over-held — the version below the predecessor is still
// removed, and a prefix-named package (`-bin`) with no entry is swept as
// today. A queue of two masked versions holds both.
//
// newCooldownFixture, cooldownEmptyBlock and the helpers come from
// applier_cooldown_test.go (3.1).

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/registry"
)

// TestCooldownSweepClean_KeepsThePredecessorWhileMasked: R2.1 through
// `--apply --clean`. Hostile half first: with the block, 1.5 (the installable
// predecessor) survives the run that masks 2.0 while 1.0 is still removed.
// Then the two controls R2.3 rests on: without a block, and for an exempt
// record, the sweep removes both as it does today.
func TestCooldownSweepClean_KeepsThePredecessorWhileMasked(t *testing.T) {
	const pkg = "app-misc/cooldown-foo"
	bump := []cooldownBump{{pkg, "1.5", "2.0"}}
	extra := map[string][]string{pkg: {"1.0"}}

	t.Run("masked: predecessor kept", func(t *testing.T) {
		f := newCooldownFixture(t, bump, extra,
			map[string]registry.PackageConfig{pkg: {Version: "1.5"}}, cooldownEmptyBlock, WithApplierClean(true))
		r := applyOne(t, f, pkg)
		if !r.Success {
			t.Fatalf("the apply failed: %v", r.Error)
		}
		if got := f.pin(pkg); got != "2.0" {
			t.Fatalf("precondition: the pin moved to %q, want 2.0 before the sweep", got)
		}
		if !f.ebuildExists(pkg, "1.5") {
			t.Error("--clean deleted 1.5, the only installable version while 2.0 is masked")
		}
		if !f.ebuildExists(pkg, "2.0") {
			t.Error("--clean deleted the masked 2.0")
		}
		if f.ebuildExists(pkg, "1.0") {
			t.Error("--clean kept 1.0, below the predecessor: the held set is too wide")
		}
		if !slices.Equal(r.CleanRemoved, []string{"1.0"}) {
			t.Errorf("CleanRemoved = %v, want [1.0]", r.CleanRemoved)
		}
	})

	reason := "exempt control"
	for name, c := range map[string]struct {
		mask string
		cfg  registry.PackageConfig
	}{
		"no block: swept as today":      {"", registry.PackageConfig{Version: "1.5"}},
		"exempt record: swept as today": {cooldownEmptyBlock, registry.PackageConfig{Version: "1.5", CooldownExempt: &reason}},
	} {
		t.Run(name, func(t *testing.T) {
			f := newCooldownFixture(t, bump, extra, map[string]registry.PackageConfig{pkg: c.cfg}, c.mask, WithApplierClean(true))
			if r := applyOne(t, f, pkg); !r.Success {
				t.Fatalf("the apply failed: %v", r.Error)
			}
			if f.ebuildExists(pkg, "1.5") || f.ebuildExists(pkg, "1.0") {
				t.Errorf("1.5 present=%v, 1.0 present=%v; with no entry the sweep removes both", f.ebuildExists(pkg, "1.5"), f.ebuildExists(pkg, "1.0"))
			}
		})
	}
}

// sweepOverlay lays out an overlay of plain ebuilds and a package.mask.
func sweepOverlay(t *testing.T, tree map[string][]string, mask string) string {
	t.Helper()
	overlay := filepath.Join(t.TempDir(), "overlay")
	for pkg, versions := range tree {
		for _, v := range versions {
			createTestEbuildFile(t, overlay, pkg, v)
		}
	}
	for _, d := range []string{"profiles", ".autoupdate", "metadata"} {
		if err := os.MkdirAll(filepath.Join(overlay, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if mask != "" {
		if err := os.WriteFile(filepath.Join(overlay, "profiles", "package.mask"), []byte(mask), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return overlay
}

// The shared standalone-sweep fixture: foo has a QUEUE of two masked versions
// (pin on the newest), foo-bin and bar have no entry.
var (
	heldTree = map[string][]string{
		"app-misc/cooldown-foo":     {"1.0", "1.5", "2.0", "2.1"},
		"app-misc/cooldown-foo-bin": {"0.9", "1.0"},
		"app-misc/cooldown-bar":     {"1.0", "2.0"},
	}
	heldCfgs = map[string]registry.PackageConfig{
		"app-misc/cooldown-foo":     {Version: "2.1"},
		"app-misc/cooldown-foo-bin": {Version: "1.0"},
		"app-misc/cooldown-bar":     {Version: "2.0"},
	}
	heldMask = "# BEGIN bentoo-cooldown format=1 days=7\n" +
		"# Managed by `bentoo overlay autoupdate`. Edit with `bentoo overlay cooldown`, never by hand.\n" +
		"# cooldown since=2026-11-05T00:00:00Z origin=autoupdate\n" +
		"=app-misc/cooldown-foo-2.0\n" +
		"# cooldown since=2026-11-06T00:00:00Z origin=autoupdate\n" +
		"=app-misc/cooldown-foo-2.1\n" +
		"# END bentoo-cooldown\n"
)

func planFor(t *testing.T, batch SweepBatch, atom string) SweepDirPlan {
	t.Helper()
	for _, d := range batch.Dirs {
		if d.Atom == atom {
			return d
		}
	}
	return SweepDirPlan{Atom: atom}
}

// TestCooldownHeld_PlanOverlaySweepNeverRemovesAHeldVersion: R2.1 for the
// standalone `bentoo overlay autoupdate` sweep. Every queued masked version
// and the predecessor stay; everything else is planned exactly as today.
func TestCooldownHeld_PlanOverlaySweepNeverRemovesAHeldVersion(t *testing.T) {
	overlay := sweepOverlay(t, heldTree, heldMask)

	batch, err := PlanOverlaySweep(nil, overlay, heldCfgs, "")
	if err != nil {
		t.Fatalf("PlanOverlaySweep: %v", err)
	}
	want := map[string][]string{
		"app-misc/cooldown-foo":     {"1.0"},
		"app-misc/cooldown-foo-bin": {"0.9"}, // prefix-named, no entry: swept as today
		"app-misc/cooldown-bar":     {"1.0"}, // no entry: R2.3
	}
	for atom, w := range want {
		if got := planFor(t, batch, atom).Remove; !slices.Equal(got, w) {
			t.Errorf("%s: planned removals %v, want %v", atom, got, w)
		}
	}
}

// TestCooldownHeld_AMalformedBlockNeverPlansTheRemovalOfAHeldVersion: a
// block that cannot be read cannot say what is held, so the sweep either
// refuses outright or leaves the versions it might hold alone — it never
// deletes them.
func TestCooldownHeld_AMalformedBlockNeverPlansTheRemovalOfAHeldVersion(t *testing.T) {
	malformed := strings.Replace(heldMask, "# END bentoo-cooldown\n", "", 1) // no END
	overlay := sweepOverlay(t, heldTree, malformed)

	batch, err := PlanOverlaySweep(nil, overlay, heldCfgs, "")
	if err != nil {
		if !strings.Contains(err.Error(), "package.mask") {
			t.Errorf("the refusal does not name profiles/package.mask: %v", err)
		}
		return
	}
	for _, v := range planFor(t, batch, "app-misc/cooldown-foo").Remove {
		if v == "1.5" || v == "2.0" {
			t.Errorf("with an unreadable block the sweep planned to remove %s, which the block may hold", v)
		}
	}
}

// TestCooldownHeld_ReconcileDoesNotReportHeldVersionsAsUnclaimed: R2.2. The
// predecessor and the queued masked version are claimed; the stray 1.0 below
// them, and the unheld neighbours, are still reported.
func TestCooldownHeld_ReconcileDoesNotReportHeldVersionsAsUnclaimed(t *testing.T) {
	overlay := sweepOverlay(t, heldTree, heldMask)

	unclaimed := map[string][]string{}
	for _, d := range Reconcile(nil, overlay, heldCfgs) {
		if d.Kind == UnclaimedEbuild {
			unclaimed[d.Key] = append(unclaimed[d.Key], d.Disk)
		}
	}
	want := map[string][]string{
		"app-misc/cooldown-foo":     {"1.0"},
		"app-misc/cooldown-foo-bin": {"0.9"},
		"app-misc/cooldown-bar":     {"1.0"},
	}
	for atom, w := range want {
		got := append([]string(nil), unclaimed[atom]...)
		slices.Sort(got)
		if !slices.Equal(got, w) {
			t.Errorf("%s: unclaimed %v, want %v", atom, got, w)
		}
	}
}
