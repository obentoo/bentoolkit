package tui

import (
	"os"

	"github.com/obentoo/bentoolkit/internal/common/output"
)

// Options carries the runtime inputs that gate the live TUI. NoTUI mirrors the
// --no-tui CLI flag value.
type Options struct {
	// NoTUI is the --no-tui flag value; when true the TUI is suppressed and
	// command code falls back to plain output.
	NoTUI bool
}

// isTerminal is the package seam for the stdout-TTY stat. It defaults to the
// real probe in internal/common/output and is overridden by tests to fake the
// terminal status so Enabled's truth table is exercised without a real TTY.
var isTerminal = output.IsTerminal

// Enabled is the ONE place the live-TUI decision is made (AD7). The TUI is used
// iff stdout is a terminal AND none of the opt-outs is set: the --no-tui flag,
// the NO_COLOR convention, or BENTOO_NO_TUI. An UNSET env var is the empty
// string; per the NO_COLOR convention an empty value means "not set", so only a
// non-empty value counts as an opt-out (R2.1/R2.2). The TTY probe goes through
// the isTerminal seam so the decision is testable.
func Enabled(o Options) bool {
	if o.NoTUI {
		return false
	}
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("BENTOO_NO_TUI") != "" {
		return false
	}
	return isTerminal()
}
