package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/obentoo/bentoo-tools/internal/common/config"
	"github.com/obentoo/bentoo-tools/internal/common/git"
	"github.com/obentoo/bentoo-tools/internal/common/logger"
	"github.com/obentoo/bentoo-tools/internal/common/output"
	"github.com/obentoo/bentoo-tools/internal/overlay"
	"github.com/spf13/cobra"
)

var (
	commitMessage string
	commitDryRun  bool
)

var commitCmd = &cobra.Command{
	Use:   "commit",
	Short: "Commit staged changes with auto-generated message",
	Long: `Commit staged changes to the overlay repository.
If no message is provided with -m, an automatic commit message is generated
based on the ebuild changes and a confirmation prompt is shown.`,
	Run: runCommit,
}

func init() {
	commitCmd.Flags().StringVarP(&commitMessage, "message", "m", "", "Custom commit message (bypasses auto-generation)")
	commitCmd.Flags().BoolVarP(&commitDryRun, "dry-run", "n", false, "Show what would be committed without committing")
	overlayCmd.AddCommand(commitCmd)
}

func runCommit(cmd *cobra.Command, args []string) {
	cfg, err := config.Load()
	if err != nil {
		logger.Error("loading config: %v", err)
		os.Exit(1)
	}

	// Get git user info
	user, email, err := cfg.GetGitUser()
	if err != nil {
		logger.Error("%v", err)
		os.Exit(1)
	}
	// Store in config for commit function
	cfg.Git.User = user
	cfg.Git.Email = email

	// If custom message provided, use it directly
	if commitMessage != "" {
		if err := overlay.Commit(cfg, commitMessage); err != nil {
			logger.Error("%v", err)
			os.Exit(1)
		}
		logger.Info("Changes committed successfully.")
		return
	}

	// Get staged changes for auto-generation
	overlayPath, err := cfg.GetOverlayPath()
	if err != nil {
		logger.Error("%v", err)
		os.Exit(1)
	}

	runner := git.NewGitRunner(overlayPath)
	entries, err := runner.Status()
	if err != nil {
		logger.Error("getting status: %v", err)
		os.Exit(1)
	}

	// Filter to only staged entries
	var stagedEntries []git.StatusEntry
	for _, e := range entries {
		status := strings.TrimSpace(e.Status)
		if len(status) > 0 && status[0] != ' ' && status != "??" {
			stagedEntries = append(stagedEntries, e)
		}
	}

	if len(stagedEntries) == 0 {
		logger.Warn("No staged changes to commit.")
		os.Exit(0)
	}

	// Analyze changes and generate message
	changes := overlay.AnalyzeChanges(stagedEntries)
	generatedMessage := overlay.GenerateMessage(changes)

	// Dry-run mode: just show what would be committed
	if commitDryRun {
		logger.Info("Dry-run mode - would commit with message:")
		fmt.Printf("  %s\n\n", output.Sprint(output.Info, generatedMessage))
		logger.Info("Staged files:")
		for _, e := range stagedEntries {
			fmt.Printf("  %s %s\n", output.FormatStatus(overlay.StatusLabel(e.Status)), e.FilePath)
		}
		return
	}

	// Show preview and prompt
	logger.Info("Generated commit message:")
	fmt.Printf("  %s\n\n", output.Sprint(output.Info, generatedMessage))
	fmt.Print("Proceed? [y]es / [e]dit / [c]ancel: ")

	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		logger.Error("reading input: %v", err)
		os.Exit(1)
	}

	input = strings.TrimSpace(strings.ToLower(input))

	switch input {
	case "y", "yes", "":
		// Proceed with generated message
		if err := overlay.Commit(cfg, generatedMessage); err != nil {
			logger.Error("%v", err)
			os.Exit(1)
		}
		logger.Info("Changes committed successfully.")

	case "e", "edit":
		// Allow user to enter custom message
		fmt.Print("Enter commit message: ")
		customMessage, err := reader.ReadString('\n')
		if err != nil {
			logger.Error("reading input: %v", err)
			os.Exit(1)
		}
		customMessage = strings.TrimSpace(customMessage)
		if customMessage == "" {
			logger.Warn("Commit cancelled (empty message).")
			os.Exit(0)
		}
		if err := overlay.Commit(cfg, customMessage); err != nil {
			logger.Error("%v", err)
			os.Exit(1)
		}
		logger.Info("Changes committed successfully.")

	case "c", "cancel":
		logger.Info("Commit cancelled.")
		os.Exit(0)

	default:
		logger.Error("Invalid option. Commit cancelled.")
		os.Exit(1)
	}
}
