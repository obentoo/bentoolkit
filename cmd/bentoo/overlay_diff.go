package main

import (
	"os"
	"os/exec"

	"github.com/obentoo/bentoo-tools/internal/common/config"
	"github.com/obentoo/bentoo-tools/internal/common/logger"
	"github.com/spf13/cobra"
)

var (
	diffStaged bool
)

var diffCmd = &cobra.Command{
	Use:   "diff [path]",
	Short: "Show diff of changes",
	Long: `Show the diff of changes in the overlay repository.
By default shows unstaged changes. Use --staged to show staged changes.`,
	Run: runDiff,
}

func init() {
	diffCmd.Flags().BoolVarP(&diffStaged, "staged", "s", false, "Show staged changes")
	overlayCmd.AddCommand(diffCmd)
}

func runDiff(cmd *cobra.Command, args []string) {
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

	// Build git diff command
	gitArgs := []string{"diff", "--color=always"}
	if diffStaged {
		gitArgs = append(gitArgs, "--staged")
	}
	gitArgs = append(gitArgs, args...)

	gitCmd := exec.Command("git", gitArgs...)
	gitCmd.Dir = overlayPath
	gitCmd.Stdout = os.Stdout
	gitCmd.Stderr = os.Stderr

	if err := gitCmd.Run(); err != nil {
		// git diff returns exit code 1 if there are differences, which is not an error
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return
		}
		logger.Error("running git diff: %v", err)
		os.Exit(1)
	}
}
