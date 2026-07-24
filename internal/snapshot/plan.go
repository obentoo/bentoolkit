package snapshot

import (
	"fmt"
	"path/filepath"
	"strings"
)

// plan.go — pure dry-run plan helpers (story 008, R2 + R3.2).
//
// The --dry-run decision lives in the cmd layer; this file contributes the
// accurate "would ..." lines because the package owns the data they describe:
// the driver-aware engine config paths (BtrbkConfPath, snapperConfigsDir +
// snapperConfigName), the snapper driver's two further apply-time side effects
// (the SNAPPER_CONFIGS registration in snapperConfdPath and the
// <subvolume>/.snapshots provisioning — 016 R6), the systemd unit names
// (serviceUnitName/timerUnitName under systemdUnitDir), the run pipeline shape
// (per-subvolume create+prune, then one ship per [[ship]] entry — manager.go's
// Run order), and the on-demand prune shape (engine prune per subvolume, then
// the remote GFS per archive ship — Manager.Prune's order). Every helper here is
// a PURE function: no I/O, no Runner, no state — which 016 R6.3 makes a
// requirement, not just a style: a plan line describes an intent, it never
// probes the host to predict an outcome.

// PlanApply returns the actions `snapshot apply` would perform for cfg as
// human-readable "would ..." lines (008 R2.1): the engine config file(s) it
// would write (driver-aware, mirroring WriteEngineConfig), for the snapper
// driver the per-subvolume .snapshots subvolume it would provision (016 R6.2),
// and — when a systemd schedule is configured — the unit files it would install
// plus the systemctl invocations (mirroring systemdScheduler.Apply). Callers
// validate cfg first; an unknown engine driver contributes no engine line.
//
// The line order mirrors Apply's own sequence — engine config (which for
// snapper includes registering the config names, planEngineConfig's concern),
// then the .snapshots provisioning, then the schedule — so the preview reads as
// the command actually runs. A preview that understates apply is what let the
// missing registration and provisioning go undiagnosed in the field.
//
// The provisioning line states its intent unconditionally ("if missing") and
// deliberately does NOT consult statPath to predict which subvolumes are
// already provisioned (016 R6.3): a dry-run that probes the filesystem stops
// being a pure preview and becomes a partial execution.
func PlanApply(cfg *Config, configPath string) []string {
	lines := planEngineConfig(cfg, configPath)
	if cfg.Engine.Driver == "snapper" {
		for _, sv := range cfg.Engine.Subvolumes {
			lines = append(lines, fmt.Sprintf("would ensure %s exists (btrfs subvolume create if missing)",
				filepath.Join(sv, snapshotsDirName)))
		}
	}
	if cfg.Schedule.Backend == "systemd" {
		lines = append(lines,
			fmt.Sprintf("would write systemd unit %s", filepath.Join(systemdUnitDir, serviceUnitName)),
			fmt.Sprintf("would write systemd unit %s", filepath.Join(systemdUnitDir, timerUnitName)),
			"would run systemctl daemon-reload",
			fmt.Sprintf("would run systemctl enable --now %s", timerUnitName),
		)
	}
	return lines
}

// PlanRun returns the pipeline `snapshot run` would execute for cfg as
// human-readable "would ..." lines (008 R2.2): per subvolume the engine
// create+prune, then one ship line per [[ship]] target, in manager.go's Run
// order.
func PlanRun(cfg *Config) []string {
	var lines []string
	for _, sv := range cfg.Engine.Subvolumes {
		lines = append(lines, fmt.Sprintf("would snapshot %s via engine %s (create + prune)", sv, cfg.Engine.Driver))
		for _, sh := range cfg.Ship {
			lines = append(lines, fmt.Sprintf("would ship %s to %s (%s)", sv, shipDestination(sh), sh.Type))
		}
	}
	return lines
}

// PlanPrune returns the actions `snapshot prune` would perform for cfg as
// human-readable "would ..." lines (008 R3.2): per subvolume the engine-native
// prune (driver-aware invocation summary), then one remote GFS line per
// in-scope archive ship with the retention it would enforce — Manager.Prune's
// order. A non-empty shipScope mirrors the real --ship scoping: the engine
// line(s) are omitted and only the named ship's remote is planned. Callers
// reject an unknown shipScope BEFORE planning (the cmd layer exits 1), so an
// unmatched scope here simply yields no lines.
func PlanPrune(cfg *Config, shipScope string) []string {
	var lines []string
	if shipScope == "" {
		for _, sv := range cfg.Engine.Subvolumes {
			lines = append(lines, fmt.Sprintf("would prune %s via engine %s (%s)",
				sv, cfg.Engine.Driver, enginePruneCommand(cfg.Engine.Driver)))
		}
	}
	for _, sh := range cfg.Ship {
		if sh.Type != "archive" {
			continue // GFS sweeps archive remotes only (ssh/restic prune elsewhere).
		}
		if shipScope != "" && shipName(sh) != shipScope {
			continue
		}
		lines = append(lines, fmt.Sprintf("would apply GFS retention to %s (ship %q, %s)",
			sh.Remote, shipName(sh), retentionSummary(cfg.Engine.Retention)))
	}
	return lines
}

// enginePruneCommand names the native prune invocation per engine driver for
// plan lines, mirroring btrbkEngine.Prune / snapperEngine.Prune argv shapes. An
// unknown driver (rejected by Validate anyway) falls back to a generic label.
func enginePruneCommand(driver string) string {
	switch driver {
	case "btrbk":
		return "btrbk clean"
	case "snapper":
		return "snapper cleanup timeline"
	default:
		return "native prune"
	}
}

// retentionSummary renders the GFS policy for plan lines: the nonzero counts in
// btrbk preserve grammar ("24h 7d 4w 6m"), or the keep-everything contract when
// no granularity is configured (gfsSelect's all-zero short-circuit).
func retentionSummary(r Retention) string {
	if line := retentionLine(r); line != "" {
		return "keep " + line
	}
	return "no GFS policy: keep everything"
}

// shipName resolves the effective name of a ship entry the way the built
// shippers do (Shipper.Name(): the configured name, falling back to the type),
// so plan-time --ship scoping matches Manager.Prune's run-time scoping for
// unnamed entries.
func shipName(sh ShipConfig) string {
	if sh.Name != "" {
		return sh.Name
	}
	return sh.Type
}

// planEngineConfig returns the engine-config write line(s) for cfg's driver,
// mirroring WriteEngineConfig: btrbk renders one btrbk.conf next to the
// snapshot.toml at configPath; snapper ensures one config per subvolume under
// snapperConfigsDir and then registers every one of those config names in
// snapperConfdPath's SNAPPER_CONFIGS list (016 R6.1). The registration line
// belongs here rather than in PlanApply because ensureSnapperConfigs performs it
// as part of writing the configs — the plan's structure mirrors the code's.
func planEngineConfig(cfg *Config, configPath string) []string {
	switch cfg.Engine.Driver {
	case "btrbk":
		return []string{fmt.Sprintf("would write engine config %s", BtrbkConfPath(configPath))}
	case "snapper":
		lines := make([]string, 0, len(cfg.Engine.Subvolumes)+1)
		names := make([]string, 0, len(cfg.Engine.Subvolumes))
		for _, sv := range cfg.Engine.Subvolumes {
			name := snapperConfigName(sv)
			names = append(names, name)
			lines = append(lines, fmt.Sprintf("would write snapper config %s",
				filepath.Join(snapperConfigsDir, name)))
		}
		// The registration line is emitted even with no subvolumes configured,
		// because ensureSnapperRegistered still runs and can still write the file
		// (an absent SNAPPER_CONFIGS assignment is created empty). Naming the
		// empty case keeps the line readable instead of rendering a blank gap.
		registered := strings.Join(names, " ")
		if registered == "" {
			registered = "(no configs)"
		}
		return append(lines, fmt.Sprintf("would register %s in %s", registered, snapperConfdPath))
	default:
		return nil
	}
}

// shipDestination returns the destination locator of a ship entry for plan
// lines — the field its type actually uses: the ssh target, the restic repo,
// or the archive remote. An unknown type (rejected by Validate anyway) falls
// back to the configured name.
func shipDestination(sh ShipConfig) string {
	switch sh.Type {
	case "ssh":
		return sh.Target
	case "restic":
		return sh.Repo
	case "archive":
		return sh.Remote
	default:
		return sh.Name
	}
}
