---
story: snapshot-foundation-mvp
type: feature
scale: full
version: 1
created: 2026-06-08
---

# Story — Snapshot Manager Foundation + MVP

## Context

bentoolkit today maintains Gentoo overlays/ebuilds (`overlay`, `autoupdate`) but
has no facility for managing the btrfs filesystem it runs on. This story adds the
**foundation** of a new `bentoo snapshot` command group that orchestrates mature
snapshot tools through a single declarative TOML config — mirroring how
`autoupdate` orchestrates external tooling rather than reimplementing it.

Scope here is **Phase 1**: config model, the four orchestration interfaces
(`Engine`/`Shipper`/`Notifier`/`Scheduler`), the first drivers (`btrbk` engine +
`ssh` shipper), systemd timer generation, dependency detection, and the CLI verbs
`apply`/`run`/`list`/`status`. Notifications (005), cloud/restore (006), snapper
rollback (007) and packaging/polish (008) build on this.

See [design.md](design.md) and
[.epic/docs/snapshot-manager-proposal.md](../../docs/snapshot-manager-proposal.md).

## User Value

A btrfs user configures one `snapshot.toml`, runs `bentoo snapshot apply`, and
gets scheduled local snapshots with optional SSH replication — without learning
btrbk's config grammar or hand-writing systemd units, and managed by the same
tool they already use for their overlay.

## Requirements (EARS)

### R1 — Config model
- R1.1 THE SYSTEM SHALL parse `snapshot.toml` (BurntSushi/toml) into a `Config`
  with `engine`, `ship[]`, `notify`, and `schedule` sections.
- R1.2 WHEN resolving the config path THE SYSTEM SHALL prefer
  `/etc/bentoo/snapshot.toml`, then `$XDG_CONFIG_HOME/bentoo/snapshot.toml`, then
  `~/.config/bentoo/snapshot.toml`.
- R1.3 WHEN `engine.driver`, a `ship.type`, or `schedule.backend` holds an unknown
  value THE SYSTEM SHALL fail validation with `ErrInvalidDriver` before any side
  effect.
- R1.4 WHEN a non-fatal issue is present (e.g. empty `subvolumes`) THE SYSTEM
  SHALL warn and continue, following the autoupdate validate-and-warn pattern.

### R2 — Engine interface + btrbk driver
- R2.1 THE SYSTEM SHALL define an `Engine` interface (`Create`/`Prune`/`List`)
  selected from `engine.driver` via a factory.
- R2.2 WHEN `engine.driver` is `"btrbk"` THE SYSTEM SHALL render a `btrbk.conf`
  from `[engine]` and create snapshots by invoking `btrbk run`.
- R2.3 WHEN pruning THE SYSTEM SHALL map `[engine.retention]` to btrbk
  `snapshot_preserve`/`target_preserve` directives (retention delegated to btrbk).
- R2.4 THE SYSTEM SHALL invoke btrbk through an injectable `Runner`/`execCommand`
  seam so tests run without a real btrfs or btrbk.

### R3 — Shipper interface + ssh driver
- R3.1 THE SYSTEM SHALL define a `Shipper` interface (`Send`) selected from
  `ship.type` via a factory.
- R3.2 WHEN a `ship` entry has `type = "ssh"` THE SYSTEM SHALL add its `target`
  as a btrbk remote so send/receive is performed by btrbk (no bytes moved in Go).

### R4 — Scheduler / systemd generation
- R4.1 WHEN `schedule.backend` is `"systemd"` THE `apply` command SHALL render and
  install `bentoo-snapshot.service` (`Type=oneshot`, `PrivateMounts=yes`,
  `ExecStart` = `bentoo snapshot run --config <path>`) and `bentoo-snapshot.timer`
  (`OnCalendar`, `Persistent`, `RandomizedDelaySec`).
- R4.2 WHEN units are installed THE SYSTEM SHALL run `systemctl daemon-reload` and
  `enable --now` the timer.
- R4.3 THE `apply` command SHALL be idempotent — re-running reconciles units
  without creating duplicates.

### R5 — CLI verbs
- R5.1 THE SYSTEM SHALL register a top-level `snapshot` command group on `rootCmd`.
- R5.2 THE `apply` verb SHALL materialize native configs + systemd units.
- R5.3 THE `run` verb SHALL execute the pipeline (engine → prune → ship) and is
  the target of the systemd timer.
- R5.4 THE `list` verb SHALL list local snapshots per subvolume.
- R5.5 THE `status` verb SHALL report the last run result, timer state, and space.

### R6 — Dependency detection / degradation
- R6.1 WHEN an active driver's binary is absent from PATH THE SYSTEM SHALL fail at
  config-validate time with an actionable error naming the Portage package
  (e.g. `requires app-backup/btrbk on PATH`).
- R6.2 THE detection SHALL use an injectable `lookPath` seam for tests.

### R7 — Manager pipeline + RunResult
- R7.1 THE `Manager.Run` SHALL, per subvolume, call `Engine.Create` then
  `Engine.Prune` then each `Shipper.Send`, accumulating a `RunResult`.
- R7.2 THE `RunResult` SHALL record per-stage status, durations, and errors and
  be persisted for `status`.
- R7.3 THE `Manager` SHALL invoke a `Notifier` hook with the `RunResult`
  (no-op default in this story; implemented in 005).

### R8 — Robustness & security
- R8.1 THE SYSTEM SHALL invoke every subprocess via `exec.CommandContext` so a
  cancelled parent context (SIGINT/timeout) kills in-flight children.
- R8.2 THE SYSTEM SHALL write generated config/unit files atomically (temp +
  rename) with `0644`, and any future secret files `0600`.

## Acceptance Criteria
- A valid `snapshot.toml` with `engine.driver=btrbk` + one `ssh` ship + a
  `systemd` schedule: `apply` renders a `btrbk.conf` and installs+enables the
  timer (verified via scripted `Runner` seam, no real btrbk/systemd).
- `run` drives the engine→prune→ship pipeline and writes a `RunResult`; `status`
  reads it back.
- Unknown `engine.driver` → validation error, exit 1, no files written.
- btrbk absent from PATH → actionable error naming `app-backup/btrbk`.
- `go build ./...` + `go vet` clean; `internal/snapshot` and `cmd/bentoo` suites
  pass with mock-based tests (real-btrfs tests gated behind `*_live_test.go`).

## Assumptions
- A1 (OQ1): persisted `RunResult` lives under `/var/lib/bentoo/snapshot/`.
- A2 (OQ2): `list` covers **local** snapshots only; remote (btrbk target) listing
  is deferred to story 006.
- A3: System scope (root) is the primary target — `/etc/bentoo` + system timers;
  XDG paths still resolve for read-only `list`/`status`.
- A4: Only `systemd` is a supported `schedule.backend` in this story.

## Out of Scope
- Notifications implementation (→ 005); restic/archive/cloud/restore (→ 006);
  snapper engine, rollback, emerge hooks (→ 007); email, full `--dry-run`,
  manual `prune` verb, final ebuild USE flags (→ 008).

## Dependencies
- `app-backup/btrbk` (engine + ssh ship); `systemd` (scheduler). Absence handled
  by R6.1. No new Go modules (stdlib `os/exec`, existing BurntSushi/toml).
