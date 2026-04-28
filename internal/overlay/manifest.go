// Package overlay provides business logic for overlay management operations.
package overlay

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/obentoo/bentoolkit/internal/common/config"
)

// Errors for manifest operations.
var (
	ErrPkgdevNotFound       = errors.New("pkgdev not found; install dev-util/pkgdev")
	ErrManifestNoTargets    = errors.New("no packages found to update")
	ErrManifestInvalidScope = errors.New("invalid manifest scope")
)

// ManifestScope identifies one or more packages to regenerate Manifests for.
//
// Resolution rules:
//   - Empty Category and Package: every package in the overlay.
//   - Non-empty Category, empty Package: every package in that category.
//   - Both set: that single package.
type ManifestScope struct {
	Category string
	Package  string
}

// ManifestOptions controls Manifest regeneration behavior.
type ManifestOptions struct {
	// Keep, if true, leaves the existing Manifest in place and lets pkgdev
	// reconcile it. By default, the existing Manifest is moved to a backup
	// before regeneration and restored only on failure (clean regen).
	Keep bool
	// DryRun, if true, lists the packages that would be processed without
	// running pkgdev or touching files.
	DryRun bool
}

// ManifestResult collects per-package results of a regeneration run.
type ManifestResult struct {
	Updates []ManifestUpdate
}

// ParseManifestScope parses a single CLI argument into a ManifestScope.
//
// Accepted forms:
//   - ""                      -> whole overlay
//   - "<category>"            -> all packages in category
//   - "<category>/<package>"  -> single package
func ParseManifestScope(arg string) (ManifestScope, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return ManifestScope{}, nil
	}
	parts := strings.Split(arg, "/")
	switch len(parts) {
	case 1:
		cat := strings.TrimSpace(parts[0])
		if cat == "" {
			return ManifestScope{}, fmt.Errorf("%w: empty category", ErrManifestInvalidScope)
		}
		return ManifestScope{Category: cat}, nil
	case 2:
		cat := strings.TrimSpace(parts[0])
		pkg := strings.TrimSpace(parts[1])
		if cat == "" || pkg == "" {
			return ManifestScope{}, fmt.Errorf("%w: expected <category>/<package>", ErrManifestInvalidScope)
		}
		return ManifestScope{Category: cat, Package: pkg}, nil
	default:
		return ManifestScope{}, fmt.Errorf("%w: too many '/' separators in %q", ErrManifestInvalidScope, arg)
	}
}

// ResolveManifestTargets expands a scope into the concrete list of packages
// (category/package pairs) present in the overlay.
func ResolveManifestTargets(overlayPath string, scope ManifestScope) ([]ManifestUpdate, error) {
	if overlayPath == "" {
		return nil, ErrOverlayPathNotSet
	}

	if scope.Category != "" && scope.Package != "" {
		pkgDir := filepath.Join(overlayPath, scope.Category, scope.Package)
		if !isPackageDir(pkgDir) {
			return nil, fmt.Errorf("package %s/%s not found in overlay", scope.Category, scope.Package)
		}
		return []ManifestUpdate{{Category: scope.Category, Package: scope.Package}}, nil
	}

	scan, err := ScanOverlay(overlayPath)
	if err != nil {
		return nil, fmt.Errorf("scanning overlay: %w", err)
	}

	var targets []ManifestUpdate
	for _, p := range scan.Packages {
		if scope.Category != "" && p.Category != scope.Category {
			continue
		}
		targets = append(targets, ManifestUpdate{Category: p.Category, Package: p.Package})
	}

	sort.Slice(targets, func(i, j int) bool {
		if targets[i].Category != targets[j].Category {
			return targets[i].Category < targets[j].Category
		}
		return targets[i].Package < targets[j].Package
	})

	if len(targets) == 0 {
		if scope.Category != "" {
			return nil, fmt.Errorf("%w: category %q has no packages", ErrManifestNoTargets, scope.Category)
		}
		return nil, ErrManifestNoTargets
	}

	return targets, nil
}

// RegenerateManifests regenerates Manifest files for the given packages using
// pkgdev. By default it removes (backs up) the existing Manifest before
// running pkgdev and restores it on failure. Pass opts.Keep=true to skip the
// backup/clean step.
//
// pkgdev is invoked with a dedicated --distdir under os.TempDir() so the
// command never requires sudo and never touches /var/cache/distfiles.
//
// Each call processes packages sequentially. Results are returned in the same
// order as the input.
func RegenerateManifests(overlayPath string, targets []ManifestUpdate, opts *ManifestOptions) []ManifestUpdate {
	if opts == nil {
		opts = &ManifestOptions{}
	}

	updates := make([]ManifestUpdate, len(targets))
	copy(updates, targets)

	if len(updates) == 0 {
		return updates
	}

	if opts.DryRun {
		return updates
	}

	if _, err := exec.LookPath("pkgdev"); err != nil {
		for i := range updates {
			updates[i].Success = false
			updates[i].Error = ErrPkgdevNotFound.Error()
		}
		return updates
	}

	tmpDistdir, err := os.MkdirTemp("", "bentoo-distfiles-")
	if err != nil {
		for i := range updates {
			updates[i].Success = false
			updates[i].Error = fmt.Sprintf("failed to create temp distdir: %v", err)
		}
		return updates
	}
	defer os.RemoveAll(tmpDistdir) //nolint:errcheck

	for i, u := range updates {
		pkgPath := filepath.Join(overlayPath, u.Category, u.Package)
		manifestPath := filepath.Join(pkgPath, "Manifest")

		// Clean regen: move Manifest aside so pkgdev produces a fresh one.
		// Restored only on failure, removed on success.
		var backupPath string
		if !opts.Keep {
			if _, statErr := os.Stat(manifestPath); statErr == nil {
				backupPath = manifestPath + ".bak"
				if mvErr := os.Rename(manifestPath, backupPath); mvErr != nil {
					updates[i].Success = false
					updates[i].Error = fmt.Sprintf("failed to back up Manifest: %v", mvErr)
					continue
				}
			}
		}

		fmt.Printf(">>> Regenerating Manifest for %s/%s (pkgdev)\n", u.Category, u.Package)

		cmd := exec.Command("pkgdev", "manifest", "--distdir", tmpDistdir)
		cmd.Dir = pkgPath
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		runErr := cmd.Run()
		if runErr != nil {
			updates[i].Success = false
			updates[i].Error = runErr.Error()
			if backupPath != "" {
				if rbErr := os.Rename(backupPath, manifestPath); rbErr != nil {
					updates[i].Error = fmt.Sprintf("%s; rollback failed: %v", updates[i].Error, rbErr)
				}
			}
			continue
		}

		if backupPath != "" {
			_ = os.Remove(backupPath)
		}
		updates[i].Success = true
	}

	return updates
}

// RegenerateManifestsForScope is a convenience wrapper that resolves a scope
// and runs RegenerateManifests.
func RegenerateManifestsForScope(cfg *config.Config, scope ManifestScope, opts *ManifestOptions) (*ManifestResult, error) {
	if cfg == nil {
		return nil, ErrOverlayPathNotSet
	}
	overlayPath, err := cfg.GetOverlayPath()
	if err != nil {
		return nil, err
	}
	targets, err := ResolveManifestTargets(overlayPath, scope)
	if err != nil {
		return nil, err
	}
	return &ManifestResult{
		Updates: RegenerateManifests(overlayPath, targets, opts),
	}, nil
}

// FormatManifestResult renders a ManifestResult for display.
func FormatManifestResult(result *ManifestResult, dryRun bool) string {
	var sb strings.Builder

	if result == nil || len(result.Updates) == 0 {
		return "No packages processed"
	}

	if dryRun {
		fmt.Fprintf(&sb, "Dry run: %d package(s) would have Manifest regenerated\n\n", len(result.Updates))
		for _, u := range result.Updates {
			fmt.Fprintf(&sb, "  %s/%s\n", u.Category, u.Package)
		}
		return sb.String()
	}

	var success, failed int
	for _, u := range result.Updates {
		if u.Success {
			success++
		} else {
			failed++
		}
	}

	fmt.Fprintf(&sb, "Manifest regeneration: %d succeeded, %d failed (of %d)\n",
		success, failed, len(result.Updates))

	if failed > 0 {
		sb.WriteString("\nFailures:\n")
		for _, u := range result.Updates {
			if !u.Success {
				fmt.Fprintf(&sb, "  %s/%s: %s\n", u.Category, u.Package, u.Error)
			}
		}
	}

	return sb.String()
}

// isPackageDir reports whether the path looks like a valid package directory
// (exists, is a directory, contains at least one .ebuild file).
func isPackageDir(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return false
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".ebuild") {
			return true
		}
	}
	return false
}
