package main

import (
	"strings"
	"testing"
)

// TestPushCmd_HasRunFunction verifies that the push command has a Run or RunE function set.
func TestPushCmd_HasRunFunction(t *testing.T) {
	if pushCmd.Run == nil && pushCmd.RunE == nil {
		t.Error("push command should have a Run or RunE function")
	}
}

// TestPushCmd_CommandUse verifies that the push command Use field contains "push".
func TestPushCmd_CommandUse(t *testing.T) {
	if !strings.Contains(pushCmd.Use, "push") {
		t.Errorf("push command Use should contain 'push', got %q", pushCmd.Use)
	}
}

// TestPushCmd_HasDryRunFlag verifies that the push command registers a --dry-run flag.
func TestPushCmd_HasDryRunFlag(t *testing.T) {
	flag := pushCmd.Flags().Lookup("dry-run")
	if flag == nil {
		t.Fatal("push command should have --dry-run flag")
	}
	if flag.Value.Type() != "bool" {
		t.Errorf("--dry-run should be bool type, got %s", flag.Value.Type())
	}
	if flag.DefValue != "false" {
		t.Errorf("--dry-run default should be false, got %q", flag.DefValue)
	}
}

// TestPushCmd_HasShortDescription verifies the push command has non-empty descriptions.
func TestPushCmd_HasShortDescription(t *testing.T) {
	if pushCmd.Short == "" {
		t.Error("push command should have a Short description")
	}
	if pushCmd.Long == "" {
		t.Error("push command should have a Long description")
	}
}

// TestPushCmd_IsRegisteredUnderOverlay verifies the push command is a child of overlayCmd.
func TestPushCmd_IsRegisteredUnderOverlay(t *testing.T) {
	found := false
	for _, cmd := range overlayCmd.Commands() {
		if cmd.Use == "push" || strings.HasPrefix(cmd.Use, "push ") {
			found = true
			break
		}
	}
	if !found {
		t.Error("push command should be registered under overlay command")
	}
}
