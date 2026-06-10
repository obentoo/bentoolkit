package snapshot

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// snapperDescription tags every snapshot created by bentoo so they are
// identifiable in `snapper list` output (R1.2).
const snapperDescription = "bentoo snapshot"

// snapperDateLayout is the timestamp format of the Date column in
// `snapper list` output.
const snapperDateLayout = "2006-01-02 15:04:05"

// snapperEngine drives snapper, implementing the 004 Engine contract (R1.1).
// Snapshot creation/pruning/listing shell out via the Runner seam
// (exec.CommandContext underneath, R6.1); the destructive work stays inside
// snapper. The driver is additive beside btrbk (R6.2) and addresses snapper's
// per-subvolume configs by name derived from the subvolume path
// (snapperConfigName).
type snapperEngine struct {
	cfg EngineConfig
	run Runner
}

// newSnapperEngine builds the snapper engine. A nil Runner falls back to the
// production execRunner.
func newSnapperEngine(cfg EngineConfig, run Runner) *snapperEngine {
	if run == nil {
		run = defaultRunner()
	}
	return &snapperEngine{cfg: cfg, run: run}
}

func (e *snapperEngine) Name() string { return "snapper" }

// Create runs `snapper -c <config> create` with the bentoo description tag
// (R1.2), the timeline cleanup algorithm (so Prune's `cleanup timeline`
// governs these snapshots, R1.4), and --print-number so the trimmed stdout
// becomes the snapshot's ID. A non-zero exit is wrapped with ErrEngineFailed
// so the Manager can record a failed stage (R6.1).
func (e *snapperEngine) Create(ctx context.Context, subvolume string) (Snapshot, error) {
	args := []string{
		"-c", snapperConfigName(subvolume), "create",
		"--description", snapperDescription,
		"--cleanup-algorithm", "timeline",
		"--print-number",
	}
	out, err := e.run.Run(ctx, "snapper", args, nil)
	if err != nil {
		return Snapshot{}, errors.Join(ErrEngineFailed, fmt.Errorf("snapper create %s: %w", subvolume, err))
	}
	return Snapshot{
		ID:        strings.TrimSpace(string(out)),
		Subvolume: subvolume,
	}, nil
}

// Prune runs `snapper -c <config> cleanup timeline` (R1.4). Retention is
// delegated to snapper's native timeline cleanup (the TIMELINE_LIMIT_* keys of
// its config), so the policy argument is accepted but not re-applied here —
// mirroring btrbkEngine.Prune.
func (e *snapperEngine) Prune(ctx context.Context, subvolume string, _ Retention) ([]Snapshot, error) {
	args := []string{"-c", snapperConfigName(subvolume), "cleanup", "timeline"}
	if _, err := e.run.Run(ctx, "snapper", args, nil); err != nil {
		return nil, errors.Join(ErrEngineFailed, fmt.Errorf("snapper cleanup %s: %w", subvolume, err))
	}
	return nil, nil
}

// List runs `snapper -c <config> list` and parses the table into snapshots
// (R1.3).
func (e *snapperEngine) List(ctx context.Context, subvolume string) ([]Snapshot, error) {
	out, err := e.run.Run(ctx, "snapper", []string{"-c", snapperConfigName(subvolume), "list"}, nil)
	if err != nil {
		return nil, errors.Join(ErrEngineFailed, fmt.Errorf("snapper list %s: %w", subvolume, err))
	}
	return parseSnapperList(out, subvolume), nil
}

// parseSnapperList extracts snapshots from the pipe-separated `snapper list`
// table (R1.3). The header, `---+` separator lines, and the "current"
// pseudo-snapshot number 0 are skipped: only lines whose first field is a
// positive integer count. The snapshot path is derived from snapper's fixed
// on-disk layout <subvolume>/.snapshots/<id>/snapshot. CreatedAt is a
// best-effort parse of the Date column — it stays the zero time when the
// column is missing or unparseable.
func parseSnapperList(out []byte, subvolume string) []Snapshot {
	var snaps []Snapshot
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Split(line, "|")
		if len(fields) < 4 {
			continue // blank or `---+` separator line: too few columns
		}
		id := strings.TrimSpace(fields[0])
		if n, err := strconv.Atoi(id); err != nil || n == 0 {
			continue // header ("#") or the "current" pseudo-snapshot 0
		}
		snap := Snapshot{
			ID:        id,
			Subvolume: subvolume,
			Path:      filepath.Join(subvolume, ".snapshots", id, "snapshot"),
		}
		if t, err := time.Parse(snapperDateLayout, strings.TrimSpace(fields[3])); err == nil {
			snap.CreatedAt = t
		}
		snaps = append(snaps, snap)
	}
	return snaps
}

// snapperConfigName maps a subvolume path to its snapper config name: "/" is
// the canonical "root" config; other paths drop the surrounding slashes and
// flatten inner ones with "_" ("/home" → "home", "/var/log" → "var_log").
func snapperConfigName(subvolume string) string {
	name := strings.Trim(subvolume, "/")
	if name == "" {
		return "root"
	}
	return strings.ReplaceAll(name, "/", "_")
}
