---
story: snapshot-notifications
type: feature
scale: standard
version: 1
created: 2026-06-08
---

# Tasks — Snapshot Notifications

Sequencing: T1 (config) → T2 (drivers) → T3 (compose + manager wiring) → T4
(docs + commit). T2 depends on T1; T3 depends on T2 and on the 004 `Manager` hook.

Test policy (Standard): each driver/integration sub-task carries a `Tests` field
(authored Red-first at run time) using a stub `httptest.Server` — no live network.
`Covered-by` points at the test file.

---

## T1 — `[notify]` config + selection

### 1.1 [x] Notify config structs
- Files: `internal/snapshot/config.go` (extend the `NotifyConfig` placeholder from 004)
- `NotifyConfig{On []string, Ntfy, Healthchecks, Webhook}` sub-tables with
  `toml:"...,omitempty"`.
- EARS: R4, R4.1
- Tests: parse `[notify]` with `on` + each sub-table → populated; absent → zero.
- Covered-by: `internal/snapshot/config_test.go`

### 1.2 [x] `on` outcome filter
- Helper `shouldNotify(on []string, failed bool) bool`.
- EARS: R4.3
- Tests: `on=["failure"]` + success → false; + failure → true; empty `on` →
  decide default (document: notify on failure only) and assert.
- Covered-by: `config_test.go`

---

## T2 — Notifier drivers

### 2.1 [x] Shared HTTP helper
- Files: `internal/snapshot/notify.go` (new)
- `notifierClient()` → `&http.Client{Transport: httputil.BuildTransport(),
  Timeout: …}`; UA constant; bounded body read via `httputil.MaxBodyBytes`.
- EARS: R6, R6.1, R6.2
- Tests: client uses bounded body; context cancellation aborts (stub server that
  blocks).
- Covered-by: `internal/snapshot/notify_test.go`

### 2.2 [x] ntfy driver
- `ntfyNotifier{url, token}` implementing `Notifier`; priority/tags by outcome;
  token via `Authorization` header.
- EARS: R1, R1.1, R1.2, R1.3, R6.3
- Tests (stub server): success → normal priority body; failure → high priority +
  tag; token sent in header and absent from any log/error string.
- Covered-by: `notify_test.go`

### 2.3 [x] healthchecks driver
- `healthchecksNotifier{pingURL, start}`; success → base, failure → `/fail`,
  optional `/start`.
- EARS: R2, R2.1, R2.2, R2.3
- Tests (stub server): success hits base; failure hits `/fail`; `start=true`
  pings `/start`.
- Covered-by: `notify_test.go`

### 2.4 [x] webhook driver
- `webhookNotifier{url, headers}`; POST JSON `RunResult` summary + custom headers.
- EARS: R3, R3.1, R3.2
- Tests (stub server): body is the serialized RunResult; custom headers applied.
- Covered-by: `notify_test.go`

---

## T3 — Compose + Manager wiring

### 3.1 [x] Composite notifier + factory
- Files: `internal/snapshot/notify.go`
- `newNotifier(NotifyConfig)` returns a `multiNotifier` fanning out to each
  configured driver; outcome filter applied; per-notifier errors collected as
  warnings, not fatal.
- EARS: R4.2, R5, R5.2
- Tests: two drivers configured → both invoked; one returns error → warning +
  other still called; `on` filter suppresses both.
- Covered-by: `notify_test.go`

### 3.2 [x] Manager invokes composed notifier
- Files: `internal/snapshot/manager.go` (replace 004 no-op default with
  `newNotifier(cfg.Notify)`)
- EARS: R5, R5.1
- Tests: `Manager.Run` calls notifier once with the final RunResult; notifier
  failure does not change run exit/error.
- Covered-by: `internal/snapshot/manager_test.go`

---

## T4 — Docs + commit

### 4.1 [x] README + CHANGELOG
- Document `[notify]` (`on`, ntfy/healthchecks/webhook), the best-effort
  semantics, and the secrets-never-logged guarantee.
- EARS: (docs for R1, R2, R3, R4, R5, R6)
- Acceptance: README section + CHANGELOG `[Unreleased]` entry present.

### 4.2 [x] Commit (gate)
- `go build ./...`, `go vet ./internal/snapshot/`, suites green.
- Commit direct to `main` (Conventional Commits + Co-Authored-By).
- Acceptance: clean build/vet/tests; single coherent commit.

---

## Quality Gates
- **G1 — Build/vet:** `go build ./...` and `go vet ./internal/snapshot/` clean.
- **G2 — Tests green:** `internal/snapshot` suite passes using `httptest`
  stubs (no live network).
- **G3 — Best-effort proven:** a test makes a notifier return an error and asserts
  the run exit code is unchanged and remaining notifiers still fire.
- **G4 — Secrets:** no test/log output contains a token or webhook secret.
- **G5 — Docs:** README + CHANGELOG updated before the T4.2 commit.

## Validation (per task)
- `go build ./...` and `go vet ./internal/snapshot/`
- `go test ./internal/snapshot/` with `httptest` stubs; no network.
- Final: full-suite green + `go vet` clean before T4.2 commit.

## Notes
- Reuses `httputil.BuildTransport()`; UA/headers are applied per-call (codebase
  convention — httputil does not own the client).
- If rendering a useful message reveals a missing `RunResult` field, raise it as a
  small follow-up against 004 rather than expanding scope here.
