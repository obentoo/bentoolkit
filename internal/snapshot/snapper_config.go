package snapshot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// snapperConfigsDir is where snapper expects its per-subvolume configs (R2.1).
// It is a package var (mirroring the lookPath/warnLogf seams) so tests can
// redirect it away from /etc.
var snapperConfigsDir = "/etc/snapper/configs"

// snapperConfigHeader opens snapper configs created from scratch by bentoo, so
// their origin is evident to anyone inspecting /etc/snapper/configs (R2.1).
const snapperConfigHeader = "# managed by bentoo snapshot"

// managedSnapperKeys returns the key/value pairs bentoo owns in a snapper
// config, in render order (R2.1):
//   - SUBVOLUME pins the config to its subvolume.
//   - TIMELINE_CREATE="no" — bentoo's own timer drives snapshot creation;
//     snapper's timeline timer would duplicate it.
//   - TIMELINE_CLEANUP="yes" plus the TIMELINE_LIMIT_* counts map
//     [engine.retention] onto snapper's native timeline cleanup (R1.4).
//     Retention has no yearly tier, so YEARLY is pinned to 0.
//   - NUMBER_CLEANUP="yes" governs the emerge hook's pre/post pairs (T3).
func managedSnapperKeys(cfg EngineConfig, subvolume string) [][2]string {
	r := cfg.Retention
	return [][2]string{
		{"SUBVOLUME", subvolume},
		{"TIMELINE_CREATE", "no"},
		{"TIMELINE_CLEANUP", "yes"},
		{"TIMELINE_LIMIT_HOURLY", strconv.Itoa(r.Hourly)},
		{"TIMELINE_LIMIT_DAILY", strconv.Itoa(r.Daily)},
		{"TIMELINE_LIMIT_WEEKLY", strconv.Itoa(r.Weekly)},
		{"TIMELINE_LIMIT_MONTHLY", strconv.Itoa(r.Monthly)},
		{"TIMELINE_LIMIT_YEARLY", "0"},
		{"NUMBER_CLEANUP", "yes"},
	}
}

// snapperConfigKey extracts the KEY of a shell-style `KEY="value"` line, or ""
// when the line is blank, a comment, or not an assignment (R2.2).
func snapperConfigKey(line string) string {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return ""
	}
	i := strings.Index(trimmed, "=")
	if i < 0 {
		return ""
	}
	return strings.TrimSpace(trimmed[:i])
}

// renderSnapperConfig renders the shell-style snapper config for subvolume
// (R2.1). With no existing content it emits the managed-by header followed by
// the managed keys. When merging over existing content it updates managed keys
// in place — preserving line order, comments, and every unmanaged line —
// appends managed keys that are absent, and never duplicates a key (a repeated
// managed key collapses into its first occurrence) (R2.2).
func renderSnapperConfig(cfg EngineConfig, subvolume string, existing []byte) []byte {
	managed := managedSnapperKeys(cfg, subvolume)
	values := make(map[string]string, len(managed))
	for _, kv := range managed {
		values[kv[0]] = kv[1]
	}

	var b strings.Builder
	written := make(map[string]bool, len(managed))
	writeKey := func(key, value string) {
		b.WriteString(key + `="` + value + `"` + "\n")
	}

	if len(existing) == 0 {
		b.WriteString(snapperConfigHeader + "\n")
	} else {
		for _, line := range strings.Split(strings.TrimSuffix(string(existing), "\n"), "\n") {
			key := snapperConfigKey(line)
			value, isManaged := values[key]
			if !isManaged {
				b.WriteString(line + "\n")
				continue
			}
			if written[key] {
				continue // never duplicate a managed key
			}
			written[key] = true
			writeKey(key, value)
		}
	}

	for _, kv := range managed {
		if !written[kv[0]] {
			writeKey(kv[0], kv[1])
		}
	}
	return []byte(b.String())
}

// ensureSnapperConfigs renders/ensures one snapper config per managed
// subvolume under snapperConfigsDir, named by snapperConfigName (R2.1). An
// existing file is merged, not clobbered — user settings beyond the managed
// keys survive — and each write is atomic (temp + rename, 0640). The operation
// is idempotent: re-running over an unchanged config produces identical bytes
// (R2.2). Once the per-subvolume configs exist, every one of their names is
// registered in SNAPPER_CONFIGS (R1.1): a config file snapper cannot enumerate
// is a config snapper rejects as "unknown config".
func ensureSnapperConfigs(cfg *Config) error {
	if err := os.MkdirAll(snapperConfigsDir, 0o755); err != nil { //nolint:gosec // matches snapper's own /etc/snapper/configs permissions
		return fmt.Errorf("create snapper configs dir %s: %w", snapperConfigsDir, err)
	}
	names := make([]string, 0, len(cfg.Engine.Subvolumes))
	for _, sv := range cfg.Engine.Subvolumes {
		name := snapperConfigName(sv)
		names = append(names, name)
		path := filepath.Join(snapperConfigsDir, name)
		existing, err := os.ReadFile(path)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("read snapper config %s: %w", path, err)
			}
			existing = nil
		}
		if err := atomicWrite(path, renderSnapperConfig(cfg.Engine, sv, existing), 0o640); err != nil {
			return err
		}
	}
	if err := ensureSnapperRegistered(names); err != nil {
		return fmt.Errorf("register snapper configs: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Story 016 C2 — register configs in SNAPPER_CONFIGS (R1).
// ---------------------------------------------------------------------------

// snapperConfigsVar is the shell variable in /etc/conf.d/snapper listing the
// configs snapper enumerates. It is the only place snapper looks: a config
// under snapperConfigsDir but absent from this list is invisible to
// `snapper create`/`list`, which fail with "unknown config" (R1.1).
const snapperConfigsVar = "SNAPPER_CONFIGS"

// snapperConfdPath is the shell config file holding snapperConfigsVar. Like
// snapperConfigsDir it is a package var rather than a const, so tests redirect
// it to a temp file instead of writing the developer's real /etc (R1.1).
var snapperConfdPath = "/etc/conf.d/snapper"

// ensureSnapperRegistered lists every name in the SNAPPER_CONFIGS assignment of
// snapperConfdPath, making the configs ensureSnapperConfigs just wrote visible
// to snapper (R1.1). A missing file is the one expected non-error: it merges as
// empty content, so the file is created carrying the managed names (R1.3). The
// merge adds only what is absent and copies every other variable, comment, and
// line through verbatim (R1.2, R1.4); when nothing is missing it returns its
// input byte for byte, so a second call rewrites identical content. The write
// is atomic (temp + rename) at 0644 — snapper's own mode for this file, which
// is world-readable shell config unlike the 0640 per-subvolume configs.
func ensureSnapperRegistered(names []string) error {
	existing, err := os.ReadFile(snapperConfdPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("read %s: %w", snapperConfdPath, err)
		}
		existing = nil
	}
	merged := mergeSnapperConfigsLine(string(existing), names)
	if err := atomicWrite(snapperConfdPath, []byte(merged), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", snapperConfdPath, err)
	}
	return nil
}

// mergeSnapperConfigsLine returns the content of /etc/conf.d/snapper with every
// name in names present in its SNAPPER_CONFIGS list (R1.1). The FIRST active
// (non-comment) SNAPPER_CONFIGS= assignment is authoritative: its
// space-separated values are unioned with names — existing values keep their
// original order and missing names are appended after them — and that one line
// is re-emitted in place in the operator's own quoting style. Every other line
// is copied verbatim, including comments, a commented-out #SNAPPER_CONFIGS=,
// and any further SNAPPER_CONFIGS= assignment (R1.4). With no active assignment
// the line is appended, which also covers content that is empty because the
// file does not exist yet (R1.3). When every name is already listed the input
// is returned byte for byte — quoting, spacing, and a missing final newline all
// untouched — so applying twice leaves identical on-disk state (R1.2, R5.1).
func mergeSnapperConfigsLine(existing string, names []string) string {
	lines := strings.Split(existing, "\n")
	idx := -1
	for i, line := range lines {
		if snapperConfigKey(line) == snapperConfigsVar {
			idx = i
			break
		}
	}

	var (
		lhs     = snapperConfigsVar
		values  []string
		quote   = byte('"')
		trailer string
	)
	if idx >= 0 {
		lhs, values, quote, trailer = parseSnapperConfigsLine(lines[idx])
	}

	merged := values
	seen := make(map[string]bool, len(values)+len(names))
	for _, v := range values {
		seen[v] = true
	}
	for _, n := range names {
		// A name holding whitespace cannot be represented in a space-separated
		// list: admitting it would corrupt the list and, since it would never
		// parse back, make every run append it again (R5.1). Drop it instead.
		if n == "" || strings.ContainsAny(n, " \t") || seen[n] {
			continue
		}
		seen[n] = true
		merged = append(merged, n)
	}
	if idx >= 0 && len(merged) == len(values) {
		return existing // nothing missing — leave the file byte-identical (R1.2, R5.1)
	}

	assignment := lhs + "=" + string(quote) + strings.Join(merged, " ") + string(quote) + trailer

	var b strings.Builder
	for i, line := range lines {
		if i > 0 {
			b.WriteString("\n")
		}
		if i == idx {
			b.WriteString(assignment)
			continue
		}
		b.WriteString(line)
	}
	if idx < 0 {
		// No active assignment to update: append one, first closing off a last
		// line that lacks its newline so the appended line stands alone (R1.3).
		if existing != "" && !strings.HasSuffix(existing, "\n") {
			b.WriteString("\n")
		}
		b.WriteString(assignment + "\n")
	}
	return b.String()
}

// parseSnapperConfigsLine splits a SNAPPER_CONFIGS= assignment into the text
// left of its "=" (keeping any indentation), the space-separated config names,
// the quote character a rewrite must re-emit with, and any same-line trailing
// comment. The operator's own quote character is preserved so an idempotent
// rewrite never restyles their file; an unquoted value re-emits as `"`, since
// the merged list needs quoting to survive the shell. The trailer is copied
// through verbatim rather than dropped (R1.4).
func parseSnapperConfigsLine(line string) (lhs string, names []string, quote byte, trailer string) {
	lhs, rhs, _ := strings.Cut(line, "=") // the "=" is why snapperConfigKey matched
	rhs = strings.TrimLeft(rhs, " \t")
	if rhs != "" && (rhs[0] == '"' || rhs[0] == '\'') {
		quote = rhs[0]
		if end := strings.IndexByte(rhs[1:], quote); end >= 0 {
			return lhs, strings.Fields(rhs[1 : 1+end]), quote, rhs[end+2:]
		}
		return lhs, strings.Fields(rhs[1:]), quote, "" // unterminated quote
	}
	if i := strings.IndexByte(rhs, '#'); i >= 0 {
		// Hand the run of whitespace before the "#" to the trailer, so the
		// rewritten line keeps the gap the operator put there.
		value := strings.TrimRight(rhs[:i], " \t")
		return lhs, strings.Fields(value), '"', rhs[len(value):]
	}
	return lhs, strings.Fields(rhs), '"', ""
}

// ---------------------------------------------------------------------------
// Story 016 C3 — provision the per-subvolume .snapshots subvolume (R2).
// ---------------------------------------------------------------------------

// snapshotsDirName is the directory, relative to a managed subvolume, in which
// snapper stores that config's snapshots (<subvolume>/.snapshots/<id>/snapshot,
// the layout parseSnapperList reconstructs). snapper requires it to be a btrfs
// subvolume of its own and will not create it for us (R2.1).
const snapshotsDirName = ".snapshots"

// snapshotsSubvolumePerm is the mode applied to a .snapshots subvolume bentoo
// creates, matching what snapper's own create-config sets. It is deliberately
// not world-readable: a snapshot exposes every file of the subvolume it came
// from, so 0755 here would hand out read access to the whole subtree (R2.1).
const snapshotsSubvolumePerm = 0o750

// statPath reports whether a path exists. Like snapperConfigsDir and
// snapperConfdPath it is a package var (defaulting to os.Stat) so tests decide
// what exists rather than inheriting the developer's filesystem: a real
// /home/.snapshots on the host would otherwise silently turn the "subvolume is
// missing" case into the "already provisioned" one and assert nothing (R2.2).
var statPath = os.Stat

// chmodPath applies snapshotsSubvolumePerm to a freshly created .snapshots
// subvolume. It is a seam for statPath's reason and one more: with statPath
// stubbed to "missing" and the Runner mocked, an unseamed os.Chmod would still
// run — against the real /home/.snapshots or /.snapshots. Unprivileged that is
// a harmless EPERM; under a root CI runner it silently repermissions a live
// system directory. Seaming it also makes the best-effort warn path observable,
// which is the only way to prove a chmod failure never masks a create error.
var chmodPath = os.Chmod

// ensureSnapshotSubvolumes creates <subvolume>/.snapshots for every managed
// subvolume that lacks one, through the Runner's `btrfs subvolume create`
// (R2.1, R2.3). snapper keeps a config's snapshots there and refuses to run
// without it, so any subvolume that was never snapshotted by hand — a fresh
// /home is the usual case — fails on its first `run` until this exists; `/`
// escapes only because the operator mounted @snapshots at /.snapshots
// themselves. The operation is idempotent: a path that already exists is left
// completely alone, never re-created and never re-permissioned, which is what
// lets that hand-mounted /.snapshots survive untouched (R2.2). A create failure
// aborts the whole pass and names the subvolume it belongs to, since the bare
// btrfs error says only which directory it could not make (R2.4).
//
// The follow-up chmod is best-effort by design: the subvolume is created and
// usable at whatever mode btrfs gave it, so a failure is warned and never
// returned. In particular it must neither mask a create error (it runs only
// after a create succeeded) nor manufacture one (it cannot make this function
// fail), because the caller aborts `apply` on the error this returns.
func ensureSnapshotSubvolumes(ctx context.Context, cfg *Config, run Runner) error {
	for _, sv := range cfg.Engine.Subvolumes {
		dir := filepath.Join(sv, snapshotsDirName)
		if _, err := statPath(dir); err == nil {
			continue // already provisioned — leave it exactly as it is (R2.2)
		}
		if _, err := run.Run(ctx, "btrfs", []string{"subvolume", "create", dir}, nil); err != nil {
			return fmt.Errorf("create snapshots subvolume for %s: %w", sv, err)
		}
		if err := chmodPath(dir, snapshotsSubvolumePerm); err != nil {
			warnLogf("snapshot: setting mode %#o on %s failed: %v; continuing at its created permissions",
				snapshotsSubvolumePerm, dir, err)
		}
	}
	return nil
}
