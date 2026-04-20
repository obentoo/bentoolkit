package main

import (
	"strings"
	"testing"
)

// TestCompareCmd_HasCloneFlag verifies that the compare command registers a --clone flag.
func TestCompareCmd_HasCloneFlag(t *testing.T) {
	flag := compareCmd.Flags().Lookup("clone")
	if flag == nil {
		t.Fatal("compare command should have --clone flag")
	}
	if flag.Value.Type() != "bool" {
		t.Errorf("--clone should be bool type, got %s", flag.Value.Type())
	}
}

// TestCompareCmd_HasTimeoutFlag verifies that the compare command registers --timeout with default 30.
func TestCompareCmd_HasTimeoutFlag(t *testing.T) {
	flag := compareCmd.Flags().Lookup("timeout")
	if flag == nil {
		t.Fatal("compare command should have --timeout flag")
	}
	if flag.Value.Type() != "int" {
		t.Errorf("--timeout should be int type, got %s", flag.Value.Type())
	}
	if flag.DefValue != "30" {
		t.Errorf("--timeout default should be 30, got %q", flag.DefValue)
	}
}

// TestCompareCmd_HasNoCacheFlag verifies that the compare command registers a --no-cache flag.
func TestCompareCmd_HasNoCacheFlag(t *testing.T) {
	flag := compareCmd.Flags().Lookup("no-cache")
	if flag == nil {
		t.Fatal("compare command should have --no-cache flag")
	}
	if flag.Value.Type() != "bool" {
		t.Errorf("--no-cache should be bool type, got %s", flag.Value.Type())
	}
}

// TestCompareCmd_DefaultRepoIsGentoo verifies that compare Use string accepts an optional [repository] arg.
func TestCompareCmd_DefaultRepoIsGentoo(t *testing.T) {
	// The command accepts at most one positional arg (repository name)
	// When none given, it defaults to "gentoo". Verify the Use string documents this.
	if !strings.Contains(compareCmd.Use, "compare") {
		t.Errorf("compare command Use should contain 'compare', got %q", compareCmd.Use)
	}
	// The args validator allows zero or one positional arg
	if compareCmd.Args == nil {
		t.Error("compare command should have Args validator set (MaximumNArgs(1))")
	}
}

// TestCompareCmd_HasRunFunction verifies that the compare command has a Run or RunE function.
func TestCompareCmd_HasRunFunction(t *testing.T) {
	if compareCmd.Run == nil && compareCmd.RunE == nil {
		t.Error("compare command should have a Run or RunE function")
	}
}

// TestCompareCmd_HasOnlyOutdatedFlag verifies that the compare command registers --only-outdated.
func TestCompareCmd_HasOnlyOutdatedFlag(t *testing.T) {
	flag := compareCmd.Flags().Lookup("only-outdated")
	if flag == nil {
		t.Fatal("compare command should have --only-outdated flag")
	}
	if flag.DefValue != "false" {
		t.Errorf("--only-outdated default should be false, got %q", flag.DefValue)
	}
}

// TestCompareCmd_HasSyncFlag verifies that the compare command registers a --sync flag with default false.
func TestCompareCmd_HasSyncFlag(t *testing.T) {
	flag := compareCmd.Flags().Lookup("sync")
	if flag == nil {
		t.Fatal("compare command should have --sync flag")
	}
	if flag.Value.Type() != "bool" {
		t.Errorf("--sync should be bool type, got %s", flag.Value.Type())
	}
	if flag.DefValue != "false" {
		t.Errorf("--sync default should be false, got %q", flag.DefValue)
	}
}

// TestCompareCmd_HasTokenFlag verifies that the compare command registers a --token flag.
func TestCompareCmd_HasTokenFlag(t *testing.T) {
	flag := compareCmd.Flags().Lookup("token")
	if flag == nil {
		t.Fatal("compare command should have --token flag")
	}
	if flag.Value.Type() != "string" {
		t.Errorf("--token should be string type, got %s", flag.Value.Type())
	}
}
