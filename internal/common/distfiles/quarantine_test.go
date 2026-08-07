package distfiles

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
)

// =============================================================================
// Test harness — the assertions are about the filesystem, not about strings
// =============================================================================
//
// Quarantine's whole subject is a directory the tool does not own and shares
// with the host's package manager, so "it did the right thing" can only be
// stated as "these bytes, modes and owners are still what they were". Every
// test below therefore snapshots a tree before the call and compares it after,
// rather than asserting on the names that come back.

// fsEntry is everything about one filesystem entry that Quarantine is forbidden
// to change. It is a comparable struct on purpose, so a whole tree can be
// diffed with ==: a mode bit, an owner or a byte that moved shows up without
// the assertion having to name it in advance.
type fsEntry struct {
	mode      fs.FileMode
	size      int64
	content   string // regular files only
	linkTo    string // symlinks only, read with Readlink (never followed)
	uid       uint32
	gid       uint32
	haveOwner bool
}

// snapshotTree records root and everything under it. It walks with Lstat
// semantics and never follows a symlink — following one would take the
// snapshot outside the tree it is supposed to be describing, which is the very
// thing several of these tests are checking does not happen.
func snapshotTree(t *testing.T, root string) map[string]fsEntry {
	t.Helper()
	out := map[string]fsEntry{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		info, infoErr := d.Info() // Lstat for the walked entry
		if infoErr != nil {
			return infoErr
		}
		entry := fsEntry{mode: info.Mode(), size: info.Size()}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, linkErr := os.Readlink(path)
			if linkErr != nil {
				return linkErr
			}
			entry.linkTo = target
		case info.Mode().IsRegular():
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			entry.content = string(data)
		}
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			entry.uid, entry.gid, entry.haveOwner = st.Uid, st.Gid, true
		}
		out[rel] = entry
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %q: %v", root, err)
	}
	return out
}

// assertTreeUnchanged reports every way the tree under root differs from the
// snapshot taken before the call — a file that changed, one that vanished, and
// one that appeared are three different failures and are named as three.
func assertTreeUnchanged(t *testing.T, before map[string]fsEntry, root, why string) {
	t.Helper()
	assertSameTree(t, before, snapshotTree(t, root), root, why)
}

// assertSameTree is that comparison with the snapshotting taken out of it, so a
// caller that has to relax one field can normalise BOTH sides before the diff
// — see withoutDirSizes in cleanup_test.go, where a removal legitimately
// changes the size of the directory that held the file — instead of
// re-deriving this loop and letting the two drift.
func assertSameTree(t *testing.T, before, after map[string]fsEntry, root, why string) {
	t.Helper()
	for name, want := range before {
		got, ok := after[name]
		if !ok {
			t.Errorf("%s: %q disappeared from %s", why, name, root)
			continue
		}
		if got != want {
			t.Errorf("%s: %q changed under %s\n  before: %+v\n  after:  %+v", why, name, root, want, got)
		}
	}
	for name := range after {
		if _, ok := before[name]; !ok {
			t.Errorf("%s: %q appeared under %s", why, name, root)
		}
	}
}

// withoutSubtree drops one directory AND everything under it from a snapshot,
// so a tree can be asserted unchanged except for the part that is meant to
// change. Deleting the directory's own key is not enough — its children are
// separate entries, and leaving them in silently compares a subtree the caller
// meant to exclude.
func withoutSubtree(snap map[string]fsEntry, rel string) map[string]fsEntry {
	out := make(map[string]fsEntry, len(snap))
	prefix := rel + string(filepath.Separator)
	for name, entry := range snap {
		if name == rel || strings.HasPrefix(name, prefix) {
			continue
		}
		out[name] = entry
	}
	return out
}

// seedFile writes one file with an exact mode. The chmod is separate from the
// write because the process umask would otherwise decide the mode, and a test
// about permissions cannot be at the mercy of the environment's umask.
func seedFile(t *testing.T, dir, name, content string, mode fs.FileMode) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("seed %q: %v", path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %q: %v", path, err)
	}
	return path
}

// dirEntryNames lists a directory, for failure messages that say what is
// actually there instead of only what was expected.
func dirEntryNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %q: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// assertIsAQuarantineNameFor checks the shape the report depends on: the
// returned string is a bare filename (not a path), it carries the marker, and
// the original distfile name is its prefix. That last part is the contract —
// one returned string has to tell an operator both what was moved and where it
// went, because the file itself is the only record left in a directory holding
// thousands of others.
func assertIsAQuarantineNameFor(t *testing.T, got, original string) {
	t.Helper()
	if filepath.Base(got) != got {
		t.Errorf("quarantine name %q is a path, not a name; the caller joins it to the distdir itself", got)
	}
	if !strings.HasPrefix(got, original+quarantineInfix) {
		t.Errorf("quarantine name %q does not begin with %q; the moved file's identity must survive in its own name", got, original+quarantineInfix)
	}
	if got == original {
		t.Errorf("quarantine name %q is the original name; nothing was actually moved aside", got)
	}
}

// =============================================================================
// Case 2 — a file the current Manifest lists is already verified (R2.1)
// =============================================================================

// TestQuarantineLeavesAManifestListedFileAlone is R2.1's reuse. A distfile named
// on a DIST line of the package's current Manifest has already been checked
// against a size and a checksum, so it is exactly what the shared distdir is
// worth having: the download is skipped because the file is there. Quarantining
// it would throw that reuse away and send pkgdev back to the network.
func TestQuarantineLeavesAManifestListedFileAlone(t *testing.T) {
	t.Run("every expected file is listed, so nothing moves", func(t *testing.T) {
		dist := t.TempDir()
		seedFile(t, dist, "hello-1.0.0.tar.gz", "a complete archive", 0o644)
		seedFile(t, dist, "world-2.tar.xz", "another complete archive", 0o640)
		before := snapshotTree(t, dist)

		listed := []string{"hello-1.0.0.tar.gz", "world-2.tar.xz"}
		moved, err := Quarantine(dist, listed, listed)
		if err != nil {
			t.Fatalf("Quarantine() error = %v", err)
		}
		if len(moved) != 0 {
			t.Errorf("Quarantine moved %v; a file the current Manifest lists is verified and must be reused in place (R2.1)", moved)
		}
		assertTreeUnchanged(t, before, dist, "a listed distfile must be left exactly as it was")
	})

	t.Run("listed and unlisted side by side: only the unlisted one moves", func(t *testing.T) {
		// The decision is per name. An implementation that answered the
		// question once for the whole call — leaving everything alone because
		// something was listed, or moving everything because something was not
		// — passes neither half of this.
		dist := t.TempDir()
		seedFile(t, dist, "hello-1.0.0.tar.gz", "verified", 0o644)
		seedFile(t, dist, "hello-1.1.0.tar.gz", "truncated", 0o644)

		moved, err := Quarantine(dist,
			[]string{"hello-1.0.0.tar.gz"},
			[]string{"hello-1.0.0.tar.gz", "hello-1.1.0.tar.gz"})
		if err != nil {
			t.Fatalf("Quarantine() error = %v", err)
		}
		if len(moved) != 1 {
			t.Fatalf("Quarantine() moved %v, want exactly the unlisted hello-1.1.0.tar.gz", moved)
		}
		assertIsAQuarantineNameFor(t, moved[0], "hello-1.1.0.tar.gz")

		// The listed one is still there, under its own name, with its bytes.
		data, err := os.ReadFile(filepath.Join(dist, "hello-1.0.0.tar.gz"))
		if err != nil {
			t.Fatalf("the listed distfile must survive under its own name: %v", err)
		}
		if string(data) != "verified" {
			t.Errorf("listed distfile content = %q, want %q", data, "verified")
		}
		if _, err := os.Lstat(filepath.Join(dist, "hello-1.1.0.tar.gz")); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("the unlisted distfile is still under its final name (lstat err = %v); pkgdev would digest it", err)
		}
	})

	t.Run("the comparison is made on base names on both sides", func(t *testing.T) {
		// Both lists are reduced with filepath.Base, so a Manifest name and an
		// expected name that differ only in a path prefix still describe the
		// same file. Reducing one side and not the other would misclassify a
		// verified file as unverifiable.
		dist := t.TempDir()
		seedFile(t, dist, "hello-1.0.0.tar.gz", "verified", 0o644)
		before := snapshotTree(t, dist)

		moved, err := Quarantine(dist,
			[]string{"hello-1.0.0.tar.gz"},
			[]string{filepath.Join("sub", "dir", "hello-1.0.0.tar.gz")})
		if err != nil {
			t.Fatalf("Quarantine() error = %v", err)
		}
		if len(moved) != 0 {
			t.Errorf("Quarantine moved %v; both sides reduce to hello-1.0.0.tar.gz, which the Manifest lists", moved)
		}
		assertTreeUnchanged(t, before, dist, "a listed distfile must be left exactly as it was")
	})
}

// =============================================================================
// Case 3 — present and unlisted is unverifiable (R2.2)
// =============================================================================

// TestQuarantineMovesAnUnlistedFileAside is R2.2, and the reason this function
// exists. Portage's FETCHCOMMAND writes straight to ${DISTDIR}/${FILE}, so a
// killed fetch leaves a truncated file under the FINAL name; on a bump the
// current Manifest does not list that name yet, so nothing can compare it
// against anything and the next run digests it. This overlay pushes on its own,
// so that checksum reaches users.
func TestQuarantineMovesAnUnlistedFileAside(t *testing.T) {
	t.Run("a truncated download under the new version's name is moved and reported", func(t *testing.T) {
		dist := t.TempDir()
		// The realistic shape of a bump: the old distfile is listed and stays,
		// the new one is a half-written file nothing has vouched for.
		seedFile(t, dist, "hello-1.0.0.tar.gz", "the previous release", 0o644)
		seedFile(t, dist, "hello-1.1.0.tar.gz", "half a tarba", 0o644)

		moved, err := Quarantine(dist,
			[]string{"hello-1.0.0.tar.gz"},
			[]string{"hello-1.1.0.tar.gz"})
		if err != nil {
			t.Fatalf("Quarantine() error = %v", err)
		}
		if len(moved) != 1 {
			t.Fatalf("Quarantine() = %v, want one quarantined name; the directory now holds %v", moved, dirEntryNames(t, dist))
		}
		assertIsAQuarantineNameFor(t, moved[0], "hello-1.1.0.tar.gz")

		// Nothing is left under the name pkgdev is about to fetch.
		if _, err := os.Lstat(filepath.Join(dist, "hello-1.1.0.tar.gz")); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("the unverifiable file is still under its final name (lstat err = %v); it would be digested (R2.2)", err)
		}
		// Moved, not deleted (R2.5): the bytes are still recoverable, in this
		// directory, under the reported name.
		quarantined := filepath.Join(dist, moved[0])
		data, err := os.ReadFile(quarantined)
		if err != nil {
			t.Fatalf("the quarantined file must still exist — quarantine is not deletion: %v", err)
		}
		if string(data) != "half a tarba" {
			t.Errorf("quarantined content = %q, want the original bytes %q", data, "half a tarba")
		}
		if got := filepath.Dir(quarantined); got != dist {
			t.Errorf("quarantined file landed in %q, want a sibling inside %q", got, dist)
		}
		// The listed sibling is untouched.
		if data, err := os.ReadFile(filepath.Join(dist, "hello-1.0.0.tar.gz")); err != nil || string(data) != "the previous release" {
			t.Errorf("the listed distfile was disturbed; read = %q, err = %v", data, err)
		}
		if names := dirEntryNames(t, dist); len(names) != 2 {
			t.Errorf("directory holds %v, want exactly the listed distfile and the one quarantine", names)
		}
	})

	t.Run("a symlink is moved, and the file it points at is not", func(t *testing.T) {
		// PrepopulateFromCache puts symlinks into this very directory, pointing
		// at the read-only portage cache. Quarantining one must move the LINK:
		// following it would move a file out of a cache that is not ours, and
		// leaving it would let pkgdev digest a target nothing verified.
		sandbox := t.TempDir()
		cache := filepath.Join(sandbox, "cache")
		dist := filepath.Join(sandbox, "dist")
		for _, d := range []string{cache, dist} {
			if err := os.Mkdir(d, 0o750); err != nil {
				t.Fatalf("mkdir %q: %v", d, err)
			}
		}
		target := seedFile(t, cache, "cached-1.0.tar.gz", "the cached bytes", 0o644)
		link := filepath.Join(dist, "cached-1.0.tar.gz")
		if err := os.Symlink(target, link); err != nil {
			t.Fatalf("symlink: %v", err)
		}
		cacheBefore := snapshotTree(t, cache)

		moved, err := Quarantine(dist, nil, []string{"cached-1.0.tar.gz"})
		if err != nil {
			t.Fatalf("Quarantine() error = %v", err)
		}
		if len(moved) != 1 {
			t.Fatalf("Quarantine() = %v, want the unlisted symlink quarantined", moved)
		}
		assertTreeUnchanged(t, cacheBefore, cache, "quarantining a symlink must not touch what it points at")

		info, err := os.Lstat(filepath.Join(dist, moved[0]))
		if err != nil {
			t.Fatalf("lstat quarantined entry: %v", err)
		}
		if info.Mode()&fs.ModeSymlink == 0 {
			t.Errorf("the quarantined entry is a %v, want a symlink; the link was followed instead of moved", info.Mode())
		}
		if got, err := os.Readlink(filepath.Join(dist, moved[0])); err != nil || got != target {
			t.Errorf("quarantined symlink points at %q (err %v), want the untouched %q", got, err, target)
		}
	})

	t.Run("a dangling symlink is quarantined, not mistaken for an absent file", func(t *testing.T) {
		// This is why the check is Lstat and not Stat. Stat resolves the link,
		// reports ENOENT for a broken one, and would call the name absent —
		// leaving a dangling link sitting under exactly the name pkgdev is
		// about to fetch.
		dist := t.TempDir()
		link := filepath.Join(dist, "ghost-3.tar.gz")
		if err := os.Symlink(filepath.Join(t.TempDir(), "never-existed"), link); err != nil {
			t.Fatalf("symlink: %v", err)
		}

		moved, err := Quarantine(dist, nil, []string{"ghost-3.tar.gz"})
		if err != nil {
			t.Fatalf("Quarantine() error = %v", err)
		}
		if len(moved) != 1 {
			t.Fatalf("Quarantine() = %v, want the dangling symlink quarantined; the directory holds %v", moved, dirEntryNames(t, dist))
		}
		assertIsAQuarantineNameFor(t, moved[0], "ghost-3.tar.gz")
		if _, err := os.Lstat(link); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("the dangling symlink is still under its final name (lstat err = %v)", err)
		}
	})

	t.Run("a directory under an expected name is not a distfile and is not moved", func(t *testing.T) {
		// A distdir belongs to the host and may hold subdirectories of its own
		// — git3-src and the like. Renaming one because its name happened to
		// match would break the host's own tooling, and a directory is not a
		// file a fetch could have truncated in the first place.
		dist := t.TempDir()
		sub := filepath.Join(dist, "git3-src")
		if err := os.Mkdir(sub, 0o750); err != nil {
			t.Fatalf("mkdir %q: %v", sub, err)
		}
		seedFile(t, sub, "checkout.tar", "the host's own working tree", 0o644)
		before := snapshotTree(t, dist)

		moved, err := Quarantine(dist, nil, []string{"git3-src"})
		if err != nil {
			t.Fatalf("Quarantine() error = %v", err)
		}
		if len(moved) != 0 {
			t.Errorf("Quarantine() = %v; a directory is not a distfile and is not ours to move", moved)
		}
		assertTreeUnchanged(t, before, dist, "a subdirectory of the host's distdir must be left alone")
	})

	t.Run("two quarantines of one name never overwrite each other", func(t *testing.T) {
		// A quarantine that replaced an earlier one would destroy the evidence
		// the whole move-instead-of-delete rule exists to preserve.
		dist := t.TempDir()
		seedFile(t, dist, "hello-1.1.0.tar.gz", "first attempt", 0o644)
		first, err := Quarantine(dist, nil, []string{"hello-1.1.0.tar.gz"})
		if err != nil || len(first) != 1 {
			t.Fatalf("first Quarantine() = %v, %v", first, err)
		}
		seedFile(t, dist, "hello-1.1.0.tar.gz", "second attempt", 0o644)
		second, err := Quarantine(dist, nil, []string{"hello-1.1.0.tar.gz"})
		if err != nil || len(second) != 1 {
			t.Fatalf("second Quarantine() = %v, %v", second, err)
		}

		if first[0] == second[0] {
			t.Fatalf("both quarantines chose the name %q; the second would replace the first", first[0])
		}
		for name, want := range map[string]string{first[0]: "first attempt", second[0]: "second attempt"} {
			data, err := os.ReadFile(filepath.Join(dist, name))
			if err != nil {
				t.Errorf("quarantined file %q is gone: %v", name, err)
				continue
			}
			if string(data) != want {
				t.Errorf("quarantined file %q content = %q, want %q", name, data, want)
			}
		}
	})

	t.Run("concurrent workers on the same files neither collide nor lose one", func(t *testing.T) {
		// Sub-task 3.3 adds the per-distfile lock; until it lands — and after
		// it, for the second bentoo run that does not share its locks — several
		// workers reach the same files. Each file must be moved exactly once,
		// reported exactly once, and losing the race must not be an error.
		//
		// The list is long and shared on purpose. Losing at the Lstat is easy
		// to hit; losing BETWEEN the Lstat and the Rename, which is the branch
		// that has to tolerate an ENOENT from the rename itself, is a window a
		// single filename almost never opens. Hundreds of names per worker put
		// every worker inside that window repeatedly.
		dist := t.TempDir()
		const (
			workers = 8
			files   = 300
		)
		expected := make([]string, 0, files)
		for i := 0; i < files; i++ {
			name := fmt.Sprintf("hello-1.1.%d.tar.gz", i)
			seedFile(t, dist, name, "truncated", 0o644)
			expected = append(expected, name)
		}

		results := make([][]string, workers)
		errs := make([]error, workers)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start // widen the window: everyone starts at once
				results[i], errs[i] = Quarantine(dist, nil, expected)
			}(i)
		}
		close(start)
		wg.Wait()

		total := 0
		reported := map[string]int{}
		for i, err := range errs {
			if err != nil {
				t.Errorf("worker %d error = %v; losing a race to another worker is not a failure — the file it was going to move aside has already been moved aside", i, err)
			}
			total += len(results[i])
			for _, name := range results[i] {
				reported[name]++
			}
		}
		if total != files {
			t.Errorf("%d workers reported %d moves in total, want exactly %d (one per file)", workers, total, files)
		}
		for name, times := range reported {
			if times != 1 {
				t.Errorf("%q was reported %d times, want once", name, times)
			}
		}
		if names := dirEntryNames(t, dist); len(names) != files {
			t.Errorf("directory holds %d entries after %d concurrent quarantines, want exactly %d", len(names), workers, files)
		}
	})
}

// =============================================================================
// Case 1 — absent is the ordinary case, and unknowable is not absent
// =============================================================================

// TestQuarantineIgnoresAbsentFiles pins the case that runs on nearly every
// package: the distfiles the new version needs have not been downloaded yet, so
// there is nothing to protect against and nothing to touch. It also pins the
// case that looks like absence and is not — an inspection that fails answers no
// question at all, and treating it as "nothing there" would hand pkgdev the
// file this function was supposed to have looked at.
func TestQuarantineIgnoresAbsentFiles(t *testing.T) {
	t.Run("nothing present, nothing moved, nothing created", func(t *testing.T) {
		dist := t.TempDir()
		before := snapshotTree(t, dist)

		moved, err := Quarantine(dist,
			[]string{"hello-1.0.0.tar.gz"},
			[]string{"hello-1.1.0.tar.gz", "hello-1.0.0.tar.gz", "extra-2.tar.xz"})
		if err != nil {
			t.Fatalf("Quarantine() error = %v", err)
		}
		if len(moved) != 0 {
			t.Errorf("Quarantine() = %v, want nothing; none of those files exists", moved)
		}
		assertTreeUnchanged(t, before, dist, "an absent distfile must produce no filesystem activity at all")
	})

	t.Run("an empty expected list leaves a populated distdir alone", func(t *testing.T) {
		dist := t.TempDir()
		seedFile(t, dist, "unrelated-9.tar.gz", "someone else's download", 0o644)
		seedFile(t, dist, "another-1.tar.xz", "and another", 0o600)
		before := snapshotTree(t, dist)

		moved, err := Quarantine(dist, nil, nil)
		if err != nil {
			t.Fatalf("Quarantine() error = %v", err)
		}
		if len(moved) != 0 {
			t.Errorf("Quarantine() = %v, want nothing; this package expects no distfiles", moved)
		}
		// The distdir is the host's and holds every download the machine has
		// made. Only the names a package expects may be considered at all.
		assertTreeUnchanged(t, before, dist, "files nobody asked about must not be touched")
	})

	t.Run("an inspection that fails is an error, not an absence", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("skipping: running as root (euid 0), where a directory without the search bit is still searchable. " +
				"Passing here would be vacuous, so it is skipped instead of faked. CI runs unprivileged.")
		}
		dist := t.TempDir()
		seedFile(t, dist, "hello-1.1.0.tar.gz", "truncated", 0o644)
		// Readable but not searchable: the names can be listed, an individual
		// entry cannot be stat'ed. That is the shape of "we cannot tell what is
		// there", as opposed to "there is nothing there".
		if err := os.Chmod(dist, 0o600); err != nil {
			t.Fatalf("chmod %q: %v", dist, err)
		}
		restored := false
		restore := func() {
			if !restored {
				restored = true
				_ = os.Chmod(dist, 0o750)
			}
		}
		t.Cleanup(restore)
		if _, err := os.Lstat(filepath.Join(dist, "hello-1.1.0.tar.gz")); err == nil {
			restore()
			t.Skip("skipping: this filesystem does not enforce the missing search bit, so the failure cannot be staged")
		}

		moved, err := Quarantine(dist, nil, []string{"hello-1.1.0.tar.gz"})
		restore()

		if err == nil {
			t.Fatalf("Quarantine() error = nil while the file could not even be inspected; moved = %v. A state we cannot read is not an absence, and pkgdev would go on to digest it", moved)
		}
		if len(moved) != 0 {
			t.Errorf("Quarantine() reported %v as moved while failing", moved)
		}
		if !errors.Is(err, fs.ErrPermission) {
			t.Errorf("the underlying os error must survive the wrap; errors.Is(err, fs.ErrPermission) = false for %v", err)
		}
		if !strings.Contains(err.Error(), "hello-1.1.0.tar.gz") {
			t.Errorf("the failure must name the distfile it could not inspect; err = %v", err)
		}
		assertNamesTheDirectory(t, err, dist)
	})

	t.Run("an empty distdir is refused rather than resolved against the working directory", func(t *testing.T) {
		// filepath.Join("", name) is a relative path. Without this guard the
		// function would inspect — and rename — files in whatever directory the
		// process happens to be in.
		sandbox := t.TempDir()
		t.Chdir(sandbox)
		seedFile(t, sandbox, "hello-1.1.0.tar.gz", "a file in the working directory", 0o644)
		before := snapshotTree(t, sandbox)

		moved, err := Quarantine("", nil, []string{"hello-1.1.0.tar.gz"})
		if err == nil {
			t.Fatalf("Quarantine(\"\", …) error = nil, moved = %v; an empty distdir is not a directory to work in", moved)
		}
		if len(moved) != 0 {
			t.Errorf("Quarantine(\"\", …) = %v, want nothing", moved)
		}
		assertTreeUnchanged(t, before, sandbox, "an empty distdir must not make the working directory the target")
	})
}

// =============================================================================
// Untrusted names
// =============================================================================

// TestQuarantineRejectsPathTraversalInDistNames states the security property as
// an OUTCOME rather than as a string rewrite: whatever the name looks like,
// nothing outside distdir is read, moved or created.
//
// The names reaching this function come from a parsed Manifest and from a
// resolved version, and the directory they are joined to is shared with the
// host's package manager, so a traversal here writes outside a directory the
// tool does not own. filepath.Base neutralises the classic form by construction
// — "../../etc/passwd" is "passwd" — but Base alone is not enough: it returns
// "." and ".." unchanged, and filepath.Join(distdir, "..") is the distdir's
// PARENT, which on the default distdir is /var/cache.
func TestQuarantineRejectsPathTraversalInDistNames(t *testing.T) {
	// newSandbox builds a tree deep enough that a naive join lands INSIDE it
	// and can therefore be observed:
	//
	//   sandbox/loot/secret.tar.gz     <- two levels up from the distdir
	//   sandbox/outer/secret.tar.gz    <- one level up from the distdir
	//   sandbox/outer/dist/            <- the distdir
	//   sandbox/outer/dist/unrelated-9.tar.gz
	//
	// Nothing named secret.tar.gz lives in the distdir, so any move of one is
	// unambiguous evidence of an escape.
	newSandbox := func(t *testing.T) (sandbox, dist string) {
		t.Helper()
		sandbox = t.TempDir()
		loot := filepath.Join(sandbox, "loot")
		outer := filepath.Join(sandbox, "outer")
		dist = filepath.Join(outer, "dist")
		for _, d := range []string{loot, outer, dist} {
			if err := os.Mkdir(d, 0o750); err != nil {
				t.Fatalf("mkdir %q: %v", d, err)
			}
		}
		seedFile(t, loot, "secret.tar.gz", "two levels up", 0o600)
		seedFile(t, outer, "secret.tar.gz", "one level up", 0o600)
		seedFile(t, dist, "unrelated-9.tar.gz", "someone else's download", 0o644)
		return sandbox, dist
	}

	t.Run("a hostile name touches nothing, inside or outside", func(t *testing.T) {
		cases := []struct {
			name string
			give func(sandbox string) string
		}{
			{"one level up", func(string) string { return filepath.Join("..", "secret.tar.gz") }},
			{"two levels up", func(string) string { return filepath.Join("..", "..", "loot", "secret.tar.gz") }},
			{"an absolute path", func(sandbox string) string { return filepath.Join(sandbox, "loot", "secret.tar.gz") }},
			{"the parent directory itself", func(string) string { return ".." }},
			{"the distdir itself", func(string) string { return "." }},
			{"an empty name", func(string) string { return "" }},
			{"the filesystem root", func(string) string { return string(filepath.Separator) }},
			{"a name that is only separators", func(string) string { return "///" }},
			{"a nested relative path to something absent", func(string) string { return filepath.Join("sub", "dir", "absent.tar.gz") }},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				// A fresh sandbox per case, so one case cannot pass because a
				// previous one already moved the file it was looking for.
				sandbox, dist := newSandbox(t)
				before := snapshotTree(t, sandbox)
				give := tc.give(sandbox)

				// Passed on both sides: manifestNames is parsed from the same
				// untrusted file, and a hostile name there must not steer
				// anything either.
				moved, err := Quarantine(dist, []string{give}, []string{give})
				if err != nil {
					t.Fatalf("Quarantine(dist, %q) error = %v; a name that reduces to nothing usable is skipped, not an error", give, err)
				}
				if len(moved) != 0 {
					t.Errorf("Quarantine(dist, %q) = %v, want nothing moved", give, moved)
				}
				assertTreeUnchanged(t, before, sandbox, "no name may reach outside the distdir")
			})
		}
	})

	t.Run("the name is reduced to its base, not thrown away", func(t *testing.T) {
		// Without this the test above would pass vacuously: an implementation
		// that refused every name containing a separator — or refused every
		// name at all — would touch nothing and satisfy all of it, while
		// quarantining nothing in production either.
		sandbox, dist := newSandbox(t)
		outer := filepath.Join(sandbox, "outer")
		loot := filepath.Join(sandbox, "loot")
		// The same base name now DOES exist in the distdir, unlisted.
		seedFile(t, dist, "secret.tar.gz", "the one inside the distdir", 0o644)
		outerBefore := snapshotTree(t, outer)
		lootBefore := snapshotTree(t, loot)

		give := filepath.Join("..", "secret.tar.gz")
		moved, err := Quarantine(dist, nil, []string{give})
		if err != nil {
			t.Fatalf("Quarantine(dist, %q) error = %v", give, err)
		}
		if len(moved) != 1 {
			t.Fatalf("Quarantine(dist, %q) = %v, want the distdir's own secret.tar.gz quarantined", give, moved)
		}
		assertIsAQuarantineNameFor(t, moved[0], "secret.tar.gz")

		// The one that moved is the one INSIDE the distdir.
		data, err := os.ReadFile(filepath.Join(dist, moved[0]))
		if err != nil {
			t.Fatalf("read quarantined file: %v", err)
		}
		if string(data) != "the one inside the distdir" {
			t.Errorf("quarantined content = %q; the wrong secret.tar.gz was moved", data)
		}
		// Deliberately compare the parent EXCLUDING the distdir subtree, which
		// is expected to have changed.
		outerBefore = withoutSubtree(outerBefore, "dist")
		outerAfter := withoutSubtree(snapshotTree(t, outer), "dist")
		for name, want := range outerBefore {
			if got, ok := outerAfter[name]; !ok || got != want {
				t.Errorf("%q outside the distdir changed or vanished: before %+v, after %+v (present=%v)", name, want, got, ok)
			}
		}
		for name := range outerAfter {
			if _, ok := outerBefore[name]; !ok {
				t.Errorf("%q appeared outside the distdir", name)
			}
		}
		assertTreeUnchanged(t, lootBefore, loot, "a reduced name must not reach two levels up either")
	})

	t.Run("the reduction itself refuses a name that is not a filename", func(t *testing.T) {
		// The sub-tests above assert the OUTCOME, which is what matters. This
		// one pins the MECHANISM, and it is here because the outcome alone
		// cannot see it: today "." and ".." are also stopped one step later, by
		// the check that refuses to move a directory, so removing this guard
		// leaves every assertion above green.
		//
		// The guard still has to stay. It decides lexically, before the
		// filesystem is consulted at all, that filepath.Join(distdir, "..") —
		// /var/cache under the default distdir — is not a candidate; the later
		// check only holds while it is a stat of a directory that stops it. And
		// this reduction is the package's one place for turning an untrusted
		// name into a path element: the sibling operations sub-tasks 3.2 and
		// 3.3 add (removing an artefact, creating a lock file) have no
		// directory check to fall back on.
		refused := []struct {
			name string
			give string
		}{
			{"empty", ""},
			{"the current directory", "."},
			{"the parent directory", ".."},
			// Written out rather than built with filepath.Join, which would
			// clean the ".." away before the function ever saw it — the point
			// is a name whose LAST element is "..".
			{"a path whose last element is the parent", "some" + string(filepath.Separator) + "where" + string(filepath.Separator) + ".."},
			{"the filesystem root", string(filepath.Separator)},
			{"only separators", "///"},
		}
		for _, tc := range refused {
			t.Run(tc.name, func(t *testing.T) {
				if got, ok := distfileName(tc.give); ok {
					t.Errorf("distfileName(%q) = %q, true; joining that to the distdir names a directory, not a file in it", tc.give, got)
				}
			})
		}

		accepted := map[string]string{
			"hello-1.0.0.tar.gz":       "hello-1.0.0.tar.gz",
			"../../etc/passwd":         "passwd",
			"/absolute/hello.tar.gz":   "hello.tar.gz",
			"sub/dir/world-2.tar.xz":   "world-2.tar.xz",
			"..hidden-but-a-file.tar":  "..hidden-but-a-file.tar",
			"trailing/slash/name.tar/": "name.tar",
		}
		for give, want := range accepted {
			got, ok := distfileName(give)
			if !ok {
				t.Errorf("distfileName(%q) refused a name that reduces to the filename %q; refusing everything would make the assertions above vacuous", give, want)
				continue
			}
			if got != want {
				t.Errorf("distfileName(%q) = %q, want %q", give, got, want)
			}
		}
	})
}

// =============================================================================
// R2.5 — the directory is the host's
// =============================================================================

// TestQuarantineDoesNotChangeDirPermissions is R2.5, asserted twice.
//
// Behaviourally: the distdir's own mode and owner, the modes and owners of the
// files left in place, and the mode and owner of the file that was moved are
// all byte-identical before and after. The moved file matters as much as the
// others — a rename carries the file's metadata across untouched, so a
// difference there means something changed it on purpose.
//
// Structurally: no function in this package calls anything named Chmod, Chown
// or Lchown. The behavioural half only describes today's code; the structural
// half is what a future edit has to get past. On a Gentoo host the default
// distdir is portage:portage 0775 and holds every download the machine has ever
// made — "fixing" its mode to make a run succeed would be a change to the
// system, made by a tool that was only asked to bump a package.
func TestQuarantineDoesNotChangeDirPermissions(t *testing.T) {
	t.Run("modes and owners survive the call", func(t *testing.T) {
		dist := t.TempDir()
		if err := os.Chmod(dist, 0o755); err != nil {
			t.Fatalf("chmod %q: %v", dist, err)
		}
		seedFile(t, dist, "hello-1.0.0.tar.gz", "verified", 0o640)
		seedFile(t, dist, "hello-1.1.0.tar.gz", "truncated", 0o604)
		seedFile(t, dist, "unrelated-9.tar.gz", "not ours", 0o600)

		dirBefore := lstatEntry(t, dist)
		keepBefore := lstatEntry(t, filepath.Join(dist, "hello-1.0.0.tar.gz"))
		bystanderBefore := lstatEntry(t, filepath.Join(dist, "unrelated-9.tar.gz"))
		strayBefore := lstatEntry(t, filepath.Join(dist, "hello-1.1.0.tar.gz"))

		moved, err := Quarantine(dist,
			[]string{"hello-1.0.0.tar.gz"},
			[]string{"hello-1.0.0.tar.gz", "hello-1.1.0.tar.gz"})
		if err != nil {
			t.Fatalf("Quarantine() error = %v", err)
		}
		if len(moved) != 1 {
			t.Fatalf("Quarantine() = %v, want one quarantine; without a move this test asserts nothing", moved)
		}

		if got := lstatEntry(t, dist); got != dirBefore {
			t.Errorf("the distdir itself changed\n  before: %+v\n  after:  %+v", dirBefore, got)
		}
		if got := lstatEntry(t, filepath.Join(dist, "hello-1.0.0.tar.gz")); got != keepBefore {
			t.Errorf("a distfile left in place changed\n  before: %+v\n  after:  %+v", keepBefore, got)
		}
		if got := lstatEntry(t, filepath.Join(dist, "unrelated-9.tar.gz")); got != bystanderBefore {
			t.Errorf("a file nobody asked about changed\n  before: %+v\n  after:  %+v", bystanderBefore, got)
		}
		if got := lstatEntry(t, filepath.Join(dist, moved[0])); got != strayBefore {
			t.Errorf("the quarantined file's own metadata changed; a rename carries it across untouched\n  before: %+v\n  after:  %+v", strayBefore, got)
		}
	})

	t.Run("no function in this package changes a mode or an owner", func(t *testing.T) {
		// Vacuity guard first: prove the detector sees such a call when there
		// is one, before trusting it to report that there is none.
		probe := metadataCallsInSource(t, "probe.go", `package p

import "os"

func repairs(f *os.File, path string) {
	_ = os.Chmod(path, 0o755)
	_ = f.Chown(0, 0)
	_ = os.Lchown(path, 0, 0)
}
`)
		for _, want := range []string{"Chmod", "Chown", "Lchown"} {
			if !strings.Contains(strings.Join(probe["repairs"], ","), want) {
				t.Fatalf("the scan missed %s in its own probe source (found %v); it is not looking at what it thinks it is", want, probe)
			}
		}

		found := metadataCallsInPackage(t)
		for owner, calls := range found {
			t.Errorf("%s calls %v; this package must not change the permissions or ownership of a distdir it did not create (R2.5) — "+
				"on a Gentoo host that directory is portage:portage 0775 and holds every download the machine has made", owner, calls)
		}
	})
}

// lstatEntry describes one path the way R2.5 cares about it: mode bits and
// ownership, taken with Lstat so a symlink is described rather than followed.
func lstatEntry(t *testing.T, path string) fsEntry {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("lstat %q: %v", path, err)
	}
	entry := fsEntry{mode: info.Mode()}
	if info.Mode().IsRegular() {
		entry.size = info.Size()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %q: %v", path, err)
		}
		entry.content = string(data)
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		entry.uid, entry.gid, entry.haveOwner = st.Uid, st.Gid, true
	}
	return entry
}

// metadataCallsInPackage parses this package's non-test sources and returns, per
// function, every call to something named Chmod, Chown or Lchown.
//
// The receiver is deliberately not checked: os.Chmod, (*os.File).Chmod and
// (*os.Root).Chmod all change a mode, and R2.5 is about the effect rather than
// about which spelling produced it.
func metadataCallsInPackage(t *testing.T) map[string][]string {
	t.Helper()

	// The package directory comes from this file's own path, not from the
	// working directory, which other tests here change.
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test's source file")
	}
	pkgDir := filepath.Dir(thisFile)

	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		t.Fatalf("read package directory %q: %v", pkgDir, err)
	}

	found := map[string][]string{}
	sources := 0
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		sources++
		file, err := parser.ParseFile(fset, filepath.Join(pkgDir, name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		collectMetadataCalls(file, found)
	}
	if sources == 0 {
		t.Fatalf("no non-test Go source found in %q; the scan would pass vacuously", pkgDir)
	}
	return found
}

// metadataCallsInSource runs the same scan over one in-memory source, so the
// detector can be proved to work before it is used to prove an absence.
func metadataCallsInSource(t *testing.T, name, src string) map[string][]string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), name, src, 0)
	if err != nil {
		t.Fatalf("parse probe source: %v", err)
	}
	found := map[string][]string{}
	collectMetadataCalls(file, found)
	return found
}

func collectMetadataCalls(file *ast.File, into map[string][]string) {
	for _, decl := range file.Decls {
		owner := "<package level>"
		if fn, isFunc := decl.(*ast.FuncDecl); isFunc && fn.Name != nil {
			owner = fn.Name.Name
		}
		ast.Inspect(decl, func(n ast.Node) bool {
			sel, isSelector := n.(*ast.SelectorExpr)
			if !isSelector {
				return true
			}
			switch sel.Sel.Name {
			case "Chmod", "Chown", "Lchown":
				into[owner] = append(into[owner], sel.Sel.Name)
			}
			return true
		})
	}
}
