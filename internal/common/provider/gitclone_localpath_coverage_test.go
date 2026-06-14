package provider

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// newFakeClonedRepo builds a GitCloneProvider whose LocalPath already looks like
// a freshly cloned repository: a .git directory and a FETCH_HEAD file with a
// recent mtime. With UpdateInterval set to a large value, repoExists() returns
// true and needsUpdate() returns false, so ensureRepo() is a no-op and no real
// "git clone"/"git pull" is ever invoked. This keeps the tests binary-free.
//
// Distinct from newClonedTreeProvider in gitclone_localpath_test.go: this helper
// also populates RepoURL/Branch/RepoName so callers can assert the no-op
// preconditions on a fully-configured provider.
func newFakeClonedRepo(t *testing.T) *GitCloneProvider {
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
		RepoURL:        "https://github.com/obentoo/example.git",
		LocalPath:      localPath,
		Branch:         "master",
		RepoName:       "x",
		UpdateInterval: time.Hour,
	}
}

// assertNoOpEnsureRepo verifies the on-disk preconditions that make ensureRepo()
// a no-op for the given provider: the .git directory is detected by repoExists()
// and the recent FETCH_HEAD mtime keeps needsUpdate() false. If either fails the
// real "git clone" path could run, which must never happen in these tests.
func assertNoOpEnsureRepo(t *testing.T, p *GitCloneProvider) {
	t.Helper()

	if !p.repoExists() {
		t.Fatalf("repoExists() = false for %q, want true (ensureRepo would clone)", p.LocalPath)
	}
	if p.needsUpdate() {
		t.Fatalf("needsUpdate() = true for %q, want false (ensureRepo would update)", p.LocalPath)
	}
}

// TestGitCloneProvider_LocalPackagePath_ExistingDevFoo covers the success branch
// using a realistic dev-foo/bar layout (distinct from the cat/pkg scenario in
// gitclone_localpath_test.go) and asserts that ensureRepo() stays a no-op.
func TestGitCloneProvider_LocalPackagePath_ExistingDevFoo(t *testing.T) {
	prov := newFakeClonedRepo(t)
	assertNoOpEnsureRepo(t, prov)

	pkgDir := filepath.Join(prov.LocalPath, "dev-foo", "bar")
	if err := os.MkdirAll(pkgDir, 0o750); err != nil {
		t.Fatalf("failed to create package dir: %v", err)
	}
	ebuild := filepath.Join(pkgDir, "bar-2.3.ebuild")
	if err := os.WriteFile(ebuild, []byte("# mock ebuild"), 0o640); err != nil {
		t.Fatalf("failed to write ebuild: %v", err)
	}

	got, err := prov.LocalPackagePath("dev-foo", "bar")
	if err != nil {
		t.Fatalf("LocalPackagePath returned error: %v", err)
	}
	if got != pkgDir {
		t.Errorf("LocalPackagePath = %q, want %q", got, pkgDir)
	}
}

// TestGitCloneProvider_LocalPackagePath_MissingPkgUnderCategory covers the
// os.IsNotExist -> ErrNotFound branch when the category directory exists but the
// requested package subdirectory does not.
func TestGitCloneProvider_LocalPackagePath_MissingPkgUnderCategory(t *testing.T) {
	prov := newFakeClonedRepo(t)
	assertNoOpEnsureRepo(t, prov)

	// Category dir exists, but the "bar" package under it does not.
	categoryDir := filepath.Join(prov.LocalPath, "dev-foo")
	if err := os.MkdirAll(categoryDir, 0o750); err != nil {
		t.Fatalf("failed to create category dir: %v", err)
	}

	got, err := prov.LocalPackagePath("dev-foo", "bar")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("LocalPackagePath error = %v, want ErrNotFound", err)
	}
	if got != "" {
		t.Errorf("LocalPackagePath path = %q, want empty string", got)
	}
}

// TestGitCloneProvider_LocalPackagePath_MissingCategory covers the
// os.IsNotExist -> ErrNotFound branch when the category directory itself is
// absent from the cloned tree.
func TestGitCloneProvider_LocalPackagePath_MissingCategory(t *testing.T) {
	prov := newFakeClonedRepo(t)
	assertNoOpEnsureRepo(t, prov)

	got, err := prov.LocalPackagePath("no-such-cat", "bar")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("LocalPackagePath error = %v, want ErrNotFound", err)
	}
	if got != "" {
		t.Errorf("LocalPackagePath path = %q, want empty string", got)
	}
}

// TestGitCloneProvider_LocalPackagePath_StatNonNotExistError covers the
// os.Stat error branch that is NOT os.IsNotExist: the "category" path component
// is a regular FILE, so stat-ing <LocalPath>/<category>/<pkg> fails with ENOTDIR
// (not a "does not exist" error). LocalPackagePath must return that raw error,
// NOT ErrNotFound. Binary-free: ensureRepo stays a no-op.
func TestGitCloneProvider_LocalPackagePath_StatNonNotExistError(t *testing.T) {
	prov := newFakeClonedRepo(t)
	assertNoOpEnsureRepo(t, prov)

	// "dev-foo" exists but as a regular file, so descending into it for the "bar"
	// package yields ENOTDIR rather than os.ErrNotExist.
	categoryAsFile := filepath.Join(prov.LocalPath, "dev-foo")
	if err := os.WriteFile(categoryAsFile, []byte("not a directory"), 0o640); err != nil {
		t.Fatalf("failed to write category-as-file: %v", err)
	}

	got, err := prov.LocalPackagePath("dev-foo", "bar")
	if err == nil {
		t.Fatal("LocalPackagePath error = nil, want a non-nil ENOTDIR-style error")
	}
	if errors.Is(err, ErrNotFound) {
		t.Errorf("LocalPackagePath error = %v, want a raw stat error (not ErrNotFound)", err)
	}
	if got != "" {
		t.Errorf("LocalPackagePath path = %q, want empty string", got)
	}
}
