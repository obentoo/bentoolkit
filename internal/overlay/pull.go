package overlay

import (
	"errors"
	"fmt"
	"strings"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/git"
)

var (
	// ErrPullConflict indicates merge conflicts occurred during pull
	ErrPullConflict = errors.New("merge conflicts detected")
	// ErrNoRemote indicates no remote is configured
	ErrNoRemote = errors.New("no remote configured")
	// ErrDirtyWorktree indicates uncommitted changes block the integration step
	ErrDirtyWorktree = errors.New("uncommitted changes in the overlay")
)

// PullMode selects how fetched commits are integrated into the current branch.
type PullMode int

const (
	// PullFFOnly integrates only when the branch can fast-forward. It is the
	// default: an overlay that has diverged from its upstream is something the
	// operator should see, not something a merge commit should paper over.
	PullFFOnly PullMode = iota
	// PullMerge merges the upstream, writing a merge commit when the branches
	// have diverged.
	PullMerge
	// PullRebase replays local commits on top of the upstream.
	PullRebase
)

// String names the mode for messages and tests.
func (m PullMode) String() string {
	switch m {
	case PullRebase:
		return "rebase"
	case PullMerge:
		return "merge"
	default:
		return "fast-forward"
	}
}

// PullResult contains pull operation results
type PullResult struct {
	Success       bool     // True if the pull completed without conflicts
	UpToDate      bool     // True if the branch was already current with upstream
	CommitsPulled int      // Number of commits the upstream was ahead by
	Branch        string   // Branch that was pulled into
	Upstream      string   // Upstream ref the commits came from (e.g. "origin/master")
	Conflicts     []string // List of conflicting file paths
	Message       string   // Human-readable status message
}

// Pull fetches the configured remote and integrates the current branch's
// upstream into it.
func Pull(cfg *config.Config, mode PullMode, dryRun bool) (*PullResult, error) {
	overlayPath, err := cfg.GetOverlayPath()
	if err != nil {
		return nil, err
	}

	remote := cfg.Overlay.Remote
	if remote == "" {
		remote = "origin"
	}

	runner := git.NewGitRunner(overlayPath)
	return PullWithRunner(runner, remote, mode, dryRun)
}

// PullWithRunner performs the pull using a provided GitExecutor.
// This allows for testing with mock implementations.
//
// The upstream is resolved from the branch that is actually checked out, not
// from the remote's default branch: merging origin/HEAD into whatever happens
// to be checked out silently drags the default branch into unrelated work.
func PullWithRunner(runner git.GitExecutor, remote string, mode PullMode, dryRun bool) (*PullResult, error) {
	if remote == "" {
		return nil, ErrNoRemote
	}

	// Resolve branch and upstream before touching the network: both failures
	// are cheap, and neither is worth a fetch to discover.
	branch, err := runner.CurrentBranch()
	if err != nil {
		return nil, err
	}

	upstream, err := runner.Upstream()
	if err != nil {
		return nil, fmt.Errorf("branch %q has no upstream to pull from: %w", branch, err)
	}

	if err := runner.Fetch(remote); err != nil {
		return nil, err
	}

	// Counted after the fetch, so the number reflects what was just fetched
	// rather than a stale remote-tracking ref.
	behind, err := runner.CountRange("HEAD", upstream)
	if err != nil {
		return nil, err
	}

	result := &PullResult{
		Branch:        branch,
		Upstream:      upstream,
		CommitsPulled: behind,
	}

	if behind == 0 {
		result.Success = true
		result.UpToDate = true
		result.Message = fmt.Sprintf("Already up to date with %s.", upstream)
		return result, nil
	}

	// Only now does a dirty worktree matter. A pull that has nothing to
	// integrate should not complain about work in progress.
	dirty, err := hasUncommittedChanges(runner)
	if err != nil {
		return nil, err
	}
	if dirty {
		return nil, fmt.Errorf("%w: commit or stash them before pulling %s into %s",
			ErrDirtyWorktree, upstream, branch)
	}

	if dryRun {
		result.Success = true
		result.Message = fmt.Sprintf("Dry-run: would integrate %s into %s by %s (%s).",
			upstream, branch, mode, commitPlural(behind))
		return result, nil
	}

	if err := integrate(runner, upstream, mode); err != nil {
		errStr := err.Error()
		if isConflictError(errStr) {
			result.Conflicts = parseConflicts(errStr)
			result.Message = "Merge conflicts detected. Please resolve conflicts manually."
			return result, nil
		}
		// Git also refuses when a file it is about to write is already there
		// with local content — including an untracked one, which the check
		// above deliberately let through. Report that in the toolkit's own
		// terms rather than passing git's text along.
		if blocking := parseOverwriteFiles(errStr); len(blocking) > 0 {
			return nil, fmt.Errorf("%w: %s would be overwritten by %s; move, remove or commit them first",
				ErrDirtyWorktree, strings.Join(blocking, ", "), upstream)
		}
		return nil, err
	}

	result.Success = true
	result.Message = fmt.Sprintf("Pulled %s from %s into %s.",
		commitPlural(behind), upstream, branch)
	return result, nil
}

// integrate applies the fetched commits using the selected mode.
func integrate(runner git.GitExecutor, upstream string, mode PullMode) error {
	switch mode {
	case PullRebase:
		return runner.Rebase(upstream)
	case PullMerge:
		return runner.Merge(upstream)
	default:
		return runner.MergeFFOnly(upstream)
	}
}

// hasUncommittedChanges reports whether the worktree carries modified tracked
// files, which an integration step could overwrite. Untracked files are not
// checked here: an overlay routinely carries them (stray caches, scratch
// files) and blocking on their mere presence would make the pull unusable.
// Git still refuses when an untracked file is about to be overwritten, and
// that refusal is translated by classifyIntegrationError.
func hasUncommittedChanges(runner git.GitExecutor) (bool, error) {
	entries, err := runner.Status()
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.Status != "??" {
			return true, nil
		}
	}
	return false, nil
}

// commitPlural renders a commit count with the right noun.
func commitPlural(n int) string {
	if n == 1 {
		return "1 commit"
	}
	return fmt.Sprintf("%d commits", n)
}

// parseOverwriteFiles extracts the file list from git's refusal to overwrite
// local content. Git prints a header, then one tab-indented path per line:
//
//	error: The following untracked working tree files would be overwritten by merge:
//		profiles/categories
//	Please move or remove them before you merge.
func parseOverwriteFiles(errStr string) []string {
	var files []string
	collecting := false

	for _, line := range strings.Split(errStr, "\n") {
		if strings.Contains(line, "would be overwritten by") {
			collecting = true
			continue
		}
		if !collecting {
			continue
		}
		// The indented run ends at the first line git does not indent.
		if !strings.HasPrefix(line, "\t") && !strings.HasPrefix(line, "  ") {
			break
		}
		if file := strings.TrimSpace(line); file != "" {
			files = append(files, file)
		}
	}

	return files
}

// isConflictError checks if an error message indicates merge conflicts
func isConflictError(errStr string) bool {
	conflictIndicators := []string{
		"CONFLICT",
		"Automatic merge failed",
		"fix conflicts",
		"Merge conflict",
		"could not apply", // rebase reports its conflicts this way
	}

	for _, indicator := range conflictIndicators {
		if strings.Contains(errStr, indicator) {
			return true
		}
	}
	return false
}

// parseConflicts extracts conflicting file paths from git merge error output
func parseConflicts(errStr string) []string {
	var conflicts []string
	lines := strings.Split(errStr, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		// Git outputs conflicts like: "CONFLICT (content): Merge conflict in <file>"
		if strings.HasPrefix(line, "CONFLICT") {
			// Extract file path from "Merge conflict in <file>"
			if idx := strings.Index(line, "Merge conflict in "); idx != -1 {
				file := strings.TrimSpace(line[idx+len("Merge conflict in "):])
				if file != "" {
					conflicts = append(conflicts, file)
				}
			}
		}
	}

	return conflicts
}
