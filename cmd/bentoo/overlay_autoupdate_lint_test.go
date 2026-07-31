package main

import (
	"os"
	"path/filepath"
	"testing"
)

// writeLintRegistry creates an overlay whose .autoupdate/packages.toml holds
// content and returns the overlay path.
func writeLintRegistry(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	autoDir := filepath.Join(dir, ".autoupdate")
	if err := os.MkdirAll(autoDir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(autoDir, "packages.toml"), []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return dir
}

// TestRunLintClean pins that a registry obeying the record model exits without
// calling osExit at all.
func TestRunLintClean(t *testing.T) {
	dir := writeLintRegistry(t, `["dev-util/claude-code"]
url = "https://registry.npmjs.org/@anthropic-ai/claude-code"
parser = "json"
path = "dist-tags.latest"
comments = """
claude-code — npm dist-tags.latest is the stable channel.
"""
# END
`)

	if code := withExitIntercept(func() { runLint(dir) }); code != -1 {
		t.Fatalf("clean registry exited with %d, want no exit", code)
	}
}

// TestRunLintReportsAndExits pins the gate behavior: a violation exits non-zero
// so the command can be wired into a pre-commit hook.
func TestRunLintReportsAndExits(t *testing.T) {
	dir := writeLintRegistry(t, `# a banner comment floating outside every record

["dev-util/claude-code"]
url = "https://registry.npmjs.org/@anthropic-ai/claude-code"
parser = "json"
path = "dist-tags.latest"
`)

	if code := withExitIntercept(func() { runLint(dir) }); code != 1 {
		t.Fatalf("got exit %d, want 1", code)
	}
}

// TestRunLintMissingRegistry pins that a missing packages.toml is an error, not
// a silent pass — a lint that quietly succeeds on nothing is worse than none.
func TestRunLintMissingRegistry(t *testing.T) {
	if code := withExitIntercept(func() { runLint(t.TempDir()) }); code != 1 {
		t.Fatalf("got exit %d, want 1", code)
	}
}
