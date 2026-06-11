---
story: autoupdate-integration-fixes
type: bugfix
scale: standard
version: 1
created: 2026-05-19
---

# Autoupdate Integration Fixes

## Summary

The `bentoo overlay autoupdate` subsystem ships four integration gaps where capabilities exist in the code but never reach the user. Story `001-bentoolkit-hardening` built the underlying infrastructure (`WithApplierContext`, per-host rate limiting, body cap, `AutoupdateConfig` schema). This story completes the wiring on the CLI side and aligns the documentation with the actual behaviour. All four gaps are bugs in the strict sense: the codebase advertises behaviour it does not deliver.

## Bug Analysis

### A. SIGINT/SIGTERM does not cancel `--apply`

- **Symptom.** Pressing Ctrl-C during `bentoo overlay autoupdate --apply <pkg>` does not interrupt the running `ebuild manifest` (or `sudo ebuild ... compile`) child process. The user must wait for the 5-minute manifest timeout or kill the process manually.
- **Evidence.** `cmd/bentoo/overlay_autoupdate.go:89` builds a `signalContext`, but `runAutoupdate` calls `runApply(overlayPath, configDir, autoupdateApply)` at line 99 — without the context. `runApply` (line 258) calls `NewApplier(overlayPath, configDir)` without `WithApplierContext`, so the applier's `ctx` field defaults to `context.Background()`. `exec.CommandContext` therefore receives a context that never cancels.
- **Root cause.** Story 001 added `WithApplierContext` and threaded the spawned commands through `exec.CommandContext`, but the CLI integration was never completed — the final wire is missing.
- **Divergence.** `CHANGELOG.md` 0.2.0 states "SIGINT/SIGTERM now cancels in-flight HTTP requests and child processes." This holds for `--check` and fails for `--apply`.

### B. `autoupdate.cache_ttl` is ignored

- **Symptom.** Users who set `autoupdate.cache_ttl` in `~/.config/bentoo/config.yaml` find that the autoupdate cache still expires at the hardcoded one-hour default.
- **Evidence.** `internal/common/config/config.go` defines `AutoupdateConfig.CacheTTL` with `GetCacheTTL()` (default 3600 s). `cmd/bentoo/overlay_autoupdate.go::runCheck` (line 108) constructs `NewChecker` with only `WithConfigDir`, `WithContext`, and `WithConcurrency`. `internal/autoupdate/checker.go::NewChecker` then builds a default `NewCache(configDir)` which uses `DefaultCacheTTL = time.Hour`.
- **Root cause.** The configuration schema and the consumer were never connected.

### C. The pending list never clears after a successful `--apply`

- **Symptom.** `bentoo overlay autoupdate --list` keeps showing packages that have already been applied successfully. `pending.json` grows monotonically over time.
- **Evidence.** `internal/autoupdate/applier.go::Apply` sets the status to `StatusValidated` on success (line 220) and returns; it never calls `pending.Delete`. The `StatusApplied` constant in `pending.go:38` is defined but never assigned anywhere in the codebase.
- **Root cause.** The "what happens after success" lifecycle was not finished. `pending.json` was treated as a queue at write time and a permanent log at read time — inconsistent.

### D. `llm_prompt` is documented as part of `--check` but is inoperative there

- **Symptom.** The README documents `llm_prompt` as an autoupdate field for LLM-assisted version extraction, but configuring it has no effect on `--check`.
- **Evidence.** `internal/autoupdate/checker.go::fetchUpstreamVersion` (line 470) only invokes the LLM when `c.llmClient != nil && cfg.LLMPrompt != ""`. `c.llmClient` is set only via `WithLLMClient`, which is never called in production (only in tests). `NewChecker` does not initialise a default LLM. The result: a user-facing field that does nothing on the documented code path.
- **Root cause.** Story 001 evolved the LLM provider implementation for `analyze`, but the legacy `Checker.llmClient` plumbing was never wired through the CLI for autoupdate. Closing that gap properly is out of scope here (see Decisions). Honesty in documentation is in scope.

## Requirements

### R1. Signal cancellation in `--apply`

- **R1.1.** WHEN the user sends SIGINT or SIGTERM during `bentoo overlay autoupdate --apply <pkg>` while the `ebuild manifest` child process is running, THE SYSTEM SHALL terminate the child process and return a non-zero exit code within 2 seconds of receiving the signal.
- **R1.2.** WHEN the user sends SIGINT or SIGTERM during `bentoo overlay autoupdate --apply <pkg> --compile` while the privilege-elevated compile process is running, THE SYSTEM SHALL terminate the child process and return a non-zero exit code within 2 seconds.
- **R1.3.** WHEN an apply operation is interrupted after `copyEbuild` has succeeded but before completion, THE SYSTEM SHALL remove the orphan `.ebuild` file via the existing rollback path so the overlay is not left half-applied.

### R2. Cache TTL honours user configuration

- **R2.1.** WHEN `~/.config/bentoo/config.yaml` defines a positive integer `autoupdate.cache_ttl` (in seconds), `bentoo overlay autoupdate --check` SHALL apply that TTL to all cache entries it consults and writes during the run.
- **R2.2.** WHEN `autoupdate.cache_ttl` is absent, zero, or negative, THE SYSTEM SHALL fall back to the default of 3600 seconds — no observable behaviour change versus today.
- **R2.3.** The configured TTL SHALL apply at evaluation time. Entries already on disk with an older `Timestamp` SHALL be re-evaluated against the current TTL on every `Get` (no migration is needed because `Cache.isExpired` recomputes `age` from `Timestamp` and the current TTL).

### R3. Pending list lifecycle after `--apply`

- **R3.1.** WHEN `--apply <pkg>` completes the full success path (copy + manifest, plus compile when `--compile` is set), THE SYSTEM SHALL delete `pkg` from `pending.json`.
- **R3.2.** WHEN `--apply <pkg>` fails at any step before final success, the pending entry SHALL remain in `pending.json` with status `failed` and the error string set, so retries are possible.
- **R3.3.** WHILE the pending file is being mutated, every write SHALL remain atomic via the existing temp-file + rename pattern with mode `0600`.
- **R3.4.** IF the `pending.Delete` call itself fails after a successful apply, THE SYSTEM SHALL log a Warn-level diagnostic but SHALL NOT downgrade the exit code or flip `result.Success` — the apply itself succeeded.

### R4. `llm_prompt` documentation and runtime alignment

- **R4.1.** THE README SHALL state explicitly that `llm_prompt` in `packages.toml` is consumed by `bentoo overlay analyze` and is NOT consulted by `bentoo overlay autoupdate --check`.
- **R4.2.** WHEN `--check` runs against a `packages.toml` that contains one or more packages with a non-empty `llm_prompt`, THE SYSTEM SHALL emit a Warn-level diagnostic per affected package, exactly once per `Checker` instance, identifying the package and stating that the LLM is not wired into the check path.
- **R4.3.** The `LLMPrompt` field in `PackageConfig` SHALL be retained so that existing `packages.toml` files load without errors and remain consumable by the `analyze` command.

## Decisions

- **C1 chosen over C2.** `Apply` deletes the package from `pending.json` on full success. `StatusApplied` remains a defined constant for backward compatibility with any external readers of `pending.json`, but is not assigned by this code path.
- **D-a chosen.** Field retained; documentation corrected; runtime Warn emitted when the field is set but unused.
- **B-i chosen.** A new `WithCacheTTL(time.Duration)` checker option is added; `runCheck` reads the configured TTL and passes it through (versus building the `Cache` externally and injecting via the existing `WithCache`).
- **Deferred:** wiring the LLM into `Checker` (item #1 / D2), evaluating the orphan `version_history` subsystem (item #4), and replacing the byte-for-byte `copyEbuild` with a structurally-aware bump that handles `EGIT_COMMIT` / commit-hash snapshots (item #5). Each is a candidate for a dedicated story.

## Unchanged Behavior

This is a bugfix story; the following observable behaviour SHALL remain identical after the changes land.

- **U1.** `--check` SHALL CONTINUE TO run packages concurrently bounded by `--concurrency` (range `[1,100]`, default 10), honour per-host rate limiting (6 s / host), apply the 10 MiB body cap on every HTTP response, and enforce the env-var header allow-list.
- **U2.** `--check` SHALL CONTINUE TO exit `0` (all-ok), `1` (partial), or `2` (total failure), as documented in the README.
- **U3.** `--apply` SHALL CONTINUE TO copy the source `.ebuild` byte-for-byte, run `ebuild manifest` under the existing 5-minute timeout, prompt for confirmation before `--compile`, and write compile logs at mode `0600`.
- **U4.** `--list` SHALL CONTINUE TO render `pending`, `validated`, `failed`, and `applied` statuses with their existing colours and field layout. After this story, `applied` will not be produced by normal flows under C1, but the renderer keeps the case so any existing on-disk entries continue to display correctly.
- **U5.** The `analyze` command path (LLM provider selection, schema generation, validator, fallback chain, analysis cache) SHALL CONTINUE TO function exactly as today; this story does not touch it.
- **U6.** Cache and pending files SHALL CONTINUE TO be written atomically (temp + rename + chmod) at mode `0600`.

## Out of Scope

- **D2** — wiring the LLM into the `Checker` so `llm_prompt` works during `--check`. Would require refactoring `Checker.llmClient` (currently the concrete legacy `*LLMClient`) to the `LLMProvider` interface so non-Claude providers (OpenAI, Ollama) work. Candidate for a future story.
- **#4** — deciding the fate of the orphan `version_history` subsystem (~1.2 k lines including tests, no production consumer). Connect (`--check` considers version history) or remove. Candidate for a future story.
- **#5** — replacing the naïve `copyEbuild` byte copy with structurally-aware bumping (commit hashes, `EGIT_COMMIT`, KEYWORDS revbump). Architectural; warrants joint design with the existing `ebuild-bumper` sub-agent. Candidate for a future story.

## Acceptance Criteria

- All requirements `R1`-`R4` are covered by automated tests (unit and/or integration).
- `go test -race ./...` is clean on the repo.
- `golangci-lint run` is clean against the project's existing linter config.
- README diff matches the wording changes implied by R4.1.
- CHANGELOG is updated under `[Unreleased]` documenting the four fixes.
- No regression in the 001-hardening test surface (header allow-list, body cap, rate limiter, BatchResult exit codes, orphan rollback).

## References

- `.epic/stories/001-bentoolkit-hardening/` — predecessor; this story completes wiring left disconnected by 001.
- `CHANGELOG.md` v0.2.0 — claims this story makes truthful for the `--apply` path.
- `internal/autoupdate/applier.go` — `Apply()` and `WithApplierContext` already exist; only the CLI wire is missing.
- `internal/common/config/config.go` — `AutoupdateConfig.CacheTTL` and `GetCacheTTL()` already exist; only the consumer is missing.
- `internal/autoupdate/checker.go::fetchUpstreamVersion` — site of the dead LLM branch (`llmClient != nil` never holds under the CLI).
