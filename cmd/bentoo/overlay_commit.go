package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/obentoo/bentoolkit/internal/common/config"
	"github.com/obentoo/bentoolkit/internal/common/git"
	"github.com/obentoo/bentoolkit/internal/common/logging"
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
		RunE: runCommit,
	}
	cmd.Flags().StringVarP(&commitMessage, "message", "m", "", "Custom commit message (bypasses auto-generation)")
	cmd.Flags().BoolVarP(&commitDryRun, "dry-run", "n", false, "Show what would be committed without committing")
	cmd.Flags().BoolVarP(&commitYes, "yes", "y", false, "Skip confirmation prompt and commit automatically")
	return cmd
}

func runCommit(cmd *cobra.Command, args []string) error {
	log := logging.FromContext(commandContext(cmd))
	appCtx, err := loadAppContext(cmd)
	if err != nil {
		log.Error("loading config: failed", "err", err)
		return exitWith(1)
	}

	cfg := appCtx.Config

	// Get git user info
	user, email, err := cfg.GetGitUser()
	if err != nil {
		log.Error("reading the git user: failed", "err", err)
		return exitWith(1)
	}
	// Store in config for commit function
	cfg.Git.User = user
	cfg.Git.Email = email

	// If custom message provided, use it directly
	if commitMessage != "" {
		if err := commitOverlay(cmd, cfg, commitMessage); err != nil {
			log.Error("committing: failed", "err", err)
			return exitWith(1)
		}
		uiInfo("Changes committed successfully.")
		return nil
	}

	overlayPath := appCtx.OverlayPath

	runner := git.NewGitRunner(overlayPath)
	// The process-wide context (func commandContext): overlay commit is
	// cancellable, so the first SIGINT, SIGTERM or SIGHUP cancels this git call.
	entries, err := runner.Status(commandContext(cmd))
	if err != nil {
		log.Error("getting status: failed", "err", err)
		return exitWith(1)
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
		uiWarn("No staged changes to commit.")
		return nil
	}

	// Analyze changes and generate message (includes both ebuild packages and
	// non-ebuild files such as eclasses, profiles, licenses, and metadata).
	changes := overlay.AnalyzeChanges(stagedEntries)
	fileChanges := overlay.AnalyzeRepoFileChanges(stagedEntries)
	generatedMessage := overlay.GenerateCommitMessage(changes, fileChanges)

	// Dry-run mode: just show what would be committed
	if commitDryRun {
		uiInfo("Dry-run mode - would commit with message:")
		fmt.Printf("  %s\n\n", output.Sprint(output.Info, generatedMessage))
		uiInfo("Staged files:")
		for _, e := range stagedEntries {
			fmt.Printf("  %s %s\n", output.FormatStatus(overlay.StatusLabel(e.Status)), e.FilePath)
		}
		return nil
	}

	// Show preview and prompt
	uiInfo("Generated commit message:")
	fmt.Printf("  %s\n\n", output.Sprint(output.Info, generatedMessage))

	// Skip confirmation if -y flag is set
	if commitYes {
		if err := commitOverlay(cmd, cfg, generatedMessage); err != nil {
			log.Error("committing: failed", "err", err)
			return exitWith(1)
		}
		uiInfo("Changes committed successfully.")
		return nil
	}

	fmt.Print("Proceed? [y]es / [e]dit / [c]ancel: ")

	// One Ctrl+C at the prompt ends the command (func yieldSignalsToPrompt);
	// the git calls after it are cancellable again.
	reader := bufio.NewReader(os.Stdin)
	restoreSignals := yieldSignalsToPrompt(cmd)
	input, err := reader.ReadString('\n')
	restoreSignals()
	if err != nil {
		log.Error("reading input: failed", "err", err)
		return exitWith(1)
	}

	input = strings.TrimSpace(strings.ToLower(input))

	switch input {
	case "y", "yes", "":
		// Proceed with generated message
		if err := commitOverlay(cmd, cfg, generatedMessage); err != nil {
			log.Error("committing: failed", "err", err)
			return exitWith(1)
		}
		uiInfo("Changes committed successfully.")

	case "e", "edit":
		// Allow user to enter custom message
		fmt.Print("Enter commit message: ")
		restoreSignals = yieldSignalsToPrompt(cmd)
		customMessage, err := reader.ReadString('\n')
		restoreSignals()
		if err != nil {
			log.Error("reading input: failed", "err", err)
			return exitWith(1)
		}
		customMessage = strings.TrimSpace(customMessage)
		if customMessage == "" {
			uiWarn("Commit cancelled (empty message).")
			return exitWith(1)
		}
		if err := commitOverlay(cmd, cfg, customMessage); err != nil {
			log.Error("committing: failed", "err", err)
			return exitWith(1)
		}
		uiInfo("Changes committed successfully.")

	case "c", "cancel":
		uiInfo("Commit cancelled.")
		return exitWith(1)

	default:
		log.Error("Invalid option. Commit cancelled.")
		return exitWith(1)
	}
	return nil
}

// commitOverlay runs overlay.Commit under the process-wide context, which the
// first SIGINT, SIGTERM or SIGHUP cancels while overlay commit runs
// (S054-R5.8). The first signal also restores the default action, so a
// confirmation prompt that is waiting on stdin still ends on the second one.
func commitOverlay(cmd *cobra.Command, cfg *config.Config, message string) error {
	return overlay.Commit(commandContext(cmd), cfg, message)
}
