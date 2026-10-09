package parse

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

// Characterization tests: they pin what extractVersionsFromPath returns today
// for wildcard and plain array paths, including skipped items and the cap.

func s086DecodeJSON(t *testing.T, raw string) interface{} {
	t.Helper()
	var data interface{}
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		t.Fatalf("decode fixture %q: %v", raw, err)
	}
	return data
}

func TestS086ExtractVersionsFromPathResults(t *testing.T) {
	tests := []struct {
		name  string
		path  string
		limit int
		data  string
		want  []string
	}{
		// Wildcard, direct items.
		{"wildcard skips non-scalar items", "[*]", 0, `[null, {"v":"x"}, ["y"], "1.0"]`, []string{"1.0"}},
		{"wildcard stringifies numbers and bools", "[*]", 0, `[1, 1.5, true, "2.0"]`, []string{"1", "1.5", "true", "2.0"}},
		{"wildcard keeps empty strings", "[*]", 0, `["", "1.0"]`, []string{"", "1.0"}},
		{"wildcard keeps duplicates", "[*]", 0, `["1.0", "1.0"]`, []string{"1.0", "1.0"}},
		{"wildcard limit stops early", "[*]", 2, `["1", "2", "3"]`, []string{"1", "2"}},
		{"wildcard negative limit is unlimited", "[*]", -1, `["1","2","3","4","5","6","7","8","9","10","11","12"]`,
			[]string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12"}},
		{"wildcard zero limit caps at ten", "[*]", 0, `["1","2","3","4","5","6","7","8","9","10","11","12"]`,
			[]string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10"}},
		// Wildcard, nested field.
		{"wildcard field skips missing and non-objects", "[*].tag", 0, `[{"name":"a"}, "plain", {"tag":"v2"}]`, []string{"v2"}},
		{"wildcard field skips non-scalar leaves", "[*].tag", 0, `[{"tag":null}, {"tag":{"x":1}}, {"tag":["v"]}, {"tag":"v3"}]`, []string{"v3"}},
		{"wildcard field stringifies a number leaf", "[*].tag", 0, `[{"tag":7}]`, []string{"7"}},
		{"wildcard field without dot", "[*]tag", 0, `[{"tag":"v1"}]`, []string{"v1"}},
		{"wildcard nested index into inner arrays", "[*][0]", 0, `[["1","2"], [], ["3"]]`, []string{"1", "3"}},
		{"wildcard deep path", "[*].a.b[1]", 0, `[{"a":{"b":["x","y"]}}, {"a":{"b":["z"]}}]`, []string{"y"}},
		{"wildcard field limit counts only kept items", "[*].tag", 1, `[{"no":1}, {"tag":"a"}, {"tag":"b"}]`, []string{"a"}},
		// Plain path to an array.
		{"array skips non-scalar items", "list", 0, `{"list":[null, {"v":"x"}, ["y"], "1.0"]}`, []string{"1.0"}},
		{"array drops empty strings", "list", 0, `{"list":["", "1.0", ""]}`, []string{"1.0"}},
		{"array stringifies numbers and bools", "list", 0, `{"list":[3, 2.5, false]}`, []string{"3", "2.5", "false"}},
		{"array limit stops early", "list", 2, `{"list":["1","2","3"]}`, []string{"1", "2"}},
		{"array limit ignores dropped empties", "list", 2, `{"list":["", "1", "", "2", "3"]}`, []string{"1", "2"}},
		{"array zero limit caps at ten", "d.list", 0, `{"d":{"list":["1","2","3","4","5","6","7","8","9","10","11"]}}`,
			[]string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10"}},
		{"array negative limit is unlimited", "list", -1, `{"list":["1","2","3","4","5","6","7","8","9","10","11"]}`,
			[]string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11"}},
		{"indexed path to an inner array", "[1]", 0, `[["a"], ["b","c"]]`, []string{"b", "c"}},
		{"empty path uses the root array", "", 0, `["r1", "r2"]`, []string{"r1", "r2"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := &JSONVersionHistoryExtractor{VersionsPath: tc.path, Limit: tc.limit}
			got, err := e.extractVersionsFromPath(s086DecodeJSON(t, tc.data))
			if err != nil {
				t.Fatalf("extractVersionsFromPath(%q) error = %v, want nil", tc.path, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("extractVersionsFromPath(%q) = %#v, want %#v", tc.path, got, tc.want)
			}
		})
	}
}

func TestS086ExtractVersionsFromPathErrors(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		data    string
		wantIs  error
		wantErr string
	}{
		{"wildcard on object", "[*]", `{"a":1}`, ErrJSONPathNotFound,
			"JSON path not found in response: expected array for [*] path"},
		{"wildcard on empty array", "[*]", `[]`, ErrJSONPathNotFound,
			"JSON path not found in response: no versions found at path"},
		{"wildcard all items non-scalar", "[*]", `[null, {}, []]`, ErrJSONPathNotFound,
			"JSON path not found in response: no versions found at path"},
		{"wildcard field missing everywhere", "[*].tag", `[{"a":1}, {"b":2}]`, ErrJSONPathNotFound,
			"JSON path not found in response: no versions found at path"},
		{"wildcard field leaves all non-scalar", "[*].tag", `[{"tag":null}, {"tag":[]}]`, ErrJSONPathNotFound,
			"JSON path not found in response: no versions found at path"},
		{"inner wildcard is skipped per item", "[*].a[*]", `[{"a":["1"]}]`, ErrJSONPathNotFound,
			"JSON path not found in response: no versions found at path"},
		{"array path missing field", "list", `{"other":[]}`, ErrJSONPathNotFound,
			`JSON path not found in response: field "list" not found`},
		{"array path invalid syntax", "list[x]", `{"list":[]}`, ErrInvalidJSONPath,
			`invalid JSON path syntax: invalid array index "x"`},
		{"array path non-leading wildcard", "list[*]", `{"list":["1"]}`, ErrInvalidJSONPath,
			`invalid JSON path syntax: invalid array index "*"`},
		{"array path index out of bounds", "list[3]", `{"list":[["1"]]}`, ErrJSONPathNotFound,
			"JSON path not found in response: array index 3 out of bounds (length 1)"},
		{"array path not an array", "list", `{"list":"1.0"}`, ErrJSONPathNotFound,
			"JSON path not found in response: expected array at path"},
		{"array path object leaf", "list", `{"list":{"v":"1.0"}}`, ErrJSONPathNotFound,
			"JSON path not found in response: expected array at path"},
		{"array path empty array", "list", `{"list":[]}`, ErrJSONPathNotFound,
			"JSON path not found in response: no versions found in array"},
		{"array path only empty strings", "list", `{"list":["", ""]}`, ErrJSONPathNotFound,
			"JSON path not found in response: no versions found in array"},
		{"array path only non-scalar items", "list", `{"list":[null, {}, []]}`, ErrJSONPathNotFound,
			"JSON path not found in response: no versions found in array"},
		{"empty path on object root", "", `{"a":1}`, ErrJSONPathNotFound,
			"JSON path not found in response: expected array at path"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := &JSONVersionHistoryExtractor{VersionsPath: tc.path}
			got, err := e.extractVersionsFromPath(s086DecodeJSON(t, tc.data))
			if err == nil {
				t.Fatalf("extractVersionsFromPath(%q) = %#v, want error %q", tc.path, got, tc.wantErr)
			}
			if got != nil {
				t.Errorf("extractVersionsFromPath(%q) versions = %#v, want nil on error", tc.path, got)
			}
			if !errors.Is(err, tc.wantIs) {
				t.Errorf("extractVersionsFromPath(%q) error %v does not wrap %v", tc.path, err, tc.wantIs)
			}
			if err.Error() != tc.wantErr {
				t.Errorf("extractVersionsFromPath(%q) error = %q, want %q", tc.path, err.Error(), tc.wantErr)
			}
		})
	}
}
