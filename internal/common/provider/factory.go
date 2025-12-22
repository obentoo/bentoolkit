package provider

import (
	"errors"
	"fmt"
)

var (
	// ErrRepositoryNotFound indicates the repository was not found
	ErrRepositoryNotFound = errors.New("repository not found")
	// ErrInvalidProvider indicates an invalid provider type
	ErrInvalidProvider = errors.New("invalid provider type")
)

// BuiltinRepositories contains pre-configured repositories
var BuiltinRepositories = map[string]*RepositoryInfo{
	"gentoo": {
		Name:     "gentoo",
		Provider: "github",
		URL:      "gentoo/gentoo",
		Branch:   "master",
	},
	"guru": {
		Name:     "guru",
		Provider: "github",
		URL:      "gentoo/guru",
		Branch:   "master",
	},
}

// NewProvider creates a new Provider based on the repository info
// If forceClone is true, always use git clone instead of API
func NewProvider(repoInfo *RepositoryInfo, forceClone bool) (Provider, error) {
	if repoInfo == nil {
		return nil, ErrRepositoryNotFound
	}

	// If --clone flag is used, always use git clone
	if forceClone {
		return NewGitCloneProvider(repoInfo)
	}

	// Auto-detect provider based on repository configuration
	switch repoInfo.Provider {
	case "github":
		return NewGitHubProvider(repoInfo)
	case "gitlab":
		return NewGitLabProvider(repoInfo)
	case "git", "":
		// Default to git clone for generic or unspecified providers
		return NewGitCloneProvider(repoInfo)
	default:
		return nil, fmt.Errorf("%w: %s", ErrInvalidProvider, repoInfo.Provider)
	}
}

// ResolveRepository resolves a repository name to its full info
// It first checks built-in repositories, then config-defined repositories
func ResolveRepository(name string, configRepos map[string]*RepositoryInfo) (*RepositoryInfo, error) {
	// Check built-in repositories first
	if repo, ok := BuiltinRepositories[name]; ok {
		return repo.Clone(), nil
	}

	// Check config-defined repositories
	if configRepos != nil {
		if repo, ok := configRepos[name]; ok {
			return repo.Clone(), nil
		}
	}

	return nil, fmt.Errorf("%w: %s", ErrRepositoryNotFound, name)
}

// ListAvailableRepositories returns a list of all available repository names
func ListAvailableRepositories(configRepos map[string]*RepositoryInfo) []string {
	repos := make([]string, 0, len(BuiltinRepositories)+len(configRepos))

	// Add built-in repos
	for name := range BuiltinRepositories {
		repos = append(repos, name)
	}

	// Add config repos
	for name := range configRepos {
		// Skip if already in built-in (built-in takes precedence)
		if _, ok := BuiltinRepositories[name]; !ok {
			repos = append(repos, name)
		}
	}

	return repos
}

