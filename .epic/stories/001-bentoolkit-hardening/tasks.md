---
story: 001-bentoolkit-hardening
type: feature
scale: full
version: 1
created: 2026-05-17
---

# Tasks — Bentoolkit Hardening

## Execution overview

17 tasks across 5 milestones. Tasks within a milestone are mostly independent and may execute in any order; milestones gate sequentially. Foundation (M1) is a hard prerequisite for the rest.

| Milestone | Tasks | Goal |
|---|---|---|
| M1 — Foundation | T1–T4 | New shared infrastructure: `httputil`, `fileutil`, `BatchResult`, sentinels |
| M2 — Security | T5–T8 | Implement R1, R2, R8, R9 |
| M3 — Reliability | T9–T12 | Implement R3, R5, R7, R11 |
| M4 — Performance | T13–T15 | Implement R6, R10, R4 (in that order) |
| M5 — CLI + Docs | T16–T17 | CLI wiring + signal handling + docs + lint gates |

Dependency edges (most important):
- T1 → T13, T12 (transport + body cap)
- T2 → T8 (file mode wiring)
- T3 → T11, T15 (BatchResult consumed by batch error reporting and parallel runs)
- T4 → T6, T7, T12 (sentinels)
- T9 → T10, T14, T15 (context spine consumed by rollback, rate limiter, parallel)
- T6 → T16 (re-enable G204 only after validators land)
- T15 → T16 (`--concurrency` flag piped from CLI to Checker)

CI must stay green at every task boundary (per Q4: 80% coverage always).

## Quality Gates

The following gates apply to **every** task before it can be marked complete:

- **Q-Build**: `go build ./...` passes on Linux amd64 and arm64.
- **Q-Lint**: `golangci-lint run ./...` reports zero new findings.
- **Q-Race**: `go test -race ./<affected-packages>` passes.
- **Q-Cover**: aggregate `go tool cover -func=coverage.out | tail -1` ≥ 80.0%.
- **Q-Vuln**: `govulncheck ./...` reports no findings beyond documented exclusions.
- **Q-Audit-Ctx** (T9+): `make audit-ctx` exits 0 (after target lands in T16).
- **Q-Goleak**: any new test exercising goroutines uses `goleak.VerifyTestMain` (added per Test Advisor gap #5).
- **Q-Validate-Story**: `bash scripts/validate-story.sh .epic/stories/001-bentoolkit-hardening` exits 0.
- **Q-Cross-Ref**: `bash scripts/cross-reference.sh .epic/stories/001-bentoolkit-hardening` exits 0.

A failed gate means rework or rollback; never `--skip-tests` or `--no-verify`.

---

## M1 — Foundation

### T1 — Create `internal/common/httputil` package

**R# coverage**: R6.3 (transport helper), R11.1 (MaxBodyBytes constant)
**AC coverage**: AC-13, AC-14 (transport+HTTP/2)

**Files**:
- NEW `internal/common/httputil/transport.go`
- NEW `internal/common/httputil/transport_test.go`
- NEW `internal/common/httputil/doc.go`

**Sub-tasks**:
- [x] 1.1 Create package with godoc summarizing purpose ("centralized outbound HTTP transport tuning").
  - Requirements: R6.3
  - Validation: `go doc ./internal/common/httputil` renders package summary; `go vet ./...` clean.
- [x] 1.2 Implement `BuildTransport() *http.Transport` with `MaxIdleConnsPerHost=16`, `MaxConnsPerHost=32`, `IdleConnTimeout=90*time.Second`, `TLSHandshakeTimeout=10*time.Second`, `ExpectContinueTimeout=1*time.Second`, `ForceAttemptHTTP2=true`.
  - Requirements: R6.1
  - Validation: `TestBuildTransport_DefaultsTuned` table-driven asserts each field.
- [x] 1.3 Honor `BENTOO_DISABLE_HTTP2=1`: when set, override `ForceAttemptHTTP2=false` AND set `TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}` (stdlib disable idiom).
  - Requirements: R6.2
  - Validation: `TestBuildTransport_HTTP2Disabled` uses `t.Setenv`, asserts `ForceAttemptHTTP2==false` and `len(TLSNextProto)==0`.
- [x] 1.4 Export constants: `MaxBodyBytes int64 = 10 * 1024 * 1024` and `EnvDisableHTTP2 = "BENTOO_DISABLE_HTTP2"`.
  - Requirements: R11.1
  - Validation: `TestMaxBodyBytes_Value` asserts `MaxBodyBytes == 10485760`.
- [x] 1.5 Confirm package adds no third-party imports beyond stdlib + `golang.org/x/net/http2` if needed.
  - Requirements: (architectural)
  - Validation: `go list -m all` diff shows zero new modules.

**Dependencies**: none.
**Blocks**: T12, T13.

---

### T2 — Create `internal/common/fileutil` package

**R# coverage**: R9.3 (single source for file mode)
**AC coverage**: AC-18 (cache file 0600)

**Files**:
- NEW `internal/common/fileutil/mode.go`
- NEW `internal/common/fileutil/mode_test.go`

**Sub-tasks**:
- [x] 2.1 Define `const CacheFileMode os.FileMode = 0600` with godoc explaining rationale.
  - Requirements: R9.3
  - Validation: `TestCacheFileMode_IsRestrictive` asserts `CacheFileMode == 0600`.
- [x] 2.2 Implement `SafeChmod(path string, mode os.FileMode, log Logger) error` swallowing `EOPNOTSUPP`/`EPERM`/`EROFS` with `Warn` log; other errors returned as-is.
  - Requirements: R9.2
  - Validation: `TestSafeChmod_NormalFS` chmods real file in `t.TempDir`; `TestSafeChmod_UnsupportedFS` uses mock logger and injected `chmodFunc` returning `syscall.EOPNOTSUPP`; assert `nil` return and one warn line.
- [x] 2.3 Use a small `Logger` interface (`Warn(msg string, args ...any)`) rather than depending on `internal/common/logger` directly, to avoid import cycle if `logger` ever imports `fileutil`.
  - Requirements: (architectural)
  - Validation: `go list -deps ./internal/common/fileutil/...` shows no `logger` import.

**Dependencies**: none.
**Blocks**: T8.

---

### T3 — Add `BatchResult[T]` generic in `internal/autoupdate`

**R# coverage**: R4.3 (BatchResult.Failures), R7.2, R7.3, R7.4 (ExitCode)
**AC coverage**: AC-9, AC-15

**Files**:
- NEW `internal/autoupdate/batch_result.go`
- NEW `internal/autoupdate/batch_result_test.go`

**Sub-tasks**:
- [x] 3.1 Define `BatchResult[T any]` with fields `Items []T` and `Failures map[string]error`.
  - Requirements: R4.3
  - Validation: `TestBatchResult_FieldsExported` compile-time test.
- [x] 3.2 Implement `ExitCode() int` per AD-4 contract (0 / 1 / 2).
  - Requirements: R7.2, R7.3, R7.4
  - Validation: `TestBatchResult_ExitCode_AllOk` / `_Partial` / `_TotalFail` / `_Empty`.
- [x] 3.3 Implement `HasFailures() bool`.
  - Requirements: R7.1
  - Validation: `TestBatchResult_HasFailures` two-case.
- [x] 3.4 Implement `FormatFailures(w io.Writer)` emitting `ERROR <pkg>: <err>` lines, one per failure, sorted by package name. Multi-line errors flattened: replace `\n` with `\n  ` (continuation indent) so each error remains parseable.
  - Requirements: R7.1
  - Validation: `TestBatchResult_FormatFailures_SortedDeterministic` with 3 failures; `TestBatchResult_FormatFailures_MultilineErrors` ensures continuation indent (Test Advisor recommendation).
- [x] 3.5 Ensure `BatchResult.FormatFailures` is goroutine-safe by NOT calling it before all goroutines have joined (caller responsibility documented in godoc).
  - Requirements: R7.1
  - Validation: package godoc explicitly states "call after Wait()".

**Dependencies**: none.
**Blocks**: T11, T15.

---

### T4 — Add sentinel errors and constants

**R# coverage**: R2.1, R2.2 (provider sentinels), R8.3 (analyzer sentinels), R11.3 (httputil sentinel)
**AC coverage**: AC-3, AC-16, AC-20

**Files**:
- CHANGE `internal/common/provider/interface.go` — add `ErrInvalidRepoURL`, `ErrInvalidBranch`.
- CHANGE `internal/autoupdate/analyzer.go` — add `ErrInvalidPattern`, `ErrInvalidXPath`, const `MaxPatternLen = 512`.
- CHANGE `internal/autoupdate/httpclient.go` — add `ErrResponseTooLarge`.
- CHANGE `internal/common/provider/gitclone.go` — add `const DefaultGitCloneTimeout = 2 * time.Minute`.

**Sub-tasks**:
- [x] 4.1 Declare each sentinel with godoc explaining trigger condition.
  - Requirements: R2.1, R2.2, R8.3, R11.3
  - Validation: `go doc <each-symbol>` shows description; `errors.Is(returnedErr, ErrXxx)` works in unit tests.
- [x] 4.2 Add `MaxPatternLen` constant = 512.
  - Requirements: R8.1
  - Validation: compile-time reference in T7 tests.
- [x] 4.3 Add `DefaultGitCloneTimeout` constant = 2 min.
  - Requirements: R2.3
  - Validation: compile-time reference in T6 tests.

**Dependencies**: none.
**Blocks**: T6, T7, T12.

---

## M2 — Security

### T5 — R1: Header env-var expansion allow-list

**R# coverage**: R1.1, R1.2, R1.3
**AC coverage**: AC-1, AC-2

**Files**:
- CHANGE `internal/autoupdate/httpclient.go` — rewrite `SubstituteEnvVars` and `applyHeaders`.
- NEW `internal/autoupdate/header_allowlist.go` — extracted constants and helpers.
- CHANGE `internal/autoupdate/httpclient_test.go`.

**Sub-tasks**:
- [ ] 5.1 Define package-private `allowedExpansionHeaders` (canonical names: `Authorization`, `X-Api-Key`, `X-Auth-Token`, `Private-Token`).
  - Requirements: R1.1
  - Validation: `TestAllowedExpansionHeaders_HasExpectedSet` enumerates.
- [ ] 5.2 Define `allowedHeaderEnvAllowList` (`GITHUB_TOKEN`, `GITLAB_TOKEN`, `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`) and prefix `allowedHeaderEnvPrefix = "BENTOO_"`.
  - Requirements: R1.1
  - Validation: `TestAllowedEnvVars_HasExpectedSet`.
- [ ] 5.3 Implement `isAllowedHeaderName(name string) bool` using `strings.TrimSpace` + `textproto.CanonicalMIMEHeaderKey` before map lookup (handles case + whitespace; Test Advisor gap #2).
  - Requirements: R1.1
  - Validation: `TestIsAllowedHeaderName` cases: `Authorization`, `authorization`, ` Authorization `, `AUTHORIZATION` all true; `X-Custom`, empty, CRLF-containing all false.
- [ ] 5.4 Reject header names containing CR/LF (`\r`, `\n`) at validation time (defense against CRLF injection; Test Advisor gap #2).
  - Requirements: R1.1
  - Validation: `TestIsAllowedHeaderName_RejectsCRLF` with `"Authorization\r\nInjected"`.
- [ ] 5.5 Implement `isAllowedEnvVar(name string) bool` (prefix OR set membership).
  - Requirements: R1.1
  - Validation: `TestIsAllowedEnvVar` table-driven.
- [ ] 5.6 Rewrite `SubstituteEnvVars(value, headerName string) string`: gated by both checks; **single-pass** substitution (no recursion: a substituted value containing `${OTHER}` is returned literal; Test Advisor gap #1).
  - Requirements: R1.1, R1.2
  - Validation: `TestSubstituteEnvVars_NoRecursiveExpansion` sets `BENTOO_TOKEN="${EVIL}"` and asserts literal output.
- [ ] 5.7 On any denial or empty value, emit `Warn` log identifying header+var; return literal `${VAR}` string.
  - Requirements: R1.2, R1.3
  - Validation: `TestSubstituteEnvVars_*Warn` captures log output via injected logger; asserts one warn per denial.
- [ ] 5.8 Update `applyHeaders` to pass canonical header name into substitution; reject + skip headers with invalid (CRLF) names.
  - Requirements: R1.1
  - Validation: `TestApplyHeaders_RejectsCRLFHeader` smoke.
- [ ] 5.9 Add fuzz target `FuzzSubstituteEnvVars` with corpus of malformed env-var refs (`${`, `${}`, `${A${B}}`).
  - Requirements: R1.1
  - Validation: `go test -fuzz=FuzzSubstituteEnvVars -fuzztime=10s` (CI-skipped, manual run target).

**Dependencies**: none.
**Blocks**: docs (T17).

---

### T6 — R2: Git clone validation + timeout

**R# coverage**: R2.1, R2.2, R2.3
**AC coverage**: AC-3, AC-4

**Files**:
- CHANGE `internal/common/provider/gitclone.go`
- NEW `internal/common/provider/gitclone_validators.go`
- CHANGE `internal/common/provider/gitclone_test.go`
- NEW `internal/common/provider/gitclone_validators_pbt_test.go`

**Sub-tasks**:
- [ ] 6.1 Implement `ValidateRepoURL(raw string) error`: parse via `net/url`; normalize scheme via `strings.ToLower(u.Scheme)` (Test Advisor gap #3); reject if scheme ∉ `{http, https, git, ssh}` or empty host. Return `ErrInvalidRepoURL`.
  - Requirements: R2.1
  - Validation: `TestValidateRepoURL` table: `https://x.io` ok; `HTTPS://x.io` ok (case-insensitive); `file:///etc/passwd` reject; `javascript:alert(1)` reject; empty host reject.
- [ ] 6.2 Implement `ValidateBranch(b string) error`: reject empty; reject if contains whitespace, control chars, `..`, `@{`, leading `-`, or any of `~^:?*[\\`; reject any unicode RTL override (`U+202E`) or NULL byte (Test Advisor gap #3).
  - Requirements: R2.2
  - Validation: `TestValidateBranch` table: `release/1.x`, `feature/foo+bar`, `v1.2.3`, `bug.fix` ok; `--upload-pack=evil`, ` `, `..`, `feat@{1}`, `foo\x00bar`, `evil‮yo` reject.
- [ ] 6.3 Add gopter PBT `TestValidateBranch_PBT` with generator producing arbitrary unicode (including controls + RTL); for every accepted input, assert it passes `git check-ref-format --branch <input>` when run as a parallel cross-check (skip if git missing).
  - Requirements: R2.2
  - Validation: `go test -run TestValidateBranch_PBT ./internal/common/provider/` exits 0.
- [ ] 6.4 Call validators inside `NewGitCloneProvider`; return wrapped sentinel early.
  - Requirements: R2.1, R2.2
  - Validation: `TestNewGitCloneProvider_RejectsBadInputs`.
- [ ] 6.5 In `Update()` / clone path, replace `exec.Command(...)` with `exec.CommandContext(ctx, ...)`; `ctx = context.WithTimeout(parent, DefaultGitCloneTimeout)`. `parent` from `WithContext` injection (T9) or `context.Background()` with `// SAFE: pre-T9, will be threaded` comment until T9 lands.
  - Requirements: R2.3
  - Validation: `TestGitCloneProvider_TimeoutHonored` injects mock `execCommand` returning slow process; assert ctx error within ~150 ms when timeout=100 ms (test override).

**Dependencies**: T4 (sentinels).
**Blocks**: T16 (G204 re-enable).

---

### T7 — R8: LLM pattern/XPath validation + lazy revalidation

**R# coverage**: R8.1, R8.2, R8.3, R8.4
**AC coverage**: AC-16, AC-17

**Files**:
- CHANGE `internal/autoupdate/analyzer.go` — add `validatePattern`, `validateXPath`; call before persisting.
- CHANGE `internal/autoupdate/analysis_cache.go` — `Get` re-runs validators.
- CHANGE `internal/autoupdate/analyzer_test.go`.
- NEW `internal/autoupdate/analyzer_pattern_pbt_test.go`.

**Sub-tasks**:
- [ ] 7.1 Implement `validatePattern(p string) error`: empty → nil; else `len(p) ≤ MaxPatternLen` AND `regexp.Compile(p)` ok. Reject `\1`–`\9` backreferences explicitly with `ErrInvalidPattern: backreferences not supported`.
  - Requirements: R8.1
  - Validation: `TestValidatePattern_AcceptsValid` table; `TestValidatePattern_RejectsOversize` 513 chars; `TestValidatePattern_RejectsUncompilable` `(a`; `TestValidatePattern_RejectsBackrefs` `(a)\1`.
- [ ] 7.2 Add ReDoS smoke test: `TestValidatePattern_RuntimeSafety` runs `regexp.MatchString("^(a+)+$", strings.Repeat("a", 50))` under a 50 ms timeout (Test Advisor gap #4). RE2 is linear so test should always pass; sentinel-style assertion to fail if someone swaps for PCRE later.
  - Requirements: R8.1
  - Validation: test runs in ≤ 50 ms wall-clock.
- [ ] 7.3 Implement `validateXPath(x string) error`: empty → nil; else parse via `github.com/antchfx/xpath` `Compile`. Return `ErrInvalidXPath` on failure.
  - Requirements: R8.2
  - Validation: `TestValidateXPath` table.
- [ ] 7.4 In analyzer save path (`analyzer.go:369-391`), call both validators; on failure return wrapped sentinel; cache write skipped.
  - Requirements: R8.3
  - Validation: `TestAnalyzer_RejectsInvalidLLMOutput` mocks LLM returning invalid pattern; asserts no file written.
- [ ] 7.5 In `analysis_cache.Get`, after unmarshal, run both validators; on failure call `Delete` and return cache-miss (no error to caller; cache-miss is normal).
  - Requirements: R8.4
  - Validation: `TestAnalysisCache_LazyRevalidation` pre-populates cache with invalid regex; `Get` returns miss; file inspection shows entry removed.
- [ ] 7.6 Log `Info`-level line on invalidation: `analysis cache entry for %s invalidated: %v`.
  - Requirements: R8.4
  - Validation: log capture asserts line emitted.

**Dependencies**: T4.
**Blocks**: none.

---

### T8 — R9: File mode 0600 wiring

**R# coverage**: R9.1, R9.2, R9.3
**AC coverage**: AC-18

**Files** (all CHANGE):
- `internal/common/provider/github.go` (line ~250).
- `internal/autoupdate/cache.go` (lines ~200–210).
- `internal/autoupdate/analysis_cache.go` (line ~193).
- `internal/autoupdate/pending.go` (lines ~262–272).
- `internal/autoupdate/applier.go` (line ~334).

**Sub-tasks**:
- [ ] 8.1 Replace each literal `0644` with `fileutil.CacheFileMode`.
  - Requirements: R9.1, R9.3
  - Validation: `grep -rn "0644" internal/` returns zero matches in production code post-task.
- [ ] 8.2 After successful `os.Rename`, call `fileutil.SafeChmod(path, fileutil.CacheFileMode, c.logger)` to repair mode on filesystems where umask widened it.
  - Requirements: R9.2
  - Validation: `TestCacheWrite_FinalModeIs0600` end-to-end uses `t.TempDir`; `os.Stat(file).Mode().Perm() == 0600`.
- [ ] 8.3 Add one test per write-site asserting final mode.
  - Requirements: R9.1
  - Validation: 5 new test functions, all assert mode.

**Dependencies**: T2.
**Blocks**: none.

---

## M3 — Reliability

### T9 — R3: Context spine

**R# coverage**: R3.1, R3.2, R3.3
**AC coverage**: AC-5 (partial), AC-6

**Files**:
- CHANGE `internal/autoupdate/checker.go` — add `ctx`, `opTimeout`; `WithContext`, `WithOpTimeout` options.
- CHANGE `internal/autoupdate/analyzer.go` — same fields + plumbing.
- CHANGE `internal/autoupdate/applier.go` — `WithApplierContext` option.
- CHANGE `internal/overlay/compare.go` — `CompareOptions.Ctx`.
- CHANGE existing `_test.go` files.

**Sub-tasks**:
- [ ] 9.1 Add `ctx context.Context` and `opTimeout time.Duration` to `Checker`; default `context.Background()` and `30 * time.Second`. Add `WithContext(ctx) CheckerOption` and `WithOpTimeout(d) CheckerOption`.
  - Requirements: R3.2
  - Validation: compile-time + `TestChecker_WithContextOption` smoke.
- [ ] 9.2 Replace internal `context.Background()` calls (`checker.go:385`, `analyzer.go:311/324/349`). Each remaining `context.Background()` line carries `// SAFE: <reason>` comment.
  - Requirements: R3.3
  - Validation: `make audit-ctx` exits 0 (after T16 lands target); meanwhile manual `grep -rn "context.Background()" internal/autoupdate internal/overlay` should yield only commented occurrences.
- [ ] 9.3 Add `WithApplierContext(ctx) ApplierOption` (non-error per Applier convention) and thread to all `exec.Command` calls.
  - Requirements: R3.2
  - Validation: `TestApplier_WithContextOption` smoke.
- [ ] 9.4 Add `CompareOptions.Ctx` (additive; default `context.Background()`).
  - Requirements: R3.2
  - Validation: `TestCompare_ContextField` smoke.
- [ ] 9.5 Write cancellation test: `TestChecker_ContextCancelled` against slow `httptest` server; cancel ctx; assert ≤ 100 ms.
  - Requirements: R3.1
  - Validation: test passes under `-race -count=10`.
- [ ] 9.6 Write deadline-expiry test: `TestChecker_ContextDeadlineExceeded` waits for natural deadline; asserts `errors.Is(err, context.DeadlineExceeded)` (Test Advisor gap #5).
  - Requirements: R3.1
  - Validation: test passes.
- [ ] 9.7 Add `go.uber.org/goleak` `VerifyTestMain` to `TestMain` of `internal/autoupdate` and `internal/overlay` packages (Test Advisor gap #5).
  - Requirements: R3.1
  - Validation: `go test ./internal/autoupdate/... ./internal/overlay/...` reports no leaks; new go.mod entry recorded.

**Dependencies**: none.
**Blocks**: T10, T14, T15, T16.

---

### T10 — R5: Applier rollback + exec.CommandContext

**R# coverage**: R5.1, R5.2, R5.3
**AC coverage**: AC-11, AC-12

**Files**:
- CHANGE `internal/autoupdate/applier.go` (lines 145–161 + 256–266).
- CHANGE `internal/autoupdate/applier_test.go`.

**Sub-tasks**:
- [ ] 10.1 After successful `copyEbuild`, register a `defer` that conditionally removes `dstPath` when the resulting error is non-nil (closure-captured named return).
  - Requirements: R5.1
  - Validation: `TestApply_RollbackOnManifestFailure` forces manifest fail; `os.Stat(dstPath)` returns `os.ErrNotExist`.
- [ ] 10.2 If `os.Remove` fails AND not `os.ErrNotExist`, log `Warn`; preserve original error.
  - Requirements: R5.2
  - Validation: `TestApply_RollbackPreservesOriginalError` makes both fail; returned error wraps `ErrManifestFailed`, not `os.ErrXxx`.
- [ ] 10.3 Wrap `a.execCommand` for `ebuild manifest` to use `exec.CommandContext` with `context.WithTimeout(a.ctx, 5 * time.Minute)`.
  - Requirements: R5.3
  - Validation: `TestApply_ManifestTimeoutHonored` mock returns blocking process; ctx timeout=100 ms (test override); assert error within ~200 ms.
- [ ] 10.4 Add `TestApply_RollbackOnManifestWriteFailure` (Test Advisor gap #6): use `t.TempDir` with overlay dir chmod 0500 post-copyEbuild; manifest command succeeds but write fails; assert rollback removes orphan.
  - Requirements: R5.1
  - Validation: test passes.

**Dependencies**: T9.
**Blocks**: none.

---

### T11 — R7: Batch error reporting

**R# coverage**: R7.1, R7.2, R7.3, R7.4
**AC coverage**: AC-15

**Files**:
- CHANGE `internal/autoupdate/checker.go` (line 412) — `CheckAll` returns `BatchResult[CheckResult]`.
- CHANGE `internal/autoupdate/analyzer.go` (line 491) — `AnalyzeAll` returns `BatchResult[AnalysisResult]`.
- CHANGE `cmd/bentoo/overlay_autoupdate.go`, `cmd/bentoo/overlay_analyze.go`.

**Sub-tasks**:
- [ ] 11.1 Update `CheckAll` return type; remove `//nolint:errcheck`; build `BatchResult`. Per-item error capture only (full parallelization in T15).
  - Requirements: R7.1
  - Validation: `TestCheckAll_ReturnsBatchResult` 3 packages, 1 fail; assert shape.
- [ ] 11.2 Update `AnalyzeAll` similarly.
  - Requirements: R7.1
  - Validation: `TestAnalyzeAll_ReturnsBatchResult`.
- [ ] 11.3 In CLI, replace `os.Exit(0)` on success path with `osExit(result.ExitCode())`. Use injected `osExit` from existing harness.
  - Requirements: R7.2, R7.3, R7.4
  - Validation: `TestCLI_ExitCodes` matrix via `cmd/bentoo/run_functions_test.go` harness (`exitSentinel`).
- [ ] 11.4 Print `result.FormatFailures(os.Stderr)` before exit. `FormatFailures` must be invoked **after** all goroutines join (post-T15 ordering concern; Test Advisor gap #7).
  - Requirements: R7.1
  - Validation: `TestCheckAll_ErrorsOnStderr` runs with `-race -count=20`; asserts deterministic lexical ordering.
- [ ] 11.5 Update display helpers consuming old shape (search `BatchResult` consumers post-rename).
  - Requirements: R7.1
  - Validation: build passes.

**Dependencies**: T3.
**Blocks**: T15.

---

### T12 — R11: Response body cap

**R# coverage**: R11.1, R11.2, R11.3
**AC coverage**: AC-20

**Files**:
- CHANGE `internal/autoupdate/httpclient.go::GetWithContext`.
- CHANGE `internal/autoupdate/llm.go`, `openai.go`, `ollama.go`.

**Sub-tasks**:
- [ ] 12.1 Wrap body in `GetWithContext` with `http.MaxBytesReader(nil, body, httputil.MaxBodyBytes)`. On `*http.MaxBytesError` from a subsequent read, wrap with `ErrResponseTooLarge`.
  - Requirements: R11.1, R11.3
  - Validation: `TestGetWithContext_BodyCap` server returns 11 MiB; read returns error wrapping `ErrResponseTooLarge`.
- [ ] 12.2 For each LLM client (`ClaudeClient`, `OpenAIClient`, `OllamaClient`), add `maxBodyBytes int64` field + `WithMaxBodyBytes(int64)` option. Default = `httputil.MaxBodyBytes`.
  - Requirements: R11.2
  - Validation: `TestClaudeClient_WithCustomMaxBody`, `TestOpenAIClient_WithCustomMaxBody`, `TestOllamaClient_WithCustomMaxBody` (Test Advisor gap on R11.2 LLM coverage).
- [ ] 12.3 Document defaults in package godoc.
  - Requirements: R11.1, R11.2
  - Validation: `go doc ./internal/autoupdate` mentions cap.

**Dependencies**: T1, T4.
**Blocks**: none.

---

## M4 — Performance

### T13 — R6: Transport tuning wired

**R# coverage**: R6.1, R6.2, R6.3
**AC coverage**: AC-13, AC-14

**Files**:
- CHANGE `internal/autoupdate/httpclient.go` (line ~111).
- CHANGE `internal/common/provider/github.go` (lines 47–48).
- CHANGE `internal/common/provider/gitlab.go` (lines 48–50).
- CHANGE `internal/autoupdate/llm.go` (lines 196–199).
- CHANGE `internal/autoupdate/openai.go` HTTP client construction.
- CHANGE `internal/autoupdate/ollama.go` HTTP client construction.

**Sub-tasks**:
- [ ] 13.1 Replace each `&http.Client{Timeout: …}` with `&http.Client{Timeout: …, Transport: httputil.BuildTransport()}` at constructor entry only (never overwrite a pre-injected Transport).
  - Requirements: R6.1, R6.3
  - Validation: build passes; per-constructor unit test checks `client.Transport != nil`.
- [ ] 13.2 Add unified reflective test `TestAllHTTPClients_UseTunedTransport`: explicit list of constructors → assert `*http.Transport` field tuning matches `httputil.BuildTransport()` output.
  - Requirements: R6.1
  - Validation: test passes.
- [ ] 13.3 Add `TestAllHTTPClients_HTTP2OptOut`: with `BENTOO_DISABLE_HTTP2=1`, every reconstructed client gets a Transport with `ForceAttemptHTTP2=false`.
  - Requirements: R6.2
  - Validation: test passes.

**Dependencies**: T1.
**Blocks**: T15.

---

### T14 — R10: Rate-limit gate

**R# coverage**: R10.1, R10.2, R10.3
**AC coverage**: AC-19

**Files**:
- CHANGE `internal/autoupdate/checker.go` — `WithRateLimiter` option + default init + hot-path call.

**Sub-tasks**:
- [ ] 14.1 Add `rateLimiter *RateLimiter` field + `WithRateLimiter(*RateLimiter) CheckerOption`.
  - Requirements: R10.3
  - Validation: `TestChecker_WithRateLimiterOption` smoke.
- [ ] 14.2 Default `NewRateLimiter()` with 1 req/s/host, burst 5 (matching `rate_limiter.go` defaults).
  - Requirements: R10.3
  - Validation: `TestChecker_DefaultRateLimiter` asserts non-nil after `NewChecker` without option.
- [ ] 14.3 In `fetchContent`, extract host via `url.Parse(u).Host`. On parse failure: log `Warn`, fail-open (proceed without rate-limit wait).
  - Requirements: R10.1
  - Validation: `TestFetchContent_ParseHostFailure_FailsOpen` passes `:bad-url:`; assert HTTP attempted and warn line.
- [ ] 14.4 Call `c.rateLimiter.WaitHTTP(c.ctx, host)`. On ctx-cancellation while waiting, return ctx error without HTTP.
  - Requirements: R10.1, R10.2
  - Validation: `TestFetchContent_CallsWaitHTTP` injects mock limiter recording host; `TestFetchContent_RateLimitContextCancelled` blocks limiter, cancels ctx, asserts no HTTP request (`httptest` counter == 0).
- [ ] 14.5 Add `TestRateLimiter_LRUEvictionUnderLoad` (Test Advisor gap #8): hit `WaitHTTP` with 35 distinct hosts; assert oldest evicted, no deadlock, all counters consistent.
  - Requirements: R10.3
  - Validation: test passes under `-race -count=10`.

**Dependencies**: T9.
**Blocks**: T15.

---

### T15 — R4: Parallel `CheckAll` & `CompareWithProvider`

**R# coverage**: R4.1, R4.3, R4.4, R4.5
**AC coverage**: AC-7, AC-9, AC-10

**Files**:
- CHANGE `internal/autoupdate/checker.go::CheckAll`.
- CHANGE `internal/overlay/compare.go::CompareWithProvider`.
- NEW `internal/autoupdate/checker_bench_test.go`.

**Sub-tasks**:
- [ ] 15.1 Add `concurrency int` field; `WithConcurrency(n int) CheckerOption` with `1 ≤ n ≤ 100` validation; `DefaultConcurrency = 10`.
  - Requirements: R4.1
  - Validation: `TestChecker_WithConcurrencyOption` table; out-of-range returns error.
- [ ] 15.2 Add `Concurrency int` to `overlay.CompareOptions`; sanitize ≤0 → 10.
  - Requirements: R4.1
  - Validation: `TestCompareOptions_DefaultConcurrency`.
- [ ] 15.3 Reimplement `CheckAll` using semaphore + WaitGroup with cancellable acquisition: `select { case sem<-struct{}{}: case <-c.ctx.Done(): }`. Aggregate to `BatchResult[CheckResult]`. **Recover from per-goroutine panics**: each worker `defer recover()` and records `fmt.Errorf("panic: %v", r)` in `Failures` (Test Advisor gap #9).
  - Requirements: R4.1, R4.3
  - Validation: `TestCheckAll_Parallel_RespectsLimit` (atomic in-flight counter); `TestCheckAll_PanicRecovery` injects panicking package handler; assert process survives, failure recorded.
- [ ] 15.4 Reimplement `CompareWithProvider` loop similarly.
  - Requirements: R4.1
  - Validation: `TestCompareWithProvider_Parallel` analogous.
- [ ] 15.5 Update `ProgressCallback` signature to `func(done, total uint64)` using `atomic.Uint64`. Expose via `WithProgressCallback`.
  - Requirements: R4.4
  - Validation: `TestProgressCallback_Monotonic` under `-race -count=20`; record callback values; assert non-decreasing.
- [ ] 15.6 Sort `Items` lexically by package name before return.
  - Requirements: R4.5
  - Validation: `TestCheckAll_ResultsSorted` with shuffled inputs.
- [ ] 15.7 Add cancel-mid-flight test: 100 packages, cancel after 50 ms; assert ≤ concurrency goroutines finished, rest reported ctx error.
  - Requirements: R4.1
  - Validation: `TestCheckAll_ContextCancelMidFlight`.
- [ ] 15.8 Add `BenchmarkCheckAll_Speedup` and a hard `TestBenchmarkSpeedup` (Test Advisor gap #9 → deterministic CI gate). Handler injects `time.Sleep(100ms)`; baseline `concurrency=1`, parallel `concurrency=10`; assert `speedup ≥ 4.0` via `t.Fatalf`. Use `b.ResetTimer()` after setup; wall-clock measurement only.
  - Requirements: R4.1, DoD #10
  - Validation: test exits 0 in CI; bench printed for record.

**Dependencies**: T3, T9, T14.
**Blocks**: T16.

---

## M5 — CLI + Docs

### T16 — CLI flag, signal, audit-ctx, golangci

**R# coverage**: R3.1, R4.2, IP-1
**AC coverage**: AC-5, AC-6, AC-8

**Files**:
- CHANGE `cmd/bentoo/overlay_autoupdate.go`, `cmd/bentoo/overlay_compare.go`.
- CHANGE `Makefile`.
- CHANGE `.golangci.yml`.
- CHANGE `.github/workflows/ci.yml`.
- NEW `cmd/bentoo/overlay_autoupdate_signal_test.go`.

**Sub-tasks**:
- [ ] 16.1 Add `Flags().IntVar(&opts.Concurrency, "concurrency", autoupdate.DefaultConcurrency, …)`; validate `1 ≤ n ≤ 100` in `runAutoupdate` before any work.
  - Requirements: R4.2
  - Validation: `TestAutoupdate_FlagValidation` table: `-1`, `0`, `101`, `1000` reject; `1`, `10`, `100` accept (Test Advisor gap #10 + recommendation: include negative ints).
- [ ] 16.2 In `runAutoupdate`, verify `cmd.Context()` already provides signal cancellation (OQ-1 verification). If not, wrap with `signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)`. Document choice with comment.
  - Requirements: R3.1
  - Validation: code review + child-process test (16.7).
- [ ] 16.3 Repeat 16.1 / 16.2 in `overlay_compare.go`.
  - Requirements: R3.1, R4.2
  - Validation: `TestCompare_FlagValidation`.
- [ ] 16.4 Add `audit-ctx` Makefile target: `grep -rn "context\.Background()" internal/autoupdate internal/overlay | grep -v "_test.go" | grep -v "// SAFE:" | (! grep .)`. Wire into `make audit` aggregate target.
  - Requirements: IP-1
  - Validation: `make audit-ctx` exits 0 on the new tree; `TestAuditCtxTarget_RejectsNakedBackground` creates a temp fixture file with `context.Background()` (no SAFE comment) and asserts the target exits non-zero (Test Advisor IP-1 negative test).
- [ ] 16.5 Edit `.golangci.yml`: remove `G204` from gosec excludes. Document rationale in YAML comment.
  - Requirements: (security follow-through)
  - Validation: `golangci-lint run ./...` exits 0 with G204 enabled.
- [ ] 16.6 Add CI step calling `make audit-ctx` after `make lint`.
  - Requirements: IP-1
  - Validation: CI YAML lints; manual workflow trigger green.
- [ ] 16.7 Write signal test: prefer direct in-process approach (call `runAutoupdate` in goroutine; send `process.Signal` via `findProcess`) over child-process build to keep CI portable (Test Advisor gap #10). Skip on Windows.
  - Requirements: R3.1
  - Validation: `TestAutoupdate_SignalInterrupt` exits ≤ 2 s.

**Dependencies**: T6, T9, T15.
**Blocks**: T17.

---

### T17 — Docs: README + CHANGELOG

**R# coverage**: R1.1 (header doc), R7.5 (exit-codes doc)
**AC coverage**: DoD #7, #8

**Files**:
- CHANGE `README.md`.
- CHANGE `CHANGELOG.md`.

**Sub-tasks**:
- [ ] 17.1 Add `README.md` section "Exit codes" documenting the 0/1/2 contract.
  - Requirements: R7.5
  - Validation: section heading present; `TestREADME_DocumentsExitCodes` greps for required keywords.
- [ ] 17.2 Add `README.md` section "Concurrency" documenting `--concurrency` flag, default 10, range [1,100].
  - Requirements: R4.2
  - Validation: grep test.
- [ ] 17.3 Add `README.md` section "Headers and environment variables" documenting the allow-list (4 named vars + `BENTOO_*` prefix) and the 4 allowed header names. Include migration example: `${MY_TOKEN}` → `${BENTOO_MY_TOKEN}`.
  - Requirements: R1.1
  - Validation: grep test for keywords (BENTOO_, allow-list, Authorization).
- [ ] 17.4 Add `README.md` section "HTTP/2" documenting `BENTOO_DISABLE_HTTP2=1` opt-out.
  - Requirements: R6.2
  - Validation: grep test.
- [ ] 17.5 Add `README.md` section "Filesystem assumptions" documenting `0600` cache mode and FAT32 fallback.
  - Requirements: R9.1, R9.2
  - Validation: grep test.
- [ ] 17.6 Update old `${ENV_VAR}` example (README:469) to reflect allow-list rules.
  - Requirements: R1.1
  - Validation: line-diff inspection.
- [ ] 17.7 Write `CHANGELOG.md` `[0.2.0]` block with `### Added`, `### Changed`, `### Security`, `### Fixed`. End with validation list: `go test -race`, `golangci-lint`, `govulncheck`, `make audit-ctx`.
  - Requirements: R7.5, DoD #7
  - Validation: `grep -q "## \[0.2.0\]" CHANGELOG.md` exits 0.

**Dependencies**: T5, T6, T15, T16.
**Blocks**: release tagging (post-story).

---

## Definition of Done — final checklist

- [ ] All 17 tasks marked complete.
- [ ] `go test -race ./...` green.
- [ ] `go test -cover ./...` aggregate ≥ 80%.
- [ ] `golangci-lint run` green (with G204 enabled).
- [ ] `govulncheck ./...` no findings outside documented exclusions.
- [ ] `make audit-ctx` exits 0.
- [ ] `goleak` reports zero leaks across `internal/autoupdate` and `internal/overlay`.
- [ ] `bash scripts/validate-story.sh .epic/stories/001-bentoolkit-hardening` exits 0.
- [ ] `bash scripts/cross-reference.sh .epic/stories/001-bentoolkit-hardening` exits 0.
- [ ] `TestBenchmarkSpeedup` reports ≥ 4× improvement.
- [ ] Release commit + tag `v0.2.0`.
