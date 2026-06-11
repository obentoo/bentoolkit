package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

	// Last run, aggregate line plus the per-stage breakdown (008 R5.1): each
	// stage of the persisted RunResult with its subvolume, ship target when
	// set, outcome, and the error text when it failed.
	if last, err := snapshot.LoadLastRun(); err != nil {
		output.PrintInfo("last run: none recorded")
	} else {
		status := "ok"
		if last.Failed() {
			status = "failed"
		}
		output.PrintInfo("last run: %s at %s (%d stages)",
			status, last.StartedAt.Format(time.RFC3339), len(last.Stages))
		for _, st := range last.Stages {
			fmt.Printf("  %s\n", stageLine(st))
		}
	}

	// Timer state and next scheduled run (008 R5.1). TimerNextRun is
	// best-effort; an empty result maps to a clean "unknown".
	output.PrintInfo("timer: %s", snapshot.TimerState(ctx, snapshotRunner))
	next := snapshot.TimerNextRun(ctx, snapshotRunner)
	if next == "" {
		next = "unknown"
	}
	output.PrintInfo("next run: %s", next)

	// Free space on the snapshot filesystem (best-effort, 008 R5.1). The
	// snapshot dir may not exist yet (apply never run), so report the nearest
	// existing ancestor's filesystem — the one that would back it.
	dir := cfg.Engine.SnapshotDir
	if dir == "" {
		dir = "/"
	}
	if avail, ok := availableSpace(nearestExisting(dir)); ok {
		output.PrintInfo("free space on %s: %s", dir, humanBytes(avail))
	}

	// Free space of local ship target paths (008 R5.1). A target containing
	// ":" is remote (ssh user@host:/path, rclone remote:path, restic URL) and
	// is skipped silently, as is any path that cannot be statted.
	for _, sh := range cfg.Ship {
		target := sh.Target
		if target == "" || strings.Contains(target, ":") {
			continue
		}
		if avail, ok := availableSpace(target); ok {
			output.PrintInfo("free space on %s: %s", target, humanBytes(avail))
		}
	}
}

// stageLine renders one stage of the last RunResult for `status` (008 R5.1):
// stage name, subvolume, ship target when set, outcome, and the error when the
// stage failed — e.g. "create /home: ok",
// "ship /home → ssh: failed — connection refused".
func stageLine(s snapshot.StageResult) string {
	head := s.Stage
	if s.Subvolume != "" {
		head += " " + s.Subvolume
	}
	if s.Target != "" {
		head += " → " + s.Target
	}
	line := fmt.Sprintf("%s: %s", head, s.Status)
	if s.Err != "" {
		line += " — " + s.Err
	}
	return line
}

// nearestExisting walks up from path to the closest existing ancestor (ending
// at the filesystem root), so free space can be reported for the filesystem
// that would back a not-yet-created directory.
func nearestExisting(path string) string {
	for {
		if _, err := os.Stat(path); err == nil {
			return path
		}
		parent := filepath.Dir(path)
		if parent == path {
			return path
		}
		path = parent
	}
}

// availableSpace returns the bytes available to an unprivileged user on the
// filesystem backing path. The second result is false when statfs fails.
func availableSpace(path string) (uint64, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, false
	}
	if st.Bsize <= 0 {
		return 0, false
	}
	return st.Bavail * uint64(st.Bsize), true // #nosec G115 -- Bsize validated non-negative above
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
