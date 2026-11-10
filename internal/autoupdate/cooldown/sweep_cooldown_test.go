package cooldown_test

// Authored for bentoo story 003 (autoupdate cooldown), sub-task 1.3 —
// R3.2, R3.4, R3.5, R3.7, R3.8, R4.1, R4.2, R4.3.
//
// Contract: design.md "Sweep" and "Installed", with the refined signatures
//
//	Sweep(b *Block, now time.Time, inst Installed, confirms []Confirm,
//	      exists func(pkg, ver string) bool) (lifts []Lift, rest *Block)
//	NewPkgdbInstalledContext(ctx context.Context, root string) (Installed, error)
//
// ASSUMPTION (not fixed by the contract excerpt): Confirm is a struct with the
// fields Package ("cat/pkg") and Version (the PVR), mirroring Lift. If the
// implementation names them otherwise, only confirmOf below changes.
//
// Lift reasons are the four strings design.md fixes: elapsed, installed,
// manual, stale.
//
// Hostile halves first: the same version in two packages, `pkg` vs `pkg-bin`,
// a group of THREE (a pair cannot show "every member" vs "any member"), an
// installed version from another repository, and PVR order (1.2.3 < 1.2.3-r1,
// 1.9 < 1.10).

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate/cooldown"
)

var sweepNow = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)

// fakeInstalled answers HighestFromRepo from a fixed table keyed by package
// and repository, and records every repository it was asked about.
type fakeInstalled struct {
	byRepo map[string]map[string]string // repo -> pkg -> version
	asked  map[string]bool              // repos queried
}

func newFakeInstalled() *fakeInstalled {
	return &fakeInstalled{byRepo: map[string]map[string]string{}, asked: map[string]bool{}}
}

func (f *fakeInstalled) set(repo, pkg, version string) *fakeInstalled {
	if f.byRepo[repo] == nil {
		f.byRepo[repo] = map[string]string{}
	}
	f.byRepo[repo][pkg] = version
	return f
}

func (f *fakeInstalled) HighestFromRepo(pkg, repo string) (string, bool) {
	f.asked[repo] = true
	v, ok := f.byRepo[repo][pkg]
	return v, ok
}

func everyEbuildExists(string, string) bool { return true }

func confirmOf(pkg, version string) cooldown.Confirm {
	return cooldown.Confirm{Package: pkg, Version: version}
}

type liftKey struct{ pkg, version string }

// liftsByAtom indexes lifts by atom and fails on a duplicate: an entry is
// lifted once, whatever number of rules would lift it.
func liftsByAtom(t *testing.T, lifts []cooldown.Lift) map[liftKey]string {
	t.Helper()
	m := map[liftKey]string{}
	for _, l := range lifts {
		k := liftKey{l.Package, l.Version}
		if _, dup := m[k]; dup {
			t.Errorf("%s-%s lifted twice", l.Package, l.Version)
		}
		m[k] = l.Reason
	}
	return m
}

func restAtoms(t *testing.T, rest *cooldown.Block) map[liftKey]bool {
	t.Helper()
	if rest == nil {
		t.Fatal("Sweep returned a nil remaining block")
	}
	m := map[liftKey]bool{}
	for _, e := range rest.Entries {
		m[liftKey{e.Package, e.Version}] = true
	}
	return m
}

// expectSweep asserts the exact lifted set (with reasons; "" = any reason)
// and the exact remaining set.
func expectSweep(t *testing.T, lifts []cooldown.Lift, rest *cooldown.Block, lifted map[liftKey]string, kept []liftKey) {
	t.Helper()
	got := liftsByAtom(t, lifts)
	for k, reason := range lifted {
		r, ok := got[k]
		if !ok {
			t.Errorf("%s-%s was not lifted, want reason %q", k.pkg, k.version, reason)
			continue
		}
		if reason != "" && r != reason {
			t.Errorf("%s-%s lifted with reason %q, want %q", k.pkg, k.version, r, reason)
		}
	}
	for k := range got {
		if _, want := lifted[k]; !want {
			t.Errorf("%s-%s was lifted (%s) and should have stayed masked", k.pkg, k.version, got[k])
		}
	}
	remaining := restAtoms(t, rest)
	for _, k := range kept {
		if !remaining[k] {
			t.Errorf("%s-%s is missing from the remaining block", k.pkg, k.version)
		}
	}
	if len(remaining) != len(kept) {
		t.Errorf("remaining block holds %d entries, want %d: %+v", len(remaining), len(kept), rest.Entries)
	}
}

func blockOf(days int, entries ...cooldown.Entry) *cooldown.Block {
	return &cooldown.Block{Days: days, Entries: entries}
}

// TestCooldownSweep_ElapsedLiftsTheWholeQueueAtTheOldestClock: R3.2. The
// whole queue of a package goes when its OLDEST since is Days old, the
// boundary is inclusive, and a package one second short stays.
func TestCooldownSweep_ElapsedLiftsEachVersionOnItsOwnClock(t *testing.T) {
	b := blockOf(7,
		entry("sys-apps/foo", "1.0", sweepNow.Add(-day(8))),
		entry("sys-apps/foo", "1.1", sweepNow.Add(-day(1))), // its own clock: 6 days to go
		entry("sys-apps/bar", "1.0", sweepNow.Add(-day(7))), // exactly due
		entry("sys-apps/baz", "1.0", sweepNow.Add(-day(7)+time.Second)),
		entry("sys-apps/qux", "1.0", sweepNow.Add(time.Hour)), // clock skew: in the future
	)
	lifts, rest := cooldown.Sweep(b, sweepNow, newFakeInstalled(), nil, everyEbuildExists)

	expectSweep(t, lifts, rest,
		map[liftKey]string{
			{"sys-apps/foo", "1.0"}: "elapsed",
			{"sys-apps/bar", "1.0"}: "elapsed",
		},
		[]liftKey{{"sys-apps/foo", "1.1"}, {"sys-apps/baz", "1.0"}, {"sys-apps/qux", "1.0"}})
	if rest != nil && rest.Days != 7 {
		t.Errorf("remaining block Days = %d, want 7", rest.Days)
	}
}

// TestCooldownSweep_InstalledLiftsUpToTheInstalledVersionOnly: R3.4. Hostile
// halves first: an install from ANOTHER repository lifts nothing, and a
// prefix-named package's install does not lift its neighbour.
func TestCooldownSweep_InstalledLiftsUpToTheInstalledVersionOnly(t *testing.T) {
	recent := sweepNow.Add(-day(1))
	b := blockOf(7,
		entry("sys-apps/foo", "1.0", recent),
		entry("sys-apps/foo", "1.1", recent),
		entry("sys-apps/foo", "1.2", recent),
		entry("sys-apps/gentoo-only", "2.0", recent),
		entry("sys-apps/near", "1.0", recent),
	)
	inst := newFakeInstalled().
		set("bentoo", "sys-apps/foo", "1.1").
		set("gentoo", "sys-apps/gentoo-only", "2.0").
		set("bentoo", "sys-apps/near-bin", "9.0")

	lifts, rest := cooldown.Sweep(b, sweepNow, inst, nil, everyEbuildExists)

	expectSweep(t, lifts, rest,
		map[liftKey]string{
			{"sys-apps/foo", "1.0"}: "installed",
			{"sys-apps/foo", "1.1"}: "installed",
		},
		[]liftKey{{"sys-apps/foo", "1.2"}, {"sys-apps/gentoo-only", "2.0"}, {"sys-apps/near", "1.0"}})
	if !inst.asked["bentoo"] {
		t.Error("Sweep never asked which version is installed from repository \"bentoo\"")
	}
}

// TestCooldownSweep_InstalledComparesVersionsAsPVR: an installed 1.2.3 is
// OLDER than a masked 1.2.3-r1, and 1.9 is older than 1.10 — neither lifts —
// while 1.0_p1 is newer than 1.0 and does.
func TestCooldownSweep_InstalledComparesVersionsAsPVR(t *testing.T) {
	recent := sweepNow.Add(-day(1))
	b := blockOf(7,
		entry("dev-libs/rev", "1.2.3-r1", recent),
		entry("dev-libs/ten", "1.10", recent),
		entry("dev-libs/patch", "1.0", recent),
	)
	inst := newFakeInstalled().
		set("bentoo", "dev-libs/rev", "1.2.3").
		set("bentoo", "dev-libs/ten", "1.9").
		set("bentoo", "dev-libs/patch", "1.0_p1")

	lifts, rest := cooldown.Sweep(b, sweepNow, inst, nil, everyEbuildExists)

	expectSweep(t, lifts, rest,
		map[liftKey]string{{"dev-libs/patch", "1.0"}: "installed"},
		[]liftKey{{"dev-libs/rev", "1.2.3-r1"}, {"dev-libs/ten", "1.10"}})
}

// TestCooldownSweep_ManualConfirmLiftsThatVersionAndOlder: R3.5 / D3.
// Hostile first: the same version of a prefix-named package and of an
// unrelated package stay masked.
func TestCooldownSweep_ManualConfirmLiftsThatVersionAndOlder(t *testing.T) {
	recent := sweepNow.Add(-day(1))
	b := blockOf(7,
		entry("sys-apps/foo", "1.0", recent),
		entry("sys-apps/foo", "1.1", recent),
		entry("sys-apps/foo", "1.2", recent),
		entry("sys-apps/foo-bin", "1.1", recent),
		entry("sys-apps/bar", "1.1", recent),
	)
	lifts, rest := cooldown.Sweep(b, sweepNow, newFakeInstalled(),
		[]cooldown.Confirm{confirmOf("sys-apps/foo", "1.1")}, everyEbuildExists)

	expectSweep(t, lifts, rest,
		map[liftKey]string{
			{"sys-apps/foo", "1.0"}: "manual",
			{"sys-apps/foo", "1.1"}: "manual",
		},
		[]liftKey{{"sys-apps/foo", "1.2"}, {"sys-apps/foo-bin", "1.1"}, {"sys-apps/bar", "1.1"}})
}

const groupABC = "dev-lang/a,dev-lang/b,dev-lang/c"

func groupOfThree(sinceA, sinceB, sinceC time.Time) *cooldown.Block {
	return blockOf(7,
		grouped("dev-lang/a", "1.0", sinceA, groupABC),
		grouped("dev-lang/b", "2.0", sinceB, groupABC),
		grouped("dev-lang/c", "3.0", sinceC, groupABC),
		// Not a member: same version as a, name prefixed by a.
		entry("dev-lang/a-bin", "1.0", sinceA),
	)
}

// TestCooldownSweep_AGroupOfThreeLiftsTogether: R4.1 and R4.2. A manual
// confirm of ANY one member lifts every member; the group's clock is its
// oldest member's. The prefix-named non-member stays in both cases.
func TestCooldownSweep_AGroupOfThreeLiftsTogether(t *testing.T) {
	recent := sweepNow.Add(-day(1))

	t.Run("manual confirm of the middle member", func(t *testing.T) {
		lifts, rest := cooldown.Sweep(groupOfThree(recent, recent, recent), sweepNow, newFakeInstalled(),
			[]cooldown.Confirm{confirmOf("dev-lang/b", "2.0")}, everyEbuildExists)
		expectSweep(t, lifts, rest,
			map[liftKey]string{
				{"dev-lang/a", "1.0"}: "manual",
				{"dev-lang/b", "2.0"}: "manual",
				{"dev-lang/c", "3.0"}: "manual",
			},
			[]liftKey{{"dev-lang/a-bin", "1.0"}})
	})

	t.Run("not elapsed while a member is still young", func(t *testing.T) {
		lifts, rest := cooldown.Sweep(groupOfThree(recent, recent, sweepNow.Add(-day(8))), sweepNow, newFakeInstalled(),
			nil, everyEbuildExists)
		expectSweep(t, lifts, rest, map[liftKey]string{},
			[]liftKey{{"dev-lang/a", "1.0"}, {"dev-lang/a-bin", "1.0"}, {"dev-lang/b", "2.0"}, {"dev-lang/c", "3.0"}})
	})

	t.Run("elapsed on the newest member", func(t *testing.T) {
		lifts, rest := cooldown.Sweep(groupOfThree(sweepNow.Add(-day(7)), sweepNow.Add(-day(9)), sweepNow.Add(-day(8))), sweepNow, newFakeInstalled(),
			nil, everyEbuildExists)
		expectSweep(t, lifts, rest,
			map[liftKey]string{
				{"dev-lang/a", "1.0"}: "elapsed",
				{"dev-lang/b", "2.0"}: "elapsed",
				{"dev-lang/c", "3.0"}: "elapsed",
				// Not a member: lifted on its own clock (it shares sinceA).
				{"dev-lang/a-bin", "1.0"}: "elapsed",
			},
			nil)
	})
}

// TestCooldownSweep_AGroupNeedsEveryMemberInstalled: R4.3. Hostile halves
// first — two of three installed, and three installed with one of them below
// its masked version — lift nothing; all three at their masked versions lift
// all three.
func TestCooldownSweep_AGroupNeedsEveryMemberInstalled(t *testing.T) {
	recent := sweepNow.Add(-day(1))
	allKept := []liftKey{{"dev-lang/a", "1.0"}, {"dev-lang/b", "2.0"}, {"dev-lang/c", "3.0"}, {"dev-lang/a-bin", "1.0"}}

	t.Run("two of three installed", func(t *testing.T) {
		inst := newFakeInstalled().set("bentoo", "dev-lang/a", "1.0").set("bentoo", "dev-lang/b", "2.0")
		lifts, rest := cooldown.Sweep(groupOfThree(recent, recent, recent), sweepNow, inst, nil, everyEbuildExists)
		expectSweep(t, lifts, rest, map[liftKey]string{}, allKept)
	})

	t.Run("three installed, one below its masked version", func(t *testing.T) {
		inst := newFakeInstalled().
			set("bentoo", "dev-lang/a", "1.0").set("bentoo", "dev-lang/b", "2.0").set("bentoo", "dev-lang/c", "2.9")
		lifts, rest := cooldown.Sweep(groupOfThree(recent, recent, recent), sweepNow, inst, nil, everyEbuildExists)
		expectSweep(t, lifts, rest, map[liftKey]string{}, allKept)
	})

	t.Run("every member installed", func(t *testing.T) {
		inst := newFakeInstalled().
			set("bentoo", "dev-lang/a", "1.0").set("bentoo", "dev-lang/b", "2.0").set("bentoo", "dev-lang/c", "3.0")
		lifts, rest := cooldown.Sweep(groupOfThree(recent, recent, recent), sweepNow, inst, nil, everyEbuildExists)
		expectSweep(t, lifts, rest,
			map[liftKey]string{
				{"dev-lang/a", "1.0"}: "installed",
				{"dev-lang/b", "2.0"}: "installed",
				{"dev-lang/c", "3.0"}: "installed",
			},
			[]liftKey{{"dev-lang/a-bin", "1.0"}})
	})
}

// TestCooldownSweep_AnEntryWhoseEbuildIsGoneIsLiftedStale: R3.7. Only the
// missing version goes; its queue-mate whose ebuild exists stays.
func TestCooldownSweep_AnEntryWhoseEbuildIsGoneIsLiftedStale(t *testing.T) {
	recent := sweepNow.Add(-day(1))
	b := blockOf(7, entry("sys-apps/foo", "1.0", recent), entry("sys-apps/foo", "1.1", recent))
	exists := func(pkg, ver string) bool { return pkg != "sys-apps/foo" || ver != "1.1" }

	lifts, rest := cooldown.Sweep(b, sweepNow, newFakeInstalled(), nil, exists)

	expectSweep(t, lifts, rest,
		map[liftKey]string{{"sys-apps/foo", "1.1"}: "stale"},
		[]liftKey{{"sys-apps/foo", "1.0"}})
}

// TestCooldownSweep_WithoutAnInstalledDatabaseTheOtherRulesStillApply: R3.8.
// A nil Installed (pkgdb unreadable) skips installation-based lifting only.
func TestCooldownSweep_WithoutAnInstalledDatabaseTheOtherRulesStillApply(t *testing.T) {
	b := blockOf(7,
		entry("sys-apps/old", "1.0", sweepNow.Add(-day(9))),
		entry("sys-apps/confirmed", "1.0", sweepNow.Add(-day(1))),
		entry("sys-apps/recent", "1.0", sweepNow.Add(-day(1))),
	)
	lifts, rest := cooldown.Sweep(b, sweepNow, nil,
		[]cooldown.Confirm{confirmOf("sys-apps/confirmed", "1.0")}, everyEbuildExists)

	expectSweep(t, lifts, rest,
		map[liftKey]string{
			{"sys-apps/old", "1.0"}:       "elapsed",
			{"sys-apps/confirmed", "1.0"}: "manual",
		},
		[]liftKey{{"sys-apps/recent", "1.0"}})
}

// TestCooldownSweep_AnEntryEveryRuleWouldLiftIsLiftedOnce: elapsed, installed
// and confirmed at the same time still produce one Lift, and the remaining
// entries keep every field.
func TestCooldownSweep_AnEntryEveryRuleWouldLiftIsLiftedOnce(t *testing.T) {
	keep := grouped("sys-apps/other", "4.0", sweepNow.Add(-day(1)), "")
	keep.Origin = "manual"
	b := blockOf(7, entry("sys-apps/foo", "1.0", sweepNow.Add(-day(9))), keep)
	inst := newFakeInstalled().set("bentoo", "sys-apps/foo", "1.0")

	lifts, rest := cooldown.Sweep(b, sweepNow, inst, []cooldown.Confirm{confirmOf("sys-apps/foo", "1.0")}, everyEbuildExists)

	expectSweep(t, lifts, rest, map[liftKey]string{{"sys-apps/foo", "1.0"}: ""}, []liftKey{{"sys-apps/other", "4.0"}})
	if rest != nil && len(rest.Entries) == 1 {
		if got := rest.Entries[0]; !got.Since.Equal(keep.Since) || got.Origin != "manual" {
			t.Errorf("the remaining entry lost a field: %+v, want %+v", got, keep)
		}
	}
}

// writeInstalled creates <root>/var/db/pkg/<cat>/<pf>/ with a SLOT and, when
// repo is non-empty, a repository record.
func writeInstalled(t *testing.T, root, cat, pf, repo string) {
	t.Helper()
	dir := filepath.Join(root, "var", "db", "pkg", cat, pf)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SLOT"), []byte(pf+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if repo != "" {
		if err := os.WriteFile(filepath.Join(dir, "repository"), []byte(repo+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestCooldownInstalled_ReadsTheHighestVersionPerRepository: the pkgdb
// adapter. Hostile first: a prefix-named package, a name holding a
// version-looking segment, another repository and a package with no
// repository record must not answer for the package asked about.
func TestCooldownInstalled_ReadsTheHighestVersionPerRepository(t *testing.T) {
	root := t.TempDir()
	writeInstalled(t, root, "sys-apps", "foo-1.9", "bentoo")
	writeInstalled(t, root, "sys-apps", "foo-1.10", "bentoo")
	writeInstalled(t, root, "sys-apps", "foo-2.0", "gentoo")
	writeInstalled(t, root, "sys-apps", "foo-bin-9.0", "bentoo")
	writeInstalled(t, root, "media-libs", "libfoo-2d-1.0-r1", "bentoo")
	writeInstalled(t, root, "dev-libs", "norepo-1.0", "")

	inst, err := cooldown.NewPkgdbInstalledContext(t.Context(), root)
	if err != nil {
		t.Fatalf("NewPkgdbInstalledContext(%s): %v", root, err)
	}
	if inst == nil {
		t.Fatal("NewPkgdbInstalled returned a nil Installed with no error")
	}

	cases := []struct {
		pkg, repo, want string
		ok              bool
	}{
		{"sys-apps/foo", "bentoo", "1.10", true}, // 1.10 > 1.9; gentoo's 2.0 and foo-bin's 9.0 do not count
		{"sys-apps/foo", "gentoo", "2.0", true},
		{"sys-apps/foo-bin", "bentoo", "9.0", true},
		{"media-libs/libfoo-2d", "bentoo", "1.0-r1", true},
		{"media-libs/libfoo", "bentoo", "", false},
		{"dev-libs/norepo", "bentoo", "", false},
		{"sys-apps/absent", "bentoo", "", false},
	}
	for _, c := range cases {
		got, ok := inst.HighestFromRepo(c.pkg, c.repo)
		if ok != c.ok || got != c.want {
			t.Errorf("HighestFromRepo(%q, %q) = (%q, %v), want (%q, %v)", c.pkg, c.repo, got, ok, c.want, c.ok)
		}
	}
}

// TestCooldownInstalled_AnUnreadableDatabaseIsAnError: R3.8's trigger. A root
// with no var/db/pkg is reported, so the caller can warn once and pass nil.
func TestCooldownInstalled_AnUnreadableDatabaseIsAnError(t *testing.T) {
	root := t.TempDir()
	if _, err := cooldown.NewPkgdbInstalledContext(t.Context(), root); err == nil {
		t.Errorf("NewPkgdbInstalledContext(%s) with no var/db/pkg returned no error", root)
	}
}
