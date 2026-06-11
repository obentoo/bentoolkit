---
story: snapshot-foundation-mvp
type: feature
scale: full
version: 1
created: 2026-06-08
---

# Tasks — Snapshot Manager Foundation + MVP

Sequencing: T1 (config) → T2 (interfaces + factories) → T3 (btrbk engine) →
T4 (ssh ship) → T5 (systemd) → T6 (manager + result) → T7 (CLI) → T8 (detect) →
T9 (docs + commit). T3/T4 depend on T2; T6 depends on T2–T4; T7 depends on T5/T6;
T8 is cross-cutting (wired into T1's `Validate`).

Test policy (Full): Unit/Integration sub-tasks carry a `Tests` field with the
scenarios to author (Red-first at run time via the scripted `Runner`/`execCommand`
seam — no real btrbk/systemd/btrfs). Pure-plumbing sub-tasks carry `Acceptance`.
`Covered-by` points at the test file. Real-filesystem checks are gated behind
`*_live_test.go` + env skip.

---

## T1 — Config model + path resolution

### 1.1 [x] Config structs + TOML tags
- Files: `internal/snapshot/config.go` (new)
- `Config`, `EngineConfig`, `Retention`, `ShipConfig`, `NotifyConfig` (placeholder),
  `ScheduleConfig` with `toml:"...,omitempty"`, `*bool` tri-state where optional.
- EARS: R1.1
- Tests: parse a representative `snapshot.toml` → all sections populated; omitted
  optionals → zero/nil; `[[ship]]` array-of-tables parses to a slice.
- Covered-by: `internal/snapshot/config_test.go`

### 1.2 [x] Path resolution
- `ConfigPaths()` (/etc → XDG → ~/.config), `FindConfigPath()`, `DefaultConfigPath()`.
- EARS: R1.2
- Tests: ordering asserted; first-existing wins (temp dirs); default = first.
- Covered-by: `config_test.go`

### 1.3 [x] `Validate()` + sentinels
- Package-level `Err*` (incl. `ErrInvalidDriver`); hard error on unknown
  enum strings; warn-but-continue on empty subvolumes. Calls `detect` (T8).
- EARS: R1.3, R1.4
- Tests: unknown engine/ship/schedule value → `ErrInvalidDriver`; empty
  subvolumes → warn + nil error; valid config → nil.
- Covered-by: `config_test.go`

---

## T2 — Interfaces + factories + Runner seam

### 2.1 [x] Define interfaces
- Files: `internal/snapshot/{engine,ship,notify,schedule}.go`
- `Engine`, `Shipper`, `Notifier`, `Scheduler`, `Snapshot`, `ShipReport` per design §5.
- EARS: R2.1, R3.1, R7.3
- Tests: compile-time assertions; interface method sets stable (doc test).
- Covered-by: `internal/snapshot/interfaces_test.go`

### 2.2 [x] Runner seam + MockRunner
- `Runner` interface (`Run(ctx,name,args,stdin)`), default `execRunner` using
  `exec.CommandContext`; `lookPath = exec.LookPath` var; `MockRunner` with
  per-call func fields + `var _ Runner = (*MockRunner)(nil)`.
- EARS: R2.4, R6.2, R8.1
- Tests: `execRunner` uses CommandContext (ctx cancel kills a sleep script);
  stdin is piped not argv; `MockRunner` records calls.
- Covered-by: `internal/snapshot/runner_test.go`

### 2.3 [x] Factories
- `newEngine`, `newShipper`, `newScheduler`, `newNotifier` (no-op default),
  `switch`-based, unknown → `ErrInvalidDriver`.
- EARS: R2.1, R3.1, R7.3
- Tests: each known string → correct concrete type; unknown → `ErrInvalidDriver`.
- Covered-by: `internal/snapshot/factory_test.go`

---

## T3 — btrbk engine driver

### 3.1 [x] Render `btrbk.conf`
- Files: `internal/snapshot/engine_btrbk.go` (new)
- Render from `EngineConfig` + `Retention` → btrbk grammar
  (`snapshot_preserve`/`target_preserve`, `subvolume`, `snapshot_dir`).
- EARS: R2, R2.2, R2.3
- Tests (golden): minimal config → expected conf; retention → expected preserve
  lines; multiple subvolumes → multiple blocks.
- Covered-by: `internal/snapshot/engine_btrbk_test.go` + `testdata/*.golden`

### 3.2 [x] Create/Prune/List via Runner
- `Create` → `btrbk run`; `Prune` → `btrbk clean`; `List` → parse `btrbk list`.
- EARS: R2.2, R2.3, R2.4
- Tests: MockRunner asserts argv + conf path; non-zero exit → wrapped error;
  `List` parses sample output into `[]Snapshot`.
- Covered-by: `engine_btrbk_test.go`

---

## T4 — ssh shipper

### 4.1 [x] ssh target contribution + Send
- Files: `internal/snapshot/ship_ssh.go` (new)
- Adds `target user@host:/path` to the btrbk conf; `Send` returns a `ShipReport`
  (btrbk performs the transfer during engine `Create`).
- EARS: R3, R3.2
- Tests: ssh ship → expected target block in rendered conf; `Send` reports the
  target; invalid target string → error.
- Covered-by: `internal/snapshot/ship_ssh_test.go`

---

## T5 — systemd scheduler

### 5.1 [x] Render .service/.timer
- Files: `internal/snapshot/systemd.go` (new)
- Const templates → `bentoo-snapshot.service` (`Type=oneshot`, `PrivateMounts=yes`,
  `ExecStart`) + `.timer` (`OnCalendar`/`Persistent`/`RandomizedDelaySec`).
- EARS: R4.1
- Tests (golden): schedule config → expected unit text; `Persistent` nil vs set;
  randomized delay formatting.
- Covered-by: `internal/snapshot/systemd_test.go` + `testdata/*.golden`

### 5.2 [x] Install/enable/remove (atomic, idempotent)
- `Apply` writes units atomically (temp+rename, 0644), `daemon-reload`,
  `enable --now`; `Remove` disables + deletes. systemctl via Runner.
- EARS: R4.2, R4.3, R8, R8.2
- Tests: MockRunner asserts daemon-reload + enable order; re-apply → no duplicate
  (same path overwritten); Remove → disable + unlink.
- Covered-by: `systemd_test.go`

---

## T6 — Manager + RunResult

### 6.1 [x] RunResult type + persistence
- Files: `internal/snapshot/result.go` (new)
- `RunResult{StartedAt, Stages []StageResult, Err}`; persist/load JSON under
  `/var/lib/bentoo/snapshot/last-run.json` (atomic write).
- EARS: R7.2
- Tests: round-trip persist/load; partial result on stage error preserved.
- Covered-by: `internal/snapshot/result_test.go`

### 6.2 [x] Manager.Run pipeline
- Files: `internal/snapshot/manager.go` (new)
- Build from `Config` (factories); per subvolume `Create→Prune→Send[]`; collect
  `RunResult`; call `Notifier.Notify` (no-op). Context plumbed throughout.
- EARS: R7, R7.1, R7.3, R8.1
- Tests: pipeline order asserted with mocks; one ship fails → RunResult marks it,
  others still attempted; ctx cancel short-circuits; Notifier hook invoked once.
- Covered-by: `internal/snapshot/manager_test.go`

---

## T7 — CLI verbs

### 7.1 [x] `snapshot` group + registration
- Files: `cmd/bentoo/snapshot.go` (new); edit `cmd/bentoo/main.go`
- `snapshotCmd` + `rootCmd.AddCommand(snapshotCmd)`; persistent `--config` flag.
- EARS: R5.1
- Acceptance: `bentoo snapshot --help` lists the verbs; group wired at main.go.
- Covered-by: `cmd/bentoo/snapshot_test.go`

### 7.2 [x] `apply` verb
- Files: `cmd/bentoo/snapshot_apply.go` (new)
- Load+validate config → render native configs → install systemd units. `--dry-run`
  prints actions (stub; full coverage in 008).
- EARS: R5.2, R4.1
- Tests: with mock seams, apply renders conf + installs timer; invalid config →
  osExit(1) (osExit seam).
- Covered-by: `snapshot_apply_test.go`

### 7.3 [x] `run` verb
- Files: `cmd/bentoo/snapshot_run.go` (new)
- Build Manager, `signal.NotifyContext`, `Manager.Run`, persist RunResult, exit
  code reflects failure.
- EARS: R5.3, R7.1, R8.1
- Tests: run drives pipeline (mock Manager/seams); failure → non-zero exit.
- Covered-by: `snapshot_run_test.go`

### 7.4 [x] `list` + `status` verbs
- Files: `cmd/bentoo/snapshot_list.go`, `snapshot_status.go` (new)
- `list` → engine `List` per subvolume; `status` → load last RunResult + timer
  state (`systemctl is-enabled`) + space.
- EARS: R5.4, R5.5
- Tests: `list` renders snapshots from mock engine; `status` reads persisted
  RunResult + reports timer state from mock Runner.
- Covered-by: `snapshot_list_test.go`, `snapshot_status_test.go`

---

## T8 — Dependency detection

### 8.1 [x] detect.go (validate-time)
- Files: `internal/snapshot/detect.go` (new)
- `detectDriver(kind, name)` via `lookPath`; name→Portage pkg map; called from
  `Config.Validate()`.
- EARS: R6, R6.1, R6.2
- Tests: missing binary (temp PATH / lookPath seam) → actionable error naming the
  pkg; present → nil.
- Covered-by: `internal/snapshot/detect_test.go`

---

## T9 — Docs + commit

### 9.1 [x] README + CHANGELOG
- Document `bentoo snapshot` (apply/run/list/status), `snapshot.toml` schema
  (engine/ship/schedule), system-scope paths, and the btrbk dependency.
- EARS: (docs for R1, R4, R5)
- Acceptance: README section + CHANGELOG `[Unreleased]` entry present.

### 9.2 [x] Commit (gate)
- `go build ./...`, `go vet ./internal/snapshot/ ./cmd/bentoo/`, suites green.
- Commit direct to `main` (Conventional Commits + Co-Authored-By).
- Acceptance: clean build/vet/tests; single coherent commit.

---

## Quality Gates
- **G1 — Build/vet:** `go build ./...` and `go vet ./internal/snapshot/ ./cmd/bentoo/` clean.
- **G2 — Tests green:** `internal/snapshot` + `cmd/bentoo` suites pass; drivers
  covered via scripted `Runner`/`execCommand` seam (no real btrbk/systemd/btrfs).
- **G3 — Validation pre-side-effect:** unknown driver and missing-binary cases
  fail before any file is written (asserted).
- **G4 — Golden render:** `btrbk.conf` and systemd unit renders match goldens.
- **G5 — Context safety:** a test cancels the parent ctx and asserts the child is
  killed (CommandContext).
- **G6 — Docs:** README + CHANGELOG updated before the T9.2 commit.

## Validation (per task)
- `go build ./...` and `go vet ./internal/snapshot/ ./cmd/bentoo/`
- `go test ./internal/snapshot/ ./cmd/bentoo/` for touched packages; mock-based,
  no network/btrfs. Live smoke tests gated behind `*_live_test.go` + env.
- Final: full-suite green + `go vet` clean before T9.2 commit.

## Notes
- The four interfaces are consumed by stories 005–007; validate their signatures
  against those stories' sketches in the proposal before landing T2.
- Test authoring/Red-verification happens at run time (Executor + scripted seam);
  this plan defines the scenarios.
