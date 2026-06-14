package provider

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// DefaultGitCloneTimeout is the default timeout applied to a git clone
// operation before it is cancelled.
const DefaultGitCloneTimeout = 2 * time.Minute

// execCommand is an indirection over exec.CommandContext so the git clone
// invocation can be replaced in tests (e.g. the timeout test). Production code
// always uses exec.CommandContext.
var execCommand = exec.CommandContext

// GitCloneProvider fetches package versions by cloning a git repository
type GitCloneProvider struct {
	RepoURL   string
	LocalPath string
	Branch    string
	RepoName  string

	// UpdateInterval is how often to pull updates (default: 24h)
	UpdateInterval time.Duration
}

// NewGitCloneProvider creates a new git clone provider.
//
// The resolved repository URL and branch name are validated before any work is
// done: a malicious scheme (e.g. file://, javascript:) or a branch name that
// enables git flag-injection causes an early error wrapping ErrInvalidRepoURL
// or ErrInvalidBranch respectively. (R2.1, R2.2)
func NewGitCloneProvider(repoInfo *RepositoryInfo) (*GitCloneProvider, error) {
	// Determine the git URL
	gitURL := repoInfo.URL
	if !strings.Contains(gitURL, "://") && !strings.Contains(gitURL, "@") {
		// Assume GitHub if just org/repo format
		gitURL = fmt.Sprintf("https://github.com/%s.git", repoInfo.URL)
	}

	branch := repoInfo.Branch
	if branch == "" {
		branch = "master"
	}

	// Reject malicious repo URLs and branch names before doing any work.
	if err := ValidateRepoURL(gitURL); err != nil {
		return nil, err
	}
	if err := ValidateBranch(branch); err != nil {
		return nil, err
	}

	// Setup cache directory
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}

	safeName := strings.ReplaceAll(repoInfo.Name, "/", "_")
	localPath := filepath.Join(home, ".cache", "bentoo", "repos", safeName)

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

// LocalPackagePath returns the on-disk directory for a package in the cloned
// repository, ensuring the repo is present/up-to-date first. It returns
// ErrNotFound if the package directory does not exist.
func (p *GitCloneProvider) LocalPackagePath(category, pkg string) (string, error) {
	// Ensure repo is cloned/updated
	if err := p.ensureRepo(); err != nil {
		return "", err
	}

	pkgPath := filepath.Join(p.LocalPath, category, pkg)
	if _, err := os.Stat(pkgPath); err != nil {
		if os.IsNotExist(err) {
			return "", ErrNotFound
		}
		return "", err
	}

	return pkgPath, nil
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
	if err := os.MkdirAll(parentDir, 0o750); err != nil {
		return fmt.Errorf("failed to create cache directory: %w", err)
	}

	// Bound the clone with a timeout so a hung or slow remote cannot block
	// indefinitely. T9 will thread a caller-supplied parent context here;
	// until then context.Background() is the parent.
	ctx, cancel := context.WithTimeout(context.Background(), DefaultGitCloneTimeout) // SAFE: pre-T9, parent context will be threaded by T9 (R3)
	defer cancel()

	// Clone with depth 1 for faster clone (we only need latest files).
	// The literal "--" end-of-options separator ensures git can never
	// interpret the positional URL/path as an option, even if it begins
	// with "-" (defense-in-depth against flag-injection; AD-9). The
	// documented syntax is: git clone [<options>] [--] <repo> [<dir>].
	cmd := execCommand(ctx, "git", "clone",
		"--depth", "1",
		"--single-branch",
		"--branch", p.Branch,
		"--",
		p.RepoURL,
		p.LocalPath,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("%w: %v: %s", ErrCloneFailed, ctx.Err(), string(output))
		}
		return fmt.Errorf("%w: %s: %s", ErrCloneFailed, err.Error(), string(output))
	}

	return nil
}

// updateRepo updates the repository.
//
// G204 (gosec) is suppressed on the three exec sites below: the command name
// is always the fixed literal "git"; p.LocalPath is a process-controlled cache
// path (filepath.Join of ~/.cache with a "/"-sanitized repo name set in
// NewGitCloneProvider, never user input); and p.Branch was validated by
// ValidateBranch in NewGitCloneProvider, so it cannot inject a git flag even
// when concatenated into "origin/"+p.Branch.
func (p *GitCloneProvider) updateRepo() error {
	cmd := exec.Command("git", "-C", p.LocalPath, "pull", "--ff-only") //nolint:gosec // G204: fixed "git" command; LocalPath is a controlled cache path
	output, err := cmd.CombinedOutput()
	if err != nil {
		// If pull fails, try a fetch + reset
		fetchCmd := exec.Command("git", "-C", p.LocalPath, "fetch", "origin", p.Branch) //nolint:gosec // G204: fixed "git" command; LocalPath controlled, Branch validated by ValidateBranch
		if fetchErr := fetchCmd.Run(); fetchErr != nil {
			return fmt.Errorf("failed to update repository: %s: %s", err.Error(), string(output))
		}

		resetCmd := exec.Command("git", "-C", p.LocalPath, "reset", "--hard", "origin/"+p.Branch) //nolint:gosec // G204: fixed "git" command; LocalPath controlled, Branch validated by ValidateBranch
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

// Ensure GitCloneProvider implements PackageDirProvider interface
var _ PackageDirProvider = (*GitCloneProvider)(nil)
