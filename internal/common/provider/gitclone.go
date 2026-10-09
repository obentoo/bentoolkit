package provider

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/procgroup"
	"github.com/obentoo/bentoolkit/internal/common/tui"
)

// DefaultGitCloneTimeout bounds each git run the provider makes against its
// remote: a clone, and an update's pull, fetch and reset taken together. When
// it elapses, git and every process it started are stopped, and the error
// wraps context.DeadlineExceeded and reads "git <op> timed out after 5m0s".
const DefaultGitCloneTimeout = 5 * time.Minute

// gitTimeout is the bound cloneRepo and updateRepo apply. It is
// DefaultGitCloneTimeout, except inside this package's own tests, which shorten
// it to watch the bound expire. A caller's short deadline cannot show that:
// then the caller's deadline expired, not the bound.
var gitTimeout = DefaultGitCloneTimeout

// execCommand is an indirection over exec.CommandContext so the git
// invocations can be replaced in tests (e.g. the timeout tests). Production
// code always uses exec.CommandContext. A replacement must build its command
// with exec.CommandContext on the context it receives: gitCommand sets the
// command's Cancel (procgroup.Group), and os/exec refuses to start a command
// that has a Cancel but no context.
var execCommand = exec.CommandContext

// GitCloneProvider fetches package versions by cloning a git repository
type GitCloneProvider struct {
	RepoURL   string
	LocalPath string
	Branch    string
	RepoName  string

	// UpdateInterval is how often to pull updates (default: 24h)
	UpdateInterval time.Duration

	// local marks an on-disk tree used in place (provider "local"): LocalPath
	// already points at an existing directory, so ensureRepo/repoExists are
	// no-ops and the destructive cache operations (RemoveCache/ForceUpdate) are
	// disabled to avoid touching the user's real tree (e.g. /var/db/repos/gentoo).
	local bool

	// reporter receives stage/done/tail events for the clone. It may be nil
	// (this struct is often built as a literal without a constructor default),
	// so cloneRepo nil-guards it locally.
	reporter tui.Reporter
	// taskID identifies this provider's task in reporter events.
	taskID string
}

// Compile-time guarantee that *GitCloneProvider satisfies PackageDirProvider,
// which the revive flow's type assertion (autoupdate.CanRevive,
// internal/autoupdate/revive.go) relies on. Enforcing it here fails the build
// if the interface drifts, so no runtime test is needed.
var _ PackageDirProvider = (*GitCloneProvider)(nil)

// NewLocalProvider builds a provider that reads an on-disk package tree in place
// (provider "local"), without cloning. repoInfo.Path must be an existing
// directory; it is resolved to an absolute path and used as LocalPath directly.
// This is the "local tree" the revive/compare guidance documents — e.g. a
// synced /var/db/repos/gentoo, which has no .git of its own.
func NewLocalProvider(repoInfo *RepositoryInfo) (*GitCloneProvider, error) {
	if strings.TrimSpace(repoInfo.Path) == "" {
		return nil, fmt.Errorf("%w: provider \"local\" requires a non-empty 'path'", ErrInvalidRepoURL)
	}

	abs, err := filepath.Abs(repoInfo.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve local repository path %q: %w", repoInfo.Path, err)
	}

	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("%w: local repository path %q: %w", ErrInvalidRepoURL, repoInfo.Path, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%w: local repository path %q is not a directory", ErrInvalidRepoURL, repoInfo.Path)
	}

	return &GitCloneProvider{
		LocalPath: abs,
		RepoName:  repoInfo.Name,
		local:     true,
	}, nil
}

// SetReporter wires a reporter and task id for clone stage/done/tail events. A
// nil reporter is normalized to tui.Noop().
func (p *GitCloneProvider) SetReporter(r tui.Reporter, id string) {
	if r == nil {
		r = tui.Noop()
	}
	p.reporter = r
	p.taskID = id
}

// NewGitCloneProvider creates a new git clone provider.
//
// The resolved repository URL and branch name are validated before any work is
// done: a malicious scheme (e.g. file://, javascript:) or a branch name that
// enables git flag-injection causes an early error wrapping ErrInvalidRepoURL
// or ErrInvalidBranch respectively.
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

// GetPackageVersions returns all ebuild versions for a package. A ctx that is
// already done returns its error before the repository is touched.
func (p *GitCloneProvider) GetPackageVersions(ctx context.Context, category, pkg string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("git-clone lookup %s/%s: %w", category, pkg, err)
	}

	// Ensure repo is cloned/updated
	if err := p.ensureRepo(ctx); err != nil {
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
	// Ensure repo is cloned/updated. PackageDirProvider carries no context, so
	// none reaches the clone from here.
	if err := p.ensureRepo(context.Background()); err != nil { // SAFE: LocalPackagePath has no ctx parameter; the clone stays bounded by DefaultGitCloneTimeout
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

// ensureRepo ensures the repository is cloned and up-to-date. ctx is the
// parent of the clone's and the update's own timeout, so a cancelled lookup
// stops either one.
func (p *GitCloneProvider) ensureRepo(ctx context.Context) error {
	// Local in-place tree: nothing to clone or update. The directory was
	// validated to exist in NewLocalProvider and may be a non-git rsync tree.
	if p.local {
		return nil
	}

	if p.repoExists() {
		// Check if we need to update
		if p.needsUpdate() {
			return p.updateRepo(ctx)
		}
		return nil
	}

	// Clone the repository
	return p.cloneRepo(ctx)
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

// cloneRepo clones the repository, bounded by gitTimeout under ctx.
func (p *GitCloneProvider) cloneRepo(ctx context.Context) error {
	// Ensure parent directory exists
	parentDir := filepath.Dir(p.LocalPath)
	if err := os.MkdirAll(parentDir, 0o750); err != nil {
		return fmt.Errorf("failed to create cache directory: %w", err)
	}

	// Bound the clone with a timeout so a hung or slow remote cannot block
	// indefinitely; the caller's context is the parent, so cancelling the
	// lookup also stops the clone. git runs in group mode (gitCommand), so the
	// bound stops its transport too.
	parent := ctx
	ctx, cancel := context.WithTimeout(parent, gitTimeout)
	defer cancel()

	// Clone with depth 1 for faster clone (we only need latest files).
	// The literal "--" end-of-options separator ensures git can never
	// interpret the positional URL/path as an option, even if it begins
	// with "-" (defense-in-depth against flag-injection). The
	// documented syntax is: git clone [<options>] [--] <repo> [<dir>].
	// p.Branch needs no separator: it is the value of --branch, which git
	// takes as given and never parses as an option.
	cmd := gitCommand(ctx, "clone",
		"--depth", "1",
		"--single-branch",
		"--branch", p.Branch,
		"--",
		p.RepoURL,
		p.LocalPath,
	)

	// Nil-guard locally: GitCloneProvider is frequently built as a struct
	// literal with no constructor default for reporter.
	rep := p.reporter
	if rep == nil {
		rep = tui.Noop()
	}
	rep.TaskStage(p.taskID, "clone")

	// Stream git's progress (written to stderr) live while still capturing the
	// full combined output verbatim for the error path. Under the default
	// Noop reporter this is byte-identical to the previous CombinedOutput path.
	sc := tui.NewStreamCapture(rep, p.taskID, tui.StreamStdout)
	cmd.Stdout = sc
	cmd.Stderr = sc

	err := procgroup.Result(cmd, cmd.Run())
	_ = sc.Close()

	rep.TaskDone(p.taskID, err == nil, "", "")

	if err != nil {
		// Captured() is already bounded to its last tui.MaxCapturedBytes.
		if stop := interrupted(parent, ctx, "clone"); stop != nil {
			return fmt.Errorf("%w: cloning into %s: %w: %s", ErrCloneFailed, p.LocalPath, stop, sc.Captured())
		}
		return fmt.Errorf("%w: %s: %s", ErrCloneFailed, err.Error(), sc.Captured())
	}

	return nil
}

// updateRepo brings the clone at p.LocalPath up to date: git pull --ff-only,
// and when the pull fails, git fetch plus git reset --hard to the fetched
// branch.
//
// The three commands share ONE context, bounded by DefaultGitCloneTimeout on
// top of ctx, so together they take at most that long, and each runs in group
// mode (gitCommand), so a cancel or the bound stops git together with its
// transport. When ctx is done or the bound elapses, updateRepo
// returns at once with an error naming the git operation that was running and
// wrapping the context's error; it never starts the next command.
//
// p.Branch reaches fetch after --end-of-options, on top of ValidateBranch.
// reset gets "origin/<branch>", which cannot begin with "-", then "--" so it
// is never read as a path; git 2.43 rejects --end-of-options after --hard.
// p.LocalPath is the value of -C, which git never parses as an option.
func (p *GitCloneProvider) updateRepo(ctx context.Context) error {
	runCtx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()

	// stopped returns the error for op when a context ended it, or nil.
	stopped := func(op string) error {
		if stop := interrupted(ctx, runCtx, op); stop != nil {
			return fmt.Errorf("failed to update repository %s: %w", p.LocalPath, stop)
		}
		return nil
	}

	pull := gitCommand(runCtx, "-C", p.LocalPath, "pull", "--ff-only")
	output, err := pull.CombinedOutput()
	if err = procgroup.Result(pull, err); err == nil {
		return nil
	}
	if stop := stopped("pull"); stop != nil {
		return stop
	}

	// The pull failed on its own: try a fetch + reset.
	fetch := gitCommand(runCtx, "-C", p.LocalPath, "fetch", "--end-of-options", "origin", p.Branch)
	if fetchErr := procgroup.Result(fetch, fetch.Run()); fetchErr != nil {
		if stop := stopped("fetch"); stop != nil {
			return stop
		}
		return fmt.Errorf("failed to update repository %s: git pull: %w; git fetch: %w: %s",
			p.LocalPath, err, fetchErr, tui.Tail(string(output)))
	}

	reset := gitCommand(runCtx, "-C", p.LocalPath, "reset", "--hard", "origin/"+p.Branch, "--")
	if resetErr := procgroup.Result(reset, reset.Run()); resetErr != nil {
		if stop := stopped("reset"); stop != nil {
			return stop
		}
		return fmt.Errorf("failed to update repository %s: git reset --hard origin/%s: %w", p.LocalPath, p.Branch, resetErr)
	}

	return nil
}

// gitCommand builds, through the execCommand seam, a git child with args that
// runs unattended under ctx. The provider's git runs under the autoupdate
// workers and the compare flow, where nobody answers a prompt, so:
//
//   - the child leads its own process group (procgroup.Group): when ctx is
//     done, git and every process it started (the transport, ssh, a
//     credential helper) get SIGTERM, and SIGKILL procgroup.GracePeriod later;
//   - GIT_TERMINAL_PROMPT=0 makes git fail at once where it would otherwise
//     ask for credentials on the terminal. A child outside the terminal's
//     foreground group that reads the terminal is stopped by SIGTTIN, so a
//     prompt would hold the run until the bound. The value overrides the
//     inherited environment, since os/exec uses the last of duplicate keys.
//
// ssh reads a passphrase or a host-key answer from the terminal itself, which
// GIT_TERMINAL_PROMPT does not govern; such a prompt is ended by the bound.
func gitCommand(ctx context.Context, args ...string) *exec.Cmd {
	cmd := execCommand(ctx, "git", args...)
	cmd.Env = append(cmd.Environ(), "GIT_TERMINAL_PROMPT=0")
	procgroup.Group(cmd)
	return cmd
}

// interrupted returns the error for git operation op when a context ended it,
// or nil when neither context is done. parent is the caller's context; run is
// the one bounded by gitTimeout that git ran under.
//
// The contexts decide, not git's error: git stopped by the signal reports only
// "signal: terminated", whatever caused it. A done parent means the caller
// stopped the run, or the caller's own deadline did, which is not this bound,
// so the parent's error is wrapped as it is. A done run context under a live
// parent hit the bound. run is read first: contexts never come
// back to life, so a parent that is live after that read was live before it,
// and a cancel landing between the two reads is the parent's.
func interrupted(parent, run context.Context, op string) error {
	runDone := run.Err() != nil
	if err := parent.Err(); err != nil {
		return fmt.Errorf("git %s: %w", op, err)
	}
	if runDone {
		return fmt.Errorf("git %s timed out after %s: %w", op, gitTimeout, run.Err())
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

// ForceUpdate forces an update of the repository regardless of age. It is a
// no-op for a local in-place tree, which bentoo does not own and must not pull.
func (p *GitCloneProvider) ForceUpdate() error {
	if p.local {
		return nil
	}
	if !p.repoExists() {
		return p.cloneRepo(context.Background()) // SAFE: ForceUpdate has no ctx parameter; the clone stays bounded by DefaultGitCloneTimeout
	}
	return p.updateRepo(context.Background()) // SAFE: ForceUpdate has no ctx parameter; updateRepo still bounds the run by DefaultGitCloneTimeout
}

// RemoveCache removes the cached repository. It is a no-op for a local in-place
// tree: LocalPath there is the user's real tree (e.g. /var/db/repos/gentoo), not
// a bentoo-managed cache, so os.RemoveAll must never run against it.
func (p *GitCloneProvider) RemoveCache() error {
	if p.local {
		return nil
	}
	return os.RemoveAll(p.LocalPath)
}

// Ensure GitCloneProvider implements Provider interface
var _ Provider = (*GitCloneProvider)(nil)

// Ensure GitCloneProvider implements PackageDirProvider interface
var _ PackageDirProvider = (*GitCloneProvider)(nil)
