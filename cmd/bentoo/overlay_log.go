package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/obentoo/bentoolkit/internal/common/config"
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
	Run:   runLog,
}

func init() {
	logCmd.Flags().IntVarP(&logCount, "count", "n", 10, "Number of commits to show")
	logCmd.Flags().BoolVarP(&logOneline, "oneline", "o", false, "Show one line per commit")
	overlayCmd.AddCommand(logCmd)
}

func runLog(cmd *cobra.Command, args []string) {
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

	// Build git log command
	gitArgs := []string{"log", fmt.Sprintf("-%d", logCount), "--color=always"}
	if logOneline {
		gitArgs = append(gitArgs, "--oneline")
	} else {
		gitArgs = append(gitArgs, "--pretty=format:%C(yellow)%h%C(reset) %C(green)%ad%C(reset) %C(blue)%an%C(reset)%n  %s%n", "--date=short")
	}

	gitCmd := exec.Command("git", gitArgs...)
	gitCmd.Dir = overlayPath
	gitCmd.Stdout = os.Stdout
	gitCmd.Stderr = os.Stderr

	if err := gitCmd.Run(); err != nil {
		logger.Error("running git log: %v", err)
		os.Exit(1)
	}
}
