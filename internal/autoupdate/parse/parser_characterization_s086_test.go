package parse

import (
	"errors"
	"reflect"
	"testing"
)

// Characterization tests: they pin which segments a user-written path yields,
// so a restructuring cannot change them. Story 087 rewrote the rows that
// pinned a lenient reading of a malformed path into rejections (R4.2-R4.5),
// and every error row now carries the quoted path.

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
		// Leading-bracket paths.
		{"leading index", "[0]", []pathSegment{s086Index(0)}},
		{"leading index then field", "[0].tag_name", []pathSegment{s086Index(0), s086Field("tag_name")}},
		{"leading chained indices", "[3][4].x", []pathSegment{s086Index(3), s086Index(4), s086Field("x")}},
		// Odd field names are accepted verbatim.
		{"non-ASCII field", "versão.x", []pathSegment{s086Field("versão"), s086Field("x")}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parsePathSegments(tc.path)
			if err != nil {
				t.Fatalf("parsePathSegments(%q) error = %v, want nil", tc.path, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parsePathSegments(%q) = %#v, want %#v", tc.path, got, tc.want)
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
		{"empty path", "", `invalid JSON path syntax: empty path at offset 0 in ""`},
		{"lone dot", ".", `invalid JSON path syntax: empty field at offset 0 in "."`},
		{"only dots", "...", `invalid JSON path syntax: empty field at offset 0 in "..."`},
		{"leading unclosed bracket", "[0", `invalid JSON path syntax: unclosed '[' at offset 0 in "[0"`},
		{"leading lone bracket", "[", `invalid JSON path syntax: unclosed '[' at offset 0 in "["`},
		{"leading empty index", "[]", `invalid JSON path syntax: empty array index at offset 0 in "[]"`},
		{"leading non-numeric index", "[x]", `invalid JSON path syntax: array index is not ASCII digits at offset 1 in "[x]"`},
		{"leading wildcard", "[*].tag", `invalid JSON path syntax: array index is not ASCII digits at offset 1 in "[*].tag"`},
		{"leading negative index", "[-1]", `invalid JSON path syntax: array index is not ASCII digits at offset 1 in "[-1]"`},
		{"leading second index unclosed", "[0][1", `invalid JSON path syntax: unclosed '[' at offset 3 in "[0][1"`},
		{"leading second index invalid", "[0][y]", `invalid JSON path syntax: array index is not ASCII digits at offset 4 in "[0][y]"`},
		{"leading second index negative", "[0][-2]", `invalid JSON path syntax: array index is not ASCII digits at offset 4 in "[0][-2]"`},
		{"field unclosed bracket", "a[0", `invalid JSON path syntax: unclosed '[' at offset 1 in "a[0"`},
		{"field empty index", "a[]", `invalid JSON path syntax: empty array index at offset 1 in "a[]"`},
		{"field wildcard", "a[*].b", `invalid JSON path syntax: array index is not ASCII digits at offset 2 in "a[*].b"`},
		{"field negative index", "a[-1]", `invalid JSON path syntax: array index is not ASCII digits at offset 2 in "a[-1]"`},
		{"field spaced index", "a[ 1]", `invalid JSON path syntax: array index is not ASCII digits at offset 2 in "a[ 1]"`},
		{"field overflowing index", "a[99999999999999999999]", `invalid JSON path syntax: array index out of range at offset 2 in "a[99999999999999999999]"`},
		{"error after valid segments", "a.b[0].c[-3]", `invalid JSON path syntax: array index is not ASCII digits at offset 9 in "a.b[0].c[-3]"`},
		// Formerly accepted by a lenient reading; rejected since story 087.
		{"missing dot after index (R4.3)", "a[0]b", `invalid JSON path syntax: missing '.' or '[' after ']' at offset 4 in "a[0]b"`},
		{"leading dot (R4.2)", ".a[0].b", `invalid JSON path syntax: empty field at offset 0 in ".a[0].b"`},
		{"doubled dot (R4.2)", "a..b", `invalid JSON path syntax: empty field at offset 2 in "a..b"`},
		{"trailing dot (R4.2)", "a.", `invalid JSON path syntax: empty field at offset 2 in "a."`},
		{"plus-signed index (R4.4)", "a[+1]", `invalid JSON path syntax: array index is not ASCII digits at offset 2 in "a[+1]"`},
		{"leading index without dot (R4.3)", "[2]x", `invalid JSON path syntax: missing '.' or '[' after ']' at offset 3 in "[2]x"`},
		{"dot before an index (R4.1/R4.2)", "a.[1]", `invalid JSON path syntax: empty field at offset 2 in "a.[1]"`},
		{"stray closing bracket (R4.5)", "a[0]]", `invalid JSON path syntax: ']' closes no '[' at offset 4 in "a[0]]"`},
		{"closing bracket inside field (R4.5)", "a]b", `invalid JSON path syntax: ']' closes no '[' at offset 1 in "a]b"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parsePathSegments(tc.path)
			if err == nil {
				t.Fatalf("parsePathSegments(%q) = %#v, want error %q", tc.path, got, tc.wantErr)
			}
			if got != nil {
				t.Errorf("parsePathSegments(%q) segments = %#v, want nil on error", tc.path, got)
			}
			if !errors.Is(err, ErrInvalidJSONPath) {
				t.Errorf("parsePathSegments(%q) error %v does not wrap ErrInvalidJSONPath", tc.path, err)
			}
			if err.Error() != tc.wantErr {
				t.Errorf("parsePathSegments(%q) error = %q, want %q", tc.path, err.Error(), tc.wantErr)
			}
		})
	}
}
