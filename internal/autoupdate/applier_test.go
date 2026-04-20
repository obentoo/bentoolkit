package autoupdate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

// =============================================================================
// Property-Based Tests
// =============================================================================

// TestEbuildCopyVersioning tests Property 9: Ebuild Copy Versioning
// **Feature: ebuild-autoupdate, Property 9: Ebuild Copy Versioning**
// **Validates: Requirements 6.2**
func TestEbuildCopyVersioning(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: Destination ebuild filename is {package}-{newVersion}.ebuild
	properties.Property("Ebuild copy creates correct destination filename", prop.ForAll(
		func(category, pkgName, oldVersion, newVersion string) bool {
			tmpDir := t.TempDir()
			overlayDir := filepath.Join(tmpDir, "overlay")
			configDir := filepath.Join(tmpDir, "config")

			pkg := category + "/" + pkgName

			// Create source ebuild
			createTestEbuildFile(t, overlayDir, pkg, oldVersion)

			// Create pending update
			pending, err := NewPendingList(configDir)
			if err != nil {
				t.Logf("Failed to create pending list: %v", err)
				return false
			}
			pending.Add(PendingUpdate{
				Package:        pkg,
				CurrentVersion: oldVersion,
				NewVersion:     newVersion,
				Status:         StatusPending,
			})

			// Create applier with mocked exec.Command
			applier, err := NewApplier(overlayDir, configDir,
				WithApplierPendingList(pending),
				WithExecCommand(mockExecCommandSuccess),
			)
			if err != nil {
				t.Logf("Failed to create applier: %v", err)
				return false
			}

			// Apply update (without compile)
			result, err := applier.Apply(pkg, false)
			if err != nil {
				t.Logf("Apply failed: %v", err)
				return false
			}

			if !result.Success {
				t.Logf("Apply was not successful: %v", result.Error)
				return false
			}

			// Verify destination file exists with correct name
			expectedDstPath := filepath.Join(overlayDir, category, pkgName, pkgName+"-"+newVersion+".ebuild")
			if _, err := os.Stat(expectedDstPath); os.IsNotExist(err) {
				t.Logf("Expected destination ebuild not found: %s", expectedDstPath)
				return false
			}

			// Verify source file still exists
			expectedSrcPath := filepath.Join(overlayDir, category, pkgName, pkgName+"-"+oldVersion+".ebuild")
			if _, err := os.Stat(expectedSrcPath); os.IsNotExist(err) {
				t.Logf("Source ebuild should still exist: %s", expectedSrcPath)
				return false
			}

			return true
		},
		genCategory(),
		genPkgName(),
		genVersion(),
		genVersion(),
	))

	// Property: Source ebuild filename is {package}-{oldVersion}.ebuild
	properties.Property("Ebuild copy reads from correct source filename", prop.ForAll(
		func(category, pkgName, oldVersion, newVersion string) bool {
			tmpDir := t.TempDir()
			overlayDir := filepath.Join(tmpDir, "overlay")
			configDir := filepath.Join(tmpDir, "config")

			pkg := category + "/" + pkgName

			// Create source ebuild with specific content
			expectedContent := "# Test ebuild content for " + oldVersion
			createTestEbuildFileWithContent(t, overlayDir, pkg, oldVersion, expectedContent)

			// Create pending update
			pending, err := NewPendingList(configDir)
			if err != nil {
				t.Logf("Failed to create pending list: %v", err)
				return false
			}
			pending.Add(PendingUpdate{
				Package:        pkg,
				CurrentVersion: oldVersion,
				NewVersion:     newVersion,
				Status:         StatusPending,
			})

			// Create applier with mocked exec.Command
			applier, err := NewApplier(overlayDir, configDir,
				WithApplierPendingList(pending),
				WithExecCommand(mockExecCommandSuccess),
			)
			if err != nil {
				t.Logf("Failed to create applier: %v", err)
				return false
			}

			// Apply update
			_, err = applier.Apply(pkg, false)
			if err != nil {
				t.Logf("Apply failed: %v", err)
				return false
			}

			// Verify destination file has same content as source
			dstPath := filepath.Join(overlayDir, category, pkgName, pkgName+"-"+newVersion+".ebuild")
			content, err := os.ReadFile(dstPath)
			if err != nil {
				t.Logf("Failed to read destination ebuild: %v", err)
				return false
			}

			if string(content) != expectedContent {
				t.Logf("Content mismatch: expected %q, got %q", expectedContent, string(content))
				return false
			}

			return true
		},
		genCategory(),
		genPkgName(),
		genVersion(),
		genVersion(),
	))

	properties.TestingRun(t)
}

// TestApplySuccessUpdatesStatus tests Property 10: Apply Success Updates Status
// **Feature: ebuild-autoupdate, Property 10: Apply Success Updates Status**
// **Validates: Requirements 6.4**
func TestApplySuccessUpdatesStatus(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: Successful apply (manifest returns 0) sets status to validated
	properties.Property("Successful apply sets status to validated", prop.ForAll(
		func(category, pkgName, oldVersion, newVersion string) bool {
			tmpDir := t.TempDir()
			overlayDir := filepath.Join(tmpDir, "overlay")
			configDir := filepath.Join(tmpDir, "config")

			pkg := category + "/" + pkgName

			// Create source ebuild
			createTestEbuildFile(t, overlayDir, pkg, oldVersion)

			// Create pending update
			pending, err := NewPendingList(configDir)
			if err != nil {
				t.Logf("Failed to create pending list: %v", err)
				return false
			}
			pending.Add(PendingUpdate{
				Package:        pkg,
				CurrentVersion: oldVersion,
				NewVersion:     newVersion,
				Status:         StatusPending,
			})

			// Create applier with mocked exec.Command that succeeds
			applier, err := NewApplier(overlayDir, configDir,
				WithApplierPendingList(pending),
				WithExecCommand(mockExecCommandSuccess),
			)
			if err != nil {
				t.Logf("Failed to create applier: %v", err)
				return false
			}

			// Apply update
			result, err := applier.Apply(pkg, false)
			if err != nil {
				t.Logf("Apply failed: %v", err)
				return false
			}

			if !result.Success {
				t.Logf("Apply was not successful: %v", result.Error)
				return false
			}

			// Verify status is validated
			update, found := pending.Get(pkg)
			if !found {
				t.Log("Pending entry not found after apply")
				return false
			}

			if update.Status != StatusValidated {
				t.Logf("Expected status 'validated', got %q", update.Status)
				return false
			}

			return true
		},
		genCategory(),
		genPkgName(),
		genVersion(),
		genVersion(),
	))

	// Property: Failed manifest sets status to failed
	properties.Property("Failed manifest sets status to failed", prop.ForAll(
		func(category, pkgName, oldVersion, newVersion string) bool {
			tmpDir := t.TempDir()
			overlayDir := filepath.Join(tmpDir, "overlay")
			configDir := filepath.Join(tmpDir, "config")

			pkg := category + "/" + pkgName

			// Create source ebuild
			createTestEbuildFile(t, overlayDir, pkg, oldVersion)

			// Create pending update
			pending, err := NewPendingList(configDir)
			if err != nil {
				t.Logf("Failed to create pending list: %v", err)
				return false
			}
			pending.Add(PendingUpdate{
				Package:        pkg,
				CurrentVersion: oldVersion,
				NewVersion:     newVersion,
				Status:         StatusPending,
			})

			// Create applier with mocked exec.Command that fails
			applier, err := NewApplier(overlayDir, configDir,
				WithApplierPendingList(pending),
				WithExecCommand(mockExecCommandFailure),
			)
			if err != nil {
				t.Logf("Failed to create applier: %v", err)
				return false
			}

			// Apply update (should fail)
			result, _ := applier.Apply(pkg, false)

			// Verify apply failed
			if result.Success {
				t.Log("Expected apply to fail")
				return false
			}

			// Verify status is failed
			update, found := pending.Get(pkg)
			if !found {
				t.Log("Pending entry not found after apply")
				return false
			}

			if update.Status != StatusFailed {
				t.Logf("Expected status 'failed', got %q", update.Status)
				return false
			}

			return true
		},
		genCategory(),
		genPkgName(),
		genVersion(),
		genVersion(),
	))

	properties.TestingRun(t)
}

// =============================================================================
// Helper Functions for Property Tests
// =============================================================================

// genCategory generates valid Gentoo category names
func genCategory() gopter.Gen {
	return gen.RegexMatch(`^[a-z]{3,8}-[a-z]{3,8}$`)
}

// genPkgName generates valid package names
func genPkgName() gopter.Gen {
	return gen.RegexMatch(`^[a-z][a-z0-9-]{2,12}$`)
}

// createTestEbuildFile creates a test ebuild file in the overlay
func createTestEbuildFile(t *testing.T, overlayDir, pkg, version string) {
	t.Helper()
	content := `# Test ebuild
EAPI=8
DESCRIPTION="Test package"
HOMEPAGE="https://example.com"
SRC_URI=""
LICENSE="MIT"
SLOT="0"
KEYWORDS="~amd64"
`
	createTestEbuildFileWithContent(t, overlayDir, pkg, version, content)
}

// createTestEbuildFileWithContent creates a test ebuild file with specific content
func createTestEbuildFileWithContent(t *testing.T, overlayDir, pkg, version, content string) {
	t.Helper()

	parts := strings.Split(pkg, "/")
	if len(parts) != 2 {
		t.Fatalf("Invalid package name: %s", pkg)
	}

	category := parts[0]
	pkgName := parts[1]

	pkgDir := filepath.Join(overlayDir, category, pkgName)
	if err := os.MkdirAll(pkgDir, 0755); err != nil {
		t.Fatalf("Failed to create package dir: %v", err)
	}

	ebuildPath := filepath.Join(pkgDir, pkgName+"-"+version+".ebuild")
	if err := os.WriteFile(ebuildPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write ebuild: %v", err)
	}
}

// mockExecCommandSuccess returns a mock exec.Cmd that always succeeds
func mockExecCommandSuccess(name string, arg ...string) *exec.Cmd {
	return exec.Command("true")
}

// mockExecCommandFailure returns a mock exec.Cmd that always fails
func mockExecCommandFailure(name string, arg ...string) *exec.Cmd {
	return exec.Command("false")
}

// =============================================================================
// Unit Tests
// =============================================================================

// TestNewApplierCreatesComponents tests that NewApplier initializes all components
func TestNewApplierCreatesComponents(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	applier, err := NewApplier(overlayDir, configDir)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if applier.Pending() == nil {
		t.Error("Expected pending list to be initialized")
	}
	if applier.OverlayPath() != overlayDir {
		t.Errorf("Expected overlay path %q, got %q", overlayDir, applier.OverlayPath())
	}
	if applier.LogsDir() == "" {
		t.Error("Expected logs dir to be set")
	}
}

// TestNewApplierCreatesLogsDir tests that NewApplier creates the logs directory
func TestNewApplierCreatesLogsDir(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	applier, err := NewApplier(overlayDir, configDir)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Verify logs directory was created
	info, err := os.Stat(applier.LogsDir())
	if err != nil {
		t.Fatalf("Logs directory not created: %v", err)
	}
	if !info.IsDir() {
		t.Error("Expected directory, got file")
	}
}

// TestNewApplierWithOptions tests functional options
func TestNewApplierWithOptions(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")
	customLogsDir := filepath.Join(tmpDir, "custom-logs")

	customPending, _ := NewPendingList(configDir)
	confirmCalled := false
	customConfirm := func(prompt string) bool {
		confirmCalled = true
		return true
	}

	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(customPending),
		WithLogsDir(customLogsDir),
		WithConfirmFunc(customConfirm),
	)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if applier.Pending() != customPending {
		t.Error("Expected custom pending list to be used")
	}
	if applier.LogsDir() != customLogsDir {
		t.Errorf("Expected logs dir %q, got %q", customLogsDir, applier.LogsDir())
	}

	// Test custom confirm function
	applier.confirmFunc("test")
	if !confirmCalled {
		t.Error("Expected custom confirm function to be called")
	}
}

// TestApplyPackageNotInPending tests error when package is not in pending
func TestApplyPackageNotInPending(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	applier, err := NewApplier(overlayDir, configDir)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	result, err := applier.Apply("nonexistent/pkg", false)
	if err != ErrPackageNotInPending {
		t.Errorf("Expected ErrPackageNotInPending, got: %v", err)
	}
	if result.Success {
		t.Error("Expected result.Success to be false")
	}
}

// TestApplySourceEbuildNotFound tests error when source ebuild doesn't exist
func TestApplySourceEbuildNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	// Create package directory but no ebuild
	pkgDir := filepath.Join(overlayDir, "test-cat", "test-pkg")
	os.MkdirAll(pkgDir, 0755)

	pending, _ := NewPendingList(configDir)
	pending.Add(PendingUpdate{
		Package:        "test-cat/test-pkg",
		CurrentVersion: "1.0.0",
		NewVersion:     "2.0.0",
		Status:         StatusPending,
	})

	applier, err := NewApplier(overlayDir, configDir, WithApplierPendingList(pending))
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	result, err := applier.Apply("test-cat/test-pkg", false)
	if err == nil {
		t.Error("Expected error for missing source ebuild")
	}
	if result.Success {
		t.Error("Expected result.Success to be false")
	}

	// Verify status was set to failed
	update, _ := pending.Get("test-cat/test-pkg")
	if update.Status != StatusFailed {
		t.Errorf("Expected status 'failed', got %q", update.Status)
	}
}

// TestApplyCopiesEbuild tests that Apply copies the ebuild correctly
func TestApplyCopiesEbuild(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pkg := "test-cat/test-pkg"
	oldVersion := "1.0.0"
	newVersion := "2.0.0"

	// Create source ebuild
	createTestEbuildFile(t, overlayDir, pkg, oldVersion)

	pending, _ := NewPendingList(configDir)
	pending.Add(PendingUpdate{
		Package:        pkg,
		CurrentVersion: oldVersion,
		NewVersion:     newVersion,
		Status:         StatusPending,
	})

	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(mockExecCommandSuccess),
	)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	result, err := applier.Apply(pkg, false)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if !result.Success {
		t.Errorf("Expected success, got error: %v", result.Error)
	}

	// Verify destination file exists
	dstPath := filepath.Join(overlayDir, "test-cat", "test-pkg", "test-pkg-2.0.0.ebuild")
	if _, err := os.Stat(dstPath); os.IsNotExist(err) {
		t.Error("Expected destination ebuild to exist")
	}
}

// TestApplyManifestFailure tests that manifest failure sets status to failed
func TestApplyManifestFailure(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pkg := "test-cat/test-pkg"
	oldVersion := "1.0.0"
	newVersion := "2.0.0"

	// Create source ebuild
	createTestEbuildFile(t, overlayDir, pkg, oldVersion)

	pending, _ := NewPendingList(configDir)
	pending.Add(PendingUpdate{
		Package:        pkg,
		CurrentVersion: oldVersion,
		NewVersion:     newVersion,
		Status:         StatusPending,
	})

	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(mockExecCommandFailure),
	)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	result, _ := applier.Apply(pkg, false)

	if result.Success {
		t.Error("Expected failure when manifest fails")
	}

	// Verify status is failed
	update, _ := pending.Get(pkg)
	if update.Status != StatusFailed {
		t.Errorf("Expected status 'failed', got %q", update.Status)
	}
}

// TestApplyWithCompileUserDeclines tests that user declining compile returns error
func TestApplyWithCompileUserDeclines(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pkg := "test-cat/test-pkg"
	oldVersion := "1.0.0"
	newVersion := "2.0.0"

	// Create source ebuild
	createTestEbuildFile(t, overlayDir, pkg, oldVersion)

	pending, _ := NewPendingList(configDir)
	pending.Add(PendingUpdate{
		Package:        pkg,
		CurrentVersion: oldVersion,
		NewVersion:     newVersion,
		Status:         StatusPending,
	})

	// User declines confirmation
	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(mockExecCommandSuccess),
		WithConfirmFunc(func(prompt string) bool { return false }),
	)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	result, err := applier.Apply(pkg, true)

	if err != ErrUserDeclined {
		t.Errorf("Expected ErrUserDeclined, got: %v", err)
	}
	if result.Success {
		t.Error("Expected failure when user declines")
	}
}

// TestEbuildPath tests the EbuildPath helper function
func TestEbuildPath(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	applier, err := NewApplier(overlayDir, configDir)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	tests := []struct {
		pkg      string
		version  string
		expected string
	}{
		{
			pkg:      "test-cat/test-pkg",
			version:  "1.0.0",
			expected: filepath.Join(overlayDir, "test-cat", "test-pkg", "test-pkg-1.0.0.ebuild"),
		},
		{
			pkg:      "app-misc/hello",
			version:  "2.10",
			expected: filepath.Join(overlayDir, "app-misc", "hello", "hello-2.10.ebuild"),
		},
		{
			pkg:      "invalid",
			version:  "1.0.0",
			expected: "",
		},
	}

	for _, tt := range tests {
		result := applier.EbuildPath(tt.pkg, tt.version)
		if result != tt.expected {
			t.Errorf("EbuildPath(%q, %q) = %q, expected %q", tt.pkg, tt.version, result, tt.expected)
		}
	}
}

// TestApplyResultFields tests that ApplyResult has correct fields
func TestApplyResultFields(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pkg := "test-cat/test-pkg"
	oldVersion := "1.0.0"
	newVersion := "2.0.0"

	// Create source ebuild
	createTestEbuildFile(t, overlayDir, pkg, oldVersion)

	pending, _ := NewPendingList(configDir)
	pending.Add(PendingUpdate{
		Package:        pkg,
		CurrentVersion: oldVersion,
		NewVersion:     newVersion,
		Status:         StatusPending,
	})

	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(mockExecCommandSuccess),
	)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	result, err := applier.Apply(pkg, false)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if result.Package != pkg {
		t.Errorf("Expected package %q, got %q", pkg, result.Package)
	}
	if result.OldVersion != oldVersion {
		t.Errorf("Expected old version %q, got %q", oldVersion, result.OldVersion)
	}
	if result.NewVersion != newVersion {
		t.Errorf("Expected new version %q, got %q", newVersion, result.NewVersion)
	}
	if !result.Success {
		t.Error("Expected success to be true")
	}
	if result.Error != nil {
		t.Errorf("Expected no error, got: %v", result.Error)
	}
}

// TestSaveCompileLog tests that compile logs are saved correctly
func TestSaveCompileLog(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	applier, err := NewApplier(overlayDir, configDir)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	output := []byte("Test compile output\nError: something failed")
	logPath := applier.saveCompileLog("test-cat/test-pkg", "1.0.0", output)

	if logPath == "" {
		t.Fatal("Expected log path to be returned")
	}

	// Verify log file exists
	if _, err := os.Stat(logPath); os.IsNotExist(err) {
		t.Error("Expected log file to exist")
	}

	// Verify content
	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("Failed to read log file: %v", err)
	}

	if string(content) != string(output) {
		t.Errorf("Log content mismatch: expected %q, got %q", string(output), string(content))
	}
}
