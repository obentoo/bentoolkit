package main

import (
	"context"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/snapshot"
)

// listTOMLRemote: btrbk engine with an ssh ship (btrbk target) and a restic
// ship — the two remote sources `list --remote` must merge in (008 R5.2).
const listTOMLRemote = `
[engine]
driver = "btrbk"
subvolumes = ["/home"]
snapshot_dir = "/.snapshots"

[[ship]]
type = "ssh"
target = "user@host:/backup"

[[ship]]
name = "cloud"
type = "restic"
repo = "rest:https://repo.example/bentoo"
password_file = "/etc/bentoo/restic.pass"
`

// remoteListMockRunner serves local btrbk lists, btrbk target backups, and
// restic snapshots JSON, so list --remote can merge all three sources.
func remoteListMockRunner() *snapshot.MockRunner {
	return &snapshot.MockRunner{
		RunFunc: func(_ context.Context, name string, args []string, _ []byte) ([]byte, error) {
			hasArg := func(want string) bool {
				for _, a := range args {
					if a == want {
						return true
					}
				}
				return false
			}
			switch {
			case name == "btrbk" && hasArg("backups"):
				return []byte("/backup/home.20260601T000000\n"), nil
			case name == "btrbk":
				return []byte("/.snapshots/home.20260608T120000\n"), nil
			case name == "restic" && hasArg("snapshots"):
				return []byte(`[{"short_id":"ab12cd34","time":"2026-06-01T00:00:00Z","paths":["/home"]}]`), nil
			}
			return nil, nil
		},
	}
}

// setListRemote toggles the --remote flag var and restores it after the test.
func setListRemote(t *testing.T, remote bool) {
	t.Helper()
	orig := snapshotListRemote
	snapshotListRemote = remote
	t.Cleanup(func() { snapshotListRemote = orig })
}

// TestRunSnapshotList_RemoteIncludesBtrbkTargetsAndRestic: 008 R5.2 — with
// --remote, list merges btrbk-target backups and restic snapshots alongside the
// local listing.
func TestRunSnapshotList_RemoteIncludesBtrbkTargetsAndRestic(t *testing.T) {
	writeSnapshotConfig(t, listTOMLRemote)
	mr := remoteListMockRunner()
	snapshotRunner = mr
	setListRemote(t, true)

	var code int
	var exited bool
	out := captureStdout(t, func() {
		code, exited = captureExit(t, func() { runSnapshotList(snapshotListCmd, nil) })
	})
	if exited {
		t.Fatalf("list --remote exited with code %d", code)
	}

	if !strings.Contains(out, "/.snapshots/home.20260608T120000") {
		t.Errorf("list --remote lost the local snapshots (008 R5.2); output:\n%s", out)
	}
	if !strings.Contains(out, "/backup/home.20260601T000000") {
		t.Errorf("list --remote missing the btrbk target backup (008 R5.2); output:\n%s", out)
	}
	if !strings.Contains(out, "ab12cd34") {
		t.Errorf("list --remote missing the restic snapshot (008 R5.2); output:\n%s", out)
	}
	if !strings.Contains(out, "remote") {
		t.Errorf("list --remote output does not label remote entries (008 R5.2); output:\n%s", out)
	}

	if snapshotListCmd.Flags().Lookup("remote") == nil {
		t.Error("--remote flag missing on list (008 R5.2)")
	}
}

// TestRunSnapshotList_WithoutRemoteSkipsRemoteQueries: without --remote the
// remote sources are not queried at all (008 R5.2 — opt-in).
func TestRunSnapshotList_WithoutRemoteSkipsRemoteQueries(t *testing.T) {
	writeSnapshotConfig(t, listTOMLRemote)
	mr := remoteListMockRunner()
	snapshotRunner = mr
	setListRemote(t, false)

	var code int
	var exited bool
	out := captureStdout(t, func() {
		code, exited = captureExit(t, func() { runSnapshotList(snapshotListCmd, nil) })
	})
	if exited {
		t.Fatalf("list exited with code %d", code)
	}

	for _, c := range mr.Calls {
		if c.Name == "restic" {
			t.Errorf("plain list queried restic (008 R5.2 — remote is opt-in): %+v", c)
		}
		if c.Name == "btrbk" {
			for _, a := range c.Args {
				if a == "backups" {
					t.Errorf("plain list queried btrbk target backups (008 R5.2): %+v", c)
				}
			}
		}
	}
	if strings.Contains(out, "/backup/home.20260601T000000") {
		t.Errorf("plain list rendered remote entries (008 R5.2); output:\n%s", out)
	}
}

func TestRunSnapshotList_RendersSnapshots(t *testing.T) {
	// list is lenient (no Validate), so no PATH stubs are needed.
	writeSnapshotConfig(t, validSnapshotTOML)
	sample := "/.snapshots/home.20260608T120000\n/.snapshots/home.20260607T120000\n"
	snapshotRunner = &snapshot.MockRunner{
		RunFunc: func(_ context.Context, _ string, _ []string, _ []byte) ([]byte, error) {
			return []byte(sample), nil
		},
	}

	var code int
	var exited bool
	out := captureStdout(t, func() {
		code, exited = captureExit(t, func() { runSnapshotList(snapshotListCmd, nil) })
	})
	if exited {
		t.Fatalf("list exited with code %d", code)
	}
	if !strings.Contains(out, "/.snapshots/home.20260608T120000") {
		t.Errorf("list output missing snapshot path:\n%s", out)
	}
}
