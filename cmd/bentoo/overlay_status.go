package main

import (
	"fmt"
	"os"

	"github.com/obentoo/bentoo-tools/internal/common/config"
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
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	statuses, err := overlay.Status(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Println(overlay.FormatStatus(statuses))
}
