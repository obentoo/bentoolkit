// Package overlay provides business logic for overlay management operations.
package overlay

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/obentoo/bentoolkit/internal/common/config"
)

// Errors for rename operations
var (
	ErrOverlayPathNotSet = errors.New("overlay path is not configured")
)

// VersionFilesBlockError indicates that version-specific files were detected
// and the operation was blocked because --force was not specified.
type VersionFilesBlockError struct {
	Files []VersionFile
}

// Error implements the error interface.
func (e *VersionFilesBlockError) Error() string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("version-specific files detected (%d files); use --force to proceed\n\n", len(e.Files)))
	sb.WriteString("Files that may need manual attention:\n")
	for _, vf := range e.Files {
		sb.WriteString(fmt.Sprintf("  %s/%s/files/%s\n", vf.Category, vf.Package, vf.Filename))
	}
	return sb.String()
}

// ConflictError indicates that target files already exist.
type ConflictError struct {
	Conflicts []Conflict
}

// Error implements the error interface.
func (e *ConflictError) Error() string {
	return fmt.Sprintf("target files already exist (%d conflicts); use --force to overwrite", len(e.Conflicts))
}

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

// ShouldBlockForVersionFiles determines if the operation should be blocked
// due to version files being detected.
// Returns true if operation should abort, false if it should proceed.
func ShouldBlockForVersionFiles(versionFiles []VersionFile, force bool) bool {
	// If no version files detected, don't block
	if len(versionFiles) == 0 {
		return false
	}
	// If force flag is set, don't block
	if force {
		return false
	}
	// Version files detected and no force flag - block the operation
	return true
}

// RenamePreview finds matching ebuilds and detects potential issues without executing.
// Used to show a preview before confirmation.
func RenamePreview(cfg *config.Config, spec *RenameSpec) (*RenameResult, error) {
	result := &RenameResult{}

	// Get overlay path from config
	overlayPath := cfg.Overlay.Path
	if overlayPath == "" {
		return nil, ErrOverlayPathNotSet
	}

	// Validate pattern
	validator := NewPatternValidator()
	if err := validator.Validate(spec.PackagePattern); err != nil {
		return nil, err
	}

	// Find matching ebuilds
	matcher := NewEbuildMatcher(overlayPath)
	matches, err := matcher.Match(spec)
	if err != nil {
		return nil, err
	}
	result.Matches = matches

	// No matches found
	if len(matches) == 0 {
		return result, nil
	}

	// Detect version-specific files
	detector := NewVersionFilesDetector(overlayPath)
	versionFiles := detector.Detect(matches, spec.OldVersion)
	result.VersionFiles = versionFiles

	// Check for conflicts (target files that already exist)
	for _, match := range matches {
		if _, err := os.Stat(match.NewPath); err == nil {
			result.Conflicts = append(result.Conflicts, Conflict{
				Match:    match,
				Existing: match.NewPath,
			})
		}
	}

	return result, nil
}

// FormatRenamePreview formats the preview for display before confirmation.
func FormatRenamePreview(result *RenameResult, isGlobalSearch bool) string {
	var sb strings.Builder

	if isGlobalSearch {
		sb.WriteString("⚠ Global search across all categories\n\n")
	}

	sb.WriteString(fmt.Sprintf("Found %d ebuild(s) to rename:\n\n", len(result.Matches)))

	for _, match := range result.Matches {
		sb.WriteString(fmt.Sprintf("  %s/%s:\n", match.Category, match.Package))
		sb.WriteString(fmt.Sprintf("    %s → %s\n", match.OldFilename, match.NewFilename))
		if match.HasRevision {
			sb.WriteString("    (revision suffix will be stripped)\n")
		}
	}

	if len(result.VersionFiles) > 0 {
		sb.WriteString(fmt.Sprintf("\n⚠ Warning: %d version-specific file(s) detected:\n", len(result.VersionFiles)))
		for _, vf := range result.VersionFiles {
			sb.WriteString(fmt.Sprintf("  %s/%s/files/%s\n", vf.Category, vf.Package, vf.Filename))
		}
		sb.WriteString("\nThese files will NOT be renamed automatically.\n")
	}

	if len(result.Conflicts) > 0 {
		sb.WriteString(fmt.Sprintf("\n⚠ Warning: %d target file(s) already exist:\n", len(result.Conflicts)))
		for _, c := range result.Conflicts {
			sb.WriteString(fmt.Sprintf("  %s\n", c.Existing))
		}
		sb.WriteString("\nUse --force to overwrite.\n")
	}

	return sb.String()
}

// Rename performs bulk ebuild renaming.
// It validates the pattern, finds matching ebuilds, detects version files,
// and performs the rename operation (or simulates it in dry-run mode).
func Rename(cfg *config.Config, spec *RenameSpec, opts *RenameOptions) (*RenameResult, error) {
	result := &RenameResult{}

	// Get overlay path from config
	overlayPath := cfg.Overlay.Path
	if overlayPath == "" {
		return nil, ErrOverlayPathNotSet
	}

	// Validate pattern
	validator := NewPatternValidator()
	if err := validator.Validate(spec.PackagePattern); err != nil {
		return nil, err
	}

	// Find matching ebuilds
	matcher := NewEbuildMatcher(overlayPath)
	matches, err := matcher.Match(spec)
	if err != nil {
		return nil, err
	}
	result.Matches = matches

	// No matches found
	if len(matches) == 0 {
		return result, nil
	}

	// Detect version-specific files
	detector := NewVersionFilesDetector(overlayPath)
	versionFiles := detector.Detect(matches, spec.OldVersion)
	result.VersionFiles = versionFiles

	// Check if version files should block the operation
	if ShouldBlockForVersionFiles(versionFiles, opts.Force) {
		return result, &VersionFilesBlockError{Files: versionFiles}
	}

	// Check for conflicts (target files that already exist)
	for _, match := range matches {
		if _, err := os.Stat(match.NewPath); err == nil {
			result.Conflicts = append(result.Conflicts, Conflict{
				Match:    match,
				Existing: match.NewPath,
			})
		}
	}

	// If conflicts exist and not forcing, return early
	if len(result.Conflicts) > 0 && !opts.Force {
		return result, &ConflictError{Conflicts: result.Conflicts}
	}

	// Dry run - don't actually rename
	if opts.DryRun {
		return result, nil
	}

	// Perform the actual rename operations
	for _, match := range matches {
		err := os.Rename(match.OldPath, match.NewPath)
		if err != nil {
			result.Failed = append(result.Failed, RenameError{
				Match:   match,
				Message: err.Error(),
			})
		} else {
			result.Renamed = append(result.Renamed, match)
		}
	}

	// Update Manifests unless --no-manifest is set
	if !opts.NoManifest && len(result.Renamed) > 0 {
		result.ManifestUpdates = updateManifests(result.Renamed, overlayPath)
	}

	return result, nil
}

// updateManifests updates Manifest files for renamed packages using pkgdev.
// Returns a slice of ManifestUpdate with the results.
func updateManifests(renamed []RenameMatch, overlayPath string) []ManifestUpdate {
	var updates []ManifestUpdate

	// Track processed packages to avoid duplicate updates
	processed := make(map[string]bool)
	var uniqueMatches []RenameMatch

	for _, match := range renamed {
		key := match.Category + "/" + match.Package
		if processed[key] {
			continue
		}
		processed[key] = true
		uniqueMatches = append(uniqueMatches, match)

		updates = append(updates, ManifestUpdate{
			Category: match.Category,
			Package:  match.Package,
		})
	}

	if len(uniqueMatches) == 0 {
		return updates
	}

	// Check if pkgdev is available
	if _, err := exec.LookPath("pkgdev"); err != nil {
		for i := range updates {
			updates[i].Success = false
			updates[i].Error = "pkgdev not found; install dev-util/pkgdev"
		}
		return updates
	}

	// Create temporary distdir to avoid permission issues
	tmpDistdir, err := os.MkdirTemp("", "bentoo-distfiles-")
	if err != nil {
		for i := range updates {
			updates[i].Success = false
			updates[i].Error = fmt.Sprintf("failed to create temp distdir: %v", err)
		}
		return updates
	}
	defer os.RemoveAll(tmpDistdir)

	// Process each package
	for i, match := range uniqueMatches {
		pkgPath := fmt.Sprintf("%s/%s/%s", overlayPath, match.Category, match.Package)

		cmd := exec.Command("pkgdev", "manifest", "--distdir", tmpDistdir)
		cmd.Dir = pkgPath
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		fmt.Printf(">>> Updating Manifest for %s/%s (pkgdev)\n", match.Category, match.Package)

		err := cmd.Run()
		if err != nil {
			updates[i].Success = false
			updates[i].Error = err.Error()
		} else {
			updates[i].Success = true
		}
	}

	return updates
}

// FormatRenameResult formats the rename result for display.
func FormatRenameResult(result *RenameResult, dryRun bool) string {
	var sb strings.Builder

	if len(result.Matches) == 0 {
		return "No matching ebuilds found"
	}

	if dryRun {
		sb.WriteString(fmt.Sprintf("Dry run: %d ebuild(s) would be renamed\n\n", len(result.Matches)))
		for _, match := range result.Matches {
			sb.WriteString(fmt.Sprintf("  %s/%s:\n", match.Category, match.Package))
			sb.WriteString(fmt.Sprintf("    %s → %s\n", match.OldFilename, match.NewFilename))
			if match.HasRevision {
				sb.WriteString("    (revision suffix will be stripped)\n")
			}
		}
	} else {
		if len(result.Renamed) > 0 {
			sb.WriteString(fmt.Sprintf("Renamed %d ebuild(s):\n\n", len(result.Renamed)))
			for _, match := range result.Renamed {
				sb.WriteString(fmt.Sprintf("  %s/%s: %s → %s\n", match.Category, match.Package, match.OldFilename, match.NewFilename))
			}
		}

		if len(result.Failed) > 0 {
			sb.WriteString(fmt.Sprintf("\nFailed %d ebuild(s):\n", len(result.Failed)))
			for _, fail := range result.Failed {
				sb.WriteString(fmt.Sprintf("  %s/%s: %s\n", fail.Match.Category, fail.Match.Package, fail.Message))
			}
		}

		// Show Manifest update results
		if len(result.ManifestUpdates) > 0 {
			successCount := 0
			failCount := 0
			for _, u := range result.ManifestUpdates {
				if u.Success {
					successCount++
				} else {
					failCount++
				}
			}

			if successCount > 0 {
				sb.WriteString(fmt.Sprintf("\nManifest updated for %d package(s)\n", successCount))
			}

			if failCount > 0 {
				sb.WriteString(fmt.Sprintf("\nManifest update failed for %d package(s):\n", failCount))
				for _, u := range result.ManifestUpdates {
					if !u.Success {
						sb.WriteString(fmt.Sprintf("  %s/%s: %s\n", u.Category, u.Package, u.Error))
					}
				}
			}
		}
	}

	if len(result.VersionFiles) > 0 {
		sb.WriteString(fmt.Sprintf("\nWarning: %d version-specific file(s) detected:\n", len(result.VersionFiles)))
		for _, vf := range result.VersionFiles {
			sb.WriteString(fmt.Sprintf("  %s/%s/files/%s\n", vf.Category, vf.Package, vf.Filename))
		}
	}

	if len(result.Conflicts) > 0 {
		sb.WriteString(fmt.Sprintf("\nConflicts: %d target file(s) already exist:\n", len(result.Conflicts)))
		for _, c := range result.Conflicts {
			sb.WriteString(fmt.Sprintf("  %s\n", c.Existing))
		}
	}

	return sb.String()
}
