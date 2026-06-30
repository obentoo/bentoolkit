// Package autoupdate provides LLM integration for version extraction and schema analysis.
//
// manifest_fixer.go implements ManifestFixer, an *agentic* counterpart to the
// sandboxed ClaudeCodeClient (claude_code.go). Where that client runs the local
// `claude` CLI tool-free (--allowedTools "") and feeds it page content on stdin,
// the fixer drives the CLI as a working agent: it is scoped to a single package
// directory (--add-dir), allowed to read/edit the ebuild and run a narrow set of
// shell commands (pkgdev/wget/ls/cat), and asked to repair a SRC_URI/manifest
// breakage in place. The agent's edits ARE the side effect; the function returns
// only a short human-readable summary.
//
// The authoritative success check is NOT the agent's self-report: after the fixer
// returns, the Applier re-runs its own `pkgdev manifest` step and only treats the
// apply as recovered if THAT succeeds (see runManifestWithFix in applier.go).
package autoupdate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// DefaultManifestFixTimeout bounds a single agentic `claude` fix invocation. The
// fixer performs a multi-turn agentic loop (read the ebuild, inspect the upstream,
// edit, self-verify with pkgdev), so it gets a far larger budget than the
// tool-free extraction path (DefaultClaudeCodeTimeout = 120s).
const DefaultManifestFixTimeout = 10 * time.Minute

// manifestFixMaxTurns caps the agent's internal tool-turn loop. It bounds cost and
// guarantees termination without an external retry loop in the Applier.
const manifestFixMaxTurns = 30

// manifestFixWaitDelay bounds how long cmd.Wait blocks draining I/O after the
// context is cancelled or the process exits. The agent spawns children (pkgdev,
// wget) that may hold the stdout pipe open past a kill of `claude`; without this
// bound Wait could hang until those children exit on their own.
const manifestFixWaitDelay = 10 * time.Second

// manifestFixAllowedTools is the scoped tool allowlist handed to the agent. Edit/
// Read/Write let it rewrite the ebuild (under --add-dir); the Bash() patterns are
// narrowed to the commands a manifest repair legitimately needs, so the agent can
// self-verify and inspect the tree without an open shell. Anything outside this
// set is denied by the CLI without an interactive prompt, which keeps the run
// non-interactive WITHOUT resorting to --dangerously-skip-permissions.
var manifestFixAllowedTools = []string{
	"Read",
	"Edit",
	"Write",
	"Bash(pkgdev *)",
	"Bash(wget *)",
	"Bash(ls *)",
	"Bash(cat *)",
}

// bentooEbuildGuidance is appended to the agent's system prompt via
// --append-system-prompt so the fix is QA-aware even in --bare mode (where plugin
// sync and CLAUDE.md auto-discovery are skipped and the /bentoo skill may not
// resolve). It is a faithful condensation of the bentoo-dev "10 critical ebuild
// gotchas" reference — the same knowledge the /bentoo skill's ebuild-editor and
// qa-checker carry — so an edit to SRC_URI / S= / MY_P* does not silently
// introduce a QA regression. Kept terse on purpose: the agent reads the real
// ebuild for specifics.
const bentooEbuildGuidance = `You are editing a Gentoo ebuild in the Bentoo overlay. Honour these critical QA rules (the "10 gotchas"):
1. eapply_user: if you override src_prepare(), call ` + "`default`" + ` (or eapply_user explicitly) or user patches are silently dropped.
2. || die: shell commands (cp, mv, sed, rm, find, chmod, mkdir) need ` + "`|| die`" + `; EAPI 8 builtins (econf/emake/dobin) auto-die.
3. Live ebuilds (PV=9999) must have KEYWORDS="" or no KEYWORDS line.
4. S= must match the directory the tarball extracts to (e.g. S="${WORKDIR}/${MY_P}"); a mismatch fails src_configure.
5. Rename non-informative distfiles with -> (e.g. ".../${COMMIT}.tar.gz -> ${P}.tar.gz") so distfiles never collide.
6. Prebuilt binaries need QA_PREBUILT and RESTRICT="strip mirror [bindist]".
7. Copyright header is line 1; EAPI is the first non-comment, non-blank line; bump the copyright year if you touch the header.
8. This overlay uses thin-manifests: the Manifest holds only DIST entries — regenerate with ` + "`pkgdev manifest`" + `, never hand-edit.
9. In src_prepare, ` + "`default`" + ` applies the PATCHES array AND eapply_user — never loop over PATCHES manually.
10. When upstream naming/versioning differs from ${PN}/${PV}, bridge it with MY_PN/MY_P/MY_PV (e.g. MY_PV="${PV/_rc/-rc}") rather than hardcoding.
Do NOT change PN or the version (PV) encoded in the ebuild filename. Prefer the smallest edit that fixes the fetch; preserve the ebuild's existing style.`

// ManifestFixRequest carries everything the agent needs to repair a failed
// manifest step for a single package bump.
type ManifestFixRequest struct {
	// Package is the full "category/package" name (e.g. "dev-games/godot").
	Package string
	// Version is the already-normalized target PV (e.g. "4.7").
	Version string
	// PkgDir is the package directory in the overlay; it becomes the agent's cwd
	// and the sole --add-dir scope.
	PkgDir string
	// EbuildPath is the full path to the freshly-copied ebuild the agent must fix.
	EbuildPath string
	// ManifestError is the raw `pkgdev manifest` failure output (404 URL, etc.)
	// that motivates the fix.
	ManifestError string
	// DistDir is a writable temp distdir the agent can pass to
	// `pkgdev manifest --distdir` when self-verifying, so it never touches the
	// system DISTDIR.
	DistDir string
}

// ManifestFixResult reports the outcome of an agentic fix attempt. Summary is a
// short, human-readable description of what the agent changed (surfaced on the
// ApplyResult and in logs).
type ManifestFixResult struct {
	// Summary is the agent's one-line description of the change it made.
	Summary string
	// CostUSD is the reported spend for the invocation, when the CLI provides it.
	CostUSD float64
}

// ManifestFixer is the optional capability an LLM provider may implement to repair
// an ebuild whose manifest step failed. It is intentionally separate from
// LLMProvider: only an agentic, file-editing provider (the claude-code CLI) can
// satisfy it, so the Applier discovers it by holding a ManifestFixer directly
// rather than by type-asserting every LLMProvider.
type ManifestFixer interface {
	// FixManifest drives an agent to repair the ebuild named in req so that a
	// subsequent `pkgdev manifest` succeeds. It returns a summary of the change on
	// success, or an error if the agent could not be run. A nil error does NOT by
	// itself guarantee the manifest now passes — the caller re-runs manifest to
	// confirm.
	FixManifest(ctx context.Context, req ManifestFixRequest) (ManifestFixResult, error)
}

// ClaudeCodeFixer implements ManifestFixer by driving the local `claude` CLI in
// agentic mode. It reuses the same auth/model resolution as ClaudeCodeClient but
// issues a very different invocation (scoped tools, --add-dir, many turns).
type ClaudeCodeFixer struct {
	// model is the resolved model name passed via --model.
	model string
	// apiKeyEnv is the environment variable name holding the Anthropic API key.
	// Only its value (looked up at call time) is injected into the child env, and
	// only when bareMode is true.
	apiKeyEnv string
	// bareMode mirrors ClaudeCodeClient.bareMode: when true the CLI runs with
	// --bare and the API key is injected via the child environment.
	bareMode bool
	// maxBudgetUSD, when > 0, is passed as --max-budget-usd to cap spend.
	maxBudgetUSD float64
	// timeout bounds a single agentic invocation. Defaults to
	// DefaultManifestFixTimeout.
	timeout time.Duration
	// execCommand creates the *exec.Cmd bound to a context. Defaults to
	// exec.CommandContext and is injectable for testing.
	execCommand func(ctx context.Context, name string, arg ...string) *exec.Cmd
}

// Compile-time assertion that ClaudeCodeFixer satisfies the capability.
var _ ManifestFixer = (*ClaudeCodeFixer)(nil)

// ClaudeCodeFixerOption configures a ClaudeCodeFixer.
type ClaudeCodeFixerOption func(*ClaudeCodeFixer)

// WithFixerExecCommand overrides the context-aware exec.Command factory used to
// spawn `claude`. Mirrors exec.CommandContext so injected commands also observe
// context cancellation. Intended for tests (scripted seam).
func WithFixerExecCommand(fn func(ctx context.Context, name string, arg ...string) *exec.Cmd) ClaudeCodeFixerOption {
	return func(f *ClaudeCodeFixer) {
		f.execCommand = fn
	}
}

// WithFixerTimeout overrides the per-invocation timeout. A non-positive duration
// is ignored so the default (DefaultManifestFixTimeout) remains in effect.
func WithFixerTimeout(d time.Duration) ClaudeCodeFixerOption {
	return func(f *ClaudeCodeFixer) {
		if d > 0 {
			f.timeout = d
		}
	}
}

// NewClaudeCodeFixer constructs a ClaudeCodeFixer from configuration. Like
// NewClaudeCodeClient it requires the `claude` CLI on PATH (returns
// ErrClaudeCodeUnavailable otherwise) and resolves the model (defaulting to
// sonnet) and the bare/auth mode from cfg.
func NewClaudeCodeFixer(cfg LLMConfig, opts ...ClaudeCodeFixerOption) (*ClaudeCodeFixer, error) {
	if !claudeAvailable() {
		return nil, ErrClaudeCodeUnavailable
	}

	model := cfg.Model
	if model == "" {
		model = DefaultClaudeCodeModel
	}

	f := &ClaudeCodeFixer{
		model:        model,
		apiKeyEnv:    cfg.APIKeyEnv,
		bareMode:     resolveBare(cfg),
		maxBudgetUSD: cfg.MaxBudgetUSD,
		timeout:      DefaultManifestFixTimeout,
		execCommand:  exec.CommandContext,
	}

	for _, opt := range opts {
		opt(f)
	}

	return f, nil
}

// buildFixArgs assembles the agentic CLI argument vector. The instruction is the
// value of -p; the per-package facts (paths, error) travel inside the instruction
// because they are bentoo-generated text, not untrusted page content. The agent is
// scoped to pkgDir via --add-dir and constrained to manifestFixAllowedTools.
func (f *ClaudeCodeFixer) buildFixArgs(instruction, pkgDir string) []string {
	args := []string{
		"-p", instruction,
		"--output-format", "json",
		"--add-dir", pkgDir,
		"--allowedTools", strings.Join(manifestFixAllowedTools, " "),
		"--append-system-prompt", bentooEbuildGuidance,
		"--max-turns", strconv.Itoa(manifestFixMaxTurns),
		"--model", f.model,
	}
	if f.bareMode {
		args = append(args, "--bare")
	}
	if f.maxBudgetUSD > 0 {
		args = append(args, "--max-budget-usd", strconv.FormatFloat(f.maxBudgetUSD, 'f', -1, 64))
	}
	return args
}

// manifestErrorBudget bounds how much of the raw `pkgdev manifest` failure output
// is embedded into the agent's -p instruction. The output can balloon to many
// megabytes: wget's progress bar emits one dot per ~1KB of a distfile, so a
// multi-hundred-MB download dumps hundreds of thousands of progress lines. Since
// the instruction travels as a SINGLE argv element, a bloated error exceeds Linux's
// MAX_ARG_STRLEN (128 KiB per argument) and execve fails with E2BIG ("argument list
// too long") before the agent ever starts. The actionable diagnostic — the failing
// URI and the final "failed fetching" lines — lives at the head and tail, so we
// keep both ends and elide the noisy middle.
const manifestErrorBudget = 16 * 1024

// diagnosticsBudget bounds how much of any single captured diagnostic stream
// (the envelope result, the child's stderr, or — on a parse failure — its raw
// stdout) is embedded into a fixer error. Unlike the instruction path
// (manifestErrorBudget, which guards execve's MAX_ARG_STRLEN because the
// instruction travels as one argv element), these strings land in logs rather
// than argv, so E2BIG does not apply — but log bloat from a chatty agent run
// does. A few KiB per stream keeps the failure legible without flooding the log.
const diagnosticsBudget = 4 * 1024

// truncateMiddle bounds s to roughly budget bytes, preserving the head and tail
// and replacing the elided middle with a marker that names label and the elided
// byte count. It returns s unchanged when already within budget, and trims any
// partial UTF-8 runes left at the cut boundaries so the result is always valid
// UTF-8.
func truncateMiddle(s string, budget int, label string) string {
	if len(s) <= budget {
		return s
	}
	head := budget / 2
	tail := budget - head
	elided := len(s) - head - tail
	marker := fmt.Sprintf("\n...[%s truncated: %d bytes elided]...\n", label, elided)
	return strings.ToValidUTF8(s[:head], "") + marker + strings.ToValidUTF8(s[len(s)-tail:], "")
}

// truncateManifestError bounds s to roughly manifestErrorBudget bytes, preserving
// the head and tail (where the failing URL and final error lines live) and
// replacing the elided middle with a marker. It returns s unchanged when already
// within budget, and trims any partial UTF-8 runes left at the cut boundaries so
// the result is always valid UTF-8.
func truncateManifestError(s string) string {
	return truncateMiddle(s, manifestErrorBudget, "manifest output")
}

// truncateDiagnostic bounds a captured diagnostic stream to diagnosticsBudget,
// mirroring truncateManifestError's head+tail+marker discipline at the smaller
// log-oriented budget. Used for the result/stderr/stdout embedded in fixer errors.
func truncateDiagnostic(s string) string {
	return truncateMiddle(s, diagnosticsBudget, "diagnostic")
}

// buildFixInstruction renders the static-but-parameterized instruction handed to
// the agent in -p. It states the goal (make `pkgdev manifest` pass), the package
// facts, the failure output, and the guardrails (preserve PN/PV; don't invent
// URLs; verify the real upstream release path; finish with a one-line summary).
func buildFixInstruction(req ManifestFixRequest) string {
	var sb strings.Builder
	sb.WriteString("You are fixing a Gentoo ebuild whose manifest generation failed during an automated version bump. ")
	sb.WriteString("Your goal: edit the ebuild so that `pkgdev manifest --distdir ")
	sb.WriteString(req.DistDir)
	sb.WriteString("` (run from the package directory) completes WITHOUT error.\n\n")

	sb.WriteString("Package: ")
	sb.WriteString(req.Package)
	sb.WriteString("\nTarget version (PV): ")
	sb.WriteString(req.Version)
	sb.WriteString("\nEbuild to fix: ")
	sb.WriteString(req.EbuildPath)
	sb.WriteString("\n\nThe manifest step failed with:\n")
	sb.WriteString(truncateManifestError(req.ManifestError))
	sb.WriteString("\n\nGuidelines:\n")
	sb.WriteString("- The most common cause is a SRC_URI whose path/naming convention changed between upstream versions ")
	sb.WriteString("(e.g. a '-stable' suffix, a renamed release asset, or a moved download host).\n")
	sb.WriteString("- Do NOT change PN or the version (PV) in the ebuild filename.\n")
	sb.WriteString("- Do NOT invent download URLs. Determine the correct one from the upstream release page/assets ")
	sb.WriteString("(you may fetch upstream release listings to confirm the real asset name).\n")
	sb.WriteString("- Prefer minimal edits (SRC_URI and any helper variables that feed it, e.g. MY_PV/MY_P), ")
	sb.WriteString("but you may edit other parts of the ebuild if the upstream change requires it.\n")
	sb.WriteString("- If the `/bentoo` skill is available in this session, you may use it for ebuild edits and QA; ")
	sb.WriteString("otherwise edit the ebuild directly with the Read/Edit/Write tools. Either way, follow the ebuild QA rules in the system prompt.\n")
	sb.WriteString("- After editing, verify by running `pkgdev manifest --distdir ")
	sb.WriteString(req.DistDir)
	sb.WriteString("` from the package directory and iterate until it succeeds.\n")
	sb.WriteString("- When done, respond with ONLY a single short line describing what you changed (no prose, no markdown).")
	return sb.String()
}

// exitCodeString renders a process exit status for an error message. It extracts
// the numeric code from an *exec.ExitError (AD5: errors.As + ExitCode); when
// runErr is not an ExitError (e.g. the process never started), it falls back to
// the wrapped error text so the cause is never lost.
func exitCodeString(runErr error) string {
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		return strconv.Itoa(exitErr.ExitCode())
	}
	return fmt.Sprintf("%v", runErr)
}

// formatFixerError builds the single, bounded error returned by every terminal
// failure path of FixManifest. Funneling all four former fmt.Errorf sites through
// one formatter guarantees each failure carries a consistent, complete set of
// signals and removes the class of bug where one branch forgets one (the empty
// "claude fixer failed (success): " was exactly that). It reports, in precedence
// order:
//
//   - ctxErr (caller context cancelled or deadline elapsed) — AD4/R1.3, takes
//     precedence over any exit-code framing;
//   - a non-zero exit paired with a self-reported success envelope as an explicit
//     contradiction ("exited N but reported success") — AD3/R1.2, never an empty
//     tail;
//   - a generic non-zero exit (with the parsed subtype when available) — R1.1;
//   - an explicit error envelope (is_error) with its subtype — R1.5;
//   - a parse failure (non-JSON stdout) — R1.4.
//
// It then appends, each bounded by truncateDiagnostic: the envelope errors, the
// result text, the captured stderr, and — on a parse failure — the raw stdout
// (R1.4). The returned error always wraps ErrLLMRequestFailed (R3.1). The API key
// is never one of its inputs, so it can never appear in the output (R2.2).
func formatFixerError(ctxErr, runErr error, env claudeCodeEnvelope, jsonErr error, stdout, stderr string) error {
	var sb strings.Builder

	switch {
	case ctxErr != nil:
		// AD4/R1.3: cancellation or deadline takes precedence over exit framing.
		sb.WriteString(fmt.Sprintf("claude fixer aborted: %v", ctxErr))
	case runErr != nil && jsonErr == nil && !env.IsError && env.Subtype == "success":
		// AD3/R1.2: non-zero exit but a self-reported success envelope.
		sb.WriteString(fmt.Sprintf("claude fixer exited %s but reported success (subtype=%s)",
			exitCodeString(runErr), env.Subtype))
	case runErr != nil:
		// R1.1: generic non-zero exit (envelope may or may not have parsed).
		sb.WriteString(fmt.Sprintf("claude fixer failed: exit %s", exitCodeString(runErr)))
		if jsonErr == nil && env.Subtype != "" {
			sb.WriteString(fmt.Sprintf(" (subtype=%s)", env.Subtype))
		}
	case env.IsError:
		// R1.5: explicit error envelope on a zero exit.
		sb.WriteString(fmt.Sprintf("claude fixer reported error (subtype=%s)", env.Subtype))
	default:
		// R1.4: zero exit but stdout did not parse as JSON.
		sb.WriteString("claude fixer emitted non-JSON output")
	}

	if jsonErr != nil {
		sb.WriteString(fmt.Sprintf(": parse error: %v", jsonErr))
	}
	if len(env.Errors) > 0 {
		sb.WriteString("; errors: ")
		sb.WriteString(strings.Join(env.Errors, "; "))
	}
	if r := strings.TrimSpace(env.Result); r != "" {
		sb.WriteString("\nresult: ")
		sb.WriteString(truncateDiagnostic(r))
	}
	if s := strings.TrimSpace(stderr); s != "" {
		sb.WriteString("\nstderr: ")
		sb.WriteString(truncateDiagnostic(s))
	}
	// On a parse failure the raw stdout is the one artifact needed to see what the
	// CLI actually printed (R1.4).
	if jsonErr != nil && strings.TrimSpace(stdout) != "" {
		sb.WriteString("\nstdout: ")
		sb.WriteString(truncateDiagnostic(stdout))
	}

	return fmt.Errorf("%w: %s", ErrLLMRequestFailed, sb.String())
}

// FixManifest drives the agentic `claude` CLI to repair the ebuild in req. It
// builds a scoped, non-interactive invocation (cwd = req.PkgDir, --add-dir
// req.PkgDir, narrow tool allowlist), runs it under the configured timeout/budget,
// and returns the agent's one-line summary. The API key is injected only via the
// child environment in bare mode and never appears in argv or returned errors.
func (f *ClaudeCodeFixer) FixManifest(ctx context.Context, req ManifestFixRequest) (ManifestFixResult, error) {
	// Derive the per-call deadline from the caller's context so a cancelled parent
	// (SIGINT/deadline, threaded in from the Applier) kills the in-flight `claude`
	// process. Callers always supply a non-nil context (the Applier passes a.ctx).
	runCtx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()

	instruction := buildFixInstruction(req)
	args := f.buildFixArgs(instruction, req.PkgDir)

	cmd := f.execCommand(runCtx, "claude", args...)
	// cwd = the package directory so the agent's relative paths and pkgdev runs
	// resolve against the package it is repairing.
	cmd.Dir = req.PkgDir

	// Bound post-cancellation cleanup. The agent spawns its own children (pkgdev,
	// wget) that can outlive a SIGKILL of `claude` while still holding the stdout
	// pipe open, which would block cmd.Wait() far past the timeout. WaitDelay makes
	// the runtime force-close the inherited pipes (and kill the process if still
	// running) a bounded time after the context is cancelled or the process exits,
	// so FixManifest always returns within timeout + manifestFixWaitDelay.
	cmd.WaitDelay = manifestFixWaitDelay

	// Resolve the child environment from the auth mode: bare injects the API key
	// solely via env (never argv/logs); non-bare scrubs any inherited API key so
	// the CLI uses its logged-in session.
	cmd.Env = childEnv(f.bareMode, f.apiKeyEnv)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()

	var env claudeCodeEnvelope
	jsonErr := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &env)
	stderrStr := strings.TrimSpace(stderr.String())

	// Every terminal failure funnels through formatFixerError so each carries the
	// full, bounded set of signals (exit code or cancellation cause, subtype,
	// result, stderr, and raw stdout on a parse failure). The success path below
	// is unchanged (UB1). runCtx.Err() captures both a timeout (DeadlineExceeded)
	// and a parent cancellation (Canceled), since runCtx derives from ctx.
	if runErr != nil || jsonErr != nil || env.IsError {
		return ManifestFixResult{}, formatFixerError(runCtx.Err(), runErr, env, jsonErr, stdout.String(), stderrStr)
	}

	return ManifestFixResult{
		Summary: strings.TrimSpace(env.Result),
		CostUSD: env.TotalCostUSD,
	}, nil
}
