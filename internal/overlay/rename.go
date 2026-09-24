// Package overlay provides business logic for overlay management operations.
package overlay

import (
	"errors"
	"fmt"
	"os"
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
	fmt.Fprintf(&sb, "version-specific files detected (%d files); use --force to proceed\n\n", len(e.Files))
	sb.WriteString("Files that may need manual attention:\n")
	for _, vf := range e.Files {
		fmt.Fprintf(&sb, "  %s/%s/files/%s\n", vf.Category, vf.Package, vf.Filename)
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

// CollisionError indicates that two or more matched ebuilds map to the same
// target filename. Renaming them would move each onto the next, so the rename
// is refused before any file moves, and --force does not override it: unlike a
// Conflict, there is no pre-existing file the operator could choose to replace
// — one of the sources itself would be destroyed.
type CollisionError struct {
	Collisions []Collision
}

// Error implements the error interface. It names, per collision, the
// category/package, the target filename and every source filename.
func (e *CollisionError) Error() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%d target file(s) would receive more than one ebuild; rename refused (--force does not override this):", len(e.Collisions))
	for _, c := range e.Collisions {
		sb.WriteString("\n  ")
		sb.WriteString(c.describe())
	}
	return sb.String()
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
	Collisions      []Collision      // Targets shared by two or more matches
	ManifestUpdates []ManifestUpdate // Manifest update results
	Warnings        []string         // Non-fatal scan warnings
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

// Collision represents one target path that two or more matches map to — for
// example foo-1.0.ebuild and foo-1.0-r1.ebuild both becoming foo-1.1.ebuild,
// because the matcher strips the revision.
type Collision struct {
	Target  string        // Full path both sources would be renamed to
	Sources []RenameMatch // Every match mapping to Target, in match order
}

// describe renders the collision as "category/package: target ← src, src".
func (c Collision) describe() string {
	names := make([]string, len(c.Sources))
	for i, m := range c.Sources {
		names[i] = m.OldFilename
	}
	first := c.Sources[0]
	return fmt.Sprintf("%s/%s: %s ← %s", first.Category, first.Package, first.NewFilename, strings.Join(names, ", "))
}

// findCollisions groups matches by NewPath and returns every target that more
// than one match maps to. It iterates the slice, not the map, so the order is
// the match order and the output is deterministic.
func findCollisions(matches []RenameMatch) []Collision {
	byTarget := make(map[string][]RenameMatch, len(matches))
	var order []string
	for _, m := range matches {
		if _, seen := byTarget[m.NewPath]; !seen {
			order = append(order, m.NewPath)
		}
		byTarget[m.NewPath] = append(byTarget[m.NewPath], m)
	}
	var collisions []Collision
	for _, target := range order {
		if sources := byTarget[target]; len(sources) > 1 {
			collisions = append(collisions, Collision{Target: target, Sources: sources})
		}
	}
	return collisions
}

// ManifestUpdate represents a Manifest update operation.
type ManifestUpdate struct {
	Category string
	Package  string
	Success  bool
	// Error is the failure as the SENTENCE the formatters print, and it
	// deliberately carries no atom: FormatManifestResult and FormatRenameResult
	// both write "<category>/<package>: " immediately before it, so a copy of
	// the atom in here would be printed twice on every failure line.
	Error string
	// Err is that same failure as a VALUE — the cause wrapped with %w and with
	// the category/package that produced it (S046-R5.1).
	//
	// It exists beside Error rather than replacing it because a string is a
	// dead end: it cannot be unwrapped, it cannot be matched with errors.Is,
	// and a caller that needs to know WHICH failure it holds is reduced to
	// searching text for a phrase. It carries the atom although Error does not,
	// because an error travels on its own — into a log line, into another wrap,
	// into a report row assembled long after the loop that produced it — and an
	// error that has left the row naming its package cannot be reproduced.
	//
	// Nil on success, and nil only on success: "did this target fail?" is
	// answered by Success and Err agreeing, never by whether a field happens to
	// have been populated.
	Err error
	// Output is what the failing command printed — pkgdev's own diagnostic,
	// verbatim, including the "[bentoo] reused N distfile(s)" line the run
	// injects into the same stream. It is the text the operator acts on, and by
	// the time a report is rendered the terminal that streamed it live is gone
	// (S046-R5.2).
	//
	// It is populated ONLY on failure. The capture is a verbatim, unbounded copy
	// of every byte the child wrote — a package fetching a large distfile can
	// emit megabytes of progress output — and a whole-overlay run would hold one
	// per target for as long as the caller holds the slice. A successful
	// target's output has no reader to justify that: it was already streamed
	// through the Reporter while the target ran.
	Output string
	// Reused is the number of distfiles symlinked from the distfiles cache
	// (e.g. /var/cache/distfiles) into the working distdir, sparing pkgdev
	// from re-downloading them. Zero when no cache is configured or no
	// matching files were found.
	Reused int
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
	matchResult, err := matcher.Match(spec)
	if err != nil {
		return nil, err
	}
	result.Matches = matchResult.Matches
	result.Warnings = matchResult.Warnings

	// No matches found
	if len(result.Matches) == 0 {
		return result, nil
	}

	// Detect version-specific files
	detector := NewVersionFilesDetector(overlayPath)
	versionFiles := detector.Detect(result.Matches, spec.OldVersion)
	result.VersionFiles = versionFiles
	result.Collisions = findCollisions(result.Matches)

	// Check for conflicts (target files that already exist)
	for _, match := range result.Matches {
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

	fmt.Fprintf(&sb, "Found %d ebuild(s) to rename:\n\n", len(result.Matches))

	for _, match := range result.Matches {
		fmt.Fprintf(&sb, "  %s/%s:\n", match.Category, match.Package)
		fmt.Fprintf(&sb, "    %s → %s\n", match.OldFilename, match.NewFilename)
		if match.HasRevision {
			sb.WriteString("    (revision suffix will be stripped)\n")
		}
	}

	if len(result.VersionFiles) > 0 {
		fmt.Fprintf(&sb, "\n⚠ Warning: %d version-specific file(s) detected:\n", len(result.VersionFiles))
		for _, vf := range result.VersionFiles {
			fmt.Fprintf(&sb, "  %s/%s/files/%s\n", vf.Category, vf.Package, vf.Filename)
		}
		sb.WriteString("\nThese files will NOT be renamed automatically.\n")
	}

	if len(result.Conflicts) > 0 {
		fmt.Fprintf(&sb, "\n⚠ Warning: %d target file(s) already exist:\n", len(result.Conflicts))
		for _, c := range result.Conflicts {
			fmt.Fprintf(&sb, "  %s\n", c.Existing)
		}
		sb.WriteString("\nUse --force to overwrite.\n")
	}

	if len(result.Collisions) > 0 {
		fmt.Fprintf(&sb, "\n⚠ Error: %d target file(s) would receive more than one ebuild:\n", len(result.Collisions))
		for _, c := range result.Collisions {
			fmt.Fprintf(&sb, "  %s\n", c.describe())
		}
		sb.WriteString("\nThe rename is refused: one source would overwrite the other. --force does not override this.\n")
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
	matchResult, err := matcher.Match(spec)
	if err != nil {
		return nil, err
	}
	result.Matches = matchResult.Matches
	result.Warnings = matchResult.Warnings

	// No matches found
	if len(result.Matches) == 0 {
		return result, nil
	}

	// Two matches sharing one target would overwrite each other. Refused before
	// every other check and before any os.Rename, whatever Force and DryRun say.
	if collisions := findCollisions(result.Matches); len(collisions) > 0 {
		result.Collisions = collisions
		return result, &CollisionError{Collisions: collisions}
	}

	// Detect version-specific files
	detector := NewVersionFilesDetector(overlayPath)
	versionFiles := detector.Detect(result.Matches, spec.OldVersion)
	result.VersionFiles = versionFiles

	// Check if version files should block the operation
	if ShouldBlockForVersionFiles(versionFiles, opts.Force) {
		return result, &VersionFilesBlockError{Files: versionFiles}
	}

	// Check for conflicts (target files that already exist)
	for _, match := range result.Matches {
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
	for _, match := range result.Matches {
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

// updateManifests updates Manifest files for renamed packages using the
// shared regeneration helper. Duplicate (category, package) pairs are
// collapsed so each package is processed once.
func updateManifests(renamed []RenameMatch, overlayPath string) []ManifestUpdate {
	processed := make(map[string]bool)
	var targets []ManifestUpdate

	for _, match := range renamed {
		key := match.Category + "/" + match.Package
		if processed[key] {
			continue
		}
		processed[key] = true
		targets = append(targets, ManifestUpdate{
			Category: match.Category,
			Package:  match.Package,
		})
	}

	if len(targets) == 0 {
		return targets
	}

	// Rename flow keeps the existing Manifest (Keep=true): the new ebuild's
	// SRC_URI may share filenames with the old one and pkgdev will reconcile.
	//
	// Only the per-target rows are taken. The run's own two facts — whether it
	// was cut short, and how many targets it never evaluated — are dropped here
	// because this path passes no Ctx, so the run cannot be cancelled and both
	// are always the zero value. A rename that grows a cancellable context is
	// the change that must start carrying them.
	return RegenerateManifests(overlayPath, targets, &ManifestOptions{Keep: true}).Updates
}

// FormatRenameResult formats the rename result for display.
func FormatRenameResult(result *RenameResult, dryRun bool) string {
	var sb strings.Builder

	if len(result.Matches) == 0 {
		return "No matching ebuilds found"
	}

	if dryRun {
		fmt.Fprintf(&sb, "Dry run: %d ebuild(s) would be renamed\n\n", len(result.Matches))
		for _, match := range result.Matches {
			fmt.Fprintf(&sb, "  %s/%s:\n", match.Category, match.Package)
			fmt.Fprintf(&sb, "    %s → %s\n", match.OldFilename, match.NewFilename)
			if match.HasRevision {
				sb.WriteString("    (revision suffix will be stripped)\n")
			}
		}
	} else {
		if len(result.Renamed) > 0 {
			fmt.Fprintf(&sb, "Renamed %d ebuild(s):\n\n", len(result.Renamed))
			for _, match := range result.Renamed {
				fmt.Fprintf(&sb, "  %s/%s: %s → %s\n", match.Category, match.Package, match.OldFilename, match.NewFilename)
			}
		}

		if len(result.Failed) > 0 {
			fmt.Fprintf(&sb, "\nFailed %d ebuild(s):\n", len(result.Failed))
			for _, fail := range result.Failed {
				fmt.Fprintf(&sb, "  %s/%s: %s\n", fail.Match.Category, fail.Match.Package, fail.Message)
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
				fmt.Fprintf(&sb, "\nManifest updated for %d package(s)\n", successCount)
			}

			if failCount > 0 {
				fmt.Fprintf(&sb, "\nManifest update failed for %d package(s):\n", failCount)
				for _, u := range result.ManifestUpdates {
					if !u.Success {
						fmt.Fprintf(&sb, "  %s/%s: %s\n", u.Category, u.Package, u.Error)
					}
				}
			}
		}
	}

	if len(result.VersionFiles) > 0 {
		fmt.Fprintf(&sb, "\nWarning: %d version-specific file(s) detected:\n", len(result.VersionFiles))
		for _, vf := range result.VersionFiles {
			fmt.Fprintf(&sb, "  %s/%s/files/%s\n", vf.Category, vf.Package, vf.Filename)
		}
	}

	if len(result.Conflicts) > 0 {
		fmt.Fprintf(&sb, "\nConflicts: %d target file(s) already exist:\n", len(result.Conflicts))
		for _, c := range result.Conflicts {
			fmt.Fprintf(&sb, "  %s\n", c.Existing)
		}
	}

	return sb.String()
}
