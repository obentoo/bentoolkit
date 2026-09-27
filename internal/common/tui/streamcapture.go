package tui

import (
	"bytes"
	"fmt"
	"sync"
)

// maxLineBytes bounds the live-line buffer the emitter assembles for a single
// unterminated line. A pathological child that never emits "\n" or "\r" must not
// grow this buffer without limit (AD8); once a line reaches this size the current
// content is flushed as an in-place (eol=false) update and assembly restarts. The
// capture buffer still receives every byte, so the flush changes nothing that
// Captured() returns.
const maxLineBytes = 64 * 1024

// MaxCapturedBytes is how much child output is kept for an error message: the
// LAST 64 KiB (S054-R7.1). Before this bound a child that wrote 200 MB had all
// 200 MB kept in memory and pasted into its error (audit finding B10).
const MaxCapturedBytes = 64 << 10

// Tail returns s unchanged when it is at most MaxCapturedBytes long. Otherwise
// it returns the last MaxCapturedBytes bytes of s, preceded by one line stating
// how many earlier bytes were dropped — the shape Captured() has past the bound.
// The cut is by bytes, so the kept text may start mid-line or mid-rune.
//
// It is for text pasted into a message (S054-R7.2), never for a transcript
// something still has to judge: the build verdict reads every phase marker of
// the whole transcript (S054-R7.3).
func Tail(s string) string {
	return keepTail(s, 0)
}

// keepTail keeps the last MaxCapturedBytes of s and, when anything was left out,
// prefixes the one truncation line naming the total: the dropped bytes the
// caller had already discarded plus whatever of s precedes the kept tail.
func keepTail(s string, dropped int) string {
	if excess := len(s) - MaxCapturedBytes; excess > 0 {
		s = s[excess:]
		dropped += excess
	}
	if dropped == 0 {
		return s
	}
	return fmt.Sprintf("[... %d earlier bytes dropped ...]\n", dropped) + s
}

// StreamCapture is the streaming capture seam that replaces exec's CombinedOutput
// for long subprocesses (AD4). It implements io.Writer, so it can be assigned to
// cmd.Stdout / cmd.Stderr, and tees every write into two sinks:
//
//   - capture: an in-memory copy of the most recent output, returned by
//     Captured() for the error path (the "Output: %s on failure" contract,
//     S010-R7.1). It keeps the last MaxCapturedBytes verbatim and counts the
//     bytes it dropped before them (S054-R7.1);
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
	capture   bytes.Buffer // the most recent bytes written, verbatim (error path)
	dropped   int          // bytes discarded from the front of capture so far
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
// verbatim (which keeps the last MaxCapturedBytes, see Captured) and feeds the
// bytes through the line splitter, emitting TaskLine events as lines update or
// complete. Because it is a tee it always consumes all of p, returning
// (len(p), nil) on success.
func (c *StreamCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// (a) Verbatim capture for the error path. bytes.Buffer.Write never returns
	// a non-nil error for a successful append, but we still honor it.
	if _, err := c.capture.Write(p); err != nil {
		return 0, err
	}
	// Past twice the bound, drop the front down to the bound (S054-R7.1).
	// Dropping only then, not on every write, keeps the cost amortised: each
	// drop follows at least MaxCapturedBytes of new output, and between writes
	// the buffer never holds more than 2*MaxCapturedBytes. Captured() cuts the
	// rest.
	if held := c.capture.Len(); held > 2*MaxCapturedBytes {
		excess := held - MaxCapturedBytes
		c.capture.Next(excess)
		c.dropped += excess
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

// Captured returns the child output kept for the error path. Up to
// MaxCapturedBytes written, it is every byte, byte-identical (S054-R8.6). Past
// that, it is the last MaxCapturedBytes bytes, preceded by one line stating how
// many earlier bytes were dropped (S054-R7.1) — the same text Tail would make of
// the whole output. Reading it neither consumes nor re-truncates the capture.
func (c *StreamCapture) Captured() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	// The buffer may still hold up to 2*MaxCapturedBytes: keepTail cuts that
	// excess too, and names it together with what Write already dropped.
	return keepTail(c.capture.String(), c.dropped)
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
