package autoupdate

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"

	"github.com/obentoo/bentoolkit/internal/common/ebuild"
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
			// These properties model a genuine upgrade. Skip when gopter
			// generates a newVersion that is not strictly greater than
			// oldVersion: the applier now treats that as an obsolete no-op
			// (the entry is pruned), which the dedicated obsolete tests cover.
			if ebuild.CompareVersions(newVersion, oldVersion) <= 0 {
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
			// These properties model a genuine upgrade. Skip when gopter
			// generates a newVersion that is not strictly greater than
			// oldVersion: the applier now treats that as an obsolete no-op
			// (the entry is pruned), which the dedicated obsolete tests cover.
			if ebuild.CompareVersions(newVersion, oldVersion) <= 0 {
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
			// Genuine upgrade only; a non-strict-greater newVersion is now an
			// obsolete no-op (pruned), covered by the dedicated obsolete tests.
			if ebuild.CompareVersions(newVersion, oldVersion) <= 0 {
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
			// A non-strict-greater newVersion is pruned as obsolete before the
			// manifest runs, so the failed-manifest path requires a real upgrade.
			if ebuild.CompareVersions(newVersion, oldVersion) <= 0 {
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

	// The recorded current_version (1.0.0) has no ebuild in the overlay, and no
	// other ebuild exists either: the entry is stale/obsolete. Apply now prunes
	// it rather than failing with a cryptic "source ebuild not found".
	result, err := applier.Apply("test-cat/test-pkg", false)
	if err != nil {
		t.Errorf("Expected no error for obsolete entry, got: %v", err)
	}
	if result.Success {
		t.Error("Expected result.Success to be false")
	}
	if !result.Obsolete {
		t.Error("Expected result.Obsolete to be true")
	}

	// The obsolete entry is pruned from pending.
	if _, found := pending.Get("test-cat/test-pkg"); found {
		t.Error("Expected obsolete pending entry to be pruned")
	}
}

// TestApplyObsoleteOverlayAlreadyAhead covers a pending entry whose target is
// already met: the overlay carries a version >= the pending new_version (e.g.
// the package was bumped further by hand since the check ran). Apply prunes it
// as obsolete instead of attempting a pointless/downgrading copy.
func TestApplyObsoleteOverlayAlreadyAhead(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pkg := "test-cat/test-pkg"
	// Overlay is already at 0.3.16; the pending entry still targets 0.3.11.
	createTestEbuildFile(t, overlayDir, pkg, "0.3.16")

	pending, _ := NewPendingList(configDir)
	pending.Add(PendingUpdate{
		Package:        pkg,
		CurrentVersion: "0.3.10",
		NewVersion:     "0.3.11",
		Status:         StatusPending,
	})

	applier, err := NewApplier(overlayDir, configDir, WithApplierPendingList(pending))
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	result, err := applier.Apply(pkg, false)
	if err != nil {
		t.Errorf("Expected no error for obsolete entry, got: %v", err)
	}
	if result.Success || !result.Obsolete {
		t.Errorf("Expected obsolete, non-success result; got Success=%v Obsolete=%v", result.Success, result.Obsolete)
	}
	if _, found := pending.Get(pkg); found {
		t.Error("Expected obsolete pending entry to be pruned")
	}
}

// TestApplyHealsStaleCurrentVersion covers a pending entry whose recorded
// current_version is stale (its ebuild is gone) but a newer one still exists in
// the overlay below the target. Apply re-resolves the source from the live
// overlay version instead of failing.
func TestApplyHealsStaleCurrentVersion(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pkg := "test-cat/test-pkg"
	// Pending says current is 1.0.0, but the overlay actually holds 1.5.0.
	createTestEbuildFile(t, overlayDir, pkg, "1.5.0")

	pending, _ := NewPendingList(configDir)
	pending.Add(PendingUpdate{
		Package:        pkg,
		CurrentVersion: "1.0.0",
		NewVersion:     "2.0.0",
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
		t.Fatalf("Expected self-healed apply to succeed, got: %v", err)
	}
	if !result.Success {
		t.Errorf("Expected Success; result.Error=%v", result.Error)
	}
	// OldVersion reflects the live overlay version, not the stale 1.0.0.
	if result.OldVersion != "1.5.0" {
		t.Errorf("Expected OldVersion resolved to 1.5.0, got %q", result.OldVersion)
	}
	// The new ebuild was copied from the live 1.5.0 source.
	dstPath := filepath.Join(overlayDir, "test-cat", "test-pkg", "test-pkg-2.0.0.ebuild")
	if _, err := os.Stat(dstPath); os.IsNotExist(err) {
		t.Errorf("Expected destination ebuild %s to exist", dstPath)
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
//
// The registry entry is what authorises the sweep: cleanPackageDir plans against
// packages.toml, and a directory no entry claims is never touched. Production
// always supplies one (every --apply path passes WithApplierPackagesConfig), so
// this fixture supplies one too.
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
		WithApplierPackagesConfig(&PackagesConfig{Packages: map[string]PackageConfig{
			pkg: regEntry("", ""),
		}}),
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

	// Signal deterministically when the compile step starts instead of guessing
	// with a fixed sleep: a slow/loaded runner may not have reached the manifest
	// step within the sleep window, so cancelling then would abort the manifest
	// (with context canceled) rather than the compile this test targets. The
	// factory closes compileStarted the first time it is asked for a non-pkgdev
	// (compile) command — which only happens after runManifest has returned.
	compileStarted := make(chan struct{})
	var once sync.Once
	execFn := func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		if name == "pkgdev" {
			return exec.CommandContext(ctx, "true") // manifest: instant success
		}
		once.Do(func() { close(compileStarted) })
		return exec.CommandContext(ctx, "sleep", "3600") // compile: blocks until cancel
	}

	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(execFn),
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

	// Cancel only once the compile step has actually started, guaranteeing the
	// manifest already completed so the cancellation hits the compile.
	select {
	case <-compileStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("compile step did not start within 5s")
	}
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

	// pkgdev → success (manifest); anything else (sudo/doas → compile) → failure.
	hybridManifestOKCompileFail := func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		if name == "pkgdev" {
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

// TestApplyRefusesHeldPackage covers the guard that makes hold = true mean
// something at apply time. The checker skips held packages, but an explicit
// `--check <pkg> --force` bypasses that filter and records the update, so a held
// entry can be sitting in pending.json when `--apply all` runs. Before the
// guard, that entry was applied like any other — the bump the hold existed to
// prevent. The refusal is not a failure (Error nil) and, unlike an obsolete
// entry, the pending record is KEPT: the update is real, just not automatable.
func TestApplyRefusesHeldPackage(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pkg := "test-cat/held-pkg"
	createTestEbuildFile(t, overlayDir, pkg, "1.0.0")

	pending, _ := NewPendingList(configDir)
	pending.Add(PendingUpdate{
		Package:        pkg,
		CurrentVersion: "1.0.0",
		NewVersion:     "1.1.0",
		Status:         StatusPending,
	})

	cfg := &PackagesConfig{Packages: map[string]PackageConfig{
		pkg: {Hold: true, URL: "https://example.invalid/", Parser: "json", Path: "tag_name"},
	}}

	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithApplierPackagesConfig(cfg),
	)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	result, err := applier.Apply(pkg, false)
	if err != nil {
		t.Errorf("Expected no error for a held package, got: %v", err)
	}
	if !result.Held {
		t.Error("Expected result.Held to be true")
	}
	if result.Success {
		t.Error("Expected Success to stay false for a held package")
	}
	if result.OldVersion != "1.0.0" || result.NewVersion != "1.1.0" {
		t.Errorf("Expected the pending versions to be reported; got %q → %q", result.OldVersion, result.NewVersion)
	}
	if _, found := pending.Get(pkg); !found {
		t.Error("Expected the held pending entry to be KEPT, not pruned")
	}
	// The refusal must happen before any filesystem work: no new ebuild.
	if _, err := os.Stat(filepath.Join(overlayDir, pkg, "held-pkg-1.1.0.ebuild")); !os.IsNotExist(err) {
		t.Error("Expected no ebuild to be written for a held package")
	}
}

// TestApplyUnheldPackageStillApplies is the counterpart: an entry whose config
// exists but carries no hold must be unaffected by the guard.
func TestApplyUnheldPackageStillApplies(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pkg := "test-cat/free-pkg"
	createTestEbuildFile(t, overlayDir, pkg, "1.0.0")

	pending, _ := NewPendingList(configDir)
	pending.Add(PendingUpdate{
		Package:        pkg,
		CurrentVersion: "1.0.0",
		NewVersion:     "1.1.0",
		Status:         StatusPending,
	})

	cfg := &PackagesConfig{Packages: map[string]PackageConfig{
		pkg: {URL: "https://example.invalid/", Parser: "json", Path: "tag_name"},
	}}

	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithApplierPackagesConfig(cfg),
	)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	result, _ := applier.Apply(pkg, false)
	if result.Held {
		t.Error("Expected Held to be false for a package with no hold")
	}
}

// =============================================================================
// --clean Sweep Tests (S021 sub-task 4.1: cleanPackageDir)
// =============================================================================

// countingManifestSeam returns an exec seam where every command succeeds and
// each `pkgdev` invocation is counted. `pkgdev manifest` is the Manifest
// regeneration (runManifest shells out to it), so counting at the seam the
// applier already exposes is how "regenerated exactly once" (R4.2) becomes
// observable — no second seam is invented for the test. The counter is atomic
// so -race is satisfied whatever goroutine exec is driven from.
func countingManifestSeam(n *atomic.Int64) func(ctx context.Context, name string, arg ...string) *exec.Cmd {
	return func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		if name == "pkgdev" {
			n.Add(1)
		}
		return exec.CommandContext(ctx, "true")
	}
}

// mockExecCommandLockDirAndSucceed returns an exec seam whose command makes dir
// read-only (0500) and then exits ZERO. Unlike mockExecCommandFailAndLockDir it
// lets the apply succeed: copyEbuild has already written the new ebuild and the
// manifest step passes, so the failure lands where the test wants it — on the
// --clean sweep's os.Remove inside a directory it can no longer write to.
func mockExecCommandLockDirAndSucceed(dir string) func(ctx context.Context, name string, arg ...string) *exec.Cmd {
	return func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", "chmod 0500 \""+dir+"\"; exit 0")
	}
}

// TestCleanPackageDirSweepsResidueAndRegeneratesManifestOnce covers the ordinary
// single-entry directory (R4.1, R4.2): everything the entry's pin does not claim
// goes, the live ebuild stays whatever the pins say, and the Manifest is
// regenerated exactly once — after the last removal, not once per file.
//
// The pin under test is the OVERLAID one: the fixture entry has no version, and
// the sweep is nonetheless authorised because the version just applied is what
// that entry now keeps. That is the whole reason the feature is not dead on
// arrival against today's pinless registry.
func TestCleanPackageDirSweepsResidueAndRegeneratesManifestOnce(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	pkg := "test-cat/test-pkg"

	for _, v := range []string{"1.0.0", "1.5.0", "2.0.0", "9999"} {
		createTestEbuildFile(t, overlayDir, pkg, v)
	}

	var manifestRuns atomic.Int64
	applier, err := NewApplier(overlayDir, filepath.Join(tmpDir, "config"),
		WithExecCommand(countingManifestSeam(&manifestRuns)),
		WithApplierPackagesConfig(&PackagesConfig{Packages: map[string]PackageConfig{
			pkg: regEntry("", ""),
		}}),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}

	plan, err := applier.cleanPackageDir(pkg, "2.0.0")
	if err != nil {
		t.Fatalf("cleanPackageDir: %v", err)
	}

	if !reflect.DeepEqual(plan.Remove, []string{"1.0.0", "1.5.0"}) {
		t.Errorf("Remove = %v, want [1.0.0 1.5.0]", plan.Remove)
	}
	wantKeep := map[string]string{"2.0.0": pkg, "9999": ""}
	if !reflect.DeepEqual(plan.Keep, wantKeep) {
		t.Errorf("Keep = %v, want %v", plan.Keep, wantKeep)
	}

	pkgDir := filepath.Join(overlayDir, "test-cat", "test-pkg")
	for _, gone := range []string{"test-pkg-1.0.0.ebuild", "test-pkg-1.5.0.ebuild"} {
		if _, err := os.Stat(filepath.Join(pkgDir, gone)); !os.IsNotExist(err) {
			t.Errorf("%s should have been swept: os.Stat error = %v", gone, err)
		}
	}
	for _, kept := range []string{"test-pkg-2.0.0.ebuild", "test-pkg-9999.ebuild"} {
		if _, err := os.Stat(filepath.Join(pkgDir, kept)); err != nil {
			t.Errorf("%s must survive the sweep: %v", kept, err)
		}
	}

	// R4.2: one regeneration for the whole sweep, not one per removed file.
	if got := manifestRuns.Load(); got != 1 {
		t.Errorf("the Manifest was regenerated %d time(s), want exactly 1", got)
	}

	// The overlaid pin must live in the sweep's own copy of the registry: the
	// applier's map is shared state the rest of Apply reads (hold, revision,
	// series, [meta]), and a version written into it here would outlive the call.
	if v := applier.configs[pkg].Version; v != "" {
		t.Errorf("cleanPackageDir mutated a.configs: %s pin = %q, want it untouched", pkg, v)
	}
}

// TestCleanPackageDirNoRemovalSkipsManifest is R4.2's other half: with nothing
// to remove, the Manifest is not regenerated at all. Rewriting it would re-fetch
// and re-digest every distfile of an untouched directory.
func TestCleanPackageDirNoRemovalSkipsManifest(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	pkg := "test-cat/test-pkg"
	createTestEbuildFile(t, overlayDir, pkg, "2.0.0")

	var manifestRuns atomic.Int64
	applier, err := NewApplier(overlayDir, filepath.Join(tmpDir, "config"),
		WithExecCommand(countingManifestSeam(&manifestRuns)),
		WithApplierPackagesConfig(&PackagesConfig{Packages: map[string]PackageConfig{
			pkg: regEntry("", ""),
		}}),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}

	plan, err := applier.cleanPackageDir(pkg, "2.0.0")
	if err != nil {
		t.Fatalf("cleanPackageDir: %v", err)
	}
	if len(plan.Remove) != 0 {
		t.Errorf("Remove = %v, want nothing removed", plan.Remove)
	}
	if got := manifestRuns.Load(); got != 0 {
		t.Errorf("the Manifest was regenerated %d time(s) with nothing removed, want 0", got)
	}
}

// TestCleanPackageDirBlockedByPinlessSibling is the guard over the overlay's 89
// deliberate multi-entry directories (UB3, R5.1, R6.2).
//
// The bumped entry gets its pin from the apply, but its SIBLING — the other
// release line, sharing the directory — has none, and one pinless claim blocks
// the whole directory. WouldRemove is asserted to contain the sibling's own
// ebuild precisely because that is what the block is saving: without it, R4.1
// read literally deletes a maintained release line.
func TestCleanPackageDirBlockedByPinlessSibling(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	atom := "media-plugins/gst-plugins-vpx"
	stable, dev := atom+"@stable", atom+"@dev"

	for _, v := range []string{"1.28.4", "1.28.5", "1.29.2"} {
		createTestEbuildFile(t, overlayDir, atom, v)
	}

	var manifestRuns atomic.Int64
	applier, err := NewApplier(overlayDir, filepath.Join(tmpDir, "config"),
		WithExecCommand(countingManifestSeam(&manifestRuns)),
		WithApplierPackagesConfig(&PackagesConfig{Packages: map[string]PackageConfig{
			stable: regEntry("", gstStableSeries),
			dev:    regEntry("", gstDevSeries), // no pin: this is what blocks
		}}),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}

	plan, err := applier.cleanPackageDir(stable, "1.28.5")
	if err == nil {
		t.Fatalf("a blocked sweep reported success: %+v", plan)
	}
	if !strings.Contains(err.Error(), dev) {
		t.Errorf("the warning does not name the entry that lacks a pin (R6.2): %v", err)
	}
	if plan.Blocked != dev {
		t.Errorf("Blocked = %q, want %q", plan.Blocked, dev)
	}
	if len(plan.Remove) != 0 {
		t.Errorf("a blocked sweep removed %v", plan.Remove)
	}
	if !reflect.DeepEqual(plan.WouldRemove, []string{"1.28.4", "1.29.2"}) {
		t.Errorf("WouldRemove = %v, want [1.28.4 1.29.2] — including the dev line the block saved", plan.WouldRemove)
	}

	pkgDir := filepath.Join(overlayDir, "media-plugins", "gst-plugins-vpx")
	for _, f := range []string{"gst-plugins-vpx-1.28.4.ebuild", "gst-plugins-vpx-1.28.5.ebuild", "gst-plugins-vpx-1.29.2.ebuild"} {
		if _, err := os.Stat(filepath.Join(pkgDir, f)); err != nil {
			t.Errorf("%s must survive a blocked sweep: %v", f, err)
		}
	}
	if got := manifestRuns.Load(); got != 0 {
		t.Errorf("a blocked sweep regenerated the Manifest %d time(s), want 0", got)
	}
}

// TestApplyCleanBlockedKeepsSuccessAndNamesEntry walks the same block through a
// full Apply: the update stands (R4.4), nothing is deleted, and the entry
// without a pin is named on the result where a user can read it (R6.2).
func TestApplyCleanBlockedKeepsSuccessAndNamesEntry(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")
	atom := "media-plugins/gst-plugins-vpx"
	stable, dev := atom+"@stable", atom+"@dev"

	createTestEbuildFile(t, overlayDir, atom, "1.28.4")
	createTestEbuildFile(t, overlayDir, atom, "1.29.2")

	pending, _ := NewPendingList(configDir)
	pending.Add(PendingUpdate{
		Package:        stable,
		CurrentVersion: "1.28.4",
		NewVersion:     "1.28.6",
		Status:         StatusPending,
	})

	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(mockExecCommandSuccess),
		WithApplierClean(true),
		WithApplierPackagesConfig(&PackagesConfig{Packages: map[string]PackageConfig{
			stable: regEntry("", gstStableSeries),
			dev:    regEntry("", gstDevSeries),
		}}),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}

	result, err := applier.Apply(stable, false)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !result.Success {
		t.Fatalf("a blocked clean flipped the apply to failed: %v", result.Error)
	}
	if !strings.Contains(result.CleanWarning, dev) {
		t.Errorf("CleanWarning = %q, want it to name %q", result.CleanWarning, dev)
	}
	if len(result.CleanRemoved) != 0 || result.CleanedOldVersion != "" {
		t.Errorf("a blocked clean reported removals: CleanRemoved=%v CleanedOldVersion=%q",
			result.CleanRemoved, result.CleanedOldVersion)
	}

	pkgDir := filepath.Join(overlayDir, "media-plugins", "gst-plugins-vpx")
	for _, f := range []string{"gst-plugins-vpx-1.28.4.ebuild", "gst-plugins-vpx-1.28.6.ebuild", "gst-plugins-vpx-1.29.2.ebuild"} {
		if _, err := os.Stat(filepath.Join(pkgDir, f)); err != nil {
			t.Errorf("%s must still be there after a blocked clean: %v", f, err)
		}
	}
}

// TestApplyCleanRemovalFailureWarnsButKeepsSuccess is R4.4: a sweep that cannot
// delete a file surfaces the reason through CleanWarning and leaves the apply
// successful. The update itself is done — the new ebuild is in place and the
// Manifest was generated — so failing the apply here would report a rollback
// that did not happen.
func TestApplyCleanRemovalFailureWarnsButKeepsSuccess(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: a read-only directory does not stop os.Remove, so no removal can fail")
	}

	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")
	pkg := "test-cat/test-pkg"

	createTestEbuildFile(t, overlayDir, pkg, "1.0.0")

	pending, _ := NewPendingList(configDir)
	pending.Add(PendingUpdate{
		Package:        pkg,
		CurrentVersion: "1.0.0",
		NewVersion:     "2.0.0",
		Status:         StatusPending,
	})

	// The manifest command locks the directory and then SUCCEEDS: copyEbuild has
	// already written 2.0.0 with the directory writable, so the apply completes
	// and the failure lands on the sweep's os.Remove (EACCES). Restore the mode
	// afterwards or t.TempDir's cleanup cannot delete the tree.
	pkgDir := filepath.Join(overlayDir, "test-cat", "test-pkg")
	t.Cleanup(func() { _ = os.Chmod(pkgDir, 0o755) })

	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(mockExecCommandLockDirAndSucceed(pkgDir)),
		WithApplierClean(true),
		WithApplierPackagesConfig(&PackagesConfig{Packages: map[string]PackageConfig{
			pkg: regEntry("", ""),
		}}),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}

	result, applyErr := applier.Apply(pkg, false)
	if applyErr != nil {
		t.Fatalf("a failed clean must not fail the apply: %v", applyErr)
	}
	if !result.Success {
		t.Fatalf("a failed clean flipped Success: %v", result.Error)
	}
	if result.Error != nil {
		// result.Error is what the deferred orphan-rollback keys on: setting it
		// here would delete the ebuild this apply just created.
		t.Errorf("result.Error = %v, want nil so the orphan rollback stays off", result.Error)
	}
	if result.CleanWarning == "" {
		t.Error("a failed removal must be surfaced through CleanWarning (R4.4)")
	}
	if len(result.CleanRemoved) != 0 || result.CleanedOldVersion != "" {
		t.Errorf("nothing was removed, yet the result claims it: CleanRemoved=%v CleanedOldVersion=%q",
			result.CleanRemoved, result.CleanedOldVersion)
	}
	if _, err := os.Stat(filepath.Join(pkgDir, "test-pkg-1.0.0.ebuild")); err != nil {
		t.Errorf("the ebuild the sweep could not remove must still be there: %v", err)
	}
	if _, err := os.Stat(filepath.Join(pkgDir, "test-pkg-2.0.0.ebuild")); err != nil {
		t.Errorf("the freshly applied ebuild must survive a failed clean: %v", err)
	}
}

// TestApplyCleanWithoutRegistryRemovesNothing pins the nil-configs path, which
// is production-reachable: loadPackagesConfigForApply returns nil whenever
// packages.toml cannot be read, and with no registry nothing says which ebuilds
// this directory keeps. Refusing to sweep is the only safe answer, and the
// reason has to reach the user.
func TestApplyCleanWithoutRegistryRemovesNothing(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")
	pkg := "test-cat/test-pkg"

	createTestEbuildFile(t, overlayDir, pkg, "1.0.0")

	pending, _ := NewPendingList(configDir)
	pending.Add(PendingUpdate{
		Package:        pkg,
		CurrentVersion: "1.0.0",
		NewVersion:     "2.0.0",
		Status:         StatusPending,
	})

	// No WithApplierPackagesConfig at all: a.configs stays nil.
	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(mockExecCommandSuccess),
		WithApplierClean(true),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}
	if applier.configs != nil {
		t.Fatalf("this test needs a nil registry; got %v", applier.configs)
	}

	result, applyErr := applier.Apply(pkg, false)
	if applyErr != nil {
		t.Fatalf("Apply: %v", applyErr)
	}
	if !result.Success {
		t.Fatalf("a refused clean flipped Success: %v", result.Error)
	}
	if !strings.Contains(result.CleanWarning, "no packages.toml entry") {
		t.Errorf("CleanWarning = %q, want it to explain that no entry claims the directory", result.CleanWarning)
	}
	if len(result.CleanRemoved) != 0 || result.CleanedOldVersion != "" {
		t.Errorf("a refused clean reported removals: CleanRemoved=%v CleanedOldVersion=%q",
			result.CleanRemoved, result.CleanedOldVersion)
	}

	pkgDir := filepath.Join(overlayDir, "test-cat", "test-pkg")
	for _, f := range []string{"test-pkg-1.0.0.ebuild", "test-pkg-2.0.0.ebuild"} {
		if _, err := os.Stat(filepath.Join(pkgDir, f)); err != nil {
			t.Errorf("%s must survive a refused clean: %v", f, err)
		}
	}
}

// TestCleanPackageDirVanishedCandidateIsNotPlanned pins that an ebuild removed
// behind the applier's back is simply not swept: the plan is computed from the
// directory as it is NOW, so a file already gone is never a candidate, nothing
// is deleted, and the Manifest is left alone.
//
// This is NOT coverage of cleanPackageDir's os.ErrNotExist branch, and no test
// here is: that branch handles a file vanishing BETWEEN the plan and the
// removal (a concurrent rm, a second process), a window a single-threaded test
// cannot open without inventing a seam in the middle of the sweep. It stays
// because the sweep deletes files and "someone beat me to it" must never be
// reported as a failure — see the mutation table in the sub-task report, where
// it is recorded as a deliberate survivor.
func TestCleanPackageDirVanishedCandidateIsNotPlanned(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	pkg := "test-cat/test-pkg"
	createTestEbuildFile(t, overlayDir, pkg, "1.0.0")
	createTestEbuildFile(t, overlayDir, pkg, "2.0.0")

	var manifestRuns atomic.Int64
	applier, err := NewApplier(overlayDir, filepath.Join(tmpDir, "config"),
		WithExecCommand(countingManifestSeam(&manifestRuns)),
		WithApplierPackagesConfig(&PackagesConfig{Packages: map[string]PackageConfig{
			pkg: regEntry("", ""),
		}}),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}

	// Plan says remove 1.0.0; something else got there first.
	if err := os.Remove(filepath.Join(overlayDir, "test-cat", "test-pkg", "test-pkg-1.0.0.ebuild")); err != nil {
		t.Fatalf("pre-removing the candidate: %v", err)
	}

	plan, err := applier.cleanPackageDir(pkg, "2.0.0")
	if err != nil {
		t.Fatalf("an already-absent candidate must not be an error: %v", err)
	}
	if len(plan.Remove) != 0 {
		t.Errorf("Remove = %v, want empty — nothing was actually deleted", plan.Remove)
	}
	if got := manifestRuns.Load(); got != 0 {
		t.Errorf("the Manifest was regenerated %d time(s) for a file that was already gone, want 0", got)
	}
}

// TestApplyCleanReportsKeptAndRemoved pins the report the sweep hands back on
// the success path: every kept version with the entry that claims it (R6.1),
// every version actually deleted, and the legacy one-line view still holding the
// HIGHEST removed version — that field feeds a printer that predates the sweep
// and prints a single "Removed: pkg-X.ebuild" line.
func TestApplyCleanReportsKeptAndRemoved(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")
	pkg := "test-cat/test-pkg"

	for _, v := range []string{"1.0.0", "1.5.0", "9999"} {
		createTestEbuildFile(t, overlayDir, pkg, v)
	}

	pending, _ := NewPendingList(configDir)
	pending.Add(PendingUpdate{
		Package:        pkg,
		CurrentVersion: "1.5.0",
		NewVersion:     "2.0.0",
		Status:         StatusPending,
	})

	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(mockExecCommandSuccess),
		WithApplierClean(true),
		WithApplierPackagesConfig(&PackagesConfig{Packages: map[string]PackageConfig{
			pkg: regEntry("", ""),
		}}),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}

	result, applyErr := applier.Apply(pkg, false)
	if applyErr != nil {
		t.Fatalf("Apply: %v", applyErr)
	}
	if !result.Success {
		t.Fatalf("Apply failed: %v", result.Error)
	}
	if result.CleanWarning != "" {
		t.Fatalf("unexpected clean warning: %s", result.CleanWarning)
	}

	if !reflect.DeepEqual(result.CleanRemoved, []string{"1.0.0", "1.5.0"}) {
		t.Errorf("CleanRemoved = %v, want [1.0.0 1.5.0]", result.CleanRemoved)
	}
	if result.CleanedOldVersion != "1.5.0" {
		t.Errorf("CleanedOldVersion = %q, want the highest removed version %q",
			result.CleanedOldVersion, "1.5.0")
	}
	wantKept := map[string]string{"2.0.0": pkg, "9999": ""}
	if !reflect.DeepEqual(result.CleanKept, wantKept) {
		t.Errorf("CleanKept = %v, want %v", result.CleanKept, wantKept)
	}

	pkgDir := filepath.Join(overlayDir, "test-cat", "test-pkg")
	for _, gone := range []string{"test-pkg-1.0.0.ebuild", "test-pkg-1.5.0.ebuild"} {
		if _, err := os.Stat(filepath.Join(pkgDir, gone)); !os.IsNotExist(err) {
			t.Errorf("%s should have been swept: os.Stat error = %v", gone, err)
		}
	}
	for _, kept := range []string{"test-pkg-2.0.0.ebuild", "test-pkg-9999.ebuild"} {
		if _, err := os.Stat(filepath.Join(pkgDir, kept)); err != nil {
			t.Errorf("%s must survive the sweep: %v", kept, err)
		}
	}
}

// =============================================================================
// Registry pin on the success path (S021 sub-task 4.2)
// =============================================================================

// applyPinFixture lays out the shape all three pin tests share: an overlay
// holding the current ebuild, a registry with the package's record plus an
// untouched neighbour, and a pending entry for the bump.
//
// The registry lives under the SAME t.TempDir() overlay the ebuilds do, because
// SetPackageVersions resolves <overlay>/.autoupdate/packages.toml itself — a
// fixture pointed anywhere else would silently test nothing. It is never the
// real overlay: that one auto-commits and publishes.
func applyPinFixture(t *testing.T, pkg, oldVersion, pendingNewVersion string) (overlayDir, configDir, registryPath string, pending *PendingList) {
	t.Helper()

	tmpDir := t.TempDir()
	overlayDir = filepath.Join(tmpDir, "overlay")
	configDir = filepath.Join(tmpDir, "config")

	createTestEbuildFile(t, overlayDir, pkg, oldVersion)

	registryDir := filepath.Join(overlayDir, ".autoupdate")
	if err := os.MkdirAll(registryDir, 0o750); err != nil {
		t.Fatalf("mkdir registry: %v", err)
	}
	registryPath = filepath.Join(registryDir, "packages.toml")
	if err := os.WriteFile(registryPath, []byte(applyPinRegistry), 0o644); err != nil {
		t.Fatalf("write registry: %v", err)
	}

	var err error
	pending, err = NewPendingList(configDir)
	if err != nil {
		t.Fatalf("NewPendingList: %v", err)
	}
	if err := pending.Add(PendingUpdate{
		Package:        pkg,
		CurrentVersion: oldVersion,
		NewVersion:     pendingNewVersion,
		Status:         StatusPending,
	}); err != nil {
		t.Fatalf("pending.Add: %v", err)
	}
	return overlayDir, configDir, registryPath, pending
}

// applyPinRegistry is the fixture registry: the package being applied, with no
// pin yet (today's state for every entry), and a neighbour that must come out
// byte-identical so a batch of one is proved not to rewrite the file.
const applyPinRegistry = `["test-cat/test-pkg"]
url = "https://example.invalid/releases.json"
parser = "json"
path = "version"
comments = """test-cat/test-pkg: the entry under test."""
# END

["other-cat/other-pkg"]
url = "https://example.invalid/other.json"
parser = "json"
path = "version"
# END
`

// TestApply_SuccessWritesRegistryPin is S021-R2.1: a successful apply records
// the version it just produced in packages.toml.
//
// The assertion is the WHOLE file, not just the pin, because the registry is a
// hand-maintained, published artifact: a write that got the version right and
// reflowed a neighbour would still be a regression. The injected writer wraps —
// rather than replaces — the real SetPackageVersions, so it can additionally
// witness ORDERING: at the moment of the write the new ebuild must already be on
// disk and the manifest step must already have run. That is what makes "the
// registry never claims a file that is not there" (S021-UB4) a tested property
// instead of a comment.
func TestApply_SuccessWritesRegistryPin(t *testing.T) {
	pkg := "test-cat/test-pkg"
	oldVersion, newVersion := "1.0.0", "2.0.0"
	overlayDir, configDir, registryPath, pending := applyPinFixture(t, pkg, oldVersion, newVersion)

	var manifestRuns atomic.Int64
	var (
		writeCalls        int
		gotOverlayPath    string
		gotPins           map[string]string
		ebuildExistsAtWri bool
		manifestsAtWrite  int64
	)
	newEbuild := filepath.Join(overlayDir, "test-cat", "test-pkg", "test-pkg-"+newVersion+".ebuild")

	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(countingManifestSeam(&manifestRuns)),
		WithApplierSetVersionsFunc(func(overlayPath string, pins map[string]string) error {
			writeCalls++
			gotOverlayPath = overlayPath
			gotPins = pins
			_, statErr := os.Stat(newEbuild)
			ebuildExistsAtWri = statErr == nil
			manifestsAtWrite = manifestRuns.Load()
			return SetPackageVersions(overlayPath, pins)
		}),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}

	result, applyErr := applier.Apply(pkg, false)
	if applyErr != nil {
		t.Fatalf("Apply: %v", applyErr)
	}
	if !result.Success {
		t.Fatalf("Apply failed: %v", result.Error)
	}
	if result.RegistryWarning != "" {
		t.Errorf("RegistryWarning = %q, want empty on a written pin", result.RegistryWarning)
	}

	if writeCalls != 1 {
		t.Fatalf("registry writer called %d time(s), want exactly 1", writeCalls)
	}
	if gotOverlayPath != overlayDir {
		t.Errorf("writer got overlay %q, want %q", gotOverlayPath, overlayDir)
	}
	if !reflect.DeepEqual(gotPins, map[string]string{pkg: newVersion}) {
		t.Errorf("writer got pins %v, want %v", gotPins, map[string]string{pkg: newVersion})
	}
	// Ordering (S021-R2.2/UB4): the file first, the claim after.
	if !ebuildExistsAtWri {
		t.Errorf("the pin was written while %s did not exist yet — the registry would claim a missing file", newEbuild)
	}
	if manifestsAtWrite < 1 {
		t.Errorf("the pin was written before the manifest step ran (pkgdev invocations at write time: %d)", manifestsAtWrite)
	}

	got, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatalf("read registry: %v", err)
	}
	want := `["test-cat/test-pkg"]
url = "https://example.invalid/releases.json"
parser = "json"
path = "version"
version = "2.0.0"
comments = """test-cat/test-pkg: the entry under test."""
# END

["other-cat/other-pkg"]
url = "https://example.invalid/other.json"
parser = "json"
path = "version"
# END
`
	if string(got) != want {
		t.Errorf("registry after a successful apply:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}

	// The pin must also survive a reload, since that is how the sweep reads it.
	cfg, err := LoadPackagesConfig(overlayDir)
	if err != nil {
		t.Fatalf("LoadPackagesConfig: %v", err)
	}
	if v := cfg.Packages[pkg].Version; v != newVersion {
		t.Errorf("reloaded pin = %q, want %q", v, newVersion)
	}
}

// TestApply_ManifestFailureLeavesRegistryByteIdentical is S021-R2.2: a failed
// apply writes nothing at all.
//
// The failure is put at the manifest step deliberately: that is the LAST thing
// standing between a copied ebuild and the success point, so it is where a pin
// written one step too early would already have landed. Two independent
// assertions cover it — the writer is never invoked, and the file's bytes are
// unchanged — because the first alone would pass a writer that got called with
// an empty batch, and the second alone would pass a writer that failed silently.
//
// S021-UB5: the deferred orphan rollback still fires, so the half-applied ebuild
// is gone and the registry, having no pin, claims nothing that is missing.
func TestApply_ManifestFailureLeavesRegistryByteIdentical(t *testing.T) {
	pkg := "test-cat/test-pkg"
	oldVersion, newVersion := "1.0.0", "2.0.0"
	overlayDir, configDir, registryPath, pending := applyPinFixture(t, pkg, oldVersion, newVersion)

	before, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatalf("read registry: %v", err)
	}

	writeCalls := 0
	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(mockExecCommandFailure),
		WithApplierSetVersionsFunc(func(overlayPath string, pins map[string]string) error {
			writeCalls++
			return SetPackageVersions(overlayPath, pins)
		}),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}

	result, applyErr := applier.Apply(pkg, false)
	if applyErr == nil {
		t.Fatal("Apply reported success although the manifest step failed")
	}
	if result.Success {
		t.Errorf("result.Success = true on a failed apply")
	}
	if result.Error == nil {
		t.Errorf("result.Error = nil on a failed apply")
	}

	if writeCalls != 0 {
		t.Errorf("the registry writer ran %d time(s) on a failed apply, want 0 (S021-R2.2)", writeCalls)
	}
	after, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatalf("read registry: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("a failed apply changed packages.toml:\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}

	// S021-UB5: the orphan rollback is untouched by this task.
	dstPath := applier.EbuildPath(pkg, newVersion)
	if _, statErr := os.Stat(dstPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("orphan ebuild survived a failed apply: os.Stat(%q) error = %v, want os.ErrNotExist", dstPath, statErr)
	}
}

// TestApply_RegistryWriteFailureWarnsButKeepsSuccess is S021-R2.4: a pin that
// cannot be written is a bookkeeping miss, not a failed update.
//
// The load-bearing assertion is the ebuild still on disk. result.Error is what
// arms the deferred orphan rollback, so an implementation that reported the
// write failure through it would DELETE the very ebuild the apply just produced
// — the update would be undone by its own bookkeeping (S021-UB5).
//
// The pending entry carries "v2.0.0" while the ebuild is written as 2.0.0, so
// the pin handed to the writer also proves S021-UB4: what is recorded is the
// version that landed on disk, never the upstream string pending.json holds.
func TestApply_RegistryWriteFailureWarnsButKeepsSuccess(t *testing.T) {
	pkg := "test-cat/test-pkg"
	oldVersion, newVersion := "1.0.0", "2.0.0"
	overlayDir, configDir, registryPath, pending := applyPinFixture(t, pkg, oldVersion, "v"+newVersion)

	before, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatalf("read registry: %v", err)
	}

	wantErr := errors.New("synthetic registry write failure")
	var gotPins map[string]string
	writeCalls := 0
	logs := captureWarnLogs(t)

	applier, err := NewApplier(overlayDir, configDir,
		WithApplierPendingList(pending),
		WithExecCommand(mockExecCommandSuccess),
		WithApplierSetVersionsFunc(func(_ string, pins map[string]string) error {
			writeCalls++
			gotPins = pins
			return wantErr
		}),
	)
	if err != nil {
		t.Fatalf("NewApplier: %v", err)
	}

	result, applyErr := applier.Apply(pkg, false)
	if applyErr != nil {
		t.Fatalf("Apply returned an error for a failed pin write: %v", applyErr)
	}
	if !result.Success {
		t.Fatalf("Success = false after a failed pin write (S021-R2.4); result.Error = %v", result.Error)
	}
	if result.Error != nil {
		t.Errorf("result.Error = %v, want nil — a non-nil error here arms the orphan rollback (S021-UB5)", result.Error)
	}
	if writeCalls != 1 {
		t.Errorf("registry writer called %d time(s), want 1", writeCalls)
	}
	if !reflect.DeepEqual(gotPins, map[string]string{pkg: newVersion}) {
		t.Errorf("writer got pins %v, want %v — the pin is the version on disk, not the pending target",
			gotPins, map[string]string{pkg: newVersion})
	}

	// The warning must reach both the log sink and the result.
	if logs.count() == 0 {
		t.Errorf("no Warn emitted via warnLogf after a failed pin write (S021-R2.4)")
	}
	joined := strings.Join(logs.all(), "\n")
	if !strings.Contains(joined, pkg) || !strings.Contains(joined, wantErr.Error()) {
		t.Errorf("Warn lines do not carry the package and the cause: %v", logs.all())
	}
	if result.RegistryWarning == "" {
		t.Fatalf("RegistryWarning is empty after a failed pin write (S021-R2.4)")
	}
	if !strings.Contains(result.RegistryWarning, newVersion) || !strings.Contains(result.RegistryWarning, wantErr.Error()) {
		t.Errorf("RegistryWarning = %q, want it to name the version and the cause", result.RegistryWarning)
	}
	if result.CleanWarning != "" {
		t.Errorf("CleanWarning = %q — a registry failure must not be reported as a --clean failure", result.CleanWarning)
	}

	// S021-UB5: the rollback stayed dormant, so the applied ebuild is still there.
	dstPath := applier.EbuildPath(pkg, newVersion)
	if _, statErr := os.Stat(dstPath); statErr != nil {
		t.Errorf("the applied ebuild was rolled back by a bookkeeping failure: os.Stat(%q) error = %v", dstPath, statErr)
	}

	// Nothing wrote to the registry: the failing writer is the only writer.
	after, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatalf("read registry: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("packages.toml changed although the write failed:\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}
}
