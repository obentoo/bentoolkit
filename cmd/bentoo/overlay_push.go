package main

import (
	"github.com/obentoo/bentoolkit/internal/common/logger"
	"github.com/obentoo/bentoolkit/internal/overlay"
	"github.com/spf13/cobra"
)

var (
	pushDryRun bool
)

var pushCmd = &cobra.Command{
	Use:   "push",
	Short: "Push committed changes to remote",
	Long:  `Push committed changes to the remote repository.`,
	Run:   runPush,
}

func init() {
	pushCmd.Flags().BoolVarP(&pushDryRun, "dry-run", "n", false, "Show what would be pushed without pushing")
	overlayCmd.AddCommand(pushCmd)
}

func runPush(cmd *cobra.Command, args []string) {
	ctx, err := loadAppContext()
	if err != nil {
		logger.Error("loading config: %v", err)
		osExit(1)
	}

	if pushDryRun {
		result, err := overlay.PushDryRun(ctx.Config)
		if err != nil {
			logger.Error("%v", err)
			osExit(1)
		}
		logger.Info("Dry-run mode - would push:")
		logger.Info("%s", result)
		return
	}

	result, err := overlay.Push(ctx.Config)
	if err != nil {
		logger.Error("%v", err)
		osExit(1)
	}

	logger.Info("%s", result.Message)
}
