package autoupdate

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/obentoo/bentoolkit/internal/autoupdate/ebuilds"
)

// A same-version mirror of ::gentoo (header "# Mirrored from ::gentoo", or a
// registry `patched` text saying so) is ::gentoo's ebuild plus a declared
// divergence, usually a wider PYTHON_COMPAT. A bump copies OUR previous
// ebuild forward, which for a mirror is only right while ::gentoo does not
// carry the new version: once it does, ::gentoo's copy is the base and ours
// is stale. On 2026-11-28 mail-client/thunderbird-161.0 was bumped from our
// 160.0 copy (firefox-160 patchset, older RUST_MIN_VER and nss floors) while
// ::gentoo shipped 161.0, and the "same version in ::gentoo" cooldown
// exemption then published it unmasked as if it were ::gentoo's release.

// gentooMirrorMarker is the header a ::gentoo mirror carries in this overlay,
// and the phrase its registry `patched` text opens with.
const gentooMirrorMarker = "Mirrored from ::gentoo"

// gentooEbuildFor returns the path of ::gentoo's ebuild of category/pn at
// version, compared without -rN: the exact file when it exists, else the one
// with the highest revision. "" when ::gentoo carries no such version or no
// tree was given.
func gentooEbuildFor(gentooPath, category, pn, version string) string {
	if gentooPath == "" {
		return ""
	}
	dir := filepath.Join(gentooPath, category, pn)
	exact := filepath.Join(dir, pn+"-"+version+".ebuild")
	if info, err := os.Stat(exact); err == nil && info.Mode().IsRegular() {
		return exact
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	base := revisionSuffixRe.ReplaceAllString(version, "")
	var matches []string
	for _, e := range entries {
		v, ok := strings.CutPrefix(e.Name(), pn+"-")
		if !ok || e.IsDir() {
			continue
		}
		if v, ok = strings.CutSuffix(v, ".ebuild"); ok && revisionSuffixRe.ReplaceAllString(v, "") == base {
			matches = append(matches, e.Name())
		}
	}
	if len(matches) == 0 {
		return ""
	}
	// -r2 < -r10 is not lexical; compare the revision numbers.
	slices.SortFunc(matches, func(x, y string) int { return revisionOf(x) - revisionOf(y) })
	return filepath.Join(dir, matches[len(matches)-1])
}

// revisionOf reads N from a name ending in -rN.ebuild, 0 without one.
func revisionOf(name string) int {
	name = strings.TrimSuffix(name, ".ebuild")
	m := revisionSuffixRe.FindString(name)
	n := 0
	if m != "" {
		_, _ = fmt.Sscanf(m, "-r%d", &n)
	}
	return n
}

// derivedFromGentoo reports whether ours is theirs plus only what a mirror
// may change: comments (the mirror header, BENTOO-DIVERGENCE tags), blank
// lines, and the PYTHON_COMPAT and KEYWORDS assignments. Any other line that
// differs — a patchset name, a dependency floor — means ours was not built
// from theirs.
func derivedFromGentoo(ours, theirs []byte) bool {
	return slices.Equal(mirrorComparable(ours), mirrorComparable(theirs))
}

// mirrorComparable is the ebuild's lines minus what derivedFromGentoo ignores.
func mirrorComparable(body []byte) []string {
	var out []string
	for line := range strings.Lines(string(body)) {
		line = strings.TrimSpace(line)
		switch {
		case line == "", strings.HasPrefix(line, "#"),
			strings.HasPrefix(line, "PYTHON_COMPAT="), strings.HasPrefix(line, "KEYWORDS="):
			continue
		}
		out = append(out, line)
	}
	return out
}

// isGentooMirror reports whether the candidate is declared a ::gentoo mirror,
// by its own header or by its registry record.
func (a *Applier) isGentooMirror(pkg string, body []byte) bool {
	return bytes.Contains(body, []byte(gentooMirrorMarker)) || strings.Contains(a.configs[pkg].Patched, gentooMirrorMarker)
}

// refuseStaleGentooMirror refuses a candidate that is a ::gentoo mirror when
// ::gentoo already ships the version and the candidate was not built from that
// copy: what was staged is our old version under a new name. Re-deriving it
// is left to a person, because the divergence a record declares is prose, not
// an edit bentoo could replay.
func (a *Applier) refuseStaleGentooMirror(ebuildPath, pkg string, update *PendingUpdate) error {
	category, pn, ok := ebuilds.SplitPkgAtom(pkg)
	if !ok {
		return nil
	}
	theirs := gentooEbuildFor(a.gentooPath, category, pn, update.NewVersion)
	if theirs == "" {
		return nil
	}
	ours, err := os.ReadFile(ebuildPath) //nolint:gosec // G304: the candidate applySubstitutions was handed, inside the overlay or its staged tree
	if err != nil {
		return fmt.Errorf("reading %s to compare it with ::gentoo's copy: %w", ebuildPath, err)
	}
	if !a.isGentooMirror(pkg, ours) {
		return nil
	}
	theirBody, err := os.ReadFile(theirs) //nolint:gosec // G304: a file gentooEbuildFor found in the ::gentoo package directory
	if err != nil {
		return fmt.Errorf("reading ::gentoo's %s to compare the mirror with it: %w", theirs, err)
	}
	if derivedFromGentoo(ours, theirBody) {
		return nil
	}
	return fmt.Errorf("%s is a same-version mirror of ::gentoo and ::gentoo ships %s, but the bump copied our %s forward instead of starting from %s, "+
		"so every ::gentoo-side value it carries (patchsets, dependency floors) is still %s's: re-derive %s-%s from %s plus the "+
		"declared BENTOO-DIVERGENCE changes, or disable the record if ::gentoo's copy is enough",
		pkg, update.NewVersion, update.CurrentVersion, theirs, update.CurrentVersion, pkg, update.NewVersion, theirs)
}

// derivedFromGentooCopy reports whether the candidate at ebuildPath is ::gentoo's
// copy of the same version plus only a mirror's changes. An unreadable file is
// not derived, so a read failure never skips the cooldown.
func derivedFromGentooCopy(ebuildPath, gentooEbuild string) bool {
	ours, err := os.ReadFile(filepath.Clean(ebuildPath))
	if err != nil {
		return false
	}
	theirs, err := os.ReadFile(filepath.Clean(gentooEbuild))
	if err != nil {
		return false
	}
	return derivedFromGentoo(ours, theirs)
}
