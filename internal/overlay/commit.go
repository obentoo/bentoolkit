package overlay

import (
	"sort"
	"strings"

	"github.com/obentoo/bentoo-tools/internal/common/config"
	"github.com/obentoo/bentoo-tools/internal/common/ebuild"
	"github.com/obentoo/bentoo-tools/internal/common/git"
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

// AnalyzeChanges analyzes git status entries and returns a list of changes
// It detects version bumps by finding paired add/delete operations
func AnalyzeChanges(entries []git.StatusEntry) []Change {
	var changes []Change

	// Track added and deleted ebuilds for version bump detection
	// Key: category/package, Value: list of versions with their status
	type versionInfo struct {
		version string
		status  string // "A" for added, "D" for deleted
	}
	packageVersions := make(map[string][]versionInfo)

	// Track modified ebuilds
	modifiedEbuilds := make(map[string]*ebuild.Ebuild)

	// First pass: collect all ebuild changes
	for _, entry := range entries {
		eb, err := ebuild.ParsePath(entry.FilePath)
		if err != nil {
			// Not an ebuild file, skip for now
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


	// Second pass: detect version bumps and classify changes
	for key, versions := range packageVersions {
		parts := strings.SplitN(key, "/", 2)
		category := parts[0]
		pkg := parts[1]

		// Separate added and deleted versions
		var added, deleted []string
		for _, v := range versions {
			if v.status == "A" {
				added = append(added, v.version)
			} else {
				deleted = append(deleted, v.version)
			}
		}

		// Try to pair added and deleted versions for bump detection
		pairedAdded := make(map[string]bool)
		pairedDeleted := make(map[string]bool)

		// For each deleted version, find the best matching added version
		for _, delVer := range deleted {
			bestMatch := ""
			for _, addVer := range added {
				if pairedAdded[addVer] {
					continue
				}
				// Prefer the closest version
				if bestMatch == "" {
					bestMatch = addVer
				}
			}

			if bestMatch != "" {
				pairedAdded[bestMatch] = true
				pairedDeleted[delVer] = true

				// Determine if it's an upgrade or downgrade
				cmp := ebuild.CompareVersions(bestMatch, delVer)
				if cmp > 0 {
					changes = append(changes, Change{
						Type:       Up,
						Category:   category,
						Package:    pkg,
						Version:    bestMatch,
						OldVersion: delVer,
					})
				} else if cmp < 0 {
					changes = append(changes, Change{
						Type:       Down,
						Category:   category,
						Package:    pkg,
						Version:    bestMatch,
						OldVersion: delVer,
					})
				} else {
					// Same version - treat as separate add/del
					pairedAdded[bestMatch] = false
					pairedDeleted[delVer] = false
				}
			}
		}

		// Add remaining unpaired additions
		for _, addVer := range added {
			if !pairedAdded[addVer] {
				changes = append(changes, Change{
					Type:     Add,
					Category: category,
					Package:  pkg,
					Version:  addVer,
				})
			}
		}

		// Add remaining unpaired deletions
		for _, delVer := range deleted {
			if !pairedDeleted[delVer] {
				changes = append(changes, Change{
					Type:     Del,
					Category: category,
					Package:  pkg,
					Version:  delVer,
				})
			}
		}
	}

	// Add modified ebuilds
	for _, eb := range modifiedEbuilds {
		changes = append(changes, Change{
			Type:     Mod,
			Category: eb.Category,
			Package:  eb.Package,
			Version:  eb.Version,
		})
	}

	// Sort changes for consistent output
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

	return changes
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


// GenerateMessage generates a commit message from a list of changes
func GenerateMessage(changes []Change) string {
	if len(changes) == 0 {
		return "update: package files"
	}

	// Group changes by type
	byType := make(map[ChangeType][]Change)
	for _, c := range changes {
		byType[c.Type] = append(byType[c.Type], c)
	}

	// Build message parts in order: add, del, mod, up, down
	typeOrder := []ChangeType{Add, Del, Mod, Up, Down}
	var parts []string

	for _, ct := range typeOrder {
		typeChanges, ok := byType[ct]
		if !ok || len(typeChanges) == 0 {
			continue
		}

		part := formatChangeGroup(ct, typeChanges)
		if part != "" {
			parts = append(parts, part)
		}
	}

	if len(parts) == 0 {
		return "update: package files"
	}

	return strings.Join(parts, ", ")
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

	// Get git user info
	user := cfg.Git.User
	email := cfg.Git.Email

	runner := git.NewGitRunner(overlayPath)
	return runner.Commit(message, user, email)
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
