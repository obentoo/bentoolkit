package main

import (
	"strings"
	"testing"

	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

// ---- diff tests ----

// TestDiffWithValidPath tests that runDiff accepts a valid file path argument.
// **Feature: quality-improvements, Requirement 12.1**
func TestDiffWithValidPath(t *testing.T) {
	overlayDir, cleanup := setupTestHomeWithGitRepo(t)
	defer cleanup()

	origStaged := diffStaged
	diffStaged = false
	defer func() { diffStaged = origStaged }()

	// A valid path should not cause exit(1) from validation
	code := withExitIntercept(func() { runDiff(diffCmd, []string{overlayDir}) })
	if code == 1 {
		t.Errorf("runDiff with valid path should not exit(1), got exit(%d)", code)
	}
}

// TestDiffWithStagedFlag tests that runDiff works with --staged flag.
// **Feature: quality-improvements, Requirement 12.2**
func TestDiffWithStagedFlag(t *testing.T) {
	_, cleanup := setupTestHomeWithGitRepo(t)
	defer cleanup()

	origStaged := diffStaged
	diffStaged = true
	defer func() { diffStaged = origStaged }()

	// --staged should not cause a validation error
	code := withExitIntercept(func() { runDiff(diffCmd, nil) })
	if code == 1 {
		t.Errorf("runDiff with --staged should not exit(1), got exit(%d)", code)
	}
}

// TestDiffRejectsGitFlags tests that runDiff rejects flag-like positional arguments.
// **Feature: quality-improvements, Requirement 12.3**
func TestDiffRejectsGitFlags(t *testing.T) {
	_, cleanup := setupTestHome(t)
	defer cleanup()

	origStaged := diffStaged
	diffStaged = false
	defer func() { diffStaged = origStaged }()

	tests := []struct {
		name string
		args []string
	}{
		{"exec flag", []string{"--exec=malicious"}},
		{"upload-pack flag", []string{"--upload-pack=evil"}},
		{"single dash flag", []string{"-x"}},
		{"mixed valid and flag", []string{"valid/path", "--exec=bad"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code := withExitIntercept(func() { runDiff(diffCmd, tt.args) })
			if code != 1 {
				t.Errorf("runDiff with %v should exit(1), got exit(%d)", tt.args, code)
			}
		})
	}
}

// ---- log tests ----

// TestLogDefaultCount tests that log command defaults to 10 commits.
// **Feature: quality-improvements, Requirement 12.4**
func TestLogDefaultCount(t *testing.T) {
	flag := logCmd.Flags().Lookup("count")
	if flag == nil {
		t.Fatal("log command should have --count flag")
	}
	if flag.DefValue != "10" {
		t.Errorf("--count default should be 10, got %q", flag.DefValue)
	}

	_, cleanup := setupTestHomeWithGitRepo(t)
	defer cleanup()

	origCount, origOneline := logCount, logOneline
	logCount = 10
	logOneline = false
	defer func() { logCount = origCount; logOneline = origOneline }()

	// Default count should work without errors
	withExitIntercept(func() { runLog(logCmd, nil) })
}

// TestLogCustomCount tests that log command accepts a custom count.
// **Feature: quality-improvements, Requirement 12.5**
func TestLogCustomCount(t *testing.T) {
	_, cleanup := setupTestHomeWithGitRepo(t)
	defer cleanup()

	origCount, origOneline := logCount, logOneline
	logCount = 5
	logOneline = false
	defer func() { logCount = origCount; logOneline = origOneline }()

	// Custom count should work without errors
	withExitIntercept(func() { runLog(logCmd, nil) })
}

// TestLogOnelineFlag tests that log command works with --oneline flag.
// **Feature: quality-improvements, Requirement 12.6**
func TestLogOnelineFlag(t *testing.T) {
	_, cleanup := setupTestHomeWithGitRepo(t)
	defer cleanup()

	origCount, origOneline := logCount, logOneline
	logCount = 3
	logOneline = true
	defer func() { logCount = origCount; logOneline = origOneline }()

	// --oneline should work without errors
	withExitIntercept(func() { runLog(logCmd, nil) })
}

// TestLogRejectsPositionalArgs tests that log command rejects extra positional arguments.
// **Feature: quality-improvements, Requirement 12.7**
func TestLogRejectsPositionalArgs(t *testing.T) {
	// Verify cobra.NoArgs is set on the command
	if logCmd.Args == nil {
		t.Fatal("log command should have Args validator set (cobra.NoArgs)")
	}

	// cobra.NoArgs should reject any positional argument
	err := logCmd.Args(logCmd, []string{"extra-arg"})
	if err == nil {
		t.Error("log command should reject positional arguments")
	}

	// Multiple args should also be rejected
	err = logCmd.Args(logCmd, []string{"arg1", "arg2"})
	if err == nil {
		t.Error("log command should reject multiple positional arguments")
	}

	// Empty args should be accepted
	err = logCmd.Args(logCmd, nil)
	if err != nil {
		t.Errorf("log command should accept no args, got error: %v", err)
	}
}

// ---- validateGitPathArgs unit tests ----

// TestValidateGitPathArgsUnit tests validateGitPathArgs with specific examples.
func TestValidateGitPathArgsUnit(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr bool
		errMsg  string
	}{
		{"nil args", nil, false, ""},
		{"empty args", []string{}, false, ""},
		{"valid path", []string{"app-misc/hello/"}, false, ""},
		{"valid multiple paths", []string{"a/b", "c/d"}, false, ""},
		{"single dash flag", []string{"-x"}, true, "-x"},
		{"double dash flag", []string{"--exec=malicious"}, true, "--exec=malicious"},
		{"flag among paths", []string{"valid/path", "--bad"}, true, "--bad"},
		{"just a dash", []string{"-"}, true, "-"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateGitPathArgs(tt.args)
			if tt.wantErr {
				if err == nil {
					t.Errorf("validateGitPathArgs(%v) should return error", tt.args)
				} else if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("error should contain %q, got %q", tt.errMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("validateGitPathArgs(%v) unexpected error: %v", tt.args, err)
				}
			}
		})
	}
}

// ---- Property-Based Tests ----

// TestValidateGitPathArgsRejectsFlags tests Property 2: Git Argument Rejection.
// For any string starting with "-", validateGitPathArgs SHALL return a non-nil error.
// **Feature: quality-improvements, Property 2: Git Argument Rejection**
// **Validates: Requirement 2.1**
func TestValidateGitPathArgsRejectsFlags(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100

	properties := gopter.NewProperties(parameters)

	properties.Property("rejects any string starting with dash", prop.ForAll(
		func(suffix string) bool {
			arg := "-" + suffix
			err := validateGitPathArgs([]string{arg})
			return err != nil
		},
		gen.AlphaString(),
	))

	properties.TestingRun(t)
}

// TestValidateGitPathArgsAcceptsPaths tests Property 3: Git Path Acceptance.
// For any string that does not start with "-", validateGitPathArgs SHALL return nil.
// **Feature: quality-improvements, Property 3: Git Path Acceptance**
// **Validates: Requirement 2.2**
func TestValidateGitPathArgsAcceptsPaths(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 100

	properties := gopter.NewProperties(parameters)

	properties.Property("accepts any string not starting with dash", prop.ForAll(
		func(s string) bool {
			// Ensure the string does not start with "-"
			if len(s) > 0 && s[0] == '-' {
				return true // skip, not in domain
			}
			err := validateGitPathArgs([]string{s})
			return err == nil
		},
		gen.AlphaString(),
	))

	properties.TestingRun(t)
}
