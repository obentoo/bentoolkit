---
story: tui-live-output
type: feature
scale: full
version: 1
created: 2026-06-21
---

# Tasks — Live TUI Output (Bubble Tea)

Sequencing: T1 (deps) → T2 (the `internal/common/tui` foundation) → T3 (applier
seam) → T4 (apply-driver wiring, the spine) → T5 (remaining subprocess sites) →
T6 (migrate `overlay manifest`, delete the ANSI reporter) → T7 (validate + docs +
commit). T2 depends on T1; T3/T4 depend on T2; T5/T6 depend on T3+T4; T7 last.
T4 is the first user-visible milestone (live apply); T6 is the highest-risk task
(removes working code under a parity gate).

Test policy (Full): Unit/Integration sub-tasks carry a `Tests` field with the
scenarios to author **Red-first** (`teatest` golden frames for the model;
scripted commands for `StreamCapture`; an injected non-TTY writer for the plain
backend; the existing `execCommand`/reporter seams for the applier). Pure wiring
sub-tasks carry `Acceptance` instead. `Covered-by` names the test file. No real
TTY, `pkgdev`, `git`, or `claude` is invoked in CI — every external command is
scripted through an injected seam.

---

## T1 — Dependencies (supply chain)

### 1.1 [x] Add bubbletea + lipgloss + bubbles, pinned
- Files: `go.mod`, `go.sum`
- Add `github.com/charmbracelet/bubbletea`, `.../lipgloss`, `.../bubbles` at exact
  versions whose release is **≥ 7 days old**; run `go mod tidy` then
  `go mod verify`; commit `go.sum`. Confirm current stable versions and API via
  context7 before pinning.
- EARS: R7.3, C1
- Acceptance: `go build ./...` resolves; `go mod verify` passes; `go.sum` is
  committed; no dependency newer than 7 days. Note transitive surface
  (`muesli/termenv`; `mattn/go-isatty` already present).

---

## T2 — `internal/common/tui` foundation

### 2.1 [x] Reporter interface + event message types
- Files: `internal/common/tui/reporter.go` (new), `internal/common/tui/events.go`
  (new)
- Define `Reporter` (BatchStart/TaskStart/TaskStage/TaskProgress/TaskLine/
  TaskDone/Log/BatchDone, per design §5) and the matching `tea.Msg` types.
- EARS: R3.1
- Tests: interface is satisfied by both backends (compile-time `var _ Reporter`);
  `Stream` enum stringifies stdout/stderr.
- Covered-by: `internal/common/tui/reporter_test.go`

### 2.2 [x] `StreamCapture` — buffered capture + live line emitter
- Files: `internal/common/tui/streamcapture.go` (new)
- `io.MultiWriter(&captureBuf, lineEmitter)`; lineEmitter splits on `\n`, treats
  `\r` as in-place replacement, emits a `TaskLine` per update; bounded throughput;
  `Close()` flushes a trailing partial line. Expose `captureBuf` for the error
  path (R7.1).
- EARS: R1.1, R1.2, R1.5, R7.1
- Tests (scripted `io` input, Red-first): `\n`-delimited input → one TaskLine per
  line + full buffer; `\r` progress (`"10%\r20%\r100%\n"`) → in-place updates,
  final line `100%`; huge input (1e5 lines) → emitter memory bounded, buffer holds
  all (or documents the cap); partial trailing line flushed on Close.
- Covered-by: `internal/common/tui/streamcapture_test.go`

### 2.3 [x] Model: tasks, bounded tail ring, history, log
- Files: `internal/common/tui/model.go` (new)
- `tea.Model` with `Init/Update/View`; tail ring (default ~10, R1.5); per-worker
  slots (R1.3); ✓/✗ history + overall progress (R1.4); `lipgloss` layout honoring
  `NO_COLOR`; inline (no alt-screen, C3).
- EARS: R1.3, R1.4, R1.5, C3
- Tests (`teatest` golden frames, Red-first): a scripted event sequence
  (BatchStart→2×TaskStart→TaskLine×N→TaskDone) renders stable golden frames;
  tail never exceeds N lines; two concurrent slots render without line interleave.
- Covered-by: `internal/common/tui/model_test.go`

### 2.4 [x] Program wrapper + signal reconciliation
- Files: `internal/common/tui/program.go` (new)
- `tea.NewProgram(model, tea.WithContext(ctx), WithOutput, WithInput)`;
  `Start/Wait/Stop`; intercept `Ctrl-C` → invoke caller cancel, quit after;
  `ReleaseTerminal`/`RestoreTerminal` passthrough.
- EARS: R5.1
- Tests: Ctrl-C key msg triggers the injected cancel func and a `tea.Quit`;
  context cancellation quits the program (`teatest` + injected ctx).
- Covered-by: `internal/common/tui/program_test.go`

### 2.5 [x] Backends: `teaReporter` and `plainReporter`
- Files: `internal/common/tui/tea_reporter.go` (new),
  `internal/common/tui/plain_reporter.go` (new)
- `teaReporter` forwards each event via `program.Send` (goroutine-safe, R7.4).
  `plainReporter` writes deterministic START/OK/FAIL lines AND streams the tail to
  its `io.Writer`, rate-limited, `\r` collapsed to full lines (R2.3); mutex-guarded
  like today's `LogReporter`; emits no ANSI (R2.2).
- EARS: R2.2, R2.3, R3.1, R7.4
- Tests (Red-first): `plainReporter` to a `bytes.Buffer` → expected line sequence,
  zero ANSI escapes, `\r` updates collapsed; `teaReporter` from N goroutines →
  `-race` clean, all events delivered.
- Covered-by: `internal/common/tui/plain_reporter_test.go`,
  `internal/common/tui/tea_reporter_test.go`

### 2.6 [x] `Enabled()` gate + `Confirm`/`RunAttached` bridge
- Files: `internal/common/tui/enabled.go` (new), `internal/common/tui/interactive.go`
  (new)
- `Enabled(opts)`: TTY (reuse `output.IsTerminal()`) ∧ ¬`--no-tui` ∧
  ¬`NO_COLOR` ∧ ¬`BENTOO_NO_TUI` (R2.1/R2.2). `Confirm(prompt) bool` rendered in
  the UI (R4.2); `RunAttached(cmd)` releases/restores the terminal for interactive
  children (R4.1) via `tea.ExecProcess`.
- EARS: R2.1, R2.2, R4.1, R4.2
- Tests: gate truth table over the four inputs (env + flag + fake TTY stat);
  `RunAttached` calls Release before and Restore after the (mock) command — asserted
  via the injected program seam (no real TTY).
- Covered-by: `internal/common/tui/enabled_test.go`,
  `internal/common/tui/interactive_test.go`

---

## T3 — Applier streaming seam + reporter

### 3.1 [x] `WithApplierReporter` + lifecycle events
- Files: `internal/autoupdate/applier.go`
- Add `WithApplierReporter(r tui.Reporter)` (default Noop = today's silence,
  R3.3). `Apply` emits `TaskStart`→`TaskStage("manifest")`→ … →`TaskDone`; the
  current `logger.Info` calls in the apply path (`applier.go:700,719,841`) become
  `Log`/`TaskStage` events. The `claude` fixer stage is a spinner only (R4.3).
- EARS: R3.3, R4.3, R6.1
- Tests: a recording fake `Reporter` captures the emitted event order for a
  scripted success and a scripted manifest-fail-then-fix path; Noop reporter →
  byte-identical behavior to pre-story (golden of existing applier test output).
- Covered-by: `internal/autoupdate/applier_reporter_test.go`

### 3.2 [x] `runManifest`/`runCompile` use `StreamCapture`; compile via `RunAttached`
- Files: `internal/autoupdate/applier.go`
- Replace `CombinedOutput()` in `runManifest` (line 815) with the `StreamCapture`
  seam (live TaskLine + `captureBuf` for the error string, R7.1). `runCompile`
  (line 855) streams via `StreamCapture` and runs the privileged child through
  `RunAttached` so the sudo/`doas` prompt reaches the real TTY (R4.1).
- EARS: R1.1, R1.2, R4.1, R7.1
- Tests (scripted `execCommand`): a fake `pkgdev` writing progressive lines →
  TaskLine events observed live AND the full error string preserved on a scripted
  failure; compile failure still writes the compile log file unchanged.
- Covered-by: `internal/autoupdate/applier_reporter_test.go`

---

## T4 — Apply-driver wiring (the spine, first user-visible result)

### 4.1 [x] Gate + program + reporter in `runApply`/`runApplyAll`
- Files: `cmd/bentoo/overlay_autoupdate.go`
- Build the reporter via `tui.Enabled(...)`: start the `Program` and use
  `teaReporter` when enabled, else `plainReporter` to stderr (R2.1/R2.2/R2.4).
  Pass `WithApplierReporter(...)` into `NewApplier` (lines 528,565). Convert the
  `output.Info.Printf("Applying update for …")` lines (539,586) into `TaskStart`.
  Reconcile Ctrl-C with the existing `ctx` (R5.1/R5.2). On program-start failure,
  log once and continue plain (R2.4).
- EARS: R2.1, R2.2, R2.4, R5.1, R5.2, R6.1
- Acceptance: in a TTY the apply shows a live tail per package; with
  `BENTOO_NO_TUI=1` or a piped stdout, output is plain with no ANSI but still
  streams the tail to stderr; Ctrl-C cancels within ~2 s and the orphan rollback
  runs.
- Covered-by: `cmd/bentoo/overlay_autoupdate_tui_test.go` (gate selection +
  non-TTY no-ANSI assertion; manual real-TTY check noted)

### 4.2 [x] `--no-tui` flag + env plumbing
- Files: `cmd/bentoo/overlay_autoupdate.go` (flag registration), wherever the
  autoupdate command flags are defined
- Register `--no-tui`; read `NO_COLOR`/`BENTOO_NO_TUI`. Thread into `tui.Enabled`.
- EARS: R2.1, R2.2
- Tests: flag set → `Enabled` false even on a (faked) TTY; env set → same.
- Covered-by: `cmd/bentoo/overlay_autoupdate_tui_test.go`

---

## T5 — Remaining subprocess sites

### 5.1 [x] git runner + git clone: stage/done, tail on downloads
- Files: `internal/common/git/runner.go`,
  `internal/common/provider/gitclone.go`
- Emit `TaskStage`/`TaskDone` for git operations; add `StreamCapture` tails to the
  streaming downloads (`git clone`, `git fetch`); fast ops (add/commit/reset) get
  status events only. Preserve captured output on error (R7.1).
- EARS: R6.2, R7.1
- Tests (scripted command seam): clone emits TaskLine progress; commit emits only
  stage/done; error path preserves output.
- Covered-by: `internal/common/git/runner_reporter_test.go`

### 5.2 [x] snapshot runner: stage/done events
- Files: `internal/snapshot/runner.go`
- Route the generic `execCommand` runner through reporter stage/done events
  (tail where a child streams; status otherwise); honor the Noop default (R3.3).
- EARS: R6.2, R3.3
- Tests: recording reporter sees stage/done for a scripted snapshot command; Noop
  default unchanged.
- Covered-by: `internal/snapshot/runner_reporter_test.go`

---

## T6 — Migrate `overlay manifest` (delete the ANSI reporter)

### 6.1 [x] Reimplement manifest reporting on the tui system, with parity
- Files: `internal/overlay/manifest.go`, `internal/overlay/manifest_reporter.go`
  (remove ANSI `TUIReporter`), `cmd/bentoo/overlay_manifest.go`
- Replace `ProgressReporter`/`TUIReporter`/`LogReporter` usage with the tui
  `Reporter` (slots per worker, ✓/✗ history, summary line). `runOneManifest`
  (manifest.go:321-347) feeds `StreamCapture` so each worker tails live. Keep the
  AD7 gate at the manifest entry (was `overlay_manifest.go:159`). Delete the
  hand-rolled ANSI once parity goldens pass (R3.2).
- EARS: R1.3, R3.2, R6.2
- Tests (`teatest` parity goldens, Red-first): the migrated manifest renders the
  same slots/history/summary as the pre-migration golden for a scripted multi-pkg
  run; non-TTY → plain lines, no ANSI.
- Covered-by: `internal/overlay/manifest_reporter_test.go`

---

## T7 — Validate, document, commit

### 7.1 [x] Validation gate
- Run: `go build ./...`; `go test ./... -race`;
  `go vet ./...`; `staticcheck ./...` (if present); `go mod verify`.
- Manual: one real-TTY apply (live tail + Ctrl-C + sudo prompt) and one piped run
  (no ANSI) — record the outcome.
- EARS: R7.3, R7.4, R5.1, R4.1
- Acceptance: all green; `-race` clean; manual TTY + piped checks pass.

### 7.2 [x] Docs + commit(s)
- Files: `README`/`CHANGELOG`, `--no-tui`/`BENTOO_NO_TUI` documented.
- Commit in logical steps (deps → foundation → apply spine → sites → manifest
  migration). Co-authored trailer per repo convention. Do not commit until the
  user asks.
- EARS: R7.3

---

## Quality Gates

- `go test ./... -race` green; reporter ingress `-race` clean under parallel
  manifest workers (R7.4).
- No ANSI escape sequences emitted in plain/non-TTY mode (asserted in tests for
  `plainReporter` and the apply driver) (R2.2).
- `StreamCapture` preserves the full captured output for every error path,
  byte-identical to the pre-story `CombinedOutput` contract (R7.1).
- Noop reporter ≡ pre-story behavior: existing autoupdate/manifest tests pass
  unchanged (R3.3).
- `go mod verify` passes; `go.sum` committed; no dependency < 7 days old (R7.3).
- No secret appears in any rendered frame, log event, or `TaskLine` (R7.2).
- `overlay manifest` parity goldens pass before the ANSI `TUIReporter` is deleted
  (R3.2).

---

## Traceability

| Requirement | Covered by |
|---|---|
| R1.1 live tail | 2.2, 3.2 |
| R1.2 `\r` in-place | 2.2, 3.2 |
| R1.3 per-worker slots | 2.3, 6.1 |
| R1.4 history + progress | 2.3 |
| R1.5 bounded tail | 2.2, 2.3 |
| R2.1 activate on TTY | 2.6, 4.1, 4.2 |
| R2.2 plain/no-ANSI fallback | 2.5, 2.6, 4.1, 4.2 |
| R2.3 stream tail in plain mode | 2.5 |
| R2.4 program-start failure → plain | 4.1 |
| R3.1 single Reporter interface | 2.1, 2.5 |
| R3.2 one system, ANSI removed | 6.1 |
| R3.3 Noop ≡ today | 3.1, 5.2 |
| R4.1 release TTY for sudo | 2.6, 3.2 |
| R4.2 in-UI confirm | 2.6 |
| R4.3 fixer spinner only | 3.1 |
| R5.1 Ctrl-C cancels + restores | 2.4, 4.1, 7.1 |
| R5.2 orphan rollback on cancel | 4.1 |
| R6.1 apply wired first | 3.1, 4.1 |
| R6.2 remaining sites | 5.1, 5.2, 6.1 |
| R7.1 captured output preserved | 2.2, 3.2, 5.1 |
| R7.2 no secrets rendered | 7.* (gate) |
| R7.3 supply chain | 1.1, 7.1, 7.2 |
| R7.4 goroutine-safe ingress | 2.5, 7.1 |
