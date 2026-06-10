package snapshot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Stage names recorded in a RunResult, in pipeline order. StageGFS is the
// on-demand remote GFS sweep of `snapshot prune` (008 R3.1) — distinct from
// StagePrune (the engine-local prune) so a RunResult tells the two apart.
const (
	StageCreate = "create"
	StagePrune  = "prune"
	StageShip   = "ship"
	StageGFS    = "gfs"
)

// Stage outcome statuses.
const (
	StatusOK     = "ok"
	StatusFailed = "failed"
)

// StageResult records the outcome of a single pipeline stage for one subvolume.
// Err is a string (not error) so the result round-trips through JSON for `status`.
type StageResult struct {
	Subvolume string        `json:"subvolume"`
	Stage     string        `json:"stage"`
	Target    string        `json:"target,omitempty"` // ship target name, for StageShip
	Status    string        `json:"status"`
	StartedAt time.Time     `json:"started_at"`
	Duration  time.Duration `json:"duration"`
	Err       string        `json:"error,omitempty"`
}

// RunResult is the accumulated outcome of a Manager.Run, persisted so `status`
// can report the last run. A non-empty Err, or any failed stage, marks the run as
// failed; partial results (stages completed before a failure) are preserved.
type RunResult struct {
	StartedAt time.Time     `json:"started_at"`
	Duration  time.Duration `json:"duration"`
	Stages    []StageResult `json:"stages"`
	Err       string        `json:"error,omitempty"`
}

// AddStage appends a stage outcome to the result.
func (r *RunResult) AddStage(s StageResult) {
	r.Stages = append(r.Stages, s)
}

// Failed reports whether the run encountered any error — a top-level Err or any
// stage with StatusFailed.
func (r *RunResult) Failed() bool {
	if r.Err != "" {
		return true
	}
	for _, s := range r.Stages {
		if s.Status == StatusFailed {
			return true
		}
	}
	return false
}

// StateDir is the directory holding persisted snapshot state (A1). It is a var so
// tests can redirect it away from the system path.
var StateDir = func() string { return "/var/lib/bentoo/snapshot" }

// LastRunPath is the canonical path of the most recent RunResult.
func LastRunPath() string {
	return filepath.Join(StateDir(), "last-run.json")
}

// Save writes the result to path as indented JSON, atomically (temp + rename).
func (r *RunResult) Save(path string) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal run result: %w", err)
	}
	return atomicWrite(path, data, 0o644)
}

// SaveLastRun persists the result to LastRunPath.
func (r *RunResult) SaveLastRun() error {
	return r.Save(LastRunPath())
}

// LoadRunResult reads and decodes a RunResult from path.
func LoadRunResult(path string) (*RunResult, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r RunResult
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("decode run result: %w", err)
	}
	return &r, nil
}

// LoadLastRun reads the most recent persisted RunResult.
func LoadLastRun() (*RunResult, error) {
	return LoadRunResult(LastRunPath())
}

// atomicWrite writes data to path via a temp file in the same directory followed
// by a rename, so a reader never observes a partial file. The parent directory is
// created (0o750) if missing. Shared by RunResult persistence and systemd unit
// generation (NFR-Security: atomic temp + rename).
func atomicWrite(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create dir %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()

	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}

	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Chmod(perm); err != nil {
		cleanup()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("rename temp file into place: %w", err)
	}
	return nil
}
