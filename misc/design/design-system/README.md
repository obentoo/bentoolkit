# bentoo design system

The catalogue of everything the bentoo terminal UI draws, and the single place
its visual vocabulary is defined.

```
go run ./misc/design/design-system/gallery          # the mode your terminal gets
go run ./misc/design/design-system/gallery -all     # all three modes, stacked
go run ./misc/design/design-system/gallery | cat    # watch it degrade, exactly as a command does
```

---

## The problem this solves

bentoo had **two visual grammars that did not know about each other**:

| | `internal/common/output` | `internal/common/tui` |
|---|---|---|
| Base | `fatih/color` | `lipgloss` |
| Vocabulary | 12 named colours, `✓ ✗ ⚠ →`, a `Box` helper | 6 styles hardcoded in `model.go` against raw ANSI numbers `"1" "2" "4" "8"` |
| Consumed by | **24 command files** | the live view of **1** command |

Nothing made "success" the same decision in both. `output.Success` is fatih green;
`tui.styleOK` is ANSI `"2"`; neither reads the other.

On top of that:

- **305 direct `fmt.Print*` calls** across `cmd/` and `internal/` bypass both grammars.
- The glyph inventory has drifted far enough to spell one symbol two ways: **24 uses
  of `✓` and one lone `✔`**.
- `─` appears **572 times**, every run built by hand, so no two separators are
  reliably the same length.
- `overlay_prune.go` reaches for `"%-45s %s"` — a column width picked once, by hand,
  that misaligns the moment an atom exceeds it.
- `output.Box` closes with a **fixed sixteen dashes** regardless of how wide it
  opened, so the panel almost never squares up.

Every component below was read out of those call sites. None was invented.

---

## Three modes, not two

The obvious split is "plain or pretty". It is wrong, and `internal/common/tui`
already contains the evidence: `Enabled()` turns the live UI off for **three**
distinct reasons — stdout is not a terminal, `NO_COLOR` is set, `BENTOO_NO_TUI` is
set — and only the first says anything about what the receiving end can **display**.

`NO_COLOR` is a statement about colour. It is not a statement about UTF-8.

| Mode | Assumes | For |
|---|---|---|
| `Plain` | **nothing** — ASCII only, no escape sequences | a pipe, a script, a CI log, a file, a non-UTF-8 locale |
| `Unicode` | a modern terminal, no colour | `NO_COLOR` on a TTY |
| `Styled` | both | an ordinary interactive terminal |

`Plain` is the **zero value** on purpose. A theme nobody configured, or a component
rendered by code that forgot to pick a mode, produces the output that is safe
everywhere rather than the one that is prettiest here.

`theme.ModeFor(isTTY, noColor)` is the one place the decision is made, and it takes
the same inputs `tui.Enabled` does so the two cannot disagree about one terminal.

### Why JSON is not a mode

A JSON render would have to invent a schema per component, and that schema would
drift from the text the moment somebody reworded a line — two representations of
one fact, kept in sync by nobody.

So a component is a **typed value with `json` tags**. `theme.Mode` picks between two
text faces; `encoding/json` serves the third consumer straight off the same struct.
`--json` cannot disagree with the human output about what happened, because it is
reading the same fields.

---

## Roles, not colours

A role is what text **means**. Naming one "green" is how the two grammars drifted
apart.

| Role | Meaning | Light / Dark |
|---|---|---|
| `OK` | succeeded | `2` / `10` |
| `Fail` | failed, needs a human | `1` / `9` |
| `Warn` | degraded, recovered on its own | `3` / `11` |
| `Info` | progress and transitions | `6` / `14` |
| `Skip` | deliberately not done — **not** a failure | `8` / `8`, faint |
| `Accent` | the subject of a line: an atom, a path, an id | `4` / `12` |
| `Heading` | a section title | `0` / `15`, bold |
| `Muted` | context that must not compete | `8` / `8`, faint |

**Basic ANSI numbers rather than hex, on purpose.** They inherit whatever palette
the operator configured in their terminal, so bentoo's green is *their* green. A hex
value would override a carefully themed terminal to impose ours.

**`AdaptiveColor` rather than a fixed number.** The Light/Dark pairs are the normal
(1–7) and bright (9–15) halves of one hue. `tui/model.go` hardcodes the normal half
only, which is tuned for one background and washes out on the other.

---

## The catalogue

Rendered in `Plain` — the mode that assumes nothing. Run the gallery for the others.

```
[OK]   media-plugins/gst-plugins-qt6-1.29.2 -- manifest regenerated
version: 1.29.1 -> 1.29.2
(none: every package is current)
-- Validation ------------------------------------------
+- Staged tree -----------------------------------------
|  ~/.config/bentoo/autoupdate/staging
|  shared with --apply and validate --depth
+-------------------------------------------------------
files (3):
  metadata.xml
  gst-plugins-qt6-1.29.2.ebuild
  Manifest
registry entries (0):
  (none)
[INFO] media-plugins -- 1 package
  [WARN] gst-plugins-qt6 -- would be removed
    [SKIP] versions: 1.28.0, 1.29.1
    [SKIP] files: 3
PACKAGE                        DEPTH      REASON
media-plugins/gst-plugins-qt6  install    series bump
dev-libs/foo                   configure  revision bump
app-editors/zed                options    patch bump
depth reached: install
gates:         4 of 4
staged at:     ~/.config/bentoo/autoupdate/staging
[OK]   patches   -- src_prepare completed
[OK]   configure -- src_configure completed
[OK]   compile   -- src_install not covered
[OK]   install   -- qmerge and src_test not covered
[SKIP] qa        -- pkgcheck crashes on this overlay's git history
21 ebuilds: 3 failed, 17 passed, 1 skipped
Checking [45 %] 9/20 ##########..............
Scanning 137 so far
Apply this bump? [y/N]
Apply this bump? [y/N/a/q]
Publish the realignment? [y]es / [e]dit / [c]ancel
```

| Component | Replaces |
|---|---|
| `Status` | `output.PrintSuccess` / `PrintError` / `PrintWarning` / `PrintInfo` — four near-identical `Printf` wrappers that print as a side effect, so nothing can assert on them |
| `Transition` | every version bump, rename and realignment line, each spelling `→` literally |
| `Empty` | `snapshot_list.go`'s `"  (none)"`; several commands print nothing at all, which reads as a bug rather than an answer |
| `Rule` | 572 hand-built runs of `─` |
| `Box` | `output.Box`, whose bottom edge is a fixed 16 dashes |
| `Group` | `overlay_prune.go`'s `"files (%d)"` / `"registry entries (%d)"` plus their indent loops |
| `Tree` | `overlay_prune.go` descending 2 → 4 → 6 → 8 spaces with a separate `Printf` per level, so the structure lives in format strings instead of in data |
| `Table` | `"%-45s %s"` |
| `KV` | every `"    %s: %s\n"`, whose colon column drifts per call site |
| `Gates` | the per-gate verdict list a validation run reports |
| `Tally` | `overlay_validate.go`'s `"\n%d ebuilds: %d failed, %d passed, %d skipped\n"` |
| `Progress` | `overlay_compare.go`'s `"\r  Checking: [%3d%%] %d/%d"` and its 66-space erase line |
| `Prompt` | `"[y/N]"`, `"[y/N/a/q]"` and `"[y]es / [e]dit / [c]ancel"`, spelled by hand in at least seven places |

---

## Decisions worth knowing about

**`Confirm` defaults to no.** Not a style choice. Every caller of this shape guards
something that writes — a bump being applied, a package removed, an overlay that
auto-commits and **pushes** within minutes. A bare Enter must never be the answer
that acts. Asserted in `TestConfirm_DefaultsToNo`.

**`Outcome`'s zero value is `Skipped`, not `Passed`.** The validation ladder's
central rule is that a gate which could not run reports SKIPPED and never PASSED. A
zero value meaning "passed" would break it by default.

**`Progress` renders text and nothing else.** The carriage return, the erase and the
redraw cadence belong to whatever drives the terminal — `tui.Reporter`'s plain
backend already throttles in-place updates per task id, and its bubbletea backend
redraws whole frames. A component emitting `"\r"` itself would be unusable inside
bubbletea and untestable everywhere.

**`Prompt` renders the question; it does not read the answer.** Reading needs the
real terminal, and `tui.RunAttached` already owns handing the TTY to something that
reads from it.

**Zero counts are shown by default.** `0 failed` is information. A summary that
silently omits the bucket a reader was looking for reads as if the run never checked.

**A glyph is content, not decoration.** `✓` and `[OK]` carry the same fact, so the
status tokens survive into `Plain` — only the escape sequences are dropped.

---

## What the mode contract covers, and what it cannot

`TestEveryComponent_HonoursTheModeContract` asserts over the **whole catalogue**:

- `Plain` — pure ASCII, no escape sequences
- `Unicode` — no escape sequences
- `Styled` — escape sequences present

The first two are the ones that matter: they are promises to a consumer that cannot
answer back.

**It does not cover the caller's words, and cannot.** A component chooses its glyphs
from the theme, but a label, a reason or a note is a string handed in from outside,
and an em dash typed there reaches `Plain` untouched. This test caught exactly that
in its own catalogue on first run. Transliterating caller text was considered and
rejected: it would mangle the package atoms, paths and upstream error text that make
up most of what gets passed in.

### `theme.NewFor` exists to stop a test passing vacuously

lipgloss detects its colour profile from the writer it was built on. A test captures
output into a buffer, a buffer is not a terminal, and lipgloss therefore degrades to
the Ascii profile and emits **nothing** — so a golden test asserting that `Styled`
output carries colour would compare `""` with `""` and pass having measured nothing.

Measured, against this tree:

```
unpinned      -> "boom"                      (ESC: false)
pinned Ascii  -> "boom"                      (ESC: false)
pinned ANSI   -> "\x1b[91mboom\x1b[0m"       (ESC: true)
```

`91` is bright red, which also confirms `SetHasDarkBackground(true)` resolved
`AdaptiveColor` to its Dark half. Verified end to end in a real 256-colour pty: all
seven roles resolve to distinct SGR codes.

---

## Why it lives under `misc/`

Because it is a **proposal that compiles**, not a migration. Wiring it into the
commands means touching those 305 print sites and changing output that scripts
already consume; that is a decision to take deliberately, per command, not as a side
effect of adding a package.

It is deliberately **not** behind a build tag. This repository already learned what
that costs: the browser-driven script evaluators sit behind `chromedp` and
`playwright` tags, so `go build ./...` skips them, and a dependency bump once passed
CI fully green while breaking the only code that called it — the
`.github/workflows/ci.yml` step that builds and vets both tags exists because of
that. Untagged means every change to lipgloss, bubbletea or this package's callers
is compiled and vetted by the normal CI run.

`Catalogue()` has exactly two consumers — the contract test and the gallery — so the
picture an operator looks at and the assertions CI runs cannot drift apart.

## Promotion path

When a component earns its way into production it moves to `internal/common/ui`,
keeping its tests. `theme` moves to `internal/common/theme` and becomes what
`internal/common/output` and `internal/common/tui` both read — which is the point of
the whole exercise: one definition of "success", rendered three ways, instead of two
definitions that happen to agree.

## Not decided here

- **Which commands migrate, and in what order.** Every component has both faces, so
  no command is excluded by policy; the sequencing is a separate call.
- **Whether `output.Print*` keeps its `✓` in Plain.** This package says a piped
  consumer gets `[OK]`; today it gets `✓`. That is a behaviour change wherever it
  lands, and it is why nothing is wired yet.
- **Text wrapping.** The gallery has a naive wrapper for its own prose. A real one
  belongs in the catalogue only once a command needs it.
- **A spinner.** `bubbles/spinner` is already a dependency and already used; framing
  it as a component means deciding how a Plain-mode spinner behaves, which is a
  question about the reporter's cadence, not about a shape.
