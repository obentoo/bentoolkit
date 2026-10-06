package snapshot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"

	"github.com/obentoo/bentoolkit/internal/common/logging"
)

// DefaultBtrbkConfPath is where `apply` writes the rendered btrbk.conf and where
// the engine points btrbk via `-c`.
const DefaultBtrbkConfPath = "/etc/bentoo/btrbk.conf"

// btrbkEngine drives btrbk. Snapshot creation/pruning/listing shell out via the
// Runner seam; the destructive work stays inside btrbk (AD1). The engine does not
// write the conf itself — that is done at `apply`/`run` time (writeBtrbkConf) so
// Create/Prune/List remain pure subprocess calls and fully mockable.
type btrbkEngine struct {
	cfg      EngineConfig
	targets  []string // ssh remote targets contributed by shippers (AD5)
	run      Runner
	confPath string
	log      *slog.Logger // nil discards; set by newEngine
}

// logger returns the engine's logger, or a discarding one for a zero value.
func (e *btrbkEngine) logger() *slog.Logger { return logging.OrDiscard(e.log) }

// newBtrbkEngine builds the btrbk engine. A nil Runner falls back to the
// production execRunner.
func newBtrbkEngine(cfg EngineConfig, targets []string, run Runner) *btrbkEngine {
	if run == nil {
		run = defaultRunner()
	}
	return &btrbkEngine{cfg: cfg, targets: targets, run: run, confPath: DefaultBtrbkConfPath}
}

func (e *btrbkEngine) Name() string { return "btrbk" }

// Create runs `btrbk run <subvolume>` against the rendered conf (R2.2). The
// snapshot (and any configured send) is performed by btrbk; a non-zero exit is
// wrapped with ErrEngineFailed so the Manager can record a failed stage (§6).
//
// btrbk run prints nothing a caller can address the new snapshot by, so a
// second call, `btrbk -c <conf> --format=raw list latest <subvolume>`, resolves
// its Path and ID (053 R1.2). When that listing fails, is empty or names more
// than one snapshot, the snapshot is returned unidentified with one warning and
// a nil error (053 R1.3): btrbk run succeeded, and btrbk may already have
// shipped it over ssh. Only the ships that need a Path refuse it.
func (e *btrbkEngine) Create(ctx context.Context, subvolume string) (Snapshot, error) {
	args := []string{"-c", e.confPath, "run", subvolume}
	if _, err := e.run.Run(ctx, "btrbk", args, nil); err != nil {
		return Snapshot{}, errors.Join(ErrEngineFailed, fmt.Errorf("btrbk run %s: %w", subvolume, err))
	}
	path, reason := e.resolveLatest(ctx, subvolume)
	if reason != "" {
		e.logger().Warn("snapshot: btrbk snapshot of the subvolume is unidentified; its archive and restic ships cannot address it",
			"subvolume", subvolume, "reason", reason)
		return Snapshot{Subvolume: subvolume}, nil
	}
	return Snapshot{ID: filepath.Base(path), Subvolume: subvolume, Path: path}, nil
}

// resolveLatest asks btrbk for the latest snapshot of subvolume and returns
// its path, or an empty path and the reason it could not be resolved. It never
// picks among several candidates.
func (e *btrbkEngine) resolveLatest(ctx context.Context, subvolume string) (path, reason string) {
	out, err := e.run.Run(ctx, "btrbk", []string{"-c", e.confPath, "--format=raw", "list", "latest", subvolume}, nil)
	if err != nil {
		return "", fmt.Sprintf("btrbk list latest failed: %v", err)
	}
	paths, err := parseBtrbkLatestRaw(out)
	switch {
	case err != nil:
		return "", err.Error()
	case len(paths) == 0:
		return "", "btrbk list latest reported no snapshot"
	case len(paths) > 1:
		return "", fmt.Sprintf("btrbk list latest reported %d snapshots: %q", len(paths), paths)
	}
	return paths[0], ""
}

// errMalformed marks a `btrbk --format=raw` line that is not a sequence of
// key=value tokens.
var errMalformed = errors.New("malformed btrbk raw row")

// parseBtrbkLatestRaw extracts the snapshot paths from
// `btrbk --format=raw list latest` output (053 R1.2). Each row is
// `format="latest"` followed by key='value' pairs, every value written by
// btrbk's quoteshell, which closes the quote, writes an escaped \' and reopens
// it for every ' inside a value. Rows whose type contains "snapshot" and carry
// a non-empty snapshot_subvolume contribute that path; the distinct paths are
// returned in order of appearance, so one snapshot listed once per target
// collapses to one entry.
func parseBtrbkLatestRaw(out []byte) ([]string, error) {
	var paths []string
	for n, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		row, err := parseBtrbkRawRow(line)
		if err != nil {
			return nil, fmt.Errorf("parse btrbk list latest line %d: %w", n+1, err)
		}
		p := row["snapshot_subvolume"]
		if !strings.Contains(row["type"], "snapshot") || p == "" || slices.Contains(paths, p) {
			continue
		}
		paths = append(paths, p)
	}
	return paths, nil
}

// parseBtrbkRawRow splits one raw row into its key=value pairs. A value is a
// concatenation of '…' and "…" segments and \-escaped characters, read the way
// a POSIX shell reads a word, so text that merely looks like key='value' inside
// a quoted value stays part of that value.
func parseBtrbkRawRow(line string) (map[string]string, error) {
	row := map[string]string{}
	i := 0
	for {
		for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
			i++
		}
		if i == len(line) {
			return row, nil
		}
		eq := strings.IndexByte(line[i:], '=')
		if eq <= 0 || strings.ContainsAny(line[i:i+eq], " \t'\"") {
			return nil, errMalformed
		}
		key := line[i : i+eq]
		i += eq + 1
		var val strings.Builder
		for i < len(line) && line[i] != ' ' && line[i] != '\t' {
			switch c := line[i]; c {
			case '\'', '"':
				end := strings.IndexByte(line[i+1:], c)
				if end < 0 {
					return nil, errMalformed
				}
				val.WriteString(line[i+1 : i+1+end])
				i += end + 2
			case '\\':
				if i+1 == len(line) {
					return nil, errMalformed
				}
				val.WriteByte(line[i+1])
				i += 2
			default:
				val.WriteByte(c)
				i++
			}
		}
		row[key] = val.String()
	}
}

// Prune runs `btrbk clean <subvolume>` (R2.3). Retention is delegated to btrbk via
// the conf's snapshot_preserve/target_preserve directives (AD6), so the policy
// argument is accepted but not re-applied here.
func (e *btrbkEngine) Prune(ctx context.Context, subvolume string, _ Retention) ([]Snapshot, error) {
	args := []string{"-c", e.confPath, "clean", subvolume}
	if _, err := e.run.Run(ctx, "btrbk", args, nil); err != nil {
		return nil, errors.Join(ErrEngineFailed, fmt.Errorf("btrbk clean %s: %w", subvolume, err))
	}
	return nil, nil
}

// List runs `btrbk list <subvolume>` and parses the output into snapshots (R5.4).
func (e *btrbkEngine) List(ctx context.Context, subvolume string) ([]Snapshot, error) {
	out, err := e.run.Run(ctx, "btrbk", []string{"-c", e.confPath, "list", subvolume}, nil)
	if err != nil {
		return nil, errors.Join(ErrEngineFailed, fmt.Errorf("btrbk list %s: %w", subvolume, err))
	}
	return parseBtrbkList(out, subvolume), nil
}

// ListRemote lists the backups present on the btrbk targets via
// `btrbk -c <conf> list backups` (008 R5.2). Targets are the ssh ship entries
// folded into btrbk.conf (AD5), so target-side enumeration belongs to this
// engine; with no targets configured there is no remote and no subprocess runs.
// Rows are parsed with the same first-field-absolute-path logic as List. The
// Subvolume attribution is left empty: a target holds backups of every
// subvolume and btrbk's first column is the backup path, not the source.
func (e *btrbkEngine) ListRemote(ctx context.Context) ([]Snapshot, error) {
	if len(e.targets) == 0 {
		return nil, nil
	}
	out, err := e.run.Run(ctx, "btrbk", []string{"-c", e.confPath, "list", "backups"}, nil)
	if err != nil {
		return nil, errors.Join(ErrEngineFailed, fmt.Errorf("btrbk list backups: %w", err))
	}
	return parseBtrbkList(out, ""), nil
}

// Compile-time assertion: the btrbk engine contributes to `list --remote`.
var _ remoteLister = (*btrbkEngine)(nil)

// parseBtrbkList extracts snapshots from `btrbk list` output. Each data line's
// first whitespace field is the snapshot path; header/blank lines and any field
// that is not an absolute path are skipped. The snapshot ID is the path's base
// name. (Real-world btrbk output formats are exercised by the gated live test.)
func parseBtrbkList(out []byte, subvolume string) []Snapshot {
	var snaps []Snapshot
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		path := fields[0]
		if !strings.HasPrefix(path, "/") {
			continue
		}
		snaps = append(snaps, Snapshot{
			ID:        filepath.Base(path),
			Subvolume: subvolume,
			Path:      path,
		})
	}
	return snaps
}

// renderBtrbkConf renders a btrbk.conf from the engine config, retention policy,
// and any ssh targets (R2, R2.2, R2.3, AD5, AD6). Retention counts map to btrbk's
// snapshot_preserve/target_preserve grammar; zero counts are omitted.
func renderBtrbkConf(cfg EngineConfig, targets []string) string {
	var b strings.Builder
	b.WriteString("# Generated by bentoo snapshot — do not edit by hand.\n")

	if cfg.SnapshotDir != "" {
		fmt.Fprintf(&b, "snapshot_dir            %s\n", cfg.SnapshotDir)
	}

	preserveMin := cfg.Retention.PreserveMin
	if preserveMin == "" {
		preserveMin = "latest"
	}
	fmt.Fprintf(&b, "snapshot_preserve_min   %s\n", preserveMin)
	if line := retentionLine(cfg.Retention); line != "" {
		fmt.Fprintf(&b, "snapshot_preserve       %s\n", line)
	}

	if len(targets) > 0 {
		fmt.Fprintf(&b, "target_preserve_min     %s\n", preserveMin)
		if line := retentionLine(cfg.Retention); line != "" {
			fmt.Fprintf(&b, "target_preserve         %s\n", line)
		}
	}

	b.WriteString("\n")
	for _, sv := range cfg.Subvolumes {
		fmt.Fprintf(&b, "subvolume %s\n", sv)
	}

	if len(targets) > 0 {
		b.WriteString("\n")
		for _, t := range targets {
			fmt.Fprintf(&b, "target ssh://%s\n", t)
		}
	}

	return b.String()
}

// retentionLine renders the nonzero retention counts as a btrbk preserve line
// (e.g. "24h 7d 4w 6m"). Empty when no counts are set.
func retentionLine(r Retention) string {
	var parts []string
	if r.Hourly > 0 {
		parts = append(parts, fmt.Sprintf("%dh", r.Hourly))
	}
	if r.Daily > 0 {
		parts = append(parts, fmt.Sprintf("%dd", r.Daily))
	}
	if r.Weekly > 0 {
		parts = append(parts, fmt.Sprintf("%dw", r.Weekly))
	}
	if r.Monthly > 0 {
		parts = append(parts, fmt.Sprintf("%dm", r.Monthly))
	}
	return strings.Join(parts, " ")
}

// writeBtrbkConf renders and atomically writes the btrbk.conf to path (0644).
// Used by the `apply` and `run` verbs to materialize the native config.
func writeBtrbkConf(path string, cfg EngineConfig, targets []string) error {
	return atomicWrite(path, []byte(renderBtrbkConf(cfg, targets)), 0o644)
}
