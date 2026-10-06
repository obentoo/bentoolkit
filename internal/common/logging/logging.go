// Package logging builds bentoo's diagnostics logger on log/slog.
//
// One logger is built per invocation by the root command and handed to every
// component that emits diagnostics — through the command's context
// (NewContext/FromContext) or a component's constructor options — never
// through package-level state. It writes key=value lines to stderr and JSON
// lines to $XDG_STATE_HOME/bentoo/logs/bentoo.log, and every value
// secrets.Lookup resolved is scrubbed from both by NewRedactingHandler.
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

// FileName is the log file's name inside the log directory.
const FileName = "bentoo.log"

// Options configures New.
type Options struct {
	// Stderr receives the key=value lines at Level and above.
	Stderr io.Writer
	// LogDir is the directory holding FileName; "" writes no file.
	LogDir string
	// Level is the terminal level; the file level is FileLevel(Level).
	Level slog.Level
	// Resolved returns the secret values to scrub; nil means none.
	Resolved func() []string
}

// New builds the invocation logger: a text handler on opts.Stderr at
// opts.Level without the top-level time attribute and, when opts.LogDir is not
// empty, a JSON handler appending to LogDir/bentoo.log at FileLevel(Level),
// both behind one redacting handler. The returned func closes the log file; it
// may be called more than once.
//
// When the directory or file cannot be created or opened, New still returns a
// usable stderr-only logger and a no-op close func, together with an error
// naming the path and the cause; the caller reports it.
func New(opts Options) (*slog.Logger, func() error, error) {
	text := slog.NewTextHandler(opts.Stderr, &slog.HandlerOptions{
		Level: opts.Level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	})
	stderrOnly := func() *slog.Logger { return slog.New(NewRedactingHandler(text, opts.Resolved)) }
	noop := func() error { return nil }

	if opts.LogDir == "" {
		return stderrOnly(), noop, nil
	}
	if err := os.MkdirAll(opts.LogDir, 0o750); err != nil {
		return stderrOnly(), noop, fmt.Errorf("creating log directory %s: %w", opts.LogDir, err)
	}
	path := filepath.Join(opts.LogDir, FileName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // G304: path is FileName under the log directory the caller chose (XDG_STATE_HOME or ~/.local/state)
	if err != nil {
		return stderrOnly(), noop, fmt.Errorf("opening log file %s: %w", path, err)
	}
	file := slog.NewJSONHandler(f, &slog.HandlerOptions{Level: FileLevel(opts.Level)})

	// The redactor sits outside the multi-handler so both sinks receive the
	// same scrubbed record.
	l := slog.New(NewRedactingHandler(slog.NewMultiHandler(text, file), opts.Resolved))

	var (
		once     sync.Once
		closeErr error
	)
	closeFn := func() error {
		once.Do(func() {
			if err := f.Close(); err != nil {
				closeErr = fmt.Errorf("closing log file %s: %w", path, err)
			}
		})
		return closeErr
	}
	return l, closeFn, nil
}

// DefaultLogDir returns $XDG_STATE_HOME/bentoo/logs, or
// ~/.local/state/bentoo/logs when XDG_STATE_HOME is unset or empty.
func DefaultLogDir() (string, error) {
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolving the log directory: %w", err)
		}
		state = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(state, "bentoo", "logs"), nil
}

// contextKey is the context key under which the invocation logger is stored.
type contextKey struct{}

// NewContext returns a copy of ctx carrying l.
func NewContext(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, contextKey{}, l)
}

// FromContext returns the logger NewContext stored in ctx, or a logger that
// discards everything when none was stored.
func FromContext(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(contextKey{}).(*slog.Logger); ok && l != nil {
		return l
	}
	return OrDiscard(nil)
}

// OrDiscard returns l, or a logger that discards everything when l is nil.
func OrDiscard(l *slog.Logger) *slog.Logger {
	if l != nil {
		return l
	}
	return slog.New(slog.DiscardHandler)
}
