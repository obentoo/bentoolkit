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
	default:
		return nil, fmt.Errorf("%w: engine driver %q", ErrInvalidDriver, cfg.Driver)
	}
}

// newShipper builds the shipper selected by cfg.Type.
func newShipper(cfg ShipConfig) (Shipper, error) {
	switch cfg.Type {
	case "ssh":
		return newSSHShipper(cfg)
	default:
		return nil, fmt.Errorf("%w: ship type %q", ErrInvalidDriver, cfg.Type)
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

// newNotifier builds the notifier selected by cfg.Driver. An empty/"none" driver
// selects the no-op default (story 004 ships no real backend; 005 adds them).
func newNotifier(cfg NotifyConfig) (Notifier, error) {
	switch cfg.Driver {
	case "", "none":
		return noopNotifier{}, nil
	default:
		return nil, fmt.Errorf("%w: notify driver %q", ErrInvalidDriver, cfg.Driver)
	}
}
