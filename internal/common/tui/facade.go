package tui

import (
	"context"
	"io"
	"time"
)

// New builds a TUI Program with a fresh model bound to ctx and returns it
// together with a Reporter that forwards to it. Ctrl-C invokes cancel — the
// caller's existing cancellation chain — because NewProgram injects cancel into
// the model (AD9). out/in are the program's terminal streams (os.Stdout/os.Stdin
// in production); callers may pass other streams in tests.
//
// This is the exported entry point for apply drivers in other packages: the
// model/reporter constructors are unexported, so the driver cannot assemble the
// pair itself.
func New(ctx context.Context, cancel func(), out io.Writer, in io.Reader) (*Program, Reporter) {
	p := NewProgram(ctx, cancel, newModel(), out, in)
	return p, newTeaReporter(p)
}

// NewPlainReporter is the exported constructor for the non-TTY / opt-out backend.
// It writes deterministic, ANSI-free lines to w; throttle rate-limits in-place
// tail updates per task id (0 disables it). See newPlainReporter for the line
// shapes (R2.2/R2.3).
func NewPlainReporter(w io.Writer, throttle time.Duration) Reporter {
	return newPlainReporter(w, throttle)
}
