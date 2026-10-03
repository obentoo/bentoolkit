package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/common/logger"
	"github.com/obentoo/bentoolkit/internal/common/output"
	"github.com/spf13/cobra"
)

var (
	// analyzeURL overrides the URL for analysis
	analyzeURL string
	// analyzeHint provides user guidance to the LLM
	analyzeHint string
	// analyzeAll triggers batch mode for all packages
	analyzeAll bool
	// analyzeNoCache bypasses all caches
	analyzeNoCache bool
	// analyzeForce overwrites existing schema
	analyzeForce bool
	// analyzeDryRun shows schema without saving
	analyzeDryRun bool
)

// newAnalyzeCmd builds `overlay analyze`.
func newAnalyzeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:         "analyze [category/package]",
		Annotations: map[string]string{cancellableAnnotation: "true"},
		Short:       "Analyze package and generate autoupdate schema",
		Long: `Analyze a package to determine the best way to check for upstream versions.

The analyze command uses intelligent analysis to discover data sources and
generate update schemas automatically. It supports multiple data sources
including GitHub releases, PyPI, npm, crates.io, and HTML scraping.

Examples:
  bentoo overlay analyze net-misc/foo           Analyze single package
  bentoo overlay analyze net-misc/foo --url URL Override URL for analysis
  bentoo overlay analyze net-misc/foo --hint "version is in header"
  bentoo overlay analyze --all                  Analyze all packages without schema
  bentoo overlay analyze net-misc/foo --no-cache  Bypass caches
  bentoo overlay analyze net-misc/foo --force   Overwrite existing schema
  bentoo overlay analyze net-misc/foo --dry-run Show schema without saving`,
		RunE: runAnalyze,
	}
	cmd.Flags().StringVar(&analyzeURL, "url", "", "Override URL for analysis")
	cmd.Flags().StringVar(&analyzeHint, "hint", "", "Provide hint to LLM for guidance")
	cmd.Flags().BoolVar(&analyzeAll, "all", false, "Analyze all packages without schema")
	cmd.Flags().BoolVar(&analyzeNoCache, "no-cache", false, "Bypass all caches")
	cmd.Flags().BoolVar(&analyzeForce, "force", false, "Overwrite existing schema")
	cmd.Flags().BoolVar(&analyzeDryRun, "dry-run", false, "Show schema without saving")
	return cmd
}

func runAnalyze(cmd *cobra.Command, args []string) error {
	appCtx, err := loadAppContextNoValidation()
	if err != nil {
		logger.Error("loading config: %v", err)
		return exitWith(1)
	}

	overlayPath := appCtx.OverlayPath

	// Determine config directory for autoupdate
	configDir, err := autoupdateConfigDir()
	if err != nil {
		logger.Error("%v", err)
		return exitWith(1)
	}

	// Validate arguments
	if !analyzeAll && len(args) == 0 {
		cmd.Help() //nolint:errcheck // help output failure is not actionable
		return exitWith(1)
	}

	// The process-wide context (func commandContext): overlay analyze is
	// annotated cancellable, so the first SIGINT, SIGTERM or SIGHUP cancels it.
	// It reaches the analyzer (its HTTP and LLM calls) and the LLM provider. The
	// `claude` CLI runs in its own process group, so a Ctrl+C at the terminal
	// never reaches it: without this context an interrupt ended bentoo and left
	// `claude` running (story 054, R4.3).
	//
	// runCtx is the ANALYSIS context, derived from it so that release can end
	// the analysis before either confirmation prompt (func confirmAfterRelease)
	// without cancelling the command. release also hands the next signal its
	// default action (func yieldSignalsToPrompt), so one Ctrl+C at the prompt
	// ends the command, as it did when release unregistered the handler.
	ctx := commandContext(cmd)
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	release := func() {
		cancelRun()
		yieldSignalsToPrompt(cmd)
	}

	// Build analyzer options, conditionally injecting an LLM provider. When a
	// provider is configured but cannot be constructed (e.g. the `claude` CLI is
	// absent or not authenticated), we log a Warn and fall back to the heuristic
	// analyzer rather than failing — analysis still proceeds (R4.2, R6.1, R6.2).
	analyzerOpts := []autoupdate.AnalyzerOption{
		autoupdate.WithAnalyzerConfigDir(configDir),
		autoupdate.WithAnalyzerContext(runCtx),
	}
	llmCfg := appCtx.Config.Autoupdate.LLM
	if p, err := newConfiguredLLMProvider(runCtx, llmCfg); err != nil {
		logger.Warn("LLM provider %q unavailable; falling back to heuristic analysis: %v", llmCfg.Provider, err)
	} else if p != nil {
		analyzerOpts = append(analyzerOpts, autoupdate.WithAnalyzerLLMClient(p))
	}

	// Create analyzer
	analyzer, err := autoupdate.NewAnalyzer(overlayPath, analyzerOpts...)
	if err != nil {
		logger.Error("failed to initialize analyzer: %v", err)
		return exitWith(1)
	}

	opts := autoupdate.AnalyzeOptions{
		URL:     analyzeURL,
		Hint:    analyzeHint,
		NoCache: analyzeNoCache,
		Force:   analyzeForce,
		DryRun:  analyzeDryRun,
	}

	// Handle different modes
	if analyzeAll {
		return runAnalyzeAll(analyzer, opts, release)
	}
	return runAnalyzeSingle(analyzer, args[0], opts, release)
}

// runAnalyzeSingle handles single package analysis and returns the command's
// exit status (func exitWith). release ends the analysis context; it runs
// before the confirmation prompt.
func runAnalyzeSingle(analyzer *autoupdate.Analyzer, pkg string, opts autoupdate.AnalyzeOptions, release func()) error {
	output.Info.Printf("Analyzing %s...\n", pkg)

	result, err := analyzer.Analyze(pkg, opts)
	if err != nil {
		displayAnalyzeResult(result)
		return exitWith(1)
	}

	displayAnalyzeResult(result)

	// If dry-run, don't save
	if opts.DryRun {
		return nil
	}

	// If schema was generated, ask for confirmation and save
	if result.SuggestedSchema != nil {
		if !result.Validated {
			// Warn about version mismatch
			output.Warning.Println("\nWarning: Extracted version does not match ebuild version")
			output.Warning.Printf("  Extracted: %s\n", result.ExtractedVersion)
			output.Warning.Printf("  Ebuild:    %s\n", result.EbuildVersion)
			if !confirmAfterRelease(release, "Save schema anyway?") {
				logger.Info("Schema not saved")
				return nil
			}
		}

		if err := analyzer.SaveSchema(pkg, result.SuggestedSchema); err != nil {
			logger.Error("failed to save schema: %v", err)
			return exitWith(1)
		}
		output.Success.Println("\n✓ Schema saved to packages.toml")
	}
	return nil
}

// runAnalyzeAll handles batch analysis of all packages and returns the batch
// exit status: BatchResult.ExitCode through func exitWith — 0 all ok, 1
// partial, 2 total failure. release ends the analysis context; it runs before
// the confirmation prompt.
func runAnalyzeAll(analyzer *autoupdate.Analyzer, opts autoupdate.AnalyzeOptions, release func()) error {
	output.Info.Println("Analyzing all packages without schema...")

	// AnalyzeAll never returns a fatal error: enumeration and per-package
	// failures are all captured in the BatchResult.
	result := analyzer.AnalyzeAll(opts)

	// Emit one stderr line per failure. FormatFailures is called only after
	// every AnalyzeAll worker goroutine has joined, so the output is
	// deterministic regardless of completion order.
	if result.HasFailures() {
		result.FormatFailures(os.Stderr)
	}

	if len(result.Items) == 0 && !result.HasFailures() {
		output.Success.Println("All packages already have schemas configured")
		return exitWith(result.ExitCode())
	}

	displayBatchResults(result.Items)

	// If dry-run, don't save; still report the batch outcome.
	if opts.DryRun {
		return exitWith(result.ExitCode())
	}

	// Count successful analyses
	var successful int
	for _, r := range result.Items {
		if r.SuggestedSchema != nil && r.Error == nil {
			successful++
		}
	}

	if successful == 0 {
		output.Warning.Println("No schemas were generated successfully")
		return exitWith(result.ExitCode())
	}

	// Ask for confirmation to save all successful schemas
	output.Info.Printf("\n%d schema(s) ready to save\n", successful)
	if !confirmAfterRelease(release, "Save all successful schemas?") {
		logger.Info("Schemas not saved")
		return exitWith(result.ExitCode())
	}

	// Save all successful schemas
	var saved int
	for _, r := range result.Items {
		if r.SuggestedSchema != nil && r.Error == nil {
			if err := analyzer.SaveSchema(r.Package, r.SuggestedSchema); err != nil {
				output.Error.Fprintf(os.Stderr, "Failed to save schema for %s: %v\n", r.Package, err)
			} else {
				saved++
			}
		}
	}

	output.Success.Printf("\n✓ Saved %d schema(s) to packages.toml\n", saved)

	// Exit with the contract-defined code: 0 all-ok, 1 partial, 2 total fail.
	return exitWith(result.ExitCode())
}

// displayAnalyzeResult formats and displays a single analysis result
func displayAnalyzeResult(result *autoupdate.AnalyzeResult) {
	fmt.Println()
	output.Header.Println("Analysis Result")
	fmt.Println()

	output.Package.Printf("  %s\n", result.Package)

	if result.Error != nil {
		output.Error.Printf("    Error: %v\n", result.Error)
		return
	}

	if result.SuggestedSchema == nil {
		output.Warning.Println("    No schema generated")
		return
	}

	// Display schema details
	fmt.Println()
	output.Header.Println("Suggested Schema")
	fmt.Println()

	displaySchema(result.Package, result.SuggestedSchema)

	// Display validation status
	fmt.Println()
	if result.Validated {
		output.Success.Printf("  ✓ Validated: extracted version %s matches ebuild\n", result.ExtractedVersion)
	} else if result.ExtractedVersion != "" {
		output.Warning.Printf("  ⚠ Version mismatch: extracted %s, ebuild %s\n",
			result.ExtractedVersion, result.EbuildVersion)
	}

	if result.FromCache {
		output.Dim.Println("  (from cache)")
	}
}

// displayBatchResults formats and displays batch analysis results
func displayBatchResults(results []autoupdate.AnalyzeResult) {
	fmt.Println()
	output.Header.Println("Batch Analysis Results")
	fmt.Println()

	var successful, failed, skipped int

	for _, r := range results {
		switch {
		case r.Error != nil:
			failed++
			output.Error.Printf("  ✗ %s: %v\n", r.Package, r.Error)
		case r.SuggestedSchema == nil:
			skipped++
			output.Dim.Printf("  - %s: no schema generated\n", r.Package)
		default:
			successful++
			validStatus := ""
			if r.Validated {
				validStatus = output.Sprintf(output.Success, " (validated)")
			} else {
				validStatus = output.Sprintf(output.Warning, " (unvalidated)")
			}
			output.Success.Printf("  ✓ %s: %s parser%s\n", r.Package, r.SuggestedSchema.Parser, validStatus)
		}
	}

	fmt.Println()
	output.Info.Printf("Summary: %d successful, %d failed, %d skipped\n", successful, failed, skipped)
}

// displaySchema prints the record `overlay analyze` suggests for a package, in
// the shape packages.toml expects: the fields in canonical order, a comments
// field, and the `# END` marker that closes a record (R8.1, R8.2).
//
// It renders through autoupdate.RenderRecord — the same function that writes the
// registry — instead of assembling a map and handing it to the TOML encoder. The
// map could not carry the order (Go map iteration is unordered, and the encoder
// sorted what it got), and each field needed its own `if` here, so a field added
// to PackageConfig was simply never suggested. Sharing the renderer means the
// printed record is what --save would write, and passes the linter the
// maintainer runs next.
//
// It is printed flush against the left margin, unlike the rest of the result
// block, because the whole value of this output is that it can be pasted into
// packages.toml verbatim: an indent would be carried into the doc field's own
// text and onto the `# END` line.
func displaySchema(pkg string, schema *autoupdate.PackageConfig) {
	if schema == nil {
		return
	}

	// Seeded on a COPY: the same pointer is handed to SaveSchema next, and the
	// registry must record what the analyzer detected, not a placeholder written
	// for a reader.
	record := *schema
	if record.Comments == "" {
		record.Comments = suggestedComments(pkg, schema)
	}

	fmt.Print(autoupdate.RenderRecord(pkg, &record))
}

// suggestedComments seeds the doc field of a suggested record: the package name
// the record model requires as its first word, the source the analyzer actually
// chose, and one line telling the maintainer what is still missing.
//
// It states only what was detected — the URL and the parser — and invents
// nothing else. The record model asks the doc field for WHY this source and
// parser, and that answer is not the analyzer's to give: it belongs to whoever
// pastes the record, which is why the second line asks for it in those words
// rather than filling the space with prose that reads like documentation and is
// not.
func suggestedComments(pkg string, schema *autoupdate.PackageConfig) string {
	source := schema.URL
	if schema.Parser != "" {
		if source == "" {
			source = "the " + schema.Parser + " parser"
		} else {
			source += " via the " + schema.Parser + " parser"
		}
	}

	first := pkg
	if source != "" {
		first += " — " + source + "."
	}

	return first + "\nSuggested by `bentoo overlay analyze`: replace this line with WHY this source and\n" +
		"parser, plus every caveat a future bump must know."
}

// confirmAfterRelease runs release, then prompts. release ends the analysis
// context (func runAnalyze): the analysis it guarded is over by the time a
// prompt is shown, so nothing it reached outlives the answer's wait.
//
// A Ctrl+C at the prompt is read by the process-wide handler (func
// processContext). release has already handed the next signal its default
// action (func yieldSignalsToPrompt), so one Ctrl+C at the prompt ends the
// command instead of cancelling a context the stdin read never watches.
func confirmAfterRelease(release func(), prompt string) bool {
	release()
	return confirmAction(prompt)
}

// confirmAction prompts the user for confirmation
func confirmAction(prompt string) bool {
	reader := bufio.NewReader(os.Stdin)
	fmt.Printf("%s [y/N]: ", prompt)
	response, err := reader.ReadString('\n')
	if err != nil {
		return false
	}
	response = strings.TrimSpace(strings.ToLower(response))
	return response == "y" || response == "yes"
}
