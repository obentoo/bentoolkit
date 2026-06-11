---
story: snapshot-foundation-mvp
type: feature
scale: full
version: 1
created: 2026-06-08
---

# Design — Snapshot Manager Foundation + MVP

## 1. Overview

bentoolkit gains a new top-level command group, `bentoo snapshot`, that manages
btrfs snapshots declaratively. This story builds the **foundation** that every
later snapshot story consumes: the TOML config model, the four orchestration
interfaces (`Engine`, `Shipper`, `Notifier`, `Scheduler`), the first concrete
drivers (`btrbk` engine + `ssh` shipper, the latter delegated to btrbk), the
systemd timer generator, dependency detection, and the CLI verbs
`apply`/`run`/`list`/`status`.

bentoolkit is an **orchestrator**, not a btrfs implementation: it renders native
config for mature tools (btrbk here; snapper/restic/rclone in later stories) and
coordinates them. No destructive btrfs logic is written in Go.

Design source: [.epic/docs/snapshot-manager-proposal.md](../../docs/snapshot-manager-proposal.md).
This is **Phase 1** of a 5-story epic (005 notifications, 006 cloud, 007 snapper
rollback, 008 packaging/polish).

## 2. Goals / Non-Goals

### Goals
- TOML config (`snapshot.toml`) parsed/validated like `autoupdate` config, with
  system-scope path resolution (`/etc/bentoo/` preferred, XDG fallback).
- Four interfaces with a factory selecting drivers from config strings.
- `btrbk` engine driver: render `btrbk.conf`, invoke `btrbk run`/`btrbk clean`.
- `ssh` shipper: remote send/receive delegated to btrbk targets.
- systemd `.service` (`Type=oneshot`, `PrivateMounts=yes`) + `.timer` generation,
  install + `daemon-reload` + `enable`.
- `detect.go`: per-driver binary presence at config-validate time, actionable error.
- CLI: `apply` (materialize configs + timers), `run` (execute pipeline),
  `list` (snapshots), `status` (last run, timers, space).
- `Manager` pipeline ENGINE → (prune) → SHIP, accumulating a `RunResult`.

### Non-Goals
- Notifications beyond a no-op `Notifier` hook (→ 005).
- restic / archive / cloud / restore (→ 006).
- snapper engine, rollback, emerge hooks (→ 007).
- email, `--dry-run` full coverage, manual `prune` verb, final ebuild (→ 008).

## 3. Architecture Decisions

- **AD1 — Orchestrator, not reimplementation.** The engine/shipper drivers shell
  out to btrbk; Go never calls `btrfs` syscalls directly. This keeps the
  destructive paths inside audited tools (proposal §2).
- **AD2 — Four interfaces + factory.** Define `Engine`, `Shipper`, `Notifier`,
  `Scheduler`. Concrete impls are selected by a factory mirroring
  `internal/common/provider/factory.go:18` (`switch driver { case ...; default:
  return ErrInvalidDriver }`). `newEngine(cfg)`, `newShipper(ship)` return a
  sentinel `ErrInvalidDriver` for unknown strings.
- **AD3 — Mockable exec seam, not GitRunner.** Each driver holds an injectable
  `execCommand func(ctx, name string, args ...string) *exec.Cmd` defaulting to
  `exec.CommandContext`, plus a package var `lookPath = exec.LookPath` — the
  `internal/autoupdate/claude_code.go` idiom (claude_code.go:54), **not**
  `internal/common/git` (git-specific). A small `Runner` interface + `MockRunner`
  (git/mock.go style, with `var _ Runner = (*MockRunner)(nil)`) lets engine/shipper
  tests run with no real btrfs.
- **AD4 — TOML config like autoupdate.** BurntSushi/toml, `toml:"x,omitempty"`,
  `*bool` tri-state with `IsEnabled()`, package-level `Err*` sentinels, `Validate()`
  that fails hard on unknown enum strings and warns-but-continues on non-fatal
  issues. Path resolution extends the `ConfigPaths()`/`FindConfigPath()` pattern
  (`internal/common/config/config.go:79`) to prefer `/etc/bentoo/snapshot.toml`
  (system scope) then `~/.config/bentoo/snapshot.toml`.
- **AD5 — btrbk MVP engine + SSH shipper.** The `btrbk` engine renders a
  `btrbk.conf` from `[engine]` and runs `btrbk run`. The `ssh` shipper adds the
  remote `target` to the same `btrbk.conf` (btrbk owns send/receive); bentoolkit
  moves no bytes itself.
- **AD6 — Retention delegated to native.** `[engine.retention]` (GFS) maps to
  btrbk `snapshot_preserve*` / `target_preserve*` directives. bentoolkit does not
  compute GFS itself in this story (the `archive` driver in 006 is the only one
  that needs bentoolkit-side pruning).
- **AD7 — systemd generation (system scope).** `apply` writes
  `/etc/systemd/system/bentoo-snapshot.service` (`Type=oneshot`,
  `ExecStart=… snapshot run --config <path>`, `PrivateMounts=yes` for safe RO
  mounts later) and `bentoo-snapshot.timer` (`OnCalendar=`, `Persistent=`,
  `RandomizedDelaySec=`), then `systemctl daemon-reload` + `enable --now`. Units
  rendered via golden-file-tested templates.
- **AD8 — Detection at validate-time.** `detect.go` checks each active driver's
  binary via the `lookPath` seam during config validation and fails with an
  actionable error naming the Portage package (e.g. `engine driver "btrbk"
  requires app-backup/btrbk on PATH`) — mirrors `ErrClaudeCodeUnavailable`
  (claude_code.go:49) and the playwright/chromedp degradation pattern.
- **AD9 — RunResult for status/notify.** `run` builds a `RunResult` (per-stage
  status, durations, errors). `status` reads the last persisted result; the
  `Notifier` hook consumes it (no-op default until 005).
- **AD10 — Manager pipeline.** `Manager.Run(ctx)` = for each subvolume:
  `Engine.Create` → `Engine.Prune` → for each ship: `Shipper.Send`; collect into
  `RunResult`; call `Notifier.Notify` (no-op now). Context cancellation propagates
  to children via `exec.CommandContext`.

## 4. Component Design

### 4.1 Config (`internal/snapshot/config.go`, new)
- Structs: `Config{Engine, Ship []ShipConfig, Notify, Schedule}`,
  `EngineConfig{Driver, Subvolumes []string, SnapshotDir, Retention}`,
  `Retention{Hourly, Daily, Weekly, Monthly, PreserveMin}`,
  `ShipConfig{Name, Type, …}`, `ScheduleConfig{Backend, OnCalendar, Persistent
  *bool, RandomizedDelay}`.
- `Validate()`: hard error on unknown `engine.driver`/`ship.type`/`schedule.backend`;
  warn on empty subvolumes; calls `detect` for active drivers.
- Path: `ConfigPaths()` → `["/etc/bentoo/snapshot.toml",
  "$XDG_CONFIG_HOME/bentoo/snapshot.toml", "~/.config/bentoo/snapshot.toml"]`;
  `FindConfigPath()` first-existing; `DefaultConfigPath()` first.

### 4.2 Interfaces (`internal/snapshot/{engine,ship,notify,schedule}.go`)
See §5. Each interface file also holds its factory + `ErrInvalidDriver`/
`ErrInvalid*` sentinel and (engine/ship) the `Runner`/`MockRunner` seam.

### 4.3 Engine — btrbk (`engine_btrbk.go`)
- `btrbkEngine{cfg, run Runner}`. `Create` runs `btrbk run` (snapshot+send per
  conf); `Prune` runs `btrbk clean`; `List` parses `btrbk list`. Renders
  `btrbk.conf` from `EngineConfig` + retention.

### 4.4 Shipper — ssh (`ship_ssh.go`)
- `sshShipper` contributes a `target user@host:/path` block to the rendered
  `btrbk.conf`; `Send` is a no-op beyond ensuring btrbk ran with the target
  (btrbk performs send/receive during `Create`). Returns a `ShipReport`.

### 4.5 Scheduler — systemd (`systemd.go`)
- `Apply(cfg)` renders+writes the `.service`/`.timer`, `daemon-reload`,
  `enable --now`; `Remove()` disables + deletes. Templates are constants;
  rendered output is golden-file tested. Writes are atomic (temp + rename), `0644`.

### 4.6 Detection (`detect.go`)
- `detectDriver(kind, name) error` via `lookPath`; map name→Portage pkg.
  Called from `Config.Validate()`.

### 4.7 Manager + Result (`manager.go`, `result.go`)
- `Manager{cfg, engine, shippers, notifier, …}` built from `Config`.
  `Run(ctx) (RunResult, error)`. `RunResult{StartedAt, Stages []StageResult,
  Err}`, persisted under a state dir for `status`.

### 4.8 CLI (`cmd/bentoo/snapshot.go` + verbs)
- `snapshot.go`: `snapshotCmd` group, `rootCmd.AddCommand(snapshotCmd)` at
  `cmd/bentoo/main.go:51`. Verbs each in their own file with flag vars + `init()`
  → `snapshotCmd.AddCommand`. Flags: `--config`, `--subvolume`, `--ship`,
  `--yes`, `--dry-run` (stubbed; full coverage in 008). Fail-fast validation via
  `logger.Error` + `osExit(1)` (overlay_autoupdate.go:76).

## 5. Interfaces & Contracts

```go
type Snapshot struct {
    ID, Subvolume, Path string
    CreatedAt           time.Time
    ReadOnly            bool
    ParentID            string // "" = full
}

type Engine interface {
    Name() string
    Create(ctx context.Context, subvolume string) (Snapshot, error)
    Prune(ctx context.Context, subvolume string, policy Retention) ([]Snapshot, error)
    List(ctx context.Context, subvolume string) ([]Snapshot, error)
}

type Shipper interface {
    Name() string
    Send(ctx context.Context, snap Snapshot) (ShipReport, error)
}

type Notifier interface { Notify(ctx context.Context, res RunResult) error }
type Scheduler interface {
    Apply(ctx context.Context, cfg ScheduleConfig) error
    Remove(ctx context.Context) error
}

type Runner interface { // exec seam shared by engine/ship drivers
    Run(ctx context.Context, name string, args []string, stdin []byte) (stdout []byte, err error)
}
```

- Factories: `newEngine(EngineConfig) (Engine, error)`,
  `newShipper(ShipConfig) (Shipper, error)`, `newScheduler(ScheduleConfig)`,
  `newNotifier(NotifyConfig)` (no-op default). Unknown driver → `ErrInvalidDriver`.

## 6. Error Handling & Fallback

| Failure | Behavior |
|---------|----------|
| Unknown driver string in config | `Validate()` → `ErrInvalidDriver`, command exits 1 before any side effect |
| Driver binary missing on PATH | `detect` → actionable error naming Portage pkg; exit 1 |
| btrbk run non-zero | wrap stderr via `errors.Join(ErrEngineFailed, errors.New(stderr))`; stage marked failed in RunResult |
| ctx cancelled (SIGINT) | `exec.CommandContext` kills btrbk child; ctx error propagates; RunResult records partial |
| systemctl unavailable / non-systemd host | `apply` warns and writes units but skips enable, or errors per `schedule.backend` |

## 7. Non-Functional Requirements
- **NFR-Security.** Generated configs and any secret files `0600` (atomic temp +
  rename); dirs `0o750`. No secrets in argv/logs (none in this story, but the
  pattern is established for 006).
- **NFR-Robustness.** All subprocesses via `exec.CommandContext`; `apply` is
  idempotent (re-render + reconcile, no duplicate units).
- **NFR-Scope.** System scope assumed (root): `/etc/bentoo` + system timers; XDG
  paths still resolve for non-root inspection commands (`list`/`status`).

## 8. Tooling Decisions
**E2E/frontend tooling: none** — backend Go/CLI feature, no web surface. Drivers
are covered by unit tests with a scripted `Runner`/`execCommand` seam; real-btrfs
tests are gated behind `*_live_test.go` + env skip (proposal §13).

## 9. Risks & Mitigations
- **R-foundation-churn (MED):** the four interfaces are consumed by 4 later
  stories; signature changes ripple. Mitigate: review interfaces here against the
  006/007 needs already sketched in the proposal before landing.
- **R-btrbk-conf-render (MED):** hand-rendered `btrbk.conf` can drift from btrbk's
  grammar. Mitigate: golden-file tests + a gated live smoke test.
- **R-systemd-portability (LOW):** non-systemd hosts. Mitigate: `schedule.backend`
  is explicit; only `systemd` supported now, documented.
- **R-config-two-formats (LOW):** global config is YAML, this is TOML. Mitigate:
  keep snapshot on TOML (per-feature side, like autoupdate `packages.toml`).

## 10. Open Questions
- OQ1: state dir for persisted `RunResult` — `/var/lib/bentoo/snapshot/` vs under
  the config dir? → confirm in acceptance (proposal leans `/var/lib`).
- OQ2: should `list` aggregate remote (btrbk target) snapshots now or defer the
  remote listing to 006? → MVP lists local; remote listing tracked in 006.
