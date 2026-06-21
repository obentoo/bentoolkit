---
story: tui-live-output
type: feature
scale: full
version: 1
created: 2026-06-21
---

# Live TUI Output (Bubble Tea)

## Summary

`bentoo` runs long external processes but shows the user almost nothing while
they run. The sharpest case is `bentoo overlay autoupdate --apply [all]`: after
a single `Applying update for <pkg>...` line the terminal freezes for minutes
while `runManifest` buffers a multi-hundred-MB `pkgdev`/`wget` fetch with
`cmd.CombinedOutput()` and surfaces output **only on failure**
(`internal/autoupdate/applier.go:815-817`). The project already has a hand-rolled
ANSI progress reporter (`internal/overlay/manifest_reporter.go`) used by
`overlay manifest`, but it does not stream a live tail either — the child's
output reaches it only at completion (`internal/overlay/manifest.go:336`).

This story introduces a single Bubble Tea-based TUI foundation
(`internal/common/tui`), renders live per-operation progress **and a bounded tail
of the running subprocess** (the "what is downloading right now" view), replaces
the hand-rolled ANSI reporter so there is one TUI system, and wires it first into
the autoupdate apply path and then across the remaining external-command sites. It
degrades automatically to plain streaming text when stdout is not a TTY or the
user opts out.

The architecture (framework choice, stdout ownership, streaming/interactive seams,
supply-chain posture) is fixed in [design.md](design.md); the requirements below
derive from those decisions (AD1-AD10).

## User Story

As a Bentoo overlay maintainer running `bentoo overlay autoupdate --apply all`,
I want to see live progress and a tail of each package's manifest fetch and
compile, so that I can tell the tool is working (and what it is doing) instead of
staring at a frozen terminal for minutes — while keeping clean logs when I pipe
the command into CI.

## Requirements

### R1. Live progress and bounded tail

- **R1.1.** WHEN a long-running subprocess (`pkgdev manifest`, the compile step,
  `git clone`/`git fetch`) runs under an interactive TTY, THE SYSTEM SHALL display
  a live tail of that subprocess's combined stdout/stderr, updated as output is
  produced rather than only at completion.
- **R1.2.** WHEN a subprocess emits a carriage-return (`\r`) progress update, THE
  SYSTEM SHALL replace the tail's current line in place instead of appending a new
  line.
- **R1.3.** WHILE several operations run concurrently (the manifest path's
  parallel workers), THE SYSTEM SHALL render one live slot per worker and SHALL
  NOT interleave partial lines from different workers.
- **R1.4.** WHEN an operation completes, THE SYSTEM SHALL render a ✓/✗ history
  line with a one-line summary and advance an overall progress indicator.
- **R1.5.** THE SYSTEM SHALL bound the live tail to a fixed number of recent lines
  (default approximately 10) such that memory use remains flat regardless of total
  subprocess output volume.

### R2. Activation and fallback

- **R2.1.** WHEN stdout is an interactive TTY AND none of `--no-tui`, `NO_COLOR`,
  or `BENTOO_NO_TUI` is set, THE SYSTEM SHALL render the Bubble Tea TUI.
- **R2.2.** WHEN stdout is not a TTY OR any opt-out (`--no-tui`, `NO_COLOR`,
  `BENTOO_NO_TUI`) is set, THE SYSTEM SHALL use plain streaming output and SHALL
  NOT emit ANSI escape sequences.
- **R2.3.** WHEN in plain mode, THE SYSTEM SHALL still stream the subprocess tail
  to stderr, rate-limited, with `\r` in-place updates collapsed to full lines
  (OQ1 resolution).
- **R2.4.** WHEN the TUI program fails to start, THE SYSTEM SHALL log the failure
  once and continue the operation in plain mode without aborting the work.

### R3. Single reporter abstraction

- **R3.1.** Command code SHALL emit progress through one `Reporter` interface and
  SHALL NOT reference the TUI framework directly.
- **R3.2.** THE SYSTEM SHALL provide exactly one terminal-UI implementation: the
  hand-rolled ANSI `TUIReporter` SHALL be removed and `overlay manifest` SHALL
  render through the same Bubble Tea system (OQ3 resolution).
- **R3.3.** WHEN no reporter is supplied (the Noop default), THE SYSTEM SHALL
  behave exactly as before this story — silent, fully-buffered execution.

### R4. Interactive children

- **R4.1.** WHEN the compile step requires `sudo`/`doas`, THE SYSTEM SHALL release
  the terminal so the password prompt is displayed and answered on the real TTY,
  then restore the UI.
- **R4.2.** WHEN the apply requests compile confirmation, THE SYSTEM SHALL present
  the yes/no prompt within the active UI and read the response without reading
  `os.Stdin` behind the program.
- **R4.3.** THE SYSTEM SHALL represent the agentic `claude` fixer as a labeled
  spinner with elapsed time and SHALL NOT release the terminal for it.

### R5. Cancellation

- **R5.1.** WHEN the user presses Ctrl-C while the TUI is active, THE SYSTEM SHALL
  cancel the in-flight operation's context — terminating child processes — and
  restore the terminal within 2 seconds, consistent with story 002 R1.1/R1.2.
- **R5.2.** WHEN an apply is cancelled after the ebuild copy but before
  completion, THE SYSTEM SHALL run the existing orphan rollback so no
  half-applied `.ebuild` remains.

### R6. Rollout scope

- **R6.1.** THE SYSTEM SHALL wire live output into `autoupdate --apply` and
  `--apply all` first (the spine).
- **R6.2.** THE SYSTEM SHALL then wire the remaining subprocess sites
  (`overlay manifest`, `internal/common/git/runner.go`,
  `internal/common/provider/gitclone.go`, `internal/snapshot/runner.go`) with at
  least stage/done events, adding a live tail WHERE the child streams meaningful
  progress (downloads: clone/fetch/manifest).

### R7. Preserved invariants and quality

- **R7.1.** THE SYSTEM SHALL preserve the full captured subprocess output for the
  error path (the manifest error string at `applier.go:817`; the compile log
  file) unchanged.
- **R7.2.** THE SYSTEM SHALL NOT render or log any secret — the bare-mode API key
  injection is untouched and only child stdout/stderr appear in the tail.
- **R7.3.** New dependencies SHALL be pinned to exact versions at least 7 days
  old, with `go.sum` committed and `go mod verify` passing.
- **R7.4.** The reporter ingress SHALL be goroutine-safe so the parallel manifest
  workers run `-race` clean.

## Constraints

- **C1.** New dependencies: `charmbracelet/bubbletea`, `charmbracelet/lipgloss`,
  `charmbracelet/bubbles` (justified in design AD1; supply-chain policy in R7.3).
- **C2.** Tooling: no browser E2E tooling applies — this is a Go CLI/TUI with no
  browser surface; `playwright`/`chrome-devtools`, though installed in the
  environment, are not used. TUI behavior is tested with
  `charmbracelet/x/exp/teatest` golden frames plus unit tests of the streaming and
  plain-mode seams.
- **C3.** Inline rendering only — no alt-screen (design AD2), so run history stays
  in the terminal scrollback and remains greppable.
- **C4.** Go 1.26 (existing toolchain); reuse the existing `output.IsTerminal()`
  detection as the basis for the activation gate.

## Acceptance Criteria

- Running `bentoo overlay autoupdate --apply <pkg>` in a real terminal shows a
  live tail of the `pkgdev`/`wget` fetch (bytes/percent advancing) and a
  per-package status, instead of a frozen line.
- Piping the same command (`… | tee log`) or setting `BENTOO_NO_TUI=1` produces
  plain output with **no** ANSI escape sequences, while still streaming the fetch
  tail to stderr (R2.3).
- Pressing Ctrl-C during a fetch kills the child within ~2 s, restores the
  terminal cleanly, and triggers the orphan rollback.
- A compile step prompts for the sudo password on the real TTY (visible,
  non-echoing) and resumes the UI afterward.
- `overlay manifest` renders through the new system with the same slots / ✓✗
  history / summary it shows today (parity verified by `teatest`), and the ANSI
  `manifest_reporter.go` TUIReporter is gone.
- With no reporter supplied, every existing autoupdate/manifest test passes
  unchanged (R3.3).

## Out of Scope

- Rendering *all* bentoolkit command output in the TUI (the long-term north star)
  — this story delivers the foundation plus the long-running subprocess paths.
- Changing apply (sequential) or manifest (parallel) concurrency models.
- A configurable tail height / full pager-style interactive navigation (OQ2
  deferred).
- Story 009 (manifest-fixer diagnostics) — independent.

## Assumptions

- `tea.ExecProcess` / `ReleaseTerminal`+`RestoreTerminal` reliably hand the TTY to
  a sudo child and restore it (verified on a real TTY during implementation;
  guarded by a mock-seam unit test for the release/restore calls).
- `program.Send` is safe to call from the manifest worker goroutines (documented
  Bubble Tea contract; asserted under `-race`).
