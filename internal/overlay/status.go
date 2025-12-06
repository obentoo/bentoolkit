package overlay

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lucascouts/bentoo-tools/internal/common/config"
	"github.com/lucascouts/bentoo-tools/internal/common/git"
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
func Status(cfg *config.Config) ([]PackageStatus, error) {
	overlayPath, err := cfg.GetOverlayPath()
	if err != nil {
		return nil, err
	}

	runner := git.NewGitRunner(overlayPath)
	entries, err := runner.Status()
	if err != nil {
		return nil, err
	}

	return GroupStatusEntries(entries), nil
}

// FormatStatus formats package statuses into a human-readable string
func FormatStatus(statuses []PackageStatus) string {
	if len(statuses) == 0 {
		return "No changes detected (working directory clean)"
	}

	var sb strings.Builder

	for i, ps := range statuses {
		if i > 0 {
			sb.WriteString("\n")
		}

		// Write package header
		if ps.Category != "" {
			sb.WriteString(fmt.Sprintf("%s/%s:\n", ps.Category, ps.Package))
		} else {
			sb.WriteString(fmt.Sprintf("%s:\n", ps.Package))
		}

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
				sb.WriteString(fmt.Sprintf("  [%s] %s (%s)\n", change.Status, change.Name, ft))
			}
		}
	}

	return strings.TrimSuffix(sb.String(), "\n")
}
