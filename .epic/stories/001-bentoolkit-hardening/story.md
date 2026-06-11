---
story: 001-bentoolkit-hardening
type: feature
scale: full
version: 1
created: 2026-05-17
---

# Story — Bentoolkit Hardening

## 1. Overview

Bentoolkit is a Go CLI for Gentoo overlay maintenance. A deep audit identified **eleven actionable findings** across security, reliability, and performance. This story coordinates remediation of all eleven into a single hardening pass shipped as release **0.2.0**. Design rationale, integration points, and per-recommendation decisions live in [`design.md`](./design.md); this document captures the **what** (requirements) and **acceptance criteria**.

The work is internal hardening. The only externally-observable behavior changes are:
- `${VAR}` expansion in `packages.toml` headers becomes restricted (breaking).
- A new `--concurrency=N` flag is available on `overlay autoupdate` and `overlay compare`.
- Process exit codes follow a documented contract (`0` / `1` / `2`).
- HTTP/2 is enabled by default; `BENTOO_DISABLE_HTTP2=1` opts out.

## 2. Stakeholders

- **Primary**: Gentoo overlay maintainers running `bentoo overlay autoupdate` and `bentoo overlay compare` against large upstream repositories (GitHub, GitLab, git-cloned).
- **Secondary**: CI / cron integrators that parse bentoo exit codes to decide whether to merge an automated PR.
- **Maintainer**: project author (solo).

## 3. Background

The audit, run prior to this story, surfaced:
- **Security**: env-var exfiltration via TOML-supplied headers (C-1); unvalidated `git clone` URL/branch (C-3); world-readable cache files (M-3); LLM-generated regex/XPath cached without validation (C-2/R8); request redirects unrestricted (M-1).
- **Reliability**: SIGINT not interrupting HTTP (R3); `.ebuild` orphans on Apply failure (R5); per-package errors silenced in batch ops (R1, R7); no timeout on git clone / `ebuild manifest` (M-5, R7).
- **Performance**: sequential `CheckAll`/`CompareWithProvider` (P1); untuned HTTP transport on all clients (P2); rate limiter present but uncalled (P3); unbounded `io.ReadAll` on response bodies (P4).

Detailed file:line references are preserved in the audit report (conversation transcript) and in `design.md` §4.

## 4. Requirements (EARS)

### R1 — Header env-var substitution allow-list (security)

> R1.1 — WHEN the SYSTEM expands `${VAR}` inside a header value loaded from `packages.toml`, IT SHALL perform the substitution only if (a) the header name (case-insensitive) is in {`Authorization`, `X-Api-Key`, `X-Auth-Token`, `Private-Token`} AND (b) `VAR` either has prefix `BENTOO_` OR is in the named allow-list {`GITHUB_TOKEN`, `GITLAB_TOKEN`, `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`}.

> R1.2 — IF the substitution is denied by R1.1 THE SYSTEM SHALL emit a `Warn`-level log line identifying the denied variable and header, AND SHALL pass the literal `${VAR}` string as the header value.

> R1.3 — IF an allow-listed env var is unset or empty THE SYSTEM SHALL skip substitution, emit `Warn`, and pass the literal string.

### R2 — Git clone URL and branch validation (security)

> R2.1 — WHEN a git-clone provider is constructed THE SYSTEM SHALL parse the configured `RepoURL` and reject it (`ErrInvalidRepoURL`) IF the scheme is not in {`http`, `https`, `git`, `ssh`}.

> R2.2 — WHEN a git-clone provider is constructed THE SYSTEM SHALL validate the configured branch against a regex compatible with `git check-ref-format`. THE SYSTEM SHALL reject (`ErrInvalidBranch`) branches that contain spaces, control characters, `..`, `@{`, leading `-`, or any of `~ ^ : ? * [ \\`.

> R2.3 — WHEN running `git clone` THE SYSTEM SHALL invoke it via `exec.CommandContext` with a context that expires after `DefaultGitCloneTimeout` (2 minutes) at the latest.

### R3 — Context propagation and signal handling (reliability)

> R3.1 — WHEN the user sends SIGINT or SIGTERM to `bentoo overlay autoupdate` THE SYSTEM SHALL cancel all in-flight HTTP requests and external commands within 2 seconds.

> R3.2 — `Checker` and `Applier` SHALL accept a `context.Context` via a `WithContext(ctx)` option; that context SHALL propagate to every outbound HTTP call, LLM call, and `exec` invocation issued by those types.

> R3.3 — THE SYSTEM SHALL NOT contain unjustified `context.Background()` calls in `internal/autoupdate/` or `internal/overlay/`. Permitted occurrences: test helpers, `init()`, and lines marked `// SAFE: <reason>`.

### R4 — Parallel batch operations (performance)

> R4.1 — WHEN `CheckAll` processes N packages THE SYSTEM SHALL run up to C checks concurrently where C is the configured concurrency.

> R4.2 — `bentoo overlay autoupdate` and `bentoo overlay compare` SHALL accept `--concurrency=N` with default `10` and valid range `[1, 100]`. A value outside the range SHALL cause the command to return a non-zero exit with a clear error message before any package work begins.

> R4.3 — IF a per-package check fails THE SYSTEM SHALL record the failure in `BatchResult.Failures` (keyed by package name) AND continue processing the remaining packages.

> R4.4 — THE `ProgressCallback` SHALL have signature `func(done, total uint64)`. `done` SHALL be incremented via an atomic counter so concurrent goroutines observe a monotonic non-decreasing value.

> R4.5 — Final `BatchResult.Items` SHALL be sorted deterministically by package name regardless of completion order.

### R5 — Applier rollback on Manifest failure (reliability)

> R5.1 — IF `runManifest` returns an error AFTER `copyEbuild` placed a new `.ebuild` file in the overlay THE SYSTEM SHALL remove that file before returning the error.

> R5.2 — IF the orphan removal itself fails THE SYSTEM SHALL emit a `Warn`-level log AND SHALL propagate the **original** `runManifest` error, not the cleanup error.

> R5.3 — THE SYSTEM SHALL execute `ebuild ... manifest` via `exec.CommandContext` with a context that expires after at most 5 minutes.

### R6 — HTTP transport tuning (performance)

> R6.1 — Every outbound `*http.Client` constructed in the codebase SHALL use a tuned `*http.Transport` with at minimum: `MaxIdleConnsPerHost ≥ 16`, `MaxConnsPerHost ≥ 32`, `IdleConnTimeout ≥ 90s`.

> R6.2 — HTTP/2 SHALL be enabled by default. THE SYSTEM SHALL disable HTTP/2 when environment variable `BENTOO_DISABLE_HTTP2` equals `"1"`.

> R6.3 — Transport construction SHALL be sourced from a single helper `internal/common/httputil.BuildTransport()`. No other location in the codebase SHALL construct an `*http.Transport` for outbound traffic.

### R7 — Batch error reporting and exit codes (reliability)

> R7.1 — WHEN `CheckAll` or `AnalyzeAll` completes WITH at least one per-package failure THE SYSTEM SHALL emit one stderr line per failure in the format `ERROR <package>: <error>`.

> R7.2 — `bentoo overlay autoupdate` SHALL exit with code `1` IF at least one package failed AND at least one package was processed successfully.

> R7.3 — `bentoo overlay autoupdate` SHALL exit with code `2` IF no package was successfully processed (total failure or invalid configuration).

> R7.4 — `bentoo overlay autoupdate` SHALL exit with code `0` IF every package was processed successfully.

> R7.5 — Exit codes 0/1/2 SHALL be documented in `README.md` under an "Exit codes" section.

### R8 — LLM-generated pattern and XPath validation (security)

> R8.1 — WHEN persisting an `analysis_cache` entry derived from LLM output THE SYSTEM SHALL validate the `Pattern` field as: (a) a successfully compiled `regexp.Regexp` (RE2), AND (b) length ≤ `MaxPatternLen` (512 characters).

> R8.2 — WHEN persisting an `analysis_cache` entry THE SYSTEM SHALL validate the `XPath` field by successfully parsing it with `htmlquery`.

> R8.3 — IF either validation fails THE SYSTEM SHALL NOT persist the entry AND SHALL return an error to the caller distinguishable by sentinel (`ErrInvalidPattern` / `ErrInvalidXPath`).

> R8.4 — WHEN reading an existing cache entry THE SYSTEM SHALL re-run R8.1 and R8.2 validation. IF either fails THE SYSTEM SHALL drop the entry from the cache AND treat the read as a cache miss.

### R9 — File permissions for cache and logs (security)

> R9.1 — WHEN writing any cache file or apply-log file THE SYSTEM SHALL set permissions to `0600` (owner read/write only).

> R9.2 — IF the underlying filesystem does not support `chmod` (e.g. FAT32, exFAT) THE SYSTEM SHALL emit `Warn` AND continue without failing.

> R9.3 — THE file mode SHALL be sourced from a single constant `internal/common/fileutil.CacheFileMode`. No other location in the codebase SHALL pass a numeric file-mode literal to `os.WriteFile` for cache or log paths.

### R10 — Rate-limit gate on HTTP hot path (performance)

> R10.1 — `Checker.fetchContent` SHALL invoke `RateLimiter.WaitHTTP(ctx, host)` before each HTTP request, where `host` is parsed from the request URL.

> R10.2 — IF the rate-limiter wait is cancelled by context THE SYSTEM SHALL return the context error from `fetchContent` without issuing the HTTP request.

> R10.3 — `RateLimiter` SHALL be injectable via `WithRateLimiter(*RateLimiter)` option on `Checker`. If not injected, `Checker` SHALL initialize a default limiter at 1 request/second per host with burst 5.

### R11 — Response body size cap (reliability)

> R11.1 — `RetryableHTTPClient.GetWithContext` SHALL wrap every response body in `http.MaxBytesReader(nil, body, MaxBodyBytes)` where `MaxBodyBytes = 10 MiB` (sourced from `internal/common/httputil`).

> R11.2 — LLM clients (`llm.go`, `openai.go`, `ollama.go`) SHALL accept a per-client `maxBodyBytes` override. The default SHALL equal R11.1's value.

> R11.3 — IF a response body exceeds the cap THE SYSTEM SHALL return an error wrapping sentinel `ErrResponseTooLarge`.

## 5. Acceptance Criteria

| AC | Source | Verification |
|---|---|---|
| AC-1 | R1.1 | Unit test: header `X-Api-Key: ${BENTOO_FOO}` with `BENTOO_FOO=bar` substitutes; `X-Custom: ${BENTOO_FOO}` does NOT substitute |
| AC-2 | R1.2 / R1.3 | Capture stderr in test; assert `Warn` line is emitted |
| AC-3 | R2.1 / R2.2 | Table test: `file:///etc/passwd` → `ErrInvalidRepoURL`; `--upload-pack=evil` → `ErrInvalidBranch`; `release/1.x` → ok |
| AC-4 | R2.3 | Test with stubbed `execCommand` that sleeps > 2 min; assert ctx-deadline error within timeout |
| AC-5 | R3.1 | Integration test spawns child process, sends SIGINT mid-fetch, asserts exit within 2 s |
| AC-6 | R3.3 | `make audit-ctx` returns 0 |
| AC-7 | R4.1 / R4.5 | Bench: 50 packages × ~100 ms latency, concurrency=10 finishes in ≤ 1.5 s; results sorted by name |
| AC-8 | R4.2 | `bentoo overlay autoupdate --concurrency=0` exits non-zero with clear error |
| AC-9 | R4.3 | Test: 5 packages, force 2 to fail via httptest server; `BatchResult.Items=3`, `BatchResult.Failures` has 2 entries |
| AC-10 | R4.4 | Race-detected test with concurrency=10; assert callback receives monotonic `done` values |
| AC-11 | R5.1 | Test forces `runManifest` to fail; `os.Stat(dstPath)` returns `os.ErrNotExist` |
| AC-12 | R5.2 | Test forces both manifest AND removal to fail; returned error wraps `ErrManifestFailed`, not the rm error |
| AC-13 | R6.1 | Reflective test: every `*http.Client` field in providers + autoupdate uses `httputil.BuildTransport()` |
| AC-14 | R6.2 | Test sets `BENTOO_DISABLE_HTTP2=1`, verifies Transport's `ForceAttemptHTTP2` is false / `TLSNextProto` is empty |
| AC-15 | R7.1 / R7.2 / R7.3 / R7.4 | Integration: cmd exits with documented codes per matrix |
| AC-16 | R8.1 / R8.2 | PBT: generate adversarial regex (e.g. `(a+)+$`); validator rejects |
| AC-17 | R8.4 | Test pre-populates cache with invalid entry; read returns miss; LLM re-invoked |
| AC-18 | R9.1 / R9.3 | Test creates cache; `os.Stat(file).Mode() & 0777 == 0600` |
| AC-19 | R10.1 / R10.2 | Test injects mock limiter that blocks; cancel ctx; assert `fetchContent` returns ctx err without hitting test server |
| AC-20 | R11.1 / R11.3 | Test responds with 11 MiB body; client returns error wrapping `ErrResponseTooLarge` |

## 6. Unchanged Behavior

For a hardening story touching 12+ files, explicit listing of preserved behavior reduces risk of regression:

1. `bentoo overlay manifest`, `bentoo overlay commit`, `bentoo overlay sync`, `bentoo overlay add`, `bentoo overlay rename`, `bentoo overlay status`, `bentoo overlay log`, `bentoo overlay push` SHALL behave identically to v0.1.11 (except for shared infrastructure changes: HTTP transport, file modes, ctx propagation).
2. `bentoo overlay analyze` SHALL retain `maxConcurrent = 3` for AI calls (NOT bound to the new `--concurrency` flag).
3. `packages.toml` parsing SHALL remain backward-compatible for all fields except `headers` containing `${VAR}` patterns covered by R1.
4. Cache file **format** SHALL NOT change (only validation on load and mode `0600`).
5. The CLI flag set on existing commands SHALL NOT change except for the additions in R4.2.
6. `gobreaker` circuit-breaker remains a single global instance per HTTP client (per-host breaker explicitly deferred — see §7).
7. `analyzer.go` AI concurrency SHALL remain at 3 (kept independent of `--concurrency`).
8. Configuration discovery order, default paths (`~/.config/bentoo/`), and TOML schema (excluding R1) SHALL remain identical.

## 7. Out of Scope

- **Per-host circuit breaker** — deferred to follow-up story `002-` (the global breaker stays; cascade risk is documented).
- **Provider abstraction redesign** (e.g. migrating `GitHubProvider` onto `RetryableHTTPClient`) — only transport tuning is shared via `httputil`.
- **LLM provider refactor** (Anthropic/OpenAI/Ollama clients keep current structure; only `Transport` and body cap change).
- **Cache eviction policy** — TTLs (24 h for GitHub/GitLab, 1 h for autoupdate, 24 h for analysis) remain as-is; only schema validation added (R8).
- **Configuration of rate-limit values via flag or TOML** — defaults are conservative; no user-facing knob added in this story.
- **`AnalyzeAll` concurrency tuning** — stays at 3 (cost concern).
- **`bentoo overlay analyze` UX redesign** — out of scope.
- **Replacing `gobreaker`** — kept.
- **`--allow-legacy-env-subst` escape hatch** for R1 — rejected per AD-8.

## 8. Definition of Done

A merge of this story to `main` is considered done when ALL of the following hold:

1. Every requirement R1–R11 has at least one corresponding test in the suite.
2. `go test -race ./...` is green.
3. Aggregate test coverage is ≥ 80% (CI gate).
4. `golangci-lint run` is green; `.golangci.yml` re-enables `G204` per AD-9.
5. `govulncheck ./...` reports no findings outside the documented stdlib exclusion.
6. `make audit-ctx` reports 0 violations.
7. `CHANGELOG.md` has a complete `[0.2.0]` block with `### Added` / `### Changed` / `### Security` / `### Fixed` sections.
8. `README.md` includes new sections: "Exit codes", "Concurrency", "Headers and environment variables", "HTTP/2 opt-out", "Filesystem assumptions".
9. The release tag `v0.2.0` is created and built artifacts verified via existing `make` targets.
10. The benchmark added per AC-7 records ≥ 4× wall-clock speedup over a sequential baseline with 50 simulated packages.

## 9. Glossary

- **Allow-list (env-var)** — explicit set of variable names + a `BENTOO_*` prefix permitted for substitution in header values.
- **BatchResult** — generic return type carrying both successes and per-item failures from a batch operation.
- **CompareWithProvider** — function in `internal/overlay/compare.go` that diffs local overlay against an upstream provider.
- **CheckAll** — function in `internal/autoupdate/checker.go` that polls each configured package for new versions.
- **httputil** — new internal package centralizing transport construction and body-size constants.
- **Orphan ebuild** — `.ebuild` file copied to overlay during Apply but not registered in `Manifest` because manifest generation failed.

## 10. References

- [`design.md`](./design.md) — design rationale, architectural decisions (AD-1 through AD-13), file map, migration table, risks.
- Audit report — in conversation transcript (prior turns).
- Architect sub-agent findings — captured in `.draft/meta.yaml`.
- Analyst sub-agent completeness checklist — captured in `.draft/meta.yaml`.
- EARS notation guide — `https://alistairmavin.com/ears/`.
