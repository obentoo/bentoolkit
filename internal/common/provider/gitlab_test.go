package provider

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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

// TestGitLabProvider_RateLimit tests Req 8.5: HTTP 429 returns ErrRateLimit
func TestGitLabProvider_RateLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
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
	prov.BaseURL = server.URL
	prov.CacheDir = ""

	_, err = prov.GetPackageVersions("app-misc", "hello")
	if err == nil {
		t.Fatal("Expected error, got nil")
	}
	if !errors.Is(err, ErrRateLimit) {
		t.Errorf("Expected ErrRateLimit, got %v", err)
	}
}

// TestParseGitLabURLAdditional tests Req 8.6: additional URL format cases
func TestParseGitLabURLAdditional(t *testing.T) {
	tests := []struct {
		input       string
		wantBase    string
		wantProject string
		wantErr     bool
	}{
		{
			input:       "subgroup/group/project",
			wantBase:    "https://gitlab.com",
			wantProject: "subgroup/group/project",
		},
		{
			input:       "https://gitlab.freedesktop.org/mesa/mesa",
			wantBase:    "https://gitlab.freedesktop.org",
			wantProject: "mesa/mesa",
		},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			base, project, err := parseGitLabURL(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Error("Expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseGitLabURL failed: %v", err)
			}
			if base != tc.wantBase {
				t.Errorf("Base: expected %s, got %s", tc.wantBase, base)
			}
			if project != tc.wantProject {
				t.Errorf("Project: expected %s, got %s", tc.wantProject, project)
			}
		})
	}
}

// TestGitLabProvider_GetName tests GetName returns non-empty string
func TestGitLabProvider_GetName(t *testing.T) {
	repoInfo := &RepositoryInfo{Name: "test", Provider: "gitlab", URL: "test/repo"}
	prov, _ := NewGitLabProvider(repoInfo)
	if prov.GetName() == "" {
		t.Error("GetName should return non-empty string")
	}
}

// TestGitLabProvider_Close tests Close returns nil
func TestGitLabProvider_Close(t *testing.T) {
	repoInfo := &RepositoryInfo{Name: "test", Provider: "gitlab", URL: "test/repo"}
	prov, _ := NewGitLabProvider(repoInfo)
	if err := prov.Close(); err != nil {
		t.Errorf("Close should return nil, got %v", err)
	}
}

// TestGitLabProvider_Cache tests cache save/load round-trip
func TestGitLabProvider_Cache(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		entries := []GitLabTreeEntry{
			{Name: "hello-1.0.ebuild", Type: "blob", Path: "app-misc/hello/hello-1.0.ebuild"},
		}
		json.NewEncoder(w).Encode(entries)
	}))
	defer server.Close()

	repoInfo := &RepositoryInfo{Name: "test", Provider: "gitlab", URL: "test/repo"}
	prov, _ := NewGitLabProvider(repoInfo)
	prov.BaseURL = server.URL
	prov.CacheDir = t.TempDir()

	_, err := prov.GetPackageVersions("app-misc", "hello")
	if err != nil {
		t.Fatalf("First call failed: %v", err)
	}
	_, err = prov.GetPackageVersions("app-misc", "hello")
	if err != nil {
		t.Fatalf("Second call failed: %v", err)
	}
	if callCount != 1 {
		t.Errorf("Expected 1 API call (cached), got %d", callCount)
	}
}

// TestGitLabProvider_NotFound tests 404 returns ErrNotFound
func TestGitLabProvider_NotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	repoInfo := &RepositoryInfo{Name: "test", Provider: "gitlab", URL: "test/repo"}
	prov, _ := NewGitLabProvider(repoInfo)
	prov.BaseURL = server.URL
	prov.CacheDir = ""

	_, err := prov.GetPackageVersions("app-misc", "nonexistent")
	if err == nil {
		t.Fatal("Expected error, got nil")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Expected ErrNotFound, got %v", err)
	}
}

// TestGitLabProvider_InvalidToken tests that HTTP 401 returns ErrAPIError.
func TestGitLabProvider_InvalidToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"message":"401 Unauthorized"}`)) //nolint:errcheck
	}))
	defer server.Close()

	repoInfo := &RepositoryInfo{
		Name:     "test",
		Provider: "gitlab",
		URL:      "test/repo",
		Token:    "bad-token",
	}
	prov, err := NewGitLabProvider(repoInfo)
	if err != nil {
		t.Fatalf("NewGitLabProvider failed: %v", err)
	}
	prov.BaseURL = server.URL
	prov.CacheDir = ""

	_, err = prov.GetPackageVersions("app-misc", "hello")
	if err == nil {
		t.Fatal("Expected error for 401, got nil")
	}
	if !errors.Is(err, ErrAPIError) {
		t.Errorf("Expected ErrAPIError, got %v", err)
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("Error should mention status code 401, got: %v", err)
	}
}

// TestGitLabProvider_RepoNotFound is an alias-style test that verifies 404 returns
// ErrNotFound with a descriptive error — distinct from the generic NotFound test
// above in that it checks error message content.
func TestGitLabProvider_RepoNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	prov.BaseURL = server.URL
	prov.CacheDir = ""

	_, err = prov.GetPackageVersions("cat", "pkg-that-does-not-exist")
	if err == nil {
		t.Fatal("Expected error for repo not found, got nil")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("Expected ErrNotFound, got %v", err)
	}
}

// TestGitLabProvider_RateLimitWithRetryAfterHeader tests that HTTP 429 with a
// Retry-After header still returns ErrRateLimit.
func TestGitLabProvider_RateLimitWithRetryAfterHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
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
	prov.BaseURL = server.URL
	prov.CacheDir = ""

	_, err = prov.GetPackageVersions("app-misc", "hello")
	if err == nil {
		t.Fatal("Expected ErrRateLimit, got nil")
	}
	if !errors.Is(err, ErrRateLimit) {
		t.Errorf("Expected ErrRateLimit, got %v", err)
	}
}

// TestGitLabProvider_SaveToCacheFileMode verifies that GitLabProvider writes
// its cache file with the restrictive owner-only mode (0600), not a
// world-readable 0644. Cache files may hold sensitive upstream metadata.
// (R9.1, R9.3)
func TestGitLabProvider_SaveToCacheFileMode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("path") == "app-misc/hello" {
			entries := []GitLabTreeEntry{
				{Name: "hello-1.0.ebuild", Type: "blob", Path: "app-misc/hello/hello-1.0.ebuild"},
			}
			if err := json.NewEncoder(w).Encode(entries); err != nil {
				t.Errorf("failed to encode response: %v", err)
			}
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
	prov.BaseURL = server.URL
	prov.CacheDir = t.TempDir()

	if _, err := prov.GetPackageVersions("app-misc", "hello"); err != nil {
		t.Fatalf("GetPackageVersions returned error: %v", err)
	}

	cacheFile := prov.cacheFilePath("app-misc", "hello")
	info, err := os.Stat(cacheFile)
	if err != nil {
		t.Fatalf("cache file was not written: %v", err)
	}

	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("cache file mode = %#o, want %#o", got, 0o600)
	}
}
