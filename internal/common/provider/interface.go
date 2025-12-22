package provider

import "errors"

var (
	// ErrNotFound indicates the requested resource was not found
	ErrNotFound = errors.New("package not found in repository")
	// ErrRateLimit indicates API rate limit exceeded
	ErrRateLimit = errors.New("API rate limit exceeded")
	// ErrAPIError indicates a general API error
	ErrAPIError = errors.New("API error")
	// ErrCloneFailed indicates git clone operation failed
	ErrCloneFailed = errors.New("git clone failed")
)

// Provider is the interface for fetching package versions from a repository
type Provider interface {
	// GetPackageVersions returns all ebuild versions for a package
	GetPackageVersions(category, pkg string) ([]string, error)

	// GetName returns a human-readable name for this provider
	GetName() string

	// SupportsAPI returns true if this provider uses an API (vs git clone)
	SupportsAPI() bool

	// Close cleans up any resources (e.g., temporary directories)
	Close() error
}

// RepositoryInfo contains information about a repository to compare against
type RepositoryInfo struct {
	Name     string // e.g., "gentoo", "guru", "my-overlay"
	Provider string // "github", "gitlab", "git"
	URL      string // Full URL or org/repo for GitHub/GitLab
	Token    string // Optional auth token
	Branch   string // Branch to use (default: master/main)
}

// Clone returns a copy of the RepositoryInfo
func (r *RepositoryInfo) Clone() *RepositoryInfo {
	return &RepositoryInfo{
		Name:     r.Name,
		Provider: r.Provider,
		URL:      r.URL,
		Token:    r.Token,
		Branch:   r.Branch,
	}
}

