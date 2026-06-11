---
story: snapshot-packaging-polish
type: feature
scale: standard
version: 1
created: 2026-06-08
---

# Story — Snapshot Packaging & Polish

## Context

With the engine/ship/notify/rollback layers landed (004–007), this final story
closes the gaps: an **email** notifier, full **`--dry-run`** coverage, a manual
**`prune`** verb, **status/list polish**, and the **ebuild** USE-flag mapping that
makes every backend an optional dependency. It turns the feature from "works" into
"shippable and discoverable".

This is **Phase 5** of the epic. Builds on all prior snapshot stories.
See [.epic/docs/snapshot-manager-proposal.md](../../docs/snapshot-manager-proposal.md)
§11, §14.

## User Value

A user can install bentoolkit pulling only the backends they enable (USE flags),
preview any operation safely with `--dry-run`, prune on demand, and see a clear
status of timers/last run/space — with complete README documentation.

## Requirements (EARS)

### R1 — Email notifier
- R1.1 WHEN `[notify.email]` is configured THE SYSTEM SHALL send a run summary via
  the configured transport (local `sendmail` or SMTP).
- R1.2 THE email notifier SHALL plug into the 005 composite notifier and respect
  `notify.on`.
- R1.3 THE SYSTEM SHALL never log SMTP credentials.

### R2 — Full `--dry-run`
- R2.1 WHEN `--dry-run` is passed to `apply` THE SYSTEM SHALL print the configs and
  systemd units it would write without writing them or calling systemctl.
- R2.2 WHEN `--dry-run` is passed to `run` THE SYSTEM SHALL print the engine/ship
  commands it would execute without running them.
- R2.3 WHEN `--dry-run` is passed to `restore`/`rollback`/`prune` THE SYSTEM SHALL
  print the destructive actions without performing them.

### R3 — Manual `prune` verb
- R3.1 THE SYSTEM SHALL provide `bentoo snapshot prune` that applies the
  `[engine.retention]` policy on demand (engine-native prune + archive GFS).
- R3.2 THE `prune` verb SHALL honor `--dry-run` and `--ship` scoping.

### R4 — Ebuild USE flags
- R4.1 THE ebuild SHALL expose `IUSE="btrbk snapper restic rclone systemd"` mapping
  each to its Portage dependency in `RDEPEND` (optional backends).
- R4.2 THE runtime `detect` SHALL fail with an actionable per-driver error when a
  required backend for the active config is absent (already built in 004/006/007;
  validated end-to-end here).

### R5 — Status / list polish
- R5.1 THE `status` verb SHALL report timer state (`systemctl is-enabled/--list-timers`),
  the last `RunResult` (per-stage), and free space of snapshot/target locations.
- R5.2 THE `list` verb SHALL optionally include remote snapshots (btrbk targets and
  restic snapshots) when `--remote` is passed.

### R6 — Documentation
- R6.1 THE README SHALL document the full `snapshot.toml` schema, every verb, the
  USE flags, and a quick-start example.
- R6.2 THE CHANGELOG SHALL record the completed snapshot feature.

## Acceptance Criteria
- `[notify.email]` sends a summary via sendmail/SMTP (stubbed transport in tests),
  respecting `notify.on`, with no credential leakage.
- `--dry-run` on apply/run/restore/rollback/prune performs no side effects and
  prints the intended actions (asserted via seams).
- `bentoo snapshot prune` applies retention on demand and honors `--dry-run`/`--ship`.
- The ebuild builds with each USE flag toggling the right RDEPEND; an active config
  needing a disabled/absent backend errors actionably.
- `status` shows timer state + last run + space; `list --remote` includes remote
  snapshots.
- README/CHANGELOG complete; `go build ./...` + `go vet` clean; suites pass.

## Assumptions
- A1: Email transport is local `sendmail` first, with optional SMTP config; no new
  heavy mail dependency beyond stdlib `net/smtp` if SMTP is used.
- A2: `--dry-run` was stubbed in 004; this story makes it real across all verbs.
- A3: The ebuild lives in the bentoo overlay; this story provides the USE-flag/
  RDEPEND mapping and metadata, validated against `detect`.

## Out of Scope
- New ship/engine drivers (kopia, etc.); grub-btrfs integration; a TUI/GUI.

## Dependencies
- Stories 004–007. Optional: `app-backup/btrbk`, `app-backup/snapper`,
  `app-backup/restic`, `net-misc/rclone`, `systemd` (all via USE flags). stdlib
  `net/smtp`/`os/exec` for email.
