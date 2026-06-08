package main

import (
	"fmt"

	"github.com/obentoo/bentoolkit/internal/common/logger"
	"github.com/obentoo/bentoolkit/internal/common/output"
	"github.com/obentoo/bentoolkit/internal/snapshot"
	"github.com/spf13/cobra"
)

var snapshotListCmd = &cobra.Command{
	Use:   "list",
	Short: "List local snapshots per subvolume",
	Run:   runSnapshotList,
}

func init() {
	snapshotCmd.AddCommand(snapshotListCmd)
}

func runSnapshotList(cmd *cobra.Command, _ []string) {
	cfg, path, err := loadSnapshotConfigLenient()
	if err != nil {
		logger.Error("snapshot list: %v", err)
		osExit(1)
		return
	}

	ctx, stop := signalContext(cmd.Context())
	defer stop()

	mgr, err := snapshot.NewManager(*cfg, path, snapshotRunner)
	if err != nil {
		logger.Error("snapshot list: %v", err)
		osExit(1)
		return
	}

	snaps, err := mgr.List(ctx)
	if err != nil {
		logger.Error("snapshot list: %v", err)
		osExit(1)
		return
	}

	for _, sv := range mgr.Subvolumes() {
		output.PrintInfo("%s:", sv)
		if len(snaps[sv]) == 0 {
			fmt.Println("  (none)")
			continue
		}
		for _, s := range snaps[sv] {
			fmt.Printf("  %s\n", s.Path)
		}
	}
}
