---
story: autoupdate-llm-provider
type: feature
scale: full
version: 1
created: 2026-06-01
---

# Design — Autoupdate LLM Provider (claude-code)

## 1. Overview

The `autoupdate` package ships a full LLM abstraction (`LLMProvider` interface,
`ClaudeClient`/`OpenAIClient`/`OllamaClient`, `NewLLMProvider` factory, the
`llm_prompt` config field) but **no CLI command instantiates a provider** — the
`analyze` command runs purely heuristic and `--check` skips LLM extraction.
This story (a) adds a new provider, **`claude-code`**, that drives the local
`claude` CLI headlessly via `os/exec`, and (b) **wires** the LLM into both
`bentoo overlay analyze` and `bentoo overlay autoupdate --check`.

This is **Level A** of a two-story epic. Level B (`004-autoupdate-agent-mode`,
the full `--agent` flow that edits ebuilds via the `/bentoo` plugin) builds on
this foundation and is out of scope here.

## 2. Goals / Non-Goals

### Goals
- A `claude-code` `LLMProvider` invoking `claude -p ... --output-format json`.
- Hybrid auth: `--bare` + API key (cheap) vs. login/subscription, chosen from config.
- Structured output via `--json-schema` with a fallback that parses the `result` text.
- Wire the provider into `analyze` (schema proposal) and `--check` (`llm_prompt` version extraction).
- Graceful degradation: missing/unauthenticated/erroring `claude` → Warn + heuristic, never a crash.
- Keep existing HTTP providers (`claude`/`openai`/`ollama`) working; `claude-code` is one more option.

### Non-Goals
- The `--agent` mode, tool use, ebuild editing, structural-diff detection (→ story 004).
- Removing/deprecating the HTTP providers.
- New parser types or changes to the version-comparison logic.

## 3. Architecture Decisions

- **AD1 — Subprocess, not SDK.** No official Go SDK for the Agent SDK exists.
  Integrate by exec'ing the `claude` CLI headless (`-p`). Validated against
  claude 2.1.159.
- **AD2 — Program the Checker to the interface.** `Checker.llmClient` is today a
  concrete `*LLMClient` (claude-HTTP only) and `WithLLMClient` rejects other
  providers. Refactor the field and option to the **`LLMProvider` interface** so
  any provider — including `claude-code` — can be injected. (Analyzer already
  uses `WithAnalyzerLLMClient(LLMProvider)`; this aligns the two.)
- **AD3 — Hybrid auth resolved from config.** `LLMConfig.Bare ∈ {auto,true,false}`
  (default `auto`). `auto` ⇒ use `--bare` + API key **iff** `api_key_env` is set
  and that env var is non-empty; otherwise use the login session (no `--bare`).
  `true`/`false` force the mode.
- **AD4 — `--json-schema` with text fallback.** `AnalyzeContent` passes
  `--json-schema` (with `--max-turns 2`, since structured output consumes a
  turn). If the structured field is absent/invalid, parse the `result` string
  (strip markdown fences) and reuse the existing `parseSchemaAnalysis`.
- **AD5 — Graceful degradation.** Provider construction and calls never abort the
  command: on any failure the caller logs a Warn and falls back (heuristic
  `generateDefaultSchema` in `analyze`; skip-extraction in `--check`).
- **AD6 — Mockable exec seam.** Reuse the `Applier.execCommand` /
  `WithExecCommand` idiom: `ClaudeCodeClient` holds an injectable
  `execCommand func(ctx, name string, args ...string) *exec.Cmd` so tests script
  the CLI without invoking real `claude`.
- **AD7 — Default model `sonnet`.** When `llm.model` is empty, default to
  `claude-sonnet-4-x`. ⚠️ See NFR-Cost: `sonnet` in login mode (no `--bare`) is
  expensive per call; the cheap path is `--bare` + API key.
- **AD8 — Content via stdin, never argv.** Page content is piped on stdin (avoids
  argv length limits and gosec G204); only the static instruction goes in `-p`.

## 4. Component Design

### 4.1 `ClaudeCodeClient` (`internal/autoupdate/claude_code.go`, new)
Implements `LLMProvider`:
- Fields: `model`, `apiKeyEnv`, `bareMode` (resolved tri-state), `execCommand`
  seam, `ctx`, `timeout`, `maxBudgetUSD`.
- `NewClaudeCodeClient(cfg autoupdate.LLMConfig, opts ...ClaudeCodeOption)`:
  resolves model (default sonnet) and auth mode; default `execCommand` =
  `exec.CommandContext`.
- `buildArgs(structured bool) []string`: `-p`, `--output-format json`,
  `--max-turns 2`, `--allowedTools ""`, `--model <m>`; `+--bare` when bare;
  `+--json-schema <schema>` when structured; `+--max-budget-usd <n>` when set.
- `run(ctx, instruction string, content []byte, schema string) (string, error)`:
  builds cmd (stdin = content), sets env (`ANTHROPIC_API_KEY` when bare),
  captures stdout, unmarshals the envelope
  `{type,subtype,is_error,result,errors[],total_cost_usd}`; `is_error||exit≠0`
  → error (include `errors`/stderr); else return `result`.
- `ExtractVersion(content, prompt)`: extraction instruction → `run` → reuse
  `cleanVersionString`.
- `AnalyzeContent(content, meta, hint)`: instruction + `--json-schema` for the
  `SchemaAnalysis` shape → `run` → parse structured else `parseSchemaAnalysis`.
- `GetModel() string`.
- Package helper `claudeAvailable() bool` = `exec.LookPath("claude")`.

### 4.2 Factory (`llm.go`)
Add `case "claude-code": return NewClaudeCodeClient(cfg)` to `NewLLMProvider`.

### 4.3 Config (`internal/common/config/config.go` + `autoupdate.LLMConfig`)
- Add `Bare string \`yaml:"bare"\`` (values `auto|true|false`, default `auto`) to
  **both** `config.LLMConfig` and `autoupdate.LLMConfig`.
- Add the missing **mapper** `config.LLMConfig → autoupdate.LLMConfig` (carry
  Provider/APIKeyEnv/Model/Bare) used by the CLI wiring.

### 4.4 Wiring — `analyze` (`cmd/bentoo/overlay_analyze.go`)
In `runAnalyze`: load `config.LLM`; if `Provider != ""`, build provider via
factory (through the mapper); on success pass `WithAnalyzerLLMClient(p)`; on
failure (incl. `claude` absent) Warn and proceed with nil client (heuristic).

### 4.5 Wiring — `--check` (`cmd/bentoo/overlay_autoupdate.go` + `checker.go`)
- Refactor `Checker.llmClient` `*LLMClient → LLMProvider`; `WithLLMClient` accepts
  `LLMProvider`. Update the call at `fetchUpstreamVersion` (already uses the
  interface method `ExtractVersion`).
- In `runCheck`: build provider from config and inject via `WithLLMClient`.
- Update the R4.2 "no LLM wired" Warn (checker.go:335): only warn about
  `llm_prompt` when no provider is actually configured.

## 5. Interfaces & Contracts
- `LLMProvider` (unchanged): `ExtractVersion([]byte,string)(string,error)`,
  `AnalyzeContent([]byte,*EbuildMetadata,string)(*SchemaAnalysis,error)`,
  `GetModel() string`.
- Envelope contract (claude `--output-format json`): object with `is_error`
  (bool), `result` (string), `subtype` (`success|error_max_turns|...`),
  `errors` ([]string), `total_cost_usd` (number). Provider depends only on these.

## 6. Error Handling & Fallback
| Failure | Behavior |
|---------|----------|
| `claude` not in PATH | provider construction returns sentinel → caller Warn + heuristic |
| not authenticated (`result`="Not logged in…", is_error) | error → Warn + heuristic |
| `error_max_turns` / malformed JSON | error surfaced; analyze → heuristic; check → skip + Warn |
| budget exceeded | error surfaced like any run failure |
| ctx cancelled (SIGINT) | `exec.CommandContext` kills child; ctx error propagated |

## 7. Non-Functional Requirements
- **NFR-Cost.** `sonnet` + login mode ≈ $0.09+/call (74k-token context). Cheap
  path = `--bare` + API key. Provider honors `--max-budget-usd` (config
  `max_budget_usd`, default unset = no cap; document recommended cap). Existing
  per-host LLM rate limiter (12s) gates calls upstream.
- **NFR-Timeout.** Headless `claude` startup is slower than an HTTP call; the 60s
  `DefaultLLMTimeout` may be tight — make the provider timeout configurable
  (default raised, e.g. 120s) and always use `exec.CommandContext`.
- **NFR-Security.** Content on stdin only (G204); `--allowedTools ""` (text→text,
  no tool use in Level A); never log the API key.
- **NFR-Compat.** Detect CLI feature drift: if `--json-schema` is unsupported by
  the installed `claude`, fall back to prompt-asks-for-JSON parsing.

## 8. Tooling Decisions
**E2E/frontend tooling: none** — backend Go/CLI feature, no web surface.
Provider behavior is covered by unit tests with a scripted exec seam.

## 9. Risks & Mitigations
- **R-Checker-refactor (HIGH):** changing `*LLMClient → LLMProvider` touches
  `WithLLMClient` and its tests. Mitigate: keep `LLMClient` as a provider
  implementation; only the field/option type changes.
- **R-JSON-parse:** envelope wraps schema JSON as a string, possibly fenced.
  Mitigate: envelope→result→strip-fences→`parseSchemaAnalysis`; tolerant.
- **R-config-drift:** two `LLMConfig` structs; `Bare` must be added to both +
  mapper. Mitigate: single mapper with a test asserting field parity.
- **R-cli-drift:** flags vary across `claude` versions. Mitigate: feature
  fallback (NFR-Compat) + a version probe is allowed but not required.
- **R-cost-surprise:** sonnet+login is pricey. Mitigate: document loudly; warn
  once when running login mode with a non-trivial model.

## 10. Open Questions
- OQ1: default `max_budget_usd` cap — leave unset (current proposal) or set a
  conservative default (e.g. 0.50)? → to confirm in story acceptance.
- OQ2: should `--check` only invoke the LLM when `llm_prompt` is set (current
  proposal), or also as a fallback when the configured parser fails? (The
  parser-failure trigger is arguably Level B territory.)
