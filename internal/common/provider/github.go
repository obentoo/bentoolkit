package provider

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// GitHubProvider fetches package versions from GitHub API
type GitHubProvider struct {
	BaseURL    string
	Repository string // e.g., "gentoo/gentoo"
	UserAgent  string
	Token      string
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

// NewGitHubProvider creates a new GitHub API provider
func NewGitHubProvider(repoInfo *RepositoryInfo) (*GitHubProvider, error) {
	p := &GitHubProvider{
		BaseURL:    "https://api.github.com",
		Repository: repoInfo.URL,
		UserAgent:  "bentoolkit/1.0",
		Token:      repoInfo.Token,
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		CacheTTL: 24 * time.Hour,
	}

	// Setup default cache directory
	home, err := os.UserHomeDir()
	if err == nil {
		p.CacheDir = filepath.Join(home, ".cache", "bentoo", "compare", "github", strings.ReplaceAll(repoInfo.URL, "/", "_"))
		os.MkdirAll(p.CacheDir, 0755)
	}

	return p, nil
}

// GetName returns the provider name
func (p *GitHubProvider) GetName() string {
	return fmt.Sprintf("GitHub API (%s)", p.Repository)
}

// SupportsAPI returns true
func (p *GitHubProvider) SupportsAPI() bool {
	return true
}

// Close cleans up resources
func (p *GitHubProvider) Close() error {
	return nil
}

// SetCacheDir sets the cache directory for API responses
func (p *GitHubProvider) SetCacheDir(dir string) error {
	if dir == "" {
		return nil
	}
	if strings.HasPrefix(dir, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		dir = filepath.Join(home, dir[1:])
	}
	p.CacheDir = dir
	return os.MkdirAll(dir, 0755)
}

// GetPackageVersions fetches all ebuild versions for a package from GitHub
func (p *GitHubProvider) GetPackageVersions(category, pkg string) ([]string, error) {
	// Check cache first
	if p.CacheDir != "" {
		if versions, ok := p.loadFromCache(category, pkg); ok {
			return versions, nil
		}
	}

	// Fetch from API
	versions, err := p.fetchPackageVersions(category, pkg)
	if err != nil {
		return nil, err
	}

	// Save to cache
	if p.CacheDir != "" {
		p.saveToCache(category, pkg, versions)
	}

	return versions, nil
}

// fetchPackageVersions fetches versions from GitHub API
func (p *GitHubProvider) fetchPackageVersions(category, pkg string) ([]string, error) {
	url := fmt.Sprintf("%s/repos/%s/contents/%s/%s", p.BaseURL, p.Repository, category, pkg)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("User-Agent", p.UserAgent)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	if p.Token != "" {
		req.Header.Set("Authorization", "Bearer "+p.Token)
	}

	resp, err := p.HTTPClient.Do(req)
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

		matches := ebuildVersionRegex.FindStringSubmatch(entry.Name)
		if matches == nil {
			continue
		}

		name := matches[1]
		version := matches[2]

		if name == pkg {
			versions = append(versions, version)
		}
	}

	return versions
}

// cacheFilePath returns the cache file path for a package
func (p *GitHubProvider) cacheFilePath(category, pkg string) string {
	return filepath.Join(p.CacheDir, fmt.Sprintf("%s_%s.json", category, pkg))
}

// loadFromCache attempts to load versions from cache
func (p *GitHubProvider) loadFromCache(category, pkg string) ([]string, bool) {
	if p.CacheDir == "" {
		return nil, false
	}

	cacheFile := p.cacheFilePath(category, pkg)
	data, err := os.ReadFile(cacheFile)
	if err != nil {
		return nil, false
	}

	var entry CacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, false
	}

	if time.Since(entry.Timestamp) > p.CacheTTL {
		return nil, false
	}

	return entry.Versions, true
}

// saveToCache saves versions to cache
func (p *GitHubProvider) saveToCache(category, pkg string, versions []string) {
	if p.CacheDir == "" {
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

	cacheFile := p.cacheFilePath(category, pkg)
	_ = os.WriteFile(cacheFile, data, 0644)
}

// GetRateLimitInfo returns current rate limit status
func (p *GitHubProvider) GetRateLimitInfo() (remaining int, resetTime time.Time, err error) {
	url := fmt.Sprintf("%s/rate_limit", p.BaseURL)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return 0, time.Time{}, err
	}

	req.Header.Set("User-Agent", p.UserAgent)

	if p.Token != "" {
		req.Header.Set("Authorization", "Bearer "+p.Token)
	}

	resp, err := p.HTTPClient.Do(req)
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

// Ensure GitHubProvider implements Provider interface
var _ Provider = (*GitHubProvider)(nil)

