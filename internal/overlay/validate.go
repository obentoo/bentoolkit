package overlay

import (
	"fmt"
	"os"

	"github.com/obentoo/bentoolkit/internal/common/config"
)

// ValidationResult contains overlay validation results
type ValidationResult struct {
	Valid    bool     // True if overlay structure is valid
	Errors   []string // Critical issues that prevent operation
	Warnings []string // Non-critical issues
}

// ValidateOverlay checks if a path is a valid Gentoo overlay.
// A valid overlay must have:
// - profiles/ directory
// - metadata/ directory
func ValidateOverlay(path string) (*ValidationResult, error) {
	// Check if path exists
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("overlay path does not exist: %s", path)
		}
		return nil, fmt.Errorf("failed to access overlay path: %w", err)
	}

	if !info.IsDir() {
		return nil, fmt.Errorf("overlay path is not a directory: %s", path)
	}

	// Use the config package's validation function
	configResult := config.ValidateOverlayStructure(path)

	return &ValidationResult{
		Valid:    configResult.Valid,
		Errors:   configResult.Errors,
		Warnings: configResult.Warnings,
	}, nil
}

// FormatValidationError formats a validation result into a user-friendly error message
func FormatValidationError(result *ValidationResult, path string) string {
	if result.Valid {
		return ""
	}

	msg := fmt.Sprintf("overlay validation failed for %s:\n", path)
	for _, err := range result.Errors {
		msg += fmt.Sprintf("  - %s\n", err)
	}
	msg += "\nSuggestion: run 'bentoo overlay init' or check the overlay path configuration"

	return msg
}
