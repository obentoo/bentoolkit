package main

import (
	"fmt"
	"syscall"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/logger"
	"github.com/obentoo/bentoolkit/internal/common/output"
	"github.com/obentoo/bentoolkit/internal/snapshot"
	"github.com/spf13/cobra"
)

var snapshotStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the last run, timer state, and free space",
	Run:   runSnapshotStatus,
}

func init() {
	snapshotCmd.AddCommand(snapshotStatusCmd)
}

func runSnapshotStatus(cmd *cobra.Command, _ []string) {
	cfg, _, err := loadSnapshotConfigLenient()
	if err != nil {
		logger.Error("snapshot status: %v", err)
		osExit(1)
		return
	}

	ctx, stop := signalContext(cmd.Context())
	defer stop()

	// Last run.
	if last, err := snapshot.LoadLastRun(); err != nil {
		output.PrintInfo("last run: none recorded")
	} else {
		status := "ok"
		if last.Failed() {
			status = "failed"
		}
		output.PrintInfo("last run: %s at %s (%d stages)",
			status, last.StartedAt.Format(time.RFC3339), len(last.Stages))
	}

	// Timer state.
	output.PrintInfo("timer: %s", snapshot.TimerState(ctx, snapshotRunner))

	// Free space on the snapshot filesystem (best-effort).
	dir := cfg.Engine.SnapshotDir
	if dir == "" {
		dir = "/"
	}
	if avail, ok := availableSpace(dir); ok {
		output.PrintInfo("free space on %s: %s", dir, humanBytes(avail))
	}
}

// availableSpace returns the bytes available to an unprivileged user on the
// filesystem backing path. The second result is false when statfs fails.
func availableSpace(path string) (uint64, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, false
	}
	return st.Bavail * uint64(st.Bsize), true
}

// humanBytes renders a byte count in binary units (KiB/MiB/GiB/TiB).
func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGT"[exp])
}
