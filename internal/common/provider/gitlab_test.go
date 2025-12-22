package provider

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGitLabProvider_GetPackageVersions(t *testing.T) {
	// Create mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Match the actual URL pattern the provider uses
		// The path query param will be URL-encoded
		if r.URL.Query().Get("path") == "app-misc/hello" {
			entries := []GitLabTreeEntry{
				{Name: "hello-1.0.ebuild", Type: "blob", Path: "app-misc/hello/hello-1.0.ebuild"},
				{Name: "hello-1.1.ebuild", Type: "blob", Path: "app-misc/hello/hello-1.1.ebuild"},
				{Name: "metadata.xml", Type: "blob", Path: "app-misc/hello/metadata.xml"},
				{Name: "files", Type: "tree", Path: "app-misc/hello/files"},
			}
			json.NewEncoder(w).Encode(entries)
			return
		}

		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	repoInfo := &RepositoryInfo{
		Name:     "test",
		Provider: "gitlab",
		URL:      "test/repo",
	}

	prov, err := NewGitLabProvider(repoInfo)
	if err != nil {
		t.Fatalf("NewGitLabProvider failed: %v", err)
	}

	// Override base URL to mock server
	prov.BaseURL = server.URL
	prov.CacheDir = "" // Disable cache

	t.Run("existing package", func(t *testing.T) {
		versions, err := prov.GetPackageVersions("app-misc", "hello")
		if err != nil {
			t.Errorf("GetPackageVersions failed: %v", err)
			return
		}

		if len(versions) != 2 {
			t.Errorf("Expected 2 versions, got %d: %v", len(versions), versions)
		}
	})
}

func TestGitLabProvider_TokenAuth(t *testing.T) {
	var privateToken string

	// Create mock server that captures auth header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		privateToken = r.Header.Get("PRIVATE-TOKEN")
		entries := []GitLabTreeEntry{}
		json.NewEncoder(w).Encode(entries)
	}))
	defer server.Close()

	repoInfo := &RepositoryInfo{
		Name:  "test",
		URL:   "test/repo",
		Token: "glpat-test-token",
	}

	prov, _ := NewGitLabProvider(repoInfo)
	prov.BaseURL = server.URL
	prov.CacheDir = ""

	_, _ = prov.GetPackageVersions("app-misc", "hello")

	if privateToken != "glpat-test-token" {
		t.Errorf("Expected 'glpat-test-token', got '%s'", privateToken)
	}
}

func TestParseGitLabURL(t *testing.T) {
	tests := []struct {
		input       string
		wantBase    string
		wantProject string
	}{
		{
			input:       "group/project",
			wantBase:    "https://gitlab.com",
			wantProject: "group/project",
		},
		{
			input:       "https://gitlab.com/gentoo/repo",
			wantBase:    "https://gitlab.com",
			wantProject: "gentoo/repo",
		},
		{
			input:       "https://gitlab.gentoo.org/repo/gentoo.git",
			wantBase:    "https://gitlab.gentoo.org",
			wantProject: "repo/gentoo",
		},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			base, project, err := parseGitLabURL(tc.input)
			if err != nil {
				t.Fatalf("parseGitLabURL failed: %v", err)
			}

			if base != tc.wantBase {
				t.Errorf("Base URL: expected %s, got %s", tc.wantBase, base)
			}
			if project != tc.wantProject {
				t.Errorf("Project: expected %s, got %s", tc.wantProject, project)
			}
		})
	}
}

