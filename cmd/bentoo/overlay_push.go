package main

import (
	"os"

	"github.com/obentoo/bentoo-tools/internal/common/config"
	"github.com/obentoo/bentoo-tools/internal/common/logger"
	"github.com/obentoo/bentoo-tools/internal/overlay"
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
	cfg, err := config.Load()
	if err != nil {
		logger.Error("loading config: %v", err)
		os.Exit(1)
	}

	if pushDryRun {
		result, err := overlay.PushDryRun(cfg)
		if err != nil {
			logger.Error("%v", err)
			os.Exit(1)
		}
		logger.Info("Dry-run mode - would push:")
		logger.Info("%s", result)
		return
	}

	result, err := overlay.Push(cfg)
	if err != nil {
		logger.Error("%v", err)
		os.Exit(1)
	}

	logger.Info("%s", result.Message)
}
