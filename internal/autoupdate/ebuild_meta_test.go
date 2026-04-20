package autoupdate

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

// =============================================================================
// Test Data Generators for Ebuild Metadata
// =============================================================================

// genEbuildCategory generates valid Gentoo category names for ebuild tests
func genEbuildCategory() gopter.Gen {
	return gen.OneConstOf(
		"app-misc", "dev-util", "net-misc", "sys-apps", "app-editors",
		"dev-python", "dev-nodejs", "dev-lang", "media-video", "www-client",
	)
}

// genEbuildPkgName generates valid package names for ebuild tests
func genEbuildPkgName() gopter.Gen {
	return gen.RegexMatch(`^[a-z][a-z0-9]{2,10}$`)
}

// genEbuildVersion generates valid Gentoo version strings for ebuild tests
func genEbuildVersion() gopter.Gen {
	return gen.OneConstOf(
		"1.0.0", "1.2.3", "2.0.0", "0.9.1", "3.1.4",
		"1.0", "2.1", "0.5",
	)
}

// genEbuildHomepage generates valid homepage URLs for ebuild tests
func genEbuildHomepage() gopter.Gen {
	return gen.OneConstOf(
		"https://github.com/example/project",
		"https://gitlab.com/user/repo",
		"https://example.com",
		"https://pypi.org/project/example",
		"https://www.npmjs.com/package/example",
		"https://crates.io/crates/example",
	)
}

// genEbuildSrcURI generates valid SRC_URI values for ebuild tests
func genEbuildSrcURI() gopter.Gen {
	return gen.OneConstOf(
		"https://github.com/example/project/archive/v1.0.0.tar.gz",
		"https://example.com/releases/pkg-1.0.0.tar.gz",
		"https://files.pythonhosted.org/packages/example-1.0.0.tar.gz",
	)
}

// =============================================================================
// Property-Based Tests
// =============================================================================

// TestEbuildMetadataExtraction tests Property 5: Ebuild Metadata Extraction
// **Feature: autoupdate-analyzer, Property 5: Ebuild Metadata Extraction**
// **Validates: Requirements 3.1, 3.2, 3.3**
func TestEbuildMetadataExtraction(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: ExtractEbuildMetadata correctly extracts HOMEPAGE when present
	properties.Property("extracts HOMEPAGE from ebuild", prop.ForAll(
		func(category, pkgName, version, homepage string) bool {
			// Create temporary overlay structure
			tmpDir := t.TempDir()
			pkgDir := filepath.Join(tmpDir, category, pkgName)
			if err := os.MkdirAll(pkgDir, 0755); err != nil {
				t.Logf("Failed to create dir: %v", err)
				return false
			}

			// Create ebuild file with HOMEPAGE
			ebuildContent := `# Copyright
EAPI=8
HOMEPAGE="` + homepage + `"
SRC_URI=""
DEPEND=""
`
			ebuildPath := filepath.Join(pkgDir, pkgName+"-"+version+".ebuild")
			if err := os.WriteFile(ebuildPath, []byte(ebuildContent), 0644); err != nil {
				t.Logf("Failed to write ebuild: %v", err)
				return false
			}

			// Extract metadata
			meta, err := ExtractEbuildMetadata(tmpDir, category+"/"+pkgName)
			if err != nil {
				t.Logf("ExtractEbuildMetadata failed: %v", err)
				return false
			}

			return meta.Homepage == homepage
		},
		genEbuildCategory(),
		genEbuildPkgName(),
		genEbuildVersion(),
		genEbuildHomepage(),
	))

	// Property: ExtractEbuildMetadata correctly extracts SRC_URI when present
	properties.Property("extracts SRC_URI from ebuild", prop.ForAll(
		func(category, pkgName, version, srcURI string) bool {
			// Create temporary overlay structure
			tmpDir := t.TempDir()
			pkgDir := filepath.Join(tmpDir, category, pkgName)
			if err := os.MkdirAll(pkgDir, 0755); err != nil {
				t.Logf("Failed to create dir: %v", err)
				return false
			}

			// Create ebuild file with SRC_URI
			ebuildContent := `# Copyright
EAPI=8
HOMEPAGE=""
SRC_URI="` + srcURI + `"
DEPEND=""
`
			ebuildPath := filepath.Join(pkgDir, pkgName+"-"+version+".ebuild")
			if err := os.WriteFile(ebuildPath, []byte(ebuildContent), 0644); err != nil {
				t.Logf("Failed to write ebuild: %v", err)
				return false
			}

			// Extract metadata
			meta, err := ExtractEbuildMetadata(tmpDir, category+"/"+pkgName)
			if err != nil {
				t.Logf("ExtractEbuildMetadata failed: %v", err)
				return false
			}

			return meta.SrcURI == srcURI
		},
		genEbuildCategory(),
		genEbuildPkgName(),
		genEbuildVersion(),
		genEbuildSrcURI(),
	))

	// Property: ExtractEbuildMetadata correctly extracts DEPEND entries
	properties.Property("extracts DEPEND from ebuild", prop.ForAll(
		func(category, pkgName, version string) bool {
			// Create temporary overlay structure
			tmpDir := t.TempDir()
			pkgDir := filepath.Join(tmpDir, category, pkgName)
			if err := os.MkdirAll(pkgDir, 0755); err != nil {
				t.Logf("Failed to create dir: %v", err)
				return false
			}

			// Create ebuild file with DEPEND
			ebuildContent := `# Copyright
EAPI=8
HOMEPAGE=""
SRC_URI=""
DEPEND="dev-libs/openssl sys-libs/zlib"
RDEPEND="app-misc/screen"
`
			ebuildPath := filepath.Join(pkgDir, pkgName+"-"+version+".ebuild")
			if err := os.WriteFile(ebuildPath, []byte(ebuildContent), 0644); err != nil {
				t.Logf("Failed to write ebuild: %v", err)
				return false
			}

			// Extract metadata
			meta, err := ExtractEbuildMetadata(tmpDir, category+"/"+pkgName)
			if err != nil {
				t.Logf("ExtractEbuildMetadata failed: %v", err)
				return false
			}

			// Check that dependencies were extracted
			hasDeps := len(meta.Dependencies) >= 3
			hasOpenssl := false
			hasZlib := false
			hasScreen := false
			for _, dep := range meta.Dependencies {
				if dep == "dev-libs/openssl" {
					hasOpenssl = true
				}
				if dep == "sys-libs/zlib" {
					hasZlib = true
				}
				if dep == "app-misc/screen" {
					hasScreen = true
				}
			}

			return hasDeps && hasOpenssl && hasZlib && hasScreen
		},
		genEbuildCategory(),
		genEbuildPkgName(),
		genEbuildVersion(),
	))

	properties.TestingRun(t)
}

// =============================================================================
// Unit Tests - ExtractEbuildMetadata
// =============================================================================

// TestExtractEbuildMetadataBasic tests basic metadata extraction
func TestExtractEbuildMetadataBasic(t *testing.T) {
	tmpDir := t.TempDir()
	pkgDir := filepath.Join(tmpDir, "app-misc", "hello")
	if err := os.MkdirAll(pkgDir, 0755); err != nil {
		t.Fatalf("Failed to create dir: %v", err)
	}

	ebuildContent := `# Copyright
EAPI=8
HOMEPAGE="https://github.com/example/hello"
SRC_URI="https://github.com/example/hello/archive/v1.0.0.tar.gz"
DEPEND="dev-libs/openssl"
RDEPEND="sys-libs/zlib"
`
	ebuildPath := filepath.Join(pkgDir, "hello-1.0.0.ebuild")
	if err := os.WriteFile(ebuildPath, []byte(ebuildContent), 0644); err != nil {
		t.Fatalf("Failed to write ebuild: %v", err)
	}

	meta, err := ExtractEbuildMetadata(tmpDir, "app-misc/hello")
	if err != nil {
		t.Fatalf("ExtractEbuildMetadata failed: %v", err)
	}

	if meta.Package != "app-misc/hello" {
		t.Errorf("Expected package 'app-misc/hello', got %q", meta.Package)
	}
	if meta.Version != "1.0.0" {
		t.Errorf("Expected version '1.0.0', got %q", meta.Version)
	}
	if meta.Homepage != "https://github.com/example/hello" {
		t.Errorf("Expected homepage 'https://github.com/example/hello', got %q", meta.Homepage)
	}
	if meta.SrcURI != "https://github.com/example/hello/archive/v1.0.0.tar.gz" {
		t.Errorf("Expected SRC_URI, got %q", meta.SrcURI)
	}
	if meta.IsLive {
		t.Error("Expected IsLive to be false")
	}
	if meta.IsBinary {
		t.Error("Expected IsBinary to be false")
	}
}

// TestExtractEbuildMetadataMultipleVersions tests selecting highest version
func TestExtractEbuildMetadataMultipleVersions(t *testing.T) {
	tmpDir := t.TempDir()
	pkgDir := filepath.Join(tmpDir, "app-misc", "hello")
	if err := os.MkdirAll(pkgDir, 0755); err != nil {
		t.Fatalf("Failed to create dir: %v", err)
	}

	// Create multiple ebuild versions
	versions := []string{"1.0.0", "1.1.0", "2.0.0", "1.5.0"}
	for _, v := range versions {
		ebuildContent := `EAPI=8
HOMEPAGE="https://example.com"
`
		ebuildPath := filepath.Join(pkgDir, "hello-"+v+".ebuild")
		if err := os.WriteFile(ebuildPath, []byte(ebuildContent), 0644); err != nil {
			t.Fatalf("Failed to write ebuild: %v", err)
		}
	}

	meta, err := ExtractEbuildMetadata(tmpDir, "app-misc/hello")
	if err != nil {
		t.Fatalf("ExtractEbuildMetadata failed: %v", err)
	}

	// Should select highest version (2.0.0)
	if meta.Version != "2.0.0" {
		t.Errorf("Expected version '2.0.0', got %q", meta.Version)
	}
}

// TestExtractEbuildMetadataPackageNotFound tests error for missing package
func TestExtractEbuildMetadataPackageNotFound(t *testing.T) {
	tmpDir := t.TempDir()

	_, err := ExtractEbuildMetadata(tmpDir, "app-misc/nonexistent")
	if err == nil {
		t.Error("Expected error for missing package")
	}
	if !errors.Is(err, ErrPackageNotFound) {
		t.Errorf("Expected ErrPackageNotFound, got: %v", err)
	}
}

// TestExtractEbuildMetadataNoEbuilds tests error for empty package directory
func TestExtractEbuildMetadataNoEbuilds(t *testing.T) {
	tmpDir := t.TempDir()
	pkgDir := filepath.Join(tmpDir, "app-misc", "empty")
	if err := os.MkdirAll(pkgDir, 0755); err != nil {
		t.Fatalf("Failed to create dir: %v", err)
	}

	_, err := ExtractEbuildMetadata(tmpDir, "app-misc/empty")
	if err == nil {
		t.Error("Expected error for empty package directory")
	}
	if !errors.Is(err, ErrEbuildNotFound) {
		t.Errorf("Expected ErrEbuildNotFound, got: %v", err)
	}
}

// TestExtractEbuildMetadataInvalidPackageFormat tests error for invalid package format
func TestExtractEbuildMetadataInvalidPackageFormat(t *testing.T) {
	tmpDir := t.TempDir()

	testCases := []string{
		"invalid",
		"too/many/parts",
		"",
	}

	for _, pkg := range testCases {
		_, err := ExtractEbuildMetadata(tmpDir, pkg)
		if err == nil {
			t.Errorf("Expected error for invalid package format %q", pkg)
		}
	}
}

// TestExtractEbuildMetadataMultiLineSrcURI tests multi-line SRC_URI extraction
func TestExtractEbuildMetadataMultiLineSrcURI(t *testing.T) {
	tmpDir := t.TempDir()
	pkgDir := filepath.Join(tmpDir, "app-misc", "hello")
	if err := os.MkdirAll(pkgDir, 0755); err != nil {
		t.Fatalf("Failed to create dir: %v", err)
	}

	ebuildContent := `EAPI=8
HOMEPAGE="https://example.com"
SRC_URI="https://example.com/file1.tar.gz
	https://example.com/file2.tar.gz"
`
	ebuildPath := filepath.Join(pkgDir, "hello-1.0.0.ebuild")
	if err := os.WriteFile(ebuildPath, []byte(ebuildContent), 0644); err != nil {
		t.Fatalf("Failed to write ebuild: %v", err)
	}

	meta, err := ExtractEbuildMetadata(tmpDir, "app-misc/hello")
	if err != nil {
		t.Fatalf("ExtractEbuildMetadata failed: %v", err)
	}

	// Should contain both URLs
	if meta.SrcURI == "" {
		t.Error("Expected SRC_URI to be extracted")
	}
}

// TestLiveEbuildDetection tests Property 6: Live Ebuild Detection
// **Feature: autoupdate-analyzer, Property 6: Live Ebuild Detection**
// **Validates: Requirements 3.4**
func TestLiveEbuildDetection(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: Ebuild with version 9999 is detected as live
	properties.Property("version 9999 is detected as live ebuild", prop.ForAll(
		func(category, pkgName string) bool {
			// Create temporary overlay structure
			tmpDir := t.TempDir()
			pkgDir := filepath.Join(tmpDir, category, pkgName)
			if err := os.MkdirAll(pkgDir, 0755); err != nil {
				t.Logf("Failed to create dir: %v", err)
				return false
			}

			// Create live ebuild file (version 9999)
			ebuildContent := `# Copyright
EAPI=8
HOMEPAGE="https://example.com"
SRC_URI=""
`
			ebuildPath := filepath.Join(pkgDir, pkgName+"-9999.ebuild")
			if err := os.WriteFile(ebuildPath, []byte(ebuildContent), 0644); err != nil {
				t.Logf("Failed to write ebuild: %v", err)
				return false
			}

			// Extract metadata
			meta, err := ExtractEbuildMetadata(tmpDir, category+"/"+pkgName)
			if err != nil {
				t.Logf("ExtractEbuildMetadata failed: %v", err)
				return false
			}

			return meta.IsLive && meta.Version == "9999"
		},
		genEbuildCategory(),
		genEbuildPkgName(),
	))

	// Property: Ebuild with non-9999 version is not detected as live
	properties.Property("non-9999 version is not detected as live ebuild", prop.ForAll(
		func(category, pkgName, version string) bool {
			// Create temporary overlay structure
			tmpDir := t.TempDir()
			pkgDir := filepath.Join(tmpDir, category, pkgName)
			if err := os.MkdirAll(pkgDir, 0755); err != nil {
				t.Logf("Failed to create dir: %v", err)
				return false
			}

			// Create regular ebuild file
			ebuildContent := `# Copyright
EAPI=8
HOMEPAGE="https://example.com"
SRC_URI=""
`
			ebuildPath := filepath.Join(pkgDir, pkgName+"-"+version+".ebuild")
			if err := os.WriteFile(ebuildPath, []byte(ebuildContent), 0644); err != nil {
				t.Logf("Failed to write ebuild: %v", err)
				return false
			}

			// Extract metadata
			meta, err := ExtractEbuildMetadata(tmpDir, category+"/"+pkgName)
			if err != nil {
				t.Logf("ExtractEbuildMetadata failed: %v", err)
				return false
			}

			return !meta.IsLive
		},
		genEbuildCategory(),
		genEbuildPkgName(),
		genEbuildVersion(),
	))

	// Property: When both 9999 and regular versions exist, prefer regular version
	properties.Property("prefers regular version over 9999 when both exist", prop.ForAll(
		func(category, pkgName, version string) bool {
			// Create temporary overlay structure
			tmpDir := t.TempDir()
			pkgDir := filepath.Join(tmpDir, category, pkgName)
			if err := os.MkdirAll(pkgDir, 0755); err != nil {
				t.Logf("Failed to create dir: %v", err)
				return false
			}

			// Create both live and regular ebuild files
			ebuildContent := `# Copyright
EAPI=8
HOMEPAGE="https://example.com"
SRC_URI=""
`
			// Create regular version
			regularPath := filepath.Join(pkgDir, pkgName+"-"+version+".ebuild")
			if err := os.WriteFile(regularPath, []byte(ebuildContent), 0644); err != nil {
				t.Logf("Failed to write regular ebuild: %v", err)
				return false
			}

			// Create live version
			livePath := filepath.Join(pkgDir, pkgName+"-9999.ebuild")
			if err := os.WriteFile(livePath, []byte(ebuildContent), 0644); err != nil {
				t.Logf("Failed to write live ebuild: %v", err)
				return false
			}

			// Extract metadata
			meta, err := ExtractEbuildMetadata(tmpDir, category+"/"+pkgName)
			if err != nil {
				t.Logf("ExtractEbuildMetadata failed: %v", err)
				return false
			}

			// Should prefer regular version over 9999
			return !meta.IsLive && meta.Version == version
		},
		genEbuildCategory(),
		genEbuildPkgName(),
		genEbuildVersion(),
	))

	properties.TestingRun(t)
}

// =============================================================================
// Unit Tests - Live Ebuild Detection
// =============================================================================

// TestLiveEbuildDetectionUnit tests live ebuild detection with specific cases
func TestLiveEbuildDetectionUnit(t *testing.T) {
	testCases := []struct {
		name     string
		version  string
		expected bool
	}{
		{"version 9999 is live", "9999", true},
		{"version 1.0.0 is not live", "1.0.0", false},
		{"version 2.0.0 is not live", "2.0.0", false},
		{"version 0.1.0 is not live", "0.1.0", false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			pkgDir := filepath.Join(tmpDir, "app-misc", "test")
			if err := os.MkdirAll(pkgDir, 0755); err != nil {
				t.Fatalf("Failed to create dir: %v", err)
			}

			ebuildContent := `EAPI=8
HOMEPAGE="https://example.com"
`
			ebuildPath := filepath.Join(pkgDir, "test-"+tc.version+".ebuild")
			if err := os.WriteFile(ebuildPath, []byte(ebuildContent), 0644); err != nil {
				t.Fatalf("Failed to write ebuild: %v", err)
			}

			meta, err := ExtractEbuildMetadata(tmpDir, "app-misc/test")
			if err != nil {
				t.Fatalf("ExtractEbuildMetadata failed: %v", err)
			}

			if meta.IsLive != tc.expected {
				t.Errorf("Expected IsLive=%v, got %v", tc.expected, meta.IsLive)
			}
		})
	}
}

// TestBinaryPackageDetection tests Property 7: Binary Package Detection
// **Feature: autoupdate-analyzer, Property 7: Binary Package Detection**
// **Validates: Requirements 3.5**
func TestBinaryPackageDetection(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: Ebuild with RESTRICT="bindist" is detected as binary package
	properties.Property("RESTRICT=bindist is detected as binary package", prop.ForAll(
		func(category, pkgName, version string) bool {
			// Create temporary overlay structure
			tmpDir := t.TempDir()
			pkgDir := filepath.Join(tmpDir, category, pkgName)
			if err := os.MkdirAll(pkgDir, 0755); err != nil {
				t.Logf("Failed to create dir: %v", err)
				return false
			}

			// Create ebuild file with RESTRICT="bindist"
			ebuildContent := `# Copyright
EAPI=8
HOMEPAGE="https://example.com"
SRC_URI=""
RESTRICT="bindist"
`
			ebuildPath := filepath.Join(pkgDir, pkgName+"-"+version+".ebuild")
			if err := os.WriteFile(ebuildPath, []byte(ebuildContent), 0644); err != nil {
				t.Logf("Failed to write ebuild: %v", err)
				return false
			}

			// Extract metadata
			meta, err := ExtractEbuildMetadata(tmpDir, category+"/"+pkgName)
			if err != nil {
				t.Logf("ExtractEbuildMetadata failed: %v", err)
				return false
			}

			return meta.IsBinary
		},
		genEbuildCategory(),
		genEbuildPkgName(),
		genEbuildVersion(),
	))

	// Property: Ebuild without RESTRICT="bindist" is not detected as binary package
	properties.Property("ebuild without bindist is not detected as binary package", prop.ForAll(
		func(category, pkgName, version string) bool {
			// Create temporary overlay structure
			tmpDir := t.TempDir()
			pkgDir := filepath.Join(tmpDir, category, pkgName)
			if err := os.MkdirAll(pkgDir, 0755); err != nil {
				t.Logf("Failed to create dir: %v", err)
				return false
			}

			// Create ebuild file without RESTRICT
			ebuildContent := `# Copyright
EAPI=8
HOMEPAGE="https://example.com"
SRC_URI="https://example.com/source.tar.gz"
`
			ebuildPath := filepath.Join(pkgDir, pkgName+"-"+version+".ebuild")
			if err := os.WriteFile(ebuildPath, []byte(ebuildContent), 0644); err != nil {
				t.Logf("Failed to write ebuild: %v", err)
				return false
			}

			// Extract metadata
			meta, err := ExtractEbuildMetadata(tmpDir, category+"/"+pkgName)
			if err != nil {
				t.Logf("ExtractEbuildMetadata failed: %v", err)
				return false
			}

			return !meta.IsBinary
		},
		genEbuildCategory(),
		genEbuildPkgName(),
		genEbuildVersion(),
	))

	properties.TestingRun(t)
}

// =============================================================================
// Unit Tests - Binary Package Detection
// =============================================================================

// TestBinaryPackageDetectionUnit tests binary package detection with specific cases
func TestBinaryPackageDetectionUnit(t *testing.T) {
	testCases := []struct {
		name     string
		content  string
		expected bool
	}{
		{
			name: "RESTRICT=bindist is binary",
			content: `EAPI=8
HOMEPAGE="https://example.com"
RESTRICT="bindist"
`,
			expected: true,
		},
		{
			name: "RESTRICT with multiple values including bindist is binary",
			content: `EAPI=8
HOMEPAGE="https://example.com"
RESTRICT="mirror bindist strip"
`,
			expected: true,
		},
		{
			name: "no RESTRICT is not binary",
			content: `EAPI=8
HOMEPAGE="https://example.com"
`,
			expected: false,
		},
		{
			name: "RESTRICT without bindist is not binary",
			content: `EAPI=8
HOMEPAGE="https://example.com"
RESTRICT="mirror strip"
`,
			expected: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			pkgDir := filepath.Join(tmpDir, "app-misc", "test")
			if err := os.MkdirAll(pkgDir, 0755); err != nil {
				t.Fatalf("Failed to create dir: %v", err)
			}

			ebuildPath := filepath.Join(pkgDir, "test-1.0.0.ebuild")
			if err := os.WriteFile(ebuildPath, []byte(tc.content), 0644); err != nil {
				t.Fatalf("Failed to write ebuild: %v", err)
			}

			meta, err := ExtractEbuildMetadata(tmpDir, "app-misc/test")
			if err != nil {
				t.Fatalf("ExtractEbuildMetadata failed: %v", err)
			}

			if meta.IsBinary != tc.expected {
				t.Errorf("Expected IsBinary=%v, got %v", tc.expected, meta.IsBinary)
			}
		})
	}
}

// =============================================================================
// Unit Tests - DetectPackageType
// =============================================================================

// TestDetectPackageType tests package type detection
func TestDetectPackageType(t *testing.T) {
	testCases := []struct {
		name     string
		meta     *EbuildMetadata
		expected PackageType
	}{
		{
			name: "GitHub homepage",
			meta: &EbuildMetadata{
				Homepage: "https://github.com/example/project",
			},
			expected: PackageTypeGitHub,
		},
		{
			name: "GitHub SRC_URI",
			meta: &EbuildMetadata{
				SrcURI: "https://github.com/example/project/archive/v1.0.0.tar.gz",
			},
			expected: PackageTypeGitHub,
		},
		{
			name: "PyPI homepage",
			meta: &EbuildMetadata{
				Homepage: "https://pypi.org/project/example",
			},
			expected: PackageTypePyPI,
		},
		{
			name: "npm homepage",
			meta: &EbuildMetadata{
				Homepage: "https://www.npmjs.com/package/example",
			},
			expected: PackageTypeNPM,
		},
		{
			name: "crates.io homepage",
			meta: &EbuildMetadata{
				Homepage: "https://crates.io/crates/example",
			},
			expected: PackageTypeCrates,
		},
		{
			name: "Python dependency",
			meta: &EbuildMetadata{
				Dependencies: []string{"dev-python/requests"},
			},
			expected: PackageTypePyPI,
		},
		{
			name: "Node.js dependency",
			meta: &EbuildMetadata{
				Dependencies: []string{"net-libs/nodejs"},
			},
			expected: PackageTypeNPM,
		},
		{
			name: "Rust dependency",
			meta: &EbuildMetadata{
				Dependencies: []string{"dev-lang/rust"},
			},
			expected: PackageTypeCrates,
		},
		{
			name: "Generic package",
			meta: &EbuildMetadata{
				Homepage: "https://example.com",
			},
			expected: PackageTypeGeneric,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := DetectPackageType(tc.meta)
			if result != tc.expected {
				t.Errorf("Expected %v, got %v", tc.expected, result)
			}
		})
	}
}

// TestExtractGitHubInfo tests GitHub info extraction
func TestExtractGitHubInfo(t *testing.T) {
	testCases := []struct {
		name          string
		meta          *EbuildMetadata
		expectedOwner string
		expectedRepo  string
		expectedFound bool
	}{
		{
			name: "GitHub homepage",
			meta: &EbuildMetadata{
				Homepage: "https://github.com/owner/repo",
			},
			expectedOwner: "owner",
			expectedRepo:  "repo",
			expectedFound: true,
		},
		{
			name: "GitHub SRC_URI",
			meta: &EbuildMetadata{
				SrcURI: "https://github.com/owner/repo/archive/v1.0.0.tar.gz",
			},
			expectedOwner: "owner",
			expectedRepo:  "repo",
			expectedFound: true,
		},
		{
			name: "GitHub with .git suffix",
			meta: &EbuildMetadata{
				Homepage: "https://github.com/owner/repo.git",
			},
			expectedOwner: "owner",
			expectedRepo:  "repo",
			expectedFound: true,
		},
		{
			name: "No GitHub URL",
			meta: &EbuildMetadata{
				Homepage: "https://example.com",
			},
			expectedOwner: "",
			expectedRepo:  "",
			expectedFound: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			owner, repo, found := ExtractGitHubInfo(tc.meta)
			if found != tc.expectedFound {
				t.Errorf("Expected found=%v, got %v", tc.expectedFound, found)
			}
			if owner != tc.expectedOwner {
				t.Errorf("Expected owner=%q, got %q", tc.expectedOwner, owner)
			}
			if repo != tc.expectedRepo {
				t.Errorf("Expected repo=%q, got %q", tc.expectedRepo, repo)
			}
		})
	}
}
