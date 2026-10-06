// manifest_failure.go decides, after `pkgdev manifest` has exited non-zero,
// whether the failure belongs to the machine or to the ebuild. Nothing here
// reads the message pkgdev printed: this file exists BECAUSE that message is
// not evidence. The host this defect was measured on runs LC_MESSAGES=pt_BR,
// so "Cannot write to" is a string that appears only when a child process
// happens to inherit a C locale. A classifier built on it answers
// "repairable" the moment that stops being true — silently, and wrongly.
//
// Every branch below is therefore an observation of state: a probe that either
// writes or does not, a filesystem that either has room or does not, a file
// that either has bytes or does not. That is also why this file imports
// neither strings nor regexp, and never calls Error() on the failure it is
// handed — the ban on reading messages is visible in the import block.

package autoupdate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/obentoo/bentoolkit/internal/common/distfiles"
)

// ErrManifestEnvironment reports that a failed manifest step failed on the
// machine, not on the ebuild: the distdir stopped being writable, its
// filesystem ran out of room, or the fetch left an empty artefact behind.
//
// It is what the fixer gate keys on. An LLM fixer handed such a failure has no repair
// available to it — its only reachable conclusion is that SRC_URI is wrong —
// so it edits a correct ebuild in a repository that commits and pushes on its
// own. Callers test this with errors.Is; the original error is always wrapped
// alongside it, so the operator still reads exactly what pkgdev said.
var ErrManifestEnvironment = errors.New("manifest step failed on the environment, not on the ebuild")

// The reason a verdict was reached, wrapped next to ErrManifestEnvironment.
// They are unexported because no caller outside this package needs to branch on
// them: the fixer gate keys on ErrManifestEnvironment alone, and the reason is there
// for the operator reading the message — and for the test that pins WHICH check
// fired when more than one would have. Export one only when a caller genuinely
// has to distinguish them.
var (
	// errDistdirFull marks the free-space verdict.
	errDistdirFull = errors.New("the distdir's filesystem has no usable free space left")
	// errArtefactEmpty marks the zero-length-artefact verdict.
	errArtefactEmpty = errors.New("an expected distfile is present but empty")
)

// minimumUsableFreeBytes is the floor this classifier compares free space
// against, and it is a deliberate under-approximation of "the size the fetch
// still needed".
//
// That size is not knowable here. The only authority on how many bytes a
// distfile still needs is the server that serves it, and this code runs after
// the failure, offline, with the connection gone. What IS observable is the
// other end of the comparison: a filesystem with less than a mebibyte free
// cannot complete a download of any real distfile, nor the temporary file and
// digest pass that follow it. A mebibyte is small enough that a filesystem
// below it is unusable for this step whatever the distfile weighs.
//
// The consequence, stated plainly because it is the point: a 2 GiB tarball that
// exhausted a filesystem with 100 MiB free is classified REPAIRABLE, and the
// fixer runs on it. That is the bargain — a wrong classification must cost a
// wasted fixer invocation, never a lost capability — so the floor errs low on
// purpose. Raising it would start turning genuine ebuild failures into
// "environment" and would gate away a repair that exists.
const minimumUsableFreeBytes = 1 << 20 // 1 MiB

// SpaceFunc reports how many bytes are still available to the INVOKING user on
// the filesystem backing dir. It is the seam that makes the full-filesystem
// branch testable without filling a real disk.
//
// Contract:
//
//   - the result is a byte count, not blocks, and it is the space available to
//     an unprivileged user — the reserved blocks root can still use are not
//     ours and must not be counted;
//   - a non-nil error means the question could not be answered. It does NOT
//     mean "no space". ClassifyManifestFailure treats an unanswerable check as
//     no evidence and moves on, because a classifier that manufactured
//     an "environment" verdict out of a failed statfs would gate away a repair
//     on the strength of knowing nothing.
type SpaceFunc func(dir string) (uint64, error)

// availableSpace is the production SpaceFunc: statfs on the directory itself,
// which is the filesystem the fetch writes into even when the path is a mount
// point or a bind mount.
//
// Bavail rather than Bfree, because the reserved blocks Bfree includes belong
// to root and this process is not root (the default distdir is portage:portage
// 0775 and the invoking user reaches it through a group). A block size that is
// not positive is reported as unanswerable rather than multiplied into a
// nonsense figure.
//
// syscall.Statfs needs no build tag here: this repository ships linux/amd64 and
// linux/arm64 only (see Makefile build-all), and the conversions below are
// written to hold on every linux architecture, where Bsize is a signed integer
// and Bavail an unsigned one of platform-dependent width.
func availableSpace(dir string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, fmt.Errorf("failed to query free space on %s: %w", dir, err)
	}
	if st.Bsize <= 0 {
		return 0, fmt.Errorf("cannot compute free space on %s: filesystem reported block size %d", dir, st.Bsize)
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil // #nosec G115 -- Bsize validated positive above; Bavail is already unsigned
}

// defaultSpaceQuery is the SpaceFunc used when a caller injects none. It is a
// variable rather than a direct call to availableSpace for one reason, and it is
// the same reason execCommand and lookPath are variables elsewhere in this
// package: it lets a test prove that a nil seam normalises to the REAL query
// instead of quietly disabling the check.
//
// That distinction is invisible from the outside on a healthy machine — both
// answer "repairable" — and it is the whole difference between a safety check
// that runs in production and one that does not. Only tests replace it, and
// they restore it.
var defaultSpaceQuery SpaceFunc = availableSpace

// ClassifyManifestFailure decides whether err — the error a failed `pkgdev
// manifest` returned — is an environment failure or a repairable one, by
// checking observable state in this order (an unwritable distdir explains an
// empty artefact, so the cause is named before its symptom):
//
//  1. distdir no longer writable (the pre-flight probe, repeated)  -> environment
//  2. free space below the floor any fetch needs                   -> environment
//  3. an expected artefact present with zero length                -> environment
//  4. none of the above                                            -> repairable
//
// For 1-3 it returns an error wrapping both ErrManifestEnvironment and err, so
// the operator still reads what pkgdev printed. For 4 it returns err untouched,
// the same value, so a caller testing it for something else keeps finding it.
//
// "Uncertain" means "repairable": a check that cannot be answered (an empty
// distdir, a failing space query, an uninspectable name) is no evidence and
// falls through. A nil space uses the real statfs query rather than skipping.
// expected holds untrusted distfile NAMES, each reduced to one filename before
// the join. It never panics, never logs, and writes only Probe's own file.
func ClassifyManifestFailure(distdir string, err error, expected []string, space SpaceFunc) error {
	if err == nil {
		// No failure to classify: a distdir's state says nothing about a step
		// that did not fail.
		return nil
	}
	if distdir == "" {
		// Every check is unanswerable, and check 3 would be worse: joining ""
		// resolves against the WORKING directory, so a stray empty file in the
		// overlay could produce an "environment" verdict. Probe, Quarantine and
		// RecordFetchScope refuse an empty distdir the same way.
		return err
	}

	// Check 1 — is the directory still writable? This is the pre-flight run a
	// second time, and Probe is safe to repeat and to run concurrently by
	// construction. A directory that passed before pkgdev started and fails now
	// has changed underneath the run: a filesystem gone read-only, a mount
	// disappearing, a full device rejecting the write.
	if probeErr := distfiles.Probe(distdir); probeErr != nil {
		return fmt.Errorf("%w: %w (the manifest step reported: %w)", ErrManifestEnvironment, probeErr, err)
	}

	// Check 2 — is there room left for anything at all? It cannot detect "this
	// distfile did not fit" (that size is unknowable offline, see
	// minimumUsableFreeBytes), only a filesystem with nothing left for anyone.
	// Check 1 usually fires first there, so this is the case where a tiny write
	// still succeeds. A nil seam gets the real query so a forgotten argument
	// cannot disable the check; a query error is no evidence and is not logged.
	if space == nil {
		space = defaultSpaceQuery
	}
	free, spaceErr := space(distdir)
	if spaceErr == nil && free < minimumUsableFreeBytes {
		return fmt.Errorf("%w: %w: %d byte(s) available on %s, below the %d bytes any fetch needs (the manifest step reported: %w)",
			ErrManifestEnvironment, errDistdirFull, free, distdir, minimumUsableFreeBytes, err)
	}

	// Check 3 — did the fetch leave an artefact with no bytes in it? That is
	// what a download killed at the first byte leaves behind, and it is not
	// something a wrong SRC_URI produces on its own.
	//
	// The lookup is Lstat and only a REGULAR file counts. A directory under an
	// expected name belongs to the host (a distdir holds git3-src and the like)
	// and its size means nothing here. A symlink is one PrepopulateFromCache
	// left pointing into the read-only cache: its own size is the length of the
	// target path, and following it would ask about a file this run neither
	// created nor fetched. Both are left to the next check, as any uncertain
	// answer is.
	//
	// Each name is untrusted (it comes from a resolved version) and is reduced
	// to one filename by the rule internal/common/distfiles applies, so
	// "../../etc/passwd" cannot reach outside distdir and read as a verdict.
	for _, raw := range expected {
		name, ok := distfileEntryName(raw)
		if !ok {
			continue
		}
		info, statErr := os.Lstat(filepath.Join(distdir, name))
		if statErr != nil {
			// Absent — the ordinary case when the fetch never got that far —
			// or unreadable. Neither is evidence of an empty artefact.
			continue
		}
		if !info.Mode().IsRegular() || info.Size() != 0 {
			continue
		}
		return fmt.Errorf("%w: %w: %s in %s (the manifest step reported: %w)",
			ErrManifestEnvironment, errArtefactEmpty, name, distdir, err)
	}

	// Check 4 — nothing observable says the machine is at fault, so the ebuild
	// still might be, and today's repair path stays open. The error goes
	// back exactly as it arrived.
	return err
}

// distfileEntryName reduces one untrusted name to the single filename this
// classifier is willing to join to the distdir, and reports whether what is
// left names a file at all.
//
// filepath.Base neutralises traversal but does not answer "is this a
// filename": it yields "." for "", keeps "." and "..", and returns the
// separator for "/". Each of those joined to the distdir names a DIRECTORY,
// never a zero-length artefact, so they are refused lexically.
//
// This duplicates the unexported distfileName in internal/common/distfiles on
// purpose. The two must agree — that package decides which names it quarantines
// and cleans up, and a classifier inspecting a different set would observe the
// wrong file — but exporting it would widen a shared package's API for one
// read-only consumer. A third consumer is where it should be exported and this
// copy deleted.
//
// The separator scan is written out rather than reached for in strings so this
// file needs no text-searching import at all — see the file header.
func distfileEntryName(raw string) (string, bool) {
	base := filepath.Base(raw)
	switch base {
	case "", ".", "..", string(filepath.Separator):
		return "", false
	}
	for _, r := range base {
		if r == '/' || r == '\\' {
			return "", false
		}
	}
	return base, true
}
