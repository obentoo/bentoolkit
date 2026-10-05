package main

// Authored for story 058, sub-task 1.1 — R2.1, R2.2, R2.3, R2.4, R3.1, R3.2, R4.5.
//
// Written from the exit contract in design.md (exitStatus, exitWith,
// exitCodeFor, execute) and its printing rule, never from an implementation.
// failWith is not part of this story (design.md: story 060 introduces it), so
// nothing here names it.
// The file also carries the helpers every later S058 file shares (s058Env,
// s058Execute, s058RunRows, s058NotOnRunE), which is why it is materialized
// first.
//
// Red on arrival: exitWith, exitCodeFor and execute do not exist.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
)

// ---------------------------------------------------------------------------
// Shared helpers for every S058 file.
// ---------------------------------------------------------------------------

// s058Env prepares an isolated run: newTestCLI's temporary HOME, config and
// overlay, plus the XDG state, cache and data homes under that same temporary
// HOME, so nothing a command writes (logs, caches) reaches the real home.
//
// It also keeps the developer's machine out of the run. GITHUB_TOKEN and
// GH_TOKEN are blanked so no row authenticates with a real credential from the
// environment; the user-scope secrets file already sits under the temporary
// HOME. An empty value only reads as "unset" to the secrets chain, so the
// root-owned /etc/bentoo/secrets is still consulted on a host where it is
// readable — no S058 row depends on a token, but only a masked /etc/bentoo
// removes that read. GIT_CONFIG_NOSYSTEM=1 stops git from reading /etc/gitconfig;
// the global config already resolves under the temporary HOME.
func s058Env(t *testing.T) *testCLI {
	t.Helper()
	c := newTestCLI(t)
	t.Setenv("XDG_STATE_HOME", filepath.Join(c.Home(), ".local", "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(c.Home(), ".cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(c.Home(), ".local", "share"))
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	return c
}

// s058Execute runs one invocation of a fresh tree, built from d, through
// execute, the production mapping and printer, and returns both streams and the
// exit code.
// Nothing intercepts an exit here: the code has to come back as a returned
// error, which is the whole point of the story.
func s058Execute(t *testing.T, ctx context.Context, d *deps, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	// The invocation logger (story 062) reads os.Stderr when the root's pre-run
	// builds it, so it writes into the redirected descriptor below.
	readOut := captureStream(t, 1, &os.Stdout)
	readErr := captureStream(t, 2, &os.Stderr)
	origColorOut, origNoColor := color.Output, color.NoColor
	color.Output, color.NoColor = os.Stdout, true

	restored := false
	restore := func() {
		if restored {
			return
		}
		restored = true
		color.Output, color.NoColor = origColorOut, origNoColor
		stdout, stderr = readOut(), readErr()
	}
	defer restore()

	// newRootCmdWith only builds the tree; ctx reaches the run through execute
	// below. contextcheck follows the RunE closure into runAutoupdate and
	// cannot see that.
	root := newRootCmdWith(d) //nolint:contextcheck // ctx is passed by execute, not at construction
	root.SetArgs(args)
	code = execute(ctx, root, os.Stderr)
	restore()
	return stdout, stderr, code
}

// s058RequireRunE stops the test before executing a command that is not a RunE
// command yet: such a command still ends the process through osExit, which
// would take the test binary down with it instead of failing one test.
func s058RequireRunE(t *testing.T, args []string) {
	t.Helper()
	cmd, _, err := newRootCmd().Find(args)
	if err != nil || cmd == nil || !cmd.Runnable() {
		return
	}
	if cmd.RunE == nil || cmd.Run != nil {
		t.Fatalf("%q is not a RunE command yet (Run set: %t, RunE set: %t): its exit code cannot travel back as a returned error, and executing it would end the test binary",
			cmd.CommandPath(), cmd.Run != nil, cmd.RunE != nil)
	}
}

// s058NotOnRunE returns every named command (a path below the root, such as
// "overlay add") that has a Run handler or lacks a RunE handler.
func s058NotOnRunE(t *testing.T, paths ...string) []string {
	t.Helper()
	root := newRootCmd()
	var bad []string
	for _, p := range paths {
		cmd, _, err := root.Find(strings.Fields(p))
		if err != nil || cmd == nil || cmd.CommandPath() != "bentoo "+p {
			t.Fatalf("the tree built by newRootCmd has no command %q (found %v, err %v)", p, cmd, err)
		}
		if cmd.RunE == nil || cmd.Run != nil {
			bad = append(bad, fmt.Sprintf("%s (Run set: %t, RunE set: %t)", p, cmd.Run != nil, cmd.RunE != nil))
		}
	}
	return bad
}

// s058Row is one row of the design.md exit-code table, driven end to end.
type s058Row struct {
	name string
	args []string
	// setup arranges the fixture and may return extra arguments.
	setup func(t *testing.T, c *testCLI) []string
	// ctx supplies the caller's context; nil means context.Background().
	ctx  func(t *testing.T) context.Context
	want int
	// once is a diagnostic that must appear exactly once across both streams.
	once string
	// usage marks a row that legitimately prints a usage or help block.
	usage bool
	// after runs extra checks once the command has returned.
	after func(t *testing.T, c *testCLI, stdout, stderr string)
}

// s058BareStatus matches the text of a silent exit status printed on its own,
// which R3.2 forbids: its diagnostic was already printed by the command.
var s058BareStatus = regexp.MustCompile(`(?m)^exit status -?[0-9]+$`)

// s058RunRows drives each row through execute on a fresh tree and checks the
// code, the printed-once diagnostic, the absence of a bare status line and the
// absence of an unexpected usage block.
func s058RunRows(t *testing.T, rows []s058Row) {
	t.Helper()
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			c := s058Env(t)
			args := append([]string(nil), row.args...)
			if row.setup != nil {
				args = append(args, row.setup(t, c)...)
			}
			s058RequireRunE(t, args)
			ctx := context.Background()
			if row.ctx != nil {
				ctx = row.ctx(t)
			}
			stdout, stderr, code := s058Execute(t, ctx, c.deps, args...)
			s058CheckRow(t, row, args, stdout, stderr, code)
			if row.after != nil {
				row.after(t, c, stdout, stderr)
			}
		})
	}
}

func s058CheckRow(t *testing.T, row s058Row, args []string, stdout, stderr string, code int) {
	t.Helper()
	if code != row.want {
		t.Errorf("bentoo %s exited %d, want %d (the code measured at 6be73ec)\n--- stdout ---\n%s\n--- stderr ---\n%s",
			strings.Join(args, " "), code, row.want, stdout, stderr)
	}
	if row.once != "" {
		if n := strings.Count(stdout+stderr, row.once); n != 1 {
			t.Errorf("%q was printed %d time(s), want exactly once (R3.1, R3.2)\n--- stdout ---\n%s\n--- stderr ---\n%s",
				row.once, n, stdout, stderr)
		}
	}
	if m := s058BareStatus.FindString(stderr); m != "" {
		t.Errorf("stderr carries the bare line %q: a status whose diagnostic the command already printed was printed again (R3.2)\n%s", m, stderr)
	}
	if !row.usage {
		for stream, text := range map[string]string{"stdout": stdout, "stderr": stderr} {
			if strings.Contains(text, "Usage:\n") {
				t.Errorf("%s carries a usage block after a handler failure — the output of a failing command changed:\n%s", stream, text)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Fixtures for the unit tests below.
// ---------------------------------------------------------------------------

// s058CodedError carries an exit-code-shaped value without being an exit
// status: exitCodeFor must not take its number.
type s058CodedError struct{ code int }

func (e s058CodedError) Error() string { return fmt.Sprintf("tool failed with code %d", e.code) }
func (e s058CodedError) ExitCode() int { return e.code }

// s058ChildExitError returns a real *exec.ExitError whose ExitCode is 3.
func s058ChildExitError(t *testing.T) error {
	t.Helper()
	err := exec.Command("sh", "-c", "exit 3").Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 3 {
		t.Fatalf("could not produce a child exit error with code 3: %v", err)
	}
	return err
}

// s058SmallTree builds a root with SilenceErrors (as newRootCmd has) and one
// sub-command "work" running run; cobra's own output goes to the returned buffer.
func s058SmallTree(run func(cmd *cobra.Command, args []string) error) (*cobra.Command, *bytes.Buffer) {
	var cobraOut bytes.Buffer
	root := &cobra.Command{Use: "bentoo", SilenceErrors: true, SilenceUsage: true}
	root.SetOut(&cobraOut)
	root.SetErr(&cobraOut)
	root.AddCommand(&cobra.Command{Use: "work", RunE: run})
	root.SetArgs([]string{"work"})
	return root, &cobraOut
}

type s058CtxKey struct{}

// ---------------------------------------------------------------------------
// Tests.
// ---------------------------------------------------------------------------

// TestS058ExitCodeFor pins the mapping (R2.1, R2.2, R2.3). The hostile rows
// come first: errors that carry a number but no exit status must stay 1, and a
// code must not collapse into 1 on its way up through wrappers.
func TestS058ExitCodeFor(t *testing.T) {
	cause := errors.New("git command failed")
	childExit := s058ChildExitError(t)

	cases := []struct {
		name string
		err  error
		want int
	}{
		// Would wrongly take a code: none of these is an exit status.
		{"a child process exit error carries no exit status", childExit, 1},
		{"a wrapped child process exit error carries no exit status", fmt.Errorf("running git log: %w", childExit), 1},
		{"a value with an ExitCode method is not an exit status", s058CodedError{code: 2}, 1},
		{"text that reads like a status carries no code", errors.New("exit status 2"), 1},
		// Would wrongly collapse into 1: the code survives wrapping.
		{"exitWith(2) wrapped once", fmt.Errorf("apply app-misc/foo: %w", exitWith(2)), 2},
		{"exitWith(130) wrapped twice", fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", exitWith(130))), 130},
		{"exitWith(2) joined with a plain error", errors.Join(cause, exitWith(2)), 2},
		// Benign.
		{"nil", nil, 0},
		{"exitWith(1)", exitWith(1), 1},
		{"exitWith(2)", exitWith(2), 2},
		{"exitWith(130)", exitWith(130), 130},
		{"a plain error", cause, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := exitCodeFor(tc.err); got != tc.want {
				t.Errorf("exitCodeFor(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

// TestS058ExitWithZeroIsNil: a success path stays a nil error, so no caller can
// wrap it into a failure; any other code is a non-nil silent status.
func TestS058ExitWithZeroIsNil(t *testing.T) {
	if err := exitWith(0); err != nil {
		t.Fatalf("exitWith(0) = %#v, want nil: a caller testing err != nil would treat success as failure", err)
	}
	for _, code := range []int{1, 2, 130} {
		err := exitWith(code)
		if err == nil {
			t.Errorf("exitWith(%d) = nil: the code is lost and the process exits 0", code)
			continue
		}
		if got, want := err.Error(), fmt.Sprintf("exit status %d", code); got != want {
			t.Errorf("exitWith(%d).Error() = %q, want %q", code, got, want)
		}
		if inner := errors.Unwrap(err); inner != nil {
			t.Errorf("exitWith(%d) unwraps to %v, want nil: a silent status carries no cause", code, inner)
		}
		var st *exitStatus
		if !errors.As(err, &st) || st.code != code {
			t.Errorf("exitWith(%d) is not an *exitStatus carrying %d: %#v", code, code, err)
		}
	}
}

// TestS058ExecutePrintsEachDiagnosticOnce pins the printing rule (R3.1, R3.2).
// The hostile rows come first: a silent status wrapped by a caller, or joined
// with a plain error, must NOT be suppressed as silent — the words around it
// were never printed.
func TestS058ExecutePrintsEachDiagnosticOnce(t *testing.T) {
	boom := errors.New("the registry was NOT updated")
	cases := []struct {
		name     string
		returned error
		wantErr  string
		wantCode int
	}{
		{"a silent status wrapped by a caller prints the wrapper once", fmt.Errorf("apply app-misc/foo: %w", exitWith(2)), "apply app-misc/foo: exit status 2\n", 2},
		{"a silent status wrapped twice prints the outer wrapper once", fmt.Errorf("check: %w", fmt.Errorf("apply: %w", exitWith(2))), "check: apply: exit status 2\n", 2},
		{"a silent status joined with a plain error prints the join once", errors.Join(boom, exitWith(2)), "the registry was NOT updated\nexit status 2\n", 2},
		{"a bare silent status prints nothing", exitWith(2), "", 2},
		{"a bare silent 130 prints nothing", exitWith(130), "", 130},
		{"nil prints nothing", nil, "", 0},
		{"a plain error prints once", boom, "the registry was NOT updated\n", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, cobraOut := s058SmallTree(func(*cobra.Command, []string) error { return tc.returned })
			var diag bytes.Buffer
			code := execute(context.Background(), root, &diag)
			if code != tc.wantCode {
				t.Errorf("execute returned %d, want %d", code, tc.wantCode)
			}
			if diag.String() != tc.wantErr {
				t.Errorf("execute printed %q to stderr, want %q", diag.String(), tc.wantErr)
			}
			if msg := strings.TrimSpace(tc.wantErr); msg != "" && strings.Contains(cobraOut.String(), msg) {
				t.Errorf("cobra printed the diagnostic too — it would reach the operator twice:\n%s", cobraOut.String())
			}
		})
	}
}

// TestS058ExecuteGivesTheCallersContextToTheHandler (R4.5), and a cancelled
// caller context leaves the handler's own code intact: there is no global
// "cancelled means 130" mapping (R4.3).
func TestS058ExecuteGivesTheCallersContextToTheHandler(t *testing.T) {
	ctx := context.WithValue(context.Background(), s058CtxKey{}, "caller")
	var seen any
	root, _ := s058SmallTree(func(cmd *cobra.Command, _ []string) error {
		seen = cmd.Context().Value(s058CtxKey{})
		return nil
	})
	if code := execute(ctx, root, &bytes.Buffer{}); code != 0 {
		t.Fatalf("execute returned %d, want 0", code)
	}
	if seen != "caller" {
		t.Errorf("the handler's cmd.Context() carried %v, want the caller's value %q", seen, "caller")
	}

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	var cause error
	root, _ = s058SmallTree(func(cmd *cobra.Command, _ []string) error {
		cause = cmd.Context().Err()
		return exitWith(5)
	})
	if code := execute(cancelled, root, &bytes.Buffer{}); code != 5 {
		t.Errorf("a handler that returned exitWith(5) under a cancelled context exited %d, want 5", code)
	}
	if !errors.Is(cause, context.Canceled) {
		t.Errorf("the handler saw ctx.Err() = %v, want %v: the caller's cancellation did not reach it", cause, context.Canceled)
	}
}

// TestS058ExecuteGivesSubCommandsTheCallersContext: a nested command and the
// root's PersistentPreRunE see the caller's context too (R4.5).
func TestS058ExecuteGivesSubCommandsTheCallersContext(t *testing.T) {
	ctx := context.WithValue(context.Background(), s058CtxKey{}, "caller")
	var inPreRun, inLeaf any
	root := &cobra.Command{
		Use:           "bentoo",
		SilenceErrors: true,
		SilenceUsage:  true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			inPreRun = cmd.Context().Value(s058CtxKey{})
			return nil
		},
	}
	group := &cobra.Command{Use: "overlay"}
	leaf := &cobra.Command{Use: "work", RunE: func(cmd *cobra.Command, _ []string) error {
		inLeaf = cmd.Context().Value(s058CtxKey{})
		return exitWith(2)
	}}
	group.AddCommand(leaf)
	root.AddCommand(group)
	root.SetArgs([]string{"overlay", "work"})

	if code := execute(ctx, root, &bytes.Buffer{}); code != 2 {
		t.Errorf("execute returned %d, want 2", code)
	}
	if inPreRun != "caller" {
		t.Errorf("PersistentPreRunE saw %v, want the caller's value", inPreRun)
	}
	if inLeaf != "caller" {
		t.Errorf("the nested handler saw %v, want the caller's value", inLeaf)
	}
}

// TestS058ExecuteOnTheRealTreeRejectsUsageOnce: cobra's rejections through the
// tree built by newRootCmd exit 1, state the rejection once on stderr, and run
// nothing (R2.4, R3.1).
func TestS058ExecuteOnTheRealTreeRejectsUsageOnce(t *testing.T) {
	cases := []struct {
		name string
		args []string
		msg  string
	}{
		{"unknown command", []string{"nosuchcmd"}, `unknown command "nosuchcmd"`},
		{"unknown flag", []string{"version", "--not-a-flag"}, "unknown flag: --not-a-flag"},
		{"wrong argument count", []string{"overlay", "rename", "a", "b"}, "accepts 3 arg(s)"},
		{"unusable --ui", []string{"--ui=bogus", "version"}, `"bogus"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := s058Env(t)
			stdout, stderr, code := s058Execute(t, context.Background(), c.deps, tc.args...)
			if code != 1 {
				t.Errorf("exit %d, want 1\nstderr:\n%s", code, stderr)
			}
			if n := strings.Count(stderr, tc.msg); n != 1 {
				t.Errorf("stderr carries %q %d time(s), want once:\n%s", tc.msg, n, stderr)
			}
			if strings.Contains(stdout, tc.msg) {
				t.Errorf("the rejection reached stdout:\n%s", stdout)
			}
			if strings.Contains(stdout, "bentoo version") {
				t.Errorf("the command ran after its invocation was rejected:\n%s", stdout)
			}
		})
	}
}
