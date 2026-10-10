package parse

import (
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// Story 087, sub-task 3.1: one JSON path grammar (R4.1-R4.5), observed through
// NavigateJSONPath, with every path the real overlay registry uses still
// navigating (R10.1) and the leading [*] wildcard of a versions_path still
// extracting (R10.3).

func s087Decode(t *testing.T, doc string) interface{} {
	t.Helper()
	var v interface{}
	if err := json.Unmarshal([]byte(doc), &v); err != nil {
		t.Fatalf("test fixture %q is not JSON: %v", doc, err)
	}
	return v
}

// TestS087_3_1_MalformedPathsAreRejected is the hostile half: every document
// below is built so that the LENIENT reading of the malformed path would
// resolve to a value. A grammar that is not enforced therefore returns "x"
// instead of an error, and a grammar enforced only as a navigation miss
// returns ErrJSONPathNotFound instead of ErrInvalidJSONPath.
func TestS087_3_1_MalformedPathsAreRejected(t *testing.T) {
	tests := []struct {
		name string
		req  string
		path string
		doc  string
	}{
		// R4.2: empty field.
		{"doubled dot", "R4.2", "a..b", `{"a":{"b":"x"}}`},
		{"leading dot", "R4.2", ".a", `{"a":"x"}`},
		{"trailing dot", "R4.2", "a.", `{"a":"x"}`},
		{"lone dot", "R4.2", ".", `{"":"x"}`},
		{"dot before an index", "R4.1/R4.2", "a.[1]", `{"a":["y","x"]}`},
		{"leading dot before an index", "R4.1/R4.2", ".[0]", `["x"]`},
		{"doubled dot after an index", "R4.2", "[0]..b", `[{"b":"x"}]`},
		// R4.3: a character other than '.' or '[' right after ']'.
		{"field glued to an index", "R4.3", "a[0]b", `{"a":[{"b":"x"}]}`},
		{"field glued to a leading index", "R4.3", "[0]b", `[{"b":"x"}]`},
		// R4.4: an index that is not plain ASCII digits.
		{"plus-signed index", "R4.4", "a[+1]", `{"a":["y","x"]}`},
		{"negative index", "R4.4", "a[-1]", `{"a":["x"]}`},
		{"space-padded index", "R4.4", "a[ 1]", `{"a":["y","x"]}`},
		{"empty index", "R4.4", "a[]", `{"a":["x"]}`},
		{"non-leading wildcard", "R4.4", "a[*]", `{"a":["x"]}`},
		{"letter index", "R4.4", "a[x]", `{"a":{"x":"x"}}`},
		{"unclosed index", "R4.4", "a[0", `{"a":["x"]}`},
		// R4.5: a ']' that closes no '['.
		{"stray closing bracket after a field", "R4.5", "a]", `{"a]":"x"}`},
		{"closing bracket inside a field", "R4.5", "a]b", `{"a]b":"x"}`},
		{"doubled closing bracket", "R4.3/R4.5", "a[0]]", `{"a":[{"]":"x"}]}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NavigateJSONPath(s087Decode(t, tc.doc), tc.path)
			if err == nil {
				t.Fatalf("%s: NavigateJSONPath(%q) = %#v, nil; want an ErrInvalidJSONPath error", tc.req, tc.path, got)
			}
			if got != nil {
				t.Errorf("%s: NavigateJSONPath(%q) value = %#v, want nil on error", tc.req, tc.path, got)
			}
			if !errors.Is(err, ErrInvalidJSONPath) {
				t.Errorf("%s: NavigateJSONPath(%q) error %q does not wrap ErrInvalidJSONPath", tc.req, tc.path, err)
			}
			if q := strconv.Quote(tc.path); !strings.Contains(err.Error(), q) {
				t.Errorf("%s: NavigateJSONPath(%q) error %q does not quote the path as %s", tc.req, tc.path, err, q)
			}
		})
	}
}

// TestS087_3_1_NearMalformedPathsStillNavigate is the converse: paths that
// look like the malformed ones above but are inside the R4.1 grammar. A
// grammar written too narrowly rejects them.
func TestS087_3_1_NearMalformedPathsStillNavigate(t *testing.T) {
	tests := []struct {
		name string
		path string
		doc  string
		want interface{}
	}{
		{"digit after a dot is a field", "a.0", `{"a":{"0":"x"}}`, "x"},
		{"leading-zero index", "a[007]", `{"a":["0","1","2","3","4","5","6","x"]}`, "x"},
		{"chained indices", "a[1][0].b", `{"a":[[{"b":"y"}],[{"b":"x"}]]}`, "x"},
		{"leading chained indices", "[0][1]", `[["y","x"]]`, "x"},
		{"index then field", "[1].b", `[{"b":"y"},{"b":"x"}]`, "x"},
		{"bare leading index", "[0]", `["x"]`, "x"},
		{"field with a hyphen", "dist-tags.latest", `{"dist-tags":{"latest":"x"}}`, "x"},
		{"field with a space", "a b.c", `{"a b":{"c":"x"}}`, "x"},
		{"field with a star", "a*.b", `{"a*":{"b":"x"}}`, "x"},
		{"non-ASCII field", "versão.x", `{"versão":{"x":"x"}}`, "x"},
		{"index yields an object", "a[0]", `{"a":[{"k":"v"}]}`, map[string]interface{}{"k": "v"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NavigateJSONPath(s087Decode(t, tc.doc), tc.path)
			if err != nil {
				t.Fatalf("NavigateJSONPath(%q) error = %v, want %#v", tc.path, err, tc.want)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("NavigateJSONPath(%q) = %#v, want %#v", tc.path, got, tc.want)
			}
		})
	}
}

// TestS087_3_1_RealRegistryPathsStillNavigate pins R10.1: the 24 distinct JSON
// paths the real overlay registry uses (path and commit_sha_path) reach the
// same value. Each document carries a decoy sibling so a wrong index or field
// lands on a different value.
func TestS087_3_1_RealRegistryPathsStillNavigate(t *testing.T) {
	tests := []struct {
		path string
		doc  string
	}{
		{"tag_name", `{"tag_name":"x","name":"decoy"}`},
		{"[0].name", `[{"name":"x"},{"name":"decoy"}]`},
		{"info.version", `{"info":{"version":"x"},"version":"decoy"}`},
		{"[0].commit.committer.date", `[{"commit":{"committer":{"date":"x"},"author":{"date":"decoy"}}},{"commit":{"committer":{"date":"decoy"}}}]`},
		{"[0].sha", `[{"sha":"x"},{"sha":"decoy"}]`},
		{"version", `{"version":"x","info":{"version":"decoy"}}`},
		{"[0].committed_date", `[{"committed_date":"x"},{"committed_date":"decoy"}]`},
		{"[0].id", `[{"id":"x"},{"id":"decoy"}]`},
		{"dist-tags.latest", `{"dist-tags":{"latest":"x","next":"decoy"}}`},
		{"[0].tag_name", `[{"tag_name":"x"},{"tag_name":"decoy"}]`},
		{"LATEST_THUNDERBIRD_VERSION", `{"LATEST_THUNDERBIRD_VERSION":"x","LATEST_THUNDERBIRD_DEVEL_VERSION":"decoy"}`},
		{"versions[0].version", `{"versions":[{"version":"x"},{"version":"decoy"}]}`},
		{"Version", `{"Version":"x","version":"decoy"}`},
		{"STABLE[0].version", `{"STABLE":[{"version":"x"},{"version":"decoy"}],"BETA":[{"version":"decoy"}]}`},
		{"Releases[0].Version", `{"Releases":[{"Version":"x"},{"Version":"decoy"}]}`},
		{"name", `{"name":"x","tag_name":"decoy"}`},
		{"latestVersion", `{"latestVersion":"x","version":"decoy"}`},
		{"entries[0].version", `{"entries":[{"version":"x"},{"version":"decoy"}]}`},
		{"currentRelease", `{"currentRelease":"x","previousRelease":"decoy"}`},
		{"crate.max_stable_version", `{"crate":{"max_stable_version":"x","max_version":"decoy"}}`},
		{"commit.committer.date", `{"commit":{"committer":{"date":"x"},"author":{"date":"decoy"}}}`},
		{"[0].version", `[{"version":"x"},{"version":"decoy"}]`},
		{"sha", `{"sha":"x","commit":{"sha":"decoy"}}`},
		{"commitSha", `{"commitSha":"x","sha":"decoy"}`},
	}
	if len(tests) != 24 {
		t.Fatalf("R10.1 names 24 distinct real paths, the table holds %d", len(tests))
	}
	seen := map[string]bool{}
	for _, tc := range tests {
		if seen[tc.path] {
			t.Fatalf("path %q listed twice; the 24 must be distinct", tc.path)
		}
		seen[tc.path] = true
		t.Run(tc.path, func(t *testing.T) {
			got, err := NavigateJSONPath(s087Decode(t, tc.doc), tc.path)
			if err != nil {
				t.Fatalf("NavigateJSONPath(%q) error = %v, want \"x\"", tc.path, err)
			}
			if got != "x" {
				t.Fatalf("NavigateJSONPath(%q) = %#v, want \"x\"", tc.path, got)
			}
		})
	}
}

// TestS087_3_1_LeadingWildcardStillExtracts pins R10.3: a versions_path that
// starts with [*] extracts one version per array item, through the public
// extractor, exactly as today.
func TestS087_3_1_LeadingWildcardStillExtracts(t *testing.T) {
	tests := []struct {
		name string
		path string
		doc  string
		want []string
	}{
		{"bare wildcard", "[*]", `["2.0","1.0"]`, []string{"2.0", "1.0"}},
		{"wildcard then field", "[*].tag_name", `[{"tag_name":"v2"},{"tag_name":"v1"}]`, []string{"v2", "v1"}},
		{"wildcard then nested field", "[*].commit.sha", `[{"commit":{"sha":"b"}},{"commit":{"sha":"a"}}]`, []string{"b", "a"}},
		{"wildcard then index", "[*].assets[0].name", `[{"assets":[{"name":"n2"},{"name":"decoy"}]},{"assets":[{"name":"n1"}]}]`, []string{"n2", "n1"}},
		{"wildcard skips items without the field", "[*].tag_name", `[{"tag_name":"v2"},{"name":"decoy"},{"tag_name":"v1"}]`, []string{"v2", "v1"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := &JSONVersionHistoryExtractor{VersionsPath: tc.path}
			got, err := e.ExtractVersions([]byte(tc.doc))
			if err != nil {
				t.Fatalf("ExtractVersions(%q) error = %v, want %v", tc.path, err, tc.want)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ExtractVersions(%q) = %#v, want %#v", tc.path, got, tc.want)
			}
		})
	}
}
