package overlay

import (
	"testing"

	"github.com/lucascouts/bentoo-tools/internal/common/config"
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
