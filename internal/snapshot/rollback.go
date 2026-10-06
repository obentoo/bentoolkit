package snapshot

import (
	"context"
	"errors"
	"fmt"
)

// rollback.go is the snapshot ROLLBACK entry point.
// Rollback is snapper-specific: it delegates to `snapper rollback <id>`, which
// makes a read-write copy of snapshot <id> the new default subvolume so the
// system boots into it on the next reboot. The engine guard fires FIRST: a
// non-snapper engine is refused before the operator is even prompted.
// Because a rollback rewires the running system it is gated behind operator
// confirmation unless --yes is given, mirroring restore.go's gate. The
// snapper subprocess goes through opts.Run.
//
// The CLI verb that wires this up is cmd/bentoo/snapshot_rollback.go.

// ErrRollbackDeclined is returned when the operator does not approve a
// destructive rollback at the confirm prompt. When this is returned,
// NOTHING has run — the gate fires before any subprocess. Mirrors
// ErrRestoreDeclined.
var ErrRollbackDeclined = errors.New("rollback declined by operator")

// ErrRollbackUnsupported is returned when the active engine is not snapper:
// `snapper rollback` has no btrbk equivalent, so rollback is refused
// outright with a clear message rather than approximated.
var ErrRollbackUnsupported = errors.New("rollback requires the snapper engine")

// RollbackOptions configures a Rollback. Yes/Confirm gate the destructive
// action; Run is the subprocess seam. Conventions mirror
// RestoreOptions: nil Confirm → defaultConfirmFunc, nil Run → defaultRunner().
type RollbackOptions struct {
	Yes     bool        // --yes: skip the confirm prompt
	Confirm confirmFunc // nil → defaultConfirmFunc
	Run     Runner      // nil → defaultRunner()
}

// Rollback rolls the system back to snapshot id via `snapper rollback`.
// The ORDER is the contract:
//
//  1. ENGINE GUARD: a non-snapper cfg.Engine.Driver is refused with
//     ErrRollbackUnsupported BEFORE the confirm gate — the operator is never
//     prompted to approve an action that cannot run.
//  2. CONFIRM GATE: unless opts.Yes, the operator must approve; a
//     decline returns ErrRollbackDeclined and NO subprocess runs.
//  3. EXECUTE: `snapper -c root rollback <id>` through opts.Run. A system
//     rollback always targets the canonical "root" snapper config
//     (snapperConfigName("/")) — rolling back is only meaningful for the
//     root filesystem.
func Rollback(ctx context.Context, cfg *Config, id string, opts RollbackOptions) error {
	// 1. Engine guard FIRST.
	if cfg.Engine.Driver != "snapper" {
		return fmt.Errorf("%w: active engine is %q", ErrRollbackUnsupported, cfg.Engine.Driver)
	}

	// 2. Confirm gate: BEFORE any subprocess. A declined rollback is a no-op.
	if !opts.Yes {
		confirm := opts.Confirm
		if confirm == nil {
			confirm = defaultConfirmFunc
		}
		prompt := fmt.Sprintf("Roll the system back to snapshot %q? This boots the root filesystem from that snapshot on next reboot and is destructive.", id)
		if !confirm(prompt) {
			return ErrRollbackDeclined
		}
	}

	// 3. Execute through the Runner seam.
	run := opts.Run
	if run == nil {
		run = defaultRunner()
	}
	args := []string{"-c", snapperConfigName("/"), "rollback", id}
	if _, err := run.Run(ctx, "snapper", args, nil); err != nil {
		return errors.Join(ErrEngineFailed, fmt.Errorf("snapper rollback %s: %w", id, err))
	}
	return nil
}
