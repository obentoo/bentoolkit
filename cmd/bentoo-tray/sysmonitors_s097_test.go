package main

// Story 097, sub-task 6.2: bentoo-tray wires the host reader. systemMonitors
// probes the CPU pressure file once and decides which reader and which tray
// configuration the App gets; loadTrayConfig logs the tray.monitors
// warnings. The reader is a fake and the configuration a temporary
// XDG_CONFIG_HOME, so no test reads the host's /proc or config.
//
// Contract (design.md, cmd/bentoo-tray):
//
//	systemMonitors(log *slog.Logger, m config.Monitors, sys tray.SystemReader)
//		(tray.SystemReader, config.Monitors)
//
// It returns nil when no disk, memory or CPU monitor is left enabled, and
// Monitors with CPU.Enabled false when the kernel has no pressure stall
// information; main passes those Monitors to the App as Deps.Monitors.

import (
	"bytes"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/sysmon"
	"github.com/obentoo/bentoolkit/internal/tray"
)

// s097Probe is a SystemReader whose pressure files can be missing.
type s097Probe struct {
	mu     sync.Mutex
	psiErr map[string]error
	asked  []string
}

var _ tray.SystemReader = (*s097Probe)(nil)

func (p *s097Probe) Memory() (sysmon.Memory, error) { return sysmon.Memory{}, nil }
func (p *s097Probe) Pressure(resource string) (sysmon.Pressure, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.asked = append(p.asked, resource)
	return sysmon.Pressure{}, p.psiErr[resource]
}
func (p *s097Probe) Disk(path string) (sysmon.Disk, error) { return sysmon.Disk{Path: path}, nil }
func (p *s097Probe) EmergeRunning() (bool, error)          { return false, nil }

// s097Mon is the resolved default configuration with edit applied.
func s097Mon(edit func(*config.Monitors)) config.Monitors {
	m, _ := config.TrayConfig{}.GetMonitors()
	edit(&m)
	return m
}

func s097Keep(*config.Monitors) {}

func s097Logger() (*slog.Logger, *bytes.Buffer) {
	var b bytes.Buffer
	return slog.New(slog.NewTextHandler(&b, &slog.HandlerOptions{Level: slog.LevelDebug})), &b
}

func s097Lines(b *bytes.Buffer, parts ...string) []string {
	var out []string
	for _, l := range strings.Split(b.String(), "\n") {
		all := l != ""
		for _, p := range parts {
			all = all && strings.Contains(strings.ToLower(l), strings.ToLower(p))
		}
		if all {
			out = append(out, l)
		}
	}
	return out
}

func s097NoResources(m *config.Monitors) {
	m.Disk.Enabled, m.Memory.Enabled, m.CPU.Enabled = false, false, false
}

func s097CPUOnly(m *config.Monitors) { m.Disk.Enabled, m.Memory.Enabled = false, false }

// TestS097SystemMonitors_R1_2_EveryResourceMonitorDisabledLeavesNoReader: with
// disk, memory and CPU all off the App gets no reader, and the pressure file
// is not even probed; one monitor left on keeps the reader.
func TestS097SystemMonitors_R1_2_EveryResourceMonitorDisabledLeavesNoReader(t *testing.T) {
	t.Run("only the CPU monitor enabled", func(t *testing.T) {
		log, _ := s097Logger()
		probe := &s097Probe{psiErr: map[string]error{}}
		sys, _ := systemMonitors(log, s097Mon(s097CPUOnly), probe)
		if sys == nil {
			t.Fatal("reader = nil with the CPU monitor enabled")
		}
	})
	t.Run("every resource monitor disabled", func(t *testing.T) {
		log, buf := s097Logger()
		probe := &s097Probe{psiErr: map[string]error{}}
		sys, _ := systemMonitors(log, s097Mon(s097NoResources), probe)
		if sys != nil {
			t.Errorf("reader = %v, want nil with disk, memory and CPU disabled", sys)
		}
		if len(probe.asked) != 0 {
			t.Errorf("pressure probed %q with every resource monitor disabled", probe.asked)
		}
		if l := s097Lines(buf, "level=WARN"); len(l) != 0 {
			t.Errorf("WARN lines %q, want none", l)
		}
	})
	t.Run("only the CPU monitor enabled on a kernel without PSI", func(t *testing.T) {
		log, _ := s097Logger()
		probe := &s097Probe{psiErr: map[string]error{"cpu": fmt.Errorf("reading /proc/pressure/cpu: %w", sysmon.ErrNoPressure)}}
		sys, _ := systemMonitors(log, s097Mon(s097CPUOnly), probe)
		if sys != nil {
			t.Errorf("reader = %v, want nil: the CPU monitor, the last one on, was disabled", sys)
		}
	})
}

// TestS097SystemMonitors_R1_5_MissingCPUPressureDisablesCPU: a kernel without
// /proc/pressure/cpu disables the CPU monitor with one INFO; a missing memory
// pressure file, or another probe error, does not.
func TestS097SystemMonitors_R1_5_MissingCPUPressureDisablesCPU(t *testing.T) {
	noPSI := func(resource string) error {
		return fmt.Errorf("reading /proc/pressure/%s: %w", resource, sysmon.ErrNoPressure)
	}
	t.Run("memory pressure missing leaves CPU and memory on", func(t *testing.T) {
		log, buf := s097Logger()
		probe := &s097Probe{psiErr: map[string]error{"memory": noPSI("memory")}}
		sys, m := systemMonitors(log, s097Mon(s097Keep), probe)
		if sys == nil || !m.CPU.Enabled || !m.Memory.Enabled {
			t.Errorf("reader %v, CPU enabled %v, memory enabled %v; want the reader and both on", sys, m.CPU.Enabled, m.Memory.Enabled)
		}
		if l := s097Lines(buf, "level=INFO", "pressure"); len(l) != 0 {
			t.Errorf("INFO lines %q about pressure stall information, want none", l)
		}
	})
	t.Run("CPU pressure missing", func(t *testing.T) {
		log, buf := s097Logger()
		probe := &s097Probe{psiErr: map[string]error{"cpu": noPSI("cpu")}}
		in := s097Mon(func(m *config.Monitors) { m.Memory.Sustain = 7 * time.Minute; m.Disk.Paths = []string{"/srv"} })
		sys, m := systemMonitors(log, in, probe)
		want := in
		want.CPU.Enabled = false
		if !reflect.DeepEqual(m, want) {
			t.Errorf("returned Monitors = %+v, want the input with only CPU.Enabled turned off", m)
		}
		if m.CPU.Enabled {
			t.Error("the CPU monitor stays enabled on a kernel without /proc/pressure/cpu")
		}
		if !m.Memory.Enabled || !m.Disk.Enabled || sys == nil {
			t.Errorf("reader %v, memory enabled %v, disk enabled %v; want the reader and both on", sys, m.Memory.Enabled, m.Disk.Enabled)
		}
		if l := s097Lines(buf, "level=INFO", "pressure"); len(l) != 1 {
			t.Errorf("INFO lines about pressure stall information = %q, want 1", l)
		}
		if l := s097Lines(buf, "level=WARN"); len(l) != 0 {
			t.Errorf("WARN lines %q, want none: a kernel without PSI is not degraded", l)
		}
		if !slices.Equal(probe.asked, []string{"cpu"}) {
			t.Errorf("probed %q, want the CPU pressure once", probe.asked)
		}
	})
	t.Run("another probe error keeps CPU on", func(t *testing.T) {
		log, buf := s097Logger()
		probe := &s097Probe{psiErr: map[string]error{"cpu": fmt.Errorf("reading /proc/pressure/cpu: %w", fs.ErrPermission)}}
		_, m := systemMonitors(log, s097Mon(s097Keep), probe)
		if !m.CPU.Enabled {
			t.Error("a permission error disabled the CPU monitor; only a missing file does")
		}
		if l := s097Lines(buf, "level=WARN", "permission denied"); len(l) != 1 {
			t.Errorf("WARN lines carrying the probe error = %q, want 1", l)
		}
	})
	t.Run("CPU disabled in the configuration is not probed", func(t *testing.T) {
		log, buf := s097Logger()
		probe := &s097Probe{psiErr: map[string]error{"cpu": noPSI("cpu")}}
		systemMonitors(log, s097Mon(func(m *config.Monitors) { m.CPU.Enabled = false }), probe)
		if len(probe.asked) != 0 {
			t.Errorf("probed %q with the CPU monitor disabled", probe.asked)
		}
		if l := s097Lines(buf, "level=INFO", "pressure"); len(l) != 0 {
			t.Errorf("INFO lines %q, want none", l)
		}
	})
}

// TestS097LoadTrayConfig_R9_4_MonitorWarningsAreLogged: each bad tray.monitors
// value is one WARN naming the key and the value; valid keys are not reported
// and the existing tray warnings still are.
func TestS097LoadTrayConfig_R9_4_MonitorWarningsAreLogged(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	yamlText := "tray:\n  interval: 5m\n  monitors:\n    sample_interval: 5s\n    disk:\n      warn_percent: 150\n    memory:\n      sustain: 3m\n"
	path := filepath.Join(dir, "bentoo", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(yamlText), 0o600); err != nil {
		t.Fatal(err)
	}
	log, buf := s097Logger()
	cfg := loadTrayConfig(log)
	for key, value := range map[string]string{
		"tray.monitors.sample_interval":   "5s",
		"tray.monitors.disk.warn_percent": "150",
	} {
		if l := s097Lines(buf, "level=WARN", key, value); len(l) != 1 {
			t.Errorf("WARN lines naming %s and %s = %q, want 1\n%s", key, value, l, buf.String())
		}
	}
	if l := s097Lines(buf, "tray.monitors.memory.sustain"); len(l) != 0 {
		t.Errorf("the valid memory sustain was reported: %q", l)
	}
	if l := s097Lines(buf, "level=WARN", "tray.interval"); len(l) != 1 {
		t.Errorf("the tray.interval warning is no longer logged: %q", l)
	}
	if m, _ := cfg.GetMonitors(); m.Memory.Sustain.Minutes() != 3 {
		t.Errorf("memory sustain = %v, want the configured 3m", m.Memory.Sustain)
	}
}
