package overlay

import (
	"errors"
	"strings"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/git"
)

var (
	// ErrSyncConflict indicates merge conflicts occurred during sync
	ErrSyncConflict = errors.New("merge conflicts detected")
	// ErrNoRemote indicates no remote is configured
	ErrNoRemote = errors.New("no remote configured")
)

// SyncResult contains sync operation results
type SyncResult struct {
	Success       bool     // True if sync completed without conflicts
	CommitsPulled int      // Number of commits pulled from upstream
	Conflicts     []string // List of conflicting file paths
	Message       string   // Human-readable status message
}

// Sync fetches and merges upstream changes from the configured remote.
// It performs a fetch followed by a merge operation.
func Sync(cfg *config.Config) (*SyncResult, error) {
	overlayPath, err := cfg.GetOverlayPath()
	if err != nil {
		return nil, err
	}

	remote := cfg.Overlay.Remote
	if remote == "" {
		remote = "origin"
	}

	runner := git.NewGitRunner(overlayPath)
	return SyncWithRunner(runner, remote)
}

// SyncWithRunner performs sync using a provided GitExecutor.
// This allows for testing with mock implementations.
func SyncWithRunner(runner git.GitExecutor, remote string) (*SyncResult, error) {
	if remote == "" {
		return nil, ErrNoRemote
	}

	// Fetch changes from remote
	if err := runner.Fetch(remote); err != nil {
		return nil, err
	}

	// Merge the remote tracking branch
	branch := remote + "/HEAD"
	err := runner.Merge(branch)

	if err != nil {
		// Check if the error indicates merge conflicts
		errStr := err.Error()
		if isConflictError(errStr) {
			conflicts := parseConflicts(errStr)
			return &SyncResult{
				Success:   false,
				Conflicts: conflicts,
				Message:   "Merge conflicts detected. Please resolve conflicts manually.",
			}, nil
		}
		return nil, err
	}

	return &SyncResult{
		Success: true,
		Message: "Sync completed successfully.",
	}, nil
}

// isConflictError checks if an error message indicates merge conflicts
func isConflictError(errStr string) bool {
	conflictIndicators := []string{
		"CONFLICT",
		"Automatic merge failed",
		"fix conflicts",
		"Merge conflict",
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
