package main

import (
	"errors"
	"os"
	"os/exec"

	"github.com/obentoo/bentoolkit/internal/common/logging"
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
		RunE: runDiff,
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

func runDiff(cmd *cobra.Command, args []string) error {
	log := logging.FromContext(commandContext(cmd))
	ctx, err := loadAppContext(cmd)
	if err != nil {
		log.Error("loading config: failed", "err", err)
		return exitWith(1)
	}

	overlayPath := ctx.OverlayPath

	// Build git diff command
	gitArgs := []string{"diff", "--color=always"}
	if diffStaged {
		gitArgs = append(gitArgs, "--staged")
	}
	if err := validateGitPathArgs(args); err != nil {
		log.Error("invalid path argument", "err", err)
		return exitWith(1)
	}
	gitArgs = append(gitArgs, args...)

	// The process-wide context, as every other subprocess in cmd/bentoo gets.
	// diff is not a cancellable command, so on Ctrl+C bentoo re-raises the
	// signal rather than cancelling: git (and its pager) stay in the terminal's
	// foreground group and receive the Ctrl+C themselves, exactly as before.
	gitCmd := exec.CommandContext(commandContext(cmd), "git", gitArgs...)
	gitCmd.Dir = overlayPath
	gitCmd.Stdout = os.Stdout
	gitCmd.Stderr = os.Stderr

	if err := gitCmd.Run(); err != nil {
		// git diff returns exit code 1 if there are differences, which is not an error
		if gitDiffFoundDifferences(err) {
			return nil
		}
		log.Error("running git diff: failed", "err", err)
		return exitWith(1)
	}
	return nil
}
