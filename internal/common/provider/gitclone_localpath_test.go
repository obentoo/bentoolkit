package provider

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// newClonedTreeProvider builds a GitCloneProvider whose LocalPath already looks
// like a freshly cloned repository: a .git directory plus a FETCH_HEAD with a
// recent mtime. This makes repoExists() return true and needsUpdate() return
// false, so ensureRepo() is a no-op and no real "git clone" is ever invoked.
func newClonedTreeProvider(t *testing.T) *GitCloneProvider {
	t.Helper()

	localPath := t.TempDir()

	gitDir := filepath.Join(localPath, ".git")
	if err := os.MkdirAll(gitDir, 0o750); err != nil {
		t.Fatalf("failed to create .git dir: %v", err)
	}

	fetchHead := filepath.Join(gitDir, "FETCH_HEAD")
	if err := os.WriteFile(fetchHead, []byte("# mock FETCH_HEAD"), 0o640); err != nil {
		t.Fatalf("failed to write FETCH_HEAD: %v", err)
	}

	return &GitCloneProvider{
		LocalPath: localPath,
		RepoName:  "test",
		// Large interval so time.Since(FETCH_HEAD mtime) <= UpdateInterval and
		// needsUpdate() stays false.
		UpdateInterval: 24 * time.Hour,
	}
}

func TestGitCloneProvider_LocalPackagePath(t *testing.T) {
	prov := newClonedTreeProvider(t)

	pkgDir := filepath.Join(prov.LocalPath, "cat", "pkg")
	if err := os.MkdirAll(pkgDir, 0o750); err != nil {
		t.Fatalf("failed to create package dir: %v", err)
	}
	ebuild := filepath.Join(pkgDir, "pkg-1.0.ebuild")
	if err := os.WriteFile(ebuild, []byte("# mock ebuild"), 0o640); err != nil {
		t.Fatalf("failed to write ebuild: %v", err)
	}

	got, err := prov.LocalPackagePath("cat", "pkg")
	if err != nil {
		t.Fatalf("LocalPackagePath returned error: %v", err)
	}
	if got != pkgDir {
		t.Errorf("LocalPackagePath = %q, want %q", got, pkgDir)
	}
}

func TestGitCloneProvider_LocalPackagePath_NotFound(t *testing.T) {
	prov := newClonedTreeProvider(t)

	got, err := prov.LocalPackagePath("cat", "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("LocalPackagePath error = %v, want ErrNotFound", err)
	}
	if got != "" {
		t.Errorf("LocalPackagePath path = %q, want empty string", got)
	}
}
