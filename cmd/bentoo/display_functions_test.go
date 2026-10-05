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
)

// ---- displayPendingUpdates ----

// TestDisplayPendingUpdatesEmpty tests displayPendingUpdates with no updates.
func TestDisplayPendingUpdatesEmpty(t *testing.T) {
	displayPendingUpdates(discardLog(), nil)
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
	displayPendingUpdates(discardLog(), updates)
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
	displayPendingUpdates(discardLog(), updates)
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
		displayPendingUpdates(discardLog(), updates)
	}
}

// ---- displayApplyResult ----

// TestDisplayApplyResultNil tests displayApplyResult with nil result (no panic).
func TestDisplayApplyResultNil(t *testing.T) {
	auOpts := testAutoupdateOptions()
	testAutoupdateRun(auOpts).displayApplyResult(nil)
}

// TestDisplayApplyResultSuccess tests displayApplyResult with successful result.
func TestDisplayApplyResultSuccess(t *testing.T) {
	auOpts := testAutoupdateOptions()
	result := &autoupdate.ApplyResult{
		Package:    "net-misc/foo",
		OldVersion: "1.0",
		NewVersion: "2.0",
		Success:    true,
	}
	testAutoupdateRun(auOpts).displayApplyResult(result)
}

// TestDisplayApplyResultFailure tests displayApplyResult with failed result.
func TestDisplayApplyResultFailure(t *testing.T) {
	auOpts := testAutoupdateOptions()
	result := &autoupdate.ApplyResult{
		Package:    "net-misc/bar",
		OldVersion: "1.0",
		NewVersion: "2.0",
		Success:    false,
		Error:      io.ErrUnexpectedEOF,
		LogPath:    "/tmp/apply.log",
	}
	testAutoupdateRun(auOpts).displayApplyResult(result)
}

// TestDisplayApplyResultFailureNoLog tests displayApplyResult with failure and no log path.
func TestDisplayApplyResultFailureNoLog(t *testing.T) {
	auOpts := testAutoupdateOptions()
	result := &autoupdate.ApplyResult{
		Package:    "net-misc/nolog",
		OldVersion: "1.0",
		NewVersion: "2.0",
		Success:    false,
	}
	testAutoupdateRun(auOpts).displayApplyResult(result)
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
	displaySchema("dev-util/minimal", schema)
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
		Type:             "bin",
		FallbackURL:      "https://fallback.com",
		FallbackParser:   "github",
		FallbackPattern:  `v(\d+)`,
		LLMPrompt:        "find version",
		Headers:          map[string]string{"Authorization": "Bearer tok"},
		VersionsPath:     "/versions",
		VersionsSelector: "span.version",
	}
	displaySchema("dev-util/full", schema)
}

// ---- printComparisonSummary ----
//
// TestPrintComparisonSummaryWithErrors and TestPrintComparisonSummaryNoErrors
// stood here and were RETIRED by story 047, sub-task 5.5 (S047-R8.2), because
// sub-task 4.2 deletes printComparisonSummary.
//
// Both were smoke tests: they built a CompareReport, called the printer, and
// asserted nothing at all — they proved the call did not panic. The summary the
// operator now reads is the report's own Summary section, whose counts are
// asserted on the payload by TestBuildCompareReport/"the counts come from the
// producer's own counters" and whose rendered form is pinned by
// testdata/TestCompareGoldenPlain.golden and TestCompareGoldenPlainNoneRead.
// Nothing is lost: a golden diff is a stronger no-panic test than a call with no
// assertion after it.

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
	// The flag's effect is a process-wide logger level; put it back so it does
	// not silence a later test that reads the logger's output.
	t.Cleanup(func() { resetLoggerLevelFlagState(t) })
	_, err := executeCommand(rootCmd, "--verbose", "version")
	if err != nil {
		t.Fatalf("--verbose version returned error: %v", err)
	}
}

// TestQuietFlagConfiguresLogger tests --quiet flag triggers PersistentPreRun.
func TestQuietFlagConfiguresLogger(t *testing.T) {
	// The flag's effect is a process-wide logger level; put it back so it does
	// not silence a later test that reads the logger's output.
	t.Cleanup(func() { resetLoggerLevelFlagState(t) })
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
	if addCmd.RunE == nil {
		t.Error("add command should have a RunE function")
	}
	if addCmd.Use == "" {
		t.Error("add command should have a Use field")
	}
}

// TestOverlayCommitSubcommandRegistered tests commit subcommand is registered.
func TestOverlayCommitSubcommandRegistered(t *testing.T) {
	if commitCmd.RunE == nil {
		t.Error("commit command should have a RunE function")
	}
}

// TestOverlayPushSubcommandRegistered tests push subcommand is registered.
func TestOverlayPushSubcommandRegistered(t *testing.T) {
	if pushCmd.RunE == nil {
		t.Error("push command should have a RunE function")
	}
}

// TestOverlayRenameSubcommandRegistered tests rename subcommand is registered.
func TestOverlayRenameSubcommandRegistered(t *testing.T) {
	if renameCmd.RunE == nil {
		t.Error("rename command should have a RunE function")
	}
}

// TestOverlayAnalyzeSubcommandRegistered tests analyze subcommand is registered.
func TestOverlayAnalyzeSubcommandRegistered(t *testing.T) {
	if analyzeCmd.RunE == nil {
		t.Error("analyze command should have a RunE function")
	}
}

// TestOverlayAutoupdateSubcommandRegistered tests autoupdate subcommand is registered.
func TestOverlayAutoupdateSubcommandRegistered(t *testing.T) {
	auCmd := testAutoupdateCmd()
	if auCmd.RunE == nil {
		t.Error("autoupdate command should have a RunE function")
	}
}

// TestAllOverlaySubcommandsRegistered tests all expected overlay subcommands exist.
func TestAllOverlaySubcommandsRegistered(t *testing.T) {
	expected := []string{
		"add", "status", "commit", "push", "compare", "pull",
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

// TestPullCommandDescription tests pull command has description.
func TestPullCommandDescription(t *testing.T) {
	if pullCmd.Use == "" {
		t.Error("pull command should have a Use field")
	}
	if pullCmd.Short == "" {
		t.Error("pull command should have a Short description")
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

// TestRootCommandPersistentPreRunNotNil tests the root's pre-run hook is set.
//
// It reads PersistentPreRunE, not PersistentPreRun, for the reason
// TestRootCommandHasPersistentPreRun in version_completion_test.go gives at
// length: story 046 sub-task 4.4 put --ui rejection in this hook (R3.2), and
// only the error-returning variant can stop cobra before RunE. Cobra ignores
// PersistentPreRun entirely once PersistentPreRunE is set, so the old field is
// now permanently nil by design.
func TestRootCommandPersistentPreRunNotNil(t *testing.T) {
	if rootCmd.PersistentPreRunE == nil {
		t.Error("rootCmd should have PersistentPreRunE set")
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
