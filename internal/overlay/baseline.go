package overlay

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/obentoo/bentoolkit/internal/common/ebuild"
	"github.com/obentoo/bentoolkit/internal/common/output"
)

// baselineRepo is the repository every baseline comes from.
//
// It is a constant rather than an argument because the invocation is already
// constrained to ::gentoo long before this is reached: a realignment run asked
// for any other repository is refused as a usage error naming the reason, so
// that the comparison can never run against GURU while the baseline was read
// from ::gentoo. Naming it on every Baseline is still worth a field — R1 is
// that the baseline is NAMED and never assumed, and a report that only implies
// where its evidence came from is exactly the assumption.
const baselineRepo = "gentoo"

// Baseline is the ::gentoo ebuild that one of our ebuilds is measured against.
//
// Its zero value is "::gentoo does not carry this package": Found is false and
// nothing is named, because there is nothing to name (R1.3). No realignment is
// ever proposed from a baseline in that state — 84 of the overlay's 321
// packages are in it, and they are Bentoo's own work rather than a divergence
// from anyone's.
//
// Every field is comparable, so the struct is: it is carried by value on a
// result, and "is this still the zero value" is how a run that requested no
// review proves it changed nothing.
type Baseline struct {
	// Repo names the repository the baseline was read from.
	Repo string
	// Version is the version that repository carries: OUR version when it
	// carries it (R1.1), and the nearest one it does carry otherwise (R1.2).
	Version string
	// Path is the ebuild the comparison will actually read, so that "which file
	// was this measured against" is answered by the report instead of being
	// re-derived by whoever reads it.
	Path string
	// Distance is how far Version is from ours, in release steps — see
	// versionDistance, which states the unit. It is 0 exactly when the baseline
	// IS our version.
	//
	// It is reported because it bounds how much the comparison is worth: a
	// baseline one patch release away and a baseline three series away are not
	// the same kind of evidence, and against the second one most of the
	// difference may be nothing but the version move between the two. The
	// report must not let them look alike (D2) — a difference measured against
	// a far baseline is a question, not a finding.
	Distance int
	// Found reports whether ::gentoo carries the package at all.
	Found bool
	// Unexamined is non-empty when a baseline could not be read, and says why.
	//
	// That is a per-package state and not a run failure, following
	// verifyAgainstLocalContent, which already reads an unreadable ebuild as
	// NotVerified and exits 0 on the principle that absence of evidence is not
	// evidence. Reported as nothing at all, such a package would be
	// indistinguishable from one that matches ::gentoo exactly.
	Unexamined string
}

// ResolveBaseline names the ::gentoo ebuild that atom's `version` in our
// overlay should be compared against, reading nothing but the tree it is given.
//
// The rule is two lines. If ::gentoo carries our exact version, that ebuild is
// the baseline and the distance is 0 (R1.1) — the same version is not a
// candidate among others, it is the answer, which is why it is matched before
// any proximity is computed at all. Otherwise the nearest version ::gentoo does
// carry is the baseline, and the distance says how far away it is (R1.2). If
// ::gentoo carries no version of the package, nothing is named and Found stays
// false (R1.3).
//
// Everything it reads is a file inside gentooTree: one directory listing and
// one ebuild. It runs no command, resolves no host and never consults git —
// which is not only R1.4 but the only thing that could work, since the local
// /var/db/repos/gentoo is a shallow clone whose history is absent by
// construction. The answer is in the tree's CONTENT, which is all such a clone
// offers.
//
// The error return is for a request that cannot be answered AS ASKED — a
// malformed atom, no tree, no version of ours to compare — and for nothing
// else. Every state of the tree itself is a value: a package ::gentoo does not
// carry is Found=false, and a baseline that exists but will not read is
// Unexamined. That distinction is load-bearing, because the caller derives a
// non-zero exit from a run that could not look at all; were a per-package
// non-answer to arrive as an error, the 84 packages with no counterpart would
// turn every review into a failed run.
//
// _Requirements: R1, R1.1, R1.2, R1.3, R1.4_
func ResolveBaseline(gentooTree, atom, version string) (Baseline, error) {
	category, pkg, err := splitBaselineAtom(atom)
	if err != nil {
		return Baseline{}, fmt.Errorf("resolving the baseline for %q: %w", atom, err)
	}
	if gentooTree == "" {
		return Baseline{}, fmt.Errorf("resolving the baseline for %s: no ::gentoo tree was given", atom)
	}
	if version == "" {
		// Without our own version there is no exact match to prefer and no
		// distance to measure from, and "nearest to nothing" would quietly pick
		// whichever version happens to sort lowest. R1 is that the baseline is
		// never a guess, so this is refused instead of answered.
		return Baseline{}, fmt.Errorf("resolving the baseline for %s: no version of ours was given", atom)
	}

	dir := filepath.Join(gentooTree, category, pkg)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// ::gentoo does not carry the package. It is the second most common
			// answer this function gives — 84 of 321 packages — and it is not a
			// failure of any kind (R1.3).
			return Baseline{}, nil
		}
		// The directory is there and would not be listed. "We could not look" is
		// not "there is nothing there", so it is reported as unexamined rather
		// than as an absent package. Found stays false because no version was
		// ever seen, and R1.3 already forbids proposing a realignment from that.
		return Baseline{Unexamined: fmt.Sprintf("the ::gentoo package directory could not be listed: %v", err)}, nil
	}

	carried := carriedVersions(entries, category, pkg)
	if len(carried) == 0 {
		// A directory with no ebuild in it carries no version, which is the same
		// answer as no directory at all.
		return Baseline{}, nil
	}

	chosen := pickBaseline(carried, version)
	baseline := Baseline{
		Repo:     baselineRepo,
		Version:  chosen.version,
		Path:     filepath.Join(dir, chosen.filename),
		Distance: versionDistance(version, chosen.version),
		Found:    true,
	}

	// The chosen candidate is read, and only it. Whether the tree carries a
	// version and whether that file opens are two questions, and the second is
	// asked by READING rather than by stat'ing: a directory standing where the
	// ebuild must be stats perfectly well, and a permission bit is only ever
	// true for the process that tries. The content is discarded — every
	// consumer opens the path for itself — so this costs one small file per
	// package and answers with the same evidence they will get.
	if _, err := os.ReadFile(baseline.Path); err != nil { //nolint:gosec // path built from the given tree, a validated atom and a filename read from that directory's own listing, never from registry input
		baseline.Unexamined = fmt.Sprintf("the baseline ebuild could not be read: %v", err)
	}

	return baseline, nil
}

// OtherRepo is a repository other than ::gentoo that carries a package ::gentoo
// does not — one row of the answer for the 84 of 321 overlay packages that have
// no ::gentoo counterpart at all (R6.1).
//
// CHECKED IS THE FIELD THE TYPE EXISTS FOR. The tempting shape is a two-way
// answer — carries it, or does not — and under it a repository nobody consulted
// reports exactly like one that was consulted and came back empty. Those are
// different answers and only one of them is true. The ~428 registered
// repositories are resolvable BY NAME, but nothing on disk holds the contents of
// most of them, and asking about 84 packages across all of them would be
// thousands of network lookups in a stage the story promises is offline.
//
// Whatever it reports is INFORMATIVE ONLY and never a baseline (R6.2): a
// repository outside ::gentoo has not been through the same review, its quality
// varies from one to the next, and where several carry a package the report
// names each and chooses between them for no purpose (R6.3).
//
// Every field is comparable, so a nil slice of them is "nothing to say" and
// renders nothing — the same terms every field of the baseline review rides on.
//
// _Requirements: R6, R6.1, R6.2, R6.3_
type OtherRepo struct {
	// Name is the repository as the registry names it — "guru", not "::guru";
	// the report adds the "::" it prints.
	Name string
	// Version is the version that repository carries, empty when it carries
	// none. It is meaningless unless Checked is true.
	Version string
	// Checked reports that the repository's contents were actually read. False
	// means NOT CHECKED — the repository is registered but not available
	// locally — and never "it does not carry the package".
	Checked bool
}

// ErrNoBaselineTree is the run-level outcome: there is no ::gentoo tree to read,
// so NOTHING was examined and the review could not do its job.
//
// It is a SENTINEL because the caller branches on it. This is the one condition
// the command exits non-zero for (D9), and an exit code cannot be derived by
// matching on a message. Everything else this package knows about a baseline is
// a per-package VALUE and never an error — Baseline.Found is false for the 84
// packages ::gentoo does not carry, Baseline.Unexamined says why a baseline that
// exists would not read — so no per-package non-answer can reach the exit code
// through here. The two states are returned through different channels on
// purpose; collapsing them would make one unreadable ebuild fail the whole run.
//
// _Requirements: R1, R1.5_
var ErrNoBaselineTree = errors.New("no ::gentoo tree to compare against")

// portageRepoMarker is the file that makes a directory a Portage repository
// rather than a directory with the right name.
//
// It is what LocateBaselineTree looks for, and therefore what R1.5's "name what
// it looked for" names. Existence of the directory is not enough to go on:
// /var/db/repos/gentoo is there on a machine that has never synced it, and taken
// for a tree it would report every one of the overlay's 321 packages as absent
// from ::gentoo — 321 packages presented as Bentoo's own work, by a run that
// exited 0 and said nothing.
//
// It is spelled with a forward slash, as Portage's own documentation does, and
// joined onto a root through filepath.FromSlash so the path itself stays
// platform-correct.
const portageRepoMarker = "profiles/repo_name"

// LocateBaselineTree resolves candidate to the local ::gentoo repository, or
// reports that there is none to compare against.
//
// A tree is RECOGNISED and never assumed (R1): the directory has to carry
// Portage's own repository marker, which is what tells a synced repository from
// an empty directory standing where one is meant to be. What it accepts it
// returns AS GIVEN — cleaned, never replaced — because the report names the tree
// the review actually read, and a default quietly fallen back to would be a
// baseline nobody asked for.
//
// The refusal wraps ErrNoBaselineTree and NAMES the path and the marker (R1.5).
// "No baseline tree" on its own is unactionable: a mistyped path and a
// repository that was never synced need opposite fixes, and the message says
// which one it found — nothing at all there, or a directory that is not a
// repository.
//
// It stats two paths and reads nothing at all: no command, no host, no git,
// exactly as ResolveBaseline does and for the same reason (R1.4).
//
// _Requirements: R1, R1.4, R1.5_
func LocateBaselineTree(candidate string) (string, error) {
	if candidate == "" {
		// Nothing was configured, so there is no path to name and only the marker
		// can be reported. Reachable: a `gentoo` entry with an empty path.
		return "", fmt.Errorf("%w: no path was given to look for %s in", ErrNoBaselineTree, portageRepoMarker)
	}

	root := filepath.Clean(candidate)
	marker := filepath.Join(root, filepath.FromSlash(portageRepoMarker))

	info, err := os.Stat(root)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "", fmt.Errorf("%w: nothing at %s, where %s was looked for", ErrNoBaselineTree, root, marker)
	case err != nil:
		// There and unexaminable. Still "we could not look", which is what this
		// error means — the stat's own text is carried with %v rather than %w
		// because the sentinel is the only thing a caller is meant to match on.
		return "", fmt.Errorf("%w: %s could not be examined: %v", ErrNoBaselineTree, root, err)
	case !info.IsDir():
		return "", fmt.Errorf("%w: %s is not a directory, so it carries no %s", ErrNoBaselineTree, root, portageRepoMarker)
	}

	markerInfo, err := os.Stat(marker)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "", fmt.Errorf("%w: %s carries no %s, so it is a directory rather than a synced repository", ErrNoBaselineTree, root, portageRepoMarker)
	case err != nil:
		return "", fmt.Errorf("%w: %s could not be examined: %v", ErrNoBaselineTree, marker, err)
	case markerInfo.IsDir():
		return "", fmt.Errorf("%w: %s is a directory rather than Portage's marker file, so %s is not a repository", ErrNoBaselineTree, marker, root)
	}

	// The marker's CONTENT is not read. Which repository a realignment run may
	// name is settled on the configuration side, before any of this is reached;
	// what this function answers is whether a repository is there to be read at
	// all.
	return root, nil
}

// MarkBaselineSkipped records on the report that the review was SKIPPED because
// no ::gentoo tree could be located, naming what was looked for (R1.5).
//
// It exists as a function rather than as a field the caller assigns so the
// wording is written once, beside the marker it names. What it must never
// record is a per-package state: an unreadable baseline ebuild is
// Baseline.Unexamined on that one package (D2) and leaves this untouched, or
// else a run that examined 320 packages would report itself as having examined
// none.
//
// _Requirements: R1, R1.5_
func MarkBaselineSkipped(report *CompareReport, lookedFor string) {
	if lookedFor == "" {
		// Nothing was configured, so there is no path to name. Naming an empty one
		// would print a sentence with a hole in it, which reads as a bug in the
		// report rather than as the missing configuration it is.
		report.BaselineSkipped = "no ::gentoo tree was configured, so nothing was compared against ::gentoo"
		return
	}
	report.BaselineSkipped = fmt.Sprintf(
		"no ::gentoo tree at %s — looked for its %s marker, so nothing was compared against ::gentoo",
		lookedFor, portageRepoMarker)
}

// baselineSkippedLead opens the run-level SKIPPED line. It is a constant so a
// test can name the word without copying the sentence, on the same argument that
// made undeclaredDivergenceCaveat one.
const baselineSkippedLead = "Baseline review SKIPPED: "

// formatBaselineSkipped renders the run-level SKIPPED line, or "" when there is
// none.
//
// "" is the answer for every run that requested no review, which is what keeps
// `overlay compare` printing exactly what it printed yesterday (R7.2): the field
// is additive and its zero value renders nothing at all.
//
// The text names an operator-configured path, so it is passed as an ARGUMENT and
// never as a format string, exactly like every other piece of text this report
// prints.
func formatBaselineSkipped(report *CompareReport) string {
	if report.BaselineSkipped == "" {
		return ""
	}
	return output.Sprintf(output.Warning, "\n%s%s\n", baselineSkippedLead, report.BaselineSkipped)
}

// splitBaselineAtom splits "category/package" and refuses anything else.
//
// It is spelled here rather than reused from autoupdate.SplitPackageKey because
// internal/overlay imports nothing from internal/autoupdate and keeps it that
// way on purpose — prune.go's RegistryKeys says so in as many words.
//
// The refusals are not ceremony. Both halves are joined onto the tree root
// below, so a "." or a ".." would walk out of the tree and read an ebuild from
// somewhere else entirely, and the registry key syntax those strings can come
// from has no validation on that path. Production reaches this with the
// directory names the overlay scan found, which is what keeps traversal absent
// today; refusing the rest is what keeps it absent tomorrow.
func splitBaselineAtom(atom string) (string, string, error) {
	category, pkg, ok := strings.Cut(atom, "/")
	if !ok {
		return "", "", errMalformedAtom
	}
	for _, part := range []string{category, pkg} {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, `/\`) {
			return "", "", errMalformedAtom
		}
	}
	return category, pkg, nil
}

// errMalformedAtom is what a baseline request that does not name one package
// comes back as. ResolveBaseline wraps it with the atom, so the caller reads
// both what was asked and why it could not be, and can still match on this.
var errMalformedAtom = errors.New("not a category/package atom")

// carriedEbuild is one version the baseline tree carries, and the file carrying
// it. The filename is kept rather than rebuilt from the version, so the Path
// the report names is the one that was actually listed.
type carriedEbuild struct {
	version  string
	filename string
}

// carriedVersions lists the versions of category/pkg that a package directory
// listing carries.
//
// A candidate is recognised BY NAME, through the repository's own ebuild path
// parser, so nothing here is a second notion of what an ebuild filename is.
// That parser also refuses a file whose prefix disagrees with the package
// directory, which is what keeps a neighbour like gst-plugins-qt6-doc out of
// gst-plugins-qt6's version list.
//
// A directory entry is NOT skipped for being a directory, unlike the overlay
// scanner's own walk. Whether the tree carries a version and whether the file
// reads are two questions; the second one is answered later, by reading the one
// candidate that was chosen. Skipping it here would report a version ::gentoo
// plainly carries as one it does not, and silently choose a farther baseline.
func carriedVersions(entries []os.DirEntry, category, pkg string) []carriedEbuild {
	var carried []carriedEbuild
	for _, entry := range entries {
		filename := entry.Name()
		if !strings.HasSuffix(filename, ".ebuild") {
			continue
		}
		// ParsePath wants the category/package/file shape this listing is one
		// directory deep in.
		eb, err := ebuild.ParsePath(filepath.Join(category, pkg, filename))
		if err != nil {
			// A name this repository cannot read a version out of is not a
			// version anything can be measured against.
			continue
		}
		carried = append(carried, carriedEbuild{version: eb.Version, filename: filename})
	}
	return carried
}

// pickBaseline chooses which carried version is the baseline for ours. It is
// only ever called with at least one candidate.
func pickBaseline(carried []carriedEbuild, ours string) carriedEbuild {
	for _, candidate := range carried {
		// R1.1, and it is checked FIRST on purpose: our own version is the
		// answer rather than a candidate among others, so no proximity is
		// computed that could rank a numerically adjacent neighbour beside it.
		//
		// String equality, not ebuild.CompareVersions: the question is whether
		// ::gentoo carries the same ebuild version, not whether two strings
		// order equally. That comparison pads missing components with zeros and
		// reads 1.0 and 1.0.0 as equal, and those are two different ebuilds —
		// reporting the second as ours would print Distance 0, "measured
		// against the same version", over a comparison that is nothing of the
		// kind.
		if candidate.version == ours {
			return candidate
		}
	}

	nearest := carried[0]
	nearestDistance := versionDistance(ours, nearest.version)
	for _, candidate := range carried[1:] {
		distance := versionDistance(ours, candidate.version)
		switch {
		case distance < nearestDistance:
			nearest, nearestDistance = candidate, distance
		case distance == nearestDistance && ebuild.CompareVersions(candidate.version, nearest.version) > 0:
			// Equidistant, so the measurement has nothing left to say and the
			// ORDER decides — the repository's own comparison, taking the newer
			// version, which is the one ::gentoo still maintains and the one a
			// realignment would move toward. Deciding it explicitly is what
			// matters: a baseline that fell out of directory order would make
			// the same run report a different answer on another host.
			nearest = candidate
		}
	}
	return nearest
}

// versionStepWeights is what one step at each version component is worth: a
// major release, a minor series, a patch release. Everything below them shares
// the single smallest step (versionStepWeight).
var versionStepWeights = [...]int{1_000_000, 10_000, 100}

// maxVersionSteps bounds one component's contribution. Only a filename no
// repository really has can reach it; the clamp is there so the arithmetic
// cannot overflow and hand back a NEGATIVE distance, which would read as the
// nearest baseline of all and quietly win.
const maxVersionSteps = 1_000_000_000

// versionDistance reports how far apart two versions are, in RELEASE STEPS.
//
// The unit: one major release counts 1_000_000, one minor series 10_000, one
// patch release 100, and anything finer — a fourth component, a revision, a
// pre-release suffix — counts 1. So a distance below 100 is our own release
// differently revised, below 10_000 the same series, and below 1_000_000 the
// same major line. 1.29.2 to 1.29.1 is 100; 1.29.2 to 1.20.0 is 90_200.
//
// THE UNIT IS THIS STORY'S OWN. Portage orders versions and never measures the
// gap between two of them, and the design fixes no unit either. Only two
// properties are relied on anywhere, and both hold here: the distance is zero
// exactly when the two versions are the same one, and it is monotone, so a
// nearer version never reports a larger number than a farther one. Ordering
// itself stays with ebuild.CompareVersions — this function never decides which
// of two versions is greater, only how far apart they are.
//
// Two versions that differ only below the numeric components — 2.47 and
// 2.47-r1, or 1.0 and 1.0.0 — are one step apart and never zero. Zero means
// "the same version", and a report that cannot tell those two apart cannot tell
// a real divergence from an artefact of comparing against the wrong ebuild.
func versionDistance(a, b string) int {
	if a == b {
		return 0
	}

	first, second := versionComponents(a), versionComponents(b)
	levels := max(len(first), len(second))

	distance := 0
	for level := 0; level < levels; level++ {
		steps := componentAt(first, level) - componentAt(second, level)
		if steps < 0 {
			steps = -steps
		}
		if steps == 0 {
			continue
		}
		if steps > maxVersionSteps {
			steps = maxVersionSteps
		}
		distance += steps * versionStepWeight(level)
	}

	if distance == 0 {
		return 1
	}
	return distance
}

// versionComponents is the numeric head of a version, split into components:
// 1.29.2, 1.29.2-r1 and 1.29.2_rc1 all yield [1 29 2].
//
// It is deliberately simpler than the parser behind ebuild.CompareVersions,
// which is unexported, and it is not a competing one: it feeds a MAGNITUDE and
// never an ordering, so a suffix it drops costs precision below the smallest
// step there is and can never reorder two versions.
func versionComponents(version string) []int {
	head := version
	if end := strings.IndexFunc(version, func(r rune) bool {
		return r != '.' && (r < '0' || r > '9')
	}); end >= 0 {
		// The head stops at the first character that is neither a digit nor a
		// separator: the "-r1" of a revision, the "_rc1" of a pre-release, the
		// trailing letter of a 1.0a.
		head = version[:end]
	}

	parts := strings.Split(head, ".")
	components := make([]int, len(parts))
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil {
			// An empty or unreadable component — "1..2", or a number too large
			// to be one — counts as zero rather than as an error. This measures
			// a distance; refusing to measure one would cost a package its
			// baseline over a typo in a filename.
			n = 0
		}
		components[i] = n
	}
	return components
}

// componentAt reads one component, treating a version that stopped short as
// zero there: on this ladder 1.29 and 1.29.0 are the same point.
func componentAt(components []int, level int) int {
	if level < len(components) {
		return components[level]
	}
	return 0
}

// versionStepWeight is what one step at a component level is worth.
func versionStepWeight(level int) int {
	if level < len(versionStepWeights) {
		return versionStepWeights[level]
	}
	// Below the patch component the granularity stops meaning anything a report
	// would act on, so a fourth component moves the distance by the same single
	// step a revision does.
	return 1
}
