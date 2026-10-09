// Story 082 — sub-task 2.1. CopyFromSources supplies the LLM repair's distdir
// from the read-only distfile sources by COPY, never by link (R3.2-R3.7; moved here from story 081, where it was R2.1-R2.6).
//
// The fixer's distdir is writable by an agent. A symlink or a hard link into
// the cache there would let one Write rewrite the cache entry, so every file
// that arrives must be an independent regular file, written through a
// temporary name, and the sources must come out byte-identical.
//
// HOSTILE CASES FIRST, both directions: a symlink in a source that must not be
// recreated vs a symlink to a regular file whose bytes must still arrive; a
// non-regular source (a FIFO, a directory) that must be passed over without
// hanging vs the regular file in the next source; and the DERIVED value this
// mechanism invents — the temporary name — checked against expected names that
// look like plausible temporary names.
//
// Uses snapshotTree / assertTreeUnchanged / seedFile (quarantine_test.go) and
// s081Entries / s081AssertRegularWith / s081SameNames (carry_s081_test.go).

package distfiles

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

type s082CopyResult struct {
	copied int
	errs   []FileError
}

// s082CopyWithDeadline runs CopyFromSources and fails the test, instead of
// hanging the suite, when it does not return: opening a FIFO read-only blocks
// until a writer appears.
func s082CopyWithDeadline(t *testing.T, distdir string, sources, names []string) s082CopyResult {
	t.Helper()
	done := make(chan s082CopyResult, 1)
	go func() {
		n, errs := CopyFromSources(distdir, sources, names)
		done <- s082CopyResult{n, errs}
	}()
	select {
	case r := <-done:
		return r
	case <-time.After(20 * time.Second):
		t.Fatal("CopyFromSources did not return within 20s: it blocked on a source that is not a regular file")
		return s082CopyResult{}
	}
}

// s082UnblockFIFO opens the FIFO's write side non-blocking and closes it, which
// releases a reader blocked in open(2), so a failed test does not leave a
// goroutine stuck for the rest of the package run.
func s082UnblockFIFO(t *testing.T, path string) {
	t.Cleanup(func() {
		if f, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			_ = f.Close()
		}
	})
}

func s082Mkdirs(t *testing.T, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
}

// s082AssertNotLinkedTo asserts the copy shares no inode with its source: a
// hard link is a link too, and a Write through it would rewrite the cache.
func s082AssertNotLinkedTo(t *testing.T, copyPath, sourcePath string) {
	t.Helper()
	ci, err := os.Lstat(copyPath)
	if err != nil {
		t.Errorf("lstat %s: %v", copyPath, err)
		return
	}
	si, err := os.Stat(sourcePath)
	if err != nil {
		t.Errorf("stat %s: %v", sourcePath, err)
		return
	}
	if os.SameFile(ci, si) {
		t.Errorf("%s is the same inode as the source %s: a hard link, not a copy", copyPath, sourcePath)
	}
}

// R3.1 + R3.2 + R3.4 — copies, from the first source holding the name, into an
// independent regular file; the sources are read-only and come out unchanged.
//
// The sources are made read-only (dirs 0555, files 0444): a correct copy needs
// nothing more, and an implementation that moved out of the cache fails here.
func TestS082CopyFromSourcesCopiesFromTheFirstSourceAndNeverLinks(t *testing.T) {
	root := t.TempDir()
	cache, host, fix := filepath.Join(root, "cache"), filepath.Join(root, "host"), filepath.Join(root, "fix")
	s082Mkdirs(t, cache, host, fix)
	seedFile(t, cache, "a-2.0.tar.gz", "cache a", 0o444)
	seedFile(t, host, "a-2.0.tar.gz", "host a", 0o444)
	seedFile(t, host, "b-2.0.tar.xz", "host b", 0o444)
	for _, d := range []string{cache, host} {
		if err := os.Chmod(d, 0o555); err != nil {
			t.Fatalf("chmod %s: %v", d, err)
		}
		t.Cleanup(func() { _ = os.Chmod(d, 0o755) })
	}
	cacheBefore, hostBefore := snapshotTree(t, cache), snapshotTree(t, host)

	r := s082CopyWithDeadline(t, fix, []string{cache, host}, []string{"a-2.0.tar.gz", "b-2.0.tar.xz", "absent-2.0.tar.gz"})

	if len(r.errs) != 0 {
		t.Errorf("unexpected failures (a name in no source is not a failure): %+v", r.errs)
	}
	if got, want := s081Entries(t, fix), []string{"a-2.0.tar.gz", "b-2.0.tar.xz"}; !s081SameNames(got, want) {
		t.Fatalf("fix distdir holds %v, want exactly %v", got, want)
	}
	s081AssertRegularWith(t, filepath.Join(fix, "a-2.0.tar.gz"), "cache a") // the cache comes first
	s081AssertRegularWith(t, filepath.Join(fix, "b-2.0.tar.xz"), "host b")
	s082AssertNotLinkedTo(t, filepath.Join(fix, "a-2.0.tar.gz"), filepath.Join(cache, "a-2.0.tar.gz"))
	s082AssertNotLinkedTo(t, filepath.Join(fix, "b-2.0.tar.xz"), filepath.Join(host, "b-2.0.tar.xz"))
	if r.copied != 2 {
		t.Errorf("CopyFromSources reported %d copied, want 2", r.copied)
	}
	assertTreeUnchanged(t, cacheBefore, cache, "the distfiles cache")
	assertTreeUnchanged(t, hostBefore, host, "the host DISTDIR")
}

// R3.1 + R3.4 — "found as a regular file", both halves:
//   - wrongly kept: a symlink to a directory, a directory and a FIFO under an
//     expected name are not regular files; each is passed over (the FIFO without
//     blocking) and the next source's regular file is what arrives;
//   - wrongly dropped: a cache symlink to a regular file still yields that
//     file's bytes, as a regular file and never as a link.
func TestS082CopyFromSourcesTakesOnlyRegularFilesFromASource(t *testing.T) {
	root := t.TempDir()
	cache, host, fix, elsewhere := filepath.Join(root, "cache"), filepath.Join(root, "host"),
		filepath.Join(root, "fix"), filepath.Join(root, "elsewhere")
	s082Mkdirs(t, cache, host, fix, elsewhere, filepath.Join(cache, "dir-2.0.tar.gz"))

	if err := os.Symlink(elsewhere, filepath.Join(cache, "todir-2.0.tar.gz")); err != nil {
		t.Fatalf("symlink to a directory: %v", err)
	}
	fifo := filepath.Join(cache, "fifo-2.0.tar.gz")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	s082UnblockFIFO(t, fifo)
	for _, n := range []string{"todir-2.0.tar.gz", "dir-2.0.tar.gz", "fifo-2.0.tar.gz"} {
		seedFile(t, host, n, "host "+n, 0o644)
	}
	target := seedFile(t, elsewhere, "real.bin", "bytes behind the link", 0o644)
	if err := os.Symlink(target, filepath.Join(cache, "linked-2.0.tar.gz")); err != nil {
		t.Fatalf("symlink to a regular file: %v", err)
	}
	cacheBefore, elsewhereBefore := snapshotTree(t, cache), snapshotTree(t, elsewhere)

	names := []string{"todir-2.0.tar.gz", "dir-2.0.tar.gz", "fifo-2.0.tar.gz", "linked-2.0.tar.gz"}
	r := s082CopyWithDeadline(t, fix, []string{cache, host}, names)

	for _, n := range []string{"todir-2.0.tar.gz", "dir-2.0.tar.gz", "fifo-2.0.tar.gz"} {
		s081AssertRegularWith(t, filepath.Join(fix, n), "host "+n)
	}
	s081AssertRegularWith(t, filepath.Join(fix, "linked-2.0.tar.gz"), "bytes behind the link")
	s082AssertNotLinkedTo(t, filepath.Join(fix, "linked-2.0.tar.gz"), target)
	if r.copied != 4 {
		t.Errorf("CopyFromSources reported %d copied, want 4 (errors: %+v)", r.copied, r.errs)
	}
	assertTreeUnchanged(t, cacheBefore, cache, "the distfiles cache")
	assertTreeUnchanged(t, elsewhereBefore, elsewhere, "the directory a cache symlink points into")
}

// R3.7 (never replace a name already present) + the planted-entry rule — a name the
// fix distdir already holds is never replaced, and nothing is written through
// an entry planted at the final name, dangling symlink included (an
// O_CREAT through it would create the file outside the distdir).
func TestS082CopyFromSourcesNeverWritesThroughAPlantedEntry(t *testing.T) {
	root := t.TempDir()
	cache, fix, outside := filepath.Join(root, "cache"), filepath.Join(root, "fix"), filepath.Join(root, "outside")
	s082Mkdirs(t, cache, fix, outside)
	victim := seedFile(t, outside, "victim", "must never change", 0o644)
	if err := os.Symlink(victim, filepath.Join(fix, "planted-2.0.tar.gz")); err != nil {
		t.Fatalf("planting: %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, "created-through"), filepath.Join(fix, "dangling-2.0.tar.gz")); err != nil {
		t.Fatalf("planting: %v", err)
	}
	seedFile(t, fix, "present-2.0.tar.gz", "already here", 0o644)
	for _, n := range []string{"planted-2.0.tar.gz", "dangling-2.0.tar.gz", "present-2.0.tar.gz", "fresh-2.0.tar.gz"} {
		seedFile(t, cache, n, "cache "+n, 0o644)
	}
	outsideBefore := snapshotTree(t, outside)

	r := s082CopyWithDeadline(t, fix, []string{cache},
		[]string{"planted-2.0.tar.gz", "dangling-2.0.tar.gz", "present-2.0.tar.gz", "fresh-2.0.tar.gz"})

	assertTreeUnchanged(t, outsideBefore, outside, "the directory planted symlinks point into")
	for _, n := range []string{"planted-2.0.tar.gz", "dangling-2.0.tar.gz"} {
		info, err := os.Lstat(filepath.Join(fix, n))
		if err != nil {
			t.Errorf("the entry planted at %s is gone: %v", n, err)
			continue
		}
		if info.Mode()&fs.ModeSymlink == 0 {
			t.Errorf("the symlink planted at %s was replaced by a %v", n, info.Mode())
		}
	}
	s081AssertRegularWith(t, filepath.Join(fix, "present-2.0.tar.gz"), "already here")
	s081AssertRegularWith(t, filepath.Join(fix, "fresh-2.0.tar.gz"), "cache fresh-2.0.tar.gz")
	if r.copied != 1 {
		t.Errorf("CopyFromSources reported %d copied, want 1 (only fresh-2.0.tar.gz)", r.copied)
	}
}

// R3.6 — a name that is not a single path element is skipped without reading
// or writing anything for it.
//
// Bait makes both halves observable: a traversal SOURCE resolves to a mode-0000
// file, so an attempt to read it surfaces as a permission failure; a traversal
// DESTINATION resolves to a writable place where a file would appear.
func TestS082CopyFromSourcesSkipsNamesThatAreNotOnePathElement(t *testing.T) {
	root := t.TempDir()
	src, dst := filepath.Join(root, "src"), filepath.Join(root, "dst")
	cache, fix := filepath.Join(src, "cache"), filepath.Join(dst, "fix")
	s082Mkdirs(t, filepath.Join(cache, "a"), filepath.Join(fix, "a"))
	baits := []string{
		seedFile(t, src, "x", "bait reached by ../x", 0o644),
		seedFile(t, filepath.Join(cache, "a"), "b", "bait reached by a/b", 0o644),
	}
	seedFile(t, cache, "ok-2.0.tar.gz", "ok", 0o644)
	// Snapshotted readable, then locked: snapshotTree reads file content.
	srcBefore, dstBefore := snapshotTree(t, src), snapshotTree(t, dst)
	unlock := func() {
		for _, b := range baits {
			_ = os.Chmod(b, 0o644)
		}
	}
	t.Cleanup(unlock)
	for _, b := range baits {
		if err := os.Chmod(b, 0o000); err != nil {
			t.Fatalf("chmod %s: %v", b, err)
		}
	}

	hostile := []string{"../x", "a/b", ".", "..", "", "/x", "x/"}
	r := s082CopyWithDeadline(t, fix, []string{cache}, append(append([]string{}, hostile...), "ok-2.0.tar.gz"))

	unlock()
	assertTreeUnchanged(t, srcBefore, src, "the source tree")
	after := snapshotTree(t, dst)
	delete(after, filepath.Join("fix", "ok-2.0.tar.gz"))
	// withoutDirSizes (cleanup_test.go): adding ok-2.0.tar.gz legitimately changes fix/'s size.
	assertSameTree(t, withoutDirSizes(dstBefore), withoutDirSizes(after), dst, "the destination side of a traversal name")
	s081AssertRegularWith(t, filepath.Join(fix, "ok-2.0.tar.gz"), "ok")
	if r.copied != 1 {
		t.Errorf("CopyFromSources reported %d copied, want 1 (only ok-2.0.tar.gz)", r.copied)
	}
	if os.Geteuid() != 0 {
		for _, e := range r.errs {
			if errors.Is(e.Err, fs.ErrPermission) {
				t.Errorf("a refused name was READ (permission failure on the bait): %+v", e)
			}
		}
	}
}

// R3.3 + R3.5 — a copy that fails after it started leaves no file under the
// final name, is reported with its name, source directory and cause, and the
// other names are still copied.
//
// /proc/self/mem opens and stats as a regular file but fails with EIO on the
// first read: a symlink to it in the cache fails the copy MID-WAY, after a
// destination already exists.
func TestS082CopyFromSourcesLeavesNothingUnderTheFinalNameWhenACopyFails(t *testing.T) {
	if _, err := os.Stat("/proc/self/mem"); err != nil {
		t.Skipf("no /proc/self/mem to fail a read with: %v", err)
	}
	root := t.TempDir()
	cache, fix := filepath.Join(root, "cache"), filepath.Join(root, "fix")
	s082Mkdirs(t, cache, fix)
	if err := os.Symlink("/proc/self/mem", filepath.Join(cache, "bad-2.0.tar.gz")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	seedFile(t, cache, "good-2.0.tar.gz", "good", 0o644)

	r := s082CopyWithDeadline(t, fix, []string{cache}, []string{"bad-2.0.tar.gz", "good-2.0.tar.gz"})

	if _, err := os.Lstat(filepath.Join(fix, "bad-2.0.tar.gz")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a failed copy left an entry under its final name (lstat err=%v)", err)
	}
	if got, want := s081Entries(t, fix), []string{"good-2.0.tar.gz"}; !s081SameNames(got, want) {
		t.Errorf("fix distdir holds %v, want exactly %v — a failed copy's temporary file was left behind", got, want)
	}
	s081AssertRegularWith(t, filepath.Join(fix, "good-2.0.tar.gz"), "good")
	if r.copied != 1 {
		t.Errorf("CopyFromSources reported %d copied, want 1", r.copied)
	}
	if len(r.errs) != 1 {
		t.Fatalf("got %d failure(s), want exactly 1 for bad-2.0.tar.gz: %+v", len(r.errs), r.errs)
	}
	e := r.errs[0]
	if e.Name != "bad-2.0.tar.gz" || e.Source != cache || e.Err == nil {
		t.Errorf("FileError = %+v, want Name %q, Source %q (the source DIRECTORY) and a cause", e, "bad-2.0.tar.gz", cache)
	}
	if e.Err != nil && !errors.Is(e.Err, syscall.EIO) {
		t.Errorf("the cause does not wrap the read error: %v", e.Err)
	}
}

// The derived value — the temporary name — never collides with an expected
// name. Names shaped like plausible temporary names of "a-2.0.tar.gz" are
// expected too, listed BEFORE it, each with its own bytes: a deterministic
// temporary name would either refuse a-2.0.tar.gz (O_EXCL on an existing name)
// or clobber its lookalike.
func TestS082CopyFromSourcesTemporaryNamesNeverCollideWithExpectedNames(t *testing.T) {
	root := t.TempDir()
	cache, fix := filepath.Join(root, "cache"), filepath.Join(root, "fix")
	s082Mkdirs(t, cache, fix)
	names := []string{".a-2.0.tar.gz.tmp", "a-2.0.tar.gz.tmp", "a-2.0.tar.gz.part", ".a-2.0.tar.gz.bentoo-copy", "a-2.0.tar.gz~", "a-2.0.tar.gz"}
	for _, n := range names {
		seedFile(t, cache, n, "own bytes of "+n, 0o644)
	}

	r := s082CopyWithDeadline(t, fix, []string{cache}, names)

	for _, n := range names {
		s081AssertRegularWith(t, filepath.Join(fix, n), "own bytes of "+n)
	}
	if got := s081Entries(t, fix); len(got) != len(names) {
		t.Errorf("fix distdir holds %v, want exactly the %d expected names", got, len(names))
	}
	if r.copied != len(names) {
		t.Errorf("CopyFromSources reported %d copied, want %d (errors: %+v)", r.copied, len(names), r.errs)
	}
	for _, e := range r.errs {
		if strings.HasPrefix(e.Name, "a-2.0") || strings.HasPrefix(e.Name, ".a-2.0") {
			t.Errorf("an expected name failed to copy: %+v", e)
		}
	}
}
