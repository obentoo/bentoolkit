package snapshot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

// ErrSchedulerFailed wraps a non-zero exit from systemctl.
var ErrSchedulerFailed = errors.New("snapshot scheduler command failed")

// Unit file names and defaults (system scope, AD7).
const (
	serviceUnitName     = "bentoo-snapshot.service"
	timerUnitName       = "bentoo-snapshot.timer"
	defaultExecPath     = "bentoo"
	defaultSnapshotConf = "/etc/bentoo/snapshot.toml"
)

// systemdUnitDir is the install directory for the generated units. It is a var
// (not const) so tests can redirect writes away from the real /etc/systemd/system
// even when the scheduler is constructed indirectly (e.g. via Apply).
var systemdUnitDir = "/etc/systemd/system"

// serviceTemplate renders the oneshot service that runs the pipeline. PrivateMounts
// isolates mount propagation for safe read-only mounts in later stories (AD7).
var serviceTemplate = template.Must(template.New("service").Parse(`[Unit]
Description=bentoo snapshot run
Documentation=man:btrbk(1)

[Service]
Type=oneshot
PrivateMounts=yes
ExecStart={{.ExecPath}} snapshot run --config {{.ConfigPath}}

[Install]
WantedBy=multi-user.target
`))

// timerTemplate renders the timer that drives the service. Persistent is emitted
// only when explicitly set (tri-state); RandomizedDelaySec only when configured.
var timerTemplate = template.Must(template.New("timer").Parse(`[Unit]
Description=bentoo snapshot timer

[Timer]
OnCalendar={{.OnCalendar}}
{{- if .HasPersistent}}
Persistent={{.Persistent}}
{{- end}}
{{- if .RandomizedDelay}}
RandomizedDelaySec={{.RandomizedDelay}}
{{- end}}

[Install]
WantedBy=timers.target
`))

// systemdScheduler installs/removes the bentoo-snapshot service+timer via
// systemctl (through the Runner seam). unitDir, execPath, and configPath are
// fields so tests can redirect writes to a temp dir and pin the ExecStart line.
type systemdScheduler struct {
	run        Runner
	unitDir    string
	execPath   string // binary for ExecStart
	configPath string // snapshot.toml path baked into ExecStart
}

// newSystemdScheduler builds the scheduler. configPath is the snapshot.toml path
// the timer-driven `run` should load; a nil Runner falls back to execRunner.
func newSystemdScheduler(configPath string, run Runner) *systemdScheduler {
	if run == nil {
		run = defaultRunner()
	}
	if configPath == "" {
		configPath = defaultSnapshotConf
	}
	return &systemdScheduler{
		run:        run,
		unitDir:    systemdUnitDir,
		execPath:   defaultExecPath,
		configPath: configPath,
	}
}

// renderServiceUnit renders the .service unit text.
func renderServiceUnit(execPath, configPath string) string {
	var b strings.Builder
	_ = serviceTemplate.Execute(&b, map[string]string{
		"ExecPath":   execPath,
		"ConfigPath": configPath,
	})
	return b.String()
}

// renderTimerUnit renders the .timer unit text from the schedule config.
func renderTimerUnit(cfg ScheduleConfig) string {
	onCalendar := cfg.OnCalendar
	if onCalendar == "" {
		onCalendar = "daily"
	}
	data := struct {
		OnCalendar      string
		HasPersistent   bool
		Persistent      bool
		RandomizedDelay string
	}{
		OnCalendar:      onCalendar,
		HasPersistent:   cfg.Persistent != nil,
		Persistent:      cfg.Persistent != nil && *cfg.Persistent,
		RandomizedDelay: cfg.RandomizedDelay,
	}
	var b strings.Builder
	_ = timerTemplate.Execute(&b, data)
	return b.String()
}

// Apply renders and installs the units, then reloads systemd and enables the
// timer (R4.1, R4.2). Writes are atomic and overwrite in place, so re-applying
// reconciles without duplicates (R4.3).
func (s *systemdScheduler) Apply(ctx context.Context, cfg ScheduleConfig) error {
	servicePath := filepath.Join(s.unitDir, serviceUnitName)
	timerPath := filepath.Join(s.unitDir, timerUnitName)

	if err := atomicWrite(servicePath, []byte(renderServiceUnit(s.execPath, s.configPath)), 0o644); err != nil {
		return fmt.Errorf("write service unit: %w", err)
	}
	if err := atomicWrite(timerPath, []byte(renderTimerUnit(cfg)), 0o644); err != nil {
		return fmt.Errorf("write timer unit: %w", err)
	}

	if err := s.systemctl(ctx, "daemon-reload"); err != nil {
		return err
	}
	if err := s.systemctl(ctx, "enable", "--now", timerUnitName); err != nil {
		return err
	}
	return nil
}

// Remove disables the timer and deletes the unit files, then reloads systemd
// (R4 inverse). systemctl errors are wrapped; missing files are not an error.
func (s *systemdScheduler) Remove(ctx context.Context) error {
	if err := s.systemctl(ctx, "disable", "--now", timerUnitName); err != nil {
		return err
	}
	for _, name := range []string{timerUnitName, serviceUnitName} {
		if err := os.Remove(filepath.Join(s.unitDir, name)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove unit %s: %w", name, err)
		}
	}
	return s.systemctl(ctx, "daemon-reload")
}

// systemctl runs a systemctl subcommand via the Runner, wrapping failures.
func (s *systemdScheduler) systemctl(ctx context.Context, args ...string) error {
	if _, err := s.run.Run(ctx, "systemctl", args, nil); err != nil {
		return errors.Join(ErrSchedulerFailed, fmt.Errorf("systemctl %s: %w", strings.Join(args, " "), err))
	}
	return nil
}
