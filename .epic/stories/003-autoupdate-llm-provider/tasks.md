---
story: autoupdate-llm-provider
type: feature
scale: full
version: 1
created: 2026-06-01
---

# Tasks — Autoupdate LLM Provider (claude-code)

Sequencing: T1 (config) → T2 (provider) → T3 (factory) → T4/T5 (wiring) → T6
(docs+commit). T2 depends on T1; T4 and T5 depend on T2+T3. T5 carries the
Checker refactor (AD2) and is the highest-risk task.

Test policy (Full): Unit/Integration sub-tasks carry a `Tests` field with the
scenarios to author (Red-first at run time via the scripted `execCommand` seam —
no real `claude` invoked). Wiring sub-tasks that are pure plumbing carry
`Acceptance` instead. `Covered-by` points at the test file.

---

## T1 — Config: `Bare` field + config→autoupdate mapper

### 1.1 [x] Add `Bare` to both LLMConfig structs
- Files: `internal/common/config/config.go`, `internal/autoupdate/llm.go` (autoupdate.LLMConfig)
- Add `Bare string \`yaml:"bare,omitempty"\`` (values `auto|true|false`, default `auto`). Also add `MaxBudgetUSD float64 \`yaml:"max_budget_usd,omitempty"\``.
- EARS: R8.2, R7.2
- Tests: parse YAML with bare unset → `auto`; with `true|false|auto` → preserved; invalid value → validation error or coerced to `auto` (decide + assert).
- Covered-by: `internal/common/config/config_test.go`

### 1.2 [x] Mapper `config.LLMConfig → autoupdate.LLMConfig`
- Files: new helper (e.g. `cmd/bentoo/llm_wiring.go` or a config accessor)
- Carry Provider/APIKeyEnv/Model/Bare/MaxBudgetUSD.
- EARS: R8.2
- Tests: field-parity test — every `autoupdate.LLMConfig` field reachable from `config.LLMConfig` is mapped (guards R-config-drift).
- Covered-by: mapper test alongside the helper.

---

## T2 — `ClaudeCodeClient` provider (`internal/autoupdate/claude_code.go`, new)

### 2.1 [x] Struct, constructor, exec seam
- `ClaudeCodeClient{model, apiKeyEnv, bareMode, maxBudgetUSD, timeout, ctx, execCommand}`.
- `NewClaudeCodeClient(cfg autoupdate.LLMConfig, opts ...ClaudeCodeOption)`; `WithExecCommand` option (mirrors `Applier.WithExecCommand`); default `execCommand = exec.CommandContext`.
- EARS: R1, R1.1, R7.3, AD6
- Tests: constructor defaults (execCommand non-nil, timeout ≥120s); `WithExecCommand` overrides the seam.
- Covered-by: `internal/autoupdate/claude_code_test.go`

### 2.2 [x] Auth-mode resolution
- `resolveBare(cfg) bool`: `true|false` force; `auto` → key present ⇒ bare, else login.
- EARS: R2.1, R2.2, R2.3
- Tests: matrix {bare=auto/true/false} × {key set/unset} → expected bareMode; key never appears in any returned string.
- Covered-by: `claude_code_test.go`

### 2.3 [x] `buildArgs` + `run` (envelope parsing)
- `buildArgs(structured bool)`: `-p`,`--output-format json`,`--max-turns 2`,`--allowedTools ""`,`--model`; `+--bare`,`+--json-schema`,`+--max-budget-usd` as applicable.
- `run(ctx, instruction, content, schema)`: stdin=content; env adds API key when bare; unmarshal `{is_error,result,subtype,errors,total_cost_usd}`; `is_error||exit≠0` → error.
- EARS: R1.2, R1.3, R1.5, R2.4, R7, R7.1, R7.2
- Tests (scripted seam): success envelope → returns `result`; `is_error:true` (+errors) → error; non-zero exit → error with stderr; malformed JSON → error; assert content went to stdin and NOT argv; assert `--bare`+key only in bare mode; assert ctx cancellation kills (CommandContext used).
- Covered-by: `claude_code_test.go`

### 2.4 [x] `ExtractVersion`
- Extraction instruction → `run` (no schema) → `cleanVersionString(result)`.
- EARS: R1.2
- Tests: envelope result "v1.2.3" → "1.2.3"; empty/garbage result → error.
- Covered-by: `claude_code_test.go`

### 2.5 [x] `AnalyzeContent` (`--json-schema` + fallback)
- Build SchemaAnalysis JSON schema; `run(structured)`; parse structured field, else strip fences + `parseSchemaAnalysis(result)`.
- EARS: R3, R3.1, R3.2, R3.3
- Tests: structured output path → SchemaAnalysis; result-as-fenced-JSON fallback → SchemaAnalysis; `--json-schema`-unsupported simulated (error) → prompt-JSON fallback path; invalid → error.
- Covered-by: `claude_code_test.go`

### 2.6 [x] `GetModel` + default model
- Default `claude-sonnet-4` when cfg.Model empty.
- EARS: R1.4
- Tests: empty model → sonnet default; explicit model → preserved.
- Covered-by: `claude_code_test.go`

### 2.7 [x] Availability detection
- `claudeAvailable() bool` via `exec.LookPath`; constructor returns a typed
  "unavailable" error when absent (callers map to Warn+fallback).
- EARS: R6.1
- Tests: LookPath miss → unavailable error (seam/temp PATH).
- Covered-by: `claude_code_test.go`

---

## T3 — Register provider in factory

### 3.1 [x] `case "claude-code"` in `NewLLMProvider`
- File: `internal/autoupdate/llm.go`
- EARS: R1.1, R8, R8.1
- Tests: factory with provider="claude-code" → *ClaudeCodeClient; existing providers unchanged; unknown provider → error.
- Covered-by: `internal/autoupdate/llm_test.go`

---

## T4 — Wire `analyze`

### 4.1 [x] Build + inject provider in `runAnalyze`
- File: `cmd/bentoo/overlay_analyze.go`
- Load config.LLM → mapper → factory; success ⇒ `WithAnalyzerLLMClient`; failure/unavailable ⇒ Warn + nil (heuristic `generateDefaultSchema`).
- EARS: R4, R4.1, R4.2, R6, R6.1, R6.2
- Acceptance: with provider configured + claude present (seam in test), analyze uses LLM; with claude absent, analyze logs Warn and still produces a heuristic schema (exit 0).
- Covered-by: `cmd/bentoo/overlay_analyze_test.go`

---

## T5 — Wire `--check` (Checker refactor — AD2, HIGH risk)

### 5.1 [x] Refactor `Checker.llmClient` to `LLMProvider`
- File: `internal/autoupdate/checker.go`
- Change field `*LLMClient → LLMProvider`; `WithLLMClient(LLMProvider)`; keep `*LLMClient` as a valid implementation. Adjust `fetchUpstreamVersion` call site (already interface-shaped).
- EARS: R5, R5.1, R8.1
- Tests: `WithLLMClient` accepts a fake `LLMProvider`; existing `WithLLMClient` tests updated; a non-claude provider is now accepted (regression vs old rejection).
- Covered-by: `internal/autoupdate/checker_test.go`

### 5.2 [x] Build + inject provider in `runCheck`; fix the Warn
- Files: `cmd/bentoo/overlay_autoupdate.go`, `internal/autoupdate/checker.go` (R4.2 warn at ~335)
- Inject provider when configured; only Warn about `llm_prompt` when NO provider is configured.
- EARS: R5.2, R5.3
- Tests: package with `llm_prompt` + provider (fake) → ExtractVersion used; without provider → Warn + skip (no crash); Warn no longer fires when a provider is wired.
- Covered-by: `checker_test.go`, `overlay_autoupdate_test.go`

---

## T6 — Docs + commit

### 6.1 [x] README + CHANGELOG
- Document `llm.provider: claude-code`, `llm.bare`, `llm.max_budget_usd`, the
  hybrid-auth/cost note (sonnet+login is expensive; prefer --bare+key), and that
  `--check` uses the LLM only with `llm_prompt` (A2).
- EARS: (docs for R2, R7.2, A1, A2)
- Acceptance: README section + CHANGELOG `[Unreleased]` entry present.

### 6.2 [x] Commit (gate)
- `go build ./...`, `go vet`, full suites (`internal/autoupdate`, `cmd/bentoo`,
  `internal/common/config`) green.
- Commit direct to `main` (Conventional Commits + Co-Authored-By), per the
  project workflow.
- Acceptance: clean build/vet/tests; single coherent commit.

---

## Quality Gates
- **G1 — Build/vet:** `go build ./...` and `go vet ./internal/autoupdate/ ./cmd/bentoo/ ./internal/common/config/` clean.
- **G2 — Tests green:** full suites for `internal/autoupdate`, `cmd/bentoo`, `internal/common/config` pass; new `claude_code_test.go` covers happy/result-fallback/missing/not-authed/malformed/auth-matrix via the scripted seam (no real `claude`).
- **G3 — No regressions:** existing provider and Checker tests still pass after the AD2 refactor (T5).
- **G4 — Degradation proven:** a test removes `claude` from PATH (seam) and asserts `analyze` (heuristic) and `--check` (skip) both succeed with a Warn.
- **G5 — Secrets:** no test or log output contains the API key value.
- **G6 — Docs:** README + CHANGELOG updated before the T6.2 commit.

## Validation (per task)
- `go build ./...` and `go vet ./internal/autoupdate/ ./cmd/bentoo/ ./internal/common/config/`
- `go test` for the touched packages; new tests in `claude_code_test.go` use the
  scripted `execCommand` seam (no network, no real `claude`).
- Final: full-suite green + `go vet` clean before T6.2 commit.

## Notes
- Test authoring/Red-verification happens at **run time** (Executor + scripted
  seam); this plan defines the scenarios. No real `claude` process in unit tests.
- T5 is the riskiest (type refactor); land it behind its own green suite before T6.
