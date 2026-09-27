package autoupdate

// Authored for story 056, sub-task 4.1 (S056-R3.1). A hard link to the ebuild,
// kept outside the package directory, is the observable difference between an
// in-place rewrite (the link sees the new text, and a crash mid-write would
// have left it short) and a replace (the link keeps the old text).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func s056EbuildWithProbe(t *testing.T, content string, mode os.FileMode) (ebuild, probe, pkgDir string) {
	t.Helper()
	root := t.TempDir()
	pkgDir = filepath.Join(root, "overlay", "app-misc", "hello")
	probeDir := filepath.Join(root, "probe")
	for _, d := range []string{pkgDir, probeDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ebuild = filepath.Join(pkgDir, "hello-1.0.ebuild")
	if err := os.WriteFile(ebuild, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ebuild, mode); err != nil {
		t.Fatal(err)
	}
	probe = filepath.Join(probeDir, "old-inode")
	if err := os.Link(ebuild, probe); err != nil {
		t.Skipf("hard links unsupported here: %v", err)
	}
	return ebuild, probe, pkgDir
}

func s056AssertReplaced(t *testing.T, ebuild, probe, pkgDir, old, wantSubstr string, mode os.FileMode) {
	t.Helper()
	if got, _ := os.ReadFile(probe); string(got) != old {
		t.Errorf("a hard link to the old ebuild now reads %q: the ebuild was rewritten in place (O_TRUNC), not replaced", got)
	}
	got, _ := os.ReadFile(ebuild)
	if !strings.Contains(string(got), wantSubstr) {
		t.Errorf("ebuild does not contain %q after the substitution:\n%s", wantSubstr, got)
	}
	info, err := os.Lstat(ebuild)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != mode || !info.Mode().IsRegular() {
		t.Errorf("ebuild is %v after the substitution, want a regular file at %#o", info.Mode(), mode)
	}
	s056AssertEntries(t, pkgDir, "hello-1.0.ebuild")
}

func TestSubstituteCommitHash_KeepsModeAndWritesAtomically(t *testing.T) {
	oldHash := strings.Repeat("a", 40)
	newHash := strings.Repeat("b", 40)
	for _, mode := range []os.FileMode{0o644, 0o640} {
		t.Run(mode.String(), func(t *testing.T) {
			s056Umask(t, 0o077)
			old := "EAPI=8\nEGIT_COMMIT=\"" + oldHash + "\"\nSRC_URI=\"https://example.invalid/${EGIT_COMMIT}.tar.gz\"\n"
			ebuild, probe, pkgDir := s056EbuildWithProbe(t, old, mode)
			if err := substituteCommitHash(ebuild, newHash); err != nil {
				t.Fatalf("substituteCommitHash: %v", err)
			}
			s056AssertReplaced(t, ebuild, probe, pkgDir, old, `EGIT_COMMIT="`+newHash+`"`, mode)
		})
	}
}

func TestSubstituteAuxVar_KeepsModeAndWritesAtomically(t *testing.T) {
	for _, mode := range []os.FileMode{0o644, 0o640} {
		t.Run(mode.String(), func(t *testing.T) {
			s056Umask(t, 0o077)
			old := "EAPI=8\nMY_BUILD=\"esr-bb23\"\nSRC_URI=\"https://example.invalid/${PV}${MY_BUILD}.tar.bz2\"\n"
			ebuild, probe, pkgDir := s056EbuildWithProbe(t, old, mode)
			if err := substituteAuxVar(ebuild, "MY_BUILD", "esr-bb24"); err != nil {
				t.Fatalf("substituteAuxVar: %v", err)
			}
			s056AssertReplaced(t, ebuild, probe, pkgDir, old, `MY_BUILD="esr-bb24"`, mode)
		})
	}
}
