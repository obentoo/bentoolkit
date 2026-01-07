package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/logger"
	"github.com/obentoo/bentoolkit/internal/overlay"
	"github.com/spf13/cobra"
)

// RenameFlags holds command-line flags for the rename operation
type RenameFlags struct {
	DryRun     bool // --dry-run: simulate without executing
	Yes        bool // -y, --yes: skip confirmation prompts
	NoManifest bool // --no-manifest: skip Manifest updates
	Force      bool // --force: proceed despite warnings
}

var renameFlags RenameFlags

var renameCmd = &cobra.Command{
	Use:   "rename <category>:<package-pattern>:<old-version> => <new-version>",
	Short: "Bulk rename ebuilds from old version to new version",
	Long: `Rename multiple ebuild files matching a pattern from an old version to a new version.

The command accepts a pattern in the format:
  <category>:<package-pattern>:<old-version> => <new-version>

Where:
  - category: specific category name or "*" for all categories
  - package-pattern: glob pattern for package names (e.g., "gst-*", "python-*")
  - old-version: exact version to match (without revision suffix)
  - new-version: target version to rename to

Examples:
  # Rename all gst-* packages in media-plugins from 1.24.11 to 1.26.10
  bentoo overlay rename media-plugins:gst-*:1.24.11 => 1.26.10

  # Global search across all categories
  bentoo overlay rename *:python-*:3.11.0 => 3.12.0

  # Dry run to preview changes
  bentoo overlay rename --dry-run media-plugins:gst-*:1.24.11 => 1.26.10

  # Skip confirmation prompt
  bentoo overlay rename -y media-plugins:gst-*:1.24.11 => 1.26.10

  # Force rename even if version-specific files exist
  bentoo overlay rename --force media-plugins:gst-*:1.24.11 => 1.26.10`,
	Args: cobra.ExactArgs(3),
	Run:  runRename,
}

func init() {
	renameCmd.Flags().BoolVarP(&renameFlags.DryRun, "dry-run", "n", false, "Show what would be renamed without making changes")
	renameCmd.Flags().BoolVarP(&renameFlags.Yes, "yes", "y", false, "Skip confirmation prompts (except for global search without --force)")
	renameCmd.Flags().BoolVar(&renameFlags.NoManifest, "no-manifest", false, "Skip Manifest updates after renaming")
	renameCmd.Flags().BoolVar(&renameFlags.Force, "force", false, "Proceed despite version-specific files or conflicts")
	overlayCmd.AddCommand(renameCmd)
}

func runRename(cmd *cobra.Command, args []string) {
	// Parse command arguments
	spec, err := ParseRenameArgs(args)
	if err != nil {
		logger.Error("%v", err)
		os.Exit(1)
	}

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		logger.Error("loading config: %v", err)
		os.Exit(1)
	}

	// Convert flags to options
	opts := &overlay.RenameOptions{
		DryRun:     renameFlags.DryRun,
		SkipPrompt: renameFlags.Yes,
		NoManifest: renameFlags.NoManifest,
		Force:      renameFlags.Force,
	}

	// Execute rename operation
	result, err := overlay.Rename(cfg, spec, opts)
	if err != nil {
		logger.Error("%v", err)
		os.Exit(1)
	}

	// Display results
	if result != nil {
		logger.Info("%s", overlay.FormatRenameResult(result, opts.DryRun))
	}
}

// Errors for command parsing
var (
	ErrInvalidArgCount     = errors.New("invalid argument count: expected 3 arguments")
	ErrMissingSeparator    = errors.New("second argument must be '=>'")
	ErrInvalidSpecFormat   = errors.New("first argument must be in format <category>:<package-pattern>:<old-version>")
	ErrEmptyCategory       = errors.New("category cannot be empty")
	ErrEmptyPackagePattern = errors.New("package pattern cannot be empty")
	ErrEmptyOldVersion     = errors.New("old version cannot be empty")
	ErrEmptyNewVersion     = errors.New("new version cannot be empty")
)

// ParseRenameArgs parses command-line arguments into a RenameSpec.
// Expected format: ["<category>:<package-pattern>:<old-version>", "=>", "<new-version>"]
func ParseRenameArgs(args []string) (*overlay.RenameSpec, error) {
	if len(args) != 3 {
		return nil, fmt.Errorf("%w: got %d", ErrInvalidArgCount, len(args))
	}

	// Validate separator
	if args[1] != "=>" {
		return nil, fmt.Errorf("%w: got '%s'", ErrMissingSeparator, args[1])
	}

	// Parse first argument: category:package-pattern:old-version
	parts := strings.SplitN(args[0], ":", 3)
	if len(parts) != 3 {
		return nil, fmt.Errorf("%w: got '%s'", ErrInvalidSpecFormat, args[0])
	}

	category := strings.TrimSpace(parts[0])
	packagePattern := strings.TrimSpace(parts[1])
	oldVersion := strings.TrimSpace(parts[2])
	newVersion := strings.TrimSpace(args[2])

	// Validate components
	if category == "" {
		return nil, ErrEmptyCategory
	}
	if packagePattern == "" {
		return nil, ErrEmptyPackagePattern
	}
	if oldVersion == "" {
		return nil, ErrEmptyOldVersion
	}
	if newVersion == "" {
		return nil, ErrEmptyNewVersion
	}

	return &overlay.RenameSpec{
		Category:       category,
		PackagePattern: packagePattern,
		OldVersion:     oldVersion,
		NewVersion:     newVersion,
	}, nil
}
