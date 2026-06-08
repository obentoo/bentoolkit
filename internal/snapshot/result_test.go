package snapshot

import (
	"path/filepath"
	"testing"
	"time"
)

func TestRunResult_RoundTrip(t *testing.T) {
	started := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	in := &RunResult{
		StartedAt: started,
		Duration:  3 * time.Second,
		Stages: []StageResult{
			{Subvolume: "/home", Stage: StageCreate, Status: StatusOK, StartedAt: started, Duration: time.Second},
			{Subvolume: "/home", Stage: StageShip, Target: "offsite", Status: StatusOK, StartedAt: started, Duration: 2 * time.Second},
		},
	}

	path := filepath.Join(t.TempDir(), "last-run.json")
	if err := in.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	out, err := LoadRunResult(path)
	if err != nil {
		t.Fatalf("LoadRunResult: %v", err)
	}
	if !out.StartedAt.Equal(started) {
		t.Errorf("StartedAt = %v, want %v", out.StartedAt, started)
	}
	if out.Duration != 3*time.Second {
		t.Errorf("Duration = %v", out.Duration)
	}
	if len(out.Stages) != 2 {
		t.Fatalf("len(Stages) = %d, want 2", len(out.Stages))
	}
	if out.Stages[1].Stage != StageShip || out.Stages[1].Target != "offsite" {
		t.Errorf("Stages[1] = %+v", out.Stages[1])
	}
	if out.Failed() {
		t.Errorf("Failed() = true, want false for all-ok result")
	}
}

func TestRunResult_PartialOnStageError(t *testing.T) {
	// A run that fails mid-pipeline must preserve the stages that completed plus
	// the failed one.
	in := &RunResult{
		StartedAt: time.Unix(0, 0).UTC(),
		Stages: []StageResult{
			{Subvolume: "/", Stage: StageCreate, Status: StatusOK},
			{Subvolume: "/", Stage: StageShip, Target: "offsite", Status: StatusFailed, Err: "ssh: connection refused"},
		},
		Err: "ship offsite failed",
	}

	path := filepath.Join(t.TempDir(), "last-run.json")
	if err := in.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out, err := LoadRunResult(path)
	if err != nil {
		t.Fatalf("LoadRunResult: %v", err)
	}

	if !out.Failed() {
		t.Errorf("Failed() = false, want true")
	}
	if len(out.Stages) != 2 || out.Stages[0].Status != StatusOK {
		t.Errorf("partial stages not preserved: %+v", out.Stages)
	}
	if out.Stages[1].Err == "" {
		t.Errorf("failed stage error not preserved")
	}
}

func TestAtomicWrite_CreatesDirAndPerm(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deep", "file.json")
	if err := atomicWrite(path, []byte("{}"), 0o644); err != nil {
		t.Fatalf("atomicWrite: %v", err)
	}
	// No leftover temp files in the target dir.
	matches, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "*.tmp-*"))
	if len(matches) != 0 {
		t.Errorf("leftover temp files: %v", matches)
	}
}

func TestStateDir_Overridable(t *testing.T) {
	orig := StateDir
	t.Cleanup(func() { StateDir = orig })
	tmp := t.TempDir()
	StateDir = func() string { return tmp }
	if LastRunPath() != filepath.Join(tmp, "last-run.json") {
		t.Errorf("LastRunPath did not honor overridden StateDir")
	}
}
