package autoupdate

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestFixer constructs a ClaudeCodeFixer with lookPath stubbed to "find"
// claude and the given options applied.
func newTestFixer(t *testing.T, cfg LLMConfig, opts ...ClaudeCodeFixerOption) *ClaudeCodeFixer {
	t.Helper()
	stubLookPathFound(t)
	f, err := NewClaudeCodeFixer(cfg, opts...)
	if err != nil {
		t.Fatalf("NewClaudeCodeFixer: unexpected error: %v", err)
	}
	return f
}

// fixerSeam is like scriptedSeam but also retains a pointer to the most recently
// returned *exec.Cmd so the test can inspect fields FixManifest sets after the
// factory returns (notably cmd.Dir).
func fixerSeam(script string) (func(ctx context.Context, name string, arg ...string) *exec.Cmd, *capturedExec, **exec.Cmd) {
	cap := &capturedExec{}
	var last *exec.Cmd
	factory := func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		cap.name = name
		cap.args = append([]string(nil), arg...)
		cmd := exec.CommandContext(ctx, "sh", "-c", script)
		last = cmd
		return cmd
	}
	return factory, cap, &last
}

// sampleFixRequest builds a request whose PkgDir is a real temp directory, so the
// spawned child (which chdirs into PkgDir) can start.
func sampleFixRequest(t *testing.T) ManifestFixRequest {
	t.Helper()
	pkgDir := t.TempDir()
	return ManifestFixRequest{
		Package:       "dev-games/godot",
		Version:       "4.7",
		PkgDir:        pkgDir,
		EbuildPath:    filepath.Join(pkgDir, "godot-4.7.ebuild"),
		ManifestError: "404 Not Found: https://example.com/godot-4.7.tar.xz",
		DistDir:       filepath.Join(pkgDir, "distdir"),
	}
}

func TestNewClaudeCodeFixer_Defaults(t *testing.T) {
	f := newTestFixer(t, LLMConfig{Provider: "claude-code"})
	if f.execCommand == nil {
		t.Error("expected execCommand to default to a non-nil factory")
	}
	if f.timeout != DefaultManifestFixTimeout {
		t.Errorf("expected default timeout == %v, got %v", DefaultManifestFixTimeout, f.timeout)
	}
	if f.model != DefaultClaudeCodeModel {
		t.Errorf("expected default model %q, got %q", DefaultClaudeCodeModel, f.model)
	}
}

func TestNewClaudeCodeFixer_UnavailableCLI(t *testing.T) {
	orig := lookPath
	lookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	t.Cleanup(func() { lookPath = orig })

	_, err := NewClaudeCodeFixer(LLMConfig{Provider: "claude-code"})
	if err == nil {
		t.Fatal("expected ErrClaudeCodeUnavailable when claude CLI is absent")
	}
}

// TestFixManifest_AgenticArgvAndCwd verifies the agentic invocation shape: the
// scoped allowlist, --add-dir == PkgDir, --max-turns, cwd == PkgDir, and that the
// dangerous bypass flag is NEVER used.
func TestFixManifest_AgenticArgvAndCwd(t *testing.T) {
	// A valid envelope so FixManifest returns success.
	envelope := `{"type":"result","is_error":false,"result":"changed SRC_URI to the -stable asset","total_cost_usd":0.02}`
	factory, cap, last := fixerSeam("printf '%s' '" + envelope + "'")

	f := newTestFixer(t, LLMConfig{Provider: "claude-code"}, WithFixerExecCommand(factory))

	req := sampleFixRequest(t)
	res, err := f.FixManifest(context.Background(), req)
	if err != nil {
		t.Fatalf("FixManifest: unexpected error: %v", err)
	}
	if res.Summary != "changed SRC_URI to the -stable asset" {
		t.Errorf("Summary = %q, want the envelope result", res.Summary)
	}
	if res.CostUSD != 0.02 {
		t.Errorf("CostUSD = %v, want 0.02", res.CostUSD)
	}

	if cap.name != "claude" {
		t.Errorf("exec name = %q, want claude", cap.name)
	}

	addDir, ok := flagValue(cap.args, "--add-dir")
	if !ok || addDir != req.PkgDir {
		t.Errorf("--add-dir = %q (found=%v), want %q", addDir, ok, req.PkgDir)
	}

	turns, ok := flagValue(cap.args, "--max-turns")
	if !ok || turns != "30" {
		t.Errorf("--max-turns = %q (found=%v), want 30", turns, ok)
	}

	allowed, ok := flagValue(cap.args, "--allowedTools")
	if !ok {
		t.Fatal("expected --allowedTools flag")
	}
	for _, want := range []string{"Edit", "Bash(pkgdev *)"} {
		if !strings.Contains(allowed, want) {
			t.Errorf("--allowedTools %q missing %q", allowed, want)
		}
	}

	if argsContain(cap.args, "--dangerously-skip-permissions") ||
		argsContain(cap.args, "--allow-dangerously-skip-permissions") {
		t.Error("fixer must NOT bypass permissions")
	}

	// The bentoo ebuild QA knowledge (gotchas) must be injected via the system
	// prompt so the fix is QA-aware even in --bare mode.
	sysPrompt, ok := flagValue(cap.args, "--append-system-prompt")
	if !ok {
		t.Fatal("expected --append-system-prompt with the ebuild QA guidance")
	}
	for _, want := range []string{"eapply_user", "thin-manifests", "MY_PN"} {
		if !strings.Contains(sysPrompt, want) {
			t.Errorf("system prompt missing gotcha marker %q", want)
		}
	}

	if *last == nil {
		t.Fatal("expected the seam to capture the spawned *exec.Cmd")
	}
	if (*last).Dir != req.PkgDir {
		t.Errorf("cmd.Dir = %q, want PkgDir %q", (*last).Dir, req.PkgDir)
	}
}

// TestFixManifest_InstructionCarriesContext checks the per-package facts land in
// the -p instruction (not page content on stdin), so the agent knows what to fix.
func TestFixManifest_InstructionCarriesContext(t *testing.T) {
	factory, cap, _ := fixerSeam(`printf '%s' '{"type":"result","is_error":false,"result":"ok"}'`)
	f := newTestFixer(t, LLMConfig{Provider: "claude-code"}, WithFixerExecCommand(factory))

	req := sampleFixRequest(t)
	if _, err := f.FixManifest(context.Background(), req); err != nil {
		t.Fatalf("FixManifest: %v", err)
	}

	instruction, ok := flagValue(cap.args, "-p")
	if !ok {
		t.Fatal("expected -p instruction")
	}
	for _, want := range []string{req.Package, req.Version, req.EbuildPath, req.ManifestError, req.DistDir} {
		if !strings.Contains(instruction, want) {
			t.Errorf("instruction missing %q", want)
		}
	}
	if !strings.Contains(instruction, "/bentoo") {
		t.Error("instruction should offer the /bentoo skill when available")
	}
}

// TestFixManifest_BareModeKeyNeverInArgv asserts that in bare mode the API key is
// injected only via the child environment and never appears in argv.
func TestFixManifest_BareModeKeyNeverInArgv(t *testing.T) {
	const keyEnv = "TEST_FIXER_KEY"
	const secret = "sk-super-secret-value"
	t.Setenv(keyEnv, secret)

	factory, cap, _ := fixerSeam(`printf '%s' '{"type":"result","is_error":false,"result":"ok"}'`)
	f := newTestFixer(t, LLMConfig{Provider: "claude-code", APIKeyEnv: keyEnv, Bare: "true"},
		WithFixerExecCommand(factory))

	if _, err := f.FixManifest(context.Background(), sampleFixRequest(t)); err != nil {
		t.Fatalf("FixManifest: %v", err)
	}

	if !f.bareMode {
		t.Fatal("expected bareMode true for Bare=\"true\"")
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

// TestFixManifest_ErrorEnvelope verifies a structured error envelope surfaces as an
// error without leaking internals.
func TestFixManifest_ErrorEnvelope(t *testing.T) {
	factory, _, _ := fixerSeam(`printf '%s' '{"type":"result","is_error":true,"subtype":"max_turns","errors":["ran out of turns"]}'`)
	f := newTestFixer(t, LLMConfig{Provider: "claude-code"}, WithFixerExecCommand(factory))

	_, err := f.FixManifest(context.Background(), sampleFixRequest(t))
	if err == nil {
		t.Fatal("expected an error for an is_error envelope")
	}
	if !strings.Contains(err.Error(), "max_turns") {
		t.Errorf("error %v should mention the subtype", err)
	}
}

// TestFixManifest_NonZeroExit verifies a non-zero CLI exit yields an error.
func TestFixManifest_NonZeroExit(t *testing.T) {
	factory, _, _ := fixerSeam("echo boom 1>&2; exit 3")
	f := newTestFixer(t, LLMConfig{Provider: "claude-code"}, WithFixerExecCommand(factory))

	if _, err := f.FixManifest(context.Background(), sampleFixRequest(t)); err == nil {
		t.Fatal("expected an error for a non-zero CLI exit")
	}
}

// TestFixManifest_BudgetFlag verifies a positive MaxBudgetUSD is forwarded.
func TestFixManifest_BudgetFlag(t *testing.T) {
	factory, cap, _ := fixerSeam(`printf '%s' '{"type":"result","is_error":false,"result":"ok"}'`)
	f := newTestFixer(t, LLMConfig{Provider: "claude-code", MaxBudgetUSD: 1.5},
		WithFixerExecCommand(factory))

	if _, err := f.FixManifest(context.Background(), sampleFixRequest(t)); err != nil {
		t.Fatalf("FixManifest: %v", err)
	}
	v, ok := flagValue(cap.args, "--max-budget-usd")
	if !ok || v != "1.5" {
		t.Errorf("--max-budget-usd = %q (found=%v), want 1.5", v, ok)
	}
}

// TestFixManifest_TimeoutHonored confirms the configured timeout bounds the call.
// The seam uses `exec sleep` so the shell replaces itself with sleep rather than
// forking a child that would keep the stdout pipe open after the kill — otherwise
// cmd.Wait blocks on the orphaned pipe (observed hanging for minutes under dash on
// CI, where bash's single-command exec optimisation does not apply).
func TestFixManifest_TimeoutHonored(t *testing.T) {
	factory, _, _ := fixerSeam("exec sleep 3600")
	f := newTestFixer(t, LLMConfig{Provider: "claude-code"},
		WithFixerExecCommand(factory), WithFixerTimeout(150*time.Millisecond))

	start := time.Now()
	_, err := f.FixManifest(context.Background(), sampleFixRequest(t))
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("FixManifest did not honor the timeout (took %v)", elapsed)
	}
}

// fakeFixer is a ManifestFixer test double for the applier integration tests.
type fakeFixer struct {
	called  int
	summary string
	err     error
	// onCall, when non-nil, runs on each invocation (e.g. to flip a seam flag or
	// edit the ebuild so the subsequent manifest re-run can succeed).
	onCall  func(req ManifestFixRequest)
	lastReq ManifestFixRequest
}

func (f *fakeFixer) FixManifest(_ context.Context, req ManifestFixRequest) (ManifestFixResult, error) {
	f.called++
	f.lastReq = req
	if f.onCall != nil {
		f.onCall(req)
	}
	if f.err != nil {
		return ManifestFixResult{}, f.err
	}
	return ManifestFixResult{Summary: f.summary}, nil
}

var _ ManifestFixer = (*fakeFixer)(nil)

// pkgdevFlakySeam returns an exec seam where the first `pkgdev` call fails and
// every subsequent `pkgdev` call succeeds (simulating a manifest that passes once
// the fixer has repaired the ebuild). Non-pkgdev commands always succeed.
func pkgdevFlakySeam() func(ctx context.Context, name string, arg ...string) *exec.Cmd {
	calls := 0
	return func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		if name == "pkgdev" {
			calls++
			if calls == 1 {
				return exec.CommandContext(ctx, "false")
			}
		}
		return exec.CommandContext(ctx, "true")
	}
}
