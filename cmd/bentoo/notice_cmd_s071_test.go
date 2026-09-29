package main

// Story 071, sub-task 7.1: `bentoo notice new` and `bentoo notice revise <id>`
// run through the real root command (newTestCLI), write both files, print
// every path written and the next steps, and perform no git operation
// (R1.1, R1.8, R2.2, R2.6, R4.2, R5.1, R6.2).
//
// "No git operation" is observed, not assumed: both repositories are real git
// repositories with no commit; after the command, nothing is staged and there
// is still no commit.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type noticeEnvS071 struct {
	c        *testCLI
	site     string
	bodyFile string
}

func noticeEnvSetupS071(t *testing.T, withSite bool) noticeEnvS071 {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	c := newTestCLI(t)
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	e := noticeEnvS071{c: c}
	noticeGitS071(t, c.Overlay(), "init", "-q")
	if withSite {
		e.site = filepath.Join(c.Home(), "site")
		if err := os.MkdirAll(filepath.Join(e.site, "src", "content", "notices"), 0o755); err != nil {
			t.Fatal(err)
		}
		noticeGitS071(t, e.site, "init", "-q")
		appendHarnessConfig(t, c, "notice:\n  site_path: "+e.site+"\n")
	}
	e.bodyFile = filepath.Join(c.Home(), "body.txt")
	if err := os.WriteFile(e.bodyFile, []byte("Upgrade dev-libs/foo now.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return e
}

func noticeGitS071(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return string(out)
}

func assertNoGitOperationS071(t *testing.T, dir string) {
	t.Helper()
	if staged := noticeGitS071(t, dir, "diff", "--cached", "--name-only"); strings.TrimSpace(staged) != "" {
		t.Errorf("files were staged in %s: %q", dir, staged)
	}
	if out, err := exec.Command("git", "-C", dir, "rev-parse", "--verify", "-q", "HEAD").CombinedOutput(); err == nil {
		t.Errorf("a commit was created in %s: %s", dir, out)
	}
}

func securityArgsS071(e noticeEnvS071) []string {
	return []string{"notice", "new",
		"--type", "security", "--severity", "critical",
		"--title", "foo 1.2 heap overflow", "--summary", "One-paragraph summary.",
		"--name", "foo-cve", "--published", "2026-09-28",
		"--affects", "dev-libs/foo:1 >=1.0,<1.2.3",
		"--body-file", e.bodyFile,
	}
}

func TestNoticeCmd_NewWritesBothFilesAndPrintsTheNextSteps(t *testing.T) {
	e := noticeEnvSetupS071(t, true)
	stdout, stderr, code := e.c.Run(securityArgsS071(e)...)
	if code != 0 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	id := "2026-09-28-foo-cve"
	newsPath := filepath.Join(e.c.Overlay(), "metadata", "news", id, id+".en.txt")
	sitePath := filepath.Join(e.site, "src", "content", "notices", id+".yaml")
	for _, p := range []string{newsPath, sitePath} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was not written: %v", p, err)
		}
		if !strings.Contains(stdout, p) {
			t.Errorf("stdout does not print the written path %s:\n%s", p, stdout)
		}
	}
	for _, step := range []string{"commit", "push"} {
		if !strings.Contains(strings.ToLower(stdout), step) {
			t.Errorf("stdout does not name the next step %q:\n%s", step, stdout)
		}
	}
	if !strings.Contains(stdout+stderr, "every installed version") {
		t.Errorf("the multi-range --affects warning was not printed:\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	news, _ := os.ReadFile(newsPath)
	if !strings.Contains(string(news), "Author: Test <test@test.com>\n") {
		t.Errorf("--author did not default to the configured git user:\n%s", news)
	}
	assertNoGitOperationS071(t, e.c.Overlay())
	assertNoGitOperationS071(t, e.site)
}

func TestNoticeCmd_NewWithoutASitePathPrintsTheYAML(t *testing.T) {
	e := noticeEnvSetupS071(t, false)
	stdout, stderr, code := e.c.Run(securityArgsS071(e)...)
	if code != 0 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "id: 2026-09-28-foo-cve") || !strings.Contains(stdout, "cp: dev-libs/foo") {
		t.Errorf("with no notice.site_path the notice YAML must be printed:\n%s", stdout)
	}
	id := "2026-09-28-foo-cve"
	if _, err := os.Stat(filepath.Join(e.c.Overlay(), "metadata", "news", id, id+".en.txt")); err != nil {
		t.Errorf("the news item was not written: %v", err)
	}
	assertNoGitOperationS071(t, e.c.Overlay())
}

func TestNoticeCmd_PublishedDefaultsToTodayInUTC(t *testing.T) {
	e := noticeEnvSetupS071(t, false)
	before := time.Now().UTC().Format(time.DateOnly)
	args := []string{"notice", "new", "--type", "news", "--severity", "info",
		"--title", "t", "--summary", "s", "--name", "today", "--body-file", e.bodyFile}
	stdout, stderr, code := e.c.Run(args...)
	after := time.Now().UTC().Format(time.DateOnly)
	if code != 0 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	found := false
	for _, day := range []string{before, after} {
		id := day + "-today"
		if _, err := os.Stat(filepath.Join(e.c.Overlay(), "metadata", "news", id, id+".en.txt")); err == nil {
			found = true
		}
	}
	if !found {
		t.Errorf("no news item dated today (UTC %s) was written", before)
	}
}

func TestNoticeCmd_InvalidInputWritesNothing(t *testing.T) {
	cases := []struct {
		name string
		edit func([]string) []string
		flag string
	}{
		{"type", func(a []string) []string { return replaceFlagS071(a, "--type", "advisory") }, "--type"},
		{"severity", func(a []string) []string { return replaceFlagS071(a, "--severity", "high") }, "--severity"},
		{"name", func(a []string) []string { return replaceFlagS071(a, "--name", "Foo.Bar") }, "--name"},
		{"title", func(a []string) []string { return replaceFlagS071(a, "--title", strings.Repeat("t", 51)) }, "--title"},
		{"affects", func(a []string) []string { return replaceFlagS071(a, "--affects", "dev-libs/foo >=oops") }, "--affects"},
		{"no affects", func(a []string) []string { return dropFlagS071(a, "--affects") }, "--affects"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := noticeEnvSetupS071(t, true)
			stdout, stderr, code := e.c.Run(c.edit(securityArgsS071(e))...)
			if code == 0 {
				t.Fatalf("invalid %s was accepted\nstdout:\n%s", c.name, stdout)
			}
			if !strings.Contains(stderr, c.flag) {
				t.Errorf("stderr does not name %s:\n%s", c.flag, stderr)
			}
			if _, err := os.Stat(filepath.Join(e.c.Overlay(), "metadata", "news")); err == nil {
				entries, _ := os.ReadDir(filepath.Join(e.c.Overlay(), "metadata", "news"))
				if len(entries) != 0 {
					t.Errorf("a news item was written for invalid input: %v", entries)
				}
			}
			if entries, _ := os.ReadDir(filepath.Join(e.site, "src", "content", "notices")); len(entries) != 0 {
				t.Errorf("a site file was written for invalid input: %v", entries)
			}
		})
	}
}

func replaceFlagS071(args []string, flag, value string) []string {
	out := append([]string(nil), args...)
	for i := range out {
		if out[i] == flag && i+1 < len(out) {
			out[i+1] = value
		}
	}
	return out
}

func dropFlagS071(args []string, flag string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		if args[i] == flag {
			i++
			continue
		}
		out = append(out, args[i])
	}
	return out
}

// editorScriptS071 writes an executable that records each argument it gets
// on its own line, then applies action to the file named by its last one.
func editorScriptS071(t *testing.T, dir, action string) (script, argsLog string) {
	t.Helper()
	script = filepath.Join(dir, "fake-editor")
	argsLog = filepath.Join(dir, "editor-args.log")
	body := "#!/bin/sh\n" +
		"for a in \"$@\"; do printf '%s\\n' \"$a\" >> '" + argsLog + "'; last=$a; done\n" +
		action + "\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script, argsLog
}

// R2.2 + R2.6 through the real process runner: the editor from $EDITOR runs
// with its words as literal arguments; `;` never separates two commands.
func TestNoticeCmd_NewRunsTheEditorWithoutAShell(t *testing.T) {
	e := noticeEnvSetupS071(t, true)
	script, argsLog := editorScriptS071(t, e.c.Home(), `printf 'Written in the editor.\n' > "$last"`)
	t.Setenv("EDITOR", script+" ; --marker")

	args := dropFlagS071(securityArgsS071(e), "--body-file")
	stdout, stderr, code := e.c.Run(args...)
	if code != 0 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	logged, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatalf("the editor did not run: %v", err)
	}
	got := strings.Split(strings.TrimRight(string(logged), "\n"), "\n")
	if len(got) != 3 || got[0] != ";" || got[1] != "--marker" {
		t.Errorf("editor arguments = %q, want [; --marker <file>]", got)
	}
	id := "2026-09-28-foo-cve"
	news, _ := os.ReadFile(filepath.Join(e.c.Overlay(), "metadata", "news", id, id+".en.txt"))
	if !strings.Contains(string(news), "Written in the editor.") {
		t.Errorf("the editor's text did not reach the news item:\n%s", news)
	}
}

func TestNoticeCmd_ReviseBumpsBothFiles(t *testing.T) {
	e := noticeEnvSetupS071(t, true)
	if stdout, stderr, code := e.c.Run(securityArgsS071(e)...); code != 0 {
		t.Fatalf("notice new: exit %d\n%s\n%s", code, stdout, stderr)
	}
	script, _ := editorScriptS071(t, e.c.Home(), `printf 'Added line.\n' >> "$last"`)
	t.Setenv("EDITOR", script)

	id := "2026-09-28-foo-cve"
	stdout, stderr, code := e.c.Run("notice", "revise", id, "--title", "foo 1.2 heap overflow, fixed")
	if code != 0 {
		t.Fatalf("notice revise: exit %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	newsPath := filepath.Join(e.c.Overlay(), "metadata", "news", id, id+".en.txt")
	sitePath := filepath.Join(e.site, "src", "content", "notices", id+".yaml")
	news, _ := os.ReadFile(newsPath)
	site, _ := os.ReadFile(sitePath)
	for _, want := range []string{"Revision: 2\n", "Title: foo 1.2 heap overflow, fixed\n", "Added line."} {
		if !strings.Contains(string(news), want) {
			t.Errorf("the news item lacks %q:\n%s", want, news)
		}
	}
	for _, want := range []string{"Added line.", "fixed"} {
		if !strings.Contains(string(site), want) {
			t.Errorf("the site file lacks %q:\n%s", want, site)
		}
	}
	for _, p := range []string{newsPath, sitePath} {
		if !strings.Contains(stdout, p) {
			t.Errorf("stdout does not print the revised path %s:\n%s", p, stdout)
		}
	}
	assertNoGitOperationS071(t, e.c.Overlay())
	assertNoGitOperationS071(t, e.site)
}

func TestNoticeCmd_HelpListsEveryFlag(t *testing.T) {
	c := newTestCLI(t)
	stdout, stderr, code := c.Run("notice", "new", "--help")
	if code != 0 {
		t.Fatalf("notice new --help: exit %d\n%s", code, stderr)
	}
	for _, f := range []string{"--type", "--severity", "--title", "--summary", "--name", "--affects", "--published", "--author", "--body-file"} {
		if !strings.Contains(stdout, f) {
			t.Errorf("notice new --help does not list %s:\n%s", f, stdout)
		}
	}
	stdout, stderr, code = c.Run("notice", "revise", "--help")
	if code != 0 {
		t.Fatalf("notice revise --help: exit %d\n%s", code, stderr)
	}
	for _, f := range []string{"--severity", "--title", "--summary", "--affects"} {
		if !strings.Contains(stdout, f) {
			t.Errorf("notice revise --help does not list %s:\n%s", f, stdout)
		}
	}
}
