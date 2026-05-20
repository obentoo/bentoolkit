---
story: autoupdate-integration-fixes
type: bugfix
scale: standard
version: 1
created: 2026-05-19
---

# Tasks — Autoupdate Integration Fixes

> Tests for Unit and Integration sub-tasks are authored Red-first by the Test Advisor in Phase 3 (and linked here under `Covered-by:` once authored). E2E-style signal tests defer Red verification to Run mode.

## Tooling Decisions

- **E2E:** not applicable — `bentoo` is a CLI Go tool. Verification is `go test` (unit + integration) plus the existing signal-driven harness in `cmd/bentoo/overlay_autoupdate_signal_test.go`.
- **Frontend-design:** not applicable.

---

## T1. Thread `signalContext` into `--apply` (R1)

**Goal:** SIGINT / SIGTERM during `--apply` cancels the in-flight `ebuild manifest` / compile child process and triggers the existing orphan-rollback path.

### T1.1 Integration test — `Apply` cancels on context cancellation
- **Type:** Integration · **Covers:** R1.1, R1.2, R1.3
- **Files (test):** `internal/autoupdate/applier_test.go`
- **Scenario:** With an injected `execCommand` (via `WithExecCommand`) that blocks until `ctx.Done()`, call `Apply(pkg, false)` from a goroutine on an `Applier` built with `WithApplierContext(ctx)`. Cancel the parent context. Assert: the call returns within 2 s, returns a context-derived error, sets `result.Error != nil`, and the new `.ebuild` file has been rolled back (does not exist on disk). Repeat with `Apply(pkg, true)` and a hanging compile to cover the compile path.

### T1.2 Wire context through `runApply`
- **Type:** Unit · **Covers:** R1.1, R1.2
- **Files:** `cmd/bentoo/overlay_autoupdate.go`
- **Change:** Add `ctx context.Context` as the first parameter of `runApply`. Update the dispatch in `runAutoupdate` (`case autoupdateApply != ""`) to pass `runCtx`. Inside `runApply`, pass `autoupdate.WithApplierContext(ctx)` to `NewApplier`.

### T1.3 End-to-end signal test for `--apply`
- **Type:** Integration (signal harness) · **Covers:** R1.1
- **Files (test):** `cmd/bentoo/overlay_autoupdate_signal_test.go` (extend existing file with an apply-path variant)
- **Scenario:** Mirror the existing `--check` signal test pattern for `--apply`. Use a fake ebuild + an injected `execCommand` that hangs; send SIGINT to the process group; assert exit within ~2 s, non-zero exit code, and that the orphan `.ebuild` was removed. Red verification deferred to Run mode (E2E-style harness).

---

## T2. Honour `autoupdate.cache_ttl` (R2)

**Goal:** The `cache_ttl` value from `~/.config/bentoo/config.yaml` reaches the autoupdate `Cache`.

### T2.1 Unit test — `WithCacheTTL` propagates the TTL
- **Type:** Unit · **Covers:** R2.1, R2.2
- **Files (test):** `internal/autoupdate/checker_test.go`
- **Scenario:**
  - Build `NewChecker` with `WithCacheTTL(5 * time.Minute)` — assert the resulting `Checker.Cache().TTL == 5 * time.Minute`.
  - Build `NewChecker` without the option — assert `Cache().TTL == DefaultCacheTTL` (1 h).
  - Build `NewChecker` with `WithCacheTTL(0)` and with `WithCacheTTL(-1)` — assert each returns a construction error (consistent with `WithOpTimeout`'s validation).

### T2.2 Add the `WithCacheTTL` checker option
- **Type:** Unit · **Covers:** R2.1, R2.2, R2.3
- **Files:** `internal/autoupdate/checker.go`
- **Change:**
  - Add a `cacheTTL time.Duration` field to `Checker`.
  - Add `WithCacheTTL(d time.Duration) CheckerOption` that rejects `d <= 0` with a wrapped error.
  - In `NewChecker`, when the cache is not injected via `WithCache`, build the default cache via `NewCache(checker.configDir, WithTTL(checker.cacheTTL))` when `cacheTTL > 0`; otherwise keep the existing default-construction path.

### T2.3 Read `cache_ttl` from config in `runCheck`
- **Type:** Unit · **Covers:** R2.1, R2.2
- **Files:** `cmd/bentoo/overlay_autoupdate.go`
- **Change:** Thread `appCtx` (or the parsed TTL) into `runCheck`. Compute `time.Duration(appCtx.Config.Autoupdate.GetCacheTTL()) * time.Second` and pass via `autoupdate.WithCacheTTL(...)` when constructing the `Checker`. The existing `GetCacheTTL` method already handles default / non-positive values per R2.2.

### T2.4 Integration test — config TTL is honoured end-to-end
- **Type:** Integration · **Covers:** R2.1
- **Files (test):** `cmd/bentoo/overlay_autoupdate_test.go`
- **Scenario:** Write a temp `config.yaml` with `autoupdate.cache_ttl: 60`. Run `runCheck` against a fake overlay + stub HTTP server. Reload the resulting `cache.json` via `NewCache(..., WithTTL(60*time.Second), WithNowFunc(...))`. With injected time at `t+59 s` the entry is fresh; at `t+61 s` it expires.

---

## T3. Clear pending after a successful `--apply` (R3)

**Goal:** Successfully applied packages are removed from `pending.json`; failures stay for retry.

### T3.1 Unit test — `Apply` deletes on success, retains on failure
- **Type:** Unit · **Covers:** R3.1, R3.2, R3.4
- **Files (test):** `internal/autoupdate/applier_test.go`
- **Scenarios:**
  - **Success.** Mock `execCommand` succeeds for `ebuild manifest`. After `Apply(pkg, false)` returns, assert `result.Success == true` and `pending.Has(pkg) == false`.
  - **Manifest failure.** `execCommand` exits non-zero. Assert `pending.Has(pkg) == true`, entry status is `failed`, error string is set, and the orphan ebuild was rolled back.
  - **Compile failure.** Apply with `compile=true`, manifest succeeds, compile fails. Assert pending entry retained with status `failed`.
  - **Delete-after-success failure.** Inject a `PendingList` whose `Delete` returns an error. Assert `result.Success == true`, a Warn was logged via the package warn sink, and the exit-code path is not flipped.

### T3.2 Delete the pending entry on apply success
- **Type:** Unit · **Covers:** R3.1, R3.4
- **Files:** `internal/autoupdate/applier.go`
- **Change:** In `Apply`, after the success path sets `result.Success = true` and immediately before returning, call `a.pending.Delete(pkg)`. If the delete returns an error, emit `logger.Warn(...)` with the package and error; do NOT flip `result.Success` and do NOT set `result.Error` (so the orphan-rollback `defer` keyed on `result.Error == nil` remains correct).

### T3.3 Doc note in package comment
- **Type:** Doc · **Covers:** R3.1
- **Files:** `internal/autoupdate/doc.go`
- **Change:** Update the "Pending updates tracking" sentence in the package comment to clarify that `pending.json` retains only items awaiting work or post-mortem retry; successfully applied items are removed.

---

## T4. Align `llm_prompt` documentation and runtime (R4)

**Goal:** README reflects reality; users with `llm_prompt` set during `--check` get a clear Warn at runtime.

### T4.1 Unit test — Warn emitted when `llm_prompt` is set without an LLM
- **Type:** Unit · **Covers:** R4.2
- **Files (test):** `internal/autoupdate/checker_test.go`
- **Scenario:** Build a `Checker` without `WithLLMClient`. Configure three packages: two with non-empty `LLMPrompt`, one with empty. Capture `warnLogf` via the existing test sink override. Assert exactly one Warn per `LLMPrompt`-set package, each Warn identifying the package name and stating that the LLM is not wired into the check path. Build a second `Checker` from the same config and assert the Warns repeat (de-dup is per `Checker` instance, not process-wide).

### T4.2 Emit Warn for unused `llm_prompt`
- **Type:** Unit · **Covers:** R4.2, R4.3
- **Files:** `internal/autoupdate/checker.go`
- **Change:** At the end of `NewChecker` (after the config is loaded, the LLM client resolved, and just before returning), iterate `checker.config.Packages` once. For each package where `cfg.LLMPrompt != ""` AND `checker.llmClient == nil`, emit (via the package-level `warnLogf` so tests can capture):
  > `package %q sets llm_prompt but no LLM is wired into the check path; this field is consumed only by 'bentoo overlay analyze' (see README)`
  Iterate keys in sorted order so the diagnostic line order is deterministic.

### T4.3 Correct the README
- **Type:** Doc · **Covers:** R4.1
- **Files:** `README.md`
- **Changes:**
  - **Optional fields table** (`packages.toml` schema): update the `llm_prompt` row description to read *"Consumed by `bentoo overlay analyze` only. Has no effect on `bentoo overlay autoupdate --check`. A package with this field set during `--check` triggers a Warn."*
  - **Supported LLM Providers** section: replace "The `analyze` and `autoupdate` commands can use an LLM for version extraction when parsers are insufficient." with "The `analyze` command uses an LLM for schema generation. `bentoo overlay autoupdate --check` does not currently invoke an LLM; the `llm_prompt` field in `packages.toml` is consumed only by `analyze`."

### T4.4 CHANGELOG entry
- **Type:** Doc · **Covers:** all of R1-R4
- **Files:** `CHANGELOG.md`
- **Change:** Under a new `[Unreleased]` block, document the four fixes:
  - `### Fixed`: A (SIGINT in `--apply` — closes the gap left by 0.2.0), B (`autoupdate.cache_ttl` is honoured), C (pending list clears after a successful `--apply`).
  - `### Changed`: D (`llm_prompt` documented as `analyze`-only; `--check` emits a Warn when set).
  - Mention that the SIGINT claim of 0.2.0 was incomplete for `--apply` and is now fully realised.

---

## T5. Commit

### T5.1 Single conventional commit
- **Type:** Commit · **Covers:** —
- **Action:** Create a single commit after all sub-tasks land green. Suggested message:
  ```
  fix(autoupdate): wire signal/ttl/pending/llm_prompt integration gaps

  - SIGINT/SIGTERM now cancels --apply (R1)
  - autoupdate.cache_ttl is honoured (R2)
  - pending.json clears after a successful --apply (R3)
  - llm_prompt is documented as analyze-only; --check emits a Warn (R4)

  Closes the wiring that 001-bentoolkit-hardening left disconnected and
  makes the SIGINT claim from CHANGELOG 0.2.0 truthful for --apply.
  ```
- **Gates:** `go test -race ./...` clean · `golangci-lint run` clean · README and CHANGELOG diffs reviewed.

---

## Quality Gates

The following gates SHALL pass before this story is considered complete (also re-asserted at T5.1 commit time):

- **G1. Tests.** `go test -race ./...` exits 0; no race-detector hits; goleak checks (already in the suite) remain clean.
- **G2. Lint.** `golangci-lint run` exits 0 against the project's existing `.golangci.yml`.
- **G3. Build.** `go build ./...` exits 0.
- **G4. Story validation.** `bash "/home/otaku/.claude/plugins/cache/lucascouts/epic/0.3.1/scripts/validate-story.sh" .epic/stories/002-autoupdate-integration-fixes` exits 0; cross-reference script also exits 0.
- **G5. Coverage of new requirements.** Each requirement (R1.1-R4.3) is exercised by at least one test listed in the Traceability table below.
- **G6. Documentation diff review.** README and CHANGELOG diffs match the wording mandated by R4.1 and T4.4.
- **G7. No regression in 001 surface.** Tests under `internal/autoupdate/` and `cmd/bentoo/` that predate this story continue to pass unchanged.

---

## Traceability

| Requirement | Sub-tasks |
|---|---|
| R1.1 | T1.1, T1.2, T1.3 |
| R1.2 | T1.1, T1.2 |
| R1.3 | T1.1, T1.3 |
| R2.1 | T2.1, T2.2, T2.3, T2.4 |
| R2.2 | T2.1, T2.2, T2.3 |
| R2.3 | T2.2 |
| R3.1 | T3.1, T3.2, T3.3 |
| R3.2 | T3.1 |
| R3.3 | — (unchanged behaviour preserved by T3.2 reusing existing `pending.Add`/`Delete` paths) |
| R3.4 | T3.1, T3.2 |
| R4.1 | T4.3 |
| R4.2 | T4.1, T4.2 |
| R4.3 | T4.2 (constraint: do not remove the struct field) |
