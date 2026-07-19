package snapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// snapperDescription tags every snapshot created by bentoo so they are
// identifiable in `snapper list` output (R1.2).
const snapperDescription = "bentoo snapshot"

// snapperDateLayout is the timestamp format of the `date` field in
// `snapper --jsonout list` output (016 R3.1). snapper emits this layout there
// irrespective of the ambient locale, so no LC_ALL pinning is needed to parse
// it — unlike the human-readable table, which localizes its Date column.
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

// List runs `snapper --jsonout -c <config> list` and parses the JSON payload
// into snapshots (R1.3, 016 R3.1).
//
// JSON is requested rather than the human-readable table because snapper 0.13.1
// draws that table with U+2502 ("│") column separators instead of the ASCII "|"
// the previous parser split on, so every row was discarded and this method
// returned an empty list against a host full of snapshots. Structured output has
// no separator to guess at and no locale-dependent rendering, which makes the
// listing robust against both without depending on LC_ALL.
//
// A non-zero exit is wrapped with ErrEngineFailed so the Manager can record a
// failed stage (R6.1).
func (e *snapperEngine) List(ctx context.Context, subvolume string) ([]Snapshot, error) {
	out, err := e.run.Run(ctx, "snapper", []string{"--jsonout", "-c", snapperConfigName(subvolume), "list"}, nil)
	if err != nil {
		return nil, errors.Join(ErrEngineFailed, fmt.Errorf("snapper list %s: %w", subvolume, err))
	}
	return parseSnapperListJSON(out, subvolume), nil
}

// snapperListEntry mirrors the fields consumed from one element of
// `snapper --jsonout list` output (016 R3.2): the snapshot's number, type,
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
// list` output (016 R3.2). It supersedes the previous table scan, which
// returned nothing on snapper 0.13.1: that release separates the table's
// columns with U+2502 ("│") rather than the ASCII "|" the scan split on, so
// every row was discarded and `bentoo snapshot list` printed "(none)" while
// `snapper list` showed real snapshots. JSON carries no separator to guess at
// and no locale-dependent rendering.
//
// The payload is one object keyed by config name — {"root": [...]} — and
// `-c <config>` makes that exactly one key. Rather than assume which key, every
// key's entries are collected, walked in sorted key order so the result stays
// deterministic (Go randomizes map iteration) in the multi-key shape snapper is
// not observed to emit. Keying off snapperConfigName instead would turn any
// future change in snapper's key into another silently empty listing — the very
// failure being fixed here.
//
// The "current" pseudo-snapshot, number 0, is skipped (016 R3.3). An empty or
// snapshot-less payload yields an empty list and no error (016 R3.4).
//
// ID and Path derivation are unchanged from the table parser: the path follows
// snapper's fixed on-disk layout <subvolume>/.snapshots/<id>/snapshot. CreatedAt
// is a best-effort parse of the date field against snapperDateLayout — a blank
// date (number 0 carries one) or an unparseable one leaves the zero time rather
// than failing the whole listing.
//
// The signature returns no error, so a malformed payload could only surface as
// an empty list, which reads as "no snapshots" — indistinguishable from the bug
// this replaces. An unmarshal failure is therefore announced through warnLogf
// before returning empty, mirroring how archiveShipper.pruneRemote reports
// unparseable `rclone lsjson` output. A blank payload is not malformed: it is
// R3.4's empty case and stays quiet.
func parseSnapperListJSON(out []byte, subvolume string) []Snapshot {
	if len(bytes.TrimSpace(out)) == 0 {
		return nil // no output at all: an empty listing, not a parse failure (016 R3.4)
	}
	var configs map[string][]snapperListEntry
	if err := json.Unmarshal(out, &configs); err != nil {
		warnLogf("snapshot: parsing `snapper --jsonout list` output failed; reporting no snapshots: %v", err)
		return nil
	}

	var snaps []Snapshot
	for _, config := range slices.Sorted(maps.Keys(configs)) {
		for _, entry := range configs[config] {
			if entry.Number == 0 {
				continue // the "current" pseudo-snapshot, not a real one (016 R3.3)
			}
			id := strconv.Itoa(entry.Number)
			snap := Snapshot{
				ID:        id,
				Subvolume: subvolume,
				Path:      filepath.Join(subvolume, ".snapshots", id, "snapshot"),
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
