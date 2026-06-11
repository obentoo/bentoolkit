---
story: autoupdate-llm-provider
type: feature
scale: full
version: 1
created: 2026-06-01
---

# Story — Autoupdate LLM Provider (claude-code)

## Context

`internal/autoupdate` already defines an `LLMProvider` abstraction, three HTTP
providers, a factory, and the `llm_prompt` config field — but **no CLI command
ever instantiates a provider**, so `bentoo overlay analyze` runs purely
heuristic and `bentoo overlay autoupdate --check` never uses the LLM. This story
adds a `claude-code` provider (driving the local `claude` CLI headlessly) and
wires the LLM into both commands. It is **Level A** of a two-story epic; the
`--agent` ebuild-editing flow is **Level B** (story 004) and out of scope.

See [design.md](design.md) for architecture decisions and
[.draft/analyst.md](.draft/analyst.md) for the context discovery.

## User Value

A maintainer can configure `llm.provider: claude-code` and have the existing,
already-paid-for `claude` login (or an API key) power version extraction and
schema proposal — without managing a separate Anthropic API integration, and
without the tool ever crashing when `claude` is unavailable.

## Requirements (EARS)

### R1 — `claude-code` provider
- R1.1 WHEN `llm.provider` is `"claude-code"` THE SYSTEM SHALL construct a
  `ClaudeCodeClient` that implements `LLMProvider`.
- R1.2 WHEN the provider invokes the CLI THE SYSTEM SHALL pass page content on
  stdin and only the static instruction via `-p` (never page content in argv).
- R1.3 WHEN the `claude` process exits non-zero OR the JSON envelope has
  `is_error: true` THE SYSTEM SHALL return an error including the reported
  `errors`.
- R1.4 WHEN `llm.model` is empty THE SYSTEM SHALL default the model to
  `claude-sonnet-4` (latest sonnet).
- R1.5 THE SYSTEM SHALL request `--output-format json`, `--max-turns 2`, and
  `--allowedTools ""` on every invocation.

### R2 — Hybrid authentication
- R2.1 WHEN `llm.bare` is `auto` AND `llm.api_key_env` is set AND that env var is
  non-empty THE SYSTEM SHALL invoke `claude` with `--bare` and export the API key.
- R2.2 WHEN `llm.bare` is `auto` AND no API key is resolvable THE SYSTEM SHALL
  invoke `claude` without `--bare` (login/subscription mode).
- R2.3 WHEN `llm.bare` is `true` OR `false` THE SYSTEM SHALL force bare or login
  mode respectively, regardless of API key presence.
- R2.4 THE SYSTEM SHALL never write the API key to logs or error messages.

### R3 — Structured schema output (`AnalyzeContent`)
- R3.1 WHEN proposing a schema THE SYSTEM SHALL pass `--json-schema` describing
  the `SchemaAnalysis` shape.
- R3.2 IF the structured output is absent or invalid THE SYSTEM SHALL parse the
  envelope `result` text (stripping markdown fences) via the existing
  `parseSchemaAnalysis`.
- R3.3 IF the installed `claude` does not support `--json-schema` THE SYSTEM
  SHALL fall back to instructing JSON in the prompt and parsing `result`.

### R4 — Wiring `analyze`
- R4.1 WHEN `llm.provider` is configured THE `bentoo overlay analyze` command
  SHALL build the provider and use it for schema proposal.
- R4.2 IF provider construction or the call fails THE `analyze` command SHALL log
  a Warn and fall back to the heuristic `generateDefaultSchema` (no crash).

### R5 — Wiring `--check`
- R5.1 THE `Checker` SHALL accept an `LLMProvider` (interface) via
  `WithLLMClient`.
- R5.2 WHEN `llm.provider` is configured AND a package defines `llm_prompt` THE
  `--check` command SHALL use the provider to extract the version.
- R5.3 WHEN no provider is configured AND a package defines `llm_prompt` THE
  `--check` command SHALL emit the existing Warn and skip LLM extraction.

### R6 — Graceful degradation
- R6.1 WHEN the `claude` binary is not on `PATH` THE SYSTEM SHALL Warn and
  continue without the LLM (the command SHALL NOT fail because of it).
- R6.2 WHEN `claude` reports a not-authenticated result THE SYSTEM SHALL treat
  the provider as unavailable (Warn + fallback per R4.2/R5.3).

### R7 — Robustness
- R7.1 THE SYSTEM SHALL invoke the CLI through `exec.CommandContext` so a
  cancelled parent context (SIGINT/SIGTERM, timeout) kills the child process.
- R7.2 WHEN `llm.max_budget_usd` is set THE SYSTEM SHALL pass `--max-budget-usd`.
- R7.3 THE provider timeout SHALL be configurable, defaulting high enough for
  headless startup (≥120s).

### R8 — Backward compatibility
- R8.1 THE existing `claude`/`openai`/`ollama` providers SHALL remain selectable;
  `claude-code` is additive and changes no existing provider behavior.
- R8.2 THE new `Bare` config field SHALL default to `auto` and SHALL be optional
  in both `config.LLMConfig` and `autoupdate.LLMConfig`.

## Acceptance Criteria
- `llm.provider: claude-code` makes `analyze` propose a schema via the local
  `claude` CLI (verified with a scripted exec seam in tests).
- With a package carrying `llm_prompt`, `--check` extracts a version via the
  provider; without a provider it Warns and skips.
- Removing `claude` from PATH degrades both commands to Warn + heuristic/skip
  with a zero exit for the LLM portion.
- `bare=auto` selects `--bare`+key when the key env is present, login otherwise;
  `bare=true|false` forces the mode.
- Existing provider tests and the full `internal/autoupdate`, `cmd/bentoo`,
  `internal/common/config` suites pass; `go vet` clean.

## Assumptions
- A1 (OQ1): `llm.max_budget_usd` has **no default cap** (unset = no cap); a
  conservative cap is documented as recommended but not enforced.
- A2 (OQ2): `--check` invokes the LLM **only** when `llm_prompt` is set. Using
  the LLM as a fallback when the normal parser fails is **Level B** (story 004).
- A3: `claude-code` is selected explicitly via `llm.provider`; it does not become
  the implicit default even when the binary is present.

## Out of Scope (→ story 004)
- `--agent` flag, tool use, ebuild editing, `/bentoo` plugin orchestration,
  structural-change detection, agent gates/rollback.

## Dependencies
- Local `claude` CLI (validated: 2.1.159). Absence is handled by R6.1.
- No new Go modules expected (uses stdlib `os/exec`).
