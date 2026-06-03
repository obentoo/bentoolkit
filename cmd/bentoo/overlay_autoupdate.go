package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/fatih/color"
	"github.com/obentoo/bentoolkit/internal/autoupdate"
	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/logger"
	"github.com/obentoo/bentoolkit/internal/common/output"
	"github.com/spf13/cobra"
)

var (
	// autoupdateCheck triggers version checking
	autoupdateCheck bool
	// autoupdateList triggers listing pending updates
	autoupdateList bool
	// autoupdateApply specifies package to apply update
	autoupdateApply string
	// autoupdateForce ignores cache when checking
	autoupdateForce bool
	// autoupdateCompile runs compile test after apply
	autoupdateCompile bool
	// autoupdateClean removes the old ebuild after a successful apply, keeping
	// only the newly created version
	autoupdateClean bool
	// autoupdateConcurrency bounds parallel version checks (range [1,100])
	autoupdateConcurrency int
	// autoupdateOnly restricts --check to a package type ("bin" or "source")
	autoupdateOnly string
)

var autoupdateCmd = &cobra.Command{
	Use:   "autoupdate [package]",
	Short: "Check and apply ebuild version updates",
	Long: `Automatically check upstream sources for new versions and apply updates.

Examples:
  bentoo overlay autoupdate --check              Check all packages for updates
  bentoo overlay autoupdate --check net-misc/foo Check specific package
  bentoo overlay autoupdate --check --force      Check ignoring cache
  bentoo overlay autoupdate --check --only source Check only source packages
  bentoo overlay autoupdate --check --only bin    Check only binary packages
  bentoo overlay autoupdate --list               List pending updates
  bentoo overlay autoupdate --apply net-misc/foo Apply update for package
  bentoo overlay autoupdate --apply all          Apply all pending updates
  bentoo overlay autoupdate --apply net-misc/foo --compile  Apply and compile test
  bentoo overlay autoupdate --apply net-misc/foo --clean    Apply and remove the old ebuild`,
	Run: runAutoupdate,
}

func init() {
	autoupdateCmd.Flags().BoolVar(&autoupdateCheck, "check", false, "Check for updates")
	autoupdateCmd.Flags().BoolVar(&autoupdateList, "list", false, "List pending updates")
	autoupdateCmd.Flags().StringVar(&autoupdateApply, "apply", "", "Apply update for specified package, or \"all\" for every pending update")
	autoupdateCmd.Flags().BoolVar(&autoupdateForce, "force", false, "Ignore cache when checking")
	autoupdateCmd.Flags().BoolVar(&autoupdateCompile, "compile", false, "Run compile test after apply")
	autoupdateCmd.Flags().BoolVarP(&autoupdateClean, "clean", "c", false, "Remove the old ebuild after a successful apply, keeping only the new version")
	autoupdateCmd.Flags().IntVar(&autoupdateConcurrency, "concurrency", autoupdate.DefaultConcurrency, "max parallel checks (1-100)")
	autoupdateCmd.Flags().StringVar(&autoupdateOnly, "only", "", "Restrict --check to packages of this type: \"bin\" or \"source\"")

	overlayCmd.AddCommand(autoupdateCmd)
}

func runAutoupdate(cmd *cobra.Command, args []string) {
	// Validate --concurrency BEFORE any package work so a bad value fails fast
	// with a clear message and a non-zero exit (R4.2). The accepted range
	// mirrors autoupdate.WithConcurrency's [1, 100] bound.
	if autoupdateConcurrency < 1 || autoupdateConcurrency > 100 {
		logger.Error("--concurrency must be in range [1, 100], got %d", autoupdateConcurrency)
		osExit(1)
		return
	}

	// Validate --only up front so a typo fails fast rather than silently
	// checking everything. Only "bin"/"source" (or unset) are accepted.
	switch autoupdateOnly {
	case "", "bin", "source":
		// valid
	default:
		logger.Error("--only must be \"bin\" or \"source\", got %q", autoupdateOnly)
		osExit(1)
		return
	}

	appCtx, err := loadAppContextNoValidation()
	if err != nil {
		logger.Error("loading config: %v", err)
		osExit(1)
		return
	}

	overlayPath := appCtx.OverlayPath

	// Determine config directory for autoupdate
	home, err := os.UserHomeDir()
	if err != nil {
		logger.Error("failed to get home directory: %v", err)
		osExit(1)
		return
	}
	configDir := filepath.Join(home, ".config", "bentoo", "autoupdate")

	// Wire SIGINT/SIGTERM into a context so an in-flight check cancels cleanly.
	// The Checker threads this context through every outbound HTTP/LLM call, so
	// the run aborts within ~2 s of a signal (R3.1). See signalContext for the
	// OQ-1 note on why cmd.Context() alone is not signal-aware.
	runCtx, stop := signalContext(cmd.Context())
	defer stop()

	// Compute the autoupdate cache TTL from config (R2.1, R2.2). GetCacheTTL
	// returns the user-configured value when positive, otherwise the
	// 3600-second default — so the duration here is always positive and safe
	// to pass to WithCacheTTL inside runCheck.
	cacheTTL := time.Duration(appCtx.Config.Autoupdate.GetCacheTTL()) * time.Second

	// Handle different modes
	switch {
	case autoupdateCheck:
		runCheck(runCtx, overlayPath, configDir, args, cacheTTL, appCtx.Config.Autoupdate.LLM, appCtx.Config.GitHub.Token)
	case autoupdateList:
		runList(configDir)
	case autoupdateApply == "all":
		runApplyAll(runCtx, overlayPath, configDir)
	case autoupdateApply != "":
		runApply(runCtx, overlayPath, configDir, autoupdateApply)
	default:
		// No flag specified, show help
		cmd.Help() //nolint:errcheck // help output failure is not actionable
	}
}

// runCheck handles the --check flag. cacheTTL must be a positive duration —
// the caller resolves it from AutoupdateConfig.GetCacheTTL, which guarantees a
// positive value (R2.1, R2.2). A non-positive cacheTTL is treated as "use the
// Checker default" and the WithCacheTTL option is skipped, since WithCacheTTL
// rejects non-positive values at construction time.
func runCheck(ctx context.Context, overlayPath, configDir string, args []string, cacheTTL time.Duration, llmCfg config.LLMConfig, githubToken string) {
	opts := []autoupdate.CheckerOption{
		autoupdate.WithConfigDir(configDir),
		autoupdate.WithContext(ctx),
		autoupdate.WithConcurrency(autoupdateConcurrency),
		// Restrict the batch to a package type when --only is set; empty is a
		// no-op (checks every package). Ignored on the single-package path.
		autoupdate.WithTypeFilter(autoupdateOnly),
		// Authenticate api.github.com from ~/.config/bentoo/config.yaml's
		// github.token (same source `overlay compare` uses). NewChecker lets a
		// GITHUB_TOKEN/GH_TOKEN env override an empty value, matching compare's
		// env > config precedence.
		autoupdate.WithGitHubToken(githubToken),
	}
	if cacheTTL > 0 {
		opts = append(opts, autoupdate.WithCacheTTL(cacheTTL))
	}

	// Wire an LLM provider into the check path (R5.2). newConfiguredLLMProvider
	// returns (nil, nil) when no provider is configured, (provider, nil) on
	// success, and (typed-nil, err) on a construction failure. The error must be
	// the PRIMARY guard: a failed constructor boxes a nil concrete pointer into a
	// NON-nil interface, so we wire WithLLMClient only on err==nil AND p!=nil —
	// never a typed-nil (which would make fetchUpstreamVersion dereference a nil
	// receiver). On failure we Warn and continue; --check still runs, skipping LLM
	// extraction. WithLLMProviderConfigured records that a provider WAS requested
	// (provider != "") so the Checker suppresses its "unused llm_prompt" Warn
	// (R5.3) and we avoid a double-warn with the failure line just below.
	if p, err := newConfiguredLLMProvider(llmCfg); err != nil {
		logger.Warn("LLM provider %q unavailable; --check will skip LLM version extraction: %v", llmCfg.Provider, err)
	} else if p != nil {
		opts = append(opts, autoupdate.WithLLMClient(p))
	}
	opts = append(opts, autoupdate.WithLLMProviderConfigured(llmCfg.Provider != ""))

	// Progress feedback: CheckAll fans out concurrently and otherwise prints
	// nothing until the final table, so show a live [pct%] done/total counter on
	// a single self-rewriting line (mirrors `overlay compare`). The callback is
	// driven by CheckAll's atomic counter, so the count is monotonic even though
	// it fires from many goroutines. Suppressed under --quiet; harmless on the
	// single-package path (CheckPackage never fires it).
	if !quiet {
		opts = append(opts, autoupdate.WithProgressCallback(func(done, total uint64) {
			percent := uint64(0)
			if total > 0 {
				percent = (done * 100) / total
			}
			fmt.Printf("\r  Checking: [%3d%%] %d/%d", percent, done, total)
		}))
	}

	checker, err := autoupdate.NewChecker(overlayPath, opts...)
	if err != nil {
		logger.Error("failed to initialize checker: %v", err)
		osExit(1)
		return
	}

	if len(args) > 0 {
		// Check specific package
		pkg := args[0]
		// ctx is threaded into the Checker via WithContext above, so every
		// outbound request observes it; CheckPackage takes no ctx parameter.
		result, err := checker.CheckPackage(pkg, autoupdateForce) //nolint:contextcheck // ctx is injected via autoupdate.WithContext
		if err != nil {
			logger.Error("failed to check package %s: %v", pkg, err)
			osExit(1)
			return
		}
		displayCheckResults([]autoupdate.CheckResult{*result})
		return
	}

	// Check all packages. CheckAll never returns a fatal error: every
	// per-package failure is captured in the BatchResult. ctx is threaded
	// into the Checker via WithContext above; CheckAll takes no ctx parameter.
	result := checker.CheckAll(autoupdateForce) //nolint:contextcheck // ctx is injected via autoupdate.WithContext

	// Clear the progress line before rendering results so the counter does not
	// bleed into the table. Mirrors `overlay compare`'s clear step.
	if !quiet {
		fmt.Print("\r                                        \r")
	}

	// Display the successfully checked packages.
	displayCheckResults(result.Items)

	// Emit one stderr line per per-package failure. FormatFailures is called
	// only after CheckAll has fully completed, so the output is deterministic.
	if result.HasFailures() {
		result.FormatFailures(os.Stderr)
	}

	// Exit with the contract-defined code: 0 all-ok, 1 partial, 2 total fail.
	osExit(result.ExitCode())
}

// displayCheckResults formats and displays check results
func displayCheckResults(results []autoupdate.CheckResult) {
	if len(results) == 0 {
		logger.Info("No packages configured for autoupdate")
		return
	}

	var updatesFound int
	var errorsFound int
	var warningsFound int
	var srcCount int
	var binCount int

	fmt.Println()
	output.Header.Println("Version Check Results")
	fmt.Println()

	for _, r := range results {
		tag := typeTag(r.Type)
		switch r.Type {
		case "bin":
			binCount++
		case "source":
			srcCount++
		}

		if r.Error != nil {
			errorsFound++
			output.Error.Printf("  %s%s: %v\n", tag, r.Package, r.Error)
			continue
		}

		if r.NotComparable {
			warningsFound++
			output.Warning.Printf("  %s%s: %q not comparable to current %s (check parser config)\n",
				tag, r.Package, r.UpstreamVersion, r.CurrentVersion)
			continue
		}

		if r.HasUpdate {
			updatesFound++
			cacheIndicator := ""
			if r.FromCache {
				cacheIndicator = output.Sprintf(output.Dim, " (cached)")
			}
			output.Success.Printf("  %s%s: %s → %s%s\n",
				tag, r.Package, r.CurrentVersion, r.UpstreamVersion, cacheIndicator)
		} else {
			output.Dim.Printf("  %s%s: %s (up to date)\n", tag, r.Package, r.CurrentVersion)
		}
	}

	fmt.Println()
	if updatesFound > 0 {
		output.Info.Printf("Found %d update(s) available\n", updatesFound)
		output.Info.Println("Use 'bentoo overlay autoupdate --list' to see pending updates")
	} else if warningsFound == 0 && errorsFound == 0 {
		output.Success.Println("All packages are up to date")
	}

	if warningsFound > 0 {
		output.Warning.Printf("%d package(s) had non-comparable upstream versions\n", warningsFound)
	}

	if errorsFound > 0 {
		output.Warning.Printf("%d package(s) had errors\n", errorsFound)
	}

	output.Dim.Printf("Checked %d source, %d bin\n", srcCount, binCount)
}

// typeTag renders a short, dim prefix marking a package's resolved type for the
// check report ("[bin] " / "[src] "). An unknown/empty type yields no tag so
// the line layout is unchanged when classification was unavailable.
func typeTag(t string) string {
	switch t {
	case "bin":
		return output.Sprintf(output.Dim, "[bin] ")
	case "source":
		return output.Sprintf(output.Dim, "[src] ")
	default:
		return ""
	}
}

// runList handles the --list flag
func runList(configDir string) {
	pending, err := autoupdate.NewPendingList(configDir)
	if err != nil {
		logger.Error("failed to load pending list: %v", err)
		osExit(1)
	}

	updates := pending.List()
	displayPendingUpdates(updates)
}

// displayPendingUpdates formats and displays pending updates
func displayPendingUpdates(updates []autoupdate.PendingUpdate) {
	if len(updates) == 0 {
		logger.Info("No pending updates")
		return
	}

	fmt.Println()
	output.Header.Println("Pending Updates")
	fmt.Println()

	for _, u := range updates {
		statusColor := getStatusColor(u.Status)
		statusStr := output.Sprintf(statusColor, "[%s]", u.Status)

		output.Package.Printf("  %s\n", u.Package)
		fmt.Printf("    Version: %s → %s\n", u.CurrentVersion, u.NewVersion)
		fmt.Printf("    Status:  %s\n", statusStr)
		if u.Error != "" {
			output.Error.Printf("    Error:   %s\n", u.Error)
		}
		fmt.Printf("    Detected: %s\n", u.DetectedAt.Format("2006-01-02 15:04:05"))
		fmt.Println()
	}

	output.Info.Printf("Total: %d pending update(s)\n", len(updates))
	output.Info.Println("Use 'bentoo overlay autoupdate --apply <package>' to apply an update")
	output.Info.Println("Or 'bentoo overlay autoupdate --apply all' to apply every pending update")
}

// getStatusColor returns the appropriate color for an update status
func getStatusColor(status autoupdate.UpdateStatus) *color.Color {
	switch status {
	case autoupdate.StatusPending:
		return output.Warning
	case autoupdate.StatusValidated:
		return output.Success
	case autoupdate.StatusFailed:
		return output.Error
	case autoupdate.StatusApplied:
		return output.Info
	default:
		return output.Dim
	}
}

// runApply handles the --apply flag. ctx is threaded into the Applier via
// WithApplierContext so a SIGINT/SIGTERM cancels the in-flight `pkgdev manifest`
// or compile child process within ~2 s (R1.1, R1.2). The existing orphan
// rollback path then removes the half-applied .ebuild (R1.3).
func runApply(ctx context.Context, overlayPath, configDir, pkg string) {
	applier, err := autoupdate.NewApplier(overlayPath, configDir,
		autoupdate.WithApplierContext(ctx),
		autoupdate.WithApplierClean(autoupdateClean),
	)
	if err != nil {
		logger.Error("failed to initialize applier: %v", err)
		osExit(1)
	}

	output.Info.Printf("Applying update for %s...\n", pkg)

	//nolint:contextcheck // ctx is propagated into Apply's spawned processes
	// via WithApplierContext (a.ctx) — the deliberate single-source wiring from
	// signal.NotifyContext in runApply. Apply takes no ctx param by design.
	result, err := applier.Apply(pkg, autoupdateCompile)
	if err != nil {
		displayApplyResult(result)
		osExit(1)
	}

	displayApplyResult(result)
}

// runApplyAll handles `--apply all`: it applies every pending update in turn,
// reusing a single Applier so the pending list and logs directory are loaded
// once. ctx is threaded into the Applier via WithApplierContext so a
// SIGINT/SIGTERM cancels the in-flight `pkgdev manifest` or compile child
// process (R1.1, R1.2).
//
// The package list is snapshotted up front: Apply mutates the underlying
// pending list (a successful apply deletes its entry), so iterating over the
// live map would be unsafe. Each Apply is independent — a failure on one
// package never aborts the others — and the process exits non-zero when any
// package failed, matching the single-package --apply contract.
func runApplyAll(ctx context.Context, overlayPath, configDir string) {
	applier, err := autoupdate.NewApplier(overlayPath, configDir,
		autoupdate.WithApplierContext(ctx),
		autoupdate.WithApplierClean(autoupdateClean),
	)
	if err != nil {
		logger.Error("failed to initialize applier: %v", err)
		osExit(1)
		return
	}

	updates := applier.Pending().List()
	if len(updates) == 0 {
		logger.Info("No pending updates to apply")
		return
	}

	results := make([]*autoupdate.ApplyResult, 0, len(updates))
	failures := 0
	for _, u := range updates {
		output.Info.Printf("Applying update for %s...\n", u.Package)

		//nolint:contextcheck // ctx is propagated into Apply's spawned processes
		// via WithApplierContext (a.ctx) — the deliberate single-source wiring from
		// signal.NotifyContext in the caller. Apply takes no ctx param by design.
		result, err := applier.Apply(u.Package, autoupdateCompile)
		if err != nil {
			failures++
		}
		results = append(results, result)
	}

	displayApplyAllResults(results, failures)

	if failures > 0 {
		osExit(1)
	}
}

// displayApplyAllResults renders the per-package outcomes of `--apply all`
// followed by an aggregate summary line.
func displayApplyAllResults(results []*autoupdate.ApplyResult, failures int) {
	for _, result := range results {
		displayApplyResult(result)
	}

	fmt.Println()
	output.Header.Println("Apply All Summary")
	applied := len(results) - failures
	output.Success.Printf("  Applied: %d\n", applied)
	if failures > 0 {
		output.Error.Printf("  Failed:  %d\n", failures)
	}
	if applied > 0 {
		output.Info.Println("Don't forget to commit the changes with 'bentoo overlay commit'")
	}
}

// displayApplyResult formats and displays apply result
func displayApplyResult(result *autoupdate.ApplyResult) {
	if result == nil {
		return
	}

	fmt.Println()
	output.Header.Println("Apply Result")
	fmt.Println()

	output.Package.Printf("  %s\n", result.Package)
	fmt.Printf("    Version: %s → %s\n", result.OldVersion, result.NewVersion)

	if result.Success {
		output.Success.Println("    Status:  Success")
		if result.CleanedOldVersion != "" {
			fmt.Printf("    Removed: %s-%s.ebuild (old version)\n", filepath.Base(result.Package), result.CleanedOldVersion)
		}
		if result.CleanWarning != "" {
			output.Warning.Printf("    Clean:   %s\n", result.CleanWarning)
		}
		output.Success.Println("\n✓ Update applied successfully")
		output.Info.Println("Don't forget to commit the changes with 'bentoo overlay commit'")
	} else {
		output.Error.Println("    Status:  Failed")
		if result.Error != nil {
			output.Error.Printf("    Error:   %v\n", result.Error)
		}
		if result.LogPath != "" {
			output.Info.Printf("    Log:     %s\n", result.LogPath)
		}
	}
}
