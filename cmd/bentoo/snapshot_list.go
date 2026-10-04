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

// newSnapshotListCmd builds `snapshot list`.
func newSnapshotListCmd(d *deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:         "list",
		Annotations: map[string]string{cancellableAnnotation: "true"},
		Short:       "List local snapshots per subvolume",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSnapshotList(cmd, args, d)
		},
	}
	cmd.Flags().BoolVar(&snapshotListRemote, "remote", false,
		"also list remote snapshots (btrbk targets, restic repository)")
	return cmd
}

func runSnapshotList(cmd *cobra.Command, _ []string, d *deps) error {
	cfg, path, err := loadSnapshotConfigLenient()
	if err != nil {
		logger.Error("snapshot list: %v", err)
		return exitWith(1)
	}

	ctx := commandContext(cmd)

	mgr, err := snapshot.NewManager(*cfg, path, d.snapshotRunner)
	if err != nil {
		logger.Error("snapshot list: %v", err)
		return exitWith(1)
	}

	snaps, err := mgr.List(ctx)
	if err != nil {
		logger.Error("snapshot list: %v", err)
		return exitWith(1)
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
		return nil
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
	return nil
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
