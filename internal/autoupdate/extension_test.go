package autoupdate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

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
