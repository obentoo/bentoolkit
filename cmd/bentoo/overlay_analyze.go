package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/common/config"
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

var analyzeCmd = &cobra.Command{
	Use:   "analyze [category/package]",
	Short: "Analyze package and generate autoupdate schema",
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
	Run: runAnalyze,
}

func init() {
	analyzeCmd.Flags().StringVar(&analyzeURL, "url", "", "Override URL for analysis")
	analyzeCmd.Flags().StringVar(&analyzeHint, "hint", "", "Provide hint to LLM for guidance")
	analyzeCmd.Flags().BoolVar(&analyzeAll, "all", false, "Analyze all packages without schema")
	analyzeCmd.Flags().BoolVar(&analyzeNoCache, "no-cache", false, "Bypass all caches")
	analyzeCmd.Flags().BoolVar(&analyzeForce, "force", false, "Overwrite existing schema")
	analyzeCmd.Flags().BoolVar(&analyzeDryRun, "dry-run", false, "Show schema without saving")

	overlayCmd.AddCommand(analyzeCmd)
}

func runAnalyze(cmd *cobra.Command, args []string) {
	cfg, err := config.Load()
	if err != nil {
		logger.Error("loading config: %v", err)
		os.Exit(1)
	}

	overlayPath := cfg.Overlay.Path
	if overlayPath == "" {
		logger.Error("overlay path not configured")
		os.Exit(1)
	}

	// Expand home directory if needed
	if overlayPath[0] == '~' {
		home, err := os.UserHomeDir()
		if err != nil {
			logger.Error("failed to get home directory: %v", err)
			os.Exit(1)
		}
		overlayPath = filepath.Join(home, overlayPath[1:])
	}

	// Determine config directory for autoupdate
	configDir := filepath.Join(os.Getenv("HOME"), ".config", "bentoo", "autoupdate")

	// Validate arguments
	if !analyzeAll && len(args) == 0 {
		cmd.Help()
		os.Exit(1)
	}

	// Create analyzer
	analyzer, err := autoupdate.NewAnalyzer(overlayPath, autoupdate.WithAnalyzerConfigDir(configDir))
	if err != nil {
		logger.Error("failed to initialize analyzer: %v", err)
		os.Exit(1)
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
		runAnalyzeAll(analyzer, opts)
	} else {
		runAnalyzeSingle(analyzer, args[0], opts)
	}
}

// runAnalyzeSingle handles single package analysis
func runAnalyzeSingle(analyzer *autoupdate.Analyzer, pkg string, opts autoupdate.AnalyzeOptions) {
	output.Info.Printf("Analyzing %s...\n", pkg)

	result, err := analyzer.Analyze(pkg, opts)
	if err != nil {
		displayAnalyzeResult(result)
		os.Exit(1)
	}

	displayAnalyzeResult(result)

	// If dry-run, don't save
	if opts.DryRun {
		return
	}

	// If schema was generated, ask for confirmation and save
	if result.SuggestedSchema != nil {
		if !result.Validated {
			// Warn about version mismatch
			output.Warning.Println("\nWarning: Extracted version does not match ebuild version")
			output.Warning.Printf("  Extracted: %s\n", result.ExtractedVersion)
			output.Warning.Printf("  Ebuild:    %s\n", result.EbuildVersion)
			if !confirmAction("Save schema anyway?") {
				logger.Info("Schema not saved")
				return
			}
		}

		if err := analyzer.SaveSchema(pkg, result.SuggestedSchema); err != nil {
			logger.Error("failed to save schema: %v", err)
			os.Exit(1)
		}
		output.Success.Println("\n✓ Schema saved to packages.toml")
	}
}

// runAnalyzeAll handles batch analysis of all packages
func runAnalyzeAll(analyzer *autoupdate.Analyzer, opts autoupdate.AnalyzeOptions) {
	output.Info.Println("Analyzing all packages without schema...")

	results, err := analyzer.AnalyzeAll(opts)
	if err != nil {
		logger.Error("failed to analyze packages: %v", err)
		os.Exit(1)
	}

	if len(results) == 0 {
		output.Success.Println("All packages already have schemas configured")
		return
	}

	displayBatchResults(results)

	// If dry-run, don't save
	if opts.DryRun {
		return
	}

	// Count successful analyses
	var successful int
	for _, r := range results {
		if r.SuggestedSchema != nil && r.Error == nil {
			successful++
		}
	}

	if successful == 0 {
		output.Warning.Println("No schemas were generated successfully")
		return
	}

	// Ask for confirmation to save all successful schemas
	output.Info.Printf("\n%d schema(s) ready to save\n", successful)
	if !confirmAction("Save all successful schemas?") {
		logger.Info("Schemas not saved")
		return
	}

	// Save all successful schemas
	var saved int
	for _, r := range results {
		if r.SuggestedSchema != nil && r.Error == nil {
			if err := analyzer.SaveSchema(r.Package, r.SuggestedSchema); err != nil {
				output.Error.Printf("Failed to save schema for %s: %v\n", r.Package, err)
			} else {
				saved++
			}
		}
	}

	output.Success.Printf("\n✓ Saved %d schema(s) to packages.toml\n", saved)
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

	displaySchema(result.SuggestedSchema)

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
		if r.Error != nil {
			failed++
			output.Error.Printf("  ✗ %s: %v\n", r.Package, r.Error)
		} else if r.SuggestedSchema == nil {
			skipped++
			output.Dim.Printf("  - %s: no schema generated\n", r.Package)
		} else {
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

// displaySchema formats and displays a PackageConfig schema
func displaySchema(schema *autoupdate.PackageConfig) {
	// Build TOML representation
	schemaMap := make(map[string]interface{})
	schemaMap["url"] = schema.URL
	schemaMap["parser"] = schema.Parser

	if schema.Path != "" {
		schemaMap["path"] = schema.Path
	}
	if schema.Pattern != "" {
		schemaMap["pattern"] = schema.Pattern
	}
	if schema.Selector != "" {
		schemaMap["selector"] = schema.Selector
	}
	if schema.XPath != "" {
		schemaMap["xpath"] = schema.XPath
	}
	if schema.Binary {
		schemaMap["binary"] = schema.Binary
	}
	if schema.FallbackURL != "" {
		schemaMap["fallback_url"] = schema.FallbackURL
	}
	if schema.FallbackParser != "" {
		schemaMap["fallback_parser"] = schema.FallbackParser
	}
	if schema.FallbackPattern != "" {
		schemaMap["fallback_pattern"] = schema.FallbackPattern
	}
	if schema.LLMPrompt != "" {
		schemaMap["llm_prompt"] = schema.LLMPrompt
	}
	if len(schema.Headers) > 0 {
		schemaMap["headers"] = schema.Headers
	}
	if schema.VersionsPath != "" {
		schemaMap["versions_path"] = schema.VersionsPath
	}
	if schema.VersionsSelector != "" {
		schemaMap["versions_selector"] = schema.VersionsSelector
	}

	// Encode to TOML
	var buf strings.Builder
	encoder := toml.NewEncoder(&buf)
	encoder.Encode(schemaMap)

	// Print with indentation
	for _, line := range strings.Split(buf.String(), "\n") {
		if line != "" {
			fmt.Printf("  %s\n", line)
		}
	}
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
