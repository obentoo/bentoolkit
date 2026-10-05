package main

import (
	"fmt"
	"os"
)

// This file is the output boundary for the lines a command prints AS ITS
// ANSWER on stderr (story 062, R6 and the Constraint "Boundary with
// internal/common/output").
//
// Before story 062 internal/common/logger printed the bare message to stderr,
// and several commands used it as a UI printer: the overlay init wizard, the
// status, rename, push, pull and commit result displays, the "Found N
// packages" count lines and the "use --clone" guidance. Those lines are not
// diagnostics. A user reads them as the command's answer, as a prompt, or as
// step-by-step guidance in an interactive flow, so they keep the shape they
// always had: bare text, one line per call (a multi-line block stays a block),
// hidden by --quiet exactly as the old logger hid Info and Warn, and kept out
// of bentoo.log — the log file is for diagnostics, and the wizard's "Using git
// config" line carries the user's email, which must never reach it.
//
// Everything that reports a failure, a degraded path, a skip or progress
// detail stays on the invocation's slog logger (logging.FromContext), and so
// does every error line that precedes a failing exit (the story 058/062
// boundary). Callers format their own text with fmt.Sprintf.

// uiInfo prints msg as a bare line on stderr, unless --quiet was given.
func uiInfo(msg string) { uiNote(msg) }

// uiWarn prints msg as a bare line on stderr, unless --quiet was given. It is
// the same stream and shape as uiInfo; the separate name keeps the old
// Info/Warn intent readable at the call site.
func uiWarn(msg string) { uiNote(msg) }

// uiNote writes msg and a newline to the stderr OF THE MOMENT: os.Stderr is
// read at call time, never captured at init, because tests swap it. quiet is
// the package variable the root's PersistentPreRunE publishes.
func uiNote(msg string) {
	if quiet {
		return
	}
	// A failed write to stderr has nowhere left to be reported.
	_, _ = fmt.Fprintln(os.Stderr, msg)
}
