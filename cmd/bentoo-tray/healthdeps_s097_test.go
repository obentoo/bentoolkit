package main

// Story 097, sub-task 11.1: healthDeps takes its host access as a value, and
// the two portageq questions are asked concurrently. Every test injects a
// fake reader, fake portageq answers and a fake stat, so none reads the
// host's /proc or /etc, stats a host path or runs portageq.
//
// Contract (design.md, cmd/bentoo-tray/main.go):
//
//	type hostHealth struct {
//		Reader  tray.SystemReader
//		TmpDir  func() string
//		DistDir func() string
//		Stat    func(string) (fs.FileInfo, error)
//	}
//	healthDeps(ctx, log, cfg config.TrayConfig, h hostHealth)
//		(tray.SystemReader, *config.Monitors, []string)
//
// With the disk monitor on and no configured paths, the watched paths are
// /, PORTAGE_TMPDIR and DISTDIR in that order, whichever portageq question
// answers first (R2.1, R2.10).

import (
	"context"
	"io/fs"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	yaml "go.yaml.in/yaml/v3"

	"github.com/obentoo/bentoolkit/internal/common/config"
)

// s097Guard bounds every wait in these fakes, so code that asks the two
// questions one after the other fails instead of hanging.
const s097Guard = 2 * time.Second

// Paths no host has, so a stat that bypassed h.Stat would drop them.
const (
	s097TmpAnswer  = "/s097-no-such-root/portage-tmpdir"
	s097DistAnswer = "/s097-no-such-root/distfiles"
)

// s097Host fakes hostHealth: two portageq answers, a stat that reports
// every path present unless listed missing, and per-func call counts. The
// funcs may run on two goroutines, so the counters are atomic.
type s097Host struct {
	probe     *s097Probe
	tmp, dist string
	missing   map[string]bool
	tmpAsked  atomic.Int32
	distAsked atomic.Int32
	statMu    sync.Mutex
	statted   []string
	tmpHook   func()
	distHook  func()
}

func newS097Host() *s097Host {
	return &s097Host{
		probe:   &s097Probe{psiErr: map[string]error{}},
		tmp:     s097TmpAnswer,
		dist:    s097DistAnswer,
		missing: map[string]bool{},
	}
}

func (f *s097Host) health() hostHealth {
	return hostHealth{
		Reader: f.probe,
		TmpDir: func() string {
			f.tmpAsked.Add(1)
			if f.tmpHook != nil {
				f.tmpHook()
			}
			return f.tmp
		},
		DistDir: func() string {
			f.distAsked.Add(1)
			if f.distHook != nil {
				f.distHook()
			}
			return f.dist
		},
		Stat: func(path string) (fs.FileInfo, error) {
			f.statMu.Lock()
			f.statted = append(f.statted, path)
			f.statMu.Unlock()
			if f.missing[path] {
				return nil, &fs.PathError{Op: "stat", Path: path, Err: fs.ErrNotExist}
			}
			return s097Info{name: path}, nil
		},
	}
}

// s097TrayCfg decodes a tray block, the way the config file reaches main.
func s097TrayCfg(t *testing.T, text string) config.TrayConfig {
	t.Helper()
	var cfg config.TrayConfig
	if err := yaml.Unmarshal([]byte(text), &cfg); err != nil {
		t.Fatalf("decoding the tray block: %v", err)
	}
	return cfg
}

// s097Wait reports whether ch closed before the guard expired.
func s097Wait(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	case <-time.After(s097Guard):
		return false
	}
}

// s097Barrier makes the two questions meet: each signals it has started and
// waits until the other has too. first names the question that must return
// first; the other waits for it before returning. Every wait that hits the
// guard is recorded as a stall.
type s097Barrier struct {
	tmpStarted, distStarted chan struct{}
	tmpDone, distDone       chan struct{}
	tmpOnce, distOnce       sync.Once
	mu                      sync.Mutex
	stalls                  []string
}

func newS097Barrier() *s097Barrier {
	return &s097Barrier{
		tmpStarted: make(chan struct{}), distStarted: make(chan struct{}),
		tmpDone: make(chan struct{}), distDone: make(chan struct{}),
	}
}

func (b *s097Barrier) stall(what string) {
	b.mu.Lock()
	b.stalls = append(b.stalls, what)
	b.mu.Unlock()
}

// install wires the barrier into f's two questions.
func (b *s097Barrier) install(f *s097Host, first string) {
	meet := func(name string, started *sync.Once, mine, other chan struct{}) bool {
		started.Do(func() { close(mine) })
		if !s097Wait(other) {
			b.stall(name + " waited " + s097Guard.String() + " for the other portageq question to start")
			return false
		}
		return true
	}
	f.tmpHook = func() {
		if meet("PORTAGE_TMPDIR", &b.tmpOnce, b.tmpStarted, b.distStarted) && first == "DISTDIR" && !s097Wait(b.distDone) {
			b.stall("PORTAGE_TMPDIR waited for DISTDIR to answer first")
		}
	}
	f.distHook = func() {
		if meet("DISTDIR", &b.distOnce, b.distStarted, b.tmpStarted) && first == "PORTAGE_TMPDIR" && !s097Wait(b.tmpDone) {
			b.stall("DISTDIR waited for PORTAGE_TMPDIR to answer first")
		}
	}
	// Each hook returns, then its question returns: mark it answered.
	tmpHook, distHook := f.tmpHook, f.distHook
	f.tmpHook = func() { tmpHook(); close(b.tmpDone) }
	f.distHook = func() { distHook(); close(b.distDone) }
}

// s097DiskWarnIndex returns the position of the disk WARN line naming part
// among all lines, or -1.
func s097DiskWarnIndex(lines []string, part string) int {
	for i, l := range lines {
		if strings.Contains(l, "level=WARN") && strings.Contains(l, "monitor=disk") && strings.Contains(l, part) {
			return i
		}
	}
	return -1
}

// TestS097HealthDeps_R2_10_PortageqQuestionsAreAskedConcurrently: hostile
// half first. Each portageq question blocks until the other has started, so
// asking them one after the other stalls on the guard; asked concurrently
// both are answered. The watched paths and the warnings keep the order /,
// PORTAGE_TMPDIR, DISTDIR whichever question answers first.
func TestS097HealthDeps_R2_10_PortageqQuestionsAreAskedConcurrently(t *testing.T) {
	for _, first := range []string{"DISTDIR", "PORTAGE_TMPDIR"} {
		t.Run(first+" answers first, both answer", func(t *testing.T) {
			f := newS097Host()
			b := newS097Barrier()
			b.install(f, first)
			log, buf := s097Logger()
			_, _, paths := healthDeps(context.Background(), log, config.TrayConfig{}, f.health())
			if len(b.stalls) != 0 {
				t.Fatalf("the portageq questions were not asked concurrently: %q", b.stalls)
			}
			if want := []string{"/", s097TmpAnswer, s097DistAnswer}; !slices.Equal(paths, want) {
				t.Errorf("paths = %q, want %q whichever question answers first", paths, want)
			}
			if l := s097Lines(buf, "level=WARN"); len(l) != 0 {
				t.Errorf("WARN lines %q, want none", l)
			}
		})
		t.Run(first+" answers first, neither answers", func(t *testing.T) {
			f := newS097Host()
			f.tmp, f.dist = "", ""
			b := newS097Barrier()
			b.install(f, first)
			log, buf := s097Logger()
			_, _, paths := healthDeps(context.Background(), log, config.TrayConfig{}, f.health())
			if len(b.stalls) != 0 {
				t.Fatalf("the portageq questions were not asked concurrently: %q", b.stalls)
			}
			if want := []string{"/"}; !slices.Equal(paths, want) {
				t.Errorf("paths = %q, want %q", paths, want)
			}
			lines := strings.Split(buf.String(), "\n")
			tmpAt, distAt := s097DiskWarnIndex(lines, "PORTAGE_TMPDIR"), s097DiskWarnIndex(lines, "DISTDIR")
			if tmpAt < 0 || distAt < 0 {
				t.Fatalf("disk WARN lines for PORTAGE_TMPDIR at %d and DISTDIR at %d, want both\n%s", tmpAt, distAt, buf.String())
			}
			if tmpAt > distAt {
				t.Errorf("the DISTDIR warning was logged before the PORTAGE_TMPDIR one; want PORTAGE_TMPDIR first whichever answers first\n%s", buf.String())
			}
		})
	}
}

// TestS097HealthDeps_R2_2_ConfiguredPathsAskNeitherQuestion: configured paths
// are watched exactly and neither portageq question is asked.
func TestS097HealthDeps_R2_2_ConfiguredPathsAskNeitherQuestion(t *testing.T) {
	f := newS097Host()
	log, _ := s097Logger()
	cfg := s097TrayCfg(t, "monitors:\n  disk:\n    paths: [/s097-no-such-root/srv, /s097-no-such-root/home]\n")
	sys, m, paths := healthDeps(context.Background(), log, cfg, f.health())
	if n, d := f.tmpAsked.Load(), f.distAsked.Load(); n != 0 || d != 0 {
		t.Errorf("portageq asked PORTAGE_TMPDIR %d and DISTDIR %d times with configured paths, want neither", n, d)
	}
	if want := []string{"/s097-no-such-root/srv", "/s097-no-such-root/home"}; !slices.Equal(paths, want) {
		t.Errorf("paths = %q, want exactly the configured %q", paths, want)
	}
	if sys != f.probe {
		t.Errorf("reader = %#v, want the injected h.Reader", sys)
	}
	if m == nil || !m.Disk.Enabled {
		t.Errorf("monitors = %+v, want the disk monitor on", m)
	}
}

// TestS097HealthDeps_R1_2_NoDiskMonitorAsksNoPortageq: with disk, memory and
// CPU all off there is no reader and nothing is asked; with only the disk
// monitor off the injected reader is kept and portageq is still not asked.
func TestS097HealthDeps_R1_2_NoDiskMonitorAsksNoPortageq(t *testing.T) {
	t.Run("disk, memory and CPU off", func(t *testing.T) {
		f := newS097Host()
		log, _ := s097Logger()
		cfg := s097TrayCfg(t, "monitors:\n  disk:\n    enabled: false\n  memory:\n    enabled: false\n  cpu:\n    enabled: false\n")
		sys, _, paths := healthDeps(context.Background(), log, cfg, f.health())
		if sys != nil {
			t.Errorf("reader = %#v, want nil with every resource monitor off", sys)
		}
		if n, d := f.tmpAsked.Load(), f.distAsked.Load(); n != 0 || d != 0 {
			t.Errorf("portageq asked PORTAGE_TMPDIR %d and DISTDIR %d times, want neither", n, d)
		}
		if len(f.statted) != 0 || len(paths) != 0 {
			t.Errorf("stat-ed %q and watching %q, want nothing", f.statted, paths)
		}
	})
	t.Run("disk off, memory on", func(t *testing.T) {
		f := newS097Host()
		log, _ := s097Logger()
		cfg := s097TrayCfg(t, "monitors:\n  disk:\n    enabled: false\n")
		sys, m, paths := healthDeps(context.Background(), log, cfg, f.health())
		if sys != f.probe {
			t.Errorf("reader = %#v, want the injected h.Reader with the memory monitor on", sys)
		}
		if m == nil || !m.Memory.Enabled || m.Disk.Enabled {
			t.Errorf("monitors = %+v, want memory on and disk off", m)
		}
		if n, d := f.tmpAsked.Load(), f.distAsked.Load(); n != 0 || d != 0 {
			t.Errorf("portageq asked PORTAGE_TMPDIR %d and DISTDIR %d times with the disk monitor off, want neither", n, d)
		}
		if len(f.statted) != 0 || len(paths) != 0 {
			t.Errorf("stat-ed %q and watching %q, want nothing", f.statted, paths)
		}
	})
}

// TestS097HealthDeps_R2_1_DefaultsUseTheInjectedHost: with the disk monitor
// on and no paths, each question is asked once, every path is checked
// through h.Stat (the answers exist nowhere on a real host), and the reader
// handed back is h.Reader, never a host reader.
func TestS097HealthDeps_R2_1_DefaultsUseTheInjectedHost(t *testing.T) {
	f := newS097Host()
	log, buf := s097Logger()
	sys, m, paths := healthDeps(context.Background(), log, config.TrayConfig{}, f.health())
	if n, d := f.tmpAsked.Load(), f.distAsked.Load(); n != 1 || d != 1 {
		t.Errorf("portageq asked PORTAGE_TMPDIR %d and DISTDIR %d times, want once each", n, d)
	}
	want := []string{"/", s097TmpAnswer, s097DistAnswer}
	if !slices.Equal(paths, want) {
		t.Errorf("paths = %q, want %q", paths, want)
	}
	got := slices.Clone(f.statted)
	slices.Sort(got)
	sorted := slices.Clone(want)
	slices.Sort(sorted)
	if !slices.Equal(got, sorted) {
		t.Errorf("h.Stat checked %q, want each of %q once", f.statted, want)
	}
	if sys != f.probe {
		t.Errorf("reader = %#v, want the injected h.Reader", sys)
	}
	if m == nil || !m.Disk.Enabled {
		t.Errorf("monitors = %+v, want the disk monitor on", m)
	}
	if l := s097Lines(buf, "level=WARN"); len(l) != 0 {
		t.Errorf("WARN lines %q, want none", l)
	}
}

// TestS097HealthDeps_R2_1_EachDiskPathsWarningIsOneDiskWarn: an unanswered
// question and a missing path are two warnings, logged as two WARN lines
// carrying monitor=disk, in the order the paths are resolved.
func TestS097HealthDeps_R2_1_EachDiskPathsWarningIsOneDiskWarn(t *testing.T) {
	f := newS097Host()
	f.tmp = ""
	f.missing[s097DistAnswer] = true
	log, buf := s097Logger()
	_, _, paths := healthDeps(context.Background(), log, config.TrayConfig{}, f.health())
	if want := []string{"/"}; !slices.Equal(paths, want) {
		t.Errorf("paths = %q, want %q", paths, want)
	}
	warns := s097Lines(buf, "level=WARN")
	if len(warns) != 2 {
		t.Fatalf("WARN lines = %q, want 2", warns)
	}
	for _, l := range warns {
		if !strings.Contains(l, "monitor=disk") {
			t.Errorf("WARN line %q lacks monitor=disk", l)
		}
	}
	if !strings.Contains(warns[0], "PORTAGE_TMPDIR") || !strings.Contains(warns[1], s097DistAnswer) {
		t.Errorf("WARN lines = %q, want the PORTAGE_TMPDIR warning then the one naming %s", warns, s097DistAnswer)
	}
}
