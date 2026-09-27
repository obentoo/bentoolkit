package tui

import (
	"fmt"
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

// checkBoundedCapture checks what got should be for input in, independently of
// the code under test: up to 65536 bytes, in itself, byte-identical (S054-R8.6);
// past that, one truncation line naming the len(in)-65536 dropped bytes and then
// exactly the last 65536 bytes of in (S054-R7.1).
func checkBoundedCapture(t *testing.T, what, got, in string) {
	t.Helper()
	const limit = 65536
	if len(in) <= limit {
		if got != in {
			t.Errorf("%s: length %d, want the %d bytes written, byte-identical", what, len(got), len(in))
		}
		return
	}
	line := fmt.Sprintf("[... %d earlier bytes dropped ...]\n", len(in)-limit)
	if !strings.HasPrefix(got, line) {
		first, _, _ := strings.Cut(got, "\n")
		t.Errorf("%s starts with %.60q, want the truncation line %q", what, first, line)
	}
	if !strings.HasSuffix(got, in[len(in)-limit:]) {
		t.Errorf("%s does not end with the last %d bytes written", what, limit)
	}
	if len(got) != len(line)+limit {
		t.Errorf("%s: length %d, want %d (the truncation line + the last %d bytes)", what, len(got), len(line)+limit, limit)
	}
}

// At high line volume every line is still emitted as a TaskLine and the emitter
// retains no per-line history (bounded memory, AD8/R1.5), while the capture keeps
// only the last 64 KiB for the error path (S054-R7.1). The name predates that
// bound: the buffer no longer holds all of a huge input.
func TestStreamCaptureBufferHoldsAllHugeInput(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		lines int
	}{
		{"200000 bytes", strings.Repeat("x\n", 100000), 100000},
		{"exactly 65536 bytes, kept whole", strings.Repeat("x\n", 32768), 32768},
		// One extra byte on the first line: still 32768 committed lines.
		{"65537 bytes, one dropped", "x" + strings.Repeat("x\n", 32768), 32768},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &recordingReporter{}
			sc := NewStreamCapture(r, "p1", StreamStdout)
			if _, err := sc.Write([]byte(tc.in)); err != nil {
				t.Fatal(err)
			}
			_ = sc.Close()

			if got := len(r.taskLines()); got != tc.lines {
				t.Fatalf("got %d TaskLines, want %d", got, tc.lines)
			}
			checkBoundedCapture(t, "Captured", sc.Captured(), tc.in)
		})
	}
}

// A single very long line with no terminator is bounded by the emitter (it does
// not grow the live-line buffer without limit), and the capture keeps the last
// 64 KiB of it (S054-R7.1).
func TestStreamCaptureBoundsUnterminatedLine(t *testing.T) {
	for _, n := range []int{300000, 65536, 65537} {
		t.Run(fmt.Sprintf("%d bytes", n), func(t *testing.T) {
			r := &recordingReporter{}
			sc := NewStreamCapture(r, "p1", StreamStdout)

			in := strings.Repeat("a", n) // no newline
			if _, err := sc.Write([]byte(in)); err != nil {
				t.Fatal(err)
			}
			_ = sc.Close()

			checkBoundedCapture(t, "Captured", sc.Captured(), in)
			// At least one emission happened (the emitter flushed rather than
			// buffering the whole line indefinitely).
			if len(r.taskLines()) == 0 {
				t.Fatalf("expected the emitter to flush a bounded long line, got 0 TaskLines")
			}
		})
	}
}

// The bound is on memory, not only on what Captured() returns: B10 was a child
// whose 200 MB all stayed in memory. Between writes the buffer holds at most
// 2*65536 live bytes, however much was written (S054-R7.1).
func TestStreamCaptureRetainsAtMostTwiceTheBound(t *testing.T) {
	sc := NewStreamCapture(nil, "p1", StreamStdout)
	chunk := []byte(numberedLines(32 << 10)) // io.Copy's buffer size, as os/exec feeds it
	for i := 1; i <= 64; i++ {               // 2 MiB in total
		if _, err := sc.Write(chunk); err != nil {
			t.Fatal(err)
		}
		sc.mu.Lock()
		held := sc.capture.Len()
		sc.mu.Unlock()
		if held > 2*65536 {
			t.Fatalf("after %d bytes written the buffer holds %d, want at most %d", i*len(chunk), held, 2*65536)
		}
	}
}

// numberedLines returns exactly n bytes of "0\n1\n2\n...": the text differs from
// one offset to the next, so a tail cut in the wrong place does not match.
func numberedLines(n int) string {
	var b strings.Builder
	for i := 0; b.Len() < n; i++ {
		fmt.Fprintf(&b, "%d\n", i)
	}
	return b.String()[:n]
}

// Tail bounds any text the way Captured() bounds a stream: unchanged up to
// 65536 bytes, then one truncation line and the last 65536 bytes (S054-R7.1;
// S054-R7.2 pastes git output through it).
func TestTailKeepsTheLastMaxCapturedBytes(t *testing.T) {
	if MaxCapturedBytes != 65536 {
		t.Fatalf("MaxCapturedBytes = %d, want 65536", MaxCapturedBytes)
	}
	for _, n := range []int{0, 1, 65536, 65537, 200000} {
		in := numberedLines(n)
		checkBoundedCapture(t, fmt.Sprintf("Tail(%d bytes)", n), Tail(in), in)
	}
}
