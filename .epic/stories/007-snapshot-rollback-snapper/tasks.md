---
story: snapshot-rollback-snapper
type: feature
scale: standard
version: 1
created: 2026-06-08
---

# Tasks — Snapshot Rollback (snapper engine)

Sequencing: T1 (snapper engine + config + detect) → T2 (rollback) → T3 (emerge
hook command) → T4 (docs+commit). T2/T3 depend on T1. All build on the 004
`Engine`/factory/`Runner`/`confirmFunc`.

Test policy (Standard): sub-tasks carry `Tests` (Red-first at run time) via a
scripted `Runner` — no real snapper/btrfs. Real snapper gated behind
`*_live_test.go` + env skip. `Covered-by` points at the test file.

---

## T1 — snapper engine driver

### 1.1 [x] Driver: Create/Prune/List + factory case
- Files: `internal/snapshot/engine_snapper.go` (new); `engine.go` factory
- `snapperEngine` implementing `Engine`; `case "snapper"` in `newEngine`;
  `Create`→`snapper create`, `List`→parse `snapper list`, `Prune`→snapper cleanup
  from `[engine.retention]`.
- EARS: R1, R1.1, R1.2, R1.3, R1.4, R6, R6.1, R6.2
- Tests (scripted Runner): create argv carries config + description; list parses
  sample output into `[]Snapshot`; prune maps retention; non-zero → wrapped error.
- Covered-by: `internal/snapshot/engine_snapper_test.go`

### 1.2 [x] snapper config rendering + detect
- Files: `internal/snapshot/engine_snapper.go`, `detect.go`
- Ensure `/etc/snapper/configs/<name>` idempotently (managed keys only);
  `snapper`→`app-backup/snapper` in detect.
- EARS: R2, R2.1, R2.2, R5, R5.1, R5.2
- Tests: render managed keys; re-apply preserves unmanaged keys; missing snapper →
  actionable error naming the pkg.
- Covered-by: `engine_snapper_test.go`, `detect_test.go`

---

## T2 — Rollback

### 2.1 [x] rollback command + confirm + engine guard
- Files: `cmd/bentoo/snapshot_rollback.go` (new); helper in `internal/snapshot`
- `snapper rollback <id>` behind `--yes`/`confirmFunc`; refuse when active engine
  is not snapper.
- EARS: R3, R3.1, R3.2, R3.3, R6, R6.1
- Tests: confirm granted → rollback invoked; denied → no-op; non-snapper engine →
  refused with clear error.
- Covered-by: `cmd/bentoo/snapshot_rollback_test.go`

---

## T3 — Opt-in emerge hook

### 3.1 [x] `hook --install/--uninstall`
- Files: `cmd/bentoo/snapshot_hook.go` (new)
- Install/remove a Portage hook (pre/post `emerge` snapshot); idempotent; never
  triggered by `apply`.
- EARS: R4, R4.1, R4.2, R4.3
- Tests: install writes the hook file (temp root); uninstall removes it; `apply`
  path does not call install (guard asserted).
- Covered-by: `cmd/bentoo/snapshot_hook_test.go`

---

## T4 — Docs + commit

### 4.1 [x] README + CHANGELOG
- Document `engine.driver = snapper`, `rollback`, the opt-in `hook` command, and
  that rollback is snapper-specific; note grub-btrfs as a future follow-up.
- EARS: (docs for R1, R2, R3, R4, R5, R6)
- Acceptance: README section + CHANGELOG `[Unreleased]` entry present.

### 4.2 [x] Commit (gate)
- `go build ./...`, `go vet ./internal/snapshot/ ./cmd/bentoo/`, suites green.
- Commit direct to `main` (Conventional Commits + Co-Authored-By).
- Acceptance: clean build/vet/tests; single coherent commit.

---

## Quality Gates
- **G1 — Build/vet:** `go build ./...` and `go vet ./internal/snapshot/ ./cmd/bentoo/` clean.
- **G2 — Tests green:** suites pass with scripted `Runner`; real snapper gated
  behind `*_live_test.go` + env.
- **G3 — Destructive safety:** rollback requires `--yes`/confirm and is refused for
  non-snapper engines (asserted).
- **G4 — No surprise side effects:** `apply` never installs the emerge hook
  (asserted).
- **G5 — Additive:** existing btrbk-engine tests unchanged and green.
- **G6 — Docs:** README + CHANGELOG updated before T4.2.

## Validation (per task)
- `go build ./...` and `go vet ./internal/snapshot/ ./cmd/bentoo/`
- `go test ./internal/snapshot/ ./cmd/bentoo/`; scripted Runner, no real snapper.
- Final: full-suite green + `go vet` clean before T4.2 commit.

## Notes
- Rollback and the emerge hook are the only destructive/side-effecting paths here;
  both are gated (confirm / explicit command) by design.
