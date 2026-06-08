package main

import (
	"github.com/obentoo/bentoolkit/internal/snapshot"
	"github.com/spf13/cobra"
)

var (
	// snapshotConfigPath is the --config persistent flag; empty means resolve via
	// the snapshot.toml search path.
	snapshotConfigPath string
	// snapshotRunner is the subprocess seam threaded into the snapshot package.
	// nil selects the production execRunner; tests inject a MockRunner.
	snapshotRunner snapshot.Runner
)

var snapshotCmd = &cobra.Command{
	Use:   "snapshot",
	Short: "Manage btrfs snapshots (btrbk + systemd)",
	Long: `Manage btrfs snapshots declaratively via a single snapshot.toml.

bentoolkit orchestrates mature tools — btrbk for snapshot/send-receive and systemd
for scheduling — rather than reimplementing them.

Examples:
  bentoo snapshot apply               Render native config + install the timer
  bentoo snapshot run                 Run the snapshot pipeline now
  bentoo snapshot list                List local snapshots per subvolume
  bentoo snapshot status              Show last run, timer state, and free space`,
}

func init() {
	snapshotCmd.PersistentFlags().StringVar(&snapshotConfigPath, "config", "",
		"path to snapshot.toml (default: /etc/bentoo, then XDG)")
}

// loadSnapshotConfig resolves, loads, and validates the snapshot config. Used by
// the side-effecting verbs (apply/run) so an unknown driver or missing dependency
// fails fast before anything is written (R1.3, R6.1, G3).
func loadSnapshotConfig() (*snapshot.Config, string, error) {
	cfg, path, err := loadSnapshotConfigLenient()
	if err != nil {
		return nil, path, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, path, err
	}
	return cfg, path, nil
}

// loadSnapshotConfigLenient resolves and loads the config without validation, for
// read-only inspection verbs (list/status) that should report even when a driver
// binary is absent (A3).
func loadSnapshotConfigLenient() (*snapshot.Config, string, error) {
	path := snapshotConfigPath
	if path == "" {
		p, err := snapshot.FindConfigPath()
		if err != nil {
			return nil, "", err
		}
		path = p
	}
	cfg, err := snapshot.LoadFrom(path)
	if err != nil {
		return nil, path, err
	}
	return cfg, path, nil
}
