---
story: snapshot-cloud-backup
type: feature
scale: full
version: 1
created: 2026-06-08
---

# Tasks — Snapshot Cloud Backup + Restore

Sequencing: T1 (config+detect) → {T2 restic, T3 archive} (independent tracks) →
T4 (parent store, used by T3) → T5 (archive retention) → T6 (restore) → T7
(docs+commit). T2 and T3 are disjoint files (agent-teams candidate); T3 depends on
T4's `parentStore` for incremental.

Test policy (Full): driver sub-tasks carry `Tests` (Red-first at run time) using a
scripted `Runner` + fake `mounter`/`parentStore` — no real btrfs/restic/rclone.
Real send/receive + rclone are gated behind `*_live_test.go` + env skip.
`Covered-by` points at the test file.

---

## T1 — Config + detection extensions

### 1.1 [x] Extend ShipConfig + detect
- Files: `internal/snapshot/config.go`, `internal/snapshot/detect.go`
- Add restic fields (`repo`, `password_file`, `compression`, `mount_strategy`) and
  archive fields (`remote`, `mode`, `compress`) to `ShipConfig`; map
  `restic`→`app-backup/restic`, `rclone`→`net-misc/rclone` in detect.
- EARS: R1, R1.2, R1.3, R2, R7, R7.1
- Tests: parse restic/archive ship entries; unknown ship type still
  `ErrInvalidDriver`; missing restic/rclone → actionable error naming the pkg.
- Covered-by: `internal/snapshot/config_test.go`, `detect_test.go`

---

## T2 — restic shipper (track A)

### 2.1 [x] mounter seam + RO mount
- Files: `internal/snapshot/ship_restic.go` (new)
- `mounter` interface (`Mount(ctx,snap)→(path,cleanup,err)`); transient RO mount
  impl; `defer cleanup()`.
- EARS: R7, R7.3
- Tests: fake mounter returns path + cleanup; cleanup called even when backup
  errors (assert via spy).
- Covered-by: `internal/snapshot/ship_restic_test.go`

### 2.2 [x] restic backup + prune
- `resticShipper.Send`: `restic backup <mount> --tag bentoo,<subvol>
  --compression …` with repo/password via flags/env; then `restic forget --prune`
  from `[engine.retention]`.
- EARS: R1, R1.1, R1.3, R1.4, R6, R6.1, R6.2, R7.2
- Tests (scripted Runner): argv carries repo/compression but NOT the password
  value; env carries `RESTIC_PASSWORD_FILE`; forget maps retention; password
  value absent from any captured argv/log.
- Covered-by: `ship_restic_test.go`

---

## T3 — archive shipper (track B)

### 3.1 [x] send|compress|rcat pipe
- Files: `internal/snapshot/ship_archive.go` (new)
- Build the `btrfs send [-p parent] | zstd | rclone rcat <remote>:<path>` pipe
  under one ctx; non-zero any stage → ship error.
- EARS: R2, R2.1, R2.3, R7.2
- Tests (scripted Runner): pipe wiring + argv; a stage failure fails the ship;
  ctx cancel kills the pipe.
- Covered-by: `internal/snapshot/ship_archive_test.go`

### 3.2 [x] incremental vs full selection
- Use `parentStore` (T4): incremental + parent present → `-p`; else full + warn.
- EARS: R2, R2.2, R3, R3.3
- Tests: parent present → `-p <id>`; absent → full + warn logged; mode=full →
  always full.
- Covered-by: `ship_archive_test.go`

---

## T4 — Parent tracking

### 4.1 [x] parentStore
- Files: `internal/snapshot/parent.go` (new)
- `Last(subvol,ship)`, `Record(subvol,ship,snap)` over
  `/var/lib/bentoo/snapshot/parents/`; atomic writes; record only after success
  (called from T3 on success).
- EARS: R3, R3.1, R3.2
- Tests: record→last round-trip; no record when ship errors (caller contract
  asserted in T3); concurrent-safe path naming.
- Covered-by: `internal/snapshot/parent_test.go`

---

## T5 — Archive retention (GFS)

### 5.1 [x] Remote GFS prune
- Files: `internal/snapshot/ship_archive.go`
- After a successful ship: `rclone lsjson` → apply GFS from `[engine.retention]`
  → `rclone deletefile` losers; never delete the active parent.
- EARS: R4, R4.1, R4.2
- Tests (scripted Runner): GFS keeps/deletes correct objects from a sample
  listing; active parent always retained.
- Covered-by: `ship_archive_test.go`

---

## T6 — Restore

### 6.1 [x] restore.go dispatch + chain validation
- Files: `internal/snapshot/restore.go` (new)
- `Restore(ctx,id,target,opts)`: detect driver; archive → validate full+delta
  chain then `rclone cat | zstd -d | btrfs receive`; restic →
  `restic restore --target`; `confirmFunc` gate for destructive.
- EARS: R5, R5.1, R5.2, R5.3, R5.4, R6, R7.2
- Tests: archive chain valid → receive invoked in order; broken chain → refused
  pre-receive; restic single-file restore; confirm denied → no-op.
- Covered-by: `internal/snapshot/restore_test.go`

### 6.2 [x] restore CLI verb
- Files: `cmd/bentoo/snapshot_restore.go` (new)
- Flags `--target`, `--ship`, `--yes`; wires `confirmFunc`; exit code reflects
  outcome.
- EARS: R5, R5.4
- Acceptance: `bentoo snapshot restore <id> --target …` dispatches; without
  `--yes` it prompts (confirmFunc seam in tests).
- Covered-by: `cmd/bentoo/snapshot_restore_test.go`

---

## T7 — Docs + commit

### 7.1 [x] README + CHANGELOG
- Document `restic`/`archive` ship types, incremental/full semantics, restore,
  the secrets policy, and the restic-rescan / chain-integrity notes.
- EARS: (docs for R1, R2, R3, R4, R5, R6, R7)
- Acceptance: README section + CHANGELOG `[Unreleased]` entry present.

### 7.2 [x] Commit (gate)
- `go build ./...`, `go vet ./internal/snapshot/ ./cmd/bentoo/`, suites green.
- Commit direct to `main` (Conventional Commits + Co-Authored-By).
- Acceptance: clean build/vet/tests; single coherent commit.

---

## Quality Gates
- **G1 — Build/vet:** `go build ./...` and `go vet ./internal/snapshot/ ./cmd/bentoo/` clean.
- **G2 — Tests green:** suites pass with mock `Runner`/`mounter`/`parentStore`;
  real send/receive + rclone gated behind `*_live_test.go` + env.
- **G3 — Chain integrity:** tests prove parent recorded only on success and a
  broken chain is refused before `btrfs receive`.
- **G4 — Secrets:** captured argv/logs never contain the restic password or rclone
  secrets.
- **G5 — Mount safety:** transient RO mount is unmounted even on backup error.
- **G6 — Docs:** README + CHANGELOG updated before T7.2.

## Validation (per task)
- `go build ./...` and `go vet ./internal/snapshot/ ./cmd/bentoo/`
- `go test ./internal/snapshot/ ./cmd/bentoo/`; mock-based, no network/btrfs.
- Final: full-suite green + `go vet` clean before T7.2 commit.

## Notes
- T2 (restic) and T3 (archive) are disjoint files with no shared data — a natural
  agent-teams split if parallel execution is enabled at run time.
- Record the parent ONLY after a successful ship; this is the single most
  important correctness invariant in this story (R3.2 / G3).
