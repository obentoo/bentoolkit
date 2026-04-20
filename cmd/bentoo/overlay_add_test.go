package main

import (
	"strings"
	"testing"
)

// TestAddCmd_AcceptsMultiplePaths verifies that the add command Use contains "[paths...]".
func TestAddCmd_AcceptsMultiplePaths(t *testing.T) {
	if !strings.Contains(addCmd.Use, "[paths...]") {
		t.Errorf("add command Use should contain '[paths...]' to signal variadic args, got %q", addCmd.Use)
	}
}

// TestAddCmd_HasRunFunction verifies that the add command has a Run or RunE function set.
func TestAddCmd_HasRunFunction(t *testing.T) {
	if addCmd.Run == nil && addCmd.RunE == nil {
		t.Error("add command should have a Run or RunE function")
	}
}

// TestAddCmd_CommandUse verifies that the add command Use field begins with "add".
func TestAddCmd_CommandUse(t *testing.T) {
	if !strings.HasPrefix(addCmd.Use, "add") {
		t.Errorf("add command Use should start with 'add', got %q", addCmd.Use)
	}
}

// TestAddCmd_HasShortDescription verifies the add command has non-empty descriptions.
func TestAddCmd_HasShortDescription(t *testing.T) {
	if addCmd.Short == "" {
		t.Error("add command should have a Short description")
	}
	if addCmd.Long == "" {
		t.Error("add command should have a Long description")
	}
}

// TestAddCmd_IsRegisteredUnderOverlay verifies the add command is a child of overlayCmd.
func TestAddCmd_IsRegisteredUnderOverlay(t *testing.T) {
	found := false
	for _, cmd := range overlayCmd.Commands() {
		if cmd.Use == "add" || strings.HasPrefix(cmd.Use, "add ") {
			found = true
			break
		}
	}
	if !found {
		t.Error("add command should be registered under overlay command")
	}
}
