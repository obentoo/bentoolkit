package autoupdate

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/obentoo/bentoolkit/internal/common/ebuild"
)

// VersionSatisfies reports whether an ebuild at version have meets the pin
// `<pin><category>/<package>-<want>`:
//
//   - "~"  — have, with its -rN revision dropped, equals want;
//   - "="  — have equals want exactly, revision included;
//   - ">=" — have compares greater than or equal to want.
//
// Any other pin is never satisfied. It is the one implementation of the rule:
// the presence scan, the check report's pending state and the --apply all wave
// planner all call it, so the three can never disagree about a revision.
func VersionSatisfies(pin, have, want string) bool {
	switch pin {
	case "~":
		return revisionSuffixRegex.ReplaceAllString(have, "") == want
	case "=":
		return have == want
	case ">=":
		return ebuild.CompareVersions(have, want) >= 0
	}
	return false
}

// exactVersion reports whether v is a Gentoo version AS WRITTEN. It exists
// because ebuild.IsValidVersion trims surrounding whitespace before matching,
// which is right for parsing and wrong for a value about to be written into
// bash: "3.14.0\n" passes it and would split the dependency line.
func exactVersion(v string) bool {
	return v == strings.TrimSpace(v) && ebuild.IsValidVersion(v)
}

// requirementMet reports whether the overlay, or else ::gentoo, holds an ebuild
// of atom that satisfies pin and version. A repository whose package directory
// does not exist simply does not hold it; an empty gentooPath skips ::gentoo.
// Any other read failure is returned, wrapped with the directory, because "could
// not look" is not "not there".
func requirementMet(overlayPath, gentooPath, atom, pin, version string) (bool, error) {
	category, pkgName, ok := splitPkgAtom(atom)
	if !ok {
		return false, fmt.Errorf("requirement %q is not a category/package atom", atom)
	}
	for _, repo := range []string{overlayPath, gentooPath} {
		if repo == "" {
			continue
		}
		dir := filepath.Join(repo, category, pkgName)
		entries, err := os.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("reading %s for %s: %w", dir, atom, err)
		}
		prefix := pkgName + "-"
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".ebuild") {
				continue
			}
			have := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".ebuild")
			if !ebuild.IsValidVersion(have) || strings.HasPrefix(have, "9999") {
				continue
			}
			if VersionSatisfies(pin, have, version) {
				return true, nil
			}
		}
	}
	return false, nil
}

// rewritePinnedAtoms points every `<pin><atom>-<version>` dependency atom in an
// ebuild at version, and returns the rewritten source with the number of atoms
// moved.
//
// Only atoms carrying exactly pin are touched. The operator must start a token
// — line start, whitespace, "(" or a quote — so "=" never matches the tail of
// ">=" or "<=" and "~" never matches inside the "!~" blocker. The old version
// must be followed by a slot ":", a USE "[", a "*", whitespace, a quote, a ")"
// or the end of the line, and must itself be a Gentoo version, so a package
// whose name merely extends the atom's (dart-sdk, dart2) never matches. Comment
// lines are left alone. version is upstream-controlled text on its way into
// bash, so it is refused unless it is a Gentoo version.
func rewritePinnedAtoms(src []byte, atom, pin, version string) ([]byte, int, error) {
	if !exactVersion(version) {
		return nil, 0, fmt.Errorf("refusing to pin %s%s to %q: not a Gentoo version", pin, atom, version)
	}
	re := regexp.MustCompile(`(?:^|[\s("'])` + regexp.QuoteMeta(pin+atom+"-") + `([0-9][0-9A-Za-z._+-]*)`)

	lines := bytes.SplitAfter(src, []byte("\n"))
	count := 0
	for i, line := range lines {
		if trimmed := bytes.TrimLeft(line, " \t"); bytes.HasPrefix(trimmed, []byte("#")) {
			continue
		}
		var out []byte
		last := 0
		for _, m := range re.FindAllSubmatchIndex(line, -1) {
			start, end := m[2], m[3]
			if end < len(line) && !bytes.ContainsAny(line[end:end+1], ":[* \t\r\n\"')") {
				continue
			}
			if !ebuild.IsValidVersion(string(line[start:end])) {
				continue
			}
			out = append(out, line[last:start]...)
			out = append(out, version...)
			last = end
			count++
		}
		if out != nil {
			lines[i] = append(out, line[last:]...)
		}
	}
	return bytes.Join(lines, nil), count, nil
}
