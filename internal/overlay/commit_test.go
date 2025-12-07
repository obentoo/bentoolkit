package overlay

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
	"github.com/obentoo/bentoo-tools/internal/common/config"
	"github.com/obentoo/bentoo-tools/internal/common/ebuild"
	"github.com/obentoo/bentoo-tools/internal/common/git"
)

// genCategory generates valid category names (e.g., app-misc, sys-apps)
func genCommitCategory() gopter.Gen {
	return gen.RegexMatch(`^[a-z]{2,8}-[a-z]{2,8}$`)
}

// genPackageName generates valid package names
func genCommitPackageName() gopter.Gen {
	return gen.RegexMatch(`^[a-z][a-z0-9]{1,10}$`)
}

// genVersion generates valid version strings
func genCommitVersion() gopter.Gen {
	return gen.RegexMatch(`^[1-9][0-9]?\.[0-9]+(\.[0-9]+)?$`)
}

// genHigherVersion generates a version that is higher than the input
func genHigherVersion(baseVersion string) gopter.Gen {
	return gen.RegexMatch(`^[1-9][0-9]?\.[0-9]+(\.[0-9]+)?$`).SuchThat(func(v string) bool {
		return ebuild.CompareVersions(v, baseVersion) > 0
	})
}

// genLowerVersion generates a version that is lower than the input
func genLowerVersion(baseVersion string) gopter.Gen {
	return gen.RegexMatch(`^[1-9][0-9]?\.[0-9]+(\.[0-9]+)?$`).SuchThat(func(v string) bool {
		return ebuild.CompareVersions(v, baseVersion) < 0
	})
}

// genEbuildStatusEntry generates a StatusEntry for an ebuild file
func genEbuildStatusEntry() gopter.Gen {
	return gopter.CombineGens(
		genCommitCategory(),
		genCommitPackageName(),
		genCommitVersion(),
		gen.OneConstOf("A", "M", "D"),
	).Map(func(values []interface{}) git.StatusEntry {
		category := values[0].(string)
		pkg := values[1].(string)
		version := values[2].(string)
		status := values[3].(string)

		filePath := category + "/" + pkg + "/" + pkg + "-" + version + ".ebuild"
		return git.StatusEntry{
			Status:   status,
			FilePath: filePath,
		}
	})
}

// genEbuildStatusEntryList generates a list of ebuild StatusEntry objects
func genEbuildStatusEntryList() gopter.Gen {
	return gen.IntRange(1, 5).FlatMap(func(n interface{}) gopter.Gen {
		count := n.(int)
		gens := make([]gopter.Gen, count)
		for i := 0; i < count; i++ {
			gens[i] = genEbuildStatusEntry()
		}
		return gopter.CombineGens(gens...).Map(func(values []interface{}) []git.StatusEntry {
			entries := make([]git.StatusEntry, len(values))
			for i, v := range values {
				entries[i] = v.(git.StatusEntry)
			}
			return entries
		})
	}, reflect.TypeOf([]git.StatusEntry{}))
}


// TestVersionBumpDetection tests Property 4: Version bump detection
// **Feature: overlay-manager, Property 4: Version bump detection**
// **Validates: Requirements 4.5**
func TestVersionBumpDetection(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("Version bump correctly classified as up when new > old", prop.ForAll(
		func(category, pkg, oldVersion, newVersion string) bool {
			// Skip if versions are equal or new is not greater
			if ebuild.CompareVersions(newVersion, oldVersion) <= 0 {
				return true // Skip this case
			}

			entries := []git.StatusEntry{
				{Status: "D", FilePath: category + "/" + pkg + "/" + pkg + "-" + oldVersion + ".ebuild"},
				{Status: "A", FilePath: category + "/" + pkg + "/" + pkg + "-" + newVersion + ".ebuild"},
			}

			changes := AnalyzeChanges(entries)

			// Should have exactly one "up" change
			upCount := 0
			for _, c := range changes {
				if c.Type == Up {
					upCount++
					if c.OldVersion != oldVersion || c.Version != newVersion {
						t.Logf("Wrong versions: expected %s -> %s, got %s -> %s",
							oldVersion, newVersion, c.OldVersion, c.Version)
						return false
					}
				}
			}

			if upCount != 1 {
				t.Logf("Expected 1 up change, got %d for %s -> %s", upCount, oldVersion, newVersion)
				return false
			}

			return true
		},
		genCommitCategory(),
		genCommitPackageName(),
		genCommitVersion(),
		genCommitVersion(),
	))

	properties.Property("Version downgrade correctly classified as down when new < old", prop.ForAll(
		func(category, pkg, oldVersion, newVersion string) bool {
			// Skip if versions are equal or new is not less
			if ebuild.CompareVersions(newVersion, oldVersion) >= 0 {
				return true // Skip this case
			}

			entries := []git.StatusEntry{
				{Status: "D", FilePath: category + "/" + pkg + "/" + pkg + "-" + oldVersion + ".ebuild"},
				{Status: "A", FilePath: category + "/" + pkg + "/" + pkg + "-" + newVersion + ".ebuild"},
			}

			changes := AnalyzeChanges(entries)

			// Should have exactly one "down" change
			downCount := 0
			for _, c := range changes {
				if c.Type == Down {
					downCount++
					if c.OldVersion != oldVersion || c.Version != newVersion {
						t.Logf("Wrong versions: expected %s -> %s, got %s -> %s",
							oldVersion, newVersion, c.OldVersion, c.Version)
						return false
					}
				}
			}

			if downCount != 1 {
				t.Logf("Expected 1 down change, got %d for %s -> %s", downCount, oldVersion, newVersion)
				return false
			}

			return true
		},
		genCommitCategory(),
		genCommitPackageName(),
		genCommitVersion(),
		genCommitVersion(),
	))

	properties.TestingRun(t)
}


// TestCommitMessageCompleteness tests Property 6: Commit message contains all changes
// **Feature: overlay-manager, Property 6: Commit message contains all changes**
// **Validates: Requirements 4.1**
func TestCommitMessageCompleteness(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	properties.Property("All packages appear in generated commit message", prop.ForAll(
		func(entries []git.StatusEntry) bool {
			if len(entries) == 0 {
				// Empty input should return default message
				changes := AnalyzeChanges(entries)
				message := GenerateMessage(changes)
				return message == "update: package files"
			}

			changes := AnalyzeChanges(entries)
			message := GenerateMessage(changes)

			// If no ebuild changes, should return default message
			if len(changes) == 0 {
				return message == "update: package files"
			}

			// Collect all unique packages from changes
			expectedPackages := make(map[string]bool)
			for _, c := range changes {
				expectedPackages[c.Package] = true
			}

			// Verify all packages appear in message
			for pkg := range expectedPackages {
				if !strings.Contains(message, pkg) {
					t.Logf("Package %s not found in message: %s", pkg, message)
					return false
				}
			}

			return true
		},
		genEbuildStatusEntryList(),
	))

	properties.TestingRun(t)
}


// Unit tests for commit message generation
// _Requirements: 4.3, 4.4, 4.5, 4.6, 4.7, 4.8, 4.9, 4.10, 4.11_

// TestAnalyzeChangesAdd tests add scenario
func TestAnalyzeChangesAdd(t *testing.T) {
	entries := []git.StatusEntry{
		{Status: "A", FilePath: "app-misc/hello/hello-1.0.ebuild"},
	}

	changes := AnalyzeChanges(entries)

	if len(changes) != 1 {
		t.Fatalf("Expected 1 change, got %d", len(changes))
	}
	if changes[0].Type != Add {
		t.Errorf("Expected Add, got %s", changes[0].Type)
	}
	if changes[0].Category != "app-misc" {
		t.Errorf("Expected category app-misc, got %s", changes[0].Category)
	}
	if changes[0].Package != "hello" {
		t.Errorf("Expected package hello, got %s", changes[0].Package)
	}
	if changes[0].Version != "1.0" {
		t.Errorf("Expected version 1.0, got %s", changes[0].Version)
	}
}

// TestAnalyzeChangesDel tests delete scenario
func TestAnalyzeChangesDel(t *testing.T) {
	entries := []git.StatusEntry{
		{Status: "D", FilePath: "app-misc/hello/hello-1.0.ebuild"},
	}

	changes := AnalyzeChanges(entries)

	if len(changes) != 1 {
		t.Fatalf("Expected 1 change, got %d", len(changes))
	}
	if changes[0].Type != Del {
		t.Errorf("Expected Del, got %s", changes[0].Type)
	}
}

// TestAnalyzeChangesMod tests modify scenario
func TestAnalyzeChangesMod(t *testing.T) {
	entries := []git.StatusEntry{
		{Status: "M", FilePath: "app-misc/hello/hello-1.0.ebuild"},
	}

	changes := AnalyzeChanges(entries)

	if len(changes) != 1 {
		t.Fatalf("Expected 1 change, got %d", len(changes))
	}
	if changes[0].Type != Mod {
		t.Errorf("Expected Mod, got %s", changes[0].Type)
	}
}

// TestAnalyzeChangesUp tests version bump (upgrade) scenario
func TestAnalyzeChangesUp(t *testing.T) {
	entries := []git.StatusEntry{
		{Status: "D", FilePath: "app-misc/hello/hello-1.0.ebuild"},
		{Status: "A", FilePath: "app-misc/hello/hello-2.0.ebuild"},
	}

	changes := AnalyzeChanges(entries)

	if len(changes) != 1 {
		t.Fatalf("Expected 1 change, got %d", len(changes))
	}
	if changes[0].Type != Up {
		t.Errorf("Expected Up, got %s", changes[0].Type)
	}
	if changes[0].OldVersion != "1.0" {
		t.Errorf("Expected old version 1.0, got %s", changes[0].OldVersion)
	}
	if changes[0].Version != "2.0" {
		t.Errorf("Expected new version 2.0, got %s", changes[0].Version)
	}
}

// TestAnalyzeChangesDown tests version downgrade scenario
func TestAnalyzeChangesDown(t *testing.T) {
	entries := []git.StatusEntry{
		{Status: "D", FilePath: "app-misc/hello/hello-2.0.ebuild"},
		{Status: "A", FilePath: "app-misc/hello/hello-1.0.ebuild"},
	}

	changes := AnalyzeChanges(entries)

	if len(changes) != 1 {
		t.Fatalf("Expected 1 change, got %d", len(changes))
	}
	if changes[0].Type != Down {
		t.Errorf("Expected Down, got %s", changes[0].Type)
	}
	if changes[0].OldVersion != "2.0" {
		t.Errorf("Expected old version 2.0, got %s", changes[0].OldVersion)
	}
	if changes[0].Version != "1.0" {
		t.Errorf("Expected new version 1.0, got %s", changes[0].Version)
	}
}

// TestGenerateMessageGroupingByAction tests grouping by action type
// _Requirements: 4.3_
func TestGenerateMessageGroupingByAction(t *testing.T) {
	changes := []Change{
		{Type: Add, Category: "app-misc", Package: "hello", Version: "1.0"},
		{Type: Add, Category: "sys-apps", Package: "world", Version: "2.0"},
	}

	message := GenerateMessage(changes)

	// Should have single add() block, not add(), add()
	if strings.Count(message, "add(") != 1 {
		t.Errorf("Expected single add() block, got: %s", message)
	}
	if !strings.Contains(message, "hello-1.0") {
		t.Errorf("Message should contain hello-1.0: %s", message)
	}
	if !strings.Contains(message, "world-2.0") {
		t.Errorf("Message should contain world-2.0: %s", message)
	}
}

// TestGenerateMessageCategoryGrouping tests category grouping with braces
// _Requirements: 4.4_
func TestGenerateMessageCategoryGrouping(t *testing.T) {
	changes := []Change{
		{Type: Add, Category: "www-client", Package: "firefox", Version: "120.0"},
		{Type: Add, Category: "www-client", Package: "chrome", Version: "121.0"},
	}

	message := GenerateMessage(changes)

	// Should use brace notation for same category
	if !strings.Contains(message, "www-client/{") {
		t.Errorf("Expected category grouping with braces: %s", message)
	}
	if !strings.Contains(message, "firefox-120.0") {
		t.Errorf("Message should contain firefox-120.0: %s", message)
	}
	if !strings.Contains(message, "chrome-121.0") {
		t.Errorf("Message should contain chrome-121.0: %s", message)
	}
}

// TestGenerateMessagePackageVariants tests package variant grouping
// _Requirements: 4.5_
func TestGenerateMessagePackageVariants(t *testing.T) {
	changes := []Change{
		{Type: Up, Category: "www-client", Package: "firefox", Version: "120.0", OldVersion: "119.0"},
		{Type: Up, Category: "www-client", Package: "firefox-bin", Version: "120.0", OldVersion: "119.0"},
	}

	message := GenerateMessage(changes)

	// Should use nested braces for variants
	if !strings.Contains(message, "firefox{,-bin}") {
		t.Errorf("Expected variant grouping with nested braces: %s", message)
	}
	if !strings.Contains(message, "119.0 -> 120.0") {
		t.Errorf("Expected version transition: %s", message)
	}
}

// TestGenerateMessageDefaultForNonEbuild tests default message for non-ebuild changes
// _Requirements: 4.11_
func TestGenerateMessageDefaultForNonEbuild(t *testing.T) {
	changes := []Change{}

	message := GenerateMessage(changes)

	expected := "update: package files"
	if message != expected {
		t.Errorf("Expected %q, got %q", expected, message)
	}
}

// TestGenerateMessageMultiplePackages tests multiple packages in single commit
func TestGenerateMessageMultiplePackages(t *testing.T) {
	changes := []Change{
		{Type: Add, Category: "app-misc", Package: "new", Version: "1.0"},
		{Type: Del, Category: "app-misc", Package: "old", Version: "1.0"},
		{Type: Up, Category: "sys-apps", Package: "pkg", Version: "2.0", OldVersion: "1.0"},
	}

	message := GenerateMessage(changes)

	// Should have all action types
	if !strings.Contains(message, "add(") {
		t.Errorf("Message should contain add(): %s", message)
	}
	if !strings.Contains(message, "del(") {
		t.Errorf("Message should contain del(): %s", message)
	}
	if !strings.Contains(message, "up(") {
		t.Errorf("Message should contain up(): %s", message)
	}
}

// TestGenerateMessageSingleAdd tests single add message format
func TestGenerateMessageSingleAdd(t *testing.T) {
	changes := []Change{
		{Type: Add, Category: "app-misc", Package: "hello", Version: "1.0"},
	}

	message := GenerateMessage(changes)

	expected := "add(app-misc/hello-1.0)"
	if message != expected {
		t.Errorf("Expected %q, got %q", expected, message)
	}
}

// TestGenerateMessageVersionBump tests version bump message format
func TestGenerateMessageVersionBump(t *testing.T) {
	changes := []Change{
		{Type: Up, Category: "sys-apps", Package: "bentoo-utils", Version: "0.3.0", OldVersion: "0.2.0"},
	}

	message := GenerateMessage(changes)

	expected := "up(sys-apps/bentoo-utils-0.2.0 -> 0.3.0)"
	if message != expected {
		t.Errorf("Expected %q, got %q", expected, message)
	}
}

// TestNormalizeStatus tests status code normalization
func TestNormalizeStatus(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"A", "A"},
		{"M", "M"},
		{"D", "D"},
		{"??", "A"},
		{"AM", "A"},
		{"MM", "M"},
		{"R", "A"},
		{" M", "M"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := normalizeStatus(tt.input)
			if result != tt.expected {
				t.Errorf("normalizeStatus(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}


// Tests for Commit() using MockGitRunner
// _Requirements: 8.2_

// TestCommitWithMockGitRunner tests the Commit function with mock git runner
func TestCommitWithMockGitRunner(t *testing.T) {
	t.Run("successful commit", func(t *testing.T) {
		var capturedMessage, capturedUser, capturedEmail string

		mock := git.NewMockGitRunner("/test/overlay")
		mock.CommitFunc = func(message, user, email string) error {
			capturedMessage = message
			capturedUser = user
			capturedEmail = email
			return nil
		}

		cfg := &config.Config{
			Git: config.GitConfig{
				User:  "Test User",
				Email: "test@example.com",
			},
		}

		err := CommitWithExecutor(cfg, "add(app-misc/hello-1.0)", mock)
		if err != nil {
			t.Errorf("CommitWithExecutor() error = %v, want nil", err)
		}

		if capturedMessage != "add(app-misc/hello-1.0)" {
			t.Errorf("Commit message = %q, want %q", capturedMessage, "add(app-misc/hello-1.0)")
		}
		if capturedUser != "Test User" {
			t.Errorf("Commit user = %q, want %q", capturedUser, "Test User")
		}
		if capturedEmail != "test@example.com" {
			t.Errorf("Commit email = %q, want %q", capturedEmail, "test@example.com")
		}
	})

	t.Run("commit with error", func(t *testing.T) {
		mock := git.NewMockGitRunner("/test/overlay")
		mock.CommitFunc = func(message, user, email string) error {
			return errors.New("git commit failed: nothing to commit")
		}

		cfg := &config.Config{
			Git: config.GitConfig{
				User:  "Test User",
				Email: "test@example.com",
			},
		}

		err := CommitWithExecutor(cfg, "test message", mock)
		if err == nil {
			t.Error("CommitWithExecutor() error = nil, want error")
		}
		if !strings.Contains(err.Error(), "nothing to commit") {
			t.Errorf("Error message should contain 'nothing to commit', got: %v", err)
		}
	})

	t.Run("commit with empty user info", func(t *testing.T) {
		var capturedUser, capturedEmail string

		mock := git.NewMockGitRunner("/test/overlay")
		mock.CommitFunc = func(message, user, email string) error {
			capturedUser = user
			capturedEmail = email
			return nil
		}

		cfg := &config.Config{
			Git: config.GitConfig{
				User:  "",
				Email: "",
			},
		}

		err := CommitWithExecutor(cfg, "test message", mock)
		if err != nil {
			t.Errorf("CommitWithExecutor() error = %v, want nil", err)
		}

		// Empty user info should be passed through
		if capturedUser != "" {
			t.Errorf("Commit user = %q, want empty string", capturedUser)
		}
		if capturedEmail != "" {
			t.Errorf("Commit email = %q, want empty string", capturedEmail)
		}
	})
}

// TestCommitMessageGeneration tests that generated messages are passed correctly to git
// _Requirements: 8.2_
func TestCommitMessageGeneration(t *testing.T) {
	testCases := []struct {
		name     string
		changes  []Change
		expected string
	}{
		{
			name: "single add",
			changes: []Change{
				{Type: Add, Category: "app-misc", Package: "hello", Version: "1.0"},
			},
			expected: "add(app-misc/hello-1.0)",
		},
		{
			name: "version bump",
			changes: []Change{
				{Type: Up, Category: "sys-apps", Package: "world", Version: "2.0", OldVersion: "1.0"},
			},
			expected: "up(sys-apps/world-1.0 -> 2.0)",
		},
		{
			name: "multiple changes",
			changes: []Change{
				{Type: Add, Category: "app-misc", Package: "new", Version: "1.0"},
				{Type: Del, Category: "app-misc", Package: "old", Version: "1.0"},
			},
			expected: "add(app-misc/new-1.0), del(app-misc/old-1.0)",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var capturedMessage string

			mock := git.NewMockGitRunner("/test/overlay")
			mock.CommitFunc = func(message, user, email string) error {
				capturedMessage = message
				return nil
			}

			cfg := &config.Config{
				Git: config.GitConfig{
					User:  "Test User",
					Email: "test@example.com",
				},
			}

			message := GenerateMessage(tc.changes)
			err := CommitWithExecutor(cfg, message, mock)
			if err != nil {
				t.Errorf("CommitWithExecutor() error = %v", err)
			}

			if capturedMessage != tc.expected {
				t.Errorf("Commit message = %q, want %q", capturedMessage, tc.expected)
			}
		})
	}
}
