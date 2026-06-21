package tui

import (
	"fmt"
	"io"
	"sync"
	"time"
)

// Compile-time guarantee that plainReporter is a Reporter.
var _ Reporter = (*plainReporter)(nil)

// plainReporter implements Reporter for non-TTY / opt-out runs by writing
// deterministic, single-line records to an io.Writer (design §5, R2.2/R2.3).
//
// It NEVER emits an ANSI escape (0x1b) or a carriage return: in-place ("\r")
// child updates are collapsed into full, newline-terminated lines. Every write
// is mutex-guarded because the parallel manifest workers call concurrently
// (R7.4), mirroring the existing LogReporter.
//
// In-place TaskLine updates (eol=false) are rate-limited per task id to at most
// one line per throttle duration so a chatty progress stream does not flood the
// log; committed lines (eol=true) and all other records are always emitted. A
// throttle of 0 disables rate-limiting entirely.
type plainReporter struct {
	mu       sync.Mutex
	w        io.Writer
	throttle time.Duration
	err      error                // first write error; subsequent writes are skipped
	lastEmit map[string]time.Time // per-id last in-place emit, for rate-limiting
}

// newPlainReporter builds a plainReporter writing to w. throttle bounds the rate
// of in-place (eol=false) tail updates per task id; throttle == 0 disables it.
func newPlainReporter(w io.Writer, throttle time.Duration) *plainReporter {
	return &plainReporter{
		w:        w,
		throttle: throttle,
		lastEmit: make(map[string]time.Time),
	}
}

// writeLine emits one full line (caller supplies content without the trailing
// newline) under the mutex. The first write error is recorded and disables
// further output; it never panics. Callers must NOT pass content containing an
// ANSI escape or a carriage return.
func (r *plainReporter) writeLine(content string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.writeLineLocked(content)
}

// writeLineLocked is the mutex-held body of writeLine; it lets methods that
// already hold the lock (the throttled TaskLine path) emit without re-locking.
func (r *plainReporter) writeLineLocked(content string) {
	if r.err != nil {
		return
	}
	if _, err := io.WriteString(r.w, content+"\n"); err != nil {
		r.err = err
	}
}

// BatchStart produces no line (the plain log is a per-task transcript).
func (r *plainReporter) BatchStart(int) {}

func (r *plainReporter) TaskStart(id, label string) {
	r.writeLine(fmt.Sprintf("[%s] START %s", id, label))
}

func (r *plainReporter) TaskStage(id, stage string) {
	r.writeLine(fmt.Sprintf("[%s] STAGE %s", id, stage))
}

// TaskProgress produces no line; numeric progress is meaningless in a plain log.
func (r *plainReporter) TaskProgress(string, float64) {}

// TaskLine emits one collapsed full line "[id] text". Committed lines (eol=true)
// are always written; in-place updates (eol=false) are rate-limited per id when
// a throttle is configured. The stream is intentionally not encoded into the
// line — the test pins the bare "[id] text" shape.
func (r *plainReporter) TaskLine(id string, _ Stream, text string, eol bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !eol && r.throttle > 0 {
		now := time.Now()
		if last, ok := r.lastEmit[id]; ok && now.Sub(last) < r.throttle {
			return
		}
		r.lastEmit[id] = now
	}
	r.writeLineLocked(fmt.Sprintf("[%s] %s", id, text))
}

// TaskDone emits a terminal OK/FAIL line carrying the summary. capturedOutput is
// deliberately NOT written: the full buffer is reserved for the error path
// elsewhere (applier.go's Output: %s contract), and the test asserts its absence.
func (r *plainReporter) TaskDone(id string, ok bool, summary, _ string) {
	status := "FAIL"
	if ok {
		status = "OK"
	}
	r.writeLine(fmt.Sprintf("[%s] %s %s", id, status, summary))
}

// Log emits a non-task-scoped line. The level is omitted from the output; the
// text alone is the transcript line.
func (r *plainReporter) Log(_, text string) {
	r.writeLine(text)
}

// BatchDone produces no line.
func (r *plainReporter) BatchDone(string) {}
