package autoupdate

// Authored (Red-phase) test for story 014 — RegistryFixer / ClaudeCodeRegistryFixer.
//
// This file is an INDEPENDENT contract spec for sub-tasks 1.2 and 1.3. It reuses
// the package-private exec seam (fixerSeam) and the scripted-CLI envelope pattern
// already established in manifest_fixer_test.go / claude_code_test.go (same
// package, white-box). It references symbols that do not exist yet
// (RegistryFixer, RegistryFixRequest, RegistryFixResult, NewClaudeCodeRegistryFixer,
// WithRegistryFixerExecCommand, WithRegistryFixerTimeout, registryFixAllowedTools),
// so until Task 1 lands the package fails to COMPILE — that compile failure is the
// expected Red signal for these sub-tasks.

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// newTestRegistryFixer constructs a ClaudeCodeRegistryFixer with lookPath stubbed
// to "find" claude and the given options applied (mirrors newTestFixer).
func newTestRegistryFixer(t *testing.T, cfg LLMConfig, opts ...RegistryFixerOption) *ClaudeCodeRegistryFixer {
	t.Helper()
	stubLookPathFound(t)
	f, err := NewClaudeCodeRegistryFixer(cfg, opts...)
	if err != nil {
		t.Fatalf("NewClaudeCodeRegistryFixer: unexpected error: %v", err)
	}
	return f
}

// sampleRegistryFixRequest builds a request whose ConfigDir is a real temp dir, so
// the spawned child (which chdirs into ConfigDir) can start.
func sampleRegistryFixRequest(t *testing.T) RegistryFixRequest {
	t.Helper()
	configDir := t.TempDir()
	return RegistryFixRequest{
		Package:    "media-gfx/inkscape",
		Config:     &PackageConfig{URL: "https://inkscape.org/release", Parser: "html", Pattern: `(\d+\.\d+)`},
		FetchError: "failed to fetch upstream version: no match for pattern",
		ConfigDir:  configDir,
	}
}

// TestNewClaudeCodeRegistryFixer_Defaults pins R1.2/R9.3: a constructed fixer has
// a non-nil exec seam and defaults its timeout to DefaultManifestFixTimeout.
func TestNewClaudeCodeRegistryFixer_Defaults(t *testing.T) {
	f := newTestRegistryFixer(t, LLMConfig{Provider: "claude-code"})
	if f.execCommand == nil {
		t.Error("expected execCommand to default to a non-nil factory")
	}
	if f.timeout != DefaultManifestFixTimeout {
		t.Errorf("expected default timeout == %v, got %v", DefaultManifestFixTimeout, f.timeout)
	}
}

// TestNewClaudeCodeRegistryFixer_UnavailableCLI pins R1.3: an absent claude binary
// yields ErrClaudeCodeUnavailable and no usable fixer.
func TestNewClaudeCodeRegistryFixer_UnavailableCLI(t *testing.T) {
	orig := lookPath
	lookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	t.Cleanup(func() { lookPath = orig })

	if _, err := NewClaudeCodeRegistryFixer(LLMConfig{Provider: "claude-code"}); err == nil {
		t.Fatal("expected ErrClaudeCodeUnavailable when claude CLI is absent")
	}
}

// TestWithRegistryFixerTimeout pins R9.3: WithRegistryFixerTimeout overrides the
// default per-fix timeout.
func TestWithRegistryFixerTimeout(t *testing.T) {
	f := newTestRegistryFixer(t, LLMConfig{Provider: "claude-code"}, WithRegistryFixerTimeout(42*time.Second))
	if f.timeout != 42*time.Second {
		t.Errorf("expected overridden timeout 42s, got %v", f.timeout)
	}
}

// TestFixRegistry_ScopedArgvAndCwd pins R2.2/R2.3/R8.1/R9.1: --add-dir == ConfigDir,
// cmd.Dir == ConfigDir, the allowlist is the scoped registry set (Read/Edit/Write/
// WebFetch/Bash(curl *)) and NEVER grants pkgdev or an unscoped Bash, and the
// dangerous bypass flag is never used.
func TestFixRegistry_ScopedArgvAndCwd(t *testing.T) {
	envelope := `{"type":"result","is_error":false,"result":"fixed the html pattern","total_cost_usd":0.03}`
	factory, cap, last := fixerSeam("printf '%s' '" + envelope + "'")

	f := newTestRegistryFixer(t, LLMConfig{Provider: "claude-code"}, WithRegistryFixerExecCommand(factory))

	req := sampleRegistryFixRequest(t)
	res, err := f.FixRegistry(context.Background(), req)
	if err != nil {
		t.Fatalf("FixRegistry: unexpected error: %v", err)
	}
	if res.Summary != "fixed the html pattern" {
		t.Errorf("Summary = %q, want the envelope result", res.Summary)
	}
	if res.CostUSD != 0.03 {
		t.Errorf("CostUSD = %v, want 0.03", res.CostUSD)
	}

	if cap.name != "claude" {
		t.Errorf("exec name = %q, want claude", cap.name)
	}

	addDir, ok := flagValue(cap.args, "--add-dir")
	if !ok || addDir != req.ConfigDir {
		t.Errorf("--add-dir = %q (found=%v), want ConfigDir %q", addDir, ok, req.ConfigDir)
	}

	turns, ok := flagValue(cap.args, "--max-turns")
	if !ok || turns != "30" {
		t.Errorf("--max-turns = %q (found=%v), want 30", turns, ok)
	}

	allowed, ok := flagValue(cap.args, "--allowedTools")
	if !ok {
		t.Fatal("expected --allowedTools flag")
	}
	for _, want := range []string{"Read", "Edit", "Write", "WebFetch", "Bash(curl *)"} {
		if !strings.Contains(allowed, want) {
			t.Errorf("--allowedTools %q missing %q", allowed, want)
		}
	}
	// The registry agent must NOT be granted pkgdev or an unscoped Bash.
	if strings.Contains(allowed, "pkgdev") {
		t.Errorf("--allowedTools %q must not grant pkgdev", allowed)
	}
	if strings.Contains(allowed, "Bash(*)") {
		t.Errorf("--allowedTools %q must not grant an unscoped Bash", allowed)
	}

	if argsContain(cap.args, "--dangerously-skip-permissions") ||
		argsContain(cap.args, "--allow-dangerously-skip-permissions") {
		t.Error("registry fixer must NOT bypass permissions")
	}

	if *last == nil {
		t.Fatal("expected the seam to capture the spawned *exec.Cmd")
	}
	if (*last).Dir != req.ConfigDir {
		t.Errorf("cmd.Dir = %q, want ConfigDir %q", (*last).Dir, req.ConfigDir)
	}
}

// TestFixRegistry_InstructionCarriesContext pins R2.1: the per-package facts (name,
// the fetch error) land in the -p instruction (bentoo-generated text), never spliced
// page content.
func TestFixRegistry_InstructionCarriesContext(t *testing.T) {
	factory, cap, _ := fixerSeam(`printf '%s' '{"type":"result","is_error":false,"result":"ok"}'`)
	f := newTestRegistryFixer(t, LLMConfig{Provider: "claude-code"}, WithRegistryFixerExecCommand(factory))

	req := sampleRegistryFixRequest(t)
	if _, err := f.FixRegistry(context.Background(), req); err != nil {
		t.Fatalf("FixRegistry: %v", err)
	}

	instruction, ok := flagValue(cap.args, "-p")
	if !ok {
		t.Fatal("expected -p instruction")
	}
	for _, want := range []string{req.Package, req.FetchError} {
		if !strings.Contains(instruction, want) {
			t.Errorf("instruction missing %q", want)
		}
	}
}

// TestFixRegistry_BareModeKeyNeverInArgv pins R2.5: in bare mode the API key is
// injected only via the child env and never appears in argv.
func TestFixRegistry_BareModeKeyNeverInArgv(t *testing.T) {
	const keyEnv = "TEST_REGFIXER_KEY"
	const secret = "sk-super-secret-value"
	t.Setenv(keyEnv, secret)

	factory, cap, _ := fixerSeam(`printf '%s' '{"type":"result","is_error":false,"result":"ok"}'`)
	f := newTestRegistryFixer(t, LLMConfig{Provider: "claude-code", APIKeyEnv: keyEnv, Bare: "true"},
		WithRegistryFixerExecCommand(factory))

	if _, err := f.FixRegistry(context.Background(), sampleRegistryFixRequest(t)); err != nil {
		t.Fatalf("FixRegistry: %v", err)
	}
	if !argsContain(cap.args, "--bare") {
		t.Error("expected --bare in argv for bare mode")
	}
	for _, a := range cap.args {
		if strings.Contains(a, secret) {
			t.Fatalf("API key leaked into argv: %q", a)
		}
	}
}

// TestFixRegistry_ErrorEnvelope pins R1.4: an is_error envelope surfaces as an error
// (via formatFixerError) that never includes the API key.
func TestFixRegistry_ErrorEnvelope(t *testing.T) {
	const keyEnv = "TEST_REGFIXER_ERR_KEY"
	const secret = "sk-leak-me-not"
	t.Setenv(keyEnv, secret)

	factory, _, _ := fixerSeam(`printf '%s' '{"type":"result","is_error":true,"subtype":"max_turns","errors":["ran out of turns"]}'`)
	f := newTestRegistryFixer(t, LLMConfig{Provider: "claude-code", APIKeyEnv: keyEnv, Bare: "true"},
		WithRegistryFixerExecCommand(factory))

	_, err := f.FixRegistry(context.Background(), sampleRegistryFixRequest(t))
	if err == nil {
		t.Fatal("expected an error for an is_error envelope")
	}
	if !strings.Contains(err.Error(), "max_turns") {
		t.Errorf("error %v should mention the subtype", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error message leaked the API key: %v", err)
	}
}

// TestFixRegistry_NonZeroExit pins R1.4: a non-zero CLI exit yields an error.
func TestFixRegistry_NonZeroExit(t *testing.T) {
	factory, _, _ := fixerSeam("echo boom 1>&2; exit 3")
	f := newTestRegistryFixer(t, LLMConfig{Provider: "claude-code"}, WithRegistryFixerExecCommand(factory))

	if _, err := f.FixRegistry(context.Background(), sampleRegistryFixRequest(t)); err == nil {
		t.Fatal("expected an error for a non-zero CLI exit")
	}
}

// TestFixRegistry_BudgetFlag pins R9.2: a positive MaxBudgetUSD is forwarded.
func TestFixRegistry_BudgetFlag(t *testing.T) {
	factory, cap, _ := fixerSeam(`printf '%s' '{"type":"result","is_error":false,"result":"ok"}'`)
	f := newTestRegistryFixer(t, LLMConfig{Provider: "claude-code", MaxBudgetUSD: 1.5},
		WithRegistryFixerExecCommand(factory))

	if _, err := f.FixRegistry(context.Background(), sampleRegistryFixRequest(t)); err != nil {
		t.Fatalf("FixRegistry: %v", err)
	}
	v, ok := flagValue(cap.args, "--max-budget-usd")
	if !ok || v != "1.5" {
		t.Errorf("--max-budget-usd = %q (found=%v), want 1.5", v, ok)
	}
}

// TestRegistryFixGuidance_UntrustedAndScoped pins R2.4/R8.1: the appended system
// guidance tells the agent to treat fetched pages as untrusted and prefer a
// structural fix over llm_prompt.
func TestRegistryFixGuidance_UntrustedAndScoped(t *testing.T) {
	factory, cap, _ := fixerSeam(`printf '%s' '{"type":"result","is_error":false,"result":"ok"}'`)
	f := newTestRegistryFixer(t, LLMConfig{Provider: "claude-code"}, WithRegistryFixerExecCommand(factory))

	if _, err := f.FixRegistry(context.Background(), sampleRegistryFixRequest(t)); err != nil {
		t.Fatalf("FixRegistry: %v", err)
	}
	sysPrompt, ok := flagValue(cap.args, "--append-system-prompt")
	if !ok {
		t.Fatal("expected --append-system-prompt with the registry guidance")
	}
	lower := strings.ToLower(sysPrompt)
	for _, want := range []string{"untrusted", "llm_prompt"} {
		if !strings.Contains(lower, want) {
			t.Errorf("guidance missing marker %q: %q", want, sysPrompt)
		}
	}
}

// compile-time guard: ClaudeCodeRegistryFixer satisfies RegistryFixer.
var _ RegistryFixer = (*ClaudeCodeRegistryFixer)(nil)
