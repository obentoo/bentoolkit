package overlay

import (
	"errors"
	"strings"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
	"github.com/obentoo/bentoolkit/internal/common/git"
)

// TestSyncWithoutConflictsSucceeds tests Property 3: Sync without conflicts succeeds
// **Feature: overlay-improvements, Property 3: Sync without conflicts succeeds**
// **Validates: Requirements 6.2**
func TestSyncWithoutConflictsSucceeds(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Generate random remote names from a set of valid git remote names
	genRemoteName := gen.OneConstOf("origin", "upstream", "fork", "backup", "remote1", "my-remote")

	properties.Property("Sync without conflicts returns Success=true and empty Conflicts", prop.ForAll(
		func(remote string) bool {
			// Create mock that simulates successful fetch and merge (no conflicts)
			mock := &git.MockGitRunner{
				FetchFunc: func(r string) error {
					return nil // Fetch succeeds
				},
				MergeFunc: func(branch string) error {
					return nil // Merge succeeds without conflicts
				},
			}

			result, err := SyncWithRunner(mock, remote)

			// Property: No error should be returned
			if err != nil {
				t.Logf("Expected no error, got: %v", err)
				return false
			}

			// Property: Success should be true
			if !result.Success {
				t.Logf("Expected Success=true, got false")
				return false
			}

			// Property: Conflicts slice should be empty
			if len(result.Conflicts) != 0 {
				t.Logf("Expected empty Conflicts, got: %v", result.Conflicts)
				return false
			}

			return true
		},
		genRemoteName,
	))

	properties.TestingRun(t)
}

// TestSyncWithConflictsReportsThem tests Property 4: Sync with conflicts reports them
// **Feature: overlay-improvements, Property 4: Sync with conflicts reports them**
// **Validates: Requirements 6.3**
func TestSyncWithConflictsReportsThem(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Generate random file paths for conflicts using alphanumeric strings
	genFilePath := gen.AlphaString().Map(func(s string) string {
		if s == "" {
			return "file.txt"
		}
		return s + ".ebuild"
	})

	// Generate a list of 1-5 conflicting files
	genConflictFiles := gen.SliceOfN(5, genFilePath).Map(func(files []string) []string {
		// Ensure at least one file
		if len(files) == 0 {
			return []string{"conflict.txt"}
		}
		return files
	})

	properties.Property("Sync with conflicts returns Success=false and reports all conflicts", prop.ForAll(
		func(conflictFiles []string) bool {
			// Build a conflict error message like git would produce
			var errParts []string
			for _, file := range conflictFiles {
				errParts = append(errParts, "CONFLICT (content): Merge conflict in "+file)
			}
			errParts = append(errParts, "Automatic merge failed; fix conflicts and then commit the result.")
			conflictErr := errors.New(strings.Join(errParts, "\n"))

			// Create mock that simulates merge with conflicts
			mock := &git.MockGitRunner{
				FetchFunc: func(r string) error {
					return nil // Fetch succeeds
				},
				MergeFunc: func(branch string) error {
					return conflictErr // Merge fails with conflicts
				},
			}

			result, err := SyncWithRunner(mock, "origin")

			// Property: No error should be returned (conflicts are reported in result)
			if err != nil {
				t.Logf("Expected no error, got: %v", err)
				return false
			}

			// Property: Success should be false
			if result.Success {
				t.Logf("Expected Success=false, got true")
				return false
			}

			// Property: Conflicts slice should contain all conflicting files
			if len(result.Conflicts) != len(conflictFiles) {
				t.Logf("Expected %d conflicts, got %d: %v", len(conflictFiles), len(result.Conflicts), result.Conflicts)
				return false
			}

			// Verify each conflict file is reported
			for _, expected := range conflictFiles {
				found := false
				for _, actual := range result.Conflicts {
					if actual == expected {
						found = true
						break
					}
				}
				if !found {
					t.Logf("Expected conflict file %q not found in result: %v", expected, result.Conflicts)
					return false
				}
			}

			return true
		},
		genConflictFiles,
	))

	properties.TestingRun(t)
}

// TestSyncFetchError tests that fetch errors are propagated
// _Requirements: 6.1_
func TestSyncFetchError(t *testing.T) {
	fetchErr := errors.New("network error: could not reach remote")

	mock := &git.MockGitRunner{
		FetchFunc: func(r string) error {
			return fetchErr
		},
	}

	_, err := SyncWithRunner(mock, "origin")
	if err == nil {
		t.Error("Expected error when fetch fails")
	}
	if !strings.Contains(err.Error(), "network error") {
		t.Errorf("Expected fetch error to be propagated, got: %v", err)
	}
}

// TestSyncNoRemote tests that empty remote returns error
// _Requirements: 6.1_
func TestSyncNoRemote(t *testing.T) {
	mock := &git.MockGitRunner{}

	_, err := SyncWithRunner(mock, "")
	if err == nil {
		t.Error("Expected error when remote is empty")
	}
	if !errors.Is(err, ErrNoRemote) {
		t.Errorf("Expected ErrNoRemote, got: %v", err)
	}
}

// TestSyncMergeNonConflictError tests that non-conflict merge errors are propagated
// _Requirements: 6.2_
func TestSyncMergeNonConflictError(t *testing.T) {
	mergeErr := errors.New("fatal: not a git repository")

	mock := &git.MockGitRunner{
		FetchFunc: func(r string) error {
			return nil
		},
		MergeFunc: func(branch string) error {
			return mergeErr
		},
	}

	_, err := SyncWithRunner(mock, "origin")
	if err == nil {
		t.Error("Expected error when merge fails with non-conflict error")
	}
	if !strings.Contains(err.Error(), "not a git repository") {
		t.Errorf("Expected merge error to be propagated, got: %v", err)
	}
}
