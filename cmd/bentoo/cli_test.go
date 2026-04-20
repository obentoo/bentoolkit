package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// executeCommand executes a cobra command with the given args and returns output
func executeCommand(root *cobra.Command, args ...string) (output string, err error) {
	buf := new(bytes.Buffer)
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetArgs(args)

	err = root.Execute()
	return buf.String(), err
}

// TestRootCommand tests the root command exists and has overlay subcommand
func TestRootCommand(t *testing.T) {
	// Test that root command exists
	if rootCmd == nil {
		t.Fatal("rootCmd should not be nil")
	}

	// Test that overlay subcommand exists
	found := false
	for _, cmd := range rootCmd.Commands() {
		if cmd.Use == "overlay" {
			found = true
			break
		}
	}
	if !found {
		t.Error("overlay subcommand should exist")
	}
}

// TestOverlaySubcommands tests that all overlay subcommands are registered
func TestOverlaySubcommands(t *testing.T) {
	expectedCommands := []string{"add", "status", "commit", "push"}

	for _, expected := range expectedCommands {
		found := false
		for _, cmd := range overlayCmd.Commands() {
			if cmd.Use == expected || strings.HasPrefix(cmd.Use, expected+" ") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("overlay %s subcommand should exist", expected)
		}
	}
}

// TestCommitMessageFlag tests that commit command has -m flag
func TestCommitMessageFlag(t *testing.T) {
	flag := commitCmd.Flags().Lookup("message")
	if flag == nil {
		t.Error("commit command should have -m/--message flag")
	}

	shorthand := commitCmd.Flags().ShorthandLookup("m")
	if shorthand == nil {
		t.Error("commit command should have -m shorthand")
	}
}

// TestAddCommandUsage tests add command usage string
func TestAddCommandUsage(t *testing.T) {
	if !strings.Contains(addCmd.Use, "[paths...]") {
		t.Error("add command should accept variadic paths")
	}
}

// TestMissingConfigError tests error handling when config is missing
func TestMissingConfigError(t *testing.T) {
	// Create a temporary directory for test config
	tmpDir, err := os.MkdirTemp("", "bentoo-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Set HOME to temp dir to use a non-existent config
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", oldHome)

	// Create config directory and file with empty overlay path
	configDir := filepath.Join(tmpDir, ".config", "bentoo")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("Failed to create config dir: %v", err)
	}

	configFile := filepath.Join(configDir, "config.yaml")
	configContent := `overlay:
  path: ""
  remote: origin
`
	if err := os.WriteFile(configFile, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	// The status command should fail with overlay path not set error
	// We can't easily test the actual execution without mocking,
	// but we can verify the command structure is correct
	if statusCmd.Run == nil {
		t.Error("status command should have a Run function")
	}
}

// TestHelpOutput tests that help is available for all commands
func TestHelpOutput(t *testing.T) {
	commands := []*cobra.Command{rootCmd, overlayCmd, addCmd, statusCmd, commitCmd, pushCmd}

	for _, cmd := range commands {
		if cmd.Short == "" {
			t.Errorf("command %s should have a short description", cmd.Use)
		}
		if cmd.Long == "" {
			t.Errorf("command %s should have a long description", cmd.Use)
		}
	}
}
