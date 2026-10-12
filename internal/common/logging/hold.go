package logging

import (
	"context"
	"log/slog"
	"sync"
)

// Hold returns a logger that keeps back every record it receives, and a
// release func that hands the kept records, in the order they were logged, to
// l's handler and makes the logger write through from then on.
//
// A caller holds the invocation logger while something else draws on the
// terminal the logger writes to: a line written under bubbletea's inline frame
// moves the cursor without the renderer knowing, so its next redraw lands on
// the wrong rows and leaves the old frame behind. Records are kept whole and
// handled later by l's own handler, so their level, attributes, redaction and
// every sink (the log file too) stay exactly as an unheld logger writes them.
//
// Loggers derived from the held one with With or WithGroup share its hold.
// Enabled answers as l's handler does, so a record l would drop is not kept.
// release may be called more than once; only the first call replays.
func Hold(l *slog.Logger) (held *slog.Logger, release func()) {
	l = OrDiscard(l)
	state := &holdState{}
	return slog.New(&holdingHandler{next: l.Handler(), state: state}), state.release
}

// holdState is the hold shared by a held logger and every logger derived from
// it.
type holdState struct {
	// mu serializes queueing with the release, so a record logged while the
	// queue is replayed is written after every record kept before it.
	mu       sync.Mutex
	released bool
	queue    []heldRecord
}

// heldRecord is one kept record and the handler, carrying its bound
// attributes and groups, that writes it at release.
type heldRecord struct {
	next slog.Handler
	ctx  context.Context //nolint:containedctx // the record's own context, handed to next.Handle at release as slog would have handed it
	rec  slog.Record
}

// release replays the queue and lets every later record through. A handler
// error is dropped as slog.Logger drops it on a direct write.
func (s *holdState) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.released {
		return
	}
	s.released = true
	for _, h := range s.queue {
		_ = h.next.Handle(h.ctx, h.rec)
	}
	s.queue = nil
}

// keep queues r for next and reports true, or reports false once the hold is
// released.
func (s *holdState) keep(ctx context.Context, next slog.Handler, r slog.Record) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.released {
		return false
	}
	// The record outlives the call, so its attributes are cloned, and its
	// context must not cancel the write it is handed to later.
	s.queue = append(s.queue, heldRecord{next: next, ctx: context.WithoutCancel(ctx), rec: r.Clone()})
	return true
}

// holdingHandler is the handler of a held logger.
type holdingHandler struct {
	next  slog.Handler
	state *holdState
}

// Enabled reports the wrapped handler's level gate.
func (h *holdingHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

// Handle keeps r until the release, and passes it on at once after it.
func (h *holdingHandler) Handle(ctx context.Context, r slog.Record) error {
	if h.state.keep(ctx, h.next, r) {
		return nil
	}
	return h.next.Handle(ctx, r)
}

// WithAttrs binds attrs on the wrapped handler under the same hold.
func (h *holdingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &holdingHandler{next: h.next.WithAttrs(attrs), state: h.state}
}

// WithGroup opens a group on the wrapped handler under the same hold.
func (h *holdingHandler) WithGroup(name string) slog.Handler {
	return &holdingHandler{next: h.next.WithGroup(name), state: h.state}
}
