package tray

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/obentoo/bentoolkit/internal/desktop/notify"
	"github.com/obentoo/bentoolkit/internal/sysmon"
	"github.com/obentoo/bentoolkit/internal/tray/messages"
)

// Monitor names, as the condition keys and the log lines spell them.
const (
	monitorDisk     = "disk"
	monitorMemory   = "memory"
	monitorCPU      = "cpu"
	monitorNetwork  = "network"
	monitorEmerge   = "emerge"
	monitorUnits    = "units"
	monitorReadonly = "readonly"
	monitorSmart    = "smart"
	monitorUpdates  = "updates"
	monitorGLSA     = "glsa"
	monitorSync     = "sync"
)

// Logical paths of the host files a sample reads, named by the WARN of a
// failed reading: the reader's root is not the App's to know.
const (
	pathMeminfo   = "/proc/meminfo"
	pathPressure  = "/proc/pressure/"
	pathProc      = "/proc"
	pathMountinfo = "/proc/self/mountinfo"
)

// reading is what an INFO transition line reports: the measured value and
// the threshold of the level the condition reached. An event monitor has
// neither, and its line carries no value or threshold.
type reading struct {
	value, threshold float64
	event            bool
}

// eventReading is the reading of a condition raised by an event.
var eventReading = reading{event: true}

// builder returns the notification and the tooltip line of an active
// condition at urgency.
type builder func(urgency byte) (notify.Message, string)

// String is the level as the log lines spell it.
func (l level) String() string {
	switch l {
	case levelNotice:
		return "notice"
	case levelWarning:
		return "warning"
	case levelCritical:
		return "critical"
	default:
		return "ok"
	}
}

// sampling reports whether the App samples the host: a system reader is set
// and at least one resource monitor is enabled, the emerge or read-only
// monitor is enabled and has its reader, drive health is watched, whose
// critical reminder the sample timer drives, or the updates, GLSA or sync
// monitor is enabled and has its command or repositories.
func (a *App) sampling() bool {
	return a.sysSampling() || a.emergeSampling() || a.mountSampling() || a.smartWatched() || a.updSampling()
}

// sysSampling reports whether the resource monitors sample.
func (a *App) sysSampling() bool {
	return a.d.Sys != nil && (a.mon.Disk.Enabled || a.mon.Memory.Enabled || a.mon.CPU.Enabled)
}

// emergeSampling reports whether the emerge log is read on the sample timer.
func (a *App) emergeSampling() bool { return a.mon.Emerge.Enabled && a.d.Emerge != nil }

// mountSampling reports whether the mount table is read on the sample timer.
func (a *App) mountSampling() bool { return a.mon.Readonly.Enabled && a.d.Mounts != nil }

// sample reads every enabled monitor once and applies what changed, then
// waits for the next sample. A reading that fails skips only its own
// monitor. ctx is checked after every reading, so a Quit during a slow read
// returns before the next one and drops what that read returned. The active
// SMART conditions are observed again from their last reading, with no bus
// call, so a failing drive is reminded about while udisks2 stays silent.
func (a *App) sample(ctx context.Context) {
	a.sampleAt = a.d.Clock.After(a.mon.SampleInterval)
	now := a.d.Clock.Now()
	if a.d.Sys != nil {
		a.sampleResources(ctx, now)
	}
	if a.emergeSampling() && ctx.Err() == nil {
		a.sampleEmerge(ctx, now)
	}
	if a.mountSampling() && ctx.Err() == nil {
		a.sampleMounts(ctx, now)
	}
	if a.smartWatched() && ctx.Err() == nil {
		a.remindSmart(ctx, now)
	}
	if a.updSampling() && ctx.Err() == nil {
		a.sampleUpdates(ctx, now)
	}
	a.render()
}

// sampleResources reads the enabled disk, memory and CPU monitors.
func (a *App) sampleResources(ctx context.Context, now time.Time) {
	if a.mon.Disk.Enabled {
		a.sampleDisks(ctx, now)
	}
	if a.mon.Memory.Enabled && ctx.Err() == nil {
		a.sampleMemory(ctx, now)
	}
	if a.mon.CPU.Enabled && ctx.Err() == nil {
		a.sampleCPU(ctx, now)
	}
}

// sampleDisks evaluates each filesystem once, under the first watched path
// that resolved to it. A path whose reading fails keeps owning the
// filesystem it last resolved to, so a later path on it raises nothing.
func (a *App) sampleDisks(ctx context.Context, now time.Time) {
	owner := map[uint64]string{} // the watched path each filesystem is evaluated under
	for _, path := range a.d.DiskPaths {
		if ctx.Err() != nil {
			return
		}
		key := condKey{Monitor: monitorDisk, Target: path}
		d, err := a.d.Sys.Disk(path)
		if a.readFailed(key, path, err) {
			a.keepDisk(ctx, owner, key, path)
			continue
		}
		if ctx.Err() != nil {
			continue
		}
		a.readSucceeded(key)
		a.diskDev[path] = d.Dev
		if first, dup := owner[d.Dev]; dup {
			if first != path {
				a.dropDisk(ctx, key, d)
			}
			continue
		}
		owner[d.Dev] = path
		d.Path = path

		c := a.cond(key)
		got := classifyDisk(d, a.mon.Disk, c.level)
		tr, changed := c.observe(now, got, a.mon.Disk.Sustain, a.mon.Reminder, a.maxGap(), a.paused(now))
		threshold := a.mon.Disk.WarnPercent
		if c.level == levelCritical {
			threshold = a.mon.Disk.CritPercent
		}
		a.apply(ctx, key, c, tr, changed, func(u byte) (notify.Message, string) { return diskSysMessage(d, c, u) },
			reading{value: percentOf(d.Free, d.Size), threshold: threshold})
	}
}

// keepDisk makes path, whose reading failed, the owner of the filesystem it
// last resolved to. When an earlier path of this sample owns that filesystem
// already, path gives its condition up instead. A path that never read has no
// filesystem and claims nothing.
func (a *App) keepDisk(ctx context.Context, owner map[uint64]string, key condKey, path string) {
	dev, known := a.diskDev[path]
	if !known {
		return
	}
	if _, taken := owner[dev]; taken {
		a.yieldDisk(ctx, key)
		return
	}
	owner[dev] = path
}

// dropDisk forgets the condition of a watched path whose filesystem is now
// evaluated under an earlier path, d being its reading.
func (a *App) dropDisk(ctx context.Context, key condKey, d sysmon.Disk) {
	a.yieldDisk(ctx, key, "value", percentOf(d.Free, d.Size))
}

// yieldDisk forgets the condition of a watched path whose filesystem is
// evaluated under an earlier path. An active condition is cleared: its
// notification is withdrawn and the move to ok is logged with the reading's
// attributes, none when the path could not be read.
func (a *App) yieldDisk(ctx context.Context, key condKey, reading ...any) {
	c, ok := a.health[key]
	if !ok {
		return
	}
	delete(a.health, key)
	if c.level == levelOK {
		return
	}
	attrs := append([]any{"monitor", key.Monitor, "target", key.Target,
		"state", levelOK.String(), "from", c.level.String()}, reading...)
	attrs = append(attrs, "threshold", a.mon.Disk.WarnPercent,
		"reason", "the path shares the filesystem of an earlier watched path")
	a.log.Info("system condition changed", attrs...)
	a.withdraw(ctx, key, c)
}

// sampleMemory evaluates memory from meminfo and, when the kernel exposes it,
// the memory pressure. A kernel without the pressure file is evaluated from
// meminfo alone, silently; any other failure of either file skips the
// monitor.
func (a *App) sampleMemory(ctx context.Context, now time.Time) {
	key := condKey{Monitor: monitorMemory}
	m, err := a.d.Sys.Memory()
	if a.readFailed(key, pathMeminfo, err) || ctx.Err() != nil {
		return
	}
	p, err := a.d.Sys.Pressure(monitorMemory)
	var psi *sysmon.Pressure
	switch {
	case err == nil:
		psi = &p
	case errors.Is(err, sysmon.ErrNoPressure):
	default:
		a.readFailed(key, pathPressure+monitorMemory, err)
		return
	}
	if ctx.Err() != nil {
		return
	}
	a.readSucceeded(key)

	c := a.cond(key)
	got := classifyMemory(m, psi, a.mon.Memory, c.level)
	tr, changed := c.observe(now, got, a.mon.Memory.Sustain, a.mon.Reminder, a.maxGap(), a.paused(now))
	threshold := a.mon.Memory.WarnPercent
	if c.level == levelCritical {
		threshold = a.mon.Memory.CritPercent
	}
	a.apply(ctx, key, c, tr, changed, func(u byte) (notify.Message, string) { return memorySysMessage(m, c, u) },
		reading{value: percentOf(m.Available, m.Total), threshold: threshold})
}

// sampleCPU evaluates the CPU pressure. The emerge scan runs only when the
// pressure alone would hold or raise a warning, since it reads every
// process's command line.
func (a *App) sampleCPU(ctx context.Context, now time.Time) {
	key := condKey{Monitor: monitorCPU}
	p, err := a.d.Sys.Pressure(monitorCPU)
	if a.readFailed(key, pathPressure+monitorCPU, err) || ctx.Err() != nil {
		return
	}
	a.readSucceeded(key)

	c := a.cond(key)
	emerge := false
	if classifyCPU(p, false, a.mon.CPU, c.level) != levelOK {
		emerge = a.emergeRunning()
		if ctx.Err() != nil {
			return
		}
	}
	got := classifyCPU(p, emerge, a.mon.CPU, c.level)
	tr, changed := c.observe(now, got, a.mon.CPU.Sustain, a.mon.Reminder, a.maxGap(), a.paused(now))
	threshold := a.mon.CPU.ClearPSI
	if c.level != levelOK {
		threshold = a.mon.CPU.WarnPSI
	}
	a.apply(ctx, key, c, tr, changed,
		func(u byte) (notify.Message, string) { return cpuSysMessage(p, c.lasted(now), c, u) },
		reading{value: p.SomeAvg60, threshold: threshold})
}

// emergeRunning scans for a running emerge for the CPU monitor. A failed scan
// counts as none running, with one WARN per failure streak.
func (a *App) emergeRunning() bool {
	return a.scanEmerge(a.d.Sys.EmergeRunning, &a.emergeFailing, condKey{Monitor: monitorCPU})
}

// scanEmerge runs one scan for a running emerge on behalf of key's monitor.
// A failed scan counts as none running; the first failure while *failing is
// false logs one WARN, and the streak lasts until a scan succeeds.
func (a *App) scanEmerge(scan func() (bool, error), failing *bool, key condKey) bool {
	running, err := scan()
	if err == nil {
		*failing = false
		return running
	}
	if !*failing {
		*failing = true
		a.log.Warn("looking for a running emerge failed; treating it as not running",
			"monitor", key.Monitor, "target", key.Target, "state", a.levelOf(key), "path", pathProc, "error", err)
	}
	return false
}

// readFailed reports whether err is a failed reading of key. The first
// failure of a streak logs one WARN; the streak lasts until readSucceeded.
func (a *App) readFailed(key condKey, path string, err error) bool {
	if err == nil {
		return false
	}
	if !a.failing[key] {
		a.failing[key] = true
		a.log.Warn("reading a system monitor failed; skipping it until it reads again",
			"monitor", key.Monitor, "target", key.Target, "path", path, "error", err)
	}
	return true
}

// readSucceeded ends the failure streak of key: every reading the monitor
// needed for this sample succeeded.
func (a *App) readSucceeded(key condKey) {
	delete(a.failing, key)
}

// cond returns the condition of key, creating an ok one on first use.
func (a *App) cond(key condKey) *condition {
	c, ok := a.health[key]
	if !ok {
		c = &condition{}
		a.health[key] = c
	}
	return c
}

// maxGap is the longest pause between two samples still counted as
// continuous; a longer one, a suspend, restarts every pending spell.
func (a *App) maxGap() time.Duration {
	return 2 * a.mon.SampleInterval
}

// apply acts on one observed sample of key: noteSample, then the transition
// sends or withdraws the condition's own notification.
func (a *App) apply(ctx context.Context, key condKey, c *condition, tr transition, changed bool, build builder, r reading) {
	msg := a.noteSample(key, c, tr, changed, build, r)
	if !changed {
		return
	}
	switch {
	case tr.Notify:
		a.sendSystem(ctx, key, c, msg)
	case tr.Withdraw:
		a.withdraw(ctx, key, c)
	}
}

// noteSample records one observed sample of key and returns the condition's
// notification while it is active. An active condition gets its tooltip line
// rebuilt on every sample, sent or not; an ok one has none. A level change
// logs one INFO line.
func (a *App) noteSample(key condKey, c *condition, tr transition, changed bool, build builder, r reading) notify.Message {
	var msg notify.Message
	c.text = ""
	if c.level != levelOK {
		msg, c.text = build(levelUrgency(c.level))
	}
	if changed && tr.From != tr.To {
		attrs := []any{"monitor", key.Monitor, "target", key.Target,
			"state", tr.To.String(), "from", tr.From.String()}
		if !r.event {
			attrs = append(attrs, "value", r.value, "threshold", r.threshold)
		}
		a.log.Info("system condition changed", attrs...)
	}
	return msg
}

// levelUrgency is the notification urgency of an active level.
func levelUrgency(l level) byte {
	switch l {
	case levelCritical:
		return urgencyCritical
	case levelNotice:
		return urgencyLow
	default:
		return urgencyNormal
	}
}

// notificationKey is the notification key of a condition: "disk:" and the
// watched path, "memory", "cpu", "network", "emerge", the one "units" key
// every failed unit shares, "readonly:" and the mount point, "smart:" and the
// drive's object path, the one "updates" key of pending updates and of an
// unresolvable @world, the one "glsa" key every advisory shares, or "sync:"
// and the repository name. A monitor with no case of its own keys by its
// name, so it can never close another monitor's notification.
func notificationKey(key condKey) string {
	switch key.Monitor {
	case monitorDisk:
		return keyDiskPrefix + key.Target
	case monitorMemory:
		return keyMemory
	case monitorCPU:
		return keyCPU
	case monitorNetwork:
		return keyNetwork
	case monitorEmerge:
		return keyEmerge
	case monitorUnits:
		return keyUnits
	case monitorReadonly:
		return keyReadonlyPrefix + key.Target
	case monitorSmart:
		return keySmartPrefix + key.Target
	case monitorUpdates:
		return keyUpdates
	case monitorGLSA:
		return keyGLSA
	case monitorSync:
		return keySyncPrefix + key.Target
	default:
		return key.Monitor
	}
}

// sendSystem sends a condition's notification, replacing its last one. A
// failed send keeps the previous id, so the next send never replaces an id
// the server did not hand out.
func (a *App) sendSystem(ctx context.Context, key condKey, c *condition, m notify.Message) {
	if id, err := a.sendSys(ctx, key, m); err == nil {
		c.notifID = id
	}
}

// sendSys sends one system notification for key and returns the id the
// server gave it. A failure is logged at WARN and returned.
func (a *App) sendSys(ctx context.Context, key condKey, m notify.Message) (uint32, error) {
	return a.sendSysAt(ctx, key, m, slog.LevelWarn)
}

// sendSysAt is sendSys logging a failure at failLevel.
func (a *App) sendSysAt(ctx context.Context, key condKey, m notify.Message, failLevel slog.Level) (uint32, error) {
	sendCtx, cancel := context.WithTimeout(ctx, callTimeout)
	id, err := a.d.Notify.Send(sendCtx, m)
	cancel()
	if err != nil {
		a.log.Log(ctx, failLevel, "sending a system notification failed", "monitor", key.Monitor,
			"target", key.Target, "state", a.stateOf(key), "urgency", m.Urgency, "error", err)
		return 0, err
	}
	a.log.Debug("system notification sent", "monitor", key.Monitor, "target", key.Target,
		"urgency", m.Urgency, "id", id, "replaces", m.ReplacesID)
	return id, nil
}

// stateOf is the level a failed send of key's notification reports: the
// network's from the spell it is in, which no condition holds, and any
// other monitor's from key's condition.
func (a *App) stateOf(key condKey) string {
	if key.Monitor == monitorNetwork && a.network != nil {
		if a.network.online {
			return levelOK.String()
		}
		return levelWarning.String()
	}
	return a.levelOf(key)
}

// withdraw closes a cleared condition's notification.
func (a *App) withdraw(ctx context.Context, key condKey, c *condition) {
	id := c.notifID
	c.notifID = 0
	a.closeSys(ctx, key, id)
}

// closeSys closes the system notification id of key; an id of 0 closes
// nothing. The server may have expired it already, so a failed close is only
// a DEBUG line, never retried.
func (a *App) closeSys(ctx context.Context, key condKey, id uint32) {
	if id == 0 {
		return
	}
	closeCtx, cancel := context.WithTimeout(ctx, callTimeout)
	err := a.d.Notify.Close(closeCtx, id, notificationKey(key))
	cancel()
	if err != nil {
		a.log.Debug("withdrawing a system notification failed", "monitor", key.Monitor, "target", key.Target,
			"id", id, "error", err)
	}
}

// alerts returns the tooltip lines of the active system conditions, whether
// any of them is critical, and the informational lines: failing drives,
// read-only filesystems, disks in watched-path order, memory, CPU, network,
// the failed build, failed units by manager and name, applicable advisories
// by id, stale repositories by name, then pending updates. The network line
// shows while a sent "network down" stands. A condition at the notice level
// goes to info, never to lines, so it never asks for attention; a condition
// with no text shows nothing.
func (a *App) alerts() (lines []string, critical bool, info []string) {
	add := func(keys []condKey) {
		for _, key := range keys {
			c, ok := a.health[key]
			if !ok || c.level == levelOK || c.text == "" {
				continue
			}
			if c.level == levelNotice {
				info = append(info, c.text)
				continue
			}
			lines = append(lines, c.text)
			critical = critical || c.level == levelCritical
		}
	}
	add(a.healthKeys(monitorSmart))
	add(a.healthKeys(monitorReadonly))
	keys := make([]condKey, 0, len(a.d.DiskPaths)+2)
	for _, path := range a.d.DiskPaths {
		keys = append(keys, condKey{Monitor: monitorDisk, Target: path})
	}
	add(append(keys, condKey{Monitor: monitorMemory}, condKey{Monitor: monitorCPU}))
	if a.network != nil && a.network.notifiedDown {
		lines = append(lines, messages.Text(messages.SysAlertNetwork))
	}
	add([]condKey{{Monitor: monitorEmerge}})
	add(a.healthKeys(monitorUnits))
	add(a.healthKeys(monitorGLSA))
	add(a.healthKeys(monitorSync))
	add([]condKey{{Monitor: monitorUpdates}})
	return lines, critical, info
}

// healthKeys returns the keys of monitor's conditions sorted by target, a
// unit's target ("<manager>:<unit>") by manager, then unit name, so the map
// order never shows in the tooltip.
func (a *App) healthKeys(monitor string) []condKey {
	var keys []condKey
	for key := range a.health {
		if key.Monitor == monitor {
			keys = append(keys, key)
		}
	}
	slices.SortFunc(keys, func(x, y condKey) int {
		xm, xn, _ := strings.Cut(x.Target, ":")
		ym, yn, _ := strings.Cut(y.Target, ":")
		if monitor != monitorUnits {
			xm, xn, ym, yn = x.Target, "", y.Target, ""
		}
		return cmp.Or(strings.Compare(xm, ym), strings.Compare(xn, yn))
	})
	return keys
}
