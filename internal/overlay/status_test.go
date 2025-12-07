package overlay

import (
	"errors"
	"strings"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
	"github.com/obentoo/bentoo-tools/internal/common/git"
)

// genCategory generates valid category names (e.g., app-misc, sys-apps)
func genCategory() gopter.Gen {
	return gen.RegexMatch(`^[a-z]{2,8}-[a-z]{2,8}$`)
}

// genPackageName generates valid package names
func genPackageName() gopter.Gen {
	return gen.RegexMatch(`^[a-z][a-z0-9-]{1,15}$`)
}

// genVersion generates valid version strings
func genVersion() gopter.Gen {
	return gen.RegexMatch(`^[0-9]+\.[0-9]+(\.[0-9]+)?$`)
}

// genGitStatus generates valid git status codes
func genGitStatus() gopter.Gen {
	return gen.OneConstOf("A", "M", "D", "R", "??")
}

// genFileType generates different file types for testing
func genFileType() gopter.Gen {
	return gen.OneConstOf("ebuild", "manifest", "metadata", "files", "other")
}

// genStatusEntry generates a valid StatusEntry with proper ebuild path structure
func genStatusEntry() gopter.Gen {
	return gopter.CombineGens(
		genCategory(),
		genPackageName(),
		genVersion(),
		genGitStatus(),
		genFileType(),
	).Map(func(values []interface{}) git.StatusEntry {
		category := values[0].(string)
		pkg := values[1].(string)
		version := values[2].(string)
		status := values[3].(string)
		fileType := values[4].(string)

		var filePath string
		switch fileType {
		case "ebuild":
			filePath = category + "/" + pkg + "/" + pkg + "-" + version + ".ebuild"
		case "manifest":
			filePath = category + "/" + pkg + "/Manifest"
		case "metadata":
			filePath = category + "/" + pkg + "/metadata.xml"
		case "files":
			filePath = category + "/" + pkg + "/files/patch-" + version + ".patch"
		default:
			filePath = category + "/" + pkg + "/README.md"
		}

		return git.StatusEntry{
			Status:   status,
			FilePath: filePath,
		}
	})
}

// genStatusEntryList generates a list of StatusEntry objects
func genStatusEntryList() gopter.Gen {
	return gen.SliceOf(genStatusEntry())
}


// TestStatusFormattingCompleteness tests Property 5: Status formatting completeness
// **Feature: overlay-manager, Property 5: Status formatting completeness**
// **Validates: Requirements 3.2, 3.3, 3.4**
func TestStatusFormattingCompleteness(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("All packages appear in formatted output", prop.ForAll(
		func(entries []git.StatusEntry) bool {
			if len(entries) == 0 {
				// Empty input should return clean message
				statuses := GroupStatusEntries(entries)
				output := FormatStatus(statuses)
				return output == "No changes detected (working directory clean)"
			}

			// Group entries
			statuses := GroupStatusEntries(entries)
			output := FormatStatus(statuses)

			// Collect all unique category/package combinations from input
			expectedPackages := make(map[string]bool)
			for _, entry := range entries {
				parts := strings.Split(entry.FilePath, "/")
				if len(parts) >= 2 {
					key := parts[0] + "/" + parts[1]
					expectedPackages[key] = true
				}
			}

			// Verify all packages appear in output
			for pkg := range expectedPackages {
				if !strings.Contains(output, pkg) {
					t.Logf("Package %s not found in output:\n%s", pkg, output)
					return false
				}
			}

			return true
		},
		genStatusEntryList(),
	))

	properties.TestingRun(t)
}

// TestStatusLabelMapping tests that all git status codes map to human-readable labels
// _Requirements: 3.4_
func TestStatusLabelMapping(t *testing.T) {
	tests := []struct {
		code     string
		expected string
	}{
		{"A", "Added"},
		{"M", "Modified"},
		{"D", "Deleted"},
		{"R", "Renamed"},
		{"??", "Untracked"},
		{"AM", "Added"},
		{"MM", "Modified"},
		{"XX", "Unknown"}, // Unknown code
	}

	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			result := StatusLabel(tt.code)
			if result != tt.expected {
				t.Errorf("StatusLabel(%q) = %q, want %q", tt.code, result, tt.expected)
			}
		})
	}
}

// TestDetectFileType tests file type detection
// _Requirements: 3.3_
func TestDetectFileType(t *testing.T) {
	tests := []struct {
		path     string
		expected FileType
	}{
		{"app-misc/hello/hello-1.0.ebuild", FileTypeEbuild},
		{"app-misc/hello/Manifest", FileTypeManifest},
		{"app-misc/hello/metadata.xml", FileTypeMetadata},
		{"app-misc/hello/files/patch.patch", FileTypeFiles},
		{"app-misc/hello/README.md", FileTypeOther},
		{"sys-apps/world/world-2.0_rc1.ebuild", FileTypeEbuild},
		{"dev-libs/foo/files/foo-1.0-fix.patch", FileTypeFiles},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			result := DetectFileType(tt.path)
			if result != tt.expected {
				t.Errorf("DetectFileType(%q) = %q, want %q", tt.path, result, tt.expected)
			}
		})
	}
}

// TestEmptyStatusReturnsCleanMessage tests that empty status returns clean message
// _Requirements: 3.5_
func TestEmptyStatusReturnsCleanMessage(t *testing.T) {
	statuses := GroupStatusEntries([]git.StatusEntry{})
	output := FormatStatus(statuses)

	expected := "No changes detected (working directory clean)"
	if output != expected {
		t.Errorf("FormatStatus([]) = %q, want %q", output, expected)
	}
}

// TestGroupStatusEntriesByPackage tests that entries are grouped by category/package
// _Requirements: 3.2_
func TestGroupStatusEntriesByPackage(t *testing.T) {
	entries := []git.StatusEntry{
		{Status: "A", FilePath: "app-misc/hello/hello-1.0.ebuild"},
		{Status: "M", FilePath: "app-misc/hello/Manifest"},
		{Status: "A", FilePath: "sys-apps/world/world-2.0.ebuild"},
	}

	statuses := GroupStatusEntries(entries)

	if len(statuses) != 2 {
		t.Fatalf("Expected 2 package groups, got %d", len(statuses))
	}

	// Check first package (sorted alphabetically)
	if statuses[0].Category != "app-misc" || statuses[0].Package != "hello" {
		t.Errorf("First package should be app-misc/hello, got %s/%s", statuses[0].Category, statuses[0].Package)
	}
	if len(statuses[0].Changes) != 2 {
		t.Errorf("app-misc/hello should have 2 changes, got %d", len(statuses[0].Changes))
	}

	// Check second package
	if statuses[1].Category != "sys-apps" || statuses[1].Package != "world" {
		t.Errorf("Second package should be sys-apps/world, got %s/%s", statuses[1].Category, statuses[1].Package)
	}
	if len(statuses[1].Changes) != 1 {
		t.Errorf("sys-apps/world should have 1 change, got %d", len(statuses[1].Changes))
	}
}

// TestFormatStatusOutput tests the formatted output structure
// _Requirements: 3.2, 3.3, 3.4_
func TestFormatStatusOutput(t *testing.T) {
	entries := []git.StatusEntry{
		{Status: "A", FilePath: "app-misc/hello/hello-1.0.ebuild"},
		{Status: "M", FilePath: "app-misc/hello/Manifest"},
	}

	statuses := GroupStatusEntries(entries)
	output := FormatStatus(statuses)

	// Check that output contains expected elements
	if !strings.Contains(output, "app-misc/hello:") {
		t.Error("Output should contain package header 'app-misc/hello:'")
	}
	if !strings.Contains(output, "[Added]") {
		t.Error("Output should contain '[Added]' status")
	}
	if !strings.Contains(output, "[Modified]") {
		t.Error("Output should contain '[Modified]' status")
	}
	if !strings.Contains(output, "hello-1.0.ebuild") {
		t.Error("Output should contain ebuild filename")
	}
	if !strings.Contains(output, "Manifest") {
		t.Error("Output should contain Manifest filename")
	}
	if !strings.Contains(output, "(ebuild)") {
		t.Error("Output should contain file type '(ebuild)'")
	}
	if !strings.Contains(output, "(manifest)") {
		t.Error("Output should contain file type '(manifest)'")
	}
}


// Tests for Status() using MockGitRunner
// _Requirements: 8.3_

// TestStatusWithMockGitRunner tests the Status function with mock git runner
func TestStatusWithMockGitRunner(t *testing.T) {
	t.Run("successful status with multiple entries", func(t *testing.T) {
		mock := git.NewMockGitRunner("/test/overlay")
		mock.StatusFunc = func() ([]git.StatusEntry, error) {
			return []git.StatusEntry{
				{Status: "A", FilePath: "app-misc/hello/hello-1.0.ebuild"},
				{Status: "M", FilePath: "app-misc/hello/Manifest"},
				{Status: "A", FilePath: "sys-apps/world/world-2.0.ebuild"},
			}, nil
		}

		statuses, err := StatusWithExecutor(mock)
		if err != nil {
			t.Errorf("StatusWithExecutor() error = %v, want nil", err)
		}

		// Should have 2 package groups
		if len(statuses) != 2 {
			t.Fatalf("Expected 2 package groups, got %d", len(statuses))
		}

		// Check first package (sorted alphabetically)
		if statuses[0].Category != "app-misc" || statuses[0].Package != "hello" {
			t.Errorf("First package = %s/%s, want app-misc/hello", statuses[0].Category, statuses[0].Package)
		}
		if len(statuses[0].Changes) != 2 {
			t.Errorf("app-misc/hello changes = %d, want 2", len(statuses[0].Changes))
		}

		// Check second package
		if statuses[1].Category != "sys-apps" || statuses[1].Package != "world" {
			t.Errorf("Second package = %s/%s, want sys-apps/world", statuses[1].Category, statuses[1].Package)
		}
	})

	t.Run("empty status", func(t *testing.T) {
		mock := git.NewMockGitRunner("/test/overlay")
		mock.StatusFunc = func() ([]git.StatusEntry, error) {
			return []git.StatusEntry{}, nil
		}

		statuses, err := StatusWithExecutor(mock)
		if err != nil {
			t.Errorf("StatusWithExecutor() error = %v, want nil", err)
		}

		if len(statuses) != 0 {
			t.Errorf("Expected 0 package groups, got %d", len(statuses))
		}
	})

	t.Run("status with error", func(t *testing.T) {
		mock := git.NewMockGitRunner("/test/overlay")
		mock.StatusFunc = func() ([]git.StatusEntry, error) {
			return nil, errors.New("git status failed: not a git repository")
		}

		_, err := StatusWithExecutor(mock)
		if err == nil {
			t.Error("StatusWithExecutor() error = nil, want error")
		}
		if !strings.Contains(err.Error(), "not a git repository") {
			t.Errorf("Error should contain 'not a git repository', got: %v", err)
		}
	})
}

// TestStatusGroupingWithMock tests status grouping behavior with mock
// _Requirements: 8.3_
func TestStatusGroupingWithMock(t *testing.T) {
	testCases := []struct {
		name           string
		entries        []git.StatusEntry
		expectedGroups int
		checkFunc      func(t *testing.T, statuses []PackageStatus)
	}{
		{
			name: "group by category/package",
			entries: []git.StatusEntry{
				{Status: "A", FilePath: "app-misc/hello/hello-1.0.ebuild"},
				{Status: "M", FilePath: "app-misc/hello/Manifest"},
				{Status: "A", FilePath: "app-misc/hello/metadata.xml"},
			},
			expectedGroups: 1,
			checkFunc: func(t *testing.T, statuses []PackageStatus) {
				if len(statuses[0].Changes) != 3 {
					t.Errorf("Expected 3 changes in group, got %d", len(statuses[0].Changes))
				}
			},
		},
		{
			name: "separate packages in same category",
			entries: []git.StatusEntry{
				{Status: "A", FilePath: "app-misc/hello/hello-1.0.ebuild"},
				{Status: "A", FilePath: "app-misc/world/world-1.0.ebuild"},
			},
			expectedGroups: 2,
			checkFunc: func(t *testing.T, statuses []PackageStatus) {
				if statuses[0].Package != "hello" {
					t.Errorf("First package = %s, want hello", statuses[0].Package)
				}
				if statuses[1].Package != "world" {
					t.Errorf("Second package = %s, want world", statuses[1].Package)
				}
			},
		},
		{
			name: "different file types",
			entries: []git.StatusEntry{
				{Status: "A", FilePath: "app-misc/hello/hello-1.0.ebuild"},
				{Status: "M", FilePath: "app-misc/hello/Manifest"},
				{Status: "A", FilePath: "app-misc/hello/metadata.xml"},
				{Status: "A", FilePath: "app-misc/hello/files/patch.patch"},
			},
			expectedGroups: 1,
			checkFunc: func(t *testing.T, statuses []PackageStatus) {
				fileTypes := make(map[FileType]bool)
				for _, change := range statuses[0].Changes {
					fileTypes[change.Type] = true
				}
				if !fileTypes[FileTypeEbuild] {
					t.Error("Expected ebuild file type")
				}
				if !fileTypes[FileTypeManifest] {
					t.Error("Expected manifest file type")
				}
				if !fileTypes[FileTypeMetadata] {
					t.Error("Expected metadata file type")
				}
				if !fileTypes[FileTypeFiles] {
					t.Error("Expected files file type")
				}
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mock := git.NewMockGitRunner("/test/overlay")
			mock.StatusFunc = func() ([]git.StatusEntry, error) {
				return tc.entries, nil
			}

			statuses, err := StatusWithExecutor(mock)
			if err != nil {
				t.Errorf("StatusWithExecutor() error = %v", err)
			}

			if len(statuses) != tc.expectedGroups {
				t.Errorf("Expected %d groups, got %d", tc.expectedGroups, len(statuses))
			}

			if tc.checkFunc != nil {
				tc.checkFunc(t, statuses)
			}
		})
	}
}

// TestStatusLabelMappingWithMock tests that status labels are correctly mapped
// _Requirements: 8.3_
func TestStatusLabelMappingWithMock(t *testing.T) {
	testCases := []struct {
		gitStatus     string
		expectedLabel string
	}{
		{"A", "Added"},
		{"M", "Modified"},
		{"D", "Deleted"},
		{"R", "Renamed"},
		{"??", "Untracked"},
	}

	for _, tc := range testCases {
		t.Run(tc.gitStatus, func(t *testing.T) {
			mock := git.NewMockGitRunner("/test/overlay")
			mock.StatusFunc = func() ([]git.StatusEntry, error) {
				return []git.StatusEntry{
					{Status: tc.gitStatus, FilePath: "app-misc/hello/hello-1.0.ebuild"},
				}, nil
			}

			statuses, err := StatusWithExecutor(mock)
			if err != nil {
				t.Errorf("StatusWithExecutor() error = %v", err)
			}

			if len(statuses) != 1 || len(statuses[0].Changes) != 1 {
				t.Fatal("Expected 1 package with 1 change")
			}

			if statuses[0].Changes[0].Status != tc.expectedLabel {
				t.Errorf("Status label = %s, want %s", statuses[0].Changes[0].Status, tc.expectedLabel)
			}
		})
	}
}
