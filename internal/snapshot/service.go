package snapshot

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// BtrbkConfPath returns where the rendered btrbk.conf lives for a given
// snapshot.toml path — the sibling file btrbk.conf in the same directory. An
// empty snapshotConfPath falls back to DefaultBtrbkConfPath.
func BtrbkConfPath(snapshotConfPath string) string {
	if snapshotConfPath == "" {
		return DefaultBtrbkConfPath
	}
	return filepath.Join(filepath.Dir(snapshotConfPath), "btrbk.conf")
}

// WriteBtrbkConf renders the btrbk.conf for cfg and writes it next to the
// snapshot.toml at configPath (R5.2). Called by `apply` and by `run` so the run
// is self-contained even if `apply` was never executed.
func WriteBtrbkConf(cfg *Config, configPath string) error {
	return writeBtrbkConf(BtrbkConfPath(configPath), cfg.Engine, collectShipTargets(cfg.Ship))
}

// WriteEngineConfig materializes the native config for the configured engine
// driver (R2.1): "btrbk" renders btrbk.conf next to the snapshot.toml at
// configPath (behavior unchanged, R6.2); "snapper" ensures the per-subvolume
// configs under snapperConfigsDir (configPath is unused there — snapper's
// config location is fixed). An unknown driver fails with ErrInvalidDriver.
func WriteEngineConfig(cfg *Config, configPath string) error {
	switch cfg.Engine.Driver {
	case "btrbk":
		return WriteBtrbkConf(cfg, configPath)
	case "snapper":
		return ensureSnapperConfigs(cfg)
	default:
		return fmt.Errorf("%w: engine driver %q", ErrInvalidDriver, cfg.Engine.Driver)
	}
}

// Apply materializes the native config and scheduler for cfg (R5.2, R4.1, R2.2):
// it renders+writes the engine's native config (driver-aware) and, when a
// systemd schedule is configured, installs and enables the timer. run is the
// injectable subprocess seam.
func Apply(ctx context.Context, cfg *Config, configPath string, run Runner) error {
	if err := WriteEngineConfig(cfg, configPath); err != nil {
		return err
	}
	if cfg.Schedule.Backend == "" {
		return nil // no scheduling requested
	}
	sched, err := newScheduler(cfg.Schedule, configPath, run)
	if err != nil {
		return err
	}
	return sched.Apply(ctx, cfg.Schedule)
}

// List returns the local snapshots per configured subvolume (R5.4). A subvolume
// that errors aborts the listing with that error.
func (m *Manager) List(ctx context.Context) (map[string][]Snapshot, error) {
	out := make(map[string][]Snapshot, len(m.subvolumes))
	for _, sv := range m.subvolumes {
		snaps, err := m.engine.List(ctx, sv)
		if err != nil {
			return out, err
		}
		out[sv] = snaps
	}
	return out, nil
}

// Subvolumes exposes the configured subvolumes (for CLI iteration/reporting).
func (m *Manager) Subvolumes() []string { return m.subvolumes }

// TimerState reports the systemd timer's enablement via
// `systemctl is-enabled bentoo-snapshot.timer` (R5.5). It is best-effort: a
// non-zero exit (e.g. the timer is disabled or absent) is mapped to the trimmed
// output rather than a hard error, so `status` always has something to print.
func TimerState(ctx context.Context, run Runner) string {
	if run == nil {
		run = defaultRunner()
	}
	out, _ := run.Run(ctx, "systemctl", []string{"is-enabled", timerUnitName}, nil)
	state := strings.TrimSpace(string(out))
	if state == "" {
		return "unknown"
	}
	return state
}
