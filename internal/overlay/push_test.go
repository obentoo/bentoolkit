package overlay

import (
	"errors"
	"testing"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/git"
)

// TestPushWithInvalidConfig tests Push with invalid configuration
// _Requirements: 5.4_
func TestPushWithInvalidConfig(t *testing.T) {
	t.Run("empty overlay path", func(t *testing.T) {
		cfg := &config.Config{
			Overlay: config.OverlayConfig{
				Path: "",
			},
		}

		_, err := Push(cfg)
		if err != config.ErrOverlayPathNotSet {
			t.Errorf("Push() should return ErrOverlayPathNotSet, got %v", err)
		}
	})

	t.Run("non-existent overlay path", func(t *testing.T) {
		cfg := &config.Config{
			Overlay: config.OverlayConfig{
				Path: "/nonexistent/path/to/overlay",
			},
		}

		_, err := Push(cfg)
		if err != config.ErrOverlayPathNotFound {
			t.Errorf("Push() should return ErrOverlayPathNotFound, got %v", err)
		}
	})
}

// TestPushResultMethods tests PushResult struct
func TestPushResultMethods(t *testing.T) {
	t.Run("up-to-date result", func(t *testing.T) {
		result := &PushResult{
			UpToDate: true,
			Message:  "Everything is up-to-date. Nothing to push.",
		}

		if !result.UpToDate {
			t.Error("UpToDate should be true")
		}

		if result.Message != "Everything is up-to-date. Nothing to push." {
			t.Errorf("unexpected message: %s", result.Message)
		}
	})

	t.Run("success result", func(t *testing.T) {
		result := &PushResult{
			UpToDate: false,
			Message:  "Changes pushed successfully.",
		}

		if result.UpToDate {
			t.Error("UpToDate should be false")
		}

		if result.Message != "Changes pushed successfully." {
			t.Errorf("unexpected message: %s", result.Message)
		}
	})
}

// TestErrUpToDate tests the ErrUpToDate error
func TestErrUpToDate(t *testing.T) {
	if ErrUpToDate.Error() != "everything is up-to-date" {
		t.Errorf("ErrUpToDate message incorrect: %s", ErrUpToDate.Error())
	}
}

// Tests for Push() using MockGitRunner
// _Requirements: 8.4_

// TestPushWithMockGitRunner tests the Push function with mock git runner
func TestPushWithMockGitRunner(t *testing.T) {
	t.Run("successful push", func(t *testing.T) {
		pushCalled := false

		mock := git.NewMockGitRunner("/test/overlay")
		mock.PushFunc = func() error {
			pushCalled = true
			return nil
		}

		result, err := PushWithExecutor(mock)
		if err != nil {
			t.Errorf("PushWithExecutor() error = %v, want nil", err)
		}

		if !pushCalled {
			t.Error("Push() was not called on mock")
		}

		if result.UpToDate {
			t.Error("Result.UpToDate = true, want false")
		}

		if result.Message != "Changes pushed successfully." {
			t.Errorf("Result.Message = %q, want %q", result.Message, "Changes pushed successfully.")
		}
	})

	t.Run("push with up-to-date error", func(t *testing.T) {
		mock := git.NewMockGitRunner("/test/overlay")
		mock.PushFunc = func() error {
			return errors.New("Everything up-to-date")
		}

		result, err := PushWithExecutor(mock)
		if err != nil {
			t.Errorf("PushWithExecutor() error = %v, want nil", err)
		}

		if !result.UpToDate {
			t.Error("Result.UpToDate = false, want true")
		}

		if result.Message != "Everything is up-to-date. Nothing to push." {
			t.Errorf("Result.Message = %q, want %q", result.Message, "Everything is up-to-date. Nothing to push.")
		}
	})

	t.Run("push with 'up to date' variant", func(t *testing.T) {
		mock := git.NewMockGitRunner("/test/overlay")
		mock.PushFunc = func() error {
			return errors.New("remote: up to date")
		}

		result, err := PushWithExecutor(mock)
		if err != nil {
			t.Errorf("PushWithExecutor() error = %v, want nil", err)
		}

		if !result.UpToDate {
			t.Error("Result.UpToDate = false, want true")
		}
	})

	t.Run("push with real error", func(t *testing.T) {
		mock := git.NewMockGitRunner("/test/overlay")
		mock.PushFunc = func() error {
			return errors.New("fatal: remote origin not found")
		}

		result, err := PushWithExecutor(mock)
		if err == nil {
			t.Error("PushWithExecutor() error = nil, want error")
		}

		if result != nil {
			t.Error("Result should be nil on error")
		}

		if err.Error() != "fatal: remote origin not found" {
			t.Errorf("Error = %v, want 'fatal: remote origin not found'", err)
		}
	})

	t.Run("push with authentication error", func(t *testing.T) {
		mock := git.NewMockGitRunner("/test/overlay")
		mock.PushFunc = func() error {
			return errors.New("fatal: Authentication failed for 'https://github.com/user/repo.git'")
		}

		result, err := PushWithExecutor(mock)
		if err == nil {
			t.Error("PushWithExecutor() error = nil, want error")
		}

		if result != nil {
			t.Error("Result should be nil on authentication error")
		}
	})

	t.Run("push with network error", func(t *testing.T) {
		mock := git.NewMockGitRunner("/test/overlay")
		mock.PushFunc = func() error {
			return errors.New("fatal: unable to access 'https://github.com/user/repo.git': Could not resolve host: github.com")
		}

		result, err := PushWithExecutor(mock)
		if err == nil {
			t.Error("PushWithExecutor() error = nil, want error")
		}

		if result != nil {
			t.Error("Result should be nil on network error")
		}
	})
}

// TestPushErrorHandling tests various error scenarios
// _Requirements: 8.4_
func TestPushErrorHandling(t *testing.T) {
	errorCases := []struct {
		name           string
		errorMsg       string
		expectError    bool
		expectUpToDate bool
	}{
		{
			name:           "Everything up-to-date",
			errorMsg:       "Everything up-to-date",
			expectError:    false,
			expectUpToDate: true,
		},
		{
			name:           "up to date lowercase",
			errorMsg:       "up to date",
			expectError:    false,
			expectUpToDate: true,
		},
		{
			name:           "remote not found",
			errorMsg:       "fatal: remote origin not found",
			expectError:    true,
			expectUpToDate: false,
		},
		{
			name:           "permission denied",
			errorMsg:       "fatal: Could not read from remote repository. Permission denied",
			expectError:    true,
			expectUpToDate: false,
		},
		{
			name:           "rejected non-fast-forward",
			errorMsg:       "error: failed to push some refs: Updates were rejected because the tip of your current branch is behind",
			expectError:    true,
			expectUpToDate: false,
		},
	}

	for _, tc := range errorCases {
		t.Run(tc.name, func(t *testing.T) {
			mock := git.NewMockGitRunner("/test/overlay")
			mock.PushFunc = func() error {
				return errors.New(tc.errorMsg)
			}

			result, err := PushWithExecutor(mock)

			if tc.expectError {
				if err == nil {
					t.Error("Expected error, got nil")
				}
				if result != nil {
					t.Error("Expected nil result on error")
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
				if result == nil {
					t.Fatal("Expected result, got nil")
				}
				if result.UpToDate != tc.expectUpToDate {
					t.Errorf("UpToDate = %v, want %v", result.UpToDate, tc.expectUpToDate)
				}
			}
		})
	}
}
