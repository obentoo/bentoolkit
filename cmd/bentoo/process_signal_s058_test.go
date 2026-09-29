//go:build unix

package main

// Authored for story 058, sub-task 1.2 — R1.2, R2.4, R4.1, R4.2, R4.3, R4.6.
//
// Written from design.md (processContext and its signal policy, commandContext,
// runMain, the single exit in main, SilenceUsage in the root's
// PersistentPreRunE) and its Testing Strategy: every exit-path claim is proved in a
// re-exec'd child that ends through exitProcess(runMain(root)), never through
// an intercept that turns an exit into a panic (a panic runs the deferred calls
// the real exit skips).
//
// The child's tree is the real root built by newRootCmd — whose
// PersistentPreRunE sets the policy from the selected command — with one
// synthetic sub-command "work" added. "work" carries
// Annotations["bentoo/cancellable"] = "true" in the roles that need a
// cancellable command, and nothing in the roles that need one that is not.
//
// Red on arrival: runMain, exitWith and commandContext do not exist.

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

const (
	s058SignalRoleEnv = "BENTOO_TEST_S058_SIGNAL_ROLE"
	s058ChildDirEnv   = "BENTOO_TEST_S058_CHILD_DIR"
	// s058ChildReturned is the child's own code when exitProcess returned.
	s058ChildReturned = 97
	// s058CodeCancelled is what the child's handler returns once its context
	// is cancelled; s058CodeNotCancelled what it returns when it never was.
	s058CodeCancelled    = 7
	s058CodeNotCancelled = 8
)

// TestS058HelperSignalChild is not a test: in the child role it builds a small
// tree and ends through the production exit path.
func TestS058HelperSignalChild(t *testing.T) {
	role := os.Getenv(s058SignalRoleEnv)
	if role == "" {
		t.Skip("child role only")
	}
	dir := os.Getenv(s058ChildDirEnv)
	mark := func(name, body string) {
		_ = os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600)
	}

	root := newRootCmd()
	work := &cobra.Command{Use: "work"}
	root.AddCommand(work)
	cancellable := func() { work.Annotations = map[string]string{s058CancellableAnnotation: "true"} }
	args := []string{"work"}

	switch role {
	case "defer-silent":
		work.RunE = func(*cobra.Command, []string) error {
			defer mark("deferred", "ran")
			return exitWith(3)
		}
	case "defer-plain":
		work.RunE = func(*cobra.Command, []string) error {
			defer mark("deferred", "ran")
			return errors.New("s058 plain failure")
		}
	case "defer-success":
		work.RunE = func(*cobra.Command, []string) error {
			defer mark("deferred", "ran")
			return nil
		}
	case "first-signal":
		cancellable()
		work.RunE = func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			mark("ready", "")
			select {
			case <-ctx.Done():
				mark("cancelled", ctx.Err().Error())
				return exitWith(s058CodeCancelled)
			case <-time.After(20 * time.Second):
				return exitWith(s058CodeNotCancelled)
			}
		}
	case "non-cancellable":
		// Same body as first-signal, no annotation: the first signal must
		// take its default action instead of cancelling the context.
		work.RunE = func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			mark("ready", "")
			select {
			case <-ctx.Done():
				mark("cancelled", ctx.Err().Error())
				return exitWith(s058CodeCancelled)
			case <-time.After(20 * time.Second):
				return exitWith(s058CodeNotCancelled)
			}
		}
	case "before-selection":
		// The command IS cancellable, but the signal lands while cobra still
		// parses its flags, before PersistentPreRunE has marked the policy.
		cancellable()
		work.Flags().Var(&s058BlockingValue{mark: func() { mark("ready", "") }}, "s058-block", "blocks while parsed")
		args = append(args, "--s058-block=x")
		work.RunE = func(cmd *cobra.Command, _ []string) error {
			if cmd.Context().Err() != nil {
				return exitWith(s058CodeCancelled)
			}
			return exitWith(s058CodeNotCancelled)
		}
	case "second-signal":
		cancellable()
		work.RunE = func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			mark("ready", "")
			select {
			case <-ctx.Done():
			case <-time.After(20 * time.Second):
				return exitWith(s058CodeNotCancelled)
			}
			mark("first-seen", "")
			// A slow cleanup that never looks at signals again: only the
			// default action of a second signal can end the process now.
			time.Sleep(20 * time.Second)
			return exitWith(s058CodeCancelled)
		}
	default:
		t.Fatalf("unknown child role %q", role)
	}

	root.SetArgs(args)
	exitProcess(runMain(root))
	os.Exit(s058ChildReturned)
}

const s058CancellableAnnotation = "bentoo/cancellable"

// s058BlockingValue is a flag value whose parsing blocks, so a signal can be
// delivered while cobra is still parsing, before any hook has run.
type s058BlockingValue struct{ mark func() }

func (v *s058BlockingValue) String() string { return "" }
func (v *s058BlockingValue) Type() string   { return "string" }
func (v *s058BlockingValue) Set(string) error {
	v.mark()
	time.Sleep(20 * time.Second)
	return nil
}

// s058SkipIfIgnored skips a test that needs sig's default action when this
// process ignores sig: a child inherits the ignored disposition.
func s058SkipIfIgnored(t *testing.T, sig syscall.Signal) {
	t.Helper()
	if signal.Ignored(sig) {
		t.Skipf("%v is ignored in this process and so in its children; its default action cannot be observed here", sig)
	}
}

// s058SyncBuffer is a bytes.Buffer safe to read while the child writes to it.
type s058SyncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *s058SyncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *s058SyncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// s058Child is a re-exec'd copy of the test binary running one helper test.
type s058Child struct {
	cmd  *exec.Cmd
	dir  string
	out  *s058SyncBuffer
	done chan struct{}
}

// s058StartChild starts the helper test named test with roleEnv=role. The child
// gets its own temporary HOME and XDG homes unless extraEnv overrides them
// (later entries win).
func s058StartChild(t *testing.T, test, roleEnv, role string, extraEnv ...string) *s058Child {
	t.Helper()
	dir := t.TempDir()
	home := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^"+test+"$", "-test.count=1")
	cmd.Env = append(os.Environ(),
		roleEnv+"="+role,
		s058ChildDirEnv+"="+dir,
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"XDG_STATE_HOME="+filepath.Join(home, ".local", "state"),
		"XDG_CACHE_HOME="+filepath.Join(home, ".cache"),
		"XDG_DATA_HOME="+filepath.Join(home, ".local", "share"),
	)
	cmd.Env = append(cmd.Env, extraEnv...)
	out := &s058SyncBuffer{}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the child: %v", err)
	}
	ch := &s058Child{cmd: cmd, dir: dir, out: out, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(ch.done)
	}()
	t.Cleanup(func() {
		select {
		case <-ch.done:
		default:
			_ = cmd.Process.Kill()
			<-ch.done
		}
	})
	return ch
}

// awaitFile waits until the child created name, the child ended, or timeout.
func (ch *s058Child) awaitFile(t *testing.T, name string, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		if _, err := os.Stat(filepath.Join(ch.dir, name)); err == nil {
			return
		}
		select {
		case <-ch.done:
			if _, err := os.Stat(filepath.Join(ch.dir, name)); err == nil {
				return
			}
			t.Fatalf("the child ended before it wrote %q:\n%s", name, ch.out.String())
		case <-deadline:
			t.Fatalf("the child did not write %q within %v:\n%s", name, timeout, ch.out.String())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// awaitExit waits for the child to end, failing the test after timeout.
func (ch *s058Child) awaitExit(t *testing.T, timeout time.Duration, why string) {
	t.Helper()
	select {
	case <-ch.done:
	case <-time.After(timeout):
		t.Fatalf("the child was still running %v later: %s\n%s", timeout, why, ch.out.String())
	}
}

// status reports how the ended child ended.
func (ch *s058Child) status() (code int, sig syscall.Signal, signaled bool) {
	if ws, ok := ch.cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return -1, ws.Signal(), true
	}
	return ch.cmd.ProcessState.ExitCode(), 0, false
}

// TestS058DeferredCallsRunOnAFailureExit (R1.2): whatever the handler returns,
// its deferred call runs before the process ends through exitProcess, and the
// process exits with the mapped code.
func TestS058DeferredCallsRunOnAFailureExit(t *testing.T) {
	cases := []struct {
		role   string
		want   int
		once   string
		absent string
	}{
		{role: "defer-silent", want: 3, absent: "exit status 3"},
		{role: "defer-plain", want: 1, once: "s058 plain failure"},
		{role: "defer-success", want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.role, func(t *testing.T) {
			ch := s058StartChild(t, "TestS058HelperSignalChild", s058SignalRoleEnv, tc.role)
			ch.awaitExit(t, 30*time.Second, "a handler that returns at once must end the process at once")
			code, sig, signaled := ch.status()
			if signaled {
				t.Fatalf("the child was killed by %v:\n%s", sig, ch.out.String())
			}
			if code == s058ChildReturned {
				t.Fatalf("exitProcess(runMain(root)) returned instead of ending the process:\n%s", ch.out.String())
			}
			if code != tc.want {
				t.Errorf("the child exited %d, want %d:\n%s", code, tc.want, ch.out.String())
			}
			if _, err := os.Stat(filepath.Join(ch.dir, "deferred")); err != nil {
				t.Errorf("the handler's deferred call did not run before the process ended (exit %d): %v", code, err)
			}
			out := ch.out.String()
			if tc.once != "" && strings.Count(out, tc.once) != 1 {
				t.Errorf("%q printed %d time(s), want once:\n%s", tc.once, strings.Count(out, tc.once), out)
			}
			if tc.absent != "" && strings.Contains(out, tc.absent) {
				t.Errorf("a silent status was printed as %q:\n%s", tc.absent, out)
			}
		})
	}
}

// TestS058FirstSignalCancelsTheCommandContext (R4.1, R4.3): the first SIGINT,
// SIGTERM or SIGHUP cancels cmd.Context() of a cancellable sub-command, and the process
// exits with the code the handler returned on its cancelled path.
func TestS058FirstSignalCancelsTheCommandContext(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			ch := s058StartChild(t, "TestS058HelperSignalChild", s058SignalRoleEnv, "first-signal")
			ch.awaitFile(t, "ready", 15*time.Second)
			if err := ch.cmd.Process.Signal(sig); err != nil {
				t.Fatalf("signalling the child: %v", err)
			}
			ch.awaitExit(t, 15*time.Second, "the first signal must cancel the command's context")
			code, got, signaled := ch.status()
			if signaled {
				t.Fatalf("the first %v killed the process by its default action (%v): cmd.Context() is not signal-aware:\n%s", sig, got, ch.out.String())
			}
			if code != s058CodeCancelled {
				t.Errorf("the child exited %d, want %d, the code its handler returned on the cancelled path:\n%s", code, s058CodeCancelled, ch.out.String())
			}
			body, err := os.ReadFile(filepath.Join(ch.dir, "cancelled"))
			if err != nil || !strings.Contains(string(body), context.Canceled.Error()) {
				t.Errorf("the handler did not observe a cancelled context (marker %q, %v)", body, err)
			}
		})
	}
}

// s058ExpectKilledByFirstSignal starts role, sends one sig once the child is
// ready, and requires the child to be terminated by that signal (R4.6).
func s058ExpectKilledByFirstSignal(t *testing.T, role string, sig syscall.Signal, why string) {
	t.Helper()
	s058SkipIfIgnored(t, sig)
	ch := s058StartChild(t, "TestS058HelperSignalChild", s058SignalRoleEnv, role)
	ch.awaitFile(t, "ready", 15*time.Second)
	if err := ch.cmd.Process.Signal(sig); err != nil {
		t.Fatalf("signalling the child: %v", err)
	}
	ch.awaitExit(t, 15*time.Second, "the first signal must take its default action "+why)
	code, got, signaled := ch.status()
	if !signaled {
		t.Fatalf("the first %v did not terminate the process %s; it exited %d instead (%d means its context was cancelled, as if it were cancellable):\n%s",
			sig, why, code, s058CodeCancelled, ch.out.String())
	}
	if got != sig {
		t.Errorf("the process was terminated by %v, want %v", got, sig)
	}
	if _, err := os.Stat(filepath.Join(ch.dir, "cancelled")); err == nil {
		t.Errorf("the handler observed a cancelled context before the process died: the signal was handled, not left to its default action")
	}
}

// TestS058NonCancellableCommandDiesOfTheFirstSignal (R4.6): a command without
// the cancellable annotation is terminated by the first SIGINT, SIGTERM or
// SIGHUP, exactly as at 6be73ec, where it registered no signal handler.
func TestS058NonCancellableCommandDiesOfTheFirstSignal(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			s058ExpectKilledByFirstSignal(t, "non-cancellable", sig, "while a command that is not cancellable runs")
		})
	}
}

// TestS058SignalBeforeCommandSelectionTerminates (R4.6): a signal that lands
// while cobra still parses the command line is terminated by its default
// action even when the command about to run is cancellable — the policy starts
// not cancellable.
func TestS058SignalBeforeCommandSelectionTerminates(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			s058ExpectKilledByFirstSignal(t, "before-selection", sig, "before cobra has selected the command")
		})
	}
}

// TestS058HandlerFailurePrintsNoUsage (R2.4, design.md Error Handling
// Strategy): on the real root, a handler that fails prints its diagnostic and
// no usage block — as at 6be73ec, where the handler ended the process first —
// while a rejection by cobra's own validation still prints usage.
func TestS058HandlerFailurePrintsNoUsage(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		wantUsage bool
		want      string
	}{
		// Would wrongly print usage: the handler ran and failed.
		{"a handler failure", []string{"s058-fail"}, false, "s058 handler failure"},
		{"a silent handler failure", []string{"s058-silent"}, false, ""},
		// Would wrongly lose usage: cobra rejected the invocation itself.
		{"an unknown flag", []string{"s058-fail", "--not-a-flag"}, true, "unknown flag: --not-a-flag"},
		{"a wrong argument count", []string{"s058-fail", "extra"}, true, "unknown command"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s058Env(t)
			root := newRootCmd()
			var cobraOut bytes.Buffer
			root.SetOut(&cobraOut)
			root.SetErr(&cobraOut)
			root.AddCommand(
				&cobra.Command{Use: "s058-fail", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error { return errors.New("s058 handler failure") }},
				&cobra.Command{Use: "s058-silent", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error { return exitWith(3) }},
			)
			root.SetArgs(tc.args)
			var diag bytes.Buffer
			code := execute(context.Background(), root, &diag)
			all := cobraOut.String() + diag.String()
			if code == 0 {
				t.Errorf("exit 0, want a failure")
			}
			if got := strings.Contains(all, "Usage:"); got != tc.wantUsage {
				t.Errorf("usage block printed: %t, want %t\n--- cobra ---\n%s\n--- stderr ---\n%s", got, tc.wantUsage, cobraOut.String(), diag.String())
			}
			if tc.want != "" && strings.Count(all, tc.want) != 1 {
				t.Errorf("%q printed %d time(s), want once:\n%s", tc.want, strings.Count(all, tc.want), all)
			}
		})
	}
}

// TestS058SecondSignalTerminatesByDefaultAction (R4.2): once the first signal
// has been handled, the next one ends the process through its default action.
// The second signal is repeated until the child dies, so the test does not
// depend on how quickly the handler is released after the first.
func TestS058SecondSignalTerminatesByDefaultAction(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			s058SkipIfIgnored(t, sig)
			ch := s058StartChild(t, "TestS058HelperSignalChild", s058SignalRoleEnv, "second-signal")
			ch.awaitFile(t, "ready", 15*time.Second)
			if err := ch.cmd.Process.Signal(sig); err != nil {
				t.Fatalf("sending the first signal: %v", err)
			}
			ch.awaitFile(t, "first-seen", 15*time.Second)

			tick := time.NewTicker(100 * time.Millisecond)
			defer tick.Stop()
			deadline := time.After(10 * time.Second)
		wait:
			for {
				select {
				case <-ch.done:
					break wait
				case <-tick.C:
					_ = ch.cmd.Process.Signal(sig)
				case <-deadline:
					t.Fatalf("the process outlived repeated %v for 10 s after the first one was handled: the handler was never released, so a second Ctrl+C cannot get the operator out", sig)
				}
			}
			code, got, signaled := ch.status()
			if !signaled {
				t.Fatalf("the process exited %d instead of being terminated by the second %v:\n%s", code, sig, ch.out.String())
			}
			if got != sig {
				t.Errorf("the process was terminated by %v, want %v", got, sig)
			}
		})
	}
}

// TestS058CommandContextOfAnUnexecutedCommandIsUsable: a handler called
// directly on a command that was never executed still gets a usable context,
// and an executed command gets the context it was given.
func TestS058CommandContextOfAnUnexecutedCommandIsUsable(t *testing.T) {
	cmd := &cobra.Command{Use: "work"}
	ctx := commandContext(cmd)
	if ctx == nil {
		t.Fatal("commandContext returned nil for a command that was never executed")
	}
	if err := ctx.Err(); err != nil {
		t.Errorf("the fallback context is already done: %v", err)
	}
	given := context.WithValue(context.Background(), s058CtxKey{}, "caller")
	cmd.SetContext(given)
	if got := commandContext(cmd).Value(s058CtxKey{}); got != "caller" {
		t.Errorf("commandContext ignored the command's own context: got %v", got)
	}
}
