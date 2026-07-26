package snapshot

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Story 018 R5 — the dry-run mirrors the new apply.
//
// The plan is the only thing an operator can inspect before letting apply touch
// the system, so it has to describe what apply actually does. During story 016
// it did not: the preview named files that apply no longer writes, and the
// mismatch is part of why the broken provisioning went undiagnosed in the field.
// ---------------------------------------------------------------------------

// planFor runs PlanApply and returns its lines joined, for substring assertions
// that do not care about line boundaries.
func planFor(t *testing.T, cfg *Config) string {
	t.Helper()
	return strings.Join(PlanApply(cfg, "/etc/bentoo/snapshot.toml"), "\n")
}

// TestPlanApply_SnapperNamesTheCommands: the snapper plan announces the two
// commands apply issues per subvolume, with the config name and subvolume it
// would use — and no longer claims it writes a config file, which stopped being
// true when provisioning moved to snapper's own API (018 R5).
func TestPlanApply_SnapperNamesTheCommands(t *testing.T) {
	cfg := &Config{Engine: EngineConfig{
		Driver:     "snapper",
		Subvolumes: []string{"/", "/home"},
		Retention:  Retention{Hourly: 24, Daily: 7},
	}}

	got := planFor(t, cfg)

	for _, want := range []string{
		"snapper -c root create-config /",
		"snapper -c home create-config /home",
		"snapper -c root set-config",
		"snapper -c home set-config",
		"TIMELINE_LIMIT_HOURLY=24",
		"TIMELINE_LIMIT_DAILY=7",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("plan is missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "/etc/snapper/configs") {
		t.Errorf("plan still promises to write a config file by hand:\n%s", got)
	}
	if strings.Contains(got, "SUBVOLUME=") {
		t.Errorf("plan shows SUBVOLUME in set-config, which snapper refuses:\n%s", got)
	}
}

// TestPlanApply_SnapperDoesNotProbeTheHost: the plan must not consult snapper or
// the filesystem to predict which subvolumes are already covered — a dry-run
// that probes is a partial execution, not a preview (016 R6.3, carried into
// 018 R5). The unconditional "if not already covered" is what keeps the line
// honest without asking.
//
// The guard is structural: PlanApply takes no Runner and no context, so it
// CANNOT reach snapper. This test pins the observable half — the line is emitted
// for a subvolume whose config plainly exists in the seam-visible world.
func TestPlanApply_SnapperDoesNotProbeTheHost(t *testing.T) {
	stubSnapshotsDirSeams(t, nil, "/home/.snapshots") // "already provisioned"
	cfg := &Config{Engine: EngineConfig{Driver: "snapper", Subvolumes: []string{"/home"}}}

	got := planFor(t, cfg)

	if !strings.Contains(got, "create-config /home") {
		t.Errorf("plan dropped the create-config line by inspecting the host:\n%s", got)
	}
	if !strings.Contains(got, "if not already covered") {
		t.Errorf("plan states the create-config unconditionally without saying so:\n%s", got)
	}
}

// TestPlanApply_BtrbkUnchanged: the btrbk driver still previews a single
// btrbk.conf write. 018 changed the snapper path only, and a regression here
// would mean the shared plan helper leaked snapper's shape into btrbk's.
func TestPlanApply_BtrbkUnchanged(t *testing.T) {
	cfg := &Config{Engine: EngineConfig{Driver: "btrbk", Subvolumes: []string{"/home"}}}

	got := planFor(t, cfg)

	if want := "would write engine config /etc/bentoo/btrbk.conf"; !strings.Contains(got, want) {
		t.Errorf("plan missing %q:\n%s", want, got)
	}
	if strings.Contains(got, "snapper") {
		t.Errorf("btrbk plan mentions snapper:\n%s", got)
	}
}

// TestPlanApply_UnknownDriverContributesNothing: an unknown driver yields no
// engine line at all, so a misconfigured file previews as "nothing to do"
// rather than inventing a command. Validate rejects it before apply anyway.
func TestPlanApply_UnknownDriverContributesNothing(t *testing.T) {
	cfg := &Config{Engine: EngineConfig{Driver: "zfs", Subvolumes: []string{"/home"}}}

	if got := planFor(t, cfg); got != "" {
		t.Errorf("unknown driver produced plan lines:\n%s", got)
	}
}
