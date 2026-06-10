package snapshot

import "fmt"

// Driver factories select a concrete implementation from a config string, in the
// switch-with-ErrInvalidDriver-default style of internal/common/provider (AD2).
//
// The factory signatures carry extra wiring beyond the design §5 sketch
// (newEngine also takes ship targets + a Runner; newScheduler takes the
// snapshot.toml path + a Runner): the mock seam (R2.4) and the btrbk-target /
// systemd ExecStart rendering require them. The interface method sets are
// unchanged — this is a constructor call-surface concern only.

// newEngine builds the engine selected by cfg.Driver. targets are the ssh remote
// targets contributed to the rendered btrbk.conf; run is the (mockable) seam.
func newEngine(cfg EngineConfig, targets []string, run Runner) (Engine, error) {
	switch cfg.Driver {
	case "btrbk":
		return newBtrbkEngine(cfg, targets, run), nil
	case "snapper":
		// snapper does not use ship targets — remote transfer is the shippers'
		// job, so the targets wiring stays btrbk-only (R6.2).
		return newSnapperEngine(cfg, run), nil
	default:
		return nil, fmt.Errorf("%w: engine driver %q", ErrInvalidDriver, cfg.Driver)
	}
}

// newShipper builds the shipper selected by cfg.Type. run is the (mockable)
// subprocess seam (nil → production execRunner) used by the restic shipper for its
// mount/backup/forget commands; retention is the engine's policy, mapped to
// restic's --keep-* flags during forget --prune. The ssh shipper ignores both (its
// transfer is delegated to btrbk), matching the newEngine(cfg, targets, run)
// precedent of carrying wiring beyond a single driver's needs.
func newShipper(cfg ShipConfig, run Runner, retention Retention) (Shipper, error) {
	if run == nil {
		run = defaultRunner()
	}
	switch cfg.Type {
	case "ssh":
		return newSSHShipper(cfg)
	case "restic":
		return newResticShipper(cfg, run, retention), nil
	case "archive":
		// retention is the [engine.retention] GFS policy; the archive shipper applies
		// it to the rclone remote after a successful ship (T5.1, R4). An all-zero
		// policy makes the prune a no-op.
		return newArchiveShipper(cfg, run, retention), nil
	default:
		return nil, fmt.Errorf("%w: ship type %q", ErrInvalidDriver, cfg.Type)
	}
}

// newResticShipper assembles a resticShipper from cfg, the subprocess seam, and
// the retention policy. The snapshot is exposed to restic through a transient
// read-only mount (the default and currently only mount strategy); cfg.MountStrategy
// is reserved for selecting alternatives (e.g. a pre-existing bind mount) in a later
// task and is otherwise a no-op here.
func newResticShipper(cfg ShipConfig, run Runner, retention Retention) *resticShipper {
	return &resticShipper{
		name:         cfg.Name,
		repo:         cfg.Repo,
		passwordFile: cfg.PasswordFile,
		compression:  cfg.Compression,
		retention:    retention,
		run:          run,
		mount:        &transientMounter{run: run},
	}
}

// newScheduler builds the scheduler selected by cfg.Backend. configPath is the
// snapshot.toml path baked into the timer-driven `run`.
func newScheduler(cfg ScheduleConfig, configPath string, run Runner) (Scheduler, error) {
	switch cfg.Backend {
	case "systemd":
		return newSystemdScheduler(configPath, run), nil
	default:
		return nil, fmt.Errorf("%w: schedule backend %q", ErrInvalidDriver, cfg.Backend)
	}
}

// newNotifier is defined in notify.go (story 005, T3.1): it composes one driver per
// populated NotifyConfig sub-table and fans out behind the Notifier interface. The
// notifier factory lives beside the drivers it builds rather than here with the
// engine/shipper/scheduler factories.
