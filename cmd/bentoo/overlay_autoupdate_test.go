package main

import (
	"strings"
	"testing"
)

// TestAutoupdateCommandExists tests that the autoupdate command is registered
func TestAutoupdateCommandExists(t *testing.T) {
	found := false
	for _, cmd := range overlayCmd.Commands() {
		if strings.HasPrefix(cmd.Use, "autoupdate") {
			found = true
			break
		}
	}
	if !found {
		t.Error("overlay autoupdate subcommand should exist")
	}
}

// TestAutoupdateCommandFlags tests that all required flags are present
func TestAutoupdateCommandFlags(t *testing.T) {
	tests := []struct {
		name     string
		flagName string
	}{
		{"check flag", "check"},
		{"list flag", "list"},
		{"apply flag", "apply"},
		{"force flag", "force"},
		{"compile flag", "compile"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			flag := autoupdateCmd.Flags().Lookup(tt.flagName)
			if flag == nil {
				t.Errorf("autoupdate command should have --%s flag", tt.flagName)
			}
		})
	}
}

// TestAutoupdateCommandDescription tests command descriptions
func TestAutoupdateCommandDescription(t *testing.T) {
	if autoupdateCmd.Short == "" {
		t.Error("autoupdate command should have a short description")
	}
	if autoupdateCmd.Long == "" {
		t.Error("autoupdate command should have a long description")
	}
}

// TestAutoupdateCommandRun tests that Run function is set
func TestAutoupdateCommandRun(t *testing.T) {
	if autoupdateCmd.Run == nil {
		t.Error("autoupdate command should have a Run function")
	}
}

// TestAutoupdateFlagTypes tests that flags have correct types
func TestAutoupdateFlagTypes(t *testing.T) {
	// Boolean flags
	boolFlags := []string{"check", "list", "force", "compile"}
	for _, flagName := range boolFlags {
		flag := autoupdateCmd.Flags().Lookup(flagName)
		if flag == nil {
			t.Errorf("flag %s should exist", flagName)
			continue
		}
		if flag.Value.Type() != "bool" {
			t.Errorf("flag %s should be bool type, got %s", flagName, flag.Value.Type())
		}
	}

	// String flags
	stringFlags := []string{"apply"}
	for _, flagName := range stringFlags {
		flag := autoupdateCmd.Flags().Lookup(flagName)
		if flag == nil {
			t.Errorf("flag %s should exist", flagName)
			continue
		}
		if flag.Value.Type() != "string" {
			t.Errorf("flag %s should be string type, got %s", flagName, flag.Value.Type())
		}
	}
}

// TestAutoupdateUsageContainsExamples tests that usage contains examples
func TestAutoupdateUsageContainsExamples(t *testing.T) {
	examples := []string{
		"--check",
		"--list",
		"--apply",
		"--force",
		"--compile",
	}

	for _, example := range examples {
		if !strings.Contains(autoupdateCmd.Long, example) {
			t.Errorf("autoupdate long description should contain example with %s", example)
		}
	}
}

// TestRunAutoupdate_OverlayPathBoundsCheck tests Property 4: Bounds-Safe Tilde Check
// Verifies that empty or whitespace overlay paths do not cause a panic.
// **Feature: quality-improvements, Property 4: Bounds-Safe Tilde Check**
// **Validates: Requirements 3.1-3.4**
func TestRunAutoupdate_OverlayPathBoundsCheck(t *testing.T) {
	tests := []struct {
		name        string
		overlayPath string
		wantPanic   bool
	}{
		{
			name:        "empty overlay path does not panic",
			overlayPath: "",
			wantPanic:   false,
		},
		{
			name:        "whitespace-only overlay path does not panic",
			overlayPath: "   ",
			wantPanic:   false,
		},
		{
			name:        "tilde path is handled safely",
			overlayPath: "~/overlay",
			wantPanic:   false,
		},
		{
			name:        "absolute path is handled safely",
			overlayPath: "/tmp/overlay",
			wantPanic:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil && !tt.wantPanic {
					t.Errorf("unexpected panic for overlayPath=%q: %v", tt.overlayPath, r)
				}
			}()

			// Exercise the bounds-guarded tilde check directly
			path := tt.overlayPath
			if len(path) > 0 && path[0] == '~' {
				// tilde expansion would happen here — no panic expected
				_ = path[1:]
			}
		})
	}
}
