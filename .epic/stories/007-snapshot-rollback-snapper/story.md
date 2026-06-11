---
story: snapshot-rollback-snapper
type: feature
scale: standard
version: 1
created: 2026-06-08
---

# Story — Snapshot Rollback (snapper engine)

## Context

The btrbk engine (004) is built for backup/replication, not system rollback. This
story adds a second `Engine` driver — **snapper** — for local timeline snapshots
and **rollback**, plus an opt-in Portage hook that snapshots before/after `emerge`.
This is the "undo a broken update" capability that complements btrbk's "ship it
off-site".

This is **Phase 4** of the epic. Builds on the 004 `Engine` interface, factory,
`Runner` seam, config, and `confirmFunc`.
See [.epic/docs/snapshot-manager-proposal.md](../../docs/snapshot-manager-proposal.md)
§4.1, §9.1, §10.

## User Value

A Gentoo user can roll the system back to a known-good snapshot after a bad update,
and can opt into automatic pre/post-`emerge` snapshots — managed by the same
`bentoo snapshot` config, without learning snapper's CLI directly.

## Requirements (EARS)

### R1 — snapper engine driver
- R1.1 WHEN `engine.driver` is `"snapper"` THE SYSTEM SHALL implement the 004
  `Engine` interface (`Create`/`Prune`/`List`) by invoking `snapper`.
- R1.2 WHEN creating THE SYSTEM SHALL run `snapper -c <config> create` with a
  description tag identifying bentoo.
- R1.3 WHEN listing THE SYSTEM SHALL parse `snapper -c <config> list` into
  `[]Snapshot`.
- R1.4 WHEN pruning THE SYSTEM SHALL delegate to snapper's timeline cleanup mapped
  from `[engine.retention]` (native retention, per the project decision).

### R2 — snapper config rendering
- R2.1 THE SYSTEM SHALL render/ensure a snapper config under
  `/etc/snapper/configs/<name>` for each managed subvolume from `[engine]`.
- R2.2 THE `apply` command SHALL create the snapper config idempotently (no
  duplicate or clobbered user settings beyond the managed keys).

### R3 — Rollback
- R3.1 THE SYSTEM SHALL provide `bentoo snapshot rollback <id>` that runs
  `snapper rollback <id>`.
- R3.2 WHEN performing a rollback THE SYSTEM SHALL require `--yes` or interactive
  confirmation via the 004 `confirmFunc` seam (destructive operation).
- R3.3 WHEN the snapper engine is not the active driver THE SYSTEM SHALL refuse
  rollback with a clear message (rollback is snapper-specific).

### R4 — Opt-in emerge hook
- R4.1 THE SYSTEM SHALL provide `bentoo snapshot hook --install` that installs a
  Portage hook taking a snapshot before and after `emerge`.
- R4.2 THE SYSTEM SHALL provide `bentoo snapshot hook --uninstall` that cleanly
  removes the hook.
- R4.3 THE hook installation SHALL NOT modify the system unless the command is run
  explicitly (no install during `apply`).

### R5 — Detection & degradation
- R5.1 WHEN `snapper` is absent from PATH THE SYSTEM SHALL fail at validate-time
  with an actionable error naming `app-backup/snapper` (verified in ::gentoo).
- R5.2 THE detection SHALL use the 004 `lookPath` seam.

### R6 — Robustness
- R6.1 THE SYSTEM SHALL invoke `snapper` via `exec.CommandContext`.
- R6.2 THE SYSTEM SHALL not alter existing btrbk-engine behavior; snapper is an
  additive driver selectable by config.

## Acceptance Criteria
- `engine.driver = snapper`: `run` creates a tagged snapshot, `list` parses
  snapper output, prune delegates to snapper cleanup (scripted `Runner`).
- `apply` ensures `/etc/snapper/configs/<name>` idempotently.
- `bentoo snapshot rollback <id>` runs `snapper rollback` only after `--yes`/
  confirm; refused when snapper is not the active engine.
- `hook --install`/`--uninstall` add/remove the Portage hook; nothing is installed
  during `apply`.
- snapper absent → actionable error naming `app-backup/snapper`.
- `go build ./...` + `go vet` clean; suites pass with scripted `Runner` (real
  snapper gated behind `*_live_test.go`).

## Assumptions
- A1: Rollback is snapper-specific; the btrbk engine has no rollback (R3.3).
- A2: The emerge hook is opt-in via the `hook` command (decision: opt-in, not
  documented-only, not during `apply`).
- A3: snapper's own timeline cleanup is the retention mechanism for this engine.

## Out of Scope
- Cloud/restore drivers (→ 006); notifications (005); email/packaging (008).
- grub-btrfs / boot-into-snapshot integration (documented as a follow-up, not
  implemented here).

## Dependencies
- Story 004. `app-backup/snapper` (handled by R5.1). No new Go modules.
