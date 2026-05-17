package main

import (
	"os"
	"strings"
	"testing"

	"github.com/fatih/color"
	"github.com/obentoo/bentoolkit/internal/common/version"
)

// TestVersionCommand tests Requirement 9.1: version command is registered and has a Run function.
func TestVersionCommand(t *testing.T) {
	if versionCmd.Run == nil {
		t.Fatal("version command should have a Run function")
	}
	if versionCmd.Use != "version" {
		t.Errorf("version command Use = %q, want %q", versionCmd.Use, "version")
	}
}

// TestVersionInfoOutput tests that version.Info() output contains all expected fields.
// The version command calls fmt.Println(version.Info()), so we validate the output directly.
func TestVersionInfoOutput(t *testing.T) {
	out := version.Info()
	fields := []string{"bentoo version", "commit:", "built:", "go:", "os/arch:"}
	for _, field := range fields {
		if !strings.Contains(out, field) {
			t.Errorf("version.Info() should contain %q, got: %q", field, out)
		}
	}
}

// TestCompletionCommandRegistration tests Requirement 9.2: completion command is registered
// with correct valid args for all supported shells.
func TestCompletionCommandRegistration(t *testing.T) {
	if completionCmd.Run == nil {
		t.Fatal("completion command should have a Run function")
	}
	expectedShells := []string{"bash", "zsh", "fish", "powershell"}
	for _, shell := range expectedShells {
		t.Run(shell, func(t *testing.T) {
			found := false
			for _, valid := range completionCmd.ValidArgs {
				if valid == shell {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("completion command should accept %q as a valid arg", shell)
			}
		})
	}
}

// TestCompletionCommandInvalidShell tests that an invalid shell argument returns an error.
func TestCompletionCommandInvalidShell(t *testing.T) {
	_, err := executeCommand(rootCmd, "completion", "invalidshell")
	if err == nil {
		t.Error("completion with invalid shell should return an error")
	}
}

// TestCompletionCommandRequiresArg tests that completion without args returns an error.
func TestCompletionCommandRequiresArg(t *testing.T) {
	_, err := executeCommand(rootCmd, "completion")
	if err == nil {
		t.Error("completion without args should return an error")
	}
}

// TestGlobalFlagsRegistered tests Requirement 9.3: global flags are registered on the root command.
func TestGlobalFlagsRegistered(t *testing.T) {
	tests := []struct {
		flagName  string
		shorthand string
		flagType  string
	}{
		{"verbose", "v", "bool"},
		{"quiet", "q", "bool"},
		{"no-color", "", "bool"},
	}
	for _, tt := range tests {
		t.Run(tt.flagName, func(t *testing.T) {
			flag := rootCmd.PersistentFlags().Lookup(tt.flagName)
			if flag == nil {
				t.Fatalf("root command should have --%s persistent flag", tt.flagName)
			}
			if flag.Value.Type() != tt.flagType {
				t.Errorf("--%s should be %s type, got %s", tt.flagName, tt.flagType, flag.Value.Type())
			}
			if tt.shorthand != "" {
				sh := rootCmd.PersistentFlags().ShorthandLookup(tt.shorthand)
				if sh == nil {
					t.Errorf("--%s should have -%s shorthand", tt.flagName, tt.shorthand)
				}
			}
		})
	}
}

// TestGlobalFlagsDefaults tests that global flags have correct default values.
func TestGlobalFlagsDefaults(t *testing.T) {
	tests := []struct {
		flagName     string
		defaultValue string
	}{
		{"verbose", "false"},
		{"quiet", "false"},
		{"no-color", "false"},
	}
	for _, tt := range tests {
		t.Run(tt.flagName, func(t *testing.T) {
			flag := rootCmd.PersistentFlags().Lookup(tt.flagName)
			if flag == nil {
				t.Fatalf("flag --%s not found", tt.flagName)
			}
			if flag.DefValue != tt.defaultValue {
				t.Errorf("--%s default = %q, want %q", tt.flagName, flag.DefValue, tt.defaultValue)
			}
		})
	}
}

// TestRootCommandHasPersistentPreRun tests that PersistentPreRun is configured for global flags.
func TestRootCommandHasPersistentPreRun(t *testing.T) {
	if rootCmd.PersistentPreRun == nil {
		t.Error("rootCmd should have PersistentPreRun configured for global flag handling")
	}
}

// TestVersionCommandRegistered tests that version command is registered on root.
func TestVersionCommandRegistered(t *testing.T) {
	found := false
	for _, cmd := range rootCmd.Commands() {
		if cmd.Use == "version" {
			found = true
			break
		}
	}
	if !found {
		t.Error("version command should be registered on root command")
	}
}

// TestCompletionCommandRegistered tests that completion command is registered on root.
func TestCompletionCommandRegistered(t *testing.T) {
	found := false
	for _, cmd := range rootCmd.Commands() {
		if strings.HasPrefix(cmd.Use, "completion") {
			found = true
			break
		}
	}
	if !found {
		t.Error("completion command should be registered on root command")
	}
}

// TestCompletionScriptGeneration tests Requirement 9.2: completion scripts are actually generated.
// completion.go writes directly to os.Stdout, so we capture it with os.Pipe.
func TestCompletionScriptGeneration(t *testing.T) {
	shells := []struct {
		name    string
		keyword string
	}{
		{"bash", "bash"},
		{"zsh", "zsh"},
		{"fish", "fish"},
		{"powershell", "powershell"},
	}

	for _, tt := range shells {
		t.Run(tt.name, func(t *testing.T) {
			// completion.go writes to os.Stdout directly, so os.Stdout must be
			// redirected to capture the generated script. A temp file is used
			// rather than an os.Pipe: a pipe's write blocks once its ~64 KiB
			// kernel buffer fills with no concurrent reader, which made this
			// test deadlock intermittently. A regular file never blocks.
			oldStdout := os.Stdout
			tmp, err := os.CreateTemp(t.TempDir(), "completion-*")
			if err != nil {
				t.Fatalf("failed to create temp file: %v", err)
			}
			os.Stdout = tmp

			// Reset args and run completion command
			rootCmd.SetArgs([]string{"completion", tt.name})
			runErr := rootCmd.Execute()

			os.Stdout = oldStdout
			if closeErr := tmp.Close(); closeErr != nil {
				t.Fatalf("failed to close temp file: %v", closeErr)
			}

			out, readErr := os.ReadFile(tmp.Name())
			if readErr != nil {
				t.Fatalf("failed to read temp file: %v", readErr)
			}

			if runErr != nil {
				t.Errorf("completion %s returned error: %v", tt.name, runErr)
			}
			if len(out) == 0 {
				t.Errorf("completion %s produced no output", tt.name)
			}
		})
	}
}

// TestVerboseFlagBehavior tests Requirement 9.3: --verbose flag runs without error
// and PersistentPreRun is invoked (logger.SetVerbose called).
func TestVerboseFlagBehavior(t *testing.T) {
	// Reset flag state before test
	if err := rootCmd.PersistentFlags().Set("verbose", "false"); err != nil {
		t.Fatalf("failed to reset verbose flag: %v", err)
	}

	_, err := executeCommand(rootCmd, "--verbose", "version")
	if err != nil {
		t.Errorf("--verbose flag should not cause an error, got: %v", err)
	}

	// Cleanup: reset flag
	_ = rootCmd.PersistentFlags().Set("verbose", "false")
}

// TestQuietFlagBehavior tests Requirement 9.3: --quiet flag runs without error
// and PersistentPreRun is invoked (logger.SetQuiet called).
func TestQuietFlagBehavior(t *testing.T) {
	if err := rootCmd.PersistentFlags().Set("quiet", "false"); err != nil {
		t.Fatalf("failed to reset quiet flag: %v", err)
	}

	_, err := executeCommand(rootCmd, "--quiet", "version")
	if err != nil {
		t.Errorf("--quiet flag should not cause an error, got: %v", err)
	}

	_ = rootCmd.PersistentFlags().Set("quiet", "false")
}

// TestNoColorFlagBehavior tests Requirement 9.3: --no-color flag disables ANSI color output.
// output.NoColor() sets color.NoColor = true in the fatih/color package.
func TestNoColorFlagBehavior(t *testing.T) {
	if err := rootCmd.PersistentFlags().Set("no-color", "false"); err != nil {
		t.Fatalf("failed to reset no-color flag: %v", err)
	}

	_, err := executeCommand(rootCmd, "--no-color", "version")
	if err != nil {
		t.Errorf("--no-color flag should not cause an error, got: %v", err)
	}

	// Verify color was disabled (fatih/color exposes color.NoColor)
	if !color.NoColor {
		t.Error("--no-color flag should set color.NoColor = true")
	}

	// Cleanup: re-enable color
	color.NoColor = false
	_ = rootCmd.PersistentFlags().Set("no-color", "false")
}
