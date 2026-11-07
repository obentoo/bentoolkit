package autoupdate

// Authored for bentoo story 003 (autoupdate cooldown), sub-task 3.3 (applier
// half) — R5.3.
//
// Contract: R5.3 — a record with both min_age_days and cooldown gets the age
// gate BEFORE the bump (checker side, unchanged) and the cooldown AFTER it.
// The two are independent: an age-gated record that reaches the applier is
// masked like any other. The hostile reading is that "already aged" exempts
// a bump from cooldown; it does not — only cooldown_exempt does.
//
// newCooldownFixture comes from applier_cooldown_test.go (3.1).

import (
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/registry"
)

// TestCooldownMinAge_AnAgeGatedBumpIsStillMasked: the record carries a valid
// age gate; the bump that passed it is masked with origin autoupdate.
func TestCooldownMinAge_AnAgeGatedBumpIsStillMasked(t *testing.T) {
	const pkg = "app-misc/cooldown-aged"
	cfg := registry.PackageConfig{
		URL:           "https://example.org/aged/releases.json",
		Parser:        "json",
		Path:          "latest.version",
		MinAgeDays:    3,
		PublishedPath: "latest.published_at",
	}
	f := newCooldownFixture(t, []cooldownBump{{pkg, "1.0", "1.1"}}, nil,
		map[string]registry.PackageConfig{pkg: cfg}, cooldownEmptyBlock)

	r := applyOne(t, f, pkg)
	if !r.Success {
		t.Fatalf("the apply failed: %v", r.Error)
	}
	e, ok := entryFor(f.block(t), pkg, "1.1")
	if !ok {
		t.Fatalf("a bump that passed min_age_days was promoted unmasked:\n%s", f.mask(t))
	}
	if e.Origin != "autoupdate" {
		t.Errorf("Origin = %q, want autoupdate", e.Origin)
	}
}
