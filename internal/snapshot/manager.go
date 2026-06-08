package snapshot

import (
	"context"
	"fmt"
	"time"
)

// Manager wires the configured drivers and runs the snapshot pipeline (AD10).
type Manager struct {
	engine     Engine
	shippers   []Shipper
	notifier   Notifier
	subvolumes []string
	retention  Retention
}

// NewManager builds a Manager from cfg using the factories. configPath is the
// snapshot.toml path; it locates the sibling btrbk.conf the engine drives. run is
// the injectable subprocess seam (nil → production execRunner); ssh targets are
// folded into the engine's btrbk.conf so btrbk performs send/receive during
// Create (AD5).
func NewManager(cfg Config, configPath string, run Runner) (*Manager, error) {
	engine, err := newEngine(cfg.Engine, collectShipTargets(cfg.Ship), run)
	if err != nil {
		return nil, err
	}
	// Point the btrbk engine at the btrbk.conf written next to snapshot.toml.
	if be, ok := engine.(*btrbkEngine); ok {
		be.confPath = BtrbkConfPath(configPath)
	}

	shippers := make([]Shipper, 0, len(cfg.Ship))
	for _, sh := range cfg.Ship {
		shipper, err := newShipper(sh)
		if err != nil {
			return nil, err
		}
		shippers = append(shippers, shipper)
	}

	notifier, err := newNotifier(cfg.Notify)
	if err != nil {
		return nil, err
	}

	return &Manager{
		engine:     engine,
		shippers:   shippers,
		notifier:   notifier,
		subvolumes: cfg.Engine.Subvolumes,
		retention:  cfg.Engine.Retention,
	}, nil
}

// Run executes the pipeline per subvolume — Create → Prune → Send for each
// shipper — accumulating a RunResult (R7, R7.1). A failed stage is recorded and
// the run continues so other subvolumes/ships are still attempted; a cancelled
// context short-circuits before the next subvolume (R8.1). The Notifier hook is
// invoked exactly once with the final result (R7.3).
func (m *Manager) Run(ctx context.Context) (RunResult, error) {
	start := time.Now()
	result := RunResult{StartedAt: start}

	for _, sv := range m.subvolumes {
		if err := ctx.Err(); err != nil {
			result.Err = err.Error()
			result.Duration = time.Since(start)
			return result, err
		}

		// Create
		var snap Snapshot
		create := m.timeStage(sv, StageCreate, "", func() error {
			var err error
			snap, err = m.engine.Create(ctx, sv)
			return err
		})
		result.AddStage(create)
		if create.Status == StatusFailed {
			result.Err = fmt.Sprintf("create %s failed: %s", sv, create.Err)
			continue // prune/ship are meaningless without a snapshot
		}

		// Prune (retention delegated to the engine/native tool)
		prune := m.timeStage(sv, StagePrune, "", func() error {
			_, err := m.engine.Prune(ctx, sv, m.retention)
			return err
		})
		result.AddStage(prune)
		if prune.Status == StatusFailed && result.Err == "" {
			result.Err = fmt.Sprintf("prune %s failed: %s", sv, prune.Err)
		}

		// Ship to each target; a single ship failure does not abort the others.
		for _, shipper := range m.shippers {
			ship := m.timeStage(sv, StageShip, shipper.Name(), func() error {
				_, err := shipper.Send(ctx, snap)
				return err
			})
			result.AddStage(ship)
			if ship.Status == StatusFailed && result.Err == "" {
				result.Err = fmt.Sprintf("ship %s for %s failed: %s", shipper.Name(), sv, ship.Err)
			}
		}
	}

	// Notify once with the accumulated result (no-op default until story 005).
	_ = m.notifier.Notify(ctx, result)

	result.Duration = time.Since(start)
	if result.Failed() {
		return result, fmt.Errorf("snapshot run completed with failures")
	}
	return result, nil
}

// timeStage runs fn, timing it and mapping its error to a StageResult.
func (m *Manager) timeStage(subvolume, stage, target string, fn func() error) StageResult {
	started := time.Now()
	err := fn()
	s := StageResult{
		Subvolume: subvolume,
		Stage:     stage,
		Target:    target,
		StartedAt: started,
		Duration:  time.Since(started),
		Status:    StatusOK,
	}
	if err != nil {
		s.Status = StatusFailed
		s.Err = err.Error()
	}
	return s
}
