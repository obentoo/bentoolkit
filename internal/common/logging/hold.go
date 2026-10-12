package logging

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// maxHeldRecords bounds the terminal queue of a held logger: past it the
// oldest kept records are dropped, and the release says how many.
const maxHeldRecords = 1000

// Hold returns a logger that keeps back the terminal copy of every record it
// receives, and a release func that hands the kept records, in the order they
// were logged, to the terminal sink and makes the logger write through from
// then on.
//
// A caller holds the invocation logger while something else draws on the
// terminal the logger writes to: a line written under bubbletea's inline frame
// moves the cursor without the renderer knowing, so its next redraw lands on
// the wrong rows and leaves the old frame behind.
//
// Only the terminal is held: on a logger New built with a log file, the file
// still gets each record at once, redacted as an unheld logger redacts it. A
// logger of any other shape is held whole. The queue keeps the newest
// maxHeldRecords records, and the release first writes one WARN line counting
// any it dropped.
//
// Loggers derived from the held one with With or WithGroup share its hold. A
// record the terminal sink would drop is not kept. release may be called more
// than once; only the first call replays.
func Hold(l *slog.Logger) (held *slog.Logger, release func()) {
	l = OrDiscard(l)
	state := &holdState{}
	return slog.New(holdTerminal(l.Handler(), state)), state.release
}

// terminalHolder is a handler that can rebuild itself with only its terminal
// sink held.
type terminalHolder interface {
	holdTerminal(state *holdState) slog.Handler
}

// holdTerminal returns h with its terminal sink held by state, or h held whole
// when it does not know which of its sinks is the terminal.
func holdTerminal(h slog.Handler, state *holdState) slog.Handler {
	if t, ok := h.(terminalHolder); ok {
		return t.holdTerminal(state)
	}
	state.root = h
	return &holdingHandler{next: h, state: state}
}

// holdState is the hold shared by a held logger and every logger derived from
// it.
type holdState struct {
	// root is the held handler as Hold found it, before any bound attribute,
	// and writes the drop line.
	root slog.Handler
	// mu serializes queueing with the release, so a record logged while the
	// queue is replayed is written after every record kept before it.
	mu       sync.Mutex
	released bool
	queue    []heldRecord
	dropped  int
}

// heldRecord is one kept record and the handler, carrying its bound
// attributes and groups, that writes it at release.
type heldRecord struct {
	next slog.Handler
	ctx  context.Context //nolint:containedctx // the record's own context, handed to next.Handle at release as slog would have handed it
	rec  slog.Record
}

// release replays the queue, after the drop line when records were dropped,
// and lets every later record through. A handler error is dropped as
// slog.Logger drops it on a direct write.
func (s *holdState) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.released {
		return
	}
	s.released = true
	s.trim(maxHeldRecords)
	if s.dropped > 0 {
		msg := fmt.Sprintf("… %d earlier record(s) dropped", s.dropped)
		_ = s.root.Handle(context.Background(), slog.NewRecord(time.Now(), slog.LevelWarn, msg, 0))
	}
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
	// Trimming only once the queue is twice the bound keeps the cost of each
	// record constant on average.
	if len(s.queue) > 2*maxHeldRecords {
		s.trim(maxHeldRecords)
	}
	return true
}

// trim drops the oldest kept records beyond the newest n and counts them.
func (s *holdState) trim(n int) {
	over := len(s.queue) - n
	if over <= 0 {
		return
	}
	s.dropped += over
	kept := copy(s.queue, s.queue[over:])
	clear(s.queue[kept:])
	s.queue = s.queue[:kept]
}

// holdingHandler is the handler of a held sink.
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

// sinksHandler fans a record out to the invocation logger's terminal and file
// sinks, and knows which is the terminal so Hold can hold it alone.
type sinksHandler struct {
	terminal, file slog.Handler
	multi          slog.Handler
}

// newSinksHandler returns the fan-out to terminal and file.
func newSinksHandler(terminal, file slog.Handler) *sinksHandler {
	return &sinksHandler{terminal: terminal, file: file, multi: slog.NewMultiHandler(terminal, file)}
}

// Enabled reports whether either sink takes level.
func (h *sinksHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.multi.Enabled(ctx, level)
}

// Handle hands r to each sink that takes its level.
func (h *sinksHandler) Handle(ctx context.Context, r slog.Record) error {
	return h.multi.Handle(ctx, r)
}

// WithAttrs binds attrs on both sinks.
func (h *sinksHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return newSinksHandler(h.terminal.WithAttrs(attrs), h.file.WithAttrs(attrs))
}

// WithGroup opens a group on both sinks.
func (h *sinksHandler) WithGroup(name string) slog.Handler {
	return newSinksHandler(h.terminal.WithGroup(name), h.file.WithGroup(name))
}

// holdTerminal holds the terminal sink and leaves the file sink writing.
func (h *sinksHandler) holdTerminal(state *holdState) slog.Handler {
	state.root = h.terminal
	return newSinksHandler(&holdingHandler{next: h.terminal, state: state}, h.file)
}

// holdTerminal holds the terminal under the redactor, so a kept record is
// already redacted and every sink sees the same scrubbed record.
func (h *redactingHandler) holdTerminal(state *holdState) slog.Handler {
	h2 := *h
	h2.next = holdTerminal(h.next, state)
	return &h2
}
