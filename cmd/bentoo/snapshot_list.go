package main

import (
	"fmt"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/logger"
	"github.com/obentoo/bentoolkit/internal/common/output"
	"github.com/obentoo/bentoolkit/internal/snapshot"
	"github.com/spf13/cobra"
)

// snapshotListRemote is --remote: additionally query and render remote
// snapshots — btrbk target backups and restic repository snapshots (008 R5.2).
// Remote sources are strictly opt-in: without the flag no remote query runs.
var snapshotListRemote bool

var snapshotListCmd = &cobra.Command{
	Use:   "list",
	Short: "List local snapshots per subvolume",
	Run:   runSnapshotList,
}

func init() {
	snapshotListCmd.Flags().BoolVar(&snapshotListRemote, "remote", false,
		"also list remote snapshots (btrbk targets, restic repository)")
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

	// Remote listing is opt-in (008 R5.2): without --remote, neither btrbk
	// `list backups` nor any restic query runs at all.
	if !snapshotListRemote {
		return
	}
	for _, g := range mgr.ListRemote(ctx) {
		if g.Err != nil {
			// Lenient: a failing remote source is reported but does not abort
			// the other sources (008 R5.2).
			output.PrintWarning("remote %s unavailable: %v", g.Label, g.Err)
			continue
		}
		output.PrintInfo("remote %s:", g.Label)
		if len(g.Snapshots) == 0 {
			fmt.Println("  (none)")
			continue
		}
		for _, s := range g.Snapshots {
			fmt.Printf("  %s\n", remoteSnapshotLine(s))
		}
	}
}

// remoteSnapshotLine renders one remote snapshot (008 R5.2): btrbk target
// backups carry an absolute path; restic snapshots carry the short id, the
// creation time, and the backed-up paths.
func remoteSnapshotLine(s snapshot.Snapshot) string {
	if s.Path != "" {
		return s.Path
	}
	line := s.ID
	if !s.CreatedAt.IsZero() {
		line += "  " + s.CreatedAt.Format(time.RFC3339)
	}
	if s.Subvolume != "" {
		line += "  " + s.Subvolume
	}
	return line
}
