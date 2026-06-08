package snapshot

import (
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "regenerate golden files")

// assertGolden compares got against testdata/<name>; with -update it rewrites the
// golden instead.
func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (run with -update to create): %v", path, err)
	}
	if got != string(want) {
		t.Errorf("golden mismatch for %s:\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func TestRenderBtrbkConf_Minimal(t *testing.T) {
	cfg := EngineConfig{Driver: "btrbk", Subvolumes: []string{"/home"}, SnapshotDir: "/.snapshots"}
	assertGolden(t, "btrbk_minimal.conf.golden", renderBtrbkConf(cfg, nil))
}

func TestRenderBtrbkConf_WithRetention(t *testing.T) {
	cfg := EngineConfig{
		Driver:      "btrbk",
		Subvolumes:  []string{"/home"},
		SnapshotDir: "/.snapshots",
		Retention:   Retention{Hourly: 24, Daily: 7, Weekly: 4, Monthly: 6, PreserveMin: "latest"},
	}
	assertGolden(t, "btrbk_retention.conf.golden", renderBtrbkConf(cfg, nil))
}

func TestRenderBtrbkConf_MultiSubvolumeWithTarget(t *testing.T) {
	cfg := EngineConfig{
		Driver:      "btrbk",
		Subvolumes:  []string{"/home", "/"},
		SnapshotDir: "/.snapshots",
		Retention:   Retention{Daily: 7},
	}
	assertGolden(t, "btrbk_multi_target.conf.golden", renderBtrbkConf(cfg, []string{"user@host:/backup/btrbk"}))
}

func TestBtrbkEngine_CreateInvokesRun(t *testing.T) {
	mock := &MockRunner{}
	e := newBtrbkEngine(EngineConfig{Driver: "btrbk"}, nil, mock)
	e.confPath = "/tmp/test-btrbk.conf"

	snap, err := e.Create(context.Background(), "/home")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if snap.Subvolume != "/home" {
		t.Errorf("snapshot subvolume = %q", snap.Subvolume)
	}
	if len(mock.Calls) != 1 {
		t.Fatalf("len(Calls) = %d, want 1", len(mock.Calls))
	}
	c := mock.Calls[0]
	wantArgs := []string{"-c", "/tmp/test-btrbk.conf", "run", "/home"}
	if c.Name != "btrbk" || !equalStrings(c.Args, wantArgs) {
		t.Errorf("call = %s %v, want btrbk %v", c.Name, c.Args, wantArgs)
	}
}

func TestBtrbkEngine_CreateWrapsNonZeroExit(t *testing.T) {
	mock := &MockRunner{
		RunFunc: func(_ context.Context, _ string, _ []string, _ []byte) ([]byte, error) {
			return nil, errors.New("ERROR: btrfs subvolume not found")
		},
	}
	e := newBtrbkEngine(EngineConfig{Driver: "btrbk"}, nil, mock)

	_, err := e.Create(context.Background(), "/home")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrEngineFailed) {
		t.Errorf("error %v is not ErrEngineFailed", err)
	}
}

func TestBtrbkEngine_PruneInvokesClean(t *testing.T) {
	mock := &MockRunner{}
	e := newBtrbkEngine(EngineConfig{Driver: "btrbk"}, nil, mock)
	e.confPath = "/c.conf"

	if _, err := e.Prune(context.Background(), "/home", Retention{Daily: 7}); err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(mock.Calls) != 1 || mock.Calls[0].Args[2] != "clean" {
		t.Errorf("Prune call = %+v, want clean", mock.Calls)
	}
}

func TestBtrbkEngine_ListParsesOutput(t *testing.T) {
	sample := `# btrbk snapshots
/.snapshots/home.20260608T120000
/.snapshots/home.20260607T120000
not-a-path header line
`
	mock := &MockRunner{
		RunFunc: func(_ context.Context, _ string, _ []string, _ []byte) ([]byte, error) {
			return []byte(sample), nil
		},
	}
	e := newBtrbkEngine(EngineConfig{Driver: "btrbk"}, nil, mock)

	snaps, err := e.List(context.Background(), "/home")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(snaps) != 2 {
		t.Fatalf("got %d snapshots, want 2: %+v", len(snaps), snaps)
	}
	if snaps[0].ID != "home.20260608T120000" || snaps[0].Subvolume != "/home" {
		t.Errorf("snaps[0] = %+v", snaps[0])
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
