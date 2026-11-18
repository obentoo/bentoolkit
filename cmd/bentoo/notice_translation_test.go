package main

// `bentoo notice new|revise --translation <lang>=<file>` writes
// `<id>.<lang>.txt` next to the English news item and keeps it at the same
// Revision; a bad translation is refused before anything is written.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNoticeCmd_TranslationIsWrittenAndRevisedWithTheItem(t *testing.T) {
	e := noticeEnvSetupS071(t, true)
	ptFile := filepath.Join(e.c.Home(), "pt_BR.txt")
	if err := os.WriteFile(ptFile, []byte("Title: Estouro de heap no foo 1.2\nTranslator: T <t@example.org>\n\nAtualize dev-libs/foo agora.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := append(securityArgsS071(e), "--translation", "pt_BR="+ptFile)
	stdout, stderr, code := e.c.Run(args...)
	if code != 0 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	id := "2026-09-28-foo-cve"
	ptPath := filepath.Join(e.c.Overlay(), "metadata", "news", id, id+".pt_BR.txt")
	got, err := os.ReadFile(ptPath)
	if err != nil {
		t.Fatalf("the translation was not written: %v", err)
	}
	if !strings.Contains(string(got), "Translator: T <t@example.org>\nPosted: 2026-09-28\nRevision: 1\n") {
		t.Errorf("pt_BR item:\n%s", got)
	}
	if !strings.Contains(stdout, ptPath) {
		t.Errorf("stdout does not print %s:\n%s", ptPath, stdout)
	}

	stdout, stderr, code = e.c.Run("notice", "revise", id, "--body-file", e.bodyFile, "--title", "foo 1.2 overflow")
	if code != 0 {
		t.Fatalf("revise: exit %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	got, _ = os.ReadFile(ptPath)
	if !strings.Contains(string(got), "Revision: 2\n") || !strings.Contains(string(got), "Title: Estouro de heap no foo 1.2\n") {
		t.Errorf("revise did not keep pt_BR in step:\n%s", got)
	}
	if !strings.Contains(stderr, "translation pt_BR keeps its old title and text") {
		t.Errorf("no stale-translation warning:\n%s", stderr)
	}
	assertNoGitOperationS071(t, e.c.Overlay())
}

func TestNoticeCmd_BadTranslationWritesNothing(t *testing.T) {
	e := noticeEnvSetupS071(t, false)
	bad := filepath.Join(e.c.Home(), "bad.txt")
	if err := os.WriteFile(bad, []byte("Title: T\n\nText.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, spec := range []string{"pt-BR=" + bad, "es=" + bad + ".missing", "es"} {
		args := append(securityArgsS071(e), "--translation", spec)
		if _, _, code := e.c.Run(args...); code == 0 {
			t.Errorf("--translation %q was accepted", spec)
		}
		if _, err := os.Stat(filepath.Join(e.c.Overlay(), "metadata", "news", "2026-09-28-foo-cve")); err == nil {
			t.Fatalf("--translation %q: the news item was written", spec)
		}
	}
}
