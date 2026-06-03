package main

import (
	"github.com/obentoo/bentoolkit/internal/common/logger"
	"github.com/obentoo/bentoolkit/internal/overlay"
	"github.com/spf13/cobra"
)

var addCmd = &cobra.Command{
	Use:   "add [paths...]",
	Short: "Add files to the staging area",
	Long: `Add files to the Git staging area in the overlay repository.
If no paths are specified, adds all changes (equivalent to "git add .").`,
	Run: runAdd,
}

func init() {
	overlayCmd.AddCommand(addCmd)
}

func runAdd(cmd *cobra.Command, args []string) {
	ctx, err := loadAppContext()
	if err != nil {
		logger.Error("loading config: %v", err)
		osExit(1)
	}

	result, err := overlay.AddFiles(ctx.Config, args...)
	if err != nil {
		logger.Error("%v", err)
		osExit(1)
	}

	// Display errors for individual files
	for _, e := range result.Errors {
		logger.Error("%v", e)
	}

	// Display success message if any files were added.
	// Show only what is staged in the index — not the whole working tree — so
	// "overlay add <pkg>" reports just the package(s) the user staged.
	if len(result.Added) > 0 {
		statuses, err := overlay.StagedStatus(ctx.Config)
		if err != nil {
			logger.Error("getting status: %v", err)
			osExit(1)
		}
		logger.Info("%s", overlay.FormatStatus(statuses))
	}

	// Exit with error if there were any failures
	if result.HasErrors() {
		osExit(1)
	}
}
