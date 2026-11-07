package cooldown

import (
	"slices"
	"strings"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/ebuild"
)

// sortEntries orders entries by package, then Gentoo version.
func sortEntries(es []Entry) {
	slices.SortStableFunc(es, func(a, b Entry) int {
		if c := strings.Compare(a.Package, b.Package); c != 0 {
			return c
		}
		return ebuild.CompareVersions(a.Version, b.Version)
	})
}

// Add masks e. It is a no-op returning false when the same Package+Version is
// already present, so a re-apply keeps the original Since. Since is stored in
// UTC, truncated to the second the block format records.
func (b *Block) Add(e Entry) (added bool) {
	for _, have := range b.Entries {
		if have.Package == e.Package && have.Version == e.Version {
			return false
		}
	}
	e.Since = e.Since.UTC().Truncate(time.Second)
	b.Entries = append(b.Entries, e)
	sortEntries(b.Entries)
	return true
}

// groupHas reports whether the comma-joined group lists pkg as a whole member.
func groupHas(group, pkg string) bool {
	if group == "" {
		return false
	}
	return slices.Contains(strings.Split(group, ","), pkg)
}

// clockEntries returns the entries that set pkg's release clock: those of
// every package released together with it (members), its own included.
func (b *Block) clockEntries(pkg string) []Entry {
	members := b.members(pkg)
	var out []Entry
	for _, e := range b.Entries {
		if slices.Contains(members, e.Package) {
			out = append(out, e)
		}
	}
	return out
}

// Eligible returns when pkg is released by time: the oldest Since over the
// package's entries — and over every member of its group — plus Days. It is
// the zero time for a package with no entry.
func (b *Block) Eligible(pkg string) time.Time {
	return b.eligibleAt(pkg, time.Time{})
}

// eligibleAt is Eligible with every Since after now read as now (clock skew);
// a zero now applies no clamp.
func (b *Block) eligibleAt(pkg string, now time.Time) time.Time {
	var oldest time.Time
	for _, e := range b.clockEntries(pkg) {
		since := e.Since
		if !now.IsZero() && since.After(now) {
			since = now
		}
		if oldest.IsZero() || since.Before(oldest) {
			oldest = since
		}
	}
	if oldest.IsZero() {
		return oldest
	}
	return oldest.AddDate(0, 0, b.Days)
}

// Future returns the entries whose Since lies after now. Sweep reads such a
// Since as now; callers log one Warn per entry.
func (b *Block) Future(now time.Time) []Entry {
	var out []Entry
	for _, e := range b.Entries {
		if e.Since.After(now) {
			out = append(out, e)
		}
	}
	return out
}

// Held returns, per package with an entry, the versions that must stay in
// the overlay tree: every masked version, plus the highest version tree lists
// below the oldest masked one — the installable predecessor. An unmasked
// version above the oldest masked one is never held. tree lists the versions
// present for a package, in any order.
func (b *Block) Held(tree func(pkg string) []string) map[string][]string {
	masked := map[string][]string{}
	for _, e := range b.Entries {
		masked[e.Package] = append(masked[e.Package], e.Version)
	}
	held := make(map[string][]string, len(masked))
	for pkg, vers := range masked {
		oldest := slices.MinFunc(vers, ebuild.CompareVersions)
		pred := ""
		for _, v := range tree(pkg) {
			if slices.Contains(vers, v) || ebuild.CompareVersions(v, oldest) >= 0 {
				continue
			}
			if pred == "" || ebuild.CompareVersions(v, pred) > 0 {
				pred = v
			}
		}
		out := slices.Clone(vers)
		if pred != "" {
			out = append(out, pred)
		}
		slices.SortFunc(out, ebuild.CompareVersions)
		held[pkg] = out
	}
	return held
}
