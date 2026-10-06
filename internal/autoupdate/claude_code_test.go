package autoupdate

import (
	"os"
	"path/filepath"
	"testing"
)

// =============================================================================
// Test harness (scripted exec seam — no real `claude` is ever invoked)
// =============================================================================

// stubLookPathFound forces claudeAvailable() to succeed regardless of the host
// PATH, so ClaudeCodeClient construction is deterministic. Restored via
// t.Cleanup. Every test that constructs a client (other than the explicit
// unavailable test) calls this first.
func stubLookPathFound(t *testing.T) {
	t.Helper()
	orig := lookPath
	lookPath = func(string) (string, error) { return "/usr/bin/claude", nil }
	t.Cleanup(func() { lookPath = orig })
	// The fixers moved to internal/autoupdate/fixer, whose own lookPath this
	// variable no longer reaches: a fake claude on PATH satisfies its check.
	// Tests drive the fixers through their exec seam, so it never runs (story 061).
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("staging a fake claude: %v", err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// flagValue returns the element immediately following the first occurrence of
// flag in args, plus whether the flag was found with a following value.
func flagValue(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}
