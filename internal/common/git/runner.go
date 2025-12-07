package git

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var (
	ErrFileNotFound     = errors.New("file not found")
	ErrPathOutsideOverlay = errors.New("path is outside overlay directory")
	ErrInvalidPath      = errors.New("invalid path")
	ErrGitCommand       = errors.New("git command failed")
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
		if strings.HasPrefix(status, "R") {
			parts := strings.Split(filePath, " -> ")
			if len(parts) == 2 {
				filePath = parts[1]
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

	// Check if the file/directory exists
	if !fileExists(absPath) {
		return ErrFileNotFound
	}

	// Add the file to staging
	_, _, err = g.runCommand("add", path)
	return err
}

// fileExists checks if a file or directory exists using os.Stat
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
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
