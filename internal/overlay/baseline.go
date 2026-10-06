package overlay

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/obentoo/bentoolkit/internal/common/ebuild"
)

// baselineRepo is the repository every baseline comes from.
//
// It is a constant rather than an argument because the invocation is already
// constrained to ::gentoo long before this is reached: a realignment run asked
// for any other repository is refused as a usage error naming the reason, so
// that the comparison can never run against GURU while the baseline was read
// from ::gentoo. Naming it on every Baseline is still worth a field: the
// baseline is NAMED and never assumed, and a report that only implies
// where its evidence came from is exactly the assumption.
const baselineRepo = "gentoo"

// Baseline is the ::gentoo ebuild that one of our ebuilds is measured against.
//
// Its zero value is "::gentoo does not carry this package": Found is false and
// nothing is named, because there is nothing to name. No realignment is
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
	// carries it, and the nearest one it does carry otherwise.
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
	// report must not let them look alike — a difference measured against
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
// the baseline and the distance is 0 — the same version is not a candidate
// among others, it is the answer, so it is matched before any proximity is
// computed. Otherwise the nearest version ::gentoo does carry is the baseline,
// and the distance says how far away it is. If ::gentoo carries no version of
// the package, nothing is named and Found stays false.
//
// It reads one directory listing and one ebuild inside gentooTree — no command,
// no host, never git: the local /var/db/repos/gentoo is a shallow clone whose
// history is absent by construction, so the answer is in the tree's CONTENT.
//
// The error return is for a request that cannot be answered AS ASKED — a
// malformed atom, no tree, no version of ours — and nothing else. Every state of
// the tree is a value (Found=false, Unexamined), because the caller derives a
// non-zero exit from an error, and the 84 packages with no counterpart would
// otherwise turn every review into a failed run.
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
		// whichever version happens to sort lowest. The baseline is
		// never a guess, so this is refused instead of answered.
		return Baseline{}, fmt.Errorf("resolving the baseline for %s: no version of ours was given", atom)
	}

	dir := filepath.Join(gentooTree, category, pkg)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// ::gentoo does not carry the package. It is the second most common
			// answer this function gives — 84 of 321 packages — and it is not a
			// failure of any kind.
			return Baseline{}, nil
		}
		// The directory is there and would not be listed. "We could not look" is
		// not "there is nothing there", so it is reported as unexamined rather
		// than as an absent package. Found stays false because no version was
		// ever seen, and no realignment is ever proposed from that.
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
	if _, err := os.ReadFile(baseline.Path); err != nil {
		baseline.Unexamined = fmt.Sprintf("the baseline ebuild could not be read: %v", err)
	}

	return baseline, nil
}

// OtherRepo is a repository other than ::gentoo that carries a package ::gentoo
// does not — one row of the answer for the 84 of 321 overlay packages that have
// no ::gentoo counterpart at all.
//
// CHECKED IS THE FIELD THE TYPE EXISTS FOR. Under a two-way answer — carries it,
// or does not — a repository nobody consulted reports exactly like one that was
// consulted and came back empty. The ~428 registered repositories are
// resolvable BY NAME, but nothing on disk holds most of them, and asking about
// 84 packages across all of them would be thousands of network lookups in a
// stage that is meant to be offline.
//
// Whatever it reports is INFORMATIVE ONLY and never a baseline: a repository
// outside ::gentoo has not been through the same review, its quality varies, and
// where several carry a package the report names each and chooses none.
//
// Every field is comparable, so a nil slice of them is "nothing to say" and
// renders nothing — the same terms every field of the baseline review rides on.
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

// LocalRepo is a repository the caller is prepared to have the report speak
// about: what it is called, where its tree is, and whether its CONTENTS are on
// this machine.
//
// AVAILABLE IS THE CALLER'S ANSWER, NOT A GUESS MADE HERE. The ~428
// repositories of the Gentoo ecosystem are resolvable BY NAME through the
// registry, and almost none of them are on disk; a list built from that registry
// alone would describe 428 trees nobody has. So the caller passes the
// repositories it means the report to speak about and says, for each, whether
// its contents are here — and a repository marked unavailable is never touched.
// That is what keeps this pass offline: the alternative, resolving the
// registry for 84 packages across every registered repository, is thousands of
// network lookups in a stage meant to make none.
type LocalRepo struct {
	// Name is the repository as the registry names it — "guru", not "::guru";
	// the report adds the "::" it prints.
	Name string
	// Path is the root of its tree, the directory <Path>/<category>/<package>
	// would be found under. It is read only when Available is true.
	Path string
	// Available reports that the repository's CONTENTS are on disk at Path.
	//
	// "Registered", "resolvable by name" and "configured with a URL" are none of
	// them available: nothing on this machine holds the tree, and the only true
	// answer the report can give about such a repository is that it was not
	// checked.
	Available bool
}

// OtherRepositories reports which of the given repositories carry atom — one row
// per repository, in the order they were given.
//
// It is the answer for the 84 of the overlay's 321 packages ::gentoo carries no
// version of. Knowing that GURU already packages something we package alone is
// worth reporting; it is also the whole of what this function claims.
//
// A repository whose contents are not here is NOT CHECKED, never one that does
// not carry the package: collapsing the two would let the report assert, on the
// strength of repositories nobody consulted, that a package is Bentoo's alone.
// Such a repository is not stat'ed, listed or resolved.
//
// Where several carry the package each gets a row and none is chosen: a winner
// would quietly create a second baseline. Every row is INFORMATIVE ONLY, held
// structurally — needsRealignVerdict reads Baseline.Found and never these rows.
//
// Per available repository it stats one directory and lists one more, and
// recognises ebuilds by NAME; it reads no ebuild text, because printing another
// repository's ebuild would be offering it as a replacement.
func OtherRepositories(atom string, repos []LocalRepo) []OtherRepo {
	category, pkg, err := splitBaselineAtom(atom)
	if err != nil {
		// The question cannot be asked of anyone, so nothing is reported about
		// anyone. NOT CHECKED rows would be worse than silence here: that state
		// is rendered with a reason — registered, contents not available locally
		// — and it is not the reason this happened.
		return nil
	}

	var others []OtherRepo
	for _, repo := range repos {
		if !reportableNeighbour(repo) {
			continue
		}
		others = append(others, consultRepository(repo, category, pkg))
	}
	return others
}

// reportableNeighbour reports whether this repository is one the rows may speak
// about at all. False says nothing about whether it carries the package: it
// means no row is printed for it either way.
//
// Two repositories are dropped, for opposite reasons. An UNNAMED one cannot be
// printed: the row is rendered by name and an empty one renders "::" followed by
// nothing, which reads as a bug in the report rather than as the empty
// configuration it is. ::gentoo itself is dropped because it is the BASELINE and
// is reported as one by ResolveBaseline; repeated here it would arrive as an
// informative row that "is never a baseline", contradicting the baseline line
// printed directly above it on the same package.
func reportableNeighbour(repo LocalRepo) bool {
	return repo.Name != "" && !strings.EqualFold(repo.Name, baselineRepo)
}

// consultRepository answers for ONE repository: was it read, and what does it
// carry.
//
// CONSULTED is the word the requirement uses and the one this function is named
// for: the three outcomes are three different sentences, and each guard below
// leaves the row in the one that is true. Every path that could not read
// something leaves Checked false, because the only claim this type can make
// about a repository is one backed by a directory listing.
func consultRepository(repo LocalRepo, category, pkg string) OtherRepo {
	row := OtherRepo{Name: repo.Name}

	if !repo.Available || repo.Path == "" {
		// NOT CHECKED, and nothing at all is touched — no stat, no listing, no
		// registry lookup. This is the branch almost every registered repository
		// takes, and it is the reason the pass costs nothing for them.
		return row
	}

	// The ROOT is examined before the package directory, and that stat is the
	// entire difference between the two negatives: <root>/<category>/<package> is
	// missing both when the repository is here and does not carry the package and
	// when the repository is not here at all. Only the first is "we looked and it
	// has nothing".
	//
	// The Portage marker (portageRepoMarker) is deliberately NOT required, unlike
	// LocateBaselineTree. That check exists because a directory mistaken for
	// ::gentoo would report all 321 packages as absent from it; here the tree is
	// one the caller named as available and the worst a bare directory can cost
	// is one informative row nobody acts on.
	info, err := os.Stat(repo.Path)
	if err != nil || !info.IsDir() {
		return row
	}

	entries, err := os.ReadDir(filepath.Join(repo.Path, category, pkg))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Read, and it has nothing: a real negative, which is what Checked with
			// no version means.
			row.Checked = true
		}
		// Anything else — a permission, an I/O error — is "we could not look", and
		// stays NOT CHECKED for the same reason a registered repository does.
		return row
	}

	row.Checked = true
	row.Version = newestCarriedVersion(carriedVersions(entries, category, pkg))
	return row
}

// newestCarriedVersion is the version a row reports for a repository carrying
// several, and "" for one carrying none.
//
// The NEWEST is reported because of the question the row answers — does anyone
// else package this — whose useful answer is the version a reader would go and
// look at. It is chosen with the repository's own comparison rather than from
// the directory listing's order, which is lexical: 0.9.0 sorts after 0.10.0 by
// name and before it by version, and a row that depended on that would report a
// different version on another filesystem.
//
// This is not the ranking the rows forbid. That one is between REPOSITORIES, and
// every repository still gets its own row; this picks one version inside a row
// that has room for exactly one.
func newestCarriedVersion(carried []carriedEbuild) string {
	newest := ""
	for _, candidate := range carried {
		if newest == "" || ebuild.CompareVersions(candidate.version, newest) > 0 {
			newest = candidate.version
		}
	}
	return newest
}

// ErrNoBaselineTree is the run-level outcome: there is no ::gentoo tree to read,
// so NOTHING was examined and the review could not do its job.
//
// It is a SENTINEL because the caller branches on it. This is the one condition
// the command exits non-zero for, and an exit code cannot be derived by
// matching on a message. Everything else this package knows about a baseline is
// a per-package VALUE and never an error — Baseline.Found is false for the 84
// packages ::gentoo does not carry, Baseline.Unexamined says why a baseline that
// exists would not read — so no per-package non-answer can reach the exit code
// through here. The two states are returned through different channels on
// purpose; collapsing them would make one unreadable ebuild fail the whole run.
var ErrNoBaselineTree = errors.New("no ::gentoo tree to compare against")

// portageRepoMarker is the file that makes a directory a Portage repository
// rather than a directory with the right name.
//
// It is what LocateBaselineTree looks for, and what its refusal names. Existence
// of the directory is not enough to go on:
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
// A tree is RECOGNISED and never assumed: the directory has to carry Portage's
// own repository marker, which tells a synced repository from an empty
// directory standing where one is meant to be. What it accepts it returns AS
// GIVEN — cleaned, never replaced — because the report names the tree the review
// actually read.
//
// The refusal wraps ErrNoBaselineTree and NAMES the path and the marker: a
// mistyped path and a repository that was never synced need opposite fixes, and
// the message says which one it found.
//
// It stats two paths and reads nothing at all: no command, no host, no git,
// exactly as ResolveBaseline does and for the same reason.
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
		// error means — and the stat's own error is wrapped with %w BESIDE the
		// sentinel, not rendered with %v.
		//
		// It once carried %v, on the ground that "the sentinel is
		// the only thing a caller is meant to match on". That is a statement
		// about what callers match today and it decided what they CAN match
		// forever: errors.Is found ErrNoBaselineTree and nothing found the
		// filesystem cause, so a caller could not tell a permission denial from
		// any other unexaminable root. Go has taken more than one %w since 1.20,
		// and this module is on 1.26, so both travel and the sentinel match is
		// unchanged.
		return "", fmt.Errorf("%w: %s could not be examined: %w", ErrNoBaselineTree, root, err)
	case !info.IsDir():
		return "", fmt.Errorf("%w: %s is not a directory, so it carries no %s", ErrNoBaselineTree, root, portageRepoMarker)
	}

	markerInfo, err := os.Stat(marker)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "", fmt.Errorf("%w: %s carries no %s, so it is a directory rather than a synced repository", ErrNoBaselineTree, root, portageRepoMarker)
	case err != nil:
		// %w for the cause here too, for the reason given on the root stat above.
		return "", fmt.Errorf("%w: %s could not be examined: %w", ErrNoBaselineTree, marker, err)
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
// no ::gentoo tree could be located, naming what was looked for.
//
// It is a function rather than a field the caller assigns so the wording is
// written once, beside the marker it names. It never records a per-package
// state: an unreadable baseline ebuild is Baseline.Unexamined on that package,
// or else a run that examined 320 packages would report having examined none.
//
// It leaves the outcome as a FINDING as well as a field: a consumer walking
// report.Findings — an export, a count, a second renderer — would otherwise be
// told nothing about a run that compared nothing. It asks EstablishFindings to
// rebuild the list rather than appending, because that is the ONE place
// report.Findings is written and `overlay compare` calls it again later, which
// would discard an appended entry. Rebuilding is idempotent, which makes it safe
// here AND in AnnotateBaseline, which calls both.
func MarkBaselineSkipped(report *CompareReport, lookedFor string) {
	if lookedFor == "" {
		// Nothing was configured, so there is no path to name. Naming an empty one
		// would print a sentence with a hole in it, which reads as a bug in the
		// report rather than as the missing configuration it is.
		//
		// It still SAYS SOMETHING, and the finding below is still established: a
		// review that could not run must speak wherever a review that ran would
		// have, and a run reporting nothing is indistinguishable from one where
		// every package matched ::gentoo.
		report.BaselineSkipped = "no ::gentoo tree was configured, so nothing was compared against ::gentoo"
	} else {
		report.BaselineSkipped = fmt.Sprintf(
			"no ::gentoo tree at %s — looked for its %s marker, so nothing was compared against ::gentoo",
			lookedFor, portageRepoMarker)
	}

	EstablishFindings(report)
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
		// An exact match is checked FIRST on purpose: our own version is the
		// answer rather than a candidate among others, so no proximity is
		// computed that could rank a numerically adjacent neighbour beside it.
		//
		// String equality, not ebuild.CompareVersions: the question is whether
		// ::gentoo carries the same ebuild version, not whether two strings
		// order equally. Two ebuild files are identified by their version text,
		// and PMS equality (1.0 and 1.0-r0, 1.010 and 1.01) is not file
		// identity — reporting the second as ours would print Distance 0,
		// "measured against the same version", over a comparison that is
		// nothing of the kind.
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

// versionValue is where one version sits on the ladder above, read as a number
// in the mixed-radix positional system that ladder already defines: 1.5.1 is
// 1*1_000_000 + 5*10_000 + 1*100 = 1_050_100, and 1.29 is 1_290_000 because a
// version that stopped short is the same point as 1.29.0 (componentAt).
//
// It is a POSITION and never an ordering: ebuild.CompareVersions alone decides
// which version is greater, and everything versionComponents drops — a
// revision, a pre-release suffix, a trailing letter — puts two versions on the
// SAME point on purpose (see versionDistance).
//
// maxVersionSteps is applied per level. It is not for a component that carries
// into the level above (fakeroot's 1.32.2 is well inside a radix of 100) but for
// one no repository publishes, which would overflow the sum into a NEGATIVE
// distance that silently wins pickBaseline's minimum. A component is never
// negative, because versionComponents' head stops before a '-'.
func versionValue(version string) int {
	components := versionComponents(version)
	// Every level the ladder names is read, plus any finer component the version
	// carries. componentAt is what answers for a level the version stopped short
	// of, and reading the value through it is what keeps 1.29 and 1.29.0 on one
	// point without this function holding a second opinion about what a missing
	// component means.
	levels := max(len(components), len(versionStepWeights))

	value := 0
	for level := 0; level < levels; level++ {
		component := componentAt(components, level)
		if component > maxVersionSteps {
			component = maxVersionSteps
		}
		value += component * versionStepWeight(level)
	}
	return value
}

// versionDistance reports how far apart two versions are, in RELEASE STEPS.
//
// It is |versionValue(a) - versionValue(b)|, the gap between POSITIONS on the
// ladder, and deliberately not a sum of per-component differences: from 1.5.1
// that sum reads 1.4.2 as nearer than 1.4.3, the later release, and over the
// overlay's 158 differing packages it chose a wrong baseline eleven times.
//
// The unit: one major release is 1_000_000, one minor series 10_000, one patch
// release 100, anything finer 1. reduceSpan's width bound compares a version
// move's span against a baseline distance, so the two must stay on this one
// ladder. Only two properties are relied on: zero exactly when the versions are
// the same one, and monotone along the version order while every component
// stays below the radix of 100. Ordering stays with ebuild.CompareVersions.
//
// 2.47 and 2.47-r1, or 1.0 and 1.0.0, are one step apart and never zero, so a
// report can tell a real divergence from comparing against the wrong ebuild. A
// revision and a _suffix are invisible BY DESIGN: 2.52.5-r601 and 2.52.5-r410
// are equidistant from any third version, and pickBaseline's tie-break (the
// newer wins) orders them.
func versionDistance(a, b string) int {
	if a == b {
		return 0
	}

	distance := versionValue(a) - versionValue(b)
	if distance < 0 {
		distance = -distance
	}

	if distance == 0 {
		// Two DIFFERENT strings on the same point: a revision, a suffix, a
		// trailing letter, or 1.0 against 1.0.0. One step apart and never zero,
		// because zero is what the report prints as "the same version". This
		// floor is not a rounding convenience — deleting it would make a
		// baseline read as our own ebuild.
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
