package snapshot

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// These tests close the gap that hid A7: shipper tests hand-built
// Snapshot{ID, Path}, so nothing drove a REAL engine's Create into a REAL
// shipper. Only the Runner is faked; engine, Manager and shippers are real.

func s053RedirectState(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	orig := StateDir
	StateDir = func() string { return dir }
	t.Cleanup(func() { StateDir = orig })
}

func s053CallsOf(mr *mockRunner, name, sub string) [][]string {
	var out [][]string
	for _, c := range mr.Calls {
		if c.Name == name && (sub == "" || (len(c.Args) > 0 && c.Args[0] == sub)) {
			out = append(out, c.Args)
		}
	}
	return out
}

func TestManagerRun_BtrbkArchiveShipsResolvedSnapshot(t *testing.T) {
	s053RedirectState(t)
	latest := map[string]string{
		"/home": "/mnt/pool/_btrbk_snap/home.20260923T0400",
		"/var":  "/mnt/pool/_btrbk_snap/var.20260923T0400",
	}
	mr := &mockRunner{RunFunc: func(_ context.Context, name string, args []string, _ []byte) ([]byte, error) {
		if name == "btrbk" && slices.Contains(args, "list") {
			sv := args[len(args)-1]
			return []byte("type='snapshot' source_url='/mnt/pool" + sv + "' snapshot_path='/mnt/pool/_btrbk_snap'" +
				" snapshot_subvolume='" + latest[sv] + "' status=''\n"), nil
		}
		return nil, nil
	}}
	cfg := Config{
		Engine: EngineConfig{Driver: "btrbk", Subvolumes: []string{"/home", "/var"}},
		Ship:   []ShipConfig{{Type: "archive", Remote: "r:bkt", Mode: "full"}},
	}
	m, err := NewManager(cfg, filepath.Join(t.TempDir(), "snapshot.toml"), mr)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	res, err := m.Run(t.Context())
	if err != nil {
		t.Fatalf("Run: %v (result err %q)", err, res.Err)
	}

	sends := s053CallsOf(mr, "btrfs", "send")
	rcats := s053CallsOf(mr, "rclone", "rcat")
	wantSends := [][]string{{"send", latest["/home"]}, {"send", latest["/var"]}}
	wantRcats := [][]string{
		{"rcat", "r:bkt/-home/home.20260923T0400.zst"},
		{"rcat", "r:bkt/-var/var.20260923T0400.zst"},
	}
	if !slices.EqualFunc(sends, wantSends, slices.Equal) {
		t.Errorf("btrfs send calls = %q, want %q (each subvolume ships its OWN resolved path)", sends, wantSends)
	}
	if !slices.EqualFunc(rcats, wantRcats, slices.Equal) {
		t.Errorf("rclone rcat calls = %q, want %q", rcats, wantRcats)
	}
	if got, want := "r:bkt/"+ArchiveObjectName("/home", "home.20260923T0400"), wantRcats[0][1]; got != want {
		t.Errorf("ArchiveObjectName key %q disagrees with the shipped key %q", got, want)
	}
}

func TestManagerRun_SnapperResticMountsDerivedPath(t *testing.T) {
	s053RedirectState(t)
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	mr := &mockRunner{RunFunc: func(_ context.Context, name string, args []string, _ []byte) ([]byte, error) {
		if name == "snapper" && slices.Contains(args, "create") {
			return []byte("42\n"), nil
		}
		return nil, nil
	}}
	cfg := Config{
		Engine: EngineConfig{Driver: "snapper", Subvolumes: []string{"/home"}},
		Ship:   []ShipConfig{{Type: "restic", Repo: "/srv/restic", PasswordFile: "/etc/bentoo/restic.pw"}},
	}
	m, err := NewManager(cfg, filepath.Join(t.TempDir(), "snapshot.toml"), mr)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	res, err := m.Run(t.Context())
	if err != nil {
		t.Fatalf("Run: %v (result err %q)", err, res.Err)
	}
	binds := s053CallsOf(mr, "mount", "--bind")
	if len(binds) != 1 || len(binds[0]) != 3 {
		t.Fatalf("mount --bind calls = %q, want exactly one [--bind <src> <dir>]", binds)
	}
	if binds[0][1] != "/home/.snapshots/42/snapshot" {
		t.Errorf("mount --bind source = %q, want /home/.snapshots/42/snapshot", binds[0][1])
	}
	dir := binds[0][2]
	if !strings.HasPrefix(dir, tmp) {
		t.Errorf("mountpoint %q is not a fresh temp dir under %q", dir, tmp)
	}
	backups := s053CallsOf(mr, "restic", "backup")
	if len(backups) != 1 || len(backups[0]) < 2 || backups[0][1] != dir {
		t.Errorf("restic backup calls = %q, want one backing up the mount path %q", backups, dir)
	}
}

func TestManagerRun_UnidentifiedSnapshotFailsOnlyAddressedShips(t *testing.T) {
	s053RedirectState(t)
	mr := &mockRunner{} // btrbk run succeeds; `list latest` prints nothing
	cfg := Config{
		Engine: EngineConfig{Driver: "btrbk", Subvolumes: []string{"/home"}},
		Ship: []ShipConfig{
			{Name: "offsite-ssh", Type: "ssh", Target: "u@h:/b"},
			{Name: "offsite-archive", Type: "archive", Remote: "r:bkt", Mode: "full"},
		},
	}
	m, err := NewManager(cfg, filepath.Join(t.TempDir(), "snapshot.toml"), mr)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	res, _ := m.Run(t.Context())
	status := map[string]string{}
	for _, s := range res.Stages {
		status[s.Stage+"/"+s.Target] = s.Status
	}
	if got := status[StageCreate+"/"]; got != StatusOK {
		t.Errorf("create stage = %q, want %q (the snapshot exists)", got, StatusOK)
	}
	if got := status[StageShip+"/offsite-ssh"]; got != StatusOK {
		t.Errorf("ssh ship stage = %q, want %q (btrbk already shipped it)", got, StatusOK)
	}
	if got := status[StageShip+"/offsite-archive"]; got != StatusFailed {
		t.Errorf("archive ship stage = %q, want %q (it cannot address the snapshot)", got, StatusFailed)
	}
	if n := len(s053CallsOf(mr, "btrfs", "")) + len(s053CallsOf(mr, "rclone", "rcat")); n != 0 {
		t.Errorf("archive ran %d btrfs/rclone calls for an unidentified snapshot, want 0", n)
	}
}
