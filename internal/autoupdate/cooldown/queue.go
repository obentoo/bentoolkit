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
// already present, so a re-apply keeps the original Since. Since and First are
// stored in UTC, truncated to the second the block format records. Add never
// supersedes anything; Replace is the line-aware form.
func (b *Block) Add(e Entry) (added bool) {
	for _, have := range b.Entries {
		if have.Package == e.Package && have.Version == e.Version {
			return false
		}
	}
	e.Since = e.Since.UTC().Truncate(time.Second)
	if !e.First.IsZero() {
		e.First = e.First.UTC().Truncate(time.Second)
	}
	b.Entries = append(b.Entries, e)
	sortEntries(b.Entries)
	return true
}

// Replace masks e as the one candidate of its release line (e.Line): every
// other entry of the same package and line is removed and returned as
// superseded, so a line never accumulates masked versions. The new candidate
// starts its own clock (Since) but inherits the line's First — the earliest
// of the superseded entries' — so MaxWaitFactor still bounds how long the
// line stays masked when upstream releases faster than the cooldown.
//
// An entry with no Line is plain Add. Re-adding the version already masked is
// a no-op (added false, nothing superseded) that keeps its Since.
func (b *Block) Replace(e Entry) (added bool, superseded []Entry) {
	if e.Line == "" {
		return b.Add(e), nil
	}
	for _, have := range b.Entries {
		if have.Package == e.Package && have.Version == e.Version {
			return false, nil
		}
	}
	first := e.First
	kept := b.Entries[:0:0]
	for _, have := range b.Entries {
		if have.Package == e.Package && have.Line == e.Line {
			superseded = append(superseded, have)
			if hf := have.lineStart(); first.IsZero() || hf.Before(first) {
				first = hf
			}
			continue
		}
		kept = append(kept, have)
	}
	b.Entries = kept
	e.First = first
	if !e.First.IsZero() && e.First.After(e.Since) {
		e.First = e.Since
	}
	return b.Add(e), superseded
}

// LineEntries returns the entries of pkg's release line (Line == line), in
// block order. An empty line matches nothing: an entry with no line belongs
// to no line.
func (b *Block) LineEntries(pkg, line string) []Entry {
	if line == "" {
		return nil
	}
	var out []Entry
	for _, e := range b.Entries {
		if e.Package == pkg && e.Line == line {
			out = append(out, e)
		}
	}
	return out
}

// LineStart returns the earliest lineStart over es, or the zero time for none:
// the First a new candidate of the line inherits.
func LineStart(es []Entry) time.Time {
	var first time.Time
	for _, e := range es {
		if s := e.lineStart(); first.IsZero() || s.Before(first) {
			first = s
		}
	}
	return first
}

// Remove drops the entry for pkg-version and reports whether there was one.
func (b *Block) Remove(pkg, version string) bool {
	for i, e := range b.Entries {
		if e.Package == pkg && e.Version == version {
			b.Entries = append(b.Entries[:i:i], b.Entries[i+1:]...)
			return true
		}
	}
	return false
}

// lineStart is when the entry's line started waiting: First, or Since.
func (e Entry) lineStart() time.Time {
	if e.First.IsZero() {
		return e.Since
	}
	return e.First
}

// days is the entry's release delay: its own override, or the block's.
func (b *Block) days(e Entry) int {
	if e.Days > 0 {
		return e.Days
	}
	return b.Days
}

// entryDue is when e alone is released by time: its own Since plus its days
// (the clock restarts with every replacement), but never later than the
// line's First plus MaxWaitFactor times its days. A Since or First after now
// is read as now (clock skew); a zero now applies no clamp.
func (b *Block) entryDue(e Entry, now time.Time) time.Time {
	clamp := func(t time.Time) time.Time {
		if !now.IsZero() && t.After(now) {
			return now
		}
		return t
	}
	d := b.days(e)
	due := clamp(e.Since).AddDate(0, 0, d)
	if limit := clamp(e.lineStart()).AddDate(0, 0, MaxWaitFactor*d); limit.Before(due) {
		due = limit
	}
	return due
}

// groupHas reports whether the comma-joined group lists pkg as a whole member.
func groupHas(group, pkg string) bool {
	if group == "" {
		return false
	}
	return slices.Contains(strings.Split(group, ","), pkg)
}

// clockEntries returns the entries released together with e: those of every
// package in its group (members), its own included.
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

// EntryEligible returns when e is released by time. A grouped entry waits for
// the whole group: the latest due of its members' entries, so a pair that
// cannot be installed apart lifts together.
func (b *Block) EntryEligible(e Entry) time.Time {
	return b.dueAt(e, time.Time{})
}

// dueAt is EntryEligible with the clock-skew clamp of entryDue.
func (b *Block) dueAt(e Entry, now time.Time) time.Time {
	if e.Group == "" && len(b.members(e.Package)) <= 1 {
		return b.entryDue(e, now)
	}
	var due time.Time
	for _, m := range b.clockEntries(e.Package) {
		if d := b.entryDue(m, now); d.After(due) {
			due = d
		}
	}
	return due
}

// Eligible returns the next time any entry of pkg is released by time: the
// earliest EntryEligible over its entries. It is the zero time for a package
// with no entry.
func (b *Block) Eligible(pkg string) time.Time {
	var next time.Time
	for _, e := range b.Entries {
		if e.Package != pkg {
			continue
		}
		if d := b.EntryEligible(e); next.IsZero() || d.Before(next) {
			next = d
		}
	}
	return next
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
