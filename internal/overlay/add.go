package overlay

import (
	"fmt"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/git"
)

// AddResult contains the result of an Add operation
type AddResult struct {
	Added  []string // Successfully added files
	Errors []error  // Errors encountered during add
}

// AddFiles stages files for commit in the overlay repository.
// If no paths are provided, it defaults to adding all changes (equivalent to "git add .").
// When paths are provided, only those specific paths are staged.
// Returns a structured result with added files and any errors encountered.
func AddFiles(cfg *config.Config, paths ...string) (*AddResult, error) {
	overlayPath, err := cfg.GetOverlayPath()
	if err != nil {
		return nil, err
	}

	runner := git.NewGitRunner(overlayPath)
	result := &AddResult{
		Added:  []string{},
		Errors: []error{},
	}

	// Only default to "." when NO paths are provided
	// This is the no-args case - add all changes
	if len(paths) == 0 {
		err := runner.Add(".")
		if err != nil {
			result.Errors = append(result.Errors, err)
		} else {
			result.Added = append(result.Added, ".")
		}
		return result, nil
	}

	// Add each provided path individually
	// Paths are passed directly without modification
	for _, path := range paths {
		err := runner.Add(path)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Errorf("failed to add %s: %w", path, err))
		} else {
			result.Added = append(result.Added, path)
		}
	}

	return result, nil
}

// HasErrors returns true if there were any errors during the Add operation
func (r *AddResult) HasErrors() bool {
	return len(r.Errors) > 0
}

// IsSuccess returns true if all files were added successfully
func (r *AddResult) IsSuccess() bool {
	return len(r.Errors) == 0
}
