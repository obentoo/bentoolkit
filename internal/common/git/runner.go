package git

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
}

// NewGitRunner creates a new GitRunner for the specified working directory
func NewGitRunner(workDir string) *GitRunner {
	return &GitRunner{
		workDir: workDir,
	}
}

// WorkDir returns the working directory of the GitRunner
func (g *GitRunner) WorkDir() string {
	return g.workDir
}

// runCommand executes a git command and returns stdout, stderr, and any error
func (g *GitRunner) runCommand(args ...string) (stdout, stderr string, err error) {
	cmd := exec.Command("git", args...)
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
	args := []string{"commit", "-m", message}

	// Set author if provided
	if user != "" && email != "" {
		author := user + " <" + email + ">"
		args = append(args, "--author", author)
	}

	_, _, err := g.runCommand(args...)
	return err
}

// Push pushes commits to the remote repository
func (g *GitRunner) Push() error {
	_, _, err := g.runCommand("push")
	return err
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

// Fetch fetches changes from a remote repository
func (g *GitRunner) Fetch(remote string) error {
	_, _, err := g.runCommand("fetch", remote)
	return err
}

// Merge merges a branch into the current branch.
// If there are conflicts, the error message includes the conflict details from stdout.
func (g *GitRunner) Merge(branch string) error {
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
}

// Ensure GitRunner implements GitExecutor interface
var _ GitExecutor = (*GitRunner)(nil)
