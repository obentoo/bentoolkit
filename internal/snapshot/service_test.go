package snapshot

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBtrbkConfPath(t *testing.T) {
	if got := BtrbkConfPath("/etc/bentoo/snapshot.toml"); got != "/etc/bentoo/btrbk.conf" {
		t.Errorf("BtrbkConfPath = %q", got)
	}
	if got := BtrbkConfPath(""); got != DefaultBtrbkConfPath {
		t.Errorf("BtrbkConfPath(\"\") = %q, want default", got)
	}
}

func TestApply_RendersConfAndInstallsTimer(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "snapshot.toml")
	mock := &MockRunner{}

	// Redirect unit writes away from the real /etc/systemd/system.
	origUnitDir := systemdUnitDir
	t.Cleanup(func() { systemdUnitDir = origUnitDir })
	systemdUnitDir = filepath.Join(dir, "systemd")

	cfg := &Config{
		Engine:   EngineConfig{Driver: "btrbk", Subvolumes: []string{"/home"}, SnapshotDir: "/.snapshots"},
		Ship:     []ShipConfig{{Type: "ssh", Target: "u@h:/p"}},
		Schedule: ScheduleConfig{Backend: "systemd", OnCalendar: "daily"},
	}

	if err := Apply(context.Background(), cfg, configPath, mock); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// btrbk.conf rendered next to snapshot.toml, with the ssh target block.
	confData, err := os.ReadFile(filepath.Join(dir, "btrbk.conf"))
	if err != nil {
		t.Fatalf("btrbk.conf not written: %v", err)
	}
	if !strings.Contains(string(confData), "target ssh://u@h:/p") {
		t.Errorf("btrbk.conf missing ssh target:\n%s", confData)
	}

	// systemctl daemon-reload + enable were invoked (units installed under the
	// default unit dir — Apply uses the scheduler's defaults; we only assert the
	// systemctl seam fired in order).
	if len(mock.Calls) < 2 || mock.Calls[0].Args[0] != "daemon-reload" {
		t.Errorf("expected daemon-reload first, got %+v", mock.Calls)
	}
}

func TestApply_NoScheduleSkipsSystemctl(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "snapshot.toml")
	mock := &MockRunner{}
	cfg := &Config{Engine: EngineConfig{Driver: "btrbk", Subvolumes: []string{"/home"}}}

	if err := Apply(context.Background(), cfg, configPath, mock); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(mock.Calls) != 0 {
		t.Errorf("no schedule should mean no systemctl calls, got %+v", mock.Calls)
	}
	if _, err := os.Stat(filepath.Join(dir, "btrbk.conf")); err != nil {
		t.Errorf("btrbk.conf still expected: %v", err)
	}
}

func TestManagerList_PerSubvolume(t *testing.T) {
	sample := "/.snapshots/home.20260608T120000\n/.snapshots/home.20260607T120000\n"
	mock := &MockRunner{
		RunFunc: func(_ context.Context, _ string, _ []string, _ []byte) ([]byte, error) {
			return []byte(sample), nil
		},
	}
	cfg := Config{Engine: EngineConfig{Driver: "btrbk", Subvolumes: []string{"/home"}}}
	m, err := NewManager(cfg, "/etc/bentoo/snapshot.toml", mock)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	got, err := m.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got["/home"]) != 2 {
		t.Errorf("List[/home] = %+v, want 2 snapshots", got["/home"])
	}
}

func TestTimerState(t *testing.T) {
	enabled := &MockRunner{
		RunFunc: func(_ context.Context, _ string, _ []string, _ []byte) ([]byte, error) {
			return []byte("enabled\n"), nil
		},
	}
	if got := TimerState(context.Background(), enabled); got != "enabled" {
		t.Errorf("TimerState = %q, want enabled", got)
	}

	empty := &MockRunner{}
	if got := TimerState(context.Background(), empty); got != "unknown" {
		t.Errorf("TimerState empty = %q, want unknown", got)
	}
}
