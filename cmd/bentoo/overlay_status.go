package main

import (
	"github.com/obentoo/bentoolkit/internal/common/logger"
	"github.com/obentoo/bentoolkit/internal/overlay"
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
	ctx, err := loadAppContext()
	if err != nil {
		logger.Error("loading config: %v", err)
		osExit(1)
	}

	statuses, err := overlay.Status(ctx.Config)
	if err != nil {
		logger.Error("%v", err)
		osExit(1)
	}

	logger.Info("%s", overlay.FormatStatus(statuses))
}
