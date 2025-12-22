package github

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var (
	// ErrRateLimit indicates GitHub API rate limit exceeded
	ErrRateLimit = errors.New("GitHub API rate limit exceeded")
	// ErrNotFound indicates the requested resource was not found
	ErrNotFound = errors.New("package not found in repository")
	// ErrAPIError indicates a general GitHub API error
	ErrAPIError = errors.New("GitHub API error")
)

// Client handles communication with the GitHub API
type Client struct {
	BaseURL    string
	Repository string
	UserAgent  string
	Token      string // GitHub personal access token (optional, increases rate limit)
	HTTPClient *http.Client
	CacheDir   string
	CacheTTL   time.Duration
}

// ContentEntry represents a file/directory entry from GitHub Contents API
type ContentEntry struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Type        string `json:"type"` // "file" or "dir"
	DownloadURL string `json:"download_url,omitempty"`
}

// CacheEntry represents a cached API response
type CacheEntry struct {
	Versions  []string  `json:"versions"`
	Timestamp time.Time `json:"timestamp"`
}

// NewClient creates a new GitHub API client
func NewClient() *Client {
	return &Client{
		BaseURL:    "https://api.github.com",
		Repository: "gentoo/gentoo",
		UserAgent:  "bentoolkit/1.0",
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		CacheTTL: 24 * time.Hour,
	}
}

// NewClientWithOptions creates a new GitHub API client with custom options
func NewClientWithOptions(repository string, cacheDir string, timeout time.Duration) *Client {
	client := NewClient()
	if repository != "" {
		client.Repository = repository
	}
	if cacheDir != "" {
		client.CacheDir = cacheDir
	}
	if timeout > 0 {
		client.HTTPClient.Timeout = timeout
	}
	return client
}

// SetCacheDir sets the cache directory for API responses
func (c *Client) SetCacheDir(dir string) error {
	if dir == "" {
		return nil
	}
	// Expand home directory
	if strings.HasPrefix(dir, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		dir = filepath.Join(home, dir[1:])
	}
	c.CacheDir = dir
	return os.MkdirAll(dir, 0755)
}

// GetPackageVersions fetches all ebuild versions for a package from GitHub
func (c *Client) GetPackageVersions(category, pkg string) ([]string, error) {
	// Check cache first
	if c.CacheDir != "" {
		if versions, ok := c.loadFromCache(category, pkg); ok {
			return versions, nil
		}
	}

	// Fetch from API
	versions, err := c.fetchPackageVersions(category, pkg)
	if err != nil {
		return nil, err
	}

	// Save to cache
	if c.CacheDir != "" {
		c.saveToCache(category, pkg, versions)
	}

	return versions, nil
}

// fetchPackageVersions fetches versions from GitHub API
func (c *Client) fetchPackageVersions(category, pkg string) ([]string, error) {
	url := fmt.Sprintf("%s/repos/%s/contents/%s/%s", c.BaseURL, c.Repository, category, pkg)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("User-Agent", c.UserAgent)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	// Add authorization header if token is set
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Handle rate limiting
	if resp.StatusCode == 403 {
		resetHeader := resp.Header.Get("X-RateLimit-Reset")
		return nil, fmt.Errorf("%w: rate limit resets at %s", ErrRateLimit, resetHeader)
	}

	// Handle not found
	if resp.StatusCode == 404 {
		return nil, ErrNotFound
	}

	// Handle other errors
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("%w: status %d: %s", ErrAPIError, resp.StatusCode, string(body))
	}

	// Parse response
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var entries []ContentEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		return nil, fmt.Errorf("failed to parse GitHub response: %w", err)
	}

	// Extract versions from ebuild filenames
	versions := extractVersionsFromEntries(entries, pkg)

	return versions, nil
}

// ebuildVersionRegex matches package-version.ebuild format
var ebuildVersionRegex = regexp.MustCompile(`^(.+)-(\d+[\d.]*[\w._-]*)\.ebuild$`)

// extractVersionsFromEntries extracts version strings from ContentEntry list
func extractVersionsFromEntries(entries []ContentEntry, pkg string) []string {
	var versions []string

	for _, entry := range entries {
		if entry.Type != "file" {
			continue
		}
		if !strings.HasSuffix(entry.Name, ".ebuild") {
			continue
		}

		// Parse version from filename
		matches := ebuildVersionRegex.FindStringSubmatch(entry.Name)
		if matches == nil {
			continue
		}

		// Validate package name matches
		name := matches[1]
		version := matches[2]

		if name == pkg {
			versions = append(versions, version)
		}
	}

	return versions
}

// cacheFilePath returns the cache file path for a package
func (c *Client) cacheFilePath(category, pkg string) string {
	return filepath.Join(c.CacheDir, fmt.Sprintf("%s_%s.json", category, pkg))
}

// loadFromCache attempts to load versions from cache
func (c *Client) loadFromCache(category, pkg string) ([]string, bool) {
	if c.CacheDir == "" {
		return nil, false
	}

	cacheFile := c.cacheFilePath(category, pkg)
	data, err := os.ReadFile(cacheFile)
	if err != nil {
		return nil, false
	}

	var entry CacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, false
	}

	// Check if cache is still valid
	if time.Since(entry.Timestamp) > c.CacheTTL {
		return nil, false
	}

	return entry.Versions, true
}

// saveToCache saves versions to cache
func (c *Client) saveToCache(category, pkg string, versions []string) {
	if c.CacheDir == "" {
		return
	}

	entry := CacheEntry{
		Versions:  versions,
		Timestamp: time.Now(),
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return
	}

	cacheFile := c.cacheFilePath(category, pkg)
	_ = os.WriteFile(cacheFile, data, 0644)
}

// ClearCache removes all cached data
func (c *Client) ClearCache() error {
	if c.CacheDir == "" {
		return nil
	}
	return os.RemoveAll(c.CacheDir)
}

// GetRateLimitInfo returns current rate limit status
func (c *Client) GetRateLimitInfo() (remaining int, resetTime time.Time, err error) {
	url := fmt.Sprintf("%s/rate_limit", c.BaseURL)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return 0, time.Time{}, err
	}

	req.Header.Set("User-Agent", c.UserAgent)

	// Add authorization header if token is set
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return 0, time.Time{}, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, time.Time{}, err
	}

	var result struct {
		Resources struct {
			Core struct {
				Remaining int   `json:"remaining"`
				Reset     int64 `json:"reset"`
			} `json:"core"`
		} `json:"resources"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return 0, time.Time{}, err
	}

	resetTime = time.Unix(result.Resources.Core.Reset, 0)
	return result.Resources.Core.Remaining, resetTime, nil
}

