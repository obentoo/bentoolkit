package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/obentoo/bentoolkit/internal/common/report/render"
)

// validateGitPathArgs rejects arguments that look like git flags.
// Only file/directory paths are accepted as positional arguments.
func validateGitPathArgs(args []string) error {
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			return fmt.Errorf("invalid argument %q: only file paths are accepted as positional args", arg)
		}
	}
	return nil
}

// signalContext derives a context that is cancelled when the process receives
// SIGINT, SIGTERM or SIGHUP, so an in-flight command aborts cleanly within ~2 s
// (R3.1).
//
// SIGHUP is here because of process groups (story 054, R4.4). The kernel sends
// a terminal's Ctrl+C and its hang-up — an SSH session dropping, a terminal
// window closed — to the terminal's FOREGROUND group only, and the children
// bentoo runs in their own group (the `claude` CLI, pkgdev, the unprivileged
// ebuild) are outside it. They receive neither signal, so this context is what
// stops them: without SIGHUP in the list a hang-up killed bentoo by its default
// action and left those children running, orphaned. Catching it turns the
// hang-up into the same clean cancel as a Ctrl+C. syscall.SIGHUP is defined on
// every Unix and on Windows (where nothing ever sends it), so the list needs no
// build-tagged split.
//
// OQ-1: cmd.Context() is NOT signal-aware on its own — main.go uses
// rootCmd.Execute() (not ExecuteContext) and never wires signal.NotifyContext.
// Cobra's Execute path guarantees a non-nil context.Background(), but unit
// tests call the run* functions directly with a freshly built command whose
// ctx is still nil; signal.NotifyContext panics on a nil parent. The nil guard
// below falls back to context.Background() so both paths are safe.
//
// The caller MUST defer the returned stop function to release the signal
// handler. This mirrors the signal.NotifyContext pattern used in runManifest
// (AD-1).
func signalContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
}

// padColumn lays value into a column that is cells display columns wide, so the
// column after it starts in the same place on every row.
//
// It is the other half of render.ColumnWidth, and the two are always used
// together: the first says how wide a column has to be to hold the values THIS
// run produced, this one puts one value into that column. A caller that
// measured a width and then padded by some other rule would have measured
// nothing.
//
// # Why not %-*s, which fmt already offers
//
// Because the two would be counting different things. fmt pads to a RUNE count
// (fmt/format.go pads with utf8.RuneCount), and a column here is measured in
// display CELLS — the only unit a terminal aligns on. "→" is three bytes, one
// rune and one cell; "日" is three bytes, one rune and TWO cells. The two units
// agree on every ASCII value and part company on the first one that is not,
// which is a misalignment that appears in someone's package list and in no test
// that was written before it. Measuring the value with the same function that
// measured the column removes the second unit entirely (R6.1).
//
// A column holding a single value is exactly as wide as that value, so
// render.ColumnWidth([]string{value}) IS its cell width — computed by the code
// that sized the column it is being laid into, so the two cannot disagree.
// internal/common/report/render keeps its own unexported pad for this reason
// and states it the same way; this is that function on the command side of the
// boundary, where the report's renderers cannot be called because these
// commands print their own text.
//
// # It pads and never cuts
//
// A value at or over the width comes back byte for byte. Cutting is
// render.Shorten's job and a separate decision: it needs a budget to cut
// AGAINST — the width of the terminal — and a column measured from its own
// values has no opinion about that. A caller that acquires such a budget calls
// Shorten before this, not instead of it.
func padColumn(value string, cells int) string {
	if missing := cells - render.ColumnWidth([]string{value}); missing > 0 {
		return value + strings.Repeat(" ", missing)
	}
	return value
}
