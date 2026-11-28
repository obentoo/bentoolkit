package validate

import "strings"

// blockersFrom names every installed package a `[blocks …]` line of a pretend
// run says is in the candidate's way, each marked with blockerNote. candidate
// is the clean cat/pkg under test.
//
// Portage's line (_emerge/resolver/output.py) is
//
//	[blocks B      ] <atom> ("<atom>" is hard blocking <parent>[, <parent>…])
//
// where <atom> is what the blocker dependency names and each <parent> is the
// package whose dependency CARRIES it. The two readings are opposite: when the
// candidate carries the blocker (its parent), the package to remove is the
// installed one <atom> matches; when an installed package carries it, that
// parent is the one to remove, and <atom> is the candidate itself. Taking
// <atom> alone told the operator, on 2026-11-28, to "remove app-editors/zed"
// for a zed bump that the installed app-editors/zeo (`!app-editors/zed`)
// blocked.
func blockersFrom(out, candidate string) []string {
	var blockers []string
	seen := map[string]bool{}
	add := func(name string) {
		if name != "" && !seen[name] {
			seen[name] = true
			blockers = append(blockers, name+blockerNote)
		}
	}
	for line := range strings.Lines(out) {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "[blocks") {
			continue
		}
		_, rest, closed := strings.Cut(trimmed, "]")
		if !closed {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		blocked := fields[0]
		parents := blockParents(rest)
		if len(parents) == 0 {
			// A line in a shape not recognised still names a package in the
			// way; the atom is the best name it offers.
			add(blocked)
			continue
		}
		for _, parent := range parents {
			if namesPackageUnderTest(parent, candidate) {
				add(blocked)
				continue
			}
			add(packageOfCPV(parent))
		}
	}
	return blockers
}

// blockParents reads the parents out of a block line's trailing
// `(… blocking a-1, b-2)`, in both of Portage's spellings: with the quoted
// atom and without it (`(is soft blocking a-1)`).
func blockParents(rest string) []string {
	open := strings.LastIndex(rest, "(")
	closeAt := strings.LastIndex(rest, ")")
	if open < 0 || closeAt < open {
		return nil
	}
	_, list, found := strings.Cut(rest[open+1:closeAt], "blocking ")
	if !found {
		return nil
	}
	var parents []string
	for parent := range strings.SplitSeq(list, ",") {
		if parent = strings.TrimSpace(parent); parent != "" {
			parents = append(parents, parent)
		}
	}
	return parents
}

// packageOfCPV cuts the version off a cat/pkg-version, with Gentoo's rule
// that the name ends at the last hyphen followed by a digit; a value with no
// version is returned as it is.
func packageOfCPV(cpv string) string {
	name, _, _ := strings.Cut(cpv, "::")
	for i := len(name) - 1; i > 0; i-- {
		if name[i-1] == '-' && name[i] >= '0' && name[i] <= '9' {
			return name[:i-1]
		}
	}
	return name
}
