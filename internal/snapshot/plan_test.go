package snapshot

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Story 016 C5 — `apply --dry-run` parity with Apply (R6).
//
// PlanApply must preview every side effect Apply performs, in Apply's own
// order: WriteEngineConfig (the per-subvolume snapper configs, then their
// SNAPPER_CONFIGS registration), then ensureSnapshotSubvolumes, then the
// systemd scheduler. The wants below are FULL slices compared element by
// element, because the ORDER is the requirement: a preview whose lines are all
// present but shuffled no longer describes the command that will run — and a
// preview that understates apply is exactly what kept 016's bugs 1 and 2
// invisible in the field.
//
// Paths are built from the package's own vars (snapperConfigsDir,
// snapperConfdPath, systemdUnitDir) rather than hardcoded under /etc, so these
// cases assert the LINE SHAPE and stay correct next to the tests that redirect
// those vars at a temp dir. The prose of every line is spelled out literally —
// it is what the operator reads.
// ---------------------------------------------------------------------------

// planApplySnapperConfig builds a snapper config over subvolumes, scheduled
// through backend ("" = no schedule), for the PlanApply cases below.
func planApplySnapperConfig(backend string, subvolumes ...string) *Config {
	return &Config{
		Engine:   EngineConfig{Driver: "snapper", Subvolumes: subvolumes},
		Schedule: ScheduleConfig{Backend: backend},
	}
}

// formatPlanLines renders a plan one line per row for failure messages, so a
// mismatch shows WHERE the order diverged instead of one unreadable dump.
func formatPlanLines(lines []string) string {
	if len(lines) == 0 {
		return "\n  (empty)"
	}
	return "\n  " + strings.Join(lines, "\n  ")
}

// TestPlanApply_SnapperMirrorsApplyOrder: over the snapper driver the plan
// previews the per-subvolume configs, their registration in the conf.d file
// (016 R6.1), the .snapshots provisioning of every managed subvolume
// (016 R6.2), and finally the systemd units — in exactly the order Apply
// performs them (config → provisioning → schedule). "/" and "/home" also cover
// snapperConfigName's two branches: the "root" special case and the
// slash-trimmed path.
func TestPlanApply_SnapperMirrorsApplyOrder(t *testing.T) {
	cfg := planApplySnapperConfig("systemd", "/", "/home")

	got := PlanApply(cfg, "/etc/bentoo/snapshot.toml")
	want := []string{
		"would write snapper config " + filepath.Join(snapperConfigsDir, "root"),
		"would write snapper config " + filepath.Join(snapperConfigsDir, "home"),
		"would register root home in " + snapperConfdPath,
		"would ensure /.snapshots exists (btrfs subvolume create if missing)",
		"would ensure /home/.snapshots exists (btrfs subvolume create if missing)",
		"would write systemd unit " + filepath.Join(systemdUnitDir, serviceUnitName),
		"would write systemd unit " + filepath.Join(systemdUnitDir, timerUnitName),
		"would run systemctl daemon-reload",
		"would run systemctl enable --now " + timerUnitName,
	}
	if !slices.Equal(got, want) {
		t.Errorf("PlanApply(snapper) plan mismatch\ngot:%s\nwant:%s",
			formatPlanLines(got), formatPlanLines(want))
	}
}

// TestPlanApply_SnapperWithoutScheduleStillPlansRegistrationAndProvisioning:
// the registration (R6.1) and provisioning (R6.2) lines belong to Apply's
// unconditional prefix, not to its schedule branch — Apply provisions BEFORE it
// checks Schedule.Backend, precisely so an unscheduled config is still prepared
// for a later `run` (016 R5.1). A plan that emitted them only alongside the
// systemd lines would understate `apply` for exactly that config.
func TestPlanApply_SnapperWithoutScheduleStillPlansRegistrationAndProvisioning(t *testing.T) {
	cfg := planApplySnapperConfig("", "/home")

	got := PlanApply(cfg, "/etc/bentoo/snapshot.toml")
	want := []string{
		"would write snapper config " + filepath.Join(snapperConfigsDir, "home"),
		"would register home in " + snapperConfdPath,
		"would ensure /home/.snapshots exists (btrfs subvolume create if missing)",
	}
	if !slices.Equal(got, want) {
		t.Errorf("PlanApply(snapper, no schedule) plan mismatch\ngot:%s\nwant:%s",
			formatPlanLines(got), formatPlanLines(want))
	}
}

// TestPlanApply_BtrbkPlanUnchanged: 016 R6 adds lines to the snapper branch
// ONLY. The btrbk driver still previews its single rendered btrbk.conf plus the
// systemd block — no registration line, no .snapshots line, since btrbk needs
// neither. The want is the complete slice, so any leakage of a snapper-only
// line into this driver fails loudly. The ship entry is here to prove ships
// contribute nothing to the apply plan (they belong to PlanRun).
func TestPlanApply_BtrbkPlanUnchanged(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "snapshot.toml")
	cfg := &Config{
		Engine:   EngineConfig{Driver: "btrbk", Subvolumes: []string{"/", "/home"}},
		Ship:     []ShipConfig{{Type: "ssh", Target: "u@h:/p"}},
		Schedule: ScheduleConfig{Backend: "systemd", OnCalendar: "daily"},
	}

	got := PlanApply(cfg, configPath)
	want := []string{
		"would write engine config " + filepath.Join(dir, "btrbk.conf"),
		"would write systemd unit " + filepath.Join(systemdUnitDir, serviceUnitName),
		"would write systemd unit " + filepath.Join(systemdUnitDir, timerUnitName),
		"would run systemctl daemon-reload",
		"would run systemctl enable --now " + timerUnitName,
	}
	if !slices.Equal(got, want) {
		t.Errorf("PlanApply(btrbk) plan changed — R6 must touch the snapper branch only\ngot:%s\nwant:%s",
			formatPlanLines(got), formatPlanLines(want))
	}
}

// TestPlanApply_SnapperPlanIsPureRegardlessOfFilesystem: the plan is identical
// whether or not <subvolume>/.snapshots already exists on disk (016 R6.3).
//
// This is what keeps --dry-run a preview rather than a partial execution:
// statting the host to predict which subvolumes are already provisioned would
// make the output depend on the machine it ran on, so the same config would
// preview differently for two operators. The line therefore states its intent
// unconditionally ("if missing").
//
// The fixture is deliberately asymmetric — one subvolume with a real
// .snapshots directory, one without, plus a path that certainly exists ("/")
// and one that certainly does not — so "same shape for all four" means the code
// ignored the filesystem, not that there was nothing to notice.
func TestPlanApply_SnapperPlanIsPureRegardlessOfFilesystem(t *testing.T) {
	dir := t.TempDir()
	provisioned := filepath.Join(dir, "provisioned")
	bare := filepath.Join(dir, "bare")
	if err := os.MkdirAll(filepath.Join(provisioned, snapshotsDirName), 0o750); err != nil {
		t.Fatalf("prepare provisioned subvolume: %v", err)
	}
	if err := os.MkdirAll(bare, 0o750); err != nil {
		t.Fatalf("prepare bare subvolume: %v", err)
	}
	if _, err := os.Stat(filepath.Join(provisioned, snapshotsDirName)); err != nil {
		t.Fatalf("fixture: provisioned .snapshots is not there: %v", err)
	}
	if _, err := os.Stat(filepath.Join(bare, snapshotsDirName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fixture: bare subvolume unexpectedly has .snapshots: %v", err)
	}

	for _, sv := range []string{provisioned, bare, "/", "/nonexistent-xyz"} {
		name := snapperConfigName(sv)
		got := PlanApply(planApplySnapperConfig("", sv), "/etc/bentoo/snapshot.toml")
		want := []string{
			"would write snapper config " + filepath.Join(snapperConfigsDir, name),
			"would register " + name + " in " + snapperConfdPath,
			fmt.Sprintf("would ensure %s exists (btrfs subvolume create if missing)",
				filepath.Join(sv, snapshotsDirName)),
		}
		if !slices.Equal(got, want) {
			t.Errorf("PlanApply(snapper, subvolume %s) plan depends on the filesystem (016 R6.3)\ngot:%s\nwant:%s",
				sv, formatPlanLines(got), formatPlanLines(want))
		}
	}
}

// TestPlanApply_SnapperWithoutSubvolumesNamesTheEmptyRegistration: with no
// subvolumes configured — which Validate only warns about (config.go) rather
// than rejecting — apply still calls ensureSnapperRegistered, and against a
// conf.d holding no active assignment that call creates an empty
// SNAPPER_CONFIGS. The plan therefore keeps the registration line rather than
// dropping it, and names the empty case instead of rendering a blank gap where
// the config names would be (016 R6.1).
func TestPlanApply_SnapperWithoutSubvolumesNamesTheEmptyRegistration(t *testing.T) {
	got := PlanApply(planApplySnapperConfig(""), "/etc/bentoo/snapshot.toml")
	want := []string{"would register (no configs) in " + snapperConfdPath}

	if !slices.Equal(got, want) {
		t.Errorf("PlanApply(snapper, no subvolumes) plan mismatch\ngot:%s\nwant:%s",
			formatPlanLines(got), formatPlanLines(want))
	}
	for _, line := range got {
		if strings.Contains(line, "  ") {
			t.Errorf("plan line has a blank gap where a value belongs: %q", line)
		}
	}
}
