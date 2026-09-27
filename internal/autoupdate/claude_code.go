// Package autoupdate provides LLM integration for version extraction and schema analysis.
//
// claude_code.go implements ClaudeCodeClient, an LLMProvider backed by the local
// `claude` CLI (Claude Code) rather than the Anthropic HTTP API. It shells out to
// the CLI, piping page content on stdin and passing only a static instruction via
// the -p flag, so untrusted page content never lands in argv. Authentication is
// hybrid: when an API key env var is configured and populated the client runs the
// CLI in --bare mode and injects the key solely through the child process
// environment; otherwise it relies on the CLI's own logged-in session. The API
// key value never appears in argv, logs, or returned errors (S003-R2.4, G5).
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

	"github.com/obentoo/bentoolkit/internal/common/secrets"
)

const (
	// DefaultClaudeCodeModel is the default model used by ClaudeCodeClient when
	// none is specified in the config. It is intentionally distinct from
	// DefaultClaudeModel (the haiku model used by the HTTP ClaudeClient): the
	// claude-code CLI provider defaults to sonnet (S003-R1.4, AD7).
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
	// a generous-but-finite budget of at least 120s per S003-R7.3.
	DefaultClaudeCodeTimeout = 120 * time.Second
)

// ErrClaudeCodeUnavailable is returned by NewClaudeCodeClient when the `claude`
// CLI cannot be found on PATH (S003-R6.1). Callers can use errors.Is to fall back to
// another provider.
var ErrClaudeCodeUnavailable = errors.New("claude CLI not available on PATH")

// The outcome sentinels say WHICH way a `claude` invocation failed, so a caller
// can branch with errors.Is instead of reading the sentence. Every failure run
// returns matches ErrLLMRequestFailed and exactly one of these; its Error() is
// the sentence it always was (see `type claudeOutcomeError`).
var (
	// ErrClaudeTimedOut: this client's own budget elapsed before the CLI answered.
	ErrClaudeTimedOut = errors.New("claude CLI timed out")
	// ErrClaudeStopped: the parent context ended the run (cancelled, or out of
	// a budget of its own).
	ErrClaudeStopped = errors.New("claude CLI stopped")
	// ErrClaudeCouldNotStart: the process never started.
	ErrClaudeCouldNotStart = errors.New("claude CLI could not start")
	// ErrClaudeExitedNonZero: the process ran and exited with a failure status.
	ErrClaudeExitedNonZero = errors.New("claude CLI exited non-zero")
	// ErrClaudeUnusableOutput: the process exited zero but its output was not
	// JSON, or was an error envelope.
	ErrClaudeUnusableOutput = errors.New("claude CLI output unusable")
)

// claudeOutcomeError attaches an outcome sentinel to a failure without
// rewording it: Error() is the wrapped error's text, byte for byte, and
// errors.Is reaches both the wrapped chain (ErrLLMRequestFailed) and the outcome.
type claudeOutcomeError struct {
	err     error
	outcome error
}

func (e *claudeOutcomeError) Error() string { return e.err.Error() }

func (e *claudeOutcomeError) Unwrap() []error { return []error{e.err, e.outcome} }

// withClaudeOutcome wraps err so it also matches outcome.
func withClaudeOutcome(err, outcome error) error {
	return &claudeOutcomeError{err: err, outcome: outcome}
}

// lookPath is the seam used to detect the `claude` binary. It defaults to
// exec.LookPath and is overridable in tests so construction is deterministic
// regardless of the host PATH.
var lookPath = exec.LookPath

// claudeAvailable reports whether the `claude` CLI is resolvable on PATH (S003-R6.1).
func claudeAvailable() bool {
	_, err := lookPath("claude")
	return err == nil
}

// ClaudeCodeClient implements LLMProvider by driving the local `claude` CLI
// (Claude Code). Page content is piped on the command's stdin and the static
// instruction is the value of the -p flag; content never appears in argv
// (S003-R1.2, AD8).
type ClaudeCodeClient struct {
	// model is the resolved model name passed via --model.
	model string
	// apiKeyEnv is the environment variable name that holds the Anthropic API
	// key. In non-bare mode it names an auth var to scrub from the child env; the
	// injected key VALUE is the pre-resolved apiKey (below), never re-read here.
	apiKeyEnv string
	// apiKey is the Anthropic API key resolved ONCE at construction via
	// secrets.Lookup(apiKeyEnv) over the fixed chain (env → user file → system
	// file). This single value drives BOTH the bare-mode decision (resolveBare)
	// and the child-env injection, so a key present only in a secrets file cannot
	// flip bare on yet be missing from the spawned CLI. It is injected solely
	// through the child env in bare mode and never appears in argv, logs, or
	// returned errors (S003-R2.4, G5).
	apiKey string
	// bareMode is the resolved tri-state auth decision (see resolveBare). When
	// true the CLI runs with --bare and the API key is injected via the child
	// process env; when false the CLI uses its own logged-in session.
	bareMode bool
	// maxBudgetUSD, when > 0, is passed to the CLI as --max-budget-usd to cap
	// spend (S003-R7.2).
	maxBudgetUSD float64
	// timeout bounds a single CLI invocation (S003-R7.3). Defaults to
	// DefaultClaudeCodeTimeout.
	timeout time.Duration
	// ctx is the parent context for spawned CLI processes. Defaults to
	// context.Background(); a cancelled parent (or the per-call timeout) kills
	// the child via exec.CommandContext (S003-R7.1).
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

// WithClaudeCodeTimeout overrides the per-invocation timeout (S003-R7.3). A
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
// decision (S003-R2.1, S003-R2.2, S003-R2.3).
//
//   - "true"  → always bare (caller must ensure auth is available).
//   - "false" → never bare; rely on the CLI's logged-in session. Any inherited
//     API key is scrubbed from the child env (see childEnv) so it cannot override
//     that session.
//   - "auto"  (or any other value — config normalize already guarantees the set
//     {auto,true,false}, but the default branch is defensive) → bare IFF an API
//     key env var is configured AND the single resolved key passed in is
//     non-empty. The key is resolved ONCE by the caller (via secrets.Lookup) and
//     handed in, so this decision no longer reads the environment itself — a key
//     that lives only in a secrets file still selects bare.
func resolveBare(cfg LLMConfig, key string) bool {
	switch cfg.Bare {
	case "true":
		return true
	case "false":
		return false
	default:
		return cfg.APIKeyEnv != "" && key != ""
	}
}

// scrubbedAuthEnvVars are the environment variables the `claude` CLI honours as
// non-interactive API auth sources. In non-bare mode they are stripped from the
// child environment so the CLI cannot silently prefer an inherited API key over
// its own logged-in (subscription) session — the canonical ANTHROPIC_API_KEY and
// ANTHROPIC_AUTH_TOKEN, plus the operator-configured key var (apiKeyEnv) which
// may be a custom name feeding the same key.
var scrubbedAuthEnvVars = []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"}

// childEnv builds the environment for a spawned `claude` process from the
// resolved auth mode.
//
//   - bare mode: the inherited environment plus an injected ANTHROPIC_API_KEY set
//     to key — the single credential the caller already resolved via
//     secrets.Lookup (injected only when non-empty). The value travels solely
//     through the env, never argv/logs, and is NOT re-read from apiKeyEnv here, so
//     a key that lives only in a secrets file (absent from the process env) is
//     still injected into the child.
//   - non-bare mode: the inherited environment with every auth source in
//     scrubbedAuthEnvVars AND apiKeyEnv REMOVED, so an API key present in the
//     parent env (e.g. exported from a shell rc) cannot override the operator's
//     explicit `bare: false` choice to run on the CLI's logged-in session.
//
// It always returns a non-nil slice so callers assign cmd.Env unconditionally
// (a nil cmd.Env would make the child inherit the parent env verbatim, defeating
// the non-bare scrub).
func childEnv(bareMode bool, apiKeyEnv string, key string) []string {
	if bareMode {
		env := os.Environ()
		if key != "" {
			env = append(env, "ANTHROPIC_API_KEY="+key)
		}
		return env
	}

	// Non-bare: drop every auth var the CLI could read so it falls back to its
	// own session. Build the strip set once (canonical vars + the configured
	// name), then filter the inherited environment by KEY prefix.
	strip := make(map[string]struct{}, len(scrubbedAuthEnvVars)+1)
	for _, name := range scrubbedAuthEnvVars {
		strip[name] = struct{}{}
	}
	if apiKeyEnv != "" {
		strip[apiKeyEnv] = struct{}{}
	}

	parent := os.Environ()
	env := make([]string, 0, len(parent))
	for _, kv := range parent {
		name := kv
		if eq := strings.IndexByte(kv, '='); eq >= 0 {
			name = kv[:eq]
		}
		if _, drop := strip[name]; drop {
			continue
		}
		env = append(env, kv)
	}
	return env
}

// NewClaudeCodeClient constructs a ClaudeCodeClient from configuration (S003-R1, S003-R1.1,
// S003-R7.3, AD6). It resolves the model (defaulting to sonnet) and the auth mode,
// applies defaults (exec.CommandContext seam, context.Background,
// DefaultClaudeCodeTimeout), then applies any options. If the `claude` CLI is
// not on PATH it returns ErrClaudeCodeUnavailable (S003-R6.1) so callers can
// fall back.
//
// The timeout it applies is a DEFAULT and not the budget every caller runs
// under. Options are applied last, so a caller handing WithClaudeCodeTimeout a
// POSITIVE duration — as the `overlay compare` review does, with the operator's
// configured value — runs under that one instead (S048-R3.1). A non-positive one
// is ignored and the default stands, which is the option's own documented rule.
func NewClaudeCodeClient(cfg LLMConfig, opts ...ClaudeCodeOption) (*ClaudeCodeClient, error) {
	if !claudeAvailable() {
		return nil, ErrClaudeCodeUnavailable
	}

	// Resolve the API key EXACTLY ONCE through the unified secrets chain (env →
	// user file → system file). This single value drives BOTH the bare-mode
	// decision and the child-env injection below, closing the split-brain gap
	// where a key present only in a secrets file flipped bare on but the old
	// os.Getenv injection spawned `claude` with no credential. A present-but-
	// unreadable secrets file surfaces as secrets.ErrUnreadable rather than
	// silently degrading to an unauthenticated run.
	//
	// An EMPTY api_key_env means no credential was requested at all — the
	// subscription shape, where the agentic `claude` authenticates itself. Skip
	// the chain entirely in that case: resolving the empty name would consult
	// the secrets file and turn an unreadable one into a spurious constructor
	// failure. NewClaudeClient/NewOpenAIClient guard the same way, except that
	// for them an empty name is fatal (ErrLLMNotConfigured) while here it is a
	// valid configuration.
	var key string
	if cfg.APIKeyEnv != "" {
		resolved, _, err := secrets.Lookup(cfg.APIKeyEnv)
		if err != nil {
			return nil, err
		}
		key = resolved
	}

	model := cfg.Model
	if model == "" {
		model = DefaultClaudeCodeModel
	}

	c := &ClaudeCodeClient{
		model:        model,
		apiKeyEnv:    cfg.APIKeyEnv,
		apiKey:       key,
		bareMode:     resolveBare(cfg, key),
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

// GetModel returns the resolved model name used by this client (S003-R1.4).
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

// buildArgs assembles the CLI argument vector (S003-R1.2, S003-R1.3, S003-R1.5, S003-R7, S003-R7.2).
//
// The static instruction is always the value of -p (S003-R1.2); page content is NEVER
// placed here — it is piped on stdin by run. The fixed flags --output-format json,
// --max-turns 2 and --allowedTools "" lock the CLI into a single structured,
// tool-free round-trip (S003-R1.3, S003-R1.5). --bare is added in bare mode; --json-schema
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
// result string (S003-R1.2, S003-R2.4, S003-R7, S003-R7.1).
//
// Page content is piped on stdin (AD8); the instruction travels in -p. The call
// is bound to a child context derived from c.ctx with c.timeout, so a cancelled
// parent or an elapsed timeout kills the child (S003-R7.1). In bare mode the API key
// is injected ONLY through the child environment (never argv/logs — S003-R2.1, S003-R2.4).
// stdout and stderr are captured separately.
//
// A FAILED invocation is classified before any exit-code framing, in the
// precedence the package keeps in one place (S048-R1.1, S048-R1.2, S048-R1.3):
// a run this client's own deadline ended says so and names the budget that
// elapsed, a process that never started says that, and only a process that ran
// and exited with a status is framed by that status. An is_error envelope or
// non-JSON stdout on a zero exit each yield an error that includes the envelope
// errors/subtype and stderr but NEVER the API key.
//
// EVERY invocation, by any outcome including success, records its wall-clock
// duration alongside that outcome as one Info line through the package's
// infoLogf sink, so the cost of a `claude` call is recoverable from a run's own
// output without instrumenting for it again (S048-R2.1, S048-R5.1).
func (c *ClaudeCodeClient) run(instruction string, content []byte, schema string) (string, error) {
	ctx, cancel := context.WithTimeout(c.ctx, c.timeout)
	defer cancel()

	cmd := c.execCommand(ctx, "claude", c.buildArgs(instruction, schema != "", schema)...)

	// Page content goes on stdin, never in argv (S003-R1.2, AD8).
	cmd.Stdin = bytes.NewReader(content)

	// Resolve the child environment from the auth mode: bare injects the API key
	// (only via env, never argv/logs — S003-R2.1, S003-R2.4, G5); non-bare scrubs any
	// inherited API key so the CLI uses its logged-in session.
	cmd.Env = childEnv(c.bareMode, c.apiKeyEnv, c.apiKey)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	startedAt := time.Now()
	runErr := cmd.Run()

	// The context is read HERE, before anything frames this failure, because the
	// exit status of a child our own deadline SIGKILLed is only what the kernel
	// left behind: "signal: killed" names neither the deadline nor the budget,
	// and a message built from it sends whoever reads it looking for a broken
	// CLI. Reading the context afterwards would print that noise first and reach
	// the cause too late to say it (S048-R1.1).
	ctxErr := ctx.Err()

	// The elapsed time is taken at the same boundary and for a related reason:
	// what S048-R2.1 asks to record is what the CLI COST, not what this function
	// spends afterwards parsing the envelope and composing a sentence.
	elapsed := time.Since(startedAt)

	// WHICH outcome this was is decided ONCE, here, and read twice: by the line
	// below and by the failure switch further down. Classifying separately for
	// the record and for the message would be two answers free to drift, which
	// is the shape of defect lifting the classifier out removed in the first
	// place (S048-R1.3). Both errors nil is claudeRanToCompletion — the success
	// case — so this names every ending, not only the bad ones.
	outcome := classifyClaudeFailure(ctxErr, runErr)

	// EVERY outcome is recorded, and `defer` is what makes "every" a fact rather
	// than a claim. run has ten exits below this point — the failure switch's,
	// the exit-code framing's, the two output-shape paths' and the success
	// path's — and the runtime executes a deferred call on each one, on a panic
	// unwinding through it, and on any return a later edit adds. A statement
	// written in the straight line would have to be re-checked against every
	// exit each time the function grows one, and the exit it missed would be
	// silence rather than an error.
	//
	// The success case is the one this exists for: a budget cannot be derived
	// from the failures, because the failures are exactly the runs that hit the
	// ceiling, and the value a new ceiling must clear is the distribution of the
	// runs that did not (S048-R2.1, S048-R5.1). `run` returns (string, error),
	// so a successful call has no return channel for a duration and widening the
	// signature would publish a number no caller consumes — a log line at the
	// boundary of the external call is the sink, as this project's convention
	// for external calls already asks (D5).
	//
	// Info, not Warn: a call that finished at its ordinary cost is an event, not
	// a degradation, and sending the failures to a different sink would split
	// one measurement across two readers. The line carries a duration and an
	// outcome and nothing else — never the API key this client injects through
	// the child environment (S003-R2.4, G5).
	defer func() {
		infoLogf("claude CLI invocation finished: outcome=%s elapsed=%s", outcome, elapsed)
	}()

	// Attempt to parse the envelope regardless of exit code: a non-zero exit
	// often still carries a structured error envelope on stdout.
	var env claudeCodeEnvelope
	jsonErr := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &env)

	// Surface the CLI's own stderr in error messages, trimmed. It must never
	// contain the API key (we never write the key to argv/logs and the CLI does
	// not echo env values).
	stderrStr := strings.TrimSpace(stderr.String())

	if runErr != nil {
		// WHICH failure this is, is decided once by the classifier the fixers use
		// too, so the two cannot drift apart on the order; only the words below
		// are this client's own, because a review told "claude fixer aborted"
		// would be told about an operation it never ran (S048-R1.3).
		switch outcome {
		case claudeCutShort:
			// WHOSE clock ran out decides what may be claimed. A parent that is
			// already done ended this run from OUTSIDE — cancelled, or out of a
			// budget of its own — and this client's budget is then not what
			// elapsed, so quoting it would quote a number that never ran out.
			endedBy := c.ctx.Err()
			if endedBy == nil && errors.Is(ctxErr, context.DeadlineExceeded) {
				// S048-R1.1: the budget is read from the client's own field —
				// the value ACTUALLY in force — and never from the package
				// default, because a caller that passed WithClaudeCodeTimeout
				// makes the two differ, and the remedy for this failure is to
				// raise the number that elapsed and not the one that did not.
				// `func formatBumpReviewSkipTimeout` is the precedent this
				// follows.
				return "", withClaudeOutcome(fmt.Errorf("%w: claude CLI ran out of time: its %s budget elapsed before it answered",
					ErrLLMRequestFailed, c.timeout), ErrClaudeTimedOut)
			}
			// Ended by anything other than this client's own budget: the cause
			// travels verbatim and no number is claimed. One sentence, written
			// once, because two spellings of one outcome is the shape of defect
			// this story exists to remove.
			if endedBy == nil {
				endedBy = ctxErr
			}
			return "", withClaudeOutcome(fmt.Errorf("%w: claude CLI was stopped before it answered: %v", ErrLLMRequestFailed, endedBy), ErrClaudeStopped)
		case claudeCouldNotStart:
			// S048-R1.2, S040-R5.6: the process never reached its first
			// instruction, so there is no exit status to frame it with and none
			// may be implied. The remedy is on the host — a missing binary, an
			// unreachable working directory — and it is the opposite of the
			// deadline's, which is why the two sentences must not be one.
			return "", withClaudeOutcome(fmt.Errorf("%w: claude CLI could not start: %v", ErrLLMRequestFailed, runErr), ErrClaudeCouldNotStart)
		}

		// claudeExitedNonZero: the process ran and exited with a status. This is
		// the outcome S048 does not touch, and its three messages are byte for
		// byte the ones four existing tests and every operator already read
		// (S048-R4.2). Prefer the structured errors/subtype from the envelope
		// when available; fall back to stderr.
		if jsonErr == nil && (len(env.Errors) > 0 || env.Subtype != "") {
			return "", withClaudeOutcome(fmt.Errorf("%w: claude CLI failed (%s): %s", ErrLLMRequestFailed, env.Subtype, strings.Join(env.Errors, "; ")), ErrClaudeExitedNonZero)
		}
		if stderrStr != "" {
			return "", withClaudeOutcome(fmt.Errorf("%w: claude CLI failed: %v: %s", ErrLLMRequestFailed, runErr, stderrStr), ErrClaudeExitedNonZero)
		}
		return "", withClaudeOutcome(fmt.Errorf("%w: claude CLI failed: %v", ErrLLMRequestFailed, runErr), ErrClaudeExitedNonZero)
	}

	if jsonErr != nil {
		// Exited zero but stdout was not valid JSON.
		if stderrStr != "" {
			return "", withClaudeOutcome(fmt.Errorf("%w: claude CLI emitted non-JSON output: %v: %s", ErrLLMRequestFailed, jsonErr, stderrStr), ErrClaudeUnusableOutput)
		}
		return "", withClaudeOutcome(fmt.Errorf("%w: claude CLI emitted non-JSON output: %v", ErrLLMRequestFailed, jsonErr), ErrClaudeUnusableOutput)
	}

	if env.IsError {
		// Structured error envelope (process may still have exited zero).
		return "", withClaudeOutcome(fmt.Errorf("%w: claude CLI reported error (%s): %s", ErrLLMRequestFailed, env.Subtype, strings.Join(env.Errors, "; ")), ErrClaudeUnusableOutput)
	}

	return env.Result, nil
}

// AskJSON runs ONE schema-constrained round trip through the `claude` CLI and
// returns the model's reply verbatim, exactly as the envelope carried it.
//
// It is the EXPORTED spelling of run, and it exists so a caller outside this
// package can reach the CLI without rebuilding the parts of run that are not
// about its own question. Those parts are the security-relevant ones: the
// instruction travels in -p while content is piped on stdin (AD8), the child
// environment is resolved by childEnv — which in non-bare mode STRIPS every
// inherited API key so the CLI falls back to its own logged-in session — and the
// invocation is bound to c.ctx with c.timeout. All three are unexported, so a
// second call site spelling its own exec.Command would be a second, divergent
// answer to each of them.
//
// The reply is returned UNTOUCHED. Callers with a schema know what shape they
// asked for; ExtractVersion and AnalyzeContent above each normalize for their
// own, and doing it here would impose one of those on everybody.
//
// Its one production caller today is the divergence-review adapter in
// cmd/bentoo/overlay_compare_review.go, which asks for a three-field JSON object
// describing how two ebuilds differ.
func (c *ClaudeCodeClient) AskJSON(instruction string, content []byte, schema string) (string, error) {
	return c.run(instruction, content, schema)
}

// buildVersionInstruction builds the static instruction for version extraction.
// The page content is NOT embedded (it is piped on stdin); the caller's prompt is
// appended as extra guidance when non-empty (S003-R1.2).
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
// (S003-R1.2). The content is piped on stdin; only a static instruction (plus the
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
// the CLI is asked to satisfy via --json-schema (S003-R3, S003-R3.1). It mirrors the field
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
// response so parseSchemaAnalysis can recover it (S003-R3.3).
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
// parser configuration (S003-R3, S003-R3.1, S003-R3.2, S003-R3.3).
//
// Control flow:
//  1. Attempt a structured request that passes --json-schema; on success parse
//     the result (stripping any markdown fences) via parseSchemaAnalysis.
//  2. If the structured request ERRORS (e.g. the CLI build does not support
//     --json-schema), retry WITHOUT a schema, asking for a raw JSON response, and
//     parse that (S003-R3.3).
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

	// Attempt 2 (fallback, S003-R3.3): retry without a schema, asking for raw JSON.
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
