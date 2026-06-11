---
story: snapshot-packaging-polish
type: feature
scale: standard
version: 1
created: 2026-06-08
---

# Tasks — Snapshot Packaging & Polish

Sequencing: T1 (email) and T2 (dry-run) are independent; T3 (prune) depends on the
006 archive GFS + engine prune; T4 (ebuild) depends on the `detect` map being
complete (004/006/007); T5 (status/list polish); T6 (docs+commit) last.

Test policy (Standard): sub-tasks carry `Tests` (Red-first at run time) using
scripted seams / stub transports — no live mail/network/btrfs. `Covered-by` points
at the test file.

---

## T1 — Email notifier

### 1.1 [x] email notifier + compose
- Files: `internal/snapshot/notify.go` (extend); `config.go` (`[notify.email]`)
- `emailNotifier` (local `sendmail` via exec seam, or SMTP via `net/smtp`);
  plugs into the 005 `multiNotifier`; respects `notify.on`.
- EARS: R1, R1.1, R1.2, R1.3
- Tests: stub transport receives the summary; `on` filter respected; SMTP
  credentials never appear in logs/argv.
- Covered-by: `internal/snapshot/notify_test.go`

---

## T2 — Full `--dry-run`

### 2.1 [x] dry-run across verbs
- Files: `cmd/bentoo/snapshot_apply.go`, `snapshot_run.go`, `snapshot_restore.go`,
  `snapshot_rollback.go`, `internal/snapshot` (thread a `DryRun bool`)
- `apply` prints would-write configs/units; `run` prints would-run commands;
  `restore`/`rollback` print destructive actions — all without side effects.
- EARS: R2, R2.1, R2.2, R2.3
- Tests: each verb in dry-run makes zero Runner/FS calls (spy asserts no writes/
  exec) and prints the plan.
- Covered-by: `cmd/bentoo/snapshot_apply_test.go`, `snapshot_run_test.go`, others

---

## T3 — Manual `prune` verb

### 3.1 [x] prune command
- Files: `cmd/bentoo/snapshot_prune.go` (new)
- Apply `[engine.retention]` on demand: engine-native prune + archive GFS;
  honors `--dry-run` and `--ship`.
- EARS: R3, R3.1, R3.2
- Tests: prune invokes engine prune + archive GFS for the selected ships; dry-run
  performs nothing; `--ship` scopes to one destination.
- Covered-by: `cmd/bentoo/snapshot_prune_test.go`

---

## T4 — Ebuild USE flags

### 4.1 [x] IUSE + RDEPEND mapping + detect end-to-end
- Files: ebuild in the bentoo overlay (`app-admin/bentoolkit` or current location),
  metadata.xml
- `IUSE="btrbk snapper restic rclone systemd"`; `RDEPEND` conditional deps;
  validate that an active config needing an absent backend errors via `detect`.
- EARS: R4, R4.1, R4.2
- Tests: ebuild metadata lints (repoman/pkgcheck if available); a Go test asserts
  `detect` errors actionably when a backend is missing for the active config.
- Covered-by: `internal/snapshot/detect_test.go` + overlay QA

---

## T5 — Status / list polish

### 5.1 [x] status timers + space; list --remote
- Files: `cmd/bentoo/snapshot_status.go`, `snapshot_list.go`
- `status`: timer state (`systemctl --list-timers`/`is-enabled`) + last RunResult +
  free space; `list --remote`: include btrbk-target + restic snapshots.
- EARS: R5, R5.1, R5.2
- Tests: status renders timer state + last run + space from scripted Runner;
  `list --remote` merges remote snapshots from mock engine/shipper.
- Covered-by: `cmd/bentoo/snapshot_status_test.go`, `snapshot_list_test.go`

---

## T6 — Docs + commit

### 6.1 [x] README + CHANGELOG (feature complete)
- Full `snapshot.toml` schema, all verbs, USE flags, quick-start; CHANGELOG records
  the completed feature.
- EARS: R6, R6.1, R6.2
- Acceptance: README snapshot section complete; CHANGELOG `[Unreleased]` entry.

### 6.2 [x] Commit (gate)
- `go build ./...`, `go vet ./internal/snapshot/ ./cmd/bentoo/`, suites green;
  overlay QA clean.
- Commit direct to `main` (Conventional Commits + Co-Authored-By).
- Acceptance: clean build/vet/tests; single coherent commit.

---

## Quality Gates
- **G1 — Build/vet:** `go build ./...` and `go vet ./internal/snapshot/ ./cmd/bentoo/` clean.
- **G2 — Tests green:** suites pass with stub transports/scripted seams; no live
  mail/network/btrfs.
- **G3 — Dry-run safety:** every verb in `--dry-run` performs zero side effects
  (asserted via spies).
- **G4 — USE-flag correctness:** each flag toggles the right RDEPEND; absent backend
  for an active config errors actionably.
- **G5 — Secrets:** SMTP credentials never logged.
- **G6 — Docs:** README + CHANGELOG complete before T6.2.

## Validation (per task)
- `go build ./...` and `go vet ./internal/snapshot/ ./cmd/bentoo/`
- `go test ./internal/snapshot/ ./cmd/bentoo/`; stub transports, no network.
- Overlay QA (repoman/pkgcheck) for the ebuild where available.
- Final: full-suite green + `go vet` clean before T6.2 commit.

## Notes
- `--dry-run` was stubbed in 004; this story makes it real and is the highest-value
  safety polish — assert zero side effects per verb.
- The ebuild change is the only task touching the overlay rather than Go code.
