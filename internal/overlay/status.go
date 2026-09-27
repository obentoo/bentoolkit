package overlay

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/git"
)

// FileType represents the type of file in an overlay
type FileType string

const (
	FileTypeEbuild   FileType = "ebuild"
	FileTypeManifest FileType = "manifest"
	FileTypeMetadata FileType = "metadata"
	FileTypeFiles    FileType = "files"
	FileTypeOther    FileType = "other"
)

// FileChange represents a single file change within a package
type FileChange struct {
	Type   FileType // ebuild, manifest, metadata, files, other
	Name   string   // filename
	Status string   // Added, Modified, Deleted, Renamed, Untracked
}

// PackageStatus represents the status of changes for a single package
type PackageStatus struct {
	Category string
	Package  string
	Changes  []FileChange
}

// statusLabelMap maps git status codes to human-readable labels
var statusLabelMap = map[string]string{
	"A":  "Added",
	"M":  "Modified",
	"D":  "Deleted",
	"R":  "Renamed",
	"??": "Untracked",
	"AM": "Added",
	"MM": "Modified",
	"AD": "Added",
}

// StatusLabel returns a human-readable label for a git status code
func StatusLabel(code string) string {
	if label, ok := statusLabelMap[code]; ok {
		return label
	}
	// Handle combined status codes (e.g., "AM", "MM")
	if len(code) >= 1 {
		firstChar := string(code[0])
		if label, ok := statusLabelMap[firstChar]; ok {
			return label
		}
	}
	return "Unknown"
}

// DetectFileType determines the type of file based on its path
func DetectFileType(filePath string) FileType {
	parts := strings.Split(filePath, "/")
	filename := parts[len(parts)-1]

	// Check for ebuild files
	if strings.HasSuffix(filename, ".ebuild") {
		return FileTypeEbuild
	}

	// Check for Manifest
	if filename == "Manifest" {
		return FileTypeManifest
	}

	// Check for metadata.xml
	if filename == "metadata.xml" {
		return FileTypeMetadata
	}

	// Check if file is in files/ directory
	for _, part := range parts {
		if part == "files" {
			return FileTypeFiles
		}
	}

	return FileTypeOther
}

// extractPackageInfo extracts category and package name from a file path
// Returns category, package, and whether extraction was successful
func extractPackageInfo(filePath string) (category, pkg string, ok bool) {
	parts := strings.Split(filePath, "/")
	if len(parts) < 2 {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// GroupStatusEntries groups git status entries by category/package
func GroupStatusEntries(entries []git.StatusEntry) []PackageStatus {
	// Map to group changes by category/package
	packageMap := make(map[string]*PackageStatus)

	for _, entry := range entries {
		category, pkg, ok := extractPackageInfo(entry.FilePath)
		if !ok {
			// Files at root level or with unusual paths
			category = ""
			pkg = "root"
		}

		key := category + "/" + pkg
		if _, exists := packageMap[key]; !exists {
			packageMap[key] = &PackageStatus{
				Category: category,
				Package:  pkg,
				Changes:  []FileChange{},
			}
		}

		// Extract filename from path
		parts := strings.Split(entry.FilePath, "/")
		filename := parts[len(parts)-1]

		change := FileChange{
			Type:   DetectFileType(entry.FilePath),
			Name:   filename,
			Status: StatusLabel(entry.Status),
		}

		packageMap[key].Changes = append(packageMap[key].Changes, change)
	}

	// Convert map to sorted slice
	result := make([]PackageStatus, 0, len(packageMap))
	for _, ps := range packageMap {
		result = append(result, *ps)
	}

	// Sort by category, then by package
	sort.Slice(result, func(i, j int) bool {
		if result[i].Category != result[j].Category {
			return result[i].Category < result[j].Category
		}
		return result[i].Package < result[j].Package
	})

	return result
}

// Status retrieves and groups the current git status for the overlay
func Status(ctx context.Context, cfg *config.Config) ([]PackageStatus, error) {
	overlayPath, err := cfg.GetOverlayPath()
	if err != nil {
		return nil, err
	}

	runner := git.NewGitRunner(overlayPath)
	return StatusWithExecutor(ctx, runner)
}

// StatusWithExecutor retrieves and groups the current git status using the provided GitExecutor.
// This function is useful for testing with mock implementations.
func StatusWithExecutor(ctx context.Context, executor git.GitExecutor) ([]PackageStatus, error) {
	entries, err := executor.Status(ctx)
	if err != nil {
		return nil, err
	}

	return GroupStatusEntries(entries), nil
}

// StagedStatus retrieves and groups only the changes staged in the index,
// i.e. exactly what a commit would include. Unlike Status, it ignores files
// that are merely modified in the worktree but not staged.
func StagedStatus(ctx context.Context, cfg *config.Config) ([]PackageStatus, error) {
	overlayPath, err := cfg.GetOverlayPath()
	if err != nil {
		return nil, err
	}

	runner := git.NewGitRunner(overlayPath)
	return StagedStatusWithExecutor(ctx, runner)
}

// StagedStatusWithExecutor retrieves and groups the staged git status using the
// provided GitExecutor. This function is useful for testing with mocks.
func StagedStatusWithExecutor(ctx context.Context, executor git.GitExecutor) ([]PackageStatus, error) {
	entries, err := executor.StagedStatus(ctx)
	if err != nil {
		return nil, err
	}

	return GroupStatusEntries(entries), nil
}

// statusCleanTree is what a working tree with nothing to report says.
//
// It is a CONSTANT so a test — and, later, a renderer — can name the sentence
// without copying it, on the same argument that made baselineSkippedLead one.
//
// It also exists at all because this outcome may not render as silence: a clean
// tree and a command that failed to look produce the same zero bytes, and only
// a sentence tells them apart.
const statusCleanTree = "No changes detected (working directory clean)"

// FormatStatus renders package statuses as the lines a caller prints, and
// chooses NO APPEARANCE (S046-R5.2).
//
// # What left, and why the composition stayed
//
// Three things here used to be built by the terminal printer package: the clean-tree
// sentence arrived dimmed, the package heading arrived blue and bold, and every
// change's status arrived in the colour of its git letter. A colour is a
// decision only a terminal can use — the same status could not then be written
// to a Markdown file, exported as JSON, diffed or counted (design.md D7) — so
// the library no longer makes it, and whoever is printing does.
//
// Two of those helpers were doing something ELSE as well, and that part stayed:
// output.FormatPackage joined the category to the package, and
// output.FormatStatus wrapped the status in brackets. Composing the facts into a
// line is what this function is for; only the appearance crossed the boundary,
// which is why the import went and the shape of the text did not.
//
// The result is byte-identical to what the command printed OFF A TTY, since
// fatih/color returns bare text there — so every pipe, log and CI run reads
// exactly what it read yesterday. On a terminal the colour is gone, and story
// 047 is where the whole CLI's presentation is restyled through the report
// renderers; reintroducing it here would put the decision back in the library
// that just gave it up.
func FormatStatus(statuses []PackageStatus) string {
	if len(statuses) == 0 {
		return statusCleanTree
	}

	var sb strings.Builder

	for i, ps := range statuses {
		if i > 0 {
			sb.WriteString("\n")
		}

		// Write package header
		sb.WriteString(statusPackageLabel(ps.Category, ps.Package))
		sb.WriteString(":\n")

		// Group changes by file type for cleaner output
		changesByType := make(map[FileType][]FileChange)
		for _, change := range ps.Changes {
			changesByType[change.Type] = append(changesByType[change.Type], change)
		}

		// Order of file types for display
		typeOrder := []FileType{FileTypeEbuild, FileTypeManifest, FileTypeMetadata, FileTypeFiles, FileTypeOther}

		for _, ft := range typeOrder {
			changes, exists := changesByType[ft]
			if !exists {
				continue
			}

			for _, change := range changes {
				// The brackets are output.FormatStatus's composition, moved here
				// without its colour. Every value interpolated is a git working
				// tree's own text — a filename this process did not choose — so
				// each is passed as an ARGUMENT and never as a format string.
				fmt.Fprintf(&sb, "  [%s] %s (%s)\n", change.Status, change.Name, ft)
			}
		}
	}

	return strings.TrimSuffix(sb.String(), "\n")
}

// statusPackageLabel names the package a group of changes belongs to.
//
// It is output.FormatPackage's join, kept because it is composition and not
// appearance: the caller is handed "app-misc/jq", which is what an operator
// types, rather than two fields it would have to join the same way itself.
//
// The empty category is a REAL case rather than defensive padding.
// GroupStatusEntries files anything outside a category/package directory under
// the category "" and the package "root" — the overlay's own metadata/ and
// profiles/ reach here that way — and a bare "/root" would read as a path in
// the filesystem root, which is the one thing it is not.
func statusPackageLabel(category, pkg string) string {
	if category == "" {
		return pkg
	}
	return category + "/" + pkg
}
