package main

import (
	"fmt"

	"github.com/obentoo/bentoolkit/internal/common/version"
	"github.com/spf13/cobra"
)

// newVersionCmd builds `version`.
func newVersionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Long:  `Print the version, commit hash, and build date of bentoo.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println(version.Info())
			return nil
		},
	}
	return cmd
}
