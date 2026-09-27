package ebuild

import (
	"regexp"
	"strings"
)

// suffixRank orders the version suffix types (PMS algorithm 3.6):
// _alpha < _beta < _pre < _rc < _p.
var suffixRank = map[string]int{
	"alpha": 0,
	"beta":  1,
	"pre":   2,
	"rc":    3,
	"p":     4,
}

// versionSuffix is one _type[number] suffix. num is the digit string as
// written, possibly empty.
type versionSuffix struct {
	kind string
	num  string
}

// parsedVersion is a valid version split into the parts PMS section 3.3
// compares. Every number is kept as its digit string, because a component of
// an ebuild version (a date stamp, a 9999 chain) can exceed any integer type.
type parsedVersion struct {
	components []string
	letter     byte // 0 when the version carries no letter
	suffixes   []versionSuffix
	revision   string // digit string, empty when there is no -r
}

// parseVersion splits v, which must already have passed IsValidVersion in its
// trimmed form, into its PMS parts. It relies on validVersionRegex: a '-' only
// ever opens the revision, a '_' only ever opens a suffix, and the head is
// dot-separated digits with at most one trailing lowercase letter.
func parseVersion(v string) parsedVersion {
	var p parsedVersion

	head, revision, _ := strings.Cut(v, "-r")
	p.revision = revision

	parts := strings.Split(head, "_")
	for _, s := range parts[1:] {
		kind := strings.TrimRight(s, "0123456789")
		p.suffixes = append(p.suffixes, versionSuffix{kind: kind, num: s[len(kind):]})
	}

	numeric := parts[0]
	if last := numeric[len(numeric)-1]; last >= 'a' && last <= 'z' {
		p.letter = last
		numeric = numeric[:len(numeric)-1]
	}
	p.components = strings.Split(numeric, ".")
	return p
}

// compareDigits orders two digit strings by the unbounded integers they
// spell: leading zeros are ignored, the longer remainder is the greater, and
// remainders of equal length compare byte-wise. An empty string is 0.
func compareDigits(a, b string) int {
	a = strings.TrimLeft(a, "0")
	b = strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		if len(a) < len(b) {
			return -1
		}
		return 1
	}
	return strings.Compare(a, b)
}

// compareComponent orders a non-first numeric component (PMS algorithm 3.3):
// when either side begins with 0 both are compared as strings with trailing
// zeros removed, so 1.01 < 1.1 while 1.010 == 1.01; otherwise as integers.
func compareComponent(a, b string) int {
	if strings.HasPrefix(a, "0") || strings.HasPrefix(b, "0") {
		return strings.Compare(strings.TrimRight(a, "0"), strings.TrimRight(b, "0"))
	}
	return compareDigits(a, b)
}

// compareSuffixLists orders two suffix lists (PMS algorithms 3.5 and 3.6):
// pairwise by type rank and then by number, and when one list is a prefix of
// the other, the longer is greater only if its next suffix is _p.
func compareSuffixLists(a, b []versionSuffix) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if ra, rb := suffixRank[a[i].kind], suffixRank[b[i].kind]; ra != rb {
			if ra < rb {
				return -1
			}
			return 1
		}
		if c := compareDigits(a[i].num, b[i].num); c != 0 {
			return c
		}
	}
	switch {
	case len(a) > len(b):
		if a[len(b)].kind == "p" {
			return 1
		}
		return -1
	case len(b) > len(a):
		if b[len(a)].kind == "p" {
			return -1
		}
		return 1
	}
	return 0
}

// comparePMS orders two parsed valid versions by PMS algorithms 3.2 to 3.7.
func comparePMS(a, b parsedVersion) int {
	if c := compareDigits(a.components[0], b.components[0]); c != 0 {
		return c
	}
	for i := 1; i < len(a.components) && i < len(b.components); i++ {
		if c := compareComponent(a.components[i], b.components[i]); c != 0 {
			return c
		}
	}
	if len(a.components) != len(b.components) {
		if len(a.components) < len(b.components) {
			return -1
		}
		return 1
	}
	if a.letter != b.letter {
		if a.letter < b.letter {
			return -1
		}
		return 1
	}
	if c := compareSuffixLists(a.suffixes, b.suffixes); c != 0 {
		return c
	}
	return compareDigits(a.revision, b.revision)
}

// validVersionRegex matches a well-formed Gentoo-style version: a numeric
// component chain (1.2.3), an optional single trailing letter (1.2.3a), zero or
// more recognized suffixes (_alpha/_beta/_pre/_rc/_p with an optional number),
// and an optional revision (-r1).
var validVersionRegex = regexp.MustCompile(`^[0-9]+(\.[0-9]+)*[a-z]?(_(alpha|beta|pre|rc|p)[0-9]*)*(-r[0-9]+)?$`)

// IsValidVersion reports whether v, once surrounding whitespace is trimmed, is
// a well-formed Gentoo-style version string that CompareVersions orders by PMS.
//
// A string it rejects — "INKSCAPE_1_4_4", "latest", a still-prefixed
// "v6.6.91" — is not an error to CompareVersions: it orders below every valid
// version. That is a deterministic answer, not a meaningful one, so callers that
// compare an upstream value against the current ebuild version should reject
// non-comparable inputs up front rather than treat them as "no update
// available".
func IsValidVersion(v string) bool {
	return validVersionRegex.MatchString(strings.TrimSpace(v))
}

// CompareVersions compares two Gentoo-style version strings by the ordering
// of PMS section 3.3 and returns -1 if v1 < v2, 0 if v1 == v2, 1 if v1 > v2.
//
// Every number is compared as an unbounded integer, so no component, suffix
// number or revision can overflow. PMS equality is not text identity: 1.0 and
// 1.0-r0 compare equal, and so do 1.010 and 1.01.
//
// The comparison is total on any two strings. A valid version is compared in
// its trimmed form, the form IsValidVersion accepts. A string IsValidVersion
// rejects orders below every valid version, and two invalid strings order
// byte-wise on their untrimmed text, so two distinct invalid strings never
// compare equal.
func CompareVersions(v1, v2 string) int {
	valid1, valid2 := IsValidVersion(v1), IsValidVersion(v2)
	switch {
	case !valid1 && !valid2:
		return strings.Compare(v1, v2)
	case !valid1:
		return -1
	case !valid2:
		return 1
	}
	return comparePMS(parseVersion(strings.TrimSpace(v1)), parseVersion(strings.TrimSpace(v2)))
}
