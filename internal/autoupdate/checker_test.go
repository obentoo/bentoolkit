package autoupdate

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

// =============================================================================
// Property-Based Tests
// =============================================================================

// TestCheckPackageFiltering tests Property 6: Check Package Filtering
// **Feature: ebuild-autoupdate, Property 6: Check Package Filtering**
// **Validates: Requirements 4.1, 4.2**
func TestCheckPackageFiltering(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: CheckAll checks exactly N packages for N packages in config
	properties.Property("CheckAll checks exactly N packages", prop.ForAll(
		func(numPackages int) bool {
			// Ensure numPackages is between 1 and 5
			numPackages = (numPackages % 5) + 1

			tmpDir := t.TempDir()
			overlayDir := filepath.Join(tmpDir, "overlay")
			configDir := filepath.Join(tmpDir, "config")

			// Create mock HTTP server
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(map[string]string{"version": "1.0.0"})
			}))
			defer server.Close()

			// Create packages config with N packages
			packages := make(map[string]PackageConfig)
			for i := 0; i < numPackages; i++ {
				pkgName := genTestPackageName(i)
				packages[pkgName] = PackageConfig{
					URL:    server.URL,
					Parser: "json",
					Path:   "version",
				}
				// Create overlay directory structure
				createTestEbuild(t, overlayDir, pkgName, "0.9.0")
			}

			config := &PackagesConfig{Packages: packages}

			// Create checker
			checker, err := NewChecker(overlayDir,
				WithConfigDir(configDir),
				WithPackagesConfig(config),
			)
			if err != nil {
				t.Logf("Failed to create checker: %v", err)
				return false
			}

			// Check all packages
			results, err := checker.CheckAll(false)
			if err != nil {
				t.Logf("CheckAll failed: %v", err)
				return false
			}

			// Verify we got exactly N results
			if len(results) != numPackages {
				t.Logf("Expected %d results, got %d", numPackages, len(results))
				return false
			}

			return true
		},
		gen.IntRange(1, 5),
	))

	// Property: CheckPackage checks exactly 1 package
	properties.Property("CheckPackage checks exactly 1 package", prop.ForAll(
		func(targetIndex int) bool {
			numPackages := 3
			targetIndex = targetIndex % numPackages

			tmpDir := t.TempDir()
			overlayDir := filepath.Join(tmpDir, "overlay")
			configDir := filepath.Join(tmpDir, "config")

			// Create mock HTTP server
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(map[string]string{"version": "1.0.0"})
			}))
			defer server.Close()

			// Create packages config with multiple packages
			packages := make(map[string]PackageConfig)
			var targetPkg string
			for i := 0; i < numPackages; i++ {
				pkgName := genTestPackageName(i)
				if i == targetIndex {
					targetPkg = pkgName
				}
				packages[pkgName] = PackageConfig{
					URL:    server.URL,
					Parser: "json",
					Path:   "version",
				}
				createTestEbuild(t, overlayDir, pkgName, "0.9.0")
			}

			config := &PackagesConfig{Packages: packages}

			checker, err := NewChecker(overlayDir,
				WithConfigDir(configDir),
				WithPackagesConfig(config),
			)
			if err != nil {
				t.Logf("Failed to create checker: %v", err)
				return false
			}

			// Check single package
			result, err := checker.CheckPackage(targetPkg, false)
			if err != nil {
				t.Logf("CheckPackage failed: %v", err)
				return false
			}

			// Verify result is for the target package
			if result.Package != targetPkg {
				t.Logf("Expected package %q, got %q", targetPkg, result.Package)
				return false
			}

			return true
		},
		gen.IntRange(0, 2),
	))

	properties.TestingRun(t)
}


// TestVersionComparisonTriggersPending tests Property 7: Version Comparison Triggers Pending Update
// **Feature: ebuild-autoupdate, Property 7: Version Comparison Triggers Pending Update**
// **Validates: Requirements 4.4, 4.5**
func TestVersionComparisonTriggersPending(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: Newer upstream version triggers pending update
	properties.Property("Newer upstream version adds pending entry with status pending", prop.ForAll(
		func(majorDiff int) bool {
			// Generate versions where upstream is newer
			currentMajor := 1
			upstreamMajor := currentMajor + (majorDiff%5) + 1 // Ensure upstream is always newer

			currentVersion := genVersionString(currentMajor, 0, 0)
			upstreamVersion := genVersionString(upstreamMajor, 0, 0)

			tmpDir := t.TempDir()
			overlayDir := filepath.Join(tmpDir, "overlay")
			configDir := filepath.Join(tmpDir, "config")

			// Create mock HTTP server returning upstream version
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(map[string]string{"version": upstreamVersion})
			}))
			defer server.Close()

			pkgName := "test-cat/test-pkg"
			packages := map[string]PackageConfig{
				pkgName: {
					URL:    server.URL,
					Parser: "json",
					Path:   "version",
				},
			}
			config := &PackagesConfig{Packages: packages}

			// Create ebuild with current version
			createTestEbuild(t, overlayDir, pkgName, currentVersion)

			checker, err := NewChecker(overlayDir,
				WithConfigDir(configDir),
				WithPackagesConfig(config),
			)
			if err != nil {
				t.Logf("Failed to create checker: %v", err)
				return false
			}

			// Check package
			result, err := checker.CheckPackage(pkgName, true)
			if err != nil {
				t.Logf("CheckPackage failed: %v", err)
				return false
			}

			// Verify update was detected
			if !result.HasUpdate {
				t.Logf("Expected HasUpdate=true for upstream %s > current %s", upstreamVersion, currentVersion)
				return false
			}

			// Verify pending entry was added
			pending, found := checker.Pending().Get(pkgName)
			if !found {
				t.Log("Expected pending entry to be added")
				return false
			}

			if pending.Status != StatusPending {
				t.Logf("Expected status 'pending', got %q", pending.Status)
				return false
			}

			if pending.NewVersion != upstreamVersion {
				t.Logf("Expected new version %q, got %q", upstreamVersion, pending.NewVersion)
				return false
			}

			return true
		},
		gen.IntRange(0, 4),
	))

	// Property: Same or older upstream version does not trigger pending update
	properties.Property("Same or older upstream version does not add pending entry", prop.ForAll(
		func(versionDiff int) bool {
			// Generate versions where upstream is same or older
			currentMajor := 5
			upstreamMajor := currentMajor - (versionDiff % 5) // Same or older

			currentVersion := genVersionString(currentMajor, 0, 0)
			upstreamVersion := genVersionString(upstreamMajor, 0, 0)

			tmpDir := t.TempDir()
			overlayDir := filepath.Join(tmpDir, "overlay")
			configDir := filepath.Join(tmpDir, "config")

			// Create mock HTTP server returning upstream version
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(map[string]string{"version": upstreamVersion})
			}))
			defer server.Close()

			pkgName := "test-cat/test-pkg"
			packages := map[string]PackageConfig{
				pkgName: {
					URL:    server.URL,
					Parser: "json",
					Path:   "version",
				},
			}
			config := &PackagesConfig{Packages: packages}

			// Create ebuild with current version
			createTestEbuild(t, overlayDir, pkgName, currentVersion)

			checker, err := NewChecker(overlayDir,
				WithConfigDir(configDir),
				WithPackagesConfig(config),
			)
			if err != nil {
				t.Logf("Failed to create checker: %v", err)
				return false
			}

			// Check package
			result, err := checker.CheckPackage(pkgName, true)
			if err != nil {
				t.Logf("CheckPackage failed: %v", err)
				return false
			}

			// Verify no update was detected
			if result.HasUpdate {
				t.Logf("Expected HasUpdate=false for upstream %s <= current %s", upstreamVersion, currentVersion)
				return false
			}

			// Verify no pending entry was added
			_, found := checker.Pending().Get(pkgName)
			if found {
				t.Log("Expected no pending entry for same/older version")
				return false
			}

			return true
		},
		gen.IntRange(0, 4),
	))

	properties.TestingRun(t)
}

// =============================================================================
// Helper Functions
// =============================================================================

// genTestPackageName generates a test package name based on index
func genTestPackageName(index int) string {
	categories := []string{"app-misc", "dev-libs", "net-misc", "sys-apps", "x11-libs"}
	names := []string{"test-pkg", "example", "sample", "demo", "widget"}
	return categories[index%len(categories)] + "/" + names[index%len(names)]
}

// genVersionString generates a version string from major, minor, patch
func genVersionString(major, minor, patch int) string {
	return string(rune('0'+major)) + "." + string(rune('0'+minor)) + "." + string(rune('0'+patch))
}

// createTestEbuild creates a test ebuild file in the overlay
func createTestEbuild(t *testing.T, overlayDir, pkgName, version string) {
	t.Helper()

	parts := splitPackageName(pkgName)
	if len(parts) != 2 {
		t.Fatalf("Invalid package name: %s", pkgName)
	}

	category := parts[0]
	name := parts[1]

	pkgDir := filepath.Join(overlayDir, category, name)
	if err := os.MkdirAll(pkgDir, 0755); err != nil {
		t.Fatalf("Failed to create package dir: %v", err)
	}

	ebuildPath := filepath.Join(pkgDir, name+"-"+version+".ebuild")
	content := `# Test ebuild
EAPI=8
DESCRIPTION="Test package"
HOMEPAGE="https://example.com"
SRC_URI=""
LICENSE="MIT"
SLOT="0"
KEYWORDS="~amd64"
`
	if err := os.WriteFile(ebuildPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write ebuild: %v", err)
	}
}

// splitPackageName splits a package name into category and name
func splitPackageName(pkg string) []string {
	for i, c := range pkg {
		if c == '/' {
			return []string{pkg[:i], pkg[i+1:]}
		}
	}
	return nil
}


// =============================================================================
// Unit Tests
// =============================================================================

// TestNewCheckerCreatesComponents tests that NewChecker initializes all components
func TestNewCheckerCreatesComponents(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	// Create minimal packages.toml
	createTestPackagesConfig(t, overlayDir, map[string]PackageConfig{
		"test-cat/test-pkg": {
			URL:    "https://example.com/api",
			Parser: "json",
			Path:   "version",
		},
	})

	checker, err := NewChecker(overlayDir, WithConfigDir(configDir))
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if checker.Config() == nil {
		t.Error("Expected config to be initialized")
	}
	if checker.Cache() == nil {
		t.Error("Expected cache to be initialized")
	}
	if checker.Pending() == nil {
		t.Error("Expected pending to be initialized")
	}
	if checker.OverlayPath() != overlayDir {
		t.Errorf("Expected overlay path %q, got %q", overlayDir, checker.OverlayPath())
	}
}

// TestNewCheckerMissingConfig tests error when packages.toml is missing
func TestNewCheckerMissingConfig(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	// Don't create packages.toml
	os.MkdirAll(overlayDir, 0755)

	_, err := NewChecker(overlayDir, WithConfigDir(configDir))
	if err == nil {
		t.Error("Expected error for missing packages.toml")
	}
}

// TestNewCheckerWithOptions tests functional options
func TestNewCheckerWithOptions(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	// Create custom components
	customCache, _ := NewCache(configDir)
	customPending, _ := NewPendingList(configDir)
	customConfig := &PackagesConfig{
		Packages: map[string]PackageConfig{
			"test/pkg": {URL: "https://example.com", Parser: "json", Path: "v"},
		},
	}

	checker, err := NewChecker(overlayDir,
		WithConfigDir(configDir),
		WithCache(customCache),
		WithPendingList(customPending),
		WithPackagesConfig(customConfig),
	)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if checker.Cache() != customCache {
		t.Error("Expected custom cache to be used")
	}
	if checker.Pending() != customPending {
		t.Error("Expected custom pending list to be used")
	}
	if checker.Config() != customConfig {
		t.Error("Expected custom config to be used")
	}
}

// TestCheckPackageNotFound tests error when package is not in config
func TestCheckPackageNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	config := &PackagesConfig{
		Packages: map[string]PackageConfig{
			"test/pkg": {URL: "https://example.com", Parser: "json", Path: "v"},
		},
	}

	checker, err := NewChecker(overlayDir,
		WithConfigDir(configDir),
		WithPackagesConfig(config),
	)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	result, err := checker.CheckPackage("nonexistent/pkg", false)
	if err == nil {
		t.Error("Expected error for non-existent package")
	}
	if result.Error == nil {
		t.Error("Expected result.Error to be set")
	}
}

// TestCheckPackageNoEbuild tests error when no ebuild exists
func TestCheckPackageNoEbuild(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	config := &PackagesConfig{
		Packages: map[string]PackageConfig{
			"test-cat/test-pkg": {URL: "https://example.com", Parser: "json", Path: "v"},
		},
	}

	// Create package directory but no ebuild
	os.MkdirAll(filepath.Join(overlayDir, "test-cat", "test-pkg"), 0755)

	checker, err := NewChecker(overlayDir,
		WithConfigDir(configDir),
		WithPackagesConfig(config),
	)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	result, err := checker.CheckPackage("test-cat/test-pkg", false)
	if err == nil {
		t.Error("Expected error for missing ebuild")
	}
	if result.Error == nil {
		t.Error("Expected result.Error to be set")
	}
}

// TestCheckPackageUsesCache tests that cache is used when available
func TestCheckPackageUsesCache(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pkgName := "test-cat/test-pkg"
	cachedVersion := "2.0.0"

	// Create ebuild
	createTestEbuild(t, overlayDir, pkgName, "1.0.0")

	config := &PackagesConfig{
		Packages: map[string]PackageConfig{
			pkgName: {URL: "https://example.com", Parser: "json", Path: "version"},
		},
	}

	// Create cache with entry
	fixedNow := time.Date(2026, 1, 22, 12, 0, 0, 0, time.UTC)
	cache, _ := NewCache(configDir, WithNowFunc(func() time.Time { return fixedNow }))
	cache.Set(pkgName, cachedVersion, "https://example.com")

	checker, err := NewChecker(overlayDir,
		WithConfigDir(configDir),
		WithPackagesConfig(config),
		WithCache(cache),
	)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Check without force - should use cache
	result, err := checker.CheckPackage(pkgName, false)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if !result.FromCache {
		t.Error("Expected result to be from cache")
	}
	if result.UpstreamVersion != cachedVersion {
		t.Errorf("Expected upstream version %q, got %q", cachedVersion, result.UpstreamVersion)
	}
}

// TestCheckPackageBypassesCache tests that force flag bypasses cache
func TestCheckPackageBypassesCache(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pkgName := "test-cat/test-pkg"
	cachedVersion := "2.0.0"
	freshVersion := "3.0.0"

	// Create mock HTTP server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"version": freshVersion})
	}))
	defer server.Close()

	// Create ebuild
	createTestEbuild(t, overlayDir, pkgName, "1.0.0")

	config := &PackagesConfig{
		Packages: map[string]PackageConfig{
			pkgName: {URL: server.URL, Parser: "json", Path: "version"},
		},
	}

	// Create cache with entry
	fixedNow := time.Date(2026, 1, 22, 12, 0, 0, 0, time.UTC)
	cache, _ := NewCache(configDir, WithNowFunc(func() time.Time { return fixedNow }))
	cache.Set(pkgName, cachedVersion, server.URL)

	checker, err := NewChecker(overlayDir,
		WithConfigDir(configDir),
		WithPackagesConfig(config),
		WithCache(cache),
	)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Check with force - should bypass cache
	result, err := checker.CheckPackage(pkgName, true)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if result.FromCache {
		t.Error("Expected result NOT to be from cache when force=true")
	}
	if result.UpstreamVersion != freshVersion {
		t.Errorf("Expected upstream version %q, got %q", freshVersion, result.UpstreamVersion)
	}
}

// TestCheckPackageDetectsUpdate tests that updates are correctly detected
func TestCheckPackageDetectsUpdate(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pkgName := "test-cat/test-pkg"
	currentVersion := "1.0.0"
	upstreamVersion := "2.0.0"

	// Create mock HTTP server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"version": upstreamVersion})
	}))
	defer server.Close()

	// Create ebuild
	createTestEbuild(t, overlayDir, pkgName, currentVersion)

	config := &PackagesConfig{
		Packages: map[string]PackageConfig{
			pkgName: {URL: server.URL, Parser: "json", Path: "version"},
		},
	}

	checker, err := NewChecker(overlayDir,
		WithConfigDir(configDir),
		WithPackagesConfig(config),
	)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	result, err := checker.CheckPackage(pkgName, true)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if !result.HasUpdate {
		t.Error("Expected HasUpdate to be true")
	}
	if result.CurrentVersion != currentVersion {
		t.Errorf("Expected current version %q, got %q", currentVersion, result.CurrentVersion)
	}
	if result.UpstreamVersion != upstreamVersion {
		t.Errorf("Expected upstream version %q, got %q", upstreamVersion, result.UpstreamVersion)
	}
}

// TestCheckPackageNoUpdate tests that no update is detected when versions match
func TestCheckPackageNoUpdate(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pkgName := "test-cat/test-pkg"
	version := "1.0.0"

	// Create mock HTTP server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"version": version})
	}))
	defer server.Close()

	// Create ebuild
	createTestEbuild(t, overlayDir, pkgName, version)

	config := &PackagesConfig{
		Packages: map[string]PackageConfig{
			pkgName: {URL: server.URL, Parser: "json", Path: "version"},
		},
	}

	checker, err := NewChecker(overlayDir,
		WithConfigDir(configDir),
		WithPackagesConfig(config),
	)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	result, err := checker.CheckPackage(pkgName, true)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if result.HasUpdate {
		t.Error("Expected HasUpdate to be false when versions match")
	}
}

// TestCheckAllReturnsAllResults tests that CheckAll returns results for all packages
func TestCheckAllReturnsAllResults(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	// Create mock HTTP server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"version": "1.0.0"})
	}))
	defer server.Close()

	packages := map[string]PackageConfig{
		"cat1/pkg1": {URL: server.URL, Parser: "json", Path: "version"},
		"cat2/pkg2": {URL: server.URL, Parser: "json", Path: "version"},
		"cat3/pkg3": {URL: server.URL, Parser: "json", Path: "version"},
	}

	for pkgName := range packages {
		createTestEbuild(t, overlayDir, pkgName, "0.9.0")
	}

	config := &PackagesConfig{Packages: packages}

	checker, err := NewChecker(overlayDir,
		WithConfigDir(configDir),
		WithPackagesConfig(config),
	)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	results, err := checker.CheckAll(true)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if len(results) != 3 {
		t.Errorf("Expected 3 results, got %d", len(results))
	}
}

// TestCheckPackageAddsToPending tests that updates are added to pending list
func TestCheckPackageAddsToPending(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pkgName := "test-cat/test-pkg"
	currentVersion := "1.0.0"
	upstreamVersion := "2.0.0"

	// Create mock HTTP server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"version": upstreamVersion})
	}))
	defer server.Close()

	// Create ebuild
	createTestEbuild(t, overlayDir, pkgName, currentVersion)

	config := &PackagesConfig{
		Packages: map[string]PackageConfig{
			pkgName: {URL: server.URL, Parser: "json", Path: "version"},
		},
	}

	checker, err := NewChecker(overlayDir,
		WithConfigDir(configDir),
		WithPackagesConfig(config),
	)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	_, err = checker.CheckPackage(pkgName, true)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Verify pending entry
	pending, found := checker.Pending().Get(pkgName)
	if !found {
		t.Fatal("Expected pending entry to be added")
	}
	if pending.CurrentVersion != currentVersion {
		t.Errorf("Expected current version %q, got %q", currentVersion, pending.CurrentVersion)
	}
	if pending.NewVersion != upstreamVersion {
		t.Errorf("Expected new version %q, got %q", upstreamVersion, pending.NewVersion)
	}
	if pending.Status != StatusPending {
		t.Errorf("Expected status 'pending', got %q", pending.Status)
	}
}

// TestCheckPackageUpdatesCache tests that cache is updated after fetch
func TestCheckPackageUpdatesCache(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pkgName := "test-cat/test-pkg"
	upstreamVersion := "2.0.0"

	// Create mock HTTP server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"version": upstreamVersion})
	}))
	defer server.Close()

	// Create ebuild
	createTestEbuild(t, overlayDir, pkgName, "1.0.0")

	config := &PackagesConfig{
		Packages: map[string]PackageConfig{
			pkgName: {URL: server.URL, Parser: "json", Path: "version"},
		},
	}

	checker, err := NewChecker(overlayDir,
		WithConfigDir(configDir),
		WithPackagesConfig(config),
	)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	_, err = checker.CheckPackage(pkgName, true)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Verify cache was updated
	cachedVersion, found := checker.Cache().Get(pkgName)
	if !found {
		t.Fatal("Expected cache entry to be added")
	}
	if cachedVersion != upstreamVersion {
		t.Errorf("Expected cached version %q, got %q", upstreamVersion, cachedVersion)
	}
}

// TestGetCurrentVersionHighest tests that highest version is returned
func TestGetCurrentVersionHighest(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pkgName := "test-cat/test-pkg"

	// Create multiple ebuilds
	createTestEbuild(t, overlayDir, pkgName, "1.0.0")
	createTestEbuild(t, overlayDir, pkgName, "2.0.0")
	createTestEbuild(t, overlayDir, pkgName, "1.5.0")

	config := &PackagesConfig{
		Packages: map[string]PackageConfig{
			pkgName: {URL: "https://example.com", Parser: "json", Path: "v"},
		},
	}

	checker, err := NewChecker(overlayDir,
		WithConfigDir(configDir),
		WithPackagesConfig(config),
	)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	version, err := checker.getCurrentVersion(pkgName)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if version != "2.0.0" {
		t.Errorf("Expected highest version '2.0.0', got %q", version)
	}
}

// TestGetCurrentVersionSkipsLive tests that 9999 ebuilds are skipped
func TestGetCurrentVersionSkipsLive(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pkgName := "test-cat/test-pkg"

	// Create ebuilds including live
	createTestEbuild(t, overlayDir, pkgName, "1.0.0")
	createTestEbuild(t, overlayDir, pkgName, "9999")

	config := &PackagesConfig{
		Packages: map[string]PackageConfig{
			pkgName: {URL: "https://example.com", Parser: "json", Path: "v"},
		},
	}

	checker, err := NewChecker(overlayDir,
		WithConfigDir(configDir),
		WithPackagesConfig(config),
	)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	version, err := checker.getCurrentVersion(pkgName)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if version != "1.0.0" {
		t.Errorf("Expected version '1.0.0' (skipping 9999), got %q", version)
	}
}

// TestFetchUpstreamVersionFallback tests fallback parser
func TestFetchUpstreamVersionFallback(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	// Create mock HTTP server that returns non-JSON content
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("pkgver=3.0.0\npkgrel=1"))
	}))
	defer server.Close()

	pkgName := "test-cat/test-pkg"
	createTestEbuild(t, overlayDir, pkgName, "1.0.0")

	config := &PackagesConfig{
		Packages: map[string]PackageConfig{
			pkgName: {
				URL:             server.URL,
				Parser:          "json",
				Path:            "version",
				FallbackURL:     server.URL,
				FallbackParser:  "regex",
				FallbackPattern: `pkgver=([0-9.]+)`,
			},
		},
	}

	checker, err := NewChecker(overlayDir,
		WithConfigDir(configDir),
		WithPackagesConfig(config),
	)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	result, err := checker.CheckPackage(pkgName, true)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if result.UpstreamVersion != "3.0.0" {
		t.Errorf("Expected upstream version '3.0.0' from fallback, got %q", result.UpstreamVersion)
	}
}

// =============================================================================
// Helper Functions for Tests
// =============================================================================

// createTestPackagesConfig creates a packages.toml file in the overlay
func createTestPackagesConfig(t *testing.T, overlayDir string, packages map[string]PackageConfig) {
	t.Helper()

	configDir := filepath.Join(overlayDir, ".autoupdate")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("Failed to create config dir: %v", err)
	}

	// Build TOML content
	var content string
	for pkg, cfg := range packages {
		content += "[\"" + pkg + "\"]\n"
		content += "url = \"" + cfg.URL + "\"\n"
		content += "parser = \"" + cfg.Parser + "\"\n"
		if cfg.Path != "" {
			content += "path = \"" + cfg.Path + "\"\n"
		}
		if cfg.Pattern != "" {
			content += "pattern = \"" + cfg.Pattern + "\"\n"
		}
		content += "\n"
	}

	if err := os.WriteFile(filepath.Join(configDir, "packages.toml"), []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write packages.toml: %v", err)
	}
}
