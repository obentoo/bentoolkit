package main

import (
	"os"

	"github.com/obentoo/bentoo-tools/internal/common/config"
	"github.com/obentoo/bentoo-tools/internal/common/logger"
	"github.com/obentoo/bentoo-tools/internal/overlay"
	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the status of changes in the overlay",
	Long:  `Display the current status of changes in the overlay repository, grouped by category/package.`,
	Run:   runStatus,
}

func init() {
	overlayCmd.AddCommand(statusCmd)
}

func runStatus(cmd *cobra.Command, args []string) {
	cfg, err := config.Load()
	if err != nil {
		logger.Error("loading config: %v", err)
		os.Exit(1)
	}

	statuses, err := overlay.Status(cfg)
	if err != nil {
		logger.Error("%v", err)
		os.Exit(1)
	}

	logger.Info("%s", overlay.FormatStatus(statuses))
}
