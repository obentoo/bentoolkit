package autoupdate

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/obentoo/bentoolkit/internal/common/logger"
)

// publishedFileMode is the mode promotion gives the files it writes into the
// published overlay.
//
// It is stated here rather than inherited from the staged tree, and that is the
// point. A staged tree is deliberately 0600/0750 (validate/stage.go): it holds a
// candidate nobody has reviewed and whatever a fixer wrote into it. The published
// overlay is the opposite kind of directory — a git repository that is committed,
// pushed, and then read by Portage as an unprivileged user — so a promoted ebuild
// carrying staging's mode would be unreadable to the consumer it was published
// for. 0644 is what `copyEbuild`'s os.Create and `pkgdev manifest` already produce
// there, so this changes nothing about the tree; it only stops staging's mode from
// travelling into it.
const publishedFileMode fs.FileMode = 0o644

// publishedUndo takes back whatever one apply placed in the published overlay,
// restoring the state that overlay held before.
//
// cause is the failure that triggered the rollback, and it is carried for one
// reason: a rollback that cannot finish must never replace the error that caused
// it. It reports what it could not undo as a warning naming the paths a human now
// has to look at, and the original error travels on untouched.
type publishedUndo func(cause error)

// candidatePaths says where this apply's candidate ebuild lives and which
// repository a gate has to run in to read it.
//
// The distinction it carries is not cosmetic. Before this story the two were
// always the published overlay, so every step — the manifest, the LLM fix, the
// compile — simply recomputed the path from a.overlayPath. With the candidate
// validated in a staged tree, a step that recomputes instead of being told would
// silently address the published tree, which is exactly what R3.2 forbids while a
// gate is running.
type candidatePaths struct {
	// staged is true when the candidate lives in a tree validate.Stage built,
	// outside the published overlay. False is the pre-staging path, kept until
	// every construction site supplies a staging root.
	staged bool
	// repoRoot is the Portage repository a gate runs against: the staged tree, or
	// the published overlay.
	repoRoot string
	// pkgDir is the <category>/<package>/ directory inside repoRoot.
	pkgDir string
	// ebuildPath is the candidate ebuild itself, inside pkgDir.
	ebuildPath string
	// fetchedDistdir is the private directory this run's manifest step fetched
	// the candidate's archive into, and "" when no manifest step ran or the
	// candidate is not staged.
	//
	// # Why it rides here and not on Applier (S035-D2)
	//
	// A field on Applier is the obvious shortcut and it is wrong for the reason
	// story 033 kept staging keyed by path rather than by an index:
	// applyAllPackages runs its workers CONCURRENTLY, so a per-Applier field
	// would be shared mutable state across packages being staged at the same
	// time, and package A's gate could be handed package B's distdir.
	// candidatePaths is already the per-bump carrier that reaches both
	// runStaticGates call sites, so the directory rides with the bump it belongs
	// to and the concurrency justification stays intact.
	fetchedDistdir string
}

// candidateIn names the candidate's paths inside one repository root. It is the
// single place a package directory and an ebuild filename are spelled, which is
// what makes "the staged tree and the published overlay have the same layout" a
// property of the code: promotion copies between the two by path, so a divergence
// in how either is built would be a copy to the wrong place.
func candidateIn(repoRoot, pkg, version string) (candidatePaths, error) {
	category, pkgName, ok := splitPkgAtom(pkg)
	if !ok {
		return candidatePaths{}, fmt.Errorf("invalid package name format: %s", pkg)
	}
	pkgDir := filepath.Join(repoRoot, category, pkgName)
	return candidatePaths{
		repoRoot:   repoRoot,
		pkgDir:     pkgDir,
		ebuildPath: filepath.Join(pkgDir, fmt.Sprintf("%s-%s.ebuild", pkgName, version)),
	}, nil
}

// publishedCandidate names the candidate's paths in the published overlay.
func publishedCandidate(overlayPath, pkg, version string) (candidatePaths, error) {
	return candidateIn(overlayPath, pkg, version)
}

// stagedCandidate names the candidate's paths inside the tree validate.Stage
// returned. The layout mirrors Stage's own (validate/stage.go writeCandidate), so
// the two must be read together: a change there is a change here.
func stagedCandidate(stagedRoot, pkg, version string) (candidatePaths, error) {
	cand, err := candidateIn(stagedRoot, pkg, version)
	if err != nil {
		return candidatePaths{}, err
	}
	cand.staged = true
	return cand, nil
}

// promote publishes the exact bytes the gates read (R3.3, R3.4) and returns the
// rollback that takes them back again.
//
// # Why this exists instead of copyEbuild happening earlier
//
// copyEbuild used to write the candidate into the published overlay BEFORE the
// manifest step and before every gate, and the deferred orphan rollback removed it
// again afterwards. The endpoints matched, so a before/after comparison saw
// nothing — but for the whole duration of the manifest run and every gate, the
// overlay really did hold an ebuild nothing had validated. This overlay
// auto-commits and pushes. "We roll it back afterwards" is not the same promise as
// "it was never there", and R3.2 asks for the second one.
//
// # The order, and the two different levers
//
// The ebuild is written first and the Manifest second, and the rollback for each
// is deliberately NOT the same operation:
//
//   - The ebuild did not exist before this apply, so removing it restores the
//     previous state exactly. It is also the removal that MUST happen: an ebuild
//     no Manifest entry covers is an unclaimed ebuild, which `--clean` deletes and
//     `overlay validate` reports.
//   - The Manifest DID exist, so removing it does not restore anything — it
//     destroys a file the overlay had before this apply ran, and an overlay with no
//     Manifest is worse than one with a stale Manifest. Its bytes are captured
//     before the overwrite and put back if any later step fails (R3.11).
//
// The capture is unconditional, taken even when the staged tree turns out to have
// no Manifest to publish. It is a precondition as much as a backup: a package
// directory whose Manifest cannot be read as a regular file is not one a promotion
// can leave consistent, and finding that out before overwriting is the difference
// between a refusal and a half-applied package.
//
// The registry pin is NOT written here. It is written by Apply, after this
// returns, so that a promotion which did not complete can never leave a pin aiming
// `--clean` at the only ebuild present (the hazard applier.go's own comment
// spells out).
func (a *Applier) promote(cand candidatePaths, pkg, version string) (publishedUndo, error) {
	// The invariant, at the single function that writes into the published
	// overlay. Both call sites pass through here — the validating path and R10.1's
	// reuse path, which reaches a published write without consulting
	// PromotionDecision at all.
	if err := a.refuseOnInterrupt(pkg, version); err != nil {
		return nil, err
	}

	if !cand.staged {
		// R3.3's second half: promotion happens only WHERE a staged tree holding
		// the validated candidate exists. Publishing from the published tree would
		// be a copy of a file onto itself dressed up as a promotion.
		return nil, fmt.Errorf("promoting %s-%s: the candidate was never staged, so there are no validated bytes to publish", pkg, version)
	}

	dst, err := publishedCandidate(a.overlayPath, pkg, version)
	if err != nil {
		return nil, err
	}

	// The exact bytes a gate read. Read out of the staged tree rather than rebuilt
	// from the source ebuild plus the substitutions, because R3.4 is a statement
	// about identity: anything that re-derives the file publishes a file no gate
	// ever saw, however faithful the derivation.
	body, err := os.ReadFile(cand.ebuildPath)
	if err != nil {
		return nil, fmt.Errorf("reading the validated ebuild %s for %s-%s: %w", cand.ebuildPath, pkg, version, err)
	}

	// Re-taken here even though Apply already refused this destination before
	// staging: the gates in between take minutes, and a check that old is a check
	// about a package directory that may have moved on.
	if err := refuseExistingEbuild(dst.ebuildPath, pkg, version); err != nil {
		return nil, err
	}

	promoted := &promotion{
		pkg:          pkg,
		version:      version,
		ebuildPath:   dst.ebuildPath,
		manifestPath: filepath.Join(dst.pkgDir, "Manifest"),
	}

	if err := writeThenRename(dst.ebuildPath, body, publishedFileMode); err != nil {
		// The rename is the only step that publishes anything, and it is atomic:
		// a failure here leaves the package directory exactly as it was, and
		// writeThenRename has already taken its temporary file away.
		return nil, fmt.Errorf("publishing the validated ebuild for %s-%s as %s: %w", pkg, version, dst.ebuildPath, err)
	}
	promoted.ebuildPublished = true

	if err := promoted.capturePublishedManifest(); err != nil {
		promoted.undo(err)
		return nil, err
	}

	stagedManifest := filepath.Join(cand.pkgDir, "Manifest")
	stagedBody, err := os.ReadFile(stagedManifest)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// The validated tree has no Manifest: `pkgdev manifest` writes none for a
		// package with nothing to digest. There is nothing that was validated to
		// publish, so the published Manifest is left exactly as it was — inventing
		// an empty one here would delete the entries of the versions still on disk.
	case err != nil:
		wrapped := fmt.Errorf("reading the validated Manifest %s for %s-%s: %w", stagedManifest, pkg, version, err)
		promoted.undo(wrapped)
		return nil, wrapped
	default:
		mode := promoted.manifestMode
		if !promoted.manifestExisted {
			mode = publishedFileMode
		}
		if err := writeThenRename(promoted.manifestPath, stagedBody, mode); err != nil {
			wrapped := fmt.Errorf("publishing the validated Manifest for %s-%s as %s: %w", pkg, version, promoted.manifestPath, err)
			promoted.undo(wrapped)
			return nil, wrapped
		}
		promoted.manifestPublished = true
	}

	return promoted.undo, nil
}

// promotion records what one promotion placed in the published overlay, so that
// undo can take back exactly that and nothing else.
//
// It is a value rather than two booleans in promote's body because the rollback
// outlives promote: Apply keeps it, and a failure AFTER a completed promotion
// (anything that sets result.Error further down) has to be able to undo a
// promotion that had already succeeded.
type promotion struct {
	pkg, version string
	ebuildPath   string
	manifestPath string

	// ebuildPublished is true once the candidate's rename into the published
	// package directory returned. Before that there is nothing to remove.
	ebuildPublished bool

	// manifestExisted, manifestBefore and manifestMode are the captured previous
	// state. manifestExisted false means the package directory had no Manifest, in
	// which case undoing a published one is a removal rather than a restore.
	manifestExisted bool
	manifestBefore  []byte
	manifestMode    fs.FileMode
	// manifestPublished is true once the Manifest's rename returned. A failed
	// rename replaced nothing — that is what makes rename the right primitive —
	// so there is nothing to put back.
	manifestPublished bool
}

// capturePublishedManifest reads the Manifest the published package directory
// already holds, before promotion overwrites it (R3.11).
//
// A missing Manifest is not an error: a package directory can legitimately have
// none, and "there was none" is a state undo can restore by removing what it
// wrote. A Manifest that is not a regular file IS an error, and it is raised
// here rather than left to the write: a directory (or a device, or a dangling
// something) at that path means the overwrite cannot be undone, and a promotion
// that cannot be undone is one that must not be started.
func (p *promotion) capturePublishedManifest() error {
	info, err := os.Stat(p.manifestPath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		p.manifestExisted = false
		return nil
	case err != nil:
		return fmt.Errorf("reading the published Manifest %s of %s before overwriting it: %w", p.manifestPath, p.pkg, err)
	case !info.Mode().IsRegular():
		return fmt.Errorf("the published Manifest %s of %s is %v and not a regular file, so the bytes promotion would overwrite cannot be captured and the promotion could not be undone",
			p.manifestPath, p.pkg, info.Mode())
	}

	body, err := os.ReadFile(p.manifestPath)
	if err != nil {
		return fmt.Errorf("reading the published Manifest %s of %s before overwriting it: %w", p.manifestPath, p.pkg, err)
	}
	p.manifestExisted = true
	p.manifestBefore = body
	p.manifestMode = info.Mode().Perm()
	return nil
}

// undo restores the published overlay to the state it held before this promotion
// began (R3.11). It never returns an error, by design: it is called on a path that
// already has one, and the failure that got there must reach the operator intact.
func (p *promotion) undo(cause error) {
	if p.ebuildPublished {
		if err := os.Remove(p.ebuildPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			logger.Warn("failed to remove the published ebuild %s for %s (%s) after a promotion that did not complete: %v "+
				"(original error preserved: %v); an ebuild no Manifest entry covers is an unclaimed ebuild that `--clean` deletes",
				p.ebuildPath, p.pkg, p.version, err, cause)
		}
	}

	if !p.manifestPublished {
		return
	}
	if !p.manifestExisted {
		// Nothing was there before, so restoring means removing what was written.
		if err := os.Remove(p.manifestPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			logger.Warn("failed to remove the Manifest %s promotion created for %s (%s) after it did not complete: %v "+
				"(original error preserved: %v)",
				p.manifestPath, p.pkg, p.version, err, cause)
		}
		return
	}
	if err := writeThenRename(p.manifestPath, p.manifestBefore, p.manifestMode); err != nil {
		logger.Warn("failed to restore the published Manifest %s of %s (%s) after a promotion that did not complete: %v "+
			"(original error preserved: %v); the package directory now holds the Manifest of a bump that was not published",
			p.manifestPath, p.pkg, p.version, err, cause)
	}
}

// refuseExistingEbuild is copyEbuild's overwrite guard, applied at the moment the
// published overlay is actually written to.
//
// It refuses only a REGULAR FILE, because that is the hazard the guard was written
// for: a package directory holding several ebuilds whose versions are not totally
// ordered by the selection that produced the source — most notably a multi-slot
// package, where the slots share a PV series and the revision suffix discriminates
// them — can already hold the destination, and rename() overwrites a regular file
// silently. Anything else sitting at that path is not an ebuild this code may
// classify, and the rename reports what it is far more precisely than a stat here
// could.
func refuseExistingEbuild(dstPath, pkg, version string) error {
	info, err := os.Lstat(dstPath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("failed to stat destination ebuild %s: %w", dstPath, err)
	case info.Mode().IsRegular():
		return fmt.Errorf("%w: %s (refusing to overwrite it with the validated %s-%s)", ErrEbuildExists, dstPath, pkg, version)
	}
	return nil
}

// writeThenRename publishes body at path through a temporary file in the same
// directory, so that path is only ever seen with its old contents or its new ones
// and never with a half-written mixture.
//
// Every exit but the successful rename takes the temporary file with it. That is
// not tidiness either: a leftover would be a file the published overlay never had,
// in an overlay that commits and pushes itself, and it would break the very
// property this story asserts — that a run in which every bump failed leaves the
// tree byte-identical.
func writeThenRename(path string, body []byte, mode fs.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".bentoo-*")
	if err != nil {
		return fmt.Errorf("creating a temporary file beside %s: %w", path, err)
	}
	tmpName := tmp.Name()

	committed := false
	defer func() {
		_ = tmp.Close() // no-op after the explicit Close below; here for the error paths
		if committed {
			return
		}
		if err := os.Remove(tmpName); err != nil && !errors.Is(err, fs.ErrNotExist) {
			logger.Warn("failed to remove the temporary file %s left beside %s: %v", tmpName, path, err)
		}
	}()

	if _, err := tmp.Write(body); err != nil {
		return fmt.Errorf("writing %s through the temporary file %s: %w", path, tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("syncing the temporary file %s for %s: %w", tmpName, path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing the temporary file %s for %s: %w", tmpName, path, err)
	}
	// os.CreateTemp creates at 0600 and os.Rename keeps the mode it finds, so the
	// mode has to be set on the temporary file — after the last write and before
	// the rename, so the file is never reachable under its final name with the
	// wrong one.
	//nolint:gosec // G302: the mode is publishedFileMode (or the mode the file being replaced already carried); see publishedFileMode.
	if err := os.Chmod(tmpName, mode); err != nil {
		return fmt.Errorf("setting mode %04o on %s before publishing it as %s: %w", mode.Perm(), tmpName, path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("renaming %s into place as %s: %w", tmpName, path, err)
	}
	committed = true
	return nil
}

// orphanEbuildUndo is the rollback of the pre-staging path, where the candidate
// was written straight into the published package directory: undoing the apply is
// removing that one file.
//
// It is the code Apply used to register inline right after copyEbuild, moved here
// unchanged in behaviour and in wording so that both rollbacks — this one and a
// promotion's — are read side by side. Which of the two an apply arms is the whole
// difference the staged path makes.
func orphanEbuildUndo(dstPath, pkg, version string) publishedUndo {
	return func(cause error) {
		if err := os.Remove(dstPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			// Rollback failed: keep the original error, just record the cleanup
			// miss so the orphan ebuild can be found and removed.
			logger.Warn(
				"failed to roll back orphan ebuild %s for %s (%s) after apply failure: %v "+
					"(original apply error preserved: %v)",
				dstPath, pkg, version, err, cause)
		}
	}
}
