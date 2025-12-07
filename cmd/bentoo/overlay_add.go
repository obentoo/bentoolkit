package main

import (
	"fmt"
	"os"

	"github.com/obentoo/bentoo-tools/internal/common/config"
	"github.com/obentoo/bentoo-tools/internal/overlay"
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
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	result, err := overlay.AddFiles(cfg, args...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// Display errors for individual files
	for _, e := range result.Errors {
		fmt.Fprintf(os.Stderr, "%v\n", e)
	}

	// Display success message if any files were added
	if len(result.Added) > 0 {
		// Get and display status after adding
		statuses, err := overlay.Status(cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting status: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(overlay.FormatStatus(statuses))
	}

	// Exit with error if there were any failures
	if result.HasErrors() {
		os.Exit(1)
	}
}
