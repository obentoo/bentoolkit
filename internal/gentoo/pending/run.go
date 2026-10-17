package pending

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/obentoo/bentoolkit/internal/common/procgroup"
)

// Output caps: what a command writes past them is read and discarded, so the
// child never blocks on a full pipe and a runaway command cannot grow the
// tray's memory without bound.
const (
	stdoutCap = 8 << 20
	stderrCap = 64 << 10
)

// waitDelay bounds how long Run keeps draining the output pipes once the
// command has exited or been killed, in case a process that escaped the group
// (one that called setsid) still holds them open.
const waitDelay = time.Second

// Exit statuses that say the command never ran. A wrapper such as nice or
// ionice exits 126 when it found the command but could not execute it, and 127
// when it could not find it; python-exec, which emerge and glsa-check run
// through, exits 127 when the implementation for the selected Python is
// missing. GNU nice exits 125 when nice itself failed.
const (
	wrapperFailed     = 125
	commandCannotExec = 126
	commandNotFound   = 127
)

// ErrTimeout reports that a command ran past its Command.Timeout and its
// process group was killed.
var ErrTimeout = errors.New("command timed out")

// Command is one Portage command to run.
type Command struct {
	// Path is the command, normally an absolute path from exec.LookPath. A
	// bare name is looked up in PATH when Wrap is empty.
	Path string
	// Args are passed to Path as they are, one argument each; no shell
	// ever sees them.
	Args []string
	// Timeout bounds the run; past it the whole process group is killed and
	// Run returns ErrTimeout. A non-positive Timeout sets no deadline of its
	// own, leaving only the caller's ctx.
	Timeout time.Duration
	// Wrap is the priority prefix the command runs behind, for instance
	// ["nice", "-n", "19", "ionice", "-c", "3"]. Empty runs Path directly.
	Wrap []string
}

// Result is what a command that ran to its own exit produced.
type Result struct {
	// Stdout is the start of the standard output, at most 8 MiB.
	Stdout []byte
	// Stderr is the start of the standard error, at most 64 KiB.
	Stderr []byte
	// ExitCode is the exit status; -1 when a signal ended the command.
	ExitCode int
	// Duration is the time from start to the end of the wait.
	Duration time.Duration
}

// Run runs c and waits for it. The command leads a new process group, has
// /dev/null as standard input, and sees the caller's environment plus
// NOCOLOR=true and TERM=dumb.
//
// A command that ran and exited, with any status, is a Result and a nil
// error: a non-zero exit is an answer, not a failure to ask. The error is
// non-nil when the command could not be started (exec.ErrNotFound and
// fs.ErrPermission stay reachable through errors.Is), when it exited 126 or
// 127, or 125 behind a Wrap, which say it never ran (exec.ErrNotFound), when
// the run passed c.Timeout (ErrTimeout), or when ctx was done first
// (ctx.Err()). In the last three cases the Result still carries the output
// captured and the duration. Whatever is left of the process group once the
// command has exited is killed before Run returns.
func Run(ctx context.Context, c Command) (Result, error) {
	runCtx, cancel := withTimeout(ctx, c.Timeout)
	defer cancel()

	name, args := argv(c)
	cmd := exec.CommandContext(runCtx, name, args...) //nolint:gosec // G204: the caller's fixed command and wrappers, an argument list, never a shell
	cmd.Stdin = nil
	cmd.Env = append(os.Environ(), "NOCOLOR=true", "TERM=dumb")
	stdout := &cappedBuffer{limit: stdoutCap}
	stderr := &cappedBuffer{limit: stderrCap}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	// The default cancel would kill only the direct child, the wrapper,
	// leaving the command orphaned and holding the output pipe.
	procgroup.KillGroupNow(cmd)
	cmd.WaitDelay = waitDelay

	start := time.Now()
	if err := cmd.Start(); err != nil {
		if ctxErr := doneErr(ctx, runCtx, c.Path); ctxErr != nil {
			return Result{}, ctxErr
		}
		return Result{}, fmt.Errorf("starting %s: %w", describe(c), err)
	}
	waitErr := procgroup.Result(cmd, cmd.Wait())
	sweepGroup(cmd.Process.Pid)
	res := Result{
		Stdout:   stdout.buf,
		Stderr:   stderr.buf,
		ExitCode: cmd.ProcessState.ExitCode(),
		Duration: time.Since(start),
	}
	return res, outcome(ctx, runCtx, c, waitErr, res.ExitCode)
}

// withTimeout derives the run's ctx: ctx with c.Timeout when it is positive.
func withTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout > 0 {
		return context.WithTimeout(ctx, timeout)
	}
	return context.WithCancel(ctx)
}

// argv returns the program to start and its arguments: the wrappers first,
// then the command and its own arguments.
func argv(c Command) (string, []string) {
	if len(c.Wrap) == 0 {
		return c.Path, c.Args
	}
	args := make([]string, 0, len(c.Wrap)+len(c.Args))
	args = append(args, c.Wrap[1:]...)
	args = append(args, c.Path)
	args = append(args, c.Args...)
	return c.Wrap[0], args
}

// describe names c for an error: the command, and its first wrapper when
// there is one.
func describe(c Command) string {
	if len(c.Wrap) == 0 {
		return c.Path
	}
	return fmt.Sprintf("%s through %s", c.Path, c.Wrap[0])
}

// doneErr reports why the run's ctx is done, or nil when it is not. A done
// caller ctx (a quit) is told apart from the run's own deadline (ErrTimeout).
func doneErr(ctx, runCtx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("running %s: %w", path, err)
	}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w", path, ErrTimeout)
	}
	return nil
}

// outcome turns the wait's error into Run's: a timeout or cancel first, then
// an exit status saying the command never ran, then any exit status is an
// answer, and only a failure of the wait itself is an error.
func outcome(ctx, runCtx context.Context, c Command, waitErr error, code int) error {
	if waitErr != nil {
		if err := doneErr(ctx, runCtx, c.Path); err != nil {
			return err
		}
	}
	switch {
	case code == commandCannotExec || code == commandNotFound:
		return &notRunError{what: describe(c), reason: "command could not be executed", code: code}
	case code == wrapperFailed && len(c.Wrap) > 0:
		return &notRunError{what: describe(c), reason: c.Wrap[0] + " failed", code: code}
	}
	var exitErr *exec.ExitError
	if waitErr == nil || errors.As(waitErr, &exitErr) {
		return nil
	}
	return fmt.Errorf("running %s: %w", describe(c), waitErr)
}

// notRunError is an exit status that says the command never ran. It matches
// exec.ErrNotFound through errors.Is, so the caller disables the monitor, but
// does not borrow that error's text: the path was found, so "not found in
// $PATH" would point at the wrong cause.
type notRunError struct {
	what   string // the command, and its first wrapper when there is one
	reason string
	code   int
}

func (e *notRunError) Error() string {
	return fmt.Sprintf("%s: %s (exit %d)", e.what, e.reason, e.code)
}

func (e *notRunError) Unwrap() error { return exec.ErrNotFound }

// cappedBuffer keeps the first limit bytes written to it and discards the
// rest while still reporting every write as complete, so the writer goes on
// draining the pipe instead of blocking or failing the child.
type cappedBuffer struct {
	buf   []byte
	limit int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - len(b.buf); room > 0 {
		b.buf = append(b.buf, p[:min(room, len(p))]...)
	}
	return len(p), nil
}
