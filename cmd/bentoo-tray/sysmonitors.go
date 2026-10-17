package main

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"sync"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/desktop/smartmon"
	"github.com/obentoo/bentoolkit/internal/desktop/unitmon"
	"github.com/obentoo/bentoolkit/internal/gentoo/news"
	"github.com/obentoo/bentoolkit/internal/gentoo/pending"
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

// probeTimeout bounds each startup question to a bus (is the systemd manager
// running, is udisks2 available), so a bus that never answers cannot hold
// the tray's start.
const probeTimeout = 5 * time.Second

// emergeFollower is the emerge log follower; *sysmon.EmergeFollower
// implements it. Start seeks to the end of the log once, at startup.
type emergeFollower interface {
	Start() error
	Read() ([]sysmon.EmergeEvent, error)
}

// unitsProbe is one systemd manager's monitor; *unitmon.Monitor implements
// it. Present is asked once, at startup.
type unitsProbe interface {
	Present(ctx context.Context) (bool, error)
	Failed(ctx context.Context) ([]unitmon.Unit, error)
	Changed() <-chan struct{}
}

// smartProbe is the udisks2 monitor; *smartmon.Monitor implements it.
// Available is asked once, at startup.
type smartProbe interface {
	Available(ctx context.Context) (bool, error)
	Drives(ctx context.Context) ([]smartmon.Drive, error)
	Changed() <-chan struct{}
}

// hostHealth is the host access the health monitors need: the resource
// reader, the two portageq questions, the existence check, the emerge
// follower factory, the mount reader, the bus monitors, the command lookup
// and the repos.conf and sync-time readers. start builds the
// real one; tests pass fakes, so nothing here touches the host on its own.
//
// A nil Emerge, Mounts, SystemUnits, UserUnits, Smart, LookPath,
// SyncedRepos or SyncTime is a capability the host lacks: nothing is opened,
// asked or logged for it. Calling one of the
// func fields opens that monitor, so it is called only for an enabled one.
// SystemUnits, UserUnits and Smart return an error when their bus is
// unreachable.
type hostHealth struct {
	Reader  tray.SystemReader
	TmpDir  func() string
	DistDir func() string
	Stat    func(string) (fs.FileInfo, error)

	Emerge      func(path string) emergeFollower
	Mounts      tray.MountReader
	SystemUnits func() (unitsProbe, error)
	UserUnits   func() (unitsProbe, error)
	Smart       func() (smartProbe, error)

	LookPath    func(string) (string, error)
	SyncedRepos func(string) ([]news.Repo, error)
	SyncTime    func(string) (time.Time, bool, error)
}

// healthWiring is what resolveHealth hands the App: each field is the
// tray.Deps field of the same name. An absent or disabled monitor is a nil
// interface, a nil command or no repository.
type healthWiring struct {
	Sys       tray.SystemReader
	Monitors  *config.Monitors
	DiskPaths []string

	PortageTmp        string
	Emerge            tray.EmergeReader
	EmergeStartFailed bool
	Mounts            tray.MountReader

	SystemUnits            tray.UnitMonitor
	SystemUnitsProbeFailed bool
	UserUnits              tray.UnitMonitor
	UserUnitsProbeFailed   bool
	Smart                  tray.SmartMonitor
	SmartProbeFailed       bool

	Updates       *pending.Command
	GLSA          *pending.Command
	Repos         []news.Repo
	RepoRevisions string
	EmergeRunning func() (bool, error)
}

// healthDeps resolves the host reader, the monitor configuration and the
// watched disk paths for the App: the first three results of resolveHealth.
func healthDeps(ctx context.Context, log *slog.Logger, cfg config.TrayConfig, h hostHealth) (tray.SystemReader, *config.Monitors, []string) {
	w := resolveHealth(ctx, log, cfg, h)
	return w.Sys, w.Monitors, w.DiskPaths
}

// resolveHealth resolves every health monitor the App runs. portageq answers
// PORTAGE_TMPDIR when the disk monitor watches its default paths or the
// emerge monitor is on, at most once per call, and DISTDIR only for the disk
// defaults, concurrently with PORTAGE_TMPDIR; diskPaths' warnings are logged
// here. The systemd and udisks2 monitors are opened and probed concurrently
// with each other and with portageq; their lines are logged once all have
// answered, system manager first, then user manager, then SMART. A disabled
// monitor opens nothing and asks nothing. The update-check commands and the
// synced repositories are resolved by resolveUpdates. Every line logged for
// the emerge, units, SMART, updates, GLSA and sync monitors carries
// monitor=<name>.
func resolveHealth(ctx context.Context, log *slog.Logger, cfg config.TrayConfig, h hostHealth) healthWiring {
	m, _ := cfg.GetMonitors() // its warnings were logged by loadTrayConfig
	sys, m := systemMonitors(log, m, h.Reader)
	w := healthWiring{Sys: sys, Monitors: &m}
	tmpdir := askOnce(h.TmpDir)

	var (
		probes       sync.WaitGroup
		system, user probeResult[tray.UnitMonitor]
		smart        probeResult[tray.SmartMonitor]
	)
	if m.Units.Enabled && m.Units.System {
		probes.Go(func() { system = probeUnits(ctx, "system", "system", h.SystemUnits) })
	}
	if m.Units.Enabled && m.Units.User {
		probes.Go(func() { user = probeUnits(ctx, "user", "session", h.UserUnits) })
	}
	if m.Smart.Enabled {
		probes.Go(func() { smart = probeSmart(ctx, h.Smart) })
	}

	if sys != nil && m.Disk.Enabled {
		var warnings []string
		w.DiskPaths, warnings = diskPaths(m.Disk.Paths, tmpdir, h.DistDir, h.Stat)
		for _, warning := range warnings {
			log.WarnContext(ctx, "disk monitor", "monitor", "disk", "warning", warning)
		}
	}
	if m.Emerge.Enabled && h.Emerge != nil {
		w.Emerge, w.EmergeStartFailed = startEmerge(ctx, log, h.Emerge, m.Emerge.Log)
		if w.Emerge != nil {
			w.PortageTmp = tmpdir()
			if w.PortageTmp == "" {
				log.InfoContext(ctx, "portageq did not answer PORTAGE_TMPDIR; failed-build notifications name no build log",
					"monitor", "emerge")
			}
		}
	}
	if m.Readonly.Enabled && h.Mounts != nil {
		w.Mounts = h.Mounts
	}
	resolveUpdates(ctx, log, m, h, &w)

	probes.Wait()
	system.note.log(ctx, log)
	user.note.log(ctx, log)
	smart.note.log(ctx, log)
	w.SystemUnits, w.SystemUnitsProbeFailed = system.monitor, system.failed
	w.UserUnits, w.UserUnitsProbeFailed = user.monitor, user.failed
	w.Smart, w.SmartProbeFailed = smart.monitor, smart.failed
	return w
}

// probeResult is what one bus monitor's start-up probe resolved: the monitor
// handed to the App (nil when absent), the one line the probe owes (nil for
// none), and failed, set when the probe question failed and its WARN is
// that note, so the App must not log the same failure again.
type probeResult[M any] struct {
	monitor M
	failed  bool
	note    probeNote
}

// probeNote writes one start-up line, held back so that probes running
// concurrently log in a fixed order.
type probeNote func(ctx context.Context, log *slog.Logger)

// log writes n to log; a nil note logs nothing.
func (n probeNote) log(ctx context.Context, log *slog.Logger) {
	if n != nil {
		n(ctx, log)
	}
}

// askOnce wraps a portageq question so that it runs at most once, however
// many monitors need its answer. A nil question answers "".
func askOnce(ask func() string) func() string {
	if ask == nil {
		return func() string { return "" }
	}
	return sync.OnceValue(ask)
}

// startEmerge builds the follower of path and starts it at the end of the
// log. A log that cannot be opened is one WARN naming the path and the
// cause; the follower is kept, as each later read retries it, and failed
// reports that the App must not log the same failure again.
func startEmerge(ctx context.Context, log *slog.Logger, open func(string) emergeFollower, path string) (r tray.EmergeReader, failed bool) {
	f := open(path)
	if f == nil {
		return nil, false
	}
	if err := f.Start(); err != nil {
		log.WarnContext(ctx, "cannot open the emerge log; retrying at every sample",
			"monitor", "emerge", "path", path, "error", err)
		return f, true
	}
	return f, false
}

// probeUnits opens the monitor of one systemd manager and asks whether the
// manager runs. An unreachable bus or an absent manager is one INFO naming
// the manager and no monitor; a failed question is one WARN carrying the
// cause, and the monitor is kept, since a later listing may succeed. A
// question cut short by the end of ctx logs nothing and keeps no monitor:
// the tray is stopping.
func probeUnits(ctx context.Context, manager, bus string, open func() (unitsProbe, error)) probeResult[tray.UnitMonitor] {
	var r probeResult[tray.UnitMonitor]
	if open == nil {
		return r
	}
	u, err := open()
	if err != nil {
		r.note = func(ctx context.Context, log *slog.Logger) {
			log.InfoContext(ctx, "the bus is unreachable; this systemd manager's units are not watched",
				"monitor", "units", "manager", manager, "bus", bus, "error", err)
		}
		return r
	}
	if u == nil {
		return r
	}
	pctx, cancel := context.WithTimeout(ctx, probeTimeout)
	present, err := u.Present(pctx)
	cancel()
	switch {
	case err != nil && ctx.Err() != nil:
		return r
	case err != nil:
		r.failed = true
		r.note = func(ctx context.Context, log *slog.Logger) {
			log.WarnContext(ctx, "cannot tell whether the systemd manager is running; its units stay watched",
				"monitor", "units", "manager", manager, "bus", bus, "error", err)
		}
	case !present:
		r.note = func(ctx context.Context, log *slog.Logger) {
			log.InfoContext(ctx, "no systemd manager runs on the bus; its units are not watched",
				"monitor", "units", "manager", manager, "bus", bus)
		}
		return r
	}
	r.monitor = u
	return r
}

// probeSmart opens the udisks2 monitor and asks whether udisks2 runs or can
// be started. An unreachable system bus or an absent udisks2 is one INFO
// saying disk health monitoring is unavailable and no monitor; a failed
// question is one WARN carrying the cause, and the monitor is kept, since a
// later listing may succeed. A question cut short by the end of ctx logs
// nothing and keeps no monitor: the tray is stopping.
func probeSmart(ctx context.Context, open func() (smartProbe, error)) probeResult[tray.SmartMonitor] {
	var r probeResult[tray.SmartMonitor]
	if open == nil {
		return r
	}
	s, err := open()
	if err != nil {
		r.note = func(ctx context.Context, log *slog.Logger) {
			log.InfoContext(ctx, "disk health monitoring is unavailable: the system bus is unreachable",
				"monitor", "smart", "bus", "system", "error", err)
		}
		return r
	}
	if s == nil {
		return r
	}
	pctx, cancel := context.WithTimeout(ctx, probeTimeout)
	available, err := s.Available(pctx)
	cancel()
	switch {
	case err != nil && ctx.Err() != nil:
		return r
	case err != nil:
		r.failed = true
		r.note = func(ctx context.Context, log *slog.Logger) {
			log.WarnContext(ctx, "cannot tell whether udisks2 is available; disk health monitoring stays on",
				"monitor", "smart", "bus", "system", "error", err)
		}
	case !available:
		r.note = func(ctx context.Context, log *slog.Logger) {
			log.InfoContext(ctx, "disk health monitoring is unavailable: udisks2 is neither running nor installed on the system bus",
				"monitor", "smart", "bus", "system")
		}
		return r
	}
	r.monitor = s
	return r
}
