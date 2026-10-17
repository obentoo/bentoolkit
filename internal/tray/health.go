package tray

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/desktop/notify"
	"github.com/obentoo/bentoolkit/internal/sysmon"
	"github.com/obentoo/bentoolkit/internal/tray/messages"
)

// level is how bad a system health condition is. The zero value is levelOK.
type level int

// Condition levels, in increasing severity.
const (
	levelOK level = iota
	levelWarning
	levelCritical
)

// clearAbove is the clear level of a "too little left" threshold: the reading
// must climb 20% of the threshold past it before an active level is left.
func clearAbove(threshold float64) float64 {
	return threshold + threshold/5
}

// clearBelow is the clear level of a "too much pressure" threshold: the
// reading must fall 20% of the threshold under it before an active level is
// left.
func clearBelow(threshold float64) float64 {
	return threshold - threshold/5
}

// percentOf returns part as a percentage of whole; a zero whole reads as
// 100% so a missing total never looks like exhaustion.
func percentOf(part, whole uint64) float64 {
	if whole == 0 {
		return 100
	}
	return float64(part) * 100 / float64(whole)
}

// classifyDisk returns the level of a filesystem reading given the current
// level. Warning needs free space below WarnPercent AND below WarnFree;
// critical needs it below CritPercent OR below CritFree. An active warning
// clears only once free space reaches the clear level of either warning
// threshold; a critical falls back only once it reaches the clear level of
// both critical thresholds. A filesystem of Size 0 (a pseudo filesystem) is
// always ok.
func classifyDisk(d sysmon.Disk, t config.DiskMonitor, cur level) level {
	if d.Size == 0 {
		return levelOK
	}
	pct := percentOf(d.Free, d.Size)
	free := float64(d.Free)

	critical := pct < t.CritPercent || free < float64(t.CritFree)
	if cur == levelCritical {
		leaves := pct >= clearAbove(t.CritPercent) && free >= clearAbove(float64(t.CritFree))
		if !leaves {
			return levelCritical
		}
		cur = levelWarning
	} else if critical {
		return levelCritical
	}

	if cur == levelWarning {
		if pct >= clearAbove(t.WarnPercent) || free >= clearAbove(float64(t.WarnFree)) {
			return levelOK
		}
		return levelWarning
	}
	if pct < t.WarnPercent && free < float64(t.WarnFree) {
		return levelWarning
	}
	return levelOK
}

// classifyMemory returns the level of a memory reading given the current
// level. Warning needs available memory below WarnPercent of total. Critical
// needs it below CritPercent while swap is absent or below CritSwapPercent
// free, or a PSI "full avg60" of at least PSIFullCrit; p is nil when the
// pressure file is missing, and a reading without a full line carries no
// full figure. An active critical falls back only once available memory or
// free swap reaches its clear level AND full pressure is under its clear
// level; an active warning clears once available memory reaches its clear
// level.
func classifyMemory(m sysmon.Memory, p *sysmon.Pressure, t config.MemoryMonitor, cur level) level {
	avail := percentOf(m.Available, m.Total)
	full, hasFull := 0.0, false
	if p != nil && p.HasFull {
		full, hasFull = p.FullAvg60, true
	}

	if cur == levelCritical {
		roomy := avail >= clearAbove(t.CritPercent) ||
			(m.SwapTotal > 0 && percentOf(m.SwapFree, m.SwapTotal) >= clearAbove(t.CritSwapPercent))
		calm := !hasFull || full < clearBelow(t.PSIFullCrit)
		if !roomy || !calm {
			return levelCritical
		}
		cur = levelWarning
	} else if memoryCritical(m, avail, full, hasFull, t) {
		return levelCritical
	}

	if cur == levelWarning {
		if avail >= clearAbove(t.WarnPercent) {
			return levelOK
		}
		return levelWarning
	}
	if avail < t.WarnPercent {
		return levelWarning
	}
	return levelOK
}

// memoryCritical reports whether a memory reading raises a critical: little
// memory available with swap absent or nearly full, or sustained full
// pressure.
func memoryCritical(m sysmon.Memory, avail, full float64, hasFull bool, t config.MemoryMonitor) bool {
	if hasFull && full >= t.PSIFullCrit {
		return true
	}
	swapLow := m.SwapTotal == 0 || percentOf(m.SwapFree, m.SwapTotal) < t.CritSwapPercent
	return avail < t.CritPercent && swapLow
}

// classifyCPU returns the level of a CPU pressure reading given the current
// level. Warning needs a PSI "some avg60" of at least WarnPSI; an active
// warning holds until the reading falls below ClearPSI. CPU pressure is never
// critical, and it is always ok while emerge runs, since a build is expected
// to saturate the processors.
func classifyCPU(p sysmon.Pressure, emerge bool, t config.CPUMonitor, cur level) level {
	if emerge {
		return levelOK
	}
	threshold := t.WarnPSI
	if cur != levelOK {
		threshold = t.ClearPSI
	}
	if p.SomeAvg60 >= threshold {
		return levelWarning
	}
	return levelOK
}

// condKey names one condition: a monitor and, for disks, the watched path.
type condKey struct{ Monitor, Target string }

// condition tracks one monitor and target across samples. The zero value is
// an ok condition with no pending candidate.
type condition struct {
	level       level
	candidate   level     // the pending level: the mildest reading of the spell
	since       time.Time // when the pending spell began
	lastSample  time.Time
	activeSince time.Time // when the spell that left ok began; zero while ok
	notifiedAt  time.Time // when observe last returned Notify; zero if never
	notifID     uint32    // the last notification sent for it, replaced by the next
	text        string    // the tooltip line while active, empty while ok
}

// transition is what one observed sample changed. Key is left zero by
// observe; the caller fills it from the map key.
type transition struct {
	Key      condKey
	From, To level
	Notify   bool // send a notification at Urgency
	Withdraw bool // close the previous notification
	Urgency  byte
}

// observe feeds one classified reading taken at now. window is the sustain
// window, reminder the critical reminder interval, maxGap the longest pause
// between samples still counted as continuous, paused the menu pause.
//
// Readings on one side of the level must last window without a break before
// the condition moves; see track for what restarts the wait. Raising to
// warning notifies at normal urgency unless paused; reaching critical
// notifies at critical urgency even when paused; an active critical notifies
// again once reminder has passed since the last notification, an active
// warning never. Critical to warning is silent; clearing to ok withdraws.
//
// observe stamps notifiedAt with now whenever it returns Notify, so the
// caller only sends: a rejected critical is retried once per reminder, not
// on every sample. A warning raised while paused never notifies, keeps
// notifiedAt zero and is not sent later. ok is false when the sample changed
// nothing the caller must act on.
func (c *condition) observe(now time.Time, got level, window, reminder, maxGap time.Duration, paused bool) (transition, bool) {
	gap := !c.lastSample.IsZero() && wallClock(now).Sub(wallClock(c.lastSample)) > maxGap
	c.lastSample = now

	if got == c.level {
		c.candidate, c.since = got, now
		if c.level == levelCritical && now.Sub(c.notifiedAt) >= reminder {
			return c.notify(now, transition{From: c.level, To: c.level}, urgencyCritical), true
		}
		return transition{}, false
	}

	c.track(now, got, gap)
	if now.Sub(c.since) < window {
		return transition{}, false
	}
	return c.move(now, got, paused), true
}

// wallClock strips the monotonic reading of t. A suspend stops the
// monotonic clock but not the wall clock, so only a wall-clock difference
// shows the gap a suspend leaves between two samples.
func wallClock(t time.Time) time.Time {
	return t.Round(0)
}

// track adds a reading off the current level to the pending spell. A reading
// on the same side of the level as the spell keeps its start and narrows the
// candidate to the level nearer the current one, so warning and critical
// readings from ok sustain a warning, and ok and warning readings from
// critical sustain a fall to warning. A reading on the other side, or one
// after a gap over maxGap, starts a new spell. A reading at the level itself
// ends the spell before track is reached.
func (c *condition) track(now time.Time, got level, gap bool) {
	rising := got > c.level
	if gap || c.candidate == c.level || (c.candidate > c.level) != rising {
		c.candidate, c.since = got, now
		return
	}
	if rising {
		c.candidate = min(c.candidate, got)
	} else {
		c.candidate = max(c.candidate, got)
	}
}

// move ends the pending spell by moving the condition to its candidate. The
// reading taken now starts the next spell at once: a reading past the new
// level must then hold a window of its own, counted from the move, so a
// warning that turns critical mid-window raises warning first and critical
// one window later.
func (c *condition) move(now time.Time, got level, paused bool) transition {
	tr := transition{From: c.level, To: c.candidate}
	switch {
	case c.candidate == levelOK:
		c.activeSince = time.Time{}
	case c.level == levelOK:
		c.activeSince = c.since
	}
	c.level = c.candidate
	c.candidate, c.since = got, now

	switch {
	case tr.To == levelOK:
		tr.Withdraw = true
	case tr.To == levelCritical:
		tr = c.notify(now, tr, urgencyCritical)
	case tr.From == levelOK && !paused:
		tr = c.notify(now, tr, urgencyNormal)
	}
	return tr
}

// lasted returns how long the condition has been active at now: since the
// first reading of the spell that raised it, across warning and critical
// moves. It is zero while the condition is ok.
func (c *condition) lasted(now time.Time) time.Duration {
	if c.level == levelOK || c.activeSince.IsZero() {
		return 0
	}
	return now.Sub(c.activeSince)
}

// notify marks tr as a notification at urgency and stamps notifiedAt.
func (c *condition) notify(now time.Time, tr transition, urgency byte) transition {
	c.notifiedAt = now
	tr.Notify, tr.Urgency = true, urgency
	return tr
}

// Notification keys of the system monitors. A disk key is "disk:" and the
// watched path, a read-only key "readonly:" and the mount point, a SMART key
// "smart:" and the drive's udisks2 object path; every failed unit shares the
// one units key.
const (
	keyDiskPrefix     = "disk:"
	keyMemory         = "memory"
	keyCPU            = "cpu"
	keyNetwork        = "network"
	keyEmerge         = "emerge"
	keyUnits          = "units"
	keyReadonlyPrefix = "readonly:"
	keySmartPrefix    = "smart:"
)

// maxField is the most bytes of one host-supplied field (an atom, a unit
// name, a mount point, a drive model) a notification or tooltip line shows.
const maxField = 128

// clip makes s safe for one line of a notification and cuts it to at most
// maxField bytes. Host names arrive raw (a mount point may hold Latin-1 bytes
// or, unescaped, a newline): invalid UTF-8 becomes U+FFFD, which the bus can
// encode, and every control character becomes a space. The cut falls on a
// rune boundary so the text stays valid UTF-8.
func clip(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s) // Map turns every invalid byte into U+FFFD
	if len(s) <= maxField {
		return s
	}
	cut := maxField
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// sysMessage is the system notification of the condition keyed key, replacing
// its last notification.
func sysMessage(key, summary, body string, replacesID uint32, urgency byte) notify.Message {
	return notify.Message{
		Summary:    summary,
		Body:       body,
		Urgency:    urgency,
		System:     true,
		Key:        key,
		ReplacesID: replacesID,
	}
}

// diskSysMessage returns the notification and the tooltip line of a low-disk
// condition: the path, the free space and its share of the filesystem.
func diskSysMessage(d sysmon.Disk, c *condition, urgency byte) (notify.Message, string) {
	free, pct := humanBytes(d.Free), percentOf(d.Free, d.Size)
	m := sysMessage(keyDiskPrefix+d.Path,
		messages.Format(messages.SysDiskSummary, d.Path),
		messages.Format(messages.SysDiskBody, free, pct),
		c.notifID, urgency)
	return m, messages.Format(messages.SysAlertDisk, d.Path, free, pct)
}

// memorySysMessage returns the notification and the tooltip line of a
// low-memory condition: available and total memory, and the free swap.
func memorySysMessage(mem sysmon.Memory, c *condition, urgency byte) (notify.Message, string) {
	avail, total := humanBytes(mem.Available), humanBytes(mem.Total)
	m := sysMessage(keyMemory,
		messages.Text(messages.SysMemorySummary),
		messages.Format(messages.SysMemoryBody, avail, total, humanBytes(mem.SwapFree)),
		c.notifID, urgency)
	return m, messages.Format(messages.SysAlertMemory, avail, total)
}

// cpuSysMessage returns the notification and the tooltip line of a CPU
// pressure condition: the "some avg60" pressure and the whole minutes it has
// lasted.
func cpuSysMessage(p sysmon.Pressure, lasted time.Duration, c *condition, urgency byte) (notify.Message, string) {
	minutes := int(lasted / time.Minute)
	m := sysMessage(keyCPU,
		messages.Text(messages.SysCPUSummary),
		messages.Format(messages.SysCPUBody, p.SomeAvg60, minutes),
		c.notifID, urgency)
	return m, messages.Format(messages.SysAlertCPU, p.SomeAvg60, minutes)
}

// networkSysMessage returns the notification and the tooltip line of a
// network event. A down carries the tooltip line; a back, which the caller
// sends at low urgency replacing the down, has no body and no line, since
// nothing is wrong any more.
func networkSysMessage(ev netEvent, replacesID uint32, urgency byte) (notify.Message, string) {
	if ev == netBack {
		return sysMessage(keyNetwork, messages.Text(messages.SysNetBackSummary), "", replacesID, urgency), ""
	}
	m := sysMessage(keyNetwork,
		messages.Text(messages.SysNetDownSummary),
		messages.Text(messages.SysNetDownBody),
		replacesID, urgency)
	return m, messages.Text(messages.SysAlertNetwork)
}

// emergeSysMessage returns the notification and the tooltip line of a failed
// build: the first failed atom, how many others failed when more is above
// zero, and the first atom's build log when logPath is not empty. logPath is
// shown whole, never cut: the caller states only a path it checked, and a
// cut one would name a file that does not exist.
func emergeSysMessage(atom string, more int, logPath string, replacesID uint32, urgency byte) (notify.Message, string) {
	failed := clip(atom)
	if more > 0 {
		failed = messages.Format(messages.SysEmergeMore, failed, more)
	}
	body := messages.Format(messages.SysEmergeBody, failed)
	if logPath != "" {
		body = messages.Format(messages.SysEmergeBodyLog, failed, logPath)
	}
	m := sysMessage(keyEmerge, messages.Text(messages.SysEmergeSummary), body, replacesID, urgency)
	return m, messages.Format(messages.SysAlertEmerge, failed)
}

// unitsSysMessage returns the shared units notification for one failed unit
// of manager ("system" or "user"), with failed units currently failed, and
// that unit's tooltip line.
func unitsSysMessage(manager, unit string, failed int, replacesID uint32, urgency byte) (notify.Message, string) {
	unit, manager = clip(unit), clip(manager)
	m := sysMessage(keyUnits,
		messages.Text(messages.SysUnitsSummary),
		messages.Format(messages.SysUnitsBody, unit, manager, failed),
		replacesID, urgency)
	return m, messages.Format(messages.SysAlertUnit, unit, manager)
}

// readonlySysMessage returns the notification and the tooltip line of a
// filesystem gone read-only: its mount point, device and filesystem type.
func readonlySysMessage(point, device, fsType string, replacesID uint32, urgency byte) (notify.Message, string) {
	p, dev, fs := clip(point), clip(device), clip(fsType)
	m := sysMessage(keyReadonlyPrefix+point,
		messages.Format(messages.SysReadonlySummary, p),
		messages.Format(messages.SysReadonlyBody, p, dev, fs),
		replacesID, urgency)
	return m, messages.Format(messages.SysAlertReadonly, p, dev, fs)
}

// smartSysMessage returns the notification and the tooltip line of the
// failing drive at the udisks2 object path: its model, its serial and the
// reason, the NVMe warning names when there are any, else the SMART verdict.
func smartSysMessage(path, model, serial string, warnings []string, replacesID uint32, urgency byte) (notify.Message, string) {
	reason := messages.Text(messages.SysSmartFailing)
	if len(warnings) > 0 {
		reason = clip(strings.Join(warnings, ", "))
	}
	mdl := clip(model)
	m := sysMessage(keySmartPrefix+path,
		messages.Text(messages.SysSmartSummary),
		messages.Format(messages.SysSmartBody, mdl, clip(serial), reason),
		replacesID, urgency)
	return m, messages.Format(messages.SysAlertSmart, mdl, reason)
}

// humanBytes renders n in the largest of GiB, MiB and KiB it fills at least
// once: GiB with one decimal, the smaller units rounded down to whole numbers
// so 1023.9 MiB never reads as 1024 MiB. Under 1 KiB it is plain bytes.
func humanBytes(n uint64) string {
	const kib, mib, gib = 1 << 10, 1 << 20, 1 << 30
	switch {
	case n >= gib:
		return fmt.Sprintf("%.1f GiB", float64(n)/gib)
	case n >= mib:
		return fmt.Sprintf("%d MiB", n/mib)
	case n >= kib:
		return fmt.Sprintf("%d KiB", n/kib)
	}
	return fmt.Sprintf("%d B", n)
}
