---
story: 001-bentoolkit-hardening
type: feature
scale: full
version: 1
created: 2026-05-17
---

# Design — Bentoolkit Hardening

## 1. Context

Bentoolkit is a Go 1.25 CLI for Gentoo overlay maintenance (~14.8k LoC production, ~34k LoC tests). A deep audit identified 11 actionable findings across three axes — **security**, **reliability**, **performance** — concentrated in `internal/autoupdate/` and `internal/common/provider/`. This story consolidates all eleven into one coordinated hardening pass targeting release **0.2.0**.

The work is Design-First because nine of eleven items are NFR-class (no user-visible behavior change except concurrency + breaking change to `${VAR}` expansion in `packages.toml` headers).

**Source of requirements**: in-session audit report (see `story.md` §References).

**Out of scope** (explicit non-goals):
- Per-host circuit breaker — deferred to follow-up story `002-`.
- Provider abstraction redesign (e.g., migrating `GitHubProvider` onto `RetryableHTTPClient`) — additive tuning only.
- LLM provider refactor (Anthropic/OpenAI/Ollama clients keep current structure).
- Cache eviction policy redesign.
- Replacing `gobreaker` with another circuit-breaker library.

## 2. Goals

| ID | Goal | Measure |
|---|---|---|
| G1 | No secret exfiltration via user-supplied config | Static review + allow-list test |
| G2 | SIGINT/SIGTERM aborts in-flight HTTP within 2s | Race-detected integration test |
| G3 | `bentoo overlay autoupdate` over 100 packages completes in ≤1/8th of sequential baseline | Bench in `checker_bench_test.go` |
| G4 | No `.ebuild` left in overlay when `runManifest` fails | Test `applier_test.go::TestRollback_*` |
| G5 | Coverage gate ≥ 80% maintained on every commit | CI |
| G6 | Zero new govulncheck findings; existing exclusions reviewed | CI |
| G7 | Per-item failures in batch ops surface to user with non-zero exit when partial-failed | Documented contract + tests |

## 3. Architectural Decisions

### AD-1 — Single context spine (resolves R3)

A single `context.Context` originates in `cmd/bentoo/overlay_autoupdate.go` via `signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)` (mirroring the established pattern at `cmd/bentoo/overlay_manifest.go:119-122`) and threads through:

```
cmd.Context()
   │
   ▼
NewChecker(overlayPath, WithContext(ctx), ...)
   │  (stored on Checker.ctx)
   ▼
Checker.CheckPackage(pkg, force)
   │
   ▼
Checker.fetchContent(url)  ─── uses c.ctx (not Background())
   │
   ▼
RetryableHTTPClient.GetWithContext(c.ctx, url)
```

**Rule**: after this story, the only `context.Background()` call permitted in `internal/autoupdate/` is in `init()` or test helpers. Audit checklist:
- `checker.go:385` — remove `WithTimeout(Background(), 30s)`, use `c.ctx` + per-op `WithTimeout(c.ctx, c.opTimeout)`.
- `analyzer.go:311, 324, 349` — same change.
- `httpclient.go:364` — already accepts ctx; verify no internal `Background()` fallback.

**Deprecation policy for legacy callers**: zero. `Checker` is internal to this repo; the option is additive (default `Background()` if not passed). No public API breaks. Public packages remain unchanged.

### AD-2 — Configuration surface convergence

Three new tunables land in this story. Each gets exactly one home:

| Knob | Home | Why |
|---|---|---|
| `Transport`, `BreakerSettings` | `RetryConfig` in `httpclient.go` | Already the config carrier for this client; extend, don't add ad-hoc setters |
| `RateLimiter`, `Concurrency`, `Context`, `OperationTimeout` | `CheckerOption` in `checker.go` | Orchestration-level, not transport-level |
| Env-var allow-list (`AllowedHeaderEnvVars`, `AllowedHeaderNames`) | Package-private constants in `httpclient.go` | Local to `SubstituteEnvVars`; not user-tunable for now |
| `GitCloneTimeout`, `AllowedBranchPattern` | Package-level constants in `internal/common/provider` | Provider-level concern |

**Anti-rule**: do not introduce a new top-level `Config` struct that fans in everything. Existing factoring is sufficient.

### AD-3 — HTTP client duality: shared transport helper

Two HTTP clients are tuned:
- `internal/autoupdate/RetryableHTTPClient` — used by autoupdate flow.
- `internal/common/provider/{GitHubProvider, GitLabProvider}.HTTPClient` — used by overlay/compare flow.

Rather than refactor providers onto `RetryableHTTPClient` (scope creep), we introduce a **shared transport builder** in a new internal package:

**New file**: `internal/common/httputil/transport.go`

```go
package httputil

// BuildTransport returns a tuned *http.Transport for outbound CLI traffic.
// HTTP/2 is enabled unless BENTOO_DISABLE_HTTP2=1 in the environment.
func BuildTransport() *http.Transport { ... }

// MaxBodyBytes is the default cap for response body reads.
const MaxBodyBytes = 10 * 1024 * 1024
```

Both clients construct their `http.Client` with `Transport: httputil.BuildTransport()`. Tuning lives in one place. HTTP/2 opt-out via env var addresses the corp-MITM-proxy risk flagged by Analyst.

### AD-4 — Error aggregation & exit-code contract

Pre-requisite for R7. Define a single shape for batch operations:

```go
// in internal/autoupdate
type BatchResult[T any] struct {
    Items    []T
    Failures map[string]error  // package name → error
}

func (b BatchResult[T]) ExitCode() int {
    switch {
    case len(b.Failures) == 0:                       return 0
    case len(b.Items) == 0 && len(b.Failures) > 0:   return 2  // total failure
    default:                                         return 1  // partial failure
    }
}
```

`CheckAll` and `AnalyzeAll` return `BatchResult[CheckResult]` / `BatchResult[AnalysisResult]`. `cmd/bentoo/overlay_autoupdate.go` reads `ExitCode()` and calls `osExit(code)`.

**Contract for callers**:
- `0` — all packages processed without error.
- `1` — one or more packages failed; survivors processed; failure detail in `Failures` map + stderr lines.
- `2` — fatal error (config invalid, overlay unreadable, etc.); no per-package processing happened.

Document in `README.md` under a new "Exit codes" section.

### AD-5 — Cache schema version bump

R8 (LLM-generated regex/XPath validation) breaks existing entries. Two options:
- **A**: Bump cache schema version; old entries discarded on next read → LLM-call storm on first run after upgrade.
- **B**: Lazy revalidation on read; invalid entries dropped silently, re-derived on next miss.

**Decision**: **B (lazy revalidation)**.

`analysis_cache.go` reads entry → attempts `regexp.Compile(entry.Pattern)` and XPath parse → on either error, deletes entry and treats as miss. No version bump needed. On miss, normal flow re-derives via LLM and validates BEFORE persisting. Result: gradual cache turnover instead of one massive LLM hit.

Add `validatePattern(p string) error` and `validateXPath(x string) error` in `analyzer.go`. Reject patterns with:
- Compile error.
- Backreferences (`\1`-`\9`) — RE2 doesn't support but reject explicitly with informative error.
- Length > 512 chars (basic ReDoS prophylaxis; tunable later via `MaxPatternLen` const).

### AD-6 — Concurrency model for CheckAll & CompareWithProvider

Pattern adopted directly from `analyzer.go:474-500` (channel semaphore + WaitGroup) **with one critical addition**: context-cancellable acquisition.

```go
const DefaultConcurrency = 10

func (c *Checker) CheckAll(force bool) BatchResult[CheckResult] {
    sem := make(chan struct{}, c.concurrency)  // default 10, configurable
    var (
        wg       sync.WaitGroup
        mu       sync.Mutex
        results  []CheckResult
        failures = map[string]error{}
        progress atomic.Uint64
    )
    for name, pkg := range c.config.Packages {
        select {
        case <-c.ctx.Done():
            failures[name] = c.ctx.Err()
            continue
        case sem <- struct{}{}:
        }
        wg.Add(1)
        go func(n string, p PackageConfig) {
            defer wg.Done()
            defer func() { <-sem }()
            r, err := c.CheckPackage(n, force)
            mu.Lock()
            if err != nil {
                failures[n] = err
                c.logger.Warn("check failed for %s: %v", n, err)
            } else {
                results = append(results, r)
            }
            mu.Unlock()
            if c.progressCallback != nil {
                c.progressCallback(progress.Add(1), uint64(len(c.config.Packages)))
            }
        }(name, pkg)
    }
    wg.Wait()
    sort.Slice(results, ...)  // deterministic final ordering
    return BatchResult[CheckResult]{Items: results, Failures: failures}
}
```

**New `CheckerOption`s** (matching the error-returning signature from existing options):
- `WithContext(ctx context.Context) CheckerOption`
- `WithConcurrency(n int) CheckerOption` (validates `1 ≤ n ≤ 100`)
- `WithRateLimiter(r *RateLimiter) CheckerOption`
- `WithProgressCallback(f func(done, total uint64)) CheckerOption`

**`ProgressCallback` signature change** (R4 mitigation per Analyst):
- Old: `func(index int, pkg PackageConfig)`
- New: `func(done, total uint64)`

Counter is atomic; callbacks may fire out-of-order but cumulative count is monotone. Update `cmd/bentoo/overlay_autoupdate.go` printers + `overlay/compare_test.go` assertions.

The same pattern is applied to `internal/overlay/compare.go::CompareWithProvider` with its own concurrency option (`overlay.CompareOptions.Concurrency`).

### AD-7 — `--concurrency=N` flag plumbing

Cobra command in `cmd/bentoo/overlay_autoupdate.go`:

```go
cmd.Flags().IntVar(&autoupdateOpts.Concurrency, "concurrency", autoupdate.DefaultConcurrency,
    "max parallel checks (1-100)")
```

Validation in `runAutoupdate`:
```go
if autoupdateOpts.Concurrency < 1 || autoupdateOpts.Concurrency > 100 {
    return fmt.Errorf("--concurrency must be in range [1,100]")
}
```

Same flag added to `bentoo overlay compare`. Both default to `10`. No env-var or config-file override in this story (keeps surface small; can add later if demand emerges).

### AD-8 — Env-var allow-list for header expansion (resolves R1)

Replace `SubstituteEnvVars` (`httpclient.go:407-413`) with a stricter variant:

```go
// Allowed environment variables for header expansion. Restricting expansion
// to vetted variables prevents a malicious packages.toml from exfiltrating
// secrets (e.g. ANTHROPIC_API_KEY) through arbitrary upstream URLs.
var allowedHeaderEnvPrefix = "BENTOO_"
var allowedHeaderEnvAllowList = map[string]struct{}{
    "GITHUB_TOKEN":      {},
    "GITLAB_TOKEN":      {},
    "OPENAI_API_KEY":    {},
    "ANTHROPIC_API_KEY": {},
}
var allowedExpansionHeaders = map[string]struct{}{
    "Authorization": {},
    "X-Api-Key":     {},
    "X-Auth-Token":  {},
    "Private-Token": {},  // GitLab
}
```

Rules:
- An `${VAR}` in a header value expands only if the **header name** ∈ `allowedExpansionHeaders` (case-insensitive) AND `VAR` ∈ `allowedHeaderEnvAllowList` OR has prefix `BENTOO_`.
- If header name is not in the allow-list: header value is sent **literally** (no expansion). Logged as `Warn`.
- If env var name is not allowed: substitution skipped, literal `${VAR}` sent. Logged as `Warn`.
- If env var is allowed but empty: skip substitution, log `Warn`, send literal.

**Migration**: Document the allow-list in README. No escape hatch — users who need other env vars must rename them to `BENTOO_*` (e.g. `BENTOO_PRIVATE_TOKEN`).

### AD-9 — Git clone validation & timeout (resolves R2, R5 partial, M-5)

In `internal/common/provider/gitclone.go`:

```go
const DefaultGitCloneTimeout = 2 * time.Minute

// ValidateBranch rejects branch names not conforming to git check-ref-format
// rules sufficient to prevent injection. Allows /, +, -, ., _, alphanumeric.
// Disallows: leading -, two consecutive dots, sequences "@{", spaces, ~, ^, :, ?, *, [, \\.
var ErrInvalidBranch = errors.New("invalid git branch name")
func ValidateBranch(b string) error { ... }

// ValidateRepoURL accepts http, https, git, ssh schemes. Rejects file://.
var ErrInvalidRepoURL = errors.New("invalid repository URL")
func ValidateRepoURL(u string) error { ... }
```

`NewGitCloneProvider` calls both validators at construction; returns `ErrInvalidBranch` / `ErrInvalidRepoURL` early. `Update()` uses `exec.CommandContext(ctx, "git", "clone", ...)` with `ctx = context.WithTimeout(parent, DefaultGitCloneTimeout)`. `parent` propagates from `cmd.Context()` (AD-1).

Validation reference: `git check-ref-format`. We will not link git's binary; we implement a conservative subset that accepts realistic refs like `release/1.x`, `feature/foo+bar`, `v1.2.3` and rejects shell metacharacters and `--upload-pack=`-style flag-injection.

### AD-10 — Applier rollback (resolves R5)

`applier.go:145-161` flow becomes:

```go
if err := a.copyEbuild(srcPath, dstPath); err != nil { ... }
// New: track for rollback
defer func() {
    if result.Error != nil {
        if rmErr := os.Remove(dstPath); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
            a.logger.Warn("failed to remove orphan ebuild %s: %v", dstPath, rmErr)
        }
    }
}()
if err := a.runManifest(...); err != nil {
    result.Error = fmt.Errorf("%w: %v", ErrManifestFailed, err)
    return result
}
```

Tests in `applier_test.go::TestApply_RollbackOnManifestFailure` assert `os.Stat(dstPath)` returns `os.ErrNotExist` after a forced manifest failure.

Same pattern is applied to `runManifest` — wrap `exec.Command("ebuild", ...)` in `exec.CommandContext(a.ctx, "ebuild", ...)` so SIGINT during `ebuild manifest` aborts within timeout. Default operation timeout: `5 * time.Minute` (manifest can be slow on large packages).

### AD-11 — File mode 0600 for cache & logs (resolves M-3, N-1)

Single constant in `internal/common/fileutil` (new package, 1 file):

```go
package fileutil
// CacheFileMode is the permission used for all cache and log files.
// 0600 ensures only the running user can read entries that may contain
// secrets or sensitive upstream metadata.
const CacheFileMode os.FileMode = 0600
```

Replace literal `0644` at:
- `internal/common/provider/github.go:250`
- `internal/autoupdate/cache.go:200-210`
- `internal/autoupdate/analysis_cache.go:193`
- `internal/autoupdate/applier.go:334`
- `internal/autoupdate/pending.go:262-272`

**FAT32/exFAT mitigation** (Analyst risk): wrap `os.Chmod` post-rename in a check that logs `Warn` on `EOPNOTSUPP`/`EPERM` instead of failing. Document Linux-ext4 assumption in README.

### AD-12 — Response body cap (resolves R11/P4)

In `internal/autoupdate/httpclient.go::GetWithContext`:

```go
resp, err := c.client.Do(req.WithContext(ctx))
// ...
resp.Body = http.MaxBytesReader(nil, resp.Body, httputil.MaxBodyBytes)
return resp, nil
```

Applies uniformly to all callers of `GetWithContext`. LLM-specific clients (`llm.go`, `openai.go`, `ollama.go`) accept a per-client override since legit Ollama JSON can exceed 10 MiB:

```go
type ClaudeClient struct {
    httpClient   *http.Client
    maxBodyBytes int64  // default httputil.MaxBodyBytes, override via NewClaudeClientWithLimit
}
```

### AD-13 — Rate limiter wiring (resolves R10/P3)

`internal/autoupdate/rate_limiter.go` exists but `Checker.fetchContent` does not call `WaitHTTP`. Fix:

1. New option `WithRateLimiter(r *RateLimiter) CheckerOption`.
2. `NewChecker` initializes a default rate limiter (1 req/s/host, burst 5) if none supplied — keeps behavior conservative-by-default.
3. `fetchContent` extracts host via `url.Parse(u).Host`, then `c.rateLimiter.WaitHTTP(c.ctx, host)` before `httpClient.GetWithContext(c.ctx, u)`.
4. `WaitHTTP` respects ctx; if `c.ctx` is cancelled while waiting, `fetchContent` returns `c.ctx.Err()`.

Note: `rate_limiter.go::DefaultMaxDomains = 30` is sufficient for current workloads (audit-confirmed). No change.

## 4. Per-recommendation file map

| R# | Files touched | New files | API changes |
|---|---|---|---|
| R1 | `httpclient.go` | — | `SubstituteEnvVars` body rewritten; allow-list constants |
| R2 | `provider/gitclone.go`, `provider/factory.go` | — | New `ValidateBranch`, `ValidateRepoURL`, sentinels |
| R3 | `cmd/bentoo/overlay_autoupdate.go`, `checker.go`, `analyzer.go`, `applier.go`, `httpclient.go` | — | New `WithContext` option on Checker, Applier; internal ctx field |
| R4 | `cmd/bentoo/overlay_autoupdate.go`, `cmd/bentoo/overlay_compare.go`, `checker.go`, `overlay/compare.go` | — | New `--concurrency` flag, `WithConcurrency` option, `ProgressCallback` signature change, `BatchResult` struct |
| R5 | `applier.go` | — | `defer`-based rollback in Apply path |
| R6 | `httpclient.go`, `provider/github.go`, `provider/gitlab.go`, `llm.go`, `openai.go`, `ollama.go` | `common/httputil/transport.go` | `BuildTransport()` helper |
| R7 | `checker.go:412`, `analyzer.go:491`, `BatchResult` | — | Removed `//nolint:errcheck`; structured logging |
| R8 | `analyzer.go`, `analysis_cache.go` | — | New `validatePattern`, `validateXPath`, lazy revalidation |
| R9 | All listed in AD-11 | `common/fileutil/mode.go` | `CacheFileMode = 0600` |
| R10 | `checker.go` | — | New `WithRateLimiter` option, `fetchContent` calls `WaitHTTP` |
| R11 | `httpclient.go`, `llm.go`, `openai.go`, `ollama.go` | (uses `httputil`) | `MaxBytesReader` wrap; per-client override on LLM |

## 5. Integration points (mandatory checks before phase 3)

### IP-1 — Context spine audit
Every `context.Background()` call in `internal/autoupdate/` and `internal/overlay/` is one of:
- Test helper.
- `init()`.
- Explicitly justified with a comment.

A grep gate in `Makefile` (`make audit-ctx`) runs:
```
grep -rn "context\.Background()" internal/autoupdate internal/overlay \
  | grep -v "_test.go" | grep -v "// SAFE:" \
  | (! grep .)
```

### IP-2 — Configuration surface convergence
`design.md` table AD-2 is the authoritative source. A new option/knob added during implementation MUST be added to that table before merge. CI cannot enforce this; the auditor sub-agent will check.

### IP-3 — HTTP client unification
After this story, **every outbound HTTP client** in the codebase uses `httputil.BuildTransport()`. CI gate: a unit test in `httputil/transport_test.go` reflects on production clients via `reflect` (or a manual list with TODOs).

## 6. Migration & backward compatibility

| Item | Breaking? | Mitigation |
|---|---|---|
| `${VAR}` in non-allow-listed headers | Yes (silent change to literal pass-through with warn) | README + CHANGELOG `### Security` entry; 0.2.0 minor bump |
| `ProgressCallback` signature | Internal; no external consumers | Compile-time change |
| `CheckAll` return type → `BatchResult` | Internal; no external consumers | Update callers in cmd/ |
| Cache file mode 0600 (was 0644) | Local FS effect only | Tolerate chmod failure (warn) |
| `--concurrency` flag | Additive | Default 10 = ~10× speedup, opt-out by `--concurrency=1` |
| Exit code 1 on partial failure | Yes (was 0 before) | Document under "Exit codes" in README; CHANGELOG `### Changed` |
| HTTP/2 default-on | Yes (corp proxy risk) | `BENTOO_DISABLE_HTTP2=1` env var |
| Analysis cache lazy revalidation | Internal; users see slight slowdown on first run after upgrade | Acceptable |

## 7. Risks & mitigations

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| Parallel `CheckAll` triggers GitHub 429 on large overlays | Medium | Medium | Default rate limit 1 req/s/host; configurable upstream |
| `git clone` regex over-restrictive, rejects legitimate branches | Medium | Low | Pattern derived from `git check-ref-format`; PBT in `gitclone_test.go` |
| HTTP/2 negotiation fails in corp proxies | Medium | High (silent failure) | `BENTOO_DISABLE_HTTP2=1`; README docs |
| Cache mass-revalidation causes LLM-call storm | Low | High (cost) | Lazy revalidation per AD-5 (drops only invalid entries) |
| Race in `CheckAll` semaphore + WaitGroup | Low | Medium | `-race` gate in CI; copy proven pattern from `analyzer.go` |
| Coverage drops below 80% during incremental tasks | Medium | Low | Each task includes unit tests; CI gates per-PR |

## 8. Test strategy

| Category | Where | What |
|---|---|---|
| Unit | `*_test.go` per file | All new functions: `BuildTransport`, validators, allow-list, rollback, BatchResult.ExitCode |
| Integration | `checker_integration_test.go` (new) | Full `CheckAll` flow against `httptest.NewServer` with 50 packages, concurrency=10, force a few failures |
| Property-based | `provider/gitclone_pbt_test.go` (new) | `ValidateBranch` rejects all shell-metachar-bearing inputs; accepts all `git check-ref-format`-valid inputs |
| Property-based | `analyzer_pattern_pbt_test.go` (new) | `validatePattern` rejects unbounded regexes; accepts all valid RE2 |
| Race | All concurrent paths | `go test -race ./...` in CI; already enabled |
| Signal | `overlay_autoupdate_signal_test.go` (new) | Spawn `bentoo overlay autoupdate`, send SIGINT mid-flight, assert exit ≤2s |
| Coverage | Aggregate | `make coverage` ≥ 80% |
| Bench | `checker_bench_test.go` (new) | Sequential vs concurrency=10 baseline; assert ≥4× wall-clock speedup |

## 9. CI / release impact

- `.github/workflows/ci.yml` — no structural change. Verify `go test -race` is on. Add `make audit-ctx` step (IP-1).
- `.golangci.yml` — re-enable G204 after AD-9 lands; keep G304 excluded; document G104/G703/G704 rationale.
- `CHANGELOG.md` — single `[0.2.0]` block with sections `### Added` (concurrency flag, transport tuning, validators), `### Changed` (ProgressCallback, BatchResult, file mode, env-var allow-list), `### Fixed` (orphan ebuild rollback, errcheck silencing, missing rate-limit calls), `### Security` (env-var allow-list, git clone validation, response body cap). End with validation list: `go test -race ./...`, `golangci-lint run`, `govulncheck ./...`, `make audit-ctx`.
- README — new sections: "Exit codes", "Concurrency tuning", "Environment variables in headers" (allow-list spec).

## 10. Open questions

| # | Question | Owner |
|---|---|---|
| OQ-1 | Does Cobra's root cmd already wire `signal.NotifyContext` for `cmd.Context()`? If yes, skip `signal.NotifyContext` in `runAutoupdate` and just call `cmd.Context()`. | Verify in Phase 3 task 1 |
| OQ-2 | Should `--concurrency` also gate `AnalyzeAll`? (it already has `maxConcurrent=3` hardcoded). | Default no (keeps AI-cost predictable); revisit if user requests |
| OQ-3 | Should `BuildTransport()` enable `ForceAttemptHTTP2: true` or rely on stdlib autodetect? | Defer to Phase 3 implementation; default stdlib behavior + opt-out env |

## 11. References

- Audit report (in-conversation, prior turn).
- Architect sub-agent findings (this session).
- Analyst sub-agent completeness checklist (this session).
- Pattern source: `internal/autoupdate/analyzer.go:474-500` (semaphore).
- Pattern source: `cmd/bentoo/overlay_manifest.go:119-122` (signal.NotifyContext).
- Pattern source: `internal/autoupdate/checker.go:63-112` (functional options).
- External: `git check-ref-format` documentation (consulted in AD-9 for branch validation).
