package autoupdate

// Authored for bentoo story 003 (autoupdate cooldown), sub-task 4.3 —
// R1.1, R8.2, R6.2 (producer half).
//
// Data-contract check: what an apply reports in ApplyResult.CooldownAdded is
// exactly what it wrote to the block — the summary (R8.2) prints from the
// result, `status --json` (R6.2) reads the block, so the two must be the same
// entry field for field (Package, Version, Since, Group, Origin).
//
// newCooldownFixture comes from applier_cooldown_test.go (3.1).

import (
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/registry"
)

func TestCooldownContract_ApplyResultCarriesExactlyTheBlockEntry(t *testing.T) {
	const a, b = "dev-lang/cc-a", "dev-lang/cc-b"
	f := newCooldownFixture(t, []cooldownBump{{a, "1.0", "1.1"}, {b, "2.0", "2.1"}}, nil,
		map[string]registry.PackageConfig{a: {CooldownWith: []string{b}}, b: {CooldownWith: []string{a}}},
		cooldownEmptyBlock)

	results := applyAll(t, f)
	blk := f.block(t)
	for pkg, version := range map[string]string{a: "1.1", b: "2.1"} {
		r := results[pkg]
		if r == nil || !r.Success {
			t.Fatalf("%s did not apply: %+v", pkg, r)
		}
		if len(r.CooldownAdded) != 1 {
			t.Errorf("%s: CooldownAdded = %+v, want exactly its own entry", pkg, r.CooldownAdded)
			continue
		}
		got := r.CooldownAdded[0]
		want, ok := entryFor(blk, pkg, version)
		if !ok {
			t.Errorf("%s-%s is reported added but is not in the block", pkg, version)
			continue
		}
		if got.Package != want.Package || got.Version != want.Version || !got.Since.Equal(want.Since) ||
			got.Group != want.Group || got.Origin != want.Origin {
			t.Errorf("%s: the result reports %+v, the block holds %+v", pkg, got, want)
		}
	}
}
