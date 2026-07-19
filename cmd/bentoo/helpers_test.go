package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/provider"
)

// TestTruncatePkgName tests the truncatePkgName helper function.
func TestTruncatePkgName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxLen   int
		wantLen  int
		wantSufx string
	}{
		{"short name padded", "foo", 10, 10, ""},
		{"exact length", "abcdefghij", 10, 10, ""},
		{"long name truncated", "this-is-a-very-long-package-name", 10, 10, "..."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncatePkgName(tt.input, tt.maxLen)
			if len(got) != tt.wantLen {
				t.Errorf("truncatePkgName(%q, %d) len = %d, want %d", tt.input, tt.maxLen, len(got), tt.wantLen)
			}
			if tt.wantSufx != "" && !strings.HasSuffix(got, tt.wantSufx) {
				t.Errorf("truncatePkgName(%q, %d) = %q, want suffix %q", tt.input, tt.maxLen, got, tt.wantSufx)
			}
		})
	}
}

// TestTruncatePkgNameShortPadded tests that short names are padded to maxLen.
func TestTruncatePkgNameShortPadded(t *testing.T) {
	got := truncatePkgName("foo", 10)
	if !strings.HasPrefix(got, "foo") {
		t.Errorf("truncatePkgName should preserve original name, got %q", got)
	}
	if len(got) != 10 {
		t.Errorf("truncatePkgName should pad to maxLen=10, got len=%d", len(got))
	}
}

// TestConvertConfigReposNil tests convertConfigRepos with nil repositories.
func TestConvertConfigReposNil(t *testing.T) {
	cfg := &config.Config{}
	result := convertConfigRepos(cfg)
	if result != nil {
		t.Errorf("convertConfigRepos with nil repos should return nil, got %v", result)
	}
}

// TestConvertConfigRepos tests convertConfigRepos with populated repositories,
// including per-repo token resolution from BENTOO_REPO_<NAME>_TOKEN via the
// secrets chain. HOME + XDG_CONFIG_HOME are isolated to an empty temp dir so the
// host's real ~/.config/bentoo/secrets cannot influence the result; the env var
// (checked first by secrets.Lookup) supplies the token deterministically.
func TestConvertConfigRepos(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("BENTOO_REPO_MYGENTOO_TOKEN", "tok123")

	cfg := &config.Config{
		Repositories: map[string]*config.RepoConfig{
			"mygentoo": {
				Provider: "github",
				URL:      "https://github.com/gentoo/gentoo",
				Branch:   "master",
			},
		},
	}
	result := convertConfigRepos(cfg)
	if result == nil {
		t.Fatal("convertConfigRepos should return non-nil map")
	}
	repo, ok := result["mygentoo"]
	if !ok {
		t.Fatal("result should contain 'mygentoo' key")
	}
	if repo.Name != "mygentoo" {
		t.Errorf("Name = %q, want %q", repo.Name, "mygentoo")
	}
	if repo.Provider != "github" {
		t.Errorf("Provider = %q, want %q", repo.Provider, "github")
	}
	if repo.URL != "https://github.com/gentoo/gentoo" {
		t.Errorf("URL = %q, want %q", repo.URL, "https://github.com/gentoo/gentoo")
	}
	if repo.Token != "tok123" {
		t.Errorf("Token = %q, want %q", repo.Token, "tok123")
	}
	if repo.Branch != "master" {
		t.Errorf("Branch = %q, want %q", repo.Branch, "master")
	}
}

// TestConvertConfigReposMultiple tests convertConfigRepos with multiple repositories.
func TestConvertConfigReposMultiple(t *testing.T) {
	cfg := &config.Config{
		Repositories: map[string]*config.RepoConfig{
			"repo1": {Provider: "github", URL: "https://github.com/a/b"},
			"repo2": {Provider: "gitlab", URL: "https://gitlab.com/c/d"},
		},
	}
	result := convertConfigRepos(cfg)
	if len(result) != 2 {
		t.Errorf("expected 2 repos, got %d", len(result))
	}
	if _, ok := result["repo1"]; !ok {
		t.Error("result should contain 'repo1'")
	}
	if _, ok := result["repo2"]; !ok {
		t.Error("result should contain 'repo2'")
	}
}

// TestGetStatusColor tests getStatusColor returns non-nil color for all statuses.
func TestGetStatusColor(t *testing.T) {
	statuses := []autoupdate.UpdateStatus{
		autoupdate.StatusPending,
		autoupdate.StatusValidated,
		autoupdate.StatusFailed,
		autoupdate.StatusApplied,
		autoupdate.UpdateStatus("unknown"),
	}
	for _, status := range statuses {
		t.Run(string(status), func(t *testing.T) {
			c := getStatusColor(status)
			if c == nil {
				t.Errorf("getStatusColor(%q) should return non-nil color", status)
			}
		})
	}
}

// TestConvertConfigReposPreservesAllFields tests that all RepositoryInfo fields
// are mapped, with the token resolved from BENTOO_REPO_<NAME>_TOKEN via the
// secrets chain. HOME + XDG_CONFIG_HOME are isolated so the host's real secrets
// file cannot influence the result.
func TestConvertConfigReposPreservesAllFields(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("BENTOO_REPO_TEST_TOKEN", "secret")

	cfg := &config.Config{
		Repositories: map[string]*config.RepoConfig{
			"test": {
				Provider: "git",
				URL:      "https://example.com/repo",
				Branch:   "main",
			},
		},
	}
	result := convertConfigRepos(cfg)
	repo := result["test"]

	// Verify it's a *provider.RepositoryInfo
	_ = (*provider.RepositoryInfo)(repo)
	if repo.Provider != "git" {
		t.Errorf("Provider = %q, want %q", repo.Provider, "git")
	}
	if repo.Token != "secret" {
		t.Errorf("Token = %q, want %q", repo.Token, "secret")
	}
}
