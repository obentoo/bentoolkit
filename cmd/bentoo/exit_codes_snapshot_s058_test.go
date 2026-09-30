package main

// Authored for story 058, sub-task 3.1 — R1.1, R1.3, R2.5, R3.2.
//
// The eight snapshot commands are RunE commands. Only rows that stop before
// touching the host are driven (snapper, btrbk and systemd are host state):
// the hook's flag check, an unreadable config for the two read-only verbs, and
// cobra's argument-count rejections. No row can reach a mutating snapshot
// operation.
//
// Red on arrival: every snapshot command is still a Run command.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var s058SnapshotCommands = []string{
	"snapshot apply", "snapshot hook", "snapshot list", "snapshot prune",
	"snapshot restore", "snapshot rollback", "snapshot run", "snapshot status",
}

func TestS058SnapshotCommandsReturnThroughRunE(t *testing.T) {
	if bad := s058NotOnRunE(t, s058SnapshotCommands...); len(bad) > 0 {
		t.Errorf("these commands still end the process themselves instead of returning their outcome (R1.1):\n  %s", strings.Join(bad, "\n  "))
	}
}

const s058HookFlagDiag = "snapshot hook: exactly one of --install or --uninstall is required"

// s058SnapshotHookRows: the diagnostic is printed by the command, so the
// returned status must add nothing (R3.2) — exactly one copy on either stream.
func s058SnapshotHookRows() []s058Row {
	return []s058Row{
		{name: "hook without --install or --uninstall", args: []string{"snapshot", "hook"}, want: 1, once: s058HookFlagDiag},
		{name: "hook with both --install and --uninstall", args: []string{"snapshot", "hook", "--install", "--uninstall"}, want: 1, once: s058HookFlagDiag},
		{name: "hook with a stray argument", args: []string{"snapshot", "hook", "bogus"}, want: 1, once: s058HookFlagDiag},
	}
}

func TestS058SnapshotHookKeepsItsExitCode(t *testing.T) {
	s058RunRows(t, s058SnapshotHookRows())
}

// s058BrokenSnapshotConfig writes a config the loader cannot parse and points
// --config at it, so the verb stops at config load, before any host access.
func s058BrokenSnapshotConfig(t *testing.T, c *testCLI) []string {
	t.Helper()
	path := filepath.Join(c.Home(), "snapshot-broken.toml")
	if err := os.WriteFile(path, []byte("this is = = not toml [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return []string{"--config", path}
}

func s058SnapshotRows() []s058Row {
	return []s058Row{
		{name: "list with an unreadable config", args: []string{"snapshot", "list"}, setup: s058BrokenSnapshotConfig, want: 1, once: "snapshot list:"},
		{name: "status with an unreadable config", args: []string{"snapshot", "status"}, setup: s058BrokenSnapshotConfig, want: 1, once: "snapshot status:"},
		{name: "rollback with no snapshot id", args: []string{"snapshot", "rollback"}, want: 1, usage: true},
		{name: "restore with two snapshot ids", args: []string{"snapshot", "restore", "1", "2"}, want: 1, usage: true},
	}
}

func TestS058SnapshotCommandsKeepTheirExitCodes(t *testing.T) {
	s058RunRows(t, s058SnapshotRows())
}
