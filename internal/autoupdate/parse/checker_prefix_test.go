package parse

import (
	"testing"
)

// TestNormalizeUpstreamVersion pins the helper the checker and the validation
// plan share, prefix by prefix, so the two callers cannot drift apart on what
// "normalized" means.
func TestNormalizeUpstreamVersion(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"v3.2.3", "3.2.3"},
		{"V3.2.3", "3.2.3"},
		{" v1.16.1 ", "1.16.1"},
		{"release-2.0", "2.0"},
		{"3.2.3", "3.2.3"},
		{"b4938", "b4938"}, // not a recognised prefix: left for the comparability warning
		{"", ""},
	}
	for _, c := range cases {
		if got := NormalizeUpstreamVersion(c.in); got != c.want {
			t.Errorf("NormalizeUpstreamVersion(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
