package snapshot

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
)

// logCapture records the Warn-level records a component's logger receives,
// each rendered as the message followed by " key=value" per attribute, so an
// assertion on what a warning names reads the same as it did on the formatted
// line the pre-slog seam produced. It is a slog.Handler: a test injects
// lc.logger() into the component under test. Safe for concurrent use.
type logCapture struct {
	mu    sync.Mutex
	lines []string
}

// captureHandler is the slog.Handler view of a logCapture, carrying the
// attributes a With call added.
type captureHandler struct {
	lc    *logCapture
	attrs []slog.Attr
}

func (h captureHandler) Enabled(_ context.Context, l slog.Level) bool { return l == slog.LevelWarn }

func (h captureHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Message)
	write := func(a slog.Attr) bool {
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

// logger returns a logger whose Warn records land in lc.
func (lc *logCapture) logger() *slog.Logger { return slog.New(captureHandler{lc: lc}) }

// all returns every captured line, in order.
func (lc *logCapture) all() []string {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return slices.Clone(lc.lines)
}

// String returns every captured line, concatenated.
func (lc *logCapture) String() string { return strings.Join(lc.all(), "") }
