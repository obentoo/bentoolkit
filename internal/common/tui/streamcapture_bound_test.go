package tui

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Story 054, R7.1: past 65536 bytes, Captured() keeps only the last 65536,
// preceded by ONE line stating how many earlier bytes were dropped.

const captureLimit = 65536

// captureData returns n bytes of letters (no digits, so a dropped-count line can
// never be mistaken for data) with a newline every 100 bytes; every position
// carries a distinct-enough pattern that a wrong tail offset shows.
func captureData(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		if i%100 == 99 {
			b[i] = '\n'
			continue
		}
		b[i] = byte('a' + (i/100+i)%26)
	}
	return b
}

func writeInChunks(t *testing.T, sc *StreamCapture, data []byte, chunk int) {
	t.Helper()
	for off := 0; off < len(data); off += chunk {
		end := min(off+chunk, len(data))
		if n, err := sc.Write(data[off:end]); err != nil || n != end-off {
			t.Fatalf("Write = (%d, %v), want (%d, nil)", n, err, end-off)
		}
	}
}

// assertTail checks Captured() == "<one line naming dropped>\n" + last 65536 bytes.
func assertTail(t *testing.T, got string, data []byte, dropped int) {
	t.Helper()
	header, rest, ok := strings.Cut(got, "\n")
	if !ok {
		t.Fatalf("Captured() has no header line; len = %d", len(got))
	}
	want := string(data[len(data)-captureLimit:])
	if rest != want {
		t.Errorf("Captured() body: len %d, want the exact last %d bytes of the input (first differing tail is not what was written last)", len(rest), captureLimit)
	}
	if !regexp.MustCompile(`\b` + strconv.Itoa(dropped) + `\b`).MatchString(header) {
		t.Errorf("header line %q does not state the %d dropped bytes", header, dropped)
	}
}

func TestStreamCaptureDropsOneByteOverTheLimit(t *testing.T) {
	data := captureData(captureLimit + 1)
	sc := NewStreamCapture(nil, "p1", StreamStdout)
	writeInChunks(t, sc, data, 4096)
	_ = sc.Close()
	assertTail(t, sc.Captured(), data, 1)
}

func TestStreamCaptureDropCountAccumulatesAcrossWrites(t *testing.T) {
	cases := []struct {
		name  string
		total int
		chunk int
	}{
		{"200000 bytes in uneven chunks (the B10 shape)", 200000, 7919},
		{"one write larger than the limit", captureLimit + 4474, captureLimit + 4474},
		{"byte by byte just over the limit", captureLimit + 10, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := captureData(tc.total)
			sc := NewStreamCapture(nil, "p1", StreamStdout)
			writeInChunks(t, sc, data, tc.chunk)
			_ = sc.Close()
			first := sc.Captured()
			assertTail(t, first, data, tc.total-captureLimit)
			if again := sc.Captured(); again != first {
				t.Error("a second Captured() call returned different text: reading the capture must not consume or re-truncate it")
			}
			if n := len(first); n > captureLimit+200 {
				t.Errorf("Captured() is %d bytes; the retained output must be bounded at %d plus one line", n, captureLimit)
			}
		})
	}
}
