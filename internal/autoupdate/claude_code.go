// Package autoupdate provides LLM integration for version extraction and schema analysis.
//
// claude_code.go implements ClaudeCodeClient, an LLMProvider backed by the local
// `claude` CLI (Claude Code) rather than the Anthropic HTTP API. It shells out to
// the CLI, piping page content on stdin and passing only a static instruction via
// the -p flag, so untrusted page content never lands in argv. Authentication is
// hybrid: when an API key env var is configured and populated the client runs the
// CLI in --bare mode and injects the key solely through the child process
// environment; otherwise it relies on the CLI's own logged-in session. The API
// key value never appears in argv, logs, or returned errors (R2.4, G5).
package autoupdate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultClaudeCodeModel is the default model used by ClaudeCodeClient when
	// none is specified in the config. It is intentionally distinct from
	// DefaultClaudeModel (the haiku model used by the HTTP ClaudeClient): the
	// claude-code CLI provider defaults to sonnet (R1.4, AD7).
	//
	// The value is the CLI's "sonnet" alias rather than a pinned ID such as
	// "claude-sonnet-4-6": `claude --model` resolves the alias to the latest
	// sonnet, which matches AD7's "latest sonnet" intent and is drift-proof as
	// new sonnet releases ship. (Verified against claude 2.1.159 --help: --model
	// accepts an alias like 'sonnet'/'opus' or a full ID like 'claude-opus-4-8';
	// a bare "claude-sonnet-4" is neither and would not resolve.)
	DefaultClaudeCodeModel = "sonnet"

	// DefaultClaudeCodeTimeout bounds a single `claude` CLI invocation. The CLI
	// performs a full agentic round-trip (model call plus tool turns), so it gets
	// a generous-but-finite budget of at least 120s per R7.3.
	DefaultClaudeCodeTimeout = 120 * time.Second
)

// ErrClaudeCodeUnavailable is returned by NewClaudeCodeClient when the `claude`
// CLI cannot be found on PATH (R6.1). Callers can use errors.Is to fall back to
// another provider.
var ErrClaudeCodeUnavailable = errors.New("claude CLI not available on PATH")

// lookPath is the seam used to detect the `claude` binary. It defaults to
// exec.LookPath and is overridable in tests so construction is deterministic
// regardless of the host PATH.
var lookPath = exec.LookPath

// claudeAvailable reports whether the `claude` CLI is resolvable on PATH (R6.1).
func claudeAvailable() bool {
	_, err := lookPath("claude")
	return err == nil
}

// ClaudeCodeClient implements LLMProvider by driving the local `claude` CLI
// (Claude Code). Page content is piped on the command's stdin and the static
// instruction is the value of the -p flag; content never appears in argv
// (R1.2, AD8).
type ClaudeCodeClient struct {
	// model is the resolved model name passed via --model.
	model string
	// apiKeyEnv is the environment variable name that holds the Anthropic API
	// key. Only its value (looked up at call time) is injected into the child
	// environment, and only when bareMode is true.
	apiKeyEnv string
	// bareMode is the resolved tri-state auth decision (see resolveBare). When
	// true the CLI runs with --bare and the API key is injected via the child
	// process env; when false the CLI uses its own logged-in session.
	bareMode bool
	// maxBudgetUSD, when > 0, is passed to the CLI as --max-budget-usd to cap
	// spend (R7.2).
	maxBudgetUSD float64
	// timeout bounds a single CLI invocation (R7.3). Defaults to
	// DefaultClaudeCodeTimeout.
	timeout time.Duration
	// ctx is the parent context for spawned CLI processes. Defaults to
	// context.Background(); a cancelled parent (or the per-call timeout) kills
	// the child via exec.CommandContext (R7.1).
	ctx context.Context
	// execCommand creates the *exec.Cmd bound to a context. It defaults to
	// exec.CommandContext and is injectable for testing.
	execCommand func(ctx context.Context, name string, arg ...string) *exec.Cmd
}

// Compile-time assertion that ClaudeCodeClient satisfies the provider contract.
var _ LLMProvider = (*ClaudeCodeClient)(nil)

// ClaudeCodeOption is a functional option for configuring ClaudeCodeClient.
//
// The option constructors are named with a ClaudeCode prefix to avoid colliding
// with the package-level WithExecCommand (ApplierOption) and WithContext
// (CheckerOption) already defined in this package.
type ClaudeCodeOption func(*ClaudeCodeClient)

// WithClaudeCodeExecCommand overrides the context-aware exec.Command factory used
// to spawn the `claude` CLI. The function mirrors exec.CommandContext so injected
// commands also observe context cancellation. Intended for tests (scripted seam).
func WithClaudeCodeExecCommand(fn func(ctx context.Context, name string, arg ...string) *exec.Cmd) ClaudeCodeOption {
	return func(c *ClaudeCodeClient) {
		c.execCommand = fn
	}
}

// WithClaudeCodeContext sets the parent context threaded into every spawned CLI
// process, so cancelling it (e.g. on SIGINT or a deadline) kills the in-flight
// `claude` process. A nil context is ignored, leaving the default
// context.Background().
func WithClaudeCodeContext(ctx context.Context) ClaudeCodeOption {
	return func(c *ClaudeCodeClient) {
		if ctx != nil {
			c.ctx = ctx
		}
	}
}

// WithClaudeCodeTimeout overrides the per-invocation timeout (R7.3). A
// non-positive duration is ignored so the default (DefaultClaudeCodeTimeout)
// remains in effect.
func WithClaudeCodeTimeout(d time.Duration) ClaudeCodeOption {
	return func(c *ClaudeCodeClient) {
		if d > 0 {
			c.timeout = d
		}
	}
}

// resolveBare resolves the tri-state Bare config into a concrete bareMode
// decision (R2.1, R2.2, R2.3).
//
//   - "true"  → always bare (caller must ensure auth is available).
//   - "false" → never bare; rely on the CLI's logged-in session.
//   - "auto"  (or any other value — config normalize already guarantees the set
//     {auto,true,false}, but the default branch is defensive) → bare IFF an API
//     key env var is configured AND populated in the environment.
func resolveBare(cfg LLMConfig) bool {
	switch cfg.Bare {
	case "true":
		return true
	case "false":
		return false
	default:
		return cfg.APIKeyEnv != "" && os.Getenv(cfg.APIKeyEnv) != ""
	}
}

// NewClaudeCodeClient constructs a ClaudeCodeClient from configuration (R1, R1.1,
// R7.3, AD6). It resolves the model (defaulting to sonnet) and the auth mode,
// applies defaults (exec.CommandContext seam, context.Background, a >=120s
// timeout), then applies any options. If the `claude` CLI is not on PATH it
// returns ErrClaudeCodeUnavailable (R6.1) so callers can fall back.
func NewClaudeCodeClient(cfg LLMConfig, opts ...ClaudeCodeOption) (*ClaudeCodeClient, error) {
	if !claudeAvailable() {
		return nil, ErrClaudeCodeUnavailable
	}

	model := cfg.Model
	if model == "" {
		model = DefaultClaudeCodeModel
	}

	c := &ClaudeCodeClient{
		model:        model,
		apiKeyEnv:    cfg.APIKeyEnv,
		bareMode:     resolveBare(cfg),
		maxBudgetUSD: cfg.MaxBudgetUSD,
		timeout:      DefaultClaudeCodeTimeout,
		ctx:          context.Background(), // SAFE: default parent; replaced by WithClaudeCodeContext when a caller wires a cancellable context.
		execCommand:  exec.CommandContext,
	}

	// Apply options AFTER defaults so they can override the seam, context, and
	// timeout.
	for _, opt := range opts {
		opt(c)
	}

	return c, nil
}

// GetModel returns the resolved model name used by this client (R1.4).
func (c *ClaudeCodeClient) GetModel() string {
	return c.model
}

// claudeCodeEnvelope is the JSON envelope emitted by `claude --output-format json`.
// Only the fields the provider consumes are modeled.
type claudeCodeEnvelope struct {
	Type         string   `json:"type"`
	Subtype      string   `json:"subtype"`
	IsError      bool     `json:"is_error"`
	Result       string   `json:"result"`
	Errors       []string `json:"errors"`
	TotalCostUSD float64  `json:"total_cost_usd"`
}

// buildArgs assembles the CLI argument vector (R1.2, R1.3, R1.5, R7, R7.2).
//
// The static instruction is always the value of -p (R1.2); page content is NEVER
// placed here — it is piped on stdin by run. The fixed flags --output-format json,
// --max-turns 2 and --allowedTools "" lock the CLI into a single structured,
// tool-free round-trip (R1.3, R1.5). --bare is added in bare mode; --json-schema
// is added only for a structured request with a non-empty schema; --max-budget-usd
// is added when a positive cap is configured.
func (c *ClaudeCodeClient) buildArgs(instruction string, structured bool, schema string) []string {
	args := []string{
		"-p", instruction,
		"--output-format", "json",
		"--max-turns", "2",
		"--allowedTools", "",
		"--model", c.model,
	}
	if c.bareMode {
		args = append(args, "--bare")
	}
	if structured && schema != "" {
		args = append(args, "--json-schema", schema)
	}
	if c.maxBudgetUSD > 0 {
		args = append(args, "--max-budget-usd", strconv.FormatFloat(c.maxBudgetUSD, 'f', -1, 64))
	}
	return args
}

// run executes the `claude` CLI for a single request and returns the envelope
// result string (R1.2, R2.4, R7, R7.1).
//
// Page content is piped on stdin (AD8); the instruction travels in -p. The call
// is bound to a child context derived from c.ctx with c.timeout, so a cancelled
// parent or an elapsed timeout kills the child (R7.1). In bare mode the API key
// is injected ONLY through the child environment (never argv/logs — R2.1, R2.4).
// stdout and stderr are captured separately. A non-zero exit, an is_error
// envelope, or non-JSON stdout each yield an error that includes the envelope
// errors/subtype and stderr but NEVER the API key.
func (c *ClaudeCodeClient) run(instruction string, content []byte, schema string) (string, error) {
	ctx, cancel := context.WithTimeout(c.ctx, c.timeout)
	defer cancel()

	cmd := c.execCommand(ctx, "claude", c.buildArgs(instruction, schema != "", schema)...)

	// Page content goes on stdin, never in argv (R1.2, AD8).
	cmd.Stdin = bytes.NewReader(content)

	// In bare mode inject the API key solely via the child environment. We start
	// from the current environment and append ANTHROPIC_API_KEY so the key value
	// never appears in argv or logs (R2.1, R2.4, G5).
	if c.bareMode {
		env := os.Environ()
		if key := os.Getenv(c.apiKeyEnv); key != "" {
			env = append(env, "ANTHROPIC_API_KEY="+key)
		}
		cmd.Env = env
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()

	// Attempt to parse the envelope regardless of exit code: a non-zero exit
	// often still carries a structured error envelope on stdout.
	var env claudeCodeEnvelope
	jsonErr := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &env)

	// Surface the CLI's own stderr in error messages, trimmed. It must never
	// contain the API key (we never write the key to argv/logs and the CLI does
	// not echo env values).
	stderrStr := strings.TrimSpace(stderr.String())

	if runErr != nil {
		// Non-zero exit (or spawn failure). Prefer the structured errors/subtype
		// from the envelope when available; fall back to stderr.
		if jsonErr == nil && (len(env.Errors) > 0 || env.Subtype != "") {
			return "", fmt.Errorf("%w: claude CLI failed (%s): %s", ErrLLMRequestFailed, env.Subtype, strings.Join(env.Errors, "; "))
		}
		if stderrStr != "" {
			return "", fmt.Errorf("%w: claude CLI failed: %v: %s", ErrLLMRequestFailed, runErr, stderrStr)
		}
		return "", fmt.Errorf("%w: claude CLI failed: %v", ErrLLMRequestFailed, runErr)
	}

	if jsonErr != nil {
		// Exited zero but stdout was not valid JSON.
		if stderrStr != "" {
			return "", fmt.Errorf("%w: claude CLI emitted non-JSON output: %v: %s", ErrLLMRequestFailed, jsonErr, stderrStr)
		}
		return "", fmt.Errorf("%w: claude CLI emitted non-JSON output: %v", ErrLLMRequestFailed, jsonErr)
	}

	if env.IsError {
		// Structured error envelope (process may still have exited zero).
		return "", fmt.Errorf("%w: claude CLI reported error (%s): %s", ErrLLMRequestFailed, env.Subtype, strings.Join(env.Errors, "; "))
	}

	return env.Result, nil
}

// buildVersionInstruction builds the static instruction for version extraction.
// The page content is NOT embedded (it is piped on stdin); the caller's prompt is
// appended as extra guidance when non-empty (R1.2).
func buildClaudeCodeVersionInstruction(prompt string) string {
	var sb strings.Builder
	sb.WriteString("Extract the version number from the piped content. ")
	sb.WriteString("Respond with ONLY the version, no other text.")
	if strings.TrimSpace(prompt) != "" {
		sb.WriteString(" Additional instructions: ")
		sb.WriteString(prompt)
	}
	return sb.String()
}

// ExtractVersion extracts a version string from content using the `claude` CLI
// (R1.2). The content is piped on stdin; only a static instruction (plus the
// caller's optional prompt) travels in -p. The envelope result is normalized via
// the shared cleanVersionString helper.
func (c *ClaudeCodeClient) ExtractVersion(content []byte, prompt string) (string, error) {
	instruction := buildClaudeCodeVersionInstruction(prompt)

	result, err := c.run(instruction, content, "")
	if err != nil {
		return "", err
	}

	version := cleanVersionString(result)
	if version == "" {
		return "", ErrLLMEmptyResponse
	}
	return version, nil
}

// claudeCodeSchemaJSON is the JSON Schema describing the SchemaAnalysis shape that
// the CLI is asked to satisfy via --json-schema (R3, R3.1). It mirrors the field
// set parseSchemaAnalysis understands.
const claudeCodeSchemaJSON = `{
  "type": "object",
  "properties": {
    "parser_type": {"type": "string", "enum": ["json", "regex", "html"]},
    "path": {"type": "string"},
    "pattern": {"type": "string"},
    "selector": {"type": "string"},
    "xpath": {"type": "string"},
    "fallback_type": {"type": "string"},
    "fallback_config": {"type": "string"},
    "confidence": {"type": "number"},
    "reasoning": {"type": "string"}
  },
  "required": ["parser_type", "confidence"]
}`

// buildClaudeCodeAnalysisInstruction builds the static analysis instruction. The
// page content is NOT embedded (it is piped on stdin); package metadata and the
// optional hint are included to guide the model. When askForJSON is true (the
// schema-less fallback path) the instruction explicitly asks for a raw JSON
// response so parseSchemaAnalysis can recover it (R3.3).
func buildClaudeCodeAnalysisInstruction(meta *EbuildMetadata, hint string, askForJSON bool) string {
	var sb strings.Builder
	sb.WriteString("Analyze the piped content and respond with the parser schema as JSON")
	if askForJSON {
		sb.WriteString(" object only, with no surrounding prose or markdown fences")
	}
	sb.WriteString(".")

	if meta != nil {
		sb.WriteString("\n\nPackage Information:")
		if meta.Package != "" {
			fmt.Fprintf(&sb, "\n- Package: %s", meta.Package)
		}
		if meta.Version != "" {
			fmt.Fprintf(&sb, "\n- Current Version: %s", meta.Version)
		}
		if meta.Homepage != "" {
			fmt.Fprintf(&sb, "\n- Homepage: %s", meta.Homepage)
		}
	}

	if strings.TrimSpace(hint) != "" {
		sb.WriteString("\n\nUser Hint: ")
		sb.WriteString(hint)
	}

	if askForJSON {
		sb.WriteString("\n\nRespond with a JSON object containing: parser_type (json|regex|html), ")
		sb.WriteString("path, pattern, selector, xpath, fallback_type, fallback_config, confidence (0.0-1.0), reasoning.")
	}

	return sb.String()
}

// stripJSONFences removes a leading ```json (or ```) fence and a trailing ```
// fence from text, returning the inner payload trimmed. parseSchemaAnalysis would
// otherwise still find the JSON object between the fences (it scans for { ... }),
// but stripping fences first keeps recovery robust against fenced output.
func stripJSONFences(text string) string {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "```") {
		return trimmed
	}
	// Drop the opening fence line (``` or ```json).
	if nl := strings.IndexByte(trimmed, '\n'); nl != -1 {
		trimmed = trimmed[nl+1:]
	} else {
		trimmed = strings.TrimPrefix(trimmed, "```")
	}
	// Drop a trailing closing fence.
	trimmed = strings.TrimSpace(trimmed)
	trimmed = strings.TrimSuffix(trimmed, "```")
	return strings.TrimSpace(trimmed)
}

// AnalyzeContent analyzes content via the `claude` CLI and returns a suggested
// parser configuration (R3, R3.1, R3.2, R3.3).
//
// Control flow:
//  1. Attempt a structured request that passes --json-schema; on success parse
//     the result (stripping any markdown fences) via parseSchemaAnalysis.
//  2. If the structured request ERRORS (e.g. the CLI build does not support
//     --json-schema), retry WITHOUT a schema, asking for a raw JSON response, and
//     parse that (R3.3).
//  3. If both attempts fail, return the resulting error.
//
// Page content is piped on stdin on both attempts.
func (c *ClaudeCodeClient) AnalyzeContent(content []byte, meta *EbuildMetadata, hint string) (*SchemaAnalysis, error) {
	// Attempt 1: structured request with --json-schema.
	structuredInstruction := buildClaudeCodeAnalysisInstruction(meta, hint, false)
	result, err := c.run(structuredInstruction, content, claudeCodeSchemaJSON)
	if err == nil {
		if analysis, parseErr := parseSchemaAnalysis(stripJSONFences(result)); parseErr == nil {
			return analysis, nil
		} else {
			err = parseErr
		}
	}

	// Attempt 2 (fallback, R3.3): retry without a schema, asking for raw JSON.
	fallbackInstruction := buildClaudeCodeAnalysisInstruction(meta, hint, true)
	fallbackResult, fallbackErr := c.run(fallbackInstruction, content, "")
	if fallbackErr != nil {
		return nil, fmt.Errorf("claude-code schema analysis failed (structured: %v; fallback: %w)", err, fallbackErr)
	}

	analysis, parseErr := parseSchemaAnalysis(stripJSONFences(fallbackResult))
	if parseErr != nil {
		return nil, fmt.Errorf("claude-code schema analysis could not be parsed (structured: %v; fallback parse: %w)", err, parseErr)
	}
	return analysis, nil
}
