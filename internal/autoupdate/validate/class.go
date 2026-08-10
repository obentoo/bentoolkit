package validate

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/obentoo/bentoolkit/internal/common/ebuild"
)

// Class is how far a version bump moved, and therefore how much validation the
// bump earns.
//
// The four rungs are ordered by DEPTH, cheapest first, so that a policy can say
// "at least ClassSeries" with a plain comparison and so that ClassMajor — the
// class every uncertain case falls back to — is the greatest. Do not reorder
// them: the ordering is the ladder, not a labelling.
type Class int

const (
	// ClassRevision is a bump where nothing upstream moved: only the Portage
	// revision suffix (-rN) differs, or the two versions are identical
	// (a revive, a retry). The tarball is the same one already validated.
	ClassRevision Class = iota

	// ClassPatch is a bump below the series component: the upstream release
	// changed but the series it belongs to did not.
	ClassPatch

	// ClassSeries is a crossing into another upstream series, the component
	// below the leading one. Build options and dependencies routinely change
	// here even though the leading component did not move.
	ClassSeries

	// ClassMajor is a move in the leading component — and the class every
	// version this package cannot read falls back to, because the greatest
	// depth is the only safe answer to "I do not know how far this moved".
	ClassMajor
)

// String makes the class readable in logs and test failures. Without it a
// classification prints as a bare integer and every reader has to go count the
// constants.
func (c Class) String() string {
	switch c {
	case ClassRevision:
		return "revision"
	case ClassPatch:
		return "patch"
	case ClassSeries:
		return "series"
	case ClassMajor:
		return "major"
	default:
		return "Class(" + strconv.Itoa(int(c)) + ")"
	}
}

// ErrUnreadableVersion reports that a version string could not be read as a
// Gentoo version, so the size of the move could not be measured.
//
// It is a sentinel so a caller can tell "this bump really is major" apart from
// "we could not tell, so we charged the deepest class" — the two produce the
// same Class on purpose, and only the error distinguishes them.
var ErrUnreadableVersion = errors.New("version cannot be read as a Gentoo version")

// Classify reports how far a bump moved, from the two version strings and
// nothing else (R1, R1.6). It performs no filesystem, network, clock or
// subprocess access — the classification decides how much machine time a bump
// is about to cost, so determining it must itself cost nothing and must give
// the same answer on every machine, forever.
//
// The classes follow the first component that differs, once the Portage
// revision suffix is set aside:
//
//	leading component      -> ClassMajor    (R1.4)
//	the one below it       -> ClassSeries   (R1.3)
//	anything deeper        -> ClassPatch    (R1.2)
//	only the -rN suffix    -> ClassRevision (R1.1)
//
// The ladder has four rungs and a version chain may have more than three
// components, so everything below the series collapses into ClassPatch. That is
// the deliberate reading of R1.2: a move that leaves the leading component and
// the series intact is a patch-level move regardless of how deep it lands.
//
// The answer is symmetric — Classify(a, b) equals Classify(b, a) — because it
// measures the DISTANCE between two versions, not the direction of travel.
// Refusing a downgrade is a separate decision, made by the caller that applies
// the bump; a classifier that disagreed with itself depending on argument order
// would make that decision unreproducible.
//
// If either version cannot be read, Classify returns ClassMajor AND an error
// naming the string it could not read (R1.5). Both, never one: the class alone
// would validate the bump at maximum depth for a reason nobody could see, and
// the error alone would leave the caller with no class to act on.
func Classify(oldPV, newPV string) (Class, error) {
	oldTrimmed := strings.TrimSpace(oldPV)
	newTrimmed := strings.TrimSpace(newPV)

	// IsValidVersion is the gate, and it is borrowed rather than rebuilt:
	// internal/common/ebuild already owns what counts as a comparable Gentoo
	// version, and a second opinion living here would drift from the first.
	var unreadable []string
	if !ebuild.IsValidVersion(oldTrimmed) {
		unreadable = append(unreadable, strconv.Quote(oldPV))
	}
	if !ebuild.IsValidVersion(newTrimmed) {
		unreadable = append(unreadable, strconv.Quote(newPV))
	}
	if len(unreadable) > 0 {
		return ClassMajor, fmt.Errorf("%w: %s; the bump is classified as major so it is validated at the greatest depth",
			ErrUnreadableVersion, strings.Join(unreadable, " and "))
	}

	moved := firstDifference(upstreamChain(oldTrimmed), upstreamChain(newTrimmed))
	return classAt(moved), nil
}

// classAt maps the index of the component that moved onto the ladder. It is
// written out separately because this mapping IS the requirement — reading it
// next to the chain arithmetic hid which line answered which rule.
func classAt(diff int) Class {
	switch diff {
	case -1:
		// The upstream part is identical, so whatever changed was the
		// revision suffix — or nothing at all (R1.1).
		return ClassRevision
	case 0:
		return ClassMajor // the leading component (R1.4)
	case 1:
		return ClassSeries // the component below it (R1.3)
	default:
		return ClassPatch // anything deeper (R1.2)
	}
}

// component is one dot-separated piece of a version, split into the number that
// orders it and whatever trails it.
//
// The split exists so that "1.28.10" is deeper than "1.28.9" rather than
// alphabetically before it, and so that a letter release ("1.2.3a") and a
// prerelease ("1.2.3_rc1") stay distinct from the plain release they hang off
// instead of collapsing onto it.
type component struct {
	num   int
	extra string
}

// upstreamChain splits everything of a version except its Portage revision into
// components. The revision is dropped because it is exactly what R1.1 says does
// not count as upstream movement.
//
// The version has already passed ebuild.IsValidVersion, so the shape is known:
// digits separated by dots, an optional trailing letter, and optional _alpha /
// _beta / _pre / _rc / _p suffixes. Anything that is not a leading digit run
// therefore rides along in extra, attached to the component it was written on.
func upstreamChain(v string) []component {
	if i := strings.LastIndex(v, "-r"); i >= 0 {
		v = v[:i]
	}

	pieces := strings.Split(v, ".")
	chain := make([]component, len(pieces))
	for i, p := range pieces {
		digits := 0
		for digits < len(p) && p[digits] >= '0' && p[digits] <= '9' {
			digits++
		}
		// Atoi cannot fail on a pure digit run, but an absurdly long one
		// can overflow; falling back to the raw text keeps such a
		// component distinct from every other instead of silently
		// becoming zero.
		n, err := strconv.Atoi(p[:digits])
		if err != nil {
			chain[i] = component{extra: p}
			continue
		}
		chain[i] = component{num: n, extra: p[digits:]}
	}
	return chain
}

// firstDifference returns the index of the first component where the two chains
// disagree, or -1 when they are the same version upstream.
//
// A chain that ends early differs from one that continues: "1.28" and "1.28.0"
// are two different ebuilds pointing at two different tarballs, and treating
// them as the same version would hand the second one the cheapest validation on
// the strength of a typographic convention.
func firstDifference(a, b []component) int {
	n := max(len(a), len(b))
	for i := range n {
		switch {
		case i >= len(a) || i >= len(b):
			return i
		case a[i] != b[i]:
			return i
		}
	}
	return -1
}
