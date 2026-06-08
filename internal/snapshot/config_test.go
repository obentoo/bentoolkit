package snapshot

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

const sampleTOML = `
[engine]
driver = "btrbk"
subvolumes = ["/home", "/"]
snapshot_dir = "/.snapshots"

[engine.retention]
hourly = 24
daily = 7
weekly = 4
monthly = 6
preserve_min = "latest"

[[ship]]
name = "offsite"
type = "ssh"
target = "user@host:/backup/btrbk"

[[ship]]
name = "nas"
type = "ssh"
target = "nas@10.0.0.2:/tank/btrbk"

[schedule]
backend = "systemd"
on_calendar = "daily"
persistent = true
randomized_delay = "5m"
`

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return path
}

func TestLoadFrom_RepresentativeConfig(t *testing.T) {
	cfg, err := LoadFrom(writeTemp(t, "snapshot.toml", sampleTOML))
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}

	if cfg.Engine.Driver != "btrbk" {
		t.Errorf("engine.driver = %q, want btrbk", cfg.Engine.Driver)
	}
	if got := cfg.Engine.Subvolumes; len(got) != 2 || got[0] != "/home" || got[1] != "/" {
		t.Errorf("engine.subvolumes = %v, want [/home /]", got)
	}
	if cfg.Engine.SnapshotDir != "/.snapshots" {
		t.Errorf("engine.snapshot_dir = %q", cfg.Engine.SnapshotDir)
	}
	if cfg.Engine.Retention.Hourly != 24 || cfg.Engine.Retention.Monthly != 6 {
		t.Errorf("retention = %+v", cfg.Engine.Retention)
	}
	if cfg.Engine.Retention.PreserveMin != "latest" {
		t.Errorf("retention.preserve_min = %q", cfg.Engine.Retention.PreserveMin)
	}

	// [[ship]] array-of-tables parses to a slice.
	if len(cfg.Ship) != 2 {
		t.Fatalf("len(ship) = %d, want 2", len(cfg.Ship))
	}
	if cfg.Ship[0].Type != "ssh" || cfg.Ship[0].Target != "user@host:/backup/btrbk" {
		t.Errorf("ship[0] = %+v", cfg.Ship[0])
	}
	if cfg.Ship[1].Name != "nas" {
		t.Errorf("ship[1].name = %q", cfg.Ship[1].Name)
	}

	if cfg.Schedule.Backend != "systemd" || cfg.Schedule.OnCalendar != "daily" {
		t.Errorf("schedule = %+v", cfg.Schedule)
	}
	if cfg.Schedule.Persistent == nil || !*cfg.Schedule.Persistent {
		t.Errorf("schedule.persistent = %v, want true", cfg.Schedule.Persistent)
	}
}

func TestLoadFrom_OmittedOptionalsAreZero(t *testing.T) {
	cfg, err := LoadFrom(writeTemp(t, "minimal.toml", "[engine]\ndriver = \"btrbk\"\n"))
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if len(cfg.Ship) != 0 {
		t.Errorf("ship = %v, want empty", cfg.Ship)
	}
	if cfg.Schedule.Persistent != nil {
		t.Errorf("schedule.persistent = %v, want nil (unset tri-state)", cfg.Schedule.Persistent)
	}
	if cfg.Engine.Retention.Daily != 0 || cfg.Engine.SnapshotDir != "" {
		t.Errorf("expected zero-value optionals, got %+v", cfg.Engine)
	}
}

func TestConfigPaths_OrderAndDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))

	paths, err := ConfigPaths()
	if err != nil {
		t.Fatalf("ConfigPaths: %v", err)
	}
	if len(paths) != 3 {
		t.Fatalf("paths = %v, want 3 entries", paths)
	}
	if paths[0] != "/etc/bentoo/snapshot.toml" {
		t.Errorf("paths[0] = %q, want system scope first", paths[0])
	}
	if paths[1] != filepath.Join(home, "xdg", "bentoo", "snapshot.toml") {
		t.Errorf("paths[1] = %q, want XDG", paths[1])
	}
	if paths[2] != filepath.Join(home, ".config", "bentoo", "snapshot.toml") {
		t.Errorf("paths[2] = %q, want ~/.config", paths[2])
	}

	def, err := DefaultConfigPath()
	if err != nil {
		t.Fatalf("DefaultConfigPath: %v", err)
	}
	if def != paths[0] {
		t.Errorf("DefaultConfigPath = %q, want %q", def, paths[0])
	}
}

func TestConfigPaths_DedupeWhenXDGUnset(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")

	paths, err := ConfigPaths()
	if err != nil {
		t.Fatalf("ConfigPaths: %v", err)
	}
	// XDG defaults to ~/.config, so entries 2 and 3 collapse to one.
	if len(paths) != 2 {
		t.Fatalf("paths = %v, want 2 (deduped)", paths)
	}
}

func TestFindConfigPath_FirstExistingWins(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))

	// Only the ~/.config copy exists; /etc and XDG do not. FindConfigPath must
	// return the first that exists in priority order.
	target := filepath.Join(home, ".config", "bentoo", "snapshot.toml")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("[engine]\ndriver=\"btrbk\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := FindConfigPath()
	if err != nil {
		t.Fatalf("FindConfigPath: %v", err)
	}
	if got != target {
		t.Errorf("FindConfigPath = %q, want %q", got, target)
	}
}

func validConfig() *Config {
	return &Config{
		Engine:   EngineConfig{Driver: "btrbk", Subvolumes: []string{"/home"}},
		Ship:     []ShipConfig{{Type: "ssh", Target: "u@h:/p"}},
		Schedule: ScheduleConfig{Backend: "systemd"},
	}
}

func TestValidate_UnknownDriversReturnInvalidDriver(t *testing.T) {
	stubLookPath(t, "btrbk", "ssh", "systemctl")

	cases := map[string]func(*Config){
		"engine":   func(c *Config) { c.Engine.Driver = "zfs" },
		"ship":     func(c *Config) { c.Ship[0].Type = "rsync" },
		"schedule": func(c *Config) { c.Schedule.Backend = "cron" },
	}
	for name, mutate := range cases {
		cfg := validConfig()
		mutate(cfg)
		if err := cfg.Validate(); !errors.Is(err, ErrInvalidDriver) {
			t.Errorf("%s: Validate err = %v, want ErrInvalidDriver", name, err)
		}
	}
}

func TestValidate_UnknownDriverBeatsMissingBinary(t *testing.T) {
	// Even with no binaries present, an unknown enum must surface as
	// ErrInvalidDriver (enum checks run before detection — G3).
	stubLookPath(t)
	cfg := validConfig()
	cfg.Engine.Driver = "zfs"
	if err := cfg.Validate(); !errors.Is(err, ErrInvalidDriver) {
		t.Errorf("err = %v, want ErrInvalidDriver, not a detection error", err)
	}
}

func TestValidate_EmptySubvolumesWarnsButPasses(t *testing.T) {
	stubLookPath(t, "btrbk", "ssh", "systemctl")

	var warnings []string
	origWarn := warnLogf
	t.Cleanup(func() { warnLogf = origWarn })
	warnLogf = func(format string, args ...interface{}) { warnings = append(warnings, format) }

	cfg := validConfig()
	cfg.Engine.Subvolumes = nil
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate = %v, want nil (warn-but-continue)", err)
	}
	if len(warnings) == 0 {
		t.Errorf("expected a warning for empty subvolumes")
	}
}

func TestValidate_MissingBinaryNamesPackage(t *testing.T) {
	stubLookPath(t) // btrbk absent
	cfg := validConfig()
	if err := cfg.Validate(); !errors.Is(err, ErrDriverUnavailable) {
		t.Errorf("err = %v, want ErrDriverUnavailable", err)
	}
}

func TestValidate_ValidConfigPasses(t *testing.T) {
	stubLookPath(t, "btrbk", "ssh", "systemctl")
	if err := validConfig().Validate(); err != nil {
		t.Errorf("Validate = %v, want nil", err)
	}
}
