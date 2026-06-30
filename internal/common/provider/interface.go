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
	// ErrInvalidRepoURL indicates the repository URL has a scheme outside the
	// allowed set {http, https, git, ssh}, or an empty host.
	ErrInvalidRepoURL = errors.New("invalid repository URL")
	// ErrInvalidBranch indicates the git branch name fails git check-ref-format
	// style validation (control chars, "..", "@{", a leading "-", shell
	// metacharacters, and similar unsafe constructs).
	ErrInvalidBranch = errors.New("invalid git branch name")
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

// PackageDirProvider is implemented by providers that expose an on-disk package
// directory (git clone / local tree). The revive flow type-asserts to it; an
// API-only provider simply does not implement it, which is the "API-only" signal.
type PackageDirProvider interface {
	LocalPackagePath(category, pkg string) (string, error)
}

// RepositoryInfo contains information about a repository to compare against
type RepositoryInfo struct {
	Name     string // e.g., "gentoo", "guru", "my-overlay"
	Provider string // "github", "gitlab", "git", "local"
	URL      string // Full URL or org/repo for GitHub/GitLab/git (remote)
	Path     string // On-disk tree for provider "local" (read in place, no clone)
	Token    string // Optional auth token
	Branch   string // Branch to use (default: master/main)
}

// Clone returns a copy of the RepositoryInfo
func (r *RepositoryInfo) Clone() *RepositoryInfo {
	return &RepositoryInfo{
		Name:     r.Name,
		Provider: r.Provider,
		URL:      r.URL,
		Path:     r.Path,
		Token:    r.Token,
		Branch:   r.Branch,
	}
}
