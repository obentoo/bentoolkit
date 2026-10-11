package autoupdate

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// The upload output kept for an error is the last bytes written, never more
// than the bound, and starts on a whole UTF-8 sequence once it was cut.
func TestTailWriterKeepsABoundedTail(t *testing.T) {
	tw := &tailWriter{max: 8}
	if got := tw.text(); got != "" {
		t.Errorf("empty output = %q, want \"\"", got)
	}
	for _, chunk := range []string{"abc", "defgh", "ij\n"} {
		if n, err := tw.Write([]byte(chunk)); n != len(chunk) || err != nil {
			t.Fatalf("Write(%q) = %d, %v", chunk, n, err)
		}
	}
	if got := tw.text(); got != "\ndefghij" {
		t.Errorf("tail = %q, want %q", got, "\ndefghij")
	}

	tw = &tailWriter{max: 8}
	_, _ = tw.Write([]byte(strings.Repeat("x", 20) + "aé€end"))
	got := tw.text()
	if len(got) > 9 || !utf8.ValidString(got) || !strings.HasSuffix(got, "€end") {
		t.Errorf("tail of a large write = %q; want at most 8 bytes, valid UTF-8, ending in €end", got)
	}
}
