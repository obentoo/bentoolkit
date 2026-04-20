package main

import (
	"strings"
	"testing"
)

// TestInitCmd_HasRunFunction verifies that the init command has a Run or RunE function set.
func TestInitCmd_HasRunFunction(t *testing.T) {
	if initCmd.Run == nil && initCmd.RunE == nil {
		t.Error("init command should have a Run or RunE function")
	}
}

// TestInitCmd_CommandUse verifies that the init command Use field contains "init".
func TestInitCmd_CommandUse(t *testing.T) {
	if !strings.Contains(initCmd.Use, "init") {
		t.Errorf("init command Use should contain 'init', got %q", initCmd.Use)
	}
}

// TestInitCmd_HasShortDescription verifies the init command has non-empty descriptions.
func TestInitCmd_HasShortDescription(t *testing.T) {
	if initCmd.Short == "" {
		t.Error("init command should have a Short description")
	}
	if initCmd.Long == "" {
		t.Error("init command should have a Long description")
	}
}

// TestInitCmd_IsRegisteredUnderOverlay verifies the init command is a child of overlayCmd.
func TestInitCmd_IsRegisteredUnderOverlay(t *testing.T) {
	found := false
	for _, cmd := range overlayCmd.Commands() {
		if cmd.Use == "init" || strings.HasPrefix(cmd.Use, "init ") {
			found = true
			break
		}
	}
	if !found {
		t.Error("init command should be registered under overlay command")
	}
}

// TestInitCmd_UsesSetupTestOverlay verifies that setupTestOverlay creates a valid overlay structure.
// This exercises the test helper to ensure it works correctly.
func TestInitCmd_UsesSetupTestOverlay(t *testing.T) {
	dir := setupTestOverlay(t)
	if dir == "" {
		t.Fatal("setupTestOverlay should return a non-empty path")
	}
}
