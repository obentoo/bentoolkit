package fixer

import (
	"context"
	"errors"
	"testing"

	"github.com/obentoo/bentoolkit/internal/autoupdate/llm"
)

// fixerWordingPins are the sentences formatFixerError produces TODAY, captured
// from the code as it stands on 2026-09-06 and asserted byte for byte.
//
// WHY BYTE-IDENTICAL AND WHY HERE. Sub-task 1.1 moves the ORDER out of
// formatFixerError and leaves its WORDING alone. Four spawn sites funnel through
// this one formatter — manifest_fixer.go:579, build_fixer.go:555,
// bump_reviewer.go:404 and registry_fixer.go:374 — so the formatter's branches
// are where all four callers' messages are decided, and pinning the branches
// pins the four. S048-R4.2 asks that the other four claude invocation sites keep
// their own behaviour; this is the assertion that says so and can fail.
//
// THE LAST TWO PINS ARE HOSTILE, NOT DECORATIVE. isError and nonJSON are the two
// branches where ctxErr AND runErr are both nil — a THIRD input shape reaching a
// classifier built for a pair. If the new outcome type's zero value happens to
// be the exited-non-zero outcome, a rewritten formatFixerError that consults the
// classifier first will route these two through the exit-code branch and render
// "exit " plus whatever exitCodeString makes of a nil error. Neither of the
// classifier tests above can see that: they only ever pass a non-nil failure.
var fixerWordingPins = []struct {
	name string
	got  func(t *testing.T) error
	want string
}{
	{
		name: "an elapsed deadline",
		got: func(t *testing.T) error {
			return formatFixerError(context.DeadlineExceeded, exitErrWithCode(t, 1),
				llm.ClaudeCodeEnvelope{Subtype: "success"}, nil, "", "")
		},
		want: "LLM API request failed: claude fixer aborted: context deadline exceeded",
	},
	{
		name: "a cancelled parent",
		got: func(t *testing.T) error {
			return formatFixerError(context.Canceled, exitErrWithCode(t, 1),
				llm.ClaudeCodeEnvelope{}, nil, "", "")
		},
		want: "LLM API request failed: claude fixer aborted: context canceled",
	},
	{
		name: "a process that could not start",
		got: func(t *testing.T) error {
			return formatFixerError(nil, errors.New("fork/exec /nonexistent/claude: no such file or directory"),
				llm.ClaudeCodeEnvelope{}, errors.New("unexpected end of JSON input"), "", "")
		},
		want: "LLM API request failed: claude fixer could not start: fork/exec /nonexistent/claude: no such file or directory: parse error: unexpected end of JSON input",
	},
	{
		name: "a non-zero exit contradicted by a success envelope",
		got: func(t *testing.T) error {
			return formatFixerError(nil, exitErrWithCode(t, 1),
				llm.ClaudeCodeEnvelope{Subtype: "success", Result: "renamed asset"}, nil, "", "")
		},
		want: "LLM API request failed: claude fixer exited 1 but reported success (subtype=success)\nresult: renamed asset",
	},
	{
		name: "a generic non-zero exit with stderr",
		got: func(t *testing.T) error {
			return formatFixerError(nil, exitErrWithCode(t, 2),
				llm.ClaudeCodeEnvelope{Subtype: "error_during_execution"}, nil, "", "boom")
		},
		want: "LLM API request failed: claude fixer failed: exit 2 (subtype=error_during_execution)\nstderr: boom",
	},
	{
		name: "an error envelope on a zero exit",
		got: func(t *testing.T) error {
			return formatFixerError(nil, nil,
				llm.ClaudeCodeEnvelope{Subtype: "error_max_turns", IsError: true, Errors: []string{"ran out of turns"}}, nil, "", "")
		},
		want: "LLM API request failed: claude fixer reported error (subtype=error_max_turns); errors: ran out of turns",
	},
	{
		name: "a zero exit whose stdout did not parse",
		got: func(t *testing.T) error {
			return formatFixerError(nil, nil, llm.ClaudeCodeEnvelope{},
				errors.New("unexpected end of JSON input"), "xyz", "")
		},
		want: "LLM API request failed: claude fixer emitted non-JSON output: parse error: unexpected end of JSON input\nstdout: xyz",
	},
}

// TestFormatFixerError_WordingSurvivesTheLift asserts the pins above.
func TestFormatFixerError_WordingSurvivesTheLift(t *testing.T) {
	for _, pin := range fixerWordingPins {
		t.Run(pin.name, func(t *testing.T) {
			err := pin.got(t)
			if err == nil {
				t.Fatalf("formatFixerError returned nil for %s; every terminal failure path must produce an error", pin.name)
			}
			if got := err.Error(); got != pin.want {
				t.Errorf("the fixer's message for %s changed.\n got: %q\nwant: %q\n"+
					"1.1 moves the precedence, not the wording: four spawn sites read this formatter and their "+
					"messages are the operator-facing contract those stories were validated against (S048-R4.2)",
					pin.name, got, pin.want)
			}
			if !errors.Is(err, llm.ErrLLMRequestFailed) {
				t.Errorf("the fixer's error for %s stopped wrapping ErrLLMRequestFailed; callers select on it "+
					"with errors.Is (S009-R3.1)", pin.name)
			}
		})
	}
}
