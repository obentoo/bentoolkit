// Package keywords reads and rewrites the KEYWORDS assignment of an ebuild.
//
// It is shared by the autoupdate applier, which converts a mirror bumped past
// ::gentoo to the overlay's ~amd64 baseline, and by overlay compare, which
// flags copies already in that state; the two packages import neither each
// other, so the one reading of "the KEYWORDS line" lives here.
//
// Only a plain single-line double-quoted value is ever rewritten. A multi-line,
// computed or repeated assignment is reported as not single-line: editing it
// would mean guessing which branch of the ebuild a value belongs to.
package keywords

import (
	"regexp"
)

// assignStartRe finds an uncommented KEYWORDS assignment at the start of a
// line; MY_KEYWORDS= and #KEYWORDS= do not match.
var assignStartRe = regexp.MustCompile(`(?m)^([ \t]*)KEYWORDS=(.*)$`)

// singleLineValueRe is the whole rest of a line holding a complete literal
// value: no expansion, no escape, an optional trailing comment.
var singleLineValueRe = regexp.MustCompile(`^"([^"$\x60\\]*)"[ \t]*(#.*)?$`)

// Read returns the value of the ebuild's KEYWORDS assignment. present is false
// when there is no assignment; singleLine is false when the assignment is
// multi-line, computed, or appears more than once.
func Read(ebuild []byte) (value string, singleLine, present bool) {
	loc, ok, present := singleAssignment(ebuild)
	if !ok {
		return "", false, present
	}
	return string(ebuild[loc.valueStart:loc.valueEnd]), true, true
}

// Rewrite replaces the value of a single-line KEYWORDS assignment with value,
// keeping indentation and any trailing comment. changed is false, and the
// input is returned unchanged, when there is no single-line assignment or it
// already holds value.
func Rewrite(ebuild []byte, value string) ([]byte, bool) {
	loc, ok, _ := singleAssignment(ebuild)
	if !ok || string(ebuild[loc.valueStart:loc.valueEnd]) == value {
		return ebuild, false
	}
	out := make([]byte, 0, len(ebuild)+len(value))
	out = append(out, ebuild[:loc.valueStart]...)
	out = append(out, value...)
	out = append(out, ebuild[loc.valueEnd:]...)
	return out, true
}

// valueSpan is the byte range of the quoted value of the one assignment.
type valueSpan struct {
	valueStart, valueEnd int
}

// singleAssignment locates the value of the ebuild's only KEYWORDS assignment
// when it is a single-line literal. present reports whether any assignment
// exists at all.
func singleAssignment(ebuild []byte) (span valueSpan, ok, present bool) {
	matches := assignStartRe.FindAllSubmatchIndex(ebuild, -1)
	switch len(matches) {
	case 0:
		return valueSpan{}, false, false
	case 1:
	default:
		return valueSpan{}, false, true
	}
	m := matches[0]
	rest := ebuild[m[4]:m[5]]
	sub := singleLineValueRe.FindSubmatchIndex(rest)
	if sub == nil {
		return valueSpan{}, false, true
	}
	return valueSpan{valueStart: m[4] + sub[2], valueEnd: m[4] + sub[3]}, true, true
}
