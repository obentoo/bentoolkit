package overlay

import (
	"errors"
	"strings"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/git"
)

var (
	// ErrUpToDate indicates the repository is already up-to-date with remote
	ErrUpToDate = errors.New("everything is up-to-date")
)

// PushResult contains the result of a Push operation
type PushResult struct {
	UpToDate bool   // True if nothing was pushed (already up-to-date)
	Message  string // Status message from git
}

// PushDryRun shows what would be pushed without actually pushing
func PushDryRun(cfg *config.Config) (string, error) {
	overlayPath, err := cfg.GetOverlayPath()
	if err != nil {
		return "", err
	}

	runner := git.NewGitRunner(overlayPath)
	return runner.PushDryRun()
}

// Push pushes committed changes to the remote repository
// Returns ErrUpToDate if there's nothing to push
func Push(cfg *config.Config) (*PushResult, error) {
	overlayPath, err := cfg.GetOverlayPath()
	if err != nil {
		return nil, err
	}

	runner := git.NewGitRunner(overlayPath)
	return PushWithExecutor(runner)
}

// PushWithExecutor pushes committed changes using the provided GitExecutor.
// This function is useful for testing with mock implementations.
func PushWithExecutor(executor git.GitExecutor) (*PushResult, error) {
	err := executor.Push()

	if err != nil {
		// Check if the error indicates up-to-date status
		errStr := err.Error()
		if strings.Contains(errStr, "Everything up-to-date") ||
			strings.Contains(errStr, "up to date") {
			return &PushResult{
				UpToDate: true,
				Message:  "Everything is up-to-date. Nothing to push.",
			}, nil
		}
		return nil, err
	}

	return &PushResult{
		UpToDate: false,
		Message:  "Changes pushed successfully.",
	}, nil
}
