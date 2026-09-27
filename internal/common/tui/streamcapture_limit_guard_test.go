package tui

import "testing"

// Story 054, R8.6 (regression guard, green today): at or under 65536 bytes the
// capture is byte-identical — no header, nothing dropped. This is the hostile
// half of R7.1: a bound that fires one byte too early fails here.
func TestStreamCaptureKeepsExactlyTheLimit(t *testing.T) {
	for _, n := range []int{0, 1, 65535, 65536} {
		data := make([]byte, n)
		for i := range data {
			data[i] = byte('a' + i%26)
			if i%100 == 99 {
				data[i] = '\n'
			}
		}
		sc := NewStreamCapture(nil, "p1", StreamStdout)
		for off := 0; off < n; off += 4096 {
			if _, err := sc.Write(data[off:min(off+4096, n)]); err != nil {
				t.Fatalf("Write: %v", err)
			}
		}
		_ = sc.Close()
		if got := sc.Captured(); got != string(data) {
			t.Errorf("n=%d: Captured() len %d is not the %d bytes written, byte-identical", n, len(got), n)
		}
	}
}
