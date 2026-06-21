---
story: tui-live-output
type: feature
scale: full
version: 1
created: 2026-06-21
---

# Design — Live TUI Output (Bubble Tea)

## 1. Overview

`bentoo` runs long external processes (`pkgdev manifest` fetching hundreds of MB
of distfiles, `ebuild … compile`, `git clone`, the agentic `claude` fixer) and
today gives the user almost no live feedback while they run. The worst case is
`bentoo overlay autoupdate --apply [all]`: after a single
`output.Info.Printf("Applying update for %s...\n", …)` line
(`cmd/bentoo/overlay_autoupdate.go:539,586`) the applier blocks for minutes
inside `runManifest`, which buffers everything with `cmd.CombinedOutput()`
(`internal/autoupdate/applier.go:815`) and only surfaces output **on error**. The
user sees a frozen terminal.

A second-order problem: the project already has a hand-rolled ANSI progress UI —
`overlay.ProgressReporter` (`internal/overlay/manifest.go:87`) with
`TUIReporter`/`LogReporter`/`NoopReporter`
(`internal/overlay/manifest_reporter.go`), TTY-gated at
`cmd/bentoo/overlay_manifest.go:159`. But even that reporter does **not** stream a
live tail: `runOneManifest` passes the *complete* buffered output to
`Reporter.Done(...)` only at completion (`internal/overlay/manifest.go:336,346`).
During a long fetch the worker slot shows a static `⟳ category/package` with no
byte-level progress.

This story introduces a **single Bubble Tea-based TUI foundation**
(`internal/common/tui`, new) that renders live per-operation progress and a
bounded tail of the running subprocess, replaces the hand-rolled ANSI reporter
(one system, not two), and is wired first into the autoupdate apply path (the
spine) and then across the remaining external-command sites. It degrades to plain
streaming text when stdout is not a TTY or the user opts out.

This is a **Design-First** story: the framework choice, the stdout-ownership
model, the streaming/interactive seams, and the dependency/supply-chain posture
are decided here; `story.md` derives its EARS requirements from these decisions.

## 2. Goals / Non-Goals

### Goals

- Live, per-operation progress for long subprocesses, with a **bounded tail** of
  the child's stdout/stderr (the "what is downloading right now" view).
- One TUI system: replace `overlay.ProgressReporter`'s hand-rolled ANSI
  implementation with the Bubble Tea foundation, preserving manifest's
  parallel-worker behavior (slots, ✓/✗ history, final summary).
- Wire the foundation into `autoupdate --apply` / `--apply all` (the spine) and
  then the remaining six subprocess sites (manifest, git runner, git clone,
  snapshot runner, the claude fixer's status) as follow-on tasks in this story.
- Correct behavior under: non-TTY (pipe/CI), `--no-tui`, `NO_COLOR`,
  `BENTOO_NO_TUI` → automatic plain-streaming fallback.
- Preserve every existing invariant: SIGINT cancellation, orphan rollback, the
  authoritative manifest re-check, the captured-output-on-error contract, and
  the sudo/`doas` password prompt during compile.

### Non-Goals

- **Not** rendering *all* of bentoolkit's command output in the TUI in this story.
  The user's stated end goal ("draw all bentoolkit responses in the TUI") is the
  north star the foundation is built toward, but converting every command's
  result rendering is out of scope here — this story delivers the foundation +
  the long-running subprocess paths.
- **Not** changing the autoupdate apply concurrency model (apply stays
  sequential; manifest stays N-worker parallel).
- **Not** building a full interactive dashboard (mouse, panes, scrollback
  navigation). The model is a progress view + tail + history, not a pager.
- **Not** touching story 009's manifest-fixer diagnostics work (independent).

## 3. Architecture Decisions

- **AD1 — Bubble Tea, replacing the hand-rolled reporter (user decision).** Adopt
  `charmbracelet/bubbletea` (+ `lipgloss` for layout/style, `bubbles` for
  `spinner`/`progress`) and reimplement `overlay.ProgressReporter` on it. Rationale
  over keeping the ANSI reporter (per the CLAUDE.md "justify a new dependency over
  existing code" rule): the existing code (a) cannot stream a live tail, (b) has
  no terminal-resize handling, and (c) cannot safely release the TTY for the sudo
  password prompt — all three are required here and are exactly what a tested
  event-loop framework provides. Consolidating on one framework also serves the
  stated goal of eventually rendering all output in the TUI. The hand-rolled ANSI
  in `manifest_reporter.go` is deleted, not left to rot beside a second system.

- **AD2 — Inline rendering, not alt-screen.** The program runs **without**
  `tea.WithAltScreen()`: the live block renders inline and finished history
  scrolls into the normal scrollback, matching the current `TUIReporter` UX and
  keeping output greppable after exit. Alt-screen (full-screen takeover) is
  rejected because bentoo is a batch CLI, not a dashboard, and alt-screen would
  erase the run's history on exit.

- **AD3 — A Reporter facade with two backends, gated once.** Command code never
  talks to Bubble Tea directly. It emits to a small `Reporter` interface (events
  in §5). Two implementations: `teaReporter` (forwards every event through
  `program.Send`, which is the documented goroutine-safe ingress) and
  `plainReporter` (streams lines to an `io.Writer`, the non-TTY fallback that
  replaces `LogReporter`). The backend is chosen once, at command entry, by the
  gating function in AD7. This keeps the applier/manifest code identical across
  TTY and non-TTY runs.

- **AD4 — Streaming capture seam replaces `CombinedOutput`.** Long subprocesses
  set `cmd.Stdout`/`cmd.Stderr` to an `io.MultiWriter(&captureBuf, lineEmitter)`.
  `captureBuf` preserves the existing "full output on error" contract
  (`applier.go:817`, `manifest.go:336`); `lineEmitter` splits the stream on `\n`
  **and** treats `\r` as an in-place line replacement (so wget/git progress bars
  update the tail's last line rather than flooding it) and emits a `TaskLine`
  event per update. The emitter is bounded (see AD8). This is the single change
  that turns silent buffering into a live tail; it is opt-in per call site.

- **AD5 — Release the terminal for interactive children.** The compile step needs
  sudo/`doas` to prompt for a password on the **real** TTY. Under the TUI this
  runs via `tea.ExecProcess(cmd, cb)` (or `program.ReleaseTerminal()` →
  run-attached → `RestoreTerminal()`), which hands the terminal to the child and
  restores the TUI afterward. The y/N compile confirmation
  (`defaultConfirmFunc`, `applier.go:930`) is rendered **in** the TUI as a key
  prompt rather than reading `os.Stdin` behind the program's back. The `claude`
  fixer needs **no** release: its stdout is captured JSON and it is
  non-interactive — it is represented by a labeled spinner with elapsed time.

- **AD6 — stdout belongs to the program; logger is routed, file-log untouched.**
  While a `teaReporter` is active, `logger`'s terminal writes
  (`internal/common/logger`, default `os.Stderr`) are redirected into the program
  as `Log` events so they appear in the TUI's scrollback instead of corrupting a
  frame; the **file** log (`~/.local/state/bentoo/logs/bentoo.log`) is unaffected.
  `output.Print*` calls on the active path are converted to reporter events. Calls
  outside an active TUI region behave exactly as today.

- **AD7 — One gating predicate, opt-out honored.** A single
  `tui.Enabled()`-style predicate decides plain-vs-Tea: TUI is used **iff**
  `output.IsTerminal()` is true AND `--no-tui` is unset AND `NO_COLOR` is unset
  AND `BENTOO_NO_TUI` is unset. Otherwise `plainReporter`. This generalizes the
  existing `output.IsTerminal()` switch at `overlay_manifest.go:159` and is the
  only place the decision is made.

- **AD8 — Bounded, throttled rendering.** The tail is a fixed-size ring buffer of
  the last N lines (default small, e.g. 8–12); history is appended, not retained
  in full in the model. `TaskLine` events are coalesced and the renderer is frame-
  rate limited (Bubble Tea's standard renderer batches), so a multi-hundred-MB
  wget dump (hundreds of thousands of dot-progress lines) cannot grow memory or
  saturate the terminal. The full text still lives in `captureBuf` for the error
  path only.

- **AD9 — SIGINT reconciled, not double-owned.** The program is bound to the
  caller's context (`tea.WithContext(ctx)`) and intercepts `Ctrl-C` to invoke the
  existing cancellation (the `signal.NotifyContext` chain that already cancels
  `a.ctx` and the child processes, `overlay_autoupdate.go:523-529`). The TUI quits
  only **after** the context is cancelled, so children are killed and the orphan
  rollback runs — no detached display, no orphaned `.ebuild`.

- **AD10 — Mockable program seam for tests.** Mirror the existing `execCommand`
  injection pattern (`applier.go:178`, `WithFixerExecCommand`): the tui package
  exposes a test harness (`teatest`) and the streaming seam is unit-testable with
  scripted commands, so no real TTY or real `pkgdev` is needed in CI.

## 4. Component Design

### 4.1 `internal/common/tui` (new package)

- **`model`** (`tea.Model`): holds header, an ordered set of **tasks** (each:
  id/label, stage string, optional progress fraction, spinner, terminal state),
  a bounded **tail ring** for the active task, a **history** list (✓/✗ + summary),
  and a **log** scrollback. `Init/Update/View` per Bubble Tea. `View` composes
  with `lipgloss`: header line, history (above), the live task block + tail
  (anchored), respecting `NO_COLOR`.
- **`Program`**: thin wrapper over `tea.NewProgram(model, tea.WithContext(ctx),
  tea.WithOutput(w), tea.WithInput(in))` providing `Start()/Wait()/Stop()` and
  `ReleaseTerminal/RestoreTerminal` passthrough. Not started when AD7 says plain.
- **Event/message types** (§5) and the **`Reporter`** interface with
  `teaReporter` (→ `program.Send`) and `plainReporter` (→ `io.Writer`).
- **`StreamCapture`**: builds the `io.MultiWriter(&buf, lineEmitter)` and returns
  the buffer (for the error path) + a `Close()` that flushes a trailing partial
  line. Handles `\n`/`\r` (AD4) and bounds throughput (AD8).
- **`RunAttached(cmd)` / `Confirm(prompt) bool`**: the interactive bridge (AD5).
- **`Enabled(opts)`**: the single gating predicate (AD7).

### 4.2 Migrate `overlay.ProgressReporter` (replace ANSI)

`internal/overlay/manifest_reporter.go`'s `TUIReporter` (hand-rolled ANSI) is
removed. `ProgressReporter` either (a) becomes a thin adapter that forwards
`Total/Start/Done/Finish` to the tui `Reporter`, or (b) is replaced at call sites
by the tui `Reporter` directly. `LogReporter` is superseded by `plainReporter`;
`NoopReporter` is kept (or replaced by a nil reporter). `runManifest`'s buffered
`pkgdev` exec gains the `StreamCapture` seam so each worker tails live. Worker
"slots" map to tui tasks keyed by worker index, preserving today's parallel UX.

### 4.3 Wire the applier (the spine)

- New option `WithApplierReporter(r tui.Reporter)` (nil = NoopReporter, preserving
  today's silent behavior and all existing tests).
- `Apply` emits the lifecycle: `TaskStart(pkg)` → `TaskStage("manifest")` +
  `StreamCapture` on `runManifest` → on failure `TaskStage("llm-fix")` (spinner) →
  `TaskStage("re-check")` → `TaskStage("compile")` via `RunAttached` →
  `TaskDone(ok, summary)`. The current `logger.Info` calls inside the apply path
  ("invoking LLM fixer", "authenticated fetch", "LLM fixer repaired",
  `applier.go:700,841,719`) become `Log`/`TaskStage` events.
- `runManifest` (`applier.go:811`) and `runCompile` (`applier.go:855`) swap
  `CombinedOutput()` for the `StreamCapture` seam (compile additionally uses
  `RunAttached` for the sudo prompt).

### 4.4 Wire the apply driver

`runApply`/`runApplyAll` (`cmd/bentoo/overlay_autoupdate.go:527,564`) build the
reporter via AD7, start the `Program` when enabled, pass
`WithApplierReporter(...)` into `NewApplier`, and convert the
`output.Info.Printf("Applying update for …")` lines into `TaskStart` events.
SIGINT wiring (AD9) reuses the existing `ctx`.

### 4.5 Remaining sites (follow-on tasks, same story)

`internal/common/git/runner.go`, `internal/common/provider/gitclone.go`,
`internal/snapshot/runner.go`: emit `TaskStage`/`TaskDone`; add `StreamCapture`
only where a live tail adds value (downloads: `git clone`, `git fetch`).
`internal/autoupdate/claude_code.go` / the fixer: labeled spinner + elapsed (no
tail — JSON-captured, non-interactive). Fast git ops (add/commit/reset) get
status events only.

## 5. Interfaces & Contracts

```
// Reporter is what command code emits to (both backends implement it).
type Reporter interface {
    BatchStart(total int)                 // optional header / denominator
    TaskStart(id, label string)
    TaskStage(id, stage string)           // "manifest", "llm-fix", "compile", …
    TaskProgress(id string, frac float64) // optional; <0 = indeterminate
    TaskLine(id string, stream Stream, text string) // the bounded tail
    TaskDone(id string, ok bool, summary, capturedOutput string)
    Log(level, text string)
    BatchDone(summary string)
}
```

- `teaReporter` forwards each call as a `tea.Msg` via `program.Send` (goroutine-
  safe; safe to call from manifest's N workers).
- `plainReporter` writes deterministic lines to `w` (mutex-guarded, like today's
  `LogReporter`): START/OK/FAIL + streamed tail lines prefixed with the task id.
- **Contract:** a nil/Noop reporter ≡ today's behavior. `capturedOutput` carries
  the full buffer for the error path so the `Output: %s` contract
  (`applier.go:817`) is preserved verbatim.

## 6. Error Handling & Fallback

- **Non-TTY / opt-out** (AD7): `plainReporter`, no ANSI, no program. Tests assert
  zero escape sequences when stdout is not a TTY.
- **Program start failure**: if `tea.NewProgram` cannot start (e.g. terminal
  probe fails), log once and fall back to `plainReporter` for the rest of the run
  — never abort the actual work for a UI failure.
- **Subprocess error**: unchanged semantics — `captureBuf` is surfaced exactly as
  today (manifest error string, compile log file); the tail is cosmetic.
- **Panic isolation**: a render panic must not crash the apply; the program runs
  in its own goroutine and a recover routes back to plain output.
- **API keys / secrets**: the fixer's bare-mode key injection is untouched; no
  child env or argv is ever rendered. `TaskLine` carries only child stdout/stderr.

## 7. Non-Functional Requirements

- **Dependencies (supply chain, per CLAUDE.md).** Add `bubbletea`, `lipgloss`,
  `bubbles` (charmbracelet). Justify each over stdlib/existing code in AD1. Pin
  exact versions; use only releases **≥7 days old**; `go mod verify`; commit
  `go.sum`; install frozen. Note transitive surface (`muesli/termenv`,
  `mattn/go-isatty` — already present via `fatih/color`). Verify current stable
  versions at implementation time via context7 / the module proxy.
- **Performance** (AD8): bounded tail ring; coalesced/throttled frames; memory
  flat regardless of subprocess output volume.
- **Compatibility**: VT100-ish terminals; honor `NO_COLOR`; no alt-screen (AD2).
- **Concurrency**: reporter ingress is goroutine-safe (manifest workers); `-race`
  clean.
- **Cancellation**: Ctrl-C within ~2 s kills children and restores the terminal
  (AD9), matching story 002's R1.1/R1.2 timing.

## 8. Tooling Decisions

- **E2E browser tooling: none.** `playwright`/`chrome-devtools` are available in
  the environment but **not applicable** — this is a Go CLI/TUI with no browser
  surface. Recorded as a Constraint in `story.md`.
- **TUI testing: `teatest`** (`charmbracelet/x/exp/teatest`) for golden-frame
  tests of the model, plus unit tests of `StreamCapture` (scripted commands,
  `\n`/`\r` handling, bounding) and the `plainReporter` (deterministic lines). The
  interactive `RunAttached`/sudo path is verified manually on a real TTY and
  guarded by a non-TTY unit test that asserts the release/restore calls via the
  mock seam (AD10).
- **Library docs:** use **context7** for `bubbletea`/`lipgloss`/`bubbles` API and
  version specifics during implementation (preferred over memory for library
  APIs).

## 9. Risks & Mitigations

- **R-stdout-contention** — stray `output.Print*`/`logger` writes corrupt a frame.
  *Mitigation:* AD6 routing; a lint/grep audit of writes on the active path; tests
  that scan rendered output for interleaving.
- **R-sudo-prompt** — password prompt invisible/!echoing under the TUI.
  *Mitigation:* AD5 terminal release via `tea.ExecProcess`; manual real-TTY
  verification; keep compile runnable in plain mode.
- **R-nontty-regression** — ANSI leaks into CI logs/pipes. *Mitigation:* strict
  AD7 gate; golden tests asserting no escapes in plain mode.
- **R-reporter-migration** — replacing the working manifest reporter regresses its
  UX. *Mitigation:* parity checklist (slots, ✓/✗ history, summary line); `teatest`
  goldens before deleting the ANSI code; land migration behind the same TTY gate.
- **R-signal-double-handling** — Ctrl-C quits TUI but leaves children running.
  *Mitigation:* AD9 (`tea.WithContext` + intercept → cancel, quit after).
- **R-dep-risk** — new third-party surface. *Mitigation:* §7 supply-chain policy;
  charmbracelet is widely used and audited; Trivy/OSV scan of `go.sum` if present.

## 10. Resolved Questions

- **OQ1 — RESOLVED: stream the tail in plain mode too.** `plainReporter` streams
  the subprocess tail to stderr even in non-TTY/`--no-tui` mode, rate-limited,
  with `\r` in-place updates collapsed to full lines. This honors the original
  ask (seeing the download progress) in pipes/CI as well, not only under a TTY.
- **OQ2 — RESOLVED: fixed small default tail height (~10 lines), not configurable
  yet.** A `--tui-tail-lines`/config knob is deferred to a follow-up.
- **OQ3 — RESOLVED: migrate `overlay manifest` in this story.** The hand-rolled
  ANSI `TUIReporter` is removed and manifest renders through the Bubble Tea
  system as the final rollout task, behind the AD7 gate and verified for UX
  parity with `teatest` goldens before the ANSI code is deleted.
