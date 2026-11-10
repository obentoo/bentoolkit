package cooldown

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/ebuild"
	"github.com/obentoo/bentoolkit/internal/gentoo/pkgdb"
)

// Lift reasons, in the order Sweep applies its rules.
const (
	ReasonStale     = "stale"
	ReasonManual    = "manual"
	ReasonInstalled = "installed"
	ReasonElapsed   = "elapsed"
)

// Repo is the repository whose installed versions release an entry.
const Repo = "bentoo"

// Lift is one entry leaving the block.
type Lift struct {
	Package, Version string
	// Reason is ReasonElapsed, ReasonInstalled, ReasonManual or ReasonStale.
	Reason string
}

// Atom returns the lifted "=cat/pkg-PVR".
func (l Lift) Atom() string { return "=" + l.Package + "-" + l.Version }

// Confirm is one manual confirmation, parsed from "=cat/pkg-N".
type Confirm struct {
	Package, Version string
}

// ParseConfirm reads "=cat/pkg-N".
func ParseConfirm(atom string) (Confirm, error) {
	pkg, ver, err := ParseAtom(atom)
	if err != nil {
		return Confirm{}, err
	}
	return Confirm{Package: pkg, Version: ver}, nil
}

// Installed answers which version of a package is installed from a repository.
type Installed interface {
	HighestFromRepo(pkg, repo string) (version string, ok bool)
}

// ErrPkgdbUnreadable is wrapped by NewPkgdbInstalled when the installed
// package database cannot be read. Callers warn once and sweep with a nil
// Installed, which skips installation-based lifting only.
var ErrPkgdbUnreadable = errors.New("installed-package database unreadable")

type pkgdbInstalled map[string]map[string]string // repo -> cat/pkg -> highest version

func (p pkgdbInstalled) HighestFromRepo(pkg, repo string) (string, bool) {
	v, ok := p[repo][pkg]
	return v, ok
}

// NewPkgdbInstalled is NewPkgdbInstalledContext without a deadline.
func NewPkgdbInstalled(root string) (Installed, error) {
	return NewPkgdbInstalledContext(context.Background(), root) // SAFE: deadline-free convenience wrapper; production calls NewPkgdbInstalledContext
}

// NewPkgdbInstalledContext reads <root>/var/db/pkg once and answers from that
// snapshot; ctx bounds the read. A package with no repository record answers
// for no repository.
func NewPkgdbInstalledContext(ctx context.Context, root string) (Installed, error) {
	dir := filepath.Join(root, strings.TrimPrefix(pkgdb.DefaultRoot, "/"))
	pkgs, _, err := pkgdb.Read(ctx, dir)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrPkgdbUnreadable, err)
	}
	out := pkgdbInstalled{}
	for _, p := range pkgs {
		if p.Repo == "" {
			continue
		}
		if out[p.Repo] == nil {
			out[p.Repo] = map[string]string{}
		}
		key := p.Category + "/" + p.Name
		if have, ok := out[p.Repo][key]; !ok || ebuild.CompareVersions(p.Version, have) > 0 {
			out[p.Repo][key] = p.Version
		}
	}
	return out, nil
}

// members returns the packages released together with pkg: the transitive
// union of every group that lists it, or pkg alone. Entries queued before a
// cooldown_with change keep the old group string; the union keeps such a
// group whole instead of letting one member go alone.
func (b *Block) members(pkg string) []string {
	set := map[string]bool{pkg: true}
	for grew := true; grew; {
		grew = false
		for _, e := range b.Entries {
			if e.Group == "" || !set[e.Package] && !anyMember(e.Group, set) {
				continue
			}
			for _, m := range strings.Split(e.Group, ",") {
				if !set[m] {
					set[m], grew = true, true
				}
			}
		}
	}
	out := make([]string, 0, len(set))
	for m := range set {
		out = append(out, m)
	}
	slices.Sort(out)
	return out
}

func anyMember(group string, set map[string]bool) bool {
	for _, m := range strings.Split(group, ",") {
		if set[m] {
			return true
		}
	}
	return false
}

func isMember(members []string, pkg string) bool {
	for _, m := range members {
		if m == pkg {
			return true
		}
	}
	return false
}

// liftSet records one reason per entry index; the first rule to claim an
// entry names it.
type liftSet map[int]string

func (s liftSet) claim(i int, reason string) {
	if _, done := s[i]; !done {
		s[i] = reason
	}
}

// manualRule lifts, for each confirm =pkg-N, every entry of the package at
// version N or older; for a grouped package, every entry of every member.
func (b *Block) manualRule(s liftSet, confirms []Confirm) {
	for _, c := range confirms {
		members := b.members(c.Package)
		grouped := len(members) > 1
		for i, e := range b.Entries {
			switch {
			case grouped && isMember(members, e.Package):
				s.claim(i, ReasonManual)
			case !grouped && e.Package == c.Package && ebuild.CompareVersions(e.Version, c.Version) <= 0:
				s.claim(i, ReasonManual)
			}
		}
	}
}

// Sweep decides every lift and removes the lifted entries from b, which it
// returns as rest (keeping the load-time hash Save checks). Each entry is
// lifted once, by the first rule that applies:
//
//   - stale: exists(pkg, ver) reports the ebuild gone;
//   - manual: a confirm of =pkg-N lifts the package's entries <= N, and a
//     grouped package's confirm lifts every entry of every member;
//   - installed: version V installed from ::bentoo lifts the entries <= V; a
//     group lifts only when every member is installed from ::bentoo — at or
//     above each of its own masked versions — and then lifts whole;
//   - elapsed: now >= Eligible lifts the package — or group — whole.
//
// A nil inst skips the installed rule only. A Since after now counts as now.
func Sweep(b *Block, now time.Time, inst Installed, confirms []Confirm, exists func(pkg, ver string) bool) (lifts []Lift, rest *Block) {
	s := liftSet{}
	for i, e := range b.Entries {
		if !exists(e.Package, e.Version) {
			s.claim(i, ReasonStale)
		}
	}
	b.manualRule(s, confirms)
	if inst != nil {
		b.installedRule(s, inst)
	}
	for i, e := range b.Entries {
		if due := b.dueAt(e, now); !now.Before(due) {
			s.claim(i, ReasonElapsed)
		}
	}
	return b.apply(s), b
}

// installedRule implements Sweep's installed rule.
func (b *Block) installedRule(s liftSet, inst Installed) {
	groupOK := map[string]bool{}
	for i, e := range b.Entries {
		members := b.members(e.Package)
		if len(members) == 1 {
			if v, ok := inst.HighestFromRepo(e.Package, Repo); ok && ebuild.CompareVersions(e.Version, v) <= 0 {
				s.claim(i, ReasonInstalled)
			}
			continue
		}
		key := strings.Join(members, ",")
		ok, seen := groupOK[key]
		if !seen {
			ok = b.groupInstalled(members, inst)
			groupOK[key] = ok
		}
		if ok {
			s.claim(i, ReasonInstalled)
		}
	}
}

// groupInstalled reports whether every member is installed from ::bentoo
// (R4.3: a member with no entry left still has to be), each at or above
// every one of its masked versions.
func (b *Block) groupInstalled(members []string, inst Installed) bool {
	for _, m := range members {
		if _, ok := inst.HighestFromRepo(m, Repo); !ok {
			return false
		}
	}
	for _, e := range b.Entries {
		if !isMember(members, e.Package) {
			continue
		}
		v, ok := inst.HighestFromRepo(e.Package, Repo)
		if !ok || ebuild.CompareVersions(e.Version, v) > 0 {
			return false
		}
	}
	return true
}

// apply removes the claimed entries and returns their lifts in entry order.
func (b *Block) apply(s liftSet) []Lift {
	var lifts []Lift
	kept := b.Entries[:0:0]
	for i, e := range b.Entries {
		if reason, ok := s[i]; ok {
			lifts = append(lifts, Lift{Package: e.Package, Version: e.Version, Reason: reason})
			continue
		}
		kept = append(kept, e)
	}
	b.Entries = kept
	return lifts
}

// ConfirmLift applies only the manual rule, for `bentoo overlay cooldown
// confirm`. Every confirmed atom must be in the block: the first that is not
// is an error naming it, and nothing is lifted.
func ConfirmLift(b *Block, confirms []Confirm) ([]Lift, error) {
	for _, c := range confirms {
		found := false
		for _, e := range b.Entries {
			if e.Package == c.Package && e.Version == c.Version {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("=%s-%s is not in the cooldown block", c.Package, c.Version)
		}
	}
	s := liftSet{}
	b.manualRule(s, confirms)
	return b.apply(s), nil
}
