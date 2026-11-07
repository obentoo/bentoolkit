package cooldown_test

// Authored for bentoo story 003 (autoupdate cooldown), sub-task 1.2 —
// R1.5, R2.1, R3.3, R4.1.
//
// Contract: design.md "internal/autoupdate/cooldown" — Add (idempotent on
// Package+Version, keeping the original Since), Eligible (minimum Since over
// the package's entries and over every member of its Group, plus Days), Held
// (every masked version plus the highest version present below the oldest
// masked one) — with the refined signature
// `Held(tree func(pkg string) []string) map[string][]string`.
//
// Uses only Entry, Block, Add, Eligible and Held, so it compiles before Sweep
// and Save exist.
//
// Hostile halves, each before the benign case:
//   - identity: `cat/pkg` vs `cat/pkg-bin`, the same version in two packages,
//     the same name in two categories, and a version differing only by `-r1`
//     must all stay distinct entries (wrong collapse), while the same
//     Package+Version with a different Since/Origin/Group is ONE entry (wrong
//     split);
//   - group membership is a whole `cat/pkg` member of the comma list, so a
//     third, prefix-named package never joins a group's clock;
//   - versions compare as Gentoo PVRs (1.9 < 1.10, 1.2.3 < 1.2.3-r1,
//     1.0_pre < 1.0), never as strings, and the tree listing is unsorted.

import (
	"sort"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate/cooldown"
)

var queueT0 = time.Date(2026, 11, 7, 7, 10, 0, 0, time.UTC)

func day(n int) time.Duration { return time.Duration(n) * 24 * time.Hour }

func entry(pkg, version string, since time.Time) cooldown.Entry {
	return cooldown.Entry{Package: pkg, Version: version, Since: since, Origin: "autoupdate"}
}

func grouped(pkg, version string, since time.Time, group string) cooldown.Entry {
	e := entry(pkg, version, since)
	e.Group = group
	return e
}

func sameSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	g := append([]string(nil), got...)
	w := append([]string(nil), want...)
	sort.Strings(g)
	sort.Strings(w)
	for i := range g {
		if g[i] != w[i] {
			return false
		}
	}
	return true
}

// TestCooldownAdd_SamePackageAndVersionIsOneEntryWithTheOriginalSince: R1.5.
// Hostile half first — the same Package+Version arriving again with a later
// Since, another origin and a group is the SAME entry (wrong split), and the
// first Since survives.
func TestCooldownAdd_SamePackageAndVersionIsOneEntryWithTheOriginalSince(t *testing.T) {
	b := &cooldown.Block{Days: 7}

	if !b.Add(entry("sys-apps/foo", "1.0", queueT0)) {
		t.Fatal("the first Add of sys-apps/foo-1.0 reported false")
	}
	again := grouped("sys-apps/foo", "1.0", queueT0.Add(day(2)), "sys-apps/foo,sys-apps/foo-bin")
	again.Origin = "manual"
	if b.Add(again) {
		t.Error("re-adding sys-apps/foo-1.0 reported true; a re-apply must be a no-op")
	}
	if len(b.Entries) != 1 {
		t.Fatalf("%d entries after re-adding the same atom, want 1: %+v", len(b.Entries), b.Entries)
	}
	if got := b.Entries[0].Since; !got.Equal(queueT0) {
		t.Errorf("Since = %s after the re-add, want the original %s", got, queueT0)
	}
}

// TestCooldownAdd_DistinctAtomsNeverCollapse is the converse: near-identical
// atoms that name different things are each a new entry.
func TestCooldownAdd_DistinctAtomsNeverCollapse(t *testing.T) {
	b := &cooldown.Block{Days: 7}
	atoms := []cooldown.Entry{
		entry("sys-apps/ai-jail", "3.2.0", queueT0),
		entry("sys-apps/ai-jail-bin", "3.2.0", queueT0), // prefix-named, same version
		entry("app-misc/ai-jail", "3.2.0", queueT0),     // same name, other category
		entry("sys-apps/ai-jail", "3.2.0-r1", queueT0),  // revision only
	}
	for _, e := range atoms {
		if !b.Add(e) {
			t.Errorf("Add(%s-%s) reported false; it is a different atom", e.Package, e.Version)
		}
	}
	if len(b.Entries) != len(atoms) {
		t.Fatalf("%d entries, want %d: %+v", len(b.Entries), len(atoms), b.Entries)
	}
}

// TestCooldownAdd_KeepsEntriesSortedByPackageThenVersion: design.md "Entries
// are sorted by package, then version" — version in Gentoo order.
func TestCooldownAdd_KeepsEntriesSortedByPackageThenVersion(t *testing.T) {
	b := &cooldown.Block{Days: 7}
	for _, e := range []cooldown.Entry{
		entry("dev-util/x", "1.10", queueT0),
		entry("app-misc/z", "1.0", queueT0),
		entry("dev-util/x", "1.9-r1", queueT0),
		entry("app-misc/z-bin", "0.1", queueT0),
		entry("dev-util/x", "1.9", queueT0),
	} {
		b.Add(e)
	}
	want := []string{"app-misc/z-1.0", "app-misc/z-bin-0.1", "dev-util/x-1.9", "dev-util/x-1.9-r1", "dev-util/x-1.10"}
	var got []string
	for _, e := range b.Entries {
		got = append(got, e.Package+"-"+e.Version)
	}
	if len(got) != len(want) {
		t.Fatalf("entries = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("entries = %v, want %v (package, then Gentoo version order)", got, want)
		}
	}
}

// TestCooldownEligible_IsTheOldestSincePlusDays reads Days from the block (3
// here, not the default 7) and anchors at the OLDEST entry, whatever the
// insertion order.
func TestCooldownEligible_IsTheOldestSincePlusDays(t *testing.T) {
	b := &cooldown.Block{Days: 3}
	b.Add(entry("sys-apps/foo", "1.1", queueT0.Add(day(2))))
	b.Add(entry("sys-apps/foo", "1.0", queueT0))

	if got, want := b.Eligible("sys-apps/foo"), queueT0.Add(day(3)); !got.Equal(want) {
		t.Errorf("Eligible = %s, want %s (oldest since + days=3)", got, want)
	}
}

// TestCooldownEligible_ANewerBumpDoesNotMoveTheClock: R3.3 / D2. A package
// bumped every day is still released Days after its FIRST masked bump.
func TestCooldownEligible_ANewerBumpDoesNotMoveTheClock(t *testing.T) {
	b := &cooldown.Block{Days: 7}
	b.Add(entry("media-libs/mesa", "26.1.0_pre20261101", queueT0))
	before := b.Eligible("media-libs/mesa")
	if want := queueT0.Add(day(7)); !before.Equal(want) {
		t.Fatalf("Eligible before the newer bump = %s, want %s", before, want)
	}

	for i := 1; i <= 5; i++ {
		b.Add(entry("media-libs/mesa", "26.1.0_pre2026110"+string(rune('1'+i)), queueT0.Add(day(i))))
	}
	if after := b.Eligible("media-libs/mesa"); !after.Equal(before) {
		t.Errorf("Eligible moved from %s to %s after newer bumps; the clock is anchored at the oldest masked version", before, after)
	}
}

// TestCooldownEligible_APrefixNamedPackageKeepsItsOwnClock: identity is the
// whole `cat/pkg`. An old `-bin` entry must not drag `cat/pkg` forward, and
// vice versa.
func TestCooldownEligible_APrefixNamedPackageKeepsItsOwnClock(t *testing.T) {
	b := &cooldown.Block{Days: 7}
	b.Add(entry("sys-apps/ai-jail-bin", "3.2.0", queueT0.Add(-day(30))))
	b.Add(entry("sys-apps/ai-jail", "3.2.0", queueT0))

	if got, want := b.Eligible("sys-apps/ai-jail"), queueT0.Add(day(7)); !got.Equal(want) {
		t.Errorf("Eligible(sys-apps/ai-jail) = %s, want %s; sys-apps/ai-jail-bin's entry was borrowed", got, want)
	}
	if got, want := b.Eligible("sys-apps/ai-jail-bin"), queueT0.Add(-day(23)); !got.Equal(want) {
		t.Errorf("Eligible(sys-apps/ai-jail-bin) = %s, want %s", got, want)
	}
}

// TestCooldownEligible_AGroupOfThreeSharesTheOldestMemberClock: R4.1 with
// three members (a pair would not show a member-of-member gap), plus a
// prefix-named fourth package outside the group with an even older Since.
func TestCooldownEligible_AGroupOfThreeSharesTheOldestMemberClock(t *testing.T) {
	const g = "dev-lang/a,dev-lang/b,dev-lang/c"
	b := &cooldown.Block{Days: 7}
	b.Add(grouped("dev-lang/a", "1.0", queueT0.Add(day(2)), g))
	b.Add(grouped("dev-lang/b", "1.0", queueT0.Add(day(1)), g))
	b.Add(grouped("dev-lang/c", "1.0", queueT0, g))
	// Hostile third element: not a member, but its name starts with one.
	b.Add(entry("dev-lang/a-bin", "1.0", queueT0.Add(-day(10))))

	want := queueT0.Add(day(7))
	for _, pkg := range []string{"dev-lang/a", "dev-lang/b", "dev-lang/c"} {
		if got := b.Eligible(pkg); !got.Equal(want) {
			t.Errorf("Eligible(%s) = %s, want %s (oldest since among the group's members)", pkg, got, want)
		}
	}
	if got, want := b.Eligible("dev-lang/a-bin"), queueT0.Add(-day(3)); !got.Equal(want) {
		t.Errorf("Eligible(dev-lang/a-bin) = %s, want %s; a non-member joined or lent its clock", got, want)
	}
}

// tree returns a fake overlay listing, deliberately UNSORTED.
func tree(listing map[string][]string) func(string) []string {
	return func(pkg string) []string { return listing[pkg] }
}

// TestCooldownHeld_NeverHoldsAnUnmaskedVersionAboveTheOldestMasked: R2.1.
// Hostile half first: 2.1 sits between two masked versions and is NOT masked,
// so it is not held — and 1.0, below the predecessor, is not held either.
func TestCooldownHeld_NeverHoldsAnUnmaskedVersionAboveTheOldestMasked(t *testing.T) {
	b := &cooldown.Block{Days: 7}
	b.Add(entry("sys-apps/foo", "2.0", queueT0))
	b.Add(entry("sys-apps/foo", "2.2", queueT0))

	held := b.Held(tree(map[string][]string{"sys-apps/foo": {"2.2", "1.0", "2.1", "1.5", "2.0"}}))
	if got, want := held["sys-apps/foo"], []string{"1.5", "2.0", "2.2"}; !sameSet(got, want) {
		t.Errorf("Held[sys-apps/foo] = %v, want %v", got, want)
	}
}

// TestCooldownHeld_EveryMaskedVersionPlusThePredecessor is the benign case,
// with the predecessor found by PVR order across three version shapes.
func TestCooldownHeld_EveryMaskedVersionPlusThePredecessor(t *testing.T) {
	cases := []struct {
		name   string
		masked []string
		listed []string
		want   []string
	}{
		{"queue of two", []string{"2.0", "2.1"}, []string{"2.1", "1.0", "2.0", "1.5"}, []string{"1.5", "2.0", "2.1"}},
		{"1.9 is below 1.10", []string{"1.10"}, []string{"1.10", "1.8", "1.9"}, []string{"1.9", "1.10"}},
		{"revision is a newer PVR", []string{"1.2.3-r1"}, []string{"1.2.3-r1", "1.2.2", "1.2.3"}, []string{"1.2.3", "1.2.3-r1"}},
		{"_pre sorts before its release", []string{"1.0"}, []string{"1.0", "0.9", "1.0_pre20261101"}, []string{"1.0_pre20261101", "1.0"}},
		{"no predecessor in the tree", []string{"1.0"}, []string{"1.0"}, []string{"1.0"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := &cooldown.Block{Days: 7}
			for _, v := range c.masked {
				b.Add(entry("sys-apps/foo", v, queueT0))
			}
			held := b.Held(tree(map[string][]string{"sys-apps/foo": c.listed}))
			if got := held["sys-apps/foo"]; !sameSet(got, c.want) {
				t.Errorf("Held[sys-apps/foo] = %v, want %v", got, c.want)
			}
		})
	}
}

// TestCooldownHeld_PackagesNeverBorrowEachOthersEntries: identity again, on
// the held set. If sys-apps/foo-bin's masked 1.0 were attributed to
// sys-apps/foo, foo's oldest masked version would become 1.0 and its
// predecessor 1.5 would be dropped. Two packages sharing a version stay
// independent, and a package without entries is not in the map at all.
func TestCooldownHeld_PackagesNeverBorrowEachOthersEntries(t *testing.T) {
	b := &cooldown.Block{Days: 7}
	b.Add(entry("sys-apps/foo", "2.0", queueT0))
	b.Add(entry("sys-apps/foo-bin", "1.0", queueT0))
	b.Add(entry("dev-libs/a", "1.0", queueT0))
	b.Add(entry("dev-libs/b", "1.0", queueT0))

	held := b.Held(tree(map[string][]string{
		"sys-apps/foo":     {"2.0", "1.0", "1.5"},
		"sys-apps/foo-bin": {"1.0", "0.9"},
		"dev-libs/a":       {"1.0", "0.5"},
		"dev-libs/b":       {"0.7", "1.0"},
		"dev-libs/c":       {"1.0", "0.1"},
	}))
	want := map[string][]string{
		"sys-apps/foo":     {"1.5", "2.0"},
		"sys-apps/foo-bin": {"0.9", "1.0"},
		"dev-libs/a":       {"0.5", "1.0"},
		"dev-libs/b":       {"0.7", "1.0"},
	}
	for pkg, w := range want {
		if got := held[pkg]; !sameSet(got, w) {
			t.Errorf("Held[%s] = %v, want %v", pkg, got, w)
		}
	}
	if got, ok := held["dev-libs/c"]; ok {
		t.Errorf("Held[dev-libs/c] = %v; a package with no entry holds nothing", got)
	}
}
