package overlay

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lucascouts/bentoo-tools/internal/common/config"
	"github.com/lucascouts/bentoo-tools/internal/common/git"
)

// setupTestOverlay creates a temporary directory with a git repo for testing
func setupTestOverlay(t *testing.T) (string, *config.Config, func()) {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "overlay-add-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	// Initialize git repo using exec.Command
	runner := git.NewGitRunner(tmpDir)
	_, err = runner.Status()
	if err != nil {
		// Need to init - use exec.Command
		initCmd := exec.Command("git", "init")
		initCmd.Dir = tmpDir
		if err := initCmd.Run(); err != nil {
			os.RemoveAll(tmpDir)
			t.Fatalf("failed to init git repo: %v", err)
		}
	}

	cfg := &config.Config{
		Overlay: config.OverlayConfig{
			Path:   tmpDir,
			Remote: "origin",
		},
	}

	cleanup := func() {
		os.RemoveAll(tmpDir)
	}

	return tmpDir, cfg, cleanup
}

// TestAddFilesWithDefaultPath tests AddFiles with no arguments (defaults to ".")
// _Requirements: 2.1_
func TestAddFilesWithDefaultPath(t *testing.T) {
	tmpDir, cfg, cleanup := setupTestOverlay(t)
	defer cleanup()

	// Create a test file
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("test content"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	// Add with no arguments
	result, err := AddFiles(cfg)
	if err != nil {
		t.Fatalf("AddFiles() returned error: %v", err)
	}

	if !result.IsSuccess() {
		t.Errorf("AddFiles() should succeed with default path")
	}

	if len(result.Added) != 1 || result.Added[0] != "." {
		t.Errorf("AddFiles() should add '.' by default, got %v", result.Added)
	}
}

// TestAddFilesWithSpecificPaths tests AddFiles with specific file paths
// _Requirements: 2.2_
func TestAddFilesWithSpecificPaths(t *testing.T) {
	tmpDir, cfg, cleanup := setupTestOverlay(t)
	defer cleanup()

	// Create test files
	file1 := filepath.Join(tmpDir, "file1.txt")
	file2 := filepath.Join(tmpDir, "file2.txt")
	if err := os.WriteFile(file1, []byte("content1"), 0644); err != nil {
		t.Fatalf("failed to create file1: %v", err)
	}
	if err := os.WriteFile(file2, []byte("content2"), 0644); err != nil {
		t.Fatalf("failed to create file2: %v", err)
	}

	// Add specific files
	result, err := AddFiles(cfg, "file1.txt", "file2.txt")
	if err != nil {
		t.Fatalf("AddFiles() returned error: %v", err)
	}

	if !result.IsSuccess() {
		t.Errorf("AddFiles() should succeed with valid paths")
	}

	if len(result.Added) != 2 {
		t.Errorf("AddFiles() should add 2 files, got %d", len(result.Added))
	}
}

// TestAddFilesWithNonExistentFile tests AddFiles with a file that doesn't exist
// _Requirements: 2.4_
func TestAddFilesWithNonExistentFile(t *testing.T) {
	_, cfg, cleanup := setupTestOverlay(t)
	defer cleanup()

	// Try to add non-existent file
	result, err := AddFiles(cfg, "nonexistent.txt")
	if err != nil {
		t.Fatalf("AddFiles() returned unexpected error: %v", err)
	}

	if result.IsSuccess() {
		t.Error("AddFiles() should fail for non-existent file")
	}

	if len(result.Errors) != 1 {
		t.Errorf("AddFiles() should have 1 error, got %d", len(result.Errors))
	}

	if result.Errors[0] != git.ErrFileNotFound {
		t.Errorf("AddFiles() should return ErrFileNotFound, got %v", result.Errors[0])
	}
}

// TestAddFilesWithMixedPaths tests AddFiles with both valid and invalid paths
// _Requirements: 2.1, 2.4_
func TestAddFilesWithMixedPaths(t *testing.T) {
	tmpDir, cfg, cleanup := setupTestOverlay(t)
	defer cleanup()

	// Create one valid file
	validFile := filepath.Join(tmpDir, "valid.txt")
	if err := os.WriteFile(validFile, []byte("valid"), 0644); err != nil {
		t.Fatalf("failed to create valid file: %v", err)
	}

	// Add both valid and invalid paths
	result, err := AddFiles(cfg, "valid.txt", "invalid.txt")
	if err != nil {
		t.Fatalf("AddFiles() returned unexpected error: %v", err)
	}

	// Should have one success and one error
	if len(result.Added) != 1 {
		t.Errorf("AddFiles() should have 1 added file, got %d", len(result.Added))
	}

	if len(result.Errors) != 1 {
		t.Errorf("AddFiles() should have 1 error, got %d", len(result.Errors))
	}

	if result.HasErrors() != true {
		t.Error("HasErrors() should return true")
	}
}

// TestAddResultMethods tests AddResult helper methods
func TestAddResultMethods(t *testing.T) {
	t.Run("IsSuccess returns true when no errors", func(t *testing.T) {
		result := &AddResult{
			Added:  []string{"file.txt"},
			Errors: []error{},
		}
		if !result.IsSuccess() {
			t.Error("IsSuccess() should return true when no errors")
		}
	})

	t.Run("IsSuccess returns false when errors exist", func(t *testing.T) {
		result := &AddResult{
			Added:  []string{},
			Errors: []error{git.ErrFileNotFound},
		}
		if result.IsSuccess() {
			t.Error("IsSuccess() should return false when errors exist")
		}
	})

	t.Run("HasErrors returns true when errors exist", func(t *testing.T) {
		result := &AddResult{
			Added:  []string{},
			Errors: []error{git.ErrFileNotFound},
		}
		if !result.HasErrors() {
			t.Error("HasErrors() should return true when errors exist")
		}
	})

	t.Run("HasErrors returns false when no errors", func(t *testing.T) {
		result := &AddResult{
			Added:  []string{"file.txt"},
			Errors: []error{},
		}
		if result.HasErrors() {
			t.Error("HasErrors() should return false when no errors")
		}
	})
}

// TestAddFilesWithInvalidConfig tests AddFiles with invalid configuration
// _Requirements: 1.3, 1.4_
func TestAddFilesWithInvalidConfig(t *testing.T) {
	t.Run("empty overlay path", func(t *testing.T) {
		cfg := &config.Config{
			Overlay: config.OverlayConfig{
				Path: "",
			},
		}

		_, err := AddFiles(cfg, "file.txt")
		if err != config.ErrOverlayPathNotSet {
			t.Errorf("AddFiles() should return ErrOverlayPathNotSet, got %v", err)
		}
	})

	t.Run("non-existent overlay path", func(t *testing.T) {
		cfg := &config.Config{
			Overlay: config.OverlayConfig{
				Path: "/nonexistent/path/to/overlay",
			},
		}

		_, err := AddFiles(cfg, "file.txt")
		if err != config.ErrOverlayPathNotFound {
			t.Errorf("AddFiles() should return ErrOverlayPathNotFound, got %v", err)
		}
	})
}
