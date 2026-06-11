---
story: snapshot-cloud-backup
type: feature
scale: full
version: 1
created: 2026-06-08
---

# Story — Snapshot Cloud Backup + Restore

## Context

Story 004 ships snapshots locally and to SSH via btrbk. This story adds **off-site
cloud backup** and the matching **restore** path through two new `Shipper`
drivers — `restic` (recommended; dedup/encryption/granular restore to S3/B2/GCS or
any rclone backend) and `archive` (a single portable `btrfs send | zstd | rclone`
object for any remote, e.g. Google Drive). It also adds incremental parent
tracking, bentoolkit-side GFS retention for `archive`, and a `restore` verb.

This is **Phase 3** of the epic. Builds on 004 (`Shipper`, `Snapshot`, `Runner`
seam, config, `confirmFunc`). snapper rollback is separate (→ 007).

See [design.md](design.md) and
[.epic/docs/snapshot-manager-proposal.md](../../docs/snapshot-manager-proposal.md)
§4.1, §9.2.

## User Value

A user can push consistent, encrypted, deduplicated backups to cheap cloud storage
(restic) — or a simple bit-exact portable snapshot file to Google Drive (archive) —
on the same schedule and config as their local snapshots, and restore either a
single file or a whole subvolume when needed.

## Requirements (EARS)

### R1 — restic shipper
- R1.1 WHEN a `ship` entry has `type = "restic"` THE SYSTEM SHALL back up a
  read-only snapshot mount via `restic backup` to the configured repo.
- R1.2 THE SYSTEM SHALL pass the repo and password via `--repo`/`--password-file`
  (or `RESTIC_*` env), never via argv values or the TOML.
- R1.3 WHEN compression is configured THE SYSTEM SHALL pass `--compression`
  (`auto|max|off`).
- R1.4 WHEN the backup succeeds THE SYSTEM SHALL apply retention via
  `restic forget --prune` mapped from `[engine.retention]`.

### R2 — archive shipper
- R2.1 WHEN a `ship` entry has `type = "archive"` THE SYSTEM SHALL stream
  `btrfs send [-p parent] | <compress> | rclone rcat <remote>:<path>`.
- R2.2 WHEN `mode = "incremental"` AND a recorded parent exists THE SYSTEM SHALL
  pass it as `btrfs send -p`; otherwise SHALL send a full stream.
- R2.3 THE SYSTEM SHALL run the entire pipe under one cancellable context and
  SHALL fail the ship if any stage exits non-zero.

### R3 — Incremental parent tracking
- R3.1 THE SYSTEM SHALL persist, per `(subvolume, ship.name)`, the id of the last
  successfully shipped snapshot under `/var/lib/bentoo/snapshot/parents/`.
- R3.2 THE SYSTEM SHALL record the parent ONLY after a successful ship (no broken
  chains).
- R3.3 WHEN a configured incremental parent is unavailable THE SYSTEM SHALL warn,
  fall back to a full send, and record the run as full (no silent fallback).

### R4 — Archive retention (bentoolkit-side GFS)
- R4.1 WHEN the `archive` ship completes THE SYSTEM SHALL list remote objects and
  apply the GFS policy from `[engine.retention]`, deleting objects outside it.
- R4.2 THE archive retention SHALL never delete an object still required as a parent
  for the current incremental chain.

### R5 — Restore
- R5.1 THE `restore` verb SHALL dispatch by the driver that produced the snapshot
  id.
- R5.2 WHEN restoring an `archive` snapshot THE SYSTEM SHALL download and apply
  `zstd -d | btrfs receive`, validating the full+delta chain before applying.
- R5.3 WHEN restoring a `restic` snapshot THE SYSTEM SHALL run
  `restic restore --target`, supporting a single file/subdirectory.
- R5.4 WHEN a restore would overwrite data THE SYSTEM SHALL require `--yes` or an
  interactive confirmation (the 004 `confirmFunc` seam).

### R6 — Secrets
- R6.1 THE SYSTEM SHALL pass only secret *paths* (password-file) and rely on
  rclone's own config/env; SHALL never place secret values in argv or TOML.
- R6.2 THE SYSTEM SHALL never write passwords/tokens to logs or error messages.

### R7 — Robustness & detection
- R7.1 WHEN `restic` or `rclone` is absent from PATH THE SYSTEM SHALL fail at
  validate-time with an actionable error naming the Portage package.
- R7.2 THE SYSTEM SHALL invoke every subprocess via `exec.CommandContext`.
- R7.3 THE SYSTEM SHALL always clean up transient read-only mounts (deferred),
  including on error.

## Acceptance Criteria
- A `restic` ship backs up a RO snapshot mount, applies `forget --prune`, and never
  exposes the password in argv/logs (verified via scripted `Runner` + fake mounter).
- An `archive` ship streams `send|zstd|rcat`; with a recorded parent it uses
  `-p`, without one it sends full and logs which.
- A broken incremental chain is refused at restore time with a clear error; a
  failed ship does not record a parent.
- archive retention deletes out-of-policy remote objects but never the active
  parent.
- `restore` reconstructs an archive subvolume (`btrfs receive`) and restores a
  single file via restic; destructive restore requires `--yes`.
- `restic`/`rclone` absent → actionable error naming the Portage package.
- `go build ./...` + `go vet` clean; suites pass with mock `Runner`/`mounter`/
  `parentStore` (real send/receive + rclone gated behind `*_live_test.go`).

## Assumptions
- A1 (OQ1): `archive` defaults to `incremental` with full fallback.
- A2 (OQ2): restic snapshot lookup for restore is tag-based (`--tag bentoo,<subvol>`).
- A3: The 004 systemd unit's `PrivateMounts=yes` covers scheduled runs; manual
  `run` uses a transient RO mount cleaned via `defer`.
- A4: restic's local re-scan cost on large subvolumes is acceptable (documented).

## Out of Scope
- snapper rollback (→ 007); kopia driver (not in Portage — recorded out of scope);
  email/notify (005/008); changes to btrbk engine/ssh shipper (004).

## Dependencies
- Story 004. `app-backup/restic`, `net-misc/rclone` (handled by R7.1). btrfs-progs
  (`btrfs send/receive`). No new Go modules.
