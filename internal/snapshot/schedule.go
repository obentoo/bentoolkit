package snapshot

import "context"

// Scheduler installs and removes the periodic trigger for `bentoo snapshot run`.
// The systemd driver renders a .service + .timer. Apply is idempotent:
// re-running reconciles the units without creating duplicates.
type Scheduler interface {
	Apply(ctx context.Context, cfg ScheduleConfig) error
	Remove(ctx context.Context) error
}
