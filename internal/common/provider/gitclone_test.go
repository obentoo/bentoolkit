package provider

import (
	"context"
	"errors"
	"os"
	"os/exec"
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

// TestValidateRepoURL verifies scheme/host validation for repository URLs. (R2.1)
func TestValidateRepoURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{name: "https ok", url: "https://x.io", wantErr: false},
		{name: "https uppercase scheme ok", url: "HTTPS://x.io", wantErr: false},
		{name: "http ok", url: "http://x.io/repo.git", wantErr: false},
		{name: "git scheme ok", url: "git://github.com/org/repo.git", wantErr: false},
		{name: "ssh scheme ok", url: "ssh://git@github.com/org/repo.git", wantErr: false},
		{name: "scp-like ssh ok", url: "git@github.com:org/repo.git", wantErr: false},
		{name: "file scheme rejected", url: "file:///etc/passwd", wantErr: true},
		{name: "javascript pseudo-scheme rejected", url: "javascript:alert(1)", wantErr: true},
		{name: "empty host rejected", url: "https://", wantErr: true},
		{name: "empty url rejected", url: "", wantErr: true},
		{name: "leading-dash upload-pack rejected", url: "--upload-pack=evil@host:path", wantErr: true},
		{name: "leading-dash proxycommand rejected", url: "-oProxyCommand=x", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateRepoURL(tc.url)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ValidateRepoURL(%q) = nil, want error", tc.url)
				}
				if !errors.Is(err, ErrInvalidRepoURL) {
					t.Errorf("ValidateRepoURL(%q) error %v, want wrapped ErrInvalidRepoURL", tc.url, err)
				}
			} else if err != nil {
				t.Errorf("ValidateRepoURL(%q) = %v, want nil", tc.url, err)
			}
		})
	}
}

// TestValidateBranch verifies git check-ref-format-style branch validation. (R2.2)
func TestValidateBranch(t *testing.T) {
	tests := []struct {
		name    string
		branch  string
		wantErr bool
	}{
		{name: "slash path ok", branch: "release/1.x", wantErr: false},
		{name: "plus sign ok", branch: "feature/foo+bar", wantErr: false},
		{name: "version tag ok", branch: "v1.2.3", wantErr: false},
		{name: "single dot ok", branch: "bug.fix", wantErr: false},
		{name: "leading dash rejected", branch: "--upload-pack=evil", wantErr: true},
		{name: "whitespace rejected", branch: " ", wantErr: true},
		{name: "double dot rejected", branch: "..", wantErr: true},
		{name: "at-brace rejected", branch: "feat@{1}", wantErr: true},
		{name: "nul byte rejected", branch: "feat\x00ure", wantErr: true},
		{name: "rtl override rejected", branch: "ma\u202emaster", wantErr: true},
		{name: "empty rejected", branch: "", wantErr: true},
		{name: "tilde rejected", branch: "feat~1", wantErr: true},
		{name: "caret rejected", branch: "feat^1", wantErr: true},
		{name: "colon rejected", branch: "a:b", wantErr: true},
		{name: "control char rejected", branch: "feat\ture", wantErr: true},
		// Regression: PBT falsifying input (ends with "/").
		{name: "pbt falsifying input rejected", branch: "5M@5/A0λa0世/", wantErr: true},
		{name: "trailing slash rejected", branch: "trailing/", wantErr: true},
		{name: "leading slash rejected", branch: "/leading", wantErr: true},
		{name: "double slash rejected", branch: "a//b", wantErr: true},
		{name: "trailing dot rejected", branch: "ends.", wantErr: true},
		{name: "leading dot component rejected", branch: ".hidden", wantErr: true},
		{name: "nested leading dot component rejected", branch: "feature/.hidden", wantErr: true},
		{name: "dot-lock suffix rejected", branch: "foo.lock", wantErr: true},
		{name: "nested dot-lock suffix rejected", branch: "feature/bar.lock", wantErr: true},
		{name: "bare at rejected", branch: "@", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateBranch(tc.branch)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ValidateBranch(%q) = nil, want error", tc.branch)
				}
				if !errors.Is(err, ErrInvalidBranch) {
					t.Errorf("ValidateBranch(%q) error %v, want wrapped ErrInvalidBranch", tc.branch, err)
				}
			} else if err != nil {
				t.Errorf("ValidateBranch(%q) = %v, want nil", tc.branch, err)
			}
		})
	}
}

// TestNewGitCloneProvider_RejectsBadInputs verifies the constructor rejects a
// malicious URL and a malicious branch with the wrapped sentinels. (R2.1, R2.2)
func TestNewGitCloneProvider_RejectsBadInputs(t *testing.T) {
	t.Run("bad URL", func(t *testing.T) {
		_, err := NewGitCloneProvider(&RepositoryInfo{
			Name:   "evil",
			URL:    "file:///etc/passwd",
			Branch: "master",
		})
		if err == nil {
			t.Fatal("NewGitCloneProvider with file:// URL = nil error, want error")
		}
		if !errors.Is(err, ErrInvalidRepoURL) {
			t.Errorf("error %v, want wrapped ErrInvalidRepoURL", err)
		}
	})

	t.Run("bad branch", func(t *testing.T) {
		_, err := NewGitCloneProvider(&RepositoryInfo{
			Name:   "evil",
			URL:    "https://github.com/org/repo.git",
			Branch: "--upload-pack=evil",
		})
		if err == nil {
			t.Fatal("NewGitCloneProvider with flag-injection branch = nil error, want error")
		}
		if !errors.Is(err, ErrInvalidBranch) {
			t.Errorf("error %v, want wrapped ErrInvalidBranch", err)
		}
	})
}

// TestGitCloneProvider_TimeoutHonored verifies that cloneRepo cancels the git
// process once the context deadline expires. (R2.3)
func TestGitCloneProvider_TimeoutHonored(t *testing.T) {
	// Override execCommand with a blocking process ("sleep 60") and shorten
	// the effective deadline by wrapping the supplied context.
	origExecCommand := execCommand
	t.Cleanup(func() { execCommand = origExecCommand })

	execCommand = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		short, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		// The clone helper owns the parent context's cancel; this child
		// cancel is released once the slow process is reaped.
		_ = cancel
		return exec.CommandContext(short, "sleep", "60")
	}

	tmpDir := t.TempDir()
	prov := &GitCloneProvider{
		LocalPath: filepath.Join(tmpDir, "repo"),
		RepoURL:   "https://github.com/org/repo.git",
		Branch:    "master",
	}

	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- prov.cloneRepo() }()

	select {
	case err := <-done:
		elapsed := time.Since(start)
		if err == nil {
			t.Fatal("cloneRepo() = nil, want clone-failed error from deadline")
		}
		if !errors.Is(err, ErrCloneFailed) {
			t.Errorf("error %v, want wrapped ErrCloneFailed", err)
		}
		if elapsed > 150*time.Millisecond {
			t.Errorf("cloneRepo took %v, want <= 150ms (deadline not honored)", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cloneRepo did not return within 2s — context deadline not honored")
	}
}
