package fileutil

// Authored for story 056, sub-task 8.2 (S056-R1.8, with R1.6 and R1.7 as the
// surrounding contract).
//
// R1.8: when a step AFTER the link fails, PublishNewFile removes the entry it
// published — but only while path still names the file it linked — and
// returns an error that wraps the cause, names path, and is NOT fs.ErrExist
// (fs.ErrExist means "something else was already there", which callers treat
// as "nothing was published by me").
//
// The failure is injected through the package's unexported directory-sync
// seam, the package variable syncDirFunc. Tests in this
// package never run in parallel, which is what makes swapping it safe.
//
// Hostile halves first. The rule is an identity rule ("path still names the
// linked file"), so it can fire wrongly in two directions:
//   - wrongly COLLAPSE: a different entry that looks like the published file
//     (same bytes, same mode; or a symlink resolving to it) is treated as the
//     linked file and removed;
//   - wrongly SPLIT: the linked file itself, whose mode and times changed
//     after the link, is treated as someone else's and left behind.
// Only then the benign case: nothing touched path, and the rollback removes it.

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// errInjectedDirSync is the cause every fake directory sync returns. Its text
// deliberately names no path, so "the error names path" is proven by the
// wrapping, never by the cause.
var errInjectedDirSync = errors.New("injected directory fsync failure")

// swapSyncDirFunc replaces the directory-sync seam for one test and restores it.
func swapSyncDirFunc(t *testing.T, fake func(dir string) error) {
	t.Helper()
	orig := syncDirFunc
	t.Cleanup(func() { syncDirFunc = orig })
	syncDirFunc = fake
}

// assertPostLinkError checks the R1.8 error shape: it wraps the injected
// cause, names path, and does not claim fs.ErrExist.
func assertPostLinkError(t *testing.T, err error, path string, fakeCalls int) {
	t.Helper()
	if fakeCalls == 0 {
		t.Fatalf("PublishNewFile never called syncDirFunc: the directory-sync failure was not injected (err = %v)", err)
	}
	if err == nil {
		t.Fatalf("PublishNewFile returned nil although the directory sync after the link failed")
	}
	if !errors.Is(err, errInjectedDirSync) {
		t.Errorf("error %q does not wrap the injected cause %q", err, errInjectedDirSync)
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not name %s", err, path)
	}
	if errors.Is(err, fs.ErrExist) {
		t.Errorf("error %q satisfies errors.Is(err, fs.ErrExist): callers would read it as 'an entry was already there'", err)
	}
}

// TestPublishNewFile_RollbackLeavesAnEntryThatReplacedPath: R1.8, the wrongly
// COLLAPSE half. Between the link and the rollback something else took path.
// The rollback must leave it untouched — even when it is byte- and
// mode-identical to what was published, and even when it is a symlink that
// resolves to the published file.
func TestPublishNewFile_RollbackLeavesAnEntryThatReplacedPath(t *testing.T) {
	const base = "hello-1.1.ebuild"
	data := []byte("EAPI=8\n")
	const mode os.FileMode = 0o644

	cases := []struct {
		name string
		// replace swaps path for another entry while the directory sync runs,
		// and returns what the rollback must leave in place.
		replace func(t *testing.T, dir, path string) (want os.FileInfo)
		// check verifies the replacement afterwards.
		check func(t *testing.T, dir, path string, want os.FileInfo)
		// entries is the directory's exact content afterwards: no temporary.
		entries []string
	}{
		{
			name: "a different file with the published bytes and mode",
			replace: func(t *testing.T, dir, path string) os.FileInfo {
				other := filepath.Join(dir, "replacement")
				if err := os.WriteFile(other, data, mode); err != nil {
					t.Errorf("planting the replacement: %v", err)
					return nil
				}
				if err := os.Chmod(other, mode); err != nil {
					t.Errorf("setting the replacement's mode: %v", err)
					return nil
				}
				if err := os.Rename(other, path); err != nil {
					t.Errorf("renaming the replacement over %s: %v", path, err)
					return nil
				}
				info, err := os.Lstat(path)
				if err != nil {
					t.Errorf("lstat of the replacement: %v", err)
					return nil
				}
				return info
			},
			check: func(t *testing.T, _, path string, want os.FileInfo) {
				info, err := os.Lstat(path)
				if err != nil {
					t.Fatalf("the replacement at %s was removed by the rollback: %v", path, err)
				}
				if !os.SameFile(info, want) {
					t.Errorf("%s is no longer the replacement that took it", path)
				}
				if got, _ := os.ReadFile(path); !bytes.Equal(got, data) {
					t.Errorf("the replacement now holds %q, want %q", got, data)
				}
			},
			entries: []string{base},
		},
		{
			name: "a symlink to the published file",
			replace: func(t *testing.T, dir, path string) os.FileInfo {
				aside := filepath.Join(dir, "moved-aside")
				if err := os.Rename(path, aside); err != nil {
					t.Errorf("moving the published file aside: %v", err)
					return nil
				}
				if err := os.Symlink("moved-aside", path); err != nil {
					t.Errorf("planting a symlink at %s: %v", path, err)
					return nil
				}
				info, err := os.Lstat(path)
				if err != nil {
					t.Errorf("lstat of the symlink: %v", err)
					return nil
				}
				return info
			},
			check: func(t *testing.T, dir, path string, want os.FileInfo) {
				info, err := os.Lstat(path)
				if err != nil {
					t.Fatalf("the symlink at %s was removed by the rollback: %v", path, err)
				}
				if !os.SameFile(info, want) || info.Mode()&os.ModeSymlink == 0 {
					t.Errorf("%s is no longer the symlink that took it (%v)", path, info.Mode())
				}
				if target, err := os.Readlink(path); err != nil || target != "moved-aside" {
					t.Errorf("the symlink now points at %q (%v), want %q", target, err, "moved-aside")
				}
				if got, err := os.ReadFile(filepath.Join(dir, "moved-aside")); err != nil || !bytes.Equal(got, data) {
					t.Errorf("the file the symlink resolves to holds %q (%v), want %q", got, err, data)
				}
			},
			entries: []string{base, "moved-aside"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, base)

			calls := 0
			var want os.FileInfo
			swapSyncDirFunc(t, func(string) error {
				calls++
				if calls == 1 {
					if _, err := os.Lstat(path); err != nil {
						t.Errorf("the directory sync ran before %s was published: %v", path, err)
					} else {
						want = tc.replace(t, dir, path)
					}
				}
				return errInjectedDirSync
			})

			err := PublishNewFile(path, data, mode)
			assertPostLinkError(t, err, path, calls)
			if want == nil {
				t.Fatalf("the replacement was never planted")
			}
			tc.check(t, dir, path, want)
			assertEntries(t, dir, tc.entries...)
		})
	}
}

// TestPublishNewFile_DirectorySyncFailureRemovesThePublishedFile: R1.8, the
// wrongly SPLIT half first, then the benign case. The file at path is still
// the one the call linked, so an error that is not fs.ErrExist must mean
// nothing was published: no entry at path, and no ".<base>.bentoo-*"
// temporary left beside it.
func TestPublishNewFile_DirectorySyncFailureRemovesThePublishedFile(t *testing.T) {
	const base = "hello-1.1.ebuild"
	data := []byte("EAPI=8\n")
	const mode os.FileMode = 0o644

	cases := []struct {
		name string
		// touch alters the linked file in place while the directory sync runs,
		// without replacing it.
		touch func(t *testing.T, path string)
	}{
		{
			name: "the linked file's mode and times changed after the link",
			touch: func(t *testing.T, path string) {
				if err := os.Chmod(path, 0o600); err != nil {
					t.Errorf("chmod of the published file: %v", err)
				}
				old := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
				if err := os.Chtimes(path, old, old); err != nil {
					t.Errorf("chtimes of the published file: %v", err)
				}
			},
		},
		{
			name:  "nothing touched the linked file",
			touch: func(*testing.T, string) {},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, base)

			calls := 0
			swapSyncDirFunc(t, func(string) error {
				calls++
				if calls == 1 {
					if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() {
						t.Errorf("the directory sync ran before %s was published as a regular file (%v)", path, err)
					} else {
						tc.touch(t, path)
					}
				}
				return errInjectedDirSync
			})

			err := PublishNewFile(path, data, mode)
			assertPostLinkError(t, err, path, calls)
			if _, lerr := os.Lstat(path); !errors.Is(lerr, fs.ErrNotExist) {
				t.Errorf("the file published at %s was left behind after a failed publish (lstat: %v)", path, lerr)
			}
			assertEntries(t, dir) // exactly empty: no published file, no .<base>.bentoo-* temporary
		})
	}
}
