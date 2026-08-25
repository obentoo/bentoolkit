package main

import (
	"github.com/obentoo/bentoolkit/internal/common/logger"
	"github.com/obentoo/bentoolkit/internal/overlay"
	"github.com/spf13/cobra"
)

var (
	pullRebase bool
	pullMerge  bool
	pullDryRun bool
)

var pullCmd = &cobra.Command{
	Use: "pull",
	// "sync" is the name this command shipped under. It stays as an alias so
	// existing scripts and muscle memory keep working.
	Aliases: []string{"sync"},
	Short:   "Pull upstream changes into the overlay",
	Long: `Fetch the configured remote and integrate the current branch's upstream.

By default the integration is fast-forward only: if the overlay has diverged
from its upstream, the pull refuses rather than writing a merge commit. Use
--rebase to replay local commits on top of the upstream, or --merge to accept
a merge commit.`,
	Run: runPull,
}

func init() {
	pullCmd.Flags().BoolVar(&pullRebase, "rebase", false, "Replay local commits on top of the upstream")
	pullCmd.Flags().BoolVar(&pullMerge, "merge", false, "Merge the upstream, allowing a merge commit")
	pullCmd.Flags().BoolVarP(&pullDryRun, "dry-run", "n", false, "Show what would be pulled without integrating")
	pullCmd.MarkFlagsMutuallyExclusive("rebase", "merge")
	overlayCmd.AddCommand(pullCmd)
}

// pullModeFromFlags maps the flag pair to an integration mode. The flags are
// mutually exclusive at the cobra level, so at most one is set here.
func pullModeFromFlags(rebase, merge bool) overlay.PullMode {
	switch {
	case rebase:
		return overlay.PullRebase
	case merge:
		return overlay.PullMerge
	default:
		return overlay.PullFFOnly
	}
}

func runPull(cmd *cobra.Command, args []string) {
	ctx, err := loadAppContext()
	if err != nil {
		logger.Error("loading config: %v", err)
		osExit(1)
		return
	}

	mode := pullModeFromFlags(pullRebase, pullMerge)

	result, err := overlay.Pull(ctx.Config, mode, pullDryRun)
	if err != nil {
		logger.Error("%v", err)
		osExit(1)
		return
	}

	if !result.Success {
		logger.Error("Pull failed: %s", result.Message)
		if len(result.Conflicts) > 0 {
			logger.Error("Conflicting files:")
			for _, conflict := range result.Conflicts {
				logger.Error("  - %s", conflict)
			}
			if mode == overlay.PullRebase {
				logger.Info("Resolve conflicts manually, then run 'git add' and 'git rebase --continue'")
				logger.Info("Or abort the rebase with 'git rebase --abort'")
			} else {
				logger.Info("Resolve conflicts manually, then run 'git add' and 'git commit'")
				logger.Info("Or abort the merge with 'git merge --abort'")
			}
		}
		osExit(1)
		return
	}

	logger.Info("%s", result.Message)
}
