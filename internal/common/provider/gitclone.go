package provider

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// GitCloneProvider fetches package versions by cloning a git repository
type GitCloneProvider struct {
	RepoURL   string
	LocalPath string
	Branch    string
	RepoName  string

	// UpdateInterval is how often to pull updates (default: 24h)
	UpdateInterval time.Duration
}

// NewGitCloneProvider creates a new git clone provider
func NewGitCloneProvider(repoInfo *RepositoryInfo) (*GitCloneProvider, error) {
	// Determine the git URL
	gitURL := repoInfo.URL
	if !strings.Contains(gitURL, "://") && !strings.Contains(gitURL, "@") {
		// Assume GitHub if just org/repo format
		gitURL = fmt.Sprintf("https://github.com/%s.git", repoInfo.URL)
	}

	// Setup cache directory
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}

	safeName := strings.ReplaceAll(repoInfo.Name, "/", "_")
	localPath := filepath.Join(home, ".cache", "bentoo", "repos", safeName)

	branch := repoInfo.Branch
	if branch == "" {
		branch = "master"
	}

	return &GitCloneProvider{
		RepoURL:        gitURL,
		LocalPath:      localPath,
		Branch:         branch,
		RepoName:       repoInfo.Name,
		UpdateInterval: 24 * time.Hour,
	}, nil
}

// GetName returns the provider name
func (p *GitCloneProvider) GetName() string {
	return fmt.Sprintf("Git Clone (%s)", p.RepoName)
}

// SupportsAPI returns false - this provider uses git clone, not API
func (p *GitCloneProvider) SupportsAPI() bool {
	return false
}

// Close cleans up resources (nothing to clean for git clone)
func (p *GitCloneProvider) Close() error {
	return nil
}

// GetPackageVersions returns all ebuild versions for a package
func (p *GitCloneProvider) GetPackageVersions(category, pkg string) ([]string, error) {
	// Ensure repo is cloned/updated
	if err := p.ensureRepo(); err != nil {
		return nil, err
	}

	// Scan local directory for ebuilds
	pkgPath := filepath.Join(p.LocalPath, category, pkg)
	return p.scanLocalPackage(pkgPath, pkg)
}

// ensureRepo ensures the repository is cloned and up-to-date
func (p *GitCloneProvider) ensureRepo() error {
	if p.repoExists() {
		// Check if we need to update
		if p.needsUpdate() {
			return p.updateRepo()
		}
		return nil
	}

	// Clone the repository
	return p.cloneRepo()
}

// repoExists checks if the local repository exists
func (p *GitCloneProvider) repoExists() bool {
	gitDir := filepath.Join(p.LocalPath, ".git")
	info, err := os.Stat(gitDir)
	return err == nil && info.IsDir()
}

// needsUpdate checks if the repository needs to be updated
func (p *GitCloneProvider) needsUpdate() bool {
	// Check modification time of .git/FETCH_HEAD
	fetchHead := filepath.Join(p.LocalPath, ".git", "FETCH_HEAD")
	info, err := os.Stat(fetchHead)
	if err != nil {
		// If FETCH_HEAD doesn't exist, we should update
		return true
	}

	return time.Since(info.ModTime()) > p.UpdateInterval
}

// cloneRepo clones the repository
func (p *GitCloneProvider) cloneRepo() error {
	// Ensure parent directory exists
	parentDir := filepath.Dir(p.LocalPath)
	if err := os.MkdirAll(parentDir, 0755); err != nil {
		return fmt.Errorf("failed to create cache directory: %w", err)
	}

	// Clone with depth 1 for faster clone (we only need latest files)
	cmd := exec.Command("git", "clone",
		"--depth", "1",
		"--single-branch",
		"--branch", p.Branch,
		p.RepoURL,
		p.LocalPath,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s: %s", ErrCloneFailed, err.Error(), string(output))
	}

	return nil
}

// updateRepo updates the repository
func (p *GitCloneProvider) updateRepo() error {
	cmd := exec.Command("git", "-C", p.LocalPath, "pull", "--ff-only")
	output, err := cmd.CombinedOutput()
	if err != nil {
		// If pull fails, try a fetch + reset
		fetchCmd := exec.Command("git", "-C", p.LocalPath, "fetch", "origin", p.Branch)
		if fetchErr := fetchCmd.Run(); fetchErr != nil {
			return fmt.Errorf("failed to update repository: %s: %s", err.Error(), string(output))
		}

		resetCmd := exec.Command("git", "-C", p.LocalPath, "reset", "--hard", "origin/"+p.Branch)
		if resetErr := resetCmd.Run(); resetErr != nil {
			return fmt.Errorf("failed to reset repository: %v", resetErr)
		}
	}

	return nil
}

// scanLocalPackage scans a local package directory for ebuild versions
func (p *GitCloneProvider) scanLocalPackage(pkgPath, pkgName string) ([]string, error) {
	entries, err := os.ReadDir(pkgPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	var versions []string

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		filename := entry.Name()
		if !strings.HasSuffix(filename, ".ebuild") {
			continue
		}

		matches := ebuildVersionRegex.FindStringSubmatch(filename)
		if matches == nil {
			continue
		}

		name := matches[1]
		version := matches[2]

		if name == pkgName {
			versions = append(versions, version)
		}
	}

	return versions, nil
}

// ForceUpdate forces an update of the repository regardless of age
func (p *GitCloneProvider) ForceUpdate() error {
	if !p.repoExists() {
		return p.cloneRepo()
	}
	return p.updateRepo()
}

// RemoveCache removes the cached repository
func (p *GitCloneProvider) RemoveCache() error {
	return os.RemoveAll(p.LocalPath)
}

// Ensure GitCloneProvider implements Provider interface
var _ Provider = (*GitCloneProvider)(nil)

