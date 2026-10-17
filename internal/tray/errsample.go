package tray

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/obentoo/bentoolkit/internal/desktop/notify"
	"github.com/obentoo/bentoolkit/internal/desktop/smartmon"
	"github.com/obentoo/bentoolkit/internal/sysmon"
)

// sampleEmerge reads the emerge log events appended since the last sample
// and feeds them, in order, to the emerge condition. A failed read keeps the
// condition as it is, with one WARN per failure streak naming the configured
// log; the next sample reads again.
func (a *App) sampleEmerge(ctx context.Context, now time.Time) {
	key := condKey{Monitor: monitorEmerge}
	events, err := a.d.Emerge.Read()
	if a.readFailed(key, a.mon.Emerge.Log, err) || ctx.Err() != nil {
		return
	}
	a.readSucceeded(key)
	c := a.cond(key)
	for _, ev := range events {
		if fail, changed := a.emerge.feed(ev); changed {
			a.emergeChanged(ctx, now, key, c, fail)
		}
	}
	a.emergeText(c)
}

// emergeChanged observes one change of the emerge condition with no sustain
// window: a non-nil fail raises it naming those atoms, a nil fail clears it.
// A failure while the condition is active already does not move its level,
// so the newer run's notification replaces the standing one here.
func (a *App) emergeChanged(ctx context.Context, now time.Time, key condKey, c *condition, fail []string) {
	got, logPath := levelOK, ""
	if len(fail) > 0 {
		got, logPath = levelWarning, a.buildLog(fail[0])
	}
	build := func(u byte) (notify.Message, string) {
		if len(fail) == 0 {
			return notify.Message{}, ""
		}
		return emergeSysMessage(fail[0], len(fail)-1, logPath, c.notifID, u)
	}
	wasActive := c.level != levelOK
	tr, changed := c.observe(now, got, 0, a.mon.Reminder, a.maxGap(), a.paused(now))
	a.apply(ctx, key, c, tr, changed, build, eventReading)
	if wasActive && len(fail) > 0 && !tr.Notify {
		msg, _ := build(levelUrgency(c.level))
		a.resend(ctx, key, c, msg)
	}
}

// emergeText rebuilds the tooltip line of an active emerge condition from the
// atoms still failed, so a partial completion drops the completed atoms from
// it. The notification is left as it was sent.
func (a *App) emergeText(c *condition) {
	failed := a.emerge.failed
	if c.level == levelOK || len(failed) == 0 {
		return
	}
	_, c.text = emergeSysMessage(failed[0], len(failed)-1, "", c.notifID, levelUrgency(c.level))
}

// buildLog returns the build log of atom under PORTAGE_TMPDIR, or "" when
// PORTAGE_TMPDIR is unknown or not absolute, when the atom does not name one
// category/package-version directory under <PORTAGE_TMPDIR>/portage, or when
// the file does not exist. The joined path must equal the plain concatenation
// of its parts: a part that filepath.Join would clean away ("..", ".", an
// empty category) is refused, so the path never leaves that directory. The
// path is shown whole, so an atom over maxField bytes, or a path that is not
// printable UTF-8, is refused too.
func (a *App) buildLog(atom string) string {
	tmp := a.d.PortageTmp
	if tmp == "" || !filepath.IsAbs(tmp) || len(atom) > maxField {
		return ""
	}
	cat, pf, ok := strings.Cut(atom, "/")
	if !ok || strings.ContainsRune(pf, '/') {
		return ""
	}
	root := filepath.Join(tmp, "portage")
	path := filepath.Join(root, cat, pf, "temp", "build.log")
	sep := string(filepath.Separator)
	if path != root+sep+cat+sep+pf+sep+"temp"+sep+"build.log" || !strings.HasPrefix(path, root+sep) {
		return ""
	}
	if !utf8.ValidString(path) || strings.ContainsFunc(path, unicode.IsControl) {
		return ""
	}
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	return path
}

// resend sends m again for the active condition c, replacing its last
// notification, when an event changed what the condition reports without
// moving its level. A warning is not sent while the menu is paused. The
// reminder clock (notifiedAt) is left alone.
func (a *App) resend(ctx context.Context, key condKey, c *condition, m notify.Message) {
	if c.level < levelCritical && a.paused(a.d.Clock.Now()) {
		return
	}
	m.ReplacesID = c.notifID
	a.sendSystem(ctx, key, c, m)
}

// mountLevel is the level of one mount point and the mount entry it comes
// from.
type mountLevel struct {
	level level
	mount sysmon.Mount
}

// sampleMounts reads the mount table and observes every mount point with no
// sustain window. A read-only condition whose mount point left the table is
// cleared. A failed read keeps every condition, with one WARN per failure
// streak naming the mount table.
func (a *App) sampleMounts(ctx context.Context, now time.Time) {
	key := condKey{Monitor: monitorReadonly}
	mounts, err := a.d.Mounts.Mounts()
	if a.readFailed(key, pathMountinfo, err) || ctx.Err() != nil {
		return
	}
	a.readSucceeded(key)
	points := a.classifyMounts(mounts)
	for _, point := range slices.Sorted(maps.Keys(points)) {
		a.observeMount(ctx, now, point, points[point])
	}
	for _, k := range a.healthKeys(monitorReadonly) {
		if _, present := points[k.Target]; !present {
			a.observeMount(ctx, now, k.Target, mountLevel{})
		}
	}
}

// classifyMounts returns the level of each mount point in mounts, judged by
// its topmost entry: the table lists a mount after the one it covers, and only
// the top one is what a write at that point reaches. A read-only bind stacked
// on a read-write mount of the same device (a common hardening step) is
// therefore a first sight, not a remount. The mounts that left the table are
// dropped from seenRW, so a later mount there is a first sight.
func (a *App) classifyMounts(mounts []sysmon.Mount) map[string]mountLevel {
	inTable := make(map[string]bool, len(mounts))
	top := make(map[string]sysmon.Mount, len(mounts))
	for _, m := range mounts {
		inTable[mountKey(m)] = true
		top[m.Point] = m
	}
	points := make(map[string]mountLevel, len(top))
	for point, m := range top {
		points[point] = mountLevel{level: classifyMount(m, a.seenRW), mount: m}
	}
	maps.DeleteFunc(a.seenRW, func(k string, _ bool) bool { return !inTable[k] })
	return points
}

// observeMount observes the read-only condition of point at ml. A point with
// no condition that reads ok creates none, and a condition back at ok is
// dropped once its notification is withdrawn, so the map holds only the
// mount points that are or just were read-only.
func (a *App) observeMount(ctx context.Context, now time.Time, point string, ml mountLevel) {
	key := condKey{Monitor: monitorReadonly, Target: point}
	c, ok := a.health[key]
	if !ok {
		if ml.level == levelOK {
			return
		}
		c = a.cond(key)
	}
	tr, changed := c.observe(now, ml.level, 0, a.mon.Reminder, a.maxGap(), a.paused(now))
	a.apply(ctx, key, c, tr, changed, func(u byte) (notify.Message, string) {
		return readonlySysMessage(point, ml.mount.Source, ml.mount.FSType, c.notifID, u)
	}, eventReading)
	if c.level == levelOK {
		delete(a.health, key)
	}
}

// unitScope is one watched systemd manager: its name in unit targets and log
// lines, and the bus it lives on.
type unitScope struct{ manager, bus string }

// The two systemd managers whose failed units are announced.
var (
	scopeSystem = unitScope{manager: "system", bus: "system"}
	scopeUser   = unitScope{manager: "user", bus: "session"}
	unitScopes  = []unitScope{scopeSystem, scopeUser}
)

// unitsNote is the one notification every failed unit shares: notifID is the
// id it was last sent under, 0 while none stands.
type unitsNote struct {
	notifID uint32
}

// unitRaise is one unit condition a reconciliation raised, waiting for its
// send.
type unitRaise struct {
	name    string
	urgency byte
}

// unitKey is the condition key of unit name under s's manager, its target
// "<manager>:<unit>".
func unitKey(s unitScope, name string) condKey {
	return condKey{Monitor: monitorUnits, Target: s.manager + ":" + name}
}

// unitMonitor is the monitor of s's manager, nil when it is absent.
func (a *App) unitMonitor(s unitScope) UnitMonitor {
	if s == scopeSystem {
		return a.d.SystemUnits
	}
	return a.d.UserUnits
}

// unitsWatched reports whether the failed units of s's manager are watched:
// the units monitor and that manager are enabled and its monitor is present.
func (a *App) unitsWatched(s unitScope) bool {
	enabled := a.mon.Units.System
	if s == scopeUser {
		enabled = a.mon.Units.User
	}
	return a.mon.Units.Enabled && enabled && a.unitMonitor(s) != nil
}

// unitsChanged is the Changed channel of s's manager, nil when it is not
// watched, so the loop never selects it.
func (a *App) unitsChanged(s unitScope) <-chan struct{} {
	if !a.unitsWatched(s) {
		return nil
	}
	return a.unitMonitor(s).Changed()
}

// onUnitsChanged handles a receive from the Changed channel *ch of s's
// manager and reports whether the user quit. A value reconciles the
// manager's units. A closed channel means its bus connection was lost: the
// channel is set to nil so the loop never selects it again, one WARN names
// the bus and the manager, and the manager's conditions are kept as they
// are, since nothing can tell them changed any more.
func (a *App) onUnitsChanged(ctx context.Context, ch *<-chan struct{}, ok bool, s unitScope) (quit bool) {
	if !received(ch, ok) {
		if ctx.Err() == nil {
			a.log.Warn("the bus connection of a systemd manager was lost; keeping its unit conditions",
				"monitor", monitorUnits, "target", "", "bus", s.bus, "manager", s.manager)
		}
		return false
	}
	return a.guard(ctx, func(ctx context.Context) { a.reconcileUnits(ctx, s) })
}

// reconcileUnits lists the failed units of s's manager and observes them
// with no sustain window: every listed unit at warning, every condition of
// that manager whose unit is no longer listed at ok. The call is bounded by
// callTimeout. A failed call keeps the manager's conditions, with one WARN
// per failure streak naming the manager; a failed call is not an empty list.
func (a *App) reconcileUnits(ctx context.Context, s unitScope) {
	failed, ok := a.listFailed(ctx, s)
	if !ok {
		return
	}
	now := a.d.Clock.Now()
	var raised []unitRaise
	for _, name := range slices.Sorted(maps.Keys(failed)) {
		if tr := a.observeUnit(now, s, name, levelWarning); tr.Notify {
			raised = append(raised, unitRaise{name: name, urgency: tr.Urgency})
		}
	}
	cleared := false
	for _, key := range a.healthKeys(monitorUnits) {
		manager, name, _ := strings.Cut(key.Target, ":")
		if manager == s.manager && !failed[name] {
			cleared = a.observeUnit(now, s, name, levelOK).Withdraw || cleared
		}
	}
	a.deliverUnits(ctx, s, raised, cleared)
	a.render()
}

// listFailed returns the names of the units s's manager lists as failed. ok
// is false when the call failed or ctx ended during it; a failure while ctx
// is live takes the manager's WARN-once streak.
func (a *App) listFailed(ctx context.Context, s unitScope) (failed map[string]bool, ok bool) {
	key := condKey{Monitor: monitorUnits, Target: s.manager}
	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	units, err := a.unitMonitor(s).Failed(callCtx)
	cancel()
	if ctx.Err() != nil {
		return nil, false
	}
	if err != nil {
		if !a.failing[key] {
			a.failing[key] = true
			a.log.Warn("listing the failed units of a systemd manager failed; keeping its unit conditions",
				"monitor", monitorUnits, "target", "", "bus", s.bus, "manager", s.manager, "error", err)
		}
		return nil, false
	}
	a.readSucceeded(key)
	failed = make(map[string]bool, len(units))
	for _, u := range units {
		failed[u.Name] = true
	}
	return failed, true
}

// observeUnit observes the condition of unit name under s's manager at got
// and returns what it changed, a zero transition when nothing. A unit with
// no condition that reads ok creates none, and a condition back at ok is
// dropped, so the map holds only failed units. Delivery is deliverUnits'.
func (a *App) observeUnit(now time.Time, s unitScope, name string, got level) transition {
	key := unitKey(s, name)
	c, ok := a.health[key]
	if !ok {
		if got == levelOK {
			return transition{}
		}
		c = a.cond(key)
	}
	tr, changed := c.observe(now, got, 0, a.mon.Reminder, a.maxGap(), a.paused(now))
	a.noteSample(key, c, tr, changed, func(u byte) (notify.Message, string) {
		return unitsSysMessage(s.manager, name, 0, 0, u)
	}, eventReading)
	if c.level == levelOK {
		delete(a.health, key)
	}
	if !changed {
		return transition{}
	}
	return tr
}

// deliverUnits sends the shared units notification once for each raised
// unit, in order, each replacing the last one and stating how many units are
// failed now, across both managers. A failed send keeps the previous id. A
// cleared unit withdraws the shared notification only when no unit condition
// is left active; while one is, a clear sends nothing and leaves it as it is.
func (a *App) deliverUnits(ctx context.Context, s unitScope, raised []unitRaise, cleared bool) {
	active := a.activeUnits()
	for _, r := range raised {
		m, _ := unitsSysMessage(s.manager, r.name, active, a.units.notifID, r.urgency)
		if id, err := a.sendSys(ctx, unitKey(s, r.name), m); err == nil {
			a.units.notifID = id
		}
	}
	if cleared && active == 0 {
		id := a.units.notifID
		a.units.notifID = 0
		a.closeSys(ctx, condKey{Monitor: monitorUnits}, id)
	}
}

// activeUnits is the number of active unit conditions, across both managers.
func (a *App) activeUnits() int {
	n := 0
	for key, c := range a.health {
		if key.Monitor == monitorUnits && c.level != levelOK {
			n++
		}
	}
	return n
}

// smartWatched reports whether drive health is watched: the SMART monitor is
// enabled and present.
func (a *App) smartWatched() bool {
	return a.mon.Smart.Enabled && a.d.Smart != nil
}

// smartChanged is the SMART monitor's Changed channel, nil when drive health
// is not watched, so the loop never selects it.
func (a *App) smartChanged() <-chan struct{} {
	if !a.smartWatched() {
		return nil
	}
	return a.d.Smart.Changed()
}

// onSmartChanged handles a receive from the SMART Changed channel *ch and
// reports whether the user quit. A value reconciles the drives. A closed
// channel means the system bus connection was lost: the channel is set to
// nil so the loop never selects it again, one WARN names the system bus, and
// the SMART conditions are kept as they are, since nothing can tell them
// changed any more. Their critical reminders still fire on the sample timer.
func (a *App) onSmartChanged(ctx context.Context, ch *<-chan struct{}, ok bool) (quit bool) {
	if !received(ch, ok) {
		if ctx.Err() == nil {
			a.log.Warn("the system bus connection was lost; keeping the SMART conditions",
				"monitor", monitorSmart, "target", "", "bus", "system")
		}
		return false
	}
	return a.guard(ctx, a.reconcileSmart)
}

// reconcileSmart lists udisks2's drives and observes them with no sustain
// window: each listed drive at classifyDrive's level, and each condition
// whose drive is no longer listed at ok, so a removed drive clears. The call
// is bounded by callTimeout. A failed call keeps every SMART condition, and a
// partial list (drives returned with an error for the malformed ones) keeps
// the conditions of the drives it left out; either takes one WARN per
// failure streak. A failed call is not an empty list.
func (a *App) reconcileSmart(ctx context.Context) {
	drives, complete := a.listDrives(ctx)
	if ctx.Err() != nil {
		return
	}
	now := a.d.Clock.Now()
	listed := make(map[string]bool, len(drives))
	for _, d := range drives {
		listed[string(d.Path)] = true
		a.observeDrive(ctx, now, string(d.Path), d, classifyDrive(d))
	}
	if complete {
		for _, key := range a.healthKeys(monitorSmart) {
			if !listed[key.Target] {
				a.observeDrive(ctx, now, key.Target, smartmon.Drive{}, levelOK)
			}
		}
	}
	a.render()
}

// listDrives returns the drives udisks2 lists and whether the list is
// complete: false when the call failed, returned an error beside its drives,
// or ctx ended during it. A failure while ctx is live takes the SMART
// monitor's WARN-once streak; only a complete list ends it.
func (a *App) listDrives(ctx context.Context) (drives []smartmon.Drive, complete bool) {
	key := condKey{Monitor: monitorSmart}
	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
	drives, err := a.d.Smart.Drives(callCtx)
	cancel()
	if ctx.Err() != nil {
		return nil, false
	}
	if err != nil {
		if !a.failing[key] {
			a.failing[key] = true
			a.log.Warn("listing the drives of udisks2 failed; keeping the SMART conditions of the drives it did not list",
				"monitor", monitorSmart, "target", "", "bus", "system", "drives", len(drives), "error", err)
		}
		return drives, false
	}
	a.readSucceeded(key)
	return drives, true
}

// remindSmart observes every active SMART condition again at its current
// level from the drive's last reading, so observe sends the critical
// reminder once the reminder interval has passed. It makes no bus call.
func (a *App) remindSmart(ctx context.Context, now time.Time) {
	for _, key := range a.healthKeys(monitorSmart) {
		if c := a.health[key]; c.level != levelOK {
			a.observeDrive(ctx, now, key.Target, a.drives[key.Target], c.level)
		}
	}
}

// observeDrive observes the SMART condition of the drive at the udisks2
// object path at got, d being its reading. A drive with no condition that
// reads ok creates none, and a condition back at ok is dropped with its
// reading, so the maps hold only the failing drives. A changed NVMe warning
// set on a condition already active does not move its level, so the
// notification is sent again, replacing the standing one.
func (a *App) observeDrive(ctx context.Context, now time.Time, path string, d smartmon.Drive, got level) {
	key := condKey{Monitor: monitorSmart, Target: path}
	c, ok := a.health[key]
	if !ok {
		if got == levelOK {
			return
		}
		c = a.cond(key)
	}
	prev, wasActive := a.drives[path], c.level != levelOK
	build := func(u byte) (notify.Message, string) {
		return smartSysMessage(path, d.Model, d.Serial, d.Warnings, c.notifID, u)
	}
	tr, changed := c.observe(now, got, 0, a.mon.Reminder, a.maxGap(), a.paused(now))
	a.apply(ctx, key, c, tr, changed, build, eventReading)
	if c.level == levelOK {
		delete(a.health, key)
		delete(a.drives, path)
		return
	}
	a.drives[path] = d
	if wasActive && !tr.Notify && !slices.Equal(prev.Warnings, d.Warnings) {
		msg, _ := build(levelUrgency(c.level))
		a.resend(ctx, key, c, msg)
	}
}
