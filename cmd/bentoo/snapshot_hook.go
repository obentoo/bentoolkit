package main

import (
	"github.com/obentoo/bentoolkit/internal/common/logging"
	"github.com/obentoo/bentoolkit/internal/common/output"
	"github.com/obentoo/bentoolkit/internal/snapshot"
	"github.com/spf13/cobra"
)

var (
	// snapshotHookInstall is --install: write the Portage emerge hook.
	snapshotHookInstall bool
	// snapshotHookUninstall is --uninstall: remove the Portage emerge hook.
	snapshotHookUninstall bool
)

// newSnapshotHookCmd builds `snapshot hook`.
func newSnapshotHookCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hook",
		Short: "Install or remove the opt-in Portage emerge snapshot hook (snapper only)",
		Long: `Install or remove an OPT-IN Portage hook that snapshots before and after emerge.

--install writes /etc/portage/bashrc.d/50-bentoo-snapshot.sh and wires it into
/etc/portage/bashrc via a managed block. The hook uses snapper's native pre/post
pair per emerged package (pre_pkg_setup / post_pkg_postinst), so it requires
engine.driver = "snapper". snapper errors never break an emerge.

--uninstall cleanly removes the script and the managed block, preserving any
user content in /etc/portage/bashrc.

The hook is never installed implicitly: 'bentoo snapshot apply' does not touch
/etc/portage — only this explicit command does.`,
		RunE: runSnapshotHook,
	}
	cmd.Flags().BoolVar(&snapshotHookInstall, "install", false,
		"install the Portage emerge hook (pre/post snapper snapshots)")
	cmd.Flags().BoolVar(&snapshotHookUninstall, "uninstall", false,
		"remove the Portage emerge hook")
	return cmd
}

func runSnapshotHook(cmd *cobra.Command, _ []string) error {
	log := logging.FromContext(commandContext(cmd))
	// EXACTLY ONE of --install/--uninstall is required. Validated before
	// anything else — no config load, no filesystem write — so invalid usage
	// leaves the system untouched.
	if snapshotHookInstall == snapshotHookUninstall {
		log.Error("snapshot hook: exactly one of --install or --uninstall is required")
		return exitWith(1)
	}

	if snapshotHookUninstall {
		// Deliberately NO config load: uninstall must succeed even with a
		// broken or absent snapshot.toml (R4.2).
		if err := snapshot.UninstallEmergeHook(); err != nil {
			log.Error("snapshot hook: failed", "err", err)
			return exitWith(1)
		}
		output.PrintSuccess("Portage emerge hook removed")
		return nil
	}

	// --install: load AND validate the config so an unknown driver or a missing
	// snapper binary fails fast before anything is written (R5.1, G3).
	cfg, _, err := loadSnapshotConfig(log)
	if err != nil {
		log.Error("snapshot hook: failed", "err", err)
		return exitWith(1)
	}
	// The hook script shells out to snapper for its pre/post pairs, so any
	// other engine is refused — with nothing written (R4.1).
	if cfg.Engine.Driver != "snapper" {
		log.Error(`snapshot hook: the emerge hook shells out to snapper and requires engine.driver = "snapper"`,
			"engine", cfg.Engine.Driver)
		return exitWith(1)
	}

	if err := snapshot.InstallEmergeHook(); err != nil {
		log.Error("snapshot hook: failed", "err", err)
		return exitWith(1)
	}
	output.PrintSuccess("Portage emerge hook installed — snapper pre/post snapshots around each emerged package")
	return nil
}
