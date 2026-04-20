package overlay

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

// setupVersionFilesTestOverlay creates a temporary overlay structure for testing.
// Returns the overlay path. Caller should use defer os.RemoveAll().
func setupVersionFilesTestOverlay(t *testing.T) string {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "versionfiles-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	// Create required overlay structure
	dirs := []string{
		"profiles",
		"metadata",
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(filepath.Join(tmpDir, dir), 0755); err != nil {
			os.RemoveAll(tmpDir)
			t.Fatalf("failed to create dir %s: %v", dir, err)
		}
	}

	return tmpDir
}

// createVersionFile creates a version-specific file in the files/ directory.
func createVersionFile(t *testing.T, overlayPath, category, pkg, filename string) {
	t.Helper()

	filesDir := filepath.Join(overlayPath, category, pkg, "files")
	if err := os.MkdirAll(filesDir, 0755); err != nil {
		t.Fatalf("failed to create files dir: %v", err)
	}

	filePath := filepath.Join(filesDir, filename)
	if err := os.WriteFile(filePath, []byte("# test file\n"), 0644); err != nil {
		t.Fatalf("failed to create version file: %v", err)
	}
}

// createPackageDir creates a package directory without files/ subdirectory.
func createPackageDir(t *testing.T, overlayPath, category, pkg string) {
	t.Helper()

	pkgDir := filepath.Join(overlayPath, category, pkg)
	if err := os.MkdirAll(pkgDir, 0755); err != nil {
		t.Fatalf("failed to create package dir: %v", err)
	}
}

// TestVersionFilesDetection tests Property 8: Version Files Detection
// **Feature: ebuild-rename, Property 8: Version Files Detection**
// **Validates: Requirements 5.1**
//
// For any package with files/ subdirectory, all files containing old version
// should be detected.
func TestVersionFilesDetection(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100

	properties := gopter.NewProperties(parameters)

	properties.Property("all files containing version string are detected", prop.ForAll(
		func(category, pkgName, version string, fileCount int) bool {
			// Setup test overlay
			overlayPath, err := os.MkdirTemp("", "overlay-vf-*")
			if err != nil {
				return false
			}
			defer os.RemoveAll(overlayPath)

			// Create required overlay structure
			os.MkdirAll(filepath.Join(overlayPath, "profiles"), 0755)
			os.MkdirAll(filepath.Join(overlayPath, "metadata"), 0755)

			// Create files/ directory
			filesDir := filepath.Join(overlayPath, category, pkgName, "files")
			os.MkdirAll(filesDir, 0755)

			// Create version-specific files
			expectedCount := 0
			for i := 0; i < fileCount; i++ {
				var filename string
				if i%2 == 0 {
					// Version-specific file
					filename = pkgName + "-" + version + "-patch" + string(rune('0'+i)) + ".patch"
					expectedCount++
				} else {
					// Non-version file
					filename = pkgName + "-generic-patch" + string(rune('0'+i)) + ".patch"
				}
				os.WriteFile(filepath.Join(filesDir, filename), []byte("# test\n"), 0644)
			}

			// Create RenameMatch
			matches := []RenameMatch{
				{
					Category:    category,
					Package:     pkgName,
					OldFilename: pkgName + "-" + version + ".ebuild",
					NewFilename: pkgName + "-9.9.9.ebuild",
					OldPath:     filepath.Join(overlayPath, category, pkgName, pkgName+"-"+version+".ebuild"),
					NewPath:     filepath.Join(overlayPath, category, pkgName, pkgName+"-9.9.9.ebuild"),
				},
			}

			// Detect version files
			detector := NewVersionFilesDetector(overlayPath)
			versionFiles := detector.Detect(matches, version)

			// Verify count matches expected
			return len(versionFiles) == expectedCount
		},
		genVersionFilesCategory(),
		genVersionFilesPackageName(),
		genVersionFilesVersion(),
		gen.IntRange(1, 6),
	))

	properties.Property("detected files have correct category and package", prop.ForAll(
		func(category, pkgName, version string) bool {
			// Setup test overlay
			overlayPath, err := os.MkdirTemp("", "overlay-vf-meta-*")
			if err != nil {
				return false
			}
			defer os.RemoveAll(overlayPath)

			// Create required overlay structure
			os.MkdirAll(filepath.Join(overlayPath, "profiles"), 0755)
			os.MkdirAll(filepath.Join(overlayPath, "metadata"), 0755)

			// Create files/ directory with version file
			filesDir := filepath.Join(overlayPath, category, pkgName, "files")
			os.MkdirAll(filesDir, 0755)
			filename := pkgName + "-" + version + "-fix.patch"
			os.WriteFile(filepath.Join(filesDir, filename), []byte("# test\n"), 0644)

			// Create RenameMatch
			matches := []RenameMatch{
				{
					Category: category,
					Package:  pkgName,
				},
			}

			// Detect version files
			detector := NewVersionFilesDetector(overlayPath)
			versionFiles := detector.Detect(matches, version)

			// Verify metadata
			if len(versionFiles) != 1 {
				return false
			}

			vf := versionFiles[0]
			return vf.Category == category &&
				vf.Package == pkgName &&
				vf.Filename == filename
		},
		genVersionFilesCategory(),
		genVersionFilesPackageName(),
		genVersionFilesVersion(),
	))

	properties.TestingRun(t)
}

// Generators for version files property tests

func genVersionFilesCategory() gopter.Gen {
	return gen.RegexMatch(`[a-z]{3,6}-[a-z]{3,6}`)
}

func genVersionFilesPackageName() gopter.Gen {
	return gen.RegexMatch(`[a-z]{3,8}`)
}

func genVersionFilesVersion() gopter.Gen {
	return gen.RegexMatch(`[1-9]\.[0-9]\.[0-9]`)
}

// TestVersionFilesBlocking tests Property 9: Version Files Blocking
// **Feature: ebuild-rename, Property 9: Version Files Blocking**
// **Validates: Requirements 5.3, 5.4**
//
// For any rename operation where version files are detected:
// - Without --force flag: operation should abort
// - With --force flag: operation should proceed
func TestVersionFilesBlocking(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100

	properties := gopter.NewProperties(parameters)

	// Test that version files block without --force
	properties.Property("version files block operation without force flag", prop.ForAll(
		func(fileCount int) bool {
			// Create version files slice
			versionFiles := make([]VersionFile, fileCount)
			for i := 0; i < fileCount; i++ {
				versionFiles[i] = VersionFile{
					Category: "app-misc",
					Package:  "testpkg",
					Filename: "testpkg-1.0.0-patch.patch",
					Path:     "/tmp/overlay/app-misc/testpkg/files/testpkg-1.0.0-patch.patch",
				}
			}

			// Without force flag, should block
			shouldBlock := ShouldBlockForVersionFiles(versionFiles, false)
			return shouldBlock == true
		},
		gen.IntRange(1, 10),
	))

	// Test that version files don't block with --force
	properties.Property("version files don't block operation with force flag", prop.ForAll(
		func(fileCount int) bool {
			// Create version files slice
			versionFiles := make([]VersionFile, fileCount)
			for i := 0; i < fileCount; i++ {
				versionFiles[i] = VersionFile{
					Category: "app-misc",
					Package:  "testpkg",
					Filename: "testpkg-1.0.0-patch.patch",
					Path:     "/tmp/overlay/app-misc/testpkg/files/testpkg-1.0.0-patch.patch",
				}
			}

			// With force flag, should not block
			shouldBlock := ShouldBlockForVersionFiles(versionFiles, true)
			return shouldBlock == false
		},
		gen.IntRange(1, 10),
	))

	// Test that empty version files never blocks
	properties.Property("empty version files never blocks regardless of force flag", prop.ForAll(
		func(force bool) bool {
			versionFiles := []VersionFile{}
			shouldBlock := ShouldBlockForVersionFiles(versionFiles, force)
			return shouldBlock == false
		},
		gen.Bool(),
	))

	properties.TestingRun(t)
}

// TestNoVersionFilesReturnsEmpty tests that operation proceeds without warnings
// when no version files exist.
// **Feature: ebuild-rename**
// **Validates: Requirements 5.5**
func TestNoVersionFilesReturnsEmpty(t *testing.T) {
	overlayPath := setupVersionFilesTestOverlay(t)
	defer os.RemoveAll(overlayPath)

	// Create package directory without files/ subdirectory
	createPackageDir(t, overlayPath, "app-misc", "testpkg")

	matches := []RenameMatch{
		{
			Category:    "app-misc",
			Package:     "testpkg",
			OldFilename: "testpkg-1.0.0.ebuild",
			NewFilename: "testpkg-2.0.0.ebuild",
			OldPath:     filepath.Join(overlayPath, "app-misc", "testpkg", "testpkg-1.0.0.ebuild"),
			NewPath:     filepath.Join(overlayPath, "app-misc", "testpkg", "testpkg-2.0.0.ebuild"),
		},
	}

	detector := NewVersionFilesDetector(overlayPath)
	versionFiles := detector.Detect(matches, "1.0.0")

	// Should return empty slice
	if len(versionFiles) != 0 {
		t.Errorf("Detect() returned %d version files, want 0", len(versionFiles))
	}
}

// TestNoVersionFilesWithEmptyFilesDir tests that empty files/ directory returns empty result.
// **Feature: ebuild-rename**
// **Validates: Requirements 5.5**
func TestNoVersionFilesWithEmptyFilesDir(t *testing.T) {
	overlayPath := setupVersionFilesTestOverlay(t)
	defer os.RemoveAll(overlayPath)

	// Create package with empty files/ directory
	filesDir := filepath.Join(overlayPath, "app-misc", "testpkg", "files")
	if err := os.MkdirAll(filesDir, 0755); err != nil {
		t.Fatalf("failed to create files dir: %v", err)
	}

	matches := []RenameMatch{
		{
			Category: "app-misc",
			Package:  "testpkg",
		},
	}

	detector := NewVersionFilesDetector(overlayPath)
	versionFiles := detector.Detect(matches, "1.0.0")

	// Should return empty slice
	if len(versionFiles) != 0 {
		t.Errorf("Detect() returned %d version files, want 0", len(versionFiles))
	}
}

// TestVersionFilesWithNonMatchingFiles tests that files not containing version are ignored.
// **Feature: ebuild-rename**
// **Validates: Requirements 5.1**
func TestVersionFilesWithNonMatchingFiles(t *testing.T) {
	overlayPath := setupVersionFilesTestOverlay(t)
	defer os.RemoveAll(overlayPath)

	// Create files that don't contain the version string
	createVersionFile(t, overlayPath, "app-misc", "testpkg", "generic-fix.patch")
	createVersionFile(t, overlayPath, "app-misc", "testpkg", "build-fix.patch")
	createVersionFile(t, overlayPath, "app-misc", "testpkg", "testpkg-2.0.0-fix.patch") // Different version

	matches := []RenameMatch{
		{
			Category: "app-misc",
			Package:  "testpkg",
		},
	}

	detector := NewVersionFilesDetector(overlayPath)
	versionFiles := detector.Detect(matches, "1.0.0")

	// Should return empty slice (no files contain "1.0.0")
	if len(versionFiles) != 0 {
		t.Errorf("Detect() returned %d version files, want 0", len(versionFiles))
	}
}

// TestVersionFilesDetectsMultipleFiles tests detection of multiple version files.
// **Feature: ebuild-rename**
// **Validates: Requirements 5.1, 5.2**
func TestVersionFilesDetectsMultipleFiles(t *testing.T) {
	overlayPath := setupVersionFilesTestOverlay(t)
	defer os.RemoveAll(overlayPath)

	// Create multiple version-specific files
	createVersionFile(t, overlayPath, "media-plugins", "gst-plugins-base", "gst-plugins-base-1.24.11-fix.patch")
	createVersionFile(t, overlayPath, "media-plugins", "gst-plugins-base", "gst-plugins-base-1.24.11-build.patch")
	createVersionFile(t, overlayPath, "media-plugins", "gst-plugins-base", "generic-fix.patch") // Should not match

	matches := []RenameMatch{
		{
			Category: "media-plugins",
			Package:  "gst-plugins-base",
		},
	}

	detector := NewVersionFilesDetector(overlayPath)
	versionFiles := detector.Detect(matches, "1.24.11")

	// Should detect 2 version files
	if len(versionFiles) != 2 {
		t.Errorf("Detect() returned %d version files, want 2", len(versionFiles))
	}

	// Verify all detected files contain the version
	for _, vf := range versionFiles {
		if !containsVersion(vf.Filename, "1.24.11") {
			t.Errorf("Detected file %q does not contain version 1.24.11", vf.Filename)
		}
	}
}

// TestVersionFilesAcrossMultiplePackages tests detection across multiple packages.
// **Feature: ebuild-rename**
// **Validates: Requirements 5.1**
func TestVersionFilesAcrossMultiplePackages(t *testing.T) {
	overlayPath := setupVersionFilesTestOverlay(t)
	defer os.RemoveAll(overlayPath)

	// Create version files in multiple packages
	createVersionFile(t, overlayPath, "media-plugins", "gst-plugins-base", "gst-plugins-base-1.24.11-fix.patch")
	createVersionFile(t, overlayPath, "media-plugins", "gst-plugins-good", "gst-plugins-good-1.24.11-fix.patch")

	matches := []RenameMatch{
		{
			Category: "media-plugins",
			Package:  "gst-plugins-base",
		},
		{
			Category: "media-plugins",
			Package:  "gst-plugins-good",
		},
	}

	detector := NewVersionFilesDetector(overlayPath)
	versionFiles := detector.Detect(matches, "1.24.11")

	// Should detect 2 version files (one per package)
	if len(versionFiles) != 2 {
		t.Errorf("Detect() returned %d version files, want 2", len(versionFiles))
	}

	// Verify both packages are represented
	packages := make(map[string]bool)
	for _, vf := range versionFiles {
		packages[vf.Package] = true
	}

	if !packages["gst-plugins-base"] {
		t.Error("Expected version file from gst-plugins-base")
	}
	if !packages["gst-plugins-good"] {
		t.Error("Expected version file from gst-plugins-good")
	}
}

// TestVersionFilesDeduplicatesPackages tests that same package is only scanned once.
// **Feature: ebuild-rename**
// **Validates: Requirements 5.1**
func TestVersionFilesDeduplicatesPackages(t *testing.T) {
	overlayPath := setupVersionFilesTestOverlay(t)
	defer os.RemoveAll(overlayPath)

	// Create one version file
	createVersionFile(t, overlayPath, "app-misc", "testpkg", "testpkg-1.0.0-fix.patch")

	// Create multiple matches for the same package (e.g., multiple ebuilds)
	matches := []RenameMatch{
		{
			Category: "app-misc",
			Package:  "testpkg",
		},
		{
			Category: "app-misc",
			Package:  "testpkg", // Duplicate
		},
		{
			Category: "app-misc",
			Package:  "testpkg", // Duplicate
		},
	}

	detector := NewVersionFilesDetector(overlayPath)
	versionFiles := detector.Detect(matches, "1.0.0")

	// Should detect only 1 version file (not 3)
	if len(versionFiles) != 1 {
		t.Errorf("Detect() returned %d version files, want 1 (deduplication failed)", len(versionFiles))
	}
}
