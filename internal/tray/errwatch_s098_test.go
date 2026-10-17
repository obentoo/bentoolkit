package tray

// Pinned contract (story 098, design.md "internal/tray/errwatch.go"):
//
//	type emergeTracker struct { started map[string]bool; failed []string }
//	func (t *emergeTracker) feed(ev sysmon.EmergeEvent) (fail []string, ok bool)
//	func classifyMount(m sysmon.Mount, seenRW map[string]bool) level
//	func classifyDrive(d smartmon.Drive) level
//
// feed reports a change of the emerge condition: ok with a non-nil fail
// raises (or replaces) it naming the failed atoms, ok with a nil fail clears
// it, !ok changes nothing.
//
// Hostile halves first: an emerge exit that must NOT raise (nothing started,
// Ctrl+C, --keep-going, another run's atom) before the one that must; a
// mount that must NOT be watched or must NOT be called a remount before the
// rw-to-ro one; drives that must NOT be critical before the failing ones.

import (
	"slices"
	"testing"

	"github.com/obentoo/bentoolkit/internal/desktop/smartmon"
	"github.com/obentoo/bentoolkit/internal/sysmon"
)

func errwatchTracker() *emergeTracker {
	return &emergeTracker{started: map[string]bool{}}
}

func errwatchStarted(atom string) sysmon.EmergeEvent {
	return sysmon.EmergeEvent{Kind: sysmon.EmergeStarted, Atom: atom}
}

func errwatchCompleted(atom string) sysmon.EmergeEvent {
	return sysmon.EmergeEvent{Kind: sysmon.EmergeCompleted, Atom: atom}
}

func errwatchExitFailed(status int) sysmon.EmergeEvent {
	return sysmon.EmergeEvent{Kind: sysmon.EmergeExitFailed, Status: status}
}

func errwatchExitOK() sysmon.EmergeEvent {
	return sysmon.EmergeEvent{Kind: sysmon.EmergeExitOK}
}

// errwatchResult is the outcome of the last event of a sequence.
type errwatchResult struct {
	fail []string
	ok   bool
}

// errwatchFeed feeds evs in order and returns every result.
func errwatchFeed(tr *emergeTracker, evs ...sysmon.EmergeEvent) []errwatchResult {
	out := make([]errwatchResult, 0, len(evs))
	for _, ev := range evs {
		fail, ok := tr.feed(ev)
		out = append(out, errwatchResult{fail: fail, ok: ok})
	}
	return out
}

func errwatchSorted(s []string) []string {
	c := slices.Clone(s)
	slices.Sort(c)
	return c
}

func errwatchIsRaise(r errwatchResult) bool { return r.ok && r.fail != nil }
func errwatchIsClear(r errwatchResult) bool { return r.ok && r.fail == nil }

// TestS098ErrwatchEmerge_R1_3_NoConditionWithoutAStartedUnfinishedAtom: an
// unsuccessful exit raises nothing when the run started no package (Ctrl+C,
// an --ask decline, a dependency failure) or when every package it started
// completed (--keep-going skipping an unsatisfiable one).
func TestS098ErrwatchEmerge_R1_3_NoConditionWithoutAStartedUnfinishedAtom(t *testing.T) {
	cases := []struct {
		name string
		evs  []sysmon.EmergeEvent
	}{
		{"Ctrl+C before any build (status 130)", []sysmon.EmergeEvent{errwatchExitFailed(130)}},
		{"dependency failure, nothing started", []sysmon.EmergeEvent{errwatchExitFailed(1)}},
		{"--keep-going, every started package completed", []sysmon.EmergeEvent{
			errwatchStarted("dev-libs/foo-1.0"), errwatchCompleted("dev-libs/foo-1.0"),
			errwatchStarted("app-misc/bar-2.0"), errwatchCompleted("app-misc/bar-2.0"),
			errwatchExitFailed(1),
		}},
		{"a successful run", []sysmon.EmergeEvent{
			errwatchStarted("dev-libs/foo-1.0"), errwatchCompleted("dev-libs/foo-1.0"),
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for i, r := range errwatchFeed(errwatchTracker(), tc.evs...) {
				if errwatchIsRaise(r) {
					t.Errorf("event %d (%+v) raised an emerge condition naming %v; want none", i, tc.evs[i], r.fail)
				}
			}
		})
	}
	t.Run("a started or completed line alone changes nothing", func(t *testing.T) {
		tr := errwatchTracker()
		for i, r := range errwatchFeed(tr, errwatchStarted("dev-libs/foo-1.0"), errwatchCompleted("dev-libs/foo-1.0")) {
			if r.ok {
				t.Errorf("event %d reported a change %+v; only an exit line or the completion of a failed atom may", i, r)
			}
		}
	})
}

// TestS098ErrwatchEmerge_R1_9_ACompletedNeighbourIsNotFailed: with two runs
// interleaved in one log, an atom that completed before the unsuccessful exit
// is not failed, and a near-identical atom (same package, other revision)
// that never completed still is: the two are different packages.
func TestS098ErrwatchEmerge_R1_9_ACompletedNeighbourIsNotFailed(t *testing.T) {
	t.Run("collapse: a sibling revision completing does not hide the failed one", func(t *testing.T) {
		tr := errwatchTracker()
		rs := errwatchFeed(tr,
			errwatchStarted("dev-libs/foo-1.0"),
			errwatchStarted("dev-libs/foo-1.0-r1"),
			errwatchCompleted("dev-libs/foo-1.0"),
			errwatchExitFailed(1),
		)
		last := rs[len(rs)-1]
		if !errwatchIsRaise(last) || !slices.Equal(last.fail, []string{"dev-libs/foo-1.0-r1"}) {
			t.Errorf("exit after foo-1.0 completed and foo-1.0-r1 did not = %+v, want a raise naming only dev-libs/foo-1.0-r1", last)
		}
	})
	t.Run("split: one atom started twice is one failed atom", func(t *testing.T) {
		tr := errwatchTracker()
		rs := errwatchFeed(tr,
			errwatchStarted("dev-libs/foo-1.0"),
			errwatchStarted("dev-libs/foo-1.0"),
			errwatchExitFailed(1),
		)
		last := rs[len(rs)-1]
		if !errwatchIsRaise(last) || !slices.Equal(last.fail, []string{"dev-libs/foo-1.0"}) {
			t.Errorf("exit after the same atom started twice = %+v, want one entry dev-libs/foo-1.0", last)
		}
	})
	t.Run("interleaved runs: the other run's completed atom is not failed", func(t *testing.T) {
		tr := errwatchTracker()
		rs := errwatchFeed(tr,
			errwatchStarted("dev-qt/qtbase-6.10.1-r1"), // run A
			errwatchStarted("app-misc/jq-1.7.1"),       // run B
			errwatchCompleted("app-misc/jq-1.7.1"),     // run B
			errwatchExitFailed(1),                      // run A
		)
		last := rs[len(rs)-1]
		if !errwatchIsRaise(last) || !slices.Equal(last.fail, []string{"dev-qt/qtbase-6.10.1-r1"}) {
			t.Errorf("interleaved exit = %+v, want a raise naming only dev-qt/qtbase-6.10.1-r1", last)
		}
	})
	t.Run("several failed atoms are all named, in a stable order", func(t *testing.T) {
		evs := []sysmon.EmergeEvent{
			errwatchStarted("x11-libs/zzz-1"), errwatchStarted("app-misc/aaa-1"), errwatchStarted("dev-libs/mmm-1"),
			errwatchExitFailed(1),
		}
		var first []string
		for i := range 20 {
			rs := errwatchFeed(errwatchTracker(), evs...)
			last := rs[len(rs)-1]
			if !errwatchIsRaise(last) {
				t.Fatalf("exit with three unfinished atoms = %+v, want a raise", last)
			}
			if want := []string{"app-misc/aaa-1", "dev-libs/mmm-1", "x11-libs/zzz-1"}; !slices.Equal(errwatchSorted(last.fail), want) {
				t.Fatalf("failed atoms = %v, want exactly %v", last.fail, want)
			}
			if i == 0 {
				first = last.fail
			} else if !slices.Equal(first, last.fail) {
				t.Fatalf("the failed atoms came out as %v and then %v: the first atom a notification names must not vary", first, last.fail)
			}
		}
	})
}

// TestS098ErrwatchEmerge_R1_9_OtherRunSuccessDoesNotHideFailure: R1.9's own
// purpose clause, "one run's success never hides the other's failure". Run B
// ends successfully while run A's atom is still building; A then exits
// unsuccessfully and its atom must be reported, since its ">>> emerge" line is
// followed by an "exiting unsuccessfully" line before any completion of it.
func TestS098ErrwatchEmerge_R1_9_OtherRunSuccessDoesNotHideFailure(t *testing.T) {
	tr := errwatchTracker()
	rs := errwatchFeed(tr,
		errwatchStarted("dev-qt/qtbase-6.10.1-r1"), // run A
		errwatchStarted("app-misc/jq-1.7.1"),       // run B
		errwatchCompleted("app-misc/jq-1.7.1"),     // run B
		errwatchExitOK(),                           // run B ends well
		errwatchExitFailed(1),                      // run A ends badly
	)
	last := rs[len(rs)-1]
	if !errwatchIsRaise(last) || !slices.Equal(last.fail, []string{"dev-qt/qtbase-6.10.1-r1"}) {
		t.Errorf("A's failed exit after B's success = %+v, want a raise naming dev-qt/qtbase-6.10.1-r1", last)
	}
}

// TestS098ErrwatchEmerge_R1_2_R1_7_R1_8_RaiseReplaceClear: a failed run
// raises; a second failed run replaces the failure list (it does not add to
// it); completing an atom of the OLD list does not clear the new one;
// completing every atom of the active list clears; a successful exit clears.
func TestS098ErrwatchEmerge_R1_2_R1_7_R1_8_RaiseReplaceClear(t *testing.T) {
	t.Run("a second failure replaces, and the first run's atom no longer clears it", func(t *testing.T) {
		tr := errwatchTracker()
		rs := errwatchFeed(tr, errwatchStarted("dev-libs/old-1"), errwatchExitFailed(1))
		if r := rs[1]; !errwatchIsRaise(r) || !slices.Equal(r.fail, []string{"dev-libs/old-1"}) {
			t.Fatalf("first failed run = %+v, want a raise naming dev-libs/old-1", r)
		}
		rs = errwatchFeed(tr, errwatchStarted("dev-libs/new-1"), errwatchExitFailed(1))
		if r := rs[1]; !errwatchIsRaise(r) || !slices.Equal(r.fail, []string{"dev-libs/new-1"}) {
			t.Fatalf("second failed run = %+v, want a raise naming only dev-libs/new-1", r)
		}
		if r := errwatchFeed(tr, errwatchStarted("dev-libs/old-1"), errwatchCompleted("dev-libs/old-1"))[1]; errwatchIsClear(r) {
			t.Errorf("completing dev-libs/old-1 cleared the condition naming dev-libs/new-1 (%+v)", r)
		}
		if r := errwatchFeed(tr, errwatchStarted("dev-libs/new-1"), errwatchCompleted("dev-libs/new-1"))[1]; !errwatchIsClear(r) {
			t.Errorf("completing the only failed atom = %+v, want a clear", r)
		}
	})
	t.Run("completing only some failed atoms does not clear", func(t *testing.T) {
		tr := errwatchTracker()
		errwatchFeed(tr, errwatchStarted("dev-libs/a-1"), errwatchStarted("dev-libs/b-1"), errwatchExitFailed(1))
		if r := errwatchFeed(tr, errwatchStarted("dev-libs/a-1"), errwatchCompleted("dev-libs/a-1"))[1]; errwatchIsClear(r) {
			t.Errorf("completing a-1 while b-1 is still failed cleared the condition (%+v)", r)
		}
		if r := errwatchFeed(tr, errwatchStarted("dev-libs/b-1"), errwatchCompleted("dev-libs/b-1"))[1]; !errwatchIsClear(r) {
			t.Errorf("completing the last failed atom = %+v, want a clear", r)
		}
	})
	t.Run("a successful exit clears", func(t *testing.T) {
		tr := errwatchTracker()
		errwatchFeed(tr, errwatchStarted("dev-libs/a-1"), errwatchExitFailed(1))
		if r := errwatchFeed(tr, errwatchExitOK())[0]; !errwatchIsClear(r) {
			t.Errorf("exiting successfully after a failure = %+v, want a clear", r)
		}
	})
	t.Run("a failed exit with nothing started keeps the active failure", func(t *testing.T) {
		tr := errwatchTracker()
		errwatchFeed(tr, errwatchStarted("dev-libs/a-1"), errwatchExitFailed(1))
		if r := errwatchFeed(tr, errwatchExitFailed(130))[0]; r.ok {
			t.Errorf("a Ctrl+C exit after a failure = %+v, want no change", r)
		}
	})
	t.Run("an interrupted build (status 130) raises nothing and forgets its atoms", func(t *testing.T) {
		tr := errwatchTracker()
		for i, r := range errwatchFeed(tr, errwatchStarted("dev-libs/a-1"), errwatchExitFailed(130)) {
			if errwatchIsRaise(r) {
				t.Errorf("event %d of a Ctrl+C mid-build = %+v, want no raise", i, r)
			}
		}
		if r := errwatchFeed(tr, errwatchExitFailed(1))[0]; errwatchIsRaise(r) {
			t.Errorf("a later failed exit with nothing new started = %+v, want no raise: the interrupted atom must be forgotten", r)
		}
	})
	t.Run("one failed run raises once", func(t *testing.T) {
		tr := errwatchTracker()
		raises := 0
		for _, r := range errwatchFeed(tr,
			errwatchStarted("dev-libs/a-1"), errwatchStarted("dev-libs/b-1"),
			errwatchCompleted("dev-libs/a-1"), errwatchExitFailed(1)) {
			if errwatchIsRaise(r) {
				raises++
			}
		}
		if raises != 1 {
			t.Errorf("one failed run raised %d times, want 1", raises)
		}
	})
}

// ---------- classifyMount ----------

func errwatchMount(point, source, fstype string, ro bool) sysmon.Mount {
	return sysmon.Mount{Point: point, Source: source, FSType: fstype, ReadOnly: ro}
}

// errwatchLevels feeds ms in order through one seenRW map.
func errwatchLevels(seen map[string]bool, ms ...sysmon.Mount) []level {
	out := make([]level, 0, len(ms))
	for _, m := range ms {
		out = append(out, classifyMount(m, seen))
	}
	return out
}

// TestS098ErrwatchMount_R3_2_UnwatchedFilesystemsNeverRaise: a mount whose
// source is not a block device, or whose type is pseudo, network or image,
// is never critical, even when it goes from rw to ro.
func TestS098ErrwatchMount_R3_2_UnwatchedFilesystemsNeverRaise(t *testing.T) {
	cases := []struct{ point, source, fstype string }{
		{"/tmp", "tmpfs", "tmpfs"},
		{"/run/user/1000", "/dev/shm-like", "tmpfs"}, // a /dev/ source does not make tmpfs watched
		{"/usr/portage", "/dev/loop0", "squashfs"},
		{"/var/lib/docker/overlay2/x/merged", "overlay", "overlay"},
		{"/srv/nfs", "server:/export", "nfs"},
		{"/srv/nfs4", "server:/export", "nfs4"},
		{"/srv/share", "//server/share", "cifs"},
		{"/home/u/remote", "u@host:/", "fuse.sshfs"},
		{"/media/cdrom", "/dev/sr0", "iso9660"},
		{"/mnt/notdev", "/devices/sda1", "ext4"}, // "/dev" without the slash is not a block device
		{"/mnt/label", "LABEL=data", "ext4"},
	}
	for _, tc := range cases {
		t.Run(tc.fstype+" "+tc.source, func(t *testing.T) {
			got := errwatchLevels(map[string]bool{},
				errwatchMount(tc.point, tc.source, tc.fstype, false),
				errwatchMount(tc.point, tc.source, tc.fstype, true))
			if got[1] != levelOK {
				t.Errorf("%s (%s on %s) going rw to ro = level %d, want ok: it is not a watched mount", tc.point, tc.fstype, tc.source, got[1])
			}
		})
	}
	t.Run("an unwatched rw mount does not arm the block device later mounted ro there", func(t *testing.T) {
		got := errwatchLevels(map[string]bool{},
			errwatchMount("/mnt/x", "tmpfs", "tmpfs", false),
			errwatchMount("/mnt/x", "/dev/sdb1", "ext4", true))
		if got[1] != levelOK {
			t.Errorf("/dev/sdb1 read-only at its first sight at /mnt/x = level %d, want ok: the tmpfs before it was never watched", got[1])
		}
	})
}

// TestS098ErrwatchMount_R3_3_R3_4_RemountIsCriticalFirstSightIsNot: read-only
// at first sight is intentional (ok, and stays ok while it stays ro); a
// watched mount seen rw and then ro at the same mount point is critical; a
// near-identical mount point is a different mount.
func TestS098ErrwatchMount_R3_3_R3_4_RemountIsCriticalFirstSightIsNot(t *testing.T) {
	t.Run("ro at first sight is ok, and stays ok", func(t *testing.T) {
		got := errwatchLevels(map[string]bool{},
			errwatchMount("/boot", "/dev/sda1", "vfat", true),
			errwatchMount("/boot", "/dev/sda1", "vfat", true))
		if got[0] != levelOK || got[1] != levelOK {
			t.Errorf("a mount read-only since first sight = %v, want ok, ok", got)
		}
	})
	t.Run("collapse: rw at /mnt/data does not arm /mnt/data2 or /mnt", func(t *testing.T) {
		seen := map[string]bool{}
		errwatchLevels(seen, errwatchMount("/mnt/data", "/dev/sdb1", "ext4", false))
		for _, p := range []string{"/mnt/data2", "/mnt", "/mnt/data/sub"} {
			if l := classifyMount(errwatchMount(p, "/dev/sdc1", "ext4", true), seen); l != levelOK {
				t.Errorf("%s read-only at first sight after /mnt/data was rw = level %d, want ok", p, l)
			}
		}
	})
	t.Run("rw then ro is critical, stays critical, and rw again is ok", func(t *testing.T) {
		got := errwatchLevels(map[string]bool{},
			errwatchMount("/", "/dev/sda2", "ext4", false),
			errwatchMount("/", "/dev/sda2", "ext4", true),
			errwatchMount("/", "/dev/sda2", "ext4", true),
			errwatchMount("/", "/dev/sda2", "ext4", false))
		if want := []level{levelOK, levelCritical, levelCritical, levelOK}; !slices.Equal(got, want) {
			t.Errorf("/ rw, ro, ro, rw = %v, want %v", got, want)
		}
	})
	t.Run("ro, then rw, then ro is critical", func(t *testing.T) {
		got := errwatchLevels(map[string]bool{},
			errwatchMount("/var", "/dev/sda3", "xfs", true),
			errwatchMount("/var", "/dev/sda3", "xfs", false),
			errwatchMount("/var", "/dev/sda3", "xfs", true))
		if want := []level{levelOK, levelOK, levelCritical}; !slices.Equal(got, want) {
			t.Errorf("/var ro, rw, ro = %v, want %v", got, want)
		}
	})
	t.Run("a mount point with an escaped space is its own key", func(t *testing.T) {
		got := errwatchLevels(map[string]bool{},
			errwatchMount("/mnt/my disk", "/dev/sdd1", "ext4", false),
			errwatchMount("/mnt/my", "/dev/sde1", "ext4", true),
			errwatchMount("/mnt/my disk", "/dev/sdd1", "ext4", true))
		if want := []level{levelOK, levelOK, levelCritical}; !slices.Equal(got, want) {
			t.Errorf("'/mnt/my disk' rw, '/mnt/my' ro, '/mnt/my disk' ro = %v, want %v", got, want)
		}
	})
}

// ---------- classifyDrive ----------

// TestS098ErrwatchDrive_R4_2_R4_3_FailingOrWarningIsCritical: a healthy
// drive, an empty warning list included, is ok; SmartFailing or any NVMe
// critical warning is critical.
func TestS098ErrwatchDrive_R4_2_R4_3_FailingOrWarningIsCritical(t *testing.T) {
	cases := []struct {
		name string
		d    smartmon.Drive
		want level
	}{
		{"healthy ATA", smartmon.Drive{Path: "/org/freedesktop/UDisks2/drives/A", Model: "WDC", Serial: "1"}, levelOK},
		{"NVMe with an empty, non-nil warning list", smartmon.Drive{Path: "/org/freedesktop/UDisks2/drives/B", Warnings: []string{}}, levelOK},
		{"ATA SmartFailing", smartmon.Drive{Path: "/org/freedesktop/UDisks2/drives/C", Failing: true}, levelCritical},
		{"NVMe spare warning", smartmon.Drive{Path: "/org/freedesktop/UDisks2/drives/D", Warnings: []string{"spare"}}, levelCritical},
		{"NVMe several warnings", smartmon.Drive{Path: "/org/freedesktop/UDisks2/drives/E", Warnings: []string{"temperature", "readonly"}}, levelCritical},
		{"failing and warnings", smartmon.Drive{Path: "/org/freedesktop/UDisks2/drives/F", Failing: true, Warnings: []string{"degraded"}}, levelCritical},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyDrive(tc.d); got != tc.want {
				t.Errorf("classifyDrive(%+v) = %d, want %d", tc.d, got, tc.want)
			}
		})
	}
}
