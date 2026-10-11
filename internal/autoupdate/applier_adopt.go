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
	"slices"

	"github.com/obentoo/bentoolkit/internal/autoupdate/validate"
)

// adoptCandidate takes the candidate already in the kept staged tree at root as
// it stands. Nothing is copied into the tree and no substitution is applied:
// either would overwrite the operator's edit.
//
// The tree is named on the result before any gate runs, so execute arms the
// record of what the gates said exactly as on the staging path, and a failure
// keeps the tree and rewrites its repair bundle. A root that is not this bump's
// staged tree, or that holds no candidate, is refused before it is named, so no
// bundle is written into it.
func (r *applyRun) adoptCandidate(root string) error {
	root, err := r.adoptedTreeRoot(root)
	if err != nil {
		return err
	}
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
	r.result.StagedPath = root
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
	rel, err := firstPackageFileDifference(cand.pkgDir, published.pkgDir)
	if err != nil {
		return fmt.Errorf("adopting the staged tree %s for %s-%s: comparing its package files with the published overlay: %w", root, r.pkg, r.newVersion, err)
	}
	if rel != "" {
		return fmt.Errorf("adopting the staged tree %s for %s-%s: %s differs from the published package directory %s, "+
			"and promotion publishes only the ebuild and its Manifest, so the gates would pass on files the overlay "+
			"does not carry; copy that change into the published package directory by hand, then repair again",
			root, r.pkg, r.newVersion, rel, published.pkgDir)
	}
	return nil
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

// packageMetadataFile is the one file beside files/ that a staged package
// directory may carry and promotion would not publish.
const packageMetadataFile = "metadata.xml"

// firstPackageFileDifference compares what a staged package directory carries
// beside its ebuild and Manifest with the published package directory, and
// returns the first relative path whose presence, kind or content differs; ""
// when none does.
//
// files/ is compared in both directions, recursively: staging copies it, so any
// difference is an edit. metadata.xml only counts when the staged directory has
// one, because staging never copies it.
func firstPackageFileDifference(stagedPkgDir, publishedPkgDir string) (string, error) {
	staged, err := packageFileEntries(stagedPkgDir)
	if err != nil {
		return "", err
	}
	published, err := packageFileEntries(publishedPkgDir)
	if err != nil {
		return "", err
	}
	if _, ok := staged[packageMetadataFile]; !ok {
		delete(published, packageMetadataFile)
	}

	paths := make([]string, 0, len(staged)+len(published))
	for rel := range staged {
		paths = append(paths, rel)
	}
	for rel := range published {
		if _, ok := staged[rel]; !ok {
			paths = append(paths, rel)
		}
	}
	slices.Sort(paths)
	for _, rel := range paths {
		if staged[rel] != published[rel] {
			return rel, nil
		}
	}
	return "", nil
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
