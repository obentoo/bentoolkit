---
story: snapshot-notifications
type: feature
scale: standard
version: 1
created: 2026-06-08
---

# Story — Snapshot Notifications

## Context

Story 004 defines a `Notifier` interface and calls it from `Manager.Run` with the
`RunResult`, but the default notifier is a no-op. This story implements real
notifier drivers — **ntfy**, **healthchecks.io**, and a generic **webhook** — and
the `[notify]` config that selects and fans out to them, so a scheduled
`bentoo snapshot run` can report success/failure. Email is deferred to story 008.

Builds on 004 (`Notifier`, `RunResult`, `Manager` hook, `httputil`).
See [.epic/docs/snapshot-manager-proposal.md](../../docs/snapshot-manager-proposal.md) §9.3.

## User Value

A user running scheduled snapshots learns immediately when a backup fails (or
confirms it succeeded) via their phone (ntfy), a dead-man's-switch
(healthchecks.io), or their own automation (webhook) — without scraping logs.

## Requirements (EARS)

### R1 — ntfy driver
- R1.1 WHEN `[notify.ntfy].url` is set THE SYSTEM SHALL POST a message summarizing
  the `RunResult` (status, subvolumes, failed stages) to that ntfy topic URL.
- R1.2 WHEN the run failed THE SYSTEM SHALL set an elevated ntfy priority and a
  failure tag; on success SHALL use a normal priority.
- R1.3 WHEN an auth token is configured THE SYSTEM SHALL send it via the
  `Authorization` header and SHALL never log the token.

### R2 — healthchecks.io driver
- R2.1 WHEN `[notify.healthchecks].ping_url` is set AND the run succeeded THE
  SYSTEM SHALL ping the base URL.
- R2.2 WHEN the run failed THE SYSTEM SHALL ping the `/fail` sub-path of that URL.
- R2.3 WHERE supported THE SYSTEM SHALL optionally ping the `/start` sub-path
  before the run when `notify.healthchecks.start = true`.

### R3 — webhook driver
- R3.1 WHEN `[notify.webhook].url` is set THE SYSTEM SHALL POST a JSON body
  containing the serialized `RunResult` summary.
- R3.2 WHEN custom headers are configured THE SYSTEM SHALL apply them to the
  webhook request.

### R4 — Config + selection
- R4.1 THE SYSTEM SHALL parse a `[notify]` section with `on` (subset of
  `{success, failure}`) plus per-driver sub-tables (`ntfy`, `healthchecks`,
  `webhook`).
- R4.2 WHEN building notifiers THE SYSTEM SHALL construct one notifier per
  configured driver and combine them behind the `Notifier` interface (fan-out).
- R4.3 WHEN `notify.on` does not include the run's outcome THE SYSTEM SHALL skip
  notification entirely.

### R5 — Manager integration
- R5.1 THE `Manager` SHALL invoke the composed notifier exactly once per run with
  the final `RunResult`.
- R5.2 WHEN a notifier call fails THE SYSTEM SHALL log a warning and continue
  (notification errors SHALL NOT change the run's exit code), and SHALL still
  attempt the remaining notifiers.

### R6 — Robustness & security
- R6.1 THE notifiers SHALL use an `http.Client` built on
  `httputil.BuildTransport()` with a bounded timeout and SHALL set a User-Agent.
- R6.2 THE notifiers SHALL bound response bodies via `httputil.MaxBodyBytes` and
  SHALL respect the parent `context.Context` (cancellation).
- R6.3 THE SYSTEM SHALL never write tokens or webhook secrets to logs or errors.

## Acceptance Criteria
- With `notify.ntfy.url` set, a failing run POSTs a high-priority ntfy message and
  a succeeding run POSTs a normal one (verified with a stub HTTP server).
- healthchecks pings base URL on success and `/fail` on failure.
- webhook POSTs the `RunResult` JSON with configured headers.
- `notify.on = ["failure"]` suppresses notifications on success.
- A notifier returning an HTTP error logs a warning but the `run` exit code is
  unchanged; remaining notifiers still fire.
- Tokens never appear in logs/output; `go build ./...` + `go vet` clean; package
  suites pass with stub HTTP servers (no live network).

## Assumptions
- A1: All HTTP notifiers reuse `httputil.BuildTransport()` and a per-call
  `http.Client` (the codebase applies UA/headers per package, not in httputil).
- A2: Notification is best-effort — failures degrade to warnings (R5.2).
- A3: `RunResult` from 004 already carries enough detail (per-stage status) to
  render a useful message; no engine changes are needed here.

## Out of Scope
- Email notifier (→ 008).
- New `RunResult` fields beyond what 004 provides (unless a gap is found, raised
  as a follow-up).
- Cloud/restore drivers (→ 006), snapper (→ 007).

## Dependencies
- Story 004 (Notifier interface, RunResult, Manager hook, httputil). No new Go
  modules (stdlib `net/http`).
