package autoupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

// adoptCandidate takes the candidate already in the kept staged tree at root as
// it stands. Nothing is copied into the tree and no substitution is applied:
// either would overwrite the operator's edit.
//
// The tree is named on the result as soon as its root is accepted, so any
// failure from there on keeps the tree and rewrites its repair bundle, and
// execute arms the record of what the gates said exactly as on the staging
// path. A root that is not this bump's staged tree is refused before it is
// named, so no bundle is written into it.
func (r *applyRun) adoptCandidate(root string) error {
	root, err := r.adoptedTreeRoot(root)
	if err != nil {
		return err
	}
	// Named from here on, so a failure before the candidate is resolved still
	// rewrites the bundle of this tree. No record is written for it: execute
	// arms that only once this function has returned without an error.
	r.result.StagedPath = root
	if err := r.captureInputs(); err != nil {
		return fmt.Errorf("adopting the staged tree %s for %s-%s: %w", root, r.pkg, r.newVersion, err)
	}
	cand, err := stagedCandidate(root, r.pkg, r.newVersion)
	if err != nil {
		return fmt.Errorf("adopting the staged tree %s for %s-%s: %w", root, r.pkg, r.newVersion, err)
	}
	info, err := os.Stat(cand.ebuildPath)
	if err != nil {
		return fmt.Errorf("adopting the staged tree %s for %s-%s: no candidate ebuild: %w", root, r.pkg, r.newVersion, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("adopting the staged tree %s for %s-%s: the candidate %s is not a regular file", root, r.pkg, r.newVersion, cand.ebuildPath)
	}
	r.cand = cand
	r.result.StagedEbuild = cand.ebuildPath

	if err := r.raiseDepthToRecord(root); err != nil {
		return err
	}
	// Checked before the gates rather than after them: nothing the gates run
	// writes into files/, so the answer cannot change, and a refusal here
	// costs no build.
	published, err := publishedCandidate(r.a.overlayPath, r.pkg, r.newVersion)
	if err != nil {
		return fmt.Errorf("adopting the staged tree %s for %s-%s: %w", root, r.pkg, r.newVersion, err)
	}
	candidate, err := os.ReadFile(cand.ebuildPath)
	if err != nil {
		return fmt.Errorf("adopting the staged tree %s for %s-%s: reading the candidate: %w", root, r.pkg, r.newVersion, err)
	}
	diffs, err := packageFileDifferences(cand.pkgDir, published.pkgDir, filesDirReach(candidate))
	if err != nil {
		return fmt.Errorf("adopting the staged tree %s for %s-%s: comparing its package files with the published overlay: %w", root, r.pkg, r.newVersion, err)
	}
	if len(diffs) > 0 {
		return fmt.Errorf("adopting the staged tree %s for %s-%s: %s from the published package directory %s, "+
			"and promotion publishes only the ebuild and its Manifest, so the gates would pass on files the overlay "+
			"does not carry; copy the change into the published package directory by hand (%s), then repair again",
			root, r.pkg, r.newVersion, describeDifferences(diffs), published.pkgDir,
			copyInstructions(cand.pkgDir, published.pkgDir, diffs))
	}
	return nil
}

// describeDifferences names every differing path: breakpad's refusal of
// 2026-11-24 named one patch, and a second one would only have surfaced after
// the operator copied the first and repaired again.
func describeDifferences(diffs []string) string {
	if len(diffs) == 1 {
		return diffs[0] + " differs"
	}
	return fmt.Sprintf("%d paths differ (%s)", len(diffs), strings.Join(diffs, ", "))
}

// copyInstructions spells out, per differing path, what the operator runs: a
// cp for a regular file the staged tree carries, a note for anything else (a
// directory, a published-only file a FILESDIR path of the candidate reaches).
func copyInstructions(stagedPkgDir, publishedPkgDir string, diffs []string) string {
	steps := make([]string, 0, len(diffs))
	for _, rel := range diffs {
		src := filepath.Join(stagedPkgDir, filepath.FromSlash(rel))
		dst := filepath.Join(publishedPkgDir, filepath.FromSlash(rel))
		info, err := os.Lstat(src)
		switch {
		case err != nil:
			steps = append(steps, fmt.Sprintf("%s is only in the published directory, where a ${FILESDIR} path of the candidate would pick it up: restore it in the staged tree or drop it from the overlay", rel))
		case info.IsDir():
			steps = append(steps, fmt.Sprintf("mkdir -p %s", dst))
		case info.Mode().IsRegular():
			steps = append(steps, fmt.Sprintf("cp %s %s", src, dst))
		default:
			steps = append(steps, fmt.Sprintf("%s is not a regular file: reproduce it in the overlay by hand", rel))
		}
	}
	return strings.Join(steps, "; ")
}

// adoptedTreeRoot returns root cleaned when it is the staged tree path of this
// bump under the configured staging root, and an error naming both paths
// otherwise.
func (r *applyRun) adoptedTreeRoot(root string) (string, error) {
	if r.a.stagingRoot == "" {
		return "", fmt.Errorf("adopting the staged tree %s for %s-%s: no staging root is configured, so it cannot be this bump's staged tree", root, r.pkg, r.newVersion)
	}
	want, err := validate.StagedTreePath(r.a.stagingRoot, r.pkg, r.newVersion)
	if err != nil {
		return "", fmt.Errorf("adopting the staged tree %s for %s-%s: %w", root, r.pkg, r.newVersion, err)
	}
	if filepath.Clean(root) != filepath.Clean(want) {
		return "", fmt.Errorf("adopting the staged tree %s for %s-%s: it is not this bump's staged tree %s", root, r.pkg, r.newVersion, want)
	}
	return filepath.Clean(root), nil
}

// raiseDepthToRecord makes the depth the run that kept the tree recorded the
// floor of this run's depth: without an agent to escalate it, the adopted run
// must not validate less than the run whose failure it repairs. A tree with
// no record changes nothing.
func (r *applyRun) raiseDepthToRecord(root string) error {
	record, err := validate.ReadStageRecord(root)
	switch {
	case errors.Is(err, validate.ErrNoStageRecord):
		return nil
	case err != nil:
		return fmt.Errorf("adopting the staged tree %s for %s-%s: reading the depth it was validated at: %w", root, r.pkg, r.newVersion, err)
	}
	if record.Depth <= r.depth.Depth {
		return nil
	}
	r.depth.Depth = record.Depth
	r.depth.Reason = appendDepthReason(r.depth.Reason,
		fmt.Sprintf("raised to %s, the depth the run that kept this staged tree validated at", record.Depth))
	r.result.DepthRequested = r.depth.Depth.String()
	r.result.DepthReason = r.depth.Reason
	return nil
}

// filesDirRef finds each ${FILESDIR} or $FILESDIR reference in an ebuild.
var filesDirRef = regexp.MustCompile(`\$(\{FILESDIR\}|FILESDIR\b)`)

// filesDirReach reports, for a path relative to the package directory, whether
// some ${FILESDIR} word of ebuild could name it or a directory above it. A word
// is read as a shell word: quotes are dropped, every expansion ($V, ${...},
// $(...), {a,b}) may stand for anything including a slash, and a glob
// character stands for what it matches whether quoted or not. Each guess errs
// towards reaching more, never less.
//
// A directory counts as reaching everything below it, because eapply applies
// every patch in a directory it is given and doins -r copies the whole tree.
func filesDirReach(ebuild []byte) func(rel string) bool {
	var words []*regexp.Regexp
	for _, loc := range filesDirRef.FindAllIndex(ebuild, -1) {
		quoted := loc[0] > 0 && ebuild[loc[0]-1] == '"'
		pattern := "^files" + filesDirWordPattern(ebuild[loc[1]:], quoted) + "(/.*)?$"
		re, err := regexp.Compile(pattern)
		if err != nil {
			// Unreachable: filesDirWordPattern quotes every literal. Reach
			// everything rather than let a file through unseen.
			return func(string) bool { return true }
		}
		words = append(words, re)
	}
	return func(rel string) bool {
		for _, re := range words {
			if re.MatchString(rel) {
				return true
			}
		}
		return false
	}
}

// filesDirWordPattern turns the rest of the shell word that follows a FILESDIR
// reference into a regular expression, quoted reports whether that reference
// sits inside double quotes.
func filesDirWordPattern(rest []byte, quoted bool) string {
	var b strings.Builder
	dq, sq := quoted, false
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		switch {
		case sq:
			if c == '\'' {
				sq = false
			} else {
				b.WriteString(regexp.QuoteMeta(string(c)))
			}
		case c == '"':
			dq = !dq
		case c == '\'' && !dq:
			sq = true
		case c == '\\' && i+1 < len(rest):
			i++
			b.WriteString(regexp.QuoteMeta(string(rest[i])))
		case !dq && (c == ' ' || c == '\t' || c == '\n' || strings.IndexByte(";|&<>()", c) >= 0):
			return b.String()
		case c == '$' && i+1 < len(rest) && (rest[i+1] == '{' || rest[i+1] == '('):
			i = skipBalanced(rest, i+1)
			b.WriteString(".*")
		case c == '$' && i+1 < len(rest) && isShellNameByte(rest[i+1]):
			for i+1 < len(rest) && isShellNameByte(rest[i+1]) {
				i++
			}
			b.WriteString(".*")
		case c == '{' && !dq:
			i = skipBalanced(rest, i)
			b.WriteString(".*")
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		case c == '[':
			if end := strings.IndexByte(string(rest[i+1:]), ']'); end >= 0 {
				i += end + 1
			}
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	return b.String()
}

// skipBalanced returns the index of the bracket closing the one at rest[open],
// or the last index when it is never closed.
func skipBalanced(rest []byte, open int) int {
	closer := byte('}')
	if rest[open] == '(' {
		closer = ')'
	}
	depth := 0
	for i := open; i < len(rest); i++ {
		switch rest[i] {
		case rest[open]:
			depth++
		case closer:
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return len(rest) - 1
}

// isShellNameByte reports whether c may appear in a shell variable name.
func isShellNameByte(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// packageMetadataFile is the one file beside files/ that a staged package
// directory may carry and promotion would not publish.
const packageMetadataFile = "metadata.xml"

// packageFileDifferences compares what a staged package directory carries
// beside its ebuild and Manifest with the published package directory, and
// returns every relative path whose presence, kind or content differs, sorted;
// nil when none does.
//
// files/ is compared in both directions, recursively: staging copies it, so any
// difference is an edit. metadata.xml only counts when the staged directory has
// one, because staging never copies it.
//
// A path only the published directory carries counts only when reaches says
// the candidate could pick it up. A bump routinely drops a patch upstream
// merged (edk2 202611 and its fix-gic-hvf patch, 2026-11-23), and the published
// copy must stay for the older ebuilds that still name it. The gates built the
// candidate without the file, so the file left behind changes nothing for it,
// unless a ${FILESDIR} glob or directory of the candidate reaches it. Scoping
// that to the paths each one reaches matters: edk2 installs
// "${FILESDIR}"/descriptors/*.json, which can never pick up a top-level patch.
func packageFileDifferences(stagedPkgDir, publishedPkgDir string, reaches func(rel string) bool) ([]string, error) {
	staged, err := packageFileEntries(stagedPkgDir)
	if err != nil {
		return nil, err
	}
	published, err := packageFileEntries(publishedPkgDir)
	if err != nil {
		return nil, err
	}
	if _, ok := staged[packageMetadataFile]; !ok {
		delete(published, packageMetadataFile)
	}

	paths := make([]string, 0, len(staged)+len(published))
	for rel := range staged {
		paths = append(paths, rel)
	}
	for rel := range published {
		if _, ok := staged[rel]; !ok && reaches(rel) {
			paths = append(paths, rel)
		}
	}
	slices.Sort(paths)
	var diffs []string
	for _, rel := range paths {
		if staged[rel] != published[rel] {
			diffs = append(diffs, rel)
		}
	}
	return diffs, nil
}

// packageFileEntries describes every entry under pkgDir's files/ and its
// metadata.xml, keyed by slash-separated path relative to pkgDir. A missing
// files/ or metadata.xml contributes nothing.
func packageFileEntries(pkgDir string) (map[string]string, error) {
	entries := make(map[string]string)
	if desc, err := describeEntry(filepath.Join(pkgDir, packageMetadataFile)); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
	} else {
		entries[packageMetadataFile] = desc
	}

	filesDir := filepath.Join(pkgDir, "files")
	err := filepath.WalkDir(filesDir, func(path string, _ fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if path == filesDir && errors.Is(walkErr, fs.ErrNotExist) {
				return filepath.SkipDir
			}
			return walkErr
		}
		if path == filesDir {
			return nil
		}
		rel, err := filepath.Rel(pkgDir, path)
		if err != nil {
			return fmt.Errorf("naming %s relative to %s: %w", path, pkgDir, err)
		}
		desc, err := describeEntry(path)
		if err != nil {
			return err
		}
		entries[filepath.ToSlash(rel)] = desc
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("listing %s: %w", filesDir, err)
	}
	return entries, nil
}

// describeEntry returns a value equal for two entries exactly when they are
// the same kind and, for a file, hold the same bytes or, for a link, name the
// same target. It does not follow links.
func describeEntry(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	switch {
	case info.IsDir():
		return "dir", nil
	case info.Mode()&fs.ModeSymlink != 0:
		target, err := os.Readlink(path)
		if err != nil {
			return "", fmt.Errorf("reading the link %s: %w", path, err)
		}
		return "link:" + target, nil
	case info.Mode().IsRegular():
		f, err := os.Open(path) //nolint:gosec // G304: path is under a package directory of the overlay or of its staged tree
		if err != nil {
			return "", fmt.Errorf("opening %s: %w", path, err)
		}
		defer func() { _ = f.Close() }() // read-only: a close error loses nothing
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			return "", fmt.Errorf("reading %s: %w", path, err)
		}
		return "file:" + hex.EncodeToString(h.Sum(nil)), nil
	default:
		return "special:" + info.Mode().Type().String(), nil
	}
}
