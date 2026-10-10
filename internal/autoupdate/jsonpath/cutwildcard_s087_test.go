package jsonpath

import (
	"errors"
	"strconv"
	"strings"
	"testing"
)

// TestS087CutWildcard pins the one wildcard rule both the registry and the
// version-history extractor apply to a versions_path.
func TestS087CutWildcard(t *testing.T) {
	accepted := []struct {
		path, rest string
		wildcard   bool
	}{
		{"list", "list", false},
		{"data.versions", "data.versions", false},
		{"[*]", "", true},
		{"[*].tag_name", "tag_name", true},
		{"[*].a.b[1]", "a.b[1]", true},
		{"[*][0]", "[0]", true},
		{"[*].[0]", "[0]", true},
	}
	for _, tc := range accepted {
		rest, wildcard, err := CutWildcard(tc.path)
		if err != nil || rest != tc.rest || wildcard != tc.wildcard {
			t.Errorf("CutWildcard(%q) = %q, %v, %v; want %q, %v, nil", tc.path, rest, wildcard, err, tc.rest, tc.wildcard)
		}
	}

	for _, path := range []string{"", "list..x", "[*].", "[*]tag", "[*]..tag", "[*].tag.", "[*][-0]", "[*].a[*]", "[*][*]", "a[*]"} {
		_, _, err := CutWildcard(path)
		if !errors.Is(err, ErrInvalidJSONPath) {
			t.Errorf("CutWildcard(%q) error = %v; want one wrapping ErrInvalidJSONPath", path, err)
			continue
		}
		if q := strconv.Quote(path); path != "" && !strings.Contains(err.Error(), q) {
			t.Errorf("CutWildcard(%q) error %q does not quote the path as %s", path, err, q)
		}
	}
}
