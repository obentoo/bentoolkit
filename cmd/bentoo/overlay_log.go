package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/obentoo/bentoolkit/internal/common/logger"
	"github.com/spf13/cobra"
)

var (
	logCount   int
	logOneline bool
)

var logCmd = &cobra.Command{
	Use:   "log",
	Short: "Show commit history",
	Long:  `Show the commit history of the overlay repository.`,
	Args:  cobra.NoArgs,
	Run:   runLog,
}

func init() {
	logCmd.Flags().IntVarP(&logCount, "count", "n", 10, "Number of commits to show")
	logCmd.Flags().BoolVarP(&logOneline, "oneline", "o", false, "Show one line per commit")
	overlayCmd.AddCommand(logCmd)
}

func runLog(cmd *cobra.Command, args []string) {
	ctx, err := loadAppContext()
	if err != nil {
		logger.Error("loading config: %v", err)
		osExit(1)
	}

	overlayPath := ctx.OverlayPath

	// Build git log command
	gitArgs := []string{"log", fmt.Sprintf("-%d", logCount), "--color=always"}
	if logOneline {
		gitArgs = append(gitArgs, "--oneline")
	} else {
		gitArgs = append(gitArgs, "--pretty=format:%C(yellow)%h%C(reset) %C(green)%ad%C(reset) %C(blue)%an%C(reset)%n  %s%n", "--date=short")
	}

	// G204: command name is the fixed literal "git"; gitArgs is built only
	// from internal literals ("log", a numeric -N, "--color=always",
	// "--oneline"/"--pretty=format:..."/"--date=short") — no user-supplied
	// input reaches the argument vector.
	gitCmd := exec.Command("git", gitArgs...) //nolint:gosec // G204: fixed "git" command + internal literal args only
	gitCmd.Dir = overlayPath
	gitCmd.Stdout = os.Stdout
	gitCmd.Stderr = os.Stderr

	if err := gitCmd.Run(); err != nil {
		logger.Error("running git log: %v", err)
		osExit(1)
	}
}
