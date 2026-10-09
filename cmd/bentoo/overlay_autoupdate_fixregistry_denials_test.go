package main

// Authored for story 051 (llm-agent-least-privilege), sub-task 4.2 — the
// registry-fix loop's "still failing after fix" line names the tools the agent
// was refused (S051-R5.2), never their input (S051-R5.1).
//
// The fixer is the REAL autoupdate.ClaudeCodeRegistryFixer, so the refusal
// travels through the production envelope parsing; only the `claude` child is
// scripted, through the exported WithRegistryFixerExecCommand seam. The
// constructor looks `claude` up on PATH, so a do-nothing stub named claude is
// put first on PATH; the seam means it is never executed. The re-check is the
// real checker over newRegfixHarness's overlay, whose config stays broken
// (the scripted agent edits nothing), so the line under test always prints.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/fixer"
	"github.com/obentoo/bentoolkit/internal/autoupdate/llm"
)

const registryDeniedSuccessEnvelope = `{"type":"result","subtype":"success","is_error":false,"result":"tried a new path","permission_denials":[` +
	`{"tool_name":"WebFetch","tool_use_id":"toolu_1","tool_input":{"url":"https://blocked.example.org/api?SENTINEL-QUERY-053"}},` +
	`{"tool_name":"Bash","tool_use_id":"toolu_2","tool_input":{"command":"curl SENTINEL-CMD-053"}}]}`

const registryPlainSuccessEnvelope = `{"type":"result","subtype":"success","is_error":false,"result":"tried a new path"}`

// runRegistryFixLoop drives promptRegistryFixes once ("y" to fix, "N" to keep)
// with a real registry fixer whose child prints envelope, and returns stdout.
func runRegistryFixLoop(t *testing.T, envelope string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	stubDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stubDir, "claude"), []byte("#!/bin/sh\nexit 97\n"), 0o755); err != nil {
		t.Fatalf("writing claude stub: %v", err)
	}
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	spawns := 0
	seam := func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		spawns++
		return exec.CommandContext(ctx, "/bin/sh", "-c", "printf '%s' '"+envelope+"'")
	}
	fixer, err := fixer.NewClaudeCodeRegistryFixer(llm.LLMConfig{Provider: "claude-code", Bare: "false"},
		fixer.WithRegistryFixerExecCommand(seam))
	if err != nil {
		t.Fatalf("NewClaudeCodeRegistryFixer: %v", err)
	}

	h := newRegfixHarness(t)
	out := captureStdout(t, func() {
		promptRegistryFixes(context.Background(), h.overlayDir, fixer,
			h.failuresForFetchError(), strings.NewReader("y\nN\n"), h.newChecker)
	})
	if spawns != 1 {
		t.Fatalf("the registry fixer spawned %d agents, want 1", spawns)
	}
	if !strings.Contains(out, "still failing after fix") {
		t.Fatalf("the re-check did not report a failure; output:\n%s", out)
	}
	return out
}

// TestFixRegistry_RecheckFailureNamesRefusedTools is R5.2 for the registry-fix
// loop, with its converse: the same failing re-check with no refusal names no
// tool, so the names are carried by the refusal and not by boilerplate.
func TestFixRegistry_RecheckFailureNamesRefusedTools(t *testing.T) {
	out := runRegistryFixLoop(t, registryDeniedSuccessEnvelope)
	for _, want := range []string{"WebFetch", "blocked.example.org", "Bash"} {
		if !strings.Contains(out, want) {
			t.Errorf("the still-failing report does not name %q (R5.2); output:\n%s", want, out)
		}
	}
	for _, leak := range []string{"SENTINEL-QUERY-053", "SENTINEL-CMD-053"} {
		if strings.Contains(out, leak) {
			t.Errorf("the still-failing report echoes the refused input %q (R5.1)", leak)
		}
	}

	plain := runRegistryFixLoop(t, registryPlainSuccessEnvelope)
	for _, tool := range []string{"WebFetch", "Bash"} {
		if strings.Contains(plain, tool) {
			t.Errorf("with no refusal the still-failing report names %q; output:\n%s", tool, plain)
		}
	}
}
