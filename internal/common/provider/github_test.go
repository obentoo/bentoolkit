package provider

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGitHubProvider_GetPackageVersions(t *testing.T) {
	// Create mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check path
		if r.URL.Path == "/repos/test/repo/contents/app-misc/hello" {
			entries := []ContentEntry{
				{Name: "hello-1.0.ebuild", Type: "file"},
				{Name: "hello-1.1.ebuild", Type: "file"},
				{Name: "hello-2.0.ebuild", Type: "file"},
				{Name: "metadata.xml", Type: "file"},
				{Name: "files", Type: "dir"},
			}
			json.NewEncoder(w).Encode(entries)
			return
		}

		// Package not found
		if r.URL.Path == "/repos/test/repo/contents/app-misc/notfound" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	// Create provider
	repoInfo := &RepositoryInfo{
		Name:     "test",
		Provider: "github",
		URL:      "test/repo",
	}

	prov, err := NewGitHubProvider(repoInfo)
	if err != nil {
		t.Fatalf("NewGitHubProvider failed: %v", err)
	}

	// Override base URL to mock server
	prov.BaseURL = server.URL
	prov.CacheDir = "" // Disable cache for tests

	t.Run("existing package", func(t *testing.T) {
		versions, err := prov.GetPackageVersions("app-misc", "hello")
		if err != nil {
			t.Errorf("GetPackageVersions failed: %v", err)
		}

		if len(versions) != 3 {
			t.Errorf("Expected 3 versions, got %d", len(versions))
		}

		expected := map[string]bool{"1.0": true, "1.1": true, "2.0": true}
		for _, v := range versions {
			if !expected[v] {
				t.Errorf("Unexpected version: %s", v)
			}
		}
	})

	t.Run("package not found", func(t *testing.T) {
		_, err := prov.GetPackageVersions("app-misc", "notfound")
		if err != ErrNotFound {
			t.Errorf("Expected ErrNotFound, got: %v", err)
		}
	})
}

func TestGitHubProvider_Cache(t *testing.T) {
	callCount := 0

	// Create mock server that counts calls
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		entries := []ContentEntry{
			{Name: "hello-1.0.ebuild", Type: "file"},
		}
		json.NewEncoder(w).Encode(entries)
	}))
	defer server.Close()

	// Create temp cache directory
	cacheDir := t.TempDir()

	// Create provider with cache
	repoInfo := &RepositoryInfo{
		Name: "test",
		URL:  "test/repo",
	}

	prov, _ := NewGitHubProvider(repoInfo)
	prov.BaseURL = server.URL
	prov.CacheDir = cacheDir
	prov.CacheTTL = 1 * time.Hour

	// First call should hit server
	_, err := prov.GetPackageVersions("app-misc", "hello")
	if err != nil {
		t.Fatalf("First call failed: %v", err)
	}
	if callCount != 1 {
		t.Errorf("Expected 1 API call, got %d", callCount)
	}

	// Second call should use cache
	_, err = prov.GetPackageVersions("app-misc", "hello")
	if err != nil {
		t.Fatalf("Second call failed: %v", err)
	}
	if callCount != 1 {
		t.Errorf("Expected 1 API call (cached), got %d", callCount)
	}

	// Verify cache file exists
	cacheFile := filepath.Join(cacheDir, "app-misc_hello.json")
	if _, err := os.Stat(cacheFile); os.IsNotExist(err) {
		t.Error("Cache file was not created")
	}
}

func TestGitHubProvider_TokenAuth(t *testing.T) {
	var authHeader string

	// Create mock server that captures auth header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		entries := []ContentEntry{}
		json.NewEncoder(w).Encode(entries)
	}))
	defer server.Close()

	repoInfo := &RepositoryInfo{
		Name:  "test",
		URL:   "test/repo",
		Token: "test-token-123",
	}

	prov, _ := NewGitHubProvider(repoInfo)
	prov.BaseURL = server.URL
	prov.CacheDir = ""

	_, _ = prov.GetPackageVersions("app-misc", "hello")

	if authHeader != "Bearer test-token-123" {
		t.Errorf("Expected 'Bearer test-token-123', got '%s'", authHeader)
	}
}

func TestExtractVersionsFromEntries(t *testing.T) {
	tests := []struct {
		name     string
		entries  []ContentEntry
		pkg      string
		expected []string
	}{
		{
			name: "normal versions",
			entries: []ContentEntry{
				{Name: "hello-1.0.ebuild", Type: "file"},
				{Name: "hello-2.0.ebuild", Type: "file"},
				{Name: "hello-2.0_rc1.ebuild", Type: "file"},
			},
			pkg:      "hello",
			expected: []string{"1.0", "2.0", "2.0_rc1"},
		},
		{
			name: "with directories",
			entries: []ContentEntry{
				{Name: "hello-1.0.ebuild", Type: "file"},
				{Name: "files", Type: "dir"},
				{Name: "metadata.xml", Type: "file"},
			},
			pkg:      "hello",
			expected: []string{"1.0"},
		},
		{
			name: "complex version",
			entries: []ContentEntry{
				{Name: "vscode-1.107.1.ebuild", Type: "file"},
				{Name: "vscode-1.107.1-r1.ebuild", Type: "file"},
			},
			pkg:      "vscode",
			expected: []string{"1.107.1", "1.107.1-r1"},
		},
		{
			name: "different package names",
			entries: []ContentEntry{
				{Name: "vim-9.0.ebuild", Type: "file"},
				{Name: "vim-core-9.0.ebuild", Type: "file"},
			},
			pkg:      "vim",
			expected: []string{"9.0"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			versions := extractVersionsFromEntries(tc.entries, tc.pkg)

			if len(versions) != len(tc.expected) {
				t.Errorf("Expected %d versions, got %d: %v", len(tc.expected), len(versions), versions)
				return
			}

			for i, v := range versions {
				if v != tc.expected[i] {
					t.Errorf("Version %d: expected %s, got %s", i, tc.expected[i], v)
				}
			}
		})
	}
}

// TestGitHubProvider_GetName tests GetName returns expected string
func TestGitHubProvider_GetName(t *testing.T) {
	repoInfo := &RepositoryInfo{Name: "gentoo", URL: "gentoo/gentoo"}
	prov, _ := NewGitHubProvider(repoInfo)
	name := prov.GetName()
	if name == "" {
		t.Error("GetName should return non-empty string")
	}
}

// TestGitHubProvider_Close tests Close returns nil
func TestGitHubProvider_Close(t *testing.T) {
	repoInfo := &RepositoryInfo{Name: "test", URL: "test/repo"}
	prov, _ := NewGitHubProvider(repoInfo)
	if err := prov.Close(); err != nil {
		t.Errorf("Close should return nil, got %v", err)
	}
}

// TestGitHubProvider_SetCacheDir tests SetCacheDir with valid and tilde paths
func TestGitHubProvider_SetCacheDir(t *testing.T) {
	repoInfo := &RepositoryInfo{Name: "test", URL: "test/repo"}
	prov, _ := NewGitHubProvider(repoInfo)

	dir := t.TempDir()
	if err := prov.SetCacheDir(dir); err != nil {
		t.Errorf("SetCacheDir failed: %v", err)
	}
	if prov.CacheDir != dir {
		t.Errorf("Expected CacheDir %s, got %s", dir, prov.CacheDir)
	}
}

// TestGitHubProvider_GetRateLimitInfo tests GetRateLimitInfo with mock server
func TestGitHubProvider_GetRateLimitInfo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"resources":{"core":{"remaining":59,"reset":1700000000}}}`))
	}))
	defer server.Close()

	repoInfo := &RepositoryInfo{Name: "test", URL: "test/repo"}
	prov, _ := NewGitHubProvider(repoInfo)
	prov.BaseURL = server.URL

	remaining, resetTime, err := prov.GetRateLimitInfo()
	if err != nil {
		t.Fatalf("GetRateLimitInfo failed: %v", err)
	}
	if remaining != 59 {
		t.Errorf("Expected remaining=59, got %d", remaining)
	}
	if resetTime.Unix() != 1700000000 {
		t.Errorf("Expected reset=1700000000, got %d", resetTime.Unix())
	}
}

// TestGitHubProvider_APIError tests non-200/403/404 returns ErrAPIError
func TestGitHubProvider_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("server error"))
	}))
	defer server.Close()

	repoInfo := &RepositoryInfo{Name: "test", URL: "test/repo"}
	prov, _ := NewGitHubProvider(repoInfo)
	prov.BaseURL = server.URL
	prov.CacheDir = ""

	_, err := prov.GetPackageVersions("app-misc", "hello")
	if err == nil {
		t.Fatal("Expected error, got nil")
	}
	if !errors.Is(err, ErrAPIError) {
		t.Errorf("Expected ErrAPIError, got %v", err)
	}
}

// TestGitHubProvider_SetCacheDirTilde tests tilde expansion in SetCacheDir
func TestGitHubProvider_SetCacheDirTilde(t *testing.T) {
	repoInfo := &RepositoryInfo{Name: "test", URL: "test/repo"}
	prov, _ := NewGitHubProvider(repoInfo)

	err := prov.SetCacheDir("~/bentoo-test-cache-tilde")
	// May succeed or fail depending on home dir, but tilde must be expanded
	if err == nil {
		if len(prov.CacheDir) > 0 && prov.CacheDir[0] == '~' {
			t.Error("Tilde was not expanded")
		}
		os.RemoveAll(prov.CacheDir)
	}
}

// TestGitHubProvider_CacheExpiry tests expired cache triggers new API call
func TestGitHubProvider_CacheExpiry(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		entries := []ContentEntry{{Name: "hello-2.0.ebuild", Type: "file"}}
		json.NewEncoder(w).Encode(entries)
	}))
	defer server.Close()

	cacheDir := t.TempDir()
	// Write expired cache
	cacheFile := filepath.Join(cacheDir, "app-misc_hello.json")
	expiredEntry := CacheEntry{
		Versions:  []string{"1.0"},
		Timestamp: time.Now().Add(-48 * time.Hour),
	}
	data, _ := json.Marshal(expiredEntry)
	os.WriteFile(cacheFile, data, 0644)

	repoInfo := &RepositoryInfo{Name: "test", URL: "test/repo"}
	prov, _ := NewGitHubProvider(repoInfo)
	prov.BaseURL = server.URL
	prov.CacheDir = cacheDir
	prov.CacheTTL = 24 * time.Hour

	versions, err := prov.GetPackageVersions("app-misc", "hello")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if callCount != 1 {
		t.Errorf("Expected 1 API call (expired cache), got %d", callCount)
	}
	if len(versions) == 0 || versions[0] != "2.0" {
		t.Errorf("Expected fresh version 2.0, got %v", versions)
	}
}

// TestGitHubProvider_GetRateLimitInfoInvalidJSON tests invalid JSON in GetRateLimitInfo
func TestGitHubProvider_GetRateLimitInfoInvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{invalid`))
	}))
	defer server.Close()

	repoInfo := &RepositoryInfo{Name: "test", URL: "test/repo"}
	prov, _ := NewGitHubProvider(repoInfo)
	prov.BaseURL = server.URL

	_, _, err := prov.GetRateLimitInfo()
	if err == nil {
		t.Fatal("Expected parse error, got nil")
	}
}
