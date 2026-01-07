package provider

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// GitLabProvider fetches package versions from GitLab API
type GitLabProvider struct {
	BaseURL    string // e.g., "https://gitlab.com" or "https://gitlab.gentoo.org"
	ProjectID  string // URL-encoded project path or numeric ID
	Token      string
	UserAgent  string
	HTTPClient *http.Client
	CacheDir   string
	CacheTTL   time.Duration
}

// GitLabTreeEntry represents a file/directory entry from GitLab Repository Tree API
type GitLabTreeEntry struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"` // "blob" or "tree"
	Path string `json:"path"`
	Mode string `json:"mode"`
}

// NewGitLabProvider creates a new GitLab API provider
func NewGitLabProvider(repoInfo *RepositoryInfo) (*GitLabProvider, error) {
	// Parse URL to extract base URL and project path
	baseURL, projectID, err := parseGitLabURL(repoInfo.URL)
	if err != nil {
		return nil, err
	}

	p := &GitLabProvider{
		BaseURL:   baseURL,
		ProjectID: url.PathEscape(projectID),
		Token:     repoInfo.Token,
		UserAgent: "bentoolkit/1.0",
		HTTPClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		CacheTTL: 24 * time.Hour,
	}

	// Setup default cache directory
	home, err := os.UserHomeDir()
	if err == nil {
		safeName := strings.ReplaceAll(projectID, "/", "_")
		p.CacheDir = filepath.Join(home, ".cache", "bentoo", "compare", "gitlab", safeName)
		os.MkdirAll(p.CacheDir, 0755)
	}

	return p, nil
}

// parseGitLabURL parses a GitLab URL into base URL and project path
// Supports formats:
// - https://gitlab.com/group/project
// - https://gitlab.gentoo.org/repo/gentoo
// - group/project (assumes gitlab.com)
func parseGitLabURL(rawURL string) (baseURL, projectPath string, err error) {
	// If it's just a path like "group/project", assume gitlab.com
	if !strings.Contains(rawURL, "://") {
		return "https://gitlab.com", rawURL, nil
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", "", err
	}

	baseURL = fmt.Sprintf("%s://%s", parsed.Scheme, parsed.Host)
	projectPath = strings.TrimPrefix(parsed.Path, "/")
	projectPath = strings.TrimSuffix(projectPath, ".git")

	return baseURL, projectPath, nil
}

// GetName returns the provider name
func (p *GitLabProvider) GetName() string {
	return fmt.Sprintf("GitLab API (%s)", p.ProjectID)
}

// SupportsAPI returns true
func (p *GitLabProvider) SupportsAPI() bool {
	return true
}

// Close cleans up resources
func (p *GitLabProvider) Close() error {
	return nil
}

// GetPackageVersions fetches all ebuild versions for a package from GitLab
func (p *GitLabProvider) GetPackageVersions(category, pkg string) ([]string, error) {
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

// fetchPackageVersions fetches versions from GitLab API
func (p *GitLabProvider) fetchPackageVersions(category, pkg string) ([]string, error) {
	// GitLab Repository Tree API
	// GET /api/v4/projects/:id/repository/tree?path=category/package
	path := url.QueryEscape(fmt.Sprintf("%s/%s", category, pkg))
	apiURL := fmt.Sprintf("%s/api/v4/projects/%s/repository/tree?path=%s&per_page=100",
		p.BaseURL, p.ProjectID, path)

	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("User-Agent", p.UserAgent)

	// GitLab uses PRIVATE-TOKEN header for authentication
	if p.Token != "" {
		req.Header.Set("PRIVATE-TOKEN", p.Token)
	}

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Handle rate limiting
	if resp.StatusCode == 429 {
		return nil, ErrRateLimit
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

	var entries []GitLabTreeEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		return nil, fmt.Errorf("failed to parse GitLab response: %w", err)
	}

	// Extract versions from ebuild filenames
	versions := p.extractVersionsFromEntries(entries, pkg)

	return versions, nil
}

// extractVersionsFromEntries extracts version strings from GitLabTreeEntry list
func (p *GitLabProvider) extractVersionsFromEntries(entries []GitLabTreeEntry, pkg string) []string {
	var versions []string

	for _, entry := range entries {
		if entry.Type != "blob" {
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
func (p *GitLabProvider) cacheFilePath(category, pkg string) string {
	return filepath.Join(p.CacheDir, fmt.Sprintf("%s_%s.json", category, pkg))
}

// loadFromCache attempts to load versions from cache
func (p *GitLabProvider) loadFromCache(category, pkg string) ([]string, bool) {
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
func (p *GitLabProvider) saveToCache(category, pkg string, versions []string) {
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

// Ensure GitLabProvider implements Provider interface
var _ Provider = (*GitLabProvider)(nil)
