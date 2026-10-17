package tray_test

// Shared harness of the story 099 App tests (sub-tasks 5.3-5.5), built on
// story 097's harness only, so it does not depend on story 098's test
// helpers: a fake command runner standing in for pending.Run, a fake emerge
// log reader, a fake sync-time reader and a sample tick that works with or
// without a SystemReader.
//
// Pinned contract (design.md "internal/tray/updsample.go",
// "cmd/bentoo-tray"; tasks.md 5.3, 5.5):
//
//	Deps.Updates, Deps.GLSA  *pending.Command // nil: monitor absent
//	Deps.Repos               []news.Repo
//	Deps.RepoRevisions       string           // "" : no sync trigger
//	Deps.EmergeRunning       func() (bool, error) // used when Deps.Sys is nil
//	Deps.Run                 func(context.Context, pending.Command) (pending.Result, error)
//	Deps.SyncTime            func(location string) (time.Time, bool, error)
//	Deps.Emerge              story 098's EmergeReader (Read() ([]sysmon.EmergeEvent, error))
//
// Monitor names (log key monitor=, notification keys): "updates", "glsa",
// "sync:<repo>". The update check runs on the sample timer once 5 minutes
// have passed since start, and after each trigger.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/desktop/notify"
	"github.com/obentoo/bentoolkit/internal/gentoo/news"
	"github.com/obentoo/bentoolkit/internal/gentoo/pending"
	"github.com/obentoo/bentoolkit/internal/sysmon"
	"github.com/obentoo/bentoolkit/internal/tray"
	"github.com/obentoo/bentoolkit/internal/tray/state"
)

// Paths no host has: a runner that bypassed Deps.Run would fail to start
// them instead of running the host's emerge.
const (
	s099EmergePath = "/s099-no-such-root/usr/bin/emerge"
	s099GLSAPath   = "/s099-no-such-root/usr/bin/glsa-check"
)

var (
	s099WorldArgs = []string{"--pretend", "--update", "--deep", "--newuse", "--ask=n", "--color=n", "--quiet", "@world"}
	s099GLSAArgs  = []string{"--list", "affected", "--nocolor"}
)

func s099WorldCmd() *pending.Command {
	return &pending.Command{Path: s099EmergePath, Args: slices.Clone(s099WorldArgs), Timeout: 10 * time.Minute,
		Wrap: []string{"/s099-no-such-root/usr/bin/nice", "-n", "19", "/s099-no-such-root/usr/bin/ionice", "-c", "3"}}
}

func s099GLSACmd() *pending.Command {
	return &pending.Command{Path: s099GLSAPath, Args: slices.Clone(s099GLSAArgs), Timeout: 2 * time.Minute}
}

// ---------- fake runner ----------

type s099Answer func(n int) (pending.Result, error)

// s099Runner fakes pending.Run. Each command path has its own answer,
// called with the 1-based index of that command's call. When gated, every
// call blocks until release or until its ctx ends, returning the ctx error.
type s099Runner struct {
	mu      sync.Mutex
	answers map[string]s099Answer
	calls   []pending.Command
	ctxs    []context.Context
	n       map[string]int
	gate    chan struct{}
	started chan string
}

func newS099Runner() *s099Runner {
	return &s099Runner{answers: map[string]s099Answer{}, n: map[string]int{}, started: make(chan string, 256)}
}

func (r *s099Runner) Run(ctx context.Context, c pending.Command) (pending.Result, error) {
	r.mu.Lock()
	r.calls = append(r.calls, c)
	r.ctxs = append(r.ctxs, ctx)
	r.n[c.Path]++
	n := r.n[c.Path]
	answer := r.answers[c.Path]
	gate := r.gate
	r.mu.Unlock()
	select {
	case r.started <- c.Path:
	default:
	}
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return pending.Result{}, fmt.Errorf("%s: %w", c.Path, ctx.Err())
		}
	}
	if answer == nil {
		return pending.Result{ExitCode: 0, Duration: time.Second}, nil
	}
	return answer(n)
}

func (r *s099Runner) set(path string, a s099Answer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.answers[path] = a
}

// hold makes every later call block until release.
func (r *s099Runner) hold() chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gate = make(chan struct{})
	return r.gate
}

func (r *s099Runner) count(path string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n[path]
}

func (r *s099Runner) total() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *s099Runner) commands() []pending.Command {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

func (r *s099Runner) lastCtx() context.Context {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.ctxs) == 0 {
		return nil
	}
	return r.ctxs[len(r.ctxs)-1]
}

// s099World is an emerge --pretend answer listing pkgs ("cat/name-ver").
func s099World(pkgs ...string) pending.Result {
	var b strings.Builder
	for _, p := range pkgs {
		fmt.Fprintf(&b, "[ebuild     U  ] %s::gentoo [0.1::gentoo]\n", p)
	}
	return pending.Result{Stdout: []byte(b.String()), ExitCode: 0, Duration: 12 * time.Second}
}

// s099Fixed answers every call with res, err.
func s099Fixed(res pending.Result, err error) s099Answer {
	return func(int) (pending.Result, error) { return res, err }
}

// s099Seq answers call i with answers[i-1], the last one repeating.
func s099Seq(answers ...pending.Result) s099Answer {
	return func(n int) (pending.Result, error) {
		if n > len(answers) {
			n = len(answers)
		}
		return answers[n-1], nil
	}
}

// ---------- fake emerge log reader (story 098's EmergeReader) ----------

type s099Emerge struct {
	mu      sync.Mutex
	batches [][]sysmon.EmergeEvent
}

func (e *s099Emerge) Read() ([]sysmon.EmergeEvent, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.batches) == 0 {
		return nil, nil
	}
	b := e.batches[0]
	e.batches = e.batches[1:]
	return b, nil
}

func (e *s099Emerge) push(evs ...sysmon.EmergeEvent) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.batches = append(e.batches, evs)
}

// ---------- fake sync-time reader ----------

type s099SyncTimes struct {
	mu    sync.Mutex
	at    map[string]time.Time
	none  map[string]bool
	err   map[string]error
	asked map[string]int
}

func newS099SyncTimes() *s099SyncTimes {
	return &s099SyncTimes{at: map[string]time.Time{}, none: map[string]bool{}, err: map[string]error{}, asked: map[string]int{}}
}

func (s *s099SyncTimes) SyncTime(location string) (time.Time, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asked[location]++
	if err := s.err[location]; err != nil {
		return time.Time{}, false, err
	}
	if s.none[location] {
		return time.Time{}, false, nil
	}
	return s.at[location], true, nil
}

func (s *s099SyncTimes) set(location string, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.at[location] = at
	delete(s.err, location)
	delete(s.none, location)
}

func (s *s099SyncTimes) setErr(location string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err[location] = err
}

func (s *s099SyncTimes) setNone(location string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.none[location] = true
}

func (s *s099SyncTimes) count(location string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.asked[location]
}

// ---------- harness ----------

type s099Harness struct {
	*s097Harness
	run   *s099Runner
	em    *s099Emerge
	syncs *s099SyncTimes
	revs  string // a repo_revisions file under t.TempDir()
}

func newS099(t *testing.T) *s099Harness {
	s := &s099Harness{s097Harness: newS097(t), run: newS099Runner(), em: &s099Emerge{}, syncs: newS099SyncTimes()}
	s.revs = filepath.Join(t.TempDir(), "repo_revisions")
	if err := os.WriteFile(s.revs, []byte("gentoo 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s.setRevisions(appStart.Add(-48 * time.Hour))
	return s
}

// setRevisions sets the repo_revisions mtime, as a sync does.
func (s *s099Harness) setRevisions(at time.Time) {
	s.t.Helper()
	if err := os.Chtimes(s.revs, at, at); err != nil {
		s.t.Fatal(err)
	}
}

// startUpd runs the App with the fakes; edit may change the Deps last.
func (s *s099Harness) startUpd(edit func(*tray.Deps)) {
	s.t.Helper()
	s.start(nil, func(d *tray.Deps) {
		d.Run = s.run.Run
		d.SyncTime = s.syncs.SyncTime
		d.RepoRevisions = s.revs
		d.Emerge = s.em
		if edit != nil {
			edit(d)
		}
	})
}

// sample fires the sample timer once and waits until the sample ran (the
// timer is re-armed first thing) and the loop is parked again. It needs no
// SystemReader.
func (s *s099Harness) sample() {
	s.t.Helper()
	s.waitFor("the sample timer", func() bool { return s.clock.hasPending(s097Sample) })
	s.clock.Advance(s097Sample)
	s.waitFor("the sample timer re-armed", func() bool { return s.clock.hasPending(s097Sample) })
	s086WaitParked(s.appHarness)
}

func (s *s099Harness) samples(n int) {
	s.t.Helper()
	for range n {
		s.sample()
	}
}

// untilRun samples until the command at path has been called want times.
func (s *s099Harness) untilRun(path string, want, limit int) {
	s.t.Helper()
	for range limit {
		if s.run.count(path) >= want {
			return
		}
		s.sample()
	}
	s.waitFor(fmt.Sprintf("call %d of %s", want, path), func() bool { return s.run.count(path) >= want })
}

// waitLines waits until n log lines carry every part, then for the loop.
func (s *s099Harness) waitLines(what string, n int, parts ...string) {
	s.t.Helper()
	s.waitFor(what, func() bool { return s.countLines(parts...) >= n })
	s086WaitParked(s.appHarness)
}

// checked waits until the n-th result of monitor ("updates" or "glsa") has
// been applied: its INFO line with duration_s is logged and the loop parked.
func (s *s099Harness) checked(monitor string, n int) {
	s.t.Helper()
	s.waitLines(fmt.Sprintf("result %d of the %s check", n, monitor), n,
		"level=INFO", "monitor="+monitor, "duration_s=")
}

// firstCheck samples through the 5-minute start delay until the first
// result of monitor has been applied.
func (s *s099Harness) firstCheck(path, monitor string) {
	s.t.Helper()
	s.untilRun(path, 1, 14)
	s.checked(monitor, 1)
}

// bump changes the repo_revisions mtime to a value never used before, as a
// sync does.
func (s *s099Harness) bump() {
	s.t.Helper()
	s.setRevisions(s.clock.Now().Add(time.Duration(s.run.total()+1) * time.Hour))
}

// resync bumps repo_revisions and samples until the next call of path, then
// until result n of monitor is applied.
func (s *s099Harness) resync(path, monitor string, n int) {
	s.t.Helper()
	before := s.run.count(path)
	s.bump()
	s.untilRun(path, before+1, 4)
	s.checked(monitor, n)
}

// next bumps repo_revisions and samples, one check at a time, until call n
// of path has started. No second check starts while one runs, so once call
// n has started every earlier result has been applied.
func (s *s099Harness) next(path string, n int) {
	s.t.Helper()
	for s.run.count(path) < n {
		before := s.run.count(path)
		s.bump()
		s.untilRun(path, before+1, 4)
	}
}

func (s *s099Harness) pause() {
	s.t.Helper()
	s.menu(202)
	s.savedWhere("the pause", func(st state.State) bool { return !st.PauseUntil.IsZero() })
	s086WaitParked(s.appHarness)
}

func s099Keyed(key string) func(notify.Message) bool {
	return func(m notify.Message) bool { return m.System && m.Key == key }
}

func (s *s099Harness) sentTo(key string) []s097Sent { return s.out.matching(s099Keyed(key)) }

func (s *s099Harness) closedFor(key string) int {
	n := 0
	for _, k := range s.out.closedKeys() {
		if k == key {
			n++
		}
	}
	return n
}

var s099LevelKey = regexp.MustCompile(`(^|\s)level=`)

// wantTransitions checks monitor's "system condition changed" lines for
// target in order: INFO, from=/state= as given, exactly one level key.
func (s *s099Harness) wantTransitions(monitor, target string, fromTo ...[2]string) {
	s.t.Helper()
	var lines []string
	for _, l := range s.logs.lines() {
		if strings.Contains(l, `msg="system condition changed"`) && strings.Contains(l, "monitor="+monitor+" ") &&
			(target == "" || strings.Contains(l, "target="+target+" ") || strings.Contains(l, `target="`+target+`"`)) {
			lines = append(lines, l)
		}
	}
	if len(lines) != len(fromTo) {
		s.t.Fatalf("%s %s transition lines = %d, want %d:\n%s", monitor, target, len(lines), len(fromTo), strings.Join(lines, "\n"))
	}
	for i, ft := range fromTo {
		l := lines[i]
		if !strings.Contains(l, "level=INFO") {
			s.t.Errorf("transition %d is not INFO: %s", i, l)
		}
		if !strings.Contains(l, "from="+ft[0]) || !strings.Contains(l, "state="+ft[1]) {
			s.t.Errorf("transition %d = %s, want from=%s state=%s", i, l, ft[0], ft[1])
		}
		if n := len(s099LevelKey.FindAllString(l, -1)); n != 1 {
			s.t.Errorf("transition %d carries %d level keys, want 1: %s", i, n, l)
		}
	}
}

// warnsFor counts WARN lines of monitor carrying every part.
func (s *s099Harness) warnsFor(monitor string, parts ...string) int {
	return s.countLines(append([]string{"level=WARN", "monitor=" + monitor}, parts...)...)
}

func s099Text(m notify.Message) string { return m.Summary + "\n" + m.Body }

// errors.Is wrappers the fakes return, named as pending.Run would.
func s099StartErr(path string, cause error) error { return fmt.Errorf("starting %s: %w", path, cause) }
func s099TimeoutErr(path string) error            { return fmt.Errorf("%s: %w", path, pending.ErrTimeout) }

var _ = errors.Is
var _ news.Repo
