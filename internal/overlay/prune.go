package overlay

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/obentoo/bentoolkit/internal/common/provider"
)

// ContentResult is the answer to one question about one package: does deleting
// our copy of it lose anything?
//
// It is a third axis, separate from both CompareStatus and Verdict, because a
// Verdict is a statement about VERSIONS ("::gentoo ships the same or more") and
// this is a statement about BYTES. The two disagree in practice: measured on the
// live overlay on 2026-08-05, `overlay compare` calls 74 packages redundant and
// 8 of them carry real local changes — kwin, plasma-desktop and nodejs among
// them. A removal driven by the verdict alone deletes those changes, which is
// why only content may ever authorise one.
type ContentResult int

const (
	// ContentUnverifiable means no comparison could be made, so nothing is known
	// in either direction. It is the ZERO VALUE deliberately: a Content that was
	// never filled in must not read as permission to delete, and ContentIdentical
	// is the only value that grants one. NotVerified is the zero Verification for
	// the same reason.
	ContentUnverifiable ContentResult = iota
	// ContentIdentical means every version the two trees share is byte-identical
	// and their files/ trees match, so our copy holds nothing that is ours.
	ContentIdentical
	// ContentDiffers means at least one shared version, or one path under files/,
	// is not the same on both sides. Content.Reason names which one.
	ContentDiffers
)

// String returns the plan's word for the result.
func (r ContentResult) String() string {
	switch r {
	case ContentIdentical:
		return "identical"
	case ContentDiffers:
		return "differs"
	case ContentUnverifiable:
		return "unverifiable"
	default:
		// A value with no word — reachable only if a result is added without a
		// case here — reads as "unverifiable" rather than as a bare integer, which
		// keeps an unnamed value on the side that authorises nothing. Matches
		// Verdict.String() in compare.go.
		return "unverifiable"
	}
}

// Content is what one package's content comparison found.
type Content struct {
	// Result is the finding. Only ContentIdentical may authorise a removal.
	Result ContentResult
	// Reason says why the result is ContentUnverifiable, or what differs when it
	// is ContentDiffers. It is empty when the two copies are identical, since
	// there is nothing to explain. When it names a difference it names the
	// VERSION or the PATH, never just the package: "zed differs" sends the
	// operator to diff a directory, "1.0 differs" sends them to a file.
	Reason string
	// Checked lists the versions actually compared byte for byte, for the plan to
	// print. On a difference the list stops at the version that differed, because
	// the comparison stops there too and claiming to have examined the rest would
	// be a false report.
	Checked []string
}

const (
	// pruneNoVersionInCommonReason is the exact wording required when the two
	// trees hold no version to compare (R3.3). It is a single literal in a single
	// place because the operator is asked to tell this apart from the OTHER
	// unverifiable case — a tree that could not be read at all (R2.3) — and a
	// reason that varies by call site cannot be recognised in a report. One sends
	// them to look at the overlay, the other at the provider.
	pruneNoVersionInCommonReason = "no version in common"

	// pruneFilesDir is the only subdirectory whose contents can be ours to lose:
	// the patches and files the ebuild installs through ${FILESDIR}.
	pruneFilesDir = "files"

	pruneEbuildSuffix = ".ebuild"
)

// pruneEbuild pairs a version string with the filename it was listed under.
//
// The filename is kept rather than rebuilt from the version because it is what
// the directory listing actually returned: reading the file back through the
// listed name means no path this function opens was ever assembled from a string
// of our own (R3.5).
type pruneEbuild struct {
	version  string
	filename string
}

// pruneSharedEbuild is one version both trees hold, with the filename each side
// listed it under. The two names are equal in practice — both trees use the same
// "<pkg>-<pv>.ebuild" shape — but each is carried from its own listing so that
// neither path is reconstructed from the other side's string.
type pruneSharedEbuild struct {
	version   string
	ourName   string
	theirName string
}

// unverifiable is the only way this file builds a "no answer" result, so that
// the reason is never attached to a Result that authorises something. Writing
// ContentIdentical where ContentUnverifiable belongs is the one mistake in here
// that deletes work; a named constructor makes it unavailable.
//
// checked carries whatever was compared before the failure, since a partial
// comparison is still worth reporting.
func unverifiable(reason string, checked []string) Content {
	return Content{Result: ContentUnverifiable, Reason: reason, Checked: checked}
}

// PruneVerification reports whether the overlay's copy of pkg carries anything
// its upstream counterpart does not, comparing the two package directories byte
// for byte. It answers the only question that may authorise a removal, and it
// answers it about the WHOLE package: every version the two trees share (R3.1)
// plus the files/ tree, recursively (R3.2).
//
// ourDir and theirDir are the two PACKAGE directories, already resolved by the
// caller — from the overlay scan on our side and from the provider on theirs.
// This function joins no category and no package name of its own, which is what
// keeps registry-sourced traversal structurally absent (R3.5): the same argument
// written at verifyAgainstLocalContent's path construction in compare.go applies
// here, and this is a second site with the same exposure. SplitPackageKey accepts
// "../x" happily and no validation runs on a registry path; the STRUCTURE is
// what keeps traversal out, not a sanitiser. Keep it that way. pkg is used only
// to recognise the "<pkg>-<pv>.ebuild" filename shape — it is never joined into
// a path.
//
// Manifest and metadata.xml are never read, and never listed (R3.4). Manifest
// holds distfile hashes that legitimately vary by version and revision, and
// metadata.xml names a maintainer — ours differs from Gentoo's on all 314
// packages the overlay carries. Comparing either would mark the entire overlay
// divergent, and a criterion that authorises nothing has stopped being one.
//
// Relation to verifyAgainstLocalContent (compare.go): that function answers a
// narrower question for a different command — it compares ONE ebuild, only when
// LocalVersion == RemoteVersion, ignores files/, and its result is only ever
// reported beside a Verdict. This one compares the whole package to decide
// whether a deletion is safe. They agree wherever they overlap, because both are
// bytes.Equal over the same two files: an ebuild pair the compare calls
// identical is one this function also calls identical.
//
// It returns no error by design. Every failure is a Content whose Result is
// ContentUnverifiable and whose Reason names what failed — "no answer" must
// never be reported as "identical", and an error return would invite a caller to
// treat a failed read as a nil-error zero value.
func PruneVerification(ourDir, theirDir string, pkg string) Content {
	ours, err := listPruneEbuilds(ourDir, pkg)
	if err != nil {
		return unverifiable(fmt.Sprintf("cannot read the overlay copy: %v", err), nil)
	}
	theirs, err := listPruneEbuilds(theirDir, pkg)
	if err != nil {
		// Distinct from pruneNoVersionInCommonReason on purpose: both leave us
		// with no upstream ebuild to compare, but a read failure is a provider
		// problem while an empty intersection is ::gentoo having dropped our
		// versions. Collapsing them sends the operator to the wrong tree (R2.3).
		return unverifiable(fmt.Sprintf("cannot read the upstream copy: %v", err), nil)
	}

	// The intersection is computed BEFORE any comparison, and its emptiness is
	// handled before the loop, because a loop over an empty intersection finds no
	// difference and would report success by vacuous truth (R3.3). That is the
	// bug this ordering exists to prevent: no evidence is not evidence of
	// sameness.
	shared := sharedEbuilds(ours, theirs)
	if len(shared) == 0 {
		return unverifiable(pruneNoVersionInCommonReason, nil)
	}

	// EVERY shared version is compared, not the newest one. The tempting
	// shortcut — reuse the version the verdict already looked at — passes a
	// package whose old ebuild is the one carrying our patch (R3.1). The first
	// difference wins, deterministically, because sharedEbuilds preserves the
	// listing order; that order is not version order and does not need to be,
	// since all of them are compared either way. Ordering versions is
	// ebuild.CompareVersions' job and belongs to the verdict, not here.
	checked := make([]string, 0, len(shared))
	for _, e := range shared {
		checked = append(checked, e.version)

		same, err := sameFileBytes(
			filepath.Join(ourDir, e.ourName),
			filepath.Join(theirDir, e.theirName),
		)
		if err != nil {
			// The listing found these files, so a failed read now is an unknown,
			// not a difference — and unknown is the answer that authorises nothing.
			return unverifiable(fmt.Sprintf("cannot compare version %s: %v", e.version, err), checked)
		}
		if !same {
			return Content{
				Result:  ContentDiffers,
				Reason:  fmt.Sprintf("version %s differs", e.version),
				Checked: checked,
			}
		}
	}

	// files/ is compared only after the ebuilds, because an ebuild difference is
	// the more direct thing to report. It is compared at all because an identical
	// ebuild plus a same-named, different-bytes patch applies OUR patch under
	// THEIR ebuild, and nothing about the ebuilds reveals it.
	ourFiles, err := prunePackageFilesDigest(ourDir)
	if err != nil {
		return unverifiable(fmt.Sprintf("cannot read the overlay files/ tree: %v", err), checked)
	}
	theirFiles, err := prunePackageFilesDigest(theirDir)
	if err != nil {
		return unverifiable(fmt.Sprintf("cannot read the upstream files/ tree: %v", err), checked)
	}
	if reason := firstFilesDifference(ourFiles, theirFiles); reason != "" {
		return Content{Result: ContentDiffers, Reason: reason, Checked: checked}
	}

	return Content{Result: ContentIdentical, Checked: checked}
}

// listPruneEbuilds returns the package directory's ebuilds, in the order the
// directory listing produced them (os.ReadDir sorts by filename, so the order is
// deterministic without this function imposing one).
//
// A version here is a FILENAME FRAGMENT and nothing more: the text between the
// "<pkg>-" prefix and the ".ebuild" suffix, never parsed and never ordered. That
// is enough to decide which versions the two trees share, since both sides use
// the same filename shape — the same one the scanner and the provider match. Any
// meaning a version string carries is ebuild.CompareVersions' business.
//
// Manifest and metadata.xml fail the suffix test and are therefore never even
// listed, let alone read (R3.4).
func listPruneEbuilds(dir, pkg string) ([]pruneEbuild, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	prefix := pkg + "-"
	found := make([]pruneEbuild, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, pruneEbuildSuffix) {
			continue
		}
		version := strings.TrimSuffix(strings.TrimPrefix(name, prefix), pruneEbuildSuffix)
		if version == "" {
			// "<pkg>-.ebuild" names no version. Keeping it would put an empty key
			// in the intersection and send two doomed reads after it.
			continue
		}
		found = append(found, pruneEbuild{version: version, filename: name})
	}
	return found, nil
}

// sharedEbuilds returns the versions both trees hold, in our listing's order.
//
// A version only one side has is NOT a difference, and cannot be treated as one:
// 157 of the overlay's packages exist upstream with no version in common
// precisely because we run ahead, so reading "they do not have our 3.0" as a
// divergence would refuse nearly everything. The question is only ever whether
// the copies we can line up hold the same bytes.
func sharedEbuilds(ours, theirs []pruneEbuild) []pruneSharedEbuild {
	theirsByVersion := make(map[string]string, len(theirs))
	for _, e := range theirs {
		theirsByVersion[e.version] = e.filename
	}

	shared := make([]pruneSharedEbuild, 0, len(ours))
	for _, e := range ours {
		theirName, both := theirsByVersion[e.version]
		if !both {
			continue
		}
		shared = append(shared, pruneSharedEbuild{
			version:   e.version,
			ourName:   e.filename,
			theirName: theirName,
		})
	}
	return shared
}

// sameFileBytes reports whether the two files hold identical bytes.
//
// The comparison is raw, matching verifyAgainstLocalContent: a copyright-year
// bump or a stray trailing newline alone reads as a divergence. The failure
// direction is the safe one — a false difference refuses a removal, while a
// false match performs one.
func sameFileBytes(ourPath, theirPath string) (bool, error) {
	ours, err := os.ReadFile(ourPath) //nolint:gosec // path built from a listing of the scanned overlay directory, never from registry input
	if err != nil {
		return false, err
	}
	theirs, err := os.ReadFile(theirPath) //nolint:gosec // path built from a listing of the provider's directory, never from registry input
	if err != nil {
		return false, err
	}
	return bytes.Equal(ours, theirs), nil
}

// prunePackageFilesDigest walks the package's files/ tree recursively and
// returns each entry as relative path -> sha256 of its contents.
//
// An absent files/ is an empty tree, not a failure: 225 of the 314 packages the
// overlay carries have none, and absent on both sides must read as identical.
// Only a files/ that exists and cannot be read is an error, because then we
// genuinely do not know what is in it.
//
// The digest is keyed by relative path so that a path present on one side only
// is visible as a difference (R3.2) — the sha256 answers "same bytes?" and the
// key answers "same tree?", and both questions have to be asked.
func prunePackageFilesDigest(dir string) (map[string]string, error) {
	root := filepath.Join(dir, pruneFilesDir)
	digest := make(map[string]string)

	if _, err := os.Lstat(root); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return digest, nil
		}
		return nil, err
	}

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable subtree is no evidence either way, so it propagates as
			// unverifiable rather than being skipped into a false "identical".
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if !d.Type().IsRegular() {
			// A symlink is recorded by its type and never dereferenced: its target
			// is not content this directory carries, and following it would read
			// somewhere the walk never entered. Its presence still counts, so a
			// link on one side only is a difference like any other path.
			digest[rel] = "non-regular:" + d.Type().String()
			return nil
		}
		data, readErr := os.ReadFile(path) //nolint:gosec // path produced by walking a package directory the caller resolved, never from registry input
		if readErr != nil {
			return readErr
		}
		sum := sha256.Sum256(data)
		digest[rel] = hex.EncodeToString(sum[:])
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	return digest, nil
}

// firstFilesDifference returns a reason naming the first differing path under
// files/, or "" when the two trees match. Paths are visited in sorted order so
// that "first" means the same thing on every run.
//
// A path on ONE SIDE ONLY is a difference in both directions (R3.2). Ours alone
// is the obvious case: the file would be deleted with the directory. Theirs
// alone matters too — the shared ebuilds are already known byte-identical at
// this point, so an upstream file we lack means our copy may not even build what
// theirs does. Either way it is a divergence for a human to look at, and this
// function is not in the business of deciding which absence is harmless.
func firstFilesDifference(ours, theirs map[string]string) string {
	paths := make([]string, 0, len(ours)+len(theirs))
	for p := range ours {
		paths = append(paths, p)
	}
	for p := range theirs {
		if _, both := ours[p]; !both {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)

	for _, p := range paths {
		ourSum, weHave := ours[p]
		theirSum, theyHave := theirs[p]
		named := filepath.Join(pruneFilesDir, p)
		switch {
		case weHave && !theyHave:
			return fmt.Sprintf("%s exists only in the overlay copy", named)
		case !weHave && theyHave:
			return fmt.Sprintf("%s exists only upstream", named)
		case ourSum != theirSum:
			return fmt.Sprintf("%s differs", named)
		}
	}
	return ""
}

// PruneClass is what the EVIDENCE says about one package.
//
// It is deliberately not "what this run will do about it" — that is
// PrunePlan.Eligible, and the two are kept apart so the plan can print a
// diverging package, with its reason, on a run that will not touch it (R1.4).
// The operator's next question about a package missing from the output is always
// "why not this one", and a report that answers it is the whole point of having
// three buckets instead of one list.
type PruneClass int

const (
	// PruneRefused means no run of this command may remove the package: its
	// verdict is not redundant, or its content could not be compared at all. It is
	// the ZERO VALUE for the same reason ContentUnverifiable is: a PrunePlan
	// nobody filled in must not read as permission to delete.
	PruneRefused PruneClass = iota
	// PruneIdentical means the verdict is redundant, no registry entry declares a
	// patch, and every byte the two trees share matches. It is the only class a
	// default run removes.
	PruneIdentical
	// PruneDiverging means the package is redundant BY VERSION but something of
	// ours is in it — either a declared `patched` entry or an undeclared
	// difference the byte comparison found. Only --include-patched may act on it
	// (R4.1).
	PruneDiverging
)

// String returns the plan's word for the class.
func (c PruneClass) String() string {
	switch c {
	case PruneIdentical:
		return "identical"
	case PruneDiverging:
		return "diverging"
	case PruneRefused:
		return "refused"
	default:
		// A class with no word — reachable only if one is added without a case
		// here — reads as "refused" rather than as a bare integer, which keeps an
		// unnamed value on the side that authorises nothing. Matches
		// ContentResult.String() above and Verdict.String() in compare.go.
		return "refused"
	}
}

// PrunePlan is everything the report, the confirmation and the executor need
// about one package: what would go, and what this run may do about it.
type PrunePlan struct {
	// Category and Package come from the compare result, which comes from the
	// overlay scan. They are the only two names any path here is built from
	// (R3.5).
	Category string
	Package  string

	// Versions is every version in OUR directory — not the versions that were
	// compared. Removing the directory removes all of them, including the ones
	// ::gentoo never carried, and a plan listing only the compared ones would
	// understate the removal.
	Versions []string

	// Files is every file under the package directory, as a path RELATIVE to it:
	// the plan prints category/package as its heading, so repeating it on every
	// line is noise.
	//
	// Manifest and metadata.xml ARE listed even though the comparison ignores them
	// (R3.4). They are not evidence about whether the copy is ours, but the
	// removal deletes them all the same, and a plan that hid them would understate
	// what it is about to do.
	//
	// Directories are not listed as entries of their own: the overlay is a git
	// tree, git cannot carry an empty directory, so every directory the removal
	// takes has at least one file listed under it.
	Files []string

	// RegistryKeys are the registry entries that would be deleted with the
	// package, exactly as the caller supplied them — every one of them, since 90
	// of 321 atoms carry more than one entry and leaving one behind orphans it.
	// This package never looks them up: internal/overlay does not know what TOML
	// is, which is the layer rule 025 established.
	RegistryKeys []string

	// Eligible is what THIS RUN may do about the package: true only when the
	// class, the flags and the evidence all allow a removal. A false Eligible on a
	// planned package is not an error — it is the plan saying "here it is, and
	// here is why I am leaving it alone".
	Eligible bool

	// Class is what the evidence says. See PruneClass.
	Class PruneClass

	// Reason says why the package was refused, or what diverges. It is empty only
	// for PruneIdentical, where there is nothing to explain; every Refused and
	// every Diverging plan carries one.
	Reason string
}

// PruneBatch is the plan for a whole run, split by what may be done about each
// package.
//
// The split is why Refused carries a reason per package: a plan that silently
// omitted the packages it will not touch reads as "these do not exist".
type PruneBatch struct {
	// Identical is the default batch — redundant by version, undeclared, and
	// byte-identical. Every plan in it is Eligible.
	Identical []PrunePlan
	// Diverging holds the packages that carry something of ours, declared or not.
	// They are planned and printed either way; they are Eligible only under
	// --include-patched (R4.1).
	Diverging []PrunePlan
	// Refused holds everything no flag may act on, each with its reason.
	Refused []PrunePlan
}

// PruneOptions is what the caller knows that this package cannot work out for
// itself.
type PruneOptions struct {
	// OverlayPath is the root of the overlay being pruned. Our side of every
	// comparison is OverlayPath/category/package.
	OverlayPath string
	// IncludePatched is the --include-patched flag: the operator saying out loud
	// that they accept discarding local work (R4.1). It moves nothing between
	// classes — it only decides whether the diverging batch is Eligible.
	IncludePatched bool
	// RegistryKeys maps "category/package" to every registry key for that atom.
	// It is SUPPLIED by the caller and never looked up here, which is what keeps
	// this package free of an import edge to internal/autoupdate.
	//
	// Nothing is ever joined from one of these keys into a filesystem path.
	// SplitPackageKey accepts "../x" and no validation runs on that path; the
	// structure is what keeps traversal absent, not a sanitiser.
	RegistryKeys map[string][]string
}

// pruneNoLocalTreeReason is the refusal for a provider with nothing on disk to
// compare against (design D6). The wording here is deliberately about the
// PROVIDER rather than about a flag: the command layer owns what it tells the
// operator to do about it, and internal/overlay names no CLI flag.
const pruneNoLocalTreeReason = "the provider exposes no local tree to compare against"

// PlanPrune turns the compare's results into three buckets, each package
// carrying the reason it is in the one it is in.
//
// The filter order — verdict, then declaration, then content — is the
// requirement, not an implementation detail:
//
//  1. A verdict other than redundant refuses the package and its content is
//     NEVER compared (R2.5). That is a correctness rule — a `keep` package is one
//     the overlay carries on purpose, and "identical to ::gentoo right now" is not
//     an argument against that — and it is also what keeps a scan cheap: 240 of
//     the overlay's 314 packages are not redundant, and reading all of them would
//     be most of the work of an answer nobody asked for.
//  2. A `patched` declaration moves the package out of the removable batch
//     REGARDLESS of what the bytes say (R2.4). A declaration that has gone stale
//     is 025's finding to report and the operator's to clear; overruling it here
//     on our own reading of the bytes would settle that disagreement in the one
//     direction that cannot be undone.
//  3. Only then does content decide (R2.1, R2.2, R2.3).
//
// Every step can only move a package AWAY from a removal, never towards one,
// which is why they compose: the answer is the strictest step's answer.
//
// The function removes nothing and takes no confirmation callback. R1.1 —
// "without --apply, remove nothing" — is a property of this structure rather than
// of a well-placed condition: the planner has no capability to delete (design
// D3). Its only I/O is reading the two trees in order to compare them.
//
// It returns no error, for the same reason PruneVerification does not: a failure
// is a refusal with a reason, and an error return would invite a caller to treat
// a failed read as an empty batch — which reads as "nothing qualified" when the
// truth is "nothing was examined" (R1.5).
func PlanPrune(results []CompareResult, prov provider.Provider, opts PruneOptions) PruneBatch {
	// The capability check runs ONCE, before the loop, and it asks the provider
	// nothing: a type assertion is not a call, so it cannot break R2.5's promise
	// that a non-redundant package is never looked up. A provider that fails it is
	// API-only and has no tree to read (design D6); the failed assertion leaves a
	// nil interface, which planPrunePackage tests for rather than dereferences, so
	// such a run yields refusals instead of a panic.
	dirProv, _ := prov.(provider.PackageDirProvider)

	var batch PruneBatch
	for _, result := range results {
		plan := planPrunePackage(result, dirProv, opts)
		switch plan.Class {
		case PruneIdentical:
			batch.Identical = append(batch.Identical, plan)
		case PruneDiverging:
			batch.Diverging = append(batch.Diverging, plan)
		case PruneRefused:
			batch.Refused = append(batch.Refused, plan)
		default:
			// A class with no bucket would vanish from the report entirely, which is
			// the one outcome R1.4 forbids. It is downgraded to a refusal first, so
			// an unrecognised class cannot arrive in a bucket still carrying
			// Eligible. The raw integer is deliberate: String() would print the word
			// for a class this branch exists because it does not recognise.
			plan.decide(refusedPrune(fmt.Sprintf("unrecognised prune class %d", plan.Class)))
			batch.Refused = append(batch.Refused, plan)
		}
	}
	return batch
}

// planPrunePackage decides one package's fate and describes what removing it
// would take.
//
// The gates are in the order PlanPrune's comment fixes, and each one returns:
// nothing below a gate runs for a package the gate already refused. That is what
// makes R2.5 observable as an ABSENCE OF WORK — a non-redundant package produces
// no directory listing, no provider lookup and no read — rather than as a message
// printed after the work was done anyway.
func planPrunePackage(result CompareResult, dirProv provider.PackageDirProvider, opts PruneOptions) PrunePlan {
	plan := PrunePlan{Category: result.Category, Package: result.Package}

	// STEP 1 — the verdict, before anything is looked at.
	if result.Verdict != VerdictRedundant {
		plan.decide(refusedPrune(fmt.Sprintf("verdict is %s, not redundant", result.Verdict)))
		return plan
	}

	// Past the gate the package is one some run could act on, so it gets an
	// inventory: R1.4 asks the plan to say exactly what would go. The inventory is
	// taken from OUR directory only — the upstream tree is evidence, never
	// something this command removes.
	ourDir := filepath.Join(opts.OverlayPath, result.Category, result.Package)
	versions, files, err := prunePackageInventory(ourDir, result.Package)
	if err != nil {
		// A directory we cannot list is one whose extent we do not know, and a plan
		// that understates what it would delete is worse than no plan. Refuse before
		// the provider is asked anything.
		plan.decide(refusedPrune(fmt.Sprintf("cannot list the overlay copy: %v", err)))
		return plan
	}
	plan.Versions = versions
	plan.Files = files
	plan.RegistryKeys = pruneRegistryKeys(opts.RegistryKeys, result.Category, result.Package)

	if dirProv == nil {
		plan.decide(refusedPrune(pruneNoLocalTreeReason))
		return plan
	}
	// LocalPackagePath rather than a path joined out here, matching
	// verifyAgainstLocalContent: it guards with the provider's own repository
	// setup, and it reports a package the upstream tree does not carry as an error
	// instead of as a path that fails to open later.
	theirDir, err := dirProv.LocalPackagePath(result.Category, result.Package)
	if err != nil {
		plan.decide(refusedPrune(fmt.Sprintf("cannot locate the upstream copy: %v", err)))
		return plan
	}

	plan.decide(classifyPrune(result, PruneVerification(ourDir, theirDir, result.Package), opts.IncludePatched))
	return plan
}

// pruneDecision is the answer to "what may this run do about this package",
// separated from the plan so the decision table below is a pure function of its
// inputs and reads as a table.
//
// Its zero value is a refusal with no reason, which is the safe shape: a decision
// nobody filled in authorises nothing.
type pruneDecision struct {
	class    PruneClass
	eligible bool
	reason   string
}

// decide writes a decision onto the plan. The three fields move together and
// only through here, so a plan cannot end up Eligible under a class that forbids
// it.
func (p *PrunePlan) decide(d pruneDecision) {
	p.Class = d.class
	p.Eligible = d.eligible
	p.Reason = d.reason
}

// refusedPrune is the only way this file builds a refusal, so that Eligible is
// never left true beside one.
func refusedPrune(reason string) pruneDecision {
	return pruneDecision{class: PruneRefused, eligible: false, reason: pruneStatedReason(reason)}
}

// divergingPrune builds the class that --include-patched exists for: the finding
// stands either way, the flag only decides whether this run may act on it.
func divergingPrune(includePatched bool, reason string) pruneDecision {
	return pruneDecision{class: PruneDiverging, eligible: includePatched, reason: pruneStatedReason(reason)}
}

// pruneStatedReason enforces the invariant every Refused and every Diverging
// plan depends on: a package the plan sets apart says why.
//
// No call site produces an empty reason today; the guard sits at the two
// constructors so that none can. An empty one would print as a package listed
// under a heading with nothing beside it, which reads as noise rather than as
// the missing explanation it is — and the operator's next question about a
// package the run will not touch is always "why not this one".
func pruneStatedReason(reason string) string {
	if reason == "" {
		return "no reason recorded (this is a bug in the prune planner)"
	}
	return reason
}

// classifyPrune applies R2's table to a package the verdict already let through.
// It is read TOP TO BOTTOM, first match wins:
//
//  1. content unverifiable  -> Refused, never eligible (R2.3)
//  2. `patched` declared    -> Diverging, eligible only with --include-patched (R2.4)
//  3. content differs       -> Diverging, likewise (R2.2)
//  4. identical, undeclared -> Identical, eligible (R2.1)
//
// Row 1 sits above row 2 even though the declaration is the earlier FILTER,
// because the two can hold at once and then the stricter class has to win. R4.1
// names R2.2 and R2.4 as the refusals --include-patched may override and
// pointedly not R2.3: a flag can say "I know I am discarding local work", and no
// flag can say "I know I am discarding work nobody could look at". Putting a
// patched-and-unverifiable package in Diverging would make it removable under the
// flag, so it goes to Refused.
//
// Row 2 above row 3 is R2.4's "regardless of content": identical bytes never
// promote a declared package into the removable batch, and when the bytes differ
// as well the reason names both.
func classifyPrune(result CompareResult, content Content, includePatched bool) pruneDecision {
	if content.Result == ContentUnverifiable {
		// content.Reason is passed through verbatim rather than rephrased: R2.3 asks
		// the operator to be told WHICH of the two unverifiable reasons applies, and
		// a reason reworded per call site cannot be recognised in a report.
		return refusedPrune(content.Reason)
	}

	if result.Patched {
		return divergingPrune(includePatched, patchedPruneReason(result, content))
	}

	// A switch, not an "everything else is identical" fallthrough: a ContentResult
	// added later must not reach the removable batch by elimination. This is the
	// one mistake in this file that deletes work.
	switch content.Result {
	case ContentIdentical:
		// No reason: there is nothing to explain about a copy that holds nothing of
		// ours, and the bucket already says what this is.
		return pruneDecision{class: PruneIdentical, eligible: true}
	case ContentDiffers:
		return divergingPrune(includePatched, content.Reason)
	default:
		return refusedPrune("the content check returned no usable answer")
	}
}

// patchedPruneReason says why a declared package is set apart, and what actually
// differs when the bytes differ too.
//
// Both halves are there because the operator confirming a diverging batch is
// being asked to discard both: the declaration says why the copy is ours, the
// content says what is currently in it. When the content is identical the
// declaration may well be stale — but that is 025's finding to report, so this
// reason does not editorialise about it.
func patchedPruneReason(result CompareResult, content Content) string {
	reason := "declared patched"
	if result.PatchedBy != "" {
		reason += " by " + result.PatchedBy
	}
	if result.PatchedReason != "" {
		reason += ": " + result.PatchedReason
	}
	if content.Result == ContentDiffers && content.Reason != "" {
		reason += " (" + content.Reason + ")"
	}
	return reason
}

// prunePackageInventory lists what removing dir would take: every version in it,
// and every file under it as a path relative to dir.
//
// It answers R1.4 for one package and nothing else — no comparison, no
// judgement. The two traversals are deliberate: listPruneEbuilds owns the rule
// for what counts as a version, and duplicating that rule inside the walk would
// give the plan a second, drifting definition of the same thing.
func prunePackageInventory(dir, pkg string) (versions, files []string, err error) {
	ebuilds, err := listPruneEbuilds(dir, pkg)
	if err != nil {
		return nil, nil, err
	}
	versions = make([]string, 0, len(ebuilds))
	for _, e := range ebuilds {
		versions = append(versions, e.version)
	}

	files, err = prunePackageFiles(dir)
	if err != nil {
		return nil, nil, err
	}
	return versions, files, nil
}

// prunePackageFiles walks the package directory and returns every file under it
// as a path relative to the directory, in the lexical order WalkDir visits, so
// two runs over the same tree print the same list.
//
// Everything is listed, including Manifest, metadata.xml and the whole files/
// tree: what the COMPARISON ignores (R3.4) and what the REMOVAL deletes are
// different sets, and this is the second one.
//
// A symlink is listed by its path and never followed — removing the package
// removes the link, not whatever it points at, and following one would walk a
// tree outside the overlay.
func prunePackageFiles(dir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// An unreadable subtree means the inventory would be incomplete, and an
			// incomplete inventory understates the removal. It propagates, and the
			// caller turns it into a refusal.
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}
		files = append(files, rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

// pruneRegistryKeys returns the caller's keys for one atom.
//
// The slice is COPIED rather than aliased: a plan is printed, then confirmed,
// then executed, and a slice shared with the caller's map would let a later edit
// there change what the operator already agreed to. slices.Clone returns nil for
// an empty input, so a package with no registry entry carries no keys rather than
// an empty list pretending to be one.
func pruneRegistryKeys(byAtom map[string][]string, category, pkg string) []string {
	return slices.Clone(byAtom[category+"/"+pkg])
}

// PruneResult is what happened to one plan the executor attempted.
type PruneResult struct {
	// Plan is the plan this result is about, carried whole so the caller can name
	// the package, and reach its RegistryKeys, without keeping its own index into
	// the batch it handed over.
	Plan PrunePlan

	// Removed is true only when os.RemoveAll returned without error. It is the
	// caller's authority to delete the package's registry entries (design D7):
	// the entry goes only for a package whose directory actually went.
	Removed bool

	// Err is why the removal did not happen — a refusal by the guard, or what
	// os.RemoveAll returned.
	//
	// Err is non-nil EXACTLY when Removed is false. The two are one fact seen from
	// two sides, and a result where they disagree leaves the caller unable to say
	// whether the package is still on disk — while its next act is deleting that
	// package's registry entries. A "removed with an error" would orphan the
	// directory; a "not removed, no error" would orphan the entry.
	Err error
}

// ExecutePrune carries out the removals the plan authorised, and no others.
//
// It takes the WHOLE batch, Refused included, because the caller has one batch
// and no reason to split it. Eligible is the authorisation; the BUCKET IS NOT. A
// loop that walked every bucket and removed what it found would delete exactly
// the packages the run refused — the worst bug this file can have — so the test
// is on the plan's own permission and never on the field it was filed under.
//
// A plan that is not Eligible produces NO result. The results are a record of
// ATTEMPTS: the plan report has already told the operator about the refused
// packages and why (R1.4), and the CLI turns any result carrying an Err into a
// non-zero exit (R5.5). Emitting a refusal here as a failed result would make
// every run that merely SAW a non-redundant package exit non-zero, which says
// "something went wrong" about a scan that worked perfectly.
//
// One failure records and continues (R5.5). A batch that aborts halfway leaves
// the overlay in a state no plan described — some packages gone, some not, and
// nothing on screen saying where the line was drawn. Every attempt gets its own
// result and the caller decides the exit code.
//
// It returns no error of its own, for the reason PlanPrune and PruneVerification
// do not: the answer is per package, and a single error return would either hide
// the successes or invite a caller to read a nil as "all of them went".
//
// It does not touch packages.toml. The registry edit happens once, afterwards, in
// the CLI, and only for the atoms whose directory actually went (design D7) —
// internal/overlay does not know what TOML is, which is the layer rule 025
// established.
func ExecutePrune(batch PruneBatch, opts PruneOptions) []PruneResult {
	// The root is resolved ONCE, and the per-plan guard measures every target
	// against that one answer. Re-resolving it per package would not make the check
	// fresher in any way that matters — the guard's freshness lives in the Lstat of
	// the TARGET, which does run before every single removal — and it would make it
	// weaker: if the root were swapped for a symlink mid-batch, re-resolving would
	// follow the swap and carry the rest of the removals somewhere the plan was
	// never made about, while the resolved-once root keeps them in the tree that
	// was actually compared.
	//
	// Its failure is still reported per plan, because the caller counts failed
	// attempts and "the root was unusable" is a different report from "nothing was
	// planned" (R1.5's distinction, one layer down).
	root, rootErr := pruneOverlayRoot(opts.OverlayPath)

	// Every bucket is walked, in a fixed order, so that two runs over the same
	// batch report in the same order. Refused is walked too — not because anything
	// in it can be removed, but because reaching it and finding nothing eligible is
	// the property this loop is asserting, and a loop that skipped the bucket would
	// leave that assertion to a reader's memory.
	var results []PruneResult
	for _, plans := range [][]PrunePlan{batch.Identical, batch.Diverging, batch.Refused} {
		for _, plan := range plans {
			if !plan.Eligible {
				continue
			}
			if rootErr != nil {
				results = append(results, PruneResult{Plan: plan, Err: rootErr})
				continue
			}
			results = append(results, executePrunePlan(root, plan))
		}
	}
	return results
}

// pruneOverlayRoot resolves the root every removal is measured against:
// absolute, with every symlink component already followed, so that the
// containment test compares two paths of the same kind. Comparing a resolved
// target against an unresolved root is how a containment check passes for a path
// that is not contained.
//
// The EMPTY CHECK IS FIRST, and it is not made redundant by the checks below.
// filepath.Abs("") returns the CURRENT WORKING DIRECTORY with a nil error, so an
// unset OverlayPath does not fail — it silently renames "the overlay" to
// "wherever this process was started". <cwd>/<category>/<package> is a real
// directory two levels below <cwd>, so it would satisfy every other guard in this
// file; for a tool run from inside a checkout, the directory it takes is
// somebody's source tree.
func pruneOverlayRoot(overlayPath string) (string, error) {
	if overlayPath == "" {
		return "", errors.New("no overlay path was given, so there is no root to remove anything from")
	}
	abs, err := filepath.Abs(overlayPath)
	if err != nil {
		return "", fmt.Errorf("cannot resolve the overlay root %q: %w", overlayPath, err)
	}
	root, err := filepath.EvalSymlinks(abs)
	if err != nil {
		// An overlay root that does not resolve is one nothing can be proven
		// against, and every removal in this file is authorised by being inside it.
		return "", fmt.Errorf("cannot resolve the overlay root %q: %w", abs, err)
	}
	return root, nil
}

// executePrunePlan removes one package directory, after proving that the path it
// is about to hand os.RemoveAll is the package the plan named (R5.1).
//
// The proof is redone here even though PlanPrune built the same path from the
// overlay scan, and even though D2 keeps registry keys out of path construction
// everywhere else. That is not distrust of the planner. The plan arrives as a
// STRUCT, and a struct can be built by anything: Category and Package are two
// strings on it, and nothing in the type system says whoever filled them walked
// the overlay. SplitPackageKey accepts "../x" happily and no validation runs on a
// registry path — the structure is what keeps traversal absent, not a sanitiser.
//
// Everywhere else in this repository a wrong path opens the wrong file and
// something fails. Here it calls os.RemoveAll. The guard exists to make the class
// of mistake unrepresentable at the point of no return.
func executePrunePlan(root string, plan PrunePlan) PruneResult {
	target, err := prunePackageTarget(root, plan)
	if err != nil {
		return PruneResult{Plan: plan, Err: err}
	}

	// One RemoveAll on the package DIRECTORY, which is R5.1 entire: every ebuild,
	// Manifest, metadata.xml and the whole files/ tree go with it. PrunePlan.Files
	// is a report of what that costs and is never joined back into a path — walking
	// it would rebuild, file by file, a path this function just finished proving.
	if err := os.RemoveAll(target); err != nil {
		return PruneResult{Plan: plan, Err: fmt.Errorf("cannot remove %s/%s: %w", plan.Category, plan.Package, err)}
	}
	return PruneResult{Plan: plan, Removed: true}
}

// prunePackageTarget returns the directory to remove, or the reason it will not
// be removed. root must already be absolute and symlink-resolved.
//
// The rule is exact: the target must resolve to a DIRECT CHILD TWO LEVELS DOWN
// from the root — <root>/<category>/<package>, no more, no fewer, no "..". Two
// levels is not a stylistic preference; it is the whole shape of a Portage
// overlay, and any other depth means the plan is describing something that is not
// a package.
func prunePackageTarget(root string, plan PrunePlan) (string, error) {
	if plan.Category == "" || plan.Package == "" {
		// filepath.Join DROPS empty elements, so a plan missing either name joins to
		// a bare category directory or to the overlay root itself — and RemoveAll on
		// the root takes all 314 packages. The containment test below catches both
		// (one component, or "."), but a plan that names no package is refused by
		// its own name rather than by the shape of the path it happened to produce.
		return "", fmt.Errorf("the plan names no package (category %q, package %q)", plan.Category, plan.Package)
	}

	target := filepath.Join(root, plan.Category, plan.Package)

	// Lstat FIRST, before anything resolves the target, because the answer this
	// asks for is destroyed by resolution: EvalSymlinks would report the link's
	// destination and there would be nothing left to refuse.
	info, err := os.Lstat(target)
	if err != nil {
		// A target that cannot be stat'ed is one whose containment cannot be proven.
		// It is also the case os.RemoveAll answers with nil — it treats an absent
		// path as success — so letting it through would report a removal that never
		// happened and hand the caller authority to delete the registry entries of a
		// package that may still be on disk under a name we got wrong.
		return "", fmt.Errorf("cannot inspect %s/%s: %w", plan.Category, plan.Package, err)
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		// A symlinked package directory is refused, never followed. os.RemoveAll on
		// a symlink removes the LINK — but PruneVerification compared the tree the
		// link points at, which may sit anywhere on the filesystem, so the
		// authorisation and the deletion would be about two different things: the
		// real tree survives and the overlay's reference to it is destroyed.
		// Following it instead is worse — a deletion outside the overlay, authorised
		// by a comparison of a directory the operator never saw. Neither is
		// acceptable, so the package is left exactly as it was.
		return "", fmt.Errorf("%s/%s is a symlink, not a package directory; the comparison that authorised this removal was about the tree it points at, which is not what deleting the link would remove", plan.Category, plan.Package)
	}
	if !info.IsDir() {
		// The plan describes a directory and R5.1 removes one. Whatever this is, the
		// evidence collected about it was collected about something else.
		return "", fmt.Errorf("%s/%s is not a directory (mode %s)", plan.Category, plan.Package, info.Mode())
	}

	// The final component is now known not to be a symlink, but the components
	// ABOVE it may still be: a symlinked category directory would carry the removal
	// out of the overlay while every string involved still looked like a package
	// name. So containment is tested on the RESOLVED path — and the resolved path
	// is what gets removed, so that the thing checked is the thing that goes.
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		return "", fmt.Errorf("cannot resolve %s/%s: %w", plan.Category, plan.Package, err)
	}

	rel, err := filepath.Rel(root, resolved)
	if err != nil {
		return "", fmt.Errorf("cannot place %s/%s relative to the overlay root %s: %w", plan.Category, plan.Package, root, err)
	}

	// The comparison has to be on the RESOLVED target against the RESOLVED root,
	// never on the raw strings: filepath.Join already cleaned "../outside" away
	// while building the path, so by the time anything can be compared the
	// traversal is no longer visible as text — only as a location.
	//
	// All four conditions are load-bearing and none implies another:
	//
	//   - TWO COMPONENTS is the shape of a Portage overlay. One is a category
	//     directory, three is something inside a package, zero (rel == ".") is the
	//     overlay root and all 314 packages in it.
	//   - NO "..", which the count does NOT imply: filepath.Rel answers
	//     "../outside" for a sibling of the root, two components exactly like a
	//     legitimate "<category>/<package>".
	//   - THE PLAN'S OWN NAMES, because "inside the overlay" is not the promise —
	//     "the package the operator was shown" is. A symlinked category directory
	//     pointing at another category inside the same root passes every test above
	//     while removing a package nobody named. This is the symlink argument again
	//     one level up, and the names are what tell the two apart. It does not make
	//     the ".." test redundant either: a plan literally carrying Category ".."
	//     and Package "outside" matches its own names perfectly.
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) != 2 || slices.Contains(parts, "..") || parts[0] != plan.Category || parts[1] != plan.Package {
		return "", fmt.Errorf(
			"%s/%s resolves to %s, which is not that package directly inside the overlay root %s",
			plan.Category, plan.Package, resolved, root,
		)
	}

	return resolved, nil
}
