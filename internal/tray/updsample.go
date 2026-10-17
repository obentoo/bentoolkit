package tray

import (
	"context"
	"errors"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/obentoo/bentoolkit/internal/desktop/notify"
	"github.com/obentoo/bentoolkit/internal/gentoo/pending"
)

// checkStartDelay is how long after start-up the first update check becomes
// due, so the check never competes with the desktop session starting.
var checkStartDelay = 5 * time.Minute

// checkStopWait bounds how long a stop waits for the update check to leave
// its command runs once cancelled: pending.Run kills the process groups from
// a goroutine of its own and returns within its one-second WaitDelay, and a
// tray that exits before then can leave emerge running. The bound keeps a
// runner that ignores its context from holding a stop past two seconds.
var checkStopWait = 1500 * time.Millisecond

// stderrTailBytes is how much of a command's standard error a WARN quotes.
const stderrTailBytes = 256

// checkResult is what one update check sends back to the loop: the outcome of
// each command it ran, nil for a command whose monitor is off or absent.
type checkResult struct {
	world *worldOutcome
	glsa  *glsaOutcome
}

// worldOutcome is the @world command's run, parsed off the loop: packages are
// the distinct category/name entries it would merge, read only on exit 0.
type worldOutcome struct {
	path       string
	packages   []string
	exit       int
	stderrTail string
	err        error
	dur        time.Duration
}

// glsaOutcome is the advisory command's run, parsed off the loop: ok reports
// whether ParseGLSA recognised the output, read only on exit 0.
type glsaOutcome struct {
	path       string
	glsas      []pending.GLSA
	ok         bool
	exit       int
	stderrTail string
	err        error
	dur        time.Duration
}

// updatesOn reports whether the @world check runs: the updates monitor is
// enabled and the caller found its command.
func (a *App) updatesOn() bool { return a.mon.Updates.Enabled && a.d.Updates != nil }

// glsaOn reports whether the advisory check runs: the GLSA monitor is enabled
// and the caller found its command.
func (a *App) glsaOn() bool { return a.mon.GLSA.Enabled && a.d.GLSA != nil }

// checksOn reports whether update checks are scheduled at all. When neither
// command runs, no trigger is watched and no check is ever due.
func (a *App) checksOn() bool { return a.updatesOn() || a.glsaOn() }

// syncOn reports whether stale repositories are watched.
func (a *App) syncOn() bool { return a.mon.Sync.Enabled && len(a.d.Repos) > 0 }

// updSampling reports whether the sample timer has update work: a check to
// schedule or repositories to watch.
func (a *App) updSampling() bool { return a.checksOn() || a.syncOn() }

// sampleUpdates looks for a repository sync, observes how long ago each
// watched repository was synced, and starts the update check once it is
// due. The sync ages are observed whether or not any check runs. A running
// emerge is looked for only while a check waits for one, since the scan
// reads every process's command line.
func (a *App) sampleUpdates(ctx context.Context, now time.Time) {
	if a.checksOn() {
		a.sampleRevisions()
	}
	if a.syncOn() && ctx.Err() == nil {
		a.sampleSync(ctx, now)
	}
	if !a.checksOn() || ctx.Err() != nil {
		return
	}
	busy := a.sched.waiting(now) && a.emergeBusy()
	if a.sched.due(now, busy) {
		a.startCheck(ctx)
	}
}

// sampleSync observes the sync age of every watched repository with no
// sustain window: older than the maximum age raises its warning, synced
// within it clears. A repository with no sync time is skipped silently (the
// caller logged it at start-up). A failed read keeps the repository's
// condition, with one WARN per failure streak naming the repository and its
// location. ctx is checked after every read, so a Quit during a slow read
// drops what it returned and reads no further repository.
func (a *App) sampleSync(ctx context.Context, now time.Time) {
	for _, repo := range a.d.Repos {
		key := condKey{Monitor: monitorSync, Target: repo.Name}
		at, ok, err := a.d.SyncTime(repo.Location)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			if !a.failing[key] {
				a.failing[key] = true
				a.log.Warn("reading the last sync time of a repository failed; keeping its sync condition",
					"monitor", monitorSync, "target", repo.Name, "state", a.levelOf(key),
					"repository", repo.Name, "path", repo.Location, "error", err)
			}
			continue
		}
		delete(a.failing, key)
		if ok {
			a.observeSync(ctx, now, key, now.Sub(at))
		}
	}
}

// observeSync observes the sync condition of key's repository, last synced
// age ago. A repository with no condition that is fresh creates none, and a
// condition back at ok is dropped once its notification is withdrawn, so the
// map holds only the stale repositories.
func (a *App) observeSync(ctx context.Context, now time.Time, key condKey, age time.Duration) {
	got := levelOK
	if age > a.mon.Sync.MaxAge {
		got = levelWarning
	}
	c, ok := a.health[key]
	if !ok {
		if got == levelOK {
			return
		}
		c = a.cond(key)
	}
	tr, changed := c.observe(now, got, 0, a.mon.Reminder, a.maxGap(), a.paused(now))
	a.apply(ctx, key, c, tr, changed, func(u byte) (notify.Message, string) {
		return syncSysMessage(key.Target, age, c.notifID, u)
	}, eventReading)
	if c.level == levelOK {
		delete(a.health, key)
	}
}

// sampleRevisions triggers a check when the repo_revisions stamp, which every
// repository sync rewrites, changed since the last sample. A missing file is
// a host without one, silently; any other failure logs one WARN per failure
// streak and leaves the stamp last seen alone, so a sync made during the
// streak still triggers once the file reads again, and a failure at the first
// sample never makes the first successful read look like a sync.
func (a *App) sampleRevisions() {
	path := a.d.RepoRevisions
	if path == "" {
		return
	}
	fi, err := os.Stat(path)
	switch {
	case err == nil:
		a.revFailing = false
		if a.sched.revisions(fi.ModTime(), true) {
			a.sched.trigger()
		}
	case errors.Is(err, fs.ErrNotExist):
		a.revFailing = false
		a.sched.revisions(time.Time{}, false)
	default:
		if !a.revFailing {
			a.revFailing = true
			a.log.Warn("reading the repository sync stamp failed; syncs are not seen until it reads again",
				"monitor", monitorUpdates, "target", "", "state", a.updLevel(), "path", path, "error", err)
		}
	}
}

// emergeBusy scans for a running emerge for the update check: through the
// system reader when there is one, else through Deps.EmergeRunning, so the
// hold works with every resource monitor off. A failed scan counts as none
// running, with one WARN per failure streak.
func (a *App) emergeBusy() bool {
	scan := a.d.EmergeRunning
	if a.d.Sys != nil {
		scan = a.d.Sys.EmergeRunning
	}
	if scan == nil {
		return false
	}
	return a.scanEmerge(scan, &a.busyFailing, condKey{Monitor: monitorUpdates})
}

// startCheck starts one update check off the loop: the GLSA command, short
// and more urgent, then the @world command, each only while its monitor runs.
// The goroutine works on copies and touches no App field; it closes
// a.checkEnded once it has left its command runs, then sends its result on
// a.checked, or returns once Run has returned.
//
// The check keeps ctx's values but not its cancellation: ctx is the sample
// branch's, cancelled as soon as that branch returns, which would kill the
// check at once. Only a stop cancels it, through cancelChecks, so a quit
// kills both process groups.
func (a *App) startCheck(ctx context.Context) {
	a.sched.started()
	var world, glsa *pending.Command
	if a.updatesOn() {
		world = cloneCommand(a.d.Updates)
	}
	if a.glsaOn() {
		glsa = cloneCommand(a.d.GLSA)
	}
	checkCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	ended := make(chan struct{})
	a.checkCancel, a.checkEnded = cancel, ended
	run := a.d.Run
	a.log.Debug("update check started", "monitor", monitorUpdates, "target", "", "state", a.updLevel(),
		"world", world != nil, "glsa", glsa != nil)
	go func() {
		defer cancel()
		var r checkResult
		if glsa != nil {
			o := runGLSA(checkCtx, run, *glsa)
			r.glsa = &o
		}
		if world != nil && checkCtx.Err() == nil {
			o := runWorld(checkCtx, run, *world)
			r.world = &o
		}
		close(ended)
		select {
		case a.checked <- r:
		case <-a.done:
		}
	}()
}

// cloneCommand copies c, its slices included, for the check goroutine.
func cloneCommand(c *pending.Command) *pending.Command {
	cp := *c
	cp.Args, cp.Wrap = slices.Clone(c.Args), slices.Clone(c.Wrap)
	return &cp
}

// runWorld runs the @world command and parses what it would merge.
func runWorld(ctx context.Context, run func(context.Context, pending.Command) (pending.Result, error), c pending.Command) worldOutcome {
	res, err := run(ctx, c)
	o := worldOutcome{path: c.Path, exit: res.ExitCode, stderrTail: stderrTail(res.Stderr), err: err, dur: res.Duration}
	if err == nil && res.ExitCode == 0 {
		o.packages = pending.ParseWorld(res.Stdout)
	}
	return o
}

// runGLSA runs the advisory command and parses the advisories it lists.
func runGLSA(ctx context.Context, run func(context.Context, pending.Command) (pending.Result, error), c pending.Command) glsaOutcome {
	res, err := run(ctx, c)
	o := glsaOutcome{path: c.Path, exit: res.ExitCode, stderrTail: stderrTail(res.Stderr), err: err, dur: res.Duration}
	if err == nil && res.ExitCode == 0 {
		o.glsas, o.ok = pending.ParseGLSA(res.Stdout)
	}
	return o
}

// stderrTail is at most the last stderrTailBytes of b as valid UTF-8, for a
// WARN. A cut that falls inside a character starts at the next one, so the
// replacement of a broken rune never grows the tail past the bound.
func stderrTail(b []byte) string {
	if len(b) > stderrTailBytes {
		b = b[len(b)-stderrTailBytes:]
		for len(b) > 0 && !utf8.RuneStart(b[0]) {
			b = b[1:]
		}
	}
	return strings.ToValidUTF8(strings.TrimSpace(string(b)), "\uFFFD")
}

// onChecked applies a finished update check and lets the scheduler start the
// next one a trigger asked for while it ran.
func (a *App) onChecked(ctx context.Context, r checkResult) {
	a.sched.finished()
	a.checkCancel, a.checkEnded = nil, nil // the goroutine released its context on return
	now := a.d.Clock.Now()
	if r.glsa != nil && ctx.Err() == nil {
		a.onGLSA(ctx, now, *r.glsa)
	}
	if r.world != nil && ctx.Err() == nil {
		a.onWorld(ctx, now, *r.world)
	}
	a.render()
}

// onGLSA applies the advisory command's run to the GLSA conditions. An
// answer (exit 0 with recognisable or blank output) lists every applicable
// advisory. A command that cannot be executed disables the monitor and
// clears every GLSA condition, which no check can refresh any more. Every
// other failure keeps the conditions as they are, with one WARN per failure
// streak; a timeout logs its own WARN, unless a streak already warned.
func (a *App) onGLSA(ctx context.Context, now time.Time, o glsaOutcome) {
	switch {
	case o.err != nil && cannotExecute(o.err):
		a.mon.GLSA.Enabled = false
		a.log.Warn("the GLSA command cannot be executed; the GLSA monitor is disabled",
			"monitor", monitorGLSA, "target", "", "state", a.monitorLevel(monitorGLSA), "command", o.path, "error", o.err)
		a.reconcileGLSA(ctx, now, nil)
		return
	case errors.Is(o.err, pending.ErrTimeout):
		if !a.glsaFailing {
			a.log.Warn("the GLSA check timed out and was killed; keeping the GLSA conditions",
				"monitor", monitorGLSA, "target", "", "state", a.monitorLevel(monitorGLSA), "command", o.path,
				"timeout", a.d.GLSA.Timeout, "duration_s", o.dur.Seconds(), "stderr_tail", o.stderrTail)
		}
		return
	case o.err != nil:
		a.glsaFailed(o, "error", o.err)
		return
	case o.exit < 0:
		a.glsaFailed(o, "reason", "the command was killed by a signal")
		return
	case o.exit != 0:
		a.glsaFailed(o, "reason", "the command exited non-zero")
	case !o.ok:
		a.glsaFailed(o, "reason", "the output carries no recognisable line")
	default:
		a.glsaFailing = false
		a.reconcileGLSA(ctx, now, o.glsas)
	}
	a.logFinished(monitorGLSA, o.path, o.dur, len(o.glsas), o.exit)
}

// glsaFailed logs a failed GLSA check once per failure streak; the
// conditions are kept as they were.
func (a *App) glsaFailed(o glsaOutcome, cause string, detail any) {
	if a.glsaFailing {
		return
	}
	a.glsaFailing = true
	a.log.Warn("the GLSA check failed; keeping the GLSA conditions until a check answers",
		"monitor", monitorGLSA, "target", "", "state", a.monitorLevel(monitorGLSA), "command", o.path,
		"exit", o.exit, cause, detail, "stderr_tail", o.stderrTail)
}

// glsaNote is the one notification every applicable advisory shares:
// notifID is the id it was last sent under, 0 while none stands.
type glsaNote struct {
	notifID uint32
}

// glsaRaise is one GLSA condition a check raised, waiting for its send.
type glsaRaise struct {
	glsa    pending.GLSA
	urgency byte
}

// reconcileGLSA observes the applicable advisories with no sustain window:
// every listed one at warning, every active one no longer listed at ok.
func (a *App) reconcileGLSA(ctx context.Context, now time.Time, glsas []pending.GLSA) {
	listed := make(map[string]pending.GLSA, len(glsas))
	for _, g := range glsas {
		listed[g.ID] = g
	}
	var raised []glsaRaise
	for _, id := range slices.Sorted(maps.Keys(listed)) {
		if tr := a.observeGLSA(now, listed[id], levelWarning); tr.Notify {
			raised = append(raised, glsaRaise{glsa: listed[id], urgency: tr.Urgency})
		}
	}
	cleared := false
	for _, key := range a.healthKeys(monitorGLSA) {
		if _, ok := listed[key.Target]; !ok {
			cleared = a.observeGLSA(now, pending.GLSA{ID: key.Target}, levelOK).Withdraw || cleared
		}
	}
	a.deliverGLSA(ctx, raised, cleared)
}

// observeGLSA observes the condition of advisory g at got and returns what
// it changed, a zero transition when nothing. An advisory with no condition
// that reads ok creates none, and a condition back at ok is dropped, so the
// map holds only applicable advisories. Delivery is deliverGLSA's.
func (a *App) observeGLSA(now time.Time, g pending.GLSA, got level) transition {
	key := condKey{Monitor: monitorGLSA, Target: g.ID}
	c, ok := a.health[key]
	if !ok {
		if got == levelOK {
			return transition{}
		}
		c = a.cond(key)
	}
	tr, changed := c.observe(now, got, 0, a.mon.Reminder, a.maxGap(), a.paused(now))
	a.noteSample(key, c, tr, changed, func(u byte) (notify.Message, string) {
		return glsaSysMessage(g, 0, 0, u)
	}, eventReading)
	if c.level == levelOK {
		delete(a.health, key)
	}
	if !changed {
		return transition{}
	}
	return tr
}

// deliverGLSA sends the shared GLSA notification once for each raised
// advisory, in id order, each replacing the last one and stating how many
// advisories apply now. A failed send keeps the previous id. A cleared
// advisory withdraws the shared notification only when no GLSA condition is
// left active; while one is, a clear sends nothing.
func (a *App) deliverGLSA(ctx context.Context, raised []glsaRaise, cleared bool) {
	active := a.activeOf(monitorGLSA)
	for _, r := range raised {
		m, _ := glsaSysMessage(r.glsa, active, a.glsa.notifID, r.urgency)
		if id, err := a.sendSys(ctx, condKey{Monitor: monitorGLSA, Target: r.glsa.ID}, m); err == nil {
			a.glsa.notifID = id
		}
	}
	if cleared && active == 0 {
		id := a.glsa.notifID
		a.glsa.notifID = 0
		a.closeSys(ctx, condKey{Monitor: monitorGLSA}, id)
	}
}

// onWorld applies the @world command's run to the updates condition. A run
// that exited is an answer: exit 0 lists the pending packages, any other
// status means @world does not resolve. A command that cannot be executed
// disables the monitor and withdraws its notice, which no check can refresh
// any more; a timeout or any other failure keeps the condition as it was.
func (a *App) onWorld(ctx context.Context, now time.Time, o worldOutcome) {
	switch {
	case o.err != nil && cannotExecute(o.err):
		a.mon.Updates.Enabled = false
		a.log.Warn("the @world update command cannot be executed; the updates monitor is disabled",
			"monitor", monitorUpdates, "target", "", "state", a.updLevel(), "command", o.path, "error", o.err)
		a.worldPending(ctx, now, nil)
	case errors.Is(o.err, pending.ErrTimeout):
		a.log.Warn("the @world update check timed out and was killed; keeping the previous state",
			"monitor", monitorUpdates, "target", "", "state", a.updLevel(), "command", o.path,
			"timeout", a.d.Updates.Timeout, "duration_s", o.dur.Seconds(), "stderr_tail", o.stderrTail)
	case o.err != nil:
		a.worldFailed(o, "error", o.err)
	case o.exit < 0:
		a.worldFailed(o, "reason", "the command was killed by a signal")
	default:
		a.worldFailing = false
		if o.exit != 0 {
			a.worldBroken(ctx, now, o)
		} else {
			a.worldPending(ctx, now, o.packages)
		}
		a.logFinished(monitorUpdates, o.path, o.dur, len(o.packages), o.exit)
	}
}

// cannotExecute reports whether err says a command could not be executed at
// all: not found (the wrappers' 126 and 127 included), an absolute path gone
// since start-up, or not executable.
func cannotExecute(err error) bool {
	return errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission)
}

// worldFailed logs a failed @world check once per failure streak; the
// condition is kept as it was.
func (a *App) worldFailed(o worldOutcome, cause string, detail any) {
	if a.worldFailing {
		return
	}
	a.worldFailing = true
	a.log.Warn("the @world update check failed; keeping the previous state until a check answers",
		"monitor", monitorUpdates, "target", "", "state", a.updLevel(), "command", o.path,
		"exit", o.exit, cause, detail, "stderr_tail", o.stderrTail)
}

// logFinished logs the INFO line of a command that ran to its exit.
func (a *App) logFinished(monitor, path string, dur time.Duration, found, exit int) {
	a.log.Info("update check command finished", "monitor", monitor, "target", "",
		"state", a.monitorLevel(monitor), "command", path,
		"duration_s", dur.Seconds(), "found", found, "exit", exit)
}

// monitorLevel is the highest level among monitor's conditions, ok when
// none is active, for the lines that speak for the whole monitor.
func (a *App) monitorLevel(monitor string) string {
	l := levelOK
	for key, c := range a.health {
		if key.Monitor == monitor {
			l = max(l, c.level)
		}
	}
	return l.String()
}

// activeOf is the number of monitor's active conditions.
func (a *App) activeOf(monitor string) int {
	n := 0
	for key, c := range a.health {
		if key.Monitor == monitor && c.level != levelOK {
			n++
		}
	}
	return n
}

// updLevel is the updates condition's level, for the WARN lines.
func (a *App) updLevel() string { return a.levelOf(condKey{Monitor: monitorUpdates}) }

// levelOf is the level of key's condition, ok when there is none; unlike
// cond it never creates one.
func (a *App) levelOf(key condKey) string {
	if c, ok := a.health[key]; ok {
		return c.level.String()
	}
	return levelOK.String()
}

// worldPending observes the packages a successful @world check would merge.
// A set of packages raises the updates notice, or replaces its notification
// when the set differs from the one shown (or the notice said @world does not
// resolve); the same set, in any order or at other versions, sends nothing.
// No package clears the notice.
func (a *App) worldPending(ctx context.Context, now time.Time, names []string) {
	key := condKey{Monitor: monitorUpdates}
	c := a.cond(key)
	if len(names) == 0 {
		a.updSet, a.updBroken = nil, false
		tr, changed := c.observe(now, levelOK, 0, a.mon.Reminder, a.maxGap(), a.paused(now))
		a.apply(ctx, key, c, tr, changed, func(byte) (notify.Message, string) { return notify.Message{}, "" }, eventReading)
		return
	}
	set := slices.Compact(slices.Sorted(slices.Values(names)))
	wasActive, wasBroken, differs := c.level != levelOK, a.updBroken, !slices.Equal(set, a.updSet)
	a.updSet, a.updBroken = set, false
	build := func(u byte) (notify.Message, string) { return updatesSysMessage(names, c.notifID, u) }
	tr, changed := c.observe(now, levelNotice, 0, a.mon.Reminder, a.maxGap(), a.paused(now))
	a.apply(ctx, key, c, tr, changed, build, eventReading)
	if !wasActive {
		return
	}
	if wasBroken {
		a.logSwitch(key, "updates")
	}
	if wasBroken || differs {
		msg, _ := build(levelUrgency(c.level))
		a.resend(ctx, key, c, msg)
	}
}

// worldBroken observes a @world check that exited non-zero: the updates
// notice says @world does not resolve, replacing a pending-updates
// notification; a second such exit in a row sends nothing.
func (a *App) worldBroken(ctx context.Context, now time.Time, o worldOutcome) {
	key := condKey{Monitor: monitorUpdates}
	c := a.cond(key)
	wasActive, wasBroken := c.level != levelOK, a.updBroken
	a.updSet, a.updBroken = nil, true
	build := func(u byte) (notify.Message, string) { return worldBrokenSysMessage(c.notifID, u) }
	tr, changed := c.observe(now, levelNotice, 0, a.mon.Reminder, a.maxGap(), a.paused(now))
	a.apply(ctx, key, c, tr, changed, build, eventReading)
	a.log.Debug("@world does not resolve", "monitor", monitorUpdates, "target", "", "state", a.updLevel(),
		"command", o.path, "exit", o.exit, "stderr_tail", o.stderrTail)
	if wasActive && !wasBroken {
		a.logSwitch(key, "unresolved")
		msg, _ := build(levelUrgency(c.level))
		a.resend(ctx, key, c, msg)
	}
}

// logSwitch logs the transition line of an updates notice that switched
// between the package count and "does not resolve" at the same level; detail
// names what it shows now.
func (a *App) logSwitch(key condKey, detail string) {
	a.log.Info("system condition changed", "monitor", key.Monitor, "target", key.Target,
		"state", levelNotice.String(), "from", levelNotice.String(), "detail", detail)
}

// cancelChecks cancels the update check in flight, killing its process
// groups, and waits up to checkStopWait for it to leave its command runs, so
// the process never exits before the kill was sent. A check still running
// at the bound is abandoned with one WARN; it exits once its run returns,
// since a.done is closed by then. Safe to call more than once and with no
// check running.
func (a *App) cancelChecks() {
	if a.checkCancel == nil {
		return
	}
	a.checkCancel()
	ended := a.checkEnded
	a.checkCancel, a.checkEnded = nil, nil
	timer := time.NewTimer(checkStopWait)
	defer timer.Stop()
	select {
	case <-ended:
	case <-timer.C:
		a.log.Warn("the update check did not end after it was cancelled; leaving it behind",
			"monitor", monitorUpdates, "target", "", "state", a.updLevel(), "waited", checkStopWait)
	}
}
