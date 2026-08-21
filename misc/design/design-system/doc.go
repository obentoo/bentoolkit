// Package designsystem is the catalogue of everything the bentoo terminal UI
// draws, and the single place its visual vocabulary is defined.
//
// # Why this exists
//
// Before it, bentoo had TWO visual grammars that did not know about each other:
//
//   - internal/common/output, built on fatih/color: twelve named colors, the
//     glyphs ✓ ✗ ⚠ →, and a Box helper. Consumed by 24 command files.
//   - internal/common/tui, built on lipgloss: six styles hardcoded inside
//     model.go against raw ANSI numbers ("1", "2", "4", "8"). Consumed by the
//     live view of exactly one command.
//
// Nothing made "success" the same thing in both. On top of that, 305 direct
// fmt.Print* calls bypassed both grammars entirely, and the glyph inventory had
// drifted far enough to spell the same symbol two ways — 24 uses of ✓ and one
// lone ✔.
//
// # Why it lives under misc/
//
// Because it is a PROPOSAL that compiles, not a migration. Wiring it into the
// commands means touching those 305 print sites and changing output that
// scripts already consume; that is a decision to take deliberately, per
// command, not as a side effect of adding a package. Living here means the
// catalogue can be read, run and argued with before anything downstream moves.
//
// It is deliberately NOT behind a build tag. This repository already learned
// what that costs: the browser-driven script evaluators sit behind chromedp and
// playwright tags, so `go build ./...` skips them entirely, and a dependency
// bump once passed CI fully green while breaking the only code that called it
// (.github/workflows/ci.yml, the "Build and vet the tagged script evaluators"
// step exists because of that). Untagged means every change to lipgloss,
// bubbletea or this package's own callers is compiled and vetted by the normal
// CI run.
//
// # The shape
//
//   - theme     — the vocabulary: modes, semantic palette, glyph sets, styles.
//   - component — the shapes: one type per thing the commands actually print.
//   - gallery   — `go run ./misc/design/design-system/gallery`, which renders
//     every component in every mode so the catalogue can be SEEN.
//
// # Promotion path
//
// When a component earns its way into production it moves to
// internal/common/ui, keeping its tests. theme moves to internal/common/theme
// and becomes what internal/common/output and internal/common/tui both read,
// which is the point of the whole exercise: one definition of "success",
// rendered three ways, instead of two definitions that happen to agree.
package designsystem
