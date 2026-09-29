package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/git"
	"github.com/obentoo/bentoolkit/internal/common/logger"
	"github.com/obentoo/bentoolkit/internal/common/output"
	"github.com/obentoo/bentoolkit/internal/overlay"
	"github.com/spf13/cobra"
)

var (
	commitMessage string
	commitDryRun  bool
	commitYes     bool
)

// newCommitCmd builds `overlay commit`.
func newCommitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:         "commit",
		Annotations: map[string]string{cancellableAnnotation: "true"},
		Short:       "Commit staged changes with auto-generated message",
		Long: `Commit staged changes to the overlay repository.
If no message is provided with -m, an automatic commit message is generated
based on the ebuild changes and a confirmation prompt is shown.
Use -y to skip the confirmation prompt and commit automatically.`,
		Run: runCommit,
	}
	cmd.Flags().StringVarP(&commitMessage, "message", "m", "", "Custom commit message (bypasses auto-generation)")
	cmd.Flags().BoolVarP(&commitDryRun, "dry-run", "n", false, "Show what would be committed without committing")
	cmd.Flags().BoolVarP(&commitYes, "yes", "y", false, "Skip confirmation prompt and commit automatically")
	return cmd
}

func runCommit(cmd *cobra.Command, args []string) {
	appCtx, err := loadAppContext()
	if err != nil {
		logger.Error("loading config: %v", err)
		osExit(1)
	}

	cfg := appCtx.Config

	// Get git user info
	user, email, err := cfg.GetGitUser()
	if err != nil {
		logger.Error("%v", err)
		osExit(1)
	}
	// Store in config for commit function
	cfg.Git.User = user
	cfg.Git.Email = email

	// If custom message provided, use it directly
	if commitMessage != "" {
		if err := commitOverlay(cmd, cfg, commitMessage); err != nil {
			logger.Error("%v", err)
			osExit(1)
		}
		logger.Info("Changes committed successfully.")
		return
	}

	overlayPath := appCtx.OverlayPath

	runner := git.NewGitRunner(overlayPath)
	// Scoped to this one git call, for the reason commitOverlay gives: the
	// confirmation prompt below must stay killable by Ctrl-C.
	statusCtx, stopStatus := signalContext(cmd.Context())
	entries, err := runner.Status(statusCtx)
	stopStatus()
	if err != nil {
		logger.Error("getting status: %v", err)
		osExit(1)
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
		osExit(0)
	}

	// Analyze changes and generate message (includes both ebuild packages and
	// non-ebuild files such as eclasses, profiles, licenses, and metadata).
	changes := overlay.AnalyzeChanges(stagedEntries)
	fileChanges := overlay.AnalyzeRepoFileChanges(stagedEntries)
	generatedMessage := overlay.GenerateCommitMessage(changes, fileChanges)

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

	// Skip confirmation if -y flag is set
	if commitYes {
		if err := commitOverlay(cmd, cfg, generatedMessage); err != nil {
			logger.Error("%v", err)
			osExit(1)
		}
		logger.Info("Changes committed successfully.")
		return
	}

	fmt.Print("Proceed? [y]es / [e]dit / [c]ancel: ")

	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		logger.Error("reading input: %v", err)
		osExit(1)
	}

	input = strings.TrimSpace(strings.ToLower(input))

	switch input {
	case "y", "yes", "":
		// Proceed with generated message
		if err := commitOverlay(cmd, cfg, generatedMessage); err != nil {
			logger.Error("%v", err)
			osExit(1)
		}
		logger.Info("Changes committed successfully.")

	case "e", "edit":
		// Allow user to enter custom message
		fmt.Print("Enter commit message: ")
		customMessage, err := reader.ReadString('\n')
		if err != nil {
			logger.Error("reading input: %v", err)
			osExit(1)
		}
		customMessage = strings.TrimSpace(customMessage)
		if customMessage == "" {
			logger.Warn("Commit cancelled (empty message).")
			osExit(1)
		}
		if err := commitOverlay(cmd, cfg, customMessage); err != nil {
			logger.Error("%v", err)
			osExit(1)
		}
		logger.Info("Changes committed successfully.")

	case "c", "cancel":
		logger.Info("Commit cancelled.")
		osExit(1)

	default:
		logger.Error("Invalid option. Commit cancelled.")
		osExit(1)
	}
}

// commitOverlay runs overlay.Commit under a context that SIGINT and SIGTERM
// cancel (S054-R5.8).
//
// The context lives only as long as the git call, not as long as runCommit.
// While signalContext is registered, a Ctrl-C cancels its context instead of
// ending the process, so one held across the confirmation prompt would leave
// that prompt unkillable: the read on stdin would simply keep waiting.
func commitOverlay(cmd *cobra.Command, cfg *config.Config, message string) error {
	ctx, stop := signalContext(cmd.Context())
	defer stop()
	return overlay.Commit(ctx, cfg, message)
}
