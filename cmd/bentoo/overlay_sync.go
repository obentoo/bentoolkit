package main

import (
	"github.com/obentoo/bentoolkit/internal/common/logger"
	"github.com/obentoo/bentoolkit/internal/overlay"
	"github.com/spf13/cobra"
)

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Sync overlay with upstream",
	Long:  `Fetch and merge changes from the upstream repository.`,
	Run:   runSync,
}

func init() {
	overlayCmd.AddCommand(syncCmd)
}

func runSync(cmd *cobra.Command, args []string) {
	ctx, err := loadAppContext()
	if err != nil {
		logger.Error("loading config: %v", err)
		osExit(1)
	}

	result, err := overlay.Sync(ctx.Config)
	if err != nil {
		logger.Error("%v", err)
		osExit(1)
	}

	if !result.Success {
		logger.Error("Sync failed: %s", result.Message)
		if len(result.Conflicts) > 0 {
			logger.Error("Conflicting files:")
			for _, conflict := range result.Conflicts {
				logger.Error("  - %s", conflict)
			}
			logger.Info("Resolve conflicts manually, then run 'git add' and 'git commit'")
			logger.Info("Or abort the merge with 'git merge --abort'")
		}
		osExit(1)
	}

	logger.Info("%s", result.Message)
}
