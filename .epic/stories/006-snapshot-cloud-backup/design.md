---
story: snapshot-cloud-backup
type: feature
scale: full
version: 1
created: 2026-06-08
---

# Design — Snapshot Cloud Backup + Restore

## 1. Overview

This story adds two `Shipper` drivers for off-site backup and the matching restore
path, on top of the 004 foundation:

- **`restic`** — file-level backup from a read-only snapshot mount to S3/B2/GCS or
  any rclone backend; dedup, compression, encryption, granular restore. The
  recommended cloud driver.
- **`archive`** — `btrfs send [-p parent] | zstd | rclone rcat` producing a single
  portable object on any rclone remote (e.g. Google Drive); bit-exact subvolume
  restore via `btrfs receive`.

Plus incremental **parent tracking** (`parent.go`), bentoolkit-side **GFS pruning
for `archive`** (rclone has no retention of its own), and a `restore` verb that
dispatches by driver. snapper rollback is a different path (→ 007).

Design source: [.epic/docs/snapshot-manager-proposal.md](../../docs/snapshot-manager-proposal.md)
§4.1, §9.2, §10. Depends on story 004 (`Shipper`, `Snapshot`, `Runner` seam,
config, `confirmFunc`).

## 2. Goals / Non-Goals

### Goals
- `restic` shipper: RO snapshot → mount → `restic backup` → `restic forget --prune`.
- `archive` shipper: `btrfs send [-p parent] | zstd | rclone rcat`, full or incremental.
- Parent tracking for incremental archive (and restic snapshot lineage where useful).
- bentoolkit-side GFS retention for the `archive` driver only.
- `restore` verb: archive (download → `zstd -d` → `btrfs receive`, chain-validated)
  and restic (`restic restore --target`, granular).
- Secrets via `password_file`/env and rclone config — never argv/TOML.
- Detection for `restic` and `rclone`; actionable errors.

### Non-Goals
- snapper rollback (→ 007). kopia driver (out of scope — not in Portage).
- Changing the btrbk engine or ssh shipper (004).
- Email/notify (005/008).

## 3. Architecture Decisions

- **AD1 — restic reads a read-only snapshot, not the live subvolume.** Consistency
  comes from a btrfs RO snapshot; restic backs up its mount. This is the
  atomic-backup best practice (proposal §9.2). The 004 systemd unit already sets
  `PrivateMounts=yes`; for a manual `run` the driver creates a transient RO mount
  and unmounts in a deferred cleanup.
- **AD2 — restic invocation.** `restic backup <mount> --tag bentoo,<subvol>
  --compression <auto|max|off>`; repo via `--repo`/`RESTIC_REPOSITORY`, password
  via `--password-file`/`RESTIC_PASSWORD_FILE`. Retention via `restic forget
  --prune` mapped from `[engine.retention]` (`--keep-hourly` etc.) — native, per
  the retention decision.
- **AD3 — archive is a streamed pipe.** `btrfs send [-p <parent>] <snap>` piped
  through a compressor (`zstd`) to `rclone rcat <remote>:<path>/<name>`. The whole
  pipe runs under one `context.Context`; any stage error fails the ship. Stream is
  tested by injecting the `Runner`/exec seam (no real btrfs).
- **AD4 — Parent tracking (`parent.go`).** Persist, per `(subvolume, ship.name)`,
  the id/UUID of the last successfully shipped snapshot under
  `/var/lib/bentoo/snapshot/parents/`. `archive` with `mode=incremental` uses it as
  `-p`; absent ⇒ full. Persisted only after a successful `rcat`. A `log()`-style
  notice records full-vs-incremental each run (no silent fallback).
- **AD5 — Archive retention is bentoolkit's job.** rclone has no GFS; the `archive`
  shipper lists remote objects (`rclone lsjson`), applies the GFS policy from
  `[engine.retention]`, and deletes the losers (`rclone deletefile`). restic/btrbk
  keep using native pruning.
- **AD6 — Restore dispatch.** `restore <id>` resolves which ship produced the id
  and dispatches: archive ⇒ `rclone cat | zstd -d | btrfs receive <target>`,
  validating the incremental chain (full + ordered deltas) before applying; restic
  ⇒ `restic restore <snap> --target <path>` (supports a single file/subdir).
  Destructive restores require `--yes`/interactive `confirmFunc` (004 seam).
- **AD7 — Secrets never in argv/TOML.** restic password via `--password-file` or
  `RESTIC_PASSWORD_FILE`; rclone via its own config/env. bentoolkit passes secret
  *paths*, never values; generated helper files are `0600` (atomic temp+rename).
- **AD8 — Two independent driver tracks.** `restic` and `archive` touch disjoint
  files (`ship_restic.go` vs `ship_archive.go` + `parent.go`) with no cross-data
  dependency — a natural agent-teams split at run time (proposal mentions this).

## 4. Component Design

### 4.1 restic shipper (`internal/snapshot/ship_restic.go`, new)
- `resticShipper{repo, passwordFile, compression, mount mounter, run Runner}`.
- `Send(ctx, snap)`: ensure RO snapshot → mount (transient or rely on
  `PrivateMounts`) → `restic backup` → `restic forget --prune` → unmount (defer).
  Returns `ShipReport{Driver:"restic", SnapshotID, Bytes, Dedup}`.
- Mount via a small `mounter` interface (`Mount`/`Unmount`) so tests fake it.

### 4.2 archive shipper (`internal/snapshot/ship_archive.go`, new)
- `archiveShipper{remote, mode, compress, run Runner, parents parentStore}`.
- `Send(ctx, snap)`: resolve parent (incremental) → build the
  `send|zstd|rclone rcat` pipe under ctx → on success persist parent → apply GFS
  retention on the remote.

### 4.3 Parent store (`internal/snapshot/parent.go`, new)
- `parentStore` over `/var/lib/bentoo/snapshot/parents/<subvol>__<ship>.json`;
  `Last(subvol, ship)`, `Record(subvol, ship, snap)`. Atomic writes.

### 4.4 Restore (`cmd/bentoo/snapshot_restore.go`, new + `internal/snapshot/restore.go`)
- `restore.go`: `Restore(ctx, id, target, opts)` dispatching by detected driver;
  archive chain validation; restic passthrough; `confirmFunc` gate.
- CLI verb wires flags `--target`, `--ship`, `--yes`.

### 4.5 Config + detect extensions
- Extend `ShipConfig` with restic fields (`repo`, `password_file`, `compression`,
  `mount_strategy`) and archive fields (`remote`, `mode`, `compress`).
- `detect.go`: add `restic`→`app-backup/restic`, `rclone`→`net-misc/rclone`.

## 5. Interfaces & Contracts
- Implements the 004 `Shipper` interface; no signature changes.
- New internal `mounter` (`Mount(ctx, snap) (path string, cleanup func() error,
  err error)`) and `parentStore` interfaces, both mockable.
- `ShipReport` (004) extended with optional `Bytes`/`Dedup`/`Incremental` fields
  (additive).
- Stream contract: `btrfs send` stream is opaque and order-dependent; restore must
  apply full + deltas in sequence (AD6).

## 6. Error Handling & Fallback

| Failure | Behavior |
|---------|----------|
| `restic`/`rclone` missing on PATH | detect → actionable error naming Portage pkg; exit 1 |
| RO mount fails | ship fails for that subvolume; cleanup still runs; RunResult records it |
| `send`/`zstd`/`rcat` pipe stage errors | whole ship fails; parent NOT recorded (no broken chain) |
| incremental parent missing remotely | warn + fall back to full; record as full |
| restore chain broken (missing delta) | refuse before `btrfs receive`; clear error |
| ctx cancelled | `exec.CommandContext` kills the pipe; partial object left for rclone/restic to overwrite next run |

## 7. Non-Functional Requirements
- **NFR-Security.** Secret paths only (never values) in argv; `0600` helper files;
  tokens/passwords never logged.
- **NFR-Integrity.** Prefer restic for frequent cloud backups (verifiable repo);
  for `archive`, validate the incremental chain before restore and never record a
  parent after a failed ship.
- **NFR-Cost/scan.** Document restic's local re-scan of large subvolumes
  (restic#4092); dedup avoids re-upload but the scan happens.
- **NFR-Robustness.** All subprocesses via `exec.CommandContext`; transient mounts
  always cleaned via `defer`.

## 8. Tooling Decisions
**E2E/frontend tooling: none** — backend Go/CLI. Drivers covered by mock
`Runner`/`mounter`/`parentStore`; real send/receive + rclone behind `*_live_test.go`
+ env skip (loopback btrfs + a throwaway rclone remote).

## 9. Risks & Mitigations
- **R-incremental-chain (HIGH):** a broken archive chain silently yields
  unrestorable backups. Mitigate: record parent only on success; validate chain
  before restore; emit full-vs-incremental notice each run.
- **R-mount-leak (MED):** a transient RO mount left mounted on error. Mitigate:
  `defer cleanup()`; live test asserts unmount on failure.
- **R-secrets (MED):** restic/rclone secrets leaking into argv/logs. Mitigate:
  password-file/env only; a test greps captured argv/logs for secret values.
- **R-restic-scan-cost (LOW):** large subvolumes re-scanned. Mitigate: document;
  not a correctness issue.

## 10. Open Questions
- OQ1: should `archive` also support a non-incremental "always full" simplicity
  mode by default? (proposal offers both `full`/`incremental`; default = ?) →
  confirm in acceptance; lean `incremental` with full fallback.
- OQ2: restic snapshot id ↔ btrfs snapshot mapping for `restore` lookup — tag-based
  (`--tag`) vs a local index? → tag-based first; revisit if lookups get slow.
