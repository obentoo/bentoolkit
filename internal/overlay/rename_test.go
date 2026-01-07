package overlay

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/config"
)

// setupRenameTestOverlay creates a temporary overlay structure for rename testing.
func setupRenameTestOverlay(t *testing.T) string {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "rename-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}

	// Create required overlay structure
	dirs := []string{"profiles", "metadata"}
	for _, dir := range dirs {
		if err := os.MkdirAll(filepath.Join(tmpDir, dir), 0755); err != nil {
			os.RemoveAll(tmpDir)
			t.Fatalf("failed to create dir %s: %v", dir, err)
		}
	}

	return tmpDir
}

// createRenameTestEbuild creates a test ebuild file.
func createRenameTestEbuild(t *testing.T, overlayPath, category, pkg, version string) {
	t.Helper()

	pkgDir := filepath.Join(overlayPath, category, pkg)
	if err := os.MkdirAll(pkgDir, 0755); err != nil {
		t.Fatalf("failed to create package dir: %v", err)
	}

	filename := pkg + "-" + version + ".ebuild"
	ebuildPath := filepath.Join(pkgDir, filename)

	if err := os.WriteFile(ebuildPath, []byte("# test ebuild\n"), 0644); err != nil {
		t.Fatalf("failed to create ebuild: %v", err)
	}
}

// TestRenamePreview tests the RenamePreview function.
func TestRenamePreview(t *testing.T) {
	overlayPath := setupRenameTestOverlay(t)
	defer os.RemoveAll(overlayPath)

	// Create test ebuilds
	createRenameTestEbuild(t, overlayPath, "media-plugins", "gst-plugins-base", "1.24.11")
	createRenameTestEbuild(t, overlayPath, "media-plugins", "gst-plugins-good", "1.24.11")

	cfg := &config.Config{
		Overlay: config.OverlayConfig{Path: overlayPath},
	}

	spec := &RenameSpec{
		Category:       "media-plugins",
		PackagePattern: "gst-*",
		OldVersion:     "1.24.11",
		NewVersion:     "1.26.10",
	}

	result, err := RenamePreview(cfg, spec)
	if err != nil {
		t.Fatalf("RenamePreview() error = %v", err)
	}

	if len(result.Matches) != 2 {
		t.Errorf("RenamePreview() got %d matches, want 2", len(result.Matches))
	}
}

// TestRenamePreviewNoMatches tests RenamePreview with no matching ebuilds.
func TestRenamePreviewNoMatches(t *testing.T) {
	overlayPath := setupRenameTestOverlay(t)
	defer os.RemoveAll(overlayPath)

	createRenameTestEbuild(t, overlayPath, "app-misc", "hello", "1.0.0")

	cfg := &config.Config{
		Overlay: config.OverlayConfig{Path: overlayPath},
	}

	spec := &RenameSpec{
		Category:       "app-misc",
		PackagePattern: "hello",
		OldVersion:     "2.0.0", // Different version
		NewVersion:     "3.0.0",
	}

	result, err := RenamePreview(cfg, spec)
	if err != nil {
		t.Fatalf("RenamePreview() error = %v", err)
	}

	if len(result.Matches) != 0 {
		t.Errorf("RenamePreview() got %d matches, want 0", len(result.Matches))
	}
}

// TestRenamePreviewWithConflicts tests RenamePreview detecting conflicts.
func TestRenamePreviewWithConflicts(t *testing.T) {
	overlayPath := setupRenameTestOverlay(t)
	defer os.RemoveAll(overlayPath)

	// Create old and new version (conflict)
	createRenameTestEbuild(t, overlayPath, "app-misc", "hello", "1.0.0")
	createRenameTestEbuild(t, overlayPath, "app-misc", "hello", "2.0.0")

	cfg := &config.Config{
		Overlay: config.OverlayConfig{Path: overlayPath},
	}

	spec := &RenameSpec{
		Category:       "app-misc",
		PackagePattern: "hello",
		OldVersion:     "1.0.0",
		NewVersion:     "2.0.0", // Already exists
	}

	result, err := RenamePreview(cfg, spec)
	if err != nil {
		t.Fatalf("RenamePreview() error = %v", err)
	}

	if len(result.Conflicts) != 1 {
		t.Errorf("RenamePreview() got %d conflicts, want 1", len(result.Conflicts))
	}
}

// TestRenamePreviewWithVersionFiles tests RenamePreview detecting version files.
func TestRenamePreviewWithVersionFiles(t *testing.T) {
	overlayPath := setupRenameTestOverlay(t)
	defer os.RemoveAll(overlayPath)

	createRenameTestEbuild(t, overlayPath, "app-misc", "hello", "1.0.0")

	// Create version-specific file
	filesDir := filepath.Join(overlayPath, "app-misc", "hello", "files")
	os.MkdirAll(filesDir, 0755)
	os.WriteFile(filepath.Join(filesDir, "hello-1.0.0-fix.patch"), []byte("# patch\n"), 0644)

	cfg := &config.Config{
		Overlay: config.OverlayConfig{Path: overlayPath},
	}

	spec := &RenameSpec{
		Category:       "app-misc",
		PackagePattern: "hello",
		OldVersion:     "1.0.0",
		NewVersion:     "2.0.0",
	}

	result, err := RenamePreview(cfg, spec)
	if err != nil {
		t.Fatalf("RenamePreview() error = %v", err)
	}

	if len(result.VersionFiles) != 1 {
		t.Errorf("RenamePreview() got %d version files, want 1", len(result.VersionFiles))
	}
}

// TestRenamePreviewInvalidPattern tests RenamePreview with invalid pattern.
func TestRenamePreviewInvalidPattern(t *testing.T) {
	overlayPath := setupRenameTestOverlay(t)
	defer os.RemoveAll(overlayPath)

	cfg := &config.Config{
		Overlay: config.OverlayConfig{Path: overlayPath},
	}

	spec := &RenameSpec{
		Category:       "app-misc",
		PackagePattern: "*", // Invalid - too broad
		OldVersion:     "1.0.0",
		NewVersion:     "2.0.0",
	}

	_, err := RenamePreview(cfg, spec)
	if err == nil {
		t.Error("RenamePreview() expected error for invalid pattern")
	}
}

// TestRenamePreviewNoOverlayPath tests RenamePreview with missing overlay path.
func TestRenamePreviewNoOverlayPath(t *testing.T) {
	cfg := &config.Config{
		Overlay: config.OverlayConfig{Path: ""},
	}

	spec := &RenameSpec{
		Category:       "app-misc",
		PackagePattern: "hello",
		OldVersion:     "1.0.0",
		NewVersion:     "2.0.0",
	}

	_, err := RenamePreview(cfg, spec)
	if err != ErrOverlayPathNotSet {
		t.Errorf("RenamePreview() error = %v, want ErrOverlayPathNotSet", err)
	}
}

// TestFormatRenamePreview tests the FormatRenamePreview function.
func TestFormatRenamePreview(t *testing.T) {
	result := &RenameResult{
		Matches: []RenameMatch{
			{
				Category:    "media-plugins",
				Package:     "gst-plugins-base",
				OldFilename: "gst-plugins-base-1.24.11.ebuild",
				NewFilename: "gst-plugins-base-1.26.10.ebuild",
				HasRevision: false,
			},
		},
	}

	output := FormatRenamePreview(result, false)

	if output == "" {
		t.Error("FormatRenamePreview() returned empty string")
	}

	// Check contains expected content
	if !containsString(output, "1 ebuild") {
		t.Error("FormatRenamePreview() should mention ebuild count")
	}
	if !containsString(output, "gst-plugins-base") {
		t.Error("FormatRenamePreview() should mention package name")
	}
}

// TestFormatRenamePreviewGlobalSearch tests FormatRenamePreview with global search.
func TestFormatRenamePreviewGlobalSearch(t *testing.T) {
	result := &RenameResult{
		Matches: []RenameMatch{
			{Category: "app-misc", Package: "hello", OldFilename: "hello-1.0.0.ebuild", NewFilename: "hello-2.0.0.ebuild"},
		},
	}

	output := FormatRenamePreview(result, true)

	if !containsString(output, "Global search") {
		t.Error("FormatRenamePreview() should warn about global search")
	}
}

// TestFormatRenamePreviewWithWarnings tests FormatRenamePreview with warnings.
func TestFormatRenamePreviewWithWarnings(t *testing.T) {
	result := &RenameResult{
		Matches: []RenameMatch{
			{Category: "app-misc", Package: "hello", OldFilename: "hello-1.0.0.ebuild", NewFilename: "hello-2.0.0.ebuild"},
		},
		VersionFiles: []VersionFile{
			{Category: "app-misc", Package: "hello", Filename: "hello-1.0.0-fix.patch"},
		},
		Conflicts: []Conflict{
			{Existing: "/path/to/hello-2.0.0.ebuild"},
		},
	}

	output := FormatRenamePreview(result, false)

	if !containsString(output, "Warning") {
		t.Error("FormatRenamePreview() should show warnings")
	}
}

// TestRename tests the Rename function.
func TestRename(t *testing.T) {
	overlayPath := setupRenameTestOverlay(t)
	defer os.RemoveAll(overlayPath)

	createRenameTestEbuild(t, overlayPath, "app-misc", "hello", "1.0.0")

	cfg := &config.Config{
		Overlay: config.OverlayConfig{Path: overlayPath},
	}

	spec := &RenameSpec{
		Category:       "app-misc",
		PackagePattern: "hello",
		OldVersion:     "1.0.0",
		NewVersion:     "2.0.0",
	}

	opts := &RenameOptions{
		DryRun:     false,
		SkipPrompt: true,
		NoManifest: true, // Skip manifest for test
		Force:      false,
	}

	result, err := Rename(cfg, spec, opts)
	if err != nil {
		t.Fatalf("Rename() error = %v", err)
	}

	if len(result.Renamed) != 1 {
		t.Errorf("Rename() got %d renamed, want 1", len(result.Renamed))
	}

	// Verify file was renamed
	oldPath := filepath.Join(overlayPath, "app-misc", "hello", "hello-1.0.0.ebuild")
	newPath := filepath.Join(overlayPath, "app-misc", "hello", "hello-2.0.0.ebuild")

	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Error("Old file should not exist after rename")
	}
	if _, err := os.Stat(newPath); os.IsNotExist(err) {
		t.Error("New file should exist after rename")
	}
}

// TestRenameDryRun tests Rename in dry-run mode.
func TestRenameDryRun(t *testing.T) {
	overlayPath := setupRenameTestOverlay(t)
	defer os.RemoveAll(overlayPath)

	createRenameTestEbuild(t, overlayPath, "app-misc", "hello", "1.0.0")

	cfg := &config.Config{
		Overlay: config.OverlayConfig{Path: overlayPath},
	}

	spec := &RenameSpec{
		Category:       "app-misc",
		PackagePattern: "hello",
		OldVersion:     "1.0.0",
		NewVersion:     "2.0.0",
	}

	opts := &RenameOptions{
		DryRun:     true,
		NoManifest: true,
	}

	result, err := Rename(cfg, spec, opts)
	if err != nil {
		t.Fatalf("Rename() error = %v", err)
	}

	// In dry-run, matches should be found but not renamed
	if len(result.Matches) != 1 {
		t.Errorf("Rename() got %d matches, want 1", len(result.Matches))
	}
	if len(result.Renamed) != 0 {
		t.Errorf("Rename() dry-run should not rename files, got %d", len(result.Renamed))
	}

	// Verify file was NOT renamed
	oldPath := filepath.Join(overlayPath, "app-misc", "hello", "hello-1.0.0.ebuild")
	if _, err := os.Stat(oldPath); os.IsNotExist(err) {
		t.Error("Old file should still exist in dry-run mode")
	}
}

// TestRenameWithVersionFilesBlocking tests Rename blocked by version files.
func TestRenameWithVersionFilesBlocking(t *testing.T) {
	overlayPath := setupRenameTestOverlay(t)
	defer os.RemoveAll(overlayPath)

	createRenameTestEbuild(t, overlayPath, "app-misc", "hello", "1.0.0")

	// Create version-specific file
	filesDir := filepath.Join(overlayPath, "app-misc", "hello", "files")
	os.MkdirAll(filesDir, 0755)
	os.WriteFile(filepath.Join(filesDir, "hello-1.0.0-fix.patch"), []byte("# patch\n"), 0644)

	cfg := &config.Config{
		Overlay: config.OverlayConfig{Path: overlayPath},
	}

	spec := &RenameSpec{
		Category:       "app-misc",
		PackagePattern: "hello",
		OldVersion:     "1.0.0",
		NewVersion:     "2.0.0",
	}

	opts := &RenameOptions{
		Force:      false, // Don't force
		NoManifest: true,
	}

	_, err := Rename(cfg, spec, opts)
	if err == nil {
		t.Error("Rename() should return error when version files detected without --force")
	}

	// Should be VersionFilesBlockError
	if _, ok := err.(*VersionFilesBlockError); !ok {
		t.Errorf("Rename() error type = %T, want *VersionFilesBlockError", err)
	}
}

// TestRenameWithVersionFilesForce tests Rename with --force bypassing version files.
func TestRenameWithVersionFilesForce(t *testing.T) {
	overlayPath := setupRenameTestOverlay(t)
	defer os.RemoveAll(overlayPath)

	createRenameTestEbuild(t, overlayPath, "app-misc", "hello", "1.0.0")

	// Create version-specific file
	filesDir := filepath.Join(overlayPath, "app-misc", "hello", "files")
	os.MkdirAll(filesDir, 0755)
	os.WriteFile(filepath.Join(filesDir, "hello-1.0.0-fix.patch"), []byte("# patch\n"), 0644)

	cfg := &config.Config{
		Overlay: config.OverlayConfig{Path: overlayPath},
	}

	spec := &RenameSpec{
		Category:       "app-misc",
		PackagePattern: "hello",
		OldVersion:     "1.0.0",
		NewVersion:     "2.0.0",
	}

	opts := &RenameOptions{
		Force:      true, // Force
		NoManifest: true,
	}

	result, err := Rename(cfg, spec, opts)
	if err != nil {
		t.Fatalf("Rename() with --force error = %v", err)
	}

	if len(result.Renamed) != 1 {
		t.Errorf("Rename() with --force got %d renamed, want 1", len(result.Renamed))
	}
}

// TestRenameWithConflictBlocking tests Rename blocked by conflicts.
func TestRenameWithConflictBlocking(t *testing.T) {
	overlayPath := setupRenameTestOverlay(t)
	defer os.RemoveAll(overlayPath)

	// Create both old and new version (conflict)
	createRenameTestEbuild(t, overlayPath, "app-misc", "hello", "1.0.0")
	createRenameTestEbuild(t, overlayPath, "app-misc", "hello", "2.0.0")

	cfg := &config.Config{
		Overlay: config.OverlayConfig{Path: overlayPath},
	}

	spec := &RenameSpec{
		Category:       "app-misc",
		PackagePattern: "hello",
		OldVersion:     "1.0.0",
		NewVersion:     "2.0.0",
	}

	opts := &RenameOptions{
		Force:      false,
		NoManifest: true,
	}

	_, err := Rename(cfg, spec, opts)
	if err == nil {
		t.Error("Rename() should return error when conflict detected without --force")
	}

	// Should be ConflictError
	if _, ok := err.(*ConflictError); !ok {
		t.Errorf("Rename() error type = %T, want *ConflictError", err)
	}
}

// TestRenameWithConflictForce tests Rename with --force overwriting conflicts.
func TestRenameWithConflictForce(t *testing.T) {
	overlayPath := setupRenameTestOverlay(t)
	defer os.RemoveAll(overlayPath)

	// Create both old and new version (conflict)
	createRenameTestEbuild(t, overlayPath, "app-misc", "hello", "1.0.0")
	createRenameTestEbuild(t, overlayPath, "app-misc", "hello", "2.0.0")

	cfg := &config.Config{
		Overlay: config.OverlayConfig{Path: overlayPath},
	}

	spec := &RenameSpec{
		Category:       "app-misc",
		PackagePattern: "hello",
		OldVersion:     "1.0.0",
		NewVersion:     "2.0.0",
	}

	opts := &RenameOptions{
		Force:      true,
		NoManifest: true,
	}

	result, err := Rename(cfg, spec, opts)
	if err != nil {
		t.Fatalf("Rename() with --force error = %v", err)
	}

	if len(result.Renamed) != 1 {
		t.Errorf("Rename() with --force got %d renamed, want 1", len(result.Renamed))
	}
}

// TestFormatRenameResult tests the FormatRenameResult function.
func TestFormatRenameResult(t *testing.T) {
	tests := []struct {
		name     string
		result   *RenameResult
		dryRun   bool
		contains []string
	}{
		{
			name:     "no matches",
			result:   &RenameResult{},
			dryRun:   false,
			contains: []string{"No matching ebuilds"},
		},
		{
			name: "dry run with matches",
			result: &RenameResult{
				Matches: []RenameMatch{
					{Category: "app-misc", Package: "hello", OldFilename: "hello-1.0.0.ebuild", NewFilename: "hello-2.0.0.ebuild"},
				},
			},
			dryRun:   true,
			contains: []string{"Dry run", "1 ebuild"},
		},
		{
			name: "successful rename",
			result: &RenameResult{
				Matches: []RenameMatch{
					{Category: "app-misc", Package: "hello", OldFilename: "hello-1.0.0.ebuild", NewFilename: "hello-2.0.0.ebuild"},
				},
				Renamed: []RenameMatch{
					{Category: "app-misc", Package: "hello", OldFilename: "hello-1.0.0.ebuild", NewFilename: "hello-2.0.0.ebuild"},
				},
			},
			dryRun:   false,
			contains: []string{"Renamed 1 ebuild"},
		},
		{
			name: "with failures",
			result: &RenameResult{
				Matches: []RenameMatch{
					{Category: "app-misc", Package: "hello", OldFilename: "hello-1.0.0.ebuild", NewFilename: "hello-2.0.0.ebuild"},
				},
				Failed: []RenameError{
					{Match: RenameMatch{Category: "app-misc", Package: "hello"}, Message: "permission denied"},
				},
			},
			dryRun:   false,
			contains: []string{"Failed"},
		},
		{
			name: "with manifest updates",
			result: &RenameResult{
				Matches: []RenameMatch{
					{Category: "app-misc", Package: "hello", OldFilename: "hello-1.0.0.ebuild", NewFilename: "hello-2.0.0.ebuild"},
				},
				Renamed: []RenameMatch{
					{Category: "app-misc", Package: "hello", OldFilename: "hello-1.0.0.ebuild", NewFilename: "hello-2.0.0.ebuild"},
				},
				ManifestUpdates: []ManifestUpdate{
					{Category: "app-misc", Package: "hello", Success: true},
				},
			},
			dryRun:   false,
			contains: []string{"Manifest updated"},
		},
		{
			name: "with version files warning",
			result: &RenameResult{
				Matches: []RenameMatch{
					{Category: "app-misc", Package: "hello", OldFilename: "hello-1.0.0.ebuild", NewFilename: "hello-2.0.0.ebuild"},
				},
				Renamed: []RenameMatch{
					{Category: "app-misc", Package: "hello", OldFilename: "hello-1.0.0.ebuild", NewFilename: "hello-2.0.0.ebuild"},
				},
				VersionFiles: []VersionFile{
					{Category: "app-misc", Package: "hello", Filename: "hello-1.0.0-fix.patch"},
				},
			},
			dryRun:   false,
			contains: []string{"Warning", "version-specific"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output := FormatRenameResult(tt.result, tt.dryRun)

			for _, expected := range tt.contains {
				if !containsString(output, expected) {
					t.Errorf("FormatRenameResult() output should contain %q, got:\n%s", expected, output)
				}
			}
		})
	}
}

// TestVersionFilesBlockErrorMessage tests the error message format.
func TestVersionFilesBlockErrorMessage(t *testing.T) {
	err := &VersionFilesBlockError{
		Files: []VersionFile{
			{Category: "app-misc", Package: "hello", Filename: "hello-1.0.0-fix.patch"},
		},
	}

	msg := err.Error()

	if !containsString(msg, "version-specific files detected") {
		t.Error("Error message should mention version-specific files")
	}
	if !containsString(msg, "--force") {
		t.Error("Error message should mention --force flag")
	}
}

// TestConflictErrorMessage tests the error message format.
func TestConflictErrorMessage(t *testing.T) {
	err := &ConflictError{
		Conflicts: []Conflict{
			{Existing: "/path/to/file.ebuild"},
		},
	}

	msg := err.Error()

	if !containsString(msg, "target files already exist") {
		t.Error("Error message should mention target files exist")
	}
	if !containsString(msg, "--force") {
		t.Error("Error message should mention --force flag")
	}
}

// containsString checks if s contains substr.
func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && searchSubstring(s, substr)))
}

func searchSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// TestRenameNoMatches tests Rename with no matching ebuilds.
func TestRenameNoMatches(t *testing.T) {
	overlayPath := setupRenameTestOverlay(t)
	defer os.RemoveAll(overlayPath)

	createRenameTestEbuild(t, overlayPath, "app-misc", "hello", "1.0.0")

	cfg := &config.Config{
		Overlay: config.OverlayConfig{Path: overlayPath},
	}

	spec := &RenameSpec{
		Category:       "app-misc",
		PackagePattern: "hello",
		OldVersion:     "9.9.9", // Non-existent version
		NewVersion:     "10.0.0",
	}

	opts := &RenameOptions{
		NoManifest: true,
	}

	result, err := Rename(cfg, spec, opts)
	if err != nil {
		t.Fatalf("Rename() error = %v", err)
	}

	if len(result.Matches) != 0 {
		t.Errorf("Rename() got %d matches, want 0", len(result.Matches))
	}
}

// TestRenameMultipleEbuilds tests Rename with multiple matching ebuilds.
func TestRenameMultipleEbuilds(t *testing.T) {
	overlayPath := setupRenameTestOverlay(t)
	defer os.RemoveAll(overlayPath)

	createRenameTestEbuild(t, overlayPath, "media-plugins", "gst-plugins-base", "1.24.11")
	createRenameTestEbuild(t, overlayPath, "media-plugins", "gst-plugins-good", "1.24.11")
	createRenameTestEbuild(t, overlayPath, "media-plugins", "gst-plugins-ugly", "1.24.11")

	cfg := &config.Config{
		Overlay: config.OverlayConfig{Path: overlayPath},
	}

	spec := &RenameSpec{
		Category:       "media-plugins",
		PackagePattern: "gst-*",
		OldVersion:     "1.24.11",
		NewVersion:     "1.26.10",
	}

	opts := &RenameOptions{
		NoManifest: true,
	}

	result, err := Rename(cfg, spec, opts)
	if err != nil {
		t.Fatalf("Rename() error = %v", err)
	}

	if len(result.Renamed) != 3 {
		t.Errorf("Rename() got %d renamed, want 3", len(result.Renamed))
	}
}

// TestFormatRenameResultWithRevision tests FormatRenameResult with revision stripping.
func TestFormatRenameResultWithRevision(t *testing.T) {
	result := &RenameResult{
		Matches: []RenameMatch{
			{
				Category:    "app-misc",
				Package:     "hello",
				OldFilename: "hello-1.0.0-r1.ebuild",
				NewFilename: "hello-2.0.0.ebuild",
				HasRevision: true,
			},
		},
	}

	output := FormatRenameResult(result, true)

	if !containsString(output, "revision suffix will be stripped") {
		t.Error("FormatRenameResult() should mention revision stripping")
	}
}

// TestFormatRenameResultWithManifestFailure tests FormatRenameResult with manifest failures.
func TestFormatRenameResultWithManifestFailure(t *testing.T) {
	result := &RenameResult{
		Matches: []RenameMatch{
			{Category: "app-misc", Package: "hello", OldFilename: "hello-1.0.0.ebuild", NewFilename: "hello-2.0.0.ebuild"},
		},
		Renamed: []RenameMatch{
			{Category: "app-misc", Package: "hello", OldFilename: "hello-1.0.0.ebuild", NewFilename: "hello-2.0.0.ebuild"},
		},
		ManifestUpdates: []ManifestUpdate{
			{Category: "app-misc", Package: "hello", Success: false, Error: "pkgdev not found"},
		},
	}

	output := FormatRenameResult(result, false)

	if !containsString(output, "Manifest update failed") {
		t.Error("FormatRenameResult() should mention manifest failure")
	}
}

// TestFormatRenameResultWithConflicts tests FormatRenameResult with conflicts.
func TestFormatRenameResultWithConflicts(t *testing.T) {
	result := &RenameResult{
		Matches: []RenameMatch{
			{Category: "app-misc", Package: "hello", OldFilename: "hello-1.0.0.ebuild", NewFilename: "hello-2.0.0.ebuild"},
		},
		Conflicts: []Conflict{
			{Existing: "/path/to/hello-2.0.0.ebuild"},
		},
	}

	output := FormatRenameResult(result, false)

	if !containsString(output, "Conflicts") {
		t.Error("FormatRenameResult() should mention conflicts")
	}
}

// TestRenameGlobalSearch tests Rename with global search across categories.
func TestRenameGlobalSearch(t *testing.T) {
	overlayPath := setupRenameTestOverlay(t)
	defer os.RemoveAll(overlayPath)

	createRenameTestEbuild(t, overlayPath, "media-plugins", "gst-plugins-base", "1.24.11")
	createRenameTestEbuild(t, overlayPath, "dev-libs", "gst-core", "1.24.11")

	cfg := &config.Config{
		Overlay: config.OverlayConfig{Path: overlayPath},
	}

	spec := &RenameSpec{
		Category:       "*", // Global search
		PackagePattern: "gst-*",
		OldVersion:     "1.24.11",
		NewVersion:     "1.26.10",
	}

	opts := &RenameOptions{
		NoManifest: true,
	}

	result, err := Rename(cfg, spec, opts)
	if err != nil {
		t.Fatalf("Rename() error = %v", err)
	}

	if len(result.Renamed) != 2 {
		t.Errorf("Rename() global search got %d renamed, want 2", len(result.Renamed))
	}
}

// TestRenameWithRevision tests Rename with revision suffix stripping.
func TestRenameWithRevision(t *testing.T) {
	overlayPath := setupRenameTestOverlay(t)
	defer os.RemoveAll(overlayPath)

	// Create ebuild with revision
	pkgDir := filepath.Join(overlayPath, "app-misc", "hello")
	os.MkdirAll(pkgDir, 0755)
	os.WriteFile(filepath.Join(pkgDir, "hello-1.0.0-r2.ebuild"), []byte("# test\n"), 0644)

	cfg := &config.Config{
		Overlay: config.OverlayConfig{Path: overlayPath},
	}

	spec := &RenameSpec{
		Category:       "app-misc",
		PackagePattern: "hello",
		OldVersion:     "1.0.0",
		NewVersion:     "2.0.0",
	}

	opts := &RenameOptions{
		NoManifest: true,
	}

	result, err := Rename(cfg, spec, opts)
	if err != nil {
		t.Fatalf("Rename() error = %v", err)
	}

	if len(result.Renamed) != 1 {
		t.Fatalf("Rename() got %d renamed, want 1", len(result.Renamed))
	}

	// Verify new filename has no revision
	if result.Renamed[0].NewFilename != "hello-2.0.0.ebuild" {
		t.Errorf("NewFilename = %q, want %q", result.Renamed[0].NewFilename, "hello-2.0.0.ebuild")
	}

	// Verify HasRevision flag
	if !result.Renamed[0].HasRevision {
		t.Error("HasRevision should be true")
	}
}

// TestRenameInvalidOverlayPath tests Rename with invalid overlay path.
func TestRenameInvalidOverlayPath(t *testing.T) {
	cfg := &config.Config{
		Overlay: config.OverlayConfig{Path: ""},
	}

	spec := &RenameSpec{
		Category:       "app-misc",
		PackagePattern: "hello",
		OldVersion:     "1.0.0",
		NewVersion:     "2.0.0",
	}

	opts := &RenameOptions{NoManifest: true}

	_, err := Rename(cfg, spec, opts)
	if err != ErrOverlayPathNotSet {
		t.Errorf("Rename() error = %v, want ErrOverlayPathNotSet", err)
	}
}

// TestRenameInvalidPattern tests Rename with invalid pattern.
func TestRenameInvalidPattern(t *testing.T) {
	overlayPath := setupRenameTestOverlay(t)
	defer os.RemoveAll(overlayPath)

	cfg := &config.Config{
		Overlay: config.OverlayConfig{Path: overlayPath},
	}

	spec := &RenameSpec{
		Category:       "app-misc",
		PackagePattern: "*", // Invalid
		OldVersion:     "1.0.0",
		NewVersion:     "2.0.0",
	}

	opts := &RenameOptions{NoManifest: true}

	_, err := Rename(cfg, spec, opts)
	if err == nil {
		t.Error("Rename() expected error for invalid pattern")
	}
}

// TestCategoryNotFoundErrorMethod tests CategoryNotFoundError.Error() method.
func TestCategoryNotFoundErrorMethod(t *testing.T) {
	err := &CategoryNotFoundError{Category: "nonexistent-cat"}
	msg := err.Error()

	if !containsString(msg, "nonexistent-cat") {
		t.Error("Error message should contain category name")
	}
}

// TestValidationErrorMethod tests ValidationError.Error() method.
func TestValidationErrorMethod(t *testing.T) {
	err := &ValidationError{Pattern: "g*", Reason: "too short"}
	msg := err.Error()

	if !containsString(msg, "g*") {
		t.Error("Error message should contain pattern")
	}
	if !containsString(msg, "too short") {
		t.Error("Error message should contain reason")
	}
}

// TestFormatRenamePreviewWithRevision tests FormatRenamePreview with revision.
func TestFormatRenamePreviewWithRevision(t *testing.T) {
	result := &RenameResult{
		Matches: []RenameMatch{
			{
				Category:    "app-misc",
				Package:     "hello",
				OldFilename: "hello-1.0.0-r1.ebuild",
				NewFilename: "hello-2.0.0.ebuild",
				HasRevision: true,
			},
		},
	}

	output := FormatRenamePreview(result, false)

	if !containsString(output, "revision suffix will be stripped") {
		t.Error("FormatRenamePreview() should mention revision stripping")
	}
}

// TestRenamePreviewGlobalSearch tests RenamePreview with global search.
func TestRenamePreviewGlobalSearch(t *testing.T) {
	overlayPath := setupRenameTestOverlay(t)
	defer os.RemoveAll(overlayPath)

	createRenameTestEbuild(t, overlayPath, "media-plugins", "gst-base", "1.0.0")
	createRenameTestEbuild(t, overlayPath, "dev-libs", "gst-core", "1.0.0")

	cfg := &config.Config{
		Overlay: config.OverlayConfig{Path: overlayPath},
	}

	spec := &RenameSpec{
		Category:       "*",
		PackagePattern: "gst-*",
		OldVersion:     "1.0.0",
		NewVersion:     "2.0.0",
	}

	result, err := RenamePreview(cfg, spec)
	if err != nil {
		t.Fatalf("RenamePreview() error = %v", err)
	}

	if len(result.Matches) != 2 {
		t.Errorf("RenamePreview() got %d matches, want 2", len(result.Matches))
	}
}

// TestPatternValidatorEdgeCases tests edge cases in pattern validation.
func TestPatternValidatorEdgeCases(t *testing.T) {
	validator := NewPatternValidator()

	tests := []struct {
		name    string
		pattern string
		wantErr bool
	}{
		{"pattern with question mark", "abc?", false},
		{"pattern with bracket", "abc[0-9]", false},
		{"two char prefix with delimiter", "ab-*", false},
		{"underscore delimiter", "abc_*", false},
		{"long prefix", "verylongprefix*", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validator.Validate(tt.pattern)
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate(%q) error = %v, wantErr %v", tt.pattern, err, tt.wantErr)
			}
		})
	}
}

// TestMatcherEdgeCases tests edge cases in ebuild matching.
func TestMatcherEdgeCases(t *testing.T) {
	overlayPath := setupRenameTestOverlay(t)
	defer os.RemoveAll(overlayPath)

	// Create package with hidden directory (should be skipped)
	hiddenDir := filepath.Join(overlayPath, "app-misc", ".hidden")
	os.MkdirAll(hiddenDir, 0755)

	// Create valid package
	createRenameTestEbuild(t, overlayPath, "app-misc", "hello", "1.0.0")

	matcher := NewEbuildMatcher(overlayPath)
	spec := &RenameSpec{
		Category:       "app-misc",
		PackagePattern: "*", // Would match hidden too if not filtered
		OldVersion:     "1.0.0",
		NewVersion:     "2.0.0",
	}

	// This should work because we're testing the matcher directly
	// Pattern validation is separate
	matches, err := matcher.Match(spec)
	if err != nil {
		t.Fatalf("Match() error = %v", err)
	}

	// Should only find "hello", not ".hidden"
	for _, m := range matches {
		if m.Package == ".hidden" {
			t.Error("Match() should skip hidden directories")
		}
	}
}

// TestMatcherWithNonEbuildFiles tests that non-ebuild files are skipped.
func TestMatcherWithNonEbuildFiles(t *testing.T) {
	overlayPath := setupRenameTestOverlay(t)
	defer os.RemoveAll(overlayPath)

	// Create package directory with various files
	pkgDir := filepath.Join(overlayPath, "app-misc", "hello")
	os.MkdirAll(pkgDir, 0755)

	// Create ebuild
	os.WriteFile(filepath.Join(pkgDir, "hello-1.0.0.ebuild"), []byte("# ebuild\n"), 0644)
	// Create non-ebuild files
	os.WriteFile(filepath.Join(pkgDir, "Manifest"), []byte("# manifest\n"), 0644)
	os.WriteFile(filepath.Join(pkgDir, "metadata.xml"), []byte("<xml/>\n"), 0644)

	matcher := NewEbuildMatcher(overlayPath)
	spec := &RenameSpec{
		Category:       "app-misc",
		PackagePattern: "hello",
		OldVersion:     "1.0.0",
		NewVersion:     "2.0.0",
	}

	matches, err := matcher.Match(spec)
	if err != nil {
		t.Fatalf("Match() error = %v", err)
	}

	// Should only find the ebuild
	if len(matches) != 1 {
		t.Errorf("Match() got %d matches, want 1", len(matches))
	}
}

// TestMatcherWithSubdirectories tests that subdirectories in package are skipped.
func TestMatcherWithSubdirectories(t *testing.T) {
	overlayPath := setupRenameTestOverlay(t)
	defer os.RemoveAll(overlayPath)

	// Create package with files/ subdirectory
	pkgDir := filepath.Join(overlayPath, "app-misc", "hello")
	filesDir := filepath.Join(pkgDir, "files")
	os.MkdirAll(filesDir, 0755)

	// Create ebuild
	os.WriteFile(filepath.Join(pkgDir, "hello-1.0.0.ebuild"), []byte("# ebuild\n"), 0644)
	// Create file in files/ directory
	os.WriteFile(filepath.Join(filesDir, "patch.patch"), []byte("# patch\n"), 0644)

	matcher := NewEbuildMatcher(overlayPath)
	spec := &RenameSpec{
		Category:       "app-misc",
		PackagePattern: "hello",
		OldVersion:     "1.0.0",
		NewVersion:     "2.0.0",
	}

	matches, err := matcher.Match(spec)
	if err != nil {
		t.Fatalf("Match() error = %v", err)
	}

	// Should only find the ebuild, not files in subdirectories
	if len(matches) != 1 {
		t.Errorf("Match() got %d matches, want 1", len(matches))
	}
}

// TestVersionFilesDetectorWithSubdirectories tests that subdirectories in files/ are skipped.
func TestVersionFilesDetectorWithSubdirectories(t *testing.T) {
	overlayPath := setupRenameTestOverlay(t)
	defer os.RemoveAll(overlayPath)

	// Create package with files/ containing a subdirectory
	filesDir := filepath.Join(overlayPath, "app-misc", "hello", "files")
	subDir := filepath.Join(filesDir, "subdir")
	os.MkdirAll(subDir, 0755)

	// Create version file
	os.WriteFile(filepath.Join(filesDir, "hello-1.0.0-fix.patch"), []byte("# patch\n"), 0644)
	// Create file in subdirectory (should be skipped)
	os.WriteFile(filepath.Join(subDir, "hello-1.0.0-other.patch"), []byte("# patch\n"), 0644)

	detector := NewVersionFilesDetector(overlayPath)
	matches := []RenameMatch{
		{Category: "app-misc", Package: "hello"},
	}

	versionFiles := detector.Detect(matches, "1.0.0")

	// Should only find the file in files/, not in subdirectory
	if len(versionFiles) != 1 {
		t.Errorf("Detect() got %d version files, want 1", len(versionFiles))
	}
}

// TestShouldBlockForVersionFilesEdgeCases tests edge cases.
func TestShouldBlockForVersionFilesEdgeCases(t *testing.T) {
	tests := []struct {
		name         string
		versionFiles []VersionFile
		force        bool
		wantBlock    bool
	}{
		{
			name:         "nil slice without force",
			versionFiles: nil,
			force:        false,
			wantBlock:    false,
		},
		{
			name:         "nil slice with force",
			versionFiles: nil,
			force:        true,
			wantBlock:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ShouldBlockForVersionFiles(tt.versionFiles, tt.force)
			if got != tt.wantBlock {
				t.Errorf("ShouldBlockForVersionFiles() = %v, want %v", got, tt.wantBlock)
			}
		})
	}
}

// TestRenameWithManifestUpdate tests Rename with manifest update enabled.
// This test will fail if pkgdev is not installed, which is expected.
func TestRenameWithManifestUpdate(t *testing.T) {
	overlayPath := setupRenameTestOverlay(t)
	defer os.RemoveAll(overlayPath)

	createRenameTestEbuild(t, overlayPath, "app-misc", "hello", "1.0.0")

	cfg := &config.Config{
		Overlay: config.OverlayConfig{Path: overlayPath},
	}

	spec := &RenameSpec{
		Category:       "app-misc",
		PackagePattern: "hello",
		OldVersion:     "1.0.0",
		NewVersion:     "2.0.0",
	}

	opts := &RenameOptions{
		NoManifest: false, // Enable manifest update
	}

	result, err := Rename(cfg, spec, opts)
	if err != nil {
		t.Fatalf("Rename() error = %v", err)
	}

	// Rename should succeed
	if len(result.Renamed) != 1 {
		t.Errorf("Rename() got %d renamed, want 1", len(result.Renamed))
	}

	// Manifest update may fail if pkgdev is not installed
	// This is expected behavior - we just verify the result structure
	if len(result.ManifestUpdates) != 1 {
		t.Errorf("Rename() got %d manifest updates, want 1", len(result.ManifestUpdates))
	}
}

// TestMatcherWithInvalidGlobPattern tests matcher with invalid glob pattern.
func TestMatcherWithInvalidGlobPattern(t *testing.T) {
	overlayPath := setupRenameTestOverlay(t)
	defer os.RemoveAll(overlayPath)

	createRenameTestEbuild(t, overlayPath, "app-misc", "hello", "1.0.0")

	matcher := NewEbuildMatcher(overlayPath)

	// Test with invalid glob pattern (unclosed bracket)
	spec := &RenameSpec{
		Category:       "app-misc",
		PackagePattern: "hello[",
		OldVersion:     "1.0.0",
		NewVersion:     "2.0.0",
	}

	matches, err := matcher.Match(spec)
	if err != nil {
		t.Fatalf("Match() error = %v", err)
	}

	// Invalid pattern should not match anything
	if len(matches) != 0 {
		t.Errorf("Match() with invalid glob got %d matches, want 0", len(matches))
	}
}

// TestMatcherEbuildVersionMismatch tests that ebuilds with wrong package name prefix are skipped.
func TestMatcherEbuildVersionMismatch(t *testing.T) {
	overlayPath := setupRenameTestOverlay(t)
	defer os.RemoveAll(overlayPath)

	// Create package directory
	pkgDir := filepath.Join(overlayPath, "app-misc", "hello")
	os.MkdirAll(pkgDir, 0755)

	// Create ebuild with wrong prefix (shouldn't happen in real overlay but tests the code path)
	os.WriteFile(filepath.Join(pkgDir, "world-1.0.0.ebuild"), []byte("# ebuild\n"), 0644)
	// Create correct ebuild
	os.WriteFile(filepath.Join(pkgDir, "hello-1.0.0.ebuild"), []byte("# ebuild\n"), 0644)

	matcher := NewEbuildMatcher(overlayPath)
	spec := &RenameSpec{
		Category:       "app-misc",
		PackagePattern: "hello",
		OldVersion:     "1.0.0",
		NewVersion:     "2.0.0",
	}

	matches, err := matcher.Match(spec)
	if err != nil {
		t.Fatalf("Match() error = %v", err)
	}

	// Should only match hello-1.0.0.ebuild
	if len(matches) != 1 {
		t.Errorf("Match() got %d matches, want 1", len(matches))
	}
}

// TestCountPrefixLengthNoWildcard tests countPrefixLength with no wildcard.
func TestCountPrefixLengthNoWildcard(t *testing.T) {
	// This tests the branch where no wildcard exists
	length := countPrefixLength("hello")
	if length != 5 {
		t.Errorf("countPrefixLength(\"hello\") = %d, want 5", length)
	}
}

// TestIsTokenCompleteNoWildcard tests isTokenComplete with no wildcard.
func TestIsTokenCompleteNoWildcard(t *testing.T) {
	// This tests the branch where no wildcard exists
	result := isTokenComplete("hello")
	if !result {
		t.Error("isTokenComplete(\"hello\") should return true")
	}
}
