package snapshot

import (
	"context"
	"errors"
	"time"
)

// ErrInvalidDriver is returned by the factories (and by Config.Validate) when an
// engine.driver, ship.type, or schedule.backend names an unknown driver. It is
// the single shared sentinel for "unknown driver string" across the package
// (R1.3), mirroring provider.ErrInvalidProvider.
var ErrInvalidDriver = errors.New("invalid snapshot driver")

// ErrEngineFailed wraps a non-zero exit from the underlying engine binary
// (e.g. `btrbk run`). Stage errors in a RunResult are built by joining this with
// the captured stderr (design §6).
var ErrEngineFailed = errors.New("snapshot engine command failed")

// Snapshot describes a single point-in-time btrfs snapshot. ParentID is empty for
// a full snapshot and set to the parent's ID for an incremental one.
type Snapshot struct {
	ID        string
	Subvolume string
	Path      string
	CreatedAt time.Time
	ReadOnly  bool
	ParentID  string // "" = full
}

// Engine is the snapshot engine contract. Drivers (btrbk here; snapper in 007)
// create, prune, and list snapshots for a subvolume. Retention is delegated to
// the native tool (R2.3, AD6) — Prune passes the policy through rather than
// computing GFS in Go.
type Engine interface {
	Name() string
	Create(ctx context.Context, subvolume string) (Snapshot, error)
	Prune(ctx context.Context, subvolume string, policy Retention) ([]Snapshot, error)
	List(ctx context.Context, subvolume string) ([]Snapshot, error)
}
