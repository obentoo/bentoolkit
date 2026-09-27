package autoupdate

// Authored for story 056, sub-task 4.2 (S056-R3.2, S056-R3.3, S056-R3.4).
//
// The "created after the check" case is staged without a hook: a DANGLING
// symlink at the destination is invisible to an os.Stat existence check (Stat
// follows it and answers ENOENT), so the check passes and the entry is there at
// publish time — exactly the window a concurrent creator opens. Only a publish
// that refuses any existing entry (link, not create) survives it.

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const s056SourceEbuild = "EAPI=8\nDESCRIPTION=\"hello\"\nSLOT=\"0\"\nKEYWORDS=\"~amd64\"\n"

func s056CopyFixture(t *testing.T) (a *Applier, overlay, pkgDir, src, dst string) {
	t.Helper()
	overlay = filepath.Join(t.TempDir(), "overlay")
	pkgDir = filepath.Join(overlay, "app-misc", "hello")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	a, err := NewApplier(overlay, t.TempDir())
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}
	return a, overlay, pkgDir, filepath.Join(pkgDir, "hello-1.0.0.ebuild"), filepath.Join(pkgDir, "hello-1.1.0.ebuild")
}

// TestCopyEbuildKeepsSourceMode: R3.2 — the new ebuild takes the SOURCE
// ebuild's mode, not the umask's: 0640 under umask 022 (which would give 0644)
// and 0644 under umask 077 (which would give 0600).
func TestCopyEbuildKeepsSourceMode(t *testing.T) {
	for _, tc := range []struct {
		umask int
		mode  os.FileMode
	}{{0o022, 0o640}, {0o077, 0o644}} {
		t.Run(tc.mode.String(), func(t *testing.T) {
			s056Umask(t, tc.umask)
			a, _, pkgDir, src, dst := s056CopyFixture(t)
			if err := os.WriteFile(src, []byte(s056SourceEbuild), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(src, tc.mode); err != nil {
				t.Fatal(err)
			}
			if err := a.copyEbuild("app-misc/hello", "1.0.0", "1.1.0"); err != nil {
				t.Fatalf("copyEbuild: %v", err)
			}
			got, _ := os.ReadFile(dst)
			if string(got) != s056SourceEbuild {
				t.Errorf("destination holds %q, want the source bytes", got)
			}
			info, err := os.Lstat(dst)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != tc.mode || !info.Mode().IsRegular() {
				t.Errorf("destination is %v, want a regular file at the source's %#o", info.Mode(), tc.mode)
			}
			s056AssertEntries(t, pkgDir, "hello-1.0.0.ebuild", "hello-1.1.0.ebuild")
		})
	}
}

// TestCopyEbuildRefusesDestinationCreatedAfterCheck: R3.3.
func TestCopyEbuildRefusesDestinationCreatedAfterCheck(t *testing.T) {
	a, _, pkgDir, src, dst := s056CopyFixture(t)
	if err := os.WriteFile(src, []byte(s056SourceEbuild), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "elsewhere.ebuild")
	if err := os.Symlink(outside, dst); err != nil {
		t.Fatal(err)
	}

	err := a.copyEbuild("app-misc/hello", "1.0.0", "1.1.0")
	if !errors.Is(err, ErrEbuildExists) {
		t.Fatalf("copyEbuild over an entry the existence check cannot see returned %v, want ErrEbuildExists", err)
	}
	if !strings.Contains(err.Error(), "refusing to overwrite") || !strings.Contains(err.Error(), dst) {
		t.Errorf("error %q must say it is refusing to overwrite and name %s", err, dst)
	}
	if target, lerr := os.Readlink(dst); lerr != nil || target != outside {
		t.Errorf("the destination entry was replaced: readlink = %q, %v", target, lerr)
	}
	if _, serr := os.Lstat(outside); !errors.Is(serr, fs.ErrNotExist) {
		t.Errorf("copyEbuild wrote through the destination symlink and created %s", outside)
	}
	s056AssertEntries(t, pkgDir, "hello-1.0.0.ebuild", "hello-1.1.0.ebuild")
}

// TestCopyEbuildFailureLeavesNoPartialFile: R3.4 — a source that cannot be
// read, or a directory that cannot be written, leaves no destination and no
// temporary file behind.
func TestCopyEbuildFailureLeavesNoPartialFile(t *testing.T) {
	t.Run("source cannot be read", func(t *testing.T) {
		a, _, pkgDir, src, dst := s056CopyFixture(t)
		// A directory under the source's name: it exists, so the not-found
		// check passes, and reading it as a file fails.
		if err := os.Mkdir(src, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := a.copyEbuild("app-misc/hello", "1.0.0", "1.1.0"); err == nil {
			t.Fatal("copyEbuild succeeded reading a directory as the source ebuild")
		}
		if _, err := os.Lstat(dst); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("a failed copy left an entry at %s", dst)
		}
		s056AssertEntries(t, pkgDir, "hello-1.0.0.ebuild")
	})

	t.Run("package directory not writable", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores directory permissions")
		}
		a, _, pkgDir, src, dst := s056CopyFixture(t)
		if err := os.WriteFile(src, []byte(s056SourceEbuild), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(pkgDir, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(pkgDir, 0o755) })
		if err := a.copyEbuild("app-misc/hello", "1.0.0", "1.1.0"); err == nil {
			t.Fatal("copyEbuild succeeded into a read-only package directory")
		}
		if _, err := os.Lstat(dst); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("a failed copy left an entry at %s", dst)
		}
		s056AssertEntries(t, pkgDir, "hello-1.0.0.ebuild")
	})
}
