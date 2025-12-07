package main

import (
	"fmt"
	"os"

	"github.com/obentoo/bentoo-tools/internal/common/logger"
	"github.com/obentoo/bentoo-tools/internal/common/output"
	"github.com/spf13/cobra"
)

var (
	verbose bool
	quiet   bool
	noColor bool
)

var rootCmd = &cobra.Command{
	Use:   "bentoo",
	Short: "Bentoo Linux tools",
	Long:  `A collection of tools for managing Bentoo Linux overlay and packages.`,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		// Configure logging based on flags
		if verbose {
			logger.SetVerbose(true)
		}
		if quiet {
			logger.SetQuiet(true)
		}
		if noColor {
			output.NoColor()
		}
	},
}

var overlayCmd = &cobra.Command{
	Use:   "overlay",
	Short: "Manage the Bentoo overlay repository",
	Long:  `Commands for managing the Bentoo overlay repository including adding files, checking status, committing changes, and pushing to remote.`,
}

func init() {
	// Global flags
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "Enable verbose output")
	rootCmd.PersistentFlags().BoolVarP(&quiet, "quiet", "q", false, "Suppress non-error output")
	rootCmd.PersistentFlags().BoolVar(&noColor, "no-color", false, "Disable colored output")

	rootCmd.AddCommand(overlayCmd)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
