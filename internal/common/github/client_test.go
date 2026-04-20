package github

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewClient(t *testing.T) {
	client := NewClient()

	if client.BaseURL != "https://api.github.com" {
		t.Errorf("Expected BaseURL https://api.github.com, got %s", client.BaseURL)
	}

	if client.Repository != "gentoo/gentoo" {
		t.Errorf("Expected Repository gentoo/gentoo, got %s", client.Repository)
	}

	if client.HTTPClient == nil {
		t.Error("Expected HTTPClient to be set")
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
			name: "single version",
			entries: []ContentEntry{
				{Name: "vscode-1.107.1.ebuild", Type: "file"},
			},
			pkg:      "vscode",
			expected: []string{"1.107.1"},
		},
		{
			name: "multiple versions",
			entries: []ContentEntry{
				{Name: "firefox-128.0.ebuild", Type: "file"},
				{Name: "firefox-129.0.ebuild", Type: "file"},
				{Name: "firefox-130.0_rc1.ebuild", Type: "file"},
			},
			pkg:      "firefox",
			expected: []string{"128.0", "129.0", "130.0_rc1"},
		},
		{
			name: "skip non-ebuild files",
			entries: []ContentEntry{
				{Name: "Manifest", Type: "file"},
				{Name: "metadata.xml", Type: "file"},
				{Name: "files", Type: "dir"},
				{Name: "hello-1.0.ebuild", Type: "file"},
			},
			pkg:      "hello",
			expected: []string{"1.0"},
		},
		{
			name: "skip wrong package name",
			entries: []ContentEntry{
				{Name: "vscode-1.0.ebuild", Type: "file"},
				{Name: "vscode-bin-1.0.ebuild", Type: "file"},
			},
			pkg:      "vscode",
			expected: []string{"1.0"},
		},
		{
			name: "complex versions",
			entries: []ContentEntry{
				{Name: "package-1.0_alpha1.ebuild", Type: "file"},
				{Name: "package-1.0_beta2.ebuild", Type: "file"},
				{Name: "package-1.0_rc1.ebuild", Type: "file"},
				{Name: "package-1.0.ebuild", Type: "file"},
				{Name: "package-1.0-r1.ebuild", Type: "file"},
			},
			pkg:      "package",
			expected: []string{"1.0_alpha1", "1.0_beta2", "1.0_rc1", "1.0", "1.0-r1"},
		},
		{
			name:     "empty entries",
			entries:  []ContentEntry{},
			pkg:      "hello",
			expected: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractVersionsFromEntries(tt.entries, tt.pkg)

			if len(result) != len(tt.expected) {
				t.Errorf("Expected %d versions, got %d: %v", len(tt.expected), len(result), result)
				return
			}

			for i, v := range tt.expected {
				if result[i] != v {
					t.Errorf("Expected version %s at index %d, got %s", v, i, result[i])
				}
			}
		})
	}
}

func TestGetPackageVersions(t *testing.T) {
	// Create mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check path
		expectedPath := "/repos/gentoo/gentoo/contents/app-editors/vscode"
		if r.URL.Path != expectedPath {
			t.Errorf("Expected path %s, got %s", expectedPath, r.URL.Path)
		}

		// Return mock response
		entries := []ContentEntry{
			{Name: "vscode-1.107.1.ebuild", Path: "app-editors/vscode/vscode-1.107.1.ebuild", Type: "file"},
			{Name: "vscode-1.108.0.ebuild", Path: "app-editors/vscode/vscode-1.108.0.ebuild", Type: "file"},
			{Name: "Manifest", Path: "app-editors/vscode/Manifest", Type: "file"},
			{Name: "metadata.xml", Path: "app-editors/vscode/metadata.xml", Type: "file"},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(entries)
	}))
	defer server.Close()

	// Create client with mock server
	client := NewClient()
	client.BaseURL = server.URL

	versions, err := client.GetPackageVersions("app-editors", "vscode")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if len(versions) != 2 {
		t.Errorf("Expected 2 versions, got %d", len(versions))
	}

	expectedVersions := []string{"1.107.1", "1.108.0"}
	for i, v := range expectedVersions {
		if versions[i] != v {
			t.Errorf("Expected version %s at index %d, got %s", v, i, versions[i])
		}
	}
}

func TestGetPackageVersionsNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	client := NewClient()
	client.BaseURL = server.URL

	_, err := client.GetPackageVersions("app-misc", "nonexistent")
	if err != ErrNotFound {
		t.Errorf("Expected ErrNotFound, got %v", err)
	}
}

func TestGetPackageVersionsRateLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Reset", "1234567890")
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	client := NewClient()
	client.BaseURL = server.URL

	_, err := client.GetPackageVersions("app-misc", "hello")
	if err == nil {
		t.Error("Expected rate limit error")
	}
}

func TestCaching(t *testing.T) {
	// Create temp dir for cache
	tempDir, err := os.MkdirTemp("", "bentoo-test-cache-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		entries := []ContentEntry{
			{Name: "hello-1.0.ebuild", Type: "file"},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(entries)
	}))
	defer server.Close()

	client := NewClient()
	client.BaseURL = server.URL
	client.SetCacheDir(tempDir)

	// First call - should hit server
	versions1, err := client.GetPackageVersions("app-misc", "hello")
	if err != nil {
		t.Fatalf("First call failed: %v", err)
	}

	// Second call - should use cache
	versions2, err := client.GetPackageVersions("app-misc", "hello")
	if err != nil {
		t.Fatalf("Second call failed: %v", err)
	}

	if callCount != 1 {
		t.Errorf("Expected 1 server call, got %d", callCount)
	}

	if len(versions1) != len(versions2) {
		t.Errorf("Cached versions differ from original")
	}

	// Verify cache file exists
	cacheFile := filepath.Join(tempDir, "app-misc_hello.json")
	if _, err := os.Stat(cacheFile); os.IsNotExist(err) {
		t.Error("Cache file not created")
	}
}

func TestCacheExpiry(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "bentoo-test-cache-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create expired cache entry
	cacheFile := filepath.Join(tempDir, "app-misc_hello.json")
	entry := CacheEntry{
		Versions:  []string{"0.9"},
		Timestamp: time.Now().Add(-48 * time.Hour), // 48 hours ago
	}
	data, _ := json.Marshal(entry)
	os.WriteFile(cacheFile, data, 0644)

	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		entries := []ContentEntry{
			{Name: "hello-1.0.ebuild", Type: "file"},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(entries)
	}))
	defer server.Close()

	client := NewClient()
	client.BaseURL = server.URL
	client.SetCacheDir(tempDir)
	client.CacheTTL = 24 * time.Hour

	// Should hit server because cache is expired
	versions, err := client.GetPackageVersions("app-misc", "hello")
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}

	if callCount != 1 {
		t.Errorf("Expected 1 server call (cache expired), got %d", callCount)
	}

	if versions[0] != "1.0" {
		t.Errorf("Expected fresh version 1.0, got %s", versions[0])
	}
}

func TestClearCache(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "bentoo-test-cache-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create a cache file
	cacheFile := filepath.Join(tempDir, "test.json")
	os.WriteFile(cacheFile, []byte("{}"), 0644)

	client := NewClient()
	client.CacheDir = tempDir

	if err := client.ClearCache(); err != nil {
		t.Fatalf("ClearCache failed: %v", err)
	}

	if _, err := os.Stat(tempDir); !os.IsNotExist(err) {
		t.Error("Cache directory should be removed")
	}
}

// TestNewClientWithOptions tests Req 7.1: NewClientWithOptions with custom parameters
func TestNewClientWithOptions(t *testing.T) {
	repo := "myorg/myrepo"
	cacheDir := t.TempDir()
	timeout := 10 * time.Second

	client := NewClientWithOptions(repo, cacheDir, timeout)

	if client.Repository != repo {
		t.Errorf("Expected Repository %s, got %s", repo, client.Repository)
	}
	if client.CacheDir != cacheDir {
		t.Errorf("Expected CacheDir %s, got %s", cacheDir, client.CacheDir)
	}
	if client.HTTPClient.Timeout != timeout {
		t.Errorf("Expected Timeout %v, got %v", timeout, client.HTTPClient.Timeout)
	}
}

// TestNewClientWithOptionsDefaults tests that empty/zero values use defaults
func TestNewClientWithOptionsDefaults(t *testing.T) {
	client := NewClientWithOptions("", "", 0)

	if client.Repository != "gentoo/gentoo" {
		t.Errorf("Expected default Repository gentoo/gentoo, got %s", client.Repository)
	}
	if client.CacheDir != "" {
		t.Errorf("Expected empty CacheDir, got %s", client.CacheDir)
	}
	if client.HTTPClient.Timeout != 30*time.Second {
		t.Errorf("Expected default Timeout 30s, got %v", client.HTTPClient.Timeout)
	}
}

// TestSetCacheDirTildeExpansion tests Req 7.2: tilde expansion in SetCacheDir
func TestSetCacheDirTildeExpansion(t *testing.T) {
	client := NewClient()

	err := client.SetCacheDir("~/some/cache/path")
	if err != nil {
		// May fail if dir can't be created, but tilde should be expanded
		// Check that CacheDir doesn't start with ~
		if len(client.CacheDir) > 0 && client.CacheDir[0] == '~' {
			t.Error("Tilde was not expanded in CacheDir")
		}
		return
	}

	if len(client.CacheDir) > 0 && client.CacheDir[0] == '~' {
		t.Error("Tilde was not expanded in CacheDir")
	}
	if client.CacheDir == "" {
		t.Error("CacheDir should not be empty after SetCacheDir")
	}

	// Cleanup
	os.RemoveAll(client.CacheDir)
}

// TestSetCacheDirEmpty tests Req 7.3: empty string is a no-op
func TestSetCacheDirEmpty(t *testing.T) {
	client := NewClient()
	client.CacheDir = "/existing/path"

	err := client.SetCacheDir("")
	if err != nil {
		t.Errorf("Expected nil error for empty string, got %v", err)
	}
	// CacheDir should remain unchanged
	if client.CacheDir != "/existing/path" {
		t.Errorf("CacheDir should not change for empty input, got %s", client.CacheDir)
	}
}

// TestGetPackageVersionsCacheDisabled tests Req 7.7: every call hits API when cache is disabled
func TestGetPackageVersionsCacheDisabled(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		entries := []ContentEntry{
			{Name: "hello-1.0.ebuild", Type: "file"},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(entries)
	}))
	defer server.Close()

	// Client with no CacheDir = caching disabled
	client := NewClient()
	client.BaseURL = server.URL
	// CacheDir is empty by default — no caching

	_, err := client.GetPackageVersions("app-misc", "hello")
	if err != nil {
		t.Fatalf("First call failed: %v", err)
	}
	_, err = client.GetPackageVersions("app-misc", "hello")
	if err != nil {
		t.Fatalf("Second call failed: %v", err)
	}

	if callCount != 2 {
		t.Errorf("Expected 2 API calls (cache disabled), got %d", callCount)
	}
}

// TestGetPackageVersionsAPIError tests Req 7.4: non-200/403/404 returns ErrAPIError
func TestGetPackageVersionsAPIError(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
	}{
		{"internal server error", http.StatusInternalServerError},
		{"service unavailable", http.StatusServiceUnavailable},
		{"bad gateway", http.StatusBadGateway},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				w.Write([]byte("error body"))
			}))
			defer server.Close()

			client := NewClient()
			client.BaseURL = server.URL

			_, err := client.GetPackageVersions("app-misc", "hello")
			if err == nil {
				t.Fatal("Expected error, got nil")
			}
			if !errors.Is(err, ErrAPIError) {
				t.Errorf("Expected ErrAPIError, got %v", err)
			}
		})
	}
}

// TestGetRateLimitInfo tests Req 7.5: parses remaining count and reset time
func TestGetRateLimitInfo(t *testing.T) {
	resetUnix := int64(1700000000)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rate_limit" {
			t.Errorf("Expected /rate_limit, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"resources":{"core":{"remaining":42,"reset":%d}}}`, resetUnix)
	}))
	defer server.Close()

	client := NewClient()
	client.BaseURL = server.URL

	remaining, resetTime, err := client.GetRateLimitInfo()
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if remaining != 42 {
		t.Errorf("Expected remaining=42, got %d", remaining)
	}
	if resetTime.Unix() != resetUnix {
		t.Errorf("Expected resetTime unix=%d, got %d", resetUnix, resetTime.Unix())
	}
}

// TestGetRateLimitInfoInvalidJSON tests Req 7.6: invalid JSON returns parse error
func TestGetRateLimitInfoInvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{invalid json`))
	}))
	defer server.Close()

	client := NewClient()
	client.BaseURL = server.URL

	_, _, err := client.GetRateLimitInfo()
	if err == nil {
		t.Fatal("Expected parse error, got nil")
	}
}

// TestGetPackageVersionsInvalidJSON tests Req 7.6: invalid JSON in GetPackageVersions
func TestGetPackageVersionsInvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[invalid json`))
	}))
	defer server.Close()

	client := NewClient()
	client.BaseURL = server.URL

	_, err := client.GetPackageVersions("app-misc", "hello")
	if err == nil {
		t.Fatal("Expected parse error, got nil")
	}
}
