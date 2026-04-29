package main

import (
	"strings"
	"testing"
)

func TestManifestCommandRegistered(t *testing.T) {
	found := false
	for _, cmd := range overlayCmd.Commands() {
		if cmd.Use == "manifest" || strings.HasPrefix(cmd.Use, "manifest ") {
			found = true
			if cmd.Short == "" {
				t.Error("manifest command should have a Short description")
			}
			if cmd.Long == "" {
				t.Error("manifest command should have a Long description")
			}
			if cmd.Run == nil {
				t.Error("manifest command should have a Run function")
			}
			break
		}
	}
	if !found {
		t.Error("overlay manifest subcommand should be registered")
	}
}

func TestManifestCommandFlags(t *testing.T) {
	tests := []struct {
		flagName  string
		shorthand string
		flagType  string
		defValue  string
	}{
		{"keep", "", "bool", "false"},
		{"dry-run", "n", "bool", "false"},
		{"jobs", "j", "int", "10"},
	}
	for _, tt := range tests {
		t.Run(tt.flagName, func(t *testing.T) {
			flag := manifestCmd.Flags().Lookup(tt.flagName)
			if flag == nil {
				t.Fatalf("manifest command should have --%s flag", tt.flagName)
			}
			if flag.Value.Type() != tt.flagType {
				t.Errorf("--%s should be %s type, got %s", tt.flagName, tt.flagType, flag.Value.Type())
			}
			if flag.DefValue != tt.defValue {
				t.Errorf("--%s default = %q, want %q", tt.flagName, flag.DefValue, tt.defValue)
			}
			if tt.shorthand != "" {
				sh := manifestCmd.Flags().ShorthandLookup(tt.shorthand)
				if sh == nil {
					t.Errorf("--%s should have -%s shorthand", tt.flagName, tt.shorthand)
				}
			}
		})
	}
}

func TestManifestCommandArgsAcceptsZeroOrOne(t *testing.T) {
	if err := manifestCmd.Args(manifestCmd, []string{}); err != nil {
		t.Errorf("manifest should accept zero args, got error: %v", err)
	}
	if err := manifestCmd.Args(manifestCmd, []string{"app-misc"}); err != nil {
		t.Errorf("manifest should accept one arg, got error: %v", err)
	}
	if err := manifestCmd.Args(manifestCmd, []string{"a", "b"}); err == nil {
		t.Error("manifest should reject two args")
	}
}
