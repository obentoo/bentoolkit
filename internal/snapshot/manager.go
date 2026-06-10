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
		shipper, err := newShipper(sh, run, cfg.Engine.Retention)
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

	// Best-effort pre-run signal (e.g. healthchecks /start, R2.3). It is
	// outcome-independent and never changes the run — any error is ignored.
	if s, ok := m.notifier.(starter); ok {
		_ = s.Start(ctx)
	}

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

// remotePruner is implemented by shippers that can apply the GFS retention
// policy to their remote on demand (008 R3.1). Manager.Prune type-asserts it to
// select which shippers participate — currently only the archive shipper: ssh
// retention is delegated to btrbk's target_preserve, and restic prunes via
// forget --prune during Send, so neither has an out-of-band remote prune.
type remotePruner interface {
	PruneRemoteOnDemand(ctx context.Context, subvolumes []string) error
}

// Prune applies the [engine.retention] policy on demand (008 R3.1): the
// engine-native prune per subvolume (btrbk clean / snapper cleanup timeline),
// then the remote GFS sweep per archive ship. A non-empty shipScope narrows the
// prune to the named destination ONLY (008 R3.2): the engine-local prune is
// skipped entirely and just that ship's remote is pruned; an unknown scope
// returns an error before any stage runs. UNLIKE the post-ship best-effort
// prune inside Send, every failure here is recorded as a failed stage — a
// user-invoked prune must not hide errors — and, mirroring Run, a failed
// result yields a non-nil error. A cancelled context short-circuits before the
// next stage (R8.1).
func (m *Manager) Prune(ctx context.Context, shipScope string) (RunResult, error) {
	start := time.Now()
	result := RunResult{StartedAt: start}

	if shipScope != "" && !m.hasShipper(shipScope) {
		err := fmt.Errorf("no ship entry named %q", shipScope)
		result.Err = err.Error()
		result.Duration = time.Since(start)
		return result, err
	}

	// Engine-local prune per subvolume — skipped entirely under --ship scoping.
	if shipScope == "" {
		for _, sv := range m.subvolumes {
			if err := ctx.Err(); err != nil {
				result.Err = err.Error()
				result.Duration = time.Since(start)
				return result, err
			}
			prune := m.timeStage(sv, StagePrune, "", func() error {
				_, err := m.engine.Prune(ctx, sv, m.retention)
				return err
			})
			result.AddStage(prune)
			if prune.Status == StatusFailed && result.Err == "" {
				result.Err = fmt.Sprintf("prune %s failed: %s", sv, prune.Err)
			}
		}
	}

	// Remote GFS per in-scope archive ship. The stage's Subvolume is empty by
	// design: the remote holds objects from all subvolumes, so the sweep is not
	// attributable to one.
	for _, shipper := range m.shippers {
		if shipScope != "" && shipper.Name() != shipScope {
			continue
		}
		rp, ok := shipper.(remotePruner)
		if !ok {
			continue // ssh/restic: no on-demand remote prune (see remotePruner).
		}
		if err := ctx.Err(); err != nil {
			result.Err = err.Error()
			result.Duration = time.Since(start)
			return result, err
		}
		gfs := m.timeStage("", StageGFS, shipper.Name(), func() error {
			return rp.PruneRemoteOnDemand(ctx, m.subvolumes)
		})
		result.AddStage(gfs)
		if gfs.Status == StatusFailed && result.Err == "" {
			result.Err = fmt.Sprintf("gfs %s failed: %s", shipper.Name(), gfs.Err)
		}
	}

	result.Duration = time.Since(start)
	if result.Failed() {
		return result, fmt.Errorf("snapshot prune completed with failures")
	}
	return result, nil
}

// hasShipper reports whether a configured shipper answers to name (Shipper.Name,
// which falls back to the ship type when unnamed).
func (m *Manager) hasShipper(name string) bool {
	for _, sh := range m.shippers {
		if sh.Name() == name {
			return true
		}
	}
	return false
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
