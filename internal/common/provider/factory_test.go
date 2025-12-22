package provider

import (
	"testing"
)

func TestResolveRepository(t *testing.T) {
	tests := []struct {
		name        string
		repoName    string
		configRepos map[string]*RepositoryInfo
		wantName    string
		wantURL     string
		wantErr     bool
	}{
		{
			name:     "builtin gentoo",
			repoName: "gentoo",
			wantName: "gentoo",
			wantURL:  "gentoo/gentoo",
		},
		{
			name:     "builtin guru",
			repoName: "guru",
			wantName: "guru",
			wantURL:  "gentoo/guru",
		},
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
			name:     "not found",
			repoName: "unknown",
			wantErr:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo, err := ResolveRepository(tc.repoName, tc.configRepos)

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

func TestListAvailableRepositories(t *testing.T) {
	configRepos := map[string]*RepositoryInfo{
		"my-overlay": {Name: "my-overlay"},
		"custom":     {Name: "custom"},
	}

	repos := ListAvailableRepositories(configRepos)

	// Should include builtin repos + config repos
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
}

