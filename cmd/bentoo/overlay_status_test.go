package main

import (
	"strings"
	"testing"
)

// TestStatusCmd_HasRunFunction verifies that the status command has a Run or RunE function set.
func TestStatusCmd_HasRunFunction(t *testing.T) {
	if statusCmd.Run == nil && statusCmd.RunE == nil {
		t.Error("status command should have a Run or RunE function")
	}
}

// TestStatusCmd_CommandUse verifies that the status command Use field contains "status".
func TestStatusCmd_CommandUse(t *testing.T) {
	if !strings.Contains(statusCmd.Use, "status") {
		t.Errorf("status command Use should contain 'status', got %q", statusCmd.Use)
	}
}

// TestStatusCmd_HasShortDescription verifies the status command has non-empty descriptions.
func TestStatusCmd_HasShortDescription(t *testing.T) {
	if statusCmd.Short == "" {
		t.Error("status command should have a Short description")
	}
	if statusCmd.Long == "" {
		t.Error("status command should have a Long description")
	}
}

// TestStatusCmd_IsRegisteredUnderOverlay verifies the status command is a child of overlayCmd.
func TestStatusCmd_IsRegisteredUnderOverlay(t *testing.T) {
	found := false
	for _, cmd := range overlayCmd.Commands() {
		if cmd.Use == "status" || strings.HasPrefix(cmd.Use, "status ") {
			found = true
			break
		}
	}
	if !found {
		t.Error("status command should be registered under overlay command")
	}
}
