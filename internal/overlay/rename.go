// Package overlay provides business logic for overlay management operations.
package overlay

import (
	"github.com/obentoo/bentoolkit/internal/common/config"
)

// RenameSpec specifies what to rename.
type RenameSpec struct {
	Category       string // "*" for all categories, or specific category
	PackagePattern string // Glob pattern for package names
	OldVersion     string // Exact old version to match
	NewVersion     string // New version to rename to
}

// RenameOptions controls rename behavior.
type RenameOptions struct {
	DryRun     bool // Simulate without executing
	SkipPrompt bool // Skip confirmation prompts
	NoManifest bool // Skip Manifest updates
	Force      bool // Proceed despite warnings
}

// RenameMatch represents a single ebuild to be renamed.
type RenameMatch struct {
	Category    string // e.g., "media-plugins"
	Package     string // e.g., "gst-plugins-base"
	OldFilename string // e.g., "gst-plugins-base-1.24.11-r1.ebuild"
	NewFilename string // e.g., "gst-plugins-base-1.26.10.ebuild"
	OldPath     string // Full path to old file
	NewPath     string // Full path to new file
	HasRevision bool   // True if old filename had -rN suffix
}

// RenameResult contains the outcome of a rename operation.
type RenameResult struct {
	Matches         []RenameMatch    // All found matches
	Renamed         []RenameMatch    // Successfully renamed
	Failed          []RenameError    // Failed operations
	VersionFiles    []VersionFile    // Version-specific files detected
	Conflicts       []Conflict       // Target files that already exist
	ManifestUpdates []ManifestUpdate // Manifest update results
}

// RenameError represents a failed rename operation.
type RenameError struct {
	Match   RenameMatch
	Message string
}

// VersionFile represents a file with version in its name.
type VersionFile struct {
	Category string
	Package  string
	Path     string
	Filename string
}

// Conflict represents a target file that already exists.
type Conflict struct {
	Match    RenameMatch
	Existing string // Path to existing file
}

// ManifestUpdate represents a Manifest update operation.
type ManifestUpdate struct {
	Category string
	Package  string
	Success  bool
	Error    string
}

// Rename performs bulk ebuild renaming.
// This is a placeholder that will be implemented in later tasks.
func Rename(cfg *config.Config, spec *RenameSpec, opts *RenameOptions) (*RenameResult, error) {
	// TODO: Implement in Task 3 (Ebuild Matcher) and beyond
	return &RenameResult{}, nil
}

// FormatRenameResult formats the rename result for display.
// This is a placeholder that will be implemented in later tasks.
func FormatRenameResult(result *RenameResult, dryRun bool) string {
	// TODO: Implement in Task 9 (Result Formatter)
	if dryRun {
		return "Dry run: no changes made"
	}
	return "Rename operation completed"
}
