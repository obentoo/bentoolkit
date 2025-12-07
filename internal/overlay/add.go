package overlay

import (
	"github.com/obentoo/bentoo-tools/internal/common/config"
	"github.com/obentoo/bentoo-tools/internal/common/git"
)

// AddResult contains the result of an Add operation
type AddResult struct {
	Added  []string // Successfully added files
	Errors []error  // Errors encountered during add
}

// AddFiles stages files for commit in the overlay repository
// If no paths are provided, it defaults to adding all changes (equivalent to "git add .")
// Returns a structured result with added files and any errors encountered
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

	// Handle default path when no args provided
	if len(paths) == 0 {
		paths = []string{"."}
	}

	// Add each path individually to track successes and failures
	for _, path := range paths {
		err := runner.Add(path)
		if err != nil {
			result.Errors = append(result.Errors, err)
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
