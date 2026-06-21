package git

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/obentoo/bentoolkit/internal/common/tui"
)

var (
	ErrFileNotFound       = errors.New("file not found")
	ErrPathOutsideOverlay = errors.New("path is outside overlay directory")
	ErrInvalidPath        = errors.New("invalid path")
	ErrGitCommand         = errors.New("git command failed")
)

// GitRunner executes git commands in a specific working directory
type GitRunner struct {
	workDir string

	// reporter receives stage/done/tail events for mutating and streaming ops.
	// Defaults to tui.Noop() so callers that supply no reporter behave exactly
	// as before (R3.3).
	reporter tui.Reporter
	// taskID identifies this runner's task in reporter events.
	taskID string
	// execCommand builds the underlying *exec.Cmd; it is a seam so tests can
	// substitute the subprocess. Defaults to exec.Command (R3.3).
	execCommand func(name string, arg ...string) *exec.Cmd
}

// GitRunnerOption configures a GitRunner at construction time.
type GitRunnerOption func(*GitRunner)

// NewGitRunner creates a new GitRunner for the specified working directory.
// With no options it uses a no-op reporter and exec.Command, so existing
// no-arg callers are unaffected (R3.3).
func NewGitRunner(workDir string, opts ...GitRunnerOption) *GitRunner {
	g := &GitRunner{
		workDir:     workDir,
		reporter:    tui.Noop(),
		execCommand: exec.Command,
	}
	for _, opt := range opts {
		opt(g)
	}
	return g
}

// WithGitReporter sets the reporter and task id used for stage/done/tail
// events. A nil reporter is normalized to tui.Noop().
func WithGitReporter(r tui.Reporter, id string) GitRunnerOption {
	return func(g *GitRunner) {
		if r == nil {
			r = tui.Noop()
		}
		g.reporter = r
		g.taskID = id
	}
}

// WithGitExecCommand overrides the subprocess seam. A nil fn leaves the
// default (exec.Command) in place.
func WithGitExecCommand(fn func(name string, arg ...string) *exec.Cmd) GitRunnerOption {
	return func(g *GitRunner) {
		if fn != nil {
			g.execCommand = fn
		}
	}
}

// staged runs fn between a TaskStage(op) and a TaskDone event, keeping the
// stage/done wiring DRY across mutating ops.
func (g *GitRunner) staged(op string, fn func() error) error {
	g.reporter.TaskStage(g.taskID, op)
	err := fn()
	g.reporter.TaskDone(g.taskID, err == nil, "", "")
	return err
}

// WorkDir returns the working directory of the GitRunner
func (g *GitRunner) WorkDir() string {
	return g.workDir
}

// runCommand executes a git command and returns stdout, stderr, and any error
func (g *GitRunner) runCommand(args ...string) (stdout, stderr string, err error) {
	cmd := g.execCommand("git", args...)
	cmd.Dir = g.workDir

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	err = cmd.Run()
	stdout = stdoutBuf.String()
	stderr = stderrBuf.String()

	if err != nil {
		// Wrap the error with stderr for context
		if stderr != "" {
			err = errors.Join(ErrGitCommand, errors.New(strings.TrimSpace(stderr)))
		}
	}

	return stdout, stderr, err
}

// StatusEntry represents a single entry from git status --porcelain
type StatusEntry struct {
	Status   string // A, M, D, R, ??
	FilePath string
}

// Status returns the current git status as a list of StatusEntry
func (g *GitRunner) Status() ([]StatusEntry, error) {
	stdout, _, err := g.runCommand("status", "--porcelain")
	if err != nil {
		return nil, err
	}

	return ParseStatusOutput(stdout), nil
}

// StagedStatus returns only the entries staged in the index, i.e. exactly what
// a commit would include. It runs git status --porcelain and keeps entries whose
// index column (X) is set, dropping unstaged-only and untracked files.
func (g *GitRunner) StagedStatus() ([]StatusEntry, error) {
	stdout, _, err := g.runCommand("status", "--porcelain")
	if err != nil {
		return nil, err
	}

	return ParseStagedStatusOutput(stdout), nil
}

// ParseStagedStatusOutput parses git status --porcelain output and keeps only
// entries staged in the index. In the "XY filename" format, X is the index
// (staged) status and Y is the worktree status; an entry is staged when X is
// neither a space (unstaged-only) nor '?' (untracked).
func ParseStagedStatusOutput(output string) []StatusEntry {
	var entries []StatusEntry

	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if len(line) < 3 {
			continue
		}

		// X = index status, Y = worktree status. Keep only staged (X set).
		indexStatus := line[0]
		if indexStatus == ' ' || indexStatus == '?' {
			continue
		}

		status := string(indexStatus)
		filePath := line[3:]

		// Staged rename: "R  old -> new" — split into delete + add, mirroring
		// ParseStatusOutput so version-bump detection keeps working.
		if status == "R" {
			parts := strings.Split(filePath, " -> ")
			if len(parts) == 2 {
				entries = append(entries, StatusEntry{
					Status:   "D",
					FilePath: strings.TrimSpace(parts[0]),
				})
				entries = append(entries, StatusEntry{
					Status:   "A",
					FilePath: strings.TrimSpace(parts[1]),
				})
				continue
			}
		}

		entries = append(entries, StatusEntry{
			Status:   status,
			FilePath: filePath,
		})
	}

	return entries
}

// ParseStatusOutput parses git status --porcelain output into StatusEntry slice
func ParseStatusOutput(output string) []StatusEntry {
	var entries []StatusEntry

	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if len(line) < 3 {
			continue
		}

		// Git status --porcelain format: XY filename
		// X = index status, Y = worktree status
		// We use the index status (X) for staged files, worktree status (Y) for unstaged
		status := strings.TrimSpace(line[:2])
		filePath := line[3:]

		// Handle renamed files: R  old -> new
		// Convert rename to delete + add for proper version bump detection
		if strings.HasPrefix(status, "R") {
			parts := strings.Split(filePath, " -> ")
			if len(parts) == 2 {
				oldPath := strings.TrimSpace(parts[0])
				newPath := strings.TrimSpace(parts[1])
				// Add delete entry for old file
				entries = append(entries, StatusEntry{
					Status:   "D",
					FilePath: oldPath,
				})
				// Add entry for new file
				entries = append(entries, StatusEntry{
					Status:   "A",
					FilePath: newPath,
				})
				continue
			}
		}

		entries = append(entries, StatusEntry{
			Status:   status,
			FilePath: filePath,
		})
	}

	return entries
}

// Add stages files for commit with path validation
func (g *GitRunner) Add(paths ...string) error {
	return g.staged("add", func() error {
		if len(paths) == 0 {
			// Default to adding all changes
			_, _, err := g.runCommand("add", ".")
			return err
		}

		for _, path := range paths {
			if err := g.validateAndAddPath(path); err != nil {
				return err
			}
		}

		return nil
	})
}

// validateAndAddPath validates a single path and adds it to staging
func (g *GitRunner) validateAndAddPath(path string) error {
	// Resolve the path relative to workDir
	var absPath string
	if filepath.IsAbs(path) {
		absPath = path
	} else {
		absPath = filepath.Join(g.workDir, path)
	}

	// Clean the path to resolve any .. or . components
	absPath = filepath.Clean(absPath)
	workDirAbs := filepath.Clean(g.workDir)

	// Check if path is inside the overlay directory
	relPath, err := filepath.Rel(workDirAbs, absPath)
	if err != nil {
		return errors.Join(ErrInvalidPath, err)
	}

	// If the relative path starts with "..", it's outside the overlay
	if strings.HasPrefix(relPath, "..") {
		return ErrPathOutsideOverlay
	}

	// Check if the path exists (use Lstat to detect symlinks without following them)
	linfo, lstatErr := os.Lstat(absPath)
	if lstatErr != nil {
		return ErrFileNotFound
	}

	// Resolve symlinks to prevent traversal attacks.
	// For symlinks: EvalSymlinks follows the link — broken symlinks return ErrInvalidPath.
	// For regular files/dirs: EvalSymlinks resolves any symlink components in the path.
	var realPath string
	if linfo.Mode()&os.ModeSymlink != 0 {
		// It's a symlink — resolve it; broken symlink → ErrInvalidPath
		resolved, err := filepath.EvalSymlinks(absPath)
		if err != nil {
			return fmt.Errorf("%w: cannot resolve symlink: %v", ErrInvalidPath, err)
		}
		realPath = resolved
	} else {
		// Regular file or directory — resolve any symlink components in the path itself
		resolved, err := filepath.EvalSymlinks(absPath)
		if err != nil {
			return fmt.Errorf("%w: cannot resolve path: %v", ErrInvalidPath, err)
		}
		realPath = resolved
	}

	realWorkDir, err := filepath.EvalSymlinks(workDirAbs)
	if err != nil {
		return fmt.Errorf("cannot resolve overlay path: %w", err)
	}

	// Re-validate containment with real (symlink-resolved) paths
	realRelPath, err := filepath.Rel(realWorkDir, realPath)
	if err != nil {
		return errors.Join(ErrInvalidPath, err)
	}

	if strings.HasPrefix(realRelPath, "..") {
		return ErrPathOutsideOverlay
	}

	// Add the file to staging
	_, _, err = g.runCommand("add", path)
	return err
}

// Commit creates a git commit with the specified message and author
func (g *GitRunner) Commit(message, user, email string) error {
	return g.staged("commit", func() error {
		args := []string{"commit", "-m", message}

		// Set author if provided
		if user != "" && email != "" {
			author := user + " <" + email + ">"
			args = append(args, "--author", author)
		}

		_, _, err := g.runCommand(args...)
		return err
	})
}

// Push pushes commits to the remote repository
func (g *GitRunner) Push() error {
	return g.staged("push", func() error {
		_, _, err := g.runCommand("push")
		return err
	})
}

// PushDryRun shows what would be pushed without actually pushing
func (g *GitRunner) PushDryRun() (string, error) {
	stdout, _, err := g.runCommand("push", "--dry-run", "-v")
	if err != nil {
		return "", err
	}
	if stdout == "" {
		return "Nothing to push (up-to-date with remote)", nil
	}
	return strings.TrimSpace(stdout), nil
}

// Fetch fetches changes from a remote repository. It is a streaming op: git's
// progress output (which it writes to stderr) is tailed live via a
// tui.StreamCapture and also captured verbatim, so a failing fetch preserves
// the full output in the returned error (R7.1). Under the default Noop reporter
// and exec.Command this is behavior-equivalent to a buffered fetch (R3.3).
func (g *GitRunner) Fetch(remote string) error {
	g.reporter.TaskStage(g.taskID, "fetch")

	cmd := g.execCommand("git", "fetch", remote)
	cmd.Dir = g.workDir

	// One capture for both streams (combined), so git's stderr progress is tailed
	// and preserved alongside any stdout.
	sc := tui.NewStreamCapture(g.reporter, g.taskID, tui.StreamStdout)
	cmd.Stdout = sc
	cmd.Stderr = sc

	runErr := cmd.Run()
	_ = sc.Close()

	g.reporter.TaskDone(g.taskID, runErr == nil, "", "")

	if runErr != nil {
		// Preserve the captured output in the error, mirroring runCommand's
		// wrapping (R7.1).
		captured := strings.TrimSpace(sc.Captured())
		if captured != "" {
			return errors.Join(ErrGitCommand, errors.New(captured))
		}
		return errors.Join(ErrGitCommand, runErr)
	}
	return nil
}

// Merge merges a branch into the current branch.
// If there are conflicts, the error message includes the conflict details from stdout.
func (g *GitRunner) Merge(branch string) error {
	return g.staged("merge", func() error {
		stdout, stderr, err := g.runCommand("merge", branch)
		if err != nil {
			// Git outputs conflict information to stdout, so include it in the error
			// for proper conflict detection
			combinedOutput := strings.TrimSpace(stdout + "\n" + stderr)
			if combinedOutput != "" {
				return errors.Join(ErrGitCommand, errors.New(combinedOutput))
			}
		}
		return err
	})
}

// Ensure GitRunner implements GitExecutor interface
var _ GitExecutor = (*GitRunner)(nil)
