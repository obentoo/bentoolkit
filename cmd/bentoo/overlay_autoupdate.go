package main

import (
	"fmt"
	"os"
	"path/filepath"

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
)

var autoupdateCmd = &cobra.Command{
	Use:   "autoupdate [package]",
	Short: "Check and apply ebuild version updates",
	Long: `Automatically check upstream sources for new versions and apply updates.

Examples:
  bentoo overlay autoupdate --check              Check all packages for updates
  bentoo overlay autoupdate --check net-misc/foo Check specific package
  bentoo overlay autoupdate --check --force      Check ignoring cache
  bentoo overlay autoupdate --list               List pending updates
  bentoo overlay autoupdate --apply net-misc/foo Apply update for package
  bentoo overlay autoupdate --apply net-misc/foo --compile  Apply and compile test`,
	Run: runAutoupdate,
}

func init() {
	autoupdateCmd.Flags().BoolVar(&autoupdateCheck, "check", false, "Check for updates")
	autoupdateCmd.Flags().BoolVar(&autoupdateList, "list", false, "List pending updates")
	autoupdateCmd.Flags().StringVar(&autoupdateApply, "apply", "", "Apply update for specified package")
	autoupdateCmd.Flags().BoolVar(&autoupdateForce, "force", false, "Ignore cache when checking")
	autoupdateCmd.Flags().BoolVar(&autoupdateCompile, "compile", false, "Run compile test after apply")

	overlayCmd.AddCommand(autoupdateCmd)
}

func runAutoupdate(cmd *cobra.Command, args []string) {
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

	// Handle different modes
	switch {
	case autoupdateCheck:
		runCheck(overlayPath, configDir, args)
	case autoupdateList:
		runList(configDir)
	case autoupdateApply != "":
		runApply(overlayPath, configDir, autoupdateApply)
	default:
		// No flag specified, show help
		cmd.Help()
	}
}

// runCheck handles the --check flag
func runCheck(overlayPath, configDir string, args []string) {
	checker, err := autoupdate.NewChecker(overlayPath, autoupdate.WithConfigDir(configDir))
	if err != nil {
		logger.Error("failed to initialize checker: %v", err)
		os.Exit(1)
	}

	var results []autoupdate.CheckResult

	if len(args) > 0 {
		// Check specific package
		pkg := args[0]
		result, err := checker.CheckPackage(pkg, autoupdateForce)
		if err != nil {
			logger.Error("failed to check package %s: %v", pkg, err)
			os.Exit(1)
		}
		results = []autoupdate.CheckResult{*result}
	} else {
		// Check all packages
		results, err = checker.CheckAll(autoupdateForce)
		if err != nil {
			logger.Error("failed to check packages: %v", err)
			os.Exit(1)
		}
	}

	// Display results
	displayCheckResults(results)
}

// displayCheckResults formats and displays check results
func displayCheckResults(results []autoupdate.CheckResult) {
	if len(results) == 0 {
		logger.Info("No packages configured for autoupdate")
		return
	}

	var updatesFound int
	var errorsFound int

	fmt.Println()
	output.Header.Println("Version Check Results")
	fmt.Println()

	for _, r := range results {
		if r.Error != nil {
			errorsFound++
			output.Error.Printf("  %s: %v\n", r.Package, r.Error)
			continue
		}

		if r.HasUpdate {
			updatesFound++
			cacheIndicator := ""
			if r.FromCache {
				cacheIndicator = output.Sprintf(output.Dim, " (cached)")
			}
			output.Success.Printf("  %s: %s → %s%s\n",
				r.Package, r.CurrentVersion, r.UpstreamVersion, cacheIndicator)
		} else {
			output.Dim.Printf("  %s: %s (up to date)\n", r.Package, r.CurrentVersion)
		}
	}

	fmt.Println()
	if updatesFound > 0 {
		output.Info.Printf("Found %d update(s) available\n", updatesFound)
		output.Info.Println("Use 'bentoo overlay autoupdate --list' to see pending updates")
	} else {
		output.Success.Println("All packages are up to date")
	}

	if errorsFound > 0 {
		output.Warning.Printf("%d package(s) had errors\n", errorsFound)
	}
}

// runList handles the --list flag
func runList(configDir string) {
	pending, err := autoupdate.NewPendingList(configDir)
	if err != nil {
		logger.Error("failed to load pending list: %v", err)
		os.Exit(1)
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

// runApply handles the --apply flag
func runApply(overlayPath, configDir, pkg string) {
	applier, err := autoupdate.NewApplier(overlayPath, configDir)
	if err != nil {
		logger.Error("failed to initialize applier: %v", err)
		os.Exit(1)
	}

	output.Info.Printf("Applying update for %s...\n", pkg)

	result, err := applier.Apply(pkg, autoupdateCompile)
	if err != nil {
		displayApplyResult(result)
		os.Exit(1)
	}

	displayApplyResult(result)
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
