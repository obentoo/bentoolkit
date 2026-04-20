// Package autoupdate provides ebuild metadata extraction for autoupdate analysis.
package autoupdate

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/obentoo/bentoolkit/internal/common/ebuild"
)

// Error variables for ebuild metadata extraction
var (
	// ErrEbuildParseFailed is returned when the ebuild file cannot be parsed
	ErrEbuildParseFailed = errors.New("failed to parse ebuild")
)

// PackageType represents the detected package type based on metadata
type PackageType string

const (
	// PackageTypeGitHub indicates a package hosted on GitHub
	PackageTypeGitHub PackageType = "github"
	// PackageTypePyPI indicates a Python package from PyPI
	PackageTypePyPI PackageType = "pypi"
	// PackageTypeNPM indicates a Node.js package from npm
	PackageTypeNPM PackageType = "npm"
	// PackageTypeCrates indicates a Rust crate from crates.io
	PackageTypeCrates PackageType = "crates"
	// PackageTypeGeneric indicates a package with no specific ecosystem detected
	PackageTypeGeneric PackageType = "generic"
)

// EbuildMetadata contains extracted ebuild information
type EbuildMetadata struct {
	// Package is the full package name in category/package format
	Package string
	// Version is the current ebuild version
	Version string
	// Homepage is the HOMEPAGE variable from the ebuild
	Homepage string
	// SrcURI is the SRC_URI variable from the ebuild
	SrcURI string
	// Dependencies contains DEPEND and RDEPEND entries
	Dependencies []string
	// IsLive indicates if this is a live/git ebuild (version 9999)
	IsLive bool
	// IsBinary indicates if this is a binary package (RESTRICT="bindist" or similar)
	IsBinary bool
}

// Regular expressions for parsing ebuild variables
var (
	// homepageRegex matches HOMEPAGE="..." or HOMEPAGE='...'
	homepageRegex = regexp.MustCompile(`(?m)^HOMEPAGE=["']([^"']+)["']`)
	// restrictRegex matches RESTRICT="..." or RESTRICT='...'
	restrictRegex = regexp.MustCompile(`(?m)^RESTRICT=["']([^"']+)["']`)
	// githubRegex matches GitHub URLs in various formats
	githubRegex = regexp.MustCompile(`github\.com[/:]([^/]+)/([^/\s"']+)`)
	// pypiRegex matches PyPI URLs
	pypiRegex = regexp.MustCompile(`pypi\.(?:org|io|python\.org)`)
	// npmRegex matches npm registry URLs
	npmRegex = regexp.MustCompile(`(?:npmjs\.(?:org|com)|registry\.npmjs\.org)`)
	// cratesRegex matches crates.io URLs
	cratesRegex = regexp.MustCompile(`crates\.io`)
	// pythonDepRegex matches Python-related dependencies
	pythonDepRegex = regexp.MustCompile(`dev-python/|python-`)
	// nodeDepRegex matches Node.js-related dependencies
	nodeDepRegex = regexp.MustCompile(`net-libs/nodejs|dev-nodejs/`)
	// rustDepRegex matches Rust-related dependencies
	rustDepRegex = regexp.MustCompile(`dev-lang/rust|virtual/rust|dev-rust/`)
)

// ExtractEbuildMetadata extracts metadata from an ebuild file.
// It finds the highest version ebuild in the package directory and extracts
// HOMEPAGE, SRC_URI, DEPEND, RDEPEND, and detects live/binary packages.
func ExtractEbuildMetadata(overlayPath, pkg string) (*EbuildMetadata, error) {
	// Validate package format (category/package)
	parts := strings.Split(pkg, "/")
	if len(parts) != 2 {
		return nil, fmt.Errorf("%w: invalid package format %q, expected category/package", ErrPackageNotFound, pkg)
	}

	category := parts[0]
	pkgName := parts[1]

	// Build package directory path
	pkgDir := filepath.Join(overlayPath, category, pkgName)

	// Check if package directory exists
	if _, err := os.Stat(pkgDir); os.IsNotExist(err) {
		return nil, fmt.Errorf("%w: %s", ErrPackageNotFound, pkg)
	}

	// Find all ebuild files in the package directory
	ebuilds, err := findEbuilds(pkgDir)
	if err != nil {
		return nil, err
	}

	if len(ebuilds) == 0 {
		return nil, fmt.Errorf("%w: no ebuilds in %s", ErrEbuildNotFound, pkg)
	}

	// Find the highest version ebuild (excluding 9999 unless it's the only one)
	ebuildPath, version := selectBestEbuild(ebuilds)

	// Read and parse the ebuild file
	content, err := os.ReadFile(ebuildPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrEbuildParseFailed, err)
	}

	// Extract metadata
	meta := &EbuildMetadata{
		Package: pkg,
		Version: version,
		IsLive:  version == "9999" || strings.HasPrefix(version, "9999"),
	}

	// Extract HOMEPAGE
	if matches := homepageRegex.FindSubmatch(content); matches != nil {
		meta.Homepage = string(matches[1])
	}

	// Extract SRC_URI (handle multi-line)
	meta.SrcURI = extractMultiLineVar(content, "SRC_URI")

	// Extract dependencies
	meta.Dependencies = extractDependencies(content)

	// Detect binary package
	meta.IsBinary = detectBinaryPackage(content)

	return meta, nil
}

// findEbuilds finds all ebuild files in a package directory
func findEbuilds(pkgDir string) ([]string, error) {
	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		return nil, fmt.Errorf("%w: cannot read directory: %v", ErrEbuildParseFailed, err)
	}

	var ebuilds []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".ebuild") {
			ebuilds = append(ebuilds, filepath.Join(pkgDir, entry.Name()))
		}
	}

	return ebuilds, nil
}

// selectBestEbuild selects the best ebuild to analyze.
// It prefers the highest non-live version, but falls back to 9999 if it's the only one.
func selectBestEbuild(ebuildPaths []string) (string, string) {
	type ebuildInfo struct {
		path    string
		version string
	}

	var ebuilds []ebuildInfo
	for _, path := range ebuildPaths {
		// Extract version from filename: package-version.ebuild
		filename := filepath.Base(path)
		// Remove .ebuild suffix
		name := strings.TrimSuffix(filename, ".ebuild")
		// Find the last dash followed by a digit (version separator)
		version := extractVersionFromFilename(name)
		if version != "" {
			ebuilds = append(ebuilds, ebuildInfo{path: path, version: version})
		}
	}

	if len(ebuilds) == 0 {
		// Fallback: return first path with unknown version
		return ebuildPaths[0], ""
	}

	// Sort by version (highest first), but put 9999 at the end
	sort.Slice(ebuilds, func(i, j int) bool {
		vi, vj := ebuilds[i].version, ebuilds[j].version
		// 9999 versions go to the end
		isLiveI := vi == "9999" || strings.HasPrefix(vi, "9999")
		isLiveJ := vj == "9999" || strings.HasPrefix(vj, "9999")
		if isLiveI && !isLiveJ {
			return false
		}
		if !isLiveI && isLiveJ {
			return true
		}
		// Compare versions (higher first)
		return ebuild.CompareVersions(vi, vj) > 0
	})

	return ebuilds[0].path, ebuilds[0].version
}

// extractVersionFromFilename extracts version from an ebuild filename.
// Format: package-version (without .ebuild suffix)
// Examples: "hello-1.0.0" -> "1.0.0", "firefox-bin-120.0" -> "120.0"
func extractVersionFromFilename(name string) string {
	// Find the last dash followed by a digit
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '-' && i+1 < len(name) && name[i+1] >= '0' && name[i+1] <= '9' {
			return name[i+1:]
		}
	}
	return ""
}

// extractMultiLineVar extracts a variable that may span multiple lines.
// Handles both quoted and heredoc-style variable assignments.
func extractMultiLineVar(content []byte, varName string) string {
	scanner := bufio.NewScanner(strings.NewReader(string(content)))
	var result strings.Builder
	inVar := false
	quoteChar := byte(0)

	for scanner.Scan() {
		line := scanner.Text()

		if !inVar {
			// Look for variable start
			prefix := varName + "="
			idx := strings.Index(line, prefix)
			if idx == -1 {
				continue
			}

			// Get the part after the =
			rest := line[idx+len(prefix):]
			if len(rest) == 0 {
				continue
			}

			// Check for quote character
			if rest[0] == '"' || rest[0] == '\'' {
				quoteChar = rest[0]
				rest = rest[1:]

				// Check if it ends on the same line
				endIdx := strings.IndexByte(rest, quoteChar)
				if endIdx != -1 {
					result.WriteString(rest[:endIdx])
					return strings.TrimSpace(result.String())
				}

				result.WriteString(rest)
				result.WriteString(" ")
				inVar = true
			} else {
				// Unquoted single-line value
				return strings.TrimSpace(rest)
			}
		} else {
			// Continue reading multi-line value
			endIdx := strings.IndexByte(line, quoteChar)
			if endIdx != -1 {
				result.WriteString(line[:endIdx])
				return strings.TrimSpace(result.String())
			}
			result.WriteString(strings.TrimSpace(line))
			result.WriteString(" ")
		}
	}

	return strings.TrimSpace(result.String())
}

// extractDependencies extracts DEPEND and RDEPEND entries from ebuild content
func extractDependencies(content []byte) []string {
	var deps []string
	seen := make(map[string]bool)

	// Extract DEPEND
	dependStr := extractMultiLineVar(content, "DEPEND")
	for _, dep := range parseDependencyString(dependStr) {
		if !seen[dep] {
			deps = append(deps, dep)
			seen[dep] = true
		}
	}

	// Extract RDEPEND
	rdependStr := extractMultiLineVar(content, "RDEPEND")
	for _, dep := range parseDependencyString(rdependStr) {
		if !seen[dep] {
			deps = append(deps, dep)
			seen[dep] = true
		}
	}

	return deps
}

// parseDependencyString parses a dependency string into individual dependencies
func parseDependencyString(depStr string) []string {
	if depStr == "" {
		return nil
	}

	var deps []string
	// Split by whitespace and filter
	for _, part := range strings.Fields(depStr) {
		// Skip USE flag conditionals and operators
		if strings.HasSuffix(part, "?") || part == "||" || part == "(" || part == ")" {
			continue
		}
		// Skip empty parts
		if part == "" {
			continue
		}
		// Extract package atom (remove version constraints)
		atom := extractPackageAtom(part)
		if atom != "" {
			deps = append(deps, atom)
		}
	}

	return deps
}

// extractPackageAtom extracts the category/package from a dependency atom
// Handles: >=cat/pkg-1.0, cat/pkg:slot, cat/pkg[use], etc.
func extractPackageAtom(atom string) string {
	// Remove leading operators (>=, <=, =, ~, !, etc.)
	atom = strings.TrimLeft(atom, ">=<~!")

	// Find the category/package part
	slashIdx := strings.Index(atom, "/")
	if slashIdx == -1 {
		return ""
	}

	// Find where the package name ends (at version, slot, or use flag)
	endIdx := len(atom)
	for i := slashIdx + 1; i < len(atom); i++ {
		c := atom[i]
		// Version starts with -[0-9]
		if c == '-' && i+1 < len(atom) && atom[i+1] >= '0' && atom[i+1] <= '9' {
			endIdx = i
			break
		}
		// Slot or subslot
		if c == ':' {
			endIdx = i
			break
		}
		// USE flags
		if c == '[' {
			endIdx = i
			break
		}
	}

	return atom[:endIdx]
}

// detectBinaryPackage checks if the ebuild is for a binary package
func detectBinaryPackage(content []byte) bool {
	// Check RESTRICT for bindist
	if matches := restrictRegex.FindSubmatch(content); matches != nil {
		restrict := string(matches[1])
		if strings.Contains(restrict, "bindist") {
			return true
		}
	}

	// Also check for common binary package indicators in the content
	contentStr := string(content)

	// Check for binary-related eclasses
	if strings.Contains(contentStr, "inherit") {
		if strings.Contains(contentStr, "unpacker") || //nolint:staticcheck // empty branch intentional
			strings.Contains(contentStr, "desktop") && strings.Contains(contentStr, "xdg") {
			// These are common in binary packages but not definitive
			// Check for other indicators
		}
	}

	// Check for common binary package patterns in SRC_URI
	srcURI := extractMultiLineVar(content, "SRC_URI")
	binaryPatterns := []string{
		".deb", ".rpm", ".AppImage", ".tar.gz", ".tar.xz", ".zip",
		"linux64", "linux-x64", "linux-amd64",
	}
	for _, pattern := range binaryPatterns {
		if strings.Contains(srcURI, pattern) {
			// Check if it's explicitly a binary package (has -bin suffix in name)
			if strings.Contains(contentStr, "PN}-bin") || strings.Contains(contentStr, "-bin-") {
				return true
			}
		}
	}

	return false
}

// DetectPackageType determines the package type from metadata.
// It analyzes HOMEPAGE, SRC_URI, and dependencies to identify the ecosystem.
func DetectPackageType(meta *EbuildMetadata) PackageType {
	// Check GitHub first (most common)
	if githubRegex.MatchString(meta.Homepage) || githubRegex.MatchString(meta.SrcURI) {
		return PackageTypeGitHub
	}

	// Check PyPI
	if pypiRegex.MatchString(meta.Homepage) || pypiRegex.MatchString(meta.SrcURI) {
		return PackageTypePyPI
	}

	// Check npm
	if npmRegex.MatchString(meta.Homepage) || npmRegex.MatchString(meta.SrcURI) {
		return PackageTypeNPM
	}

	// Check crates.io
	if cratesRegex.MatchString(meta.Homepage) || cratesRegex.MatchString(meta.SrcURI) {
		return PackageTypeCrates
	}

	// Check dependencies for ecosystem hints
	for _, dep := range meta.Dependencies {
		if pythonDepRegex.MatchString(dep) {
			return PackageTypePyPI
		}
		if nodeDepRegex.MatchString(dep) {
			return PackageTypeNPM
		}
		if rustDepRegex.MatchString(dep) {
			return PackageTypeCrates
		}
	}

	return PackageTypeGeneric
}

// ExtractGitHubInfo extracts owner and repo from GitHub URLs in metadata
func ExtractGitHubInfo(meta *EbuildMetadata) (owner, repo string, found bool) {
	// Try HOMEPAGE first
	if matches := githubRegex.FindStringSubmatch(meta.Homepage); matches != nil {
		return matches[1], cleanRepoName(matches[2]), true
	}

	// Try SRC_URI
	if matches := githubRegex.FindStringSubmatch(meta.SrcURI); matches != nil {
		return matches[1], cleanRepoName(matches[2]), true
	}

	return "", "", false
}

// cleanRepoName removes common suffixes from repository names
func cleanRepoName(repo string) string {
	// Remove .git suffix
	repo = strings.TrimSuffix(repo, ".git")
	// Remove trailing slashes
	repo = strings.TrimSuffix(repo, "/")
	return repo
}
