package validate

import "testing"

// TestGateListNamesEachGateOnce pins that a gate recorded twice, as the build
// fixer's re-run records it, is named once: the report used to read "the
// configure, configure gates FAILED".
func TestGateListNamesEachGateOnce(t *testing.T) {
	for _, tc := range []struct {
		in   []string
		want string
	}{
		{[]string{"configure", "configure"}, "the configure gate"},
		{[]string{"patches", "configure", "patches"}, "the patches, configure gates"},
		{[]string{"options"}, "the options gate"},
	} {
		if got := gateList(tc.in); got != tc.want {
			t.Errorf("gateList(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
