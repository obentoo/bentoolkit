package ebuilds

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/obentoo/bentoolkit/internal/common/ebuild"
	"github.com/obentoo/bentoolkit/internal/common/logging"
)

// Sentinel errors for locating packages and ebuilds in an overlay tree.
var (
	// ErrPackageNotFound is returned when a package is not found in the configuration
	ErrPackageNotFound = errors.New("package not found in configuration")
	// ErrNoEbuildFound is returned when no ebuild file is found for a package
	ErrNoEbuildFound = errors.New("no ebuild file found for package")
	// ErrEbuildNotFound is returned when the source ebuild file is not found
	ErrEbuildNotFound = errors.New("source ebuild file not found")
)

// ErrSlotNotFound is returned when a package's directory IS present in the
// overlay but no ebuild in it declares the slot the entry's key asks for.
//
// It is deliberately NOT an ErrNoEbuildFound. That error means "this package is
// gone from the overlay", and the checker acts on it: the entry is auto-disabled
// in packages.toml so a removed package stops being processed. A slot that
// matches nothing is the opposite situation — the package is right there, the
// key is wrong (a typo, or a slot that has not been packaged yet) — and
// silently writing enabled = false for it would turn a config mistake into a
// package that quietly stops being updated.
//
// This distinction is not hypothetical. A 0.14.0 checker, which predates slot
// keys entirely, reads "net-libs/webkit-gtk:4.1" as a directory name, fails to
// find it, concludes the ebuild was removed and disables both webkit-gtk slot
// entries — with no error and no exit code to notice. Config the checker cannot
// interpret must fail loudly, never silently downgrade itself.
var ErrSlotNotFound = errors.New("no ebuild in the package directory declares the requested slot")

// A package key in packages.toml is normally a plain "category/package" atom.
// Two suffixes may narrow it, and both exist for the same reason: the overlay
// holds several ebuilds for one package, and one entry cannot track them all —
// taking the directory's highest version means one of them is bumped forever and
// the rest never are.
//
//   - ":slot" — "net-libs/webkit-gtk:4.1", mirroring Portage's atom syntax. The
//     entry considers only ebuilds whose SLOT= declares that slot.
//   - "@label" — "app-office/libreoffice@testing". A free identifier that only
//     makes the key unique, for a package whose parallel ebuilds share one SLOT
//     and are told apart by release LINE instead: the stable 26.2 series and the
//     testing 26.8 one, or zed-bin's 1.13 stable next to its 1.14 preview. The
//     line itself is declared by the entry's `series` field; the label just names
//     it. A separate character is used because ":" already means SLOT — both may
//     appear ("cat/pkg:4.1@stable"), and reading a label as a slot would filter
//     on a SLOT= value no ebuild declares.
//
// Both are part of the identity used to key pending.json, cache.json and the
// config map, and NEITHER is part of any filesystem path: every path-building
// site must strip them first.

// SplitPkgLabel splits a package key into everything before its "@label" and the
// label itself. A key with no "@" yields an empty label. The label carries no
// behavior of its own — it exists so two entries for the same atom and slot can
// coexist — so every consumer other than the config map simply drops it.
func SplitPkgLabel(key string) (rest, label string) {
	if i := strings.IndexByte(key, '@'); i >= 0 {
		return key[:i], key[i+1:]
	}
	return key, ""
}

// SplitPkgSlot splits a package key into its "category/package" atom and its
// optional slot restriction, dropping any "@label" first. A key with no ":"
// yields an empty slot, which every consumer reads as "no slot filtering" — the
// behaviour of every single-slot package, i.e. all of them before this existed.
func SplitPkgSlot(key string) (atom, slot string) {
	key, _ = SplitPkgLabel(key)
	if i := strings.IndexByte(key, ':'); i >= 0 {
		return key[:i], key[i+1:]
	}
	return key, ""
}

// SplitPkgAtom splits a package key into its category and package-name
// components, dropping any ":slot" and "@label" suffix. It reports false when
// the key is not a well-formed "category/package" atom, leaving the (varied)
// error wording to each caller. This is the single place that knows a package
// key is not necessarily a bare atom, so path building cannot silently inherit a
// suffix.
//
// Both halves are joined under the overlay (and the staged tree) as directory
// names, and the key comes from packages.toml, so each half must name exactly
// one directory: "../x" or "cat/.." would otherwise be accepted as an atom and
// build a path outside the package directory (S064-R1.4). parsePkgAtom says
// which half was refused and why.
func SplitPkgAtom(key string) (category, pkgName string, ok bool) {
	category, pkgName, err := ParsePkgAtom(key)
	if err != nil {
		return "", "", false
	}
	return category, pkgName, true
}

// ParsePkgAtom is splitPkgAtom with the refusal explained: the error names the
// half that cannot be used as one directory name. Callers that report a bad key
// to a person (ValidatePackageConfig) use it; path builders use splitPkgAtom.
func ParsePkgAtom(key string) (category, pkgName string, err error) {
	atom, _ := SplitPkgSlot(key)
	category, pkgName, found := strings.Cut(atom, "/")
	if !found {
		return "", "", fmt.Errorf("%q does not name category/package", atom)
	}
	for _, half := range []struct{ kind, value string }{{"category", category}, {"package name", pkgName}} {
		if err := atomPathElementError(half.kind, half.value); err != nil {
			return "", "", err
		}
	}
	return category, pkgName, nil
}

// atomPathElementError refuses a value that would make a joined path mean
// something other than "one directory named this": empty, the two relative
// directory names, a separator of either flavour, and a NUL byte. It mirrors
// validate.usableAsPathElement, which guards the staged side of the same key.
func atomPathElementError(kind, value string) error {
	switch {
	case value == "":
		return fmt.Errorf("the %s is empty", kind)
	case value == "." || value == "..":
		return fmt.Errorf("the %s %q names a directory other than itself", kind, value)
	case strings.ContainsAny(value, `/\`) || strings.ContainsRune(value, 0):
		return fmt.Errorf("the %s %q contains a path separator, so it cannot be one directory name", kind, value)
	}
	return nil
}

// SplitPackageKey splits a packages.toml key into its category and package-name
// components, dropping any ":slot" suffix. It is the exported form of
// splitPkgAtom, for callers outside this package that turn a key into a
// filesystem path (cmd/bentoo's revive flow) — a second, slot-blind copy of the
// split is exactly the bug the slot suffix invites.
func SplitPackageKey(key string) (category, name string, ok bool) {
	return SplitPkgAtom(key)
}

// PkgDirFor returns the overlay directory holding pkg's ebuilds, or "" when the
// key is malformed.
func PkgDirFor(overlayPath, pkg string) string {
	category, pkgName, ok := SplitPkgAtom(pkg)
	if !ok {
		return ""
	}
	return filepath.Join(overlayPath, category, pkgName)
}

// slotAssignRegex matches a SLOT assignment at the start of a line, quoted with
// either quote style or bare: SLOT="4.1/0", SLOT='0', SLOT=0. The captured value
// stops at the closing quote or at the first whitespace/comment, so a trailing
// comment (`SLOT="6/0" # soname version`) does not leak into the value.
var slotAssignRegex = regexp.MustCompile(`(?m)^SLOT=(?:"([^"]*)"|'([^']*)'|([^\s#]+))`)

// readEbuildSlot returns the slot an ebuild declares, without its subslot: the
// SLOT="4.1/0" of a webkit-gtk 4.1 ebuild yields "4.1". It returns "" when the
// file is unreadable or declares no SLOT, which callers treat as "does not match
// any requested slot" — a slot filter must never widen its selection because a
// file could not be read.
func readEbuildSlot(path string) string {
	content, err := os.ReadFile(path) //nolint:gosec // path is built from the overlay dir listing
	if err != nil {
		return ""
	}
	m := slotAssignRegex.FindSubmatch(content)
	if m == nil {
		return ""
	}
	var value string
	for _, group := range m[1:] {
		if len(group) > 0 {
			value = string(group)
			break
		}
	}
	// Drop the subslot: Portage's dependency syntax names ":4.1", not ":4.1/0".
	if i := strings.IndexByte(value, '/'); i >= 0 {
		value = value[:i]
	}
	return strings.TrimSpace(value)
}

// ebuildCandidate is one non-live ebuild found in a package directory.
type ebuildCandidate struct {
	// Version is the PV as it appears in the filename, revision suffix
	// included: "2.52.4-r411".
	Version string
	// Path is the absolute path to the .ebuild file.
	Path string
}

// ErrSeriesNotFound is returned when a package's directory IS present in the
// overlay but no ebuild in it matches the entry's `series`. Like ErrSlotNotFound
// — and for the same reason — it is deliberately NOT an ErrNoEbuildFound: the
// package is there, the entry's filter is what selects nothing, and
// auto-disabling it would turn a config mistake into a package that quietly
// stops being updated.
var ErrSeriesNotFound = errors.New("no ebuild in the package directory matches the entry's series")

// seriesMatcher compiles a series regex once for a scan. A nil matcher means
// "no series filtering", which is every entry that does not declare one.
type seriesMatcher struct{ re *regexp.Regexp }

// NewSeriesMatcher compiles series into a matcher. An empty series yields a
// pass-everything matcher. An uncompilable one is warned and also passes
// everything: ValidatePackageConfig rejects it up front, and a scan that got
// this far must not silently select the wrong ebuild — a filter that fails open
// is visible (the wrong line gets bumped and the warning says why), one that
// fails closed looks exactly like a removed package. The warning goes to log;
// nil discards it.
func NewSeriesMatcher(log *slog.Logger, series string) seriesMatcher {
	if series == "" {
		return seriesMatcher{}
	}
	re, err := regexp.Compile(series)
	if err != nil {
		logging.OrDiscard(log).Warn("series: bad regex; no series filtering applied", "regex", series, "err", err)
		return seriesMatcher{}
	}
	return seriesMatcher{re: re}
}

// Matches reports whether version belongs to the matcher's release line.
func (m seriesMatcher) Matches(version string) bool {
	return m.re == nil || m.re.MatchString(version)
}

// Active reports whether the matcher actually filters anything.
func (m seriesMatcher) Active() bool { return m.re != nil }

// SelectCurrentEbuild returns the highest-version, non-live ebuild for pkg in
// the overlay. It is the single implementation behind the checker's
// getCurrentVersion/currentEbuildPath and the applier's resolveCurrentVersion,
// which were three byte-for-byte copies of the same scan.
//
// Two filters narrow the scan, and an entry uses whichever tells its package's
// parallel ebuilds apart:
//
//   - ":slot" in the key — only ebuilds whose SLOT= declares that slot.
//   - `series` — only ebuilds whose version matches that regex, for a package
//     whose parallel ebuilds share one SLOT and differ by release line.
//
// Without them the scan returns the directory's highest PV whatever line it
// belongs to, so one line is bumped forever and the rest never are. That is not
// hypothetical: with zed-bin-1.13.1 and zed-bin-1.14.1_pre both in the overlay
// and one entry tracking the stable channel, every stable release below 1.14.1
// compares older than the preview ebuild and reports "up to date" — the stable
// line silently stops being updated.
//
// Reading file contents is confined to the slot-filtered path, so the ordinary
// unfiltered scan still costs one readdir and no file reads (a series filter
// reads no files at all: it matches on the version in the filename).
//
// A series that does not compile is warned about to log; nil discards it.
func SelectCurrentEbuild(log *slog.Logger, overlayPath, pkg, series string) (ebuildCandidate, error) {
	category, pkgName, ok := SplitPkgAtom(pkg)
	if !ok {
		return ebuildCandidate{}, fmt.Errorf("invalid package name format: %s", pkg)
	}
	_, slot := SplitPkgSlot(pkg)
	matcher := NewSeriesMatcher(log, series)

	pkgDir := filepath.Join(overlayPath, category, pkgName)
	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		if os.IsNotExist(err) {
			return ebuildCandidate{}, fmt.Errorf("%w: %s", ErrNoEbuildFound, pkg)
		}
		return ebuildCandidate{}, fmt.Errorf("failed to read package directory: %w", err)
	}

	var best ebuildCandidate
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".ebuild") || strings.Contains(name, "-9999.ebuild") {
			continue
		}
		eb, err := ebuild.ParsePath(filepath.Join(category, pkgName, name))
		if err != nil {
			continue // Skip invalid ebuild files
		}
		// The series names an upstream release line, so it is matched against the
		// PV: matched against the PVR, an exact `^1\.8\.3$` rejected the line's
		// own 1.8.3-r1 and every revbump broke the entry.
		if !matcher.Matches(RevisionSuffixRegex.ReplaceAllString(eb.Version, "")) {
			continue
		}
		path := filepath.Join(pkgDir, name)
		if slot != "" && readEbuildSlot(path) != slot {
			continue
		}
		if best.Version == "" || ebuild.CompareVersions(eb.Version, best.Version) > 0 {
			best = ebuildCandidate{Version: eb.Version, Path: path}
		}
	}

	if best.Version == "" {
		// The package directory exists (ReadDir succeeded above), so this is
		// never an orphan. With a filter it is a config error; without one the
		// directory holds no parsable, non-live ebuild at all.
		if slot != "" {
			return ebuildCandidate{}, fmt.Errorf("%w: %s (slot %q)", ErrSlotNotFound, pkg, slot)
		}
		if matcher.Active() {
			return ebuildCandidate{}, fmt.Errorf("%w: %s (series %q)", ErrSeriesNotFound, pkg, series)
		}
		return ebuildCandidate{}, fmt.Errorf("%w: %s", ErrNoEbuildFound, pkg)
	}
	return best, nil
}

// RevisionSuffixRegex matches the trailing -rN of a PV.
var RevisionSuffixRegex = regexp.MustCompile(`-r\d+$`)

// ApplyRevision returns the PV to write for a freshly bumped ebuild, attaching
// the revision a multi-slot package pins via `revision = N` in packages.toml.
//
// The revision is configured rather than carried over from the source ebuild
// because carrying it over is wrong twice. For an ordinary package the revision
// must RESET on a PV change — bumping foo-1.2.3-r1 produces foo-1.2.4, never
// foo-1.2.4-r1. And where a revision does discriminate slots, the value to write
// is the slot's base, not the source's: ::gentoo bumps webkit-gtk-2.52.3-r411
// (SLOT 4.1) to webkit-gtk-2.52.5-r410, because r411 was a revbump within the
// old PV. Neither rule is derivable from the source filename, so it is declared.
//
// revision <= 0 means "plain PV", which is both the default and correct for
// every single-slot package — and for a slot that happens to use a bare PV, as
// the bentoo overlay's SLOT 6 webkit-gtk ebuild does.
func ApplyRevision(version string, revision int) string {
	base := RevisionSuffixRegex.ReplaceAllString(version, "")
	if revision <= 0 {
		return base
	}
	return base + "-r" + strconv.Itoa(revision)
}
