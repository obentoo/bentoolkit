package autoupdate

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

// portageGroupName is the group Portage's own unprivileged uid belongs to, and
// the whole reason this file exists.
//
// # The defect it fixes, measured on this host 2026-08-21
//
// The compile gate escalates: `sudo ebuild <path> clean compile` (compileOnce).
// Running as root is PRECISELY what makes Portage honour
// FEATURES="userpriv userfetch", so from that moment the repository is READ, and
// the distfiles are FETCHED, by uid `portage` — not by the operator who started
// the sweep.
//
// Everything the gate hands it, though, is built for that operator alone: a
// staged tree is 0750/0600 (validate/stage.go) and a private distdir is whatever
// os.MkdirTemp makes, 0700. uid `portage` belongs to the `portage` group and to
// nothing else, so it could not so much as TRAVERSE the staged tree, and the
// gate died on its first read of the repository:
//
//	bash: <distdir>/.__portage_test_write__: Permission denied
//	!!! Permission Denied: <staged>/profiles/thirdpartymirrors
//
// before unpack, before src_prepare, before anything about the candidate had
// been exercised. Every bump reaching a privileged compile failed identically,
// and the attribution gate correctly called it the host's fault — which did not
// help anyone, because it was still a bump that could not be applied.
//
// # Why the GROUP, and not simply a wider mode
//
// Widening the staged tree to 0755/0644 would fix it in three lines and was
// rejected. stage.go states, and means, that a candidate nobody has reviewed —
// together with whatever a fixer wrote into it — does not belong in a
// world-readable directory. Granting the single group that HAS to read it keeps
// that stance intact and grants strictly less than the mode change would.
const portageGroupName = "portage"

// portageGroupID answers this host's gid for portageGroupName.
//
// "No such group" is NOT an error and the bool is what says so: a host without a
// Portage group has nothing to grant access TO, and a build there will succeed
// or fail for reasons this file has no part in. The CI that runs this package's
// tests is exactly such a host, which is why the grant below must be a no-op
// there rather than a skipped test.
//
// It is a variable so a test can answer for a host it is not running on. Only
// tests replace it.
var portageGroupID = func() (int, bool) {
	g, err := user.LookupGroup(portageGroupName)
	if err != nil {
		return 0, false
	}
	gid, err := strconv.Atoi(g.Gid)
	if err != nil {
		return 0, false
	}
	return gid, true
}

// grantPortageAccess makes root, and everything beneath it, reachable by the
// `portage` group: the group comes to own every entry, and every entry's group
// bits are opened to mirror its owner's.
//
// # writable is the distdir/staged-tree distinction, and it is not cosmetic
//
// A staged repository is READ by uid `portage`, so g+rx on directories and g+r
// on files is the whole of what it may have. A private distdir is WRITTEN into
// — that is what a fetch under `userfetch` does — so it gets g+w as well.
// Handing the staged tree g+w would let the unprivileged half of a build edit
// the very candidate it is being judged on.
//
// A host with no `portage` group is a no-op, not a failure — see portageGroupID.
//
// Symlinks are chowned through Lchown and never chmodded: a symlink's own mode
// is consulted by nothing, and following it would change the mode of a file
// that may sit outside the tree entirely.
//
// That skip is a SAFEGUARD AND NOT AN OBSERVABLE BEHAVIOUR on Linux, which is
// worth saying out loud because no test can hold it down here: a Linux symlink
// is always lstat'd as 0777, so groupBitsFor already asks for nothing new and
// the Chmod below is skipped by the `want == mode` check whether or not this
// branch exists. Deleting it would therefore break nothing TODAY and would make
// the walk follow links the moment groupBitsFor learns to ask for a bit 0777
// does not already carry — or the moment this runs somewhere a symlink has a
// mode of its own.
func grantPortageAccess(root string, writable bool) error {
	if root == "" {
		return nil
	}
	gid, ok := portageGroupID()
	if !ok {
		return nil
	}
	// A tree that is not there is nothing to open, and saying so here rather
	// than letting the walk return ENOENT keeps this function from inventing a
	// failure mode of its own. A missing staged tree or a missing distdir is a
	// real problem, but it is `ebuild`'s to report — it names the path it could
	// not read, which is a far better sentence than "could not chgrp".
	if _, err := os.Lstat(root); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("walking %s to open it to the %s group: %w", root, portageGroupName, err)
		}
		if err := os.Lchown(path, -1, gid); err != nil {
			return fmt.Errorf("giving %s to the %s group: %w", path, portageGroupName, err)
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("reading the mode of %s: %w", path, err)
		}
		mode := info.Mode().Perm()
		want := mode | groupBitsFor(mode, writable)
		if want == mode {
			return nil
		}
		if err := os.Chmod(path, want); err != nil {
			return fmt.Errorf("opening %s to the %s group: %w", path, portageGroupName, err)
		}
		return nil
	})
}

// groupBitsFor mirrors an entry's OWNER bits into its group bits: the group may
// read what the owner reads and traverse what the owner traverses, and write
// only where the caller asked AND the owner can write too.
//
// Mirroring rather than setting a constant is what keeps a mode the tree chose
// deliberately from being widened past its own owner's reach — a file the
// staging step made read-only stays read-only for the group as well.
func groupBitsFor(mode fs.FileMode, writable bool) fs.FileMode {
	var g fs.FileMode
	if mode&0o400 != 0 {
		g |= 0o040
	}
	if mode&0o100 != 0 {
		g |= 0o010
	}
	if writable && mode&0o200 != 0 {
		g |= 0o020
	}
	return g
}

// grantCompileAccess opens the directories a PRIVILEGED compile makes uid
// `portage` read to that group, and nothing else.
//
// # What is deliberately absent from this list
//
// A candidate that is not staged lives in the published overlay — the repository
// that auto-commits and pushes — and re-permissioning that tree is not this
// gate's business; it is also unnecessary, an overlay being world-readable
// already. The host's own DISTDIR is absent for the mirror-image reason: it is
// where Portage keeps its archives and already belongs to the group. Only
// cand.fetchedDistdir, the private directory THIS run's manifest step created
// and this run will delete, is ours to open.
func (a *Applier) grantCompileAccess(cand candidatePaths) error {
	if cand.staged {
		// Read, never written: the build must not be able to edit the candidate
		// whose verdict it is producing.
		if err := grantPortageAccess(cand.repoRoot, false); err != nil {
			return fmt.Errorf("preparing the staged tree for a privileged build: %w", err)
		}
		// And the directories that lead to it. Opening the tree without opening
		// the path to it fixes nothing: uid `portage` is refused at the staging
		// root and never reaches the tree whose modes were just corrected.
		if err := grantPortageTraversal(cand.repoRoot, a.stagingRoot); err != nil {
			return fmt.Errorf("opening the path to the staged tree for a privileged build: %w", err)
		}
	}
	// Written: a fetch under `userfetch` downloads into it as uid `portage`.
	if err := grantPortageAccess(cand.fetchedDistdir, true); err != nil {
		return fmt.Errorf("preparing the private distdir for a privileged build: %w", err)
	}
	return nil
}

// grantPortageTraversal opens the DIRECTORIES BETWEEN upto and from to the
// `portage` group.
//
// # Why the grant cannot stop at the staged tree's own root
//
// grantPortageAccess opens a tree downward, and a tree nobody can REACH is not
// opened at all. A staged root sits three directories below the staging root
//
//	<staging>/<category>/<package>/<version>
//
// and every one of them is created at stagedDirMode, 0750, owned by the
// operator. Measured on this host 2026-08-21, that is the outer half of the same
// defect: uid `portage` was refused at `<staging>` and never saw the tree whose
// permissions were the visible symptom.
//
// # It adds x where x is missing, and never adds r
//
// On the layout this actually runs against the chmod is usually a no-op: a
// stagedDirMode directory is 0750, which already grants its group r-x. What was
// missing was never the BIT, it was WHOSE group those bits belong to — which is
// the Lchown, and is why that happens unconditionally while the chmod does not.
//
// The chmod is there for a directory whose group cannot traverse it at all
// (0700, say, from a caller that made one itself), and it grants x alone. r is
// never added by this function: a directory that did not let its group list it
// still does not, so the names of other packages being staged do not become
// readable as a side effect of opening a path to one of them. What a 0750
// ancestor already granted its own group, of course, it goes on granting to the
// new one — this widens nothing, it re-points it.
//
// # from is exclusive, upto inclusive
//
// from is the staged root, which grantPortageAccess has already opened as a
// whole; re-opening it here would be harmless and is skipped anyway to keep one
// directory from having its mode decided in two places.
//
// upto must be an ancestor of from or nothing happens at all. That check is the
// bound on the walk, and it is a real one rather than a formality: without it a
// mistaken pair of paths would climb to / opening every directory on the way.
func grantPortageTraversal(from, upto string) error {
	if from == "" || upto == "" {
		return nil
	}
	gid, ok := portageGroupID()
	if !ok {
		return nil
	}
	from = filepath.Clean(from)
	upto = filepath.Clean(upto)
	rel, err := filepath.Rel(upto, from)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		// Not an ancestor — including the case where they are the same
		// directory, which has nothing between it and itself.
		return nil
	}

	for dir := filepath.Dir(from); ; dir = filepath.Dir(dir) {
		if err := openForTraversal(dir, gid); err != nil {
			return err
		}
		if dir == upto {
			return nil
		}
		// The ancestor check above guarantees this loop reaches upto, so this is
		// a backstop against a future caller passing a pair it did not verify —
		// a walk that reaches the filesystem root has lost its bound and must
		// stop rather than keep opening directories.
		if parent := filepath.Dir(dir); parent == dir {
			return fmt.Errorf("climbing from %s to %s reached the filesystem root without finding it", from, upto)
		}
	}
}

// openForTraversal gives one directory to the `portage` group and adds g+x where
// its owner already has x. A directory that is not there is skipped: the caller
// is climbing a path that exists, so this only fires on a race with a concurrent
// cleanup, and inventing a failure for it would fail a build over a directory
// nothing needed any more.
func openForTraversal(dir string, gid int) error {
	info, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading %s on the way to the staged tree: %w", dir, err)
	}
	if err := os.Lchown(dir, -1, gid); err != nil {
		return fmt.Errorf("giving %s to the %s group: %w", dir, portageGroupName, err)
	}
	mode := info.Mode().Perm()
	if mode&0o100 == 0 || mode&0o010 != 0 {
		return nil
	}
	if err := os.Chmod(dir, mode|0o010); err != nil {
		return fmt.Errorf("opening %s for traversal by the %s group: %w", dir, portageGroupName, err)
	}
	return nil
}
