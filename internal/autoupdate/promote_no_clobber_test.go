package autoupdate

// Authored for story 056, sub-task 4.3 (S056-R3.5, S056-R3.6). A dangling
// symlink at the published ebuild path passes refuseExistingEbuild (it refuses
// only a regular file) and is therefore the destination "appearing between the
// check and the publish", staged without a hook. A rename replaces it silently;
// a link-based publish refuses it.

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestPromoteRefusesEbuildCreatedAfterCheck(t *testing.T) {
	root := t.TempDir()
	overlay := filepath.Join(root, "overlay")
	pkg := "app-misc/hello"
	pkgDir := filepath.Join(overlay, "app-misc", "hello")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "hello-1.0.0.ebuild"), []byte("EAPI=8\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := NewApplier(overlay, filepath.Join(root, "config"))
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}
	cand, err := stagedCandidate(filepath.Join(root, "staging"), pkg, "1.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(cand.ebuildPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cand.ebuildPath, []byte("EAPI=8\n# validated\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(pkgDir, "hello-1.1.0.ebuild")
	outside := filepath.Join(root, "elsewhere.ebuild")
	if err := os.Symlink(outside, dst); err != nil {
		t.Fatal(err)
	}

	undo, err := a.promote(cand, pkg, "1.1.0")
	if !errors.Is(err, ErrEbuildExists) {
		if undo != nil {
			undo(errors.New("test cleanup"))
		}
		t.Fatalf("promote over an entry that appeared after refuseExistingEbuild returned %v, want ErrEbuildExists", err)
	}
	if target, lerr := os.Readlink(dst); lerr != nil || target != outside {
		t.Errorf("the destination entry was replaced by the publish: readlink = %q, %v", target, lerr)
	}
	if _, serr := os.Lstat(outside); !errors.Is(serr, fs.ErrNotExist) {
		t.Errorf("promote wrote through the destination symlink and created %s", outside)
	}
	s056AssertEntries(t, pkgDir, "hello-1.0.0.ebuild", "hello-1.1.0.ebuild")
}
