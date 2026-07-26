package snapshot

import (
	"fmt"
	"path/filepath"
)

// plan.go — pure dry-run plan helpers (story 008, R2 + R3.2).
//
// The --dry-run decision lives in the cmd layer; this file contributes the
// accurate "would ..." lines because the package owns the data they describe:
// the driver-aware engine config paths (BtrbkConfPath, snapperConfigsDir +
// snapperConfigName), the systemd unit names (serviceUnitName/timerUnitName
// under systemdUnitDir), the run pipeline shape (per-subvolume create+prune,
// then one ship per [[ship]] entry — manager.go's Run order), and the
// on-demand prune shape (engine prune per subvolume, then the remote GFS per
// archive ship — Manager.Prune's order). Every helper here is a PURE function:
// no I/O, no Runner, no state.

// PlanApply returns the actions `snapshot apply` would perform for cfg as
// human-readable "would ..." lines (008 R2.1): the engine config file(s) it
// would write (driver-aware, mirroring WriteEngineConfig) and — when a systemd
// schedule is configured — the unit files it would install plus the systemctl
// invocations (mirroring systemdScheduler.Apply). Callers validate cfg first;
// an unknown engine driver contributes no engine line.
func PlanApply(cfg *Config, configPath string) []string {
	lines := planEngineConfig(cfg, configPath)
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
// snapperConfigsDir.
func planEngineConfig(cfg *Config, configPath string) []string {
	switch cfg.Engine.Driver {
	case "btrbk":
		return []string{fmt.Sprintf("would write engine config %s", BtrbkConfPath(configPath))}
	case "snapper":
		lines := make([]string, 0, len(cfg.Engine.Subvolumes))
		for _, sv := range cfg.Engine.Subvolumes {
			lines = append(lines, fmt.Sprintf("would write snapper config %s",
				filepath.Join(snapperConfigsDir, snapperConfigName(sv))))
		}
		return lines
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
