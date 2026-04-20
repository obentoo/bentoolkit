package provider

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func newMockRegistry(t *testing.T, xml string) *RepositoryRegistry {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, xml)
	}))
	t.Cleanup(server.Close)
	dir := t.TempDir()
	return &RepositoryRegistry{
		CacheDir: dir,
		CacheTTL: defaultCacheTTL,
		XMLPath:  filepath.Join(dir, registryXMLFile),
		url:      server.URL,
	}
}

const registryTestXML = `<?xml version="1.0"?>
<repositories version="1.0">
  <repo><name>gentoo</name><source type="git">https://github.com/gentoo-mirror/gentoo.git</source></repo>
  <repo><name>guru</name><source type="git">https://github.com/gentoo-mirror/guru.git</source></repo>
  <repo><name>my-overlay</name><source type="git">https://github.com/user/overlay.git</source></repo>
</repositories>`

func TestResolveRepository(t *testing.T) {
	registry := newMockRegistry(t, registryTestXML)

	tests := []struct {
		name        string
		repoName    string
		configRepos map[string]*RepositoryInfo
		registry    *RepositoryRegistry
		wantName    string
		wantURL     string
		wantErr     bool
	}{
		{
			name:     "config defined repo",
			repoName: "my-overlay",
			configRepos: map[string]*RepositoryInfo{
				"my-overlay": {
					Name:     "my-overlay",
					Provider: "github",
					URL:      "user/overlay",
					Branch:   "main",
				},
			},
			wantName: "my-overlay",
			wantURL:  "user/overlay",
		},
		{
			name:     "registry fallback for gentoo",
			repoName: "gentoo",
			registry: registry,
			wantName: "gentoo",
			wantURL:  "gentoo-mirror/gentoo",
		},
		{
			name:     "registry fallback for guru",
			repoName: "guru",
			registry: registry,
			wantName: "guru",
			wantURL:  "gentoo-mirror/guru",
		},
		{
			name:     "config takes priority over registry",
			repoName: "my-overlay",
			configRepos: map[string]*RepositoryInfo{
				"my-overlay": {
					Name:     "my-overlay",
					Provider: "git",
					URL:      "/local/path",
					Branch:   "main",
				},
			},
			registry: registry,
			wantName: "my-overlay",
			wantURL:  "/local/path",
		},
		{
			name:     "not found in either",
			repoName: "unknown",
			registry: registry,
			wantErr:  true,
		},
		{
			name:     "nil registry config-only resolution",
			repoName: "test",
			configRepos: map[string]*RepositoryInfo{
				"test": {Name: "test", URL: "org/test"},
			},
			wantName: "test",
			wantURL:  "org/test",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo, err := ResolveRepository(tc.repoName, tc.configRepos, tc.registry)

			if tc.wantErr {
				if err == nil {
					t.Error("Expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if repo.Name != tc.wantName {
				t.Errorf("Name: expected %s, got %s", tc.wantName, repo.Name)
			}
			if repo.URL != tc.wantURL {
				t.Errorf("URL: expected %s, got %s", tc.wantURL, repo.URL)
			}
		})
	}
}

func TestListAvailableRepositories(t *testing.T) {
	registry := newMockRegistry(t, registryTestXML)

	configRepos := map[string]*RepositoryInfo{
		"custom": {Name: "custom"},
	}

	repos := ListAvailableRepositories(configRepos, registry)

	expected := map[string]bool{
		"gentoo":     true,
		"guru":       true,
		"my-overlay": true,
		"custom":     true,
	}

	if len(repos) != len(expected) {
		t.Errorf("Expected %d repos, got %d: %v", len(expected), len(repos), repos)
	}

	for _, repo := range repos {
		if !expected[repo] {
			t.Errorf("Unexpected repo: %s", repo)
		}
	}

	// Verify sorted
	for i := 1; i < len(repos); i++ {
		if repos[i] < repos[i-1] {
			t.Errorf("list not sorted: %q before %q", repos[i-1], repos[i])
		}
	}
}

func TestListAvailableRepositories_Deduplicated(t *testing.T) {
	registry := newMockRegistry(t, registryTestXML)

	configRepos := map[string]*RepositoryInfo{
		"gentoo": {Name: "gentoo", URL: "/local/gentoo"},
	}

	repos := ListAvailableRepositories(configRepos, registry)

	count := 0
	for _, r := range repos {
		if r == "gentoo" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 'gentoo' once, found %d times in %v", count, repos)
	}
}

func TestListAvailableRepositories_NilRegistry(t *testing.T) {
	configRepos := map[string]*RepositoryInfo{
		"test": {Name: "test"},
	}

	repos := ListAvailableRepositories(configRepos, nil)
	if len(repos) != 1 || repos[0] != "test" {
		t.Errorf("expected [test], got %v", repos)
	}
}

func TestNewProvider(t *testing.T) {
	tests := []struct {
		name       string
		repoInfo   *RepositoryInfo
		forceClone bool
		wantType   string
		wantAPI    bool
	}{
		{
			name: "github provider",
			repoInfo: &RepositoryInfo{
				Name:     "test",
				Provider: "github",
				URL:      "test/repo",
			},
			forceClone: false,
			wantType:   "GitHubProvider",
			wantAPI:    true,
		},
		{
			name: "gitlab provider",
			repoInfo: &RepositoryInfo{
				Name:     "test",
				Provider: "gitlab",
				URL:      "test/repo",
			},
			forceClone: false,
			wantType:   "GitLabProvider",
			wantAPI:    true,
		},
		{
			name: "git clone provider",
			repoInfo: &RepositoryInfo{
				Name:     "test",
				Provider: "git",
				URL:      "https://example.com/repo.git",
			},
			forceClone: false,
			wantType:   "GitCloneProvider",
			wantAPI:    false,
		},
		{
			name: "force clone on github",
			repoInfo: &RepositoryInfo{
				Name:     "test",
				Provider: "github",
				URL:      "test/repo",
			},
			forceClone: true,
			wantType:   "GitCloneProvider",
			wantAPI:    false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			prov, err := NewProvider(tc.repoInfo, tc.forceClone)
			if err != nil {
				t.Fatalf("NewProvider failed: %v", err)
			}

			if prov.SupportsAPI() != tc.wantAPI {
				t.Errorf("SupportsAPI: expected %v, got %v", tc.wantAPI, prov.SupportsAPI())
			}

			switch tc.wantType {
			case "GitHubProvider":
				if _, ok := prov.(*GitHubProvider); !ok {
					t.Errorf("Expected *GitHubProvider, got %T", prov)
				}
			case "GitLabProvider":
				if _, ok := prov.(*GitLabProvider); !ok {
					t.Errorf("Expected *GitLabProvider, got %T", prov)
				}
			case "GitCloneProvider":
				if _, ok := prov.(*GitCloneProvider); !ok {
					t.Errorf("Expected *GitCloneProvider, got %T", prov)
				}
			}
		})
	}
}

func TestNewProviderNilInput(t *testing.T) {
	_, err := NewProvider(nil, false)
	if err == nil {
		t.Fatal("Expected error for nil RepositoryInfo, got nil")
	}
	if !errors.Is(err, ErrRepositoryNotFound) {
		t.Errorf("Expected ErrRepositoryNotFound, got %v", err)
	}
}

func TestNewProviderInvalidType(t *testing.T) {
	repoInfo := &RepositoryInfo{
		Name:     "test",
		Provider: "unknown-provider",
		URL:      "test/repo",
	}
	_, err := NewProvider(repoInfo, false)
	if err == nil {
		t.Fatal("Expected error for invalid provider type, got nil")
	}
	if !errors.Is(err, ErrInvalidProvider) {
		t.Errorf("Expected ErrInvalidProvider, got %v", err)
	}
}

func TestFactory_UnknownProvider(t *testing.T) {
	repoInfo := &RepositoryInfo{
		Name:     "test",
		Provider: "bitbucket",
		URL:      "myorg/myrepo",
	}

	_, err := NewProvider(repoInfo, false)
	if err == nil {
		t.Fatal("Expected error for unknown provider, got nil")
	}
	if !errors.Is(err, ErrInvalidProvider) {
		t.Errorf("Expected ErrInvalidProvider, got %v", err)
	}
	if err.Error() == "" {
		t.Error("Error message should be non-empty")
	}
}

func TestFactory_MissingURL(t *testing.T) {
	tests := []struct {
		name     string
		provider string
	}{
		{"github empty url", "github"},
		{"gitlab empty url", "gitlab"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repoInfo := &RepositoryInfo{
				Name:     "test",
				Provider: tc.provider,
				URL:      "",
			}
			prov, err := NewProvider(repoInfo, false)
			if err != nil {
				return
			}
			if prov != nil {
				_ = prov.Close()
			}
		})
	}
}

func TestRepositoryInfoClone(t *testing.T) {
	original := &RepositoryInfo{
		Name:     "original",
		Provider: "github",
		URL:      "org/repo",
		Token:    "secret",
		Branch:   "main",
	}

	clone := original.Clone()

	if clone.Name != original.Name || clone.Provider != original.Provider ||
		clone.URL != original.URL || clone.Token != original.Token ||
		clone.Branch != original.Branch {
		t.Error("Clone fields do not match original")
	}

	clone.Name = "modified"
	clone.Token = "other"
	if original.Name != "original" {
		t.Error("Modifying clone affected original Name")
	}
	if original.Token != "secret" {
		t.Error("Modifying clone affected original Token")
	}
}
