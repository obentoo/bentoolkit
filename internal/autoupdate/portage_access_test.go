package autoupdate

import (
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// withPortageGroupID points the grant at a gid the test process can actually
// chown to, so the walk exercises the real Lchown and Chmod rather than a stub.
// Restores the production lookup on cleanup.
func withPortageGroupID(t *testing.T, gid int, ok bool) {
	t.Helper()
	prev := portageGroupID
	portageGroupID = func() (int, bool) { return gid, ok }
	t.Cleanup(func() { portageGroupID = prev })
}

// stagedLikeTree builds the shape a staged tree really has — a directory the
// owner alone can enter, holding a file the owner alone can read — which is the
// shape that broke the privileged compile.
func stagedLikeTree(t *testing.T) (root, dir, file string) {
	t.Helper()
	root = t.TempDir()
	dir = filepath.Join(root, "profiles")
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatalf("creating %s: %v", dir, err)
	}
	file = filepath.Join(dir, "repo_name")
	if err := os.WriteFile(file, []byte("bentoolkit-staging"), 0o600); err != nil {
		t.Fatalf("writing %s: %v", file, err)
	}
	// t.TempDir() picks its own mode; the root is pinned so the assertions below
	// are about what the grant did and not about what the harness happened to do.
	if err := os.Chmod(root, 0o750); err != nil {
		t.Fatalf("pinning the mode of %s: %v", root, err)
	}
	return root, dir, file
}

// aSecondaryGID picks a group the test process belongs to that is NOT its
// primary one, and skips when there is none.
//
// Using os.Getgid() here would be a test that proves nothing: t.TempDir() and
// os.Mkdir already create directories owned by the primary group, so the
// assertion would hold before the grant ever ran. A secondary gid is the only
// value on this host that the chown can reach AND that is not already there.
func aSecondaryGID(t *testing.T) int {
	t.Helper()
	groups, err := os.Getgroups()
	if err != nil {
		t.Skipf("cannot read this process's groups: %v", err)
	}
	for _, g := range groups {
		if g != os.Getgid() {
			return g
		}
	}
	t.Skip("this process belongs to no group but its primary one; nothing to chown to")
	return 0
}

// gidOf is what most of the traversal assertions have to read. On the layout
// this really runs against every ancestor is already 0750 — g+x is present
// before anything happens — so asserting the BIT proves nothing there; what the
// grant changes is WHICH group those bits belong to.
func gidOf(t *testing.T, path string) int {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skipf("this platform does not expose an owning gid for %s", path)
	}
	return int(st.Gid)
}

func modeOf(t *testing.T, path string) fs.FileMode {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Mode().Perm()
}

// A read-only grant is what the staged tree gets: the group must be able to
// traverse every directory and read every file, and must NOT be able to write
// the candidate whose verdict the build is producing.
func TestGrantPortageAccessOpensStagedTreeToTheGroupReadOnly(t *testing.T) {
	withPortageGroupID(t, os.Getgid(), true)
	root, dir, file := stagedLikeTree(t)

	if err := grantPortageAccess(root, false); err != nil {
		t.Fatalf("grantPortageAccess: %v", err)
	}

	for _, d := range []string{root, dir} {
		got := modeOf(t, d)
		if got&0o050 != 0o050 {
			t.Errorf("directory %s is %04o; uid portage cannot traverse and read it (want g+rx)", d, got)
		}
		if got&0o020 != 0 {
			t.Errorf("directory %s is %04o; a read-only grant must not add g+w", d, got)
		}
	}
	if got := modeOf(t, file); got&0o040 != 0o040 {
		t.Errorf("file %s is %04o; uid portage cannot read it (want g+r)", file, got)
	} else if got&0o020 != 0 {
		t.Errorf("file %s is %04o; a read-only grant must not add g+w", file, got)
	}
}

// A writable grant is what the private distdir gets, because a fetch under
// `userfetch` downloads into it as uid portage.
func TestGrantPortageAccessOpensDistdirToTheGroupForWriting(t *testing.T) {
	withPortageGroupID(t, os.Getgid(), true)
	root, _, file := stagedLikeTree(t)

	if err := grantPortageAccess(root, true); err != nil {
		t.Fatalf("grantPortageAccess: %v", err)
	}

	if got := modeOf(t, root); got&0o070 != 0o070 {
		t.Errorf("directory %s is %04o; uid portage cannot write into it (want g+rwx)", root, got)
	}
	if got := modeOf(t, file); got&0o060 != 0o060 {
		t.Errorf("file %s is %04o; uid portage cannot write it (want g+rw)", file, got)
	}
}

// A host with no `portage` group has nothing to grant access to. The grant must
// then change nothing at all — that host is the CI running this very suite.
func TestGrantPortageAccessIsANoOpWithoutThePortageGroup(t *testing.T) {
	withPortageGroupID(t, 0, false)
	root, dir, file := stagedLikeTree(t)
	before := []fs.FileMode{modeOf(t, root), modeOf(t, dir), modeOf(t, file)}

	if err := grantPortageAccess(root, false); err != nil {
		t.Fatalf("grantPortageAccess on a host with no portage group: %v", err)
	}

	for i, path := range []string{root, dir, file} {
		if got := modeOf(t, path); got != before[i] {
			t.Errorf("%s changed from %04o to %04o; a host with no portage group must be untouched", path, before[i], got)
		}
	}
}

// An empty path is the "no private distdir was created" case and answers
// without walking anything.
func TestGrantPortageAccessAcceptsAnEmptyPath(t *testing.T) {
	withPortageGroupID(t, os.Getgid(), true)
	if err := grantPortageAccess("", true); err != nil {
		t.Fatalf("grantPortageAccess(\"\"): %v", err)
	}
}

// A tree that is not there is nothing to open. This matters beyond tidiness:
// the grant runs on every privileged compile, so a path that does not exist
// must not become a NEW way for the gate to fail — `ebuild` reports a missing
// tree far better than a failed chgrp would.
func TestGrantPortageAccessIsANoOpOnAMissingTree(t *testing.T) {
	withPortageGroupID(t, os.Getgid(), true)
	missing := filepath.Join(t.TempDir(), "never-staged")

	if err := grantPortageAccess(missing, false); err != nil {
		t.Fatalf("grantPortageAccess on a missing tree: %v", err)
	}
	if _, err := os.Lstat(missing); !os.IsNotExist(err) {
		t.Errorf("grantPortageAccess created %s; it must open trees, never make them", missing)
	}
}

// A symlink in the tree must not derail the walk, and the file it points at
// must come out unchanged.
//
// This does NOT prove the symlink branch in grantPortageAccess, and is named so
// it cannot be mistaken for a test that does: on Linux a symlink lstat's as
// 0777, so the `want == mode` check skips the Chmod with or without that
// branch — mutating the branch away leaves this test green. What it does hold
// down is the property the caller depends on, which is that a staged tree
// carrying a link out of itself is re-permissioned without touching the target.
func TestGrantPortageAccessLeavesASymlinkTargetUnchanged(t *testing.T) {
	withPortageGroupID(t, os.Getgid(), true)
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatalf("writing %s: %v", outside, err)
	}
	root := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("this filesystem does not support symlinks: %v", err)
	}

	if err := grantPortageAccess(root, true); err != nil {
		t.Fatalf("grantPortageAccess: %v", err)
	}

	if got := modeOf(t, outside); got != 0o600 {
		t.Errorf("the symlink target is %04o, was 0600; the walk followed a link out of the tree", got)
	}
}

func TestGroupBitsForMirrorsTheOwnerAndNeverExceedsIt(t *testing.T) {
	cases := []struct {
		name     string
		mode     fs.FileMode
		writable bool
		want     fs.FileMode
	}{
		{"a 0750 directory read-only", 0o750, false, 0o050},
		{"a 0750 directory writable", 0o750, true, 0o070},
		{"a 0600 file read-only", 0o600, false, 0o040},
		{"a 0600 file writable", 0o600, true, 0o060},
		{"an owner-read-only file cannot gain g+w", 0o400, true, 0o040},
		{"an owner with nothing grants nothing", 0o000, true, 0o000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := groupBitsFor(tc.mode, tc.writable); got != tc.want {
				t.Errorf("groupBitsFor(%04o, %v) = %04o, want %04o", tc.mode, tc.writable, got, tc.want)
			}
		})
	}
}

// The published overlay is not this gate's to re-permission, so an unstaged
// candidate's repoRoot must be left exactly as it was found.
func TestGrantCompileAccessLeavesAnUnstagedOverlayAlone(t *testing.T) {
	withPortageGroupID(t, os.Getgid(), true)
	overlay, _, file := stagedLikeTree(t)

	a := &Applier{}
	if err := a.grantCompileAccess(candidatePaths{staged: false, repoRoot: overlay}); err != nil {
		t.Fatalf("grantCompileAccess: %v", err)
	}

	if got := modeOf(t, file); got != 0o600 {
		t.Errorf("%s is %04o, was 0600; an unstaged candidate's overlay was re-permissioned", file, got)
	}
}

// The staged tree and the private distdir are opened together, each with its own
// stance — the distdir writable, the tree not.
func TestGrantCompileAccessOpensStagedTreeAndPrivateDistdir(t *testing.T) {
	withPortageGroupID(t, os.Getgid(), true)
	staged, _, stagedFile := stagedLikeTree(t)
	distdir, _, distFile := stagedLikeTree(t)

	a := &Applier{}
	err := a.grantCompileAccess(candidatePaths{staged: true, repoRoot: staged, fetchedDistdir: distdir})
	if err != nil {
		t.Fatalf("grantCompileAccess: %v", err)
	}

	if got := modeOf(t, stagedFile); got&0o040 == 0 || got&0o020 != 0 {
		t.Errorf("staged file is %04o; want the group to read it and not write it", got)
	}
	if got := modeOf(t, distFile); got&0o060 != 0o060 {
		t.Errorf("distdir file is %04o; want the group to read and write it", got)
	}
}

// stagingLikeLayout builds the shape the applier really stages into —
// <staging>/<category>/<package>/<version> — every level 0750, which is what
// stagedDirMode makes and what refused uid portage at the staging root.
func stagingLikeLayout(t *testing.T) (stagingRoot, repoRoot string, levels []string) {
	t.Helper()
	stagingRoot = filepath.Join(t.TempDir(), "staging")
	repoRoot = filepath.Join(stagingRoot, "sys-firmware", "edk2", "202608")
	if err := os.MkdirAll(repoRoot, 0o750); err != nil {
		t.Fatalf("creating %s: %v", repoRoot, err)
	}
	for _, d := range []string{stagingRoot, filepath.Dir(filepath.Dir(repoRoot)), filepath.Dir(repoRoot), repoRoot} {
		if err := os.Chmod(d, 0o750); err != nil {
			t.Fatalf("pinning the mode of %s: %v", d, err)
		}
		levels = append(levels, d)
	}
	return stagingRoot, repoRoot, levels
}

// Every directory between the staging root and the staged tree must become
// traversable, or the tree that was just opened cannot be reached at all.
func TestGrantPortageTraversalOpensEveryDirectoryDownToTheStagedTree(t *testing.T) {
	want := aSecondaryGID(t)
	withPortageGroupID(t, want, true)
	stagingRoot, repoRoot, levels := stagingLikeLayout(t)

	if err := grantPortageTraversal(repoRoot, stagingRoot); err != nil {
		t.Fatalf("grantPortageTraversal: %v", err)
	}

	// The staged root itself is grantPortageAccess's to open, not this one's.
	//
	// The GROUP is what is asserted. These directories are 0750, so they already
	// carried g+x before the call; what refused uid portage was that the group
	// holding those bits was the operator's, which it does not belong to.
	for _, dir := range levels[:len(levels)-1] {
		if got := gidOf(t, dir); got != want {
			t.Errorf("directory %s is owned by gid %d, want %d; uid portage cannot reach the staged tree", dir, got, want)
		}
	}
}

// Traversal adds x and never r. The case that can show it is a directory whose
// group could not enter it at ALL — 0700 — because the layout this normally runs
// against is 0750, which already grants its group r-x and leaves the chmod with
// nothing to do (there, the whole fix is the chgrp).
//
// A directory that did not let its group list it must still not, or opening a
// path to one staged package would leak the names of every other.
func TestGrantPortageTraversalAddsTraversalWithoutAddingRead(t *testing.T) {
	withPortageGroupID(t, os.Getgid(), true)
	stagingRoot, repoRoot, levels := stagingLikeLayout(t)
	for _, dir := range levels {
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Fatalf("closing %s to its group: %v", dir, err)
		}
	}

	if err := grantPortageTraversal(repoRoot, stagingRoot); err != nil {
		t.Fatalf("grantPortageTraversal: %v", err)
	}

	for _, dir := range levels[:len(levels)-1] {
		got := modeOf(t, dir)
		if got&0o010 == 0 {
			t.Errorf("directory %s is %04o; uid portage cannot traverse it (want g+x)", dir, got)
		}
		if got&0o040 != 0 {
			t.Errorf("directory %s is %04o; traversal must not add g+r to a 0700 directory", dir, got)
		}
	}
}

// A pair that is not ancestor-and-descendant must do nothing whatsoever. Without
// that bound a mistaken pair of paths would climb to / opening every directory
// it passed.
func TestGrantPortageTraversalRefusesAPathThatIsNotBelowTheRoot(t *testing.T) {
	withPortageGroupID(t, os.Getgid(), true)
	tmp := t.TempDir()
	elsewhere := filepath.Join(tmp, "elsewhere")
	unrelated := filepath.Join(tmp, "unrelated")
	for _, d := range []string{elsewhere, unrelated} {
		if err := os.Mkdir(d, 0o750); err != nil {
			t.Fatalf("creating %s: %v", d, err)
		}
	}

	// Pinned to 0700 first: t.TempDir() hands back 0755, whose g+x would satisfy
	// the assertion below whether or not the climb was ever bounded.
	if err := os.Chmod(tmp, 0o700); err != nil {
		t.Fatalf("pinning the mode of %s: %v", tmp, err)
	}

	if err := grantPortageTraversal(filepath.Join(elsewhere, "tree"), unrelated); err != nil {
		t.Fatalf("grantPortageTraversal on an unrelated pair: %v", err)
	}

	if got := modeOf(t, tmp); got&0o010 != 0 {
		t.Errorf("the common parent %s is %04o; the climb escaped its bound", tmp, got)
	}
	if got := modeOf(t, elsewhere); got != 0o750 {
		t.Errorf("%s is %04o, was 0750; an unrelated pair changed a directory", elsewhere, got)
	}
}

// The staged root and the staging root being the same directory leaves nothing
// in between, and must not walk to the filesystem root looking for one.
func TestGrantPortageTraversalIsANoOpWhenTheRootsAreTheSame(t *testing.T) {
	withPortageGroupID(t, os.Getgid(), true)
	root := t.TempDir()
	if err := os.Chmod(root, 0o750); err != nil {
		t.Fatalf("pinning the mode of %s: %v", root, err)
	}

	if err := grantPortageTraversal(root, root); err != nil {
		t.Fatalf("grantPortageTraversal: %v", err)
	}
	if got := modeOf(t, filepath.Dir(root)); got&0o010 == 0 {
		t.Skip("this host's temp parent is not traversable; nothing to assert")
	}
}

// grantCompileAccess must open the path as well as the tree — the whole point of
// the outer half of the fix.
func TestGrantCompileAccessOpensThePathToTheStagedTree(t *testing.T) {
	want := aSecondaryGID(t)
	withPortageGroupID(t, want, true)
	stagingRoot, repoRoot, levels := stagingLikeLayout(t)

	a := &Applier{stagingRoot: stagingRoot}
	if err := a.grantCompileAccess(candidatePaths{staged: true, repoRoot: repoRoot}); err != nil {
		t.Fatalf("grantCompileAccess: %v", err)
	}

	for _, dir := range levels {
		if got := gidOf(t, dir); got != want {
			t.Errorf("directory %s is owned by gid %d, want %d; grantCompileAccess opened the tree but not the path to it", dir, got, want)
		}
	}
}
