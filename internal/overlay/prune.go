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
	"sort"
	"strings"
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
