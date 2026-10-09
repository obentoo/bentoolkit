package parse

import (
	"errors"
	"reflect"
	"testing"
)

// Characterization tests: they pin what parseJSONPath does today, including
// lenient cases, so a restructuring cannot change which segments a
// user-written path yields.

func s086Field(name string) pathSegment {
	return pathSegment{segType: segmentField, value: name}
}

func s086Index(i int) pathSegment {
	return pathSegment{segType: segmentIndex, index: i}
}

func TestS086ParseJSONPathSegments(t *testing.T) {
	tests := []struct {
		name string
		path string
		want []pathSegment
	}{
		// Near-identical paths that must stay apart.
		{"dot digit is a field, not an index", "a.0", []pathSegment{s086Field("a"), s086Field("0")}},
		{"bracket digit is an index", "a[0]", []pathSegment{s086Field("a"), s086Index(0)}},
		{"two indices are not one field", "a[1][2]", []pathSegment{s086Field("a"), s086Index(1), s086Index(2)}},
		// Differently-shaped paths that yield the same segments.
		{"canonical form", "a[0].b", []pathSegment{s086Field("a"), s086Index(0), s086Field("b")}},
		{"missing dot after index", "a[0]b", []pathSegment{s086Field("a"), s086Index(0), s086Field("b")}},
		{"leading dot", ".a[0].b", []pathSegment{s086Field("a"), s086Index(0), s086Field("b")}},
		{"doubled dot", "a..b", []pathSegment{s086Field("a"), s086Field("b")}},
		{"trailing dot", "a.", []pathSegment{s086Field("a")}},
		{"plus-signed index", "a[+1]", []pathSegment{s086Field("a"), s086Index(1)}},
		// Leading-bracket paths.
		{"leading index", "[0]", []pathSegment{s086Index(0)}},
		{"leading index then field", "[0].tag_name", []pathSegment{s086Index(0), s086Field("tag_name")}},
		{"leading chained indices", "[3][4].x", []pathSegment{s086Index(3), s086Index(4), s086Field("x")}},
		{"leading index without dot", "[2]x", []pathSegment{s086Index(2), s086Field("x")}},
		{"index after field then leading-bracket loop", "a.[1]", []pathSegment{s086Field("a"), s086Index(1)}},
		// Odd field names are accepted verbatim.
		{"stray closing bracket becomes a field", "a[0]]", []pathSegment{s086Field("a"), s086Index(0), s086Field("]")}},
		{"closing bracket inside field", "a]b", []pathSegment{s086Field("a]b")}},
		{"non-ASCII field", "versão.x", []pathSegment{s086Field("versão"), s086Field("x")}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseJSONPath(tc.path)
			if err != nil {
				t.Fatalf("parseJSONPath(%q) error = %v, want nil", tc.path, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parseJSONPath(%q) = %#v, want %#v", tc.path, got, tc.want)
			}
		})
	}
}

func TestS086ParseJSONPathErrors(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		wantErr string
	}{
		{"empty path", "", "invalid JSON path syntax: empty path"},
		{"lone dot", ".", "invalid JSON path syntax: empty path"},
		{"only dots", "...", "invalid JSON path syntax: empty path"},
		{"leading unclosed bracket", "[0", "invalid JSON path syntax: unclosed bracket"},
		{"leading lone bracket", "[", "invalid JSON path syntax: unclosed bracket"},
		{"leading empty index", "[]", `invalid JSON path syntax: invalid array index ""`},
		{"leading non-numeric index", "[x]", `invalid JSON path syntax: invalid array index "x"`},
		{"leading wildcard", "[*].tag", `invalid JSON path syntax: invalid array index "*"`},
		{"leading negative index", "[-1]", "invalid JSON path syntax: negative array index"},
		{"leading second index unclosed", "[0][1", "invalid JSON path syntax: unclosed bracket"},
		{"leading second index invalid", "[0][y]", `invalid JSON path syntax: invalid array index "y"`},
		{"leading second index negative", "[0][-2]", "invalid JSON path syntax: negative array index"},
		{"field unclosed bracket", "a[0", "invalid JSON path syntax: unclosed bracket"},
		{"field empty index", "a[]", `invalid JSON path syntax: invalid array index ""`},
		{"field wildcard", "a[*].b", `invalid JSON path syntax: invalid array index "*"`},
		{"field negative index", "a[-1]", "invalid JSON path syntax: negative array index"},
		{"field spaced index", "a[ 1]", `invalid JSON path syntax: invalid array index " 1"`},
		{"field overflowing index", "a[99999999999999999999]", `invalid JSON path syntax: invalid array index "99999999999999999999"`},
		{"error after valid segments", "a.b[0].c[-3]", "invalid JSON path syntax: negative array index"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseJSONPath(tc.path)
			if err == nil {
				t.Fatalf("parseJSONPath(%q) = %#v, want error %q", tc.path, got, tc.wantErr)
			}
			if got != nil {
				t.Errorf("parseJSONPath(%q) segments = %#v, want nil on error", tc.path, got)
			}
			if !errors.Is(err, ErrInvalidJSONPath) {
				t.Errorf("parseJSONPath(%q) error %v does not wrap ErrInvalidJSONPath", tc.path, err)
			}
			if err.Error() != tc.wantErr {
				t.Errorf("parseJSONPath(%q) error = %q, want %q", tc.path, err.Error(), tc.wantErr)
			}
		})
	}
}
