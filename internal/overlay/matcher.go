// Package overlay provides business logic for overlay management operations.
package overlay

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// revisionRegex matches revision suffixes like -r1, -r2, etc.
var revisionRegex = regexp.MustCompile(`-r(\d+)$`)

// EbuildMatcher finds ebuilds matching a rename specification.
type EbuildMatcher struct {
	overlayPath string
}

// NewEbuildMatcher creates a new EbuildMatcher for the given overlay path.
func NewEbuildMatcher(overlayPath string) *EbuildMatcher {
	return &EbuildMatcher{
		overlayPath: overlayPath,
	}
}

// Match finds all ebuilds matching the specification.
// It searches within the specified category (or all categories if "*")
// and matches both package name pattern and exact old version.
func (m *EbuildMatcher) Match(spec *RenameSpec) ([]RenameMatch, error) {
	var matches []RenameMatch

	if spec.Category == "*" {
		// Global search: scan all categories
		entries, err := os.ReadDir(m.overlayPath)
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

			categoryPath := filepath.Join(m.overlayPath, categoryName)
			categoryMatches, err := m.matchCategory(categoryPath, categoryName, spec)
			if err != nil {
				// Continue scanning other categories on error
				continue
			}
			matches = append(matches, categoryMatches...)
		}
	} else {
		// Specific category search
		categoryPath := filepath.Join(m.overlayPath, spec.Category)
		if _, err := os.Stat(categoryPath); os.IsNotExist(err) {
			return nil, &CategoryNotFoundError{Category: spec.Category}
		}

		categoryMatches, err := m.matchCategory(categoryPath, spec.Category, spec)
		if err != nil {
			return nil, err
		}
		matches = categoryMatches
	}

	return matches, nil
}

// CategoryNotFoundError indicates that the specified category does not exist.
type CategoryNotFoundError struct {
	Category string
}

// Error returns the error message.
func (e *CategoryNotFoundError) Error() string {
	return "category not found: " + e.Category
}

// matchCategory searches within a single category for matching ebuilds.
func (m *EbuildMatcher) matchCategory(categoryPath, categoryName string, spec *RenameSpec) ([]RenameMatch, error) {
	var matches []RenameMatch

	entries, err := os.ReadDir(categoryPath)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		// Skip hidden directories
		pkgName := entry.Name()
		if strings.HasPrefix(pkgName, ".") {
			continue
		}

		// Check if package name matches the pattern
		if !m.matchPackage(pkgName, spec.PackagePattern) {
			continue
		}

		// Scan package directory for matching ebuilds
		pkgPath := filepath.Join(categoryPath, pkgName)
		pkgMatches, err := m.matchPackageEbuilds(pkgPath, categoryName, pkgName, spec)
		if err != nil {
			// Continue scanning other packages on error
			continue
		}
		matches = append(matches, pkgMatches...)
	}

	return matches, nil
}

// matchPackage checks if a package name matches the glob pattern.
// Uses filepath.Match for glob pattern matching.
func (m *EbuildMatcher) matchPackage(pkgName, pattern string) bool {
	// If pattern has no wildcards, do exact match
	if !strings.ContainsAny(pattern, "*?[") {
		return pkgName == pattern
	}

	// Use filepath.Match for glob pattern matching
	matched, err := filepath.Match(pattern, pkgName)
	if err != nil {
		return false
	}
	return matched
}

// matchPackageEbuilds scans a package directory for ebuilds matching the version.
func (m *EbuildMatcher) matchPackageEbuilds(pkgPath, category, pkgName string, spec *RenameSpec) ([]RenameMatch, error) {
	var matches []RenameMatch

	entries, err := os.ReadDir(pkgPath)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		filename := entry.Name()
		if !strings.HasSuffix(filename, ".ebuild") {
			continue
		}

		// Check if ebuild matches the old version
		matched, hasRevision := m.matchEbuild(filename, pkgName, spec.OldVersion)
		if !matched {
			continue
		}

		// Build the rename match
		match := m.buildRenameMatch(category, pkgName, filename, spec.OldVersion, spec.NewVersion, hasRevision)
		match.OldPath = filepath.Join(pkgPath, filename)
		match.NewPath = filepath.Join(pkgPath, match.NewFilename)
		matches = append(matches, match)
	}

	return matches, nil
}

// matchEbuild checks if an ebuild filename matches the old version.
// Returns (matched, hasRevision) where:
// - matched: true if the base version (without revision) equals oldVersion
// - hasRevision: true if the filename has a revision suffix (-rN)
func (m *EbuildMatcher) matchEbuild(filename, pkgName, oldVersion string) (bool, bool) {
	// Parse the ebuild filename to extract version
	// Expected format: pkgName-version.ebuild
	prefix := pkgName + "-"
	suffix := ".ebuild"

	if !strings.HasPrefix(filename, prefix) || !strings.HasSuffix(filename, suffix) {
		return false, false
	}

	// Extract version from filename
	version := strings.TrimPrefix(filename, prefix)
	version = strings.TrimSuffix(version, suffix)

	// Check for revision suffix
	hasRevision := revisionRegex.MatchString(version)

	// Get base version (without revision)
	baseVersion := version
	if hasRevision {
		baseVersion = revisionRegex.ReplaceAllString(version, "")
	}

	// Match if base version equals old version
	return baseVersion == oldVersion, hasRevision
}

// buildRenameMatch creates a RenameMatch from matched ebuild information.
// The new filename will NOT contain any revision suffix.
func (m *EbuildMatcher) buildRenameMatch(category, pkgName, oldFilename, oldVersion, newVersion string, hasRevision bool) RenameMatch {
	// Build new filename: pkgName-newVersion.ebuild (no revision)
	newFilename := pkgName + "-" + newVersion + ".ebuild"

	return RenameMatch{
		Category:    category,
		Package:     pkgName,
		OldFilename: oldFilename,
		NewFilename: newFilename,
		HasRevision: hasRevision,
	}
}
