package snapshot

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func boolPtr(b bool) *bool { return &b }

func TestRenderServiceUnit_Golden(t *testing.T) {
	got := renderServiceUnit("bentoo", "/etc/bentoo/snapshot.toml")
	assertGolden(t, "service.golden", got)
}

func TestRenderTimerUnit_PersistentSet_Golden(t *testing.T) {
	cfg := ScheduleConfig{OnCalendar: "daily", Persistent: boolPtr(true), RandomizedDelay: "5m"}
	assertGolden(t, "timer_persistent.golden", renderTimerUnit(cfg))
}

func TestRenderTimerUnit_PersistentNil_Golden(t *testing.T) {
	// Persistent unset → the line is omitted; no RandomizedDelaySec either.
	cfg := ScheduleConfig{OnCalendar: "hourly"}
	assertGolden(t, "timer_minimal.golden", renderTimerUnit(cfg))
}

func TestSystemdApply_WritesUnitsAndOrdersSystemctl(t *testing.T) {
	dir := t.TempDir()
	mock := &MockRunner{}
	s := newSystemdScheduler("/etc/bentoo/snapshot.toml", mock)
	s.unitDir = dir

	cfg := ScheduleConfig{OnCalendar: "daily", Persistent: boolPtr(true)}
	if err := s.Apply(context.Background(), cfg); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Units written.
	if _, err := os.Stat(filepath.Join(dir, serviceUnitName)); err != nil {
		t.Errorf("service unit not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, timerUnitName)); err != nil {
		t.Errorf("timer unit not written: %v", err)
	}

	// systemctl order: daemon-reload BEFORE enable --now.
	if len(mock.Calls) != 2 {
		t.Fatalf("systemctl calls = %d, want 2", len(mock.Calls))
	}
	if mock.Calls[0].Args[0] != "daemon-reload" {
		t.Errorf("first systemctl = %v, want daemon-reload", mock.Calls[0].Args)
	}
	if mock.Calls[1].Args[0] != "enable" || mock.Calls[1].Args[len(mock.Calls[1].Args)-1] != timerUnitName {
		t.Errorf("second systemctl = %v, want enable --now %s", mock.Calls[1].Args, timerUnitName)
	}
}

func TestSystemdApply_Idempotent(t *testing.T) {
	dir := t.TempDir()
	s := newSystemdScheduler("/etc/bentoo/snapshot.toml", &MockRunner{})
	s.unitDir = dir
	cfg := ScheduleConfig{OnCalendar: "daily"}

	for i := 0; i < 2; i++ {
		if err := s.Apply(context.Background(), cfg); err != nil {
			t.Fatalf("Apply #%d: %v", i, err)
		}
	}

	// Re-apply overwrites in place — exactly one service unit, no .tmp leftovers.
	entries, _ := os.ReadDir(dir)
	count := 0
	for _, e := range entries {
		if e.Name() == serviceUnitName || e.Name() == timerUnitName {
			count++
		} else {
			t.Errorf("unexpected leftover file: %s", e.Name())
		}
	}
	if count != 2 {
		t.Errorf("unit file count = %d, want 2 (no duplicates)", count)
	}
}

func TestSystemdRemove_DisablesAndUnlinks(t *testing.T) {
	dir := t.TempDir()
	mock := &MockRunner{}
	s := newSystemdScheduler("/etc/bentoo/snapshot.toml", mock)
	s.unitDir = dir

	if err := s.Apply(context.Background(), ScheduleConfig{OnCalendar: "daily"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	mock.Calls = nil // reset to inspect Remove's calls only

	if err := s.Remove(context.Background()); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	if mock.Calls[0].Args[0] != "disable" {
		t.Errorf("first Remove systemctl = %v, want disable", mock.Calls[0].Args)
	}
	if _, err := os.Stat(filepath.Join(dir, timerUnitName)); !os.IsNotExist(err) {
		t.Errorf("timer unit not unlinked")
	}
	if _, err := os.Stat(filepath.Join(dir, serviceUnitName)); !os.IsNotExist(err) {
		t.Errorf("service unit not unlinked")
	}
}
