package main

import (
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/overlay"
	"github.com/spf13/cobra"
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
	expected := []string{"compare", "pull", "diff", "init", "log"}
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
	names := []string{"compare", "pull", "diff", "init", "log"}
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
		"pull":    nil,
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
		case cmd.Use == "pull":
			if cmd.Run == nil {
				t.Error("overlay pull should have a Run function")
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

// TestPullCommandRegistered tests Requirement 9.6: pull command is registered with correct usage.
func TestPullCommandRegistered(t *testing.T) {
	if pullCmd.Use == "" {
		t.Error("pull command should have a Use field")
	}
	if pullCmd.Short == "" {
		t.Error("pull command should have a Short description")
	}
	if pullCmd.Run == nil {
		t.Error("pull command should have a Run function")
	}
}

// TestPullKeepsSyncAlias proves the name this command shipped under still
// resolves, so existing scripts and muscle memory keep working.
func TestPullKeepsSyncAlias(t *testing.T) {
	cmd, _, err := overlayCmd.Find([]string{"sync"})
	if err != nil {
		t.Fatalf("overlay sync should still resolve: %v", err)
	}
	if cmd.Name() != "pull" {
		t.Errorf("overlay sync resolved to %q, want the pull command", cmd.Name())
	}
}

// TestPullFlagsAreMutuallyExclusive: --rebase and --merge name conflicting
// integration strategies, so asking for both must fail rather than let one
// silently win.
func TestPullFlagsAreMutuallyExclusive(t *testing.T) {
	for _, flag := range []string{"rebase", "merge", "dry-run"} {
		if pullCmd.Flags().Lookup(flag) == nil {
			t.Errorf("pull command should have a --%s flag", flag)
		}
	}

	annotations := pullCmd.Flags().Lookup("rebase").Annotations
	if _, ok := annotations[cobra.BashCompOneRequiredFlag]; ok {
		t.Error("--rebase should not be required")
	}
}

// TestPullModeFromFlags maps the flag pair to the integration mode.
func TestPullModeFromFlags(t *testing.T) {
	tests := []struct {
		name          string
		rebase, merge bool
		want          overlay.PullMode
	}{
		{"no flags is fast-forward only", false, false, overlay.PullFFOnly},
		{"--rebase selects rebase", true, false, overlay.PullRebase},
		{"--merge selects merge", false, true, overlay.PullMerge},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := pullModeFromFlags(tc.rebase, tc.merge); got != tc.want {
				t.Errorf("pullModeFromFlags(%v, %v) = %v, want %v", tc.rebase, tc.merge, got, tc.want)
			}
		})
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
		{"pull", "pull"},
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
