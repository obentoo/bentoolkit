package autoupdate

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
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
				WithRateLimiter(unlimitedRateLimiter()),
			)
			if err != nil {
				t.Logf("Failed to create checker: %v", err)
				return false
			}

			// Check all packages
			batch := checker.CheckAll(false)

			// Verify we got exactly N results across Items and Failures.
			total := len(batch.Items) + len(batch.Failures)
			if total != numPackages {
				t.Logf("Expected %d results, got %d (Items=%d, Failures=%d)",
					numPackages, total, len(batch.Items), len(batch.Failures))
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
			targetIndex %= numPackages

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
				WithRateLimiter(unlimitedRateLimiter()),
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
			upstreamMajor := currentMajor + (majorDiff % 5) + 1 // Ensure upstream is always newer

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
				WithRateLimiter(unlimitedRateLimiter()),
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
				WithRateLimiter(unlimitedRateLimiter()),
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

// createTestEbuildContent writes an ebuild with caller-supplied content, so a
// test can shape the metadata (e.g. RESTRICT="bindist") that drives type
// auto-detection. Mirrors createTestEbuild's directory layout.
func createTestEbuildContent(t *testing.T, overlayDir, pkgName, version, content string) {
	t.Helper()

	parts := splitPackageName(pkgName)
	if len(parts) != 2 {
		t.Fatalf("Invalid package name: %s", pkgName)
	}
	pkgDir := filepath.Join(overlayDir, parts[0], parts[1])
	if err := os.MkdirAll(pkgDir, 0755); err != nil {
		t.Fatalf("Failed to create package dir: %v", err)
	}
	ebuildPath := filepath.Join(pkgDir, parts[1]+"-"+version+".ebuild")
	if err := os.WriteFile(ebuildPath, []byte(content), 0644); err != nil {
		t.Fatalf("Failed to write ebuild: %v", err)
	}
}

// TestWithTypeFilter verifies the option accepts "", "bin", "source" and
// rejects anything else.
func TestWithTypeFilter(t *testing.T) {
	for _, v := range []string{"", "bin", "source"} {
		c := &Checker{}
		if err := WithTypeFilter(v)(c); err != nil {
			t.Errorf("WithTypeFilter(%q): unexpected error: %v", v, err)
		}
		if c.typeFilter != v {
			t.Errorf("typeFilter = %q, want %q", c.typeFilter, v)
		}
	}

	c := &Checker{}
	if err := WithTypeFilter("nonsense")(c); err == nil {
		t.Error("WithTypeFilter(\"nonsense\"): expected error, got nil")
	}
}

// TestResolveType covers explicit-type precedence, ebuild auto-detection for
// both bin and source, and the "source" default when the ebuild is missing.
func TestResolveType(t *testing.T) {
	overlayDir := t.TempDir()

	// Source package: generic ebuild, no bindist / -bin markers.
	createTestEbuild(t, overlayDir, "dev-libs/srcpkg", "1.0.0")

	// Binary package: RESTRICT="bindist" triggers detectBinaryPackage.
	createTestEbuildContent(t, overlayDir, "app-editors/foo-bin", "2.0.0", `EAPI=8
DESCRIPTION="Binary package"
HOMEPAGE="https://example.com"
SLOT="0"
KEYWORDS="-* ~amd64"
RESTRICT="bindist mirror strip"
`)

	c := &Checker{overlayPath: overlayDir}

	tests := []struct {
		name string
		pkg  string
		cfg  PackageConfig
		want string
	}{
		{"explicit bin overrides source ebuild", "dev-libs/srcpkg", PackageConfig{Type: "bin"}, "bin"},
		{"explicit source overrides bin ebuild", "app-editors/foo-bin", PackageConfig{Type: "source"}, "source"},
		{"auto-detect source", "dev-libs/srcpkg", PackageConfig{}, "source"},
		{"auto-detect bin", "app-editors/foo-bin", PackageConfig{}, "bin"},
		{"missing ebuild defaults to source", "no/such-pkg", PackageConfig{}, "source"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.cfg
			if got := c.resolveType(tt.pkg, &cfg); got != tt.want {
				t.Errorf("resolveType(%q) = %q, want %q", tt.pkg, got, tt.want)
			}
		})
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

// unlimitedRateLimiter returns an httpRateLimiter that never blocks and never
// errors. NewChecker installs a real default limiter (1 req / 6s per host,
// R10.3) when WithRateLimiter is absent; unit tests below issue several
// requests to the same host in quick succession and would otherwise stall on
// the 6s-per-token wait. Injecting this no-op limiter keeps these tests fast
// without weakening the production default.
func unlimitedRateLimiter() httpRateLimiter { return &recordingRateLimiter{} }

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
		WithRateLimiter(unlimitedRateLimiter()),
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
		WithRateLimiter(unlimitedRateLimiter()),
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
		WithRateLimiter(unlimitedRateLimiter()),
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
		WithRateLimiter(unlimitedRateLimiter()),
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
		WithRateLimiter(unlimitedRateLimiter()),
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
		WithRateLimiter(unlimitedRateLimiter()),
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

// TestCheckPackageNotComparable verifies that an upstream value that is not a
// well-formed version (e.g. an upstream tag like "INKSCAPE_1_4_4") is surfaced
// as NotComparable instead of being silently coerced to a near-zero version and
// reported as "up to date" — which would mask a real update.
func TestCheckPackageNotComparable(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pkgName := "test-cat/test-pkg"
	currentVersion := "1.4.4"
	upstreamVersion := "INKSCAPE_1_4_4"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"version": upstreamVersion})
	}))
	defer server.Close()

	createTestEbuild(t, overlayDir, pkgName, currentVersion)

	config := &PackagesConfig{
		Packages: map[string]PackageConfig{
			pkgName: {URL: server.URL, Parser: "json", Path: "version"},
		},
	}

	checker, err := NewChecker(overlayDir,
		WithConfigDir(configDir),
		WithPackagesConfig(config),
		WithRateLimiter(unlimitedRateLimiter()),
	)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	result, err := checker.CheckPackage(pkgName, true)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if !result.NotComparable {
		t.Error("Expected NotComparable to be true for an unparseable upstream version")
	}
	if result.HasUpdate {
		t.Error("Expected HasUpdate to be false when the upstream version is not comparable")
	}

	// A non-comparable result must never leak into the pending list.
	if _, ok := checker.pending.Get(pkgName); ok {
		t.Error("Expected non-comparable package NOT to be added to the pending list")
	}
}

// TestCheckPackageStripsVPrefix verifies that a leading "v" on the upstream
// version is normalized before comparison, so a "v"-tagged upstream is compared
// against the bare ebuild version correctly (no false "up to date" / no false
// downgrade).
func TestCheckPackageStripsVPrefix(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pkgName := "test-cat/test-pkg"
	currentVersion := "6.6.91"
	upstreamVersion := "v7.0.0"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"version": upstreamVersion})
	}))
	defer server.Close()

	createTestEbuild(t, overlayDir, pkgName, currentVersion)

	config := &PackagesConfig{
		Packages: map[string]PackageConfig{
			pkgName: {URL: server.URL, Parser: "json", Path: "version"},
		},
	}

	checker, err := NewChecker(overlayDir,
		WithConfigDir(configDir),
		WithPackagesConfig(config),
		WithRateLimiter(unlimitedRateLimiter()),
	)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	result, err := checker.CheckPackage(pkgName, true)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if result.NotComparable {
		t.Error("Expected a v-prefixed version to be comparable")
	}
	if !result.HasUpdate {
		t.Error("Expected HasUpdate to be true (v7.0.0 > 6.6.91)")
	}
}

// TestCheckPackageHTMLParser verifies that a package configured with the "html"
// parser is actually usable from --check. It is a regression guard: the fetch
// path used to build parsers via NewParser, which rejects "html" outright
// ("use NewParserFromConfig for html parser"), so every html-configured package
// failed. Here the version lives in an href attribute, extracted via an XPath
// onto the attribute plus a regex post-processing step (carried in pattern).
func TestCheckPackageHTMLParser(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pkgName := "test-cat/test-pkg"
	currentVersion := "3.5.33"

	const page = `<html><body>
<a href="https://example.com/download/linux-x64/app/3.6">Linux AppImage (x64)</a>
<a href="https://example.com/download/win32-x64/app/3.6">Windows</a>
</body></html>`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(page))
	}))
	defer server.Close()

	createTestEbuild(t, overlayDir, pkgName, currentVersion)

	config := &PackagesConfig{
		Packages: map[string]PackageConfig{
			pkgName: {
				URL:     server.URL,
				Parser:  "html",
				XPath:   "(//a[contains(@href, '/linux-x64/app/')]/@href)[1]",
				Pattern: `app/([0-9.]+)`,
			},
		},
	}

	checker, err := NewChecker(overlayDir,
		WithConfigDir(configDir),
		WithPackagesConfig(config),
		WithRateLimiter(unlimitedRateLimiter()),
	)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	result, err := checker.CheckPackage(pkgName, true)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if result.Error != nil {
		t.Fatalf("Expected no error, got %v", result.Error)
	}
	if result.UpstreamVersion != "3.6" {
		t.Errorf("Expected upstream version %q, got %q", "3.6", result.UpstreamVersion)
	}
	if !result.HasUpdate {
		t.Error("Expected HasUpdate to be true (3.6 > 3.5.33)")
	}
}

// TestFetchContentRateLimitNotChargedToOpTimeout is a regression guard: time
// spent waiting on the per-host rate limiter must NOT count against the
// per-operation HTTP timeout. A limiter wait longer than opTimeout previously
// made the fetch fail with "context deadline exceeded" before any request was
// issued — so packages sharing a busy host failed spuriously. Here the limiter
// waits 300ms while opTimeout is 100ms; the fetch must still succeed because
// the 100ms deadline only starts after the token is acquired.
func TestFetchContentRateLimitNotChargedToOpTimeout(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pkgName := "test-cat/test-pkg"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"version": "2.0.0"})
	}))
	defer server.Close()

	createTestEbuild(t, overlayDir, pkgName, "1.0.0")

	config := &PackagesConfig{
		Packages: map[string]PackageConfig{
			pkgName: {URL: server.URL, Parser: "json", Path: "version"},
		},
	}

	checker, err := NewChecker(overlayDir,
		WithConfigDir(configDir),
		WithPackagesConfig(config),
		// Limiter wait (300ms) deliberately exceeds the op timeout (100ms).
		WithRateLimiter(sleepingRateLimiter{d: 300 * time.Millisecond}),
		WithOpTimeout(100*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	result, err := checker.CheckPackage(pkgName, true)
	if err != nil {
		t.Fatalf("fetch failed despite a healthy server (rate-limit wait charged to opTimeout?): %v", err)
	}
	if result.Error != nil {
		t.Fatalf("unexpected result error: %v", result.Error)
	}
	if !result.HasUpdate {
		t.Error("expected update 1.0.0 -> 2.0.0")
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
		WithRateLimiter(unlimitedRateLimiter()),
	)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	batch := checker.CheckAll(true)

	total := len(batch.Items) + len(batch.Failures)
	if total != 3 {
		t.Errorf("Expected 3 results, got %d (Items=%d, Failures=%d)",
			total, len(batch.Items), len(batch.Failures))
	}
}

// TestCheckAll_ReturnsBatchResult verifies CheckAll returns a BatchResult that
// separates successfully checked packages from per-package failures. Three
// packages are configured; one is pointed at a URL that always returns HTTP
// 500, so it must land in Failures keyed by its package name while the other
// two succeed.
func TestCheckAll_ReturnsBatchResult(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	// okServer always returns a valid version JSON payload.
	okServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"version": "1.0.0"})
	}))
	defer okServer.Close()

	// failServer always returns HTTP 500, forcing ErrFetchFailed.
	failServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer failServer.Close()

	const failingPkg = "cat3/pkg3"
	packages := map[string]PackageConfig{
		"cat1/pkg1": {URL: okServer.URL, Parser: "json", Path: "version"},
		"cat2/pkg2": {URL: okServer.URL, Parser: "json", Path: "version"},
		failingPkg:  {URL: failServer.URL, Parser: "json", Path: "version"},
	}

	for pkgName := range packages {
		createTestEbuild(t, overlayDir, pkgName, "0.9.0")
	}

	// Disable HTTP retries so the failing package fails fast.
	httpClient := NewRetryableHTTPClientWithConfig(RetryConfig{
		MaxRetries: 0,
		Timeout:    5 * time.Second,
	})

	checker, err := NewChecker(overlayDir,
		WithConfigDir(configDir),
		WithPackagesConfig(&PackagesConfig{Packages: packages}),
		WithHTTPClient(httpClient),
		WithRateLimiter(unlimitedRateLimiter()),
	)
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}

	batch := checker.CheckAll(true)

	if len(batch.Items) != 2 {
		t.Errorf("expected 2 successful items, got %d", len(batch.Items))
	}
	if len(batch.Failures) != 1 {
		t.Errorf("expected 1 failure, got %d", len(batch.Failures))
	}
	if _, ok := batch.Failures[failingPkg]; !ok {
		t.Errorf("expected failure keyed by %q, got keys %v", failingPkg, failureKeys(batch.Failures))
	}
	// Successes must not include the failing package.
	for _, item := range batch.Items {
		if item.Package == failingPkg {
			t.Errorf("failing package %q leaked into Items", failingPkg)
		}
	}
	// Partial failure: ExitCode must be 1.
	if got := batch.ExitCode(); got != 1 {
		t.Errorf("expected ExitCode 1 (partial), got %d", got)
	}
}

// TestCheckAll_ErrorsOnStderr verifies that the failures recorded by CheckAll
// are emitted by FormatFailures in deterministic lexical order regardless of
// the map iteration order. Run under -race to catch any data race in the
// sequential capture path.
func TestCheckAll_ErrorsOnStderr(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	// failServer always returns HTTP 500 so every package fails.
	failServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer failServer.Close()

	// Package names deliberately not in sorted order.
	pkgNames := []string{"cat-z/zeta", "cat-a/alpha", "cat-m/mike"}
	packages := make(map[string]PackageConfig, len(pkgNames))
	for _, pkgName := range pkgNames {
		packages[pkgName] = PackageConfig{URL: failServer.URL, Parser: "json", Path: "version"}
		createTestEbuild(t, overlayDir, pkgName, "0.9.0")
	}

	httpClient := NewRetryableHTTPClientWithConfig(RetryConfig{
		MaxRetries: 0,
		Timeout:    5 * time.Second,
	})

	checker, err := NewChecker(overlayDir,
		WithConfigDir(configDir),
		WithPackagesConfig(&PackagesConfig{Packages: packages}),
		WithHTTPClient(httpClient),
		WithRateLimiter(unlimitedRateLimiter()),
	)
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}

	batch := checker.CheckAll(true)

	if len(batch.Failures) != 3 {
		t.Fatalf("expected 3 failures, got %d", len(batch.Failures))
	}
	// Total failure: no item succeeded.
	if got := batch.ExitCode(); got != 2 {
		t.Errorf("expected ExitCode 2 (total failure), got %d", got)
	}

	var buf bytes.Buffer
	batch.FormatFailures(&buf)

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 stderr lines, got %d: %q", len(lines), buf.String())
	}

	// Each line must start with "ERROR <pkg>: " and the package order must be
	// lexically sorted.
	gotPkgs := make([]string, 0, len(lines))
	for _, line := range lines {
		if !strings.HasPrefix(line, "ERROR ") {
			t.Errorf("line missing ERROR prefix: %q", line)
			continue
		}
		rest := strings.TrimPrefix(line, "ERROR ")
		idx := strings.Index(rest, ": ")
		if idx < 0 {
			t.Errorf("line missing ': ' separator: %q", line)
			continue
		}
		gotPkgs = append(gotPkgs, rest[:idx])
	}

	wantPkgs := append([]string(nil), pkgNames...)
	sort.Strings(wantPkgs)
	if !equalStringSlices(gotPkgs, wantPkgs) {
		t.Errorf("stderr package order = %v, want sorted %v", gotPkgs, wantPkgs)
	}
}

// failureKeys returns the sorted keys of a Failures map for diagnostics.
func failureKeys(failures map[string]error) []string {
	keys := make([]string, 0, len(failures))
	for k := range failures {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// equalStringSlices reports whether two string slices are element-wise equal.
func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
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
		WithRateLimiter(unlimitedRateLimiter()),
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
		WithRateLimiter(unlimitedRateLimiter()),
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
		WithRateLimiter(unlimitedRateLimiter()),
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
		WithRateLimiter(unlimitedRateLimiter()),
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
		WithRateLimiter(unlimitedRateLimiter()),
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

// =============================================================================
// R2: cache_ttl honours user configuration
// =============================================================================

// TestWithCacheTTL_Custom verifies R2.1: a positive WithCacheTTL value reaches
// the underlying Cache.TTL field, overriding the default 1-hour TTL.
func TestWithCacheTTL_Custom(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	custom := 5 * time.Minute
	checker, err := NewChecker(overlayDir,
		WithConfigDir(configDir),
		WithPackagesConfig(&PackagesConfig{Packages: map[string]PackageConfig{}}),
		WithCacheTTL(custom),
	)
	if err != nil {
		t.Fatalf("NewChecker failed: %v", err)
	}

	if got := checker.Cache().TTL; got != custom {
		t.Errorf("Cache().TTL = %v, want %v (R2.1)", got, custom)
	}
}

// TestWithCacheTTL_DefaultWhenAbsent verifies R2.2: when WithCacheTTL is not
// supplied, the Cache keeps its DefaultCacheTTL of 1 hour.
func TestWithCacheTTL_DefaultWhenAbsent(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	checker, err := NewChecker(overlayDir,
		WithConfigDir(configDir),
		WithPackagesConfig(&PackagesConfig{Packages: map[string]PackageConfig{}}),
	)
	if err != nil {
		t.Fatalf("NewChecker failed: %v", err)
	}

	if got := checker.Cache().TTL; got != DefaultCacheTTL {
		t.Errorf("Cache().TTL = %v, want default %v (R2.2)", got, DefaultCacheTTL)
	}
}

// TestWithCacheTTL_RejectsNonPositive verifies R2.2: WithCacheTTL rejects zero
// and negative durations at construction time, mirroring WithOpTimeout's
// validation. The CLI guards positive values upstream via GetCacheTTL, so this
// is defence-in-depth for direct API callers.
func TestWithCacheTTL_RejectsNonPositive(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	for _, d := range []time.Duration{0, -1, -1 * time.Second} {
		t.Run(d.String(), func(t *testing.T) {
			_, err := NewChecker(overlayDir,
				WithConfigDir(configDir),
				WithPackagesConfig(&PackagesConfig{Packages: map[string]PackageConfig{}}),
				WithCacheTTL(d),
			)
			if err == nil {
				t.Errorf("NewChecker with WithCacheTTL(%v) succeeded; want construction error (R2.2)", d)
			}
		})
	}
}

// =============================================================================
// R4: llm_prompt is documented as analyze-only; --check emits a Warn (T4.1)
// =============================================================================

// TestNewChecker_WarnsOnUnusedLLMPrompt verifies R4.2: building a Checker
// without an LLM emits exactly one Warn per package whose LLMPrompt is set,
// identifying the package and stating that the LLM is not wired into the
// check path. R4.3 is exercised by passing PackageConfigs that carry the
// field — they must not be rejected at construction time.
func TestNewChecker_WarnsOnUnusedLLMPrompt(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	cfg := &PackagesConfig{Packages: map[string]PackageConfig{
		"cat-a/pkg-one":   {URL: "https://example.com/a", Parser: "json", Path: "v", LLMPrompt: "extract version"},
		"cat-b/pkg-two":   {URL: "https://example.com/b", Parser: "json", Path: "v", LLMPrompt: "find latest"},
		"cat-c/pkg-three": {URL: "https://example.com/c", Parser: "json", Path: "v"}, // no llm_prompt
	}}

	logs := captureWarnLogs(t)

	_, err := NewChecker(overlayDir,
		WithConfigDir(configDir),
		WithPackagesConfig(cfg),
	)
	if err != nil {
		t.Fatalf("NewChecker failed: %v", err)
	}

	// Filter the captured Warns down to ones that mention this story's
	// dedicated phrase so unrelated Warns (from other init paths) don't
	// poison the count.
	want := []string{"cat-a/pkg-one", "cat-b/pkg-two"}
	llmWarns := llmPromptWarnsFor(logs.all(), want)
	if len(llmWarns) != len(want) {
		t.Errorf("got %d llm_prompt Warns, want %d (R4.2): %v",
			len(llmWarns), len(want), llmWarns)
	}

	// The package without an llm_prompt must NOT appear in any llm_prompt Warn.
	for _, line := range llmWarns {
		if strings.Contains(line, "cat-c/pkg-three") {
			t.Errorf("Warn unexpectedly mentions package without llm_prompt: %q", line)
		}
	}

	// Build a SECOND Checker from the same config: de-dup is per Checker
	// instance, so the Warns must repeat (R4.2).
	logs2 := captureWarnLogs(t)
	_, err = NewChecker(overlayDir,
		WithConfigDir(configDir),
		WithPackagesConfig(cfg),
	)
	if err != nil {
		t.Fatalf("second NewChecker failed: %v", err)
	}
	llmWarns2 := llmPromptWarnsFor(logs2.all(), want)
	if len(llmWarns2) != len(want) {
		t.Errorf("second Checker produced %d llm_prompt Warns, want %d (R4.2 per-instance dedup): %v",
			len(llmWarns2), len(want), llmWarns2)
	}
}

// llmPromptWarnsFor returns the subset of Warn lines that name one of the
// expected packages and identify the llm_prompt+check gap (R4.2).
func llmPromptWarnsFor(lines []string, pkgs []string) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if !strings.Contains(line, "llm_prompt") {
			continue
		}
		for _, p := range pkgs {
			if strings.Contains(line, p) {
				out = append(out, line)
				break
			}
		}
	}
	// Sort for stable comparison/printing.
	sort.Strings(out)
	return out
}

// =============================================================================
// R5 / R8.1: Checker is programmed to the LLMProvider interface (AD2)
// =============================================================================

// fakeLLMProvider is a non-claude LLMProvider used to prove the Checker now
// accepts ANY provider via WithLLMClient (AD2), not just the legacy claude
// *LLMClient. It records the content/prompt of the last ExtractVersion call so
// a test can assert the LLM fallback path was actually taken, and returns a
// caller-supplied version (or err). It deliberately does NOT embed *LLMClient.
type fakeLLMProvider struct {
	version string
	err     error

	called     bool
	gotContent []byte
	gotPrompt  string
}

func (f *fakeLLMProvider) ExtractVersion(content []byte, prompt string) (string, error) {
	f.called = true
	f.gotContent = content
	f.gotPrompt = prompt
	return f.version, f.err
}

func (f *fakeLLMProvider) AnalyzeContent(_ []byte, _ *EbuildMetadata, _ string) (*SchemaAnalysis, error) {
	return &SchemaAnalysis{ParserType: "json"}, nil
}

func (f *fakeLLMProvider) GetModel() string { return "fake-model" }

// TestWithLLMClient_AcceptsFakeProvider verifies the AD2 refactor: WithLLMClient
// now takes an LLMProvider, so a non-claude provider — which the pre-refactor
// `*LLMClient` parameter could not express and the legacy NewLLMClient rejects
// outright — is accepted and stored. This is the regression-vs-old-rejection
// guard for R5/R8.1.
func TestWithLLMClient_AcceptsFakeProvider(t *testing.T) {
	tmpDir := t.TempDir()
	fake := &fakeLLMProvider{version: "9.9.9"}

	cfg := &PackagesConfig{Packages: map[string]PackageConfig{}}
	checker, err := NewChecker(tmpDir,
		WithConfigDir(filepath.Join(tmpDir, "config")),
		WithPackagesConfig(cfg),
		WithLLMClient(fake),
	)
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}
	if checker.llmClient != fake {
		t.Error("Expected llmClient to be the injected fake LLMProvider (AD2: any provider is accepted)")
	}
}

// TestWithLLMClient_NilLeavesFieldNil verifies the WithLLMClient nil-guard: a
// nil provider is ignored and llmClient stays an UNTYPED nil. This is the
// defence-in-depth that keeps the typed-nil from a failed CLI constructor out
// of the field, so the `== nil` warn gate and the `!= nil` fetch gate behave.
func TestWithLLMClient_NilLeavesFieldNil(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &PackagesConfig{Packages: map[string]PackageConfig{}}
	checker, err := NewChecker(tmpDir,
		WithConfigDir(filepath.Join(tmpDir, "config")),
		WithPackagesConfig(cfg),
		WithLLMClient(nil),
	)
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}
	if checker.llmClient != nil {
		t.Error("Expected llmClient to remain nil after WithLLMClient(nil)")
	}
}

// TestFetchUpstreamVersion_UsesProviderWhenParseFails verifies R5.2: when the
// primary (and fallback) parse fails and the package sets llm_prompt, the
// configured LLMProvider's ExtractVersion supplies the version. The server
// returns plain text with no extractable JSON "version", so the json parser
// fails and the LLM fallback is exercised. The fake provider both returns the
// upstream version AND records that it was actually called with the fetched
// page content.
func TestFetchUpstreamVersion_UsesProviderWhenParseFails(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pkgName := "test-cat/test-pkg"
	currentVersion := "1.0.0"
	const page = "no version key here, just prose about a release"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(page))
	}))
	defer server.Close()

	createTestEbuild(t, overlayDir, pkgName, currentVersion)

	config := &PackagesConfig{
		Packages: map[string]PackageConfig{
			// json parser on non-JSON content fails, so the LLM fallback runs.
			pkgName: {URL: server.URL, Parser: "json", Path: "version", LLMPrompt: "extract the version"},
		},
	}

	fake := &fakeLLMProvider{version: "2.0.0"}

	checker, err := NewChecker(overlayDir,
		WithConfigDir(configDir),
		WithPackagesConfig(config),
		WithRateLimiter(unlimitedRateLimiter()),
		WithLLMClient(fake),
		WithLLMProviderConfigured(true),
	)
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}

	result, err := checker.CheckPackage(pkgName, true)
	if err != nil {
		t.Fatalf("CheckPackage: %v", err)
	}
	if result.Error != nil {
		t.Fatalf("unexpected result error: %v", result.Error)
	}

	if !fake.called {
		t.Fatal("expected the LLM provider's ExtractVersion to be invoked on parse failure (R5.2)")
	}
	if fake.gotPrompt != "extract the version" {
		t.Errorf("provider got prompt %q, want %q", fake.gotPrompt, "extract the version")
	}
	if string(fake.gotContent) != page {
		t.Errorf("provider got content %q, want the fetched page %q", string(fake.gotContent), page)
	}
	if result.UpstreamVersion != "2.0.0" {
		t.Errorf("UpstreamVersion = %q, want %q (from the LLM provider)", result.UpstreamVersion, "2.0.0")
	}
	if !result.HasUpdate {
		t.Error("expected HasUpdate true (2.0.0 > 1.0.0)")
	}
}

// TestNewChecker_NoProviderConfigured_WarnsAndSkipsLLM verifies R5.3: when no
// provider is configured (WithLLMProviderConfigured(false), llmClient nil) and
// a package sets llm_prompt, NewChecker emits the unused-llm_prompt Warn AND a
// subsequent check skips LLM extraction without crashing (the nil llmClient
// gate in fetchUpstreamVersion is respected — no nil dereference). The parse
// itself succeeds here, so the check completes normally.
func TestNewChecker_NoProviderConfigured_WarnsAndSkipsLLM(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")
	configDir := filepath.Join(tmpDir, "config")

	pkgName := "cat-a/pkg-one"
	createTestEbuild(t, overlayDir, pkgName, "1.0.0")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"version": "2.0.0"})
	}))
	defer server.Close()

	cfg := &PackagesConfig{Packages: map[string]PackageConfig{
		pkgName: {URL: server.URL, Parser: "json", Path: "version", LLMPrompt: "extract version"},
	}}

	logs := captureWarnLogs(t)

	checker, err := NewChecker(overlayDir,
		WithConfigDir(configDir),
		WithPackagesConfig(cfg),
		WithRateLimiter(unlimitedRateLimiter()),
		WithLLMProviderConfigured(false), // no provider configured
	)
	if err != nil {
		t.Fatalf("NewChecker: %v", err)
	}

	// R5.3: the unused-llm_prompt Warn fired for the package.
	warns := llmPromptWarnsFor(logs.all(), []string{pkgName})
	if len(warns) != 1 {
		t.Fatalf("got %d unused-llm_prompt Warns, want 1 (R5.3): %v", len(warns), warns)
	}

	// The check must not crash on the nil llmClient and should succeed via the
	// normal json parse (LLM extraction skipped).
	result, err := checker.CheckPackage(pkgName, true)
	if err != nil {
		t.Fatalf("CheckPackage: %v", err)
	}
	if result.UpstreamVersion != "2.0.0" {
		t.Errorf("UpstreamVersion = %q, want %q (normal parse; LLM skipped)", result.UpstreamVersion, "2.0.0")
	}
}

// TestNewChecker_ProviderConfigured_SuppressesUnusedWarn verifies R5.3's
// suppression half: the unused-llm_prompt Warn must NOT fire when a provider was
// configured for the run. It covers BOTH wirings the CLI can produce:
//   - configured AND wired (llmClient != nil, llmProviderConfigured true);
//   - configured but FAILED to build (llmClient nil, llmProviderConfigured
//     true) — runCheck logs its own failure Warn, so this construction Warn is
//     suppressed to avoid a double-warn.
func TestNewChecker_ProviderConfigured_SuppressesUnusedWarn(t *testing.T) {
	tmpDir := t.TempDir()
	overlayDir := filepath.Join(tmpDir, "overlay")

	pkgName := "cat-a/pkg-one"
	cfg := &PackagesConfig{Packages: map[string]PackageConfig{
		pkgName: {URL: "https://example.com/a", Parser: "json", Path: "v", LLMPrompt: "extract version"},
	}}

	cases := []struct {
		name string
		opts []CheckerOption
	}{
		{
			name: "configured and wired",
			opts: []CheckerOption{
				WithLLMClient(&fakeLLMProvider{version: "9.9.9"}),
				WithLLMProviderConfigured(true),
			},
		},
		{
			name: "configured but construction failed (nil provider)",
			opts: []CheckerOption{
				// llmClient stays nil (no WithLLMClient), but the run DID
				// configure a provider — mirrors runCheck's failure branch.
				WithLLMProviderConfigured(true),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureWarnLogs(t)

			opts := append([]CheckerOption{
				WithConfigDir(filepath.Join(tmpDir, tc.name)),
				WithPackagesConfig(cfg),
				WithRateLimiter(unlimitedRateLimiter()),
			}, tc.opts...)

			if _, err := NewChecker(overlayDir, opts...); err != nil {
				t.Fatalf("NewChecker: %v", err)
			}

			if warns := llmPromptWarnsFor(logs.all(), []string{pkgName}); len(warns) != 0 {
				t.Errorf("unused-llm_prompt Warn fired despite a configured provider (R5.3): %v", warns)
			}
		})
	}
}
