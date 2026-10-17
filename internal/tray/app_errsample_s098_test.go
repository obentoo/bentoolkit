package tray_test

// Story 098, sub-task 5.3: the emerge log and the mount table, read on the
// sample timer, raise, notify, replace and withdraw through story 097's
// condition lifecycle with no sustain window.
//
// Pinned contract (design.md "internal/tray/sample.go and app.go"): Deps
// gains Emerge (Read() ([]sysmon.EmergeEvent, error)), Mounts (Mounts()
// ([]sysmon.Mount, error)) and PortageTmp; nil means absent. Notification
// keys are "emerge" and "readonly:<mount point>".
//
// Hostile halves first: exits that must raise nothing, build.log paths that
// must NOT be shown (outside <PORTAGE_TMPDIR>/portage, a sibling directory
// sharing its prefix, an unset PORTAGE_TMPDIR resolved against the working
// directory, a later failed atom's log), mounts that must not raise, and a
// near-identical mount point that must not share a condition.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/sysmon"
	"github.com/obentoo/bentoolkit/internal/tray"
)

// The production readers satisfy the App's interfaces.
var (
	_ tray.EmergeReader = (*sysmon.EmergeFollower)(nil)
	_ tray.MountReader  = sysmon.Reader{}
	_ tray.EmergeReader = (*s098Emerge)(nil)
	_ tray.MountReader  = (*s098Mounts)(nil)
)

// ---------- fakes ----------

// s098Emerge hands out one queued batch of events per Read, then none.
type s098Emerge struct {
	mu      sync.Mutex
	batches [][]sysmon.EmergeEvent
	err     error
	reads   int
}

func (e *s098Emerge) Read() ([]sysmon.EmergeEvent, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.reads++
	if e.err != nil {
		return nil, e.err
	}
	if len(e.batches) == 0 {
		return nil, nil
	}
	b := e.batches[0]
	e.batches = e.batches[1:]
	return b, nil
}

func (e *s098Emerge) push(evs ...sysmon.EmergeEvent) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.batches = append(e.batches, evs)
}
func (e *s098Emerge) setErr(err error) { e.mu.Lock(); defer e.mu.Unlock(); e.err = err }
func (e *s098Emerge) count() int       { e.mu.Lock(); defer e.mu.Unlock(); return e.reads }

// s098Mounts returns the mount table the test sets.
type s098Mounts struct {
	mu    sync.Mutex
	list  []sysmon.Mount
	err   error
	reads int
}

func (m *s098Mounts) Mounts() ([]sysmon.Mount, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reads++
	if m.err != nil {
		return nil, m.err
	}
	return slices.Clone(m.list), nil
}

func (m *s098Mounts) set(list ...sysmon.Mount) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.list, m.err = list, nil
}
func (m *s098Mounts) setErr(err error) { m.mu.Lock(); defer m.mu.Unlock(); m.err = err }
func (m *s098Mounts) count() int       { m.mu.Lock(); defer m.mu.Unlock(); return m.reads }

func s098Started(atom string) sysmon.EmergeEvent {
	return sysmon.EmergeEvent{Kind: sysmon.EmergeStarted, Atom: atom}
}
func s098Completed(atom string) sysmon.EmergeEvent {
	return sysmon.EmergeEvent{Kind: sysmon.EmergeCompleted, Atom: atom}
}
func s098ExitFailed(status int) sysmon.EmergeEvent {
	return sysmon.EmergeEvent{Kind: sysmon.EmergeExitFailed, Status: status}
}
func s098ExitOK() sysmon.EmergeEvent { return sysmon.EmergeEvent{Kind: sysmon.EmergeExitOK} }

func s098Mount(point, source, fstype string, ro bool) sysmon.Mount {
	return sysmon.Mount{Point: point, Source: source, FSType: fstype, ReadOnly: ro}
}

const s098Qtbase = "dev-qt/qtbase-6.10.1-r1"

// startPolled runs the App with the fake readers; a nil reader stays unset.
func (s *s098Harness) startPolled(em *s098Emerge, mt *s098Mounts, portageTmp string, edit func(*tray.Deps)) {
	s.t.Helper()
	s.start(nil, func(d *tray.Deps) {
		if em != nil {
			d.Emerge = em
		}
		if mt != nil {
			d.Mounts = mt
		}
		d.PortageTmp = portageTmp
		if edit != nil {
			edit(d)
		}
	})
}

func (s *s098Harness) emergeSent() []s097Sent { return s.out.matching(s098Keyed("emerge")) }

// failOnce queues one failed run of atom and samples it.
func (s *s098Harness) failOnce(em *s098Emerge, atoms ...string) {
	s.t.Helper()
	var evs []sysmon.EmergeEvent
	for _, a := range atoms {
		evs = append(evs, s098Started(a))
	}
	em.push(append(evs, s098ExitFailed(1))...)
	s.tick()
}

// s098BuildLog creates <tmp>/<rel>/temp/build.log and returns its path.
func s098BuildLog(t *testing.T, tmp, rel string) string {
	t.Helper()
	dir := filepath.Join(tmp, rel, "temp")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "build.log")
	if err := os.WriteFile(p, []byte("make: *** [all] Error 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// ---------- emerge: R1.1-R1.11 ----------

// TestS098Sample_R1_3_NoConditionForCtrlCOrKeepGoing: a run interrupted
// before any build, or a --keep-going run whose every started package
// completed, raises nothing: no notification, no tooltip line, no
// transition line.
func TestS098Sample_R1_3_NoConditionForCtrlCOrKeepGoing(t *testing.T) {
	s := newS098(t)
	em := &s098Emerge{}
	s.startPolled(em, nil, "", nil)
	em.push(s098ExitFailed(130))
	s.tick()
	em.push(s098Started("dev-libs/foo-1.0"), s098Completed("dev-libs/foo-1.0"), s098ExitFailed(1))
	s.ticks(3)
	if em.count() < 4 {
		t.Fatalf("the emerge log was read %d times in 4 samples, want 4", em.count())
	}
	if got := s.emergeSent(); len(got) != 0 {
		t.Errorf("emerge notifications = %+v, want none", got)
	}
	if lines := s.transitions("emerge"); len(lines) != 0 {
		t.Errorf("emerge transition lines = %q, want none", lines)
	}
	if a := s.alertLines(); len(a) != 0 {
		t.Errorf("tooltip alerts = %q, want none", a)
	}
}

// TestS098Sample_R1_2_R1_4_FailedBuildNotifiesAtOnceNamingTheAtom: the first
// sample after a failed run sends one warning naming the first failed atom
// and, when more failed, how many others; a warning is never re-sent.
func TestS098Sample_R1_2_R1_4_FailedBuildNotifiesAtOnceNamingTheAtom(t *testing.T) {
	t.Run("one failed atom, a completed one not named", func(t *testing.T) {
		s := newS098(t)
		s.cfg = s097Config(t, "monitors:\n  reminder: 15m\n")
		em := &s098Emerge{}
		s.startPolled(em, nil, "", nil)
		em.push(s098Started("app-misc/jq-1.7.1"), s098Started(s098Qtbase), s098Completed("app-misc/jq-1.7.1"), s098ExitFailed(1))
		s.tick()
		sent := s.emergeSent()
		if len(sent) != 1 {
			t.Fatalf("emerge notifications after the failed run's sample = %+v, want 1 at once", sent)
		}
		m := sent[0].Msg
		if m.Urgency != 1 || m.ReplacesID != 0 || m.Summary == "" {
			t.Errorf("emerge notification = %+v, want urgency 1 (warning), replacing nothing, with a summary", m)
		}
		if !strings.Contains(m.Body, s098Qtbase) {
			t.Errorf("emerge body %q does not name the failed atom %s", m.Body, s098Qtbase)
		}
		if strings.Contains(m.Body, "jq") || strings.Contains(m.Body, "more") {
			t.Errorf("emerge body %q names the completed atom or other failures; only %s failed", m.Body, s098Qtbase)
		}
		v := s.viewNow()
		if len(v.Alerts) != 1 || v.AlertCritical {
			t.Errorf("view = %+v, want one non-critical alert line", v)
		}
		s.wantTransitions("emerge", [2]string{"ok", "warning"})

		s.ticks(32) // past two 15 min reminders: a warning never reminds
		if n := len(s.emergeSent()); n != 1 {
			t.Errorf("emerge notifications after 16 min more = %d, want still 1", n)
		}
	})
	t.Run("three failed atoms name the first and two more", func(t *testing.T) {
		s := newS098(t)
		em := &s098Emerge{}
		s.startPolled(em, nil, "", nil)
		s.failOnce(em, "app-misc/aaa-1.0", "app-misc/bbb-1.0", "app-misc/ccc-1.0")
		sent := s.emergeSent()
		if len(sent) != 1 {
			t.Fatalf("emerge notifications = %+v, want 1", sent)
		}
		if b := sent[0].Msg.Body; !strings.Contains(b, "app-misc/aaa-1.0") || !strings.Contains(b, "2 more") {
			t.Errorf("emerge body %q, want the first failed atom app-misc/aaa-1.0 and \"2 more\"", b)
		}
	})
}

// TestS098Sample_R1_5_R1_6_BuildLogOnlyWhenItExistsUnderPortageTmp: the
// notification states the first failed atom's build.log only when that file
// exists inside <PORTAGE_TMPDIR>/portage.
func TestS098Sample_R1_5_R1_6_BuildLogOnlyWhenItExistsUnderPortageTmp(t *testing.T) {
	escapes := []struct{ name, atom, rel string }{
		{"a category of ..", "../evil", "evil"},
		{"a version climbing out", "dev-qt/../../evil", "evil"},
		{"a sibling directory sharing the portage prefix", "../portage-evil", "portage-evil"},
	}
	for _, tc := range escapes {
		t.Run("hostile: "+tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			outside := s098BuildLog(t, tmp, tc.rel)
			s := newS098(t)
			em := &s098Emerge{}
			s.startPolled(em, nil, tmp, nil)
			s.failOnce(em, tc.atom)
			if em.count() == 0 {
				t.Fatal("the emerge log was never read; nothing was tested")
			}
			// Dropping the malformed atom altogether is safe too; stating
			// a path outside <PORTAGE_TMPDIR>/portage is not.
			for _, m := range s.emergeSent() {
				if strings.Contains(m.Msg.Body, "build.log") || strings.Contains(m.Msg.Body, outside) {
					t.Errorf("atom %q led the notification to %s, outside %s/portage: %q", tc.atom, outside, tmp, m.Msg.Body)
				}
			}
		})
	}
	t.Run("hostile: an unset PORTAGE_TMPDIR never resolves against the working directory", func(t *testing.T) {
		cwd := t.TempDir()
		s098BuildLog(t, cwd, filepath.Join("portage", s098Qtbase))
		t.Chdir(cwd)
		s := newS098(t)
		em := &s098Emerge{}
		s.startPolled(em, nil, "", nil)
		s.failOnce(em, s098Qtbase)
		sent := s.emergeSent()
		if len(sent) != 1 {
			t.Fatalf("emerge notifications = %+v, want 1", sent)
		}
		if strings.Contains(sent[0].Msg.Body, "build.log") {
			t.Errorf("with no PORTAGE_TMPDIR the body states a log path: %q", sent[0].Msg.Body)
		}
	})
	t.Run("hostile: a later failed atom's log is not the first atom's", func(t *testing.T) {
		tmp := t.TempDir()
		s098BuildLog(t, tmp, filepath.Join("portage", "app-misc/bbb-1.0"))
		s := newS098(t)
		em := &s098Emerge{}
		s.startPolled(em, nil, tmp, nil)
		s.failOnce(em, "app-misc/aaa-1.0", "app-misc/bbb-1.0")
		sent := s.emergeSent()
		if len(sent) != 1 {
			t.Fatalf("emerge notifications = %+v, want 1", sent)
		}
		if strings.Contains(sent[0].Msg.Body, "build.log") {
			t.Errorf("only bbb's log exists, yet the notification for aaa states a log: %q", sent[0].Msg.Body)
		}
	})
	t.Run("the log exists: its path is stated", func(t *testing.T) {
		tmp := t.TempDir()
		want := s098BuildLog(t, tmp, filepath.Join("portage", s098Qtbase))
		s := newS098(t)
		em := &s098Emerge{}
		s.startPolled(em, nil, tmp, nil)
		s.failOnce(em, s098Qtbase)
		sent := s.emergeSent()
		if len(sent) != 1 || !strings.Contains(sent[0].Msg.Body, want) || !strings.Contains(sent[0].Msg.Body, s098Qtbase) {
			t.Errorf("emerge notifications = %+v, want one naming %s and %s", sent, s098Qtbase, want)
		}
	})
	t.Run("the log is absent: no path, the atom still named", func(t *testing.T) {
		tmp := t.TempDir()
		s := newS098(t)
		em := &s098Emerge{}
		s.startPolled(em, nil, tmp, nil)
		s.failOnce(em, s098Qtbase)
		sent := s.emergeSent()
		if len(sent) != 1 || strings.Contains(sent[0].Msg.Body, "build.log") || !strings.Contains(sent[0].Msg.Body, s098Qtbase) {
			t.Errorf("emerge notifications = %+v, want one naming %s and no log path", sent, s098Qtbase)
		}
	})
}

// TestS098Sample_R1_7_ClearWithdrawsAndSendsNothing: a successful exit, or
// the completion of every failed atom, withdraws the emerge notification
// under its own key, sends nothing and empties the tooltip.
func TestS098Sample_R1_7_ClearWithdrawsAndSendsNothing(t *testing.T) {
	for _, tc := range []struct {
		name  string
		clear []sysmon.EmergeEvent
	}{
		{"a successful run", []sysmon.EmergeEvent{s098ExitOK()}},
		{"the failed atom completing", []sysmon.EmergeEvent{s098Started(s098Qtbase), s098Completed(s098Qtbase)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newS098(t)
			em := &s098Emerge{}
			s.startPolled(em, nil, "", nil)
			s.failOnce(em, s098Qtbase)
			sent := s.emergeSent()
			if len(sent) != 1 {
				t.Fatalf("emerge notifications = %+v, want 1", sent)
			}
			em.push(tc.clear...)
			s.tick()
			if !slices.Contains(s.out.closedIDs(), sent[0].ID) {
				t.Errorf("closed %v, want the emerge notification %d withdrawn", s.out.closedIDs(), sent[0].ID)
			}
			if keys := s.out.closedKeys(); !slices.Contains(keys, "emerge") || slices.Contains(keys, "cpu") {
				t.Errorf("close keys = %q, want \"emerge\" and never \"cpu\"", keys)
			}
			if n := len(s.emergeSent()); n != 1 {
				t.Errorf("emerge notifications after the clear = %d, want still 1: a clear sends nothing", n)
			}
			if a := s.alertLines(); len(a) != 0 {
				t.Errorf("tooltip alerts after the clear = %q, want none", a)
			}
			s.wantTransitions("emerge", [2]string{"ok", "warning"}, [2]string{"warning", "ok"})
		})
	}
}

// TestS098Sample_R1_8_SecondFailureReplacesTheNotification: a second failed
// run while the condition is active sends again, replacing the first
// notification and naming the newer run's atom; nothing is withdrawn.
func TestS098Sample_R1_8_SecondFailureReplacesTheNotification(t *testing.T) {
	s := newS098(t)
	em := &s098Emerge{}
	s.startPolled(em, nil, "", nil)
	s.failOnce(em, "dev-libs/old-1.0")
	s.failOnce(em, "dev-libs/new-2.0")
	sent := s.emergeSent()
	if len(sent) != 2 {
		t.Fatalf("emerge notifications after two failed runs = %+v, want 2", sent)
	}
	second := sent[1].Msg
	if second.ReplacesID != sent[0].ID {
		t.Errorf("the second failure replaces %d, want the first notification %d", second.ReplacesID, sent[0].ID)
	}
	if !strings.Contains(second.Body, "dev-libs/new-2.0") || strings.Contains(second.Body, "dev-libs/old-1.0") {
		t.Errorf("second emerge body %q, want the newer run's atom only", second.Body)
	}
	if len(s.out.closedIDs()) != 0 {
		t.Errorf("closed %v: a replacement withdraws nothing", s.out.closedIDs())
	}
	if a := s.alertLines(); len(a) != 1 {
		t.Errorf("tooltip alerts = %q, want one emerge line", a)
	}
}

// TestS098Sample_R1_11_FailedReadsKeepTheConditionAndWarnOncePerStreak: a
// failing read of the configured log keeps the active condition, is retried
// every sample, and logs one WARN naming that path and the cause per streak.
func TestS098Sample_R1_11_FailedReadsKeepTheConditionAndWarnOncePerStreak(t *testing.T) {
	s := newS098(t)
	s.cfg = s097Config(t, "monitors:\n  emerge:\n    log: /srv/logs/emerge.log\n")
	em := &s098Emerge{}
	s.startPolled(em, nil, "", nil)
	s.failOnce(em, s098Qtbase)
	first := s.emergeSent()
	if len(first) != 1 {
		t.Fatalf("emerge notifications = %+v, want 1", first)
	}
	id := first[0].ID

	cause := fmt.Errorf("reading the emerge log: %w", fs.ErrPermission)
	em.setErr(cause)
	before := em.count()
	s.ticks(3)
	if n := em.count() - before; n != 3 {
		t.Errorf("a failing log was read %d times in 3 samples, want a retry every sample", n)
	}
	if slices.Contains(s.out.closedIDs(), id) {
		t.Error("a failed read withdrew the active emerge condition")
	}
	warns := func() int { return s.warns("monitor=emerge", "path=/srv/logs/emerge.log", "permission denied") }
	if n := warns(); n != 1 {
		t.Fatalf("emerge WARN lines after 3 failed reads = %d, want 1\n%s", n, strings.Join(s.logs.lines(), "\n"))
	}
	em.setErr(nil)
	s.tick()
	em.setErr(cause)
	s.ticks(2)
	if n := warns(); n != 2 {
		t.Errorf("emerge WARN lines after fail, succeed, fail = %d, want 2", n)
	}
	if n := len(s.emergeSent()); n != 1 {
		t.Errorf("emerge notifications = %d, want the original 1", n)
	}
}

// TestS098Sample_R5_1_PauseSilencesTheEmergeWarning: a failure raised while
// paused shows in the tooltip, sends nothing, and is not sent on resume.
func TestS098Sample_R5_1_PauseSilencesTheEmergeWarning(t *testing.T) {
	s := newS098(t)
	em := &s098Emerge{}
	s.startPolled(em, nil, "", nil)
	s.pause()
	s.failOnce(em, s098Qtbase)
	if got := s.emergeSent(); len(got) != 0 {
		t.Errorf("an emerge warning was sent while paused: %+v", got)
	}
	if a := s.alertLines(); len(a) != 1 {
		t.Errorf("tooltip alerts while paused = %q, want the emerge line", a)
	}
	s.resume()
	s.ticks(2)
	if got := s.emergeSent(); len(got) != 0 {
		t.Errorf("resuming sent the emerge warning raised during the pause: %+v", got)
	}
}

// TestS098Sample_R6_2_DisabledMonitorsAreNeverRead: emerge and read-only
// disabled in the configuration are never read, even with readers set.
func TestS098Sample_R6_2_DisabledMonitorsAreNeverRead(t *testing.T) {
	s := newS098(t)
	s.cfg = s097Config(t, "monitors:\n  emerge:\n    enabled: false\n  readonly:\n    enabled: false\n")
	em, mt := &s098Emerge{}, &s098Mounts{}
	mt.set(s098Mount("/", "/dev/sda2", "ext4", false))
	s.startPolled(em, mt, "", nil)
	s.ticks(3)
	if em.count() != 0 || mt.count() != 0 {
		t.Errorf("disabled monitors read: emerge log %d times, mount table %d times; want 0", em.count(), mt.count())
	}
}

// TestS098Sample_R1_1_R3_1_EventMonitorsAloneArmTheSampleTimer: without a
// SystemReader, the emerge and read-only monitors still sample, each once
// per sample interval.
func TestS098Sample_R1_1_R3_1_EventMonitorsAloneArmTheSampleTimer(t *testing.T) {
	s := newS098(t)
	em, mt := &s098Emerge{}, &s098Mounts{}
	mt.set(s098Mount("/", "/dev/sda2", "ext4", false))
	s.startPolled(em, mt, "", func(d *tray.Deps) { d.Sys = nil })
	e0, m0 := em.count(), mt.count()
	for i := range 3 {
		s.waitFor("a 30 s sample timer", func() bool { return s.clock.hasPending(s097Sample) })
		n := em.count()
		s.clock.Advance(s097Sample)
		s.waitFor(fmt.Sprintf("sample %d's emerge read", i+1), func() bool { return em.count() > n })
		s086WaitParked(s.appHarness)
	}
	if de, dm := em.count()-e0, mt.count()-m0; de != 3 || dm != 3 {
		t.Errorf("3 samples read the emerge log %d and the mount table %d times, want 3 each", de, dm)
	}
}

// ---------- read-only: R3.1-R3.7 ----------

func (s *s098Harness) readonlySent(point string) []s097Sent {
	return s.out.matching(s098Keyed("readonly:" + point))
}

// TestS098Sample_R3_3_R3_6_RemountReadOnlyIsCritical: a watched mount seen
// rw and then ro sends one critical notification at once, under its own
// mount point's key, naming the mount point, device and filesystem type; a
// near-identical mount point is untouched.
func TestS098Sample_R3_3_R3_6_RemountReadOnlyIsCritical(t *testing.T) {
	s := newS098(t)
	mt := &s098Mounts{}
	mt.set(s098Mount("/mnt/data", "/dev/sdb1", "ext4", false),
		s098Mount("/mnt/data2", "/dev/nvme0n1p3", "btrfs", false))
	s.startPolled(nil, mt, "", nil)
	s.tick()
	mt.set(s098Mount("/mnt/data", "/dev/sdb1", "ext4", false),
		s098Mount("/mnt/data2", "/dev/nvme0n1p3", "btrfs", true))
	s.tick()

	if got := s.readonlySent("/mnt/data"); len(got) != 0 {
		t.Errorf("/mnt/data stayed rw, yet notified: %+v", got)
	}
	sent := s.readonlySent("/mnt/data2")
	if len(sent) != 1 {
		t.Fatalf("read-only notifications for /mnt/data2 = %+v, want 1 at once", sent)
	}
	m := sent[0].Msg
	if m.Urgency != 2 || m.ReplacesID != 0 || m.Summary == "" {
		t.Errorf("read-only notification = %+v, want urgency 2 (critical), replacing nothing", m)
	}
	for _, want := range []string{"/mnt/data2", "/dev/nvme0n1p3", "btrfs"} {
		if !strings.Contains(m.Summary+" "+m.Body, want) {
			t.Errorf("read-only notification %q / %q does not state %s", m.Summary, m.Body, want)
		}
	}
	if v := s.viewNow(); len(v.Alerts) != 1 || !v.AlertCritical {
		t.Errorf("view = %+v, want one critical alert line", v)
	}
	s.wantTransitions("readonly", [2]string{"ok", "critical"})
	if l := s.transitions("readonly")[0]; !strings.Contains(l, "target=/mnt/data2") {
		t.Errorf("read-only transition line %q does not carry target=/mnt/data2", l)
	}
}

// TestS098Sample_R3_2_R3_4_IntentionalOrUnwatchedRaiseNothing: a mount read-
// only at first sight, and an unwatched filesystem going rw to ro, raise
// nothing.
func TestS098Sample_R3_2_R3_4_IntentionalOrUnwatchedRaiseNothing(t *testing.T) {
	s := newS098(t)
	mt := &s098Mounts{}
	mt.set(s098Mount("/boot", "/dev/sda1", "vfat", true), s098Mount("/tmp", "tmpfs", "tmpfs", false))
	s.startPolled(nil, mt, "", nil)
	s.ticks(2)
	mt.set(s098Mount("/boot", "/dev/sda1", "vfat", true), s098Mount("/tmp", "tmpfs", "tmpfs", true))
	s.ticks(2)
	if mt.count() < 4 {
		t.Fatalf("the mount table was read %d times in 4 samples; nothing was tested", mt.count())
	}
	for _, l := range s.out.system() {
		if strings.HasPrefix(l.Msg.Key, "readonly:") {
			t.Errorf("a read-only notification was sent: %+v", l.Msg)
		}
	}
	if lines := s.transitions("readonly"); len(lines) != 0 {
		t.Errorf("read-only transition lines = %q, want none", lines)
	}
}

// TestS098Sample_R3_5_RwAgainOrGoneWithdrawsOnlyItsOwn: a mount seen rw
// again, or gone from the table, withdraws its own notification and sends
// nothing; another read-only mount's notification stays.
func TestS098Sample_R3_5_RwAgainOrGoneWithdrawsOnlyItsOwn(t *testing.T) {
	rw := func() []sysmon.Mount {
		return []sysmon.Mount{s098Mount("/home", "/dev/sda3", "ext4", false), s098Mount("/var", "/dev/sda4", "xfs", false)}
	}
	ro := func() []sysmon.Mount {
		return []sysmon.Mount{s098Mount("/home", "/dev/sda3", "ext4", true), s098Mount("/var", "/dev/sda4", "xfs", true)}
	}
	for _, tc := range []struct {
		name  string
		after []sysmon.Mount
	}{
		{"/home rw again", []sysmon.Mount{s098Mount("/home", "/dev/sda3", "ext4", false), s098Mount("/var", "/dev/sda4", "xfs", true)}},
		{"/home unmounted", []sysmon.Mount{s098Mount("/var", "/dev/sda4", "xfs", true)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newS098(t)
			mt := &s098Mounts{}
			mt.set(rw()...)
			s.startPolled(nil, mt, "", nil)
			s.tick()
			mt.set(ro()...)
			s.tick()
			home, varSent := s.readonlySent("/home"), s.readonlySent("/var")
			if len(home) != 1 || len(varSent) != 1 {
				t.Fatalf("read-only notifications: /home %+v, /var %+v, want 1 each", home, varSent)
			}
			mt.set(tc.after...)
			s.tick()
			closed := s.out.closedIDs()
			if !slices.Contains(closed, home[0].ID) {
				t.Errorf("closed %v, want /home's notification %d withdrawn", closed, home[0].ID)
			}
			if slices.Contains(closed, varSent[0].ID) {
				t.Errorf("closed %v includes /var's notification %d, still read-only", closed, varSent[0].ID)
			}
			if keys := s.out.closedKeys(); !slices.Equal(keys, []string{"readonly:/home"}) {
				t.Errorf("close keys = %q, want only readonly:/home", keys)
			}
			if n := len(s.readonlySent("/home")); n != 1 {
				t.Errorf("/home notifications after the clear = %d, want still 1", n)
			}
			if v := s.viewNow(); len(v.Alerts) != 1 || !v.AlertCritical {
				t.Errorf("view = %+v, want /var's critical line only", v)
			}
		})
	}
}

// TestS098Sample_R3_7_UnreadableMountTableKeepsConditions: a failing read
// keeps every read-only condition (it is not an empty table), and logs one
// WARN per streak naming the mount table and the cause.
func TestS098Sample_R3_7_UnreadableMountTableKeepsConditions(t *testing.T) {
	s := newS098(t)
	mt := &s098Mounts{}
	mt.set(s098Mount("/home", "/dev/sda3", "ext4", false))
	s.startPolled(nil, mt, "", nil)
	s.tick()
	mt.set(s098Mount("/home", "/dev/sda3", "ext4", true))
	s.tick()
	sent := s.readonlySent("/home")
	if len(sent) != 1 {
		t.Fatalf("read-only notifications = %+v, want 1", sent)
	}
	mt.setErr(fmt.Errorf("reading the mount table: %w", errors.New("input/output error")))
	s.ticks(3)
	if slices.Contains(s.out.closedIDs(), sent[0].ID) {
		t.Error("an unreadable mount table withdrew the read-only condition")
	}
	if a := s.alertLines(); len(a) != 1 {
		t.Errorf("tooltip alerts while the table is unreadable = %q, want the read-only line kept", a)
	}
	if n := s.warns("monitor=readonly", "mountinfo", "input/output error"); n != 1 {
		t.Errorf("read-only WARN lines after 3 failed reads = %d, want 1 naming mountinfo and the cause\n%s",
			n, strings.Join(s.logs.lines(), "\n"))
	}
}

// TestS098Sample_R5_1_ReadOnlyCriticalOverridesPauseAndReminds: a read-only
// remount notifies while paused, and reminds once per reminder interval,
// replacing its own last notification.
func TestS098Sample_R5_1_ReadOnlyCriticalOverridesPauseAndReminds(t *testing.T) {
	s := newS098(t)
	s.cfg = s097Config(t, "monitors:\n  reminder: 15m\n")
	mt := &s098Mounts{}
	mt.set(s098Mount("/", "/dev/sda2", "ext4", false))
	s.startPolled(nil, mt, "", nil)
	s.tick()
	s.pause()
	mt.set(s098Mount("/", "/dev/sda2", "ext4", true))
	s.tick()
	first := s.readonlySent("/")
	if len(first) != 1 || first[0].Msg.Urgency != 2 {
		t.Fatalf("read-only notifications while paused = %+v, want 1 critical", first)
	}
	s.tickUntil("a read-only reminder", 35, func() bool { return len(s.readonlySent("/")) >= 2 })
	if r := s.readonlySent("/")[1]; r.Msg.ReplacesID != first[0].ID || r.Msg.Urgency != 2 {
		t.Errorf("reminder = %+v, want urgency 2 replacing %d", r.Msg, first[0].ID)
	}
	if elapsed := s.clock.Now().Sub(appStart); elapsed < 15*time.Minute {
		t.Errorf("the reminder came %v after start, before the 15 min interval", elapsed)
	}
}
