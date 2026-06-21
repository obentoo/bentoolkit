package tui

import (
	"bytes"
	"sync"
)

// maxLineBytes bounds the live-line buffer the emitter assembles for a single
// unterminated line. A pathological child that never emits "\n" or "\r" must not
// grow this buffer without limit (AD8); once a line reaches this size the current
// content is flushed as an in-place (eol=false) update and assembly restarts. The
// capture buffer still receives every byte, so the full-output-on-error contract
// (R7.1) is unaffected.
const maxLineBytes = 64 * 1024

// StreamCapture is the streaming capture seam that replaces exec's CombinedOutput
// for long subprocesses (AD4). It implements io.Writer, so it can be assigned to
// cmd.Stdout / cmd.Stderr, and tees every write into two sinks:
//
//   - capture: a verbatim in-memory buffer of everything written, returned by
//     Captured() for the error path (preserves the "Output: %s on failure"
//     contract, R7.1);
//   - a line emitter that splits the stream into lines and forwards each update
//     to the Reporter as a TaskLine event (R1.1). It treats "\r" as an in-place
//     line replacement (R1.2) and "\r\n" as a single terminator.
//
// All mutable state is guarded by a mutex so a single StreamCapture is safe to
// drive from an os/exec copy goroutine and passes -race (R7.4).
type StreamCapture struct {
	r      Reporter
	id     string
	stream Stream

	mu        sync.Mutex
	capture   bytes.Buffer // verbatim copy of all bytes written (error path)
	cur       []byte       // the live line currently being assembled
	pendingCR bool         // a "\r" was seen; its meaning depends on the next byte
}

// NewStreamCapture returns a StreamCapture that emits TaskLine events for task id
// on the given stream. A nil Reporter is normalized to a no-op (orNoop), so the
// returned writer is always safe to use.
func NewStreamCapture(r Reporter, id string, stream Stream) *StreamCapture {
	return &StreamCapture{
		r:      orNoop(r),
		id:     id,
		stream: stream,
	}
}

// Write implements io.Writer. It appends every byte of p to the capture buffer
// verbatim and feeds the bytes through the line splitter, emitting TaskLine
// events as lines update or complete. Because it is a tee it always consumes all
// of p, returning (len(p), nil) on success.
func (c *StreamCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// (a) Verbatim capture for the error path. bytes.Buffer.Write never returns
	// a non-nil error for a successful append, but we still honor it.
	if _, err := c.capture.Write(p); err != nil {
		return 0, err
	}

	// (b) Line splitting / emission.
	for _, b := range p {
		if c.pendingCR {
			if b == '\n' {
				// "\r\n" is a single line terminator: commit and consume "\n".
				c.emitLocked(true)
				c.pendingCR = false
				continue
			}
			// A bare "\r": in-place reset of the live line, then fall through to
			// process b as the first byte of the next line.
			c.emitLocked(false)
			c.pendingCR = false
		}

		switch b {
		case '\r':
			// Defer the decision until we see the next byte (\n vs. anything).
			c.pendingCR = true
		case '\n':
			c.emitLocked(true)
		default:
			c.cur = append(c.cur, b)
			if len(c.cur) >= maxLineBytes {
				// Bound the live-line buffer for an unterminated line (AD8).
				c.emitLocked(false)
			}
		}
	}

	return len(p), nil
}

// Captured returns the full, verbatim concatenation of every byte written. It is
// byte-identical to the input and carries the complete child output for the error
// path (R7.1).
func (c *StreamCapture) Captured() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.capture.String()
}

// Close flushes any not-yet-emitted live-line state as an in-place (eol=false)
// update: a deferred trailing "\r", or a partial line with no terminator. It is
// safe to call more than once; a second Close emits nothing. Close always returns
// nil.
func (c *StreamCapture) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.pendingCR {
		// A trailing bare "\r": flush the line it would have reset.
		c.emitLocked(false)
		c.pendingCR = false
	} else if len(c.cur) > 0 {
		c.emitLocked(false)
	}
	return nil
}

// emitLocked forwards the current live line to the Reporter and resets it. The
// caller must hold c.mu. Emitting under the lock is acceptable here: the reporter
// backends are non-reentrant (recordingReporter is independent; the real backends
// use program.Send), so this cannot deadlock and keeps the splitter state
// consistent.
func (c *StreamCapture) emitLocked(eol bool) {
	c.r.TaskLine(c.id, c.stream, string(c.cur), eol)
	c.cur = c.cur[:0]
}
