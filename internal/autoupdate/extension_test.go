package autoupdate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	cfg := &PackageConfig{Parser: "json", Path: "[0].name"}
	ext, err := newSelectExtractor(cfg)
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
	ext, err := newSelectExtractor(&PackageConfig{Parser: "script", Script: "x"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ext != nil {
		t.Fatalf("script parser must not be list-capable, got %T", ext)
	}
}

// --- resolveScript -----------------------------------------------------------

func TestResolveScript_Inline(t *testing.T) {
	got, err := resolveScript("return '1.0'", "/nonexistent")
	if err != nil || got != "return '1.0'" {
		t.Fatalf("inline: got %q err %v", got, err)
	}
}

func TestResolveScript_File(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "lo.js"), []byte("return '26.2'"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := resolveScript("@lo.js", dir)
	if err != nil || got != "return '26.2'" {
		t.Fatalf("file: got %q err %v", got, err)
	}
}

func TestResolveScript_RejectsTraversal(t *testing.T) {
	for _, ref := range []string{"@../secret.js", "@sub/lo.js", "@..", "@"} {
		if _, err := resolveScript(ref, t.TempDir()); err == nil {
			t.Fatalf("expected error for %q, got nil", ref)
		}
	}
}

func TestResolveScript_MissingFile(t *testing.T) {
	if _, err := resolveScript("@missing.js", t.TempDir()); err == nil {
		t.Fatal("expected error for missing file")
	}
}

// --- ScriptParser.ParseLive (fake evaluator) --------------------------------

type fakeEvaluator struct {
	out        string
	err        error
	gotURL     string
	gotScript  string
	gotHeaders map[string]string
}

func (f *fakeEvaluator) Evaluate(_ context.Context, url, script string, headers map[string]string) (string, error) {
	f.gotURL, f.gotScript, f.gotHeaders = url, script, headers
	return f.out, f.err
}

func TestScriptParser_ParseLive_TrimsAndPasses(t *testing.T) {
	fe := &fakeEvaluator{out: "  26.2.3.2\n"}
	p := &ScriptParser{URL: "https://x/", Script: "JS", Headers: map[string]string{"A": "b"}, eval: fe}
	got, err := p.ParseLive(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "26.2.3.2" {
		t.Fatalf("got %q, want trimmed %q", got, "26.2.3.2")
	}
	if fe.gotURL != "https://x/" || fe.gotScript != "JS" || fe.gotHeaders["A"] != "b" {
		t.Fatalf("evaluator did not receive expected args: %+v", fe)
	}
}

func TestScriptParser_ParseLive_NilEvalNotBuilt(t *testing.T) {
	p := &ScriptParser{URL: "https://x/", Script: "JS"}
	if _, err := p.ParseLive(context.Background()); !errors.Is(err, ErrScriptSupportNotBuilt) {
		t.Fatalf("want ErrScriptSupportNotBuilt, got %v", err)
	}
}

func TestScriptParser_ParseLive_PropagatesError(t *testing.T) {
	sentinel := errors.New("boom")
	p := &ScriptParser{eval: &fakeEvaluator{err: sentinel}}
	if _, err := p.ParseLive(context.Background()); !errors.Is(err, sentinel) {
		t.Fatalf("want sentinel error, got %v", err)
	}
}

// --- ValidatePackageConfig: new fields --------------------------------------

func TestValidatePackageConfig_Script(t *testing.T) {
	if err := ValidatePackageConfig("a/b", &PackageConfig{URL: "u", Parser: "script"}); !errors.Is(err, ErrMissingScript) {
		t.Fatalf("missing script: want ErrMissingScript, got %v", err)
	}
	if err := ValidatePackageConfig("a/b", &PackageConfig{URL: "u", Parser: "script", Script: "@lo.js"}); err != nil {
		t.Fatalf("valid script config: unexpected error %v", err)
	}
}

func TestValidatePackageConfig_Select(t *testing.T) {
	base := func(sel string) *PackageConfig {
		return &PackageConfig{URL: "u", Parser: "regex", Pattern: `(\d+)`, Select: sel}
	}
	for _, ok := range []string{"", "first", "max", "last"} {
		if err := ValidatePackageConfig("a/b", base(ok)); err != nil {
			t.Fatalf("select=%q should be valid, got %v", ok, err)
		}
	}
	if err := ValidatePackageConfig("a/b", base("highest")); !errors.Is(err, ErrInvalidSelect) {
		t.Fatalf("select=highest: want ErrInvalidSelect, got %v", err)
	}
}

func TestValidatePackageConfig_ScriptIgnoresTransformSelectWithWarn(t *testing.T) {
	lc := captureWarnLogs(t)
	cfg := &PackageConfig{
		URL: "u", Parser: "script", Script: "@lo.js",
		Transform: [][]string{{"-", "."}}, Select: "max",
	}
	if err := ValidatePackageConfig("a/b", cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(lc.all()) < 2 {
		t.Fatalf("expected warnings for transform+select with script, got %v", lc.all())
	}
}
