package main

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/common/output"
	"github.com/obentoo/bentoolkit/internal/common/version"
	"github.com/obentoo/bentoolkit/internal/overlay"
)

// ---- displayCheckResults ----

// TestDisplayCheckResultsEmpty tests displayCheckResults with no results (no panic).
func TestDisplayCheckResultsEmpty(t *testing.T) {
	displayCheckResults(nil)
}

// TestDisplayCheckResultsWithUpdate tests displayCheckResults with an update available.
func TestDisplayCheckResultsWithUpdate(t *testing.T) {
	results := []autoupdate.CheckResult{
		{
			Package:         "net-misc/foo",
			CurrentVersion:  "1.0",
			UpstreamVersion: "2.0",
			HasUpdate:       true,
		},
	}
	// Just verify no panic; output goes to real stdout via fatih/color
	displayCheckResults(results)
}

// TestDisplayCheckResultsUpToDate tests displayCheckResults with no updates.
func TestDisplayCheckResultsUpToDate(t *testing.T) {
	results := []autoupdate.CheckResult{
		{
			Package:        "net-misc/bar",
			CurrentVersion: "1.0",
			HasUpdate:      false,
		},
	}
	displayCheckResults(results)
}

// TestDisplayCheckResultsWithError tests displayCheckResults with an error result.
func TestDisplayCheckResultsWithError(t *testing.T) {
	results := []autoupdate.CheckResult{
		{
			Package: "net-misc/baz",
			Error:   io.ErrUnexpectedEOF,
		},
	}
	displayCheckResults(results)
}

// TestDisplayCheckResultsFromCache tests displayCheckResults with cached result.
func TestDisplayCheckResultsFromCache(t *testing.T) {
	results := []autoupdate.CheckResult{
		{
			Package:         "net-misc/cached",
			CurrentVersion:  "1.0",
			UpstreamVersion: "2.0",
			HasUpdate:       true,
			FromCache:       true,
		},
	}
	displayCheckResults(results)
}

// TestDisplayCheckResultsMultiple tests displayCheckResults with mixed results.
func TestDisplayCheckResultsMultiple(t *testing.T) {
	results := []autoupdate.CheckResult{
		{Package: "a/pkg1", HasUpdate: true, CurrentVersion: "1.0", UpstreamVersion: "2.0"},
		{Package: "a/pkg2", HasUpdate: false, CurrentVersion: "3.0"},
		{Package: "a/pkg3", Error: io.ErrUnexpectedEOF},
	}
	displayCheckResults(results)
}

// ---- displayPendingUpdates ----

// TestDisplayPendingUpdatesEmpty tests displayPendingUpdates with no updates.
func TestDisplayPendingUpdatesEmpty(t *testing.T) {
	displayPendingUpdates(nil)
}

// TestDisplayPendingUpdatesWithItems tests displayPendingUpdates with items.
func TestDisplayPendingUpdatesWithItems(t *testing.T) {
	updates := []autoupdate.PendingUpdate{
		{
			Package:        "app-misc/foo",
			CurrentVersion: "1.0",
			NewVersion:     "2.0",
			Status:         autoupdate.StatusPending,
			DetectedAt:     time.Now(),
		},
	}
	displayPendingUpdates(updates)
}

// TestDisplayPendingUpdatesWithError tests displayPendingUpdates with error field.
func TestDisplayPendingUpdatesWithError(t *testing.T) {
	updates := []autoupdate.PendingUpdate{
		{
			Package:        "app-misc/broken",
			CurrentVersion: "1.0",
			NewVersion:     "2.0",
			Status:         autoupdate.StatusFailed,
			Error:          "something went wrong",
			DetectedAt:     time.Now(),
		},
	}
	displayPendingUpdates(updates)
}

// TestDisplayPendingUpdatesAllStatuses tests displayPendingUpdates with all status types.
func TestDisplayPendingUpdatesAllStatuses(t *testing.T) {
	statuses := []autoupdate.UpdateStatus{
		autoupdate.StatusPending,
		autoupdate.StatusValidated,
		autoupdate.StatusFailed,
		autoupdate.StatusApplied,
	}
	for _, s := range statuses {
		updates := []autoupdate.PendingUpdate{
			{Package: "a/pkg", CurrentVersion: "1.0", NewVersion: "2.0", Status: s, DetectedAt: time.Now()},
		}
		displayPendingUpdates(updates)
	}
}

// ---- displayApplyResult ----

// TestDisplayApplyResultNil tests displayApplyResult with nil result (no panic).
func TestDisplayApplyResultNil(t *testing.T) {
	displayApplyResult(nil)
}

// TestDisplayApplyResultSuccess tests displayApplyResult with successful result.
func TestDisplayApplyResultSuccess(t *testing.T) {
	result := &autoupdate.ApplyResult{
		Package:    "net-misc/foo",
		OldVersion: "1.0",
		NewVersion: "2.0",
		Success:    true,
	}
	displayApplyResult(result)
}

// TestDisplayApplyResultFailure tests displayApplyResult with failed result.
func TestDisplayApplyResultFailure(t *testing.T) {
	result := &autoupdate.ApplyResult{
		Package:    "net-misc/bar",
		OldVersion: "1.0",
		NewVersion: "2.0",
		Success:    false,
		Error:      io.ErrUnexpectedEOF,
		LogPath:    "/tmp/apply.log",
	}
	displayApplyResult(result)
}

// TestDisplayApplyResultFailureNoLog tests displayApplyResult with failure and no log path.
func TestDisplayApplyResultFailureNoLog(t *testing.T) {
	result := &autoupdate.ApplyResult{
		Package:    "net-misc/nolog",
		OldVersion: "1.0",
		NewVersion: "2.0",
		Success:    false,
	}
	displayApplyResult(result)
}

// ---- displayAnalyzeResult ----

// TestDisplayAnalyzeResultWithError tests displayAnalyzeResult with error.
func TestDisplayAnalyzeResultWithError(t *testing.T) {
	result := &autoupdate.AnalyzeResult{
		Package: "net-misc/foo",
		Error:   io.ErrUnexpectedEOF,
	}
	displayAnalyzeResult(result)
}

// TestDisplayAnalyzeResultNoSchema tests displayAnalyzeResult with no schema.
func TestDisplayAnalyzeResultNoSchema(t *testing.T) {
	result := &autoupdate.AnalyzeResult{
		Package: "net-misc/noschema",
	}
	displayAnalyzeResult(result)
}

// TestDisplayAnalyzeResultValidated tests displayAnalyzeResult with validated schema.
func TestDisplayAnalyzeResultValidated(t *testing.T) {
	result := &autoupdate.AnalyzeResult{
		Package: "net-misc/validated",
		SuggestedSchema: &autoupdate.PackageConfig{
			URL:    "https://example.com",
			Parser: "github",
		},
		Validated:        true,
		ExtractedVersion: "2.0",
		EbuildVersion:    "2.0",
	}
	displayAnalyzeResult(result)
}

// TestDisplayAnalyzeResultVersionMismatch tests displayAnalyzeResult with version mismatch.
func TestDisplayAnalyzeResultVersionMismatch(t *testing.T) {
	result := &autoupdate.AnalyzeResult{
		Package: "net-misc/mismatch",
		SuggestedSchema: &autoupdate.PackageConfig{
			URL:    "https://example.com",
			Parser: "html",
		},
		Validated:        false,
		ExtractedVersion: "2.1",
		EbuildVersion:    "2.0",
		FromCache:        true,
	}
	displayAnalyzeResult(result)
}

// TestDisplayAnalyzeResultNoExtractedVersion tests displayAnalyzeResult with no extracted version.
func TestDisplayAnalyzeResultNoExtractedVersion(t *testing.T) {
	result := &autoupdate.AnalyzeResult{
		Package: "net-misc/noextract",
		SuggestedSchema: &autoupdate.PackageConfig{
			URL:    "https://example.com",
			Parser: "pypi",
		},
		Validated: false,
	}
	displayAnalyzeResult(result)
}

// ---- displayBatchResults ----

// TestDisplayBatchResultsEmpty tests displayBatchResults with empty slice.
func TestDisplayBatchResultsEmpty(t *testing.T) {
	displayBatchResults(nil)
}

// TestDisplayBatchResultsMixed tests displayBatchResults with mixed results.
func TestDisplayBatchResultsMixed(t *testing.T) {
	results := []autoupdate.AnalyzeResult{
		{
			Package: "net-misc/ok",
			SuggestedSchema: &autoupdate.PackageConfig{
				URL:    "https://example.com",
				Parser: "github",
			},
			Validated: true,
		},
		{
			Package: "net-misc/fail",
			Error:   io.ErrUnexpectedEOF,
		},
		{
			Package: "net-misc/noschema",
		},
		{
			Package: "net-misc/unvalidated",
			SuggestedSchema: &autoupdate.PackageConfig{
				URL:    "https://example.com",
				Parser: "html",
			},
			Validated: false,
		},
	}
	displayBatchResults(results)
}

// ---- displaySchema ----

// TestDisplaySchemaMinimal tests displaySchema with minimal config.
func TestDisplaySchemaMinimal(t *testing.T) {
	schema := &autoupdate.PackageConfig{
		URL:    "https://example.com",
		Parser: "github",
	}
	displaySchema(schema)
}

// TestDisplaySchemaFull tests displaySchema with all optional fields.
func TestDisplaySchemaFull(t *testing.T) {
	schema := &autoupdate.PackageConfig{
		URL:              "https://example.com",
		Parser:           "html",
		Path:             "/releases",
		Pattern:          `v(\d+\.\d+)`,
		Selector:         "a.release",
		XPath:            "//a",
		Binary:           true,
		FallbackURL:      "https://fallback.com",
		FallbackParser:   "github",
		FallbackPattern:  `v(\d+)`,
		LLMPrompt:        "find version",
		Headers:          map[string]string{"Authorization": "Bearer tok"},
		VersionsPath:     "/versions",
		VersionsSelector: "span.version",
	}
	displaySchema(schema)
}

// ---- printComparisonSummary ----

// TestPrintComparisonSummaryWithErrors tests printComparisonSummary with errors.
func TestPrintComparisonSummaryWithErrors(t *testing.T) {
	report := &overlay.CompareReport{
		TotalPackages:    10,
		ComparedPackages: 8,
		NotInRemoteCount: 2,
		ErrorCount:       1,
	}
	printComparisonSummary(report, "gentoo")
}

// TestPrintComparisonSummaryNoErrors tests printComparisonSummary with no errors.
func TestPrintComparisonSummaryNoErrors(t *testing.T) {
	report := &overlay.CompareReport{
		TotalPackages:    5,
		ComparedPackages: 5,
		NotInRemoteCount: 0,
		ErrorCount:       0,
	}
	printComparisonSummary(report, "guru")
}

// ---- output.NoColor / global flag side effects ----

// TestNoColorFlagDoesNotPanic tests that calling output.NoColor() does not panic.
func TestNoColorFlagDoesNotPanic(t *testing.T) {
	output.NoColor()
}

// ---- version command output via executeCommand ----

// TestVersionCommandOutput tests that version command executes without error.
// Note: version.go uses fmt.Println which writes to os.Stdout directly, not cobra's writer.
func TestVersionCommandOutput(t *testing.T) {
	_, err := executeCommand(rootCmd, "version")
	if err != nil {
		t.Fatalf("version command returned error: %v", err)
	}
}

// TestVersionCommandContainsVersionInfo tests version.Info() output contains expected fields.
// Validates Requirement 9.1: version information is printed to output.
func TestVersionCommandContainsVersionInfo(t *testing.T) {
	// version.Info() is what versionCmd.Run calls via fmt.Println(version.Info())
	out := version.Info()
	if !strings.Contains(out, "bentoo") {
		t.Errorf("version.Info() should contain 'bentoo', got: %q", out)
	}
}

// ---- completion command output ----

// TestCompletionBashOutput tests completion bash executes without error.
// Note: cobra's GenBashCompletion writes to os.Stdout directly, not cobra's writer.
func TestCompletionBashOutput(t *testing.T) {
	_, err := executeCommand(rootCmd, "completion", "bash")
	if err != nil {
		t.Fatalf("completion bash returned error: %v", err)
	}
}

// TestCompletionZshOutput tests completion zsh executes without error.
func TestCompletionZshOutput(t *testing.T) {
	_, err := executeCommand(rootCmd, "completion", "zsh")
	if err != nil {
		t.Fatalf("completion zsh returned error: %v", err)
	}
}

// TestCompletionFishOutput tests completion fish executes without error.
func TestCompletionFishOutput(t *testing.T) {
	_, err := executeCommand(rootCmd, "completion", "fish")
	if err != nil {
		t.Fatalf("completion fish returned error: %v", err)
	}
}

// TestCompletionPowershellOutput tests completion powershell executes without error.
func TestCompletionPowershellOutput(t *testing.T) {
	_, err := executeCommand(rootCmd, "completion", "powershell")
	if err != nil {
		t.Fatalf("completion powershell returned error: %v", err)
	}
}

// ---- global flags configure logger/output (PersistentPreRun) ----

// TestVerboseFlagConfiguresLogger tests --verbose flag triggers PersistentPreRun.
func TestVerboseFlagConfiguresLogger(t *testing.T) {
	_, err := executeCommand(rootCmd, "--verbose", "version")
	if err != nil {
		t.Fatalf("--verbose version returned error: %v", err)
	}
}

// TestQuietFlagConfiguresLogger tests --quiet flag triggers PersistentPreRun.
func TestQuietFlagConfiguresLogger(t *testing.T) {
	_, err := executeCommand(rootCmd, "--quiet", "version")
	if err != nil {
		t.Fatalf("--quiet version returned error: %v", err)
	}
}

// TestNoColorFlagConfiguresOutput tests --no-color flag triggers PersistentPreRun.
func TestNoColorFlagConfiguresOutput(t *testing.T) {
	_, err := executeCommand(rootCmd, "--no-color", "version")
	if err != nil {
		t.Fatalf("--no-color version returned error: %v", err)
	}
}

// ---- overlay subcommands registration ----

// TestOverlayAddSubcommandRegistered tests add subcommand is registered.
func TestOverlayAddSubcommandRegistered(t *testing.T) {
	if addCmd.Run == nil {
		t.Error("add command should have a Run function")
	}
	if addCmd.Use == "" {
		t.Error("add command should have a Use field")
	}
}

// TestOverlayCommitSubcommandRegistered tests commit subcommand is registered.
func TestOverlayCommitSubcommandRegistered(t *testing.T) {
	if commitCmd.Run == nil {
		t.Error("commit command should have a Run function")
	}
}

// TestOverlayPushSubcommandRegistered tests push subcommand is registered.
func TestOverlayPushSubcommandRegistered(t *testing.T) {
	if pushCmd.Run == nil {
		t.Error("push command should have a Run function")
	}
}

// TestOverlayRenameSubcommandRegistered tests rename subcommand is registered.
func TestOverlayRenameSubcommandRegistered(t *testing.T) {
	if renameCmd.Run == nil {
		t.Error("rename command should have a Run function")
	}
}

// TestOverlayAnalyzeSubcommandRegistered tests analyze subcommand is registered.
func TestOverlayAnalyzeSubcommandRegistered(t *testing.T) {
	if analyzeCmd.Run == nil {
		t.Error("analyze command should have a Run function")
	}
}

// TestOverlayAutoupdateSubcommandRegistered tests autoupdate subcommand is registered.
func TestOverlayAutoupdateSubcommandRegistered(t *testing.T) {
	if autoupdateCmd.Run == nil {
		t.Error("autoupdate command should have a Run function")
	}
}

// TestAllOverlaySubcommandsRegistered tests all expected overlay subcommands exist.
func TestAllOverlaySubcommandsRegistered(t *testing.T) {
	expected := []string{
		"add", "status", "commit", "push", "compare", "sync",
		"diff", "init", "log", "rename", "analyze", "autoupdate",
	}
	for _, name := range expected {
		t.Run(name, func(t *testing.T) {
			found := false
			for _, cmd := range overlayCmd.Commands() {
				if cmd.Use == name || strings.HasPrefix(cmd.Use, name+" ") || strings.HasPrefix(cmd.Use, name+"\n") {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("overlay %s subcommand should be registered", name)
			}
		})
	}
}

// TestSyncCommandDescription tests sync command has description.
func TestSyncCommandDescription(t *testing.T) {
	if syncCmd.Use == "" {
		t.Error("sync command should have a Use field")
	}
	if syncCmd.Short == "" {
		t.Error("sync command should have a Short description")
	}
}

// TestPushCommandFlags tests push command has --dry-run flag.
func TestPushCommandFlags(t *testing.T) {
	flag := pushCmd.Flags().Lookup("dry-run")
	if flag == nil {
		t.Fatal("push command should have --dry-run flag")
	}
	if flag.Value.Type() != "bool" {
		t.Errorf("--dry-run should be bool, got %s", flag.Value.Type())
	}
	sh := pushCmd.Flags().ShorthandLookup("n")
	if sh == nil {
		t.Error("--dry-run should have -n shorthand")
	}
}

// TestCommitCommandAllFlags tests commit command flags.
func TestCommitCommandAllFlags(t *testing.T) {
	dryRun := commitCmd.Flags().Lookup("dry-run")
	if dryRun == nil {
		t.Fatal("commit command should have --dry-run flag")
	}
	msg := commitCmd.Flags().Lookup("message")
	if msg == nil {
		t.Fatal("commit command should have --message flag")
	}
}

// TestAnalyzeCommandFlags tests analyze command flags.
func TestAnalyzeCommandFlags(t *testing.T) {
	flags := []string{"url", "hint", "all", "no-cache", "force", "dry-run"}
	for _, name := range flags {
		t.Run(name, func(t *testing.T) {
			if analyzeCmd.Flags().Lookup(name) == nil {
				t.Errorf("analyze command should have --%s flag", name)
			}
		})
	}
}

// TestRenameCommandAllFlags tests rename command flags.
func TestRenameCommandAllFlags(t *testing.T) {
	flags := []string{"dry-run", "yes", "no-manifest", "force"}
	for _, name := range flags {
		t.Run(name, func(t *testing.T) {
			if renameCmd.Flags().Lookup(name) == nil {
				t.Errorf("rename command should have --%s flag", name)
			}
		})
	}
}

// TestOverlayCommandHasLongDescription tests overlay command has long description.
func TestOverlayCommandHasLongDescription(t *testing.T) {
	if overlayCmd.Long == "" {
		t.Error("overlay command should have a long description")
	}
}

// TestRootCommandPersistentPreRunNotNil tests PersistentPreRun is set.
func TestRootCommandPersistentPreRunNotNil(t *testing.T) {
	if rootCmd.PersistentPreRun == nil {
		t.Error("rootCmd should have PersistentPreRun set")
	}
}

// TestHelpFlagOnOverlay tests that overlay --help does not panic.
func TestHelpFlagOnOverlay(t *testing.T) {
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"overlay", "--help"})
	_ = rootCmd.Execute()
}

// TestVersionCommandHelpOutput tests version --help does not panic.
func TestVersionCommandHelpOutput(t *testing.T) {
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"version", "--help"})
	_ = rootCmd.Execute()
}
