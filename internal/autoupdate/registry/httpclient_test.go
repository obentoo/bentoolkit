package registry

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
)

// =============================================================================
// Header Env-Var Expansion Allow-List Tests (Task T5 / R1)
// =============================================================================

// logCapture records the lines one level of a component's logger receives,
// each rendered as the message followed by " key=value" per attribute. It is a
// slog.Handler, so a test injects lc.logger() into the component under test. It
// is safe for concurrent use by the -race detector.
type logCapture struct {
	mu    sync.Mutex
	level slog.Level
	lines []string
}

// captureHandler is the slog.Handler view of a logCapture, carrying the
// attributes a With call added.
type captureHandler struct {
	lc    *logCapture
	attrs []slog.Attr
}

func (h captureHandler) Enabled(_ context.Context, l slog.Level) bool { return l == h.lc.level }

func (h captureHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Message)
	write := func(a slog.Attr) bool {
		// Strings are quoted, as the pre-slog lines quoted the values they named.
		if v := a.Value.Resolve(); v.Kind() == slog.KindString {
			fmt.Fprintf(&b, " %s=%q", a.Key, v.String())
		} else {
			fmt.Fprintf(&b, " %s=%s", a.Key, v.String())
		}
		return true
	}
	for _, a := range h.attrs {
		write(a)
	}
	r.Attrs(write)
	h.lc.mu.Lock()
	defer h.lc.mu.Unlock()
	h.lc.lines = append(h.lc.lines, b.String())
	return nil
}

func (h captureHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return captureHandler{lc: h.lc, attrs: append(slices.Clone(h.attrs), attrs...)}
}

func (h captureHandler) WithGroup(string) slog.Handler { return h }

// logger returns a logger whose records at the capture's level land in lc.
func (lc *logCapture) logger() *slog.Logger {
	return slog.New(captureHandler{lc: lc})
}

func (lc *logCapture) all() []string {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	out := make([]string, len(lc.lines))
	copy(out, lc.lines)
	return out
}

// captureWarnLogs returns a capture of Warn-level records; the test injects
// lc.logger() into the component it exercises.
func captureWarnLogs(t *testing.T) *logCapture {
	t.Helper()
	return &logCapture{level: slog.LevelWarn}
}
