package main

import (
	"context"
	"errors"
	"log/slog"
	"os"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/distfiles"
	"github.com/obentoo/bentoolkit/internal/sysmon"
	"github.com/obentoo/bentoolkit/internal/tray"
)

// cpuPressurePath is the file the CPU probe reads, named in its log lines.
const cpuPressurePath = "/proc/pressure/cpu"

// systemMonitors decides which host reader and which monitor configuration the
// App gets. When the CPU monitor is enabled it probes the CPU pressure once: a
// kernel without pressure stall information turns the CPU monitor off with one
// INFO, any other probe error is a WARN and leaves it on, since a later sample
// may succeed. The reader is nil when no disk, memory or CPU monitor is left
// enabled, so the App arms no sampling timer.
func systemMonitors(log *slog.Logger, m config.Monitors, sys tray.SystemReader) (tray.SystemReader, config.Monitors) {
	if m.CPU.Enabled {
		if _, err := sys.Pressure("cpu"); errors.Is(err, sysmon.ErrNoPressure) {
			m.CPU.Enabled = false
			log.Info("the kernel exposes no pressure stall information; the CPU monitor is off",
				"monitor", "cpu", "path", cpuPressurePath)
		} else if err != nil {
			log.Warn("cannot read the CPU pressure at startup; the CPU monitor stays on",
				"monitor", "cpu", "path", cpuPressurePath, "error", err)
		}
	}
	if !m.Disk.Enabled && !m.Memory.Enabled && !m.CPU.Enabled {
		return nil, m
	}
	return sys, m
}

// healthDeps resolves the host reader, the monitor configuration and the
// watched disk paths for the App. portageq runs only when the disk monitor is
// enabled with no configured paths; diskPaths' warnings are logged here.
func healthDeps(ctx context.Context, log *slog.Logger, cfg config.TrayConfig) (tray.SystemReader, *config.Monitors, []string) {
	m, _ := cfg.GetMonitors() // its warnings were logged by loadTrayConfig
	sys, m := systemMonitors(log, m, sysmon.Reader{Root: "/"})
	var paths []string
	if sys != nil && m.Disk.Enabled {
		var warnings []string
		paths, warnings = diskPaths(m.Disk.Paths,
			func() string { return distfiles.TempRoot(ctx) },
			func() string { return distfiles.HostDistdir(ctx) },
			os.Stat)
		for _, w := range warnings {
			log.Warn("disk monitor", "monitor", "disk", "warning", w)
		}
	}
	return sys, &m, paths
}
