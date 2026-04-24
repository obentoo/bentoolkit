package overlay

import (
	"sort"
	"strings"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/ebuild"
	"github.com/obentoo/bentoolkit/internal/common/git"
)

// ChangeType represents the type of change detected
type ChangeType string

const (
	Add  ChangeType = "add"
	Del  ChangeType = "del"
	Mod  ChangeType = "mod"
	Up   ChangeType = "up"
	Down ChangeType = "down"
)

// Change represents a single package change
type Change struct {
	Type       ChangeType
	Category   string
	Package    string
	Version    string
	OldVersion string // for up/down
}

// FileKind classifies a non-ebuild overlay file by its top-level directory.
type FileKind string

const (
	KindEclass   FileKind = "eclass"
	KindProfile  FileKind = "profile"
	KindLicense  FileKind = "license"
	KindMetadata FileKind = "metadata"
	KindOther    FileKind = "other"
)

// kindDirectory is the top-level directory name used when rendering a FileKind.
var kindDirectory = map[FileKind]string{
	KindEclass:   "eclass",
	KindProfile:  "profiles",
	KindLicense:  "licenses",
	KindMetadata: "metadata",
}

// RepoFileChange represents a change to a non-ebuild file in the overlay.
type RepoFileChange struct {
	Type ChangeType // Add, Del, Mod
	Kind FileKind
	Path string // path relative to overlay root (e.g. "eclass/rpm.eclass")
	Name string // filename only (e.g. "rpm.eclass")
}

// ClassifyFile returns the FileKind for a path relative to the overlay root.
// Classification is based solely on the top-level directory; paths that live
// inside a category (category/pkg/...) return KindOther because ebuild-aware
// logic handles them elsewhere.
func ClassifyFile(filePath string) FileKind {
	parts := strings.Split(filePath, "/")
	if len(parts) == 0 {
		return KindOther
	}
	switch parts[0] {
	case "eclass":
		return KindEclass
	case "profiles":
		return KindProfile
	case "licenses":
		return KindLicense
	case "metadata":
		return KindMetadata
	default:
		return KindOther
	}
}

// versionInfo holds a version string and its git status ("A" added, "D" deleted).
type versionInfo struct {
	version string
	status  string
}

// AnalyzeChanges analyzes git status entries and returns a sorted list of changes.
// It detects version bumps by finding paired add/delete operations.
func AnalyzeChanges(entries []git.StatusEntry) []Change {
	packageVersions, modifiedEbuilds := collectEbuildChanges(entries)
	changes := detectVersionBumps(packageVersions)
	changes = append(changes, buildModifiedChanges(modifiedEbuilds)...)
	sortChanges(changes)
	return changes
}

// collectEbuildChanges categorizes status entries into added/deleted versions and modified ebuilds.
func collectEbuildChanges(entries []git.StatusEntry) (map[string][]versionInfo, map[string]*ebuild.Ebuild) {
	packageVersions := make(map[string][]versionInfo)
	modifiedEbuilds := make(map[string]*ebuild.Ebuild)

	for _, entry := range entries {
		eb, err := ebuild.ParsePath(entry.FilePath)
		if err != nil {
			continue
		}

		key := eb.FullName()
		status := normalizeStatus(entry.Status)

		switch status {
		case "A":
			packageVersions[key] = append(packageVersions[key], versionInfo{eb.Version, "A"})
		case "D":
			packageVersions[key] = append(packageVersions[key], versionInfo{eb.Version, "D"})
		case "M":
			modifiedEbuilds[key+"-"+eb.Version] = eb
		}
	}

	return packageVersions, modifiedEbuilds
}

// detectVersionBumps pairs added/deleted versions to detect upgrades and downgrades.
// Unpaired additions become Add changes; unpaired deletions become Del changes.
func detectVersionBumps(packageVersions map[string][]versionInfo) []Change {
	var changes []Change

	for key, versions := range packageVersions {
		parts := strings.SplitN(key, "/", 2)
		category := parts[0]
		pkg := parts[1]

		var added, deleted []string
		for _, v := range versions {
			if v.status == "A" {
				added = append(added, v.version)
			} else {
				deleted = append(deleted, v.version)
			}
		}

		pairedAdded := make(map[string]bool)
		pairedDeleted := make(map[string]bool)

		for _, delVer := range deleted {
			bestMatch := ""
			for _, addVer := range added {
				if pairedAdded[addVer] {
					continue
				}
				if bestMatch == "" {
					bestMatch = addVer
				}
			}

			if bestMatch != "" {
				pairedAdded[bestMatch] = true
				pairedDeleted[delVer] = true

				cmp := ebuild.CompareVersions(bestMatch, delVer)
				switch {
				case cmp > 0:
					changes = append(changes, Change{Type: Up, Category: category, Package: pkg, Version: bestMatch, OldVersion: delVer})
				case cmp < 0:
					changes = append(changes, Change{Type: Down, Category: category, Package: pkg, Version: bestMatch, OldVersion: delVer})
				default:
					pairedAdded[bestMatch] = false
					pairedDeleted[delVer] = false
				}
			}
		}

		for _, addVer := range added {
			if !pairedAdded[addVer] {
				changes = append(changes, Change{Type: Add, Category: category, Package: pkg, Version: addVer})
			}
		}

		for _, delVer := range deleted {
			if !pairedDeleted[delVer] {
				changes = append(changes, Change{Type: Del, Category: category, Package: pkg, Version: delVer})
			}
		}
	}

	return changes
}

// buildModifiedChanges converts modified ebuilds into Change objects.
func buildModifiedChanges(modifiedEbuilds map[string]*ebuild.Ebuild) []Change {
	changes := make([]Change, 0, len(modifiedEbuilds))
	for _, eb := range modifiedEbuilds {
		changes = append(changes, Change{Type: Mod, Category: eb.Category, Package: eb.Package, Version: eb.Version})
	}
	return changes
}

// sortChanges sorts changes deterministically by type, category, package, and version.
func sortChanges(changes []Change) {
	sort.Slice(changes, func(i, j int) bool {
		if changes[i].Type != changes[j].Type {
			return changes[i].Type < changes[j].Type
		}
		if changes[i].Category != changes[j].Category {
			return changes[i].Category < changes[j].Category
		}
		if changes[i].Package != changes[j].Package {
			return changes[i].Package < changes[j].Package
		}
		return changes[i].Version < changes[j].Version
	})
}

// normalizeStatus converts git status codes to single-character codes
func normalizeStatus(status string) string {
	status = strings.TrimSpace(status)
	if len(status) == 0 {
		return ""
	}
	// Use the first character (index status) for staged files
	// Handle special cases
	switch {
	case status == "??":
		return "A" // Treat untracked as added
	case strings.HasPrefix(status, "A"):
		return "A"
	case strings.HasPrefix(status, "D"):
		return "D"
	case strings.HasPrefix(status, "M"):
		return "M"
	case strings.HasPrefix(status, "R"):
		return "A" // Renamed files are treated as added
	default:
		return status[:1]
	}
}

// AnalyzeRepoFileChanges analyzes git status entries and returns a sorted list of
// changes to non-ebuild files (eclasses, profiles, licenses, metadata,
// arbitrary repo files). Entries that correspond to a parseable ebuild are
// ignored here — use AnalyzeChanges for those.
func AnalyzeRepoFileChanges(entries []git.StatusEntry) []RepoFileChange {
	var files []RepoFileChange

	for _, entry := range entries {
		if _, err := ebuild.ParsePath(entry.FilePath); err == nil {
			continue
		}

		path := entry.FilePath
		if path == "" {
			continue
		}

		var ct ChangeType
		switch normalizeStatus(entry.Status) {
		case "A":
			ct = Add
		case "D":
			ct = Del
		case "M":
			ct = Mod
		default:
			continue
		}

		parts := strings.Split(path, "/")
		name := parts[len(parts)-1]

		files = append(files, RepoFileChange{
			Type: ct,
			Kind: ClassifyFile(path),
			Path: path,
			Name: name,
		})
	}

	sortRepoFileChanges(files)
	return files
}

// sortRepoFileChanges sorts file changes deterministically by type, kind, and path.
func sortRepoFileChanges(files []RepoFileChange) {
	sort.Slice(files, func(i, j int) bool {
		if files[i].Type != files[j].Type {
			return files[i].Type < files[j].Type
		}
		if files[i].Kind != files[j].Kind {
			return files[i].Kind < files[j].Kind
		}
		return files[i].Path < files[j].Path
	})
}

// GenerateMessage generates a commit message from a list of package changes.
// Kept for backward compatibility; see GenerateCommitMessage for the combined
// form that also renders non-ebuild file changes.
func GenerateMessage(changes []Change) string {
	return GenerateCommitMessage(changes, nil)
}

// GenerateCommitMessage generates a commit message from both package (ebuild)
// changes and non-ebuild file changes. Parts are joined with ", " in the order
// add, del, mod, up, down — packages first within each action, then files.
func GenerateCommitMessage(changes []Change, files []RepoFileChange) string {
	if len(changes) == 0 && len(files) == 0 {
		return "update: package files"
	}

	byType := make(map[ChangeType][]Change)
	for _, c := range changes {
		byType[c.Type] = append(byType[c.Type], c)
	}

	filesByType := make(map[ChangeType][]RepoFileChange)
	for _, f := range files {
		filesByType[f.Type] = append(filesByType[f.Type], f)
	}

	typeOrder := []ChangeType{Add, Del, Mod, Up, Down}
	var parts []string

	for _, ct := range typeOrder {
		if pkgPart := formatChangeGroup(ct, byType[ct]); pkgPart != "" {
			parts = append(parts, pkgPart)
		}
		if filePart := formatRepoFileChangeGroup(ct, filesByType[ct]); filePart != "" {
			parts = append(parts, filePart)
		}
	}

	if len(parts) == 0 {
		return "update: package files"
	}

	return strings.Join(parts, ", ")
}

// formatRepoFileChangeGroup formats a group of non-ebuild file changes of the same
// action type into a string like "add(eclass/rpm.eclass)" or
// "add(eclass/{rpm.eclass, sourceforge.eclass})".
func formatRepoFileChangeGroup(ct ChangeType, files []RepoFileChange) string {
	if len(files) == 0 {
		return ""
	}

	byKind := make(map[FileKind][]RepoFileChange)
	for _, f := range files {
		byKind[f.Kind] = append(byKind[f.Kind], f)
	}

	kindOrder := []FileKind{KindEclass, KindProfile, KindLicense, KindMetadata, KindOther}

	var kindParts []string
	for _, kind := range kindOrder {
		group, ok := byKind[kind]
		if !ok || len(group) == 0 {
			continue
		}
		kindParts = append(kindParts, formatFileKindGroup(kind, group))
	}

	return string(ct) + "(" + strings.Join(kindParts, ", ") + ")"
}

// formatFileKindGroup formats a group of file changes sharing the same kind.
// Known kinds render as "dir/name" or "dir/{name1, name2}"; unknown files
// (KindOther) render using their full relative path.
func formatFileKindGroup(kind FileKind, files []RepoFileChange) string {
	dir, hasDir := kindDirectory[kind]

	if !hasDir {
		paths := make([]string, 0, len(files))
		for _, f := range files {
			paths = append(paths, f.Path)
		}
		sort.Strings(paths)
		if len(paths) == 1 {
			return paths[0]
		}
		return "{" + strings.Join(paths, ", ") + "}"
	}

	names := make([]string, 0, len(files))
	seen := make(map[string]bool)
	for _, f := range files {
		rel := strings.TrimPrefix(f.Path, dir+"/")
		if seen[rel] {
			continue
		}
		seen[rel] = true
		names = append(names, rel)
	}
	sort.Strings(names)

	if len(names) == 1 {
		return dir + "/" + names[0]
	}
	return dir + "/{" + strings.Join(names, ", ") + "}"
}

// formatChangeGroup formats a group of changes of the same type
func formatChangeGroup(ct ChangeType, changes []Change) string {
	if len(changes) == 0 {
		return ""
	}

	// Group by category
	byCategory := make(map[string][]Change)
	for _, c := range changes {
		byCategory[c.Category] = append(byCategory[c.Category], c)
	}

	// Sort categories for consistent output
	categories := make([]string, 0, len(byCategory))
	for cat := range byCategory {
		categories = append(categories, cat)
	}
	sort.Strings(categories)

	var categoryParts []string
	for _, cat := range categories {
		catChanges := byCategory[cat]
		part := formatCategoryChanges(cat, catChanges, ct)
		categoryParts = append(categoryParts, part)
	}

	return string(ct) + "(" + strings.Join(categoryParts, ", ") + ")"
}

// formatCategoryChanges formats changes within a single category
func formatCategoryChanges(category string, changes []Change, ct ChangeType) string {
	if len(changes) == 0 {
		return ""
	}

	// Check for package variants (e.g., firefox and firefox-bin)
	variants := detectVariants(changes)

	if len(variants) > 0 {
		return formatVariants(category, variants, ct)
	}

	// No variants, format normally
	if len(changes) == 1 {
		return formatSingleChange(category, changes[0], ct)
	}

	// Multiple packages in same category - use braces
	if sharedVer := sharedVersion(changes, ct); sharedVer != "" {
		var names []string
		for _, c := range changes {
			names = append(names, c.Package)
		}
		return category + "/{" + strings.Join(names, ", ") + "}-" + sharedVer
	}

	var pkgParts []string
	for _, c := range changes {
		pkgParts = append(pkgParts, formatPackageVersion(c, ct))
	}

	return category + "/{" + strings.Join(pkgParts, ", ") + "}"
}

// variantGroup represents a group of package variants
type variantGroup struct {
	baseName string
	suffixes []string // e.g., ["", "-bin"]
	changes  []Change
}

// detectVariants detects package variants like firefox/firefox-bin
func detectVariants(changes []Change) []variantGroup {
	if len(changes) < 2 {
		return nil
	}

	// Group by potential base name
	// A variant is detected when we have packages like "pkg" and "pkg-bin"
	pkgMap := make(map[string]Change)
	for _, c := range changes {
		pkgMap[c.Package] = c
	}

	var groups []variantGroup
	used := make(map[string]bool)

	for _, c := range changes {
		if used[c.Package] {
			continue
		}

		// Check for common variant suffixes
		suffixes := []string{"-bin", "-qt5", "-qt6", "-gtk", "-gtk2", "-gtk3"}
		var foundVariants []Change
		var foundSuffixes []string

		for _, suffix := range suffixes {
			variantName := c.Package + suffix
			if variant, ok := pkgMap[variantName]; ok && !used[variantName] {
				// Check if versions match for up/down changes
				if c.Type == Up || c.Type == Down {
					if variant.Version == c.Version && variant.OldVersion == c.OldVersion {
						foundVariants = append(foundVariants, variant)
						foundSuffixes = append(foundSuffixes, suffix)
					}
				} else if variant.Version == c.Version {
					foundVariants = append(foundVariants, variant)
					foundSuffixes = append(foundSuffixes, suffix)
				}
			}
		}

		if len(foundVariants) > 0 {
			// Found variants
			group := variantGroup{
				baseName: c.Package,
				suffixes: append([]string{""}, foundSuffixes...),
				changes:  append([]Change{c}, foundVariants...),
			}
			groups = append(groups, group)
			used[c.Package] = true
			for _, v := range foundVariants {
				used[v.Package] = true
			}
		}
	}

	return groups
}

// formatVariants formats a group of package variants with nested braces
func formatVariants(category string, groups []variantGroup, ct ChangeType) string {
	var parts []string

	for _, g := range groups {
		// Format: pkg{,-bin}-version or pkg{,-bin}-oldver -> newver
		suffixPart := "{" + strings.Join(g.suffixes, ",") + "}"
		c := g.changes[0] // Use first change for version info

		if ct == Up || ct == Down {
			parts = append(parts, g.baseName+suffixPart+"-"+c.OldVersion+" -> "+c.Version)
		} else {
			parts = append(parts, g.baseName+suffixPart+"-"+c.Version)
		}
	}

	if len(parts) == 1 {
		return category + "/" + parts[0]
	}

	return category + "/{" + strings.Join(parts, ", ") + "}"
}

// sharedVersion returns a common version string when all changes share the
// same version (and OldVersion for Up/Down). Returns "" if versions differ.
func sharedVersion(changes []Change, ct ChangeType) string {
	if len(changes) == 0 {
		return ""
	}
	first := changes[0]
	for _, c := range changes[1:] {
		if c.Version != first.Version {
			return ""
		}
		if (ct == Up || ct == Down) && c.OldVersion != first.OldVersion {
			return ""
		}
	}
	if ct == Up || ct == Down {
		return first.OldVersion + " -> " + first.Version
	}
	return first.Version
}

// formatSingleChange formats a single change
func formatSingleChange(category string, c Change, ct ChangeType) string {
	if ct == Up || ct == Down {
		return category + "/" + c.Package + "-" + c.OldVersion + " -> " + c.Version
	}
	return category + "/" + c.Package + "-" + c.Version
}

// formatPackageVersion formats a package-version string for grouping
func formatPackageVersion(c Change, ct ChangeType) string {
	if ct == Up || ct == Down {
		return c.Package + "-" + c.OldVersion + " -> " + c.Version
	}
	return c.Package + "-" + c.Version
}

// Commit executes a git commit with the given message
func Commit(cfg *config.Config, message string) error {
	overlayPath, err := cfg.GetOverlayPath()
	if err != nil {
		return err
	}

	runner := git.NewGitRunner(overlayPath)
	return CommitWithExecutor(cfg, message, runner)
}

// CommitWithExecutor executes a git commit using the provided GitExecutor.
// This function is useful for testing with mock implementations.
func CommitWithExecutor(cfg *config.Config, message string, executor git.GitExecutor) error {
	// Get git user info
	user := cfg.Git.User
	email := cfg.Git.Email

	return executor.Commit(message, user, email)
}

// GetStagedChanges returns the list of changes from staged files
func GetStagedChanges(cfg *config.Config) ([]Change, error) {
	overlayPath, err := cfg.GetOverlayPath()
	if err != nil {
		return nil, err
	}

	runner := git.NewGitRunner(overlayPath)
	entries, err := runner.Status()
	if err != nil {
		return nil, err
	}

	// Filter to only staged entries (those with index status)
	var stagedEntries []git.StatusEntry
	for _, e := range entries {
		status := strings.TrimSpace(e.Status)
		if len(status) > 0 && status[0] != ' ' && status != "??" {
			stagedEntries = append(stagedEntries, e)
		}
	}

	return AnalyzeChanges(stagedEntries), nil
}

// HasEbuildChanges checks if there are any ebuild changes in the list
func HasEbuildChanges(changes []Change) bool {
	return len(changes) > 0
}
