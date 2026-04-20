package ebuild

import (
	"errors"
	"regexp"
	"strings"
)

var (
	ErrInvalidEbuildPath = errors.New("invalid ebuild path format")
)

// ebuildPathRegex matches: category/package/package-version.ebuild
// Version can include: digits, dots, underscores, letters, hyphens (for -r1 revisions)
var ebuildPathRegex = regexp.MustCompile(`^([^/]+)/([^/]+)/([^/]+)-(\d+[\d.]*[\w._-]*)\.ebuild$`)

// Ebuild represents a parsed ebuild file path
type Ebuild struct {
	Category string // e.g., "app-misc"
	Package  string // e.g., "hello"
	Name     string // e.g., "hello" (same as Package for simple cases)
	Version  string // e.g., "1.0", "1.0_rc1", "1.0-r1"
}

// ParsePath parses an ebuild path and extracts category, package, name, and version
// Expected format: category/package/package-version.ebuild
func ParsePath(path string) (*Ebuild, error) {
	// Normalize path separators
	path = strings.ReplaceAll(path, "\\", "/")

	// Remove leading ./ if present
	path = strings.TrimPrefix(path, "./")

	matches := ebuildPathRegex.FindStringSubmatch(path)
	if matches == nil {
		return nil, ErrInvalidEbuildPath
	}

	category := matches[1]
	pkg := matches[2]
	name := matches[3]
	version := matches[4]

	// Validate that the filename prefix matches the package directory name
	// For packages like "firefox-bin", the name would be "firefox-bin"
	if name != pkg {
		return nil, ErrInvalidEbuildPath
	}

	return &Ebuild{
		Category: category,
		Package:  pkg,
		Name:     name,
		Version:  version,
	}, nil
}

// FullName returns the category/package format
func (e *Ebuild) FullName() string {
	return e.Category + "/" + e.Package
}

// String returns the full ebuild path format: category/package/package-version.ebuild
func (e *Ebuild) String() string {
	return e.Category + "/" + e.Package + "/" + e.Name + "-" + e.Version + ".ebuild"
}
