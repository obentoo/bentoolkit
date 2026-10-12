package autoupdate

// Story 095: a child line ending in CRLF keeps its text in the one-line error.

import "testing"

func TestChildRunCRLFFirstErrorLine(t *testing.T) {
	for _, tc := range []struct{ name, out, want string }{
		{"crlf error line", "fetching\r\nerror: x\r\n", "error: x"},
		{"rewritten in place", "10%\r20%\rerror: done\n", "error: done"},
		{"blank carriage return", "\r\nerror: y\r\n", "error: y"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := firstErrorLine(tc.out); got != tc.want {
				t.Errorf("firstErrorLine(%q) = %q, want %q", tc.out, got, tc.want)
			}
		})
	}
}
