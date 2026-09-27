package main

import (
	"errors"
	"os"
	"os/exec"

	"github.com/obentoo/bentoolkit/internal/common/logger"
	"github.com/spf13/cobra"
)

var (
	diffStaged bool
)

// newDiffCmd builds `overlay diff`.
func newDiffCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "diff [path]",
		Short: "Show diff of changes",
		Long: `Show the diff of changes in the overlay repository.
By default shows unstaged changes. Use --staged to show staged changes.`,
		Run: runDiff,
	}
	cmd.Flags().BoolVarP(&diffStaged, "staged", "s", false, "Show staged changes")
	return cmd
}

// gitDiffFoundDifferences reports whether err is git diff's exit status 1,
// which means "there are differences" rather than a failure. errors.As, not a
// type assertion, so a wrapped *exec.ExitError is still recognised.
func gitDiffFoundDifferences(err error) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr) && exitErr.ExitCode() == 1
}

func runDiff(cmd *cobra.Command, args []string) {
	ctx, err := loadAppContext()
	if err != nil {
		logger.Error("loading config: %v", err)
		osExit(1)
	}

	overlayPath := ctx.OverlayPath

	// Build git diff command
	gitArgs := []string{"diff", "--color=always"}
	if diffStaged {
		gitArgs = append(gitArgs, "--staged")
	}
	if err := validateGitPathArgs(args); err != nil {
		logger.Error("%v", err)
		osExit(1)
	}
	gitArgs = append(gitArgs, args...)

	gitCmd := exec.Command("git", gitArgs...)
	gitCmd.Dir = overlayPath
	gitCmd.Stdout = os.Stdout
	gitCmd.Stderr = os.Stderr

	if err := gitCmd.Run(); err != nil {
		// git diff returns exit code 1 if there are differences, which is not an error
		if gitDiffFoundDifferences(err) {
			return
		}
		logger.Error("running git diff: %v", err)
		osExit(1)
	}
}
