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

// Unit file names and defaults (system scope).
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
// isolates mount propagation, so the read-only mounts a shipper makes stay
// private to the run.
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

// systemdExecArg renders one ExecStart argument so systemd reads it back as the
// same single literal word. A control character is refused:
// systemd.service(5) allows none on a command line, and a newline would start a
// new directive. `%` and `$` are doubled so neither a specifier
// (systemd.unit(5)) nor a variable expansion (systemd.service(5) "Command
// lines") applies. An argument that is empty, is a lone `;`, or contains
// whitespace, `"`, `'` or `\` is double-quoted, with `\` and `"` escaped
// (systemd.syntax(7) "Quoting"). Anything else is written as is, so ordinary
// paths render unchanged.
func systemdExecArg(s string) (string, error) {
	if hasControl(s) {
		return "", fmt.Errorf("ExecStart argument %q contains a control character", s)
	}
	s = strings.ReplaceAll(s, "%", "%%")
	s = strings.ReplaceAll(s, "$", "$$")
	if s != "" && s != ";" && !strings.ContainsAny(s, " \t\n\v\f\r\"'\\") {
		return s, nil
	}
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`, nil
}

// renderServiceUnit renders the .service unit text, quoting execPath and
// configPath for ExecStart (see systemdExecArg).
func renderServiceUnit(execPath, configPath string) (string, error) {
	execArg, err := systemdExecArg(execPath)
	if err != nil {
		return "", fmt.Errorf("exec path: %w", err)
	}
	configArg, err := systemdExecArg(configPath)
	if err != nil {
		return "", fmt.Errorf("config path: %w", err)
	}
	var b strings.Builder
	if err := serviceTemplate.Execute(&b, map[string]string{
		"ExecPath":   execArg,
		"ConfigPath": configArg,
	}); err != nil {
		return "", fmt.Errorf("execute template: %w", err)
	}
	return b.String(), nil
}

// renderTimerUnit renders the .timer unit text from the schedule config.
func renderTimerUnit(cfg ScheduleConfig) (string, error) {
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
	if err := timerTemplate.Execute(&b, data); err != nil {
		return "", fmt.Errorf("execute template: %w", err)
	}
	return b.String(), nil
}

// Apply renders and installs the units, then reloads systemd and enables the
// timer. Writes are atomic and overwrite in place, so re-applying
// reconciles without duplicates. Both units are rendered before either
// is written, so a render failure writes no unit and runs no systemctl.
func (s *systemdScheduler) Apply(ctx context.Context, cfg ScheduleConfig) error {
	servicePath := filepath.Join(s.unitDir, serviceUnitName)
	timerPath := filepath.Join(s.unitDir, timerUnitName)

	service, err := renderServiceUnit(s.execPath, s.configPath)
	if err != nil {
		return fmt.Errorf("render %s: %w", serviceUnitName, err)
	}
	timer, err := renderTimerUnit(cfg)
	if err != nil {
		return fmt.Errorf("render %s: %w", timerUnitName, err)
	}

	if err := atomicWrite(servicePath, []byte(service), 0o644); err != nil {
		return fmt.Errorf("write service unit: %w", err)
	}
	if err := atomicWrite(timerPath, []byte(timer), 0o644); err != nil {
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
// (the inverse of Apply). systemctl errors are wrapped; missing files are not an error.
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
