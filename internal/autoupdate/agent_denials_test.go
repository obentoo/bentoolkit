package autoupdate

// Authored for story 051 (llm-agent-least-privilege), sub-tasks 4.1 and 4.2 —
// a refused tool ends as a normal, named failure (S051-R5.1..R5.3).
//
// The envelope is the CLI's `--output-format json` result carrying
// `permission_denials`: one object per refused call, with `tool_name` and the
// call's `tool_input`. Every tool_input below carries a SENTINEL string so a
// message that echoes the input — rather than naming the tool — is caught.

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/fixer"
	"github.com/obentoo/bentoolkit/internal/autoupdate/llm"
)

// deniedSuccessEnvelope is a SUCCESSFUL envelope that nevertheless lists
// refusals — the agent "finished" without the tools it asked for.
const deniedSuccessEnvelope = `{"type":"result","subtype":"success","is_error":false,"result":"edited SRC_URI","permission_denials":[` +
	`{"tool_name":"WebFetch","tool_use_id":"toolu_5","tool_input":{"url":"https://blocked.example.org/rel?SENTINEL-QUERY-052"}},` +
	`{"tool_name":"Bash","tool_use_id":"toolu_6","tool_input":{"command":"wget SENTINEL-CMD-052"}}]}`

// TestApplyManifestFix_RecheckFailureNamesRefusedTools is R5.2 through the
// Applier: the fixer SUCCEEDED but was refused WebFetch and Bash, and the
// authoritative re-check then failed — the "still failed" message names the
// refused tools and the WebFetch host, never the input. Converse: the same
// failure with no refusal names no tool, so the names are not boilerplate.
func TestApplyManifestFix_RecheckFailureNamesRefusedTools(t *testing.T) {
	applyWith := func(t *testing.T, envelope string) error {
		t.Helper()
		isolateSecretsPaths(t)
		tmpDir := t.TempDir()
		overlayDir := filepath.Join(tmpDir, "overlay")
		configDir := filepath.Join(tmpDir, "config")
		pkg, oldVersion, newVersion := "dev-games/godot", "4.7_rc3", "4.7"
		createTestEbuildFile(t, overlayDir, pkg, oldVersion)
		pending, _ := NewPendingList(configDir)
		pending.Add(PendingUpdate{Package: pkg, CurrentVersion: oldVersion, NewVersion: newVersion, Status: StatusPending})

		seam, spy := agentSeam(printEnvelopeScript(envelope))
		fixer := newTestFixer(t, llm.LLMConfig{Provider: "claude-code", Bare: "false"}, fixer.WithFixerExecCommand(seam))
		applier, err := NewApplier(overlayDir, configDir,
			WithApplierPendingList(pending),
			WithExecCommand(pkgdevFailsPrinting("SRC_URI is unreachable: 404 Not Found")),
			WithApplierFixer(fixer),
		)
		if err != nil {
			t.Fatalf("NewApplier: %v", err)
		}
		_, applyErr := applier.Apply(t.Context(), pkg, false)
		if spy.spawns() != 1 {
			t.Fatalf("the fixer spawned %d agents, want 1", spy.spawns())
		}
		if applyErr == nil || !strings.Contains(applyErr.Error(), "still failed") {
			t.Fatalf("want the re-check failure, got %v", applyErr)
		}
		return applyErr
	}

	msg := applyWith(t, deniedSuccessEnvelope).Error()
	for _, want := range []string{"WebFetch", "blocked.example.org", "Bash"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the re-check failure does not name %q (R5.2): %s", want, msg)
		}
	}
	for _, leak := range []string{"SENTINEL-QUERY-052", "SENTINEL-CMD-052"} {
		if strings.Contains(msg, leak) {
			t.Errorf("the re-check failure echoes the refused input %q (R5.1): %s", leak, msg)
		}
	}

	plain := applyWith(t, okEnvelope).Error()
	for _, tool := range []string{"WebFetch", "Bash"} {
		if strings.Contains(plain, tool) {
			t.Errorf("with no refusal the re-check failure still names %q: %s", tool, plain)
		}
	}
}
