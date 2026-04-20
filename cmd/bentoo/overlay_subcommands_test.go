package main

import (
	"strings"
	"testing"
)

// findOverlaySubcommand returns the overlay subcommand with the given use prefix, or nil.
func findOverlaySubcommand(t *testing.T, usePrefix string) interface{ GetUse() string } { //nolint:unused // test helper
	t.Helper()
	for _, cmd := range overlayCmd.Commands() {
		if cmd.Use == usePrefix || strings.HasPrefix(cmd.Use, usePrefix+" ") || strings.HasPrefix(cmd.Use, usePrefix+"\n") {
			return nil // just used for existence check below
		}
	}
	return nil
}

// overlaySubcmdExists returns true if an overlay subcommand with the given use prefix exists.
func overlaySubcmdExists(usePrefix string) bool {
	for _, cmd := range overlayCmd.Commands() {
		if cmd.Use == usePrefix || strings.HasPrefix(cmd.Use, usePrefix+" ") {
			return true
		}
	}
	return false
}

// TestOverlayExtendedSubcommands tests Requirement 9.4-9.9: overlay subcommands are registered.
func TestOverlayExtendedSubcommands(t *testing.T) {
	expected := []string{"compare", "sync", "diff", "init", "log"}
	for _, name := range expected {
		t.Run(name, func(t *testing.T) {
			if !overlaySubcmdExists(name) {
				t.Errorf("overlay %s subcommand should be registered", name)
			}
		})
	}
}

// TestOverlaySubcommandsHaveDescriptions tests that all overlay subcommands have descriptions.
func TestOverlaySubcommandsHaveDescriptions(t *testing.T) {
	names := []string{"compare", "sync", "diff", "init", "log"}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			for _, cmd := range overlayCmd.Commands() {
				if cmd.Use == name || strings.HasPrefix(cmd.Use, name+" ") {
					if cmd.Short == "" {
						t.Errorf("overlay %s should have a short description", name)
					}
					if cmd.Long == "" {
						t.Errorf("overlay %s should have a long description", name)
					}
					return
				}
			}
		})
	}
}

// TestOverlaySubcommandsHaveRunFunc tests that overlay subcommands have a Run function set.
func TestOverlaySubcommandsHaveRunFunc(t *testing.T) {
	cmds := map[string]interface{ GetUse() string }{
		"compare": nil,
		"sync":    nil,
		"diff":    nil,
		"log":     nil,
	}
	_ = cmds
	for _, cmd := range overlayCmd.Commands() {
		switch {
		case cmd.Use == "compare" || strings.HasPrefix(cmd.Use, "compare "):
			if cmd.Run == nil {
				t.Error("overlay compare should have a Run function")
			}
		case cmd.Use == "sync":
			if cmd.Run == nil {
				t.Error("overlay sync should have a Run function")
			}
		case cmd.Use == "diff" || strings.HasPrefix(cmd.Use, "diff "):
			if cmd.Run == nil {
				t.Error("overlay diff should have a Run function")
			}
		case cmd.Use == "log":
			if cmd.Run == nil {
				t.Error("overlay log should have a Run function")
			}
		}
	}
}

// TestCompareCommandFlags tests Requirement 9.5: compare command flags are registered.
func TestCompareCommandFlags(t *testing.T) {
	tests := []struct {
		flagName string
		flagType string
	}{
		{"clone", "bool"},
		{"cache-dir", "string"},
		{"no-cache", "bool"},
		{"timeout", "int"},
		{"token", "string"},
		{"only-outdated", "bool"},
	}
	for _, tt := range tests {
		t.Run(tt.flagName, func(t *testing.T) {
			flag := compareCmd.Flags().Lookup(tt.flagName)
			if flag == nil {
				t.Fatalf("compare command should have --%s flag", tt.flagName)
			}
			if flag.Value.Type() != tt.flagType {
				t.Errorf("--%s should be %s type, got %s", tt.flagName, tt.flagType, flag.Value.Type())
			}
		})
	}
}

// TestCompareCommandFlagDefaults tests that compare command flags have correct defaults.
func TestCompareCommandFlagDefaults(t *testing.T) {
	tests := []struct {
		flagName     string
		defaultValue string
	}{
		{"clone", "false"},
		{"no-cache", "false"},
		{"only-outdated", "false"},
		{"timeout", "30"},
	}
	for _, tt := range tests {
		t.Run(tt.flagName, func(t *testing.T) {
			flag := compareCmd.Flags().Lookup(tt.flagName)
			if flag == nil {
				t.Fatalf("flag --%s not found", tt.flagName)
			}
			if flag.DefValue != tt.defaultValue {
				t.Errorf("--%s default = %q, want %q", tt.flagName, flag.DefValue, tt.defaultValue)
			}
		})
	}
}

// TestDiffCommandFlags tests Requirement 9.7: diff command has --staged flag.
func TestDiffCommandFlags(t *testing.T) {
	flag := diffCmd.Flags().Lookup("staged")
	if flag == nil {
		t.Fatal("diff command should have --staged flag")
	}
	if flag.Value.Type() != "bool" {
		t.Errorf("--staged should be bool type, got %s", flag.Value.Type())
	}
	sh := diffCmd.Flags().ShorthandLookup("s")
	if sh == nil {
		t.Error("--staged should have -s shorthand")
	}
}

// TestLogCommandFlags tests Requirement 9.9: log command has --count and --oneline flags.
func TestLogCommandFlags(t *testing.T) {
	tests := []struct {
		flagName  string
		shorthand string
		flagType  string
		defValue  string
	}{
		{"count", "n", "int", "10"},
		{"oneline", "o", "bool", "false"},
	}
	for _, tt := range tests {
		t.Run(tt.flagName, func(t *testing.T) {
			flag := logCmd.Flags().Lookup(tt.flagName)
			if flag == nil {
				t.Fatalf("log command should have --%s flag", tt.flagName)
			}
			if flag.Value.Type() != tt.flagType {
				t.Errorf("--%s should be %s type, got %s", tt.flagName, tt.flagType, flag.Value.Type())
			}
			if flag.DefValue != tt.defValue {
				t.Errorf("--%s default = %q, want %q", tt.flagName, flag.DefValue, tt.defValue)
			}
			sh := logCmd.Flags().ShorthandLookup(tt.shorthand)
			if sh == nil {
				t.Errorf("--%s should have -%s shorthand", tt.flagName, tt.shorthand)
			}
		})
	}
}

// TestInitCommandRegistered tests Requirement 9.8: init command is registered with correct usage.
func TestInitCommandRegistered(t *testing.T) {
	if initCmd.Use == "" {
		t.Error("init command should have a Use field")
	}
	if initCmd.Short == "" {
		t.Error("init command should have a Short description")
	}
	if initCmd.Run == nil {
		t.Error("init command should have a Run function")
	}
}

// TestSyncCommandRegistered tests Requirement 9.6: sync command is registered with correct usage.
func TestSyncCommandRegistered(t *testing.T) {
	if syncCmd.Use == "" {
		t.Error("sync command should have a Use field")
	}
	if syncCmd.Short == "" {
		t.Error("sync command should have a Short description")
	}
	if syncCmd.Run == nil {
		t.Error("sync command should have a Run function")
	}
}

// TestCompareCommandUsage tests Requirement 9.5: compare command has correct usage info.
func TestCompareCommandUsage(t *testing.T) {
	if compareCmd.Use == "" {
		t.Error("compare command should have a Use field")
	}
	if compareCmd.Short == "" {
		t.Error("compare command should have a Short description")
	}
	if compareCmd.Long == "" {
		t.Error("compare command should have a Long description")
	}
}

// TestOverlaySubcommandsWithoutConfig tests Requirement 9.4: overlay subcommands have Run
// functions that would handle missing config (structure test — execution would call os.Exit).
func TestOverlaySubcommandsWithoutConfig(t *testing.T) {
	// Verify that commands requiring config have their Run functions set.
	// Actual execution without config calls os.Exit(1), so we verify structure only.
	cmdsRequiringConfig := []struct {
		name string
		use  string
	}{
		{"compare", "compare"},
		{"sync", "sync"},
		{"diff", "diff"},
		{"log", "log"},
	}
	for _, tc := range cmdsRequiringConfig {
		t.Run(tc.name, func(t *testing.T) {
			for _, cmd := range overlayCmd.Commands() {
				if cmd.Use == tc.use || strings.HasPrefix(cmd.Use, tc.use+" ") {
					if cmd.Run == nil {
						t.Errorf("overlay %s should have a Run function that handles missing config", tc.name)
					}
					return
				}
			}
			t.Errorf("overlay %s subcommand not found", tc.name)
		})
	}
}
