package autoupdate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// jsonQuote returns s encoded as a JSON string literal (with surrounding double
// quotes and inner escaping), so it can be embedded as the value of a JSON field
// inside a scripted envelope. It panics only on the impossible case of a string
// that cannot be JSON-encoded.
func jsonQuote(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

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
}

// capturedExec records the argv handed to the exec seam for the most recent
// call, so tests can assert what landed in argv (and what did NOT — e.g. page
// content or the API key).
type capturedExec struct {
	name string
	args []string
}

// scriptedSeam returns an exec-seam factory whose spawned process runs `script`
// under `sh -c`. The script may consume stdin and print a canned envelope to
// stdout and/or exit with a chosen code. The returned *capturedExec is populated
// with the argv on every invocation so the caller can inspect it after the call.
func scriptedSeam(script string) (func(ctx context.Context, name string, arg ...string) *exec.Cmd, *capturedExec) {
	cap := &capturedExec{}
	factory := func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		cap.name = name
		cap.args = append([]string(nil), arg...)
		return exec.CommandContext(ctx, "sh", "-c", script)
	}
	return factory, cap
}

// scriptedSeamCapturingStdin is like scriptedSeam but the script first copies
// everything piped on stdin into stdinFile, letting the test verify what content
// was sent to the child via stdin. The script then prints `stdout` and exits 0.
func scriptedSeamCapturingStdin(stdinFile, stdout string) (func(ctx context.Context, name string, arg ...string) *exec.Cmd, *capturedExec) {
	// `cat > file` captures stdin; printf emits the canned envelope.
	script := "cat > '" + stdinFile + "'; printf '%s' '" + stdout + "'"
	return scriptedSeam(script)
}

// argsContain reports whether any element of args equals target.
func argsContain(args []string, target string) bool {
	for _, a := range args {
		if a == target {
			return true
		}
	}
	return false
}

// argsContainSubstr reports whether any element of args contains substr.
func argsContainSubstr(args []string, substr string) bool {
	for _, a := range args {
		if strings.Contains(a, substr) {
			return true
		}
	}
	return false
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

// newTestClient constructs a ClaudeCodeClient with lookPath stubbed to "find"
// claude and the given options applied.
func newTestClient(t *testing.T, cfg LLMConfig, opts ...ClaudeCodeOption) *ClaudeCodeClient {
	t.Helper()
	stubLookPathFound(t)
	c, err := NewClaudeCodeClient(cfg, opts...)
	if err != nil {
		t.Fatalf("NewClaudeCodeClient: unexpected error: %v", err)
	}
	return c
}

// =============================================================================
// 2.1 — Struct, constructor, exec seam
// =============================================================================

func TestNewClaudeCodeClient_Defaults(t *testing.T) {
	c := newTestClient(t, LLMConfig{Provider: "claude-code"})

	if c.execCommand == nil {
		t.Error("expected execCommand to default to a non-nil factory")
	}
	if c.timeout < 120*time.Second {
		t.Errorf("expected default timeout >= 120s (R7.3), got %v", c.timeout)
	}
	if c.timeout != DefaultClaudeCodeTimeout {
		t.Errorf("expected default timeout == DefaultClaudeCodeTimeout (%v), got %v", DefaultClaudeCodeTimeout, c.timeout)
	}
	if c.ctx == nil {
		t.Error("expected ctx to default to a non-nil context")
	}
}

func TestNewClaudeCodeClient_WithExecCommandOverridesSeam(t *testing.T) {
	stubLookPathFound(t)

	called := false
	seam := func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		called = true
		return exec.CommandContext(ctx, "true")
	}

	c, err := NewClaudeCodeClient(LLMConfig{}, WithClaudeCodeExecCommand(seam))
	if err != nil {
		t.Fatalf("NewClaudeCodeClient: %v", err)
	}

	// Invoke the seam indirectly through run to prove the override took effect.
	_, _ = c.run("instr", []byte("content"), "")
	if !called {
		t.Error("WithClaudeCodeExecCommand seam was not used by run()")
	}
}

func TestNewClaudeCodeClient_WithTimeoutAndContext(t *testing.T) {
	stubLookPathFound(t)

	type ctxKey string
	const k ctxKey = "marker"
	parent := context.WithValue(context.Background(), k, "v")

	c, err := NewClaudeCodeClient(LLMConfig{},
		WithClaudeCodeTimeout(5*time.Minute),
		WithClaudeCodeContext(parent),
	)
	if err != nil {
		t.Fatalf("NewClaudeCodeClient: %v", err)
	}

	if c.timeout != 5*time.Minute {
		t.Errorf("expected timeout 5m, got %v", c.timeout)
	}
	if got := c.ctx.Value(k); got != "v" {
		t.Errorf("expected custom parent context to be stored, got value %v", got)
	}
}

func TestNewClaudeCodeClient_IgnoresNilContextAndNonPositiveTimeout(t *testing.T) {
	stubLookPathFound(t)

	c, err := NewClaudeCodeClient(LLMConfig{},
		WithClaudeCodeTimeout(0),
		WithClaudeCodeTimeout(-1*time.Second),
		WithClaudeCodeContext(nil), //nolint:staticcheck // SA1012: this test deliberately verifies nil-context handling
	)
	if err != nil {
		t.Fatalf("NewClaudeCodeClient: %v", err)
	}

	if c.timeout != DefaultClaudeCodeTimeout {
		t.Errorf("non-positive timeout should be ignored; want %v, got %v", DefaultClaudeCodeTimeout, c.timeout)
	}
	if c.ctx == nil {
		t.Error("nil context should be ignored, leaving a non-nil default")
	}
}

// =============================================================================
// 2.7 — Availability detection
// =============================================================================

func TestNewClaudeCodeClient_UnavailableWhenNotOnPath(t *testing.T) {
	orig := lookPath
	lookPath = func(string) (string, error) { return "", errors.New("not found") }
	t.Cleanup(func() { lookPath = orig })

	c, err := NewClaudeCodeClient(LLMConfig{})
	if !errors.Is(err, ErrClaudeCodeUnavailable) {
		t.Errorf("expected ErrClaudeCodeUnavailable, got %v", err)
	}
	if c != nil {
		t.Errorf("expected nil client when claude is unavailable, got %v", c)
	}
}

func TestClaudeAvailable_Seam(t *testing.T) {
	orig := lookPath
	t.Cleanup(func() { lookPath = orig })

	lookPath = func(string) (string, error) { return "/usr/bin/claude", nil }
	if !claudeAvailable() {
		t.Error("claudeAvailable() should be true when lookPath succeeds")
	}

	lookPath = func(string) (string, error) { return "", errors.New("nope") }
	if claudeAvailable() {
		t.Error("claudeAvailable() should be false when lookPath fails")
	}
}

// =============================================================================
// 2.6 — GetModel + default model
// =============================================================================

func TestClaudeCode_GetModel_DefaultIsSonnet(t *testing.T) {
	c := newTestClient(t, LLMConfig{})
	if c.GetModel() != DefaultClaudeCodeModel {
		t.Errorf("expected default model %q, got %q", DefaultClaudeCodeModel, c.GetModel())
	}
	if DefaultClaudeCodeModel == DefaultClaudeModel {
		t.Errorf("DefaultClaudeCodeModel (%q) must differ from DefaultClaudeModel (%q)", DefaultClaudeCodeModel, DefaultClaudeModel)
	}
}

func TestClaudeCode_GetModel_ExplicitPreserved(t *testing.T) {
	c := newTestClient(t, LLMConfig{Model: "claude-opus-4"})
	if c.GetModel() != "claude-opus-4" {
		t.Errorf("expected explicit model preserved, got %q", c.GetModel())
	}
}

// =============================================================================
// 2.2 — Auth-mode resolution
// =============================================================================

func TestResolveBare_Matrix(t *testing.T) {
	const keyEnv = "TEST_CC_API_KEY_RESOLVE"

	tests := []struct {
		name     string
		bare     string
		keySet   bool
		keyEnv   string
		expected bool
	}{
		{name: "explicit true, key set", bare: "true", keySet: true, keyEnv: keyEnv, expected: true},
		{name: "explicit true, key unset", bare: "true", keySet: false, keyEnv: keyEnv, expected: true},
		{name: "explicit false, key set", bare: "false", keySet: true, keyEnv: keyEnv, expected: false},
		{name: "explicit false, key unset", bare: "false", keySet: false, keyEnv: keyEnv, expected: false},
		{name: "auto, key set", bare: "auto", keySet: true, keyEnv: keyEnv, expected: true},
		{name: "auto, key unset", bare: "auto", keySet: false, keyEnv: keyEnv, expected: false},
		{name: "auto, empty key env name", bare: "auto", keySet: false, keyEnv: "", expected: false},
		{name: "empty bare (defensive), key set", bare: "", keySet: true, keyEnv: keyEnv, expected: true},
		{name: "empty bare (defensive), key unset", bare: "", keySet: false, keyEnv: keyEnv, expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The single resolved key is now passed to resolveBare directly, so the
			// matrix no longer manipulates the environment (nor reads any file):
			// keySet chooses whether a non-empty key was resolved upstream.
			key := ""
			if tt.keySet {
				key = "secret-value"
			}

			cfg := LLMConfig{Bare: tt.bare, APIKeyEnv: tt.keyEnv}
			if got := resolveBare(cfg, key); got != tt.expected {
				t.Errorf("resolveBare(bare=%q, keySet=%v, keyEnv=%q) = %v, want %v",
					tt.bare, tt.keySet, tt.keyEnv, got, tt.expected)
			}
		})
	}
}

// TestResolveBare_StoredInClient verifies the resolved decision lands in
// bareMode and that the secret key VALUE never appears in any string the client
// exposes (model, args). (R2.4, G5)
func TestResolveBare_StoredInClient(t *testing.T) {
	const keyEnv = "TEST_CC_API_KEY_STORED"
	const secret = "sk-ant-SUPER-SECRET-VALUE"
	t.Setenv(keyEnv, secret)

	c := newTestClient(t, LLMConfig{Bare: "auto", APIKeyEnv: keyEnv, Model: "claude-sonnet-4"})
	if !c.bareMode {
		t.Fatalf("expected bareMode true when bare=auto and key set")
	}

	// The key value must never be reachable via model, apiKeyEnv, or argv.
	if strings.Contains(c.model, secret) {
		t.Error("model must not contain the API key value")
	}
	if strings.Contains(c.apiKeyEnv, secret) {
		t.Error("apiKeyEnv (the env var NAME) must not contain the API key value")
	}
	args := c.buildArgs("instr", false, "")
	if argsContainSubstr(args, secret) {
		t.Errorf("API key value leaked into argv: %v", args)
	}
}

// =============================================================================
// 2.3 — buildArgs
// =============================================================================

func TestBuildArgs_FixedFlagsAlwaysPresent(t *testing.T) {
	c := newTestClient(t, LLMConfig{Model: "claude-sonnet-4"})
	args := c.buildArgs("my-instruction", false, "")

	// -p carries the instruction.
	if v, ok := flagValue(args, "-p"); !ok || v != "my-instruction" {
		t.Errorf("-p value = %q (found=%v), want %q", v, ok, "my-instruction")
	}
	if v, ok := flagValue(args, "--output-format"); !ok || v != "json" {
		t.Errorf("--output-format = %q (found=%v), want json", v, ok)
	}
	if v, ok := flagValue(args, "--max-turns"); !ok || v != "2" {
		t.Errorf("--max-turns = %q (found=%v), want 2", v, ok)
	}
	if v, ok := flagValue(args, "--model"); !ok || v != "claude-sonnet-4" {
		t.Errorf("--model = %q (found=%v), want claude-sonnet-4", v, ok)
	}
	// --allowedTools "" must be present with an empty-string value.
	if v, ok := flagValue(args, "--allowedTools"); !ok || v != "" {
		t.Errorf("--allowedTools value = %q (found=%v), want empty string", v, ok)
	}
}

func TestBuildArgs_BareOnlyWhenBareMode(t *testing.T) {
	// Not bare.
	c := newTestClient(t, LLMConfig{Bare: "false"})
	if argsContain(c.buildArgs("i", false, ""), "--bare") {
		t.Error("--bare must NOT be present when bareMode is false")
	}

	// Bare.
	const keyEnv = "TEST_CC_API_KEY_BAREARG"
	t.Setenv(keyEnv, "secret")
	cb := newTestClient(t, LLMConfig{Bare: "auto", APIKeyEnv: keyEnv})
	if !argsContain(cb.buildArgs("i", false, ""), "--bare") {
		t.Error("--bare must be present when bareMode is true")
	}
}

func TestBuildArgs_JSONSchemaOnlyWhenStructured(t *testing.T) {
	c := newTestClient(t, LLMConfig{})

	// structured=false → no --json-schema even if schema non-empty.
	if argsContain(c.buildArgs("i", false, "{}"), "--json-schema") {
		t.Error("--json-schema must be absent when structured=false")
	}
	// structured=true but empty schema → absent.
	if argsContain(c.buildArgs("i", true, ""), "--json-schema") {
		t.Error("--json-schema must be absent when schema is empty")
	}
	// structured=true and non-empty schema → present with the schema value.
	args := c.buildArgs("i", true, `{"type":"object"}`)
	if v, ok := flagValue(args, "--json-schema"); !ok || v != `{"type":"object"}` {
		t.Errorf("--json-schema = %q (found=%v), want the schema string", v, ok)
	}
}

func TestBuildArgs_MaxBudgetOnlyWhenPositive(t *testing.T) {
	// No budget.
	c := newTestClient(t, LLMConfig{})
	if argsContain(c.buildArgs("i", false, ""), "--max-budget-usd") {
		t.Error("--max-budget-usd must be absent when maxBudgetUSD == 0")
	}

	// Positive budget formatted via strconv.FormatFloat.
	cb := newTestClient(t, LLMConfig{MaxBudgetUSD: 0.25})
	args := cb.buildArgs("i", false, "")
	if v, ok := flagValue(args, "--max-budget-usd"); !ok || v != "0.25" {
		t.Errorf("--max-budget-usd = %q (found=%v), want 0.25", v, ok)
	}
}

// =============================================================================
// 2.3 — run
// =============================================================================

func TestRun_SuccessEnvelopeReturnsResult(t *testing.T) {
	seam, _ := scriptedSeam(`printf '%s' '{"type":"result","is_error":false,"result":"1.2.3"}'`)
	c := newTestClient(t, LLMConfig{}, WithClaudeCodeExecCommand(seam))

	out, err := c.run("instr", []byte("content"), "")
	if err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	if out != "1.2.3" {
		t.Errorf("run result = %q, want %q", out, "1.2.3")
	}
}

func TestRun_IsErrorEnvelopeReturnsError(t *testing.T) {
	seam, _ := scriptedSeam(`printf '%s' '{"type":"result","is_error":true,"subtype":"budget_exceeded","errors":["over budget","try later"]}'`)
	c := newTestClient(t, LLMConfig{}, WithClaudeCodeExecCommand(seam))

	_, err := c.run("instr", []byte("content"), "")
	if err == nil {
		t.Fatal("expected error for is_error envelope")
	}
	if !strings.Contains(err.Error(), "over budget") {
		t.Errorf("error should mention the envelope errors, got %v", err)
	}
}

func TestRun_NonZeroExitReturnsErrorWithStderr(t *testing.T) {
	// Non-JSON stdout, a stderr message, exit non-zero.
	seam, _ := scriptedSeam(`printf 'boom on stderr' 1>&2; exit 7`)
	c := newTestClient(t, LLMConfig{}, WithClaudeCodeExecCommand(seam))

	_, err := c.run("instr", []byte("content"), "")
	if err == nil {
		t.Fatal("expected error for non-zero exit")
	}
	if !strings.Contains(err.Error(), "boom on stderr") {
		t.Errorf("error should include stderr, got %v", err)
	}
}

func TestRun_MalformedJSONStdoutReturnsError(t *testing.T) {
	seam, _ := scriptedSeam(`printf '%s' 'this is not json'`)
	c := newTestClient(t, LLMConfig{}, WithClaudeCodeExecCommand(seam))

	_, err := c.run("instr", []byte("content"), "")
	if err == nil {
		t.Fatal("expected error for malformed JSON stdout")
	}
}

// TestRun_ContentGoesToStdinNotArgv proves R1.2/AD8: page content is piped on
// stdin and never appears in argv.
func TestRun_ContentGoesToStdinNotArgv(t *testing.T) {
	stdinFile := t.TempDir() + "/stdin.txt"
	seam, cap := scriptedSeamCapturingStdin(stdinFile, `{"type":"result","is_error":false,"result":"ok"}`)
	c := newTestClient(t, LLMConfig{}, WithClaudeCodeExecCommand(seam))

	const secretContent = "UNIQUE-PAGE-CONTENT-MARKER-9173"
	if _, err := c.run("the-instruction", []byte(secretContent), ""); err != nil {
		t.Fatalf("run: %v", err)
	}

	// Content must have arrived on stdin.
	piped, err := os.ReadFile(stdinFile)
	if err != nil {
		t.Fatalf("reading captured stdin: %v", err)
	}
	if string(piped) != secretContent {
		t.Errorf("stdin = %q, want the page content %q", string(piped), secretContent)
	}

	// Content must NOT be anywhere in argv.
	if argsContainSubstr(cap.args, secretContent) {
		t.Errorf("page content leaked into argv: %v", cap.args)
	}
	// The instruction, however, IS expected in argv (as the -p value).
	if v, _ := flagValue(cap.args, "-p"); v != "the-instruction" {
		t.Errorf("-p = %q, want the instruction", v)
	}
}

// TestRun_BareInjectsKeyOnlyViaEnv_AndNeverInArgsOrErr proves R2.1/R2.4/G5:
// in bare mode the key is injected via the child env and --bare is present, but
// the key VALUE never appears in argv nor in any returned error string.
func TestRun_BareInjectsKeyViaEnv_NotArgsNorErrors(t *testing.T) {
	const keyEnv = "TEST_CC_API_KEY_RUN_BARE"
	const secret = "sk-ant-RUN-SECRET-42"
	t.Setenv(keyEnv, secret)

	// The script echoes whether ANTHROPIC_API_KEY is set in its env (without
	// printing the value), then fails so we also get an error path to inspect.
	stdinFile := t.TempDir() + "/stdin.txt"
	script := `cat > '` + stdinFile + `'; ` +
		`if [ -n "$ANTHROPIC_API_KEY" ]; then printf 'KEYPRESENT' 1>&2; fi; exit 3`
	seam, cap := scriptedSeam(script)
	c := newTestClient(t, LLMConfig{Bare: "auto", APIKeyEnv: keyEnv}, WithClaudeCodeExecCommand(seam))

	_, err := c.run("instr", []byte("content"), "")
	if err == nil {
		t.Fatal("expected error (script exits 3)")
	}

	// --bare present in argv.
	if !argsContain(cap.args, "--bare") {
		t.Errorf("--bare missing from argv in bare mode: %v", cap.args)
	}
	// Child saw ANTHROPIC_API_KEY (the script wrote KEYPRESENT to stderr, which
	// is surfaced in the error).
	if !strings.Contains(err.Error(), "KEYPRESENT") {
		t.Errorf("child did not observe ANTHROPIC_API_KEY in env; err=%v", err)
	}
	// The key VALUE must never appear in argv nor the returned error.
	if argsContainSubstr(cap.args, secret) {
		t.Errorf("API key value leaked into argv: %v", cap.args)
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("API key value leaked into returned error: %v", err)
	}
}

// TestRun_NonBareDoesNotAddBareNorKey verifies that without bare mode --bare is
// absent AND any inherited ANTHROPIC_API_KEY is actively scrubbed from the child
// env, so the CLI falls back to its logged-in session even when a key is exported
// in the parent environment (the bare:false intent).
func TestRun_NonBareDoesNotAddBareNorInjectKey(t *testing.T) {
	const keyEnv = "TEST_CC_API_KEY_RUN_NONBARE"
	t.Setenv(keyEnv, "should-not-be-used")
	// Simulate a key exported in the parent env (e.g. from a shell rc). Non-bare
	// mode MUST strip it; if it leaks the script's guard trips with KEYINJECTED.
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-INHERITED-SHOULD-BE-SCRUBBED")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "tok-INHERITED-SHOULD-BE-SCRUBBED")

	// If either auth source leaks into the child's env, the script exits non-zero
	// with KEYINJECTED on stderr so run() surfaces it as an error; otherwise it
	// prints the success envelope and exits zero.
	script := `if [ -n "$ANTHROPIC_API_KEY" ] || [ -n "$ANTHROPIC_AUTH_TOKEN" ]; then printf 'KEYINJECTED' 1>&2; exit 9; fi; ` +
		`printf '%s' '{"type":"result","is_error":false,"result":"ok"}'`
	seam, cap := scriptedSeam(script)

	// bare=false → not bare even though a key env is configured & populated.
	c := newTestClient(t, LLMConfig{Bare: "false", APIKeyEnv: keyEnv}, WithClaudeCodeExecCommand(seam))

	out, err := c.run("instr", []byte("content"), "")

	// --bare must be absent from argv when not bare.
	if argsContain(cap.args, "--bare") {
		t.Errorf("--bare must be absent when not bare: %v", cap.args)
	}

	// The inherited auth vars must have been scrubbed: a leak trips the script's
	// exit-9/KEYINJECTED guard, turning the call into an error.
	if err != nil {
		t.Fatalf("non-bare run failed (auth env not scrubbed from child?): %v", err)
	}
	if out != "ok" {
		t.Errorf("result = %q, want ok", out)
	}
}

// TestRun_ContextCancellationKillsChild mirrors applier_test.go's blocking-sleep
// pattern: a cancelled parent context aborts the call promptly via
// exec.CommandContext (R7.1).
func TestRun_ContextCancellationKillsChild(t *testing.T) {
	stubLookPathFound(t)

	// Blocking child: sleeps far longer than the test budget.
	seam := func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sleep", "3600")
	}

	parent, cancel := context.WithCancel(context.Background())
	c, err := NewClaudeCodeClient(LLMConfig{},
		WithClaudeCodeExecCommand(seam),
		WithClaudeCodeContext(parent),
		WithClaudeCodeTimeout(30*time.Second), // long, so cancellation (not timeout) is what aborts
	)
	if err != nil {
		t.Fatalf("NewClaudeCodeClient: %v", err)
	}

	done := make(chan error, 1)
	start := time.Now()
	go func() {
		_, runErr := c.run("instr", []byte("content"), "")
		done <- runErr
	}()

	// Give the spawned sleep a beat to start, then cancel the parent.
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case runErr := <-done:
		if runErr == nil {
			t.Fatal("expected an error after context cancellation")
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Errorf("run took %v after cancel; want prompt abort (<=2s)", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return within 5s of cancel; context cancellation not propagating")
	}
}

// TestRun_TimeoutKillsChild verifies the per-call timeout (R7.1/R7.3) aborts a
// blocking child even when the parent context is never cancelled.
func TestRun_TimeoutKillsChild(t *testing.T) {
	stubLookPathFound(t)

	seam := func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sleep", "3600")
	}

	c, err := NewClaudeCodeClient(LLMConfig{},
		WithClaudeCodeExecCommand(seam),
		WithClaudeCodeTimeout(150*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("NewClaudeCodeClient: %v", err)
	}

	done := make(chan error, 1)
	start := time.Now()
	go func() {
		_, runErr := c.run("instr", []byte("content"), "")
		done <- runErr
	}()

	select {
	case runErr := <-done:
		if runErr == nil {
			t.Fatal("expected an error after timeout")
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Errorf("run took %v; want abort shortly after the 150ms timeout", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not return; per-call timeout not honored")
	}
}

// =============================================================================
// 2.4 — ExtractVersion
// =============================================================================

func TestExtractVersion_CleansResult(t *testing.T) {
	seam, _ := scriptedSeam(`printf '%s' '{"type":"result","is_error":false,"result":"v1.2.3"}'`)
	c := newTestClient(t, LLMConfig{}, WithClaudeCodeExecCommand(seam))

	got, err := c.ExtractVersion([]byte("content"), "")
	if err != nil {
		t.Fatalf("ExtractVersion: %v", err)
	}
	if got != "1.2.3" {
		t.Errorf("ExtractVersion = %q, want %q (v-prefix stripped)", got, "1.2.3")
	}
}

func TestExtractVersion_EmptyResultErrors(t *testing.T) {
	seam, _ := scriptedSeam(`printf '%s' '{"type":"result","is_error":false,"result":"   "}'`)
	c := newTestClient(t, LLMConfig{}, WithClaudeCodeExecCommand(seam))

	_, err := c.ExtractVersion([]byte("content"), "")
	if !errors.Is(err, ErrLLMEmptyResponse) {
		t.Errorf("expected ErrLLMEmptyResponse for empty result, got %v", err)
	}
}

// TestExtractVersion_PromptAppendedToInstruction verifies the caller's prompt is
// appended to the static instruction (and reaches -p), while content stays on
// stdin.
func TestExtractVersion_PromptAppendedToInstruction(t *testing.T) {
	seam, cap := scriptedSeam(`cat >/dev/null; printf '%s' '{"type":"result","is_error":false,"result":"1.0"}'`)
	c := newTestClient(t, LLMConfig{}, WithClaudeCodeExecCommand(seam))

	// Use a distinctive payload marker that cannot appear in the static
	// instruction wording, so the argv-absence assertion tests the real
	// invariant (page payload absent from argv) without false positives.
	const payloadMarker = "PAGE-PAYLOAD-MARKER-55012"
	if _, err := c.ExtractVersion([]byte(payloadMarker), "prefer the latest stable tag"); err != nil {
		t.Fatalf("ExtractVersion: %v", err)
	}

	pVal, ok := flagValue(cap.args, "-p")
	if !ok {
		t.Fatal("-p not present in argv")
	}
	if !strings.Contains(pVal, "prefer the latest stable tag") {
		t.Errorf("-p value did not include the caller prompt: %q", pVal)
	}
	// The page payload must not be in argv (it goes to stdin).
	if argsContainSubstr(cap.args, payloadMarker) {
		t.Errorf("page content leaked into argv: %v", cap.args)
	}
}

// =============================================================================
// 2.5 — AnalyzeContent
// =============================================================================

// validAnalysisJSON is a result body that parseSchemaAnalysis accepts.
const validAnalysisJSON = `{"parser_type":"json","path":"$.tag_name","confidence":0.9,"reasoning":"releases API"}`

func TestAnalyzeContent_StructuredPath(t *testing.T) {
	seam, cap := scriptedSeam(`cat >/dev/null; printf '%s' '{"type":"result","is_error":false,"result":` + jsonQuote(validAnalysisJSON) + `}'`)
	c := newTestClient(t, LLMConfig{}, WithClaudeCodeExecCommand(seam))

	meta := &EbuildMetadata{Package: "dev-foo/bar", Version: "1.0.0", Homepage: "https://example.com"}
	got, err := c.AnalyzeContent([]byte("content"), meta, "look at the API")
	if err != nil {
		t.Fatalf("AnalyzeContent: %v", err)
	}
	if got.ParserType != "json" || got.Path != "$.tag_name" {
		t.Errorf("unexpected analysis: %+v", got)
	}
	// First (structured) call must carry --json-schema.
	if !argsContain(cap.args, "--json-schema") {
		t.Errorf("structured AnalyzeContent should pass --json-schema; argv=%v", cap.args)
	}
	// Metadata should appear in the -p instruction.
	pVal, _ := flagValue(cap.args, "-p")
	if !strings.Contains(pVal, "dev-foo/bar") {
		t.Errorf("instruction should include package metadata; -p=%q", pVal)
	}
}

func TestAnalyzeContent_StripsMarkdownFences(t *testing.T) {
	// Result body is the analysis JSON wrapped in a ```json fence. The CLI
	// envelope's result field contains that fenced string.
	fenced := "```json\n" + validAnalysisJSON + "\n```"
	seam, _ := scriptedSeam(`cat >/dev/null; printf '%s' '{"type":"result","is_error":false,"result":` + jsonQuote(fenced) + `}'`)
	c := newTestClient(t, LLMConfig{}, WithClaudeCodeExecCommand(seam))

	got, err := c.AnalyzeContent([]byte("content"), nil, "")
	if err != nil {
		t.Fatalf("AnalyzeContent (fenced): %v", err)
	}
	if got.ParserType != "json" {
		t.Errorf("expected fenced JSON to parse; got %+v", got)
	}
}

// TestAnalyzeContent_FallbackWhenStructuredErrors proves R3.3: when the
// structured (--json-schema) run fails, AnalyzeContent retries WITHOUT a schema.
// The scripted seam fails on the FIRST call (the one carrying --json-schema) and
// succeeds on the SECOND.
func TestAnalyzeContent_FallbackWhenStructuredErrors(t *testing.T) {
	stubLookPathFound(t)

	var calls int
	var sawSchemaFirst, sawNoSchemaSecond bool
	seam := func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		calls++
		hasSchema := argsContain(arg, "--json-schema")
		if calls == 1 {
			sawSchemaFirst = hasSchema
			// Simulate `--json-schema` unsupported: non-zero exit.
			return exec.CommandContext(ctx, "sh", "-c", `cat >/dev/null; printf 'unknown flag --json-schema' 1>&2; exit 2`)
		}
		sawNoSchemaSecond = !hasSchema
		return exec.CommandContext(ctx, "sh", "-c",
			`cat >/dev/null; printf '%s' '{"type":"result","is_error":false,"result":`+jsonQuote(validAnalysisJSON)+`}'`)
	}

	c, err := NewClaudeCodeClient(LLMConfig{}, WithClaudeCodeExecCommand(seam))
	if err != nil {
		t.Fatalf("NewClaudeCodeClient: %v", err)
	}

	got, err := c.AnalyzeContent([]byte("content"), nil, "")
	if err != nil {
		t.Fatalf("AnalyzeContent fallback: %v", err)
	}
	if got.ParserType != "json" {
		t.Errorf("fallback analysis = %+v, want parser_type json", got)
	}
	if calls != 2 {
		t.Errorf("expected exactly 2 CLI calls (structured then fallback), got %d", calls)
	}
	if !sawSchemaFirst {
		t.Error("first (structured) call should have carried --json-schema")
	}
	if !sawNoSchemaSecond {
		t.Error("second (fallback) call should NOT have carried --json-schema")
	}
}

// TestAnalyzeContent_BothFail verifies an error is returned when both the
// structured and fallback runs fail.
func TestAnalyzeContent_BothFail(t *testing.T) {
	seam, _ := scriptedSeam(`cat >/dev/null; printf 'always broken' 1>&2; exit 5`)
	c := newTestClient(t, LLMConfig{}, WithClaudeCodeExecCommand(seam))

	_, err := c.AnalyzeContent([]byte("content"), nil, "")
	if err == nil {
		t.Fatal("expected error when both structured and fallback runs fail")
	}
}

// TestAnalyzeContent_InvalidJSONInBothFails verifies that when both runs return
// unparseable bodies (valid envelope, garbage result), an error is returned.
func TestAnalyzeContent_InvalidResultInBothFails(t *testing.T) {
	// Envelope is valid JSON and is_error=false, but the result is not a schema
	// object, so parseSchemaAnalysis fails on both attempts.
	seam, _ := scriptedSeam(`cat >/dev/null; printf '%s' '{"type":"result","is_error":false,"result":"not a schema"}'`)
	c := newTestClient(t, LLMConfig{}, WithClaudeCodeExecCommand(seam))

	_, err := c.AnalyzeContent([]byte("content"), nil, "")
	if err == nil {
		t.Fatal("expected error when the result cannot be parsed as a schema in either attempt")
	}
}
