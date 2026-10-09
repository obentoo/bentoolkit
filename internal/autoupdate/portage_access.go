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
// The compile gate escalates (`sudo ebuild <path> clean compile`), and running
// as root makes Portage honour FEATURES="userpriv userfetch": the repository is
// READ, and distfiles FETCHED, by uid `portage`, not by the operator. But a
// staged tree is 0750/0600 (validate/stage.go) and a private distdir is 0700,
// and uid `portage` belongs only to the `portage` group, so it could not even
// traverse them. Every privileged compile died on its first read
// (`Permission denied` on <distdir> and <staged>/profiles/thirdpartymirrors)
// before anything about the candidate had been exercised.
//
// # Why the GROUP, and not simply a wider mode
//
// Widening the staged tree to 0755/0644 was rejected: a candidate nobody has
// reviewed, plus whatever a fixer wrote into it, does not belong in a
// world-readable directory. Granting the single group that HAS to read it keeps
// that stance and grants strictly less.
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
// writable is the distdir/staged-tree distinction. A staged repository is only
// READ by uid `portage`, so it gets g+rx/g+r; a private distdir is WRITTEN by a
// fetch under `userfetch`, so it gets g+w too. g+w on the staged tree would let
// the unprivileged half of a build edit the candidate it is being judged on.
//
// A host with no `portage` group is a no-op, not a failure — see portageGroupID.
//
// Symlinks are chowned through Lchown and never chmodded: their own mode is
// consulted by nothing, and following them could change a file outside the
// tree. On Linux that skip is a SAFEGUARD no test can observe — a symlink
// lstats as 0777, so the `want == mode` check already skips it — but it stops
// the walk following links once groupBitsFor asks for a bit 0777 lacks, or on
// a platform where a symlink has a mode of its own.
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
		// Threat model, stated once for the three G122 sites in this tool. What gosec
		// flags is not a missing symlink check -- both are deliberate below -- but the
		// residual race: a path component swapped between WalkDir's lstat and the
		// syscall here. The trees walked are the host's own Portage repo
		// (cand.repoRoot) and its fetched DISTDIR (cand.fetchedDistdir). Winning that
		// race needs write access to one of them already, which is a strictly larger
		// compromise than anything the race would buy.
		//
		// os.Root IS a viable migration if that ever stops holding: each root is
		// walked separately, and os.Root carries Lchown, Chmod, Lstat, Readlink and
		// Symlink as of Go 1.26. The reason not to take it is the threat model above,
		// not a missing API -- so this is a decision to revisit, not a dead end.
		//nolint:gosec // G122: Lchown acts on the link itself and never follows it,
		// so the only exposure is the directory-component race described above.
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
		//nolint:gosec // G122: symlinks already returned above, so Chmod -- which does
		// follow them -- is only ever reached for a non-link entry. Same residual
		// directory-component race, same threat model as the Lchown above.
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
// grantPortageAccess opens a tree downward, but a tree nobody can REACH is not
// opened at all: a staged root sits at <staging>/<category>/<package>/<version>,
// each level created 0750 and owned by the operator, so uid `portage` was
// refused at `<staging>` before it ever saw the tree.
//
// It re-points each directory's group with Lchown (always — that, not the bit,
// is what was missing) and adds x only where the group lacks it. It never adds
// r: a directory its group could not list stays unlistable, so the names of
// other packages being staged do not leak as a side effect. This widens
// nothing a 0750 ancestor did not already grant; it re-points it.
//
// from is exclusive (grantPortageAccess already opened it, and one directory's
// mode is decided in one place), upto inclusive. upto must be an ancestor of
// from or nothing happens at all: that bound stops a mistaken pair of paths
// from climbing to / opening every directory on the way.
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
		return nil //nolint:nilerr // not an ancestor: doing nothing is the bound that stops a climb to /
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
