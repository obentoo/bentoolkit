package snapshot

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// snapper_config.go — provisioning snapper configs through snapper's own API
// (story 018).
//
// Everything here used to be done by writing /etc/snapper/configs/<name> and
// /etc/conf.d/snapper directly. Measured on a live host (snapper 0.13.1), that
// cannot work:
//
//   - snapperd caches its config list and offers no reload on D-Bus (only
//     ListConfigs and CreateConfig), so a name added to SNAPPER_CONFIGS by hand
//     is invisible to a daemon that is already running — `apply` registered it
//     and the very next `run` still failed with "unknown config";
//   - a config file edited behind the daemon's back is not observed at all:
//     with TIMELINE_LIMIT_HOURLY="7" on disk, snapper reported 10.
//
// So bentoo asks snapper to do both: `create-config` to provision (it writes
// the config with every template key, registers the name, and creates
// .snapshots as a 0750 subvolume) and `set-config` to apply the managed keys.
// The daemon performs each change itself and is therefore never stale.

// snapshotsDirName is the directory, relative to a managed subvolume, in which
// snapper stores that config's snapshots. `create-config` creates it and
// refuses to run when it already exists, which is why provisionSnapperConfig
// has to deal with a leftover one.
const snapshotsDirName = ".snapshots"

// statPath and readDirPath are the filesystem seams used to classify a leftover
// .snapshots. They are package vars (the lookPath/warnLogf seam pattern) so
// tests decide what exists rather than inheriting the developer's filesystem: a real
// /home/.snapshots on the host would otherwise turn the "leftover" case into
// the "nothing there" one and assert nothing.
var (
	statPath     = os.Stat
	readDirPath  = os.ReadDir
	removePath   = os.Remove
	readFilePath = os.ReadFile
)

// mountTables are the two files that answer "is this path a mount point?", and
// both are needed: the kernel's live table names what is mounted right now, and
// fstab names what the operator declared. A separately mounted @snapshots that
// happens to be UNMOUNTED appears in fstab only — and to ReadDir it looks like
// an ordinary empty directory, which is exactly the one that must not be
// removed. Deleting it destroys no snapshot (those live in the real subvolume)
// but breaks the mount point, so the next boot fails to mount it.
var mountTables = []string{"/proc/self/mounts", "/etc/fstab"}

// mountFieldUnescape decodes the octal escapes both mount tables use for
// characters that would otherwise split a field.
var mountFieldUnescape = strings.NewReplacer(
	`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)

// declaredMountpoint reports which table declares dir as a mount point, if any.
// An unreadable table is skipped rather than fatal: the check exists to refuse
// a destructive removal, and a missing /etc/fstab must not block provisioning a
// host that has none.
func declaredMountpoint(dir string) (string, bool) {
	for _, table := range mountTables {
		data, err := readFilePath(table)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			if filepath.Clean(mountFieldUnescape.Replace(fields[1])) == dir {
				return table, true
			}
		}
	}
	return "", false
}

// managedSnapperKeys returns the key/value pairs bentoo owns in a snapper
// config, in apply order:
//   - SUBVOLUME pins the config to its subvolume. It is listed for the plan and
//     for documentation; set-config cannot change it (snapper refuses) and
//     create-config has already set it correctly.
//   - TIMELINE_CREATE="no" — bentoo's own timer drives snapshot creation;
//     snapper's timeline timer would duplicate it.
//   - TIMELINE_CLEANUP="yes" plus the TIMELINE_LIMIT_* counts map
//     [engine.retention] onto snapper's native timeline cleanup.
//     Retention has no yearly tier, so YEARLY is pinned to 0.
//   - NUMBER_CLEANUP="yes" governs the emerge hook's pre/post pairs.
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

// settableSnapperKeys is managedSnapperKeys minus SUBVOLUME, which is what
// set-config may actually carry: snapper rejects a set-config that tries to
// change SUBVOLUME or FSTYPE, and sending it would fail the whole call.
func settableSnapperKeys(cfg EngineConfig, subvolume string) []string {
	managed := managedSnapperKeys(cfg, subvolume)
	out := make([]string, 0, len(managed))
	for _, kv := range managed {
		if kv[0] == "SUBVOLUME" {
			continue
		}
		out = append(out, kv[0]+"="+kv[1])
	}
	return out
}

// ensureSnapperConfigs makes sure every managed subvolume has a snapper config
// carrying bentoo's keys, provisioning what is missing (018 R1, R3).
//
// Existing coverage is read from `snapper list-configs` rather than from the
// presence of a file, because that is the question snapper itself answers:
// create-config fails with "subvolume already covered" when a config — under
// any name — already claims the subvolume (R2). The managed keys are then
// applied to every config, new or pre-existing: the old file-merge never
// reached a running daemon, so a config bentoo has managed for months may still
// be running the template's retention rather than the operator's.
func ensureSnapperConfigs(ctx context.Context, cfg *Config, run Runner) error {
	// A nil Runner means "use the production one" everywhere else in this
	// package, and the cmd layer relies on it: snapshotRunner is left nil
	// outside tests, so every real apply arrives here with nil.
	if run == nil {
		run = defaultRunner()
	}
	covered, err := snapperConfigsBySubvolume(ctx, run)
	if err != nil {
		return err
	}
	for _, sv := range cfg.Engine.Subvolumes {
		subvolume := filepath.Clean(sv)
		name, exists := covered[subvolume]
		switch {
		case !exists:
			name = snapperConfigName(subvolume)
			if err := provisionSnapperConfig(ctx, run, name, subvolume); err != nil {
				return err
			}
		case name != snapperConfigName(subvolume):
			// snapper covers this subvolume under a name bentoo would not have
			// chosen — an operator-made config. Its keys are still managed, but
			// the engine derives the config name it passes to create/list from
			// the subvolume, so those commands will not find it.
			warnLogf("snapshot: %s is covered by snapper config %q, not %q; "+
				"snapshot create/list will not find it until the config is renamed",
				subvolume, name, snapperConfigName(subvolume))
		}
		if err := applyManagedSnapperKeys(ctx, run, cfg.Engine, name, subvolume); err != nil {
			return err
		}
	}
	return nil
}

// snapperConfigsBySubvolume returns the config name snapper holds for each
// subvolume it already covers, keyed by cleaned path so the lookup is not
// defeated by a trailing slash in snapshot.toml.
func snapperConfigsBySubvolume(ctx context.Context, run Runner) (map[string]string, error) {
	out, err := run.Run(ctx, "snapper", []string{"--jsonout", "list-configs"}, nil)
	if err != nil {
		return nil, fmt.Errorf("list snapper configs: %w", err)
	}
	var payload struct {
		Configs []struct {
			Config    string `json:"config"`
			Subvolume string `json:"subvolume"`
		} `json:"configs"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return nil, fmt.Errorf("parse `snapper list-configs` output: %w", err)
	}
	bySubvolume := make(map[string]string, len(payload.Configs))
	for _, c := range payload.Configs {
		bySubvolume[filepath.Clean(c.Subvolume)] = c.Config
	}
	return bySubvolume, nil
}

// provisionSnapperConfig creates the snapper config for subvolume (018 R1).
// snapper writes the config from its template, registers the name in
// SNAPPER_CONFIGS, and creates <subvolume>/.snapshots as a 0750 subvolume — all
// of it visible to the running daemon, because the daemon is what did it.
//
// A leftover .snapshots has to go first: create-config refuses outright
// ("creating btrfs subvolume .snapshots failed since it already exists") and
// has no option to skip that step, so it is remove-it-or-fail (018 R4).
func provisionSnapperConfig(ctx context.Context, run Runner, name, subvolume string) error {
	dir := filepath.Join(subvolume, snapshotsDirName)
	if _, err := statPath(dir); err == nil {
		if err := clearEmptySnapshotsDir(ctx, run, dir, subvolume); err != nil {
			return err
		}
	}
	if _, err := run.Run(ctx, "snapper", []string{"-c", name, "create-config", subvolume}, nil); err != nil {
		return fmt.Errorf("create snapper config %s for %s: %w", name, subvolume, err)
	}
	return nil
}

// clearEmptySnapshotsDir removes a .snapshots that no config claims, so
// create-config can make its own (018 R4).
//
// It removes ONLY an empty one, and only when "empty" means what it looks like.
// Two cases are refused instead:
//
//   - a .snapshots holding entries holds snapshots, and deleting it would
//     destroy backups to fix a configuration problem — never a trade bentoo
//     makes on its own;
//   - a .snapshots that any mount table declares as a mount point, because an
//     unmounted @snapshots reads as empty and removing it silently breaks the
//     mount (018 R6).
//
// Removal goes through `btrfs subvolume delete` first, since that is what
// create-config makes it. A plain directory (or one on a filesystem where that
// fails) falls back to a directory unlink. A failure past both guards is
// reported with the operator's way out, because no amount of retrying fixes it.
func clearEmptySnapshotsDir(ctx context.Context, run Runner, dir, subvolume string) error {
	entries, err := readDirPath(dir)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", dir, err)
	}
	if len(entries) > 0 {
		return fmt.Errorf("%s holds %d entries but no snapper config covers %s: "+
			"refusing to delete snapshots; run `snapper -c %s create-config %s` by hand "+
			"after moving them aside",
			dir, len(entries), subvolume, snapperConfigName(subvolume), subvolume)
	}
	if table, declared := declaredMountpoint(dir); declared {
		return fmt.Errorf("%s is a mount point (named in %s) but no snapper config covers %s: "+
			"refusing to remove it — it reads as empty while unmounted, and removing it breaks "+
			"the mount; either unmount it and drop its /etc/fstab entry so snapper can create "+
			"its own %s, or leave the mount in place and manage this subvolume with "+
			"`snapper -c %s ...` by hand",
			dir, table, subvolume, snapshotsDirName, snapperConfigName(subvolume))
	}
	if _, err := run.Run(ctx, "btrfs", []string{"subvolume", "delete", dir}, nil); err == nil {
		return nil
	}
	if err := removePath(dir); err != nil {
		return fmt.Errorf("%s exists but no snapper config covers %s, and it could not be "+
			"removed so snapper can recreate it (%w); if it is a separately mounted "+
			"subvolume, unmount it or run `snapper -c %s create-config %s` by hand",
			dir, subvolume, err, snapperConfigName(subvolume), subvolume)
	}
	return nil
}

// applyManagedSnapperKeys pushes bentoo's keys into an existing config through
// `snapper set-config` (018 R3). It goes through snapper rather than the config
// file because a file write is not observed by a running daemon — the reason
// the retention in snapshot.toml has never actually reached snapper.
func applyManagedSnapperKeys(ctx context.Context, run Runner, cfg EngineConfig, name, subvolume string) error {
	args := append([]string{"-c", name, "set-config"}, settableSnapperKeys(cfg, subvolume)...)
	if _, err := run.Run(ctx, "snapper", args, nil); err != nil {
		return fmt.Errorf("apply managed keys to snapper config %s: %w", name, err)
	}
	return nil
}
