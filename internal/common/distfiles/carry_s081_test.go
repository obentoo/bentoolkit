// Story 081 — sub-task 1.1. CarryOver hands the LLM repair the first attempt's
// completed downloads (R1.1, R1.2, R1.3, R1.5).
//
// The repair's distdir is a directory an agent holding Write can modify. What
// reaches it must therefore be a real, complete file this run downloaded:
// never a partial download under any name, never a link into a shared
// directory, never anything that was not a regular file at the top level of the
// first attempt's distdir.
//
// HOSTILE CASES FIRST. Each rule below is exercised by the input that would make
// it fire wrongly in both directions before the benign input: a partial that a
// sloppy suffix check would rename to its final name, and a complete file whose
// name merely CONTAINS the partial suffix, which an over-eager check would drop.
//
// This file uses snapshotTree / assertTreeUnchanged / seedFile from
// quarantine_test.go and defines no TestMain (lock_test.go owns it).

package distfiles

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"testing"
)

// s081Partial is portage's in-progress download suffix (`_download_suffix` in
// portage/package/ebuild/fetch.py), spelled out here rather than read from the
// package so the test pins the value portage uses, not whatever the code says.
const s081Partial = ".__download__"

// s081Entries lists dir's entry names, sorted, for exact-set assertions.
func s081Entries(t *testing.T, dir string) []string {
	t.Helper()
	names := dirEntryNames(t, dir)
	sort.Strings(names)
	return names
}

// s081AssertRegularWith asserts path is a regular file (Lstat: a symlink is
// described, never followed) holding exactly want.
func s081AssertRegularWith(t *testing.T, path, want string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Errorf("%s: %v", path, err)
		return
	}
	if !info.Mode().IsRegular() {
		t.Errorf("%s has mode %v, want a regular file", path, info.Mode())
		return
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Errorf("reading %s: %v", path, err)
		return
	}
	if string(got) != want {
		t.Errorf("%s holds %q, want %q", path, got, want)
	}
}

func s081SameNames(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// R1.2 — a partial download never reaches the fixer, under ANY name.
//
// Both converse halves before the benign one:
//   - wrongly collapse: "x.tar.gz.__download__" must not arrive as "x.tar.gz"
//     (a suffix-stripping implementation hands the agent a truncated archive
//     under the name pkgdev will digest), nor under its own name;
//   - wrongly split: "x.__download__.tar.gz" is a COMPLETE file whose name only
//     contains the suffix — it must be carried;
//   - and when a complete "d.tar.gz" sits beside its own partial, the complete
//     bytes are the ones that arrive.
func TestS081CarryOverNeverCarriesAPartialDownload(t *testing.T) {
	from, to := t.TempDir(), t.TempDir()

	seedFile(t, from, "b-2.0.tar.gz"+s081Partial, "partial b", 0o644)
	seedFile(t, from, s081Partial, "a file named only by the suffix", 0o644)
	seedFile(t, from, "c"+s081Partial+".tar.gz", "complete c", 0o644)
	seedFile(t, from, "d.tar.gz", "complete d", 0o644)
	seedFile(t, from, "d.tar.gz"+s081Partial, "partial d", 0o644)

	moved, errs := CarryOver(from, to)

	if len(errs) != 0 {
		t.Errorf("CarryOver reported failures on a plain local move: %+v", errs)
	}
	want := []string{"c" + s081Partial + ".tar.gz", "d.tar.gz"}
	if got := s081Entries(t, to); !s081SameNames(got, want) {
		t.Fatalf("destination holds %v, want exactly %v — a partial download reached the fixer, "+
			"or a complete file whose name merely contains %q was dropped", got, want, s081Partial)
	}
	s081AssertRegularWith(t, filepath.Join(to, "d.tar.gz"), "complete d")
	s081AssertRegularWith(t, filepath.Join(to, "c"+s081Partial+".tar.gz"), "complete c")
	if moved != 2 {
		t.Errorf("CarryOver reported %d file(s) moved, want 2", moved)
	}
}

// R1.1 + R1.3 — only top-level REGULAR files move; nothing else is moved or
// recreated.
//
// The symlinks are the shape the first attempt really holds: the ordinary
// staged path seeds its private distdir with links into the distfiles cache.
// Re-creating one in the fixer's distdir would let the agent's Write overwrite
// the cache entry through it. A file inside a subdirectory must not be
// flattened into the destination either.
func TestS081CarryOverMovesOnlyTopLevelRegularFiles(t *testing.T) {
	root := t.TempDir()
	from := filepath.Join(root, "first")
	to := filepath.Join(root, "fix")
	cache := filepath.Join(root, "cache")
	for _, d := range []string{from, to, cache, filepath.Join(from, "sub")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	seedFile(t, cache, "cached-1.0.tar.gz", "cache bytes", 0o644)
	cacheBefore := snapshotTree(t, cache)

	// Hostile entries first.
	if err := os.Symlink(filepath.Join(cache, "cached-1.0.tar.gz"), filepath.Join(from, "cached-1.0.tar.gz")); err != nil {
		t.Fatalf("symlink into the cache: %v", err)
	}
	if err := os.Symlink(filepath.Join(root, "nowhere"), filepath.Join(from, "dangling.tar.gz")); err != nil {
		t.Fatalf("dangling symlink: %v", err)
	}
	seedFile(t, filepath.Join(from, "sub"), "inner-1.0.tar.gz", "nested bytes", 0o644)
	if err := syscall.Mkfifo(filepath.Join(from, "pipe.tar.gz"), 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	// The benign one.
	seedFile(t, from, "b-2.0.tar.gz", "downloaded by the first attempt", 0o644)

	moved, errs := CarryOver(from, to)

	if got, want := s081Entries(t, to), []string{"b-2.0.tar.gz"}; !s081SameNames(got, want) {
		t.Fatalf("destination holds %v, want exactly %v — a symlink, directory, fifo or nested file was carried", got, want)
	}
	s081AssertRegularWith(t, filepath.Join(to, "b-2.0.tar.gz"), "downloaded by the first attempt")
	if moved != 1 {
		t.Errorf("CarryOver reported %d file(s) moved, want 1", moved)
	}
	for _, e := range errs {
		if e.Name == "b-2.0.tar.gz" {
			t.Errorf("the one regular file was reported as a failure: %+v", e)
		}
	}
	// Moved, not copied: the first attempt's distdir no longer holds it.
	if _, err := os.Lstat(filepath.Join(from, "b-2.0.tar.gz")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("b-2.0.tar.gz is still in the first distdir (err=%v); R1.1 moves the file", err)
	}
	assertTreeUnchanged(t, cacheBefore, cache, "the distfiles cache a carried symlink pointed into")
}

// A name the destination already holds is never replaced — neither a regular
// file nor a symlink planted there — and nothing is written through a planted
// symlink to the file it points at.
func TestS081CarryOverNeverReplacesAnEntryAlreadyInTheDestination(t *testing.T) {
	root := t.TempDir()
	from := filepath.Join(root, "first")
	to := filepath.Join(root, "fix")
	outside := filepath.Join(root, "outside")
	for _, d := range []string{from, to, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	victim := seedFile(t, outside, "victim", "must never change", 0o644)
	outsideBefore := snapshotTree(t, outside)

	seedFile(t, to, "same.tar.gz", "already in the fix distdir", 0o644)
	seedFile(t, from, "same.tar.gz", "first attempt bytes", 0o644)

	if err := os.Symlink(victim, filepath.Join(to, "planted.tar.gz")); err != nil {
		t.Fatalf("planting a symlink: %v", err)
	}
	seedFile(t, from, "planted.tar.gz", "would overwrite the victim", 0o644)

	if err := os.Symlink(filepath.Join(outside, "created-through"), filepath.Join(to, "dangling.tar.gz")); err != nil {
		t.Fatalf("planting a dangling symlink: %v", err)
	}
	seedFile(t, from, "dangling.tar.gz", "would be created outside", 0o644)

	seedFile(t, from, "fresh.tar.gz", "carried", 0o644)

	moved, _ := CarryOver(from, to)

	s081AssertRegularWith(t, filepath.Join(to, "same.tar.gz"), "already in the fix distdir")
	for _, name := range []string{"planted.tar.gz", "dangling.tar.gz"} {
		info, err := os.Lstat(filepath.Join(to, name))
		if err != nil {
			t.Errorf("the entry planted at %s is gone: %v", name, err)
			continue
		}
		if info.Mode()&fs.ModeSymlink == 0 {
			t.Errorf("the symlink planted at %s was replaced by a %v", name, info.Mode())
		}
	}
	assertTreeUnchanged(t, outsideBefore, outside, "the directory a planted symlink points into")
	s081AssertRegularWith(t, filepath.Join(to, "fresh.tar.gz"), "carried")
	if moved != 1 {
		t.Errorf("CarryOver reported %d file(s) moved, want 1 (only fresh.tar.gz)", moved)
	}
}

// R1.5 — a move that fails is reported per file, with its name and a wrapped
// cause, and does not stop the others from being attempted. The file whose
// move failed is not lost.
//
// The destination is made read-only, so every rename into it fails with
// EACCES: one FileError per regular file proves each was attempted, where an
// implementation that stopped at the first failure would report one.
func TestS081CarryOverReportsEachFailedMoveAndContinues(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions, so no rename can be made to fail this way")
	}
	from, to := t.TempDir(), t.TempDir()
	names := []string{"a-1.tar.gz", "b-2.tar.xz", "c-3.zip"}
	for _, n := range names {
		seedFile(t, from, n, "bytes of "+n, 0o644)
	}
	if err := os.Chmod(to, 0o555); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(to, 0o755) })

	moved, errs := CarryOver(from, to)

	if moved != 0 {
		t.Errorf("CarryOver reported %d file(s) moved into a read-only directory", moved)
	}
	var reported []string
	for _, e := range errs {
		reported = append(reported, e.Name)
		if e.Err == nil {
			t.Errorf("FileError for %q carries no cause", e.Name)
		} else if !errors.Is(e.Err, fs.ErrPermission) {
			t.Errorf("FileError for %q does not wrap the operating system's cause: %v", e.Name, e.Err)
		}
	}
	sort.Strings(reported)
	if !s081SameNames(reported, names) {
		t.Errorf("failures reported for %v, want one per file %v — a failed move must not stop the rest", reported, names)
	}
	for _, n := range names {
		s081AssertRegularWith(t, filepath.Join(from, n), "bytes of "+n)
	}
}
