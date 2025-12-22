package overlay

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanOverlay(t *testing.T) {
	// Create temporary overlay structure
	tempDir, err := os.MkdirTemp("", "bentoo-test-overlay-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create overlay structure
	createTestOverlay(t, tempDir)

	// Scan overlay
	result, err := ScanOverlay(tempDir)
	if err != nil {
		t.Fatalf("ScanOverlay failed: %v", err)
	}

	// Verify results
	if len(result.Packages) != 3 {
		t.Errorf("Expected 3 packages, got %d", len(result.Packages))
		for _, p := range result.Packages {
			t.Logf("  Found: %s/%s", p.Category, p.Package)
		}
	}

	// Check specific packages
	foundHello := false
	foundVscode := false
	foundFirefox := false

	for _, pkg := range result.Packages {
		switch pkg.FullName() {
		case "app-misc/hello":
			foundHello = true
			if pkg.LatestVersion != "2.0" {
				t.Errorf("hello: expected latest 2.0, got %s", pkg.LatestVersion)
			}
			if len(pkg.Versions) != 2 {
				t.Errorf("hello: expected 2 versions, got %d", len(pkg.Versions))
			}
		case "app-editors/vscode":
			foundVscode = true
			if pkg.LatestVersion != "1.108.0" {
				t.Errorf("vscode: expected latest 1.108.0, got %s", pkg.LatestVersion)
			}
		case "www-client/firefox":
			foundFirefox = true
			if pkg.LatestVersion != "129.0" {
				t.Errorf("firefox: expected latest 129.0, got %s", pkg.LatestVersion)
			}
		}
	}

	if !foundHello {
		t.Error("Package app-misc/hello not found")
	}
	if !foundVscode {
		t.Error("Package app-editors/vscode not found")
	}
	if !foundFirefox {
		t.Error("Package www-client/firefox not found")
	}
}

func TestScanOverlaySkipsSpecialDirs(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "bentoo-test-overlay-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Create special directories that should be skipped
	specialDirs := []string{"profiles", "metadata", "eclass", "licenses", ".git"}
	for _, dir := range specialDirs {
		os.MkdirAll(filepath.Join(tempDir, dir), 0755)
	}

	// Create a valid package
	createPackage(t, tempDir, "app-misc", "hello", []string{"1.0"})

	result, err := ScanOverlay(tempDir)
	if err != nil {
		t.Fatalf("ScanOverlay failed: %v", err)
	}

	// Should only find the hello package
	if len(result.Packages) != 1 {
		t.Errorf("Expected 1 package, got %d", len(result.Packages))
	}
}

func TestScanOverlayEmptyOverlay(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "bentoo-test-overlay-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	result, err := ScanOverlay(tempDir)
	if err != nil {
		t.Fatalf("ScanOverlay failed: %v", err)
	}

	if len(result.Packages) != 0 {
		t.Errorf("Expected 0 packages, got %d", len(result.Packages))
	}
}

func TestScanOverlayNonexistent(t *testing.T) {
	_, err := ScanOverlay("/nonexistent/path")
	if err == nil {
		t.Error("Expected error for nonexistent path")
	}
}

func TestFindLatestVersion(t *testing.T) {
	tests := []struct {
		name     string
		versions []string
		expected string
	}{
		{
			name:     "simple versions",
			versions: []string{"1.0", "2.0", "1.5"},
			expected: "2.0",
		},
		{
			name:     "with suffixes",
			versions: []string{"1.0_rc1", "1.0", "1.0_beta1"},
			expected: "1.0",
		},
		{
			name:     "with revisions",
			versions: []string{"1.0", "1.0-r1", "1.0-r2"},
			expected: "1.0-r2",
		},
		{
			name:     "complex mixed",
			versions: []string{"1.0_alpha1", "1.0_beta1", "1.0_rc1", "1.0", "1.0_p1"},
			expected: "1.0_p1",
		},
		{
			name:     "single version",
			versions: []string{"1.0"},
			expected: "1.0",
		},
		{
			name:     "empty list",
			versions: []string{},
			expected: "",
		},
		{
			name:     "multi-part versions",
			versions: []string{"1.0.1", "1.0.10", "1.0.2"},
			expected: "1.0.10",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FindLatestVersion(tt.versions)
			if result != tt.expected {
				t.Errorf("Expected %s, got %s", tt.expected, result)
			}
		})
	}
}

func TestPackageInfoFullName(t *testing.T) {
	pkg := PackageInfo{
		Category: "app-misc",
		Package:  "hello",
	}

	if pkg.FullName() != "app-misc/hello" {
		t.Errorf("Expected app-misc/hello, got %s", pkg.FullName())
	}
}

func TestPackageInfoString(t *testing.T) {
	pkg := PackageInfo{
		Category:      "app-misc",
		Package:       "hello",
		LatestVersion: "1.0",
	}

	if pkg.String() != "app-misc/hello-1.0" {
		t.Errorf("Expected app-misc/hello-1.0, got %s", pkg.String())
	}
}

// Helper functions

func createTestOverlay(t *testing.T, basePath string) {
	t.Helper()

	// Create app-misc/hello with versions 1.0 and 2.0
	createPackage(t, basePath, "app-misc", "hello", []string{"1.0", "2.0"})

	// Create app-editors/vscode with single version
	createPackage(t, basePath, "app-editors", "vscode", []string{"1.108.0"})

	// Create www-client/firefox with versions
	createPackage(t, basePath, "www-client", "firefox", []string{"128.0", "129.0"})
}

func createPackage(t *testing.T, basePath, category, pkg string, versions []string) {
	t.Helper()

	pkgPath := filepath.Join(basePath, category, pkg)
	if err := os.MkdirAll(pkgPath, 0755); err != nil {
		t.Fatalf("Failed to create package dir: %v", err)
	}

	for _, version := range versions {
		ebuildPath := filepath.Join(pkgPath, pkg+"-"+version+".ebuild")
		content := []byte("# Fake ebuild for testing\nEAPI=8\n")
		if err := os.WriteFile(ebuildPath, content, 0644); err != nil {
			t.Fatalf("Failed to create ebuild: %v", err)
		}
	}

	// Create Manifest file
	manifestPath := filepath.Join(pkgPath, "Manifest")
	if err := os.WriteFile(manifestPath, []byte(""), 0644); err != nil {
		t.Fatalf("Failed to create Manifest: %v", err)
	}
}

