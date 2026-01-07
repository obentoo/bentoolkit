package overlay

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

// setupMatcherTestOverlay creates a temporary overlay structure for matcher testing.
// Returns the overlay path. Caller should use t.Cleanup() or defer os.RemoveAll().
func setupMatcherTestOverlay(t *testing.T) string {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "matcher-test-*")
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

// createMatcherTestEbuild creates a test ebuild file in the overlay.
func createMatcherTestEbuild(t *testing.T, overlayPath, category, pkg, version string) {
	t.Helper()

	pkgDir := filepath.Join(overlayPath, category, pkg)
	if err := os.MkdirAll(pkgDir, 0755); err != nil {
		t.Fatalf("failed to create package dir: %v", err)
	}

	filename := pkg + "-" + version + ".ebuild"
	ebuildPath := filepath.Join(pkgDir, filename)

	// Create empty ebuild file
	if err := os.WriteFile(ebuildPath, []byte("# test ebuild\n"), 0644); err != nil {
		t.Fatalf("failed to create ebuild: %v", err)
	}
}

// TestCategorySearchScope tests Property 5: Category Search Scope
// **Feature: ebuild-rename, Property 5: Category Search Scope**
// **Validates: Requirements 3.1**
//
// For any specific category, matcher should search only that category
// and not search other categories.
func TestCategorySearchScope(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100

	properties := gopter.NewProperties(parameters)

	properties.Property("specific category search only returns matches from that category", prop.ForAll(
		func(targetCategory string, otherCategories []string, pkgName, version string) bool {
			// Setup test overlay
			overlayPath, err := os.MkdirTemp("", "overlay-scope-*")
			if err != nil {
				return false
			}
			defer os.RemoveAll(overlayPath)

			// Create required overlay structure
			os.MkdirAll(filepath.Join(overlayPath, "profiles"), 0755)
			os.MkdirAll(filepath.Join(overlayPath, "metadata"), 0755)

			// Create ebuild in target category
			targetPkgDir := filepath.Join(overlayPath, targetCategory, pkgName)
			os.MkdirAll(targetPkgDir, 0755)
			targetEbuild := filepath.Join(targetPkgDir, pkgName+"-"+version+".ebuild")
			os.WriteFile(targetEbuild, []byte("# test\n"), 0644)

			// Create ebuilds in other categories with same package name and version
			for _, cat := range otherCategories {
				if cat == targetCategory {
					continue // Skip if same as target
				}
				otherPkgDir := filepath.Join(overlayPath, cat, pkgName)
				os.MkdirAll(otherPkgDir, 0755)
				otherEbuild := filepath.Join(otherPkgDir, pkgName+"-"+version+".ebuild")
				os.WriteFile(otherEbuild, []byte("# test\n"), 0644)
			}

			// Create matcher and search specific category
			matcher := NewEbuildMatcher(overlayPath)
			spec := &RenameSpec{
				Category:       targetCategory,
				PackagePattern: pkgName,
				OldVersion:     version,
				NewVersion:     "2.0.0",
			}

			matches, err := matcher.Match(spec)
			if err != nil {
				return false
			}

			// Verify all matches are from target category only
			for _, match := range matches {
				if match.Category != targetCategory {
					return false
				}
			}

			// Should have exactly 1 match (from target category)
			return len(matches) == 1
		},
		genMatcherCategoryName(),
		gen.SliceOfN(3, genMatcherCategoryName()),
		genMatcherPackageName(),
		genMatcherVersion(),
	))

	properties.TestingRun(t)
}

// Generators for matcher property tests (prefixed to avoid conflicts)

// genMatcherCategoryName generates valid Gentoo category names (e.g., "app-misc", "dev-libs")
func genMatcherCategoryName() gopter.Gen {
	return gen.RegexMatch(`[a-z]{3,6}-[a-z]{3,6}`)
}

// genMatcherPackageName generates valid package names
func genMatcherPackageName() gopter.Gen {
	return gen.RegexMatch(`[a-z]{3,8}`)
}

// genMatcherVersion generates valid version strings
func genMatcherVersion() gopter.Gen {
	return gen.OneGenOf(
		// Simple version: 1.0.0
		gen.RegexMatch(`[1-9]\.[0-9]\.[0-9]`),
		// Two-part version: 1.0
		gen.RegexMatch(`[1-9]\.[0-9]`),
	)
}

// TestPackageAndVersionMatching tests Property 6: Package and Version Matching
// **Feature: ebuild-rename, Property 6: Package and Version Matching**
// **Validates: Requirements 3.3, 3.4**
//
// For any ebuild file, it should be matched if and only if:
// (a) its package name matches the glob pattern, AND
// (b) its base version (without revision) equals the old version.
func TestPackageAndVersionMatching(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100

	properties := gopter.NewProperties(parameters)

	// Test that matching package and version produces a match
	properties.Property("ebuild is matched when package matches pattern AND version matches", prop.ForAll(
		func(category, pkgPrefix, pkgSuffix, version string) bool {
			// Setup test overlay
			overlayPath, err := os.MkdirTemp("", "overlay-match-*")
			if err != nil {
				return false
			}
			defer os.RemoveAll(overlayPath)

			// Create required overlay structure
			os.MkdirAll(filepath.Join(overlayPath, "profiles"), 0755)
			os.MkdirAll(filepath.Join(overlayPath, "metadata"), 0755)

			// Create package name from prefix + suffix
			pkgName := pkgPrefix + pkgSuffix

			// Create ebuild
			pkgDir := filepath.Join(overlayPath, category, pkgName)
			os.MkdirAll(pkgDir, 0755)
			ebuildPath := filepath.Join(pkgDir, pkgName+"-"+version+".ebuild")
			os.WriteFile(ebuildPath, []byte("# test\n"), 0644)

			// Create matcher with pattern that matches prefix
			matcher := NewEbuildMatcher(overlayPath)
			spec := &RenameSpec{
				Category:       category,
				PackagePattern: pkgPrefix + "*",
				OldVersion:     version,
				NewVersion:     "9.9.9",
			}

			matches, err := matcher.Match(spec)
			if err != nil {
				return false
			}

			// Should have exactly 1 match
			return len(matches) == 1 && matches[0].Package == pkgName
		},
		genMatcherCategoryName(),
		genMatcherPackagePrefix(),
		genMatcherPackageSuffix(),
		genMatcherVersion(),
	))

	// Test that non-matching version produces no match
	properties.Property("ebuild is NOT matched when version differs", prop.ForAll(
		func(category, pkgName, oldVersion, actualVersion string) bool {
			// Skip if versions are the same
			if oldVersion == actualVersion {
				return true
			}

			// Setup test overlay
			overlayPath, err := os.MkdirTemp("", "overlay-nomatch-*")
			if err != nil {
				return false
			}
			defer os.RemoveAll(overlayPath)

			// Create required overlay structure
			os.MkdirAll(filepath.Join(overlayPath, "profiles"), 0755)
			os.MkdirAll(filepath.Join(overlayPath, "metadata"), 0755)

			// Create ebuild with actualVersion
			pkgDir := filepath.Join(overlayPath, category, pkgName)
			os.MkdirAll(pkgDir, 0755)
			ebuildPath := filepath.Join(pkgDir, pkgName+"-"+actualVersion+".ebuild")
			os.WriteFile(ebuildPath, []byte("# test\n"), 0644)

			// Create matcher looking for oldVersion
			matcher := NewEbuildMatcher(overlayPath)
			spec := &RenameSpec{
				Category:       category,
				PackagePattern: pkgName,
				OldVersion:     oldVersion,
				NewVersion:     "9.9.9",
			}

			matches, err := matcher.Match(spec)
			if err != nil {
				return false
			}

			// Should have no matches
			return len(matches) == 0
		},
		genMatcherCategoryName(),
		genMatcherPackageName(),
		genMatcherVersion(),
		genMatcherVersion(),
	))

	// Test that non-matching pattern produces no match
	properties.Property("ebuild is NOT matched when package name doesn't match pattern", prop.ForAll(
		func(category, pkgName, version string) bool {
			// Setup test overlay
			overlayPath, err := os.MkdirTemp("", "overlay-nopattern-*")
			if err != nil {
				return false
			}
			defer os.RemoveAll(overlayPath)

			// Create required overlay structure
			os.MkdirAll(filepath.Join(overlayPath, "profiles"), 0755)
			os.MkdirAll(filepath.Join(overlayPath, "metadata"), 0755)

			// Create ebuild
			pkgDir := filepath.Join(overlayPath, category, pkgName)
			os.MkdirAll(pkgDir, 0755)
			ebuildPath := filepath.Join(pkgDir, pkgName+"-"+version+".ebuild")
			os.WriteFile(ebuildPath, []byte("# test\n"), 0644)

			// Create matcher with pattern that won't match
			matcher := NewEbuildMatcher(overlayPath)
			spec := &RenameSpec{
				Category:       category,
				PackagePattern: "zzz-nonexistent-*",
				OldVersion:     version,
				NewVersion:     "9.9.9",
			}

			matches, err := matcher.Match(spec)
			if err != nil {
				return false
			}

			// Should have no matches
			return len(matches) == 0
		},
		genMatcherCategoryName(),
		genMatcherPackageName(),
		genMatcherVersion(),
	))

	properties.TestingRun(t)
}

// genMatcherPackagePrefix generates valid package name prefixes (≥3 chars for pattern)
func genMatcherPackagePrefix() gopter.Gen {
	return gen.RegexMatch(`[a-z]{3,5}`)
}

// genMatcherPackageSuffix generates valid package name suffixes
func genMatcherPackageSuffix() gopter.Gen {
	return gen.RegexMatch(`[a-z]{2,4}`)
}

// TestRevisionStripping tests Property 7: Revision Stripping
// **Feature: ebuild-rename, Property 7: Revision Stripping**
// **Validates: Requirements 4.1, 4.3**
//
// For any ebuild filename with a revision suffix (e.g., -r1, -r2),
// when renamed, the new filename should not contain any revision suffix.
func TestRevisionStripping(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100

	properties := gopter.NewProperties(parameters)

	// Test that revision is stripped from new filename
	properties.Property("new filename has no revision suffix when old has revision", prop.ForAll(
		func(category, pkgName, version string, revNum int) bool {
			// Setup test overlay
			overlayPath, err := os.MkdirTemp("", "overlay-rev-*")
			if err != nil {
				return false
			}
			defer os.RemoveAll(overlayPath)

			// Create required overlay structure
			os.MkdirAll(filepath.Join(overlayPath, "profiles"), 0755)
			os.MkdirAll(filepath.Join(overlayPath, "metadata"), 0755)

			// Create ebuild with revision suffix
			pkgDir := filepath.Join(overlayPath, category, pkgName)
			os.MkdirAll(pkgDir, 0755)
			revSuffix := "-r" + string(rune('0'+revNum%10))
			oldFilename := pkgName + "-" + version + revSuffix + ".ebuild"
			ebuildPath := filepath.Join(pkgDir, oldFilename)
			os.WriteFile(ebuildPath, []byte("# test\n"), 0644)

			// Create matcher
			matcher := NewEbuildMatcher(overlayPath)
			spec := &RenameSpec{
				Category:       category,
				PackagePattern: pkgName,
				OldVersion:     version,
				NewVersion:     "9.9.9",
			}

			matches, err := matcher.Match(spec)
			if err != nil {
				return false
			}

			// Should have exactly 1 match
			if len(matches) != 1 {
				return false
			}

			match := matches[0]

			// Verify HasRevision is true
			if !match.HasRevision {
				return false
			}

			// Verify new filename has no revision
			expectedNewFilename := pkgName + "-9.9.9.ebuild"
			return match.NewFilename == expectedNewFilename
		},
		genMatcherCategoryName(),
		genMatcherPackageName(),
		genMatcherVersion(),
		gen.IntRange(1, 9),
	))

	// Test that ebuilds without revision remain without revision
	properties.Property("new filename has no revision when old has no revision", prop.ForAll(
		func(category, pkgName, version string) bool {
			// Setup test overlay
			overlayPath, err := os.MkdirTemp("", "overlay-norev-*")
			if err != nil {
				return false
			}
			defer os.RemoveAll(overlayPath)

			// Create required overlay structure
			os.MkdirAll(filepath.Join(overlayPath, "profiles"), 0755)
			os.MkdirAll(filepath.Join(overlayPath, "metadata"), 0755)

			// Create ebuild WITHOUT revision suffix
			pkgDir := filepath.Join(overlayPath, category, pkgName)
			os.MkdirAll(pkgDir, 0755)
			oldFilename := pkgName + "-" + version + ".ebuild"
			ebuildPath := filepath.Join(pkgDir, oldFilename)
			os.WriteFile(ebuildPath, []byte("# test\n"), 0644)

			// Create matcher
			matcher := NewEbuildMatcher(overlayPath)
			spec := &RenameSpec{
				Category:       category,
				PackagePattern: pkgName,
				OldVersion:     version,
				NewVersion:     "9.9.9",
			}

			matches, err := matcher.Match(spec)
			if err != nil {
				return false
			}

			// Should have exactly 1 match
			if len(matches) != 1 {
				return false
			}

			match := matches[0]

			// Verify HasRevision is false
			if match.HasRevision {
				return false
			}

			// Verify new filename has no revision
			expectedNewFilename := pkgName + "-9.9.9.ebuild"
			return match.NewFilename == expectedNewFilename
		},
		genMatcherCategoryName(),
		genMatcherPackageName(),
		genMatcherVersion(),
	))

	properties.TestingRun(t)
}

// TestGlobalSearchWithAsterisk tests that global search with "*" category searches all categories
// **Feature: ebuild-rename**
// **Validates: Requirements 3.2**
func TestGlobalSearchWithAsterisk(t *testing.T) {
	overlayPath := setupMatcherTestOverlay(t)
	defer os.RemoveAll(overlayPath)

	// Create ebuilds in multiple categories
	createMatcherTestEbuild(t, overlayPath, "media-plugins", "gst-plugins-base", "1.24.11")
	createMatcherTestEbuild(t, overlayPath, "dev-libs", "gst-plugins-core", "1.24.11")
	createMatcherTestEbuild(t, overlayPath, "app-misc", "gst-tools", "1.24.11")

	matcher := NewEbuildMatcher(overlayPath)
	spec := &RenameSpec{
		Category:       "*",
		PackagePattern: "gst-*",
		OldVersion:     "1.24.11",
		NewVersion:     "1.26.10",
	}

	matches, err := matcher.Match(spec)
	if err != nil {
		t.Fatalf("Match() returned error: %v", err)
	}

	// Should find matches in all 3 categories
	if len(matches) != 3 {
		t.Errorf("Match() returned %d matches, want 3", len(matches))
	}

	// Verify all categories are represented
	categories := make(map[string]bool)
	for _, match := range matches {
		categories[match.Category] = true
	}

	expectedCategories := []string{"media-plugins", "dev-libs", "app-misc"}
	for _, cat := range expectedCategories {
		if !categories[cat] {
			t.Errorf("Expected category %s in matches", cat)
		}
	}
}

// TestNoMatchesReturnsEmpty tests graceful handling when no ebuilds match
// **Feature: ebuild-rename**
// **Validates: Requirements 3.5**
func TestNoMatchesReturnsEmpty(t *testing.T) {
	overlayPath := setupMatcherTestOverlay(t)
	defer os.RemoveAll(overlayPath)

	// Create an ebuild with different version
	createMatcherTestEbuild(t, overlayPath, "media-plugins", "gst-plugins-base", "1.24.11")

	matcher := NewEbuildMatcher(overlayPath)
	spec := &RenameSpec{
		Category:       "media-plugins",
		PackagePattern: "gst-*",
		OldVersion:     "2.0.0", // Different version
		NewVersion:     "3.0.0",
	}

	matches, err := matcher.Match(spec)
	if err != nil {
		t.Fatalf("Match() returned error: %v", err)
	}

	// Should return empty slice, not error
	if len(matches) != 0 {
		t.Errorf("Match() returned %d matches, want 0", len(matches))
	}
}

// TestRevisionStrippingSpecificExample tests the specific example from requirements
// **Feature: ebuild-rename**
// **Validates: Requirements 4.2**
// Test specific example: package-1.0.0-r3.ebuild → package-2.0.0.ebuild
func TestRevisionStrippingSpecificExample(t *testing.T) {
	overlayPath := setupMatcherTestOverlay(t)
	defer os.RemoveAll(overlayPath)

	// Create ebuild with revision suffix
	pkgDir := filepath.Join(overlayPath, "app-misc", "mypackage")
	if err := os.MkdirAll(pkgDir, 0755); err != nil {
		t.Fatalf("failed to create package dir: %v", err)
	}

	// Create mypackage-1.0.0-r3.ebuild
	ebuildPath := filepath.Join(pkgDir, "mypackage-1.0.0-r3.ebuild")
	if err := os.WriteFile(ebuildPath, []byte("# test\n"), 0644); err != nil {
		t.Fatalf("failed to create ebuild: %v", err)
	}

	matcher := NewEbuildMatcher(overlayPath)
	spec := &RenameSpec{
		Category:       "app-misc",
		PackagePattern: "mypackage",
		OldVersion:     "1.0.0",
		NewVersion:     "2.0.0",
	}

	matches, err := matcher.Match(spec)
	if err != nil {
		t.Fatalf("Match() returned error: %v", err)
	}

	if len(matches) != 1 {
		t.Fatalf("Match() returned %d matches, want 1", len(matches))
	}

	match := matches[0]

	// Verify old filename
	if match.OldFilename != "mypackage-1.0.0-r3.ebuild" {
		t.Errorf("OldFilename = %q, want %q", match.OldFilename, "mypackage-1.0.0-r3.ebuild")
	}

	// Verify new filename has no revision
	if match.NewFilename != "mypackage-2.0.0.ebuild" {
		t.Errorf("NewFilename = %q, want %q", match.NewFilename, "mypackage-2.0.0.ebuild")
	}

	// Verify HasRevision flag
	if !match.HasRevision {
		t.Error("HasRevision should be true")
	}
}

// TestCategoryNotFoundError tests that non-existent category returns error
// **Feature: ebuild-rename**
// **Validates: Requirements 11.3**
func TestCategoryNotFoundError(t *testing.T) {
	overlayPath := setupMatcherTestOverlay(t)
	defer os.RemoveAll(overlayPath)

	matcher := NewEbuildMatcher(overlayPath)
	spec := &RenameSpec{
		Category:       "nonexistent-category",
		PackagePattern: "pkg-*",
		OldVersion:     "1.0.0",
		NewVersion:     "2.0.0",
	}

	_, err := matcher.Match(spec)
	if err == nil {
		t.Fatal("Match() should return error for non-existent category")
	}

	// Verify it's a CategoryNotFoundError
	catErr, ok := err.(*CategoryNotFoundError)
	if !ok {
		t.Errorf("Expected CategoryNotFoundError, got %T", err)
	}

	if catErr.Category != "nonexistent-category" {
		t.Errorf("CategoryNotFoundError.Category = %q, want %q", catErr.Category, "nonexistent-category")
	}
}

// TestMatchPackageExactMatch tests exact package name matching (no wildcards)
// **Feature: ebuild-rename**
// **Validates: Requirements 3.3**
func TestMatchPackageExactMatch(t *testing.T) {
	overlayPath := setupMatcherTestOverlay(t)
	defer os.RemoveAll(overlayPath)

	// Create two packages with similar names
	createMatcherTestEbuild(t, overlayPath, "app-misc", "hello", "1.0.0")
	createMatcherTestEbuild(t, overlayPath, "app-misc", "hello-world", "1.0.0")

	matcher := NewEbuildMatcher(overlayPath)
	spec := &RenameSpec{
		Category:       "app-misc",
		PackagePattern: "hello", // Exact match, no wildcard
		OldVersion:     "1.0.0",
		NewVersion:     "2.0.0",
	}

	matches, err := matcher.Match(spec)
	if err != nil {
		t.Fatalf("Match() returned error: %v", err)
	}

	// Should only match "hello", not "hello-world"
	if len(matches) != 1 {
		t.Fatalf("Match() returned %d matches, want 1", len(matches))
	}

	if matches[0].Package != "hello" {
		t.Errorf("Match().Package = %q, want %q", matches[0].Package, "hello")
	}
}

// TestMatchPackageGlobPattern tests glob pattern matching
// **Feature: ebuild-rename**
// **Validates: Requirements 3.3**
func TestMatchPackageGlobPattern(t *testing.T) {
	overlayPath := setupMatcherTestOverlay(t)
	defer os.RemoveAll(overlayPath)

	// Create packages with similar prefixes
	createMatcherTestEbuild(t, overlayPath, "media-plugins", "gst-plugins-base", "1.24.11")
	createMatcherTestEbuild(t, overlayPath, "media-plugins", "gst-plugins-good", "1.24.11")
	createMatcherTestEbuild(t, overlayPath, "media-plugins", "gst-plugins-ugly", "1.24.11")
	createMatcherTestEbuild(t, overlayPath, "media-plugins", "other-package", "1.24.11")

	matcher := NewEbuildMatcher(overlayPath)
	spec := &RenameSpec{
		Category:       "media-plugins",
		PackagePattern: "gst-plugins-*",
		OldVersion:     "1.24.11",
		NewVersion:     "1.26.10",
	}

	matches, err := matcher.Match(spec)
	if err != nil {
		t.Fatalf("Match() returned error: %v", err)
	}

	// Should match 3 gst-plugins-* packages, not other-package
	if len(matches) != 3 {
		t.Errorf("Match() returned %d matches, want 3", len(matches))
	}

	for _, match := range matches {
		if match.Package == "other-package" {
			t.Error("Should not match 'other-package'")
		}
	}
}

// TestPathsAreCorrect tests that OldPath and NewPath are correctly set
// **Feature: ebuild-rename**
// **Validates: Requirements 8.1**
func TestPathsAreCorrect(t *testing.T) {
	overlayPath := setupMatcherTestOverlay(t)
	defer os.RemoveAll(overlayPath)

	createMatcherTestEbuild(t, overlayPath, "app-misc", "hello", "1.0.0")

	matcher := NewEbuildMatcher(overlayPath)
	spec := &RenameSpec{
		Category:       "app-misc",
		PackagePattern: "hello",
		OldVersion:     "1.0.0",
		NewVersion:     "2.0.0",
	}

	matches, err := matcher.Match(spec)
	if err != nil {
		t.Fatalf("Match() returned error: %v", err)
	}

	if len(matches) != 1 {
		t.Fatalf("Match() returned %d matches, want 1", len(matches))
	}

	match := matches[0]

	expectedOldPath := filepath.Join(overlayPath, "app-misc", "hello", "hello-1.0.0.ebuild")
	expectedNewPath := filepath.Join(overlayPath, "app-misc", "hello", "hello-2.0.0.ebuild")

	if match.OldPath != expectedOldPath {
		t.Errorf("OldPath = %q, want %q", match.OldPath, expectedOldPath)
	}

	if match.NewPath != expectedNewPath {
		t.Errorf("NewPath = %q, want %q", match.NewPath, expectedNewPath)
	}
}
