package provider

import (
	"errors"
	"fmt"
	"sort"
)

var (
	// ErrRepositoryNotFound indicates the repository was not found
	ErrRepositoryNotFound = errors.New("repository not found")
	// ErrInvalidProvider indicates an invalid provider type
	ErrInvalidProvider = errors.New("invalid provider type")
)

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

// ResolveRepository resolves a repository name to its full info.
// Priority: config repos > registry > error.
func ResolveRepository(name string, configRepos map[string]*RepositoryInfo, registry *RepositoryRegistry) (*RepositoryInfo, error) {
	if configRepos != nil {
		if repo, ok := configRepos[name]; ok {
			return repo.Clone(), nil
		}
	}

	if registry != nil {
		info, err := registry.Resolve(name)
		if err == nil {
			return info, nil
		}
		if !errors.Is(err, ErrRepositoryNotFound) {
			return nil, err
		}
	}

	return nil, fmt.Errorf("%w: %s", ErrRepositoryNotFound, name)
}

// ListAvailableRepositories returns a sorted list of all available repository names
// from both config and registry sources, deduplicated.
func ListAvailableRepositories(configRepos map[string]*RepositoryInfo, registry *RepositoryRegistry) []string {
	seen := make(map[string]bool)
	var repos []string

	for name := range configRepos {
		if !seen[name] {
			seen[name] = true
			repos = append(repos, name)
		}
	}

	if registry != nil {
		if names, err := registry.List(); err == nil {
			for _, name := range names {
				if !seen[name] {
					seen[name] = true
					repos = append(repos, name)
				}
			}
		}
	}

	sort.Strings(repos)
	return repos
}
