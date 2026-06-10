package snapshot

import (
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
// (R2.2).
func ensureSnapperConfigs(cfg *Config) error {
	if err := os.MkdirAll(snapperConfigsDir, 0o755); err != nil {
		return fmt.Errorf("create snapper configs dir %s: %w", snapperConfigsDir, err)
	}
	for _, sv := range cfg.Engine.Subvolumes {
		path := filepath.Join(snapperConfigsDir, snapperConfigName(sv))
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
	return nil
}
