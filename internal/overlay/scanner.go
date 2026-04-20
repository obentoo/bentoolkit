package overlay

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/obentoo/bentoolkit/internal/common/ebuild"
)

// PackageInfo represents information about a package in an overlay
type PackageInfo struct {
	Category      string   // e.g., "app-editors"
	Package       string   // e.g., "vscode"
	Versions      []string // All versions found
	LatestVersion string   // Most recent version
}

// ScanResult contains the results of scanning an overlay
type ScanResult struct {
	OverlayPath string
	Packages    []PackageInfo
	Errors      []ScanError
}

// ScanError represents an error encountered during scanning
type ScanError struct {
	Path    string
	Message string
}

// isCategory checks if a directory name looks like a valid Gentoo category
func isCategory(name string) bool {
	// Categories have format: word-word (e.g., app-editors, sys-apps)
	// Skip hidden directories and special directories
	if strings.HasPrefix(name, ".") {
		return false
	}

	// Skip known non-category directories
	skipDirs := map[string]bool{
		"profiles":  true,
		"metadata":  true,
		"eclass":    true,
		"licenses":  true,
		"scripts":   true,
		"distfiles": true,
		"packages":  true,
	}

	if skipDirs[name] {
		return false
	}

	// Check if it contains a hyphen (common for categories)
	// But also allow categories without hyphen for flexibility
	return true
}

// ScanOverlay scans an overlay directory and returns all packages with their versions
func ScanOverlay(overlayPath string) (*ScanResult, error) {
	result := &ScanResult{
		OverlayPath: overlayPath,
		Packages:    []PackageInfo{},
		Errors:      []ScanError{},
	}

	// Read category directories
	entries, err := os.ReadDir(overlayPath)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		categoryName := entry.Name()
		if !isCategory(categoryName) {
			continue
		}

		categoryPath := filepath.Join(overlayPath, categoryName)
		packages, errors := scanCategory(categoryPath, categoryName)

		result.Packages = append(result.Packages, packages...)
		result.Errors = append(result.Errors, errors...)
	}

	// Sort packages by category/package for consistent output
	sort.Slice(result.Packages, func(i, j int) bool {
		if result.Packages[i].Category != result.Packages[j].Category {
			return result.Packages[i].Category < result.Packages[j].Category
		}
		return result.Packages[i].Package < result.Packages[j].Package
	})

	return result, nil
}

// scanCategory scans a category directory and returns all packages
func scanCategory(categoryPath, categoryName string) ([]PackageInfo, []ScanError) {
	var packages []PackageInfo
	var errors []ScanError

	entries, err := os.ReadDir(categoryPath)
	if err != nil {
		errors = append(errors, ScanError{
			Path:    categoryPath,
			Message: err.Error(),
		})
		return packages, errors
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		// Skip hidden directories
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		pkgName := entry.Name()
		pkgPath := filepath.Join(categoryPath, pkgName)

		pkg, scanErrs := scanPackage(pkgPath, categoryName, pkgName)
		if pkg != nil {
			packages = append(packages, *pkg)
		}
		errors = append(errors, scanErrs...)
	}

	return packages, errors
}

// scanPackage scans a package directory and extracts all ebuild versions
func scanPackage(pkgPath, category, pkgName string) (*PackageInfo, []ScanError) {
	var errors []ScanError

	entries, err := os.ReadDir(pkgPath)
	if err != nil {
		errors = append(errors, ScanError{
			Path:    pkgPath,
			Message: err.Error(),
		})
		return nil, errors
	}

	var versions []string

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		filename := entry.Name()
		if !strings.HasSuffix(filename, ".ebuild") {
			continue
		}

		// Construct a fake path for the parser
		fakePath := filepath.Join(category, pkgName, filename)
		eb, err := ebuild.ParsePath(fakePath)
		if err != nil {
			// Not a valid ebuild, skip silently
			continue
		}

		// Validate package name matches directory
		if eb.Package != pkgName {
			continue
		}

		versions = append(versions, eb.Version)
	}

	// No ebuilds found
	if len(versions) == 0 {
		return nil, errors
	}

	// Find the latest version
	latestVersion := FindLatestVersion(versions)

	return &PackageInfo{
		Category:      category,
		Package:       pkgName,
		Versions:      versions,
		LatestVersion: latestVersion,
	}, errors
}

// FindLatestVersion finds the latest version from a list using Gentoo version comparison
func FindLatestVersion(versions []string) string {
	return FindLatestVersionFiltered(versions, false)
}

// FindLatestVersionFiltered finds the latest version with optional live ebuild filtering
func FindLatestVersionFiltered(versions []string, ignoreLive bool) string {
	if len(versions) == 0 {
		return ""
	}

	// Filter out live versions if requested
	filtered := versions
	if ignoreLive {
		filtered = filterLiveVersions(versions)
		if len(filtered) == 0 {
			// All versions are live, return the original latest
			return FindLatestVersionFiltered(versions, false)
		}

		// Also filter out pre-release versions (alpha, beta, rc)
		filtered = filterPreReleaseVersions(filtered)
		if len(filtered) == 0 {
			// All non-live versions are pre-releases, use them anyway
			filtered = filterLiveVersions(versions)
		}
	}

	if len(filtered) == 1 {
		return filtered[0]
	}

	latest := filtered[0]
	for _, v := range filtered[1:] {
		if ebuild.CompareVersions(v, latest) > 0 {
			latest = v
		}
	}

	return latest
}

// isLiveVersion checks if a version is a live ebuild (9999, 99999999, etc.)
func isLiveVersion(version string) bool {
	// Live ebuilds use 9999 in various forms:
	// - 9999 (plain)
	// - 99999999 (nodejs)
	// - 3.0.9999 (vlc)
	// - 1.2.9999_pre (other variations)
	return strings.Contains(version, "9999")
}

// isPreReleaseVersion checks if a version is a pre-release (alpha, beta, rc)
func isPreReleaseVersion(version string) bool {
	// Pre-release versions use suffixes: _alpha, _beta, _rc
	// Note: _pre and _p are NOT filtered as they represent valid release versions
	// Examples: 1.0_alpha1, 2.0_beta2, 3.0_rc1
	return strings.Contains(version, "_alpha") ||
		strings.Contains(version, "_beta") ||
		strings.Contains(version, "_rc")
}

// filterLiveVersions removes live ebuild versions from a list
func filterLiveVersions(versions []string) []string {
	var filtered []string
	for _, v := range versions {
		if !isLiveVersion(v) {
			filtered = append(filtered, v)
		}
	}
	return filtered
}

// filterPreReleaseVersions removes pre-release versions from a list
func filterPreReleaseVersions(versions []string) []string {
	var filtered []string
	for _, v := range versions {
		if !isPreReleaseVersion(v) {
			filtered = append(filtered, v)
		}
	}
	return filtered
}

// FullName returns the category/package format
func (p *PackageInfo) FullName() string {
	return p.Category + "/" + p.Package
}

// String returns a human-readable representation
func (p *PackageInfo) String() string {
	return p.Category + "/" + p.Package + "-" + p.LatestVersion
}
