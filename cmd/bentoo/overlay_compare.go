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
	compareClone         bool
	compareCacheDir      string
	compareNoCache       bool
	compareTimeout       int
	compareToken         string
	compareIncludeSynced bool
)

var compareCmd = &cobra.Command{
	Use:   "compare [repository]",
	Short: "Compare overlay packages with upstream repository",
	Long: `Compare package versions in your local Bentoo overlay against
an upstream repository.

Repositories:
  gentoo (default) - Official Gentoo repository (github.com/gentoo/gentoo)
  guru             - Gentoo User Repository (github.com/gentoo/guru)
  custom           - Defined in ~/.config/bentoo/config.yaml

The provider (GitHub API, GitLab API, or Git) is automatically detected
based on the repository configuration. Use --clone to force git clone.

By default, only outdated packages are shown. Use --include-synced to also
display packages that have the same version in both repositories.

Examples:
  bentoo overlay compare                    # Compare with gentoo (API)
  bentoo overlay compare guru               # Compare with GURU (API)
  bentoo overlay compare --clone            # Compare with gentoo (git clone)
  bentoo overlay compare guru --clone       # Compare with GURU (git clone)
  bentoo overlay compare --include-synced   # Include up-to-date packages`,
	Args: cobra.MaximumNArgs(1),
	Run:  runCompare,
}

func init() {
	compareCmd.Flags().BoolVar(&compareClone, "clone", false, "Use git clone instead of API")
	compareCmd.Flags().StringVar(&compareCacheDir, "cache-dir", "", "Directory to cache data")
	compareCmd.Flags().BoolVar(&compareNoCache, "no-cache", false, "Disable caching")
	compareCmd.Flags().IntVar(&compareTimeout, "timeout", 30, "HTTP request timeout in seconds")
	compareCmd.Flags().StringVar(&compareToken, "token", "", "Auth token for API provider")
	compareCmd.Flags().BoolVar(&compareIncludeSynced, "include-synced", false, "Include packages with same version in both repositories")
	overlayCmd.AddCommand(compareCmd)
}

func runCompare(cmd *cobra.Command, args []string) {
	cfg, err := config.Load()
	if err != nil {
		logger.Error("loading config: %v", err)
		os.Exit(1)
	}

	overlayPath, err := cfg.GetOverlayPath()
	if err != nil {
		logger.Error("%v", err)
		os.Exit(1)
	}

	// Determine repository name (default: gentoo)
	repoName := "gentoo"
	if len(args) > 0 {
		repoName = args[0]
	}

	// Convert config repos to provider.RepositoryInfo map
	configRepos := convertConfigRepos(cfg)

	// Resolve repository info
	repoInfo, err := provider.ResolveRepository(repoName, configRepos)
	if err != nil {
		logger.Error("Repository '%s' not found.", repoName)
		logger.Info("Available repositories: %s", strings.Join(provider.ListAvailableRepositories(configRepos), ", "))
		os.Exit(1)
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
		os.Exit(1)
	}
	defer prov.Close()

	// Set timeout for API providers
	if ghProv, ok := prov.(*provider.GitHubProvider); ok {
		ghProv.HTTPClient.Timeout = time.Duration(compareTimeout) * time.Second
		if compareNoCache {
			ghProv.CacheDir = ""
		}
	}

	// Check rate limit for GitHub provider
	if ghProv, ok := prov.(*provider.GitHubProvider); ok {
		remaining, resetTime, err := ghProv.GetRateLimitInfo()
		if err == nil {
			if remaining < 10 {
				logger.Warn("GitHub API rate limit low: %d requests remaining (resets at %s)",
					remaining, resetTime.Format("15:04:05"))
				if !compareClone {
					logger.Info("Tip: Use --clone flag to avoid rate limits")
				}
			} else if verbose {
				logger.Debug("GitHub API rate limit: %d requests remaining", remaining)
			}
		}
	}

	// Scan local overlay
	logger.Info("Scanning Bentoo overlay at %s...", overlayPath)
	scanResult, err := overlay.ScanOverlay(overlayPath)
	if err != nil {
		logger.Error("scanning overlay: %v", err)
		os.Exit(1)
	}

	if len(scanResult.Packages) == 0 {
		logger.Warn("No packages found in overlay")
		os.Exit(0)
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
		OnlyOutdated:  !compareIncludeSynced,
		IncludeSynced: compareIncludeSynced,
		ProgressCallback: func(current, total int, pkg string) {
			percent := (current * 100) / total
			fmt.Printf("\r  Checking: [%3d%%] %s", percent, truncatePkgName(pkg, 40))
		},
	}

	report, err := overlay.CompareWithProvider(scanResult.Packages, prov, opts)
	if err != nil {
		// Check if it's a rate limit error and suggest --clone
		if strings.Contains(err.Error(), "rate limit") && !compareClone {
			logger.Error("GitHub API rate limit exceeded.")
			logger.Info("Try using --clone flag to download the repository instead:")
			logger.Info("  bentoo overlay compare %s --clone", repoName)
			os.Exit(1)
		}
		logger.Error("comparing packages: %v", err)
		os.Exit(1)
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
