package autoupdate

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/obentoo/bentoolkit/internal/autoupdate/ebuilds"
	"github.com/obentoo/bentoolkit/internal/common/distfiles"
	"github.com/obentoo/bentoolkit/internal/common/fileutil"
	"github.com/obentoo/bentoolkit/internal/common/logging"
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
// The distinction it carries is not cosmetic. Before staging the two were
// always the published overlay, so every step — the manifest, the LLM fix, the
// compile — simply recomputed the path from a.overlayPath. With the candidate
// validated in a staged tree, a step that recomputes instead of being told would
// silently address the published tree, which must not change while a gate is
// running.
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
	// # Why it rides here and not on Applier
	//
	// A field on Applier is the obvious shortcut and it is wrong for the reason
	// staging is keyed by path rather than by an index: ApplyAll runs its workers CONCURRENTLY, so a per-Applier field
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
	category, pkgName, ok := ebuilds.SplitPkgAtom(pkg)
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

// promote publishes the exact bytes the gates read and returns the rollback
// that takes them back again.
//
// It exists so the candidate is never in the published overlay while it is
// unvalidated: copyEbuild used to write it there BEFORE the manifest step and
// every gate, rolling it back afterwards. This overlay auto-commits and pushes,
// and "we roll it back afterwards" is not "it was never there".
//
// The ebuild is written first, the Manifest second, and their rollbacks differ:
//   - The ebuild did not exist before, so removing it restores the state — and
//     MUST happen, since an ebuild no Manifest covers is one `--clean` deletes.
//   - The Manifest DID exist, so removing it would destroy a file; its bytes are
//     captured before the overwrite and put back if any later step fails. The
//     capture is unconditional and doubles as a precondition: a Manifest that
//     is not a regular file is refused before anything is overwritten.
//
// The registry pin is NOT written here but by Apply after this returns, so an
// incomplete promotion can never leave a pin aiming `--clean` at the only
// ebuild present.
func (a *Applier) promote(ctx context.Context, cand candidatePaths, pkg, version string) (publishedUndo, error) {
	// The invariant, at the single function that writes into the published
	// overlay. Both call sites pass through here — the validating path and the
	// proof-reuse path, which reaches a published write without consulting
	// PromotionDecision at all.
	if err := a.refuseOnInterrupt(ctx, pkg, version); err != nil {
		return nil, err
	}

	if !cand.staged {
		// Promotion happens only WHERE a staged tree holding
		// the validated candidate exists. Publishing from the published tree would
		// be a copy of a file onto itself dressed up as a promotion.
		return nil, fmt.Errorf("promoting %s-%s: the candidate was never staged, so there are no validated bytes to publish", pkg, version)
	}

	dst, err := publishedCandidate(a.overlayPath, pkg, version)
	if err != nil {
		return nil, err
	}

	// The exact bytes a gate read. Read out of the staged tree rather than rebuilt
	// from the source ebuild plus the substitutions, because the promise is one
	// of identity: anything that re-derives the file publishes a file no gate
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
		log:          a.logger(),
		pkg:          pkg,
		version:      version,
		ebuildPath:   dst.ebuildPath,
		manifestPath: filepath.Join(dst.pkgDir, "Manifest"),
	}

	// Published with no-clobber (fileutil.PublishNewFile links a synced
	// temporary file to the name): an ebuild that appeared after the check
	// above — or a dangling symlink the check could not see — is refused and
	// left byte-identical, never replaced. The link is the only step that
	// publishes anything, so a failure leaves the package directory exactly as
	// it was, with no temporary file.
	if err := fileutil.PublishNewFile(dst.ebuildPath, body, publishedFileMode); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("%w: %s (refusing to overwrite it with the validated %s-%s)", ErrEbuildExists, dst.ebuildPath, pkg, version)
		}
		return nil, fmt.Errorf("publishing the validated ebuild for %s-%s as %s: %w", pkg, version, dst.ebuildPath, err)
	}
	promoted.ebuildPublished = true

	if err := promoted.capturePublishedManifest(); err != nil {
		promoted.undo(err)
		return nil, err
	}

	stagedManifest := filepath.Join(cand.pkgDir, "Manifest")
	stagedBody, err := os.ReadFile(stagedManifest) //nolint:gosec // G304: stagedManifest is <staged package dir>/Manifest; stagedCandidate built that directory from a package key splitPkgAtom confines
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
		// The staged Manifest covers the candidate alone. Its records are the
		// validated bytes and are written unchanged; every other record is the
		// published tree's own, taken from the captured Manifest. With no
		// published Manifest the staged bytes are written as they are, and only
		// the counts come from the merge.
		body := stagedBody
		merged, merge := distfiles.MergeManifestDist(promoted.manifestBefore, stagedBody)
		if promoted.manifestExisted {
			body = merged
		}
		mode := promoted.manifestMode
		if !promoted.manifestExisted {
			mode = publishedFileMode
		}
		if err := writeThenRename(promoted.log, promoted.manifestPath, body, mode); err != nil {
			wrapped := fmt.Errorf("publishing the validated Manifest for %s-%s as %s: %w", pkg, version, promoted.manifestPath, err)
			promoted.undo(wrapped)
			return nil, wrapped
		}
		promoted.manifestPublished = true
		promoted.log.Info("promotion: published Manifest merged",
			"package", pkg, "version", version, "kept", merge.Kept, "written", merge.Written, "replaced", merge.Replaced)
		if merge.Dropped > 0 {
			promoted.log.Warn("promotion: dropped malformed DIST records from the published Manifest",
				"package", pkg, "version", version, "manifest", promoted.manifestPath, "dropped", merge.Dropped)
		}
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
	// log receives the warnings undo raises; nil discards them.
	log          *slog.Logger
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
// already holds, before promotion overwrites it.
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
// began. It never returns an error, by design: it is called on a path that
// already has one, and the failure that got there must reach the operator intact.
func (p *promotion) undo(cause error) {
	log := logging.OrDiscard(p.log)
	if p.ebuildPublished {
		if err := os.Remove(p.ebuildPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			log.Warn("failed to remove the published ebuild after a promotion that did not complete "+
				"(original error preserved); an ebuild no Manifest entry covers is an unclaimed ebuild that `--clean` deletes",
				"path", p.ebuildPath, "package", p.pkg, "version", p.version, "err", err, "cause", cause)
		}
	}

	if !p.manifestPublished {
		return
	}
	if !p.manifestExisted {
		// Nothing was there before, so restoring means removing what was written.
		if err := os.Remove(p.manifestPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			log.Warn("failed to remove the Manifest the promotion created after it did not complete "+
				"(original error preserved)",
				"path", p.manifestPath, "package", p.pkg, "version", p.version, "err", err, "cause", cause)
		}
		return
	}
	if err := writeThenRename(log, p.manifestPath, p.manifestBefore, p.manifestMode); err != nil {
		log.Warn("failed to restore the published Manifest after a promotion that did not complete "+
			"(original error preserved); the package directory now holds the Manifest of a bump that was not published",
			"path", p.manifestPath, "package", p.pkg, "version", p.version, "err", err, "cause", cause)
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
// property staging promises — that a run in which every bump failed leaves the
// tree byte-identical. A temporary file that cannot be removed is a warning to
// log; nil discards it.
func writeThenRename(log *slog.Logger, path string, body []byte, mode fs.FileMode) error {
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
			logging.OrDiscard(log).Warn("failed to remove the temporary file left beside the target", "tmp", tmpName, "path", path, "err", err)
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
// difference the staged path makes. The cleanup miss is a warning to log; nil
// discards it.
func orphanEbuildUndo(log *slog.Logger, dstPath, pkg, version string) publishedUndo {
	log = logging.OrDiscard(log)
	return func(cause error) {
		if err := os.Remove(dstPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			// Rollback failed: keep the original error, just record the cleanup
			// miss so the orphan ebuild can be found and removed.
			log.Warn("failed to roll back orphan ebuild after apply failure (original apply error preserved)",
				"path", dstPath, "package", pkg, "version", version, "err", err, "cause", cause)
		}
	}
}
