package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/autoupdate/ebuilds"
	"github.com/obentoo/bentoolkit/internal/autoupdate/repair"
	"github.com/obentoo/bentoolkit/internal/common/i18n"
	"github.com/obentoo/bentoolkit/internal/common/output"
)

// offerStagedCopies runs once a repair session has ended and before the
// re-validation: it lists every path under files/ the adoption would refuse
// and, on one yes per package, copies what bentoo can copy into the published
// package directory. The batch run of 2026-11-26 re-validated breakpad the
// moment its session ended, so the rebased patch the session wrote could only
// be copied by hand and repaired again. --yes does not answer the question;
// a repair already needs a terminal on both ends.
func (ar *autoupdateRun) offerStagedCopies(ctx context.Context, overlayPath string, b repair.Bundle) {
	plan, err := autoupdate.StagedFileCopies(overlayPath, b.StagedRoot, b.Package, b.Candidate)
	if err != nil {
		ar.log().Warn("the staged files could not be compared with the published overlay; nothing offered",
			"package", b.Package, "staged_root", b.StagedRoot, "err", err)
		return
	}
	if len(plan) == 0 {
		return
	}
	publishedDir := ebuilds.PkgDirFor(overlayPath, b.Package)
	fmt.Println()
	output.Header.Println(i18n.T("Files to carry into the published overlay"))
	output.Package.Printf("  %s → %s\n", b.Package, publishedDir)
	copyable := 0
	for _, c := range plan {
		switch c.Action {
		case autoupdate.CopyFile:
			copyable++
			fmt.Printf("    cp %s %s\n", c.Src, c.Dst)
		case autoupdate.MakeDir:
			copyable++
			fmt.Printf("    mkdir -p %s\n", c.Dst)
		default:
			output.Warning.Printf("    %s\n", c.Note)
		}
	}
	if copyable == 0 {
		return
	}
	restore := yieldContextSignalsToPrompt(ctx)
	ok := ar.deps.confirmRepair(fmt.Sprintf(i18n.TCount("Copy %d path(s) into %s now?", copyable), copyable, publishedDir))
	restore()
	if !ok {
		output.Info.Println("  " + i18n.T("Nothing copied: the re-validation refuses the staged tree until these paths match."))
		return
	}
	for _, c := range plan {
		if err := applyStagedCopy(c); err != nil {
			output.Warning.Printf("  %s: %v\n", c.Rel, err)
			ar.log().Warn("a staged file was not copied into the published overlay", "package", b.Package, "path", c.Rel, "err", err)
			continue
		}
		if c.Action != autoupdate.ByHand {
			ar.log().Info("copied a staged path into the published overlay", "package", b.Package, "src", c.Src, "dst", c.Dst)
		}
	}
}

// applyStagedCopy carries one planned path into the published directory. A
// destination that is a symbolic link is refused rather than written through:
// the bytes would land wherever the link points.
func applyStagedCopy(c autoupdate.StagedFileCopy) error {
	switch c.Action {
	case autoupdate.MakeDir:
		if err := os.MkdirAll(c.Dst, 0o750); err != nil {
			return fmt.Errorf("creating %s: %w", c.Dst, err)
		}
		return nil
	case autoupdate.CopyFile:
		if info, err := os.Lstat(c.Dst); err == nil && info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symbolic link; copy it by hand", c.Dst)
		} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("checking %s: %w", c.Dst, err)
		}
		info, err := os.Lstat(c.Src)
		if err != nil {
			return fmt.Errorf("reading %s: %w", c.Src, err)
		}
		data, err := os.ReadFile(c.Src)
		if err != nil {
			return fmt.Errorf("reading %s: %w", c.Src, err)
		}
		if err := os.MkdirAll(filepath.Dir(c.Dst), 0o750); err != nil {
			return fmt.Errorf("creating %s: %w", filepath.Dir(c.Dst), err)
		}
		if err := os.WriteFile(c.Dst, data, info.Mode().Perm()); err != nil { //nolint:gosec // G703: Dst is the published package directory of a confined package key (ebuilds.PkgDirFor) joined with a path walked under the staged files/, and the operator confirmed this copy
			return fmt.Errorf("writing %s: %w", c.Dst, err)
		}
		return nil
	default:
		return nil
	}
}
