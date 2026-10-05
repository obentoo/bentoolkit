package main

// Authored for story 060, sub-task 2.2 — R7.1, R7.2.
//
// Written from the objective: every `logger.Error(...)` immediately followed by
// `return exitWith(n)` in overlay_autoupdate*.go becomes
// `return failWith(n, fmt.Errorf(...))` with the same text, so the handler
// RETURNS its one diagnostic and execute prints it once.
//
// The six flag checks of `overlay autoupdate` are the rows: they fail before
// any config, lock or network is touched, so each run is hermetic. For every
// row, three things are asserted:
//
//  1. through testCLI (runMain -> execute), stderr is EXACTLY the line the
//     command printed at 1462803, once, with the same exit status (R7.1);
//  2. with file logging enabled, the log file carries that line exactly once
//     as an ERROR entry, as before (R7.2);
//  3. the handler itself prints nothing: the tree executed WITHOUT execute
//     leaves stderr empty and returns a bare exit status whose Unwrap is the
//     diagnostic, byte for byte. This is the part that is Red today — at
//     1462803 the handler prints the line itself and returns a status with no
//     cause — and it is what makes 1 and 2 mean "execute is the one printer"
//     rather than "the handler still prints".
//
// Parts 1 and 2 pass today by design (the output must not change); part 3
// carries the Red.

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// s060FailWithSites are the single-line flag-check failures of `overlay
// autoupdate`, with the exact line each printed at 1462803.
var s060FailWithSites = []struct {
	name string
	args []string
	line string
	code int
}{
	{
		name: "concurrency out of range",
		args: []string{"overlay", "autoupdate", "--check", "--concurrency", "0"},
		line: "--concurrency must be in range [1, 100], got 0",
		code: 1,
	},
	{
		name: "negative timeout",
		args: []string{"overlay", "autoupdate", "--check", "--timeout", "-1"},
		line: "--timeout must be >= 0 seconds, got -1",
		code: 1,
	},
	{
		name: "unknown --only",
		args: []string{"overlay", "autoupdate", "--check", "--only", "both"},
		line: `--only must be "bin" or "source", got "both"`,
		code: 1,
	},
	{
		name: "--fix without --lint",
		args: []string{"overlay", "autoupdate", "--fix"},
		line: "--fix repairs what --lint reports, so it is valid only together with it — run: bentoo overlay autoupdate --lint --fix",
		code: 1,
	},
	{
		name: "--except without --mark-auto-disabled",
		args: []string{"overlay", "autoupdate", "--except", "dev-libs/icu-compat"},
		line: "--except names the entries --mark-auto-disabled must not stamp, so it is valid only together with it — run: bentoo overlay autoupdate --mark-auto-disabled --except <atom>[,<atom>...]",
		code: 1,
	},
	{
		name: "--mark-auto-disabled with --lint",
		args: []string{"overlay", "autoupdate", "--mark-auto-disabled", "--lint"},
		line: "--mark-auto-disabled and --lint are separate modes and only one runs per invocation — run them one after the other",
		code: 1,
	},
}

// TestS060FailWithSiteStderrIsUnchanged is R7.1 end to end: one line, the same
// bytes, the same status.
func TestS060FailWithSiteStderrIsUnchanged(t *testing.T) {
	for _, tt := range s060FailWithSites {
		t.Run(tt.name, func(t *testing.T) {
			stdout, stderr, code := newTestCLI(t).Run(tt.args...)

			if code != tt.code {
				t.Errorf("exit status = %d, want %d (R7.1, U2)", code, tt.code)
			}
			if want := s060CauseRecord(tt.line); stderr != want {
				t.Errorf("stderr = %q\nwant     %q — the line, byte-identical, exactly once (R7.1)", stderr, want)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want nothing", stdout)
			}
		})
	}
}

// TestS060FailWithSiteLogFileIsUnchanged is R7.2: with file logging on, the
// log file gets the same single ERROR entry it got before.
func TestS060FailWithSiteLogFileIsUnchanged(t *testing.T) {
	for _, tt := range s060FailWithSites {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestCLI(t)
			t.Setenv("XDG_STATE_HOME", filepath.Join(c.Home(), ".local", "state"))

			// The invocation logger writes its file unbuffered, and execute
			// closes it once the cause is logged (story 062).
			c.Run(tt.args...)

			data, err := os.ReadFile(filepath.Join(c.Home(), ".local", "state", "bentoo", "logs", "bentoo.log"))
			if err != nil {
				t.Fatalf("read log file: %v", err)
			}
			n := 0
			for _, l := range strings.Split(string(data), "\n") {
				var rec struct{ Level, Msg, Err string }
				if json.Unmarshal([]byte(l), &rec) == nil && rec.Level == "ERROR" && rec.Msg == "command failed" && rec.Err == tt.line {
					n++
				}
			}
			if n != 1 {
				t.Errorf("the log file holds %d ERROR entr(ies) for %q, want exactly 1 (R7.2)\nlog:\n%s", n, tt.line, data)
			}
		})
	}
}

// TestS060FailWithSiteHandlerReturnsItsDiagnostic is the conversion itself:
// executed without execute, the handler prints nothing and hands back a bare
// exit status whose cause is the diagnostic.
func TestS060FailWithSiteHandlerReturnsItsDiagnostic(t *testing.T) {
	for _, tt := range s060FailWithSites {
		t.Run(tt.name, func(t *testing.T) {
			newTestCLI(t) // the same isolated HOME, config and render opt-outs

			root := newRootCmd()
			root.SetArgs(tt.args)

			ctx, stop, policy := processContext()
			defer stop()
			readOut := captureStream(t, 1, &os.Stdout)
			readErr := captureStream(t, 2, &os.Stderr)
			err := root.ExecuteContext(withSignalPolicy(ctx, policy))
			handlerErr, handlerOut := readErr(), readOut()

			if handlerErr != "" || handlerOut != "" {
				t.Errorf("the handler printed its own diagnostic (stderr %q, stdout %q); it must return it for execute to print once", handlerErr, handlerOut)
			}
			if _, bare := err.(*exitStatus); !bare { //nolint:errorlint // execute decides on the identity of the returned value
				t.Fatalf("handler returned %T (%v), want a bare *exitStatus", err, err)
			}
			if got := exitCodeFor(err); got != tt.code {
				t.Errorf("exit code = %d, want %d", got, tt.code)
			}
			cause := errors.Unwrap(err)
			if cause == nil {
				t.Fatalf("the returned status carries no cause; the diagnostic %q was not returned", tt.line)
			}
			if cause.Error() != tt.line {
				t.Errorf("cause = %q\nwant    %q (byte-identical text)", cause.Error(), tt.line)
			}
		})
	}
}

// s060CauseRecord is the stderr record the invocation logger writes for a
// failWith cause since story 062: one key=value line carrying the cause, byte
// for byte, as its err attribute. It is rendered by the same TextHandler setup
// logging.New uses, so the quoting is slog's own.
func s060CauseRecord(cause string) string {
	var b bytes.Buffer
	h := slog.NewTextHandler(&b, &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	})
	slog.New(h).Error("command failed", "err", errors.New(cause))
	return b.String()
}
