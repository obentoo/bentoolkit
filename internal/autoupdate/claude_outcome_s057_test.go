package autoupdate

// Authored for story 057, sub-task 2.1 — R2.9, R6.6.
//
// Every failure run returns must match exactly ONE exported outcome sentinel,
// keep matching ErrLLMRequestFailed, and keep its sentence byte for byte.
// Contract names chosen here (story.md names none): ErrClaudeTimedOut,
// ErrClaudeStopped, ErrClaudeCouldNotStart, ErrClaudeExitedNonZero,
// ErrClaudeUnusableOutput. A rename is mechanical: edit s057Outcomes only.
//
// RED ON ARRIVAL: none of the five sentinels exist.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

var s057Outcomes = map[string]error{
	"timed out":       ErrClaudeTimedOut,
	"stopped":         ErrClaudeStopped,
	"could not start": ErrClaudeCouldNotStart,
	"exited non-zero": ErrClaudeExitedNonZero,
	"unusable output": ErrClaudeUnusableOutput,
}

func TestRunFailuresCarryTheirOutcome(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	exitSeam, _ := scriptedSeam(`exit 3`)
	junkSeam, _ := scriptedSeam(`printf 'not json'`)

	cases := []struct {
		name     string
		ctx      context.Context // the call's context; nil means t.Context()
		opts     []ClaudeCodeOption
		want     string
		wantText string // byte-exact where an existing test already pins it
		contains string
	}{
		// Hostile: the pairs a classifier most easily merges.
		{name: "a deadline that elapses before Start is timed out, not could-not-start",
			opts: []ClaudeCodeOption{WithClaudeCodeExecCommand(blockingSeam()), WithClaudeCodeTimeout(time.Nanosecond)},
			want: "timed out", contains: "budget elapsed"},
		{name: "a cancelled parent is stopped, not timed out",
			ctx:  cancelled,
			opts: []ClaudeCodeOption{WithClaudeCodeExecCommand(blockingSeam()), WithClaudeCodeTimeout(30 * time.Second)},
			want: "stopped", contains: "was stopped before it answered"},
		{name: "its own budget elapsed",
			opts: []ClaudeCodeOption{WithClaudeCodeExecCommand(blockingSeam()), WithClaudeCodeTimeout(250 * time.Millisecond)},
			want: "timed out", wantText: "LLM API request failed: claude CLI ran out of time: its 250ms budget elapsed before it answered"},
		{name: "a binary that is not there",
			opts: []ClaudeCodeOption{WithClaudeCodeExecCommand(unstartableSeam()), WithClaudeCodeTimeout(30 * time.Second)},
			want: "could not start", contains: "could not start"},
		{name: "a non-zero exit",
			opts: []ClaudeCodeOption{WithClaudeCodeExecCommand(exitSeam), WithClaudeCodeTimeout(30 * time.Second)},
			want: "exited non-zero", wantText: "LLM API request failed: claude CLI failed: exit status 3"},
		{name: "exit zero with non-JSON output",
			opts: []ClaudeCodeOption{WithClaudeCodeExecCommand(junkSeam), WithClaudeCodeTimeout(30 * time.Second)},
			want: "unusable output", contains: "emitted non-JSON output"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestClient(t, LLMConfig{}, tc.opts...)
			ctx := tc.ctx
			if ctx == nil {
				ctx = t.Context()
			}
			_, err := c.run(ctx, "instr", []byte("content"), "")
			if err == nil {
				t.Fatal("the invocation returned no error")
			}
			if !errors.Is(err, ErrLLMRequestFailed) {
				t.Errorf("%q no longer matches ErrLLMRequestFailed", err)
			}
			for name, sentinel := range s057Outcomes {
				if got := errors.Is(err, sentinel); got != (name == tc.want) {
					t.Errorf("errors.Is(%q, <%s>) = %v; want exactly one outcome, %q (R2.9)", err, name, got, tc.want)
				}
			}
			if tc.wantText != "" && err.Error() != tc.wantText {
				t.Errorf("the sentence changed\n got: %q\nwant: %q (R6.6)", err.Error(), tc.wantText)
			}
			if tc.contains != "" && !strings.Contains(err.Error(), tc.contains) {
				t.Errorf("the sentence %q lost %q (R6.6)", err.Error(), tc.contains)
			}
		})
	}
}
