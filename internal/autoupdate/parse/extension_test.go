package parse

import (
	"errors"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/registry"
)

// --- RegexVersionHistoryExtractor -------------------------------------------

func TestRegexVersionHistoryExtractor(t *testing.T) {
	content := []byte(`gn-0.2122.tar.xz gn-0.2200.tar.xz gn-0.2374.tar.xz`)
	e := &RegexVersionHistoryExtractor{Pattern: `gn-([0-9][0-9.]*)\.tar\.xz`, Limit: -1}
	got, err := e.ExtractVersions(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"0.2122", "0.2200", "0.2374"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestRegexVersionHistoryExtractor_NoMatch(t *testing.T) {
	e := &RegexVersionHistoryExtractor{Pattern: `gn-([0-9.]+)\.tar\.xz`, Limit: -1}
	if _, err := e.ExtractVersions([]byte("nothing here")); !errors.Is(err, ErrRegexNoMatch) {
		t.Fatalf("want ErrRegexNoMatch, got %v", err)
	}
}

func TestRegexVersionHistoryExtractor_NoCaptureGroup(t *testing.T) {
	e := &RegexVersionHistoryExtractor{Pattern: `gn-[0-9.]+`, Limit: -1}
	if _, err := e.ExtractVersions([]byte("gn-1.2")); !errors.Is(err, ErrNoCaptureGroup) {
		t.Fatalf("want ErrNoCaptureGroup, got %v", err)
	}
}

func TestRegexVersionHistoryExtractor_LimitCaps(t *testing.T) {
	content := []byte("a-1.tar a-2.tar a-3.tar")
	e := &RegexVersionHistoryExtractor{Pattern: `a-([0-9]+)\.tar`, Limit: 2}
	got, err := e.ExtractVersions(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Limit=2 should cap to 2, got %d (%v)", len(got), got)
	}
}

// --- effectiveLimit / list-extractor cap ------------------------------------

func TestEffectiveLimit(t *testing.T) {
	cases := map[int]int{0: MaxVersionHistoryLimit, -1: -1, 3: 3}
	for in, want := range cases {
		if got := effectiveLimit(in); got != want {
			t.Fatalf("effectiveLimit(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestJSONExtractor_UnlimitedVsDefault(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("[")
	for i := 0; i < 15; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`"1.` + string(rune('0'+i%10)) + `"`)
	}
	sb.WriteString("]")
	content := []byte(sb.String())

	def := &JSONVersionHistoryExtractor{VersionsPath: "[*]"} // Limit 0 -> default 10
	gotDef, err := def.ExtractVersions(content)
	if err != nil {
		t.Fatalf("default: %v", err)
	}
	if len(gotDef) != MaxVersionHistoryLimit {
		t.Fatalf("default cap: got %d, want %d", len(gotDef), MaxVersionHistoryLimit)
	}

	unl := &JSONVersionHistoryExtractor{VersionsPath: "[*]", Limit: -1}
	gotUnl, err := unl.ExtractVersions(content)
	if err != nil {
		t.Fatalf("unlimited: %v", err)
	}
	if len(gotUnl) != 15 {
		t.Fatalf("unlimited: got %d, want 15", len(gotUnl))
	}
}

// --- newSelectExtractor ------------------------------------------------------

func TestNewSelectExtractor_JSONMapsIndexToWildcard(t *testing.T) {
	cfg := &registry.PackageConfig{Parser: "json", Path: "[0].name"}
	ext, err := NewSelectExtractor(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	je, ok := ext.(*JSONVersionHistoryExtractor)
	if !ok {
		t.Fatalf("want *JSONVersionHistoryExtractor, got %T", ext)
	}
	if je.VersionsPath != "[*].name" {
		t.Fatalf("path mapping: got %q, want %q", je.VersionsPath, "[*].name")
	}
	if je.Limit != -1 {
		t.Fatalf("select extractor must be unlimited, got Limit=%d", je.Limit)
	}
	// And it actually collects every element's field.
	got, err := ext.ExtractVersions([]byte(`[{"name":"1.0"},{"name":"2.0"}]`))
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if strings.Join(got, ",") != "1.0,2.0" {
		t.Fatalf("got %v, want [1.0 2.0]", got)
	}
}

func TestNewSelectExtractor_ScriptIsNotListCapable(t *testing.T) {
	ext, err := NewSelectExtractor(&registry.PackageConfig{Parser: "script", Script: "x"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ext != nil {
		t.Fatalf("script parser must not be list-capable, got %T", ext)
	}
}
