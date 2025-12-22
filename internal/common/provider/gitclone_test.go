package provider

import (
	"os"
	"path/filepath"
	"testing"
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
		name     string
		url      string
		wantURL  string
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

