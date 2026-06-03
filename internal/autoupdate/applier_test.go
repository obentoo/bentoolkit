package autoupdate

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
			// Real upgrades always have distinct versions; skip the degenerate
			// case where gopter happens to generate equal inputs.
			if oldVersion == newVersion {
				return true
			}

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
			// Real upgrades always have distinct versions; skip the degenerate
			// case where gopter happens to generate equal inputs.
			if oldVersion == newVersion {
				return true
			}

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

	// Property: Successful apply removes the pending entry (R3.1).
	// Predecessor: pre-R3.1, a successful apply left the entry with
	// StatusValidated. After R3.1 (story 002), the entry is deleted so
	// `--list` no longer shows successfully applied packages.
	properties.Property("Successful apply removes pending entry", prop.ForAll(
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

			// R3.1: pending entry is removed on successful apply.
			if pending.Has(pkg) {
				t.Logf("Pending entry for %s still present after successful apply (R3.1 violation)", pkg)
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

// mockExecCommandSuccess returns a mock exec.Cmd that always succeeds.
// It is context-aware so cancellation propagates to the spawned process.
func mockExecCommandSuccess(ctx context.Context, name string, arg ...string) *exec.Cmd {
	return exec.CommandContext(ctx, "true")
}

// mockExecCommandFailure returns a mock exec.Cmd that always fails.
// It is context-aware so cancellation propagates to the spawned process.
func mockExecCommandFailure(ctx context.Context, name string, arg ...string) *exec.Cmd {
	return exec.CommandContext(ctx, "false")
}

// mockExecCommandBlocking returns a mock exec.Cmd that blocks for an effectively
// unbounded time. Because it is created with exec.CommandContext, cancelling
// (or timing out) the supplied context kills the process, letting tests assert
// that the manifest timeout is honored.
func mockExecCommandBlocking(ctx context.Context, name string, arg ...string) *exec.Cmd {
	return exec.CommandContext(ctx, "sleep", "3600")
}

// mockExecCommandWriteInto returns a mock exec.Cmd factory whose command tries
// to create a file inside dir and exits non-zero when it cannot. Pointing dir
// at a read-only directory makes the manifest step fail with a genuine
// filesystem write error rather than a synthetic non-zero exit.
func mockExecCommandWriteInto(dir string) func(ctx context.Context, name string, arg ...string) *exec.Cmd {
	return func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		// `set -e` ensures the failed redirection aborts with a non-zero status.
		return exec.CommandContext(ctx, "sh", "-c", "set -e; : > \""+dir+"/Manifest\"")
	}
}

// mockExecCommandFailAndLockDir returns a mock exec.Cmd factory whose command
// makes dir read-only (0500) and then exits non-zero. It runs only after
// copyEbuild has already placed the orphan ebuild (with dir still writable),
// so the subsequent rollback os.Remove inside dir fails with EACCES. This lets
// tests exercise the R5.2 path where the manifest step AND the cleanup both
// fail.
func mockExecCommandFailAndLockDir(dir string) func(ctx context.Context, name string, arg ...string) *exec.Cmd {
	return func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", "chmod 0500 \""+dir+"\"; exit 1")
	}
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

// TestApplyStripsVersionPrefix verifies that a detected upstream version
// carrying a leading tag prefix (e.g. the git tag "v2.0.0") is normalized to a
// bare Gentoo PV before it reaches the ebuild filename. Without this, `ebuild
// manifest` rejects "test-pkg-v2.0.0.ebuild" with "does not follow correct
// package syntax".
func TestApplyStripsVersionPrefix(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pkg := "test-cat/test-pkg"
	oldVersion := "1.0.0"

	createTestEbuildFile(t, overlayDir, pkg, oldVersion)

	pending, _ := NewPendingList(configDir)
	pending.Add(PendingUpdate{
		Package:        pkg,
		CurrentVersion: oldVersion,
		NewVersion:     "v2.0.0", // upstream git tag prefix
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
		t.Fatalf("Expected success, got error: %v", result.Error)
	}

	// Result reflects the normalized (bare) version.
	if result.NewVersion != "2.0.0" {
		t.Errorf("Expected result.NewVersion %q, got %q", "2.0.0", result.NewVersion)
	}

	// The ebuild is written without the "v" prefix.
	dstPath := filepath.Join(overlayDir, "test-cat", "test-pkg", "test-pkg-2.0.0.ebuild")
	if _, err := os.Stat(dstPath); os.IsNotExist(err) {
		t.Error("Expected destination ebuild test-pkg-2.0.0.ebuild to exist")
	}
	if _, err := os.Stat(filepath.Join(overlayDir, "test-cat", "test-pkg", "test-pkg-v2.0.0.ebuild")); err == nil {
		t.Error("Did not expect a prefixed ebuild test-pkg-v2.0.0.ebuild to be created")
	}
}

// TestApplyRejectsInvalidNewVersion verifies that a NewVersion that is not a
// well-formed Gentoo version even after prefix stripping fails fast with
// ErrInvalidNewVersion instead of producing a broken ebuild filename.
func TestApplyRejectsInvalidNewVersion(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pkg := "test-cat/test-pkg"
	oldVersion := "1.0.0"

	createTestEbuildFile(t, overlayDir, pkg, oldVersion)

	pending, _ := NewPendingList(configDir)
	pending.Add(PendingUpdate{
		Package:        pkg,
		CurrentVersion: oldVersion,
		NewVersion:     "latest", // not a version
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
	if err == nil {
		t.Fatal("Expected error for invalid new version, got nil")
	}
	if !errors.Is(err, ErrInvalidNewVersion) {
		t.Errorf("Expected ErrInvalidNewVersion, got %v", err)
	}
	if result.Success {
		t.Error("Expected failure for invalid new version")
	}

	// No ebuild should have been written, and status must be failed.
	if _, statErr := os.Stat(filepath.Join(overlayDir, "test-cat", "test-pkg", "test-pkg-latest.ebuild")); statErr == nil {
		t.Error("Did not expect an ebuild to be created for an invalid version")
	}
	update, _ := pending.Get(pkg)
	if update.Status != StatusFailed {
		t.Errorf("Expected status 'failed', got %q", update.Status)
	}
}

// TestApplyCleanRemovesOldEbuild verifies that WithApplierClean makes a
// successful apply delete the previous version's ebuild, keep the new one, and
// report the removed version on the result.
func TestApplyCleanRemovesOldEbuild(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pkg := "test-cat/test-pkg"
	oldVersion := "1.0.0"
	newVersion := "2.0.0"

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
		WithApplierClean(true),
	)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	result, err := applier.Apply(pkg, false)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("Expected success, got error: %v", result.Error)
	}
	if result.CleanedOldVersion != oldVersion {
		t.Errorf("Expected CleanedOldVersion %q, got %q", oldVersion, result.CleanedOldVersion)
	}
	if result.CleanWarning != "" {
		t.Errorf("Expected no clean warning, got %q", result.CleanWarning)
	}

	pkgDir := filepath.Join(overlayDir, "test-cat", "test-pkg")
	if _, err := os.Stat(filepath.Join(pkgDir, "test-pkg-2.0.0.ebuild")); os.IsNotExist(err) {
		t.Error("Expected new ebuild test-pkg-2.0.0.ebuild to exist")
	}
	if _, err := os.Stat(filepath.Join(pkgDir, "test-pkg-1.0.0.ebuild")); err == nil {
		t.Error("Expected old ebuild test-pkg-1.0.0.ebuild to be removed")
	}
}

// TestApplyWithoutCleanKeepsOldEbuild verifies the default (clean off): both the
// old and new ebuilds remain and CleanedOldVersion stays empty.
func TestApplyWithoutCleanKeepsOldEbuild(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pkg := "test-cat/test-pkg"
	oldVersion := "1.0.0"
	newVersion := "2.0.0"

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
		t.Fatalf("Expected success, got error: %v", result.Error)
	}
	if result.CleanedOldVersion != "" {
		t.Errorf("Expected empty CleanedOldVersion, got %q", result.CleanedOldVersion)
	}

	pkgDir := filepath.Join(overlayDir, "test-cat", "test-pkg")
	if _, err := os.Stat(filepath.Join(pkgDir, "test-pkg-1.0.0.ebuild")); os.IsNotExist(err) {
		t.Error("Expected old ebuild test-pkg-1.0.0.ebuild to be kept when clean is off")
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

// TestSaveCompileLog_FinalModeIs0600 verifies that a compile log written by
// Applier.saveCompileLog ends up with owner-only (0600) permissions. The log
// is written via os.WriteFile, which applies the mode directly on creation.
func TestSaveCompileLog_FinalModeIs0600(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	applier, err := NewApplier(overlayDir, configDir)
	if err != nil {
		t.Fatalf("NewApplier failed: %v", err)
	}

	output := []byte("compile output\nError: build failed")
	logPath := applier.saveCompileLog("test-cat/test-pkg", "1.0.0", output)
	if logPath == "" {
		t.Fatal("expected a non-empty log path")
	}

	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("os.Stat on compile log failed: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("compile log mode = %#o, want %#o", got, 0o600)
	}
}

// =============================================================================
// R5: Applier rollback on manifest failure + exec timeout
// =============================================================================

// TestApply_RollbackOnManifestFailure verifies that when runManifest fails, the
// orphan .ebuild that copyEbuild placed in the overlay is removed so the
// overlay is not left half-applied. (R5.1)
func TestApply_RollbackOnManifestFailure(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pkg := "test-cat/test-pkg"
	oldVersion := "1.0.0"
	newVersion := "2.0.0"

	createTestEbuildFile(t, overlayDir, pkg, oldVersion)

	pending, _ := NewPendingList(configDir)
	pending.Add(PendingUpdate{
		Package:        pkg,
		CurrentVersion: oldVersion,
		NewVersion:     newVersion,
		Status:         StatusPending,
	})

	// mockExecCommandFailure makes runManifest fail (non-zero exit).
	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(mockExecCommandFailure),
	)
	if err != nil {
		t.Fatalf("NewApplier failed: %v", err)
	}

	result, applyErr := applier.Apply(pkg, false)
	if applyErr == nil {
		t.Fatal("expected Apply to fail when manifest fails")
	}
	if result.Success {
		t.Error("expected result.Success to be false")
	}

	// The freshly copied ebuild must have been rolled back.
	dstPath := applier.EbuildPath(pkg, newVersion)
	if _, statErr := os.Stat(dstPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("orphan ebuild was not rolled back: os.Stat(%q) error = %v, want os.ErrNotExist",
			dstPath, statErr)
	}

	// The source ebuild must remain untouched.
	srcPath := applier.EbuildPath(pkg, oldVersion)
	if _, statErr := os.Stat(srcPath); statErr != nil {
		t.Errorf("source ebuild should still exist: os.Stat(%q) error = %v", srcPath, statErr)
	}
}

// TestApply_RollbackPreservesOriginalError verifies that when BOTH the manifest
// step and the rollback removal fail, Apply still surfaces the original
// ErrManifestFailed error and never substitutes the os.Remove error. (R5.2)
func TestApply_RollbackPreservesOriginalError(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pkg := "test-cat/test-pkg"
	oldVersion := "1.0.0"
	newVersion := "2.0.0"

	createTestEbuildFile(t, overlayDir, pkg, oldVersion)

	pending, _ := NewPendingList(configDir)
	pending.Add(PendingUpdate{
		Package:        pkg,
		CurrentVersion: oldVersion,
		NewVersion:     newVersion,
		Status:         StatusPending,
	})

	// The ebuild's package directory. copyEbuild needs it writable to create
	// the destination file, so it cannot be chmod'd read-only before Apply
	// runs (that would fail copyEbuild and never create an orphan). Instead the
	// mocked manifest command makes the directory read-only and THEN exits
	// non-zero: copyEbuild has already succeeded, so the deferred rollback
	// os.Remove inside the now-read-only directory fails with EACCES. Restore
	// the mode afterwards so t.TempDir cleanup can delete the tree.
	pkgDir := filepath.Join(overlayDir, "test-cat", "test-pkg")
	t.Cleanup(func() { _ = os.Chmod(pkgDir, 0o755) })

	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(mockExecCommandFailAndLockDir(pkgDir)),
	)
	if err != nil {
		t.Fatalf("NewApplier failed: %v", err)
	}

	result, applyErr := applier.Apply(pkg, false)
	if applyErr == nil {
		t.Fatal("expected Apply to fail when manifest fails")
	}
	if result.Success {
		t.Error("expected result.Success to be false")
	}

	// The returned error must wrap the ORIGINAL manifest failure, not the
	// cleanup os.Remove error.
	if !errors.Is(applyErr, ErrManifestFailed) {
		t.Errorf("Apply error = %v, want it to wrap ErrManifestFailed", applyErr)
	}
	if !errors.Is(result.Error, ErrManifestFailed) {
		t.Errorf("result.Error = %v, want it to wrap ErrManifestFailed", result.Error)
	}
	// A permission-denied removal error must not have leaked into the result.
	if errors.Is(applyErr, os.ErrPermission) {
		t.Errorf("Apply error leaked the cleanup os.Remove error: %v", applyErr)
	}
}

// TestApply_ManifestTimeoutHonored verifies that the manifest invocation is
// bounded: with a blocking manifest process and a short parent-context
// deadline, Apply aborts promptly instead of hanging. The 5-minute manifest
// timeout derives a child from a.ctx, so a shorter parent deadline is
// inherited and wins. (R5.3)
func TestApply_ManifestTimeoutHonored(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pkg := "test-cat/test-pkg"
	oldVersion := "1.0.0"
	newVersion := "2.0.0"

	createTestEbuildFile(t, overlayDir, pkg, oldVersion)

	pending, _ := NewPendingList(configDir)
	pending.Add(PendingUpdate{
		Package:        pkg,
		CurrentVersion: oldVersion,
		NewVersion:     newVersion,
		Status:         StatusPending,
	})

	// Parent context with a ~100ms deadline. context.WithTimeout(a.ctx,
	// manifestTimeout) inside runManifest inherits this shorter deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(mockExecCommandBlocking),
		WithApplierContext(ctx),
	)
	if err != nil {
		t.Fatalf("NewApplier failed: %v", err)
	}

	done := make(chan error, 1)
	start := time.Now()
	go func() {
		_, applyErr := applier.Apply(pkg, false)
		done <- applyErr
	}()

	select {
	case applyErr := <-done:
		elapsed := time.Since(start)
		if applyErr == nil {
			t.Fatal("expected Apply to fail when the manifest exec times out")
		}
		if !errors.Is(applyErr, ErrManifestFailed) {
			t.Errorf("Apply error = %v, want it to wrap ErrManifestFailed", applyErr)
		}
		if elapsed > 2*time.Second {
			t.Errorf("Apply took %v, want it to abort promptly after the ~100ms deadline", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Apply did not return: manifest exec timeout was not honored")
	}

	// The orphan ebuild must still have been rolled back on this failure path.
	dstPath := applier.EbuildPath(pkg, newVersion)
	if _, statErr := os.Stat(dstPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("orphan ebuild was not rolled back after timeout: os.Stat(%q) error = %v, want os.ErrNotExist",
			dstPath, statErr)
	}
}

// mockExecCommandHybrid returns a factory whose behavior depends on the
// invoked command. "ebuild" calls (manifest) succeed instantly via /bin/true;
// any other call (e.g. sudo/doas for the compile path) blocks under sleep, so
// only the compile step is cancellable by context. This lets a single test
// exercise context cancellation during the compile step without interference
// from the manifest step.
func mockExecCommandHybrid(ctx context.Context, name string, arg ...string) *exec.Cmd {
	if name == "ebuild" {
		return exec.CommandContext(ctx, "true")
	}
	return exec.CommandContext(ctx, "sleep", "3600")
}

// TestApply_CancelsOnContextCancellation_Manifest verifies R1.1, R1.3:
// cancelling the WithApplierContext parent while runManifest is blocked in the
// spawned process aborts Apply within ~2 s, surfaces a context-derived error
// in result.Error, and rolls back the orphan ebuild placed by copyEbuild.
func TestApply_CancelsOnContextCancellation_Manifest(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pkg := "test-cat/test-pkg"
	oldVersion := "1.0.0"
	newVersion := "2.0.0"

	createTestEbuildFile(t, overlayDir, pkg, oldVersion)

	pending, _ := NewPendingList(configDir)
	pending.Add(PendingUpdate{
		Package:        pkg,
		CurrentVersion: oldVersion,
		NewVersion:     newVersion,
		Status:         StatusPending,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(mockExecCommandBlocking),
		WithApplierContext(ctx),
	)
	if err != nil {
		t.Fatalf("NewApplier failed: %v", err)
	}

	done := make(chan error, 1)
	resCh := make(chan *ApplyResult, 1)
	go func() {
		r, applyErr := applier.Apply(pkg, false)
		resCh <- r
		done <- applyErr
	}()

	// Give the spawned `sleep 3600` a beat to actually start under runManifest.
	time.Sleep(100 * time.Millisecond)
	cancelAt := time.Now()
	cancel()

	select {
	case applyErr := <-done:
		elapsed := time.Since(cancelAt)
		if applyErr == nil {
			t.Fatal("expected Apply to fail when parent context is cancelled")
		}
		if elapsed > 2*time.Second {
			t.Errorf("Apply returned %v after cancel; want <= 2s (R1.1)", elapsed)
		}
		result := <-resCh
		if result == nil {
			t.Fatal("expected non-nil result")
		}
		if result.Error == nil {
			t.Error("expected result.Error to be set after cancellation")
		}
		// The underlying failure must be reachable as ErrManifestFailed or a
		// context error — proves the cancellation propagated through
		// exec.CommandContext (not e.g. a panic).
		if !errors.Is(applyErr, ErrManifestFailed) &&
			!errors.Is(applyErr, context.Canceled) {
			t.Errorf("Apply error = %v, want ErrManifestFailed or context.Canceled", applyErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Apply did not return within 5s of cancel; context cancellation is not propagating")
	}

	// The orphan ebuild must have been rolled back (R1.3).
	dstPath := applier.EbuildPath(pkg, newVersion)
	if _, statErr := os.Stat(dstPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("orphan ebuild not rolled back after cancellation: os.Stat(%q) error = %v, want os.ErrNotExist",
			dstPath, statErr)
	}
}

// TestApply_CancelsOnContextCancellation_Compile verifies R1.2, R1.3:
// cancelling the WithApplierContext parent while runCompile is blocked in the
// elevated child aborts Apply within ~2 s and the orphan ebuild is rolled
// back. Manifest succeeds fast; only the compile step blocks under the cancel.
//
// Skipped when neither sudo nor doas is on PATH (e.g. minimal CI images), since
// runCompile fails fast with ErrNoPrivilegeEscalation before any cancellation
// can be observed.
func TestApply_CancelsOnContextCancellation_Compile(t *testing.T) {
	if _, err := exec.LookPath("sudo"); err != nil {
		if _, err := exec.LookPath("doas"); err != nil {
			t.Skip("neither sudo nor doas on PATH; cannot exercise compile cancellation")
		}
	}

	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pkg := "test-cat/test-pkg"
	oldVersion := "1.0.0"
	newVersion := "2.0.0"

	createTestEbuildFile(t, overlayDir, pkg, oldVersion)

	pending, _ := NewPendingList(configDir)
	pending.Add(PendingUpdate{
		Package:        pkg,
		CurrentVersion: oldVersion,
		NewVersion:     newVersion,
		Status:         StatusPending,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(mockExecCommandHybrid),
		WithApplierContext(ctx),
		WithConfirmFunc(func(prompt string) bool { return true }),
	)
	if err != nil {
		t.Fatalf("NewApplier failed: %v", err)
	}

	done := make(chan error, 1)
	resCh := make(chan *ApplyResult, 1)
	go func() {
		r, applyErr := applier.Apply(pkg, true) // compile=true
		resCh <- r
		done <- applyErr
	}()

	// Wait for manifest to complete and compile to start blocking.
	time.Sleep(200 * time.Millisecond)
	cancelAt := time.Now()
	cancel()

	select {
	case applyErr := <-done:
		elapsed := time.Since(cancelAt)
		if applyErr == nil {
			t.Fatal("expected Apply to fail when parent context is cancelled during compile")
		}
		if elapsed > 2*time.Second {
			t.Errorf("Apply returned %v after cancel; want <= 2s (R1.2)", elapsed)
		}
		result := <-resCh
		if result == nil {
			t.Fatal("expected non-nil result")
		}
		if result.Error == nil {
			t.Error("expected result.Error to be set after cancellation")
		}
		if !errors.Is(applyErr, ErrCompileFailed) &&
			!errors.Is(applyErr, context.Canceled) {
			t.Errorf("Apply error = %v, want ErrCompileFailed or context.Canceled", applyErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Apply did not return within 5s of cancel; compile cancellation is not propagating")
	}

	// On compile failure, runCompile returns an error and the deferred
	// rollback fires keyed on result.Error != nil, so the orphan must be gone.
	dstPath := applier.EbuildPath(pkg, newVersion)
	if _, statErr := os.Stat(dstPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("orphan ebuild not rolled back after compile cancellation: os.Stat(%q) error = %v, want os.ErrNotExist",
			dstPath, statErr)
	}
}

// =============================================================================
// R3: pending list lifecycle after --apply (T3.1)
// =============================================================================

// TestApply_DeletesPendingOnSuccess verifies R3.1: a successful Apply removes
// the package from pending.json so `--list` no longer shows it.
func TestApply_DeletesPendingOnSuccess(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pkg := "test-cat/test-pkg"
	oldVersion := "1.0.0"
	newVersion := "2.0.0"

	createTestEbuildFile(t, overlayDir, pkg, oldVersion)

	pending, _ := NewPendingList(configDir)
	if err := pending.Add(PendingUpdate{
		Package:        pkg,
		CurrentVersion: oldVersion,
		NewVersion:     newVersion,
		Status:         StatusPending,
	}); err != nil {
		t.Fatalf("pending.Add: %v", err)
	}

	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(mockExecCommandSuccess),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}

	result, err := applier.Apply(pkg, false)
	if err != nil {
		t.Fatalf("Apply unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("Apply.Success = false, want true (result.Error = %v)", result.Error)
	}

	if pending.Has(pkg) {
		t.Errorf("pending still contains %s after successful Apply; want it removed (R3.1)", pkg)
	}
}

// TestApply_RetainsPendingOnManifestFailure verifies R3.2: a failed manifest
// leaves the pending entry in place (status=failed, error set) so the user
// can retry. Also re-asserts R1.3 rollback to keep the contract explicit.
func TestApply_RetainsPendingOnManifestFailure(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pkg := "test-cat/test-pkg"
	oldVersion := "1.0.0"
	newVersion := "2.0.0"

	createTestEbuildFile(t, overlayDir, pkg, oldVersion)

	pending, _ := NewPendingList(configDir)
	if err := pending.Add(PendingUpdate{
		Package:        pkg,
		CurrentVersion: oldVersion,
		NewVersion:     newVersion,
		Status:         StatusPending,
	}); err != nil {
		t.Fatalf("pending.Add: %v", err)
	}

	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(mockExecCommandFailure),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}

	result, _ := applier.Apply(pkg, false)
	if result.Success {
		t.Fatal("Apply.Success = true, want false on manifest failure")
	}

	if !pending.Has(pkg) {
		t.Errorf("pending lost %s after manifest failure; want it retained (R3.2)", pkg)
	}
	update, _ := pending.Get(pkg)
	if update.Status != StatusFailed {
		t.Errorf("pending status = %q, want %q (R3.2)", update.Status, StatusFailed)
	}
	if update.Error == "" {
		t.Error("pending entry Error string empty after failure (R3.2)")
	}
	if _, statErr := os.Stat(applier.EbuildPath(pkg, newVersion)); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("orphan ebuild not rolled back on manifest failure (R1.3)")
	}
}

// TestApply_RetainsPendingOnCompileFailure verifies R3.2 for the compile path:
// manifest succeeds, compile fails — the pending entry stays with status=failed.
// Skipped when neither sudo nor doas is on PATH.
func TestApply_RetainsPendingOnCompileFailure(t *testing.T) {
	if _, err := exec.LookPath("sudo"); err != nil {
		if _, err := exec.LookPath("doas"); err != nil {
			t.Skip("neither sudo nor doas on PATH; cannot exercise compile path")
		}
	}

	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pkg := "test-cat/test-pkg"
	oldVersion := "1.0.0"
	newVersion := "2.0.0"

	createTestEbuildFile(t, overlayDir, pkg, oldVersion)

	pending, _ := NewPendingList(configDir)
	if err := pending.Add(PendingUpdate{
		Package:        pkg,
		CurrentVersion: oldVersion,
		NewVersion:     newVersion,
		Status:         StatusPending,
	}); err != nil {
		t.Fatalf("pending.Add: %v", err)
	}

	// ebuild → success (manifest); anything else (sudo/doas → compile) → failure.
	hybridManifestOKCompileFail := func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		if name == "ebuild" {
			return exec.CommandContext(ctx, "true")
		}
		return exec.CommandContext(ctx, "false")
	}

	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(hybridManifestOKCompileFail),
		WithConfirmFunc(func(prompt string) bool { return true }),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}

	result, _ := applier.Apply(pkg, true) // compile=true
	if result.Success {
		t.Fatal("Apply.Success = true, want false on compile failure")
	}

	if !pending.Has(pkg) {
		t.Errorf("pending lost %s after compile failure; want it retained (R3.2)", pkg)
	}
	update, _ := pending.Get(pkg)
	if update.Status != StatusFailed {
		t.Errorf("pending status = %q, want %q (R3.2)", update.Status, StatusFailed)
	}
}

// TestApply_DeleteAfterSuccessFailure_LogsWarnButSucceeds verifies R3.4: if
// the final pending.Delete call returns an error AFTER the apply itself
// succeeded, the result keeps Success=true and a Warn line is emitted via the
// package warnLogf sink — the exit-code path must not flip on a bookkeeping
// failure that does not undo the actual update.
func TestApply_DeleteAfterSuccessFailure_LogsWarnButSucceeds(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pkg := "test-cat/test-pkg"
	oldVersion := "1.0.0"
	newVersion := "2.0.0"

	createTestEbuildFile(t, overlayDir, pkg, oldVersion)

	pending, _ := NewPendingList(configDir)
	if err := pending.Add(PendingUpdate{
		Package:        pkg,
		CurrentVersion: oldVersion,
		NewVersion:     newVersion,
		Status:         StatusPending,
	}); err != nil {
		t.Fatalf("pending.Add: %v", err)
	}

	wantErr := errors.New("synthetic delete failure")
	deleteCalled := 0
	deleteFn := func(p string) error {
		deleteCalled++
		if p != pkg {
			t.Errorf("delete called with %q, want %q", p, pkg)
		}
		return wantErr
	}

	logs := captureWarnLogs(t)

	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(mockExecCommandSuccess),
		WithApplierPendingDeleteFunc(deleteFn),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}

	result, applyErr := applier.Apply(pkg, false)
	if applyErr != nil {
		t.Fatalf("Apply unexpected error: %v", applyErr)
	}
	if !result.Success {
		t.Fatalf("Apply.Success = false, want true even when delete fails (R3.4); result.Error = %v", result.Error)
	}
	if result.Error != nil {
		t.Errorf("result.Error = %v, want nil (R3.4)", result.Error)
	}
	if deleteCalled != 1 {
		t.Errorf("delete called %d times, want 1", deleteCalled)
	}
	if logs.count() == 0 {
		t.Errorf("no Warn emitted via warnLogf after delete failure (R3.4)")
	}
	joined := strings.Join(logs.all(), "\n")
	if !strings.Contains(joined, pkg) {
		t.Errorf("Warn lines do not mention package %q: %v", pkg, logs.all())
	}
}

// TestApply_RollbackOnManifestWriteFailure verifies the rollback when the
// manifest step fails because of a filesystem write error (rather than a plain
// non-zero exit). The injected manifest command writes a Manifest file into a
// read-only sibling directory; that write fails, so the manifest step fails,
// and the orphan ebuild must be rolled back. The ebuild's OWN directory stays
// writable, so this exercises R5.1 (successful rollback) and not R5.2.
//
// Approach (sub-task 10.4): the design suggested chmod'ing a directory 0500
// after copyEbuild. Making the ebuild's own package directory read-only would
// also block the rollback os.Remove and turn this into an R5.2 test, so
// instead a separate sub-path (<pkgDir>/ro) is made read-only and the manifest
// command is pointed at it. The package directory keeps mode 0755 so the
// rollback os.Remove of the orphan ebuild still succeeds.
func TestApply_RollbackOnManifestWriteFailure(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pkg := "test-cat/test-pkg"
	oldVersion := "1.0.0"
	newVersion := "2.0.0"

	createTestEbuildFile(t, overlayDir, pkg, oldVersion)

	pending, _ := NewPendingList(configDir)
	pending.Add(PendingUpdate{
		Package:        pkg,
		CurrentVersion: oldVersion,
		NewVersion:     newVersion,
		Status:         StatusPending,
	})

	// Create a read-only sibling directory; the mocked manifest command will
	// try (and fail) to write into it. Restore the mode for t.TempDir cleanup.
	roDir := filepath.Join(overlayDir, "test-cat", "test-pkg", "ro")
	if mkErr := os.MkdirAll(roDir, 0o755); mkErr != nil {
		t.Fatalf("failed to create read-only dir: %v", mkErr)
	}
	if chErr := os.Chmod(roDir, 0o500); chErr != nil {
		t.Fatalf("failed to chmod read-only dir: %v", chErr)
	}
	t.Cleanup(func() { _ = os.Chmod(roDir, 0o755) })

	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(mockExecCommandWriteInto(roDir)),
	)
	if err != nil {
		t.Fatalf("NewApplier failed: %v", err)
	}

	result, applyErr := applier.Apply(pkg, false)
	if applyErr == nil {
		t.Fatal("expected Apply to fail when the manifest write fails")
	}
	if result.Success {
		t.Error("expected result.Success to be false")
	}
	if !errors.Is(applyErr, ErrManifestFailed) {
		t.Errorf("Apply error = %v, want it to wrap ErrManifestFailed", applyErr)
	}

	// The ebuild's package directory is still writable, so the orphan ebuild
	// must have been removed.
	dstPath := applier.EbuildPath(pkg, newVersion)
	if _, statErr := os.Stat(dstPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("orphan ebuild was not rolled back: os.Stat(%q) error = %v, want os.ErrNotExist",
			dstPath, statErr)
	}
}
