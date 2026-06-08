// Package snapshot orchestrates btrfs snapshot tooling (btrbk, systemd) through a
// single declarative TOML config. bentoolkit is an orchestrator, not a btrfs
// implementation: it renders native config for mature tools and coordinates them;
// no destructive btrfs logic is written in Go.
//
// This file holds the config model and path resolution. The four orchestration
// interfaces (Engine, Shipper, Notifier, Scheduler) live in their own files, the
// concrete drivers in engine_btrbk.go / ship_ssh.go / systemd.go, and the run
// pipeline in manager.go.
package snapshot

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/obentoo/bentoolkit/internal/common/logger"
)

// warnLogf emits a non-fatal warning. It is a package var (defaulting to
// logger.Warn) so tests can capture the warn-but-continue path of Validate,
// mirroring internal/autoupdate's warnLogf seam.
var warnLogf = logger.Warn

// Config is the parsed snapshot.toml. The engine produces snapshots, each ship
// replicates them, notify reports the outcome (no-op until story 005), and
// schedule installs the systemd timer that drives `bentoo snapshot run`.
type Config struct {
	Engine   EngineConfig   `toml:"engine"`
	Ship     []ShipConfig   `toml:"ship,omitempty"`
	Notify   NotifyConfig   `toml:"notify,omitempty"`
	Schedule ScheduleConfig `toml:"schedule,omitempty"`
}

// EngineConfig selects and configures the snapshot engine driver. Subvolumes are
// the btrfs subvolumes to snapshot; SnapshotDir is where the engine keeps them.
type EngineConfig struct {
	Driver      string    `toml:"driver"`
	Subvolumes  []string  `toml:"subvolumes,omitempty"`
	SnapshotDir string    `toml:"snapshot_dir,omitempty"`
	Retention   Retention `toml:"retention,omitempty"`
}

// Retention is a GFS-style retention policy delegated to the native engine
// (mapped to btrbk snapshot_preserve/target_preserve). The counts are how many of
// each interval to keep; PreserveMin is btrbk's snapshot_preserve_min (e.g.
// "latest" or "2d") — left as a string because btrbk accepts both a keyword and a
// duration there.
type Retention struct {
	Hourly      int    `toml:"hourly,omitempty"`
	Daily       int    `toml:"daily,omitempty"`
	Weekly      int    `toml:"weekly,omitempty"`
	Monthly     int    `toml:"monthly,omitempty"`
	PreserveMin string `toml:"preserve_min,omitempty"`
}

// ShipConfig is one replication target ([[ship]] array-of-tables). Type selects
// the shipper driver; Target is the destination (for ssh: user@host:/path).
type ShipConfig struct {
	Name   string `toml:"name,omitempty"`
	Type   string `toml:"type"`
	Target string `toml:"target,omitempty"`
}

// NotifyConfig selects and configures notification backends (story 005). On filters
// which run outcomes notify — a subset of {"success", "failure"}; an empty On means
// failure only (see shouldNotify). Each sub-table, when populated, activates one
// driver; the configured drivers fan out behind the Notifier interface (R4).
type NotifyConfig struct {
	On           []string           `toml:"on,omitempty"`
	Ntfy         NtfyConfig         `toml:"ntfy,omitempty"`
	Healthchecks HealthchecksConfig `toml:"healthchecks,omitempty"`
	Webhook      WebhookConfig      `toml:"webhook,omitempty"`
}

// NtfyConfig configures the ntfy driver (R1). URL is the topic URL; Token, when set,
// is sent via the Authorization header and is never logged (R1.3).
type NtfyConfig struct {
	URL   string `toml:"url,omitempty"`
	Token string `toml:"token,omitempty"`
}

// HealthchecksConfig configures the healthchecks.io driver (R2). PingURL is the base
// check URL (success → base, failure → /fail); Start optionally pings /start before
// the run (R2.3).
type HealthchecksConfig struct {
	PingURL string `toml:"ping_url,omitempty"`
	Start   bool   `toml:"start,omitempty"`
}

// WebhookConfig configures the generic webhook driver (R3). URL receives a POST with
// the serialized RunResult; Headers are applied to the request (R3.2). Header values
// holding secrets are never logged (R6.3).
type WebhookConfig struct {
	URL     string            `toml:"url,omitempty"`
	Headers map[string]string `toml:"headers,omitempty"`
}

// shouldNotify reports whether a run with the given outcome should notify, given
// the configured `on` filter (R4.3). The outcome is "failure" when failed, else
// "success"; notification fires only when that outcome is listed in on. An empty on
// defaults to notifying on failure only.
func shouldNotify(on []string, failed bool) bool {
	outcome := "success"
	if failed {
		outcome = "failure"
	}

	if len(on) == 0 {
		on = []string{"failure"}
	}

	for _, o := range on {
		if o == outcome {
			return true
		}
	}
	return false
}

// ScheduleConfig configures unit generation. Backend selects the scheduler driver
// ("systemd"; empty = no scheduling). Persistent is tri-state (*bool) so an unset
// value is distinguishable from an explicit false.
type ScheduleConfig struct {
	Backend         string `toml:"backend,omitempty"`
	OnCalendar      string `toml:"on_calendar,omitempty"`
	Persistent      *bool  `toml:"persistent,omitempty"`
	RandomizedDelay string `toml:"randomized_delay,omitempty"`
}

// ConfigPaths returns the snapshot.toml search paths in priority order:
//  1. /etc/bentoo/snapshot.toml          (system scope — the primary target)
//  2. $XDG_CONFIG_HOME/bentoo/snapshot.toml
//  3. ~/.config/bentoo/snapshot.toml
//
// When XDG_CONFIG_HOME is unset it defaults to ~/.config, which makes paths 2 and
// 3 coincide; the duplicate is dropped so callers see each location once.
func ConfigPaths() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	xdgConfig := os.Getenv("XDG_CONFIG_HOME")
	if xdgConfig == "" {
		xdgConfig = filepath.Join(home, ".config")
	}

	candidates := []string{
		"/etc/bentoo/snapshot.toml",
		filepath.Join(xdgConfig, "bentoo", "snapshot.toml"),
		filepath.Join(home, ".config", "bentoo", "snapshot.toml"),
	}

	paths := make([]string, 0, len(candidates))
	seen := make(map[string]bool, len(candidates))
	for _, p := range candidates {
		if seen[p] {
			continue
		}
		seen[p] = true
		paths = append(paths, p)
	}
	return paths, nil
}

// DefaultConfigPath returns the highest-priority config path (system scope). It is
// the write target for `apply`-generated config.
func DefaultConfigPath() (string, error) {
	paths, err := ConfigPaths()
	if err != nil {
		return "", err
	}
	return paths[0], nil
}

// FindConfigPath returns the first existing config path, or the default path when
// none exists yet (so callers have a stable location to create one).
func FindConfigPath() (string, error) {
	paths, err := ConfigPaths()
	if err != nil {
		return "", err
	}
	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	return paths[0], nil
}

// Load reads and parses the config from the first existing search path.
func Load() (*Config, error) {
	path, err := FindConfigPath()
	if err != nil {
		return nil, err
	}
	return LoadFrom(path)
}

// LoadFrom reads and parses snapshot.toml from a specific path. It does not
// validate; call Validate after loading.
func LoadFrom(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read snapshot.toml: %w", err)
	}

	var cfg Config
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse snapshot.toml: %w", err)
	}
	return &cfg, nil
}

// Validate checks the config before any side effect (R1.3, R1.4, AD4). It fails
// hard with ErrInvalidDriver on an unknown engine.driver, ship.type, or
// schedule.backend; warns-but-continues on non-fatal issues (empty subvolumes);
// and verifies each active driver's binary is on PATH via detect (R6.1).
//
// Order matters: every enum is checked first, so an unknown driver string is
// reported before — and independently of — any missing-binary detection, and
// both happen before the command writes any file (G3).
func (c *Config) Validate() error {
	switch c.Engine.Driver {
	case "btrbk":
		// supported
	default:
		return fmt.Errorf("%w: engine.driver %q", ErrInvalidDriver, c.Engine.Driver)
	}

	for i, sh := range c.Ship {
		switch sh.Type {
		case "ssh":
			// supported
		default:
			return fmt.Errorf("%w: ship[%d].type %q", ErrInvalidDriver, i, sh.Type)
		}
	}

	switch c.Schedule.Backend {
	case "", "systemd":
		// "" = no scheduling; "systemd" = supported
	default:
		return fmt.Errorf("%w: schedule.backend %q", ErrInvalidDriver, c.Schedule.Backend)
	}

	// Non-fatal: an empty subvolume list means nothing is snapshotted, but it is
	// not an error (the autoupdate validate-and-warn pattern, R1.4).
	if len(c.Engine.Subvolumes) == 0 {
		warnLogf("snapshot: engine.subvolumes is empty; nothing will be snapshotted")
	}

	// Dependency detection for the active drivers (still before any side effect).
	if err := detectDriver("engine", c.Engine.Driver); err != nil {
		return err
	}
	for _, sh := range c.Ship {
		if err := detectDriver("ship", sh.Type); err != nil {
			return err
		}
	}
	if c.Schedule.Backend != "" {
		if err := detectDriver("schedule", c.Schedule.Backend); err != nil {
			return err
		}
	}

	return nil
}
