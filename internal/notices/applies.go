package notices

import (
	"slices"
	"strings"

	"github.com/obentoo/bentoolkit/internal/common/ebuild"
	"github.com/obentoo/bentoolkit/internal/gentoo/pkgdb"
)

// bentooRepo is the repository a package must be installed from for an
// affects entry to match it.
const bentooRepo = "bentoo"

// Applies reports whether n concerns this system: a news item
// always applies, since portage already filtered it; a notice with no affects
// entry applies everywhere; otherwise at least one entry must match a package
// installed from the bentoo repository.
func Applies(n Notice, installed []pkgdb.Package) bool {
	if n.Source == SourceNews || len(n.Affects) == 0 {
		return true
	}
	for _, a := range n.Affects {
		for _, p := range installed {
			if matches(a, p) {
				return true
			}
		}
	}
	return false
}

// matches reports whether one affects entry matches one installed package:
// same cp, same slot when the entry names one, installed from bentoo, and
// every range satisfied under PMS ordering.
func matches(a Affects, p pkgdb.Package) bool {
	if p.Repo != bentooRepo || a.CP != p.Category+"/"+p.Name {
		return false
	}
	slot, _, _ := strings.Cut(p.Slot, "/")
	if a.Slot != "" && a.Slot != slot {
		return false
	}
	for _, r := range a.Ranges {
		if !satisfies(ebuild.CompareVersions(p.Version, r.Ver), r.Op) {
			return false
		}
	}
	return true
}

// satisfies reports whether a comparison result cmp (installed vs bound)
// meets op. An unknown operator never matches; ParseFeed rejects them anyway.
func satisfies(cmp int, op string) bool {
	switch op {
	case "<":
		return cmp < 0
	case "<=":
		return cmp <= 0
	case "=":
		return cmp == 0
	case ">=":
		return cmp >= 0
	case ">":
		return cmp > 0
	}
	return false
}

// Merge unites the feed and news notices into one notice per ID, sorted by ID.
// When both sources carry an ID, the feed item wins; a repeated
// news ID yields one notice.
func Merge(feed []Notice, news []Notice) []Notice {
	byID := make(map[string]Notice, len(feed)+len(news))
	for _, n := range news {
		byID[n.ID] = n
	}
	for _, n := range feed {
		byID[n.ID] = n
	}
	out := make([]Notice, 0, len(byID))
	for _, n := range byID {
		out = append(out, n)
	}
	slices.SortFunc(out, func(a, b Notice) int { return strings.Compare(a.ID, b.ID) })
	return out
}
