package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/logger"
	"github.com/obentoo/bentoolkit/internal/common/output"
	"github.com/obentoo/bentoolkit/internal/common/provider"
	"github.com/obentoo/bentoolkit/internal/overlay"
	"github.com/spf13/cobra"
)

var (
	compareClone        bool
	compareCacheDir     string
	compareNoCache      bool
	compareTimeout      int
	compareToken        string
	compareOnlyOutdated bool
	compareSync         bool
)

var compareCmd = &cobra.Command{
	Use:   "compare [repository]",
	Short: "Compare overlay packages with upstream repository",
	Long: `Compare package versions in your local Bentoo overlay against
an upstream repository.

Any repository from the Gentoo ecosystem (~428 repos) can be used by name.
The repository list is fetched from the official Gentoo repositories.xml
registry and cached locally. Use --sync to force a refresh.

Custom repositories can also be defined in ~/.config/bentoo/config.yaml
and take priority over registry entries.

The provider (GitHub API, GitLab API, or Git) is automatically detected
based on the repository's source URL. Use --clone to force git clone.

By default, all packages are shown (outdated, up-to-date, and newer).
Use --only-outdated to filter and show only packages that need updates.

Examples:
  bentoo overlay compare                    # Compare with gentoo (API)
  bentoo overlay compare guru               # Compare with GURU (API)
  bentoo overlay compare some-overlay       # Compare with any registered repo
  bentoo overlay compare --clone            # Compare with gentoo (git clone)
  bentoo overlay compare --sync             # Refresh repo list before comparing
  bentoo overlay compare --only-outdated    # Show only outdated packages`,
	Args: cobra.MaximumNArgs(1),
	Run:  runCompare,
}

func init() {
	compareCmd.Flags().BoolVar(&compareClone, "clone", false, "Use git clone instead of API")
	compareCmd.Flags().StringVar(&compareCacheDir, "cache-dir", "", "Directory to cache data")
	compareCmd.Flags().BoolVar(&compareNoCache, "no-cache", false, "Disable caching")
	compareCmd.Flags().IntVar(&compareTimeout, "timeout", 30, "HTTP request timeout in seconds")
	compareCmd.Flags().StringVar(&compareToken, "token", "", "Auth token for API provider")
	compareCmd.Flags().BoolVar(&compareOnlyOutdated, "only-outdated", false, "Show only outdated packages (Bentoo < Gentoo)")
	compareCmd.Flags().BoolVar(&compareSync, "sync", false, "Force refresh of repository list")
	overlayCmd.AddCommand(compareCmd)
}

func runCompare(cmd *cobra.Command, args []string) {
	ctx, err := loadAppContext()
	if err != nil {
		logger.Error("loading config: %v", err)
		osExit(1)
	}

	overlayPath := ctx.OverlayPath
	cfg := ctx.Config

	// Determine repository name (default: gentoo)
	repoName := "gentoo"
	if len(args) > 0 {
		repoName = args[0]
	}

	// Convert config repos to provider.RepositoryInfo map
	configRepos := convertConfigRepos(cfg)

	// Create repository registry
	registry, err := provider.NewRepositoryRegistry()
	if err != nil {
		logger.Error("Failed to initialize repository registry: %v", err)
		osExit(1)
	}

	if compareSync {
		if err := registry.Sync(); err != nil {
			logger.Error("Failed to sync repository list: %v", err)
			osExit(1)
		}
	}

	// Resolve repository info
	repoInfo, err := provider.ResolveRepository(repoName, configRepos, registry)
	if err != nil {
		logger.Error("Repository '%s' not found.", repoName)
		configNames := provider.ListAvailableRepositories(configRepos, nil)
		registryNames := provider.ListAvailableRepositories(nil, registry)
		if len(configNames) > 0 {
			logger.Info("Config repositories: %s", strings.Join(configNames, ", "))
		}
		if len(registryNames) > 0 {
			logger.Info("Registry repositories: use `eselect repository list` to see all available")
		} else {
			logger.Info("Registry unavailable. Use --sync to refresh or run `eselect repository list`")
		}
		osExit(1)
	}

	// Apply token (priority: flag > env > config > repo-specific)
	token := compareToken
	if token == "" {
		token = os.Getenv("GITHUB_TOKEN")
	}
	if token == "" {
		token = cfg.GitHub.Token
	}
	if token != "" && repoInfo.Token == "" {
		repoInfo.Token = token
	}

	// Create provider
	prov, err := provider.NewProvider(repoInfo, compareClone)
	if err != nil {
		logger.Error("Failed to create provider: %v", err)
		osExit(1)
	}
	defer prov.Close() //nolint:errcheck

	// Set timeout for API providers
	if ghProv, ok := prov.(*provider.GitHubProvider); ok {
		ghProv.HTTPClient.Timeout = time.Duration(compareTimeout) * time.Second
		if compareNoCache {
			ghProv.CacheDir = ""
		}
	}

	// Check rate limit for GitHub provider - block if exhausted
	if ghProv, ok := prov.(*provider.GitHubProvider); ok {
		remaining, resetTime, err := ghProv.GetRateLimitInfo()
		if err == nil {
			switch {
			case remaining == 0:
				logger.Error("GitHub API rate limit exceeded (resets at %s)", resetTime.Format("15:04:05"))
				logger.Info("")
				logger.Info("Options:")
				logger.Info("  1. Use --clone to download the repository:")
				logger.Info("     bentoo overlay compare %s --clone", repoName)
				logger.Info("")
				logger.Info("  2. Configure a local repository path in ~/.config/bentoo/config.yaml:")
				logger.Info("     repositories:")
				logger.Info("       gentoo-local:")
				logger.Info("         provider: git")
				logger.Info("         url: /var/db/repos/gentoo")
				logger.Info("")
				logger.Info("  3. Wait until %s for rate limit reset", resetTime.Format("15:04:05"))
				osExit(1)
			case remaining < 10:
				logger.Warn("GitHub API rate limit low: %d requests remaining (resets at %s)",
					remaining, resetTime.Format("15:04:05"))
				if !compareClone {
					logger.Info("Tip: Use --clone flag to avoid rate limits")
				}
			case verbose:
				logger.Debug("GitHub API rate limit: %d requests remaining", remaining)
			}
		}
	}

	// Scan local overlay
	logger.Info("Scanning Bentoo overlay at %s...", overlayPath)
	scanResult, err := overlay.ScanOverlay(overlayPath)
	if err != nil {
		logger.Error("scanning overlay: %v", err)
		osExit(1)
	}

	if len(scanResult.Packages) == 0 {
		logger.Warn("No packages found in overlay")
		osExit(0)
	}

	logger.Info("Found %s packages in Bentoo overlay",
		output.Sprint(output.Info, fmt.Sprintf("%d", len(scanResult.Packages))))

	// Report scan errors if any
	if len(scanResult.Errors) > 0 {
		logger.Warn("Encountered %d errors during scan:", len(scanResult.Errors))
		for _, e := range scanResult.Errors {
			logger.Debug("  %s: %s", e.Path, e.Message)
		}
	}

	// Compare with upstream
	logger.Info("Comparing with %s using %s...", repoInfo.Name, prov.GetName())

	opts := overlay.CompareOptions{
		OnlyOutdated:  compareOnlyOutdated,
		IncludeSynced: !compareOnlyOutdated, // Include synced unless only-outdated is set
		ProgressCallback: func(done, total uint64) {
			percent := uint64(0)
			if total > 0 {
				percent = (done * 100) / total
			}
			fmt.Printf("\r  Checking: [%3d%%] %d/%d", percent, done, total)
		},
	}

	report, err := overlay.CompareWithProvider(scanResult.Packages, prov, opts)
	if err != nil {
		// Check if it's a rate limit error and suggest --clone
		if strings.Contains(err.Error(), "rate limit") && !compareClone {
			logger.Error("GitHub API rate limit exceeded.")
			logger.Info("Try using --clone flag to download the repository instead:")
			logger.Info("  bentoo overlay compare %s --clone", repoName)
			osExit(1)
		}
		logger.Error("comparing packages: %v", err)
		osExit(1)
	}

	// Clear progress line
	fmt.Printf("\r%s\r", "                                                                  ")

	// Display results
	if len(report.Results) == 0 {
		logger.Info("%s", output.Sprintf(output.Success, "All packages are up-to-date with %s!", repoInfo.Name))
		printComparisonSummary(report, repoInfo.Name)
		return
	}

	// Print the formatted report
	fmt.Print(overlay.FormatReport(report))

	// Print summary
	printComparisonSummary(report, repoInfo.Name)
}

func truncatePkgName(name string, maxLen int) string {
	if len(name) <= maxLen {
		return name + strings.Repeat(" ", maxLen-len(name))
	}
	return name[:maxLen-3] + "..."
}

func printComparisonSummary(report *overlay.CompareReport, repoName string) {
	logger.Info("\nSummary:")
	logger.Info("  Total packages scanned: %d", report.TotalPackages)
	logger.Info("  Found in both repos: %d", report.ComparedPackages-report.NotInRemoteCount-report.ErrorCount)
	logger.Info("  Only in Bentoo: %d", report.NotInRemoteCount)

	if report.ErrorCount > 0 {
		logger.Warn("  Errors (API issues): %d", report.ErrorCount)
	}
}

// convertConfigRepos converts config.RepoConfig map to provider.RepositoryInfo map
func convertConfigRepos(cfg *config.Config) map[string]*provider.RepositoryInfo {
	if cfg.Repositories == nil {
		return nil
	}

	result := make(map[string]*provider.RepositoryInfo)
	for name, repo := range cfg.Repositories {
		result[name] = &provider.RepositoryInfo{
			Name:     name,
			Provider: repo.Provider,
			URL:      repo.URL,
			Token:    repo.Token,
			Branch:   repo.Branch,
		}
	}
	return result
}
