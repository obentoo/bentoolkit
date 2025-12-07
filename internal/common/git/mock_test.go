package git

import (
	"errors"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

// **Feature: overlay-improvements, Property: Mock implements all interface methods**
// **Validates: Requirements 1.2**
func TestMockGitRunnerImplementsInterface(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100
	properties := gopter.NewProperties(parameters)

	// Property: MockGitRunner satisfies GitExecutor interface for any workDir
	properties.Property("MockGitRunner satisfies GitExecutor for any workDir", prop.ForAll(
		func(workDir string) bool {
			mock := NewMockGitRunner(workDir)
			// Compile-time check is done via var _ GitExecutor = (*MockGitRunner)(nil)
			// Runtime check: verify the mock can be assigned to interface
			var executor GitExecutor = mock
			return executor != nil && executor.WorkDir() == workDir
		},
		gen.AnyString(),
	))

	// Property: Status returns configured function result
	properties.Property("Status returns configured function result", prop.ForAll(
		func(workDir string, statusCount int) bool {
			mock := NewMockGitRunner(workDir)
			expectedEntries := make([]StatusEntry, statusCount)
			for i := 0; i < statusCount; i++ {
				expectedEntries[i] = StatusEntry{Status: "A", FilePath: "test.txt"}
			}
			mock.StatusFunc = func() ([]StatusEntry, error) {
				return expectedEntries, nil
			}
			entries, err := mock.Status()
			return err == nil && len(entries) == statusCount
		},
		gen.AnyString(),
		gen.IntRange(0, 10),
	))

	// Property: Add calls configured function with correct paths
	properties.Property("Add calls configured function with correct paths", prop.ForAll(
		func(workDir string, paths []string) bool {
			mock := NewMockGitRunner(workDir)
			var receivedPaths []string
			mock.AddFunc = func(p ...string) error {
				receivedPaths = p
				return nil
			}
			err := mock.Add(paths...)
			if err != nil {
				return false
			}
			if len(receivedPaths) != len(paths) {
				return false
			}
			for i, p := range paths {
				if receivedPaths[i] != p {
					return false
				}
			}
			return true
		},
		gen.AnyString(),
		gen.SliceOf(gen.AnyString()),
	))

	// Property: Commit calls configured function with correct parameters
	properties.Property("Commit calls configured function with correct parameters", prop.ForAll(
		func(workDir, message, user, email string) bool {
			mock := NewMockGitRunner(workDir)
			var receivedMsg, receivedUser, receivedEmail string
			mock.CommitFunc = func(m, u, e string) error {
				receivedMsg, receivedUser, receivedEmail = m, u, e
				return nil
			}
			err := mock.Commit(message, user, email)
			return err == nil && receivedMsg == message && receivedUser == user && receivedEmail == email
		},
		gen.AnyString(),
		gen.AnyString(),
		gen.AnyString(),
		gen.AnyString(),
	))

	// Property: Error propagation works correctly
	properties.Property("Error propagation works correctly", prop.ForAll(
		func(workDir, errMsg string) bool {
			mock := NewMockGitRunner(workDir)
			expectedErr := errors.New(errMsg)
			mock.StatusFunc = func() ([]StatusEntry, error) {
				return nil, expectedErr
			}
			_, err := mock.Status()
			return errors.Is(err, expectedErr)
		},
		gen.AnyString(),
		gen.AnyString().SuchThat(func(s string) bool { return len(s) > 0 }),
	))

	// Property: Fetch calls configured function with correct remote
	properties.Property("Fetch calls configured function with correct remote", prop.ForAll(
		func(workDir, remote string) bool {
			mock := NewMockGitRunner(workDir)
			var receivedRemote string
			mock.FetchFunc = func(r string) error {
				receivedRemote = r
				return nil
			}
			err := mock.Fetch(remote)
			return err == nil && receivedRemote == remote
		},
		gen.AnyString(),
		gen.AnyString(),
	))

	// Property: Merge calls configured function with correct branch
	properties.Property("Merge calls configured function with correct branch", prop.ForAll(
		func(workDir, branch string) bool {
			mock := NewMockGitRunner(workDir)
			var receivedBranch string
			mock.MergeFunc = func(b string) error {
				receivedBranch = b
				return nil
			}
			err := mock.Merge(branch)
			return err == nil && receivedBranch == branch
		},
		gen.AnyString(),
		gen.AnyString(),
	))

	properties.TestingRun(t)
}

// TestMockGitRunnerDefaultBehavior verifies default behavior when no functions are configured
func TestMockGitRunnerDefaultBehavior(t *testing.T) {
	mock := NewMockGitRunner("/test/dir")

	t.Run("Status returns nil without error", func(t *testing.T) {
		entries, err := mock.Status()
		if err != nil {
			t.Errorf("expected no error, got %v", err)
		}
		if entries != nil {
			t.Errorf("expected nil entries, got %v", entries)
		}
	})

	t.Run("Add returns nil without error", func(t *testing.T) {
		err := mock.Add("test.txt")
		if err != nil {
			t.Errorf("expected no error, got %v", err)
		}
	})

	t.Run("Commit returns nil without error", func(t *testing.T) {
		err := mock.Commit("msg", "user", "email")
		if err != nil {
			t.Errorf("expected no error, got %v", err)
		}
	})

	t.Run("Push returns nil without error", func(t *testing.T) {
		err := mock.Push()
		if err != nil {
			t.Errorf("expected no error, got %v", err)
		}
	})

	t.Run("PushDryRun returns empty string without error", func(t *testing.T) {
		result, err := mock.PushDryRun()
		if err != nil {
			t.Errorf("expected no error, got %v", err)
		}
		if result != "" {
			t.Errorf("expected empty string, got %q", result)
		}
	})

	t.Run("Fetch returns nil without error", func(t *testing.T) {
		err := mock.Fetch("origin")
		if err != nil {
			t.Errorf("expected no error, got %v", err)
		}
	})

	t.Run("Merge returns nil without error", func(t *testing.T) {
		err := mock.Merge("main")
		if err != nil {
			t.Errorf("expected no error, got %v", err)
		}
	})

	t.Run("WorkDir returns configured directory", func(t *testing.T) {
		if mock.WorkDir() != "/test/dir" {
			t.Errorf("expected /test/dir, got %q", mock.WorkDir())
		}
	})
}
