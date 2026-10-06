package snapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/logging"
)

// snapperDescription tags every snapshot created by bentoo so they are
// identifiable in `snapper list` output.
const snapperDescription = "bentoo snapshot"

// snapperDateLayout is the timestamp format of the `date` field in
// `snapper --jsonout list` output. snapper emits this layout there
// irrespective of the ambient locale, so no LC_ALL pinning is needed to parse
// it — unlike the human-readable table, which localizes its Date column.
const snapperDateLayout = "2006-01-02 15:04:05"

// snapperEngine drives snapper, implementing the Engine contract.
// Snapshot creation/pruning/listing shell out via the Runner seam
// (exec.CommandContext underneath); the destructive work stays inside
// snapper. The driver is additive beside btrbk and addresses snapper's
// per-subvolume configs by name derived from the subvolume path
// (snapperConfigName).
type snapperEngine struct {
	cfg EngineConfig
	run Runner
	log *slog.Logger // nil discards; set by newEngine
}

// logger returns the engine's logger, or a discarding one for a zero value.
func (e *snapperEngine) logger() *slog.Logger { return logging.OrDiscard(e.log) }

// newSnapperEngine builds the snapper engine. A nil Runner falls back to the
// production execRunner.
func newSnapperEngine(cfg EngineConfig, run Runner) *snapperEngine {
	if run == nil {
		run = defaultRunner()
	}
	return &snapperEngine{cfg: cfg, run: run}
}

func (e *snapperEngine) Name() string { return "snapper" }

// Create runs `snapper -c <config> create` with the bentoo description tag,
// the timeline cleanup algorithm (so Prune's `cleanup timeline` governs these
// snapshots), and --print-number so the trimmed stdout becomes the snapshot's
// ID, and Path follows snapperSnapshotPath. Output that is not a positive
// number yields a snapshot with no ID or Path and one warning. A non-zero exit
// is wrapped with ErrEngineFailed so the Manager can record a failed stage.
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
	id := strings.TrimSpace(string(out))
	if !isSnapperNumber(id) {
		// The snapshot exists; only its identity is unknown. The ships that
		// need a Path refuse it with ErrSnapshotUnidentified.
		e.logger().Warn("snapshot: snapper create printed no snapshot number; its ships cannot address it",
			"subvolume", subvolume, "output", string(out))
		return Snapshot{Subvolume: subvolume}, nil
	}
	return Snapshot{
		ID:        id,
		Subvolume: subvolume,
		Path:      snapperSnapshotPath(subvolume, id),
	}, nil
}

// isSnapperNumber reports whether s is a positive decimal snapshot number, the
// only shape `snapper create --print-number` prints on success.
func isSnapperNumber(s string) bool {
	if s == "" || strings.TrimLeft(s, "0123456789") != "" {
		return false
	}
	n, err := strconv.Atoi(s)
	return err == nil && n > 0
}

// snapperSnapshotPath is snapper's fixed on-disk layout for snapshot id of
// subvolume: <subvolume>/.snapshots/<id>/snapshot. Create and
// parseSnapperListJSON both derive Path through it so the two cannot drift.
func snapperSnapshotPath(subvolume, id string) string {
	return filepath.Join(subvolume, ".snapshots", id, "snapshot")
}

// Prune runs `snapper -c <config> cleanup timeline`. Retention is
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

// List runs `snapper --jsonout -c <config> list` and parses the JSON payload
// into snapshots.
//
// JSON is requested rather than the human-readable table because snapper 0.13.1
// draws that table with U+2502 ("│") column separators instead of the ASCII "|"
// the previous parser split on, so every row was discarded and this method
// returned an empty list against a host full of snapshots. Structured output has
// no separator to guess at and no locale-dependent rendering, which makes the
// listing robust against both without depending on LC_ALL.
//
// A non-zero exit is wrapped with ErrEngineFailed so the Manager can record a
// failed stage.
func (e *snapperEngine) List(ctx context.Context, subvolume string) ([]Snapshot, error) {
	out, err := e.run.Run(ctx, "snapper", []string{"--jsonout", "-c", snapperConfigName(subvolume), "list"}, nil)
	if err != nil {
		return nil, errors.Join(ErrEngineFailed, fmt.Errorf("snapper list %s: %w", subvolume, err))
	}
	return parseSnapperListJSON(out, subvolume, e.logger()), nil
}

// snapperListEntry mirrors the fields consumed from one element of
// `snapper --jsonout list` output: the snapshot's number, type,
// creation timestamp, and description. snapper 0.13.1 emits eight further
// fields per entry (subvolume, default, active, pre-number, user, used-space,
// cleanup, userdata); encoding/json ignores what is not declared here, so a
// field snapper adds in a later release cannot break this parser.
type snapperListEntry struct {
	Number      int    `json:"number"`
	Type        string `json:"type"`
	Date        string `json:"date"`
	Description string `json:"description"`
}

// parseSnapperListJSON extracts snapshots from `snapper --jsonout -c <config>
// list` output. JSON replaced a table scan that returned nothing on snapper
// 0.13.1, whose table separates columns with U+2502 ("│") rather than "|";
// JSON has no separator to guess at and no locale-dependent rendering.
//
// The payload is one object keyed by config name — {"root": [...]}. Every key's
// entries are collected in sorted key order, so the result stays deterministic;
// keying off snapperConfigName instead would turn a future change in snapper's
// key into a silently empty listing.
//
// The "current" pseudo-snapshot, number 0, is skipped. An empty or
// snapshot-less payload yields an empty list and no error. Path follows
// snapper's layout <subvolume>/.snapshots/<id>/snapshot; CreatedAt is a
// best-effort parse against snapperDateLayout, a blank or unparseable date
// leaving the zero time rather than failing the listing.
//
// An empty list alone would read as "no snapshots", so an unmarshal failure is
// logged as a warning on log before returning empty. A blank payload is the
// empty case and stays quiet. A nil log discards.
func parseSnapperListJSON(out []byte, subvolume string, log *slog.Logger) []Snapshot {
	if len(bytes.TrimSpace(out)) == 0 {
		return nil // no output at all: an empty listing, not a parse failure
	}
	var configs map[string][]snapperListEntry
	if err := json.Unmarshal(out, &configs); err != nil {
		logging.OrDiscard(log).Warn("snapshot: parsing `snapper --jsonout list` output failed; reporting no snapshots",
			"subvolume", subvolume, "err", err)
		return nil
	}

	var snaps []Snapshot
	for _, config := range slices.Sorted(maps.Keys(configs)) {
		for _, entry := range configs[config] {
			if entry.Number == 0 {
				continue // the "current" pseudo-snapshot, not a real one
			}
			id := strconv.Itoa(entry.Number)
			snap := Snapshot{
				ID:        id,
				Subvolume: subvolume,
				Path:      snapperSnapshotPath(subvolume, id),
			}
			if t, err := time.Parse(snapperDateLayout, strings.TrimSpace(entry.Date)); err == nil {
				snap.CreatedAt = t
			}
			snaps = append(snaps, snap)
		}
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
