package autoupdate

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/obentoo/bentoolkit/internal/autoupdate/ebuilds"
	"github.com/obentoo/bentoolkit/internal/autoupdate/registry"
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
		return ebuilds.RevisionSuffixRegex.ReplaceAllString(have, "") == want
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
	category, pkgName, ok := ebuilds.SplitPkgAtom(atom)
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
// whose name merely extends the atom's (dart-sdk, dart2) never matches. Shell
// comments are left alone — a whole comment line and a trailing "# …" alike —
// while a "#" inside a quoted, possibly multi-line, string is text, as in bash. version is upstream-controlled text on its way into
// bash, so it is refused unless it is a Gentoo version.
func rewritePinnedAtoms(src []byte, atom, pin, version string) ([]byte, int, error) {
	if !exactVersion(version) {
		return nil, 0, fmt.Errorf("refusing to pin %s%s to %q: not a Gentoo version", pin, atom, version)
	}
	re := regexp.MustCompile(`(?:^|[\s("'])` + regexp.QuoteMeta(pin+atom+"-") + `([0-9][0-9A-Za-z._+-]*)`)

	lines := bytes.SplitAfter(src, []byte("\n"))
	count := 0
	var quote byte // the open quote carried across lines: 0, '"' or '\''
	for i, line := range lines {
		code := line[:shellCodeEnd(line, &quote)]
		var out []byte
		last := 0
		for _, m := range re.FindAllSubmatchIndex(code, -1) {
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

// unmetRequirements returns, as atoms ("~dev-lang/dart-3.14.0"), the
// requirements of pkg's pending bump that neither the overlay nor ::gentoo
// satisfies yet. A record without `requires` has none. A record that declares
// some while the pending entry carries no captured version for one of them
// returns ErrRequirementsNotCaptured: an absent capture is never read as "no
// requirement". A presence scan that cannot read a directory is returned as is.
func (a *Applier) unmetRequirements(pkg string, update *PendingUpdate) ([]string, error) {
	cfg, ok := a.configs[pkg]
	if !ok || len(cfg.Requires) == 0 {
		return nil, nil
	}
	var waiting []string
	for _, atom := range slices.Sorted(maps.Keys(cfg.Requires)) {
		pin := cfg.Requires[atom].Pin
		version, captured := update.Requires[atom]
		if !captured || version == "" {
			return nil, fmt.Errorf("%w (%s has no captured version for %s)", ErrRequirementsNotCaptured, pkg, atom)
		}
		met, err := requirementMet(a.overlayPath, a.gentooPath, atom, pin, version)
		if err != nil {
			return nil, fmt.Errorf("checking %s's requirement %s%s-%s: %w", pkg, pin, atom, version, err)
		}
		if !met {
			waiting = append(waiting, pin+atom+"-"+version)
		}
	}
	return waiting, nil
}

// rewriteRequirementPins points every pinned atom of each required package in
// the candidate ebuild at the version captured for it. A record that declares a
// pin the ebuild does not carry fails with ErrRequirementPinNotFound. The file
// is rewritten once, atomically, keeping its mode.
func (a *Applier) rewriteRequirementPins(ebuildPath, pkg string, update *PendingUpdate) error {
	cfg := a.configs[pkg]
	if len(cfg.Requires) == 0 {
		return nil
	}
	content, err := os.ReadFile(ebuildPath) //nolint:gosec // G304: the candidate path candidateIn built from a confined package key and a validated version
	if err != nil {
		return fmt.Errorf("reading %s to rewrite its requirement pins: %w", ebuildPath, err)
	}
	updated := content
	for _, atom := range slices.Sorted(maps.Keys(cfg.Requires)) {
		pin := cfg.Requires[atom].Pin
		var n int
		updated, n, err = rewritePinnedAtoms(updated, atom, pin, update.Requires[atom])
		if err != nil {
			return fmt.Errorf("rewriting %s%s in %s: %w", pin, atom, filepath.Base(ebuildPath), err)
		}
		if n == 0 {
			return fmt.Errorf("%w: %s%s in %s", ErrRequirementPinNotFound, pin, atom, filepath.Base(ebuildPath))
		}
	}
	if bytes.Equal(updated, content) {
		return nil
	}
	if err := replaceEbuildKeepingMode(ebuildPath, string(updated)); err != nil {
		return fmt.Errorf("writing %s after rewriting its requirement pins: %w", ebuildPath, err)
	}
	return nil
}

// shellCodeEnd returns where the code part of one ebuild line ends: at the
// first "#" that starts a word outside quotes, or at the end of the line. quote
// carries the open quote across lines, since DEPEND="…" spans many; it is
// updated over the code part only. A backslash escapes the next byte outside
// single quotes, as in bash.
func shellCodeEnd(line []byte, quote *byte) int {
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == '\\' && *quote != '\'':
			i++
		case *quote != 0:
			if c == *quote {
				*quote = 0
			}
		case c == '"' || c == '\'':
			*quote = c
		case c == '#' && (i == 0 || line[i-1] == ' ' || line[i-1] == '\t'):
			return i
		}
	}
	return len(line)
}

// settleRequirements fills result.Requirements for the versions captured in
// reqs: present when the overlay or ::gentoo satisfies the pin, pending when a
// pending entry of that package will, missing otherwise. A presence scan that
// cannot read a repository is joined into result.Error and that requirement
// gets no state.
func (c *Checker) settleRequirements(pkg string, cfg *registry.PackageConfig, reqs map[string]string, result *CheckResult) {
	if len(reqs) == 0 {
		return
	}
	entries := c.pending.List()
	for _, atom := range slices.Sorted(maps.Keys(reqs)) {
		pin, version := cfg.Requires[atom].Pin, reqs[atom]
		met, err := requirementMet(c.overlayPath, c.gentooPath, atom, pin, version)
		if err != nil {
			result.Error = errors.Join(result.Error, fmt.Errorf("%s requiring %s: %w", pkg, atom, err))
			continue
		}
		state := RequirementMissing
		switch {
		case met:
			state = RequirementPresent
		case pendingSatisfies(entries, atom, pin, version):
			state = RequirementPending
		}
		result.Requirements = append(result.Requirements, RequirementState{Package: atom, Version: version, State: state})
	}
}

// resettleMissing re-reads the pending list once and turns every missing
// requirement that a pending entry now satisfies into pending. CheckAll calls it
// after every worker has joined, because the required package may have been
// checked, and queued, after the package that requires it.
func (c *Checker) resettleMissing(results []CheckResult) {
	var entries []PendingUpdate
	loaded := false
	for i := range results {
		for j, req := range results[i].Requirements {
			if req.State != RequirementMissing {
				continue
			}
			if !loaded {
				entries, loaded = c.pending.List(), true
			}
			pin := c.config.Packages[results[i].Package].Requires[req.Package].Pin
			if pendingSatisfies(entries, req.Package, pin, req.Version) {
				results[i].Requirements[j].State = RequirementPending
			}
		}
	}
}

// pendingSatisfies reports whether a pending entry of atom — keyed with or
// without a ":slot" or "@label" — bumps to a version meeting pin and version.
func pendingSatisfies(entries []PendingUpdate, atom, pin, version string) bool {
	for _, e := range entries {
		cat, name, ok := ebuilds.SplitPkgAtom(e.Package)
		if ok && cat+"/"+name == atom && VersionSatisfies(pin, e.NewVersion, version) {
			return true
		}
	}
	return false
}

// PackageAtom returns the "category/package" a package key names, dropping any
// ":slot" and "@label", and "" for a key that is not an atom. The --apply all
// planner uses it to match a pending entry against another entry's requirement.
func PackageAtom(key string) string {
	cat, name, ok := ebuilds.SplitPkgAtom(key)
	if !ok {
		return ""
	}
	return cat + "/" + name
}

// RequirePin returns the pin operator pkg's record declares for atom, and false
// when the record does not (any more) require it.
func (a *Applier) RequirePin(pkg, atom string) (string, bool) {
	spec, ok := a.configs[pkg].Requires[atom]
	return spec.Pin, ok
}
