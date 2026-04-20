// Package overlay provides business logic for overlay management operations.
package overlay

import (
	"os"
	"path/filepath"
	"strings"
)

// VersionFilesDetector finds files with version numbers in their names.
// It scans the files/ subdirectory of packages for version-specific files
// that may need manual attention during rename operations.
type VersionFilesDetector struct {
	overlayPath string
}

// NewVersionFilesDetector creates a new VersionFilesDetector for the given overlay path.
func NewVersionFilesDetector(overlayPath string) *VersionFilesDetector {
	return &VersionFilesDetector{
		overlayPath: overlayPath,
	}
}

// Detect scans for version-specific files in package directories.
// It checks the files/ subdirectory of each unique category/package
// in the matches and returns all files containing the old version string.
func (d *VersionFilesDetector) Detect(matches []RenameMatch, oldVersion string) []VersionFile {
	var versionFiles []VersionFile

	// Track processed packages to avoid duplicate scans
	processed := make(map[string]bool)

	for _, match := range matches {
		key := match.Category + "/" + match.Package
		if processed[key] {
			continue
		}
		processed[key] = true

		// Construct path to files/ subdirectory
		filesDir := filepath.Join(d.overlayPath, match.Category, match.Package, "files")

		// Scan the files directory for version-specific files
		found := d.scanFilesDir(filesDir, match.Category, match.Package, oldVersion)
		versionFiles = append(versionFiles, found...)
	}

	return versionFiles
}

// scanFilesDir scans the files/ subdirectory for version-specific files.
// It returns a slice of VersionFile for each file containing the version string.
func (d *VersionFilesDetector) scanFilesDir(filesDir, category, pkg, oldVersion string) []VersionFile {
	var result []VersionFile

	// Check if files/ directory exists
	info, err := os.Stat(filesDir)
	if err != nil || !info.IsDir() {
		return result
	}

	// Read all entries in the files/ directory
	entries, err := os.ReadDir(filesDir)
	if err != nil {
		return result
	}

	for _, entry := range entries {
		// Skip directories
		if entry.IsDir() {
			continue
		}

		filename := entry.Name()

		// Check if filename contains the old version
		if containsVersion(filename, oldVersion) {
			result = append(result, VersionFile{
				Category: category,
				Package:  pkg,
				Path:     filepath.Join(filesDir, filename),
				Filename: filename,
			})
		}
	}

	return result
}

// containsVersion checks if a filename contains the version string.
// It performs a simple substring match to detect version-specific files.
func containsVersion(filename, version string) bool {
	return strings.Contains(filename, version)
}
