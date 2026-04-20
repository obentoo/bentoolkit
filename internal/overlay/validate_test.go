package overlay

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

// DirectoryConfig represents which directories exist in a test overlay
type DirectoryConfig struct {
	HasProfiles bool
	HasMetadata bool
}

// genDirectoryConfig generates all possible directory configurations
// excluding the case where both directories exist (which is valid)
func genDirectoryConfig() gopter.Gen {
	return gen.OneConstOf(
		DirectoryConfig{HasProfiles: false, HasMetadata: false}, // missing both
		DirectoryConfig{HasProfiles: true, HasMetadata: false},  // missing metadata
		DirectoryConfig{HasProfiles: false, HasMetadata: true},  // missing profiles
	)
}

// TestOverlayValidationDetectsMissingDirectories tests Property 5: Overlay validation detects missing directories
// **Feature: overlay-improvements, Property 5: Overlay validation detects missing directories**
// **Validates: Requirements 7.1, 7.2, 7.3**
func TestOverlayValidationDetectsMissingDirectories(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("Missing directories are detected and reported", prop.ForAll(
		func(dirConfig DirectoryConfig) bool {
			// Create temp directory
			tmpDir, err := os.MkdirTemp("", "overlay-validate-test-*")
			if err != nil {
				t.Logf("Failed to create temp dir: %v", err)
				return false
			}
			defer os.RemoveAll(tmpDir)

			// Create directories based on config
			if dirConfig.HasProfiles {
				if err := os.MkdirAll(filepath.Join(tmpDir, "profiles"), 0755); err != nil {
					t.Logf("Failed to create profiles dir: %v", err)
					return false
				}
			}
			if dirConfig.HasMetadata {
				if err := os.MkdirAll(filepath.Join(tmpDir, "metadata"), 0755); err != nil {
					t.Logf("Failed to create metadata dir: %v", err)
					return false
				}
			}

			// Validate the overlay
			result, err := ValidateOverlay(tmpDir)
			if err != nil {
				t.Logf("ValidateOverlay returned error: %v", err)
				return false
			}

			// Property: Valid should be false when any directory is missing
			if result.Valid {
				t.Logf("Expected Valid=false for config %+v", dirConfig)
				return false
			}

			// Property: Errors should contain message about missing profiles/ if it's missing
			if !dirConfig.HasProfiles {
				found := false
				for _, errMsg := range result.Errors {
					if strings.Contains(errMsg, "profiles") {
						found = true
						break
					}
				}
				if !found {
					t.Logf("Expected error about missing profiles/ directory")
					return false
				}
			}

			// Property: Errors should contain message about missing metadata/ if it's missing
			if !dirConfig.HasMetadata {
				found := false
				for _, errMsg := range result.Errors {
					if strings.Contains(errMsg, "metadata") {
						found = true
						break
					}
				}
				if !found {
					t.Logf("Expected error about missing metadata/ directory")
					return false
				}
			}

			return true
		},
		genDirectoryConfig(),
	))

	properties.TestingRun(t)
}

// TestValidOverlayPassesValidation tests that a valid overlay passes validation
// _Requirements: 7.1, 7.2_
func TestValidOverlayPassesValidation(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "overlay-validate-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create both required directories
	if err := os.MkdirAll(filepath.Join(tmpDir, "profiles"), 0755); err != nil {
		t.Fatalf("Failed to create profiles dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(tmpDir, "metadata"), 0755); err != nil {
		t.Fatalf("Failed to create metadata dir: %v", err)
	}

	result, err := ValidateOverlay(tmpDir)
	if err != nil {
		t.Fatalf("ValidateOverlay returned error: %v", err)
	}

	if !result.Valid {
		t.Errorf("Expected Valid=true for valid overlay, got false with errors: %v", result.Errors)
	}

	if len(result.Errors) != 0 {
		t.Errorf("Expected no errors for valid overlay, got: %v", result.Errors)
	}
}

// TestValidateOverlayNonExistentPath tests validation with non-existent path
// _Requirements: 7.3_
func TestValidateOverlayNonExistentPath(t *testing.T) {
	_, err := ValidateOverlay("/nonexistent/path/to/overlay")
	if err == nil {
		t.Error("Expected error for non-existent path")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("Expected 'does not exist' error, got: %v", err)
	}
}

// TestValidateOverlayNotADirectory tests validation when path is a file
// _Requirements: 7.3_
func TestValidateOverlayNotADirectory(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "overlay-validate-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	_, err = ValidateOverlay(tmpFile.Name())
	if err == nil {
		t.Error("Expected error when path is a file")
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("Expected 'not a directory' error, got: %v", err)
	}
}

// TestFormatValidationError tests the error formatting function
// _Requirements: 7.3_
func TestFormatValidationError(t *testing.T) {
	t.Run("valid result returns empty string", func(t *testing.T) {
		result := &ValidationResult{Valid: true}
		msg := FormatValidationError(result, "/path/to/overlay")
		if msg != "" {
			t.Errorf("Expected empty string for valid result, got: %s", msg)
		}
	})

	t.Run("invalid result contains path and errors", func(t *testing.T) {
		result := &ValidationResult{
			Valid:  false,
			Errors: []string{"missing profiles/ directory", "missing metadata/ directory"},
		}
		msg := FormatValidationError(result, "/path/to/overlay")

		if !strings.Contains(msg, "/path/to/overlay") {
			t.Error("Error message should contain the path")
		}
		if !strings.Contains(msg, "missing profiles/ directory") {
			t.Error("Error message should contain profiles error")
		}
		if !strings.Contains(msg, "missing metadata/ directory") {
			t.Error("Error message should contain metadata error")
		}
		if !strings.Contains(msg, "bentoo overlay init") {
			t.Error("Error message should suggest running 'bentoo overlay init'")
		}
	})
}
