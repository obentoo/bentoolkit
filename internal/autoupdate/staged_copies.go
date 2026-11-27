package autoupdate

import (
	"fmt"
	"os"
	"path/filepath"
)

// CopyAction is what carrying one differing path into the published package
// directory takes.
type CopyAction string

const (
	// CopyFile copies a regular file the staged tree carries.
	CopyFile CopyAction = "copy"
	// MakeDir creates a directory the staged tree carries.
	MakeDir CopyAction = "mkdir"
	// ByHand is anything bentoo does not do for the operator: a file only the
	// published directory has, or an entry that is not a file or directory.
	ByHand CopyAction = "by-hand"
)

// StagedFileCopy is one path under a staged package directory that differs
// from the published one, and how it reaches the published directory.
type StagedFileCopy struct {
	// Rel is the slash-separated path relative to the package directory.
	Rel      string
	Src, Dst string
	Action   CopyAction
	// Note says what to do by hand when Action is ByHand.
	Note string
}

// StagedFileCopies lists what the adoption of the staged tree at stagedRoot
// would refuse for pkg-version: every path under files/ (and a staged
// metadata.xml) that differs from the published package directory under
// overlayPath, by the same rule adoptCandidate applies. Nil means the tree
// would be adopted as it is. A repair offers these copies once its session
// has ended, before the re-validation, so a rebased patch does not have to
// wait for a second repair run (breakpad, 2026-11-26).
func StagedFileCopies(overlayPath, stagedRoot, pkg, version string) ([]StagedFileCopy, error) {
	cand, err := stagedCandidate(stagedRoot, pkg, version)
	if err != nil {
		return nil, fmt.Errorf("listing the staged files of %s-%s: %w", pkg, version, err)
	}
	published, err := publishedCandidate(overlayPath, pkg, version)
	if err != nil {
		return nil, fmt.Errorf("listing the staged files of %s-%s: %w", pkg, version, err)
	}
	candidate, err := os.ReadFile(cand.ebuildPath)
	if err != nil {
		return nil, fmt.Errorf("listing the staged files of %s-%s: reading the candidate: %w", pkg, version, err)
	}
	diffs, err := packageFileDifferences(cand.pkgDir, published.pkgDir, filesDirReach(candidate))
	if err != nil {
		return nil, fmt.Errorf("listing the staged files of %s-%s: comparing with the published overlay: %w", pkg, version, err)
	}
	return planFileCopies(cand.pkgDir, published.pkgDir, diffs), nil
}

// planFileCopies classifies each differing path: a regular file or a
// directory the staged tree carries is copied or created, anything else is
// left to the operator with a note.
func planFileCopies(stagedPkgDir, publishedPkgDir string, diffs []string) []StagedFileCopy {
	plan := make([]StagedFileCopy, 0, len(diffs))
	for _, rel := range diffs {
		c := StagedFileCopy{
			Rel: rel,
			Src: filepath.Join(stagedPkgDir, filepath.FromSlash(rel)),
			Dst: filepath.Join(publishedPkgDir, filepath.FromSlash(rel)),
		}
		info, err := os.Lstat(c.Src)
		switch {
		case err != nil:
			c.Action = ByHand
			c.Note = fmt.Sprintf("%s is only in the published directory, where a ${FILESDIR} path of the candidate would pick it up: restore it in the staged tree or drop it from the overlay", rel)
		case info.IsDir():
			c.Action = MakeDir
		case info.Mode().IsRegular():
			c.Action = CopyFile
		default:
			c.Action = ByHand
			c.Note = fmt.Sprintf("%s is not a regular file: reproduce it in the overlay by hand", rel)
		}
		plan = append(plan, c)
	}
	return plan
}
