package autoupdate

// Gap tests the Test Advisor recorded for bentoo story 003, sub-task 3.3
// (R5.3): the age gate and the cooldown are two gates with two reasons, and
// neither report is mistaken for the other.

import (
	"slices"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/registry"
)

func TestCooldownMinAge_TheTwoGatesReportDistinctReasons(t *testing.T) {
	// The age gate holds a young candidate at check time, before any cooldown
	// step: nothing is queued, so nothing reaches the applier to be masked.
	published := minAgeNow.Add(-1 * minAgeDay)
	_, run := minAgeSetup(t, minAgeBody(t, minAgeTimes(minAgeStamp(published), minAgeNow.Add(-90*minAgeDay))), minAgeGated)
	held := run.check(t, minAgePkg, true)
	held.expectHeld(t)
	if !strings.Contains(held.report, "held by min_age_days") {
		t.Errorf("the age gate's report does not say \"held by min_age_days\":\n%s", held.report)
	}
	if strings.Contains(strings.ToLower(held.report), "cooldown") {
		t.Errorf("the age gate's hold is reported as a cooldown:\n%s", held.report)
	}

	// An old-enough candidate is promoted and masked by the cooldown, whose
	// report names the cooldown and never the age gate.
	const pkg = "app-misc/cooldown-aged"
	cfg := registry.PackageConfig{
		URL: "https://example.org/aged/releases.json", Parser: "json", Path: "latest.version",
		MinAgeDays: 3, PublishedPath: "latest.published_at",
	}
	f := newCooldownFixture(t, []cooldownBump{{pkg, "1.0", "1.1"}}, nil, map[string]registry.PackageConfig{pkg: cfg}, cooldownEmptyBlock)
	if r := applyOne(t, f, pkg); !r.Success || len(r.CooldownAdded) != 1 {
		t.Fatalf("the aged bump was not promoted and masked: success=%v added=%+v err=%v", r.Success, r.CooldownAdded, r.Error)
	}
	events := f.logs.cooldownEvents(pkg, "1.1")
	if len(events) != 1 {
		t.Fatalf("%d cooldown events, want 1: %+v", len(events), events)
	}
	if strings.Contains(events[0].msg, "min_age") {
		t.Errorf("the cooldown event names the age gate: %q", events[0].msg)
	}
}

// Task 3 tech review, decided with the requester (R2.1 per release line): a
// directory holding two release lines, each with a masked version, keeps one
// installable version per line under --clean and the standalone sweep.
func TestCooldownHeld_EachReleaseLineKeepsItsOwnPredecessor(t *testing.T) {
	const pkg = "app-misc/cooldown-lines"
	tree := map[string][]string{pkg: {"1.1", "1.2", "1.3", "2.0", "2.1"}}
	cfgs := map[string]registry.PackageConfig{
		pkg + "@stable": {Version: "1.3", Series: `^1\.`},
		pkg + "@dev":    {Version: "2.1", Series: `^2\.`},
	}
	mask := "# BEGIN bentoo-cooldown format=1 days=7\n" +
		"# Managed by `bentoo overlay autoupdate`. Edit with `bentoo overlay cooldown`, never by hand.\n" +
		"# cooldown since=2026-11-05T00:00:00Z origin=autoupdate\n=" + pkg + "-1.3\n" +
		"# cooldown since=2026-11-06T00:00:00Z origin=autoupdate\n=" + pkg + "-2.1\n" +
		"# END bentoo-cooldown\n"
	overlay := sweepOverlay(t, tree, mask)

	batch, err := PlanOverlaySweep(nil, overlay, cfgs, "")
	if err != nil {
		t.Fatalf("PlanOverlaySweep: %v", err)
	}
	removed := planFor(t, batch, pkg).Remove
	for _, keep := range []string{"1.2", "1.3", "2.0", "2.1"} {
		if slices.Contains(removed, keep) {
			t.Errorf("the sweep plans to remove %s; each line must keep its masked version and its predecessor (removals %v)", keep, removed)
		}
	}
	if !slices.Equal(removed, []string{"1.1"}) {
		t.Errorf("removals = %v, want [1.1]", removed)
	}
}
