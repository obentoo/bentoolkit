package tui

import (
	"strings"
	"testing"
)

// A "\n"-delimited stream emits one committed TaskLine per line and the full
// buffer is preserved verbatim for the error path (R1.1, R7.1).
func TestStreamCaptureNewlineDelimited(t *testing.T) {
	r := &recordingReporter{}
	sc := NewStreamCapture(r, "p1", StreamStdout)

	in := "first\nsecond\nthird\n"
	n, err := sc.Write([]byte(in))
	if err != nil || n != len(in) {
		t.Fatalf("Write n=%d err=%v, want n=%d err=nil", n, err, len(in))
	}
	if err := sc.Close(); err != nil {
		t.Fatalf("Close err=%v", err)
	}

	lines := r.taskLines()
	if len(lines) != 3 {
		t.Fatalf("got %d TaskLines, want 3: %+v", len(lines), lines)
	}
	for i, w := range []string{"first", "second", "third"} {
		if lines[i].Text != w || !lines[i].EOL {
			t.Errorf("line %d = %q eol=%v, want %q eol=true", i, lines[i].Text, lines[i].EOL, w)
		}
		if lines[i].Stream != StreamStdout {
			t.Errorf("line %d stream = %v, want stdout", i, lines[i].Stream)
		}
	}
	if got := sc.Captured(); got != in {
		t.Errorf("Captured = %q, want %q", got, in)
	}
}

// A carriage-return progress sequence updates the live line in place (eol=false)
// and the final newline commits it (eol=true). The captured buffer is verbatim
// (R1.2, R7.1).
func TestStreamCaptureCarriageReturnInPlace(t *testing.T) {
	r := &recordingReporter{}
	sc := NewStreamCapture(r, "p1", StreamStderr)

	in := "10%\r20%\r100%\n"
	if _, err := sc.Write([]byte(in)); err != nil {
		t.Fatal(err)
	}
	_ = sc.Close()

	lines := r.taskLines()
	if len(lines) != 3 {
		t.Fatalf("got %d TaskLines, want 3: %+v", len(lines), lines)
	}
	if lines[0].Text != "10%" || lines[0].EOL {
		t.Errorf("line0 = %q eol=%v, want 10%% eol=false", lines[0].Text, lines[0].EOL)
	}
	if lines[1].Text != "20%" || lines[1].EOL {
		t.Errorf("line1 = %q eol=%v, want 20%% eol=false", lines[1].Text, lines[1].EOL)
	}
	if lines[2].Text != "100%" || !lines[2].EOL {
		t.Errorf("line2 = %q eol=%v, want 100%% eol=true", lines[2].Text, lines[2].EOL)
	}
	if got := sc.Captured(); got != in {
		t.Errorf("Captured = %q, want %q", got, in)
	}
}

// CRLF ("\r\n") is a single line terminator, not a reset followed by an empty
// committed line.
func TestStreamCaptureCRLFIsOneLine(t *testing.T) {
	r := &recordingReporter{}
	sc := NewStreamCapture(r, "p1", StreamStdout)
	if _, err := sc.Write([]byte("alpha\r\nbeta\r\n")); err != nil {
		t.Fatal(err)
	}
	_ = sc.Close()

	lines := r.taskLines()
	if len(lines) != 2 {
		t.Fatalf("got %d TaskLines, want 2: %+v", len(lines), lines)
	}
	for i, w := range []string{"alpha", "beta"} {
		if lines[i].Text != w || !lines[i].EOL {
			t.Errorf("line %d = %q eol=%v, want %q eol=true", i, lines[i].Text, lines[i].EOL, w)
		}
	}
}

// A trailing partial line (no terminator) is not emitted until Close, which
// flushes it as an in-place (non-committed) update.
func TestStreamCapturePartialFlushedOnClose(t *testing.T) {
	r := &recordingReporter{}
	sc := NewStreamCapture(r, "p1", StreamStdout)

	if _, err := sc.Write([]byte("no newline here")); err != nil {
		t.Fatal(err)
	}
	if got := len(r.taskLines()); got != 0 {
		t.Fatalf("partial line emitted %d TaskLines before Close, want 0", got)
	}
	if err := sc.Close(); err != nil {
		t.Fatalf("Close err=%v", err)
	}
	lines := r.taskLines()
	if len(lines) != 1 || lines[0].Text != "no newline here" || lines[0].EOL {
		t.Fatalf("Close should flush trailing partial as eol=false; got %+v", lines)
	}
	if got := sc.Captured(); got != "no newline here" {
		t.Errorf("Captured = %q, want %q", got, "no newline here")
	}
}

// A line split across multiple Write calls is assembled into a single line.
func TestStreamCaptureWriteInChunks(t *testing.T) {
	r := &recordingReporter{}
	sc := NewStreamCapture(r, "p1", StreamStdout)
	for _, chunk := range []string{"hel", "lo wor", "ld\n"} {
		if _, err := sc.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	_ = sc.Close()
	lines := r.taskLines()
	if len(lines) != 1 || lines[0].Text != "hello world" || !lines[0].EOL {
		t.Fatalf("chunked line not assembled: %+v", lines)
	}
}

// A carriage return split across a Write boundary still resets the live line.
func TestStreamCaptureCRAcrossWriteBoundary(t *testing.T) {
	r := &recordingReporter{}
	sc := NewStreamCapture(r, "p1", StreamStdout)
	sc.Write([]byte("50%\r"))
	sc.Write([]byte("99%\n"))
	_ = sc.Close()
	lines := r.taskLines()
	if len(lines) != 2 {
		t.Fatalf("got %d TaskLines, want 2: %+v", len(lines), lines)
	}
	if lines[0].Text != "50%" || lines[0].EOL {
		t.Errorf("line0 = %q eol=%v, want 50%% eol=false", lines[0].Text, lines[0].EOL)
	}
	if lines[1].Text != "99%" || !lines[1].EOL {
		t.Errorf("line1 = %q eol=%v, want 99%% eol=true", lines[1].Text, lines[1].EOL)
	}
}

// The full buffer holds all output for the error path even at high line volume,
// while the emitter retains no per-line history (bounded memory, AD8/R1.5).
func TestStreamCaptureBufferHoldsAllHugeInput(t *testing.T) {
	r := &recordingReporter{}
	sc := NewStreamCapture(r, "p1", StreamStdout)

	const N = 100000
	var b strings.Builder
	for i := 0; i < N; i++ {
		b.WriteString("x\n")
	}
	in := b.String()
	if _, err := sc.Write([]byte(in)); err != nil {
		t.Fatal(err)
	}
	_ = sc.Close()

	if got := len(r.taskLines()); got != N {
		t.Fatalf("got %d TaskLines, want %d", got, N)
	}
	if got := sc.Captured(); got != in {
		t.Errorf("Captured length = %d, want %d", len(got), len(in))
	}
}

// A single very long line with no terminator is bounded by the emitter (it does
// not grow the live-line buffer without limit) yet is preserved in full in the
// capture buffer.
func TestStreamCaptureBoundsUnterminatedLine(t *testing.T) {
	r := &recordingReporter{}
	sc := NewStreamCapture(r, "p1", StreamStdout)

	in := strings.Repeat("a", 300000) // 300 KB, no newline
	if _, err := sc.Write([]byte(in)); err != nil {
		t.Fatal(err)
	}
	_ = sc.Close()

	if got := sc.Captured(); got != in {
		t.Errorf("Captured length = %d, want %d", len(got), len(in))
	}
	// At least one emission happened (the emitter flushed rather than buffering
	// the whole 300 KB line indefinitely).
	if len(r.taskLines()) == 0 {
		t.Fatalf("expected the emitter to flush a bounded long line, got 0 TaskLines")
	}
}
