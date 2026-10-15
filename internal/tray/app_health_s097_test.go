package tray_test

// App-level characterization tests for the system health sampling of story
// 097, sub-task 5.1: the sampling timer, failed readings, the WARN per failure
// streak, responsiveness during a sample, one condition per filesystem, emerge
// suppression of CPU only, notification replace and withdraw per condition,
// the INFO line per transition and a restart that remembers nothing.
//
// The harness here (fake SystemReader, Notifier with Close, sample ticks) is
// shared by the 097 network and render tests.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/desktop/netmon"
	"github.com/obentoo/bentoolkit/internal/desktop/notify"
	"github.com/obentoo/bentoolkit/internal/sysmon"
	"github.com/obentoo/bentoolkit/internal/tray"
	"github.com/obentoo/bentoolkit/internal/tray/messages"
)

// The production adapters satisfy the App's interfaces (contract check of
// sub-task 5.4), and so do this file's fakes.
var (
	_ tray.SystemReader = sysmon.Reader{}
	_ tray.Notifier     = (*notify.Notifier)(nil)
	_ tray.SystemReader = (*s097Sys)(nil)
	_ tray.Notifier     = (*s097Notifier)(nil)
)

const (
	s097GiB    = uint64(1) << 30
	s097MiB    = uint64(1) << 20
	s097Sample = 30 * time.Second // the default sample interval
)

// ---------- fake SystemReader ----------

// s097Sys is a SystemReader whose readings the test sets. Every call counts;
// delay makes each call take that long, and started receives one token per
// call when it is not full.
type s097Sys struct {
	mu        sync.Mutex
	mem       sysmon.Memory
	memErr    error
	psi       map[string]sysmon.Pressure
	psiErr    map[string]error
	disks     map[string]sysmon.Disk
	diskErr   map[string]error
	emerge    bool
	emergeErr error
	delay     time.Duration
	started   chan struct{}
	all       int
	memReads  int
	psiReads  map[string]int
	diskReads map[string]int
}

func newS097Sys() *s097Sys {
	return &s097Sys{
		mem:       s097MemOK(),
		psi:       map[string]sysmon.Pressure{"cpu": {SomeAvg60: 5}, "memory": {HasFull: true}},
		psiErr:    map[string]error{},
		disks:     map[string]sysmon.Disk{},
		diskErr:   map[string]error{},
		started:   make(chan struct{}, 64),
		psiReads:  map[string]int{},
		diskReads: map[string]int{},
	}
}

// enter counts a call, signals it and waits out the delay outside the lock.
func (s *s097Sys) enter() {
	s.mu.Lock()
	s.all++
	d := s.delay
	s.mu.Unlock()
	select {
	case s.started <- struct{}{}:
	default:
	}
	if d > 0 {
		time.Sleep(d)
	}
}

func (s *s097Sys) Memory() (sysmon.Memory, error) {
	s.enter()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.memReads++
	if s.memErr != nil {
		return sysmon.Memory{}, s.memErr
	}
	return s.mem, nil
}

func (s *s097Sys) Pressure(resource string) (sysmon.Pressure, error) {
	s.enter()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.psiReads[resource]++
	if err := s.psiErr[resource]; err != nil {
		return sysmon.Pressure{}, err
	}
	return s.psi[resource], nil
}

func (s *s097Sys) Disk(path string) (sysmon.Disk, error) {
	s.enter()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.diskReads[path]++
	if err := s.diskErr[path]; err != nil {
		return sysmon.Disk{}, err
	}
	d, ok := s.disks[path]
	if !ok {
		return sysmon.Disk{}, fmt.Errorf("stat %s: %w", path, fs.ErrNotExist)
	}
	return d, nil
}

func (s *s097Sys) EmergeRunning() (bool, error) {
	s.enter()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.emerge, s.emergeErr
}

func (s *s097Sys) calls() int { s.mu.Lock(); defer s.mu.Unlock(); return s.all }
func (s *s097Sys) setMem(m sysmon.Memory, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mem, s.memErr = m, err
}
func (s *s097Sys) setDisk(d sysmon.Disk) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.disks[d.Path] = d
	delete(s.diskErr, d.Path)
}
func (s *s097Sys) setDiskErr(path string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.diskErr[path] = err
}
func (s *s097Sys) setCPU(some float64, emerge bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.psi["cpu"] = sysmon.Pressure{SomeAvg60: some}
	s.emerge = emerge
}
func (s *s097Sys) setEmergeErr(err error) { s.mu.Lock(); defer s.mu.Unlock(); s.emergeErr = err }
func (s *s097Sys) setPSIErr(resource string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.psiErr[resource] = err
}
func (s *s097Sys) setDelay(d time.Duration) { s.mu.Lock(); defer s.mu.Unlock(); s.delay = d }
func (s *s097Sys) reads() (mem int, cpu int, disk map[string]int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	disk = map[string]int{}
	for k, v := range s.diskReads {
		disk[k] = v
	}
	return s.memReads, s.psiReads["cpu"], disk
}

// Readings: 16 GiB of memory without swap, disks of 100 GiB.
func s097MemOK() sysmon.Memory   { return sysmon.Memory{Total: 16 * s097GiB, Available: 8 * s097GiB} }
func s097MemWarn() sysmon.Memory { return sysmon.Memory{Total: 16 * s097GiB, Available: s097GiB} }       // 6.25%
func s097MemCrit() sysmon.Memory { return sysmon.Memory{Total: 16 * s097GiB, Available: 512 * s097MiB} } // 3.1%, no swap

func s097Disk(path string, dev uint64, freeGiB uint64) sysmon.Disk {
	return sysmon.Disk{Path: path, Dev: dev, Size: 100 * s097GiB, Free: freeGiB * s097GiB}
}

const (
	s097DiskOK   = 50 // GiB free of 100
	s097DiskWarn = 5  // 5%, under 20 GiB: warning, not critical
	s097DiskCrit = 1  // 1%, under 2 GiB: critical
)

// ---------- fake Notifier with Close ----------

type s097Sent struct {
	Msg notify.Message
	ID  uint32
}

type s097Notifier struct {
	mu        sync.Mutex
	next      uint32
	sent      []s097Sent
	closed    []uint32
	closeKeys []string
	sendErr   error
	failed    []notify.Message
	events    chan notify.Event
}

func newS097Notifier() *s097Notifier { return &s097Notifier{events: make(chan notify.Event, 8)} }

func (n *s097Notifier) Send(_ context.Context, m notify.Message) (uint32, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.sendErr != nil {
		n.failed = append(n.failed, m)
		return 0, n.sendErr
	}
	n.next++
	n.sent = append(n.sent, s097Sent{Msg: m, ID: n.next})
	return n.next, nil
}

func (n *s097Notifier) Close(_ context.Context, id uint32, key string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.closed = append(n.closed, id)
	n.closeKeys = append(n.closeKeys, key)
	return nil
}

func (n *s097Notifier) setSendErr(err error) { n.mu.Lock(); defer n.mu.Unlock(); n.sendErr = err }
func (n *s097Notifier) failedSends() int     { n.mu.Lock(); defer n.mu.Unlock(); return len(n.failed) }
func (n *s097Notifier) closedKeys() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.closeKeys)
}

func (n *s097Notifier) Events() <-chan notify.Event { return n.events }

// matching returns the sent notifications that match.
func (n *s097Notifier) matching(match func(notify.Message) bool) []s097Sent {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []s097Sent
	for _, s := range n.sent {
		if match(s.Msg) {
			out = append(out, s)
		}
	}
	return out
}

func (n *s097Notifier) system() []s097Sent {
	return n.matching(func(m notify.Message) bool { return m.System })
}

func (n *s097Notifier) closedIDs() []uint32 {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.closed)
}

// System notifications are told apart by their condition key: "memory",
// "cpu", "disk:<watched path>" and "network".
func s097IsMemory(m notify.Message) bool { return m.System && m.Key == "memory" }
func s097IsCPU(m notify.Message) bool    { return m.System && m.Key == "cpu" }
func s097IsDiskOf(path string) func(notify.Message) bool {
	return func(m notify.Message) bool { return m.System && m.Key == "disk:"+path }
}
func s097IsDisk(m notify.Message) bool { return m.System && strings.HasPrefix(m.Key, "disk:") }
func s097IsNetDown(m notify.Message) bool {
	return m.System && m.Key == "network" && m.Summary == messages.Text(messages.SysNetDownSummary)
}
func s097IsNetBack(m notify.Message) bool {
	return m.System && m.Key == "network" && m.Summary == messages.Text(messages.SysNetBackSummary)
}
func s097IsNetwork(m notify.Message) bool { return s097IsNetDown(m) || s097IsNetBack(m) }

// ---------- harness ----------

type s097Harness struct {
	*appHarness
	sys *s097Sys
	out *s097Notifier
}

// newS097 is the shared harness with NetworkManager absent, so the network
// watch arms no timer and every 30 s timer is the sample timer.
func newS097(t *testing.T) *s097Harness {
	h := newApp(t)
	h.net.set(netmon.Verdict{NMAbsent: true})
	return &s097Harness{appHarness: h, sys: newS097Sys(), out: newS097Notifier()}
}

// s097Monitors is the resolved default configuration, edited for Deps.Monitors.
func s097Monitors(edit func(*config.Monitors)) *config.Monitors {
	m, _ := config.TrayConfig{}.GetMonitors()
	edit(&m)
	return &m
}

// s097Config decodes a `tray:` block body, as config.yaml spells it.
func s097Config(t *testing.T, body string) config.TrayConfig {
	t.Helper()
	var c config.TrayConfig
	if err := yaml.Unmarshal([]byte(body), &c); err != nil {
		t.Fatalf("decoding the tray block: %v", err)
	}
	return c
}

// start runs the App with the fake reader over paths and this harness's
// Notifier; edit may change the Deps last.
func (s *s097Harness) start(paths []string, edit func(*tray.Deps)) {
	s.t.Helper()
	startWithDeps(s.appHarness, func(d *tray.Deps) {
		d.Notify = s.out
		d.Sys = s.sys
		d.DiskPaths = paths
		if edit != nil {
			edit(d)
		}
	})
	s.waitFor("the startup fetch timer", func() bool { return s.clock.hasPending(3 * time.Minute) })
	s086WaitParked(s.appHarness)
}

// tick fires the sample timer once and waits until the sample has run and
// the loop is parked again.
func (s *s097Harness) tick() {
	s.t.Helper()
	s.waitFor("the sample timer", func() bool { return s.clock.hasPending(s097Sample) })
	n := s.sys.calls()
	s.clock.Advance(s097Sample)
	s.waitFor("the sample's readings", func() bool { return s.sys.calls() > n })
	s086WaitParked(s.appHarness)
}

func (s *s097Harness) ticks(n int) {
	s.t.Helper()
	for range n {
		s.tick()
	}
}

// tickUntil ticks until cond holds, failing after limit samples.
func (s *s097Harness) tickUntil(what string, limit int, cond func() bool) {
	s.t.Helper()
	for range limit {
		if cond() {
			return
		}
		s.tick()
	}
	if !cond() {
		s.t.Fatalf("after %d samples: no %s; sent %+v, closed %v", limit, what, s.out.system(), s.out.closedIDs())
	}
}

// countLines counts the log lines that contain every part.
func (s *s097Harness) countLines(parts ...string) int {
	n := 0
	for _, l := range s.logs.lines() {
		all := l != ""
		for _, p := range parts {
			all = all && strings.Contains(l, p)
		}
		if all {
			n++
		}
	}
	return n
}

// ---------- R1.1, R1.2 ----------

// TestS097Sample_R1_1_EveryEnabledMonitorOncePerInterval: the App arms a 30 s
// sample timer and reads every enabled monitor once per sample; a disabled
// monitor is never read.
func TestS097Sample_R1_1_EveryEnabledMonitorOncePerInterval(t *testing.T) {
	t.Run("defaults read memory, CPU and every disk each sample", func(t *testing.T) {
		s := newS097(t)
		s.sys.setDisk(s097Disk("/", 1, s097DiskOK))
		s.sys.setDisk(s097Disk("/var/tmp", 2, s097DiskOK))
		s.start([]string{"/", "/var/tmp"}, nil)
		s.waitFor("a 30 s sample timer", func() bool { return s.clock.hasPending(s097Sample) })
		mem0, cpu0, disk0 := s.sys.reads()
		s.ticks(4)
		mem, cpu, disk := s.sys.reads()
		if mem-mem0 != 4 || cpu-cpu0 != 4 {
			t.Errorf("4 samples read memory %d and CPU pressure %d times, want 4 each", mem-mem0, cpu-cpu0)
		}
		for _, p := range []string{"/", "/var/tmp"} {
			if disk[p]-disk0[p] != 4 {
				t.Errorf("4 samples read disk %s %d times, want 4", p, disk[p]-disk0[p])
			}
		}
		if !s.clock.hasPending(s097Sample) {
			t.Error("the sample timer was not re-armed after a sample")
		}
	})
	t.Run("a disabled monitor is never read", func(t *testing.T) {
		s := newS097(t)
		s.cfg = s097Config(t, "monitors:\n  memory:\n    enabled: false\n")
		s.sys.setDisk(s097Disk("/", 1, s097DiskOK))
		s.start([]string{"/"}, nil)
		s.ticks(3)
		mem, _, disk := s.sys.reads()
		if mem != 0 {
			t.Errorf("memory disabled, yet read %d times", mem)
		}
		if disk["/"] < 3 {
			t.Errorf("disk / read %d times in 3 samples, want 3", disk["/"])
		}
	})
	t.Run("Deps.Monitors disables what Config enables", func(t *testing.T) {
		s := newS097(t)
		s.start(nil, func(d *tray.Deps) {
			d.Monitors = s097Monitors(func(m *config.Monitors) { m.CPU.Enabled = false })
		})
		s.ticks(3)
		if mem, cpu, _ := s.sys.reads(); cpu != 0 || mem < 3 {
			t.Errorf("CPU pressure read %d times and memory %d times; Deps.Monitors turned CPU off", cpu, mem)
		}
	})
	t.Run("Deps.Monitors enables what Config disables", func(t *testing.T) {
		s := newS097(t)
		s.cfg = s097Config(t, "monitors:\n  cpu:\n    enabled: false\n")
		s.start(nil, func(d *tray.Deps) { d.Monitors = s097Monitors(func(*config.Monitors) {}) })
		s.ticks(2)
		if _, cpu, _ := s.sys.reads(); cpu < 2 {
			t.Errorf("CPU pressure read %d times in 2 samples; Deps.Monitors, not Config, decides", cpu)
		}
	})
}

// TestS097Sample_R1_2_NoTimerWithoutAnEnabledResourceMonitor: without a reader
// or with disk, memory and CPU all disabled no sample timer is armed; one
// enabled monitor is enough to arm it.
func TestS097Sample_R1_2_NoTimerWithoutAnEnabledResourceMonitor(t *testing.T) {
	t.Run("one enabled monitor still arms the timer", func(t *testing.T) {
		s := newS097(t)
		s.cfg = s097Config(t, "monitors:\n  disk:\n    enabled: false\n  memory:\n    enabled: false\n")
		s.start(nil, nil)
		s.waitFor("a sample timer with only the CPU monitor enabled", func() bool { return s.clock.hasPending(s097Sample) })
		s.tick()
		if _, cpu, _ := s.sys.reads(); cpu == 0 {
			t.Error("the CPU-only sample did not read the CPU pressure")
		}
	})
	t.Run("every resource monitor disabled", func(t *testing.T) {
		s := newS097(t)
		s.cfg = s097Config(t, "monitors:\n  disk:\n    enabled: false\n  memory:\n    enabled: false\n  cpu:\n    enabled: false\n")
		s.sys.setDisk(s097Disk("/", 1, s097DiskOK))
		s.start([]string{"/"}, nil)
		if s.clock.hasPending(s097Sample) {
			t.Fatalf("a sample timer is armed with every resource monitor disabled (pending %v)", s.clock.pending())
		}
		s.clock.Advance(3 * time.Minute)
		s.waitFor("the scheduled fetch", s.fetches(1))
		s086WaitParked(s.appHarness)
		if n := s.sys.calls(); n != 0 {
			t.Errorf("the reader was called %d times with every resource monitor disabled", n)
		}
		if s.clock.hasPending(s097Sample) {
			t.Errorf("a sample timer was armed after the fetch (pending %v)", s.clock.pending())
		}
	})
	t.Run("no system reader", func(t *testing.T) {
		s := newS097(t)
		s.start(nil, func(d *tray.Deps) { d.Sys = nil })
		if s.clock.hasPending(s097Sample) {
			t.Fatalf("a sample timer is armed without a SystemReader (pending %v)", s.clock.pending())
		}
	})
}

// ---------- R1.3, R1.4 ----------

// TestS097Sample_R1_3_FailedReadSkipsOnlyThatMonitor: while memory reads fail,
// the disk is still evaluated, and the active memory condition is neither
// withdrawn nor re-sent.
func TestS097Sample_R1_3_FailedReadSkipsOnlyThatMonitor(t *testing.T) {
	s := newS097(t)
	s.sys.setMem(s097MemCrit(), nil)
	s.sys.setDisk(s097Disk("/", 1, s097DiskOK))
	s.start([]string{"/"}, nil)
	s.tickUntil("critical memory notification", 8, func() bool { return len(s.out.matching(s097IsMemory)) == 1 })
	memID := s.out.matching(s097IsMemory)[0].ID

	s.sys.setMem(sysmon.Memory{}, fmt.Errorf("reading /proc/meminfo: %w", fs.ErrPermission))
	s.sys.setDisk(s097Disk("/", 1, s097DiskCrit))
	s.tickUntil("disk notification while memory fails", 6, func() bool { return len(s.out.matching(s097IsDiskOf("/"))) == 1 })
	s.ticks(6) // well past the 2 min memory sustain window
	if slices.Contains(s.out.closedIDs(), memID) {
		t.Error("a failed memory read withdrew the active memory condition")
	}
	if n := len(s.out.matching(s097IsMemory)); n != 1 {
		t.Errorf("memory notifications = %d while its reads fail, want the original 1", n)
	}
}

// TestS097Sample_R1_4_OneWarnPerFailureStreak: a failing monitor logs one WARN
// naming the monitor, the path and the cause per failure streak, per target.
func TestS097Sample_R1_4_OneWarnPerFailureStreak(t *testing.T) {
	t.Run("one streak per disk target, not one per monitor", func(t *testing.T) {
		s := newS097(t)
		s.sys.setDiskErr("/", fmt.Errorf("statfs /: %w", errors.New("input/output error")))
		s.sys.setDiskErr("/var/tmp", fmt.Errorf("statfs /var/tmp: %w", errors.New("input/output error")))
		s.start([]string{"/", "/var/tmp"}, nil)
		s.ticks(3)
		root := regexp.MustCompile(`target=/(\s|$)`)
		rootPath := regexp.MustCompile(`path=/(\s|$)`)
		var forRoot, forTmp int
		for _, l := range s.logs.lines() {
			if !strings.Contains(l, "level=WARN") || !strings.Contains(l, "monitor=disk") {
				continue
			}
			if !strings.Contains(l, "input/output error") {
				t.Errorf("disk WARN does not carry the cause: %s", l)
			}
			switch {
			case strings.Contains(l, "target=/var/tmp") && strings.Contains(l, "path=/var/tmp"):
				forTmp++
			case root.MatchString(l) && rootPath.MatchString(l):
				forRoot++
			}
		}
		if forRoot != 1 || forTmp != 1 {
			t.Errorf("disk WARN lines: %d for /, %d for /var/tmp over 3 failed samples, want 1 each\n%s",
				forRoot, forTmp, strings.Join(s.logs.lines(), "\n"))
		}
	})
	t.Run("a new streak after a success warns again", func(t *testing.T) {
		s := newS097(t)
		cause := fmt.Errorf("reading /proc/meminfo: %w", fs.ErrPermission)
		s.sys.setMem(sysmon.Memory{}, cause)
		s.start(nil, nil)
		s.ticks(3)
		warns := func() int {
			return s.countLines("level=WARN", "monitor=memory", "path=/proc/meminfo", "permission denied")
		}
		if n := warns(); n != 1 {
			t.Fatalf("memory WARN lines after 3 failed samples = %d, want 1\n%s", n, strings.Join(s.logs.lines(), "\n"))
		}
		s.sys.setMem(s097MemOK(), nil)
		s.tick()
		s.sys.setMem(sysmon.Memory{}, cause)
		s.ticks(2)
		if n := warns(); n != 2 {
			t.Errorf("memory WARN lines after fail, succeed, fail = %d, want 2", n)
		}
	})
	t.Run("a failing emerge scan warns once per streak", func(t *testing.T) {
		s := newS097(t)
		s.sys.setCPU(95, false) // at the threshold: the emerge scan is needed
		cause := errors.New("s097 process scan failed")
		s.sys.setEmergeErr(cause)
		s.start(nil, nil)
		s.ticks(4)
		warns := func() int { return s.countLines("level=WARN", cause.Error()) }
		if n := warns(); n != 1 {
			t.Fatalf("WARN lines for a failing emerge scan over 4 samples = %d, want 1\n%s", n, strings.Join(s.logs.lines(), "\n"))
		}
		s.sys.setEmergeErr(nil)
		s.tick()
		s.sys.setEmergeErr(cause)
		s.ticks(2)
		if n := warns(); n != 2 {
			t.Errorf("WARN lines for the emerge scan after fail, succeed, fail = %d, want 2", n)
		}
	})
}

// ---------- R3.4 ----------

// TestS097Sample_R3_4_MissingMemoryPressureFallsBackToMeminfo: a kernel
// without /proc/pressure/memory still gets memory conditions from meminfo and
// no WARN; any other failure of that file is a WARN, once per streak.
func TestS097Sample_R3_4_MissingMemoryPressureFallsBackToMeminfo(t *testing.T) {
	t.Run("another memory pressure error warns once", func(t *testing.T) {
		s := newS097(t)
		s.sys.setPSIErr("memory", fmt.Errorf("reading /proc/pressure/memory: %w", fs.ErrPermission))
		s.start(nil, nil)
		s.ticks(3)
		if n := s.countLines("level=WARN", "monitor=memory", "path=/proc/pressure/memory", "permission denied"); n != 1 {
			t.Errorf("WARN lines for an unreadable memory pressure file over 3 samples = %d, want 1\n%s",
				n, strings.Join(s.logs.lines(), "\n"))
		}
	})
	t.Run("missing memory pressure", func(t *testing.T) {
		s := newS097(t)
		s.sys.setPSIErr("memory", fmt.Errorf("reading /proc/pressure/memory: %w", sysmon.ErrNoPressure))
		s.sys.setMem(s097MemCrit(), nil)
		s.start(nil, nil)
		s.tickUntil("a critical memory notification from meminfo alone", 8, func() bool {
			return len(s.out.matching(s097IsMemory)) == 1
		})
		if n := s.countLines("level=WARN"); n != 0 {
			t.Errorf("WARN lines on a kernel without memory pressure = %d, want 0\n%s", n, strings.Join(s.logs.lines(), "\n"))
		}
	})
}

// ---------- R1.6 ----------

// TestS097Sample_R1_6_QuitDuringASampleStopsWithinOneSecond: Quit chosen while
// a sample's readings are under way stops the run within 1 s, even when the
// whole sample would take longer.
func TestS097Sample_R1_6_QuitDuringASampleStopsWithinOneSecond(t *testing.T) {
	s := newS097(t)
	paths := []string{"/", "/var/tmp", "/var/cache/distfiles"}
	for i, p := range paths {
		s.sys.setDisk(s097Disk(p, uint64(i+1), s097DiskOK))
	}
	s.start(paths, nil)
	s.waitFor("the sample timer", func() bool { return s.clock.hasPending(s097Sample) })
	for len(s.sys.started) > 0 {
		<-s.sys.started
	}
	s.sys.setDelay(400 * time.Millisecond) // a sample now takes 2 s or more
	s.clock.Advance(s097Sample)
	select {
	case <-s.sys.started:
	case <-time.After(3 * time.Second):
		t.Fatal("the sample did not start")
	}
	clicked := time.Now()
	s.menu(300)
	select {
	case err := <-s.done:
		s.done <- err
		if err != nil {
			t.Fatalf("Run = %v, want nil", err)
		}
		if took := time.Since(clicked); took > time.Second {
			t.Errorf("Quit during a sample took %v, want within 1s", took)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not stop within 1s of Quit during a sample")
	}
}

// ---------- R2.3 ----------

// TestS097Sample_R2_3_OneConditionPerFilesystem: paths on one filesystem (same
// Dev) are one condition named after the first watched path; different
// filesystems stay apart even with identical figures.
func TestS097Sample_R2_3_OneConditionPerFilesystem(t *testing.T) {
	type disk struct {
		path string
		dev  uint64
	}
	cases := []struct {
		name  string
		disks []disk
		want  []string // the paths announced, one notification each
	}{
		{"two filesystems with identical figures stay apart",
			[]disk{{"/var/tmp", 2}, {"/var/cache/distfiles", 3}}, []string{"/var/tmp", "/var/cache/distfiles"}},
		{"one filesystem under two paths is announced once",
			[]disk{{"/", 1}, {"/var/cache/distfiles", 1}}, []string{"/"}},
		{"the first watched path names it",
			[]disk{{"/var/cache/distfiles", 1}, {"/", 1}}, []string{"/var/cache/distfiles"}},
		{"the same path listed twice is one condition",
			[]disk{{"/", 1}, {"/", 1}}, []string{"/"}},
		{"defaults with DISTDIR on the root filesystem",
			[]disk{{"/", 1}, {"/var/tmp", 2}, {"/var/cache/distfiles", 1}}, []string{"/", "/var/tmp"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newS097(t)
			var paths []string
			for _, d := range c.disks {
				paths = append(paths, d.path)
				s.sys.setDisk(s097Disk(d.path, d.dev, s097DiskCrit))
			}
			s.start(paths, nil)
			s.tickUntil(fmt.Sprintf("%d disk notifications", len(c.want)), 8, func() bool {
				return len(s.out.matching(s097IsDisk)) >= len(c.want)
			})
			s.ticks(2)
			got := s.out.matching(s097IsDisk)
			if len(got) != len(c.want) {
				t.Fatalf("disk notifications = %+v, want one each for %v", got, c.want)
			}
			for _, p := range c.want {
				if n := len(s.out.matching(s097IsDiskOf(p))); n != 1 {
					t.Errorf("notifications naming %s = %d, want 1 (sent %+v)", p, n, got)
				}
			}
		})
	}
}

// ---------- R4.4 ----------

// TestS097Sample_R4_4_EmergeKeepsDiskAndMemory: while emerge runs, disk and
// memory are still evaluated and CPU pressure raises nothing; the same pressure
// without emerge does.
func TestS097Sample_R4_4_EmergeKeepsDiskAndMemory(t *testing.T) {
	t.Run("emerge running", func(t *testing.T) {
		s := newS097(t)
		s.sys.setCPU(95, true)
		s.sys.setMem(s097MemCrit(), nil)
		s.sys.setDisk(s097Disk("/", 1, s097DiskCrit))
		s.start([]string{"/"}, nil)
		s.tickUntil("memory and disk notifications during emerge", 8, func() bool {
			return len(s.out.matching(s097IsMemory)) == 1 && len(s.out.matching(s097IsDiskOf("/"))) == 1
		})
		s.ticks(13) // past the 5 min CPU sustain window
		if got := s.out.matching(s097IsCPU); len(got) != 0 {
			t.Errorf("CPU notifications while emerge runs = %+v, want none", got)
		}
	})
	t.Run("the same pressure without emerge", func(t *testing.T) {
		s := newS097(t)
		s.sys.setCPU(95, false)
		s.start(nil, nil)
		s.tickUntil("a CPU notification", 14, func() bool { return len(s.out.matching(s097IsCPU)) == 1 })
		if m := s.out.matching(s097IsCPU)[0].Msg; m.Urgency != 1 {
			t.Errorf("CPU notification urgency = %d, want 1 (warning, never critical)", m.Urgency)
		}
	})
}

// ---------- R6.6, R6.11, R6.12 ----------

// TestS097Sample_R6_6_LaterNotificationReplacesTheEarlierOne: escalation and
// reminders replace the same condition's last notification; a condition never
// replaces another's.
func TestS097Sample_R6_6_LaterNotificationReplacesTheEarlierOne(t *testing.T) {
	s := newS097(t)
	s.cfg = s097Config(t, "monitors:\n  reminder: 15m\n")
	s.sys.setMem(s097MemWarn(), nil)
	s.sys.setDisk(s097Disk("/", 1, s097DiskOK))
	s.start([]string{"/"}, nil)

	s.tickUntil("memory warning", 8, func() bool { return len(s.out.matching(s097IsMemory)) == 1 })
	warn := s.out.matching(s097IsMemory)[0]
	if warn.Msg.Urgency != 1 || warn.Msg.ReplacesID != 0 {
		t.Fatalf("first memory notification = %+v, want urgency 1 replacing nothing", warn.Msg)
	}

	s.sys.setMem(s097MemCrit(), nil)
	s.tickUntil("memory escalation", 8, func() bool { return len(s.out.matching(s097IsMemory)) == 2 })
	crit := s.out.matching(s097IsMemory)[1]
	if crit.Msg.Urgency != 2 || crit.Msg.ReplacesID != warn.ID {
		t.Fatalf("escalation = %+v, want urgency 2 replacing %d", crit.Msg, warn.ID)
	}

	s.sys.setDisk(s097Disk("/", 1, s097DiskCrit))
	s.tickUntil("disk notification", 6, func() bool { return len(s.out.matching(s097IsDiskOf("/"))) == 1 })
	disk := s.out.matching(s097IsDiskOf("/"))[0]
	if disk.Msg.ReplacesID != 0 {
		t.Errorf("the first disk notification replaces %d, want 0: it must not take memory's notification", disk.Msg.ReplacesID)
	}

	s.tickUntil("critical reminders for memory and disk", 40, func() bool {
		return len(s.out.matching(s097IsMemory)) >= 3 && len(s.out.matching(s097IsDiskOf("/"))) >= 2
	})
	if r := s.out.matching(s097IsMemory)[2]; r.Msg.ReplacesID != crit.ID {
		t.Errorf("memory reminder replaces %d, want the critical notification %d", r.Msg.ReplacesID, crit.ID)
	}
	if r := s.out.matching(s097IsDiskOf("/"))[1]; r.Msg.ReplacesID != disk.ID {
		t.Errorf("disk reminder replaces %d, want the disk notification %d", r.Msg.ReplacesID, disk.ID)
	}
}

// TestS097Sample_R6_12_ClearWithdrawsItsOwnNotification: a cleared condition
// closes its own notification, not another's, and sends nothing.
func TestS097Sample_R6_12_ClearWithdrawsItsOwnNotification(t *testing.T) {
	s := newS097(t)
	s.sys.setMem(s097MemCrit(), nil)
	s.sys.setDisk(s097Disk("/", 1, s097DiskCrit))
	s.start([]string{"/"}, nil)
	s.tickUntil("memory and disk notifications", 8, func() bool {
		return len(s.out.matching(s097IsMemory)) == 1 && len(s.out.matching(s097IsDiskOf("/"))) == 1
	})
	memID := s.out.matching(s097IsMemory)[0].ID
	diskID := s.out.matching(s097IsDiskOf("/"))[0].ID
	sentBefore := len(s.out.system())

	s.sys.setMem(s097MemOK(), nil)
	s.tickUntil("memory withdrawn", 8, func() bool { return slices.Contains(s.out.closedIDs(), memID) })
	if i := slices.Index(s.out.closedIDs(), memID); s.out.closedKeys()[i] != "memory" {
		t.Errorf("memory withdrawn under key %q, want \"memory\"", s.out.closedKeys()[i])
	}
	if slices.Contains(s.out.closedIDs(), diskID) {
		t.Errorf("clearing memory closed the disk notification %d too (closed %v)", diskID, s.out.closedIDs())
	}
	if n := len(s.out.system()); n != sentBefore {
		t.Errorf("clearing memory sent %d notifications, want none", n-sentBefore)
	}

	s.sys.setDisk(s097Disk("/", 1, s097DiskOK))
	s.tickUntil("disk withdrawn", 6, func() bool { return slices.Contains(s.out.closedIDs(), diskID) })
}

// ---------- R6.13 ----------

// TestS097Sample_R6_13_OneInfoLinePerTransition: becoming active, changing
// level and clearing each log one INFO with monitor, target, level, value and
// threshold.
func TestS097Sample_R6_13_OneInfoLinePerTransition(t *testing.T) {
	s := newS097(t)
	s.sys.setMem(s097MemWarn(), nil)
	s.start(nil, nil)
	transitions := func() int {
		return s.countLines("level=INFO", "monitor=memory", "target=", "value=", "threshold=")
	}

	s.tickUntil("memory warning", 8, func() bool { return len(s.out.matching(s097IsMemory)) == 1 })
	s.tick()
	if n := transitions(); n != 1 {
		t.Fatalf("INFO transition lines after becoming active = %d, want 1\n%s", n, strings.Join(s.logs.lines(), "\n"))
	}
	for _, l := range s.logs.lines() {
		if strings.Contains(l, "monitor=memory") && strings.Contains(l, "threshold=") && strings.Count(l, "level=") < 2 {
			t.Errorf("transition line has no level key besides the record level: %s", l)
		}
	}

	s.sys.setMem(s097MemCrit(), nil)
	s.tickUntil("memory escalation", 8, func() bool { return len(s.out.matching(s097IsMemory)) == 2 })
	s.tick()
	if n := transitions(); n != 2 {
		t.Fatalf("INFO transition lines after escalation = %d, want 2", n)
	}

	s.sys.setMem(s097MemOK(), nil)
	s.tickUntil("memory withdrawn", 8, func() bool { return len(s.out.closedIDs()) == 1 })
	s.tick()
	if n := transitions(); n != 3 {
		t.Errorf("INFO transition lines after clearing = %d, want 3", n)
	}
}

// ---------- R6.14 ----------

// TestS097Sample_R6_14_RestartStartsWithNoCondition: a restarted App knows no
// condition from the last run, and announces one still present once its
// sustain window has passed again.
func TestS097Sample_R6_14_RestartStartsWithNoCondition(t *testing.T) {
	first := newS097(t)
	first.sys.setMem(s097MemCrit(), nil)
	first.start(nil, nil)
	first.tickUntil("critical memory notification", 8, func() bool { return len(first.out.matching(s097IsMemory)) == 1 })
	first.cancel()
	s086Stopped(t, first.appHarness)
	saved, _ := first.store.last()

	s := newS097(t)
	s.store.initial = saved
	s.sys.setMem(s097MemCrit(), nil)
	s.start(nil, nil)
	s.tick()
	if got := s.out.matching(s097IsMemory); len(got) != 0 {
		t.Fatalf("the restarted App notified at its first sample: %+v; want the sustain window first", got)
	}
	s.tickUntil("memory announced again after the restart", 8, func() bool { return len(s.out.matching(s097IsMemory)) == 1 })
	if m := s.out.matching(s097IsMemory)[0].Msg; m.Urgency != 2 || m.ReplacesID != 0 {
		t.Errorf("announcement after restart = %+v, want a new critical notification", m)
	}
}
