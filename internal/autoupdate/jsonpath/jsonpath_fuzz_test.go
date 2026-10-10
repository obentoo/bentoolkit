package jsonpath_test

import (
	"errors"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/jsonpath"
	"github.com/obentoo/bentoolkit/internal/autoupdate/parse"
)

// maxFuzzIndex bounds the arrays the fuzz target builds, so an accepted
// "[999999999]" does not allocate a billion-element slice.
const maxFuzzIndex = 1024

// leadingZeros matches the zeros an index may carry before its first
// significant digit, which the canonical rendering drops.
var leadingZeros = regexp.MustCompile(`\[0+([0-9])`)

// FuzzJSONPath is Q11 of story 087: Parse never panics; a rejection wraps
// ErrInvalidJSONPath, quotes the path and returns no segments; an accepted
// path re-renders to the same segments, and a document built from its
// segments navigates, through parse.NavigateJSONPath, to the planted leaf.
func FuzzJSONPath(f *testing.F) {
	for _, seed := range []string{
		// Paths the real overlay registry uses.
		"tag_name", "[0].name", "info.version", "[0].commit.committer.date",
		"versions[0].version", "STABLE[0].version", "dist-tags.latest",
		// Inside the grammar but unusual.
		"a.0", "a[007]", "a[1][0].b", "[0][1]", "a b.c", "a*.b", "versão.x",
		// Outside the grammar.
		"", ".", "a..b", ".a", "a.", "a.[1]", "[0]..b", "a[0]b", "[0]b",
		"a[+1]", "a[-1]", "a[ 1]", "a[]", "a[*]", "[*].tag", "a[0", "a]",
		"a]b", "a[0]]", "a[99999999999999999999]", "\xff[0]",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, path string) {
		segments, err := jsonpath.Parse(path)
		if err != nil {
			checkRejection(t, path, segments, err)
			return
		}
		if len(segments) == 0 {
			t.Fatalf("Parse(%q) accepted the path with no segments", path)
		}

		rendered := render(segments)
		// Only leading zeros of an index may differ between a path and its
		// canonical rendering; anything else means Parse accepted a path the
		// grammar does not describe and read it as a different one.
		if want := leadingZeros.ReplaceAllString(path, "[$1"); rendered != want {
			t.Fatalf("Parse(%q) = %#v renders as %q; an accepted path must render back to itself (%q)", path, segments, rendered, want)
		}
		again, err := jsonpath.Parse(rendered)
		if err != nil {
			t.Fatalf("Parse(%q) = %#v, but its rendering %q is rejected: %v", path, segments, rendered, err)
		}
		if !reflect.DeepEqual(again, segments) {
			t.Fatalf("Parse(%q) = %#v, Parse(rendering %q) = %#v", path, segments, rendered, again)
		}

		const leaf = "leaf"
		doc, ok := build(segments, leaf)
		if !ok {
			return
		}
		got, err := parse.NavigateJSONPath(doc, path)
		if err != nil {
			t.Fatalf("NavigateJSONPath(doc built from %#v, %q) error = %v", segments, path, err)
		}
		if got != leaf {
			t.Fatalf("NavigateJSONPath(doc built from %#v, %q) = %#v, want %q", segments, path, got, leaf)
		}
	})
}

func checkRejection(t *testing.T, path string, segments []jsonpath.Segment, err error) {
	t.Helper()
	if segments != nil {
		t.Errorf("Parse(%q) returned segments %#v alongside error %v", path, segments, err)
	}
	if !errors.Is(err, jsonpath.ErrInvalidJSONPath) {
		t.Errorf("Parse(%q) error %v does not wrap ErrInvalidJSONPath", path, err)
	}
	if q := strconv.Quote(path); !strings.Contains(err.Error(), q) {
		t.Errorf("Parse(%q) error %q does not quote the path as %s", path, err, q)
	}
}

// render writes segments back in canonical form: "." before every field but
// a leading one, "[N]" for every index.
func render(segments []jsonpath.Segment) string {
	var b strings.Builder
	for i, seg := range segments {
		switch seg.Kind {
		case jsonpath.KindField:
			if i > 0 {
				b.WriteByte('.')
			}
			b.WriteString(seg.Field)
		case jsonpath.KindIndex:
			b.WriteByte('[')
			b.WriteString(strconv.Itoa(seg.Index))
			b.WriteByte(']')
		}
	}
	return b.String()
}

// build returns a JSON-shaped document in which segments lead to leaf, or
// false when an index exceeds maxFuzzIndex.
func build(segments []jsonpath.Segment, leaf string) (interface{}, bool) {
	var doc interface{} = leaf
	for i := len(segments) - 1; i >= 0; i-- {
		seg := segments[i]
		switch seg.Kind {
		case jsonpath.KindField:
			doc = map[string]interface{}{seg.Field: doc}
		case jsonpath.KindIndex:
			if seg.Index > maxFuzzIndex {
				return nil, false
			}
			arr := make([]interface{}, seg.Index+1)
			arr[seg.Index] = doc
			doc = arr
		}
	}
	return doc, true
}
