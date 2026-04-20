package provider

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGitCloneProvider_ScanLocalPackage(t *testing.T) {
	// Create a mock package directory
	tmpDir := t.TempDir()
	pkgDir := filepath.Join(tmpDir, "app-misc", "hello")
	os.MkdirAll(pkgDir, 0755)

	// Create mock ebuild files
	ebuilds := []string{
		"hello-1.0.ebuild",
		"hello-1.1.ebuild",
		"hello-2.0_rc1.ebuild",
		"hello-9999.ebuild",
	}

	for _, e := range ebuilds {
		os.WriteFile(filepath.Join(pkgDir, e), []byte("# mock ebuild"), 0644)
	}

	// Create non-ebuild files
	os.WriteFile(filepath.Join(pkgDir, "metadata.xml"), []byte("<pkgmetadata/>"), 0644)
	os.MkdirAll(filepath.Join(pkgDir, "files"), 0755)

	// Create provider (won't actually clone since we're testing scanLocalPackage)
	prov := &GitCloneProvider{
		LocalPath: tmpDir,
		RepoName:  "test",
	}

	versions, err := prov.scanLocalPackage(pkgDir, "hello")
	if err != nil {
		t.Fatalf("scanLocalPackage failed: %v", err)
	}

	if len(versions) != 4 {
		t.Errorf("Expected 4 versions, got %d: %v", len(versions), versions)
	}

	expected := map[string]bool{"1.0": true, "1.1": true, "2.0_rc1": true, "9999": true}
	for _, v := range versions {
		if !expected[v] {
			t.Errorf("Unexpected version: %s", v)
		}
	}
}

func TestGitCloneProvider_NotFound(t *testing.T) {
	tmpDir := t.TempDir()

	prov := &GitCloneProvider{
		LocalPath: tmpDir,
		RepoName:  "test",
	}

	_, err := prov.scanLocalPackage(filepath.Join(tmpDir, "nonexistent"), "hello")
	if err != ErrNotFound {
		t.Errorf("Expected ErrNotFound, got: %v", err)
	}
}

func TestGitCloneProvider_GetName(t *testing.T) {
	repoInfo := &RepositoryInfo{
		Name: "gentoo",
		URL:  "gentoo/gentoo",
	}

	prov, err := NewGitCloneProvider(repoInfo)
	if err != nil {
		t.Fatalf("NewGitCloneProvider failed: %v", err)
	}

	name := prov.GetName()
	expected := "Git Clone (gentoo)"
	if name != expected {
		t.Errorf("Expected %s, got %s", expected, name)
	}
}

func TestGitCloneProvider_SupportsAPI(t *testing.T) {
	prov := &GitCloneProvider{}
	if prov.SupportsAPI() {
		t.Error("GitCloneProvider should not support API")
	}
}

func TestGitCloneProvider_URLFormats(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantURL string
	}{
		{
			name:    "github shorthand",
			url:     "gentoo/gentoo",
			wantURL: "https://github.com/gentoo/gentoo.git",
		},
		{
			name:    "full https url",
			url:     "https://github.com/gentoo/gentoo.git",
			wantURL: "https://github.com/gentoo/gentoo.git",
		},
		{
			name:    "ssh url",
			url:     "git@github.com:gentoo/gentoo.git",
			wantURL: "git@github.com:gentoo/gentoo.git",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repoInfo := &RepositoryInfo{
				Name: "test",
				URL:  tc.url,
			}

			prov, err := NewGitCloneProvider(repoInfo)
			if err != nil {
				t.Fatalf("NewGitCloneProvider failed: %v", err)
			}

			if prov.RepoURL != tc.wantURL {
				t.Errorf("Expected URL %s, got %s", tc.wantURL, prov.RepoURL)
			}
		})
	}
}

// TestGitCloneProvider_Close tests Close returns nil
func TestGitCloneProvider_Close(t *testing.T) {
	prov := &GitCloneProvider{}
	if err := prov.Close(); err != nil {
		t.Errorf("Close should return nil, got %v", err)
	}
}

// TestGitCloneProvider_RepoExists tests repoExists with and without .git dir
func TestGitCloneProvider_RepoExists(t *testing.T) {
	tmpDir := t.TempDir()
	prov := &GitCloneProvider{LocalPath: tmpDir}

	// No .git dir — should not exist
	if prov.repoExists() {
		t.Error("Expected repoExists=false when .git dir is absent")
	}

	// Create .git dir
	os.MkdirAll(filepath.Join(tmpDir, ".git"), 0755)
	if !prov.repoExists() {
		t.Error("Expected repoExists=true when .git dir is present")
	}
}

// TestGitCloneProvider_NeedsUpdate tests needsUpdate logic
func TestGitCloneProvider_NeedsUpdate(t *testing.T) {
	tmpDir := t.TempDir()
	prov := &GitCloneProvider{LocalPath: tmpDir, UpdateInterval: 24 * time.Hour}

	// No FETCH_HEAD — needs update
	if !prov.needsUpdate() {
		t.Error("Expected needsUpdate=true when FETCH_HEAD is absent")
	}

	// Create fresh FETCH_HEAD
	fetchHead := filepath.Join(tmpDir, ".git", "FETCH_HEAD")
	os.MkdirAll(filepath.Join(tmpDir, ".git"), 0755)
	os.WriteFile(fetchHead, []byte(""), 0644)

	if prov.needsUpdate() {
		t.Error("Expected needsUpdate=false for fresh FETCH_HEAD")
	}
}

// TestGitCloneProvider_RemoveCache tests RemoveCache removes the local path
func TestGitCloneProvider_RemoveCache(t *testing.T) {
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "repo")
	os.MkdirAll(subDir, 0755)

	prov := &GitCloneProvider{LocalPath: subDir}
	if err := prov.RemoveCache(); err != nil {
		t.Fatalf("RemoveCache failed: %v", err)
	}
	if _, err := os.Stat(subDir); !os.IsNotExist(err) {
		t.Error("Expected directory to be removed")
	}
}

// TestGitCloneProvider_ScanLocalPackageNonEbuild tests files without .ebuild are skipped
func TestGitCloneProvider_ScanLocalPackageNonEbuild(t *testing.T) {
	tmpDir := t.TempDir()
	os.WriteFile(filepath.Join(tmpDir, "metadata.xml"), []byte(""), 0644)
	os.WriteFile(filepath.Join(tmpDir, "Manifest"), []byte(""), 0644)

	prov := &GitCloneProvider{LocalPath: tmpDir}
	versions, err := prov.scanLocalPackage(tmpDir, "hello")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if len(versions) != 0 {
		t.Errorf("Expected 0 versions, got %d", len(versions))
	}
}

// TestGitCloneProvider_ForceUpdate_NoRepo tests ForceUpdate when repo doesn't exist
// (will fail to clone, but exercises the code path)
func TestGitCloneProvider_ForceUpdate_NoRepo(t *testing.T) {
	tmpDir := t.TempDir()
	prov := &GitCloneProvider{
		LocalPath: filepath.Join(tmpDir, "nonexistent"),
		RepoURL:   "https://invalid.example.com/repo.git",
		Branch:    "master",
	}
	// Expected to fail (no real git), but exercises cloneRepo path
	_ = prov.ForceUpdate()
}

// TestGitCloneProvider_ForceUpdate_ExistingRepo tests ForceUpdate when .git exists
func TestGitCloneProvider_ForceUpdate_ExistingRepo(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(filepath.Join(tmpDir, ".git"), 0755)

	prov := &GitCloneProvider{
		LocalPath: tmpDir,
		RepoURL:   "https://invalid.example.com/repo.git",
		Branch:    "master",
	}
	// Expected to fail (no real git), but exercises updateRepo path
	_ = prov.ForceUpdate()
}

// TestGitCloneProvider_EnsureRepo_NeedsClone tests ensureRepo when repo doesn't exist
func TestGitCloneProvider_EnsureRepo_NeedsClone(t *testing.T) {
	tmpDir := t.TempDir()
	prov := &GitCloneProvider{
		LocalPath: filepath.Join(tmpDir, "repo"),
		RepoURL:   "https://invalid.example.com/repo.git",
		Branch:    "master",
	}
	// Will fail to clone but exercises the path
	_ = prov.ensureRepo()
}

// TestGitCloneProvider_EnsureRepo_UpToDate tests ensureRepo when repo is fresh
func TestGitCloneProvider_EnsureRepo_UpToDate(t *testing.T) {
	tmpDir := t.TempDir()
	os.MkdirAll(filepath.Join(tmpDir, ".git"), 0755)
	// Write fresh FETCH_HEAD
	os.WriteFile(filepath.Join(tmpDir, ".git", "FETCH_HEAD"), []byte(""), 0644)

	prov := &GitCloneProvider{
		LocalPath:      tmpDir,
		UpdateInterval: 24 * time.Hour,
	}
	// Repo is fresh — ensureRepo should return nil without calling update
	err := prov.ensureRepo()
	if err != nil {
		t.Errorf("Expected nil for up-to-date repo, got %v", err)
	}
}
