package cooldown_test

// Regression tests for the Task 1 tech review of bentoo story 003
// (.draft/reports/1-tech-review.md): I1 days overflow, I2 Save writing an
// unvalidated entry, I3 a group not holding its own package, I4 a group
// split by an ungrouped member entry, D1 (R4.3) a group member with no entry
// left still has to be installed.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate/cooldown"
)

type mapInstalled map[string]string

func (m mapInstalled) HighestFromRepo(pkg, _ string) (string, bool) {
	v, ok := m[pkg]
	return v, ok
}

func TestCooldownReview_DaysBeyondTheBoundAreRefused(t *testing.T) {
	root := writeOverlayMask(t, "# BEGIN bentoo-cooldown format=1 days=106752\n# END bentoo-cooldown\n")
	if _, err := cooldown.Load(root); err == nil {
		t.Error("days=106752 loaded; the release date would overflow into the past")
	}
}

func TestCooldownReview_SaveRefusesAnUnsafeEntryAndWritesNothing(t *testing.T) {
	root := writeOverlayMask(t, "# BEGIN bentoo-cooldown format=1 days=7\n# END bentoo-cooldown\n")
	b := loadOrFatal(t, root)
	b.Add(cooldown.Entry{Package: "sys-apps/a", Version: "1.0\n=sys-libs/glibc-2.40", Since: time.Now(), Origin: "manual"})
	before := readMask(t, root)
	if err := b.Save(root); err == nil {
		t.Error("Save wrote a version carrying a second atom line")
	}
	if string(readMask(t, root)) != string(before) {
		t.Error("Save changed package.mask although it refused")
	}
	if _, err := os.Stat(filepath.Join(root, ".autoupdate", "cooldown.unmask")); err == nil {
		t.Error("Save wrote the unmask list although it refused")
	}
}

func TestCooldownReview_AGroupWithoutTheEntrysPackageIsMalformed(t *testing.T) {
	root := writeOverlayMask(t, "# BEGIN bentoo-cooldown format=1 days=7\n"+
		"# cooldown since=2026-11-01T00:00:00Z origin=manual group=sys-apps/x,sys-apps/y\n"+
		"=sys-apps/a-2.0\n# END bentoo-cooldown\n")
	if _, err := cooldown.Load(root); err == nil {
		t.Error("a group that does not list the entry's own package loaded")
	}
}

func TestCooldownReview_AnUngroupedMemberEntryDoesNotSplitTheGroup(t *testing.T) {
	b := blockOf(7,
		grouped("sys-apps/a", "1.0", mustTime(t, "2026-11-04T00:00:00Z"), "sys-apps/a,sys-apps/b"),
		entry("sys-apps/b", "1.0", mustTime(t, "2026-11-01T00:00:00Z")),
	)
	if a, bb := b.Eligible("sys-apps/a"), b.Eligible("sys-apps/b"); !a.Equal(bb) {
		t.Errorf("Eligible(a) = %s, Eligible(b) = %s; one group, one clock", a, bb)
	}
	lifts, _ := cooldown.Sweep(b, mustTime(t, "2026-11-09T00:00:00Z"), nil, nil, everyEbuildExists)
	if len(lifts) != 2 {
		t.Errorf("lifts = %v, want both members", lifts)
	}
}

func TestCooldownReview_AGroupMemberWithNoEntryMustStillBeInstalled(t *testing.T) {
	const g = "x-y/a,x-y/b,x-y/c"
	now := time.Now().UTC()
	mk := func() *cooldown.Block {
		return blockOf(7, grouped("x-y/a", "1", now, g), grouped("x-y/b", "1", now, g))
	}
	if l, _ := cooldown.Sweep(mk(), now, mapInstalled{"x-y/a": "1", "x-y/b": "1"}, nil, everyEbuildExists); len(l) != 0 {
		t.Errorf("lifted %v with x-y/c not installed (R4.3)", l)
	}
	if l, _ := cooldown.Sweep(mk(), now, mapInstalled{"x-y/a": "1", "x-y/b": "1", "x-y/c": "0.1"}, nil, everyEbuildExists); len(l) != 2 {
		t.Errorf("lifts = %v with every member installed, want 2", l)
	}
}

// R1 of the re-review: entries queued before a cooldown_with change carry the
// old group string; the group must still lift whole.
func TestCooldownReview_GroupsWithDifferentStringsStayWhole(t *testing.T) {
	b := blockOf(7,
		grouped("c-x/a", "1", mustTime(t, "2026-11-04T00:00:00Z"), "c-x/a,c-x/b"),
		grouped("c-x/b", "1", mustTime(t, "2026-11-04T00:00:00Z"), "c-x/a,c-x/b"),
		grouped("c-x/c", "1", mustTime(t, "2026-11-01T00:00:00Z"), "c-x/a,c-x/b,c-x/c"),
	)
	if lifts, _ := cooldown.Sweep(b, mustTime(t, "2026-11-09T00:00:00Z"), nil, nil, everyEbuildExists); len(lifts) != 3 {
		t.Errorf("elapsed lifts = %v, want the whole group of 3", lifts)
	}
	b = blockOf(7,
		grouped("c-x/a", "1", mustTime(t, "2026-11-04T00:00:00Z"), "c-x/a,c-x/b"),
		grouped("c-x/b", "1", mustTime(t, "2026-11-04T00:00:00Z"), "c-x/a,c-x/b"),
		grouped("c-x/c", "1", mustTime(t, "2026-11-04T00:00:00Z"), "c-x/a,c-x/b,c-x/c"),
	)
	if lifts, err := cooldown.ConfirmLift(b, []cooldown.Confirm{confirmOf("c-x/a", "1")}); err != nil || len(lifts) != 3 {
		t.Errorf("ConfirmLift(=c-x/a-1) = %v, %v; want the whole group of 3", lifts, err)
	}
}
